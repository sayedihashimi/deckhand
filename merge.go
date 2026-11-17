package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Merging: m merges a session's branch, then offers to close the session
// with x's close path. The git and gh work is here; the key, the
// questions and the close offer are in mergeui.go.
//
// With gh and a GitHub remote, m squash-merges the branch's PR on GitHub,
// and only when GitHub would merge it as it is: open, not a draft, checks
// passing, mergeable, and with every local commit pushed. Nothing is ever
// bypassed (no --admin).
//
// Without gh or a GitHub remote, a worktree session's branch is merged into
// the default branch in the main checkout, with git merge --no-ff, and
// isn't pushed. The worktree's files are never touched.

// prMergeFields is what m asks gh pr view for.
const prMergeFields = prFields + ",title,baseRefName,mergeable,mergeStateStatus"

// mergePlan is what m found out before it asks.
type mergePlan struct {
	dir      string // where the session's branch is checked out
	branch   string
	base     string
	tip      string // the branch's commit
	dirty    int    // uncommitted files in dir, which aren't merged
	github   bool   // merge the PR on GitHub; else merge locally in repoRoot
	repo     string // HOST/OWNER/REPO, for gh -R
	pr       prInfo
	repoRoot string // the main checkout, for a local merge
	ahead    int    // commits not on the local base (a local merge)
	merged   string // already merged, and how ("PR #12 merged"): m offers to close
}

// planMerge works out what m should do for the branch checked out in dir.
// want is the session's branch (a worktree session), which must still be
// checked out; base is the branch it started from, or "" for the default
// branch; repoRoot is the main checkout of a worktree session, or "" for
// a plain session, which can only merge on GitHub.
func planMerge(dir, want, base, repoRoot string) (mergePlan, error) {
	p := mergePlan{dir: dir, repoRoot: repoRoot}
	cur, err := gitRun(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return p, fmt.Errorf("%s isn't in a git repo with commits", dir)
	}
	switch {
	case cur == "HEAD":
		return p, errors.New("HEAD is detached: check out a branch to merge")
	case want != "" && cur != want:
		return p, fmt.Errorf("the worktree is on %s, not the session's branch %s: switch back to merge it", cur, want)
	}
	p.branch = cur
	if base == "" {
		if base, err = defaultBranch(worktreeSettings, dir); err != nil {
			return p, err
		}
	}
	p.base = base
	if protectedBranch(dir, cur, base) {
		return p, fmt.Errorf("%s is the default branch, so there's nothing to merge it into", cur)
	}
	if p.tip, err = gitRun(dir, "rev-parse", "HEAD"); err != nil {
		return p, err
	}
	if out, err := gitRun(dir, "status", "--porcelain"); err == nil {
		p.dirty = len(outLines(out))
	}

	// GitHub when it's there; a local merge without gh or a GitHub remote.
	why := ""
	if _, err := githubHost(dir); err != nil {
		why = err.Error()
	} else if _, err := ghReady(dir); errors.Is(err, errNeedGH) {
		why = "gh isn't installed"
	} else if err != nil {
		return p, err // signed out: say so rather than merge locally
	} else {
		return planGitHubMerge(p)
	}
	return planLocalMerge(p, why)
}

func planGitHubMerge(p mergePlan) (mergePlan, error) {
	p.github = true
	pr, ok, err := prView(p.dir, p.branch, prMergeFields)
	if err != nil {
		return p, err
	}
	if !ok {
		return p, fmt.Errorf("%s has no PR yet: press p to push it and open one", p.branch)
	}
	p.pr = pr
	n := pr.Number
	switch pr.prState() {
	case "merged":
		p.merged = fmt.Sprintf("PR #%d merged", n)
		return p, nil
	case "closed":
		return p, fmt.Errorf("PR #%d is closed: reopen it on GitHub to merge it", n)
	case "draft":
		return p, fmt.Errorf("PR #%d is a draft: mark it ready for review first", n)
	}
	remote := ""
	if out, err := gitRun(p.dir, "ls-remote", "--heads", "origin", "refs/heads/"+p.branch); err == nil && out != "" {
		remote = strings.Fields(out)[0]
	}
	if !strings.EqualFold(remote, p.tip) || !strings.EqualFold(pr.HeadRefOid, p.tip) {
		return p, fmt.Errorf("%s has commits that aren't in PR #%d: press p to push them first", p.branch, n)
	}
	switch pr.checks() {
	case checksFailing:
		return p, fmt.Errorf("PR #%d's checks are failing", n)
	case checksPending:
		return p, fmt.Errorf("PR #%d's checks are still running: wait for them to finish", n)
	}
	if why := mergeBlocker(pr); why != "" {
		return p, fmt.Errorf("GitHub can't merge PR #%d: %s", n, why)
	}
	if p.repo = githubRepo(originURL(p.dir)); p.repo == "" {
		return p, fmt.Errorf("couldn't read the GitHub repo from origin's URL (%s)", originURL(p.dir))
	}
	return p, nil
}

// mergeBlocker is why GitHub won't merge the PR as it is, from mergeable
// and mergeStateStatus, or "" when it will.
func mergeBlocker(pr prInfo) string {
	base := pr.BaseRefName
	if base == "" {
		base = "the base branch"
	}
	switch strings.ToUpper(pr.Mergeable) {
	case "CONFLICTING":
		return "it has conflicts with " + base + ": resolve them first"
	case "MERGEABLE":
	default:
		return "it hasn't worked out yet whether it can be merged: try again in a moment"
	}
	switch strings.ToUpper(pr.MergeStateStatus) {
	case "DIRTY":
		return "it has conflicts with " + base + ": resolve them first"
	case "BLOCKED":
		return "it's blocked by the branch's rules, such as a required review or check"
	case "BEHIND":
		return "it's behind " + base + ", and the branch's rules require it to be up to date"
	case "DRAFT":
		return "it's a draft"
	case "UNKNOWN", "":
		return "it hasn't worked out yet whether it can be merged: try again in a moment"
	}
	return "" // CLEAN, HAS_HOOKS, UNSTABLE (only checks that aren't required are failing)
}

func planLocalMerge(p mergePlan, why string) (mergePlan, error) {
	root := p.repoRoot
	if root == "" || strings.EqualFold(filepath.Clean(p.dir), filepath.Clean(root)) {
		return p, fmt.Errorf("%s, and a local merge needs a worktree session (this one works in the checkout itself): merge %s there yourself",
			why, p.branch)
	}
	if err := checkMainCheckout(root, p.base); err != nil {
		return p, err
	}
	out, err := gitRun(root, "rev-list", "--count", "refs/heads/"+p.base+".."+p.tip)
	if err != nil {
		return p, err
	}
	_, _ = fmt.Sscanf(out, "%d", &p.ahead)
	if p.ahead == 0 {
		p.merged = "in " + p.base
	}
	return p, nil
}

// checkMainCheckout refuses a local merge unless the main checkout is on
// the default branch with no uncommitted changes to tracked files.
func checkMainCheckout(root, base string) error {
	if !isDir(root) {
		return fmt.Errorf("the main checkout %s is gone", root)
	}
	head, err := gitRun(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if head != base {
		return fmt.Errorf("the main checkout is on %s, not %s: switch it to %s to merge locally (%s)", head, base, base, root)
	}
	out, err := gitRun(root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if n := len(outLines(out)); n > 0 {
		what := "1 uncommitted change"
		if n > 1 {
			what = fmt.Sprintf("%d uncommitted changes", n)
		}
		return fmt.Errorf("the main checkout has %s: commit or stash them first (%s)", what, root)
	}
	return nil
}

// mergeQuestion is the confirmation m asks before merging.
func mergeQuestion(p mergePlan) string {
	dirty := uncommittedNote(p.dirty)
	if p.github {
		return fmt.Sprintf("Squash-merge PR #%d %q into %s on GitHub and delete its branch?%s y/n", p.pr.Number, p.pr.Title, p.base, dirty)
	}
	commits := "1 commit"
	if p.ahead != 1 {
		commits = fmt.Sprintf("%d commits", p.ahead)
	}
	return fmt.Sprintf("Merge %s (%s) into %s in %s, without pushing?%s y/n", p.branch, commits, p.base, filepath.Base(p.repoRoot), dirty)
}

// mergePR squash-merges the PR on GitHub and deletes its branch there. -R
// keeps gh away from the local checkout (it doesn't switch branches or
// delete the local branch; closing the session does that), and
// --match-head-commit makes GitHub refuse if the PR has moved on.
func mergePR(p mergePlan) error {
	_, err := ghRun(p.dir, "pr", "merge", strconv.Itoa(p.pr.Number), "--squash", "--delete-branch",
		"--match-head-commit", p.tip, "-R", p.repo)
	if err != nil {
		return fmt.Errorf("GitHub didn't merge PR #%d: %s", p.pr.Number, firstLine(strings.TrimPrefix(err.Error(), "gh pr: ")))
	}
	return nil
}

// mergeLocal merges branch into base in the main checkout, always with a
// merge commit (--no-ff). On a conflict it aborts the merge, so the main
// checkout is as it was, and says which files conflicted.
func mergeLocal(root, branch, base string) error {
	if err := checkMainCheckout(root, base); err != nil { // it may have changed since the question
		return err
	}
	_, err := gitRun(root, "merge", "--no-ff", "--no-edit", branch)
	if err == nil {
		return nil
	}
	var conflicts []string
	if out, e := gitRun(root, "diff", "--name-only", "--diff-filter=U"); e == nil {
		conflicts = outLines(out)
	}
	if _, e := gitRun(root, "rev-parse", "-q", "--verify", "MERGE_HEAD"); e == nil {
		if _, e := gitRun(root, "merge", "--abort"); e != nil {
			return fmt.Errorf("the merge failed and git merge --abort did too, so fix %s by hand: %s", root, firstLine(e.Error()))
		}
	}
	if len(conflicts) > 0 {
		const most = 5
		list := strings.Join(conflicts[:min(len(conflicts), most)], ", ")
		if len(conflicts) > most {
			list += fmt.Sprintf(" and %d more", len(conflicts)-most)
		}
		return fmt.Errorf("merge conflict in %s: aborted, so %s is unchanged", list, base)
	}
	return fmt.Errorf("git merge failed: %s", firstLine(strings.TrimPrefix(err.Error(), "git merge: ")))
}
