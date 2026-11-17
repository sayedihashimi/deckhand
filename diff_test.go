package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const sampleDiff = `diff --git a/src/login.ts b/src/login.ts
index 1111111..2222222 100644
--- a/src/login.ts
+++ b/src/login.ts
@@ -1,4 +1,5 @@
 import { session } from "./session";
-const retries = 1;
+const retries = 3;
+// --- not a header
 export function login() {
@@ -10,2 +11,2 @@ export function login() {
-  check();
+  refresh();
diff --git a/docs/new file.md b/docs/new file.md
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/docs/new file.md
@@ -0,0 +1,2 @@
+# New
+++ text
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 4444444..0000000
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
\ No newline at end of file
diff --git a/a.go b/b.go
similarity index 90%
rename from a.go
rename to b.go
index 5555555..6666666 100644
--- a/a.go
+++ b/b.go
@@ -1 +1 @@
-package a
+package b
diff --git a/logo.png b/logo.png
index 7777777..8888888 100644
Binary files a/logo.png and b/logo.png differ
diff --git "a/tab\there.txt" "b/tab\there.txt"
index 9999999..aaaaaaa 100644
--- "a/tab\there.txt"
+++ "b/tab\there.txt"
@@ -1 +1 @@
-x
+y
`

func TestParseDiff(t *testing.T) {
	files := parseDiff([]byte(sampleDiff))
	want := []struct {
		path, old  string
		status     byte
		adds, dels int
		binary     bool
	}{
		{"src/login.ts", "", 'M', 3, 2, false},
		{"docs/new file.md", "", 'A', 2, 0, false},
		{"old.txt", "", 'D', 0, 1, false},
		{"b.go", "a.go", 'R', 1, 1, false},
		{"logo.png", "", 'M', 0, 0, true},
		{"tab\there.txt", "", 'M', 1, 1, false},
	}
	if len(files) != len(want) {
		t.Fatalf("got %d files: %+v", len(files), files)
	}
	for i, w := range want {
		f := files[i]
		if f.path != w.path || f.oldPath != w.old || f.status != w.status || f.adds != w.adds || f.dels != w.dels || f.binary != w.binary {
			t.Errorf("file %d = %q (old %q) %c +%d -%d binary=%v, want %+v", i, f.path, f.oldPath, f.status, f.adds, f.dels, f.binary, w)
		}
	}
	// The body starts at the first hunk and keeps every hunk line.
	if b := files[0].body; len(b) != 9 || b[0] != "@@ -1,4 +1,5 @@" || b[len(b)-1] != "+  refresh();" {
		t.Errorf("body = %q", b)
	}
	if b := files[2].body; b[len(b)-1] != `\ No newline at end of file` {
		t.Errorf("deleted body = %q", b)
	}
}

func TestSplitDiffHeader(t *testing.T) {
	for _, c := range []struct{ in, a, b string }{
		{"a/x.go b/x.go", "x.go", "x.go"},
		{"a/dir with b/ space.txt b/dir with b/ space.txt", "dir with b/ space.txt", "dir with b/ space.txt"},
		{"a/old.go b/new.go", "old.go", "new.go"},
		{`"a/t\tx" "b/t\tx"`, "t\tx", "t\tx"},
		{`a/plain "b/q\"x"`, "plain", `q"x`},
	} {
		a, b := splitDiffHeader(c.in)
		if a != c.a || b != c.b {
			t.Errorf("splitDiffHeader(%q) = %q, %q; want %q, %q", c.in, a, b, c.a, c.b)
		}
	}
}

func TestCleanDiffText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":         "plain",
		"\tx":           "    x",
		"ab\tx":         "ab  x",
		"red \x1b[31mx": "red ?[31mx",
		"bell\a\r":      "bell??",
		"naïve\u009b2J": "naïve?2J",
		"日本a\tx":        "日本a   x", // wide characters count as two
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderDiff(t *testing.T) {
	res := diffResult{base: "origin/main", mergeBase: "abc1234", files: parseDiff([]byte(sampleDiff))}
	res.files = append(res.files, diffFile{path: "notes.txt", status: '?', adds: 1, body: []string{"@@ -0,0 +1,1 @@", "+hello"}})
	lines, starts := renderDiff(res)
	text := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{
		"7 files changed  +8 −5 vs origin/main (merge base abc1234)",
		"M  src/login.ts",
		"R  a.go → b.go",
		"?  notes.txt",
		"untracked",
		"binary",
		"@@ -10,2 +11,2 @@ export function login() {",
		"+hello",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if len(starts) != len(res.files) {
		t.Fatalf("starts = %v", starts)
	}
	for i, s := range starts {
		if !strings.Contains(ansi.Strip(lines[s]), clean(res.files[i].path)) {
			t.Errorf("start %d (line %d) = %q", i, s, ansi.Strip(lines[s]))
		}
	}
	// Hunk lines are coloured.
	for _, l := range lines {
		if ansi.Strip(l) == " +const retries = 3;" && l == ansi.Strip(l) {
			t.Errorf("added line has no colour: %q", l)
		}
	}

	for _, c := range []struct {
		res  diffResult
		want string
	}{
		{diffResult{notGit: true, dir: `C:\notes`}, `Not a git repository: C:\notes`},
		{diffResult{base: "origin/main"}, "No changes vs origin/main."},
		{diffResult{err: "git: boom"}, "Couldn't read the diff. git: boom"},
	} {
		l, _ := renderDiff(c.res)
		if s := ansi.Strip(strings.Join(l, "\n")); !strings.Contains(s, c.want) {
			t.Errorf("want %q in:\n%s", c.want, s)
		}
	}

	// A big diff is capped.
	big := diffFile{path: "big.txt", status: 'A'}
	for i := 0; i < maxDiffLines+10; i++ {
		big.body = append(big.body, "+x")
	}
	l, _ := renderDiff(diffResult{files: []diffFile{big}, truncated: true})
	if len(l) > maxDiffLines+3 {
		t.Errorf("%d lines, want at most %d", len(l), maxDiffLines+3)
	}
	if s := ansi.Strip(strings.Join(l, "\n")); !strings.Contains(s, "Only the first") || !strings.Contains(s, "only the start is shown") {
		t.Errorf("no cap notes")
	}
}

func TestDiffKeys(t *testing.T) {
	m := testModel("api")
	m.width, m.height = 120, 12 // shorter than the diff, so it scrolls
	m.diff = &diffView{name: "api", loading: true}
	res := diffResult{files: parseDiff([]byte(sampleDiff))}
	m.Update(diffLoadedMsg{view: &diffView{}, res: res}) // a stale load is ignored
	if !m.diff.loading {
		t.Fatal("a load for another view was applied")
	}
	m.Update(diffLoadedMsg{view: m.diff, res: res})
	if m.diff.loading || len(m.diff.starts) != 6 {
		t.Fatalf("loaded: %+v", m.diff.starts)
	}
	m.View() // sets rows
	v := m.diff
	press(m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if v.scroll != v.starts[0] {
		t.Errorf("n: scroll %d, want %d", v.scroll, v.starts[0])
	}
	press(m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if v.scroll != v.starts[1] {
		t.Errorf("n n: scroll %d, want %d", v.scroll, v.starts[1])
	}
	press(m, tea.KeyPressMsg{Code: 'N', Text: "N"})
	if v.scroll != v.starts[0] {
		t.Errorf("N: scroll %d, want %d", v.scroll, v.starts[0])
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	end := v.scroll
	if end != len(v.lines)-v.rows || end <= 0 {
		t.Errorf("end: scroll %d, lines %d, rows %d", end, len(v.lines), v.rows)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if v.scroll != end {
		t.Errorf("down past the end: %d", v.scroll)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if v.scroll != end-(v.rows-1) {
		t.Errorf("pgup: %d", v.scroll)
	}
	m.Update(tea.MouseWheelMsg{X: 80, Y: 10, Button: tea.MouseWheelUp})
	if v.scroll != end-(v.rows-1)-wheelLines {
		t.Errorf("wheel: %d", v.scroll)
	}
	// Other keys do nothing: it's read-only.
	press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.confirm != nil || m.focus != focusSidebar || m.diff == nil {
		t.Errorf("x/enter should be ignored")
	}
	screen := ansi.Strip(m.View().Content)
	if !strings.Contains(screen, "Diff  api") || !strings.Contains(screen, "esc close") {
		t.Errorf("screen:\n%s", screen)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.diff != nil {
		t.Errorf("esc should close it")
	}
}

// git runs git for a test, failing it on error.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	empty := filepath.Join(t.TempDir(), "gitconfig")
	_ = os.WriteFile(empty, nil, 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadDiff on a clone: the base is origin/HEAD, and the diff has the
// branch's commit, an uncommitted change and an untracked file, but not a
// commit made on main after the branch started.
func TestLoadDiff(t *testing.T) {
	isolateGit(t)
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "upstream")
	testGit(t, tmp, "init", "-q", "-b", "trunk", upstream)
	write(t, filepath.Join(upstream, "app.txt"), "one\ntwo\nthree\n")
	write(t, filepath.Join(upstream, "gone.txt"), "bye\n")
	testGit(t, upstream, "add", ".")
	testGit(t, upstream, "commit", "-q", "-m", "start")

	clone := filepath.Join(tmp, "clone")
	testGit(t, tmp, "clone", "-q", upstream, clone)
	testGit(t, clone, "switch", "-q", "-c", "feature")
	write(t, filepath.Join(clone, "app.txt"), "one\n2\nthree\n")
	testGit(t, clone, "commit", "-q", "-am", "change two")
	write(t, filepath.Join(clone, "app.txt"), "one\n2\nthree\nfour\n") // uncommitted
	_ = os.Remove(filepath.Join(clone, "gone.txt"))
	write(t, filepath.Join(clone, "sub", "new.txt"), "hello\nworld\n") // untracked
	write(t, filepath.Join(clone, ".gitignore"), "*.log\n")
	write(t, filepath.Join(clone, "skip.log"), "ignored\n")
	// Main moves on after the branch started: not part of the diff.
	write(t, filepath.Join(upstream, "later.txt"), "later\n")
	testGit(t, upstream, "add", ".")
	testGit(t, upstream, "commit", "-q", "-m", "later")
	testGit(t, clone, "fetch", "-q")

	res := loadDiff(filepath.Join(clone, "sub")) // a subfolder works too
	if res.err != "" || res.notGit {
		t.Fatalf("loadDiff: %+v", res)
	}
	if res.base != "origin/trunk" || res.mergeBase == "" {
		t.Errorf("base %q merge base %q", res.base, res.mergeBase)
	}
	got := map[string]diffFile{}
	for _, f := range res.files {
		got[f.path] = f
	}
	if f := got["app.txt"]; f.status != 'M' || f.adds != 2 || f.dels != 1 {
		t.Errorf("app.txt: %+v", f)
	}
	if f := got["gone.txt"]; f.status != 'D' || f.dels != 1 {
		t.Errorf("gone.txt: %+v", f)
	}
	if f := got["sub/new.txt"]; f.status != '?' || f.adds != 2 || len(f.body) != 3 || f.body[1] != "+hello" {
		t.Errorf("sub/new.txt: %+v", f)
	}
	if _, ok := got[".gitignore"]; !ok {
		t.Errorf("untracked .gitignore missing: %v", res.files)
	}
	for _, p := range []string{"later.txt", "skip.log"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s shouldn't be in the diff", p)
		}
	}

	// No remote: the local main.
	local := filepath.Join(tmp, "local")
	testGit(t, tmp, "init", "-q", "-b", "main", local)
	write(t, filepath.Join(local, "a.txt"), "a\n")
	testGit(t, local, "add", ".")
	testGit(t, local, "commit", "-q", "-m", "a")
	testGit(t, local, "switch", "-q", "-c", "work")
	write(t, filepath.Join(local, "a.txt"), "b\n")
	testGit(t, local, "commit", "-q", "-am", "b")
	res = loadDiff(local)
	if res.base != "main" || len(res.files) != 1 || res.files[0].adds != 1 || res.files[0].dels != 1 {
		t.Errorf("local: %+v", res)
	}

	// No commits yet: everything is new.
	fresh := filepath.Join(tmp, "fresh")
	testGit(t, tmp, "init", "-q", fresh)
	write(t, filepath.Join(fresh, "x.txt"), "x\n")
	res = loadDiff(fresh)
	if res.err != "" || len(res.files) != 1 || res.files[0].path != "x.txt" || res.note == "" {
		t.Errorf("fresh: %+v", res)
	}

	// Not git.
	if res := loadDiff(t.TempDir()); !res.notGit {
		t.Errorf("a plain folder: %+v", res)
	}
}

func TestUntrackedCaps(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "bin.dat"), "a\x00b")
	write(t, filepath.Join(dir, "long.txt"), strings.Repeat("line\n", maxUntrackedLines+5))
	write(t, filepath.Join(dir, "empty.txt"), "")
	if f := untrackedFile(dir, "bin.dat"); !f.binary || f.adds != 0 {
		t.Errorf("binary: %+v", f)
	}
	if f := untrackedFile(dir, "long.txt"); !f.truncated || f.adds != maxUntrackedLines {
		t.Errorf("long: adds %d truncated %v", f.adds, f.truncated)
	}
	if f := untrackedFile(dir, "empty.txt"); f.adds != 0 || len(f.body) != 1 {
		t.Errorf("empty: %+v", f)
	}
}

func TestGitDir(t *testing.T) {
	s := &Session{Dir: `C:\src\api`}
	if s.gitDir() != `C:\src\api` {
		t.Errorf("gitDir = %q", s.gitDir())
	}
}

// A notes start folder that isn't git, with a code repo and a worktree:
// the diff is the worktree's, never the notes folder's.
func TestDiffNotesFolder(t *testing.T) {
	clone := testRepo(t)
	w, err := createWorktree(worktreeConfig{}, clone, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, w.path, "feature.txt", "new\n")
	write(t, filepath.Join(w.path, "wip.txt"), "wip\n")
	notes := t.TempDir()
	write(t, filepath.Join(notes, "plan.md"), "notes\n")

	s := &Session{Name: w.branch, Dir: notes, CodeRepo: clone, RepoRoot: w.repoRoot, Worktree: w.path, Branch: w.branch}
	if s.gitDir() != w.path {
		t.Fatalf("gitDir = %q, want the worktree", s.gitDir())
	}
	m := &model{sessions: []*Session{s}}
	msg := m.openDiff()().(diffLoadedMsg)
	res := msg.res
	if res.notGit || res.err != "" || res.dir != w.path {
		t.Fatalf("loadDiff: %+v", res)
	}
	var paths []string
	for _, f := range res.files {
		paths = append(paths, f.path)
	}
	if strings.Join(paths, ",") != "feature.txt,wip.txt" {
		t.Errorf("files %q, want the worktree's commit and change", paths)
	}

	// A code repo without a worktree: its checkout.
	s = &Session{Dir: notes, CodeRepo: clone, RepoRoot: clone}
	if s.gitDir() != clone {
		t.Errorf("gitDir = %q, want the code repo", s.gitDir())
	}
}
