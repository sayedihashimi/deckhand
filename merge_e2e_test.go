//go:build e2e

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startWorktreeSessionE2E starts a worktree session from the dialog, goes
// back to the list and returns the saved session.
func startWorktreeSessionE2E(t *testing.T, h *harness, home, log string) savedWorktree {
	t.Helper()
	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Worktree", 5*time.Second)
	h.send("\x14") // ctrl+t
	h.waitFor("a new branch and folder for", 3*time.Second)
	h.send("\r")
	waitLaunches(t, h, log, 1)
	h.waitFor("argsapp done", 10*time.Second)
	h.send("\x11")
	s := savedWorktrees(t, home)[0]
	h.waitFor("⎇ "+s.Branch, 10*time.Second)
	return s
}

// TestE2EMergeGitHub merges a worktree session's PR with m, with a fake gh
// and origin a local repo that looks like GitHub: no PR yet, failing
// checks, an unpushed commit, then the squash merge and the close offer.
func TestE2EMergeGitHub(t *testing.T) {
	clone, origin := githubClone(t)
	home := t.TempDir()
	exe, env, log := worktreeE2E(t, home)
	env, ghState, ghLog := withGHApp(t, home, env)
	h := startHarness(t, exe, env, clone)
	s := startWorktreeSessionE2E(t, h, home, log)

	// No PR yet: m suggests p.
	commitFile(t, s.Worktree, "feature.txt", "new")
	h.send("m")
	h.waitFor("Can't merge: "+s.Branch+" has no PR yet: press p", 10*time.Second)
	h.send("p")
	h.waitFor("open a PR into main", 10*time.Second)
	h.send("y")
	h.waitFor("Created PR #1", 30*time.Second)

	// Failing checks: refused.
	prPath := filepath.Join(ghState, "pr-"+s.Branch+".json")
	good, err := os.ReadFile(prPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(prPath, []byte(strings.Replace(string(good), `"SUCCESS"`, `"FAILURE"`, 1)), 0o644)
	h.send("m")
	h.waitFor("Can't merge: PR #1's checks are failing", 10*time.Second)
	_ = os.WriteFile(prPath, good, 0o644)

	// A commit that isn't pushed: refused, with p suggested.
	commitFile(t, s.Worktree, "more.txt", "more")
	h.send("m")
	h.waitFor("has commits that aren't in PR #1: press p to push them first", 10*time.Second)
	git(t, s.Worktree, "reset", "-q", "--hard", "HEAD~1")

	// The merge: asked, merged, then the close offer.
	tip := git(t, s.Worktree, "rev-parse", "HEAD")
	h.send("m")
	h.waitFor(`Squash-merge PR #1 "Change feature.txt" into main on GitHub and delete its branch? y/n`, 10*time.Second)
	h.send("y")
	h.waitFor("Merged PR #1. Close the session and remove the worktree? y/n", 30*time.Second)
	calls := ghCalls(t, ghLog)
	if countPrefix(calls, "pr merge 1 --squash --delete-branch --match-head-commit "+tip+" -R github.com/example/api") != 1 {
		t.Errorf("gh calls: %q", calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "--admin") {
			t.Errorf("never --admin: %q", c)
		}
	}
	h.send("y")
	h.waitFor("Closed: removed", 30*time.Second)
	h.waitFor("No sessions yet", 5*time.Second)
	if isDir(s.Worktree) || refExists(clone, "refs/heads/"+s.Branch) {
		t.Error("the worktree and branch should be removed")
	}
	if _, err := gitRun(origin, "rev-parse", "--verify", "refs/heads/"+s.Branch); err == nil {
		t.Error("the branch on origin should be deleted")
	}
	h.send("q")
	h.waitExit(10 * time.Second)
}

// TestE2EMergeLocal merges a worktree session's branch locally with m, with
// no gh: the dirty-checkout and wrong-branch refusals, a conflict that's
// aborted, then the merge and the close offer.
func TestE2EMergeLocal(t *testing.T) {
	clone := testRepo(t)
	home := t.TempDir()
	exe, env, log := worktreeE2E(t, home) // no gh on PATH
	env = withGitUsrBin(env)              // so ls-remote to the local origin works, as for a push
	h := startHarness(t, exe, env, clone)
	s := startWorktreeSessionE2E(t, h, home, log)
	commitFile(t, s.Worktree, "README.md", "theirs\n")

	// The main checkout has an uncommitted change, then is on another branch.
	_ = os.WriteFile(filepath.Join(clone, "README.md"), []byte("wip\n"), 0o644)
	h.send("m")
	h.waitFor("Can't merge: the main checkout has 1 uncommitted change", 10*time.Second)
	git(t, clone, "checkout", "-q", "--", "README.md")
	git(t, clone, "switch", "-q", "-c", "other")
	h.send("m")
	h.waitFor("Can't merge: the main checkout is on other, not main", 10*time.Second)
	git(t, clone, "switch", "-q", "main")

	// A conflict: aborted, main unchanged.
	commitFile(t, clone, "README.md", "ours\n")
	before := git(t, clone, "rev-parse", "HEAD")
	h.send("m")
	h.waitFor("Merge "+s.Branch+" (1 commit) into main in api, without pushing? y/n", 10*time.Second)
	h.send("y")
	h.waitFor("Merge conflict in README.md: aborted, so main is unchanged", 30*time.Second)
	if git(t, clone, "rev-parse", "HEAD") != before || git(t, clone, "status", "--porcelain") != "" {
		t.Error("the main checkout should be as it was")
	}

	// Without the conflicting commit: merged, not pushed, then closed.
	git(t, clone, "reset", "-q", "--hard", "origin/main")
	git(t, s.Worktree, "push", "-q", "origin", s.Branch)
	tip := git(t, s.Worktree, "rev-parse", "HEAD")
	h.send("m")
	h.waitFor("into main in api, without pushing? y/n", 10*time.Second)
	h.send("y")
	h.waitFor("Merged into main in api (not pushed). Close the session and remove the worktree? y/n", 30*time.Second)
	if parents := strings.Fields(git(t, clone, "rev-list", "--parents", "-n", "1", "main")); len(parents) != 3 || parents[2] != tip {
		t.Errorf("want a merge commit with %s, got %q", tip, parents)
	}
	if git(t, clone, "rev-parse", "origin/main") == git(t, clone, "rev-parse", "main") {
		t.Error("main must not be pushed")
	}
	h.send("y")
	h.waitFor("Kept origin/"+s.Branch+": main isn't pushed yet.", 30*time.Second)
	if isDir(s.Worktree) || refExists(clone, "refs/heads/"+s.Branch) {
		t.Error("the worktree and branch should be removed")
	}
	if _, err := gitRun(filepath.Join(filepath.Dir(clone), "origin.git"), "rev-parse", "--verify", "refs/heads/"+s.Branch); err != nil {
		t.Error("origin's branch must be kept while main isn't pushed")
	}
	h.send("q")
	h.waitExit(10 * time.Second)
}
