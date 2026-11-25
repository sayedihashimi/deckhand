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

const keyF6 = "\x1b[17~"

// waitFile waits until a file's content is want.
func waitFile(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	var got []byte
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if got, _ = os.ReadFile(path); string(got) == want {
			return
		}
	}
	t.Fatalf("%s: got %q, want %q", filepath.Base(path), got, want)
}

// TestE2EPad opens a session's scratch pad with e and with F6, types in it
// and sends it to the fake Claude, which turned on bracketed paste and
// records what it's sent: the text arrives as one paste, then Enter. The
// pad shows ✎ in the list, is still there after a restart, and removing the
// session (x, keeping the notes) archives it.
func TestE2EPad(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}
	proj := filepath.Join(t.TempDir(), "api")
	_ = os.MkdirAll(proj, 0o755)
	input := filepath.Join(t.TempDir(), "input.txt")
	script := "event SessionStart\nprint \\e[?2004h\ninput " + input + "\nsay hookapp ready\n"
	if err := os.WriteFile(filepath.Join(proj, "hookapp.txt"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	const id = "pad-0001"
	saved := fmt.Sprintf(`[{"id":%q,"name":"api","agent":"claude","dir":%q,"created":"2026-01-01T00:00:00Z"}]`, id, proj)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	path := bin + ";" + os.Getenv("SystemRoot") + `\System32`
	// Windows Terminal's win32-input-mode records, so ctrl+enter arrives as
	// itself. Every key is sent that way, as Windows Terminal does.
	env := wtEnv(home, path)
	h := startHarness(t, exe, env, t.TempDir())
	const vkF5, vkF6, leftAlt = 0x74, 0x75, 0x02

	h.waitFor("api", 10*time.Second)
	h.wtType("1")
	h.waitFor("hookapp ready", 15*time.Second)
	h.wtKey(vkF5, 0, 0)
	h.waitFor("n new", 5*time.Second) // the list's footer

	// e: the pad opens below the terminal, focused.
	h.wtType("e")
	h.waitFor("scratch pad", 5*time.Second)
	h.waitFor("ctrl+enter send", 5*time.Second)
	h.wtType("ask about retries")
	h.waitFor("ask about retries", 5*time.Second)
	h.wtKey(vkReturn, '\r', leftAlt) // alt+enter sends it
	waitFile(t, input, "\x1b[200~ask about retries\x1b[201~\r", 10*time.Second)
	h.waitFor("sent 1 line", 5*time.Second)

	// F6 to the terminal and back, a second line, then ctrl+enter sends the
	// whole pad as one paste.
	h.wtKey(vkF6, 0, 0)
	h.waitGone("ctrl+enter send", 5*time.Second)
	h.wtKey(vkF6, 0, 0)
	h.waitFor("ctrl+enter send", 5*time.Second)
	h.wtKey(vkReturn, '\r', 0)
	h.wtType("then run the tests")
	h.waitFor("then run the tests", 5*time.Second)
	h.wtKey(vkReturn, '\n', wtCtrl)
	waitFile(t, input, "\x1b[200~ask about retries\x1b[201~\r\x1b[200~ask about retries\rthen run the tests\x1b[201~\r", 10*time.Second)
	h.waitFor("sent 2 lines", 5*time.Second)

	// The list marks it; the notes are saved when focus leaves the pad.
	h.wtKey(vkF5, 0, 0)
	h.waitFor("✎", 5*time.Second)
	notes := filepath.Join(home, "notes", id+".md")
	waitFile(t, notes, "ask about retries\nthen run the tests", 5*time.Second)
	t.Logf("with the pad:\n%s", h.screen())
	h.wtType("q")
	h.waitFor("Quit and end 1 running", 5*time.Second)
	h.wtType("y")
	h.waitExit(15 * time.Second)

	// After a restart the pad is open again, with its notes.
	var list []struct {
		PadOpen bool `json:"pad_open"`
	}
	data, _ := os.ReadFile(filepath.Join(home, "sessions.json"))
	if err := json.Unmarshal(data, &list); err != nil || len(list) != 1 || !list[0].PadOpen {
		t.Fatalf("sessions.json should remember the open pad: %s", data)
	}
	h = startHarness(t, exe, env, t.TempDir())
	h.waitFor("then run the tests", 10*time.Second)
	h.waitFor("✎", 5*time.Second)

	// x keeps the notes by default, in the archive.
	h.wtType("x")
	h.waitFor("notes are kept (D deletes them)", 5*time.Second)
	h.wtKey(vkReturn, '\r', 0)
	h.waitFor("No sessions yet", 5*time.Second)
	if _, err := os.Stat(notes); err == nil {
		t.Error("the notes are still in notes after x")
	}
	waitFile(t, filepath.Join(home, "notes", "archive", id+" api.md"), "ask about retries\nthen run the tests", 5*time.Second)
	if strings.Contains(h.screen(), "✎") {
		t.Errorf("✎ after the session was removed:\n%s", h.screen())
	}
	h.wtType("q")
	h.waitExit(10 * time.Second)
}

// TestE2EPadEditor opens the pad in the configured editor (ctrl+o) and
// reloads it when the file changes. The editor is recordapp, which logs
// its arguments.
func TestE2EPadEditor(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	rec := filepath.Join(bin, "recordapp.exe")
	if out, err := exec.Command("go", "build", "-o", rec, "./testdata/recordapp").CombinedOutput(); err != nil {
		t.Fatalf("build recordapp: %v\n%s", err, out)
	}
	home := t.TempDir()
	proj := t.TempDir()
	const id = "pad-0002"
	saved := fmt.Sprintf(`[{"id":%q,"name":"docs","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z"}]`, id, proj)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(home, "editor.log")
	cfg := filepath.Join(home, "config.toml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf("[open]\neditor = '\"%s\" \"%s\" \"{dir}\"'\n", rec, log)), 0o644)
	path := os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, append(envWith(home, path), "DECKHAND_CONFIG="+cfg), t.TempDir())

	h.waitFor("docs", 10*time.Second)
	h.send("e") // an exited session's pad opens too, without starting it
	h.waitFor("ctrl+enter send", 5*time.Second)
	h.send("draft")
	h.waitFor("draft", 5*time.Second)
	h.send("\x0f") // ctrl+o
	notes := filepath.Join(home, "notes", id+".md")
	h.waitFor("opened in the editor", 5*time.Second)
	waitFile(t, notes, "draft", 5*time.Second) // saved first
	var run struct {
		Args []string `json:"args"`
	}
	data, _ := os.ReadFile(log)
	if err := json.Unmarshal(data, &run); err != nil || len(run.Args) != 1 || run.Args[0] != notes {
		t.Fatalf("the editor ran with %s", data)
	}
	// The editor saves the file: the pad reloads.
	if err := os.WriteFile(notes, []byte("draft\r\nedited outside\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.waitFor("edited outside", 5*time.Second)
	h.send(keyF5 + "q")
	h.waitExit(10 * time.Second)
}
