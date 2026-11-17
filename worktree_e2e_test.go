//go:build e2e

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// argsLaunch is one start of testdata/argsapp, from its ARGSAPP_LOG.
type argsLaunch struct {
	Args     []string `json:"args"`
	Cwd      string   `json:"cwd"`
	Worktree string   `json:"worktree"`
	Branch   string   `json:"branch"`
}

func argsLaunches(t *testing.T, path string) []argsLaunch {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []argsLaunch
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var l argsLaunch
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, l)
		}
	}
	return out
}

// waitLaunches waits for argsapp to have started n times.
func waitLaunches(t *testing.T, h *harness, path string, n int) []argsLaunch {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if l := argsLaunches(t, path); len(l) >= n {
			return l
		}
	}
	t.Fatalf("argsapp didn't start %d times; screen:\n%s", n, h.screen())
	return nil
}

type savedWorktree struct {
	ID, Name, Dir string
	CodeRepo      string `json:"code_repo"`
	RepoRoot      string `json:"repo_root"`
	Worktree      string `json:"worktree"`
	Branch        string `json:"branch"`
	BaseBranch    string `json:"base_branch"`
	BaseCommit    string `json:"base_commit"`
}

func savedWorktrees(t *testing.T, home string) []savedWorktree {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []savedWorktree
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// worktreeE2E builds deckhand and argsapp (as claude.exe) and returns the
// environment: PATH has only argsapp, git, PowerShell and System32, so
// there's no gh.
func worktreeE2E(t *testing.T, home string) (exe string, env []string, log string) {
	t.Helper()
	exe = buildDeckhand(t)
	gitExe, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/argsapp").CombinedOutput(); err != nil {
		t.Fatalf("build argsapp: %v\n%s", err, out)
	}
	path := bin + ";" + filepath.Dir(gitExe) + ";" + os.Getenv("SystemRoot") + `\System32`
	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		path += ";" + filepath.Dir(pwsh)
	}
	log = filepath.Join(home, "argsapp.log")
	env = append(envWith(home, path), "ARGSAPP_LOG="+log)
	return exe, env, log
}

// TestE2EWorktreeSession creates a worktree session from the dialog in a
// subfolder of a repo, resumes it, adds a second one with W, and closes
// them: the merged one is removed, the unmerged one warns and is kept.
func TestE2EWorktreeSession(t *testing.T) {
	clone := testRepo(t) // also isolates git from your config
	home := t.TempDir()
	exe, env, log := worktreeE2E(t, home)
	h := startHarness(t, exe, env, filepath.Join(clone, "src", "app"))

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.waitFor("Worktree", 3*time.Second) // the folder is in git
	h.send("\x14")                       // ctrl+t
	h.waitFor("a new branch and folder for", 3*time.Second)
	h.send("\r")
	first := waitLaunches(t, h, log, 1)[0]
	h.waitFor("argsapp done", 10*time.Second)

	saved := savedWorktrees(t, home)
	if len(saved) != 1 {
		t.Fatalf("saved %+v", saved)
	}
	a := saved[0]
	r, _ := findRepo(clone)
	if !sameDir(a.RepoRoot, r.root) || a.Branch == "" || a.Name != a.Branch || a.BaseBranch != "main" || a.BaseCommit == "" {
		t.Errorf("saved session %+v", a)
	}
	if !sameDir(a.Worktree, filepath.Join(r.root+"-wt", a.Branch)) || !sameDir(a.Dir, filepath.Join(a.Worktree, "src", "app")) {
		t.Errorf("worktree %s, dir %s", a.Worktree, a.Dir)
	}
	if !sameDir(first.Cwd, a.Dir) || !sameDir(first.Worktree, a.Worktree) || first.Branch != a.Branch {
		t.Errorf("agent started with %+v", first)
	}
	if slices.Contains(first.Args, "--add-dir") {
		t.Errorf("the agent runs in the worktree, so it needs no --add-dir: %q", first.Args)
	}
	h.send("\x11") // back to the list
	h.waitFor("⎇ "+a.Branch, 10*time.Second)

	// Resume it: same folder, --resume.
	h.send("K")
	h.waitFor("End ", 3*time.Second)
	h.send("y")
	h.waitFor("⏎ resume", 10*time.Second)
	h.send("\r")
	second := waitLaunches(t, h, log, 2)[1]
	if !slices.Contains(second.Args, "--resume") || !sameDir(second.Cwd, a.Dir) {
		t.Errorf("resume: %+v", second)
	}
	h.send("\x11")

	// W: another worktree session below it, same agent and repo.
	h.send("W")
	third := waitLaunches(t, h, log, 3)[2]
	h.send("\x11")
	saved = savedWorktrees(t, home)
	if len(saved) != 2 {
		t.Fatalf("after W: %+v", saved)
	}
	b := saved[1]
	if b.Branch == a.Branch || !sameDir(b.Dir, filepath.Join(b.Worktree, "src", "app")) || !sameDir(third.Cwd, b.Dir) {
		t.Errorf("W session %+v, started in %s", b, third.Cwd)
	}

	// b is merged (pushed to main); a has a commit that isn't.
	commitFile(t, a.Worktree, "a.txt", "a")
	commitFile(t, b.Worktree, "b.txt", "b")
	git(t, b.Worktree, "push", "-q", "origin", "HEAD:main")

	// x on b (selected): it's stopped, checked and removed.
	h.send("x")
	h.waitFor("and close it?", 3*time.Second)
	h.send("y")
	h.waitFor("Closed: removed", 30*time.Second)
	if isDir(b.Worktree) || refExists(r.root, "refs/heads/"+b.Branch) {
		t.Errorf("merged worktree or branch left behind")
	}

	// x on a: the warning, then k keeps the worktree.
	h.send("x")
	h.waitFor("and close it?", 3*time.Second)
	h.send("y")
	h.waitFor("isn't merged into main", 30*time.Second)
	h.waitFor("Change a.txt", 3*time.Second)
	h.waitFor("The 1 commit not on main will be lost", 3*time.Second) // a isn't pushed
	h.waitFor("k keep the worktree · D delete anyway", 3*time.Second)
	h.send("k")
	h.waitFor("No sessions yet", 5*time.Second)
	if !isDir(a.Worktree) || !refExists(r.root, "refs/heads/"+a.Branch) {
		t.Errorf("k should keep the worktree and branch")
	}
	if !isDir(clone) {
		t.Fatal("the repo must never be removed")
	}
	h.send("q")
	h.waitExit(10 * time.Second)
}

// TestE2EWorktreeNotesFolder starts a wrapper agent (like a PowerShell
// function around claude) in a notes folder that isn't git, with a code
// repo elsewhere. The agent stays in the notes folder and gets --add-dir
// and --append-system-prompt through the claude shim, on resume too.
func TestE2EWorktreeNotesFolder(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("pwsh not found")
	}
	clone := testRepo(t)
	home := t.TempDir()
	exe, env, log := worktreeE2E(t, home)
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte(`
[[agents]]
name   = "wrap"
pwsh   = "function Start-Wrapped { claude --name demo }; Start-Wrapped"
type   = "claude"
resume = "claude --resume {id}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	env = append(env, "DECKHAND_CONFIG="+cfg)
	notes := filepath.Join(t.TempDir(), "notes")
	_ = os.MkdirAll(notes, 0o755)
	h := startHarness(t, exe, env, notes)

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.waitFor("Repo", 3*time.Second) // the folder isn't git: a code repo row
	if strings.Contains(h.screen(), "Worktree ") {
		t.Fatalf("no worktree line before there's a repo:\n%s", h.screen())
	}
	h.send("\x1b[B") // ↓ (no suggestions) to the repo row
	h.send(clone)
	h.waitFor("Code repo", 3*time.Second)
	h.waitFor("Worktree", 3*time.Second)
	h.send("\x14") // ctrl+t
	h.waitFor("a new branch and folder for api", 3*time.Second)
	h.send("\r")
	first := waitLaunches(t, h, log, 1)[0]
	h.waitFor("argsapp done", 10*time.Second)

	saved := savedWorktrees(t, home)
	if len(saved) != 1 {
		t.Fatalf("saved %+v", saved)
	}
	s := saved[0]
	if !sameDir(s.Dir, notes) || !sameDir(s.CodeRepo, clone) || s.Worktree == "" || !isDir(s.Worktree) {
		t.Fatalf("saved session %+v", s)
	}
	checkArgs := func(l argsLaunch, what string) {
		t.Helper()
		if !sameDir(l.Cwd, notes) {
			t.Errorf("%s: the agent should stay in the notes folder, not %s", what, l.Cwd)
		}
		i := slices.Index(l.Args, "--add-dir")
		if i < 0 || i+1 >= len(l.Args) || !sameDir(l.Args[i+1], s.Worktree) {
			t.Errorf("%s: --add-dir %s missing: %q", what, s.Worktree, l.Args)
		}
		j := slices.Index(l.Args, "--append-system-prompt")
		if j < 0 || j+1 >= len(l.Args) || !strings.Contains(l.Args[j+1], s.Worktree) || !strings.Contains(l.Args[j+1], s.Branch) {
			t.Errorf("%s: --append-system-prompt missing: %q", what, l.Args)
		}
		if !slices.Contains(l.Args, "--settings") || (what == "start" && !slices.Contains(l.Args, "--name")) {
			t.Errorf("%s: the wrapper's own arguments and the hooks should still be there: %q", what, l.Args)
		}
	}
	checkArgs(first, "start")

	h.send("\x11")
	h.waitFor("⎇ api:", 10*time.Second) // the marker and the repo's name

	// Resume passes them again.
	h.send("K")
	h.waitFor("End ", 3*time.Second)
	h.send("y")
	h.waitFor("⏎ resume", 10*time.Second)
	h.send("\r")
	second := waitLaunches(t, h, log, 2)[1]
	if !slices.Contains(second.Args, "--resume") {
		t.Errorf("resume: %q", second.Args)
	}
	checkArgs(second, "resume")
	h.send("\x11")

	// The code repo is remembered for the notes folder. Start a session
	// on it without a worktree: Claude gets the repo itself.
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.waitFor("Code repo "+clone, 3*time.Second)
	h.waitFor("same folder, no new branch", 3*time.Second) // the worktree is off
	h.send("\r")
	codeOnly := func(l argsLaunch, what string) {
		t.Helper()
		if !sameDir(l.Cwd, notes) {
			t.Errorf("%s: the agent should stay in the notes folder, not %s", what, l.Cwd)
		}
		i := slices.Index(l.Args, "--add-dir")
		if i < 0 || i+1 >= len(l.Args) || !sameDir(l.Args[i+1], clone) {
			t.Errorf("%s: --add-dir %s missing: %q", what, clone, l.Args)
		}
		j := slices.Index(l.Args, "--append-system-prompt")
		if j < 0 || j+1 >= len(l.Args) || !strings.Contains(l.Args[j+1], clone) || strings.Contains(l.Args[j+1], "worktree") {
			t.Errorf("%s: --append-system-prompt about the repo missing: %q", what, l.Args)
		}
		if l.Worktree != "" {
			t.Errorf("%s: no worktree, so no DECKHAND_WORKTREE: %q", what, l.Worktree)
		}
	}
	codeOnly(waitLaunches(t, h, log, 3)[2], "code repo, start")
	h.send("\x11")
	h.waitFor("api:main", 10*time.Second) // the code repo's branch line
	h.send("K")
	h.waitFor("End ", 3*time.Second)
	h.send("y")
	h.waitFor("⏎ resume", 10*time.Second)
	h.send("\r")
	resumed := waitLaunches(t, h, log, 4)[3]
	if !slices.Contains(resumed.Args, "--resume") {
		t.Errorf("code repo, resume: %q", resumed.Args)
	}
	codeOnly(resumed, "code repo, resume")
	h.send("\x11")
	h.send("x") // a plain session: removed from the list, nothing else
	h.waitFor("and remove it?", 3*time.Second)
	h.send("y")
	h.waitGone("api:main", 5*time.Second)
	if !isDir(clone) {
		t.Fatal("the code repo must never be touched")
	}

	// Closing: nothing committed, so it's removed; the notes folder stays.
	h.send("x")
	h.waitFor("and close it?", 3*time.Second)
	h.send("y")
	h.waitFor("Closed: removed", 30*time.Second)
	if isDir(s.Worktree) || !isDir(notes) || !isDir(clone) {
		t.Errorf("worktree %v, notes %v, repo %v", isDir(s.Worktree), isDir(notes), isDir(clone))
	}
	h.send("q")
	h.waitExit(10 * time.Second)
}
