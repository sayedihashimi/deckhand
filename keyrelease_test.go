package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

var ctrlBackslash = tea.Key{Code: '\\', Mod: uv.ModCtrl}

// WezTerm's win32-input-mode encoding of ctrl+\ delivers only the release.
// A lone release of a ctrl/alt combination counts as the press. ctrl+\ is
// no longer the default toggle key (F3 is), but it can be configured, and
// the fallback covers any ctrl+symbol key.
func TestLoneReleaseCountsAsPress(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	withListOpts(t, listSettings{toggleKey: "ctrl+\\", statusLine: true})
	m := testModel("api")
	if tea.KeyPressMsg(ctrlBackslash).String() != listOpts.toggleKey {
		t.Fatalf("test key %q isn't the toggle key %q", tea.KeyPressMsg(ctrlBackslash).String(), listOpts.toggleKey)
	}
	m.Update(tea.KeyReleaseMsg(ctrlBackslash))
	if !m.listHidden {
		t.Fatal("a lone ctrl+\\ release should toggle the list")
	}
	m.Update(tea.KeyReleaseMsg(ctrlBackslash))
	if m.listHidden {
		t.Fatal("a second lone release should toggle it back")
	}
}

// A normal press and release acts once, on the press.
func TestPressThenReleaseActsOnce(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	withListOpts(t, listSettings{toggleKey: "ctrl+\\", statusLine: true})
	m := testModel("api")
	// NumLock is often on; the release must still match the press.
	press := ctrlBackslash
	press.Mod |= uv.ModNumLock
	m.Update(tea.KeyPressMsg(press))
	m.Update(tea.KeyReleaseMsg(ctrlBackslash))
	if !m.listHidden {
		t.Fatal("press then release should toggle the list exactly once")
	}
	// Auto-repeat: several presses, one release.
	m.Update(tea.KeyPressMsg(ctrlBackslash))
	m.Update(tea.KeyPressMsg(ctrlBackslash))
	m.Update(tea.KeyReleaseMsg(ctrlBackslash))
	if !m.listHidden {
		t.Fatal("two presses should toggle twice (back to hidden) and the release nothing more")
	}
}

// A plain key's lone release, such as the enter that started deckhand, is
// ignored.
func TestLonePlainReleaseIgnored(t *testing.T) {
	started := fakeStart(t)
	m := testModel("api")
	m.Update(tea.KeyReleaseMsg(tea.Key{Code: tea.KeyEnter}))
	m.Update(tea.KeyReleaseMsg(tea.Key{Code: 'q'}))
	if len(*started) != 0 || m.quitting || m.focus != focusSidebar {
		t.Fatalf("lone plain releases should do nothing (started %d, quitting %v, focus %d)", len(*started), m.quitting, m.focus)
	}
}

// alt+enter (ESC CR) is what terminals with shift+enter remapped to send ESC
// CR deliver; in the list it starts a new session like shift+enter.
func TestAltEnterStartsLikeSelected(t *testing.T) {
	started := fakeStart(t)
	m := testModel("api", "docs")
	m.sel = 0
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModAlt})
	if len(*started) != 1 || len(m.sessions) != 3 || m.sel != 1 {
		t.Fatalf("alt+enter: started %d, %d sessions, sel %d", len(*started), len(m.sessions), m.sel)
	}
	if m.sessions[1].Dir != m.sessions[0].Dir || m.sessions[1].Agent != m.sessions[0].Agent {
		t.Fatal("the new session should have the selected session's agent and folder")
	}
}
