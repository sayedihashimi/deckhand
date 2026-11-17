package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// withListOpts sets the [list] settings for a test and keeps prefs.json in
// a temporary folder.
func withListOpts(t *testing.T, o listSettings) {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	old := listOpts
	listOpts = o
	t.Cleanup(func() { listOpts = old })
}

func TestToggleListTermSize(t *testing.T) {
	withListOpts(t, defaultListSettings())
	m := testModel("api", "docs")
	m.width, m.height = 120, 32
	if w, h := m.termSize(); w != 120-m.sidebarWidth()-1 || h != 30 {
		t.Fatalf("list shown: termSize = %d×%d", w, h)
	}
	// ctrl+\ by default, as the Windows console reports it.
	toggle := tea.KeyPressMsg{Code: '\\', Mod: uv.ModCtrl | uv.ModNumLock}
	if toggle.String() != defaultToggleKey {
		t.Fatalf("ctrl+\\ is %q", toggle.String())
	}
	m.handleKey(toggle)
	if !m.listHidden {
		t.Fatal("ctrl+\\ should hide the list")
	}
	if w, h := m.termSize(); w != 120 || h != 30 {
		t.Errorf("list hidden: termSize = %d×%d, want 120×30", w, h)
	}
	if m.mainLeft() != 0 {
		t.Errorf("mainLeft = %d", m.mainLeft())
	}
	if !loadPrefs().ListHidden {
		t.Error("hidden state should be saved in prefs.json")
	}

	// Inside a session, the key is taken by deckhand and shows the list again.
	m.focus = focusTerminal
	m.handleKey(toggle)
	if m.listHidden || m.focus != focusTerminal {
		t.Errorf("ctrl+\\ in a session: hidden=%v focus=%v", m.listHidden, m.focus)
	}
	if loadPrefs().ListHidden {
		t.Error("shown state should be saved in prefs.json")
	}

	// ctrl+q while hidden shows the list and focuses it.
	m.handleKey(toggle)
	m.handleKey(tea.KeyPressMsg{Code: 'q', Mod: uv.ModCtrl})
	if m.listHidden || m.focus != focusSidebar {
		t.Errorf("ctrl+q while hidden: hidden=%v focus=%v", m.listHidden, m.focus)
	}
}

func TestToggleKeyFromConfig(t *testing.T) {
	withListOpts(t, listSettings{toggleKey: "f2", statusLine: true})
	m := testModel("api")
	m.width, m.height = 120, 32
	m.handleKey(tea.KeyPressMsg{Code: '\\', Mod: uv.ModCtrl})
	if m.listHidden {
		t.Fatal("ctrl+\\ shouldn't toggle when another key is configured")
	}
	k := tea.KeyPressMsg{Code: tea.KeyF2, Mod: uv.ModNumLock}
	if k.String() != "f2" {
		t.Fatalf("F2 is %q", k.String())
	}
	m.handleKey(k)
	if !m.listHidden {
		t.Error("F2 should hide the list")
	}
}

func TestStatusLineParts(t *testing.T) {
	cases := []struct {
		waiting, finished int
		key               string
		want              string
	}{
		{0, 0, "f2", "F2 list"},
		{2, 0, "f2", "2 need you · F2 list"},
		{0, 1, "f2", "1 finished · F2 list"},
		{2, 1, "f2", "2 need you · 1 finished · F2 list"},
		{1, 3, "ctrl+\\", "1 need you · 3 finished · ctrl+\\ list"},
	}
	for _, c := range cases {
		need, done, hint := statusLineParts(c.waiting, c.finished, c.key)
		var parts []string
		for _, p := range []string{need, done, hint} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if got := strings.Join(parts, " · "); got != c.want {
			t.Errorf("%d waiting, %d finished: got %q, want %q", c.waiting, c.finished, got, c.want)
		}
	}
}

func TestStatusLineCounts(t *testing.T) {
	withListOpts(t, defaultListSettings())
	m := testModel("api", "docs", "web", "notes")
	m.sessions[0].status = StatusWaiting
	m.sessions[1].status = StatusWaiting
	m.sessions[2].status = StatusIdle
	m.sessions[2].unread = true
	if w, f := attentionCounts(m.sessions); w != 2 || f != 1 {
		t.Errorf("attentionCounts = %d, %d; want 2, 1", w, f)
	}
	if got := ansi.Strip(m.statusLine()); got != `2 need you · 1 finished · ctrl+\ list ` {
		t.Errorf("statusLine = %q", got)
	}
}

// headerRow renders the view and returns its first line without styles.
func headerRow(m *model) string {
	return strings.SplitN(ansi.Strip(m.View().Content), "\n", 2)[0]
}

func TestStatusLineInView(t *testing.T) {
	withListOpts(t, defaultListSettings())
	m := testModel("api", "docs")
	m.sessions[1].status = StatusWaiting
	m.width, m.height = 100, 20

	row := headerRow(m)
	if !strings.Contains(row, "deckhand") || strings.Contains(row, `ctrl+\ list`) {
		t.Errorf("list shown: header row %q", row)
	}

	m.setListHidden(true)
	row = headerRow(m)
	if strings.Contains(row, "deckhand") || strings.Contains(row, "│") {
		t.Errorf("list hidden: the sidebar still shows: %q", row)
	}
	if !strings.HasSuffix(row, `1 need you · ctrl+\ list `) || !strings.HasPrefix(row, " ") || !strings.Contains(row, "api") {
		t.Errorf("list hidden: header row %q", row)
	}
	if n := ansi.StringWidth(row); n != 100 {
		t.Errorf("header row is %d cells wide, want 100", n)
	}

	// status_line = false: the plain session header.
	listOpts.statusLine = false
	row = headerRow(m)
	if strings.Contains(row, "need you") || strings.Contains(row, `ctrl+\ list`) || !strings.Contains(row, "api") {
		t.Errorf("status line off: header row %q", row)
	}
}

func TestParseListConfig(t *testing.T) {
	got, err := parseListConfig("")
	if err != nil || got != defaultListSettings() {
		t.Errorf("empty: %+v, %v", got, err)
	}
	got, err = parseListConfig("[list]\ntoggle_key = \"F3\"\nstatus_line = false\n")
	if err != nil || got.toggleKey != "f3" || got.statusLine {
		t.Errorf("f3/off: %+v, %v", got, err)
	}
	got, err = parseListConfig("[list]\ntoggle_key = \"f2\"\n")
	if err != nil || got.toggleKey != "f2" {
		t.Errorf("f2: %+v, %v", got, err)
	}
	got, err = parseListConfig("[list]\ntoggle_key = 'ctrl+\\'\n")
	if err != nil || got.toggleKey != "ctrl+\\" || !got.statusLine {
		t.Errorf("ctrl+\\: %+v, %v", got, err)
	}
	for _, bad := range []string{"x", "ctrl+q", "enter", "esc"} {
		got, err := parseListConfig("[list]\ntoggle_key = \"" + bad + "\"\n")
		if err == nil || got.toggleKey != defaultToggleKey {
			t.Errorf("%q: want an error and the default key, got %+v, %v", bad, got, err)
		}
	}
	if _, err := parseListConfig("[list]\nstatus_line = \"yes\"\n"); err == nil {
		t.Error("status_line must be a boolean")
	}
}

func TestPrefsListHiddenRoundTrip(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	p := loadPrefs()
	p.LastAgent = "claude"
	p.ListHidden = true
	if err := p.save(); err != nil {
		t.Fatal(err)
	}
	q := loadPrefs()
	if !q.ListHidden || q.LastAgent != "claude" {
		t.Errorf("loaded %+v", q)
	}
	// Saving other prefs (as the new-session dialog does) keeps it.
	q.useDir(`C:\src`)
	_ = q.save()
	if !loadPrefs().ListHidden {
		t.Error("list_hidden lost after saving other prefs")
	}
}

func TestFooterFitsWithToggleKey(t *testing.T) {
	withListOpts(t, defaultListSettings())
	m := testModel("api")
	for _, w := range []int{120, 200} {
		f := ansi.Strip(m.renderFooter(w))
		if !strings.Contains(f, `ctrl+\ list`) || !strings.Contains(f, "q quit") {
			t.Errorf("width %d: footer %q", w, f)
		}
	}
	m.focus = focusTerminal
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, `ctrl+\ hide list`) {
		t.Errorf("terminal footer %q", f)
	}
	m.listHidden = true
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, `ctrl+\ show list`) {
		t.Errorf("terminal footer while hidden %q", f)
	}

	// A configured function key shows as "F2".
	listOpts.toggleKey = "f2"
	m.focus, m.listHidden = focusSidebar, false
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, "F2 list · q quit") {
		t.Errorf("footer with f2: %q", f)
	}
}

func TestKeyLabel(t *testing.T) {
	for in, want := range map[string]string{"f2": "F2", "f12": "F12", "ctrl+\\": "ctrl+\\", "foo": "foo"} {
		if got := keyLabel(in); got != want {
			t.Errorf("keyLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
