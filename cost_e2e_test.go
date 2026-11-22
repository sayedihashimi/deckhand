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

// TestE2ECostStatusLine runs a fake claude.exe (testdata/hookapp) that
// writes a reply to its transcript, then runs the statusLine command from
// the --settings file with a payload that says the session cost $1.84.
// The sidebar shows the transcript estimate first (~$), then Claude's
// number, and the list header shows today's total. The user's own status
// line (from CLAUDE_CONFIG_DIR's settings.json) is run and its output
// passed back. The last cost is saved in sessions.json.
func TestE2ECostStatusLine(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}
	dir := filepath.Join(t.TempDir(), "costly")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "hookapp.txt"), []byte(`event SessionStart
reply 50000 Done.
event UserPromptSubmit
say hookapp replied
sleep 5000
statusline 1.84 200000 50000
event Stop
say hookapp done
`), 0o644)

	// The user's own status line: echo works in Git Bash and PowerShell.
	claudeCfg := t.TempDir()
	_ = os.WriteFile(filepath.Join(claudeCfg, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"echo from-user-line","padding":1}}`), 0o644)

	home := t.TempDir()
	path := bin + ";" + filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	if git, err := exec.LookPath("git"); err == nil {
		path += ";" + filepath.Dir(git) // for Git Bash, which runs the user's status line
	}
	env := append(envWith(home, path), "CLAUDE_CONFIG_DIR="+claudeCfg)
	h := startHarness(t, exe, env, t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send(dir)
	h.send("\r")
	h.waitFor("hookapp replied", 20*time.Second)
	// Before the status line: the estimate from the transcript.
	h.waitFor("~$0.01", 10*time.Second)
	h.waitFor("hookapp done", 20*time.Second)
	h.waitFor("statusline: from-user-line", 5*time.Second)
	h.send(keyF5)
	h.waitFor("$1.84 today", 10*time.Second) // the header
	h.waitFor("25% · $1.84", 5*time.Second)  // the third line
	if strings.Contains(h.screen(), "~$1.84") {
		t.Errorf("Claude's own number isn't an estimate:\n%s", h.screen())
	}
	t.Logf("screen:\n%s", h.screen())

	// deckhand's statusLine is in the settings file, with the user's padding.
	matches, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "claude-settings.json"))
	if len(matches) != 1 {
		t.Fatalf("settings files: %v", matches)
	}
	settings, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(settings), `statusline --session`) || !strings.Contains(string(settings), `"padding": 1`) {
		t.Errorf("settings file:\n%s", settings)
	}

	h.send("q")
	h.send("y")
	h.waitExit(10 * time.Second)
	saved, _ := os.ReadFile(filepath.Join(home, "sessions.json"))
	if !strings.Contains(string(saved), `"usd": 1.84`) {
		t.Errorf("sessions.json lacks the cost:\n%s", saved)
	}
}
