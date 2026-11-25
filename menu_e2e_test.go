//go:build e2e

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// rightClick sends an SGR right-button press and release at screen cell
// (x, y), 0-based, as Windows Terminal and WezTerm report it with mouse
// tracking on.
func (h *harness) rightClick(x, y int) {
	h.send(fmt.Sprintf("\x1b[<2;%d;%dM\x1b[<2;%d;%dm", x+1, y+1, x+1, y+1))
}

// menuRename right-clicks sess-bravo, checks the menu, picks Rename with a
// click and checks the rename box. esc (sent by esc) closes it.
func menuRename(t *testing.T, h *harness, esc func()) {
	t.Helper()
	h.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")
	_, y := h.screenPos("sess-bravo")
	if y < 0 {
		t.Fatalf("no sess-bravo:\n%s", h.screen())
	}
	h.rightClick(6, y)
	h.waitFor("Open terminal", 5*time.Second)
	for _, item := range []string{"Color…", "Rename", "Fork", "Group…", "Open folder", "Remove…"} {
		if !strings.Contains(h.screen(), item) {
			t.Errorf("no %q in the menu:\n%s", item, h.screen())
		}
	}
	if strings.Contains(h.screen(), "Create PR") {
		t.Errorf("Create PR on a session without a worktree:\n%s", h.screen())
	}
	t.Logf("menu:\n%s", h.screen())
	x, ry := h.screenPos("Rename ")
	if x < 0 {
		t.Fatalf("no Rename:\n%s", h.screen())
	}
	h.click(x+1, ry)
	h.waitFor("Rename: sess-bravo", 5*time.Second)
	h.waitGone("Open terminal", 3*time.Second)
	esc()
	h.waitGone("Rename: ", 3*time.Second)
}

// TestE2EMenuRename right-clicks a session, picks Rename and checks the
// rename box opens for it.
func TestE2EMenuRename(t *testing.T) {
	exe := buildDeckhand(t)
	path := e2ePwshPath(t)
	home := savedThree(t)
	h := startHarness(t, exe, envWith(home, path), t.TempDir())
	menuRename(t, h, func() { h.send("\x1b") })

	// esc closes the menu; the left drag still reorders.
	_, y := h.screenPos("sess-alpha")
	h.rightClick(6, y)
	h.waitFor("Open terminal", 5*time.Second)
	h.send("\x1b")
	h.waitGone("Open terminal", 3*time.Second)
	h.drag(6, y, y+3, y+5)
	h.waitOrder("sess-bravo", "sess-alpha", "sess-charlie")
	h.send("q")
	h.waitExit(15 * time.Second)
}

// The same with win32-input-mode on (as in Windows Terminal): the mouse
// still arrives as VT sequences, right-clicks too.
func TestE2EWTMenuRename(t *testing.T) {
	exe := buildDeckhand(t)
	path := e2ePwshPath(t)
	home := savedThree(t)
	h := startHarness(t, exe, wtEnv(home, path), t.TempDir())
	menuRename(t, h, func() { h.wtKey(0x1b, 0x1b, 0) })
	h.wtType("q")
	h.waitExit(15 * time.Second)
}
