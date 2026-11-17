//go:build e2e

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EDiffPreview opens the diff preview (d) on a temporary repo with a
// commit on a branch, an uncommitted change and an untracked file, jumps
// between files with n and N, scrolls, and closes it with esc. A session
// outside git gets a message instead. Neither session is started.
func TestE2EDiffPreview(t *testing.T) {
	isolateGit(t)
	exe := buildDeckhand(t)
	gitExe, _ := exec.LookPath("git")

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	testGit(t, tmp, "init", "-q", "-b", "main", repo)
	write(t, filepath.Join(repo, "app.txt"), "one\ntwo\nthree\n")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-q", "-m", "start")
	testGit(t, repo, "switch", "-q", "-c", "feature")
	write(t, filepath.Join(repo, "app.txt"), "one\nCOMMITTED-TWO\nthree\n")
	testGit(t, repo, "commit", "-q", "-am", "change two")
	write(t, filepath.Join(repo, "app.txt"), "one\nCOMMITTED-TWO\nthree\nUNCOMMITTED-FOUR\n")
	var long strings.Builder
	for i := 0; i < 80; i++ {
		long.WriteString("UNTRACKED-LINE\n")
	}
	long.WriteString("UNTRACKED-END\n")
	write(t, filepath.Join(repo, "notes.md"), long.String())
	plain := filepath.Join(tmp, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	type saved struct {
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Agent   string    `json:"agent"`
		Dir     string    `json:"dir"`
		Created time.Time `json:"created"`
	}
	data, _ := json.Marshal([]saved{
		{"diff-repo", "repo", "pwsh", repo, time.Now()},
		{"diff-plain", "plain", "pwsh", plain, time.Now()},
	})
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Dir(gitExe) + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, envWith(home, path), tmp)

	h.waitFor("plain", 10*time.Second)
	h.send("d")
	h.waitFor("2 files changed", 10*time.Second)
	h.waitFor("vs main (merge base", 3*time.Second)
	h.waitFor("M  app.txt", 3*time.Second)
	h.waitFor("?  notes.md", 3*time.Second)
	h.waitFor("+2 −1", 3*time.Second) // app.txt: the commit and the uncommitted line
	h.waitFor("+COMMITTED-TWO", 3*time.Second)
	h.waitFor("+UNCOMMITTED-FOUR", 3*time.Second)
	h.waitFor("esc close", 3*time.Second)
	t.Logf("diff:\n%s", h.screen())

	// n jumps to the next file's section: notes.md at the top.
	h.send("n")
	h.waitGone("2 files changed", 3*time.Second)
	h.send("n")
	h.waitGone("+COMMITTED-TWO", 3*time.Second)
	h.waitFor("? notes.md  +81 −0  untracked", 3*time.Second)
	// The end of the untracked file, then back to the top.
	h.send("\x1b[F") // end
	h.waitFor("UNTRACKED-END", 3*time.Second)
	h.send("\x1b[H") // home
	h.waitFor("2 files changed", 3*time.Second)
	// The wheel scrolls it.
	h.wheel(false, 80, 20)
	h.waitGone("2 files changed", 3*time.Second)
	h.wheel(true, 80, 20)
	h.waitFor("2 files changed", 3*time.Second)
	// N from the end goes back a file.
	h.send("\x1b[F")
	h.send("N")
	h.waitFor("? notes.md  +81 −0", 3*time.Second)

	// Read-only: x doesn't ask to remove the session.
	h.send("x")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(h.screen(), "Remove") {
		t.Fatalf("x reached the list:\n%s", h.screen())
	}
	h.send("\x1b")
	h.waitGone("esc close", 3*time.Second)
	h.waitFor("? help", 3*time.Second) // the list's footer

	// A folder that isn't in git.
	h.send("j")
	h.send("d")
	h.waitFor("Not a git repository: "+plain, 5*time.Second)
	h.send("\x1b")
	h.waitGone("Not a git repository", 3*time.Second)
	h.send("q") // nothing is running, so it quits straight away
	h.waitExit(10 * time.Second)
}
