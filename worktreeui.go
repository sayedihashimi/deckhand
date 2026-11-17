package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Worktree sessions in the UI: creating one (the dialog's worktree line, or
// W), what a session passes to its agent, and closing one (x). The git work
// is in worktree.go.

// Sessions ----------------------------------------------------------------------

// gitDirLocked is the folder the branch line, and later the diff, the PR
// and the merge, use: the worktree, else the code repo, else the start
// folder. Never a notes start folder when there's a code repo.
func (s *Session) gitDirLocked() string {
	switch {
	case s.Worktree != "":
		return s.Worktree
	case s.CodeRepo != "":
		return s.CodeRepo
	}
	return s.Dir
}

// separateRepoLocked reports whether the code repo is somewhere other than
// the start folder (a notes folder, say), so the agent has to be told
// about it.
func (s *Session) separateRepoLocked() bool {
	return s.CodeRepo != "" && !within(s.Dir, s.CodeRepo) && !within(s.Dir, s.RepoRoot) && !within(s.Dir, s.Worktree)
}

// repoNameLocked is the code repo's folder name.
func (s *Session) repoNameLocked() string {
	if s.RepoRoot != "" {
		return filepath.Base(s.RepoRoot)
	}
	return filepath.Base(s.CodeRepo)
}

// launchExtrasLocked is what the session adds to its agent's command line:
// a worktree outside the start folder reaches Claude through --add-dir and
// --append-system-prompt (custom agents get them from the claude shim),
// and every agent gets DECKHAND_WORKTREE and DECKHAND_BRANCH.
func (s *Session) launchExtrasLocked() launchExtras {
	var x launchExtras
	if s.Worktree == "" {
		return x
	}
	x.env = []string{"DECKHAND_WORKTREE=" + s.Worktree, "DECKHAND_BRANCH=" + s.Branch}
	if s.separateRepoLocked() {
		x.addDir = s.Worktree
		x.prompt = worktreePrompt(s.RepoRoot, s.Worktree, s.Branch)
	}
	return x
}

func (s *Session) hasWorktree() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Worktree != ""
}

const maxPrepLog = 500

// addPrepLog adds a line to what creating the worktree printed.
func (s *Session) addPrepLog(line string) {
	s.mu.Lock()
	line = strings.TrimRight(ansi.Strip(strings.ReplaceAll(line, "\t", "    ")), " \r")
	s.prepLog = append(s.prepLog, line)
	if len(s.prepLog) > maxPrepLog {
		s.prepLog = s.prepLog[len(s.prepLog)-maxPrepLog:]
	}
	dirty := s.dirty
	s.mu.Unlock()
	if dirty != nil {
		dirty.Store(true)
	}
}

// PrepLog is what creating the worktree and the setup printed, until the
// agent starts.
func (s *Session) PrepLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.prepLog)
}

func (s *Session) setPrepStatus(st Status, detail string) {
	s.mu.Lock()
	s.setStatusLocked(st, detail)
	s.mu.Unlock()
}

// Creating ----------------------------------------------------------------------

type worktreeCreatedMsg struct {
	s        *Session
	w        newWorktree
	separate bool
	err      error
}

type worktreeSetupMsg struct {
	s   *Session
	err error
}

// startWorktreeSession adds a session at position at, creates its worktree
// in the background (showing each step in the main pane), runs the setup
// command, then starts the agent. codeRepo is the repo when the start
// folder isn't in git.
func (m *model) startWorktreeSession(agent Agent, startDir, codeRepo string, at int) tea.Cmd {
	repoDir, separate := startDir, codeRepo != ""
	if separate {
		repoDir = codeRepo
	}
	s := newSession(agent, startDir, sessionName(repoDir))
	s.CodeRepo = repoDir
	s.dirty = m.dirty
	s.creating.Store(true)
	s.preparing.Store(true)
	s.setPrepStatus(StatusStarting, "creating worktree…")
	at = min(max(at, 0), len(m.sessions))
	m.sessions = slices.Insert(m.sessions, at, s)
	m.sel = at
	m.clearFilter() // so the new session shows
	m.focus = focusSidebar
	cfg := worktreeSettings
	s.addPrepLog("Creating a worktree for " + repoDir)
	return func() tea.Msg {
		w, err := createWorktree(cfg, repoDir, s.addPrepLog)
		return worktreeCreatedMsg{s: s, w: w, separate: separate, err: err}
	}
}

func (m *model) indexOf(s *Session) int { return slices.Index(m.sessions, s) }

func (m *model) worktreeCreated(msg worktreeCreatedMsg) tea.Cmd {
	s := msg.s
	if m.indexOf(s) < 0 {
		return nil
	}
	s.creating.Store(false)
	if msg.err != nil {
		s.preparing.Store(false)
		m.removeSession(s)
		m.setFlash("Couldn't create the worktree: " + firstLine(msg.err.Error()))
		return nil
	}
	w := msg.w
	s.mu.Lock()
	s.Name = w.branch
	s.RepoRoot, s.Worktree, s.Branch = w.repoRoot, w.path, w.branch
	s.BaseBranch, s.BaseCommit = w.base, w.baseCommit
	if !msg.separate {
		// Start in the same subfolder of the worktree.
		dir := filepath.Join(w.path, w.sub)
		if !isDir(dir) {
			dir = w.path
		}
		s.Dir = dir
	}
	s.lastPoll = time.Time{} // show the new branch now
	s.mu.Unlock()
	_ = saveSessions(m.sessions)

	if setup := worktreeSettings.Setup; setup != "" {
		s.setPrepStatus(StatusStarting, "running setup…")
		s.addPrepLog("")
		s.addPrepLog("Setup: " + setup)
		return func() tea.Msg {
			return worktreeSetupMsg{s: s, err: runSetup(w.path, setup, s.addPrepLog)}
		}
	}
	return m.startPrepared(s)
}

func (m *model) worktreeSetupDone(msg worktreeSetupMsg) tea.Cmd {
	s := msg.s
	if m.indexOf(s) < 0 {
		return nil
	}
	if msg.err != nil {
		s.preparing.Store(false)
		s.addPrepLog("")
		s.addPrepLog("Setup failed: " + firstLine(msg.err.Error()) + ". Press enter to start the agent anyway.")
		s.setPrepStatus(StatusExited, "setup failed · ⏎ start")
		m.setFlash("Setup failed in " + s.Name + ": " + firstLine(msg.err.Error()))
		return nil
	}
	return m.startPrepared(s)
}

// startPrepared starts the agent once its worktree is ready, and goes into
// it if it's still the selected session and nothing else has the focus.
func (m *model) startPrepared(s *Session) tea.Cmd {
	s.preparing.Store(false)
	w, h := m.termSize()
	if err := startSession(s, w, h, false, m.dirty); err != nil {
		s.addPrepLog("Couldn't start: " + err.Error())
		s.setPrepStatus(StatusExited, "couldn't start")
		m.setFlash("Couldn't start: " + err.Error())
		return nil
	}
	if m.current() == s && m.focus == focusSidebar && m.dialog == nil && m.cfg == nil && m.closing == nil && m.confirm == nil {
		m.focus = focusTerminal
	}
	_ = saveSessions(m.sessions)
	return nil
}

// worktreeLikeSelected (W) starts a new worktree session with the selected
// session's agent and repo, right below it, as shift+enter does for a
// plain session.
func (m *model) worktreeLikeSelected() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	agentName, dir, code, separate := s.Agent, s.Dir, s.CodeRepo, s.separateRepoLocked()
	s.mu.Unlock()
	agent, ok := agentByName(agentName)
	if !ok {
		m.setFlash(fmt.Sprintf("Couldn't start: unknown agent %q (was it removed from the config?)", agentName))
		return nil
	}
	startDir, repo := dir, ""
	switch {
	case separate:
		repo = code
	case code != "":
		startDir = code // a worktree session: start from the folder it was made from
	}
	check := startDir
	if repo != "" {
		check = repo
	}
	if !isDir(check) || !inGitRepo(check) {
		m.setFlash(fmt.Sprintf("W needs a git repo, and %s isn't in one. Press n and pick a code repo.", check))
		return nil
	}
	return m.startWorktreeSession(agent, startDir, repo, m.sel+1)
}

// removeSession ends a session and removes it from the list.
func (m *model) removeSession(s *Session) {
	i := m.indexOf(s)
	if i < 0 {
		return
	}
	s.Kill()
	removeSessionFiles(s.ID)
	m.sessions = slices.Delete(m.sessions, i, i+1)
	if m.sel > i || (m.sel == i && m.sel >= len(m.sessions)) {
		m.sel--
	}
	if m.sel < 0 {
		m.sel = 0
	}
	m.keepSelectionVisible()
	_ = saveSessions(m.sessions)
}

// Closing -----------------------------------------------------------------------

// closePrompt is the warning x shows when a worktree session's work isn't
// merged, or has uncommitted files.
type closePrompt struct {
	s     *Session
	check closeCheck
}

type closeCheckedMsg struct {
	s     *Session
	check closeCheck
}

type closeDoneMsg struct {
	s    *Session
	done []string
	err  error
}

// closeWorktreeSession asks before closing a worktree session (x). Its
// agent is stopped, then the check runs in the background.
func (m *model) closeWorktreeSession(s *Session) {
	switch {
	case s.preparing.Load():
		m.setFlash("Still creating its worktree; wait for it to finish")
		return
	case s.closing.Load():
		m.setFlash("Already checking its worktree")
		return
	}
	s.mu.Lock()
	name, branch := s.Name, s.Branch
	s.mu.Unlock()
	text := fmt.Sprintf("Close %q? If %s is merged, its worktree and branch are removed; if not, you choose. y/n", name, branch)
	if s.Running() {
		text = fmt.Sprintf("End %q and close it? If %s is merged, its worktree and branch are removed; if not, you choose. y/n", name, branch)
	}
	m.confirm = &confirmPrompt{text: text, action: func(m *model) tea.Cmd { return m.startCloseCheck(s) }}
}

func (m *model) startCloseCheck(s *Session) tea.Cmd {
	s.closing.Store(true)
	s.Kill()
	s.mu.Lock()
	repo, wt, branch, base := s.RepoRoot, s.Worktree, s.Branch, s.BaseBranch
	s.mu.Unlock()
	m.setFlash("Checking whether " + branch + " is merged…")
	return func() tea.Msg {
		waitStopped(s, 10*time.Second) // a running agent holds files in the worktree
		return closeCheckedMsg{s: s, check: checkMerged(repo, wt, branch, base)}
	}
}

func waitStopped(s *Session, timeout time.Duration) {
	for end := time.Now().Add(timeout); s.Running() && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
	}
}

func (m *model) closeChecked(msg closeCheckedMsg) tea.Cmd {
	if m.indexOf(msg.s) < 0 {
		msg.s.closing.Store(false)
		return nil
	}
	if msg.check.safe() {
		m.setFlash("Merged (" + msg.check.how + "): removing the worktree and branch…")
		return m.removeWorktreeCmd(msg.s, false, true)
	}
	m.closing = &closePrompt{s: msg.s, check: msg.check}
	return nil
}

// closeKey handles a key while the close warning is open.
func (m *model) closeKey(key string) tea.Cmd {
	c := m.closing
	switch key {
	case "k":
		m.closing = nil
		c.s.closing.Store(false)
		c.s.mu.Lock()
		wt, branch := c.s.Worktree, c.s.Branch
		c.s.mu.Unlock()
		m.removeSession(c.s)
		m.setFlash(fmt.Sprintf("Removed the session. Kept the worktree %s and branch %s.", wt, branch))
	case "D":
		m.closing = nil
		m.setFlash("Deleting the worktree and branch…")
		return m.removeWorktreeCmd(c.s, true, false)
	case "esc", "n", "q":
		m.closing = nil
		c.s.closing.Store(false)
	}
	return nil
}

// removeWorktreeCmd removes a session's worktree and branch in the
// background. deleteRemote also deletes the branch on origin (only when
// the work is merged); force removes uncommitted changes ("delete anyway").
func (m *model) removeWorktreeCmd(s *Session, force, deleteRemote bool) tea.Cmd {
	s.mu.Lock()
	repo, wt, branch := s.RepoRoot, s.Worktree, s.Branch
	s.mu.Unlock()
	return func() tea.Msg {
		done, err := removeWorktree(repo, wt, branch, force, deleteRemote)
		return closeDoneMsg{s: s, done: done, err: err}
	}
}

func (m *model) closeDone(msg closeDoneMsg) {
	msg.s.closing.Store(false)
	if msg.err != nil && len(msg.done) == 0 {
		m.setFlash("Couldn't remove the worktree: " + firstLine(msg.err.Error()))
		return
	}
	m.removeSession(msg.s)
	text := "Closed: " + strings.Join(msg.done, ", ")
	if len(msg.done) == 0 {
		text = "Closed the session"
	}
	if msg.err != nil {
		text += ". But: " + firstLine(msg.err.Error())
	}
	m.setFlash(text)
}

// renderClose is the close warning, in the main pane.
func (m *model) renderClose(w, h int) []string {
	c := m.closing
	c.s.mu.Lock()
	name, branch, wt, base := c.s.Name, c.s.Branch, c.s.Worktree, c.s.BaseBranch
	c.s.mu.Unlock()
	ck := c.check
	warn := lipgloss.NewStyle().Foreground(colWaiting).Bold(true)
	lines := []string{fit(stTitle.Render(" Close "+name+"?"), w), ""}
	add := func(s string) { lines = append(lines, fit(s, w)) }
	switch {
	case ck.noBranch:
		add(warn.Render("   The branch " + branch + " no longer exists."))
	case ck.merged:
		add(warn.Render("   " + branch + " is merged (" + ck.how + "), but the worktree has uncommitted files."))
	default:
		add(warn.Render("   " + branch + " isn't merged into " + base + "."))
	}
	add(stMuted.Render("   Worktree  ") + wt)
	add(stMuted.Render("   PR        ") + ck.pr)
	if ck.fetchErr != "" {
		add(stMuted.Render("   Couldn't fetch, so this may be out of date: " + ck.fetchErr))
	}
	if ck.missing {
		add(stMuted.Render("   The worktree folder is already gone."))
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		add("")
		add(fmt.Sprintf("   %s (%d):", title, len(items)))
		const most = 8
		for i, it := range items {
			if i == most {
				add(stMuted.Render(fmt.Sprintf("     … %d more", len(items)-most)))
				break
			}
			add("     " + it)
		}
	}
	list("Uncommitted files", ck.dirty)
	list("Commits not on "+base, ck.commits)
	key := lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	add("")
	add("   " + key.Render("k") + "    keep the worktree and branch; remove only the session")
	add("   " + key.Render("D") + "    delete anyway: the worktree, its uncommitted files and the local branch")
	add(stMuted.Render("        (the branch on origin, if any, is kept)"))
	add("   " + key.Render("esc") + "  cancel")
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:h]
}

// renderPrep is a session's worktree log while it's being made, or after
// its setup failed: the last lines that fit.
func renderPrep(log []string, w, rows int) []string {
	out := []string{""}
	start := max(len(log)-(rows-1), 0)
	for _, l := range log[start:] {
		out = append(out, fit(stMuted.Render("  "+l), w))
	}
	return out
}
