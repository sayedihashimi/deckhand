//go:build e2e

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Claude session's name follows Claude's title: the automatic title
// first, then a /rename (written twice, as on resume; the copy doesn't
// count again). The name and its source are saved, and a restart, which
// reads the transcript from the start again, keeps them.
func TestE2EClaudeTitleName(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}
	proj := filepath.Join(t.TempDir(), "proj-t")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.Join([]string{
		"event SessionStart",
		"title ai Fix the login bug",
		"say hookapp titled",
		"sleep 5000",
		"title custom auth-work",
		"title custom auth-work",
		"say hookapp renamed",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(proj, "hookapp.txt"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	saved := fmt.Sprintf(`[{"id":"title-0001","name":"proj-t","agent":"claude","dir":%q,"created":"2026-01-01T00:00:00Z","claude_session_id":"11111111-2222-3333-4444-555555555555","started":true}]`, proj)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	type savedName struct {
		Name       string `json:"name"`
		NameSource string `json:"name_source"`
	}
	waitSaved := func(name, src string) {
		t.Helper()
		var got []savedName
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			data, _ := os.ReadFile(filepath.Join(home, "sessions.json"))
			if json.Unmarshal(data, &got) == nil && len(got) == 1 && got[0].Name == name && got[0].NameSource == src {
				return
			}
		}
		t.Fatalf("sessions.json: %+v, want %q (%s)", got, name, src)
	}
	path := bin + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, envWith(home, path), t.TempDir())

	h.waitFor("⏎ resume · ⇧⏎ new", 10*time.Second)
	h.send("\r")
	h.waitFor("hookapp titled", 15*time.Second)
	h.send(keyF5)
	h.waitFor("Fix the login bug", 10*time.Second)
	waitSaved("Fix the login bug", nameAI)

	h.waitFor("hookapp renamed", 15*time.Second)
	h.waitFor("auth-work", 10*time.Second)
	waitSaved("auth-work", nameClaude)
	if got := sidebarNames(h.screen()); strings.Join(got, ",") != "auth-work" {
		t.Fatalf("sidebar %q; screen:\n%s", got, h.screen())
	}
	h.send("q")
	h.waitFor("Quit and end 1 running", 5*time.Second)
	h.send("y")
	h.waitExit(15 * time.Second)

	// After a restart the name stays, though the transcript's ai-title is
	// read again.
	h = startHarness(t, exe, envWith(home, path), t.TempDir())
	h.waitFor("auth-work", 10*time.Second)
	time.Sleep(4 * time.Second) // a poll or two
	if s := h.screen(); strings.Contains(s, "Fix the login bug") {
		t.Fatalf("the old ai-title came back:\n%s", s)
	}
	waitSaved("auth-work", nameClaude)
	h.send("q")
	h.waitExit(15 * time.Second)
}
