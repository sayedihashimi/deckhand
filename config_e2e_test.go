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

// TestE2EConfigScreen adds a PowerShell agent through the config screen,
// picking a function from a test profile (never the real one), and checks
// it shows in the new-session dialog without a restart. Then it edits and
// deletes the agent, and checks that esc leaves without saving.
func TestE2EConfigScreen(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	tmp := t.TempDir()
	profile := filepath.Join(tmp, "profile.ps1")
	_ = os.WriteFile(profile, []byte(`Write-Host "hello from the test profile"
Write-Warning "a noisy profile"
function Start-Fake { param([string]$Resume) "fake $Resume" }
function Other-Thing { }
Set-Alias fk Start-Fake
`), 0o644)
	cfg := filepath.Join(tmp, "deckhand", "config.toml") // doesn't exist yet
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	env := append(envWith(t.TempDir(), path), "DECKHAND_CONFIG="+cfg, "DECKHAND_TEST_PROFILE="+profile)
	h := startHarness(t, exe, env, t.TempDir())
	read := func() string {
		data, _ := os.ReadFile(cfg)
		return string(data)
	}

	h.waitFor("press c to set up deckhand", 10*time.Second)
	h.send("c")
	h.waitFor("+ Add agent", 5*time.Second)
	h.send("\r") // + Add agent is the first row
	h.waitFor("New agent", 5*time.Second)
	h.send("\t\t") // Kind, then "Pick from your PowerShell profile"
	h.send("\r")
	h.waitFor("Start-Fake", 20*time.Second)
	if !strings.Contains(h.screen(), "Other-Thing") || strings.Contains(h.screen(), "Get-ChildItem") {
		t.Fatalf("picker should list the profile's functions only; screen:\n%s", h.screen())
	}
	h.send("Start-F")
	h.waitFor("has -Resume", 3*time.Second)
	h.send("\r")
	h.waitFor("Start-Fake -Resume {id}", 3*time.Second) // the suggested resume template
	h.send("\x15fake\r")                                // rename it on the Name field, then done
	h.waitFor("unsaved changes", 3*time.Second)
	t.Logf("before saving:\n%s", h.screen())
	h.send("\x13") // ctrl+s
	h.waitFor("Saved and applied", 5*time.Second)
	for _, want := range []string{`name = "fake"`, `pwsh = "Start-Fake"`, `type = "claude"`, `resume = "Start-Fake -Resume {id}"`} {
		if !strings.Contains(read(), want) {
			t.Fatalf("config.toml missing %q:\n%s", want, read())
		}
	}

	// The new agent is in the new-session dialog straight away.
	h.send("\x1b")
	h.waitFor("n new", 3*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.waitFor(" fake ", 3*time.Second)
	h.send("\x1b")

	// Edit it.
	h.send("c")
	h.waitFor("PowerShell Start-Fake", 5*time.Second)
	h.send("\r")
	h.waitFor(`Edit "fake"`, 3*time.Second)
	h.send("\x15fake2\r\x13")
	h.waitFor("config.toml.bak", 5*time.Second)
	if !strings.Contains(read(), `name = "fake2"`) {
		t.Fatalf("edit not saved:\n%s", read())
	}
	if bak, _ := os.ReadFile(cfg + ".bak"); !strings.Contains(string(bak), `name = "fake"`) {
		t.Fatalf(".bak should have the previous file:\n%s", bak)
	}

	// Delete it.
	h.send("d")
	h.waitFor("Deleted", 3*time.Second)
	h.send("\x13")
	h.waitFor("Saved and applied", 5*time.Second)
	if strings.Contains(read(), "fake2") {
		t.Fatalf("delete not saved:\n%s", read())
	}

	// esc leaves without saving (after asking, since there are changes).
	before := read()
	h.send("\x1b[B\r") // ↓ to notifications "Enabled", toggle it off
	h.waitFor("unsaved changes", 3*time.Second)
	h.send("\x1b")
	h.waitFor("discard your changes", 3*time.Second)
	h.send("y")
	h.waitFor("n new", 3*time.Second)
	if read() != before {
		t.Fatalf("esc shouldn't save; file now:\n%s", read())
	}
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	if strings.Contains(h.screen(), "fake") {
		t.Fatalf("deleted agent still offered; screen:\n%s", h.screen())
	}
	h.send("\x1b")
	h.send("q")
	h.waitExit(10 * time.Second)
}
