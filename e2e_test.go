//go:build e2e

// End-to-end tests: build deckhand, run it inside a real pseudo console and
// drive it with keystrokes, reading the screen back through a VT emulator.
//
//	go test -tags e2e -run TestE2EShell -v
//	DECKHAND_E2E_CLAUDE=1 go test -tags e2e -run TestE2EClaude -v   (sends one tiny prompt)
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

type harness struct {
	t   *testing.T
	pty xpty.Pty
	emu *vt.SafeEmulator
	cmd *exec.Cmd
	mu  sync.Mutex
}

func buildDeckhand(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "deckhand.exe")
	out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return exe
}

func startHarness(t *testing.T, exe string, env []string, dir string) *harness {
	t.Helper()
	const w, h = 140, 40
	pty, err := xpty.NewPty(w, h)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = env
	cmd.Dir = dir
	if err := pty.Start(cmd); err != nil {
		t.Fatal(err)
	}
	hs := &harness{t: t, pty: pty, emu: vt.NewSafeEmulator(w, h), cmd: cmd}
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				_, _ = hs.emu.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { _, _ = io.Copy(pty, hs.emu) }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = pty.Close()
	})
	return hs
}

func (h *harness) screen() string { return h.emu.String() }

func (h *harness) waitFor(text string, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(h.screen(), text) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %q; screen:\n%s", text, h.screen())
}

func (h *harness) waitForAny(timeout time.Duration, texts ...string) string {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := h.screen()
		for _, t := range texts {
			if strings.Contains(s, t) {
				return t
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for any of %q; screen:\n%s", texts, h.screen())
	return ""
}

func (h *harness) send(s string) {
	h.t.Helper()
	if _, err := h.pty.Write([]byte(s)); err != nil {
		h.t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
}

func (h *harness) waitExit(timeout time.Duration) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := xpty.WaitProcess(ctx, h.cmd); err != nil && ctx.Err() != nil {
		h.t.Fatalf("deckhand didn't exit; screen:\n%s", h.screen())
	}
}

func envWith(home, path string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if k == "DECKHAND_HOME" || (path != "" && k == "PATH") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "DECKHAND_HOME="+home)
	if path != "" {
		env = append(env, "PATH="+path)
	}
	return env
}

func TestE2EShell(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	// Only PowerShell 7 and System32 on PATH, so pwsh is the first agent.
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := t.TempDir()
	h := startHarness(t, exe, envWith(home, path), t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("New session", 5*time.Second)
	h.waitFor("pwsh", 2*time.Second)
	h.send("\r") // empty folder box: start in the current folder
	h.waitFor("PS ", 15*time.Second)

	// Rename it.
	h.send("\x11") // ctrl+q
	h.send("r")
	h.waitFor("Rename:", 5*time.Second)
	h.send("\x15shell-one\r") // ctrl+u clears, then the new name
	h.send("\r")              // back into the session
	h.waitFor("PS ", 5*time.Second)

	h.send(`echo ("deck" + "hand" + 42)` + "\r")
	h.waitFor("deckhand42", 10*time.Second)
	if !strings.Contains(h.screen(), "shell-one") {
		t.Fatalf("sidebar should list the session; screen:\n%s", h.screen())
	}

	h.send("\x11") // ctrl+q back to the list
	h.waitFor("n new", 5*time.Second)

	// Status settles to idle once the shell is quiet.
	h.waitFor("pwsh · idle", 10*time.Second)
	t.Logf("screen:\n%s", h.screen())

	// Sessions are saved for next time.
	data, err := os.ReadFile(filepath.Join(home, "sessions.json"))
	if err != nil || !strings.Contains(string(data), "shell-one") {
		t.Fatalf("sessions.json: %v %s", err, data)
	}

	h.send("q")
	h.waitFor("Quit and end 1 running", 5*time.Second)
	h.send("y")
	h.waitExit(10 * time.Second)
}

func TestE2EFolderPicker(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	root := t.TempDir()
	for _, d := range []string{`alpha\beta`, `alps`, `zeta`} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	h := startHarness(t, exe, envWith(home, path), root)

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	// Empty box lists the current folder's subfolders.
	h.waitFor(`zeta\`, 3*time.Second)

	h.send("alp")
	h.waitFor(`alps\`, 3*time.Second)
	if strings.Contains(h.screen(), `zeta\`) {
		t.Fatalf("zeta shouldn't match 'alp'; screen:\n%s", h.screen())
	}
	h.send("\t") // completes to the first match, alpha\, and lists inside it
	h.waitFor(`alpha\`, 3*time.Second)
	h.waitFor(`beta\`, 3*time.Second)
	t.Logf("picker after tab:\n%s", h.screen())
	h.send("\x17") // ctrl+w: back up to the root
	h.waitFor(`zeta\`, 3*time.Second)
	h.send("\x1b[B\x1b[B\x1b[B") // ↓↓↓: alpha, alps, zeta
	h.waitFor("Start in  "+filepath.Join(root, "zeta"), 3*time.Second)
	h.send("\r")
	h.waitFor("PS ", 15*time.Second)
	h.waitFor("zeta  pwsh", 3*time.Second) // header: name and agent

	// The folder is remembered for next time.
	data, _ := os.ReadFile(filepath.Join(home, "prefs.json"))
	if !strings.Contains(string(data), "zeta") || !strings.Contains(string(data), `"last_agent": "pwsh"`) {
		t.Fatalf("prefs.json: %s", data)
	}
	h.send("\x11q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

// wheel sends an SGR mouse wheel event at screen cell (x, y), 0-based.
func (h *harness) wheel(up bool, x, y int) {
	b := 65
	if up {
		b = 64
	}
	h.send(fmt.Sprintf("\x1b[<%d;%d;%dM", b, x+1, y+1))
}

func TestE2EScroll(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, envWith(t.TempDir(), path), t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send("\r")
	h.waitFor("PS ", 15*time.Second)
	h.send(`1..300 | % { "line-$_" }` + "\r")
	h.waitFor("line-300", 10*time.Second)
	time.Sleep(500 * time.Millisecond)

	// Wheel up over the terminal: 5 notches = 15 lines.
	for i := 0; i < 5; i++ {
		h.wheel(true, 80, 20)
	}
	h.waitFor("15 lines back", 3*time.Second)
	if strings.Contains(h.screen(), "line-300") {
		t.Fatalf("line-300 should be scrolled out of view; screen:\n%s", h.screen())
	}
	t.Logf("scrolled:\n%s", h.screen())

	// Wheel back down to the live view.
	for i := 0; i < 5; i++ {
		h.wheel(false, 80, 20)
	}
	h.waitFor("line-300", 3*time.Second)
	if strings.Contains(h.screen(), "lines back") {
		t.Fatalf("should be back at the live view; screen:\n%s", h.screen())
	}

	// shift+PgUp scrolls a page; typing returns to the live view.
	h.send("\x1b[5;2~")
	h.waitFor("lines back", 3*time.Second)
	h.send("x")
	h.waitFor("line-300", 3*time.Second)
	if strings.Contains(h.screen(), "lines back") {
		t.Fatalf("typing should return to the live view; screen:\n%s", h.screen())
	}

	// Wheel over the sidebar doesn't scroll the terminal.
	h.wheel(true, 5, 5)
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(h.screen(), "lines back") {
		t.Fatalf("sidebar wheel shouldn't scroll the terminal; screen:\n%s", h.screen())
	}

	h.send("\x11q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

// A fullscreen app that turns on mouse reporting gets the wheel itself.
func TestE2EMouseForwarding(t *testing.T) {
	exe := buildDeckhand(t)
	// Build the stand-in app as "pwsh.exe" so deckhand's pwsh agent runs it.
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "pwsh.exe"), "./testdata/mouseapp").CombinedOutput(); err != nil {
		t.Fatalf("build mouseapp: %v\n%s", err, out)
	}
	path := bin + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, envWith(t.TempDir(), path), t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send("\r")
	h.waitFor("mouseapp ready", 10*time.Second)

	// Terminal starts at screen column 33 (sidebar 32 + separator) and row 1.
	h.wheel(true, 40, 6)
	h.waitFor(`got "\x1b[<64;8;6M"`, 5*time.Second) // wheel up at terminal cell (7,5), 1-based
	h.wheel(false, 40, 6)
	h.waitFor(`got "\x1b[<65;8;6M"`, 5*time.Second)
	if strings.Contains(h.screen(), "lines back") {
		t.Fatalf("deckhand shouldn't scroll when the app handles the mouse; screen:\n%s", h.screen())
	}

	h.send("\x11q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

func TestE2EClaude(t *testing.T) {
	if os.Getenv("DECKHAND_E2E_CLAUDE") != "1" {
		t.Skip("set DECKHAND_E2E_CLAUDE=1 to run (sends one short prompt to Claude)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not found")
	}
	exe := buildDeckhand(t)
	home := t.TempDir()
	cwd, _ := os.Getwd()
	h := startHarness(t, exe, envWith(home, ""), cwd)

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("New session", 5*time.Second)
	h.send("\r") // current folder

	// Claude may ask whether to trust the folder first.
	// The default choice is "No, exit", so move down to "Yes" first.
	// Claude draws the dialog slightly before it starts reading keys.
	if h.waitForAny(60*time.Second, "trust this folder", "claude · ready") == "trust this folder" {
		time.Sleep(2 * time.Second)
		h.send("\x1b[B")
		time.Sleep(500 * time.Millisecond)
		h.send("\r")
	}
	h.waitFor("claude · ready", 60*time.Second)

	h.send("Reply with only the word PONG and nothing else.")
	time.Sleep(500 * time.Millisecond)
	h.send("\r")
	h.waitForAny(30*time.Second, "claude · thinking", "claude · your turn")
	h.waitFor("claude · your turn", 120*time.Second)
	h.waitFor("PONG", 5*time.Second)
	t.Logf("screen at 'your turn':\n%s", h.screen())
	if logs, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "events.jsonl")); len(logs) == 1 {
		data, _ := os.ReadFile(logs[0])
		t.Logf("hook events:\n%s", data)
	}

	h.send("\x11")
	h.send("q")
	h.waitFor("Quit and end 1 running", 5*time.Second)
	h.send("y")
	h.waitExit(15 * time.Second)
}
