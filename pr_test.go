package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestParsePRChecks(t *testing.T) {
	out := `{"number":12,"state":"OPEN","headRefOid":"abc","url":"https://github.com/example/api/pull/12","isDraft":false,
	"statusCheckRollup":[
		{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},
		{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SKIPPED"},
		{"__typename":"StatusContext","state":"SUCCESS"}]}`
	pr, err := parsePR(out)
	if err != nil || pr.Number != 12 || pr.URL == "" || len(pr.Checks) != 3 {
		t.Fatalf("parsed %+v, %v", pr, err)
	}
	if _, err := parsePR("not json"); err == nil {
		t.Error("bad JSON should be an error")
	}

	run := func(status, conclusion string) prCheck {
		return prCheck{Typename: "CheckRun", Status: status, Conclusion: conclusion}
	}
	status := func(state string) prCheck { return prCheck{Typename: "StatusContext", State: state} }
	for _, c := range []struct {
		name   string
		pr     prInfo
		label  string
		badge  string
		checks checksState
	}{
		{"passing", pr, "PR #12 open · checks passing", "#12✓", checksPassing},
		{"no checks", prInfo{Number: 3, State: "OPEN"}, "PR #3 open", "#3", checksNone},
		{"failing run", prInfo{Number: 4, State: "OPEN", Checks: []prCheck{run("COMPLETED", "SUCCESS"), run("COMPLETED", "FAILURE")}}, "PR #4 open · checks failing", "#4✗", checksFailing},
		{"timed out", prInfo{Number: 4, State: "OPEN", Checks: []prCheck{run("COMPLETED", "TIMED_OUT")}}, "PR #4 open · checks failing", "#4✗", checksFailing},
		{"failing status", prInfo{Number: 5, State: "OPEN", Checks: []prCheck{status("ERROR")}}, "PR #5 open · checks failing", "#5✗", checksFailing},
		{"running", prInfo{Number: 6, State: "OPEN", Checks: []prCheck{run("IN_PROGRESS", ""), run("COMPLETED", "SUCCESS")}}, "PR #6 open · checks running", "#6…", checksPending},
		{"pending status", prInfo{Number: 6, State: "OPEN", Checks: []prCheck{status("PENDING")}}, "PR #6 open · checks running", "#6…", checksPending},
		{"failing beats running", prInfo{Number: 6, State: "OPEN", Checks: []prCheck{run("QUEUED", ""), run("COMPLETED", "FAILURE")}}, "PR #6 open · checks failing", "#6✗", checksFailing},
		{"draft", prInfo{Number: 7, State: "OPEN", IsDraft: true, Checks: []prCheck{run("COMPLETED", "SUCCESS")}}, "PR #7 draft · checks passing", "#7 draft✓", checksPassing},
		{"merged", prInfo{Number: 8, State: "MERGED", Checks: []prCheck{run("COMPLETED", "FAILURE")}}, "PR #8 merged", "#8 merged", checksFailing},
		{"closed", prInfo{Number: 9, State: "CLOSED"}, "PR #9 closed", "#9 closed", checksNone},
	} {
		if got := c.pr.checks(); got != c.checks {
			t.Errorf("%s: checks %v, want %v", c.name, got, c.checks)
		}
		if got := prLabel(c.pr); got != c.label {
			t.Errorf("%s: label %q, want %q", c.name, got, c.label)
		}
		n, mark := prBadge(c.pr)
		if n+mark != c.badge {
			t.Errorf("%s: badge %q, want %q", c.name, n+mark, c.badge)
		}
		if got := ansi.Strip(renderPRBadge(c.pr)); got != c.badge {
			t.Errorf("%s: rendered badge %q", c.name, got)
		}
		if got := ansi.Strip(renderPRLabel(c.pr)); got != c.label {
			t.Errorf("%s: rendered label %q", c.name, got)
		}
	}
	if prNumberFromURL("https://github.com/example/api/pull/42") != 42 || prNumberFromURL("") != 0 {
		t.Error("prNumberFromURL")
	}
}

func TestGitHubRemote(t *testing.T) {
	for _, c := range []struct {
		url, host string
		github    bool
	}{
		{"https://github.com/example/api.git", "github.com", true},
		{"https://user@GitHub.com/example/api", "github.com", true},
		{"git@github.com:example/api.git", "github.com", true},
		{"ssh://git@ssh.github.com:443/example/api.git", "ssh.github.com", true},
		{"https://acme.ghe.com/team/api.git", "acme.ghe.com", true},
		{"https://git.example.com/team/api.git", "git.example.com", false},
		{"git@gitlab.com:team/api.git", "gitlab.com", false},
		{`C:\src\origin.git`, "", false},
		{"C:/src/origin.git", "", false},
		{"../origin.git", "", false},
		{"file:///C:/src/origin.git", "", false},
	} {
		h := remoteHost(c.url)
		if h != c.host || isGitHubHost(h, "") != c.github {
			t.Errorf("%s: host %q (GitHub %v), want %q (%v)", c.url, h, isGitHubHost(h, ""), c.host, c.github)
		}
	}
	if !isGitHubHost("git.example.com", "git.example.com") {
		t.Error("GH_HOST should count as GitHub")
	}
}

// githubClone is testRepo's clone with origin looking like GitHub: its URL
// is on github.com, and insteadOf sends fetches and pushes to the local
// bare repo, so nothing reaches GitHub.
func githubClone(t *testing.T) (clone, origin string) {
	t.Helper()
	clone = testRepo(t)
	origin = filepath.Join(filepath.Dir(clone), "origin.git")
	const u = "https://github.com/example/api.git"
	git(t, clone, "config", "remote.origin.url", u)
	git(t, clone, "config", "url."+filepath.ToSlash(origin)+".insteadOf", u)
	return clone, origin
}

// ghFake stands in for gh: auth status, pr view (--json and --web), pr
// create and pr merge, with one PR at most.
type ghFake struct {
	calls    []string
	pr       *prInfo
	noAuth   bool
	mergeErr error // pr merge fails with it
}

func (f *ghFake) run(dir string, args ...string) (string, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.HasPrefix(call, "auth status"):
		if f.noAuth {
			return "", errors.New("gh auth: You are not logged into any GitHub hosts. To log in, run: gh auth login")
		}
		return "github.com\n  ✓ Logged in", nil
	case strings.HasPrefix(call, "pr view") && slices.Contains(args, "--web"):
		return "", nil
	case strings.HasPrefix(call, "pr view"):
		if f.pr == nil {
			return noPR(dir, args...)
		}
		b, _ := json.Marshal(f.pr)
		return string(b), nil
	case strings.HasPrefix(call, "pr create"):
		f.pr = &prInfo{Number: 1, State: "OPEN", URL: "https://github.com/example/api/pull/1",
			Checks: []prCheck{{Typename: "CheckRun", Status: "QUEUED"}}}
		return "Creating pull request\n" + f.pr.URL, nil
	case strings.HasPrefix(call, "pr merge"):
		if f.mergeErr != nil {
			return "", f.mergeErr
		}
		if f.pr == nil {
			return "", errors.New("gh pr: no pull requests found")
		}
		f.pr.State = "MERGED"
		return "", nil
	}
	return "", errors.New("gh: unexpected call " + call)
}

func (f *ghFake) called(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestPlanPRRefuses(t *testing.T) {
	clone, _ := githubClone(t)
	gh := &ghFake{}
	fakeGH(t, gh.run)
	worktreeSettings = worktreeConfig{}

	// The default branch is never pushed, and gh isn't even asked.
	if _, err := planPR(clone, "", ""); err == nil || !strings.Contains(err.Error(), "main is the default branch") {
		t.Errorf("on main: %v", err)
	}
	if len(gh.calls) != 0 {
		t.Errorf("gh called for the default branch: %q", gh.calls)
	}
	git(t, clone, "switch", "-q", "-c", "feature")
	if _, err := planPR(clone, "other", "main"); err == nil || !strings.Contains(err.Error(), "not the session's branch other") {
		t.Errorf("a worktree on another branch: %v", err)
	}
	git(t, clone, "switch", "-q", "--detach")
	if _, err := planPR(clone, "", ""); err == nil || !strings.Contains(err.Error(), "detached") {
		t.Errorf("detached: %v", err)
	}
	git(t, clone, "switch", "-q", "feature")

	// gh signed out, then missing.
	gh.noAuth = true
	if _, err := planPR(clone, "", ""); err == nil || !strings.Contains(err.Error(), "gh isn't signed in to github.com: run gh auth login") {
		t.Errorf("signed out: %v", err)
	}
	fakeGH(t, func(string, ...string) (string, error) { return "", errNoGH })
	if _, err := planPR(clone, "", ""); err == nil || !strings.Contains(err.Error(), "needs the GitHub CLI (gh)") {
		t.Errorf("no gh: %v", err)
	}

	// Not on GitHub, then no origin at all.
	fakeGH(t, gh.run)
	gh.noAuth, gh.calls = false, nil
	git(t, clone, "config", "remote.origin.url", "https://git.example.com/team/api.git")
	if _, err := planPR(clone, "", ""); err == nil || !strings.Contains(err.Error(), "isn't on GitHub") {
		t.Errorf("not GitHub: %v", err)
	}
	git(t, clone, "remote", "remove", "origin")
	if _, err := planPR(clone, "", ""); err == nil || !strings.Contains(err.Error(), "no origin remote") {
		t.Errorf("no origin: %v", err)
	}
	if len(gh.calls) != 0 {
		t.Errorf("gh called without a GitHub remote: %q", gh.calls)
	}
}

func TestPlanPRAndPush(t *testing.T) {
	clone, origin := githubClone(t)
	gh := &ghFake{}
	fakeGH(t, gh.run)
	worktreeSettings = worktreeConfig{}
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	// Nothing committed: nothing to push.
	p, err := planPR(w.path, w.branch, "main")
	if err != nil {
		t.Fatal(err)
	}
	if q, none := prQuestion(p); !none || q != "" || p.hasPR {
		t.Errorf("nothing to push: %q %v %+v", q, none, p)
	}

	commitFile(t, w.path, "feature.txt", "new")
	commitFile(t, w.path, "more.txt", "more")
	_ = os.WriteFile(filepath.Join(w.path, "notes.txt"), []byte("x"), 0o644)
	p, err = planPR(w.path, w.branch, "main")
	if err != nil {
		t.Fatal(err)
	}
	q, none := prQuestion(p)
	want := `Push ` + w.branch + ` to origin and open a PR into main, "Change feature.txt"? (1 uncommitted file isn't included) y/n`
	if none || q != want || p.ahead != 2 || !p.push || p.dirty != 1 {
		t.Errorf("question %q (want %q), plan %+v", q, want, p)
	}

	// y: push, create, open.
	gh.calls = nil
	done := pushAndOpen(&Session{}, p)
	if done.err != nil || !done.created || done.pr.Number != 1 {
		t.Fatalf("pushAndOpen: %+v", done)
	}
	tip := git(t, w.path, "rev-parse", "HEAD")
	if got := git(t, origin, "rev-parse", "refs/heads/"+w.branch); got != tip {
		t.Errorf("origin has %s at %s, want %s", w.branch, got, tip)
	}
	if up := git(t, w.path, "rev-parse", "--abbrev-ref", "@{upstream}"); up != "origin/"+w.branch {
		t.Errorf("upstream %s", up)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/main"); got == tip {
		t.Error("main must not be pushed")
	}
	if gh.called("pr create --fill-first --base main --head "+w.branch) != 1 || gh.called("pr view https://github.com/example/api/pull/1 --web") != 1 {
		t.Errorf("gh calls %q", gh.calls)
	}

	// p again: the PR is there and has everything, so it's just opened.
	p, err = planPR(w.path, w.branch, "main")
	if err != nil || !p.hasPR || p.push {
		t.Fatalf("after the push: %+v, %v", p, err)
	}
	if q, none := prQuestion(p); q != "" || none {
		t.Errorf("an up-to-date PR is opened without asking: %q", q)
	}

	// A new commit: push it, then open the PR.
	commitFile(t, w.path, "third.txt", "3")
	p, _ = planPR(w.path, w.branch, "main")
	if q, _ := prQuestion(p); q != "Push "+w.branch+" to origin, then open PR #1? (1 uncommitted file isn't included) y/n" {
		t.Errorf("new commit: %q", q)
	}

	// origin's branch moved on: the push is rejected and not forced.
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", origin, other)
	git(t, other, "switch", "-q", w.branch)
	commitFile(t, other, "theirs.txt", "theirs")
	git(t, other, "push", "-q", "origin", w.branch)
	theirs := git(t, origin, "rev-parse", "refs/heads/"+w.branch)
	done = pushAndOpen(&Session{}, p)
	if done.err == nil || !strings.Contains(done.err.Error(), "push rejected") || !strings.Contains(done.err.Error(), "[rejected]") ||
		strings.Contains(done.err.Error(), "hint:") {
		t.Errorf("rejected push: %v", done.err)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/"+w.branch); got != theirs {
		t.Error("a rejected push must leave origin alone")
	}
}

func TestPollPR(t *testing.T) {
	clone, _ := githubClone(t)
	gh := &ghFake{}
	fakeGH(t, gh.run)
	worktreeSettings = worktreeConfig{}

	// The default branch: no gh call, and a long wait.
	if r := pollPR(clone, "main", ""); r.pr != nil || r.retry != prPollOff || len(gh.calls) != 0 {
		t.Errorf("default branch: %+v %q", r, gh.calls)
	}
	git(t, clone, "switch", "-q", "-c", "feature")
	if r := pollPR(clone, "feature", ""); r.pr != nil || r.keep || r.retry != prPollEvery {
		t.Errorf("no PR: %+v", r)
	}
	gh.pr = &prInfo{Number: 3, State: "OPEN"}
	if r := pollPR(clone, "feature", ""); r.pr == nil || r.pr.Number != 3 {
		t.Errorf("a PR: %+v", r)
	}
	fakeGH(t, func(string, ...string) (string, error) { return "", errors.New("gh: HTTP 502") })
	if r := pollPR(clone, "feature", ""); !r.keep {
		t.Errorf("a failing gh keeps the badge: %+v", r)
	}
	fakeGH(t, func(string, ...string) (string, error) { return "", errNoGH })
	if r := pollPR(clone, "feature", ""); r.retry != prPollOff {
		t.Errorf("no gh: %+v", r)
	}
	// Not on GitHub: gh isn't run.
	fakeGH(t, func(string, ...string) (string, error) { t.Error("gh run without a GitHub remote"); return "", nil })
	git(t, clone, "config", "remote.origin.url", "https://git.example.com/api.git")
	if r := pollPR(clone, "feature", ""); r.retry != prPollOff {
		t.Errorf("not GitHub: %+v", r)
	}
}

func TestSessionPRBadge(t *testing.T) {
	s := &Session{Name: "quiet-otter", Agent: "claude", Dir: `C:\notes`, CodeRepo: `C:\src\cli`, RepoRoot: `C:\src\cli`,
		Worktree: `C:\src\cli-wt\quiet-otter`, Branch: "quiet-otter"}
	s.info.git = gitInfo{ok: true, branch: "quiet-otter", ahead: 1}
	s.info.model, s.info.ctxTokens = "claude-opus-5-5", 64100
	s.setPR("", prPoll{pr: &prInfo{Number: 12}}, time.Now()) // for another branch: ignored
	if s.Snapshot().pr != nil {
		t.Fatal("a result for another branch shouldn't show")
	}
	s.prs.branch = "quiet-otter"
	s.setPR("quiet-otter", prPoll{pr: &prInfo{Number: 12, State: "OPEN", Checks: []prCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}}}}, time.Now())
	sn := s.Snapshot()
	if sn.pr == nil {
		t.Fatal("no PR in the snapshot")
	}
	// 120 columns: the model goes first, then the context, so the branch
	// and the badge fit.
	if got := ansi.Strip(infoLine(s, sn, 24)); got != "⎇ cli:quiet-otter↑1 #12✓" {
		t.Errorf("info line at 24: %q", got)
	}
	if got := ansi.Strip(infoLine(s, sn, 30)); got != "⎇ cli:quiet-otter↑1 #12✓ · 64k" {
		t.Errorf("info line at 30: %q", got)
	}
	if got := ansi.Strip(infoLine(s, sn, 50)); got != "⎇ cli:quiet-otter↑1 #12✓ · opus 5.5 · 64k" {
		t.Errorf("wide info line: %q", got)
	}
	if got := ansi.Strip(infoLine(s, sn, 14)); ansi.StringWidth(got) > 14 || !strings.HasSuffix(got, "#12✓") {
		t.Errorf("narrow info line keeps the badge: %q", got)
	}
	// A plain session on a branch: the badge follows the branch it's on.
	p := &Session{Name: "api", Agent: "claude", Dir: `C:\src\api`}
	p.info.git = gitInfo{ok: true, branch: "feature/login", dirty: true}
	p.info.model, p.info.ctxTokens = "claude-opus-5-5", 84200
	p.prs = prState{branch: "feature/login", pr: &prInfo{Number: 7, State: "OPEN", Checks: []prCheck{{Typename: "StatusContext", State: "FAILURE"}}}}
	if got := ansi.Strip(infoLine(p, p.Snapshot(), 24)); got != "feature/login* #7✗ · 84k" {
		t.Errorf("plain session: %q", got)
	}
	p.info.git.branch = "main" // switched: the old PR doesn't show
	if p.Snapshot().pr != nil {
		t.Error("the PR of a branch it's no longer on shouldn't show")
	}
}

func TestPKey(t *testing.T) {
	clone, origin := githubClone(t)
	gh := &ghFake{}
	fakeGH(t, gh.run)
	worktreeSettings = worktreeConfig{}
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	m := testModel("plain", "otter")
	m.width, m.height = 120, 32
	m.sessions[0].Dir = clone // on main
	s := m.sessions[1]
	s.Dir, s.CodeRepo, s.RepoRoot, s.Worktree, s.Branch, s.BaseBranch = w.path, clone, w.repoRoot, w.path, w.branch, "main"
	press := func() tea.Cmd {
		_, cmd := m.handleKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
		return cmd
	}

	// The plain session is on main: refused.
	runCmd(m, press())
	if !strings.Contains(m.flash, "main is the default branch") || m.confirm != nil || m.sessions[0].prBusy.Load() {
		t.Errorf("p on main: %q", m.flash)
	}

	m.sel = 1
	runCmd(m, press())
	if !strings.Contains(m.flash, "Nothing to push") || s.prBusy.Load() {
		t.Errorf("nothing to push: %q", m.flash)
	}

	commitFile(t, w.path, "feature.txt", "new")
	runCmd(m, press())
	if m.confirm == nil || !strings.Contains(m.confirm.text, `Push `+w.branch+` to origin and open a PR into main, "Change feature.txt"?`) {
		t.Fatalf("confirmation: %+v", m.confirm)
	}
	// n cancels, and p works again afterwards.
	m.handleKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if s.prBusy.Load() || refExists(clone, "refs/remotes/origin/"+w.branch) || gh.called("pr create") != 0 {
		t.Error("n should cancel without pushing")
	}
	runCmd(m, press())
	_, cmd := m.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(m, cmd)
	if !strings.Contains(m.flash, "Created PR #1") || s.prBusy.Load() {
		t.Errorf("after y: %q", m.flash)
	}
	if _, err := gitRun(origin, "rev-parse", "--verify", "refs/heads/"+w.branch); err != nil {
		t.Error("the branch wasn't pushed")
	}
	if sn := s.Snapshot(); sn.pr == nil || sn.pr.Number != 1 {
		t.Errorf("the badge should show straight away: %+v", sn.pr)
	}

	// p again opens it.
	runCmd(m, press())
	if !strings.Contains(m.flash, "Opened PR #1 (open)") || m.confirm != nil || gh.called("pr view https://github.com/example/api/pull/1 --web") != 2 {
		t.Errorf("p with a PR: %q %q", m.flash, gh.calls)
	}
}
