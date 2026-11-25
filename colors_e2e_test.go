//go:build e2e

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// savedColors is each saved session's name and color: "sess-alpha:red".
func savedColors(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range list {
		out = append(out, s.Name+":"+s.Color)
	}
	return strings.Join(out, " ")
}

// screenPos is the cell where text first shows on screen, or -1, -1.
func (h *harness) screenPos(text string) (x, y int) {
	for y, line := range strings.Split(h.screen(), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return len([]rune(line[:i])), y
		}
	}
	return -1, -1
}

// barColor is the color of the bar left of a session's name, as "#rrggbb",
// or "" when there's no bar.
func (h *harness) barColor(name string) string {
	h.t.Helper()
	y := -1
	for i, line := range strings.Split(h.sidebarText(), "\n") {
		if i >= listTop && strings.Contains(line, name) {
			y = i
			break
		}
	}
	if y < 0 {
		h.t.Fatalf("%s isn't shown:\n%s", name, h.screen())
	}
	c := h.emu.CellAt(0, y)
	if c == nil || c.Content != "┃" || c.Style.Fg == nil {
		return ""
	}
	r, g, b, _ := c.Style.Fg.RGBA()
	return "#" + hexByte(r>>8) + hexByte(g>>8) + hexByte(b>>8)
}

func hexByte(v uint32) string {
	return string("0123456789abcdef"[v>>4&15]) + string("0123456789abcdef"[v&15])
}

// TestE2EColors colors sessions with C: a palette name with its first
// letter, a hex color typed in the hex box, and a click in the picker.
// After a restart the colors are still there.
func TestE2EColors(t *testing.T) {
	exe := buildDeckhand(t)
	path := e2ePwshPath(t)
	home := savedThree(t)
	h := startHarness(t, exe, envWith(home, path), t.TempDir())
	h.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")

	// alpha: C, r (red), enter.
	h.send("C")
	h.waitFor(`Color for "sess-alpha"`, 5*time.Second)
	h.send("r")
	h.send("\r")
	h.waitGone(`Color for`, 3*time.Second)
	h.waitSidebar("┃", false)

	// bravo: an invalid hex color is refused, then a good one.
	h.send("\x1b[B")
	h.send("C")
	h.waitFor(`Color for "sess-bravo"`, 5*time.Second)
	h.send("\t")
	for _, r := range "#12" {
		h.send(string(r))
	}
	h.send("\r")
	h.waitFor("not a color", 3*time.Second)
	h.send("3")
	h.send("\r")
	h.waitGone(`Color for`, 3*time.Second)

	// charlie: a click on teal in the picker.
	h.send("\x1b[B")
	h.send("C")
	h.waitFor(`Color for "sess-charlie"`, 5*time.Second)
	x, y := h.screenPos("██ teal")
	if x < 0 {
		t.Fatalf("no teal in the picker:\n%s", h.screen())
	}
	h.click(x+3, y)
	h.waitGone(`Color for`, 3*time.Second)
	t.Logf("screen:\n%s", h.screen())

	// The sessions aren't running, so their bars are dimmed.
	want := map[string]string{"sess-alpha": dimHex(palette["red"]), "sess-bravo": dimHex("#112233"), "sess-charlie": dimHex(palette["teal"])}
	check := func(h *harness) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			bad := ""
			for name, c := range want {
				if got := h.barColor(name); got != c {
					bad += name + " " + got + " want " + c + "; "
				}
			}
			if bad == "" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("bars: %s\n%s", bad, h.screen())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	check(h)
	if got := savedColors(t, home); got != "sess-alpha:red sess-bravo:#112233 sess-charlie:teal" {
		t.Errorf("saved: %q", got)
	}
	h.send("q")
	h.waitExit(15 * time.Second)

	h2 := startHarness(t, exe, envWith(home, path), t.TempDir())
	h2.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")
	check(h2)

	// Cleared with n (none) and enter: its bar's column is blank.
	h2.send("C")
	h2.waitFor(`Color for "sess-alpha"`, 5*time.Second)
	h2.send("n")
	h2.send("\r")
	h2.waitGone(`Color for`, 3*time.Second)
	delete(want, "sess-alpha")
	check(h2)
	if got := h2.barColor("sess-alpha"); got != "" {
		t.Errorf("cleared alpha still has a bar: %s", got)
	}
	h2.send("q")
	h2.waitExit(15 * time.Second)
}
