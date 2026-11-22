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

// savedGroups is each saved session's name and group: "sess-alpha:work".
func savedGroups(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Name  string `json:"name"`
		Group string `json:"group"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range list {
		out = append(out, s.Name+":"+s.Group)
	}
	return strings.Join(out, " ")
}

// sidebarText is the sidebar's column of the screen.
func (h *harness) sidebarText() string {
	var out []string
	for _, line := range strings.Split(h.screen(), "\n") {
		left, _, _ := strings.Cut(line, "│")
		out = append(out, left)
	}
	return strings.Join(out, "\n")
}

// waitSidebar waits for the sidebar to contain text, or not (gone).
func (h *harness) waitSidebar(text string, gone bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.sidebarText(), text) != gone {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("sidebar: waiting for %q (gone %v); screen:\n%s", text, gone, h.screen())
}

func e2ePwshPath(t *testing.T) string {
	t.Helper()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	return filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
}

// TestE2EGroups makes a group with a, collapses it, restarts deckhand and
// checks that it's still collapsed with the right session in it.
func TestE2EGroups(t *testing.T) {
	exe := buildDeckhand(t)
	path := e2ePwshPath(t)
	home := savedThree(t)
	h := startHarness(t, exe, envWith(home, path), t.TempDir())
	h.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")
	if strings.Contains(h.sidebarText(), "(no group)") {
		t.Fatalf("no headers without groups:\n%s", h.screen())
	}

	h.send("\x1b[B") // bravo
	h.send("a")
	h.waitFor(`Move "sess-bravo" to group`, 5*time.Second)
	for _, r := range "work" {
		h.send(string(r))
	}
	h.waitFor(`+ new group "work"`, 3*time.Second)
	h.send("\r")
	h.waitSidebar("▾ work  (1)", false)
	h.waitSidebar("▾ (no group)  (2)", false)
	h.waitOrder("sess-bravo", "sess-alpha", "sess-charlie")
	if got := savedGroups(t, home); got != "sess-bravo:work sess-alpha: sess-charlie:" {
		t.Errorf("saved: %q", got)
	}

	h.send("\x1b[D") // ← to the work header
	h.send(" ")      // collapse it
	h.waitSidebar("▸ work  (1)", false)
	h.waitSidebar("sess-bravo", true)
	t.Logf("screen:\n%s", h.screen())
	h.send("q")
	h.waitExit(15 * time.Second)

	h2 := startHarness(t, exe, envWith(home, path), t.TempDir())
	h2.waitSidebar("▸ work  (1)", false)
	h2.waitOrder("sess-alpha", "sess-charlie")
	if strings.Contains(h2.sidebarText(), "sess-bravo") {
		t.Errorf("bravo should be folded away in work:\n%s", h2.screen())
	}
	data, _ := os.ReadFile(filepath.Join(home, "groups.json"))
	if !strings.Contains(string(data), `"collapsed": true`) {
		t.Errorf("groups.json: %s", data)
	}
}

// TestE2EGroupDrag drags a session into a collapsed group with the mouse,
// opens the group by hovering, then drags a session out to "(no group)",
// restarts deckhand and checks the groups.
func TestE2EGroupDrag(t *testing.T) {
	exe := buildDeckhand(t)
	path := e2ePwshPath(t)
	home := savedThree(t)
	// alpha in "work", collapsed.
	data, _ := os.ReadFile(filepath.Join(home, "sessions.json"))
	var list []map[string]any
	_ = json.Unmarshal(data, &list)
	list[0]["group"] = "work"
	data, _ = json.Marshal(list)
	_ = os.WriteFile(filepath.Join(home, "sessions.json"), data, 0o600)
	_ = os.WriteFile(filepath.Join(home, "groups.json"), []byte(`{"groups":[{"name":"work","collapsed":true}]}`), 0o600)

	h := startHarness(t, exe, envWith(home, path), t.TempDir())
	// 2 ▸ work, 3 blank, 4 (no group), 5 bravo (5-7), 8 charlie (8-10)
	h.waitSidebar("▸ work  (1)", false)
	h.waitOrder("sess-bravo", "sess-charlie")

	// Charlie onto the collapsed work header: into work, which stays
	// collapsed.
	h.drag(5, 8, 5, 2)
	h.waitSidebar("▸ work  (2)", false)
	h.waitSidebar("sess-charlie", true)
	if got := savedGroups(t, home); got != "sess-alpha:work sess-charlie:work sess-bravo:" {
		t.Errorf("saved after the drop: %q", got)
	}

	// Hovering bravo over the collapsed header opens it after a second;
	// esc closes it again.
	h.send("\x1b[<0;6;6M")  // press on bravo
	h.send("\x1b[<32;6;3M") // onto work's header
	time.Sleep(1500 * time.Millisecond)
	h.waitSidebar("sess-alpha", false)
	h.waitSidebar("▾ work  (2)", false)
	h.send("\x1b")
	h.send("\x1b[<0;6;3m")
	h.waitSidebar("▸ work  (2)", false)

	// Open work with a click on its arrow, then drag charlie out onto the
	// (no group) header.
	// 2 ▾ work, 3 alpha (3-5), 6 charlie (6-8), 9 blank, 10 (no group), 11 bravo
	h.click(1, 2)
	h.waitSidebar("▾ work  (2)", false)
	h.waitOrder("sess-alpha", "sess-charlie", "sess-bravo")
	h.drag(5, 6, 8, 10)
	h.waitSidebar("▾ work  (1)", false)
	h.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")
	if got := savedGroups(t, home); got != "sess-alpha:work sess-bravo: sess-charlie:" {
		t.Errorf("saved after dragging out: %q", got)
	}
	t.Logf("screen:\n%s", h.screen())
	h.send("q")
	h.waitExit(15 * time.Second)

	h2 := startHarness(t, exe, envWith(home, path), t.TempDir())
	h2.waitSidebar("▾ work  (1)", false)
	h2.waitSidebar("▾ (no group)  (2)", false)
	h2.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")
}
