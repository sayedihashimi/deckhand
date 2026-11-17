package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// Keyboard setups an agent can ask for, as the output it writes.
const (
	legacyApp = ""
	// What Claude Code writes at startup: kitty flags and modifyOtherKeys.
	claudeApp = "\x1b[<u\x1b[>5u\x1b[>4;2m"
	// modifyOtherKeys only.
	xtermApp = "\x1b[>4;2m"
)

// Keys as the Windows console reports them must reach the agent intact.
func TestForwardedKeys(t *testing.T) {
	shiftEnter := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModShift}
	ctrlEnter := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModCtrl}
	cases := []struct {
		app  string
		key  tea.KeyPressMsg
		want string
	}{
		{legacyApp, tea.KeyPressMsg{Code: tea.KeyDown}, "\x1b[B"},
		{legacyApp, tea.KeyPressMsg{Code: tea.KeyDown, Mod: uv.ModNumLock}, "\x1b[B"},
		{legacyApp, tea.KeyPressMsg{Code: tea.KeyUp, IsRepeat: true, BaseCode: tea.KeyUp}, "\x1b[A"},
		{legacyApp, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModCapsLock}, "\r"},
		{legacyApp, tea.KeyPressMsg{Code: tea.KeyEscape}, "\x1b"},
		{legacyApp, tea.KeyPressMsg{Code: 'c', Mod: uv.ModCtrl | uv.ModNumLock}, "\x03"},
		{legacyApp, tea.KeyPressMsg{Code: 'a', Text: "A", ShiftedCode: 'A', Mod: uv.ModShift}, "A"},
		{legacyApp, tea.KeyPressMsg{Code: tea.KeyTab, Mod: uv.ModShift}, "\x1b[Z"},

		// Claude Code: shift+enter must arrive as a new line, not "\r".
		{claudeApp, shiftEnter, "\x1b[13;2u"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModShift | uv.ModNumLock}, "\x1b[13;2u"},
		{claudeApp, ctrlEnter, "\x1b[13;5u"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModCtrl | uv.ModShift}, "\x1b[13;6u"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModShift | uv.ModAlt}, "\x1b[13;4u"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyTab, Mod: uv.ModCtrl}, "\x1b[9;5u"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyTab, Mod: uv.ModCtrl | uv.ModShift}, "\x1b[9;6u"},
		// Keys legacy already expresses stay as they were.
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyEnter}, "\r"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModAlt}, "\x1b\r"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyTab}, "\t"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyTab, Mod: uv.ModShift}, "\x1b[Z"},
		{claudeApp, tea.KeyPressMsg{Code: tea.KeyEscape}, "\x1b"},
		{claudeApp, tea.KeyPressMsg{Code: 'c', Mod: uv.ModCtrl}, "\x03"},

		{xtermApp, shiftEnter, "\x1b[27;2;13~"},
		{xtermApp, ctrlEnter, "\x1b[27;5;13~"},

		// Popped kitty flags and reset modifyOtherKeys: back to legacy.
		{claudeApp + "\x1b[<u\x1b[>4m", shiftEnter, "\r"},
		// Each screen has its own kitty stack.
		{"\x1b[>1u\x1b[?1049h", shiftEnter, "\r"},
		{"\x1b[?1049h\x1b[>1u", shiftEnter, "\x1b[13;2u"},
		{"\x1b[?1049h\x1b[>1u\x1b[?1049l", shiftEnter, "\r"},
		// CSI = flags ; mode u changes the top entry in place.
		{"\x1b[>1u\x1b[=1;3u", shiftEnter, "\r"},
		{"\x1b[=1;1u", shiftEnter, "\x1b[13;2u"},
	}
	for _, c := range cases {
		emu := vt.NewSafeEmulator(80, 24)
		keys := &keyProtocol{}
		keys.register(emu.Emulator)
		_, _ = emu.Write([]byte(c.app))
		go sendKey(emu, keys, toUVKey(c.key))
		got := make(chan string, 1)
		go func() {
			buf := make([]byte, 32)
			n, _ := emu.Read(buf)
			got <- string(buf[:n])
		}()
		select {
		case g := <-got:
			if g != c.want {
				t.Errorf("app %q, %+v: got %q, want %q", c.app, c.key, g, c.want)
			}
		case <-time.After(time.Second):
			t.Errorf("app %q, %+v: nothing sent, want %q", c.app, c.key, c.want)
		}
	}
}

