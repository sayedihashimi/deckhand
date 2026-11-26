package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// names is the history as session names, with the current one in brackets.
func (h *visitHistory) names() string {
	var out []string
	for i, s := range h.entries {
		if i == h.pos {
			out = append(out, "["+s.Name+"]")
		} else {
			out = append(out, s.Name)
		}
	}
	return strings.Join(out, " ")
}

func sessionsNamed(names ...string) map[string]*Session {
	out := map[string]*Session{}
	for _, n := range names {
		out[n] = &Session{ID: n, Name: n}
	}
	return out
}

func TestVisitHistoryRules(t *testing.T) {
	s := sessionsNamed("a", "b", "c", "d")
	all := func(*Session) bool { return true }
	var h visitHistory
	if _, ok := h.step(-1, nil, all); ok {
		t.Fatal("an empty history went back")
	}
	h.visit(s["a"])
	h.visit(s["a"]) // the same session again adds nothing
	h.visit(s["b"])
	h.visit(s["c"])
	if got := h.names(); got != "a b [c]" {
		t.Fatalf("after three visits: %s", got)
	}
	if got, ok := h.step(1, s["c"], all); ok {
		t.Fatalf("forward at the end went to %s", got.Name)
	}
	if got, ok := h.step(-1, s["c"], all); !ok || got != s["b"] {
		t.Fatalf("back: %v %v", got, ok)
	}
	if got, ok := h.step(-1, s["b"], all); !ok || got != s["a"] {
		t.Fatalf("back again: %v %v", got, ok)
	}
	if _, ok := h.step(-1, s["a"], all); ok {
		t.Fatal("back at the start moved")
	}
	if got, ok := h.step(1, s["a"], all); !ok || got != s["b"] || h.names() != "a [b] c" {
		t.Fatalf("forward: %v %v, %s", got, ok, h.names())
	}
	// Selecting the current one again keeps the entries ahead.
	h.visit(s["b"])
	if got := h.names(); got != "a [b] c" {
		t.Fatalf("revisit: %s", got)
	}
	// A new visit after going back drops the entries ahead.
	h.visit(s["d"])
	if got := h.names(); got != "a b [d]" {
		t.Fatalf("a new visit after back: %s", got)
	}

	// Hidden sessions are skipped, both ways.
	hidden := func(x *Session) bool { return x != s["b"] }
	if got, ok := h.step(-1, s["d"], hidden); !ok || got != s["a"] || h.pos != 0 {
		t.Fatalf("back past a hidden one: %v %v, %s", got, ok, h.names())
	}
	if got, ok := h.step(1, s["a"], hidden); !ok || got != s["d"] {
		t.Fatalf("forward past a hidden one: %v %v, %s", got, ok, h.names())
	}

	// From a group header (nothing selected), back returns to the
	// current entry first.
	if got, ok := h.step(-1, nil, all); !ok || got != s["d"] || h.pos != 2 {
		t.Fatalf("back from a header: %v %v, %s", got, ok, h.names())
	}
}

func TestVisitHistoryPrune(t *testing.T) {
	s := sessionsNamed("a", "b", "c")
	var h visitHistory
	for _, n := range []string{"a", "b", "a", "c", "b"} {
		h.visit(s[n])
	}
	h.pos = 3 // at c, with b ahead
	// Removing b leaves a a c: the two a's merge, and c stays current.
	h.prune(func(x *Session) bool { return x != s["b"] })
	if got := h.names(); got != "a [c]" {
		t.Fatalf("after removing b: %s", got)
	}
	// Removing the current one moves to the entry before it.
	h.prune(func(x *Session) bool { return x != s["c"] })
	if got := h.names(); got != "[a]" {
		t.Fatalf("after removing c: %s", got)
	}
	h.prune(func(*Session) bool { return false })
	if len(h.entries) != 0 || h.pos != 0 {
		t.Fatalf("after removing all: %s (pos %d)", h.names(), h.pos)
	}
	h.visit(s["a"])
	if got := h.names(); got != "[a]" {
		t.Fatalf("a visit after emptying: %s", got)
	}
}

func TestVisitHistoryCap(t *testing.T) {
	var h visitHistory
	var all []*Session
	for i := range maxVisits + 10 {
		s := &Session{ID: fmt.Sprint(i), Name: fmt.Sprint(i)}
		all = append(all, s)
		h.visit(s)
	}
	if len(h.entries) != maxVisits || h.entries[0] != all[10] || h.pos != maxVisits-1 || h.entries[h.pos] != all[len(all)-1] {
		t.Fatalf("len %d, first %s, pos %d", len(h.entries), h.entries[0].Name, h.pos)
	}
}

// update sends msg through Update, as the program does, so visits are
// recorded.
func update(m *model, msg tea.Msg) { m.Update(msg) }

// nopMsg is a message nothing handles: the first one records the starting
// selection as a visit, as the program's first messages do.
type nopMsg struct{}

var (
	keyHistBack = tea.KeyPressMsg{Code: tea.KeyF7}
	keyHistFwd  = tea.KeyPressMsg{Code: tea.KeyF8}
	keyDown     = tea.KeyPressMsg{Code: tea.KeyDown}
	keyUp       = tea.KeyPressMsg{Code: tea.KeyUp}
)

func selectedName(m *model) string {
	if s := m.current(); s != nil {
		return s.Name
	}
	return "-"
}

func TestHistoryKeysInTheList(t *testing.T) {
	m := padModel(t, "a", "b", "c", "d")
	update(m, nopMsg{}) // a
	update(m, keyDown)  // b
	update(m, keyDown)  // c
	update(m, keyDown)  // d
	if got := m.visits.names(); got != "a b c [d]" {
		t.Fatalf("visits: %s", got)
	}
	update(m, keyHistBack)
	update(m, keyHistBack)
	update(m, keyHistFwd)
	if selectedName(m) != "c" || m.focus != focusSidebar || m.visits.names() != "a b [c] d" {
		t.Fatalf("back, back, forward: %s, focus %v, visits %s", selectedName(m), m.focus, m.visits.names())
	}
	// alt+← and alt+→ do the same in the list.
	update(m, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt})
	if selectedName(m) != "b" {
		t.Fatalf("alt+left: %s", selectedName(m))
	}
	update(m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt})
	if selectedName(m) != "c" {
		t.Fatalf("alt+right: %s", selectedName(m))
	}
	// At the start, nothing moves and the footer says so.
	for range 3 {
		update(m, keyHistBack)
	}
	if selectedName(m) != "a" || !strings.Contains(m.renderFooter(120), "No earlier session") {
		t.Fatalf("back at the start: %s, footer %q", selectedName(m), m.renderFooter(120))
	}
	// A new visit drops the ones ahead.
	update(m, keyDown) // b
	update(m, keyHistFwd)
	if selectedName(m) != "b" || m.visits.names() != "a [b]" || !strings.Contains(m.renderFooter(120), "No later session") {
		t.Fatalf("forward after a new visit: %s, visits %s, footer %q", selectedName(m), m.visits.names(), m.renderFooter(120))
	}
	// Removing a session drops it from the history.
	update(m, keyDown) // c
	update(m, keyDown) // d
	m.removeSession(m.sessions[2])
	update(m, keyHistBack)
	if selectedName(m) != "b" || m.visits.names() != "a [b] d" {
		t.Fatalf("back after removing c: %s, visits %s", selectedName(m), m.visits.names())
	}
}

func TestHistorySkipsFilteredAndOpensGroups(t *testing.T) {
	m := groupedModel(t, "api@app", "docs@app", "web@site")
	visit := func(i int) {
		m.selectSession(i)
		update(m, nopMsg{})
	}
	visit(0)
	visit(1)
	m.onHeader, m.selHeader = true, "site" // a header isn't a visit
	update(m, nopMsg{})
	visit(2)
	if got := m.visits.names(); got != "api docs [web]" {
		t.Fatalf("visits: %s", got)
	}
	// A filter that hides the others: back has nowhere to go, and the
	// filter stays.
	m.filter = "web"
	update(m, keyHistBack)
	if selectedName(m) != "web" || m.filter != "web" || !strings.Contains(m.renderFooter(120), "No earlier session") {
		t.Fatalf("back with only web shown: %s, filter %q", selectedName(m), m.filter)
	}
	// One that shows only api: back skips docs (and web, which it hides).
	m.filter = "api"
	update(m, keyHistBack)
	if selectedName(m) != "api" || m.filter != "api" || m.visits.pos != 0 {
		t.Fatalf("back past hidden docs: %s, filter %q, visits %s", selectedName(m), m.filter, m.visits.names())
	}
	m.clearFilter()

	// A target in a collapsed group opens the group.
	update(m, keyHistFwd) // docs
	update(m, keyHistFwd) // web
	if selectedName(m) != "web" {
		t.Fatalf("forward twice: %s, visits %s", selectedName(m), m.visits.names())
	}
	m.setCollapsed("app", true)
	update(m, keyHistBack)
	if selectedName(m) != "docs" || m.collapsed("app") {
		t.Fatalf("back into a collapsed group: %s, collapsed %v", selectedName(m), m.collapsed("app"))
	}
}

func TestHistoryInsideASession(t *testing.T) {
	m := padModel(t, "a", "b", "c")
	a, b, c := m.sessions[0], m.sessions[1], m.sessions[2]
	a.running, b.running = true, true
	update(m, nopMsg{}) // a
	update(m, keyDown)  // b
	update(m, keyDown)  // c, not running
	m.focus = focusTerminal
	update(m, keyHistBack)
	if selectedName(m) != "b" || m.focus != focusTerminal {
		t.Fatalf("back from a terminal: %s, focus %v", selectedName(m), m.focus)
	}
	update(m, keyHistBack)
	if selectedName(m) != "a" || m.focus != focusTerminal {
		t.Fatalf("back again: %s, focus %v", selectedName(m), m.focus)
	}
	update(m, keyHistFwd)
	// A session that isn't running has no terminal to type into: the list.
	update(m, keyHistFwd)
	if selectedName(m) != "c" || m.focus != focusSidebar {
		t.Fatalf("forward to an exited one: %s, focus %v", selectedName(m), m.focus)
	}
	// From the pad, you land in the other session's terminal.
	update(m, keyHistBack) // b, in the list
	m.focus = focusTerminal
	update(m, tea.KeyPressMsg{Code: tea.KeyF6})
	if m.focus != focusPad {
		t.Fatalf("F6 should open b's pad, focus %v", m.focus)
	}
	update(m, keyUp) // moves in the pad: not a visit
	c.running = true
	update(m, keyHistFwd)
	if selectedName(m) != "c" || m.focus != focusTerminal {
		t.Fatalf("forward from the pad: %s, focus %v", selectedName(m), m.focus)
	}
}

// Inside a session, alt+arrows go to the agent (they move by word in
// Claude's input); only F7 and F8 move through the history.
func TestHistoryAltArrowsReachTheAgent(t *testing.T) {
	m := padModel(t, "a", "b")
	update(m, keyDown) // b
	b := m.sessions[1]
	emu := vt.NewSafeEmulator(80, 24)
	b.emu, b.running = emu, true
	in := readEmu(emu)
	m.focus = focusTerminal
	update(m, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt})
	update(m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt})
	in.waitFor(t, "\x1b\x1b[D\x1b\x1b[C") // alt+← and alt+→, as legacy input sends them
	if selectedName(m) != "b" || m.focus != focusTerminal {
		t.Fatalf("alt+arrows moved: %s, focus %v", selectedName(m), m.focus)
	}
}

func TestHistoryButton(t *testing.T) {
	for b, want := range map[tea.MouseButton]int{
		tea.MouseBackward: -1, tea.MouseForward: 1,
		tea.MouseLeft: 0, tea.MouseRight: 0, tea.MouseMiddle: 0, tea.MouseWheelUp: 0, tea.MouseNone: 0,
	} {
		if got := historyButton(b); got != want {
			t.Errorf("%v: %d, want %d", b, got, want)
		}
	}
}

// The side buttons move through the history anywhere in the window, and
// never reach the agent, even one that turned on mouse reporting.
func TestHistoryMouseButtons(t *testing.T) {
	m := padModel(t, "a", "b")
	update(m, nopMsg{}) // a
	update(m, keyDown)  // b
	b := m.sessions[1]
	emu := vt.NewSafeEmulator(80, 24)
	_, _ = emu.Write([]byte("\x1b[?1000h\x1b[?1006h")) // the app wants SGR mouse reports
	b.emu, b.running = emu, true
	b.mouseModes = map[ansi.Mode]bool{ansi.ModeMouseNormal: true}
	in := readEmu(emu)
	m.focus = focusTerminal
	x, y := 100, 5
	tx, ty := x-m.mainLeft(), y-1

	// Forward at the end does nothing, and isn't forwarded; nor is its
	// release.
	update(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseForward})
	update(m, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseForward})
	if selectedName(m) != "b" {
		t.Fatalf("forward at the end: %s", selectedName(m))
	}
	// A left click still reaches the app: it's the only thing it gets.
	update(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	left := fmt.Sprintf("\x1b[<0;%d;%dM", tx+1, ty+1)
	in.waitFor(t, left)

	// Back over the terminal goes to a; a isn't running, so focus goes
	// to the list.
	update(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseBackward})
	update(m, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseBackward})
	if selectedName(m) != "a" || m.focus != focusSidebar {
		t.Fatalf("back button: %s, focus %v", selectedName(m), m.focus)
	}
	// Forward in the list (over the sidebar) comes back to b.
	update(m, tea.MouseClickMsg{X: 3, Y: 5, Button: tea.MouseForward})
	if selectedName(m) != "b" {
		t.Fatalf("forward button in the list: %s", selectedName(m))
	}
	in.waitFor(t, left)

	// With the help open, the buttons do nothing.
	m.openHelp()
	update(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseBackward})
	if selectedName(m) != "b" {
		t.Fatalf("back button under the help: %s", selectedName(m))
	}
}

// The history keys are compared with listOpts, not literals, so
// handledKeys can't see them; check their help entries directly, and that
// configured keys show in the help.
func TestHelpCoversHistoryKeys(t *testing.T) {
	for _, title := range []string{helpList, helpTerminal} {
		var back, fwd bool
		for _, e := range helpGroupByTitle(title).entries {
			back = back || slices.Contains(e.match, defaultHistoryBackKey)
			fwd = fwd || slices.Contains(e.match, defaultHistoryForwardKey)
		}
		if !back || !fwd {
			t.Errorf("%s: F7 or F8 isn't in the help table", title)
		}
	}
	withListOpts(t, listSettings{toggleKey: defaultToggleKey, histBack: "f9", histFwd: "f12", statusLine: true})
	if got := withListKeys("F7 F8 alt+←→"); got != "F9 F12 alt+←→" {
		t.Errorf("configured history keys in the help: %q", got)
	}
}

func TestParseListConfigHistoryKeys(t *testing.T) {
	got, err := parseListConfig("")
	if err != nil || got.historyBack() != "f7" || got.historyForward() != "f8" {
		t.Fatalf("defaults: %+v, %v", got, err)
	}
	got, err = parseListConfig("[list]\nhistory_back_key = \"F9\"\nhistory_forward_key = 'f12'\n")
	if err != nil || got.historyBack() != "f9" || got.historyForward() != "f12" {
		t.Fatalf("f9/f12: %+v, %v", got, err)
	}
	// Swapped is fine.
	got, err = parseListConfig("[list]\nhistory_back_key = \"f8\"\nhistory_forward_key = \"f7\"\n")
	if err != nil || got.historyBack() != "f8" || got.historyForward() != "f7" {
		t.Fatalf("swapped: %+v, %v", got, err)
	}
	for _, bad := range []string{"x", "ctrl+q", "enter", "left", "f1"} {
		got, err := parseListConfig("[list]\nhistory_back_key = \"" + bad + "\"\n")
		if err == nil || !strings.Contains(err.Error(), "history_back_key") || got != defaultListSettings() {
			t.Errorf("history_back_key %q: want an error and the defaults, got %+v, %v", bad, got, err)
		}
		got, err = parseListConfig("[list]\nhistory_forward_key = \"" + bad + "\"\n")
		if err == nil || !strings.Contains(err.Error(), "history_forward_key") || got != defaultListSettings() {
			t.Errorf("history_forward_key %q: want an error and the defaults, got %+v, %v", bad, got, err)
		}
	}
	// No two [list] keys can be the same, counting the defaults.
	for _, c := range []struct{ text, want string }{
		{"history_back_key = \"f3\"", "toggle_key and history_back_key are both F3"},
		{"history_forward_key = \"f5\"", "back_key and history_forward_key are both F5"},
		{"history_back_key = \"f8\"", "history_back_key and history_forward_key are both F8"},
		{"toggle_key = \"f7\"", "toggle_key and history_back_key are both F7"},
		{"back_key = \"f8\"", "back_key and history_forward_key are both F8"},
	} {
		got, err := parseListConfig("[list]\n" + c.text + "\n")
		if err == nil || !strings.Contains(err.Error(), c.want) || got != defaultListSettings() {
			t.Errorf("%s: want %q and the defaults, got %+v, %v", c.text, c.want, got, err)
		}
	}
	// The pad's key can't be a history key, configured or not.
	list, _ := parseListConfig("[list]\nhistory_back_key = \"f10\"\n")
	if got, err := parsePadConfig("[pad]\nkey = 'f10'\n", list); err == nil || got.key != "" || !strings.Contains(err.Error(), "list.history_back_key") {
		t.Errorf("pad key = history_back_key: %+v, %v", got, err)
	}
	if got, err := parsePadConfig("[pad]\nkey = 'f7'\n", list); err != nil || got.key != "f7" {
		t.Errorf("pad key F7 once history_back_key moved: %+v, %v", got, err)
	}
}
