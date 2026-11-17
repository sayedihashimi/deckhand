package main

import (
	"fmt"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

// Merging in the UI: m in the list, its confirmation, and the offer to
// close the session afterwards. The git and gh work is in merge.go.

type mergePlannedMsg struct {
	s    *Session
	plan mergePlan
	err  error
}

type mergeDoneMsg struct {
	s    *Session
	plan mergePlan
	err  error
}

// startMerge (m) checks the selected session's branch in the background,
// then mergePlanned asks before merging, or says why it can't.
func (m *model) startMerge() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	switch {
	case s.preparing.Load():
		m.setFlash("Still creating its worktree; wait for it to finish")
		return nil
	case s.closing.Load():
		m.setFlash("It's being closed")
		return nil
	}
	s.mu.Lock()
	dir, want, base, root, name := s.gitDirLocked(), s.Branch, s.BaseBranch, s.RepoRoot, s.Name
	if s.Worktree == "" {
		want, base, root = "", "", "" // a plain session: whatever branch is checked out
	}
	s.mu.Unlock()
	if !isDir(dir) || !inGitRepo(dir) {
		m.setFlash(fmt.Sprintf("m needs a git repo, and %s isn't in one", dir))
		return nil
	}
	if !s.mergeBusy.CompareAndSwap(false, true) {
		m.setFlash("Already merging " + name)
		return nil
	}
	m.setFlash("Checking whether " + name + " can be merged…")
	return func() tea.Msg {
		plan, err := planMerge(dir, want, base, root)
		return mergePlannedMsg{s: s, plan: plan, err: err}
	}
}

func (m *model) mergePlanned(msg mergePlannedMsg) tea.Cmd {
	s, p := msg.s, msg.plan
	if m.indexOf(s) < 0 {
		s.mergeBusy.Store(false)
		return nil
	}
	if msg.err != nil {
		s.mergeBusy.Store(false)
		m.setFlash("Can't merge: " + firstLine(msg.err.Error()))
		return nil
	}
	if p.merged != "" {
		s.mergeBusy.Store(false)
		m.offerClose(s, fmt.Sprintf("%s is already merged (%s).", p.branch, p.merged))
		return nil
	}
	m.confirm = &confirmPrompt{
		text:   mergeQuestion(p),
		cancel: func(*model) { s.mergeBusy.Store(false) },
		action: func(m *model) tea.Cmd {
			m.setFlash("Merging " + p.branch + "…")
			return func() tea.Msg {
				var err error
				if p.github {
					err = mergePR(p)
				} else {
					err = mergeLocal(p.repoRoot, p.branch, p.base)
				}
				return mergeDoneMsg{s: s, plan: p, err: err}
			}
		},
	}
	return nil
}

func (m *model) mergeDone(msg mergeDoneMsg) {
	s, p := msg.s, msg.plan
	s.mergeBusy.Store(false)
	if msg.err != nil {
		m.setFlash(upperFirst(firstLine(msg.err.Error())))
		return
	}
	what := fmt.Sprintf("Merged into %s in %s (not pushed).", p.base, filepath.Base(p.repoRoot))
	if p.github {
		what = fmt.Sprintf("Merged PR #%d.", p.pr.Number)
		s.mu.Lock()
		pr := p.pr
		pr.State = "MERGED"
		s.prs = prState{pr: &pr, branch: p.branch} // the badge, until the next poll
		s.mu.Unlock()
	}
	if m.indexOf(s) < 0 {
		m.setFlash(what)
		return
	}
	m.offerClose(s, what)
}

// offerClose asks, after a merge, whether to close a worktree session with
// x's close path: the merged check, then removing the worktree and the
// branch (on origin too). A plain session just says what happened.
func (m *model) offerClose(s *Session, what string) {
	m.setFlash(what)
	if !s.hasWorktree() {
		return
	}
	m.confirm = &confirmPrompt{
		text: what + " Close the session and remove the worktree? y/n",
		action: func(m *model) tea.Cmd {
			if m.indexOf(s) < 0 || s.closing.Load() || s.preparing.Load() {
				return nil
			}
			return m.startCloseCheck(s)
		},
	}
}
