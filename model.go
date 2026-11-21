package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	frameInterval   = 50 * time.Millisecond
	refreshEvery    = 5 // frames between status refreshes (250ms)
	maxSidebarWidth = 32
)

type focusArea int

const (
	focusSidebar focusArea = iota
	focusTerminal
	focusDialog
	focusConfig
)

type frameMsg time.Time

func frameTick() tea.Cmd {
	return tea.Tick(frameInterval, func(t time.Time) tea.Msg { return frameMsg(t) })
}

type model struct {
	sessions []*Session
	sel      int
	scroll   int
	focus    focusArea
	width    int
	height   int
	frame    int
	dirty    *atomic.Bool
	agents   []Agent
	dialog   *newDialog
	confirm  *confirmPrompt
	closing  *closePrompt // x's warning for a worktree session that isn't merged
	rename   *textinput.Model
	flash    string

	// Filtering the list ("/" and "@").
	filter        string
	attentionOnly bool
	filterInput   *textinput.Model // open while typing a filter

	// A press on a session that may become a drag (drag.go).
	drag *dragState

	help *helpOverlay // the key help (? or F1), when open
	diff *diffView    // the diff preview (d), when open

	// Whether deckhand's window has focus, when the terminal reports it.
	focusKnown    bool
	windowFocused bool

	flashAt  time.Time
	quitting bool

	// Whether "o" has said how to set an editor (it does once, the first
	// time it falls back to Explorer).
	editorHinted bool

	// Whether the session list is hidden (the toggle key, ctrl+\ by default).
	listHidden bool

	// Keys pressed and not yet released, to spot a release whose press
	// never arrived (see releaseOnlyKey).
	pressed map[string]bool

	// The config screen ("c"), and whether there's a config file (the
	// welcome screen suggests the config screen when there isn't).
	cfg        *configScreen
	haveConfig bool

	// The PowerShell profile's functions and aliases, once loaded.
	profile        *profileCommandsMsg
	profileLoading bool

	// An agent being tested from the config screen (see agenttest.go).
	test *agentTest
}

type confirmPrompt struct {
	text   string
	action func(m *model) tea.Cmd
	cancel func(m *model) // when it's answered with anything but y; may be nil
}

func newModel() *model {
	m := &model{
		sessions: loadSessions(),
		dirty:    &atomic.Bool{},
		agents:   availableAgents(),
	}
	m.listHidden = loadPrefs().ListHidden
	if _, err := os.Stat(configPath()); err == nil {
		m.haveConfig = true
	}
	return m
}

func (m *model) Init() tea.Cmd { return frameTick() }

// Layout -------------------------------------------------------------------

func (m *model) sidebarWidth() int {
	w := m.width / 4
	if w < 24 {
		w = 24
	}
	if w > maxSidebarWidth {
		w = maxSidebarWidth
	}
	return w
}

// termSize is the size of the embedded terminal: right of the sidebar (or
// the full width while the list is hidden), below the session header, above
// the footer.
func (m *model) termSize() (int, int) {
	w := m.width - m.mainLeft()
	h := m.height - 2
	if w < 10 {
		w = 10
	}
	if h < 3 {
		h = 3
	}
	return w, h
}

// current is the selected session, or nil when there's none or the filter
// hides it.
func (m *model) current() *Session {
	if m.sel < 0 || m.sel >= len(m.sessions) {
		return nil
	}
	if m.filtering() && visiblePos(m.visible(), m.sel) < 0 {
		return nil
	}
	return m.sessions[m.sel]
}

func (m *model) setFlash(s string) {
	m.flash = s
	m.flashAt = time.Now()
}

// Update ---------------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeAll()
		return m, nil

	case worktreeCreatedMsg:
		return m, m.worktreeCreated(msg)

	case worktreeSetupMsg:
		return m, m.worktreeSetupDone(msg)

	case closeCheckedMsg:
		return m, m.closeChecked(msg)

	case closeDoneMsg:
		m.closeDone(msg)
		return m, nil

	case prPlannedMsg:
		return m, m.prPlanned(msg)

	case prDoneMsg:
		m.prDone(msg)
		return m, nil

	case mergePlannedMsg:
		return m, m.mergePlanned(msg)

	case mergeDoneMsg:
		m.mergeDone(msg)
		return m, nil

	case profileCommandsMsg:
		m.profileLoading = false
		m.profile = &msg
		return m, nil

	case diffLoadedMsg:
		if m.diff == msg.view { // not closed (or reopened) meanwhile
			m.diff.setResult(msg.res)
		}
		return m, nil

	case configStatusMsg:
		if m.cfg != nil {
			m.cfg.setStatus(msg.text, msg.isErr)
		} else {
			m.setFlash(msg.text)
		}
		return m, nil

	case tea.FocusMsg:
		m.focusKnown, m.windowFocused = true, true
		return m, nil

	case tea.BlurMsg:
		m.focusKnown, m.windowFocused = true, false
		return m, nil

	case frameMsg:
		m.frame++
		if m.frame%refreshEvery == 0 {
			now := time.Now()
			for i, s := range m.sessions {
				s.Refresh(now)
				if n := s.TakeNotice(); n != nil && !s.testing.Load() && shouldNotify(notifyMode, m.focusKnown, m.windowFocused, i == m.sel) {
					go sendNotice(s, *n)
				}
				s.MaybePoll(now, i == m.sel)
				s.MaybePollPR(now)
				s.MaybeSaveScreen(now)
			}
			m.tickTest(now)
		}
		m.dragTick()
		if s := m.current(); s != nil {
			s.MarkSeen()
		}
		m.dirty.Store(false)
		return m, frameTick()

	case tea.PasteMsg:
		if m.help != nil {
			return m, nil
		}
		if m.filterInput != nil {
			var cmd tea.Cmd
			*m.filterInput, cmd = m.filterInput.Update(msg)
			m.applyFilterInput()
			return m, cmd
		}
		if m.rename != nil {
			var cmd tea.Cmd
			*m.rename, cmd = m.rename.Update(msg)
			return m, cmd
		}
		switch m.focus {
		case focusTerminal:
			if s := m.current(); s != nil {
				s.ScrollToBottom()
				s.Paste(msg.Content)
			}
		case focusDialog:
			return m, m.dialog.update(msg)
		case focusConfig:
			return m, m.cfg.updateInputs(msg)
		}
		return m, nil

	case tea.KeyPressMsg:
		debugf("key press %q code=%d mod=%d", msg.String(), msg.Code, msg.Mod)
		if lockOrModifierKey(msg.Code) {
			return m, nil
		}
		if m.pressed == nil {
			m.pressed = map[string]bool{}
		}
		m.pressed[pressID(tea.Key(msg))] = true
		return m.handleKey(msg)

	case tea.KeyReleaseMsg:
		debugf("key release %q code=%d mod=%d", msg.String(), msg.Code, msg.Mod)
		if lockOrModifierKey(msg.Code) {
			return m, nil
		}
		id := pressID(tea.Key(msg))
		seen := m.pressed[id]
		delete(m.pressed, id)
		if !seen && releaseOnlyKey(tea.Key(msg)) {
			// Some terminals' win32-input-mode encoding of a ctrl+symbol
			// press (WezTerm's ctrl+\) never reaches us; only the release
			// does. Treat the lone release as the press.
			return m.handleKey(tea.KeyPressMsg(msg))
		}
		return m, nil

	case tea.MouseMsg:
		return m, m.handleMouse(msg)
	}
	if m.focus == focusDialog && m.dialog != nil {
		return m, m.dialog.update(msg)
	}
	if m.focus == focusConfig && m.cfg != nil {
		return m, m.cfg.updateInputs(msg)
	}
	return m, nil
}

// resizeAll fits every session (and the new-session dialog and the config
// screen) to termSize.
func (m *model) resizeAll() {
	w, h := m.termSize()
	for _, s := range m.sessions {
		s.Resize(w, h)
	}
	if m.dialog != nil {
		m.dialog.setWidth(w)
	}
	if m.cfg != nil {
		m.cfg.setWidth(w)
	}
}

func (m *model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()

	if m.help != nil {
		m.helpKey(key)
		return m, nil
	}

	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if key == "y" || key == "Y" {
			return m, c.action(m)
		}
		if c.cancel != nil {
			c.cancel(m)
		}
		return m, nil
	}

	if m.closing != nil {
		return m, m.closeKey(key)
	}

	// Any key ends a drag; esc only cancels it.
	if m.drag != nil {
		m.cancelDrag()
		if key == "esc" {
			return m, nil
		}
	}

	// F1 opens the help from anywhere, so it never reaches the agent.
	if key == "f1" {
		m.openHelp()
		return m, nil
	}

	if m.filterInput != nil {
		switch key {
		case "esc":
			m.filter = ""
			m.filterInput = nil
			m.keepSelectionVisible()
		case "enter", "down", "up", "tab":
			// Keep the filter and go back to the list.
			m.filterInput = nil
			if key == "enter" && len(m.visible()) == 1 {
				return m, m.openSelected()
			}
			if key == "down" || key == "up" {
				m.moveSel(map[string]int{"down": 1, "up": -1}[key])
			}
		default:
			var cmd tea.Cmd
			*m.filterInput, cmd = m.filterInput.Update(k)
			m.applyFilterInput()
			return m, cmd
		}
		return m, nil
	}

	if m.rename != nil {
		switch key {
		case "esc":
			m.rename = nil
		case "enter":
			if s := m.current(); s != nil {
				if name := strings.TrimSpace(m.rename.Value()); name != "" {
					s.mu.Lock()
					s.Name = name
					s.mu.Unlock()
					_ = saveSessions(m.sessions)
				}
			}
			m.rename = nil
		default:
			var cmd tea.Cmd
			*m.rename, cmd = m.rename.Update(k)
			return m, cmd
		}
		return m, nil
	}

	// The toggle key hides or shows the list from anywhere; it doesn't
	// reach the agent.
	if key == listOpts.toggleKey {
		m.setListHidden(!m.listHidden)
		return m, nil
	}

	// The diff preview is read-only; it takes every other key.
	if m.diff != nil {
		m.diffKey(key)
		return m, nil
	}

	switch m.focus {
	case focusTerminal:
		if key == listOpts.back() || key == "ctrl+q" || key == "ctrl+]" {
			// Back to the list (F5, or the fallbacks), showing it again if
			// it's hidden.
			m.setListHidden(false)
			m.focus = focusSidebar
			return m, nil
		}
		if s := m.current(); s != nil {
			if !s.Running() {
				m.focus = focusSidebar
				return m, nil
			}
			// shift+PgUp/PgDn scroll deckhand's scrollback (when the app
			// isn't on the alternate screen, which has none).
			_, th := m.termSize()
			switch key {
			case "shift+pgup":
				if s.Scroll(th - 2) {
					return m, nil
				}
			case "shift+pgdown":
				if s.Scroll(-(th - 2)) {
					return m, nil
				}
			}
			// Typing returns to the live view.
			s.ScrollToBottom()
			debugf("key %q %+v", key, k)
			s.SendKey(toUVKey(k))
		}
		return m, nil

	case focusDialog:
		switch key {
		case "esc":
			m.dialog = nil
			m.focus = focusSidebar
			return m, nil
		case "enter":
			return m, m.createFromDialog()
		}
		return m, m.dialog.update(k)

	case focusConfig:
		return m, m.configKey(k)
	}

	// Sidebar.
	switch key {
	case listOpts.back(), "ctrl+q", "ctrl+]":
		m.setListHidden(false)
	case "up", "k":
		m.moveSel(-1)
	case "down", "j":
		m.moveSel(1)
	case "home", "g":
		m.selectVisible(0)
	case "end", "G":
		m.selectVisible(-1)
	case "shift+up", "shift+down", "shift+home", "shift+end":
		// Move the selected session; the order is saved right away.
		moved := false
		switch key {
		case "shift+up":
			moved = m.moveSession(-1)
		case "shift+down":
			moved = m.moveSession(1)
		default:
			moved = m.moveSessionToEnd(key == "shift+home")
		}
		if moved {
			_ = saveSessions(m.sessions)
		}
	case "/":
		m.setListHidden(false) // the filter is typed in the list's header
		ti := textinput.New()
		ti.Prompt = "/"
		ti.Placeholder = "filter"
		ti.SetValue(m.filter)
		ti.CursorEnd()
		ti.SetWidth(m.sidebarWidth() - 3)
		m.filterInput = &ti
		return m, m.filterInput.Focus()
	case "?":
		m.openHelp()
	case "@":
		m.attentionOnly = !m.attentionOnly
		m.keepSelectionVisible()
	case "esc":
		if m.filtering() {
			m.clearFilter()
			m.keepSelectionVisible()
		}
	case "enter", "right", "l":
		return m, m.openSelected()
	case "shift+enter", "alt+enter":
		// alt+enter is ESC CR: terminals with shift+enter remapped to send
		// it (a common setup for Claude Code) deliver shift+enter this way.
		return m, m.startLikeSelected()
	case "W":
		return m, m.worktreeLikeSelected()
	case "tab":
		m.jumpToAttention()
	case "v":
		// An exited Claude session: its last output or the conversation.
		if s := m.current(); s != nil {
			s.ToggleHistory()
		}
	case "shift+pgup", "shift+pgdown":
		// Scroll an exited session's saved output from the list.
		if s := m.current(); s != nil && !s.Running() {
			_, th := m.termSize()
			if key == "shift+pgup" {
				s.Scroll(th - 2)
			} else {
				s.Scroll(-(th - 2))
			}
		}
	case "n":
		if len(m.agents) == 0 {
			m.setFlash("No supported CLIs found on PATH")
			return m, nil
		}
		w, _ := m.termSize()
		m.dialog = newNewDialog(m.agents, w, m.sessions)
		m.focus = focusDialog
		return m, m.dialog.focusCmd()
	case "c":
		return m, m.openConfigScreen()
	case "d":
		return m, m.openDiff()
	case "p":
		return m, m.startPR()
	case "m":
		return m, m.startMerge()
	case "r":
		if s := m.current(); s != nil {
			ti := textinput.New()
			ti.Prompt = " Rename: "
			ti.SetValue(s.Name)
			ti.CursorEnd()
			ti.SetWidth(40)
			m.rename = &ti
			return m, m.rename.Focus()
		}
	case "x", "delete":
		if s := m.current(); s != nil && s.hasWorktree() {
			m.closeWorktreeSession(s)
		} else if s != nil && s.preparing.Load() {
			m.setFlash("Still creating its worktree; wait for it to finish")
		} else if s != nil {
			name := s.Name
			text := fmt.Sprintf("Remove %q from the list? y/n", name)
			if s.Running() {
				text = fmt.Sprintf("End %q and remove it? y/n", name)
			}
			m.confirm = &confirmPrompt{text: text, action: func(m *model) tea.Cmd {
				m.removeSession(s) // by pointer: the list can change while it asks
				return nil
			}}
		}
	case "o":
		m.openFolder(openEditor)
	case "O":
		m.openFolder(openExplorer)
	case "t":
		m.openFolder(openTerminal)
	case "K":
		if s := m.current(); s != nil && s.Running() {
			m.confirm = &confirmPrompt{text: fmt.Sprintf("End %q (it stays in the list)? y/n", s.Name), action: func(m *model) tea.Cmd {
				s.Kill()
				return nil
			}}
		}
	case "q", "ctrl+c":
		running := 0
		for _, s := range m.sessions {
			if s.Running() {
				running++
			}
		}
		if running == 0 {
			return m, m.quit()
		}
		m.confirm = &confirmPrompt{
			text:   fmt.Sprintf("Quit and end %d running session(s)? Claude sessions can be resumed later. y/n", running),
			action: func(m *model) tea.Cmd { return m.quit() },
		}
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			if m.selectVisible(int(key[0] - '1')) {
				return m, m.openSelected()
			}
		}
	}
	return m, nil
}

// Mouse ----------------------------------------------------------------------

const wheelLines = 3

// handleMouse routes the wheel and clicks.
//
//   - Sidebar: the wheel moves the selection; a click selects a session.
//   - Terminal, when the app has turned on mouse reporting (e.g. a
//     fullscreen TUI): events are forwarded so the app scrolls itself.
//   - Terminal otherwise: the wheel scrolls deckhand's scrollback; on the
//     alternate screen it becomes arrow keys if the app asked for that.
func (m *model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	ms := msg.Mouse()
	sw := m.sidebarWidth()
	if m.listHidden {
		sw = 0 // no sidebar: everything goes to the terminal
	}

	if m.help != nil {
		m.helpMouse(msg)
		return nil
	}
	if m.diff != nil {
		m.diffMouse(msg)
		return nil
	}
	if m.focus == focusDialog || m.focus == focusConfig || m.rename != nil || m.confirm != nil || m.closing != nil || m.filterInput != nil {
		m.cancelDrag()
		return nil
	}

	// A press in the list, maybe dragging: the motion and the release are
	// the list's wherever they happen, and never reach the agent.
	if m.drag != nil {
		m.dragMouse(msg)
		return nil
	}

	// Sidebar.
	if ms.X < sw {
		switch msg.(type) {
		case tea.MouseWheelMsg:
			switch ms.Button {
			case tea.MouseWheelUp:
				m.moveSel(-1)
			case tea.MouseWheelDown:
				m.moveSel(1)
			}
		case tea.MouseClickMsg:
			if ms.Button == tea.MouseLeft && ms.Y >= 2 {
				p := m.scroll + (ms.Y-listTop)/rowsPerSession
				if m.selectVisible(p) {
					m.focus = focusSidebar
					m.startDrag(p, ms.X, ms.Y) // a drag if it moves before the release
				}
			}
		}
		return nil
	}

	s := m.current()
	if s == nil {
		return nil
	}
	tx, ty := ms.X-m.mainLeft(), ms.Y-1 // terminal coordinates
	inTerm := tx >= 0 && ty >= 0

	if s.MouseReporting() && inTerm {
		ev := uv.Mouse{X: tx, Y: ty, Button: ms.Button, Mod: ms.Mod}
		switch msg.(type) {
		case tea.MouseWheelMsg:
			s.SendMouse(uv.MouseWheelEvent(ev))
		case tea.MouseClickMsg:
			m.focus = focusTerminal
			s.SendMouse(uv.MouseClickEvent(ev))
		case tea.MouseReleaseMsg:
			s.SendMouse(uv.MouseReleaseEvent(ev))
		case tea.MouseMotionMsg:
			s.SendMouse(uv.MouseMotionEvent(ev))
		}
		return nil
	}

	switch msg.(type) {
	case tea.MouseWheelMsg:
		delta := 0
		switch ms.Button {
		case tea.MouseWheelUp:
			delta = wheelLines
		case tea.MouseWheelDown:
			delta = -wheelLines
		}
		if delta == 0 {
			return nil
		}
		if s.Scroll(delta) {
			return nil
		}
		if s.AltScrollWanted() {
			code := tea.KeyUp
			if delta < 0 {
				code = tea.KeyDown
			}
			for i := 0; i < wheelLines; i++ {
				s.SendKey(uv.KeyPressEvent{Code: code})
			}
		}
	case tea.MouseClickMsg:
		if ms.Button == tea.MouseLeft && s.Running() {
			m.focus = focusTerminal
		}
	}
	return nil
}

func (m *model) quit() tea.Cmd {
	_ = saveSessions(m.sessions)
	// Save each running session's output while its emulator still has it.
	saveAll(m.sessions)
	quittingAll.Store(true)
	for _, s := range m.sessions {
		s.Kill()
	}
	m.quitting = true
	return tea.Quit
}

// openSelected focuses the terminal of the selected session, starting (or
// resuming) it first if it isn't running.
func (m *model) openSelected() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	if s.preparing.Load() {
		return nil // its worktree log shows until the agent starts
	}
	if !s.Running() {
		w, h := m.termSize()
		resume := s.Started // Start ignores it for agents that can't resume
		if err := s.Start(w, h, resume, m.dirty); err != nil {
			m.setFlash("Couldn't start: " + err.Error())
			return nil
		}
		_ = saveSessions(m.sessions)
	}
	m.focus = focusTerminal
	return nil
}

// startSession starts a session's agent. Tests replace it so they don't run
// real processes.
var startSession = func(s *Session, w, h int, resume bool, dirty *atomic.Bool) error {
	return s.Start(w, h, resume, dirty)
}

// startLikeSelected adds a new session with the selected session's agent and
// folder right below it, selects it and starts it. The selected session is
// left alone, so its conversation can still be resumed. A Claude agent gets
// a fresh conversation, and a custom agent runs its normal command rather
// than its resume one.
func (m *model) startLikeSelected() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	agentName, dir := s.Agent, s.Dir
	s.mu.Unlock()
	agent, ok := s.agent()
	if !ok {
		m.setFlash(fmt.Sprintf("Couldn't start: unknown agent %q (was it removed from the config?)", agentName))
		return nil
	}
	n := newSession(agent, dir, sessionName(dir))
	n.override = s.override // a kept test of an agent that may not be saved
	w, h := m.termSize()
	if err := startSession(n, w, h, false, m.dirty); err != nil {
		m.setFlash("Couldn't start: " + err.Error())
		return nil
	}
	m.sel++
	m.sessions = slices.Insert(m.sessions, m.sel, n)
	m.clearFilter() // so the new session shows
	m.focus = focusTerminal
	_ = saveSessions(m.sessions)
	return nil
}

type openTarget int

const (
	openEditor openTarget = iota
	openExplorer
	openTerminal
)

// openFolder opens the selected session's folder in the editor, Explorer or
// a new terminal tab. It works whether or not the session is running.
func (m *model) openFolder(target openTarget) {
	s := m.current()
	if s == nil {
		return
	}
	s.mu.Lock()
	dir := s.Dir
	if target != openExplorer && s.Worktree != "" {
		dir = s.Worktree // the editor and the terminal open the worktree
	}
	s.mu.Unlock()
	var tmpl string
	fallback := false
	switch target {
	case openEditor:
		tmpl, fallback = editorTemplate(openSettings, exec.LookPath)
	case openExplorer:
		tmpl = defaultExplorer
	case openTerminal:
		tmpl = terminalTemplate(openSettings)
	}
	if err := launchOpen(tmpl, dir); err != nil {
		m.setFlash("Couldn't open: " + err.Error())
		return
	}
	if fallback && !m.editorHinted {
		m.editorHinted = true
		m.setFlash("Opened in Explorer: no editor set and VS Code isn't on PATH. Set [open] editor in the config (deckhand config).")
	}
}

// applyFilterInput filters the list as the filter is typed.
func (m *model) applyFilterInput() {
	m.filter = strings.TrimSpace(m.filterInput.Value())
	m.keepSelectionVisible()
}

// jumpToAttention selects the next session that needs you, then the next one
// that finished its turn (among the sessions the filter shows).
func (m *model) jumpToAttention() {
	vis := m.visible()
	n := len(vis)
	p := visiblePos(vis, m.sel)
	for _, want := range []func(snapshot) bool{
		func(s snapshot) bool { return s.status == StatusWaiting },
		func(s snapshot) bool { return s.unread },
	} {
		for i := 1; i <= n; i++ {
			j := vis[(p+i+n)%n]
			if want(m.sessions[j].Snapshot()) {
				m.sel = j
				return
			}
		}
	}
	if m.filtering() {
		m.setFlash("Nothing in this filter needs you (esc clears the filter)")
		return
	}
	m.setFlash("Nothing needs you")
}

// sendNotice shows a desktop notification for a session's status change.
func sendNotice(s *Session, n notice) {
	s.mu.Lock()
	name, agent, id := s.Name, s.Agent, s.ID
	s.mu.Unlock()
	last := ""
	if n.kind == noticeFinished {
		last = s.LastText()
	}
	title, body := noticeText(name, agent, n, last)
	if p := os.Getenv("DECKHAND_NOTIFY_LOG"); p != "" {
		// Tests record notifications instead of showing them.
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s\t%s\n", title, body)
			_ = f.Close()
		}
		return
	}
	if err := showToast(title, body, id[:8]); err != nil {
		debugf("notify %s: %v", id[:8], err)
	}
}

func (m *model) createFromDialog() tea.Cmd {
	d := m.dialog
	dir := d.target()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		d.err = "Folder not found: " + dir
		return nil
	}
	codeRepo := ""
	if !inGitRepo(dir) {
		if r := d.repoTarget(); r != "" {
			if !isDir(r) || !inGitRepo(r) {
				d.err = "Not a git repo: " + r
				return nil
			}
			codeRepo = r
		}
	}
	agent := d.agents[d.agentIdx]
	remember := func() {
		p := loadPrefs()
		p.useDir(dir)
		p.LastAgent = agent.Name
		if !inGitRepo(dir) {
			p.setCodeRepo(dir, codeRepo)
		}
		_ = p.save()
	}
	if d.useWorktree() {
		m.dialog = nil
		remember()
		return m.startWorktreeSession(agent, dir, codeRepo, len(m.sessions))
	}
	s := newSession(agent, dir, sessionName(dir))
	if codeRepo != "" {
		s.CodeRepo = codeRepo
		if r, err := findRepo(codeRepo); err == nil {
			s.RepoRoot = r.root
		}
	}
	w, h := m.termSize()
	if err := s.Start(w, h, false, m.dirty); err != nil {
		d.err = err.Error()
		return nil
	}
	m.sessions = append(m.sessions, s)
	m.sel = len(m.sessions) - 1
	m.clearFilter() // so the new session shows
	m.dialog = nil
	m.focus = focusTerminal
	_ = saveSessions(m.sessions)
	remember()
	return nil
}

// sessionName is the folder name, or the drive for a drive root.
func sessionName(dir string) string {
	name := filepath.Base(dir)
	if name == `\` || name == "." || name == "" {
		return dir
	}
	return name
}

// lockOrModifierKey reports keys that only arrive on their own as
// win32-input-mode records (see enableKeyRecords): shift, ctrl, alt and the
// windows key pressed alone, the lock keys, and media and volume keys.
// Agents never got these before, and x/vt has no encoding for them.
func lockOrModifierKey(code rune) bool {
	return code >= uv.KeyCapsLock && code <= uv.KeyIsoLevel5Shift
}

// pressID identifies a key for matching its press and release, ignoring
// lock-key states.
func pressID(k tea.Key) string {
	return fmt.Sprintf("%d/%d", k.Code, k.Mod&^(uv.ModCapsLock|uv.ModNumLock|uv.ModScrollLock))
}

// releaseOnlyKey reports whether a release with no press before it should
// count as a press: only ctrl or alt combinations. A plain key's lone
// release (the enter that started deckhand, say) is ignored.
func releaseOnlyKey(k tea.Key) bool {
	return k.Code != 0 && k.Mod&(uv.ModCtrl|uv.ModAlt) != 0
}

// toUVKey converts a key press for the session's emulator. The emulator
// matches keys by comparing every field, so drop what the Windows console
// adds that it doesn't understand: lock-key states (NumLock is usually on,
// which made arrow keys vanish), the repeat flag and the base/shifted codes.
func toUVKey(k tea.KeyPressMsg) uv.KeyPressEvent {
	return uv.KeyPressEvent{
		Text: k.Text,
		Mod:  k.Mod &^ (uv.ModCapsLock | uv.ModNumLock | uv.ModScrollLock),
		Code: k.Code,
	}
}

// New-session dialog --------------------------------------------------------
//
// Rows: the agent (←/→ to change) and a folder picker, then, when git is
// involved, a code repo picker (only when the folder isn't in git) and the
// worktree line. The pickers list matching folders as you type, recent ones
// first; tab completes like PowerShell, ↑/↓ choose (↓ past the last one
// goes to the next row), ctrl+w goes up a level, ctrl+t turns the worktree
// on or off, enter starts.

const maxFolderSuggestions = 8

const (
	fieldAgent = iota
	fieldFolder
	fieldRepo     // the code repo, when the folder isn't in git
	fieldWorktree // worktree on/off, when there's a repo
)

type newDialog struct {
	agents   []Agent
	agentIdx int
	field    int
	dir      textinput.Model
	repo     textinput.Model // the code repo
	repoSet  bool            // the repo box was edited: a new folder doesn't replace it
	worktree bool
	prefs    prefs // for the code repo last used with each folder
	cwd      string
	recents  []string
	lister   *dirLister
	sugg     []folderSuggestion // for the focused picker (the folder, unless it's the repo)
	hi       int                // highlighted suggestion, -1 for none (use what's typed)
	err      string
}

func newNewDialog(agents []Agent, width int, sessions []*Session) *newDialog {
	cwd, _ := os.Getwd()
	p := loadPrefs()
	d := &newDialog{
		agents:  agents,
		field:   fieldFolder,
		dir:     textinput.New(),
		repo:    textinput.New(),
		prefs:   p,
		cwd:     cwd,
		recents: recentDirs(p, sessions),
		lister:  newDirLister(),
		hi:      -1,
	}
	for i, a := range agents {
		if a.Name == p.LastAgent {
			d.agentIdx = i
		}
	}
	d.dir.Prompt = ""
	d.dir.Placeholder = "type a path or a folder name; tab completes"
	d.repo.Prompt = ""
	d.repo.Placeholder = "optional: a git repo this session works on"
	d.setWidth(width)
	d.refresh()
	d.syncRepo()
	return d
}

func (d *newDialog) setWidth(w int) {
	iw := w - 14
	if iw > 90 {
		iw = 90
	}
	if iw < 10 {
		iw = 10
	}
	d.dir.SetWidth(iw)
	d.repo.SetWidth(iw)
}

// input is the picker the suggestions are for.
func (d *newDialog) input() *textinput.Model {
	if d.field == fieldRepo {
		return &d.repo
	}
	return &d.dir
}

func (d *newDialog) focusCmd() tea.Cmd {
	d.dir.Blur()
	d.repo.Blur()
	switch d.field {
	case fieldFolder:
		return d.dir.Focus()
	case fieldRepo:
		return d.repo.Focus()
	}
	return nil
}

func (d *newDialog) refresh() {
	d.sugg = folderSuggestions(d.input().Value(), d.cwd, d.recents, d.lister.list, maxFolderSuggestions)
	if d.hi >= len(d.sugg) {
		d.hi = len(d.sugg) - 1
	}
}

// target is the folder the session will start in.
func (d *newDialog) target() string {
	if d.field != fieldRepo && d.hi >= 0 && d.hi < len(d.sugg) {
		return d.sugg[d.hi].Path
	}
	return resolvePath(d.dir.Value(), d.cwd)
}

// repoTarget is the code repo typed or highlighted, or "".
func (d *newDialog) repoTarget() string {
	if d.field == fieldRepo && d.hi >= 0 && d.hi < len(d.sugg) {
		return d.sugg[d.hi].Path
	}
	if strings.TrimSpace(d.repo.Value()) == "" {
		return ""
	}
	return resolvePath(d.repo.Value(), d.cwd)
}

// folderIsGit reports whether the start folder is in a git repo, which is
// then the code repo too.
func (d *newDialog) folderIsGit() bool {
	t := d.target()
	return isDir(t) && inGitRepo(t)
}

// showRepo reports whether the code repo row is shown: the start folder
// isn't in git.
func (d *newDialog) showRepo() bool { return !d.folderIsGit() }

// gitRepo is the repo a worktree would be made in, or "".
func (d *newDialog) gitRepo() string {
	if d.folderIsGit() {
		return d.target()
	}
	if r := d.repoTarget(); r != "" && isDir(r) && inGitRepo(r) {
		return r
	}
	return ""
}

func (d *newDialog) useWorktree() bool { return d.worktree && d.gitRepo() != "" }

// fields are the rows shown, in order.
func (d *newDialog) fields() []int {
	f := []int{fieldAgent, fieldFolder}
	if d.showRepo() {
		f = append(f, fieldRepo)
	}
	if d.gitRepo() != "" {
		f = append(f, fieldWorktree)
	}
	return f
}

func (d *newDialog) moveField(delta int) tea.Cmd {
	fs := d.fields()
	i := slices.Index(fs, d.field)
	if j := i + delta; i >= 0 && j >= 0 && j < len(fs) {
		return d.setField(fs[j])
	}
	return nil
}

// setField moves to a row. Leaving a picker whose suggestions are about to
// be replaced keeps the highlighted folder by typing it in.
func (d *newDialog) setField(f int) tea.Cmd {
	if f == d.field {
		return nil
	}
	swap := d.field == fieldRepo || f == fieldRepo
	if swap {
		if d.hi >= 0 && d.hi < len(d.sugg) {
			in := d.input()
			in.SetValue(d.sugg[d.hi].Path)
			in.CursorEnd()
			if d.field == fieldRepo {
				d.repoSet = true
			}
		}
		d.hi = -1
	}
	d.field = f
	if swap {
		d.refresh()
	}
	return d.focusCmd()
}

// syncRepo fills in the code repo last used with the start folder, unless
// the repo box has been edited.
func (d *newDialog) syncRepo() {
	if d.repoSet {
		return
	}
	want := ""
	if !d.folderIsGit() {
		want = d.prefs.codeRepoFor(d.target())
	}
	if d.repo.Value() != want {
		d.repo.SetValue(want)
		d.repo.CursorEnd()
	}
}

func (d *newDialog) setInput(s string) {
	in := d.input()
	in.SetValue(s)
	in.CursorEnd()
	if d.field == fieldRepo {
		d.repoSet = true
	}
	d.hi = -1
	d.err = ""
	d.refresh()
}

func (d *newDialog) update(msg tea.Msg) tea.Cmd {
	defer d.syncRepo()
	k, isKey := msg.(tea.KeyPressMsg)
	if isKey && k.String() == "ctrl+t" {
		if d.gitRepo() == "" {
			d.err = "A worktree needs a git repo: pick a folder in one, or a code repo"
			return nil
		}
		d.worktree = !d.worktree
		d.err = ""
		return nil
	}
	if isKey && d.field == fieldAgent {
		switch k.String() {
		case "left", "h":
			d.agentIdx = (d.agentIdx + len(d.agents) - 1) % len(d.agents)
			return nil
		case "right", "l", "space":
			d.agentIdx = (d.agentIdx + 1) % len(d.agents)
			return nil
		case "down", "tab", "shift+tab":
			return d.setField(fieldFolder)
		}
		// Anything else is typing: send it to the folder box.
		cmd := d.setField(fieldFolder)
		return tea.Batch(cmd, d.update(msg))
	}

	if isKey && d.field == fieldWorktree {
		switch k.String() {
		case "left", "right", "h", "l", "space":
			d.worktree = !d.worktree
		case "up", "shift+tab":
			return d.moveField(-1)
		case "tab":
			return d.setField(fieldFolder)
		}
		return nil
	}

	if isKey {
		switch k.String() {
		case "tab":
			// Complete the highlighted (or best) match and list what's inside it.
			if len(d.sugg) > 0 {
				i := d.hi
				if i < 0 {
					i = 0
				}
				d.setInput(withSep(d.sugg[i].Path))
			}
			return nil
		case "down":
			if d.hi < len(d.sugg)-1 {
				d.hi++
				return nil
			}
			return d.moveField(1)
		case "up":
			if d.hi >= 0 {
				d.hi--
				return nil
			}
			return d.moveField(-1)
		case "shift+tab":
			return d.moveField(-1)
		case "ctrl+w", "ctrl+backspace", "alt+backspace":
			d.setInput(parentPath(d.input().Value(), d.cwd))
			return nil
		}
	}

	in := d.input()
	before := in.Value()
	var cmd tea.Cmd
	*in, cmd = in.Update(msg)
	if in.Value() != before {
		if d.field == fieldRepo {
			d.repoSet = true
		}
		d.hi = -1
		d.err = ""
		d.refresh()
	}
	return cmd
}
