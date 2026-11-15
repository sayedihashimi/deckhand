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
		m.dialog = newNewDialog(m.agents, w)
		m.focus = focusDialog
		return m, m.dialog.focusCmd()
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
	dir := strings.TrimSpace(d.dir.Value())
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if strings.HasPrefix(dir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[1:])
		}
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		d.err = "Folder not found: " + dir
		return nil
	}
	name := strings.TrimSpace(d.name.Value())
	if name == "" {
		name = filepath.Base(dir)
	}
	agent := d.agents[d.agentIdx]
	s := newSession(agent, dir, name)
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
	return nil
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

type newDialog struct {
	agents   []Agent
	agentIdx int
	field    int // 0 agent, 1 folder, 2 name
	dir      textinput.Model
	name     textinput.Model
	err      string
}

func newNewDialog(agents []Agent, width int) *newDialog {
	cwd, _ := os.Getwd()
	d := &newDialog{agents: agents, dir: textinput.New(), name: textinput.New()}
	d.dir.SetValue(cwd)
	d.dir.Prompt = ""
	d.name.Prompt = ""
	d.name.Placeholder = "defaults to the folder name"
	d.setWidth(width)
	return d
}

func (d *newDialog) setWidth(w int) {
	iw := w - 16
	if iw > 70 {
		iw = 70
	}
	if iw < 10 {
		iw = 10
	}
	d.dir.SetWidth(iw)
	d.name.SetWidth(iw)
}

func (d *newDialog) focusCmd() tea.Cmd {
	d.dir.Blur()
	d.name.Blur()
	switch d.field {
	case 1:
		return d.dir.Focus()
	case 2:
		return d.name.Focus()
	}
	return nil
}

func (d *newDialog) update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "tab", "down":
			d.field = (d.field + 1) % 3
			return d.focusCmd()
		case "shift+tab", "up":
			d.field = (d.field + 2) % 3
			return d.focusCmd()
		case "left":
			if d.field == 0 {
				d.agentIdx = (d.agentIdx + len(d.agents) - 1) % len(d.agents)
				return nil
			}
		case "right", "space":
			if d.field == 0 {
				d.agentIdx = (d.agentIdx + 1) % len(d.agents)
				return nil
			}
		}
	}
	var cmd tea.Cmd
	switch d.field {
	case 1:
		d.dir, cmd = d.dir.Update(msg)
		d.err = ""
	case 2:
		d.name, cmd = d.name.Update(msg)
	}
	return cmd
}
