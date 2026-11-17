package main

import (
	"os"
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
	// F3 by default, as the Windows console reports it (NumLock on).
	toggle := tea.KeyPressMsg{Code: tea.KeyF3, Mod: uv.ModNumLock}
	if toggle.String() != defaultToggleKey {
		t.Fatalf("F3 is %q", toggle.String())
	}
	m.handleKey(toggle)
	if !m.listHidden {
		t.Fatal("F3 should hide the list")
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
		t.Errorf("F3 in a session: hidden=%v focus=%v", m.listHidden, m.focus)
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
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF3})
	if m.listHidden {
		t.Fatal("the default F3 shouldn't toggle when another key is configured")
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
	if got := ansi.Strip(m.statusLine()); got != `2 need you · 1 finished · F3 list ` {
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
	if !strings.Contains(row, "deckhand") || strings.Contains(row, `F3 list`) {
		t.Errorf("list shown: header row %q", row)
	}

	m.setListHidden(true)
	row = headerRow(m)
	if strings.Contains(row, "deckhand") || strings.Contains(row, "│") {
		t.Errorf("list hidden: the sidebar still shows: %q", row)
	}
	if !strings.HasSuffix(row, `1 need you · F3 list `) || !strings.HasPrefix(row, " ") || !strings.Contains(row, "api") {
		t.Errorf("list hidden: header row %q", row)
	}
	if n := ansi.StringWidth(row); n != 100 {
		t.Errorf("header row is %d cells wide, want 100", n)
	}

	// status_line = false: the plain session header.
	listOpts.statusLine = false
	row = headerRow(m)
	if strings.Contains(row, "need you") || strings.Contains(row, `F3 list`) || !strings.Contains(row, "api") {
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
	for _, bad := range []string{"x", "ctrl+q", "enter", "esc", "f1"} {
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
		if !strings.Contains(f, `F3 list`) || !strings.Contains(f, "q quit") {
			t.Errorf("width %d: footer %q", w, f)
		}
	}
	m.focus = focusTerminal
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, `F3 hide list`) {
		t.Errorf("terminal footer %q", f)
	}
	m.listHidden = true
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, `F3 show list`) {
		t.Errorf("terminal footer while hidden %q", f)
	}

	// A configured function key shows as "F2".
	listOpts.toggleKey = "f2"
	m.focus, m.listHidden = focusSidebar, false
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, "F2 list · ? help") {
		t.Errorf("footer with f2: %q", f)
	}
	// ... and in the help overlay.
	m.openHelp()
	if s := ansi.Strip(strings.Join(m.overlayHelp(make([]string, 31), 89, 31), "\n")); !strings.Contains(s, "F2 ") || strings.Contains(s, `ctrl+\`) {
		t.Errorf("help overlay with f2:\n%s", s)
	}
}

func TestKeyLabel(t *testing.T) {
	for in, want := range map[string]string{"f2": "F2", "f12": "F12", "ctrl+\\": "ctrl+\\", "foo": "foo"} {
		if got := keyLabel(in); got != want {
			t.Errorf("keyLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// The config screen keeps [list] through a decode and encode, and leaves it
// out when it isn't set.
func TestConfigListRoundTrip(t *testing.T) {
	tempConfig(t, "")
	text := "[list]\ntoggle_key = \"f2\"\nstatus_line = false\n"
	cfg, raw, err := decodeConfig(text)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.List.ToggleKey != "f2" || cfg.List.StatusLine == nil || *cfg.List.StatusLine {
		t.Fatalf("decoded %+v", cfg.List)
	}
	out, err := encodeConfig(cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseListConfig(out)
	if err != nil || got.toggleKey != "f2" || got.statusLine {
		t.Errorf("after round trip: %+v, %v\n%s", got, err, out)
	}

	cfg, raw, _ = decodeConfig("[open]\neditor = 'code \"{dir}\"'\n")
	if out, _ := encodeConfig(cfg, raw); strings.Contains(out, "[list]") {
		t.Errorf("[list] written though it wasn't set:\n%s", out)
	}
	if err := checkConfigText("[list]\ntoggle_key = \"enter\"\n"); err == nil {
		t.Error("checkConfigText should refuse toggle_key = \"enter\"")
	}
}

// Setting the toggle key and the status line on the config screen saves
// them and applies them straight away.
func TestConfigScreenListSettings(t *testing.T) {
	keepGlobals(t)
	t.Setenv("DECKHAND_HOME", t.TempDir())
	p := tempConfig(t, "")
	m := testModel("api")
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	if m.cfg == nil {
		t.Fatal("c should open the config screen")
	}
	// The whole screen, the new section and Save included, fits at 120×32.
	w, _ := m.termSize()
	screen := ansi.Strip(strings.Join(m.renderConfig(w, m.height-1), "\n"))
	for _, want := range []string{"Session list", "Hide key", "Status", "Save"} {
		if !strings.Contains(screen, want) {
			t.Errorf("config screen at 120×32 lacks %q:\n%s", want, screen)
		}
	}

	// A key it can't be is refused, on its row, and nothing is written.
	m.cfg.focusRow(m.cfg.rowOf(rowListKey))
	typeText(m, "x")
	m.handleKey(key("ctrl+s"))
	if !m.cfg.statusErr || m.cfg.current().kind != rowListKey {
		t.Fatalf("bad key: status %q, row %v", m.cfg.status, m.cfg.current())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("nothing should be written for a bad key")
	}

	m.cfg.listKey.SetValue("")
	typeText(m, "F3")
	m.handleKey(key("enter")) // to the status line row
	if m.cfg.current().kind != rowListStatus {
		t.Fatalf("enter should move to the status row, at %v", m.cfg.current())
	}
	m.handleKey(key("enter")) // off
	m.handleKey(key("ctrl+s"))
	if m.cfg.statusErr || m.cfg.dirty() {
		t.Fatalf("save: %q", m.cfg.status)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `toggle_key = "f3"`) || !strings.Contains(string(data), "status_line = false") {
		t.Errorf("config.toml:\n%s", data)
	}
	if listOpts.toggleKey != "f3" || listOpts.statusLine {
		t.Errorf("listOpts not reloaded: %+v", listOpts)
	}
	// Opening the screen again shows what was saved.
	m.handleKey(key("esc"))
	m.handleKey(key("c"))
	if m.cfg.listKey.Value() != "f3" || m.cfg.listStatus || m.cfg.dirty() {
		t.Errorf("reopened: key %q status %v dirty %v", m.cfg.listKey.Value(), m.cfg.listStatus, m.cfg.dirty())
	}
}
