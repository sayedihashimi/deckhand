package main

import (
	"fmt"
	"regexp"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
)

// Forking (f): a new session right below the selected Claude session that
// starts from a copy of its conversation, so you can try another approach
// without losing the original. Claude does the copying: the fork runs
// claude --resume <original> --fork-session --session-id <new> (see
// buildLaunch), and a PowerShell wrapper agent runs its resume command with
// the original's ID while the claude shim adds the other two.

// forkMark starts a new fork's second sidebar line: "⑂ <original name>".
const forkMark = "⑂"

// forkSuffix matches " (fork)" or " (fork 2)" at the end of a name, so a
// fork of a fork is "api (fork 2)" rather than "api (fork) (fork)".
var forkSuffix = regexp.MustCompile(` \(fork(?: \d+)?\)$`)

// forkName is "<name> (fork)", or "(fork 2)" and so on if that's taken.
func forkName(name string, taken func(string) bool) string {
	base := forkSuffix.ReplaceAllString(name, "")
	n := base + " (fork)"
	for i := 2; taken(n); i++ {
		n = fmt.Sprintf("%s (fork %d)", base, i)
	}
	return n
}

// forkCheck says why a session can't be forked, or "" when it can. The
// original has to be a Claude session that has started at least once (so
// there's a conversation to copy), and not in the middle of a turn: Claude
// copies the transcript as it stands, and half a turn (a tool call waiting
// for its result) isn't a good place to branch from.
func forkCheck(a Agent, s *Session) string {
	s.mu.Lock()
	started, claudeID, name := s.Started, s.ClaudeSessionID, s.Name
	running, st := s.running, s.status
	s.mu.Unlock()
	switch {
	case !a.Claude:
		return "Fork needs a Claude agent"
	case !a.CanResume():
		return fmt.Sprintf("Fork needs a resume command for %s (press c to add one)", a.Name)
	case s.preparing.Load():
		return "Still creating its worktree; wait for it to finish"
	case !started || claudeID == "":
		return fmt.Sprintf("Nothing to fork yet: start %s first", name)
	case running && (st == StatusWorking || st == StatusWaiting):
		return "Wait for it to finish its turn"
	}
	return ""
}

// forkSelected (f) adds a fork of the selected session right below it,
// selects it and starts it. The original is left alone.
func (m *model) forkSelected() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	a, ok := s.agent()
	if !ok {
		m.setFlash(fmt.Sprintf("Couldn't fork: unknown agent %q (was it removed from the config?)", s.Agent))
		return nil
	}
	if why := forkCheck(a, s); why != "" {
		m.setFlash(why)
		return nil
	}
	n := m.newFork(a, s)
	w, h := m.termSize()
	if err := startSession(n, w, h, false, m.dirty); err != nil {
		m.setFlash("Couldn't fork: " + err.Error())
		return nil
	}
	m.sel = m.indexOf(s) + 1
	m.sessions = slices.Insert(m.sessions, m.sel, n)
	m.clearFilter() // so the fork shows
	m.focus = focusTerminal
	_ = saveSessions(m.sessions)
	return nil
}

// newFork makes the fork of s: the same agent and folder, a new Claude
// session ID, and the original's conversation to copy on its first start.
func (m *model) newFork(a Agent, s *Session) *Session {
	// Names are only changed on this goroutine (r), so they're read as the
	// sidebar reads them.
	name := forkName(s.Name, func(n string) bool {
		return slices.ContainsFunc(m.sessions, func(o *Session) bool { return o.Name == n })
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	n := newSession(a, s.Dir, name)
	n.ClaudeSessionID = uuid.NewString()
	n.ForkedFrom = s.ID
	n.forkOf = s.ClaudeSessionID
	n.forkHint = s.Name
	n.override = s.override // a kept test of an agent that may not be saved
	// The fork shares the original's folder. A worktree session's worktree
	// becomes the fork's code repo, so the fork shows its branch but doesn't
	// own it: x on the fork doesn't offer to remove the worktree.
	n.CodeRepo, n.RepoRoot = s.CodeRepo, s.RepoRoot
	if s.Worktree != "" {
		n.CodeRepo = s.Worktree
	}
	return n
}
