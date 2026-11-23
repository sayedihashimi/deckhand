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
	h.send("\x1b[B ") // ↓ to notifications "Enabled", space toggles it off
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

// TestE2EConfigHelp opens the help with F1 on the config screen: the config
// screen's keys are the first group, and esc goes back to the screen with
// the unsaved edit still there.
func TestE2EConfigHelp(t *testing.T) {
	exe := buildDeckhand(t)
	cfg := filepath.Join(t.TempDir(), "config.toml") // doesn't exist yet
	env := append(envWith(t.TempDir(), ""), "DECKHAND_CONFIG="+cfg)
	h := startHarnessSize(t, exe, env, t.TempDir(), 120, 32)

	h.waitFor("press c to set up deckhand", 10*time.Second)
	h.send("c")
	h.waitFor("+ Add agent", 5*time.Second)
	// Down to the editor command (+ Add agent, 3 notification rows, 6 list
	// rows, 2 history rows) and type into it.
	h.send(strings.Repeat("\x1b[B", 12))
	h.send("e2e-editor {dir}")
	h.waitFor("e2e-editor {dir}", 3*time.Second)
	// It has to be the editor field, not another text field on the way.
	for _, line := range strings.Split(h.screen(), "\n") {
		if strings.Contains(line, "e2e-editor {dir}") && !strings.Contains(line, "Editor") {
			t.Fatalf("typed into the wrong field: %q\n%s", line, h.screen())
		}
	}
	h.waitFor("unsaved changes", 3*time.Second)
	h.waitFor("F1 help", 3*time.Second) // the config screen's footer

	h.send("\x1bOP") // F1
	h.waitFor("Keys · Config screen (c)", 5*time.Second)
	screen := h.screen()
	t.Logf("help from the config screen:\n%s", screen)
	rows := strings.Split(screen, "\n")
	title, group, list := -1, -1, -1
	for i, r := range rows {
		switch {
		case title < 0 && strings.Contains(r, "Keys · Config screen (c)"):
			title = i
		case group < 0 && strings.Contains(r, "│ Config screen (c)"):
			group = i
		}
		if list < 0 && strings.Contains(r, "Session list") {
			list = i
		}
	}
	if title < 0 || group != title+2 {
		t.Fatalf("the config keys should be the first group (title %d, group %d):\n%s", title, group, screen)
	}
	for _, want := range []string{"ctrl+s", "save and apply", "leave without saving", "shift+↑↓", "form: done / cancel", "picker: choose, pick"} {
		if !strings.Contains(screen, want) {
			t.Errorf("help is missing %q", want)
		}
	}
	if list >= 0 && list < group {
		t.Errorf("Session list is above the config keys:\n%s", screen)
	}
	if !strings.Contains(screen, "more group") {
		t.Errorf("last line should say how many more groups:\n%s", screen)
	}

	// esc closes the help, not the config screen, and the edit is kept.
	h.send("\x1b")
	h.waitGone("Keys · Config screen (c)", 5*time.Second)
	h.waitFor("e2e-editor {dir}", 3*time.Second)
	h.waitFor("unsaved changes", 3*time.Second)
	if strings.Contains(h.screen(), "discard your changes") {
		t.Fatalf("esc in the help reached the config screen:\n%s", h.screen())
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatalf("nothing should be saved yet")
	}

	// The edit is still unsaved: esc now asks before discarding it.
	h.send("\x1b")
	h.waitFor("discard your changes", 3*time.Second)
	h.send("y")
	h.waitFor("press c to set up deckhand", 5*time.Second)
	h.send("q")
	h.waitExit(10 * time.Second)
}

// TestE2EConfigTestAgent tests agents from the config screen. hookapp, a
// fake claude.exe that sends hooks, reports "hooks work", and is kept. An
// unsaved agent running argsapp, which never sends hooks, reports "no hook
// events yet" and is discarded. config.toml doesn't change until ctrl+s.
func TestE2EConfigTestAgent(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	for _, app := range []string{"hookapp", "argsapp"} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, app+".exe"), "./testdata/"+app).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", app, err, out)
		}
	}
	home := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	text := "[[agents]]\nname = \"hooky\"\ncommand = '" + filepath.Join(bin, "hookapp.exe") + "'\ntype = \"claude\"\n"
	if err := os.WriteFile(cfg, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		data, _ := os.ReadFile(cfg)
		return string(data)
	}
	dir := t.TempDir()
	h := startHarnessSize(t, exe, append(envWith(home, ""), "DECKHAND_CONFIG="+cfg), dir, 140, 40)

	// hookapp: the first agent row is selected.
	h.waitFor("No sessions yet", 10*time.Second)
	h.send("c")
	h.waitFor("hooky", 5*time.Second)
	h.send("t")
	h.waitFor("test · hooky", 5*time.Second)
	h.waitFor("✓ hooks work (SessionStart received)", 30*time.Second)
	t.Logf("hooks work:\n%s", h.screen())
	h.send("\x14") // ctrl+t
	h.waitFor("k keep it as a session", 3*time.Second)
	h.send("k")
	h.waitFor("Kept the test session as", 3*time.Second)
	h.waitGone("test · hooky", 3*time.Second)
	if !strings.Contains(h.screen(), filepath.Base(dir)) {
		t.Fatalf("the kept session should be named after its folder:\n%s", h.screen())
	}

	// An unsaved agent: argsapp, as a Claude program, sends no hooks.
	h.send("a")
	h.waitFor("New agent", 3*time.Second)
	h.send("noargs")
	h.send("\t ") // kind: program
	h.send("\t" + filepath.Join(bin, "argsapp.exe"))
	h.send("\t\t ") // type: Claude Code
	h.send("\t")    // [Test]
	h.waitFor("in a test session", 3*time.Second)
	h.send("\r")
	h.waitFor("test · noargs", 5*time.Second)
	h.waitFor("waiting for Claude's SessionStart hook…", 5*time.Second)
	h.waitFor("✗ no hook events yet", 45*time.Second)
	h.waitFor("must pass deckhand's arguments", 3*time.Second)
	h.waitFor("argsapp done", 3*time.Second) // its last output
	t.Logf("no hooks:\n%s", h.screen())
	if read() != text {
		t.Fatalf("config.toml shouldn't change before ctrl+s:\n%s", read())
	}

	// Discard it, then add the agent and save.
	h.send("\x14")
	h.waitFor("x discard it", 3*time.Second)
	h.send("x")
	h.waitFor("Discarded the test session", 5*time.Second)
	h.waitGone("test · noargs", 3*time.Second)
	if logs, _ := filepath.Glob(filepath.Join(home, "sessions", "*")); len(logs) == 0 {
		t.Log("no session folders")
	}
	h.send("\x1b[Z\r") // back to Type, then done
	h.waitFor("unsaved changes", 3*time.Second)
	if read() != text {
		t.Fatalf("config.toml shouldn't change before ctrl+s:\n%s", read())
	}
	h.send("\x13")
	h.waitFor("Saved and applied", 5*time.Second)
	if !strings.Contains(read(), `name = "noargs"`) {
		t.Fatalf("not saved:\n%s", read())
	}
	data, _ := os.ReadFile(filepath.Join(home, "sessions.json"))
	if strings.Contains(string(data), "test ·") || !strings.Contains(string(data), `"agent": "hooky"`) {
		t.Errorf("sessions.json should have the kept session only:\n%s", data)
	}

	h.send("\x1b")
	h.waitFor("n new", 3*time.Second)
	h.send("q")
	h.waitFor("y/n", 3*time.Second)
	h.send("y")
	h.waitExit(15 * time.Second)
}
