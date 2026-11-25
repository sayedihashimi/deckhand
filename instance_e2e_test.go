//go:build e2e

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sessionFolders is the names of the folders under <home>\sessions.
func sessionFolders(home string) map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(home, "sessions"))
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// TestE2EInstances runs two deckhands on one data folder: the first is the
// main instance and the second is temporary. The temporary one starts
// empty, leaves sessions.json and groups.json alone, keeps working after
// main has gone, and deletes its sessions' folders when it quits. A main
// instance that ended without cleaning up (killed) doesn't stop the next
// one being main.
func TestE2EInstances(t *testing.T) {
	exe := buildDeckhand(t)
	path := e2ePwshPath(t)
	home := savedThree(t)
	groups := `{"groups":[{"name":"work"}]}`
	if err := os.WriteFile(filepath.Join(home, "groups.json"), []byte(groups), 0o600); err != nil {
		t.Fatal(err)
	}
	env := envWith(home, path)

	main := startHarness(t, exe, env, t.TempDir())
	main.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")
	if strings.Contains(main.sidebarText(), "temporary") {
		t.Fatalf("the first instance should be main:\n%s", main.screen())
	}
	// Take the files as main left them.
	sessionsBefore, _ := os.ReadFile(filepath.Join(home, "sessions.json"))
	groupsBefore, _ := os.ReadFile(filepath.Join(home, "groups.json"))

	tmp := startHarness(t, exe, env, t.TempDir())
	tmp.waitFor("deckhand temporary", 10*time.Second)
	tmp.waitFor("No sessions yet", 5*time.Second)

	// Two sessions: one renamed, one ended with K.
	for range 2 {
		tmp.send("n")
		tmp.waitFor("New session", 5*time.Second)
		tmp.send("\r")
		tmp.waitFor("PS ", 15*time.Second)
		tmp.send(keyF5)
		tmp.waitFor("n new", 5*time.Second)
	}
	tmp.send("r")
	tmp.waitFor("Rename:", 5*time.Second)
	tmp.send("\x15temp-renamed\r")
	tmp.waitSidebar("temp-renamed", false)
	tmp.send("j")
	tmp.send("K")
	tmp.waitFor("it stays in the list", 5*time.Second)
	tmp.send("y")
	tmp.waitSidebar("restart", false)
	tmp.send("a")
	tmp.waitFor(tempGroupsNote, 5*time.Second)

	folders := sessionFolders(home)
	if len(folders) == 0 {
		t.Fatalf("the temporary instance's sessions should have folders while it runs")
	}

	// main goes away (killed, like a crash); the temporary one stays
	// temporary and keeps working.
	_ = main.cmd.Process.Kill()
	main.waitExit(10 * time.Second)
	tmp.send("n")
	tmp.waitFor("New session", 5*time.Second)
	tmp.send("\x1b")
	if !strings.Contains(tmp.sidebarText(), "deckhand temporary") {
		t.Fatalf("it should still be temporary:\n%s", tmp.screen())
	}

	// The next one started is main, with the saved sessions.
	next := startHarness(t, exe, env, t.TempDir())
	next.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")
	if strings.Contains(next.sidebarText(), "temporary") || strings.Contains(next.screen(), "temp-renamed") {
		t.Fatalf("the next instance should be main with the saved sessions:\n%s", next.screen())
	}

	// Quitting the temporary one (one session still running) asks as main
	// does, then deletes its folders.
	tmp.send("q")
	tmp.waitFor("Quit and end 1 running", 5*time.Second)
	tmp.send("y")
	tmp.waitExit(15 * time.Second)
	for f := range folders {
		if _, err := os.Stat(filepath.Join(home, "sessions", f)); !os.IsNotExist(err) {
			t.Errorf("session folder %s should be gone after the temporary instance quit (%v)", f, err)
		}
	}
	// Checked before next quits: main saves the list when it quits.
	if got, _ := os.ReadFile(filepath.Join(home, "sessions.json")); string(got) != string(sessionsBefore) {
		t.Errorf("sessions.json changed:\n%s\nwas\n%s", got, sessionsBefore)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "groups.json")); string(got) != string(groupsBefore) {
		t.Errorf("groups.json changed:\n%s\nwas\n%s", got, groupsBefore)
	}
	next.send("q")
	next.waitExit(10 * time.Second)
}

// deckhand --temp is temporary even with no main instance running, and
// doesn't stop the next one being main.
func TestE2ETempFlag(t *testing.T) {
	exe := buildDeckhand(t)
	path := e2ePwshPath(t)
	home := savedThree(t)
	env := envWith(home, path)
	tmp := startHarnessArgs(t, exe, []string{"--temp"}, env, t.TempDir(), 140, 40)
	tmp.waitFor("deckhand temporary", 10*time.Second)
	tmp.waitFor("No sessions yet", 5*time.Second)

	main := startHarness(t, exe, env, t.TempDir())
	main.waitOrder("sess-alpha", "sess-bravo", "sess-charlie")
	if strings.Contains(main.sidebarText(), "temporary") {
		t.Fatalf("with only a --temp instance running, the next is main:\n%s", main.screen())
	}
	tmp.send("q")
	tmp.waitExit(10 * time.Second)
	main.send("q")
	main.waitExit(10 * time.Second)
}
