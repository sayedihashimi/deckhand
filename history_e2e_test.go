//go:build e2e

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// savedScreens lists the screen.ans files under a DECKHAND_HOME.
func savedScreens(t *testing.T, home string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(home, "sessions", "*", screenFile))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestE2ERestoreOutput prints a marker in a pwsh session, quits deckhand
// (which saves the session's output) and starts it again: the exited
// session shows its last output, which the wheel and shift+PgUp scroll, and
// enter restarts it.
func TestE2ERestoreOutput(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := t.TempDir()
	dir := t.TempDir()
	h := startHarness(t, exe, envWith(home, path), dir)

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send("\r")
	h.waitFor("PS ", 15*time.Second)
	// The command line doesn't contain the marker itself, only its output.
	h.send(`1..120 | % { "KEPT-$_" }; "KEPT-" + "MARKER"` + "\r")
	h.waitFor("KEPT-MARKER", 10*time.Second)
	time.Sleep(500 * time.Millisecond)
	h.send("\x11q")
	h.waitFor("Quit and end 1 running", 5*time.Second)
	h.send("y")
	h.waitExit(10 * time.Second)

	files := savedScreens(t, home)
	if len(files) != 1 {
		t.Fatalf("want one screen.ans, got %q", files)
	}
	if data, _ := os.ReadFile(files[0]); !strings.Contains(string(data), "KEPT-MARKER") || !strings.Contains(string(data), "KEPT-1\n") {
		t.Fatalf("screen.ans lacks the output:\n%s", data)
	}

	// Start again: the exited session shows its saved output.
	h = startHarness(t, exe, envWith(home, path), dir)
	h.waitFor("last output · saved", 10*time.Second)
	h.waitFor("KEPT-MARKER", 5*time.Second)
	t.Logf("restored:\n%s", h.screen())

	// The wheel over the pane scrolls it (3 lines a notch).
	h.wheel(true, 80, 20)
	h.waitFor("3 lines back", 3*time.Second)
	if strings.Contains(h.screen(), "KEPT-MARKER") {
		t.Fatalf("the marker should be scrolled out of view; screen:\n%s", h.screen())
	}
	h.wheel(false, 80, 20)
	h.waitFor("KEPT-MARKER", 3*time.Second)
	h.waitGone("lines back", 3*time.Second)

	// shift+PgUp from the list scrolls a page, shift+PgDn comes back.
	h.send("\x1b[5;2~")
	h.waitFor("lines back", 3*time.Second)
	h.waitGone("KEPT-MARKER", 3*time.Second)
	h.send("\x1b[6;2~")
	h.waitFor("KEPT-MARKER", 3*time.Second)
	h.waitGone("lines back", 3*time.Second)

	// enter starts it again: the live terminal replaces the saved output.
	h.send("\r")
	h.waitGone("last output", 10*time.Second)
	h.waitFor("PS ", 15*time.Second)
	h.send("\x11q")
	h.waitFor("Quit and end 1 running", 5*time.Second)
	h.send("y")
	h.waitExit(10 * time.Second)
}

// TestE2ERestoreClaudeConversation runs the fake claude.exe, quits and
// restarts deckhand: the exited session shows its last output, v switches
// to the conversation from the transcript and back, and with no saved
// screen the conversation shows by itself.
func TestE2ERestoreClaudeConversation(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}
	path := bin + ";" + filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo-conv")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	h := startHarness(t, exe, envWith(home, path), t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send(repo)
	h.send("\r")
	h.waitFor("hookapp done", 20*time.Second)
	h.send("\x11q")
	h.waitFor("Quit and end 1 running", 5*time.Second)
	h.send("y")
	h.waitExit(10 * time.Second)

	h = startHarness(t, exe, envWith(home, path), t.TempDir())
	h.waitFor("last output · saved", 10*time.Second)
	h.waitFor("v conversation", 5*time.Second)
	h.waitFor("hookapp done", 5*time.Second)
	h.send("v")
	h.waitFor("conversation · v last output", 5*time.Second)
	h.waitFor("> fix it", 5*time.Second)
	h.waitFor("Fixed the bug.", 5*time.Second)
	h.waitGone("hookapp done", 3*time.Second)
	t.Logf("conversation:\n%s", h.screen())
	h.send("v")
	h.waitFor("hookapp done", 5*time.Second)
	h.waitGone("> fix it", 3*time.Second)
	h.send("q") // nothing is running, so it quits straight away
	h.waitExit(10 * time.Second)

	// Without a saved screen, the conversation is the fallback.
	for _, f := range savedScreens(t, home) {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	h = startHarness(t, exe, envWith(home, path), t.TempDir())
	h.waitFor("last conversation (from the transcript", 10*time.Second)
	h.waitFor("> fix it", 5*time.Second)
	if strings.Contains(h.screen(), "v last output") {
		t.Errorf("there's no saved screen to switch to; screen:\n%s", h.screen())
	}
	h.send("q")
	h.waitExit(10 * time.Second)
}
