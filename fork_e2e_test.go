//go:build e2e

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

type savedFork struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ClaudeSessionID string `json:"claude_session_id"`
	ForkedFrom      string `json:"forked_from"`
	Started         bool   `json:"started"`
}

func readSaved(t *testing.T, home string) []savedFork {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []savedFork
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

// waitHookID waits until a session's latest hook event carries a Claude
// session ID, and returns it.
func waitHookID(t *testing.T, home, id string, timeout time.Duration) string {
	t.Helper()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		data, err := os.ReadFile(filepath.Join(home, "sessions", id, "status.json"))
		if err != nil {
			continue
		}
		var ev struct {
			ClaudeSessionID string `json:"claude_session_id"`
		}
		if json.Unmarshal(data, &ev) == nil && ev.ClaudeSessionID != "" {
			return ev.ClaudeSessionID
		}
	}
	t.Fatalf("no hook event for session %s", id)
	return ""
}

var forkedLine = regexp.MustCompile(`hookapp forked (\S+) as (\S+)`)

// f on an exited Claude session starts a fork below it: a second session
// with a different Claude session ID, made with --resume <id>
// --fork-session (testdata/hookapp checks the flags). The original stays
// resumable with its own ID, and both survive a restart.
func TestE2EFork(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}
	proj := filepath.Join(t.TempDir(), "proj-f")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only SessionStart, so the fork stays at "ready" and shows ⑂.
	if err := os.WriteFile(filepath.Join(proj, "hookapp.txt"), []byte("event SessionStart\nsay hookapp ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	const oldID = "11111111-2222-3333-4444-555555555555"
	saved := fmt.Sprintf(`[{"id":"orig-0001","name":"orig","agent":"claude","dir":%q,"created":"2026-01-01T00:00:00Z","claude_session_id":%q,"started":true}]`, proj, oldID)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	path := bin + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, envWith(home, path), t.TempDir())

	h.waitFor("⏎ resume · ⇧⏎ new", 10*time.Second)
	h.send("f")
	h.waitFor("hookapp ready", 15*time.Second)
	m := forkedLine.FindStringSubmatch(h.screen())
	if m == nil || m[1] != oldID || m[2] == oldID {
		t.Fatalf("want a fork of %s; screen:\n%s", oldID, h.screen())
	}
	newID := m[2]
	list := readSaved(t, home)
	if len(list) != 2 || list[0].ID != "orig-0001" || list[0].ClaudeSessionID != oldID {
		t.Fatalf("sessions: %+v", list)
	}
	fork := list[1]
	if fork.Name != "orig (fork)" || fork.ForkedFrom != "orig-0001" || fork.ClaudeSessionID != newID || !fork.Started {
		t.Fatalf("fork: %+v (hookapp got --session-id %s)", fork, newID)
	}
	if got := waitHookID(t, home, fork.ID, 10*time.Second); got != newID {
		t.Fatalf("the fork's hooks report session %s, want %s", got, newID)
	}

	h.send(keyF5)
	h.waitFor("⑂ orig", 5*time.Second)
	if got := sidebarNames(h.screen()); strings.Join(got, ",") != "orig,orig" { // the regexp stops at the space
		t.Fatalf("sidebar %q; screen:\n%s", got, h.screen())
	}
	t.Logf("after the fork:\n%s", h.screen())

	// The original resumes its own conversation, not the fork's.
	h.send("k\r")
	if got := waitHookID(t, home, "orig-0001", 15*time.Second); got != oldID {
		t.Fatalf("the original resumed %s, want %s", got, oldID)
	}
	h.waitFor("hookapp ready", 10*time.Second)
	if strings.Contains(h.screen(), "hookapp forked") {
		t.Fatalf("the original shouldn't fork; screen:\n%s", h.screen())
	}
	h.send(keyF5 + "q")
	h.waitFor("Quit and end 2 running", 5*time.Second)
	h.send("y")
	h.waitExit(15 * time.Second)

	// Both are still there after a restart, and the fork resumes its own
	// conversation rather than forking again.
	h = startHarness(t, exe, envWith(home, path), t.TempDir())
	h.waitFor("orig (fork)", 10*time.Second)
	list = readSaved(t, home)
	if len(list) != 2 || list[0].ClaudeSessionID != oldID || list[1].ClaudeSessionID != newID || list[1].ForkedFrom != "orig-0001" {
		t.Fatalf("sessions after a restart: %+v", list)
	}
	_ = os.Remove(filepath.Join(home, "sessions", fork.ID, "status.json"))
	h.send("j\r")
	if got := waitHookID(t, home, fork.ID, 15*time.Second); got != newID {
		t.Fatalf("the fork resumed %s, want %s", got, newID)
	}
	h.waitFor("hookapp ready", 10*time.Second)
	if strings.Contains(h.screen(), "hookapp forked") {
		t.Fatalf("a resumed fork shouldn't fork again; screen:\n%s", h.screen())
	}
	h.send(keyF5 + "q")
	h.send("y")
	h.waitExit(15 * time.Second)
}

// A PowerShell wrapper agent forks by running its resume command with the
// original's ID; the claude shim adds --fork-session and the new
// --session-id. f on an agent that isn't Claude only shows a message.
func TestE2EForkPwshWrapper(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/argsapp").CombinedOutput(); err != nil {
		t.Fatalf("build argsapp: %v\n%s", err, out)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte(`
[[agents]]
name   = "wrap"
pwsh   = "claude --name demo"
type   = "claude"
resume = "claude --resume {id}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	const oldID = "11111111-2222-3333-4444-555555555555"
	saved := fmt.Sprintf(`[{"id":"orig-0001","name":"orig","agent":"wrap","dir":%q,"created":"2026-01-01T00:00:00Z","claude_session_id":%q,"started":true},
{"id":"shell-0001","name":"shell","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z","started":true}]`, t.TempDir(), oldID, t.TempDir())
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	argsLog := filepath.Join(t.TempDir(), "args.jsonl")
	path := bin + ";" + filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	env := append(envWith(home, path), "DECKHAND_CONFIG="+cfg, "ARGSAPP_LOG="+argsLog)
	h := startHarness(t, exe, env, t.TempDir())

	h.waitFor("wrap · ⏎ resume", 10*time.Second)
	// Not Claude: a message, no new session.
	h.send("j")
	h.send("f")
	h.waitFor("Fork needs a Claude agent", 5*time.Second)
	if n := len(readSaved(t, home)); n != 2 {
		t.Fatalf("%d sessions after f on a shell", n)
	}

	h.send("k")
	h.send("f")
	h.waitFor("argsapp done", 20*time.Second)
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	var run struct{ Args []string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &run); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	list := readSaved(t, home)
	if len(list) != 3 || list[1].Name != "orig (fork)" || list[1].ForkedFrom != "orig-0001" {
		t.Fatalf("sessions: %+v", list)
	}
	newID := list[1].ClaudeSessionID
	if newID == "" || newID == oldID {
		t.Fatalf("the fork needs a new Claude ID: %+v", list[1])
	}
	args := run.Args
	i := slices.Index(args, "--resume")
	j := slices.Index(args, "--session-id")
	if i < 0 || i+1 >= len(args) || args[i+1] != oldID || j < 0 || j+1 >= len(args) || args[j+1] != newID || !slices.Contains(args, "--fork-session") || !slices.Contains(args, "--settings") {
		t.Fatalf("args %q; want --resume %s --fork-session --session-id %s", args, oldID, newID)
	}
	h.send(keyF5 + "q")
	h.send("y")
	h.waitExit(10 * time.Second)
}
