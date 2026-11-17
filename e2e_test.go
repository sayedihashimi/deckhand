//go:build e2e

// End-to-end tests: build deckhand, run it inside a real pseudo console and
// drive it with keystrokes, reading the screen back through a VT emulator.
//
//	go test -tags e2e -run TestE2EShell -v
//	DECKHAND_E2E_CLAUDE=1 go test -tags e2e -run TestE2EClaude -v   (sends one tiny prompt)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	return startHarnessSize(t, exe, env, dir, 140, 40)
}

func startHarnessSize(t *testing.T, exe string, env []string, dir string, w, h int) *harness {
	t.Helper()
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
	// Don't pick up the user's own config (its custom agents come first in
	// the new-session dialog). Tests that need a config append their own
	// DECKHAND_CONFIG later, which wins.
	env = append(env, "DECKHAND_HOME="+home, "DECKHAND_CONFIG="+filepath.Join(home, "no-config.toml"))
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

// A custom agent that starts Claude through a PowerShell function (like a
// profile's `smc`) still gets deckhand's hook settings via the claude shim,
// without the function forwarding any arguments.
func TestE2EPwshWrapper(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/argsapp").CombinedOutput(); err != nil {
		t.Fatalf("build argsapp: %v\n%s", err, out)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte(`
[[agents]]
name   = "wrap"
pwsh   = "function Start-Wrapped { claude 'hello world' --name demo }; Start-Wrapped"
type   = "claude"
resume = "claude --resume {id}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := bin + ";" + filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	env := append(envWith(t.TempDir(), path), "DECKHAND_CONFIG="+cfg)
	h := startHarness(t, exe, env, t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("wrap", 5*time.Second) // custom agent listed (and selected) first
	h.send("\r")
	h.waitFor("argsapp done", 20*time.Second)
	s := h.screen()
	for _, want := range []string{"arg: [--settings]", "arg: [--session-id]", "arg: [hello world]", "arg: [--name]"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q; screen:\n%s", want, s)
		}
	}
	t.Logf("first start:\n%s", s)

	// End it, then resume: the shim must not add --session-id.
	h.send("\x11")
	h.send("K")
	h.waitFor("End ", 3*time.Second)
	h.send("y")
	h.waitFor("wrap · ⏎ resume · ⇧⏎ new", 10*time.Second) // "exited" is dropped to fit the hint
	h.send("\r")
	h.waitFor("arg: [--resume]", 20*time.Second)
	if strings.Contains(h.screen(), "arg: [--session-id]") {
		t.Fatalf("resume shouldn't pass --session-id; screen:\n%s", h.screen())
	}

	h.send("\x11q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

// TestE2ECustomClaudeAgent starts a real custom Claude agent from your own
// config (for example a profile function) and checks that hook status
// arrives. It sends no prompt.
//
//	DECKHAND_E2E_AGENT=smc DECKHAND_E2E_DIR=C:\some\project go test -tags e2e -run TestE2ECustomClaudeAgent -v
func TestE2ECustomClaudeAgent(t *testing.T) {
	agent, dir := os.Getenv("DECKHAND_E2E_AGENT"), os.Getenv("DECKHAND_E2E_DIR")
	if agent == "" || dir == "" {
		t.Skip("set DECKHAND_E2E_AGENT and DECKHAND_E2E_DIR to run")
	}
	exe := buildDeckhand(t)
	home := t.TempDir()
	h := startHarness(t, exe, envWith(home, ""), dir)

	h.waitFor("No sessions yet", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	// Select the agent: move to the agent row and step right until it's chosen.
	h.send("\x1b[Z") // shift+tab
	for i := 0; i < 10 && !strings.Contains(h.screen(), "Agent    "+agent+" "); i++ {
		h.send("\x1b[C")
	}
	h.send("\x1b[B\r") // back to the folder box, start in the current folder
	if h.waitForAny(90*time.Second, "trust this folder", agent+" · ready") == "trust this folder" {
		time.Sleep(2 * time.Second)
		h.send("\x1b[B")
		time.Sleep(500 * time.Millisecond)
		h.send("\r")
	}
	h.waitFor(agent+" · ready", 90*time.Second)
	t.Logf("screen:\n%s", h.screen())
	if logs, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "events.jsonl")); len(logs) == 1 {
		data, _ := os.ReadFile(logs[0])
		t.Logf("hook events:\n%s", data)
	}
	h.send("\x11q")
	h.send("y")
	h.waitExit(15 * time.Second)
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
	h.waitForAny(30*time.Second, "claude · thinking", "claude · your turn", "claude · PONG")
	h.waitFor("claude · PONG", 120*time.Second) // the reply replaces "your turn"
	t.Logf("screen at the end of the turn:\n%s", h.screen())
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

// TestE2ENotifyInfoFilter runs a fake claude.exe (testdata/hookapp) that
// sends a turn's hook events, in a git repo. It checks the notifications
// (recorded to a file instead of shown), the branch, model and context in
// the sidebar and header, and the "/" filter.
func TestE2ENotifyInfoFilter(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}

	// A git repo with a change, on a known branch.
	repo := filepath.Join(t.TempDir(), "repo-e2e")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(gitExe, "-C", repo, "init", "-b", "e2e-branch").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	_ = os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644)

	// A saved session to filter against.
	home := t.TempDir()
	other := t.TempDir()
	saved := fmt.Sprintf(`[{"id":"zeta-0001","name":"zeta","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z"}]`, other)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(cfg, []byte("[notifications]\nwhen = \"always\"\n"), 0o644)
	notifyLog := filepath.Join(t.TempDir(), "notify.log")

	path := bin + ";" + filepath.Dir(pwsh) + ";" + filepath.Dir(gitExe) + ";" + os.Getenv("SystemRoot") + `\System32`
	env := append(envWith(home, path), "DECKHAND_CONFIG="+cfg, "DECKHAND_NOTIFY_LOG="+notifyLog)
	h := startHarness(t, exe, env, t.TempDir())

	h.waitFor("zeta", 10*time.Second)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send(repo)
	h.send("\r")
	h.waitFor("hookapp done", 20*time.Second)
	h.send("\x11") // back to the list

	// Branch, model and context.
	h.waitFor("e2e-branch*", 10*time.Second)    // header
	h.waitFor("opus 5.5 · 83k", 10*time.Second) // sidebar and header
	// After the Stop hook, the second line shows the reply, not "your turn".
	h.waitFor("claude · Fixed the bug.", 5*time.Second)
	if strings.Contains(h.screen(), "your turn") {
		t.Errorf("the reply should replace \"your turn\"; screen:\n%s", h.screen())
	}

	// Notifications: one for the permission request, one when it finished.
	deadline := time.Now().Add(10 * time.Second)
	var log string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(notifyLog)
		log = string(data)
		if strings.Count(log, "\n") >= 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, want := range []string{"repo-e2e needs you\tpermission: Bash: npm test", "repo-e2e finished\tFixed the bug."} {
		if !strings.Contains(log, want) {
			t.Fatalf("notification %q missing; log:\n%s", want, log)
		}
	}

	// Filter: "/" then a name hides the other session; esc brings it back.
	h.send("/")
	h.send("zet")
	h.waitFor("/zet", 3*time.Second)
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(h.screen(), "repo-e2e") {
		t.Fatalf("filter should hide repo-e2e; screen:\n%s", h.screen())
	}
	h.send("\t") // keep the filter, back to the list (enter would open the only match)
	h.waitFor("esc clears", 3*time.Second)
	h.send("\x1b")
	h.waitFor("repo-e2e", 3*time.Second)
	t.Logf("screen:\n%s", h.screen())

	h.send("q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

// TestE2EOpenFolder points [open] editor and terminal at a test app that
// records its arguments, then presses o and t on a session that isn't
// running. The folder has a space and an ampersand in its name.
func TestE2EOpenFolder(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	app := filepath.Join(bin, "recordapp.exe")
	if out, err := exec.Command("go", "build", "-o", app, "./testdata/recordapp").CombinedOutput(); err != nil {
		t.Fatalf("build recordapp: %v\n%s", err, out)
	}
	dir := filepath.Join(t.TempDir(), "my proj & co")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	saved := fmt.Sprintf(`[{"id":"open-0001","name":"proj","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z"}]`, dir)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	editorLog, termLog := filepath.Join(logDir, "editor.log"), filepath.Join(logDir, "terminal.log")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf("[open]\neditor = '\"%s\" \"%s\" --editor \"{dir}\"'\nterminal = '\"%s\" \"%s\" -d \"{dir}\"'\n",
		app, editorLog, app, termLog)), 0o644)

	env := append(envWith(home, ""), "DECKHAND_CONFIG="+cfg)
	h := startHarness(t, exe, env, t.TempDir())
	h.waitFor("proj", 10*time.Second)
	if strings.Contains(h.screen(), "Config error") {
		t.Fatalf("config error; screen:\n%s", h.screen())
	}

	check := func(key, log, flag string) {
		t.Helper()
		h.send(key)
		var rec struct {
			Cwd  string   `json:"cwd"`
			Args []string `json:"args"`
		}
		deadline := time.Now().Add(10 * time.Second)
		var data []byte
		for time.Now().Before(deadline) {
			if data, _ = os.ReadFile(log); strings.HasSuffix(string(data), "\n") {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("%s: log %q: %v; screen:\n%s", key, data, err, h.screen())
		}
		if len(rec.Args) != 2 || rec.Args[0] != flag || rec.Args[1] != dir || rec.Cwd != dir {
			t.Fatalf("%s launched with %+v, want [%s %s]", key, rec, flag, dir)
		}
	}
	check("o", editorLog, "--editor")
	check("t", termLog, "-d")
	if strings.Contains(h.screen(), "Couldn't open") {
		t.Fatalf("unexpected error; screen:\n%s", h.screen())
	}
	t.Logf("screen:\n%s", h.screen())

	h.send("q")
	h.waitExit(10 * time.Second)
}

// shiftEnter is shift+enter as a CSI u sequence. Through the pseudo console
// it reaches deckhand as shift+enter. (A win32-input-mode key event with the
// shift state doesn't: the pseudo console drops the shift for enter.)
const shiftEnter = "[13;2u"

// sessionOrder reads the saved session list: name and folder base, in order.
func sessionOrder(t *testing.T, home string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Name, Dir, Agent string
		ClaudeSessionID  string `json:"claude_session_id"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range list {
		out = append(out, s.Name+"|"+filepath.Base(s.Dir)+"|"+s.Agent+"|"+s.ClaudeSessionID)
	}
	return out
}

// shift+enter in the list adds a session with the same agent and folder
// right below the selected one and starts it, for an exited session and for
// a running one.
func TestE2EShiftEnterNewSession(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	home := t.TempDir()
	proj := filepath.Join(t.TempDir(), "proj-a")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	saved := fmt.Sprintf(`[{"id":"orig-0001","name":"orig","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z","started":true},
{"id":"last-0001","name":"last","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z"}]`, proj, t.TempDir())
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startHarness(t, exe, envWith(home, path), t.TempDir())

	// The exited session shows the hint.
	h.waitFor("⏎ restart · ⇧⏎ new", 10*time.Second)

	// Exited session: a new row below it starts.
	h.send(shiftEnter)
	h.waitFor("PS ", 15*time.Second)
	h.waitFor("proj-a  pwsh", 5*time.Second) // header: the new session is on screen
	want := []string{"orig|proj-a|pwsh|", "proj-a|proj-a|pwsh|", "last|"}
	got := sessionOrder(t, home)
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || !strings.HasPrefix(got[2], want[2]) {
		t.Fatalf("after shift+enter on an exited session: %q", got)
	}
	h.send("\x11") // back to the list
	h.waitFor("n new", 5*time.Second)
	s := h.screen()
	if got := sidebarNames(s); strings.Join(got, ",") != "orig,proj-a,last" {
		t.Fatalf("sidebar order %q; screen:\n%s", got, s)
	}
	if !strings.Contains(s, "pwsh · ⏎ restart") {
		t.Fatalf("the original should still be exited; screen:\n%s", s)
	}

	// Running session: the same.
	h.waitFor("pwsh · idle", 10*time.Second)
	h.send(shiftEnter)
	h.waitFor("PS ", 15*time.Second)
	got = sessionOrder(t, home)
	if len(got) != 4 || got[0] != want[0] || got[1] != want[1] || got[2] != want[1] || !strings.HasPrefix(got[3], want[2]) {
		t.Fatalf("after shift+enter on a running session: %q", got)
	}
	h.send("\x11")
	h.waitFor("n new", 5*time.Second)
	if got := sidebarNames(h.screen()); strings.Join(got, ",") != "orig,proj-a,proj-a,last" {
		t.Fatalf("sidebar order %q; screen:\n%s", got, h.screen())
	}
	t.Logf("screen:\n%s", h.screen())

	h.send("q")
	h.waitFor("Quit and end 2 running", 5*time.Second) // both new sessions are running
	h.send("y")
	h.waitExit(10 * time.Second)
}

// sidebarNames lists the session names in the sidebar, top to bottom.
func sidebarNames(screen string) []string {
	var out []string
	for _, m := range sidebarRow.FindAllStringSubmatch(screen, -1) {
		out = append(out, m[1])
	}
	return out
}

var sidebarRow = regexp.MustCompile(`(?m)^▌?\d \S (\S+) `)

// For a Claude session, shift+enter starts a fresh conversation: a new
// --session-id and no --resume.
func TestE2EShiftEnterClaude(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/argsapp").CombinedOutput(); err != nil {
		t.Fatalf("build argsapp: %v\n%s", err, out)
	}
	home := t.TempDir()
	const oldID = "11111111-2222-3333-4444-555555555555"
	saved := fmt.Sprintf(`[{"id":"orig-0001","name":"orig","agent":"claude","dir":%q,"created":"2026-01-01T00:00:00Z","claude_session_id":%q,"started":true}]`, t.TempDir(), oldID)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	path := bin + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, envWith(home, path), t.TempDir())

	h.waitFor("⏎ resume · ⇧⏎ new", 10*time.Second)
	h.send(shiftEnter)
	h.waitFor("argsapp done", 15*time.Second)
	s := h.screen()
	if !strings.Contains(s, "arg: [--session-id]") || strings.Contains(s, "--resume") || strings.Contains(s, oldID) {
		t.Fatalf("want a fresh conversation; screen:\n%s", s)
	}
	got := sessionOrder(t, home)
	if len(got) != 2 || !strings.HasPrefix(got[0], "orig|") || !strings.HasSuffix(got[0], "|claude|"+oldID) || strings.HasSuffix(got[1], oldID) {
		t.Fatalf("sessions: %q", got)
	}
	h.send("\x11q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

// ? in the list and F1 inside a session open the help; esc closes it and
// focus goes back. ? typed inside a session still reaches the agent.
func TestE2EHelp(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarnessSize(t, exe, envWith(t.TempDir(), path), t.TempDir(), 120, 32)

	h.waitFor("No sessions yet", 10*time.Second)
	h.waitFor("? or F1  all the keys", 2*time.Second) // the welcome screen
	h.waitFor("? help", 2*time.Second)                // the footer
	h.send("?")
	h.waitFor("New-session dialog", 5*time.Second)
	h.waitFor("Config file:", 2*time.Second)
	t.Logf("help from the list:\n%s", h.screen())
	h.send("\x1b")
	h.waitGone("New-session dialog", 5*time.Second)
	h.waitFor("No sessions yet", 2*time.Second)

	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send("\r")
	h.waitFor("PS ", 15*time.Second)

	// ? is typed text inside a session.
	h.send(`echo ("deck?" + "hand")` + "\r")
	h.waitFor("deck?hand", 10*time.Second)
	if strings.Contains(h.screen(), "New-session dialog") {
		t.Fatalf("? inside a session opened the help; screen:\n%s", h.screen())
	}

	// F1 (ESC O P) opens it inside a session; esc closes it and the
	// terminal still has focus.
	h.send("\x1bOP")
	h.waitFor("New-session dialog", 5*time.Second)
	h.waitFor("esc, ? or F1 close", 2*time.Second)
	h.send("\x1b")
	h.waitGone("New-session dialog", 5*time.Second)
	h.send(`echo ("af" + "ter")` + "\r")
	h.waitFor("after", 10*time.Second)

	h.send("\x11q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

func (h *harness) waitGone(text string, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !strings.Contains(h.screen(), text) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %q to go; screen:\n%s", text, h.screen())
}
