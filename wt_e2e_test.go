//go:build e2e

// e2e tests that run deckhand as if in Windows Terminal: WT_SESSION is set,
// so deckhand asks its console for win32-input-mode, and the harness sends
// input the way Windows Terminal does in that mode: keys as win32-input-mode
// records, mouse and paste as plain VT sequences.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Control key states in win32-input-mode records.
const (
	wtShift    = 0x10  // SHIFT_PRESSED
	wtCtrl     = 0x08  // LEFT_CTRL_PRESSED
	wtEnhanced = 0x100 // ENHANCED_KEY (arrows, PgUp, ...)
)

// Virtual key codes.
const (
	vkShift   = 0x10
	vkControl = 0x11
	vkReturn  = 0x0d
	vkPrior   = 0x21 // PgUp
	vkUp      = 0x26
)

func wtEnv(home, path string) []string {
	return append(envWith(home, path), "WT_SESSION=e2e")
}

var (
	user32         = windows.NewLazySystemDLL("user32.dll")
	procVkKeyScan  = user32.NewProc("VkKeyScanW")
	procMapVirtKey = user32.NewProc("MapVirtualKeyW")
)

func scanCode(vk int) int {
	sc, _, _ := procMapVirtKey.Call(uintptr(vk), 0) // MAPVK_VK_TO_VSC
	return int(sc)
}

// wtKey sends a key press and release as win32-input-mode records, with the
// modifier keys' own press and release around it, as Windows Terminal does.
func (h *harness) wtKey(vk int, char rune, state int) {
	h.t.Helper()
	var b strings.Builder
	mods := 0
	if state&wtShift != 0 {
		mods |= wtShift
		b.WriteString(win32Key(vkShift, scanCode(vkShift), 0, mods, true))
	}
	if state&wtCtrl != 0 {
		mods |= wtCtrl
		b.WriteString(win32Key(vkControl, scanCode(vkControl), 0, mods, true))
	}
	b.WriteString(win32Key(vk, scanCode(vk), char, state, true))
	b.WriteString(win32Key(vk, scanCode(vk), char, state, false))
	if state&wtCtrl != 0 {
		mods &^= wtCtrl
		b.WriteString(win32Key(vkControl, scanCode(vkControl), 0, mods, false))
	}
	if state&wtShift != 0 {
		b.WriteString(win32Key(vkShift, scanCode(vkShift), 0, 0, false))
	}
	h.send(b.String())
}

// wtType types text as win32-input-mode key records.
func (h *harness) wtType(text string) {
	h.t.Helper()
	var b strings.Builder
	for _, r := range text {
		res, _, _ := procVkKeyScan.Call(uintptr(r))
		vk, shift := int(res&0xff), res&0x100 != 0
		state := 0
		if shift {
			state = wtShift
			b.WriteString(win32Key(vkShift, scanCode(vkShift), 0, wtShift, true))
		}
		b.WriteString(win32Key(vk, scanCode(vk), r, state, true))
		b.WriteString(win32Key(vk, scanCode(vk), r, state, false))
		if shift {
			b.WriteString(win32Key(vkShift, scanCode(vkShift), 0, 0, false))
		}
	}
	if _, err := h.pty.Write([]byte(b.String())); err != nil {
		h.t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
}

// wtCtrlQ goes back to the session list, as Windows Terminal sends ctrl+q.
func (h *harness) wtCtrlQ() { h.wtKey('Q', 0x11, wtCtrl) }

// click sends an SGR left-button press and release at screen cell (x, y),
// 0-based.
func (h *harness) click(x, y int) {
	h.send(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+1, y+1))
}

// Scrolling, clicks, arrow keys and ctrl+q work with win32-input-mode on.
func TestE2EWTScroll(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, wtEnv(t.TempDir(), path), t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.wtType("n")
	h.waitFor("Start in", 5*time.Second)
	h.wtKey(vkReturn, '\r', 0)
	h.waitFor("PS ", 15*time.Second)
	h.wtType(`1..300 | % { "line-$_" }`)
	h.wtKey(vkReturn, '\r', 0)
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
	for i := 0; i < 5; i++ {
		h.wheel(false, 80, 20)
	}
	h.waitFor("line-300", 3*time.Second)
	if strings.Contains(h.screen(), "lines back") {
		t.Fatalf("should be back at the live view; screen:\n%s", h.screen())
	}

	// shift+PgUp scrolls a page; typing returns to the live view.
	h.wtKey(vkPrior, 0, wtShift|wtEnhanced)
	h.waitFor("lines back", 3*time.Second)
	h.wtType("x")
	h.waitFor("line-300", 3*time.Second)
	if strings.Contains(h.screen(), "lines back") {
		t.Fatalf("typing should return to the live view; screen:\n%s", h.screen())
	}
	h.wtKey(0x08, '\b', 0) // backspace the x

	// Up arrow recalls the command in PowerShell.
	h.wtKey(vkReturn, '\r', 0)
	time.Sleep(500 * time.Millisecond)
	h.wtKey(vkUp, 0, wtEnhanced)
	time.Sleep(500 * time.Millisecond)
	if s := h.screen(); strings.Count(s, `% { "line-$_" }`) < 1 || strings.Contains(s, ";1_") {
		t.Fatalf("up arrow should recall the command, with no stray characters; screen:\n%s", s)
	}
	h.wtKey(0x1b, 0x1b, 0) // esc clears the line

	// F1 opens the help inside a session; esc closes it.
	h.wtKey(0x70, 0, 0) // VK_F1
	h.waitFor("esc, ? or F1 close", 3*time.Second)
	h.wtKey(0x1b, 0x1b, 0)
	h.waitGone("esc, ? or F1 close", 3*time.Second)

	// Wheel over the sidebar doesn't scroll the terminal.
	h.wheel(true, 5, 5)
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(h.screen(), "lines back") {
		t.Fatalf("sidebar wheel shouldn't scroll the terminal; screen:\n%s", h.screen())
	}

	// Clicking the session list focuses it, clicking the terminal focuses
	// the terminal again.
	h.click(5, 2)
	h.waitFor("n new", 3*time.Second)
	h.click(80, 20)
	h.waitFor("ctrl+q back to the list", 3*time.Second)

	h.wtCtrlQ()
	h.waitFor("n new", 3*time.Second)
	h.wtType("q")
	h.wtType("y")
	h.waitExit(10 * time.Second)
}

// A fullscreen app that turns on mouse reporting gets the wheel and clicks,
// with win32-input-mode on.
func TestE2EWTMouseForwarding(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "pwsh.exe"), "./testdata/mouseapp").CombinedOutput(); err != nil {
		t.Fatalf("build mouseapp: %v\n%s", err, out)
	}
	path := bin + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, wtEnv(t.TempDir(), path), t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.wtType("n")
	h.waitFor("Start in", 5*time.Second)
	h.wtKey(vkReturn, '\r', 0)
	h.waitFor("mouseapp ready", 10*time.Second)

	h.wheel(true, 40, 6)
	h.waitFor(`got "\x1b[<64;8;6M"`, 5*time.Second)
	h.wheel(false, 40, 6)
	h.waitFor(`got "\x1b[<65;8;6M"`, 5*time.Second)
	h.click(40, 6)
	h.waitFor(`got "\x1b[<0;8;6M"`, 5*time.Second)
	h.waitFor(`got "\x1b[<0;8;6m"`, 5*time.Second)
	if s := h.screen(); strings.Contains(s, "lines back") || strings.Contains(s, ";1_") {
		t.Fatalf("mouse should go to the app, with no stray characters; screen:\n%s", s)
	}

	h.wtCtrlQ()
	h.waitFor("n new", 3*time.Second)
	h.wtType("q")
	h.wtType("y")
	h.waitExit(10 * time.Second)
}

// A bracketed paste reaches the agent as one paste, with win32-input-mode
// on. Windows Terminal sends it as VT (with CR for each line break).
func TestE2EWTPaste(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "pwsh.exe"), "./testdata/keyapp").CombinedOutput(); err != nil {
		t.Fatalf("build keyapp: %v\n%s", err, out)
	}
	path := bin + ";" + os.Getenv("SystemRoot") + `\System32`
	h := startHarness(t, exe, wtEnv(t.TempDir(), path), t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.wtType("n")
	h.waitFor("Start in", 5*time.Second)
	h.wtKey(vkReturn, '\r', 0)
	h.waitFor("keyapp ready", 10*time.Second)

	h.send("\x1b[200~hello world\rsecond [line] ~$_\x1b[201~")
	time.Sleep(500 * time.Millisecond)
	s := h.screen()
	// The app asked for bracketed paste, so it gets the paste bracketed.
	// x/vt may hand it over in more than one read; join what it printed.
	var got strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, `got "`); i >= 0 {
			got.WriteString(strings.TrimSuffix(strings.TrimSpace(line[i+5:]), `"`))
		}
	}
	// Bubble Tea's reader turns the line break into LF in this mode. (Without
	// win32-input-mode, the same paste arrives with the CR but with a NUL
	// before each shifted character: "\x00~\x00$\x00_".)
	want := `\x1b[200~hello world\nsecond [line] ~$_\x1b[201~`
	if got.String() != want {
		t.Fatalf("paste: got %s, want %s; screen:\n%s", got.String(), want, s)
	}

	h.wtCtrlQ()
	h.waitFor("n new", 3*time.Second)
	h.wtType("q")
	h.wtType("y")
	h.waitExit(10 * time.Second)
}

// In the session list, shift+enter from Windows Terminal (win32-input-mode
// records) starts a new session like the selected one.
func TestE2EWTShiftEnterNewSession(t *testing.T) {
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
	saved := fmt.Sprintf(`[{"id":"orig-0001","name":"orig","agent":"pwsh","dir":%q,"created":"2026-01-01T00:00:00Z","started":true}]`, proj)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startHarness(t, exe, wtEnv(home, path), t.TempDir())

	h.waitFor("⏎ restart · ⇧⏎ new", 10*time.Second)
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

// The config screen with win32-input-mode on: typing and pasting into its
// text fields, tab between fields, enter, and ctrl+s to save.
func TestE2EWTConfigScreen(t *testing.T) {
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	path := filepath.Dir(pwsh) + ";" + os.Getenv("SystemRoot") + `\System32`
	env := append(wtEnv(t.TempDir(), path), "DECKHAND_CONFIG="+cfg)
	h := startHarness(t, exe, env, t.TempDir())

	h.waitFor("No sessions yet", 10*time.Second)
	h.wtType("c")
	h.waitFor("+ Add agent", 5*time.Second)
	h.wtKey(vkReturn, '\r', 0)
	h.waitFor("New agent", 3*time.Second)
	h.wtType("wt-agent")
	h.waitFor("wt-agent", 3*time.Second)
	for i := 0; i < 3; i++ { // Kind, Pick, Command
		h.wtKey(0x09, '\t', 0) // VK_TAB
	}
	h.send("\x1b[200~Start-Pasted\n\x1b[201~") // bracketed paste, LF as Windows Terminal sends it
	h.waitFor("Start-Pasted", 3*time.Second)
	h.wtKey(vkReturn, '\r', 0)
	h.waitFor("unsaved changes", 3*time.Second)
	h.wtKey('S', 0x13, wtCtrl)
	h.waitFor("Saved and applied", 5*time.Second)
	data, _ := os.ReadFile(cfg)
	if !strings.Contains(string(data), `name = "wt-agent"`) || !strings.Contains(string(data), `pwsh = "Start-Pasted"`) {
		t.Fatalf("config.toml:\n%s\nscreen:\n%s", data, h.screen())
	}
	h.wtKey(0x1b, 0x1b, 0)
	h.waitFor("n new", 3*time.Second)
	h.wtType("q")
	h.waitExit(10 * time.Second)
}
