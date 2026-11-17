package main

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// fakeStart replaces startSession for a test and records what it starts.
func fakeStart(t *testing.T) *[]*Session {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	var started []*Session
	old := startSession
	startSession = func(s *Session, w, h int, resume bool, dirty *atomic.Bool) error {
		if resume {
			t.Errorf("shift+enter started %s with resume", s.Name)
		}
		started = append(started, s)
		s.mu.Lock()
		s.running, s.Started = true, true
		s.mu.Unlock()
		return nil
	}
	t.Cleanup(func() { startSession = old })
	return &started
}

func shiftEnterKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: uv.ModShift} }

func TestShiftEnterKeyString(t *testing.T) {
	if got := shiftEnterKey().String(); got != "shift+enter" {
		t.Fatalf("got %q", got)
	}
}

func TestShiftEnterStartsLikeSelected(t *testing.T) {
	for _, running := range []bool{false, true} {
		started := fakeStart(t)
		m := testModel("api", "docs", "web")
		m.sessions[1].Agent = "claude"
		m.sessions[1].Dir = `C:\src\docs-repo`
		m.sessions[1].ClaudeSessionID = "old-conversation"
		m.sessions[1].Started = true
		m.sessions[1].running = running
		orig := m.sessions[1]
		m.sel = 1

		m.handleKey(shiftEnterKey())

		if len(*started) != 1 || len(m.sessions) != 4 {
			t.Fatalf("running=%v: started %d, %d sessions", running, len(*started), len(m.sessions))
		}
		n := m.sessions[2]
		if n != (*started)[0] || m.sel != 2 || m.focus != focusTerminal {
			t.Fatalf("running=%v: new session should be selected right below the original (sel %d)", running, m.sel)
		}
		if n.Agent != "claude" || n.Dir != `C:\src\docs-repo` || n.Name != "docs-repo" {
			t.Errorf("running=%v: new session %+v", running, n)
		}
		if n.ID == orig.ID || n.ClaudeSessionID == "" || n.ClaudeSessionID == orig.ClaudeSessionID {
			t.Errorf("running=%v: want a new ID and conversation, got %s / %s", running, n.ID, n.ClaudeSessionID)
		}
		if m.sessions[1] != orig || orig.ClaudeSessionID != "old-conversation" || orig.Name != "docs" || orig.running != running {
			t.Errorf("running=%v: original changed: %+v", running, orig)
		}
		if m.sessions[0].Name != "api" || m.sessions[3].Name != "web" {
			t.Errorf("running=%v: order %s, %s", running, m.sessions[0].Name, m.sessions[3].Name)
		}
	}
}

func TestShiftEnterClearsFilter(t *testing.T) {
	started := fakeStart(t)
	m := testModel("api", "docs", "api-tests")
	m.filter = "api"
	m.attentionOnly = true
	m.sessions[2].status = StatusWaiting
	m.sel = 2

	m.handleKey(shiftEnterKey())

	if len(*started) != 1 || m.sel != 3 || m.filtering() || m.current() != (*started)[0] {
		t.Fatalf("new session should be selected and visible; sel %d, filter %q %v", m.sel, m.filter, m.attentionOnly)
	}
}

func TestShiftEnterOnlyInList(t *testing.T) {
	started := fakeStart(t)
	m := testModel("api")
	m.sessions[0].running = true

	// Nothing selected (hidden by the filter): nothing happens.
	m.filter = "zzz"
	m.handleKey(shiftEnterKey())
	m.filter = ""

	// Inside a session, shift+enter goes to the agent.
	m.focus = focusTerminal
	m.handleKey(shiftEnterKey())
	if len(*started) != 0 || len(m.sessions) != 1 {
		t.Fatalf("started %d, %d sessions", len(*started), len(m.sessions))
	}
}

func TestExitedDetail(t *testing.T) {
	cases := []struct {
		detail string
		room   int
		want   string
	}{
		{"exited · ⏎ resume", 40, "exited · ⏎ resume · ⇧⏎ new"},
		{"exited · ⏎ resume", 17, "⏎ resume · ⇧⏎ new"},
		{"exited · ⏎ restart", 16, "⏎ restart ⇧⏎ new"},
		{"exited · ⏎ resume", 8, "exited · ⏎ resume"},
		{"⏎ resume", 40, "⏎ resume · ⇧⏎ new"},
		{"exited", 40, "exited"},
	}
	for _, c := range cases {
		if got := exitedDetail(c.detail, c.room); got != c.want {
			t.Errorf("%q in %d: got %q, want %q", c.detail, c.room, got, c.want)
		}
	}

	// The widest sidebar fits the hint for a Claude session.
	m := testModel("api")
	m.sessions[0].Agent = "claude"
	m.sessions[0].status = StatusExited
	m.sessions[0].detail = "exited · ⏎ resume"
	lines := m.renderSidebar(maxSidebarWidth, 8)
	if !strings.Contains(lines[3], "⏎ resume · ⇧⏎ new") {
		t.Errorf("sidebar row: %q", lines[3])
	}
}
