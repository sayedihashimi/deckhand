//go:build e2e

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// s cycles the state filter: active shows the running shell, inactive the
// restored one, waiting nothing. The header names the filter, and esc
// clears it.
func TestE2EStateFilter(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := t.TempDir()
	saved := fmt.Sprintf(`[{"id":"live-0001","name":"live","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z"},
{"id":"old-0001","name":"old","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z"}]`, t.TempDir(), t.TempDir())
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startHarness(t, exe, envWith(home, path), t.TempDir())

	h.waitFor("⏎ restart", 10*time.Second)
	h.send("\r") // start live, the first one
	h.waitFor("PS ", 15*time.Second)
	h.send(keyF5)
	h.waitFor("pwsh · idle", 10*time.Second)

	names := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			got := strings.Join(sidebarNames(h.screen()), ",")
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("sidebar %q, want %q; screen:\n%s", got, want, h.screen())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	names("live,old")

	h.send("s")
	h.waitFor("filter: active · esc clears", 3*time.Second)
	names("live")

	h.send("s")
	h.waitFor("filter: inactive", 3*time.Second)
	names("old")

	h.send("s")
	h.waitFor("filter: waiting", 3*time.Second)
	h.waitFor("No sessions match", 3*time.Second)

	h.send("s") // back to all
	names("live,old")
	if strings.Contains(h.screen(), "filter:") {
		t.Fatalf("all should show no filter; screen:\n%s", h.screen())
	}

	h.send("s")
	h.waitFor("filter: active", 3*time.Second)
	names("live")
	h.send("\x1b")
	h.waitGone("filter: active", 3*time.Second)
	names("live,old")
	t.Logf("screen:\n%s", h.screen())

	h.send("q")
	h.waitFor("Quit and end 1 running", 5*time.Second)
	h.send("y")
	h.waitExit(10 * time.Second)
}
