//go:build e2e

// e2e tests for WezTerm on Windows, which (like Windows Terminal) sends keys
// as win32-input-mode records, and for terminals with shift+enter remapped
// to send ESC CR.
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

func weztermEnv(home, path string) []string {
	return append(envWith(home, path), "TERM_PROGRAM=WezTerm")
}

// savedPwshSession writes a sessions.json with one exited pwsh session in a
// folder named proj-a, and returns the deckhand home.
func savedPwshSession(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	proj := filepath.Join(t.TempDir(), "proj-a")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	saved := fmt.Sprintf(`[{"id":"orig-0001","name":"orig","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z","started":true}]`, proj)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// listShown waits until the session list is shown (or hidden).
func (h *harness) listShown(shown bool) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		top := strings.SplitN(h.screen(), "\n", 2)[0]
		if strings.Contains(top, "deckhand") == shown && (shown || strings.Contains(top, `ctrl+\ list`)) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("list shown should be %v; screen:\n%s", shown, h.screen())
}

// TestE2EWezTermKeys runs deckhand as if in WezTerm (TERM_PROGRAM=WezTerm),
// so it turns on win32-input-mode. It sends ctrl+\ the way WezTerm was seen
// to deliver it: the ctrl press, then only the ctrl+\ release (the press
// never arrives), then the ctrl release. And shift+enter as records.
func TestE2EWezTermKeys(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := savedPwshSession(t)
	h := startHarness(t, exe, weztermEnv(home, path), t.TempDir())
	res, _, _ := procVkKeyScan.Call(uintptr('\\'))
	vkBackslash := int(res & 0xff)
	weztermCtrlBackslash := func() {
		h.send(win32Key(vkControl, scanCode(vkControl), 0, wtCtrl, true) +
			win32Key(vkBackslash, scanCode(vkBackslash), 0x1c, wtCtrl, false) +
			win32Key(vkControl, scanCode(vkControl), 0, 0, false))
	}

	h.waitFor("⏎ restart · ⇧⏎ new", 10*time.Second)
	weztermCtrlBackslash()
	h.listShown(false)
	weztermCtrlBackslash()
	h.listShown(true)

	h.wtKey(vkReturn, '\r', wtShift)
	h.waitFor("PS ", 15*time.Second)
	if got := sessionOrder(t, home); len(got) != 2 || got[0] != "orig|proj-a|pwsh|" || got[1] != "proj-a|proj-a|pwsh|" {
		t.Fatalf("after shift+enter: %q; screen:\n%s", got, h.screen())
	}

	h.wtCtrlQ()
	h.waitFor("n new", 5*time.Second)
	h.wtType("q")
	h.wtType("y")
	h.waitExit(10 * time.Second)
}

// TestE2EEscEnterInList sends ESC CR, what a terminal with shift+enter
// remapped to it sends (Windows Terminal's sendInput "\u001b\r", a common
// setup for Claude Code). In the list it starts a new session like the
// selected one, as shift+enter does.
func TestE2EEscEnterInList(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := savedPwshSession(t)
	h := startHarness(t, exe, envWith(home, path), t.TempDir())

	h.waitFor("⏎ restart · ⇧⏎ new", 10*time.Second)
	h.send("\x1b\r")
	h.waitFor("PS ", 15*time.Second)
	if got := sessionOrder(t, home); len(got) != 2 || got[1] != "proj-a|proj-a|pwsh|" {
		t.Fatalf("after ESC CR: %q; screen:\n%s", got, h.screen())
	}

	h.send("\x11") // ctrl+q
	h.waitFor("n new", 5*time.Second)
	h.send("q")
	h.send("y")
	h.waitExit(10 * time.Second)
}
