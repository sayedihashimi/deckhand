package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnv makes git in tests independent of the user's config (no signing,
// no hooks, a known identity).
func gitEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	empty := filepath.Join(t.TempDir(), "gitconfig")
	_ = os.WriteFile(empty, nil, 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitRun(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func commitFile(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", name)
	git(t, dir, "commit", "-q", "-m", "Change "+name)
}

// testRepo makes a bare "origin" with one commit on main and a clone of it,
// both under a temporary folder. It returns the clone.
func testRepo(t *testing.T) string {
	t.Helper()
	gitEnv(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	seed := filepath.Join(base, "seed")
	clone := filepath.Join(base, "api")
	git(t, base, "init", "-q", "--bare", "-b", "main", origin)
	git(t, base, "init", "-q", "-b", "main", seed)
	_ = os.MkdirAll(filepath.Join(seed, "src", "app"), 0o755)
	commitFile(t, seed, "README.md", "hello\n")
	commitFile(t, filepath.Join(seed, "src", "app"), "main.go", "package main\n")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "push", "-q", "origin", "main")
	git(t, base, "clone", "-q", origin, clone)
	return clone
}

// fakeGH replaces ghRun for a test.
func fakeGH(t *testing.T, f func(dir string, args ...string) (string, error)) {
	t.Helper()
	old := ghRun
	ghRun = f
	t.Cleanup(func() { ghRun = old })
}

func noPR(dir string, args ...string) (string, error) {
	return "", errors.New("gh pr view: no pull requests found for branch")
}

func TestWorktreeName(t *testing.T) {
	seq := []int{0, 0, 0, 0, 1, 2}
	i := 0
	rnd := func(n int) int { v := seq[i%len(seq)] % n; i++; return v }
	taken := map[string]bool{"amber-badger": true}
	name, err := newWorktreeName(func(n string) bool { return taken[n] }, rnd)
	if err != nil || name != "brave-bison" {
		t.Fatalf("got %q, %v; want the first free name, brave-bison", name, err)
	}
	// Every two-word name taken: a number is added.
	name, err = newWorktreeName(func(n string) bool { return !strings.HasSuffix(n, "-3") }, func(int) int { return 0 })
	if err != nil || name != "amber-badger-3" {
		t.Fatalf("got %q, %v", name, err)
	}
	for _, w := range append(append([]string{}, nameAdjectives...), nameNouns...) {
		if w != strings.ToLower(w) || strings.ContainsAny(w, " -_") {
			t.Errorf("name word %q should be one lowercase word", w)
		}
	}
}

func TestParseWorktreeConfig(t *testing.T) {
	cfg, err := parseWorktreeConfig("[worktree]\nroot = ' D:\\wt\\{repo} '\nbase = 'develop'\nsetup = 'npm ci'\n")
	if err != nil || cfg.Root != `D:\wt\{repo}` || cfg.Base != "develop" || cfg.Setup != "npm ci" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if _, err := parseWorktreeConfig("[worktree]\nbase = 'my branch'\n"); err == nil {
		t.Error("a base with a space should be refused")
	}
	if cfg, err := parseWorktreeConfig(""); err != nil || cfg != (worktreeConfig{}) {
		t.Errorf("no [worktree]: %+v, %v", cfg, err)
	}
	if err := checkConfigText("[worktree]\nbase = '-x'\n"); err == nil {
		t.Error("checkConfigText should check [worktree]")
	}
}

func TestWorktreeRoot(t *testing.T) {
	repo := filepath.Join(`C:\src`, "api")
	for _, c := range []struct{ root, want string }{
		{"", `C:\src\api-wt`},
		{"{repo}-trees", `C:\src\api-trees`},
		{"worktrees", `C:\src\worktrees`},
		{`D:\wt`, `D:\wt`},
	} {
		if filepath.Separator != '\\' {
			t.Skip("Windows paths")
		}
		if got := worktreeRoot(worktreeConfig{Root: c.root}, repo); got != c.want {
			t.Errorf("root %q: got %q, want %q", c.root, got, c.want)
		}
	}
}

func TestDefaultBranch(t *testing.T) {
	clone := testRepo(t)
	if b, err := defaultBranch(worktreeConfig{}, clone); err != nil || b != "main" {
		t.Errorf("from origin/HEAD: %q, %v", b, err)
	}
	if b, _ := defaultBranch(worktreeConfig{Base: "develop"}, clone); b != "develop" {
		t.Errorf("[worktree] base should win: %q", b)
	}
	// No origin/HEAD: origin/main, then main, then master.
	git(t, clone, "remote", "set-head", "origin", "--delete")
	if b, _ := defaultBranch(worktreeConfig{}, clone); b != "main" {
		t.Errorf("without origin/HEAD: %q", b)
	}
	solo := filepath.Join(t.TempDir(), "solo")
	git(t, filepath.Dir(solo), "init", "-q", "-b", "master", solo)
	commitFile(t, solo, "a.txt", "a")
	if b, _ := defaultBranch(worktreeConfig{}, solo); b != "master" {
		t.Errorf("a local master: %q", b)
	}
	git(t, solo, "branch", "-m", "trunk")
	if _, err := defaultBranch(worktreeConfig{}, solo); err == nil {
		t.Error("no main or master should be an error")
	}
}

func TestCreateWorktree(t *testing.T) {
	clone := testRepo(t)
	var log []string
	w, err := createWorktree(worktreeConfig{}, filepath.Join(clone, "src", "app"), func(s string) { log = append(log, s) })
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(log, "\n"))
	}
	r, _ := findRepo(clone)
	if w.repoRoot != r.root || w.base != "main" || w.sub != filepath.Join("src", "app") {
		t.Errorf("got %+v", w)
	}
	if w.path != filepath.Join(r.root+"-wt", w.branch) || !isDir(filepath.Join(w.path, "src", "app")) {
		t.Errorf("worktree at %s", w.path)
	}
	if head := git(t, w.path, "rev-parse", "--abbrev-ref", "HEAD"); head != w.branch {
		t.Errorf("worktree is on %q, want %q", head, w.branch)
	}
	if w.baseCommit != git(t, clone, "rev-parse", "origin/main") {
		t.Errorf("base commit %s", w.baseCommit)
	}
	// It doesn't track the default branch, so a push can't go there.
	if up, err := gitRun(w.path, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		t.Errorf("branch tracks %s", up)
	}
	if !strings.Contains(strings.Join(log, "\n"), "git fetch origin main") {
		t.Errorf("log: %q", log)
	}

	// From inside the worktree: the same main repo, and a different name.
	w2, err := createWorktree(worktreeConfig{}, w.path, func(string) {})
	if err != nil || w2.repoRoot != r.root || w2.branch == w.branch {
		t.Errorf("second worktree: %+v, %v", w2, err)
	}

	// Offline (origin unreachable): the local default branch, and a note.
	git(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	log = nil
	w3, err := createWorktree(worktreeConfig{}, clone, func(s string) { log = append(log, s) })
	if err != nil || !strings.Contains(strings.Join(log, "\n"), "local main") {
		t.Errorf("offline: %v\n%s", err, strings.Join(log, "\n"))
	}
	_ = w3

	if _, err := createWorktree(worktreeConfig{}, t.TempDir(), func(string) {}); err == nil {
		t.Error("a folder outside git should be an error")
	}
}

func TestCheckMerged(t *testing.T) {
	clone := testRepo(t)
	fakeGH(t, noPR)
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	root := w.repoRoot

	// No commits yet: nothing to lose, so it counts as merged.
	if c := checkMerged(root, w.path, w.branch, "main"); !c.safe() {
		t.Errorf("fresh worktree: %+v", c)
	}

	commitFile(t, w.path, "feature.txt", "new")
	c := checkMerged(root, w.path, w.branch, "main")
	if c.merged || c.safe() || len(c.commits) != 1 || !strings.Contains(c.commits[0], "Change feature.txt") || c.pr != "no PR" ||
		c.ahead != 1 || c.onOrigin || c.pushed {
		t.Errorf("unmerged: %+v", c)
	}
	git(t, w.path, "push", "-q", "origin", w.branch)
	if c := checkMerged(root, w.path, w.branch, "main"); c.merged || !c.onOrigin || !c.pushed {
		t.Errorf("pushed, not merged: %+v", c)
	}
	commitFile(t, w.path, "more.txt", "more")
	if c := checkMerged(root, w.path, w.branch, "main"); !c.onOrigin || c.pushed || c.ahead != 2 {
		t.Errorf("pushed, then another commit: %+v", c)
	}
	git(t, w.path, "reset", "-q", "--hard", "HEAD~1")

	// Merged on origin (a merge or fast-forward): the tip is in origin/main.
	git(t, w.path, "push", "-q", "origin", "HEAD:main")
	c = checkMerged(root, w.path, w.branch, "main")
	if !c.merged || !c.safe() || c.how != "in origin/main" {
		t.Errorf("merged: %+v", c)
	}

	// Merged, but with an uncommitted file.
	_ = os.WriteFile(filepath.Join(w.path, "notes.txt"), []byte("x"), 0o644)
	c = checkMerged(root, w.path, w.branch, "main")
	if !c.merged || c.safe() || len(c.dirty) != 1 || !strings.Contains(c.dirty[0], "notes.txt") {
		t.Errorf("merged and dirty: %+v", c)
	}
}

func TestCheckMergedSquash(t *testing.T) {
	clone := testRepo(t)
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, w.path, "feature.txt", "new")
	commitFile(t, w.path, "more.txt", "more")
	tip := git(t, w.path, "rev-parse", "HEAD")

	// GitHub squash-merged the PR: origin/main has a different commit, so
	// only the PR shows it's merged.
	var asked []string
	head := tip
	fakeGH(t, func(dir string, args ...string) (string, error) {
		asked = args
		return `{"number":7,"state":"MERGED","headRefOid":"` + head + `","url":"https://example.com/pr/7"}`, nil
	})
	c := checkMerged(w.repoRoot, w.path, w.branch, "main")
	if !c.merged || !c.safe() || c.how != "PR #7 merged" || len(c.commits) != 2 {
		t.Errorf("squash-merged: %+v", c)
	}
	if strings.Join(asked, " ") != "pr view "+w.branch+" --json number,state,headRefOid,url" {
		t.Errorf("gh called with %q", asked)
	}

	// The PR was merged, then more was committed: not merged.
	head = git(t, w.path, "rev-parse", "HEAD~1")
	if c := checkMerged(w.repoRoot, w.path, w.branch, "main"); c.merged || !strings.Contains(c.pr, "commits after it") {
		t.Errorf("commits after the merged PR: %+v", c)
	}

	// An open PR isn't merged.
	fakeGH(t, func(string, ...string) (string, error) {
		return `{"number":8,"state":"OPEN","headRefOid":"` + tip + `"}`, nil
	})
	if c := checkMerged(w.repoRoot, w.path, w.branch, "main"); c.merged || c.pr != "PR #8 open" {
		t.Errorf("open PR: %+v", c)
	}

	// Without gh, say so.
	fakeGH(t, func(string, ...string) (string, error) { return "", errNoGH })
	if c := checkMerged(w.repoRoot, w.path, w.branch, "main"); c.merged || !strings.Contains(c.pr, "gh isn't installed") {
		t.Errorf("no gh: %+v", c)
	}
}

func TestRemoveWorktree(t *testing.T) {
	clone := testRepo(t)
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	// Never the main checkout, or a folder that isn't a worktree.
	if _, err := removeWorktree(w.repoRoot, w.repoRoot, w.branch, true, false); err == nil || !isDir(w.repoRoot) {
		t.Fatalf("removing the main checkout should be refused: %v", err)
	}
	other := t.TempDir()
	if _, err := removeWorktree(w.repoRoot, other, w.branch, true, false); err == nil || !isDir(other) {
		t.Fatalf("removing another folder should be refused: %v", err)
	}

	commitFile(t, w.path, "feature.txt", "new")
	git(t, w.path, "push", "-q", "origin", w.branch)
	done, err := removeWorktree(w.repoRoot, w.path, w.branch, false, true)
	if err != nil {
		t.Fatalf("%v (did %q)", err, done)
	}
	if isDir(w.path) || refExists(w.repoRoot, "refs/heads/"+w.branch) {
		t.Errorf("worktree or branch left behind: %q", done)
	}
	if _, err := gitRun(w.repoRoot, "ls-remote", "--exit-code", "--heads", "origin", w.branch); err == nil {
		t.Errorf("the branch on origin should be deleted: %q", done)
	}
	if len(done) != 3 {
		t.Errorf("done = %q", done)
	}

	// Delete anyway: uncommitted files go, the branch on origin stays.
	w2, _ := createWorktree(worktreeConfig{}, clone, func(string) {})
	commitFile(t, w2.path, "a.txt", "a")
	git(t, w2.path, "push", "-q", "origin", w2.branch)
	_ = os.WriteFile(filepath.Join(w2.path, "wip.txt"), []byte("x"), 0o644)
	if _, err := removeWorktree(w2.repoRoot, w2.path, w2.branch, false, false); err == nil {
		t.Error("a dirty worktree needs force")
	}
	if _, err := removeWorktree(w2.repoRoot, w2.path, w2.branch, true, false); err != nil || isDir(w2.path) {
		t.Errorf("force: %v", err)
	}
	if _, err := gitRun(w2.repoRoot, "ls-remote", "--exit-code", "--heads", "origin", w2.branch); err != nil {
		t.Error("delete anyway shouldn't delete the branch on origin")
	}
}

func TestRepoRootGuess(t *testing.T) {
	clone := testRepo(t)
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := repoRootGuess(filepath.Join(clone, "src")); !strings.EqualFold(got, clone) {
		t.Errorf("in the clone: %s", got)
	}
	if got := repoRootGuess(filepath.Join(w.path, "src", "app")); !strings.EqualFold(filepath.Base(got), filepath.Base(clone)) {
		t.Errorf("in a worktree: %s, want %s", got, w.repoRoot)
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		path, dir string
		want      bool
	}{
		{`C:\src\api`, `C:\src\api`, true},
		{`C:\src\api\sub`, `c:\SRC\api`, true},
		{`C:\src\api-wt\x`, `C:\src\api`, false},
		{`C:\notes`, ``, false},
	} {
		if filepath.Separator != '\\' {
			t.Skip("Windows paths")
		}
		if got := within(c.path, c.dir); got != c.want {
			t.Errorf("within(%q, %q) = %v", c.path, c.dir, got)
		}
	}
}

// The config screen doesn't edit [worktree], but saving keeps it.
func TestConfigSaveKeepsWorktree(t *testing.T) {
	cfg, raw, err := decodeConfig("[worktree]\nroot = 'D:\\wt'\nsetup = 'npm ci'\n")
	if err != nil {
		t.Fatal(err)
	}
	text, err := encodeConfig(cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	w, err := parseWorktreeConfig(text)
	if err != nil || w.Root != `D:\wt` || w.Setup != "npm ci" {
		t.Errorf("after a save: %+v, %v\n%s", w, err, text)
	}
	if _, err := parseWorktreeConfig(sampleConfig); err != nil {
		t.Errorf("sample config: %v", err)
	}
}
