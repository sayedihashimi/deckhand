package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
	"github.com/google/uuid"
)

// Agent is a CLI that deckhand can run in a session.
type Agent struct {
	Name  string   // shown in the UI and stored with the session
	Exe   string   // looked up on PATH
	Args  []string // extra arguments
	Hooks bool     // reports status through Claude Code hooks
}

var knownAgents = []Agent{
	{Name: "claude", Exe: "claude", Hooks: true},
	{Name: "codex", Exe: "codex"},
	{Name: "gemini", Exe: "gemini"},
	{Name: "pwsh", Exe: "pwsh", Args: []string{"-NoLogo"}},
	{Name: "powershell", Exe: "powershell", Args: []string{"-NoLogo"}},
	{Name: "cmd", Exe: "cmd"},
}

// availableAgents returns the agents found on PATH, keeping the order above
// and dropping Windows PowerShell when PowerShell 7 is present.
func availableAgents() []Agent {
	var out []Agent
	havePwsh := false
	for _, a := range knownAgents {
		if _, err := exec.LookPath(a.Exe); err != nil {
			continue
		}
		if a.Name == "pwsh" {
			havePwsh = true
		}
		if a.Name == "powershell" && havePwsh {
			continue
		}
		out = append(out, a)
	}
	return out
}

func agentByName(name string) (Agent, bool) {
	for _, a := range knownAgents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// Session is one agent CLI running in its own pseudo console.
type Session struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Agent           string    `json:"agent"`
	Dir             string    `json:"dir"`
	Created         time.Time `json:"created"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	Started         bool      `json:"started,omitempty"` // has run at least once, so it can be resumed

	mu         sync.Mutex
	emu        *vt.SafeEmulator
	pty        xpty.Pty
	cmd        *exec.Cmd
	running    bool
	status     Status
	detail     string
	since      time.Time
	lastOutput time.Time
	lastSeq    int64
	bell       bool
	unread     bool // finished or needs you, and not looked at yet
	title      string
	exitInfo   string

	// Mouse and scrolling.
	mouseModes map[ansi.Mode]bool // mouse reporting modes the app has turned on
	altScroll  bool               // app asked for wheel-as-arrow-keys on the alt screen (mode 1007)
	scrollOff  int                // lines scrolled back from the live view
	scrollBase int                // scrollback length when scrollOff was last set

	dirty *atomic.Bool // shared "screen changed" flag owned by the model
}

func newSession(agent Agent, dir, name string) *Session {
	id := uuid.NewString()
	s := &Session{
		ID:      id,
		Name:    name,
		Agent:   agent.Name,
		Dir:     dir,
		Created: time.Now(),
	}
	if agent.Hooks {
		s.ClaudeSessionID = id
	}
	return s
}

// Start launches the agent at the given terminal size. resume continues a
// previous Claude conversation instead of starting a new one.
func (s *Session) Start(width, height int, resume bool, dirty *atomic.Bool) error {
	agent, ok := agentByName(s.Agent)
	if !ok {
		return fmt.Errorf("unknown agent %q", s.Agent)
	}
	exe, err := exec.LookPath(agent.Exe)
	if err != nil {
		return fmt.Errorf("%s not found on PATH", agent.Exe)
	}
	args := append([]string{}, agent.Args...)
	if agent.Hooks {
		settings, err := writeClaudeSettings(s.ID)
		if err != nil {
			return fmt.Errorf("write hook settings: %w", err)
		}
		_ = os.Remove(filepath.Join(sessionDir(s.ID), "status.json"))
		if resume && s.ClaudeSessionID != "" {
			args = append(args, "--resume", s.ClaudeSessionID)
		} else {
			if s.ClaudeSessionID == "" {
				s.ClaudeSessionID = s.ID
			}
			args = append(args, "--session-id", s.ClaudeSessionID)
		}
		args = append(args, "--settings", settings)
	}

	// .cmd and .bat shims (e.g. npm installs) have to run through cmd.exe.
	var cmd *exec.Cmd
	switch strings.ToLower(filepath.Ext(exe)) {
	case ".cmd", ".bat":
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		cmd = exec.Command(comspec, append([]string{"/c", exe}, args...)...)
	default:
		cmd = exec.Command(exe, args...)
	}
	cmd.Dir = s.Dir
	cmd.Env = append(sessionEnv(os.Environ()),
		"DECKHAND_SESSION_ID="+s.ID,
		"DECKHAND_HOME="+dataDir(),
	)

	if width < 10 {
		width = 10
	}
	if height < 3 {
		height = 3
	}
	pty, err := xpty.NewPty(width, height)
	if err != nil {
		return fmt.Errorf("create pseudo console: %w", err)
	}
	emu := vt.NewSafeEmulator(width, height)
	emu.SetScrollbackSize(5000)
	emu.Emulator.SetCallbacks(vt.Callbacks{
		Bell: func() {
			s.mu.Lock()
			s.bell = true
			s.mu.Unlock()
		},
		Title: func(t string) {
			s.mu.Lock()
			s.title = t
			s.mu.Unlock()
		},
		EnableMode:  func(m ansi.Mode) { s.setMode(m, true) },
		DisableMode: func(m ansi.Mode) { s.setMode(m, false) },
	})
	if err := pty.Start(cmd); err != nil {
		_ = pty.Close()
		return fmt.Errorf("start %s: %w", agent.Exe, err)
	}

	s.mu.Lock()
	s.emu, s.pty, s.cmd, s.dirty = emu, pty, cmd, dirty
	s.mouseModes = map[ansi.Mode]bool{}
	s.altScroll = false
	s.scrollOff = 0
	s.running = true
	s.Started = true
	s.bell = false
	s.exitInfo = ""
	s.lastSeq = 0
	s.setStatusLocked(StatusStarting, "")
	s.mu.Unlock()

	go s.readLoop(pty, emu)
	go copyInput(pty, emu, s.ID) // keys and terminal replies -> agent
	go s.waitLoop(cmd, pty)
	return nil
}

// parentSessionVars are markers a running Claude Code sets for its own child
// processes. If deckhand is started from inside a Claude session, passing
// them on makes each agent think it's a sub-session (Claude then turns off
// transcript saving, which breaks --resume). Configuration such as
// ANTHROPIC_BASE_URL is kept.
var parentSessionVars = map[string]bool{
	"CLAUDECODE":                   true,
	"CLAUDE_CODE_CHILD_SESSION":    true,
	"CLAUDE_CODE_ENTRYPOINT":       true,
	"CLAUDE_CODE_EXECPATH":         true,
	"CLAUDE_CODE_MESSAGING_SOCKET": true,
	"CLAUDE_CODE_MESSAGING_TOKEN":  true,
	"CLAUDE_CODE_SESSION_ATTENDED": true,
	"CLAUDE_CODE_SESSION_ID":       true,
	"CLAUDE_PID":                   true,
	"AI_AGENT":                     true,
	"DECKHAND_SESSION_ID":          true,
}

func sessionEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !parentSessionVars[strings.ToUpper(name)] {
			out = append(out, kv)
		}
	}
	return out
}

func (s *Session) readLoop(pty xpty.Pty, emu *vt.SafeEmulator) {
	buf := make([]byte, 64*1024)
	for {
		n, err := pty.Read(buf)
		if n > 0 {
			_, _ = emu.Write(buf[:n])
			s.mu.Lock()
			s.lastOutput = time.Now()
			s.mu.Unlock()
			if s.dirty != nil {
				s.dirty.Store(true)
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) waitLoop(cmd *exec.Cmd, pty xpty.Pty) {
	err := xpty.WaitProcess(context.Background(), cmd)
	time.Sleep(150 * time.Millisecond) // let the last output drain
	_ = pty.Close()
	s.mu.Lock()
	s.running = false
	info := "exited"
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		info = fmt.Sprintf("exited (%d)", ee.ExitCode())
	} else if err != nil {
		info = "exited: " + err.Error()
	} else if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() > 0 {
		info = fmt.Sprintf("exited (%d)", cmd.ProcessState.ExitCode())
	}
	s.exitInfo = info
	s.setStatusLocked(StatusExited, info)
	s.mu.Unlock()
	if s.dirty != nil {
		s.dirty.Store(true)
	}
}

func (s *Session) setStatusLocked(st Status, detail string) {
	if st != s.status {
		s.since = time.Now()
		if st == StatusWaiting || (st == StatusIdle && s.status == StatusWorking) || st == StatusExited {
			s.unread = true
		}
	}
	s.status = st
	s.detail = detail
}

// Refresh updates the status from hook events or output heuristics.
func (s *Session) Refresh(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	agent, _ := agentByName(s.Agent)
	if !agent.Hooks {
		st, d := heuristicStatus(s.lastOutput, now, s.bell)
		if st != StatusIdle || s.status != StatusStarting || now.Sub(s.since) > 3*time.Second {
			s.setStatusLocked(st, d)
		}
		return
	}
	if ev, _, err := readStatusFile(s.ID); err == nil && ev.Seq > s.lastSeq {
		s.lastSeq = ev.Seq
		if ev.ClaudeSessionID != "" {
			s.ClaudeSessionID = ev.ClaudeSessionID
		}
		if st, d, ok := statusFromHook(ev); ok {
			s.setStatusLocked(st, d)
		}
	}
	switch {
	case s.status == StatusStarting && !s.lastOutput.IsZero() && now.Sub(s.lastOutput) > 3*time.Second:
		// Older Claude versions may not send SessionStart: settle to idle.
		s.setStatusLocked(StatusIdle, "ready")
	case s.status == StatusWorking && now.Sub(s.lastOutput) > stuckWorkingWindow:
		// Claude animates a spinner while it works, so a long silence means
		// the turn ended without a Stop hook (for example an Esc interrupt).
		s.setStatusLocked(StatusIdle, "interrupted?")
	}
}

// snapshot is a consistent copy of what the sidebar needs.
type snapshot struct {
	status  Status
	detail  string
	since   time.Time
	unread  bool
	running bool
	title   string
}

func (s *Session) Snapshot() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return snapshot{s.status, s.detail, s.since, s.unread, s.running, s.title}
}

// MarkSeen clears the unread flag and any bell.
func (s *Session) MarkSeen() {
	s.mu.Lock()
	s.unread = false
	s.bell = false
	if s.status == StatusWaiting && s.detail == "bell" {
		s.setStatusLocked(StatusIdle, "")
	}
	s.mu.Unlock()
}

func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Session) Emulator() *vt.SafeEmulator {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emu
}

func (s *Session) SendKey(k uv.KeyPressEvent) {
	if emu := s.Emulator(); emu != nil && s.Running() {
		emu.SendKey(k)
	}
}

func (s *Session) Paste(text string) {
	if emu := s.Emulator(); emu != nil && s.Running() {
		emu.Paste(text)
	}
}

func (s *Session) Resize(width, height int) {
	s.mu.Lock()
	emu, pty, running := s.emu, s.pty, s.running
	s.mu.Unlock()
	if emu == nil || width < 10 || height < 3 {
		return
	}
	if emu.Width() == width && emu.Height() == height {
		return
	}
	emu.Resize(width, height)
	if running && pty != nil {
		_ = pty.Resize(width, height)
	}
}

// modeAltScroll is DEC private mode 1007: on the alternate screen, the
// terminal turns the mouse wheel into up/down arrow keys.
var modeAltScroll = ansi.DECMode(1007)

var mouseReportingModes = []ansi.Mode{
	ansi.ModeMouseX10, ansi.ModeMouseNormal, ansi.ModeMouseHighlight,
	ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent,
}

func (s *Session) setMode(m ansi.Mode, on bool) {
	debugf("mode %v on=%v (%s)", m, on, s.ID[:8])
	s.mu.Lock()
	defer s.mu.Unlock()
	if m == modeAltScroll {
		s.altScroll = on
		return
	}
	for _, mm := range mouseReportingModes {
		if m == mm {
			if s.mouseModes == nil {
				s.mouseModes = map[ansi.Mode]bool{}
			}
			s.mouseModes[m] = on
		}
	}
}

// MouseReporting reports whether the app wants mouse events itself.
func (s *Session) MouseReporting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return false
	}
	for _, on := range s.mouseModes {
		if on {
			return true
		}
	}
	return false
}

// SendMouse forwards a mouse event (in terminal coordinates) to the app.
func (s *Session) SendMouse(ev uv.MouseEvent) {
	if emu := s.Emulator(); emu != nil && s.Running() {
		emu.SendMouse(ev)
	}
}

// Scroll moves the scrollback view by delta lines (positive is back in
// history). It returns false when there's nothing to scroll, e.g. on the
// alternate screen, which has no scrollback.
func (s *Session) Scroll(delta int) bool {
	emu := s.Emulator()
	if emu == nil || emu.IsAltScreen() {
		return false
	}
	sb := emu.ScrollbackLen()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrollOff += delta
	if s.scrollOff > sb {
		s.scrollOff = sb
	}
	if s.scrollOff < 0 {
		s.scrollOff = 0
	}
	s.scrollBase = sb
	return true
}

// ScrollToBottom returns to the live view.
func (s *Session) ScrollToBottom() {
	s.mu.Lock()
	s.scrollOff = 0
	s.mu.Unlock()
}

// ScrollOffset returns how far back the view is. While scrolled back, new
// output would push the content up, so the offset grows with the scrollback
// to keep the same lines in view.
func (s *Session) ScrollOffset() int {
	emu := s.Emulator()
	if emu == nil {
		return 0
	}
	if emu.IsAltScreen() {
		s.ScrollToBottom()
		return 0
	}
	sb := emu.ScrollbackLen()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scrollOff > 0 && sb > s.scrollBase {
		s.scrollOff += sb - s.scrollBase
	}
	s.scrollBase = sb
	if s.scrollOff > sb {
		s.scrollOff = sb
	}
	return s.scrollOff
}

// AltScrollWanted reports whether the wheel should become arrow keys.
func (s *Session) AltScrollWanted() bool {
	emu := s.Emulator()
	if emu == nil || !emu.IsAltScreen() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.altScroll
}

// Kill ends the agent process.
func (s *Session) Kill() {
	s.mu.Lock()
	cmd, running := s.cmd, s.running
	s.mu.Unlock()
	if running && cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
