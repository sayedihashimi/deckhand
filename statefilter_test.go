package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// stateModel has one session in each status, named after it.
func stateModel() *model {
	m := testModel("starting", "idle", "working", "waiting", "exited", "error")
	for i, st := range []Status{StatusStarting, StatusIdle, StatusWorking, StatusWaiting, StatusExited, StatusError} {
		m.sessions[i].status = st
	}
	m.sel = -1 // nothing selected, so nothing is kept
	return m
}

func visibleNames(m *model) string {
	var out []string
	for _, i := range m.visible() {
		out = append(out, m.sessions[i].Name)
	}
	return strings.Join(out, " ")
}

func TestStateFilterVisible(t *testing.T) {
	m := stateModel()
	for _, c := range []struct {
		f    stateFilter
		want string
	}{
		{stateAll, "starting idle working waiting exited error"},
		{stateActive, "starting idle working waiting"},
		{stateInactive, "exited error"},
		{stateWaiting, "waiting"},
	} {
		m.stateFilter = c.f
		if got := visibleNames(m); got != c.want {
			t.Errorf("%v: visible %q, want %q", c.f, got, c.want)
		}
		if m.filtering() != (c.f != stateAll) {
			t.Errorf("%v: filtering() = %v", c.f, m.filtering())
		}
	}
}

func TestStateFilterCycle(t *testing.T) {
	var f stateFilter
	var got []string
	for range 5 {
		f = f.next()
		got = append(got, f.String())
	}
	if s := strings.Join(got, " "); s != "active inactive waiting all active" {
		t.Errorf("cycle: %s", s)
	}
}

func TestStateFilterWithTextAndAttention(t *testing.T) {
	m := testModel("api", "api-old", "docs", "web")
	m.sessions[1].status = StatusExited
	m.sessions[2].status = StatusWaiting
	m.sessions[3].unread = true
	m.sel = -1
	m.stateFilter = stateActive
	m.filter = "api"
	if got := visibleNames(m); got != "api" {
		t.Errorf("active + /api: %q", got)
	}
	m.stateFilter = stateInactive
	if got := visibleNames(m); got != "api-old" {
		t.Errorf("inactive + /api: %q", got)
	}
	m.filter = ""
	m.attentionOnly = true
	m.stateFilter = stateActive
	if got := visibleNames(m); got != "docs web" {
		t.Errorf("active + @: %q", got)
	}
	m.stateFilter = stateWaiting
	if got := visibleNames(m); got != "docs" {
		t.Errorf("waiting + @: %q", got)
	}
	m.clearFilter()
	if m.filtering() || m.stateFilter != stateAll || visibleNames(m) != "api api-old docs web" {
		t.Errorf("clearFilter: filtering %v, state %v, visible %q", m.filtering(), m.stateFilter, visibleNames(m))
	}
}

func TestStateFilterGroups(t *testing.T) {
	m := groupedModel(t, "api@app", "web@app", "docs@cli", "notes")
	m.sessions[1].status = StatusExited // web
	m.sessions[2].status = StatusExited // docs
	m.onHeader, m.selHeader = true, "app"
	m.stateFilter = stateInactive
	if got := rowsText(m); got != "[app] web [cli] docs" {
		t.Errorf("inactive, expanded: %q", got)
	}
	m.setCollapsed("app", true)
	if got := rowsText(m); got != "▸[app] web [cli] docs" {
		t.Errorf("inactive, app collapsed: %q (matches in a collapsed group show)", got)
	}
	m.stateFilter = stateActive
	if got := rowsText(m); got != "▸[app] api [] notes" {
		t.Errorf("active: %q (empty headers hide)", got)
	}
	m.stateFilter = stateWaiting
	if got := rowsText(m); got != "" {
		t.Errorf("waiting: %q", got)
	}
	m.stateFilter = stateAll
	if got := rowsText(m); got != "▸[app] [cli] docs [] notes" {
		t.Errorf("all: %q", got)
	}
}

// The selected session stays while its state changes, and drops out once
// the selection moves.
func TestStateFilterKeepsSelected(t *testing.T) {
	m := testModel("a", "b", "c")
	m.sessions[0].status = StatusWaiting
	m.sessions[1].status = StatusWaiting
	m.sel = 0
	m.stateFilter = stateWaiting
	if got := visibleNames(m); got != "a b" {
		t.Fatalf("waiting: %q", got)
	}
	m.sessions[0].status = StatusIdle // a stops waiting while selected
	if got := visibleNames(m); got != "a b" {
		t.Errorf("selected a should stay: %q", got)
	}
	m.moveSel(1)
	if m.sel != 1 {
		t.Fatalf("sel = %d", m.sel)
	}
	if got := visibleNames(m); got != "b" {
		t.Errorf("a should drop out once the selection moves: %q", got)
	}
	// A session selected some other way, such as a new one, shows too.
	m.sel = 2
	if got := visibleNames(m); got != "b c" {
		t.Errorf("selected c should show: %q", got)
	}
}

// s moves the selection off a session the new filter hides, and with
// nothing to show, nothing is selected.
func TestStateFilterKey(t *testing.T) {
	m := testModel("live", "old")
	m.sessions[1].status = StatusExited
	m.width, m.height = 100, 30
	press := func(s string) {
		k := tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
		if s == "esc" {
			k = tea.KeyPressMsg{Code: tea.KeyEscape}
		}
		m.handleKey(k)
	}
	header := func() string { return ansi.Strip(m.renderSidebar(60, 20)[0]) }
	press("s")
	if m.stateFilter != stateActive || visibleNames(m) != "live" || m.current() != m.sessions[0] {
		t.Errorf("active: visible %q, current %v", visibleNames(m), m.current())
	}
	if h := header(); !strings.Contains(h, "filter: active") || !strings.Contains(h, "esc clears") {
		t.Errorf("header %q", h)
	}
	press("s")
	if m.stateFilter != stateInactive || visibleNames(m) != "old" || m.current() != m.sessions[1] {
		t.Errorf("inactive: visible %q, current %v", visibleNames(m), m.current())
	}
	press("s")
	if m.stateFilter != stateWaiting || visibleNames(m) != "" || m.current() != nil {
		t.Errorf("waiting: visible %q, current %v", visibleNames(m), m.current())
	}
	press("s")
	if m.stateFilter != stateAll || visibleNames(m) != "live old" {
		t.Errorf("all: visible %q", visibleNames(m))
	}
	press("s")
	press("esc")
	if m.stateFilter != stateAll || m.filtering() {
		t.Errorf("esc should clear the state filter: %v", m.stateFilter)
	}
	if h := header(); strings.Contains(h, "filter:") {
		t.Errorf("header after esc %q", h)
	}
}

// Sessions restored from sessions.json are exited, so they count as
// inactive until they're started.
func TestStateFilterRestoredIsInactive(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	saved := `[{"id":"old-0001","name":"old","agent":"pwsh","dir":"C:\\x","created":"2026-01-01T00:00:00Z","started":true}]`
	if err := os.WriteFile(filepath.Join(dataDir(), "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{sessions: loadSessions(), sel: -1}
	if len(m.sessions) != 1 {
		t.Fatalf("loaded %d sessions", len(m.sessions))
	}
	m.stateFilter = stateInactive
	if got := visibleNames(m); got != "old" {
		t.Errorf("inactive: %q", got)
	}
	m.stateFilter = stateActive
	if got := visibleNames(m); got != "" {
		t.Errorf("active: %q", got)
	}
}
