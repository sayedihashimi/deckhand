package main

import (
	"fmt"
	"os"
	"path/filepath"
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
	rename   *textinput.Model
	flash    string
	flashAt  time.Time
	quitting bool
}

type confirmPrompt struct {
	text   string
	action func(m *model) tea.Cmd
}

func newModel() *model {
	m := &model{
		sessions: loadSessions(),
		dirty:    &atomic.Bool{},
		agents:   availableAgents(),
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

// termSize is the size of the embedded terminal: right of the sidebar, below
// the session header, above the footer.
func (m *model) termSize() (int, int) {
	w := m.width - m.sidebarWidth() - 1
	h := m.height - 2
	if w < 10 {
		w = 10
	}
	if h < 3 {
		h = 3
	}
	return w, h
}

func (m *model) current() *Session {
	if m.sel >= 0 && m.sel < len(m.sessions) {
		return m.sessions[m.sel]
	}
	return nil
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
		w, h := m.termSize()
		for _, s := range m.sessions {
			s.Resize(w, h)
		}
		if m.dialog != nil {
			m.dialog.setWidth(w)
		}
		return m, nil

	case frameMsg:
		m.frame++
		if m.frame%refreshEvery == 0 {
			now := time.Now()
			for _, s := range m.sessions {
				s.Refresh(now)
			}
		}
		if s := m.current(); s != nil {
			s.MarkSeen()
		}
		m.dirty.Store(false)
		return m, frameTick()

	case tea.PasteMsg:
		if m.rename != nil {
			var cmd tea.Cmd
			*m.rename, cmd = m.rename.Update(msg)
			return m, cmd
		}
		switch m.focus {
		case focusTerminal:
			if s := m.current(); s != nil {
				s.Paste(msg.Content)
			}
		case focusDialog:
			return m, m.dialog.update(msg)
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	if m.focus == focusDialog && m.dialog != nil {
		return m, m.dialog.update(msg)
	}
	return m, nil
}

func (m *model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()

	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if key == "y" || key == "Y" {
			return m, c.action(m)
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

	switch m.focus {
	case focusTerminal:
		if key == "ctrl+q" || key == "ctrl+]" {
			m.focus = focusSidebar
			return m, nil
		}
		if s := m.current(); s != nil {
			if !s.Running() {
				m.focus = focusSidebar
				return m, nil
			}
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
	}

	// Sidebar.
	switch key {
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < len(m.sessions)-1 {
			m.sel++
		}
	case "home", "g":
		m.sel = 0
	case "end", "G":
		m.sel = len(m.sessions) - 1
	case "enter", "right", "l":
		return m, m.openSelected()
	case "tab":
		m.jumpToAttention()
	case "n":
		if len(m.agents) == 0 {
			m.setFlash("No supported CLIs found on PATH")
			return m, nil
		}
		w, _ := m.termSize()
		m.dialog = newNewDialog(m.agents, w, m.sessions)
		m.focus = focusDialog
		return m, m.dialog.focusCmd()
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
		if s := m.current(); s != nil {
			name := s.Name
			text := fmt.Sprintf("Remove %q from the list? y/n", name)
			if s.Running() {
				text = fmt.Sprintf("End %q and remove it? y/n", name)
			}
			m.confirm = &confirmPrompt{text: text, action: func(m *model) tea.Cmd {
				m.removeSelected()
				return nil
			}}
		}
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
			if i := int(key[0] - '1'); i < len(m.sessions) {
				m.sel = i
				return m, m.openSelected()
			}
		}
	}
	return m, nil
}

func (m *model) quit() tea.Cmd {
	_ = saveSessions(m.sessions)
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
	if !s.Running() {
		w, h := m.termSize()
		resume := s.Agent == "claude" && s.Started
		if err := s.Start(w, h, resume, m.dirty); err != nil {
			m.setFlash("Couldn't start: " + err.Error())
			return nil
		}
		_ = saveSessions(m.sessions)
	}
	m.focus = focusTerminal
	return nil
}

func (m *model) removeSelected() {
	s := m.current()
	if s == nil {
		return
	}
	s.Kill()
	removeSessionFiles(s.ID)
	m.sessions = append(m.sessions[:m.sel], m.sessions[m.sel+1:]...)
	if m.sel >= len(m.sessions) && m.sel > 0 {
		m.sel--
	}
	_ = saveSessions(m.sessions)
}

// jumpToAttention selects the next session that needs you, then the next one
// that finished its turn.
func (m *model) jumpToAttention() {
	n := len(m.sessions)
	for _, want := range []func(snapshot) bool{
		func(s snapshot) bool { return s.status == StatusWaiting },
		func(s snapshot) bool { return s.unread },
	} {
		for i := 1; i <= n; i++ {
			j := (m.sel + i) % n
			if want(m.sessions[j].Snapshot()) {
				m.sel = j
				return
			}
		}
	}
	m.setFlash("Nothing needs you")
}

func (m *model) createFromDialog() tea.Cmd {
	d := m.dialog
	dir := d.target()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		d.err = "Folder not found: " + dir
		return nil
	}
	agent := d.agents[d.agentIdx]
	s := newSession(agent, dir, sessionName(dir))
	w, h := m.termSize()
	if err := s.Start(w, h, false, m.dirty); err != nil {
		d.err = err.Error()
		return nil
	}
	m.sessions = append(m.sessions, s)
	m.sel = len(m.sessions) - 1
	m.dialog = nil
	m.focus = focusTerminal
	_ = saveSessions(m.sessions)

	p := loadPrefs()
	p.useDir(dir)
	p.LastAgent = agent.Name
	_ = p.save()
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
// Two rows: the agent (←/→ to change) and a folder picker. The picker lists
// matching folders as you type, recent ones first; tab completes like
// PowerShell, ↑/↓ choose, ctrl+w goes up a level, enter starts.

const maxFolderSuggestions = 8

const (
	fieldAgent = iota
	fieldFolder
)

type newDialog struct {
	agents   []Agent
	agentIdx int
	field    int
	dir      textinput.Model
	cwd      string
	recents  []string
	lister   *dirLister
	sugg     []folderSuggestion
	hi       int // highlighted suggestion, -1 for none (use what's typed)
	err      string
}

func newNewDialog(agents []Agent, width int, sessions []*Session) *newDialog {
	cwd, _ := os.Getwd()
	p := loadPrefs()
	d := &newDialog{
		agents:  agents,
		field:   fieldFolder,
		dir:     textinput.New(),
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
	d.setWidth(width)
	d.refresh()
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
}

func (d *newDialog) focusCmd() tea.Cmd {
	if d.field == fieldFolder {
		return d.dir.Focus()
	}
	d.dir.Blur()
	return nil
}

func (d *newDialog) refresh() {
	d.sugg = folderSuggestions(d.dir.Value(), d.cwd, d.recents, d.lister.list, maxFolderSuggestions)
	if d.hi >= len(d.sugg) {
		d.hi = len(d.sugg) - 1
	}
}

// target is the folder the session will start in.
func (d *newDialog) target() string {
	if d.hi >= 0 && d.hi < len(d.sugg) {
		return d.sugg[d.hi].Path
	}
	return resolvePath(d.dir.Value(), d.cwd)
}

func (d *newDialog) setInput(s string) {
	d.dir.SetValue(s)
	d.dir.CursorEnd()
	d.hi = -1
	d.err = ""
	d.refresh()
}

func (d *newDialog) update(msg tea.Msg) tea.Cmd {
	k, isKey := msg.(tea.KeyPressMsg)
	if isKey && d.field == fieldAgent {
		switch k.String() {
		case "left", "h":
			d.agentIdx = (d.agentIdx + len(d.agents) - 1) % len(d.agents)
			return nil
		case "right", "l", "space":
			d.agentIdx = (d.agentIdx + 1) % len(d.agents)
			return nil
		case "down", "tab", "shift+tab":
			d.field = fieldFolder
			return d.focusCmd()
		}
		// Anything else is typing: send it to the folder box.
		d.field = fieldFolder
		cmd := d.focusCmd()
		return tea.Batch(cmd, d.update(msg))
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
			}
			return nil
		case "up":
			if d.hi >= 0 {
				d.hi--
				return nil
			}
			d.field = fieldAgent
			return d.focusCmd()
		case "shift+tab":
			d.field = fieldAgent
			return d.focusCmd()
		case "ctrl+w", "ctrl+backspace", "alt+backspace":
			d.setInput(parentPath(d.dir.Value(), d.cwd))
			return nil
		}
	}

	before := d.dir.Value()
	var cmd tea.Cmd
	d.dir, cmd = d.dir.Update(msg)
	if d.dir.Value() != before {
		d.hi = -1
		d.err = ""
		d.refresh()
	}
	return cmd
}
