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

const (
	keyF7 = "\x1b[18~"
	keyF8 = "\x1b[19~"
)

// sideButton sends a press and release of the mouse's back (or forward)
// button at screen cell (x, y), 0-based, as SGR reports them: buttons 8
// and 9 are 128 and 129.
func (h *harness) sideButton(forward bool, x, y int) {
	b := 128
	if forward {
		b = 129
	}
	h.send(fmt.Sprintf("\x1b[<%d;%d;%dM\x1b[<%d;%d;%dm", b, x+1, y+1, b, x+1, y+1))
}

// headerLine is the session header: the first line, right of the list.
func (h *harness) headerLine() string {
	first, _, _ := strings.Cut(h.screen(), "\n")
	_, right, _ := strings.Cut(first, "│")
	return right
}

// waitSelected waits for the session header to show name, and the footer
// to show where the keys go: the session's terminal or the list.
func (h *harness) waitSelected(name string, inTerminal bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lines := strings.Split(strings.TrimRight(h.screen(), "\n"), "\n")
		// Only a session's footer has F5 (the list's says F3 list). A
		// flash ("No later session") hides the footer for a while.
		footer := lines[len(lines)-1]
		if strings.Contains(h.headerLine(), name) && strings.Contains(footer, "F5 list") == inTerminal && !strings.Contains(footer, " session") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("want %s selected (in its terminal: %v); screen:\n%s", name, inTerminal, h.screen())
}

// typeIn types an echo into the focused session and waits for its output,
// which shows the keys reach that session's terminal.
func (h *harness) typeIn(word string) {
	h.t.Helper()
	h.send(fmt.Sprintf(`echo ("%s" + "%s")`, word[:2], word[2:]) + "\r")
	h.waitFor(word, 10*time.Second)
}

// TestE2EHistory visits three running sessions, then goes back twice and
// forward once inside a session with F7 and F8, and in the list with the
// mouse's side buttons, checking the selected session and the focus.
func TestE2EHistory(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, envWith(savedThree(t), path), t.TempDir())
	h.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")

	// Visit alpha, bravo and charlie, starting each.
	for i, n := range []string{"alpha", "bravo", "charlie"} {
		h.send(fmt.Sprint(i + 1)) // jump into session N
		h.waitSelected("sess-"+n, true)
		h.waitFor("PS ", 15*time.Second)
		h.typeIn("at" + n)
		if n != "charlie" {
			h.send(keyF5)
			h.waitSelected("sess-"+n, false)
		}
	}

	// Inside charlie: back twice and forward once, staying in a terminal.
	h.send(keyF7)
	h.waitSelected("sess-bravo", true)
	h.send(keyF7)
	h.waitSelected("sess-alpha", true)
	h.send(keyF8)
	h.waitSelected("sess-bravo", true)
	h.typeIn("inbravo") // the keys go to bravo
	h.send(keyF8)
	h.waitSelected("sess-charlie", true)
	h.send(keyF8) // the end
	h.waitFor("No later session", 5*time.Second)
	h.waitSelected("sess-charlie", true)

	// In the list, the side buttons do the same, and focus stays there.
	h.send(keyF5)
	h.waitSelected("sess-charlie", false)
	h.sideButton(false, 5, 10)
	h.waitSelected("sess-bravo", false)
	h.sideButton(false, 5, 10)
	h.waitSelected("sess-alpha", false)
	h.sideButton(true, 5, 10)
	h.waitSelected("sess-bravo", false)

	// Over the terminal, the back button lands in alpha's terminal.
	h.send("\r") // into bravo
	h.waitSelected("sess-bravo", true)
	h.sideButton(false, 100, 10)
	h.waitSelected("sess-alpha", true)
	h.typeIn("inalpha")

	// alt+←/→ inside a session go to the shell, not the history.
	h.send("\x1b\x1b[C") // alt+→
	h.typeIn("stillalpha")
	h.waitSelected("sess-alpha", true)
	// In the list they go back and forward.
	h.send(keyF5)
	h.waitSelected("sess-alpha", false)
	h.send("\x1b\x1b[C") // alt+→
	h.waitSelected("sess-bravo", false)
	h.send("\x1b\x1b[D") // alt+←
	h.waitSelected("sess-alpha", false)
	t.Logf("screen:\n%s", h.screen())

	h.send("q")
	h.send("y")
	h.waitExit(10 * time.Second)
}
