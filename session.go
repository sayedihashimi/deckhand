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

// Session is one agent CLI running in its own pseudo console.
type Session struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Agent           string    `json:"agent"`
	Dir             string    `json:"dir"`
	Created         time.Time `json:"created"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	Started         bool      `json:"started,omitempty"` // has run at least once, so it can be resumed
	TranscriptPath  string    `json:"transcript_path,omitempty"`
	Group           string    `json:"group,omitempty"` // its group in the list (groups.go); "" for none

	// Git (see worktree.go). CodeRepo is the repo folder picked for the
	// session: a code repo beside a start folder that isn't in git, or the
	// folder a worktree was made from. RepoRoot is that repo's main
	// checkout. A worktree session also has its worktree folder, branch,
	// and the branch and commit it started from.
	CodeRepo   string `json:"code_repo,omitempty"`
	RepoRoot   string `json:"repo_root,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Branch     string `json:"branch,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`

	info     sessionInfo // git and transcript details, filled in the background
	summary  string      // latest hook's tool summary
	notice   *notice     // a status change to notify about, taken by the model
	polling  atomic.Bool
	lastPoll time.Time
	trStamp  string // transcript size and mtime when last read

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

	keys *keyProtocol // keyboard enhancements the app asked for

	dirty *atomic.Bool // shared "screen changed" flag owned by the model

	// Saved output (screen.ans) and the conversation, for after a restart
	// (see history.go).
	saveMu      sync.Mutex  // one save at a time
	saving      atomic.Bool // a background save is running
	savedAt     time.Time   // when the last periodic save started
	hist        *history    // loaded saved screen and conversation; nil until loaded
	histLoading atomic.Bool
	wantConv    bool // v: show the conversation instead of the output
	viewRows    int  // terminal rows, for scrolling the saved screen

	// Making its worktree (see worktreeui.go).
	creating  atomic.Bool // the worktree doesn't exist yet, so it isn't saved
	preparing atomic.Bool // creating the worktree or running the setup
	prepLog   []string    // what creating the worktree and the setup printed
	closing   atomic.Bool // x is checking or removing its worktree

	// Its pull request (see prui.go).
	prs       prState     // the PR badge, polled in the background
	prPolling atomic.Bool // a PR poll is running
	prBusy    atomic.Bool // p is checking, pushing or opening
	mergeBusy atomic.Bool // m is checking or merging (see mergeui.go)

	// Testing an agent from the config screen (see agenttest.go).
	override *Agent      // the agent as edited, used instead of looking it up by name; set before Start
	testing  atomic.Bool // a test session: not saved, no notifications
	exitCode int         // the last run's exit code; -1 when it isn't known
}

// agent is the session's agent: the one it was tested with, or the one of
// that name.
func (s *Session) agent() (Agent, bool) {
	if s.override != nil {
		return *s.override, true
	}
	return agentByName(s.Agent)
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
	if agent.Claude {
		s.ClaudeSessionID = id
	}
	return s
}

// Start launches the agent at the given terminal size. resume continues a
// previous Claude conversation instead of starting a new one.
func (s *Session) Start(width, height int, resume bool, dirty *atomic.Bool) error {
	agent, ok := s.agent()
	if !ok {
		return fmt.Errorf("unknown agent %q (was it removed from the config?)", s.Agent)
	}
	resume = resume && agent.CanResume() && s.ClaudeSessionID != ""
	if agent.Claude {
		_ = os.Remove(filepath.Join(sessionDir(s.ID), "status.json"))
		if !resume && (s.ClaudeSessionID == "" || s.Started) {
			// A fresh conversation needs an unused session ID.
			s.ClaudeSessionID = uuid.NewString()
		}
	}
	s.mu.Lock()
	extra := s.launchExtrasLocked()
	s.mu.Unlock()
	spec, err := buildLaunch(agent, s.ID, s.ClaudeSessionID, resume, extra)
	if err != nil {
		return err
	}
	cmd := exec.Command(spec.exe, spec.args...)
	cmd.Dir = s.Dir
	cmd.Env = append(sessionEnv(os.Environ()), spec.env...)
	cmd.Env = append(cmd.Env,
		"DECKHAND_SESSION_ID="+s.ID,
		"DECKHAND_HOME="+dataDir(),
	)
	debugf("start %s: %s %q", s.ID[:8], spec.exe, spec.args)

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
	emu.SetScrollbackSize(maxHistoryLines)
	keys := &keyProtocol{}
	keys.register(emu.Emulator)
	emu.Emulator.SetCallbacks(vt.Callbacks{
		Bell: func() {
			s.mu.Lock()
			s.bell = true
			s.mu.Unlock()
		},
		Title: func(t string) {
			s.mu.Lock()
			s.title = strings.TrimSpace(t) // "✳ name" arrives as " name" (see oscfilter.go)
			s.mu.Unlock()
		},
		EnableMode:  func(m ansi.Mode) { s.setMode(m, true) },
		DisableMode: func(m ansi.Mode) { s.setMode(m, false) },
	})
	if err := pty.Start(cmd); err != nil {
		_ = pty.Close()
		return fmt.Errorf("start %s: %w", agent.Name, err)
	}

	s.mu.Lock()
	s.emu, s.pty, s.cmd, s.dirty = emu, pty, cmd, dirty
	s.keys = keys
	s.mouseModes = map[ansi.Mode]bool{}
	s.altScroll = false
	s.scrollOff = 0
	s.running = true
	s.Started = true
	s.bell = false
	s.exitInfo = ""
	s.lastSeq = 0
	s.savedAt = time.Now()
	s.hist = nil // reload after this run: the transcript and screen change
	s.wantConv = false
	s.prepLog = nil
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
	out := make([]byte, 0, len(buf))
	var filter stringFilter // see oscfilter.go; one per run, its state spans reads
	for {
		n, err := pty.Read(buf)
		if n > 0 {
			out = filter.Filter(out[:0], buf[:n])
			_, _ = emu.Write(out)
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
	if !quittingAll.Load() { // quit already saved it, before ending the process
		s.SaveScreen()
	}
	s.mu.Lock()
	s.running = false
	info := "exited"
	s.exitCode = -1
	if cmd.ProcessState != nil {
		s.exitCode = cmd.ProcessState.ExitCode()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		info = fmt.Sprintf("exited (%d)", ee.ExitCode())
		s.exitCode = ee.ExitCode()
	} else if err != nil {
		info = "exited: " + err.Error()
	} else if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() > 0 {
		info = fmt.Sprintf("exited (%d)", cmd.ProcessState.ExitCode())
	}
	s.exitInfo = info // full text for the header; the sidebar has little room
	detail := "exited · ⏎ restart"
	if a, ok := s.agent(); ok && a.CanResume() {
		detail = "exited · ⏎ resume"
	}
	s.setStatusLocked(StatusExited, detail)
	s.mu.Unlock()
	if s.dirty != nil {
		s.dirty.Store(true)
	}
}

func (s *Session) setStatusLocked(st Status, detail string) {
	if n, ok := noticeFor(s.status, st, detail, s.summary); ok {
		s.notice = &n
	}
	if st != s.status {
		s.since = time.Now()
		if st == StatusWaiting || (st == StatusIdle && s.status == StatusWorking) || st == StatusExited {
			s.unread = true
		}
		if st == StatusIdle {
			// Read the transcript on the next tick, so the finished reply
			// shows without waiting for the regular poll.
			s.lastPoll = time.Time{}
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
	agent, _ := s.agent()
	if !agent.Claude {
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
		if ev.Transcript != "" {
			s.TranscriptPath = ev.Transcript
		}
		s.summary = ev.Summary
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
	status   Status
	detail   string
	since    time.Time
	unread   bool
	running  bool
	title    string
	exitInfo string
	info     sessionInfo
	summary  string   // latest hook's tool summary, e.g. the Bash command
	reply    []string // the reply that ended the turn, once it's been read
	worktree bool     // it has its own git worktree
	repoName string   // the code repo's name, when it isn't the start folder's
	pr       *prInfo  // the branch's pull request, when it has one
}

func (s *Session) Snapshot() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	sn := snapshot{s.status, s.detail, s.since, s.unread, s.running, s.title, s.exitInfo, s.info, s.summary, nil, s.Worktree != "", "", nil}
	if s.prs.pr != nil && s.prs.branch == s.prBranchLocked() {
		pr := *s.prs.pr
		sn.pr = &pr
	}
	if s.separateRepoLocked() {
		sn.repoName = s.repoNameLocked()
	}
	// Only a transcript read after the turn ended has that turn's reply;
	// an earlier read could still hold the previous one.
	if s.status == StatusIdle && s.detail == detailYourTurn && !s.info.textAt.Before(s.since) {
		sn.reply = s.info.lastText
	}
	return sn
}

// TakeNotice returns (and clears) a pending status change to notify about.
func (s *Session) TakeNotice() *notice {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notice
	s.notice = nil
	return n
}

// LastText is the latest Claude reply's text, read from the transcript now
// so a "finished" notification can quote the reply that just ended.
func (s *Session) LastText() string {
	s.mu.Lock()
	path := s.transcriptPathLocked()
	s.mu.Unlock()
	if path == "" {
		return ""
	}
	_, _, text, err := readTranscriptInfo(path)
	if err != nil {
		return ""
	}
	return text
}

func (s *Session) transcriptPathLocked() string {
	if s.TranscriptPath != "" {
		return s.TranscriptPath
	}
	if a, ok := s.agent(); ok && a.Claude {
		return guessTranscriptPath(s.Dir, s.ClaudeSessionID)
	}
	return ""
}

// MaybePoll starts a background refresh of the git and transcript details
// if they're due: every few seconds for running or selected sessions, less
// often for the rest. Only one refresh runs at a time per session.
func (s *Session) MaybePoll(now time.Time, active bool) {
	s.mu.Lock()
	every := pollIdle
	if active || s.running {
		every = pollActive
	}
	due := s.lastPoll.IsZero() || now.Sub(s.lastPoll) >= every
	if due {
		s.lastPoll = now
	}
	dir, base, tr, stamp := s.gitDirLocked(), s.BaseBranch, s.transcriptPathLocked(), s.trStamp
	s.mu.Unlock()
	if !due || !s.polling.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.polling.Store(false)
		g := readGitInfo(dir, base)
		var model, text string
		var tokens int
		var readErr error
		checked := false
		newStamp := stamp
		if tr != "" {
			if fi, err := os.Stat(tr); err == nil {
				checked = true
				newStamp = fmt.Sprintf("%d/%d", fi.Size(), fi.ModTime().UnixNano())
				if newStamp != stamp {
					model, tokens, text, readErr = readTranscriptInfo(tr)
				}
			}
		}
		s.mu.Lock()
		s.info.git = g
		if newStamp != stamp && readErr == nil {
			s.trStamp = newStamp
			if model != "" {
				s.info.model, s.info.ctxTokens = model, tokens
			}
			s.info.lastText = replyLines(text)
		}
		if checked && readErr == nil {
			s.info.textAt = now
		}
		s.mu.Unlock()
		if s.dirty != nil {
			s.dirty.Store(true)
		}
	}()
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

// SendKey forwards a key to the agent. Keys the legacy encoding can't
// express (shift+enter, ...) go in the protocol the agent asked for.
func (s *Session) SendKey(k uv.KeyPressEvent) {
	s.mu.Lock()
	emu, keys, running := s.emu, s.keys, s.running
	s.mu.Unlock()
	if emu != nil && running {
		sendKey(emu, keys, k)
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
	s.viewRows = height
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
	if most, ok := s.savedScroll(); ok {
		// After a restart: the saved screen, or nothing to scroll.
		s.mu.Lock()
		defer s.mu.Unlock()
		s.scrollOff = min(max(s.scrollOff+delta, 0), most)
		return most > 0
	}
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
	if most, ok := s.savedScroll(); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.scrollOff = min(max(s.scrollOff, 0), most)
		return s.scrollOff
	}
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

// savedScroll reports whether the session isn't running and shows
// something other than this run's terminal, and if so how far back it can
// scroll: the saved screen's lines beyond one screenful, or 0 for the
// conversation and the "Not running" message.
func (s *Session) savedScroll() (most int, ok bool) {
	if s.Running() {
		return 0, false
	}
	st := s.historyState()
	switch st.view {
	case viewTerminal:
		return 0, false
	case viewScreen:
		s.mu.Lock()
		rows := max(s.viewRows, 1)
		s.mu.Unlock()
		return max(len(st.hist.screen.lines)-rows, 0), true
	}
	return 0, true
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

// ExitCode is the last run's exit code, or -1 when it isn't known.
func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// LastLines is the last n lines of output that aren't blank.
func (s *Session) LastLines(n int) []string {
	emu := s.Emulator()
	if emu == nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(emu.String(), "\r\n", "\n"), "\n") {
		if l = strings.TrimRight(l, " \t"); l != "" {
			out = append(out, l)
		}
	}
	return out[max(len(out)-n, 0):]
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
