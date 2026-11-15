package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// Keys as the Windows console reports them must reach the agent intact.
func TestForwardedKeys(t *testing.T) {
	cases := []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Code: tea.KeyDown}, "\x1b[B"},
		{tea.KeyPressMsg{Code: tea.KeyDown, Mod: uv.ModNumLock}, "\x1b[B"},
		{tea.KeyPressMsg{Code: tea.KeyUp, IsRepeat: true, BaseCode: tea.KeyUp}, "\x1b[A"},
		{tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModCapsLock}, "\r"},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, "\x1b"},
		{tea.KeyPressMsg{Code: 'c', Mod: uv.ModCtrl | uv.ModNumLock}, "\x03"},
		{tea.KeyPressMsg{Code: 'a', Text: "A", ShiftedCode: 'A', Mod: uv.ModShift}, "A"},
	}
	for _, c := range cases {
		emu := vt.NewSafeEmulator(80, 24)
		go emu.SendKey(toUVKey(c.key))
		got := make(chan string, 1)
		go func() {
			buf := make([]byte, 32)
			n, _ := emu.Read(buf)
			got <- string(buf[:n])
		}()
		select {
		case g := <-got:
			if g != c.want {
				t.Errorf("%+v: got %q, want %q", c.key, g, c.want)
			}
		case <-time.After(time.Second):
			t.Errorf("%+v: nothing sent, want %q", c.key, c.want)
		}
	}
}
