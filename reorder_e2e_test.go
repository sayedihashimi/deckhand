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

// savedThree writes a sessions.json with three exited pwsh sessions,
// sess-alpha, sess-bravo and sess-charlie, each in its own folder, and
// returns the deckhand home.
func savedThree(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	type saved struct {
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Agent   string    `json:"agent"`
		Dir     string    `json:"dir"`
		Created time.Time `json:"created"`
	}
	var list []saved
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		dir := filepath.Join(t.TempDir(), n)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		list = append(list, saved{"reorder-" + n, "sess-" + n, "pwsh", dir, time.Now()})
	}
	data, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// sidebarOrder is the order the sessions show in the list, read from the
// sidebar's column of the screen.
func (h *harness) sidebarOrder(names ...string) []string {
	type at struct {
		name string
		line int
	}
	var found []at
	for i, line := range strings.Split(h.screen(), "\n") {
		left, _, _ := strings.Cut(line, "│") // the sidebar's edge
		for _, n := range names {
			if strings.Contains(left, n) {
				found = append(found, at{n, i})
			}
		}
	}
	out := make([]string, 0, len(found))
	for _, f := range found { // lines are in screen order already
		out = append(out, f.name)
	}
	return out
}

// waitOrder waits for the list to show the sessions in this order.
func (h *harness) waitOrder(want ...string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Join(h.sidebarOrder(want...), " ") == strings.Join(want, " ") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("list order %q, want %q; screen:\n%s", h.sidebarOrder(want...), want, h.screen())
}

func savedOrder(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	return strings.Join(names, " ")
}

// TestE2EMoveSessions moves sessions with shift+↑ and shift+end, checks the
// list's order and that the running session's terminal keeps showing, then
// restarts deckhand and checks the order again.
func TestE2EMoveSessions(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := savedThree(t)
	h := startHarness(t, exe, envWith(home, path), t.TempDir())
	h.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")

	// Start charlie and leave something on its screen.
	h.send("\x1b[F") // end
	h.send("\r")
	h.waitFor("PS ", 15*time.Second)
	h.send(`echo ("re" + "order" + 7)` + "\r")
	h.waitFor("reorder7", 10*time.Second)
	h.send("\x11") // ctrl+q back to the list
	h.waitFor("n new", 5*time.Second)

	h.send("\x1b[1;2A") // shift+up
	h.waitOrder("sess-alpha", "sess-charlie", "sess-bravo")
	if !strings.Contains(h.screen(), "reorder7") {
		t.Fatalf("charlie's terminal should still show after the move; screen:\n%s", h.screen())
	}
	if got := savedOrder(t, home); got != "sess-alpha sess-charlie sess-bravo" {
		t.Errorf("saved after shift+up: %q", got)
	}

	h.send("\x1b[H")    // home: select alpha
	h.send("\x1b[1;2F") // shift+end
	h.waitOrder("sess-charlie", "sess-bravo", "sess-alpha")
	if got := savedOrder(t, home); got != "sess-charlie sess-bravo sess-alpha" {
		t.Errorf("saved after shift+end: %q", got)
	}
	// shift+end at the bottom does nothing.
	h.send("\x1b[1;2F")
	h.waitOrder("sess-charlie", "sess-bravo", "sess-alpha")
	t.Logf("screen:\n%s", h.screen())

	h.send("q")
	h.waitFor("y/n", 5*time.Second) // charlie is running
	h.send("y")
	h.waitExit(15 * time.Second)

	// The order survives a restart.
	h2 := startHarness(t, exe, envWith(home, path), t.TempDir())
	h2.waitOrder("sess-charlie", "sess-bravo", "sess-alpha")
}

// TestE2EWTMoveSessions sends the move keys as Windows Terminal does in
// win32-input-mode: shift+↑ and shift+home as key records.
func TestE2EWTMoveSessions(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := savedThree(t)
	h := startHarness(t, exe, wtEnv(home, path), t.TempDir())
	h.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")

	const vkEnd, vkHome, vkDown = 0x23, 0x24, 0x28
	h.wtKey(vkDown, 0, wtEnhanced) // select bravo
	h.wtKey(vkUp, 0, wtShift|wtEnhanced)
	h.waitOrder("sess-bravo", "sess-alpha", "sess-charlie")
	h.wtKey(vkEnd, 0, wtEnhanced) // select charlie
	h.wtKey(vkHome, 0, wtShift|wtEnhanced)
	h.waitOrder("sess-charlie", "sess-bravo", "sess-alpha")
	if got := savedOrder(t, home); got != "sess-charlie sess-bravo sess-alpha" {
		t.Errorf("saved: %q", got)
	}
}
