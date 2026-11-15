//go:build e2e

// End-to-end tests: build deckhand, run it inside a real pseudo console and
// drive it with keystrokes, reading the screen back through a VT emulator.
//
//	go test -tags e2e -run TestE2EShell -v
//	DECKHAND_E2E_CLAUDE=1 go test -tags e2e -run TestE2EClaude -v   (sends one tiny prompt)
package main

import (
	"context"
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
	h.send("\t\tshell-one") // to the Name field
	h.send("\r")
	h.waitFor("PS ", 15*time.Second)

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
	h.send("\t\tclaude-e2e\r")

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
