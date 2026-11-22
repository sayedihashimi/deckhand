package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestGitHubRepo(t *testing.T) {
	for u, want := range map[string]string{
		"https://github.com/example/api.git":           "github.com/example/api",
		"https://github.com/example/api":               "github.com/example/api",
		"git@github.com:example/api.git":               "github.com/example/api",
		"ssh://git@ssh.github.com:443/example/api.git": "github.com/example/api",
		"https://acme.ghe.com/team/api.git":            "acme.ghe.com/team/api",
		"https://github.com/example":                   "",
		"https://github.com/example/api/extra":         "",
		`C:\src\origin.git`:                            "",
		"file:///C:/src/origin.git":                    "",
	} {
		if got := githubRepo(u); got != want {
			t.Errorf("%s: %q, want %q", u, got, want)
		}
	}
}

func TestMergeBlocker(t *testing.T) {
	for _, c := range []struct {
		mergeable, state, want string
	}{
		{"MERGEABLE", "CLEAN", ""},
		{"MERGEABLE", "HAS_HOOKS", ""},
		{"MERGEABLE", "UNSTABLE", ""},
		{"CONFLICTING", "DIRTY", "conflicts with main"},
		{"MERGEABLE", "DIRTY", "conflicts with main"},
		{"MERGEABLE", "BLOCKED", "blocked by the branch's rules"},
		{"MERGEABLE", "BEHIND", "behind main"},
		{"UNKNOWN", "UNKNOWN", "try again in a moment"},
		{"", "", "try again in a moment"},
		{"MERGEABLE", "", "try again in a moment"},
	} {
		got := mergeBlocker(prInfo{BaseRefName: "main", Mergeable: c.mergeable, MergeStateStatus: c.state})
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s/%s: %q, want %q", c.mergeable, c.state, got, c.want)
		}
	}
}

// readyPR is an open PR GitHub would merge, at head.
func readyPR(head string) *prInfo {
	return &prInfo{Number: 12, State: "OPEN", Title: "Retry failed uploads", BaseRefName: "main", HeadRefOid: head,
		URL: "https://github.com/example/api/pull/12", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: []prCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}}}
}

// pushedWorktree is a worktree on a GitHub-looking clone with one commit,
// pushed to origin.
func pushedWorktree(t *testing.T) (w newWorktree, origin, tip string) {
	t.Helper()
	clone, origin := githubClone(t)
	worktreeSettings = worktreeConfig{}
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, w.path, "feature.txt", "new")
	git(t, w.path, "push", "-q", "origin", w.branch)
	return w, origin, git(t, w.path, "rev-parse", "HEAD")
}

func TestPlanMergeGitHub(t *testing.T) {
	w, _, tip := pushedWorktree(t)
	gh := &ghFake{}
	fakeGH(t, gh.run)
	plan := func() (mergePlan, error) { return planMerge(w.path, w.branch, "main", w.repoRoot) }
	refuses := func(name, want string, edit func(pr *prInfo)) {
		t.Helper()
		gh.pr = readyPR(tip)
		if edit != nil {
			edit(gh.pr)
		}
		if _, err := plan(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}

	// No PR: suggest p.
	if _, err := plan(); err == nil || !strings.Contains(err.Error(), "has no PR yet: press p") {
		t.Errorf("no PR: %v", err)
	}
	refuses("draft", "PR #12 is a draft", func(pr *prInfo) { pr.IsDraft = true })
	refuses("closed", "PR #12 is closed", func(pr *prInfo) { pr.State = "CLOSED" })
	refuses("failing", "PR #12's checks are failing", func(pr *prInfo) { pr.Checks[0].Conclusion = "FAILURE" })
	refuses("pending", "PR #12's checks are still running", func(pr *prInfo) { pr.Checks[0] = prCheck{Typename: "CheckRun", Status: "IN_PROGRESS"} })
	refuses("conflicting", "GitHub can't merge PR #12: it has conflicts with main", func(pr *prInfo) { pr.Mergeable, pr.MergeStateStatus = "CONFLICTING", "DIRTY" })
	refuses("blocked", "GitHub can't merge PR #12: it's blocked", func(pr *prInfo) { pr.MergeStateStatus = "BLOCKED" })
	refuses("unknown", "try again in a moment", func(pr *prInfo) { pr.Mergeable = "UNKNOWN" })
	refuses("PR head behind", "aren't in PR #12: press p to push them first", func(pr *prInfo) { pr.HeadRefOid = "0123456789" })

	// Already merged: no question, straight to the close offer.
	gh.pr = readyPR(tip)
	gh.pr.State = "MERGED"
	if p, err := plan(); err != nil || p.merged != "PR #12 merged" {
		t.Errorf("merged: %+v, %v", p, err)
	}

	// Ready: the question shows the number, title and base.
	gh.pr = readyPR(tip)
	p, err := plan()
	if err != nil || !p.github || p.repo != "github.com/example/api" {
		t.Fatalf("ready: %+v, %v", p, err)
	}
	if q := mergeQuestion(p); q != `Squash-merge PR #12 "Retry failed uploads" into main on GitHub and delete its branch? y/n` {
		t.Errorf("question %q", q)
	}
	gh.calls = nil
	if err := mergePR(p); err != nil {
		t.Fatal(err)
	}
	want := "pr merge 12 --squash --delete-branch --match-head-commit " + tip + " -R github.com/example/api"
	if len(gh.calls) != 1 || gh.calls[0] != want {
		t.Errorf("gh calls %q, want %q", gh.calls, want)
	}
	for _, c := range gh.calls {
		if strings.Contains(c, "--admin") {
			t.Error("never --admin")
		}
	}
	gh.mergeErr = errors.New("gh pr: Pull request is not mergeable: the base branch policy prohibits the merge")
	if err := mergePR(p); err == nil || !strings.Contains(err.Error(), "GitHub didn't merge PR #12: Pull request is not mergeable") {
		t.Errorf("merge error: %v", err)
	}

	// A local commit origin doesn't have.
	commitFile(t, w.path, "more.txt", "more")
	refuses("unpushed", "has commits that aren't in PR #12: press p to push them first", nil)

	// Signed out: say so, don't merge locally instead.
	gh.noAuth = true
	if _, err := plan(); err == nil || !strings.Contains(err.Error(), "gh isn't signed in") {
		t.Errorf("signed out: %v", err)
	}
}

func TestPlanMergeRefusesDefaultBranch(t *testing.T) {
	clone, _ := githubClone(t)
	gh := &ghFake{}
	fakeGH(t, gh.run)
	worktreeSettings = worktreeConfig{}
	if _, err := planMerge(clone, "", "", ""); err == nil || !strings.Contains(err.Error(), "main is the default branch") {
		t.Errorf("on main: %v", err)
	}
	if len(gh.calls) != 0 {
		t.Errorf("gh called for the default branch: %q", gh.calls)
	}
	// A plain session on a branch with a PR: the GitHub merge is fine.
	git(t, clone, "switch", "-q", "-c", "feature")
	commitFile(t, clone, "f.txt", "f")
	git(t, clone, "push", "-q", "origin", "feature")
	gh.pr = readyPR(git(t, clone, "rev-parse", "HEAD"))
	if p, err := planMerge(clone, "", "", ""); err != nil || !p.github {
		t.Errorf("plain session with a PR: %+v, %v", p, err)
	}
	// Without gh, a plain session can't merge locally.
	fakeGH(t, func(string, ...string) (string, error) { return "", errNoGH })
	if _, err := planMerge(clone, "", "", ""); err == nil || !strings.Contains(err.Error(), "gh isn't installed, and a local merge needs a worktree session") {
		t.Errorf("plain session, no gh: %v", err)
	}
}

func TestMergeLocal(t *testing.T) {
	clone := testRepo(t) // origin is a local path: not GitHub
	fakeGH(t, func(string, ...string) (string, error) { t.Error("gh run without a GitHub remote"); return "", nil })
	worktreeSettings = worktreeConfig{}
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	plan := func() (mergePlan, error) { return planMerge(w.path, w.branch, "main", w.repoRoot) }

	// Nothing committed: already in main.
	if p, err := plan(); err != nil || p.merged != "in main" {
		t.Errorf("nothing to merge: %+v, %v", p, err)
	}

	commitFile(t, w.path, "feature.txt", "new")
	commitFile(t, w.path, "more.txt", "more")
	_ = os.WriteFile(filepath.Join(w.path, "wip.txt"), []byte("x"), 0o644)
	tip := git(t, w.path, "rev-parse", "HEAD")
	p, err := plan()
	if err != nil || p.github || p.ahead != 2 {
		t.Fatalf("plan: %+v, %v", p, err)
	}
	if q := mergeQuestion(p); q != "Merge "+w.branch+" (2 commits) into main in api, without pushing? (1 uncommitted file isn't included) y/n" {
		t.Errorf("question %q", q)
	}

	// The main checkout on another branch, then with uncommitted changes.
	git(t, clone, "switch", "-q", "-c", "other")
	if _, err := plan(); err == nil || !strings.Contains(err.Error(), "is on other, not main: switch it to main") {
		t.Errorf("wrong branch: %v", err)
	}
	git(t, clone, "switch", "-q", "main")
	_ = os.WriteFile(filepath.Join(clone, "README.md"), []byte("edited\n"), 0o644)
	if _, err := plan(); err == nil || !strings.Contains(err.Error(), "has 1 uncommitted change: commit or stash") {
		t.Errorf("dirty: %v", err)
	}
	if err := mergeLocal(w.repoRoot, w.branch, "main"); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("mergeLocal must check again: %v", err)
	}
	git(t, clone, "checkout", "-q", "--", "README.md")
	_ = os.WriteFile(filepath.Join(clone, "untracked.txt"), []byte("x"), 0o644) // doesn't count

	// The merge: a merge commit on main, the worktree untouched.
	before := git(t, clone, "rev-parse", "main")
	if err := mergeLocal(w.repoRoot, w.branch, "main"); err != nil {
		t.Fatal(err)
	}
	if parents := strings.Fields(git(t, clone, "rev-list", "--parents", "-n", "1", "main")); len(parents) != 3 ||
		parents[1] != before || parents[2] != tip {
		t.Errorf("want a --no-ff merge commit of %s and %s, got %q", before, tip, parents)
	}
	if git(t, w.path, "rev-parse", "HEAD") != tip || !strings.Contains(git(t, w.path, "status", "--porcelain"), "wip.txt") {
		t.Error("the worktree must not be touched")
	}
	if git(t, clone, "rev-parse", "origin/main") != before {
		t.Error("main must not be pushed")
	}
	if p, err := plan(); err != nil || p.merged != "in main" {
		t.Errorf("after the merge: %+v, %v", p, err)
	}
	// x's merged check sees the local merge.
	fakeGH(t, noPR) // checkMerged also asks gh about a squash-merged PR
	if c := checkMerged(w.repoRoot, w.path, w.branch, "main"); !c.merged || c.how != "in the local main, not pushed" || !c.localOnly {
		t.Errorf("checkMerged: %+v", c)
	}
	// A merged PR at the tip means GitHub has the work too: not local only.
	fakeGH(t, func(string, ...string) (string, error) {
		return `{"number":3,"state":"MERGED","headRefOid":"` + tip + `"}`, nil
	})
	if c := checkMerged(w.repoRoot, w.path, w.branch, "main"); !c.merged || c.localOnly || c.how != "PR #3 merged" {
		t.Errorf("checkMerged with a merged PR: %+v", c)
	}
}

func TestMergeLocalConflict(t *testing.T) {
	clone := testRepo(t)
	worktreeSettings = worktreeConfig{}
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, w.path, "README.md", "theirs\n")
	commitFile(t, w.path, "clean.txt", "fine\n")
	commitFile(t, clone, "README.md", "ours\n")
	before := git(t, clone, "rev-parse", "HEAD")
	tip := git(t, w.path, "rev-parse", "HEAD")
	if _, err := planMerge(w.path, w.branch, "main", w.repoRoot); err != nil {
		t.Fatal(err)
	}
	err = mergeLocal(w.repoRoot, w.branch, "main")
	if err == nil || err.Error() != "merge conflict in README.md: aborted, so main is unchanged" {
		t.Fatalf("conflict: %v", err)
	}
	if git(t, clone, "rev-parse", "HEAD") != before || git(t, clone, "status", "--porcelain") != "" {
		t.Error("the main checkout should be as it was")
	}
	if _, err := gitRun(clone, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		t.Error("the merge should be aborted")
	}
	if git(t, w.path, "rev-parse", "HEAD") != tip {
		t.Error("the worktree must not be touched")
	}
}

// mergeModel is a model with one worktree session for w, selected.
func mergeModel(w newWorktree) (*model, *Session) {
	m := testModel("wt")
	m.width, m.height = 120, 32
	s := m.sessions[0]
	s.Dir, s.CodeRepo, s.RepoRoot, s.Worktree, s.Branch, s.BaseBranch = w.path, w.repoRoot, w.repoRoot, w.path, w.branch, "main"
	return m, s
}

func pressKey(m *model, r rune) {
	_, cmd := m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	runCmd(m, cmd)
}

func TestMKeyLocal(t *testing.T) {
	clone := testRepo(t)
	fakeGH(t, func(string, ...string) (string, error) { return "", errNoGH })
	t.Setenv("LOCALAPPDATA", t.TempDir()) // saveSessions
	worktreeSettings = worktreeConfig{}
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, w.path, "feature.txt", "new")
	git(t, w.path, "push", "-q", "origin", w.branch) // origin (a bare repo) has the branch
	origin := filepath.Join(filepath.Dir(clone), "origin.git")
	m, s := mergeModel(w)

	pressKey(m, 'm')
	if m.confirm == nil || m.confirm.text != "Merge "+w.branch+" (1 commit) into main in api, without pushing? y/n" {
		t.Fatalf("question: %+v (flash %q)", m.confirm, m.flash)
	}
	pressKey(m, 'n') // cancels
	if m.confirm != nil || s.mergeBusy.Load() || git(t, clone, "rev-parse", "main") != git(t, clone, "rev-parse", "origin/main") {
		t.Fatal("n should cancel without merging")
	}
	pressKey(m, 'm')
	_, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // no default: cancels too
	runCmd(m, cmd)
	if m.confirm != nil || s.mergeBusy.Load() || git(t, clone, "rev-parse", "main") != git(t, clone, "rev-parse", "origin/main") {
		t.Fatal("enter should cancel without merging")
	}
	pressKey(m, 'm')
	pressKey(m, 'y')
	want := "Merged into main in api (not pushed). Close the session and remove the worktree? y/n"
	if m.confirm == nil || m.confirm.text != want || s.mergeBusy.Load() {
		t.Fatalf("close offer: %+v, want %q (flash %q)", m.confirm, want, m.flash)
	}
	pressKey(m, 'y')
	if slices.Contains(m.sessions, s) || isDir(w.path) || refExists(clone, "refs/heads/"+w.branch) {
		t.Errorf("should be closed and removed (flash %q, closing %v)", m.flash, m.closing != nil)
	}
	if !isDir(clone) {
		t.Fatal("the main checkout must never be removed")
	}
	// main isn't pushed, so origin's branch is the only other copy: kept.
	if _, err := gitRun(origin, "rev-parse", "--verify", "refs/heads/"+w.branch); err != nil {
		t.Error("origin's branch must be kept while the merge is only local")
	}
	wantFlash := "Kept origin/" + w.branch + ": main isn't pushed yet. Closed: removed " + w.path + ", deleted branch " + w.branch
	if m.flash != wantFlash {
		t.Errorf("flash %q, want %q", m.flash, wantFlash)
	}
}

// A branch that's in origin/<base> is merged for good, so closing deletes
// origin's branch too.
func TestCloseMergedOnOriginDeletesRemote(t *testing.T) {
	clone := testRepo(t)
	fakeGH(t, noPR)
	t.Setenv("LOCALAPPDATA", t.TempDir())
	worktreeSettings = worktreeConfig{}
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, w.path, "feature.txt", "new")
	git(t, w.path, "push", "-q", "origin", w.branch, "HEAD:main")
	origin := filepath.Join(filepath.Dir(clone), "origin.git")
	if c := checkMerged(w.repoRoot, w.path, w.branch, "main"); !c.merged || c.localOnly || c.how != "in origin/main" {
		t.Fatalf("checkMerged: %+v", c)
	}
	m, s := mergeModel(w)
	pressKey(m, 'x')
	if m.confirm == nil || !m.confirm.defaultYes || !strings.HasSuffix(m.confirm.text, "you choose. Y/n") {
		t.Fatalf("x prompt: %+v", m.confirm)
	}
	_, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // enter is yes
	runCmd(m, cmd)
	if slices.Contains(m.sessions, s) || isDir(w.path) {
		t.Fatalf("should be closed (flash %q)", m.flash)
	}
	if _, err := gitRun(origin, "rev-parse", "--verify", "refs/heads/"+w.branch); err == nil {
		t.Error("origin's branch should be deleted")
	}
	if !strings.Contains(m.flash, "deleted origin/"+w.branch) || strings.Contains(m.flash, "Kept origin") {
		t.Errorf("flash %q", m.flash)
	}
}

func TestMKeyGitHub(t *testing.T) {
	w, origin, tip := pushedWorktree(t)
	gh := &ghFake{}
	fakeGH(t, gh.run)
	t.Setenv("LOCALAPPDATA", t.TempDir())
	m, s := mergeModel(w)

	// No PR: refused, with p suggested.
	pressKey(m, 'm')
	if m.confirm != nil || !strings.Contains(m.flash, "Can't merge: "+w.branch+" has no PR yet: press p") || s.mergeBusy.Load() {
		t.Errorf("no PR: %q", m.flash)
	}

	gh.pr = readyPR(tip)
	pressKey(m, 'm')
	if m.confirm == nil || !strings.Contains(m.confirm.text, `Squash-merge PR #12 "Retry failed uploads" into main`) {
		t.Fatalf("question: %+v (flash %q)", m.confirm, m.flash)
	}
	pressKey(m, 'y')
	if gh.called("pr merge 12 --squash --delete-branch") != 1 {
		t.Errorf("gh calls %q", gh.calls)
	}
	if m.confirm == nil || m.confirm.text != "Merged PR #12. Close the session and remove the worktree? y/n" {
		t.Fatalf("close offer: %+v (flash %q)", m.confirm, m.flash)
	}
	if sn := s.Snapshot(); sn.pr == nil || sn.pr.prState() != "merged" {
		t.Errorf("the badge should say merged: %+v", sn.pr)
	}
	// The close path: the PR is merged at the tip, so it's removed, and
	// origin's branch (the fake gh left it) is deleted.
	pressKey(m, 'y')
	if slices.Contains(m.sessions, s) || isDir(w.path) || refExists(w.repoRoot, "refs/heads/"+w.branch) {
		t.Errorf("should be closed (flash %q)", m.flash)
	}
	if _, err := gitRun(origin, "rev-parse", "--verify", "refs/heads/"+w.branch); err == nil {
		t.Error("the branch on origin should be deleted")
	}
}

func TestMKeyAlreadyMerged(t *testing.T) {
	w, _, tip := pushedWorktree(t)
	gh := &ghFake{pr: readyPR(tip)}
	gh.pr.State = "MERGED"
	fakeGH(t, gh.run)
	m, s := mergeModel(w)
	pressKey(m, 'm')
	if m.confirm == nil || m.confirm.text != w.branch+" is already merged (PR #12 merged). Close the session and remove the worktree? y/n" {
		t.Fatalf("offer: %+v (flash %q)", m.confirm, m.flash)
	}
	pressKey(m, 'n')
	if !slices.Contains(m.sessions, s) || !isDir(w.path) || gh.called("pr merge") != 0 {
		t.Error("n keeps everything, and nothing is merged")
	}
}
