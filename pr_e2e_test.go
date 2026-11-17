//go:build e2e

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ghCalls reads testdata/ghapp's GHAPP_LOG: each call's arguments, joined.
func ghCalls(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l struct{ Args []string }
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, strings.Join(l.Args, " "))
		}
	}
	return out
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// TestE2EPullRequest pushes a worktree session's branch and opens a PR
// with p, with a fake gh on PATH and origin a local repo that looks like
// GitHub: nothing to push, the confirmation, the push and PR, the badge,
// opening the existing PR, a rejected push, gh signed out, and a plain
// session on the default branch.
func TestE2EPullRequest(t *testing.T) {
	clone, origin := githubClone(t)
	home := t.TempDir()
	exe, env, log := worktreeE2E(t, home)
	originMain := git(t, origin, "rev-parse", "refs/heads/main")
	env, ghState, ghLog := withGHApp(t, home, env)
	h := startHarness(t, exe, env, clone)

	// A worktree session.
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

	// Nothing committed yet.
	h.send("p")
	h.waitFor("Nothing to push", 10*time.Second)

	// A commit: p asks, showing the branch, the remote and the title.
	commitFile(t, s.Worktree, "feature.txt", "new")
	h.send("p")
	h.waitFor(`Push `+s.Branch+` to origin and open a PR into main, "Change feature.txt"? y/n`, 10*time.Second)
	h.send("y")
	h.waitFor("Created PR #1", 30*time.Second)
	tip := git(t, s.Worktree, "rev-parse", "HEAD")
	if got := git(t, origin, "rev-parse", "refs/heads/"+s.Branch); got != tip {
		t.Errorf("origin has %s, want %s", got, tip)
	}
	if up := git(t, s.Worktree, "rev-parse", "--abbrev-ref", "@{upstream}"); up != "origin/"+s.Branch {
		t.Errorf("upstream %s", up)
	}
	calls := ghCalls(t, ghLog)
	if countPrefix(calls, "pr create --fill-first --base main --head "+s.Branch) != 1 ||
		countPrefix(calls, "pr view https://github.com/example/api/pull/1 --web") != 1 {
		t.Errorf("gh calls: %q", calls)
	}
	// The badge, with the checks from the next poll.
	h.waitFor("⎇ "+s.Branch+"↑1 #1✓", 15*time.Second)

	// p again: the PR is up to date, so it's opened without asking.
	h.send("p")
	h.waitFor("Opened PR #1 (open) in the browser", 10*time.Second)
	if n := countPrefix(ghCalls(t, ghLog), "pr view https://github.com/example/api/pull/1 --web"); n != 2 {
		t.Errorf("opened %d times", n)
	}

	// origin's branch moved on and there's a new local commit: the push is
	// rejected, git's reason shows, and nothing is forced.
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", origin, other)
	git(t, other, "switch", "-q", s.Branch)
	commitFile(t, other, "theirs.txt", "theirs")
	git(t, other, "push", "-q", "origin", s.Branch)
	theirs := git(t, origin, "rev-parse", "refs/heads/"+s.Branch)
	commitFile(t, s.Worktree, "mine.txt", "mine")
	h.send("p")
	h.waitFor("Push "+s.Branch+" to origin, then open PR #1? y/n", 10*time.Second)
	h.send("y")
	h.waitFor("Push rejected, not retried with --force: ! [rejected] "+s.Branch+" -> "+s.Branch+" (fetch first)", 30*time.Second)
	if got := git(t, origin, "rev-parse", "refs/heads/"+s.Branch); got != theirs {
		t.Error("a rejected push must leave origin alone")
	}

	// gh signed out.
	_ = os.WriteFile(filepath.Join(ghState, "noauth"), nil, 0o644)
	h.send("p")
	h.waitFor("gh isn't signed in to github.com: run gh auth login", 10*time.Second)
	_ = os.Remove(filepath.Join(ghState, "noauth"))

	// A plain session in the main checkout, on main: refused, nothing run.
	before := len(ghCalls(t, ghLog))
	h.send("n")
	h.waitFor("Worktree", 5*time.Second)
	h.waitFor("same folder, no new branch", 3*time.Second) // the worktree is off
	h.send("\r")
	waitLaunches(t, h, log, 2)
	h.send("\x11")
	h.waitFor("api", 5*time.Second)
	h.send("p")
	h.waitFor("main is the default branch, and p never pushes it", 10*time.Second)
	if after := ghCalls(t, ghLog); countPrefix(after[before:], "auth") != 0 || countPrefix(after[before:], "pr create") != 0 {
		t.Errorf("gh run for the default branch: %q", after[before:])
	}
	if got := git(t, origin, "rev-parse", "refs/heads/main"); got != originMain {
		t.Error("main must never be pushed")
	}

	h.send("q")
	h.waitFor("Quit and end", 3*time.Second)
	h.send("y")
	h.waitExit(10 * time.Second)
}

// withPathPrefix puts dir first on env's PATH.
func withPathPrefix(env []string, dir string) []string {
	return editPath(env, func(p string) string { return dir + ";" + p })
}

// withPathSuffix puts dir last on env's PATH.
func withPathSuffix(env []string, dir string) []string {
	return editPath(env, func(p string) string { return p + ";" + dir })
}

func editPath(env []string, f func(string) string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			e = e[:5] + f(e[5:])
		}
		out = append(out, e)
	}
	return out
}

// withGHApp builds testdata/ghapp as gh.exe and puts it first on env's
// PATH, with its state folder and log under home.
func withGHApp(t *testing.T, home string, env []string) (out []string, ghState, ghLog string) {
	t.Helper()
	ghBin := t.TempDir()
	if b, err := exec.Command("go", "build", "-o", filepath.Join(ghBin, "gh.exe"), "./testdata/ghapp").CombinedOutput(); err != nil {
		t.Fatalf("build ghapp: %v\n%s", err, b)
	}
	ghState = filepath.Join(home, "gh")
	_ = os.MkdirAll(ghState, 0o755)
	ghLog = filepath.Join(home, "gh.log")
	env = withPathPrefix(env, ghBin)
	env = withGitUsrBin(env)
	return append(env, "GHAPP_DIR="+ghState, "GHAPP_LOG="+ghLog), ghState, ghLog
}

// withGitUsrBin adds Git for Windows' usr\bin to the end of env's PATH:
// its ucrt64\bin\git.exe crashes pushing to a local repo without it (sh
// and the msys DLLs).
//
// The install root comes from git --exec-path (<root>\<mingw64 or
// ucrt64>\libexec\git-core), not from where git.exe is, because the git on
// PATH may be <root>\cmd\git.exe or <root>\bin\git.exe (as on GitHub's
// Windows runners) rather than <root>\ucrt64\bin\git.exe.
func withGitUsrBin(env []string) []string {
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return env
	}
	execPath := filepath.Clean(strings.TrimSpace(string(out)))
	root := filepath.Dir(filepath.Dir(filepath.Dir(execPath)))
	if usrBin := filepath.Join(root, "usr", "bin"); isDir(usrBin) {
		env = withPathSuffix(env, usrBin)
	}
	return env
}
