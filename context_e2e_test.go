//go:build e2e

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestE2EContextWindow runs a fake claude.exe (testdata/hookapp) whose
// status line payload says the window is 200k. The sidebar shows how full
// it is, the header the tokens and window size, and after the context
// drops sharply (a compaction), "↻ compacted" until the next reply.
func TestE2EContextWindow(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}
	dir := filepath.Join(t.TempDir(), "ctx")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "hookapp.txt"), []byte(`event SessionStart
reply 170000 Read the whole codebase.
statusline 2.00 200000 170000
event Stop
say hookapp full
sleep 5000
reply 30000 Compacted.
say hookapp compacted
sleep 5000
reply 34000 Next reply.
say hookapp next
`), 0o644)

	home := t.TempDir()
	path := bin + ";" + filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, envWith(home, path), t.TempDir())
	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send(dir)
	h.send("\r")
	h.waitFor("hookapp full", 20*time.Second)
	h.send(keyF5)
	h.waitFor("85%", 10*time.Second)                    // the sidebar
	h.waitFor("170k/200k context · 85%", 5*time.Second) // the header
	h.waitFor("↻ compacted", 15*time.Second)            // after the drop to 30k
	h.waitFor("15%", 5*time.Second)
	h.waitGone("↻ compacted", 15*time.Second) // the next reply
	h.waitFor("17%", 5*time.Second)
	t.Logf("screen:\n%s", h.screen())

	h.send("q")
	h.send("y")
	h.waitExit(10 * time.Second)
}
