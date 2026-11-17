package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Pull requests in the UI: p in the list, its confirmation, and the PR
// badge's background polling. The git and gh work is in pr.go.

// prState is a session's PR as last polled (guarded by Session.mu).
type prState struct {
	pr     *prInfo   // nil: none, or not known yet
	branch string    // the branch it's for
	next   time.Time // when the next poll is due
}

// prBranchLocked is the branch the PR badge is for: a worktree session's
// branch, else the branch its git dir is on, or "" for none.
func (s *Session) prBranchLocked() string {
	if s.Worktree != "" {
		return s.Branch
	}
	if g := s.info.git; g.ok && g.branch != "detached" {
		return g.branch
	}
	return ""
}

// MaybePollPR looks up the session's PR in the background when it's due:
// every minute, and only for sessions with a branch. A poll that finds gh
// missing, no GitHub remote or the default branch waits longer.
func (s *Session) MaybePollPR(now time.Time) {
	if s.preparing.Load() || s.closing.Load() {
		return
	}
	s.mu.Lock()
	branch := s.prBranchLocked()
	if branch != s.prs.branch {
		s.prs = prState{branch: branch} // switched branches: poll now
	}
	due := branch != "" && !now.Before(s.prs.next)
	if due {
		s.prs.next = now.Add(prPollEvery)
	}
	dir, base := s.gitDirLocked(), s.BaseBranch
	s.mu.Unlock()
	if !due || !s.prPolling.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.prPolling.Store(false)
		res := pollPR(dir, branch, base)
		s.setPR(branch, res, now)
	}()
}

// setPR records a poll's result, unless the branch changed meanwhile.
func (s *Session) setPR(branch string, res prPoll, now time.Time) {
	s.mu.Lock()
	if s.prs.branch == branch {
		if !res.keep {
			s.prs.pr = res.pr
		}
		s.prs.next = now.Add(res.retry)
	}
	dirty := s.dirty
	s.mu.Unlock()
	if dirty != nil {
		dirty.Store(true)
	}
}

// p ------------------------------------------------------------------------

type prPlannedMsg struct {
	s    *Session
	plan prPlan
	err  error
}

type prDoneMsg struct {
	s       *Session
	branch  string
	pr      prInfo
	created bool
	pushed  bool
	err     error
}

// startPR (p) checks the selected session's branch in the background:
// the default branch is refused, and gh and the GitHub remote have to be
// there. Then prPlanned asks before pushing, or opens the existing PR.
func (m *model) startPR() tea.Cmd {
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
	dir, want, base, name := s.gitDirLocked(), s.Branch, s.BaseBranch, s.Name
	if s.Worktree == "" {
		want, base = "", "" // a plain session: whatever branch is checked out
	}
	s.mu.Unlock()
	if !isDir(dir) || !inGitRepo(dir) {
		m.setFlash(fmt.Sprintf("p needs a git repo, and %s isn't in one", dir))
		return nil
	}
	if !s.prBusy.CompareAndSwap(false, true) {
		m.setFlash("Already working on " + name + "'s PR")
		return nil
	}
	m.setFlash("Checking " + name + "'s branch…")
	return func() tea.Msg {
		plan, err := planPR(dir, want, base)
		return prPlannedMsg{s: s, plan: plan, err: err}
	}
}

func (m *model) prPlanned(msg prPlannedMsg) tea.Cmd {
	s, p := msg.s, msg.plan
	if m.indexOf(s) < 0 {
		s.prBusy.Store(false)
		return nil
	}
	if msg.err != nil {
		s.prBusy.Store(false)
		m.setFlash("Can't open a PR: " + firstLine(msg.err.Error()))
		return nil
	}
	q, none := prQuestion(p)
	switch {
	case none:
		s.prBusy.Store(false)
		m.setFlash(fmt.Sprintf("Nothing to push: %s has no commits that aren't on %s", p.branch, p.base))
		return nil
	case q == "":
		m.setFlash(fmt.Sprintf("Opening PR #%d…", p.pr.Number))
		return func() tea.Msg {
			return prDoneMsg{s: s, branch: p.branch, pr: p.pr, err: openPR(p.dir, p.pr)}
		}
	}
	m.confirm = &confirmPrompt{
		text:   q,
		cancel: func(*model) { s.prBusy.Store(false) },
		action: func(m *model) tea.Cmd {
			m.setFlash("Pushing " + p.branch + "…")
			return func() tea.Msg { return pushAndOpen(s, p) }
		},
	}
	return nil
}

// pushAndOpen pushes the branch, creates its PR if it has none, and opens
// the PR in the browser.
func pushAndOpen(s *Session, p prPlan) prDoneMsg {
	if err := pushBranch(p.dir, p.branch); err != nil {
		return prDoneMsg{s: s, branch: p.branch, err: err}
	}
	pr, created := p.pr, false
	if !p.hasPR {
		var err error
		if pr, err = createPR(p.dir, p.branch, p.base); err != nil {
			return prDoneMsg{s: s, branch: p.branch, pushed: true, err: err}
		}
		created = true
	}
	return prDoneMsg{s: s, branch: p.branch, pr: pr, created: created, pushed: true, err: openPR(p.dir, pr)}
}

func (m *model) prDone(msg prDoneMsg) {
	s := msg.s
	s.prBusy.Store(false)
	if msg.pr.Number != 0 {
		s.mu.Lock()
		pr := msg.pr
		s.prs = prState{pr: &pr, branch: msg.branch} // and poll again now, for the checks
		s.mu.Unlock()
	}
	what := fmt.Sprintf("PR #%d", msg.pr.Number)
	switch {
	case msg.err != nil && msg.pr.Number != 0:
		m.setFlash(fmt.Sprintf("%s: %s, but couldn't open it: %s", what, msg.pr.URL, firstLine(msg.err.Error())))
	case msg.err != nil && msg.pushed:
		m.setFlash("Pushed, but " + firstLine(msg.err.Error()))
	case msg.err != nil:
		m.setFlash(upperFirst(firstLine(msg.err.Error())))
	case msg.created:
		m.setFlash("Created " + what + " and opened it in the browser: " + msg.pr.URL)
	case msg.pushed:
		m.setFlash("Pushed, and opened " + what + " in the browser")
	default:
		m.setFlash("Opened " + what + " (" + msg.pr.prState() + ") in the browser")
	}
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Showing -------------------------------------------------------------------

var (
	stPROpen   = lipgloss.NewStyle().Foreground(colIdle)
	stPRMerged = stBranch
	stPRClosed = lipgloss.NewStyle().Foreground(colError)
)

// renderPRLabel is the PR's state in words, for the session header:
// "PR #12 open · checks passing", the PR in its state's colour.
func renderPRLabel(p prInfo) string {
	head := fmt.Sprintf("PR #%d %s", p.Number, p.prState())
	return prStyle(p).Render(head) + stMuted.Render(strings.TrimPrefix(prLabel(p), head))
}

// prStyle is the colour of a PR's state: green while open, purple when
// merged, red when closed, muted for a draft.
func prStyle(p prInfo) lipgloss.Style {
	switch p.prState() {
	case "draft":
		return stMuted
	case "merged":
		return stPRMerged
	case "closed":
		return stPRClosed
	}
	return stPROpen
}

// renderPRBadge is the sidebar's PR badge: "#12" in the state's colour,
// then the checks mark in theirs.
func renderPRBadge(p prInfo) string {
	number, mark := prBadge(p)
	st := prStyle(p)
	if p.prState() == "merged" || p.prState() == "closed" {
		return st.Render(number + mark)
	}
	ms := stMuted
	switch p.checks() {
	case checksPassing:
		ms = stPROpen
	case checksFailing:
		ms = stPRClosed
	case checksPending:
		ms = lipgloss.NewStyle().Foreground(colWorking)
	}
	if rest, ok := strings.CutPrefix(mark, " draft"); ok {
		return st.Render(number+" draft") + ms.Render(rest)
	}
	return st.Render(number) + ms.Render(mark)
}
