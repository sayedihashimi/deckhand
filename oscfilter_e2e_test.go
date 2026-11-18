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

// TestE2EIdleTitle has the fake claude.exe draw an input row and then set
// Claude's idle window title, ESC ] 0 ; ✳ name BEL, with the cursor in that
// row. The ✳ has a 0x9C byte, which x/ansi took as the end of the title, so
// the rest of the title used to be printed in the input box (issue #37).
func TestE2EIdleTitle(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}

	dir := filepath.Join(t.TempDir(), "idle-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "event SessionStart\n" +
		"say reply text\n" +
		"print \\e[8;1Hinput> \n" +
		"print \\e]0;✳ title-probe\\a\n" +
		"event Stop\n" +
		"print \\e[12;1Hprobe done\\e[8;8H\n"
	if err := os.WriteFile(filepath.Join(dir, "hookapp.txt"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	path := bin + ";" + filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	// Wide enough for the header to show the title after the long temp path.
	h := startHarnessSize(t, exe, envWith(home, path), t.TempDir(), 220, 40)
	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send(dir)
	h.send("\r")
	h.waitFor("probe done", 20*time.Second)
	time.Sleep(500 * time.Millisecond)

	screen := h.screen()
	var row string
	found := false
	for _, l := range strings.Split(screen, "\n") {
		if _, after, ok := strings.Cut(l, "input>"); ok {
			row, found = after, true
		}
	}
	if !found {
		t.Fatalf("no input row; screen:\n%s", screen)
	}
	if strings.Contains(row, "title-probe") {
		t.Fatalf("the window title was printed in the input row %q; screen:\n%s", row, screen)
	}
	// The title itself still reaches the header, without the ✳.
	if !strings.Contains(screen, "— title-probe") {
		t.Fatalf("the header should show the title; screen:\n%s", screen)
	}
	t.Logf("screen:\n%s", screen)
}
