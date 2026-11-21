package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// groupedModel is a 120x40 model whose sessions are "name" or
// "name@group", put in groups in the order the groups first appear (or
// the order in order, when given).
func groupedModel(t *testing.T, specs ...string) *model {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	m := &model{width: 120, height: 40}
	var f groupsFile
	seen := map[string]bool{}
	for _, sp := range specs {
		name, g, _ := strings.Cut(sp, "@")
		s := &Session{ID: name, Name: name, Agent: "pwsh", Dir: `C:\x\` + name, Group: g}
		m.sessions = append(m.sessions, s)
		if g != "" && !seen[g] {
			seen[g] = true
			f.Groups = append(f.Groups, group{Name: g})
		}
	}
	m.setGroups(f)
	return m
}

// rowsText is the rows as text: "[app] A C [cli] D [] B", with a header's
// group in brackets and ▸ for a collapsed one.
func rowsText(m *model) string {
	var out []string
	for _, r := range m.rows() {
		switch {
		case r.kind == rowSession:
			out = append(out, m.sessions[r.idx].Name)
		case r.temp:
			out = append(out, "[(drop)]")
		case m.collapsed(r.group):
			out = append(out, "▸["+r.group+"]")
		default:
			out = append(out, "["+r.group+"]")
		}
	}
	return strings.Join(out, " ")
}

// groupsOf is each session's group, in order: "A:app C:app D:cli B:".
func groupsOf(m *model) string {
	var out []string
	for _, s := range m.sessions {
		out = append(out, s.Name+":"+s.group())
	}
	return strings.Join(out, " ")
}

func selectHeader(m *model, name string) { m.onHeader, m.selHeader = true, name }

// rowLine is the screen line of line `line` of row r (counting a header's
// blank line as its line 0), with the list scrolled to m.scroll.
func rowLine(m *model, r, line int) int {
	rows := m.rows()
	y := listTop
	for i := m.scroll; i < r; i++ {
		y += rowHeight(rows, i)
	}
	return y + line
}

// rowOf is the index in rows() of a session (by name) or a header ("[g]").
func rowOf(t *testing.T, m *model, what string) int {
	t.Helper()
	for r, row := range m.rows() {
		if row.kind == rowSession && m.sessions[row.idx].Name == what ||
			row.kind == rowHeader && !row.temp && "["+row.group+"]" == what {
			return r
		}
	}
	t.Fatalf("no row %q in %s", what, rowsText(m))
	return -1
}

// headerLine is the screen line of a header's text (below its blank line).
func headerLine(t *testing.T, m *model, g string) int {
	t.Helper()
	r := rowOf(t, m, "["+g+"]")
	return rowLine(m, r, rowHeight(m.rows(), r)-1)
}

func TestNoGroupsListUnchanged(t *testing.T) {
	m := groupedModel(t, "A", "B", "C")
	if m.grouped() {
		t.Fatal("no groups expected")
	}
	if got := rowsText(m); got != "A B C" {
		t.Errorf("rows %q", got)
	}
	lines := m.renderSidebar(30, 39)
	for _, l := range lines {
		if s := ansi.Strip(l); strings.ContainsAny(s, "▾▸") || strings.Contains(s, noGroupName) {
			t.Errorf("a header with no groups: %q", s)
		}
	}
	// Sessions start right under the border, three lines each, as before.
	for p, name := range []string{"A", "B", "C"} {
		if got := ansi.Strip(lines[listTop+p*rowsPerSession]); !strings.HasPrefix(strings.TrimLeft(got, "▌"), string(rune('1'+p))+" ") || !strings.Contains(got, name) {
			t.Errorf("line %d: %q, want session %d %s", listTop+p*rowsPerSession, got, p+1, name)
		}
	}
	if _, err := os.Stat(groupsPath()); err == nil {
		t.Error("groups.json shouldn't be written with no groups")
	}
}

func TestGroupRowsOrderAndNumbers(t *testing.T) {
	m := groupedModel(t, "A@app", "B", "C@app", "D@cli")
	if got := order(m); got != "A C D B" {
		t.Errorf("display order %q, want A C D B", got)
	}
	if got := rowsText(m); got != "[app] A C [cli] D [] B" {
		t.Errorf("rows %q", got)
	}
	selectHeader(m, "cli")
	press(m, tea.KeyPressMsg{Code: tea.KeySpace})
	if got := rowsText(m); got != "[app] A C ▸[cli] [] B" {
		t.Errorf("after space on cli: %q", got)
	}
	// Numbers skip headers and the collapsed D: 3 is B.
	if !m.selectVisible(2) || m.current() == nil || m.current().Name != "B" {
		t.Errorf("session 3 should be B, got %v", m.current())
	}
	lines := m.renderSidebar(30, 39)
	text := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"▾ app  (2)", "▸ cli  (1)", "▾ (no group)  (1)"} {
		if !strings.Contains(text, want) {
			t.Errorf("sidebar lacks %q:\n%s", want, text)
		}
	}
	// No "(no group)" with nothing in it.
	m = groupedModel(t, "A@app")
	if got := rowsText(m); got != "[app] A" {
		t.Errorf("all grouped: %q", got)
	}
}

func TestGroupHeaderKeys(t *testing.T) {
	m := groupedModel(t, "A@app", "B", "C@app")
	selectHeader(m, "app")
	for _, c := range []struct {
		key       tea.KeyPressMsg
		collapsed bool
	}{
		{tea.KeyPressMsg{Code: tea.KeyEnter}, true},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, false},
		{tea.KeyPressMsg{Code: tea.KeyLeft}, true},
		{tea.KeyPressMsg{Code: tea.KeyLeft}, true},
		{tea.KeyPressMsg{Code: tea.KeyRight}, false},
		{tea.KeyPressMsg{Code: tea.KeyRight}, false},
		{tea.KeyPressMsg{Code: tea.KeySpace}, true},
	} {
		press(m, c.key)
		if m.collapsed("app") != c.collapsed {
			t.Errorf("%s: collapsed %v, want %v", c.key, m.collapsed("app"), c.collapsed)
		}
	}
	if m.focus != focusSidebar || m.current() != nil {
		t.Error("keys on a header shouldn't open anything")
	}
	if loadGroups().Groups[0].Collapsed != true {
		t.Error("the collapsed state should be saved")
	}
	// ← on a session goes to its header.
	press(m, tea.KeyPressMsg{Code: tea.KeySpace})
	selectByName(t, m, "C")
	m.onHeader = false
	press(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if !m.onHeader || m.selHeader != "app" {
		t.Errorf("← on C: header %v %q", m.onHeader, m.selHeader)
	}
	// ↑↓ walk through headers and sessions.
	m.selectRowAt(0)
	var walk []string
	for i := 0; i < 5; i++ {
		if m.onHeader {
			walk = append(walk, "["+m.selHeader+"]")
		} else {
			walk = append(walk, m.current().Name)
		}
		press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if got := strings.Join(walk, " "); got != "[app] A C [] B" {
		t.Errorf("↓ walk: %q", got)
	}
}

func TestCollapseMovesSelectionToHeader(t *testing.T) {
	m := groupedModel(t, "A@app", "C@app", "B")
	selectByName(t, m, "C")
	m.setCollapsed("app", true)
	if m.current() != nil {
		t.Error("a session in a collapsed group isn't current")
	}
	m.keepSelectionVisible()
	if !m.onHeader || m.selHeader != "app" {
		t.Errorf("selection should move to the header: %v %q", m.onHeader, m.selHeader)
	}
}

func TestGroupRollup(t *testing.T) {
	m := groupedModel(t, "A@app", "B@app", "C@app", "D")
	m.sessions[0].status = StatusWaiting
	m.sessions[1].status = StatusWorking
	m.sessions[2].status, m.sessions[2].unread = StatusIdle, true
	m.setCollapsed("app", true)
	got := ansi.Strip(m.renderHeader(listRow{kind: rowHeader, group: "app"}, 40, false, false))
	for _, want := range []string{"▸ app  (3)", "● 1", "✓ 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("header %q lacks %q", got, want)
		}
	}
	if !strings.ContainsAny(got, strings.Join(spinner, "")) {
		t.Errorf("header %q should show a spinner for the working session", got)
	}
	if w := ansi.StringWidth(m.renderHeader(listRow{kind: rowHeader, group: "app"}, 40, true, false)); w != 40 {
		t.Errorf("selected header is %d cells, want 40", w)
	}
	// tab finds A in the collapsed group and opens it.
	selectByName(t, m, "D")
	m.onHeader = false
	press(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.current() == nil || m.current().Name != "A" || m.collapsed("app") {
		t.Errorf("tab: current %v, collapsed %v", m.current(), m.collapsed("app"))
	}
}

func TestGroupFilterAcrossCollapsed(t *testing.T) {
	m := groupedModel(t, "api@app", "web@app", "docs@cli", "notes")
	m.setCollapsed("app", true)
	m.filter = "api"
	if got := rowsText(m); got != "▸[app] api" {
		t.Errorf("filter api: %q (matches in a collapsed group show; empty headers hide)", got)
	}
	m.keepSelectionVisible()
	if m.current() == nil || m.current().Name != "api" {
		t.Errorf("the match should be selectable: %v", m.current())
	}
	lines := ansi.Strip(strings.Join(m.renderSidebar(30, 39), "\n"))
	if !strings.Contains(lines, "▾ app") {
		t.Errorf("a group showing matches is drawn open:\n%s", lines)
	}
	m.filter = ""
	if got := rowsText(m); got != "▸[app] [cli] docs [] notes" {
		t.Errorf("filter cleared: %q", got)
	}
}

func TestMoveSessionAcrossGroups(t *testing.T) {
	m := groupedModel(t, "A@app", "C@app", "D@cli", "B")
	selectByName(t, m, "C")
	m.onHeader = false
	steps := []struct {
		delta int
		moved bool
		order string
	}{
		{1, true, "A:app C:cli D:cli B:"},  // over the edge: the top of cli
		{1, true, "A:app D:cli C:cli B:"},  // within cli
		{1, true, "A:app D:cli C: B:"},     // the top of (no group)
		{1, true, "A:app D:cli B: C:"},     // within it
		{1, false, "A:app D:cli B: C:"},    // the bottom
		{-1, true, "A:app D:cli C: B:"},    // up within (no group)
		{-1, true, "A:app D:cli C:cli B:"}, // the end of cli
	}
	for i, st := range steps {
		if moved := m.moveSession(st.delta); moved != st.moved || groupsOf(m) != st.order {
			t.Errorf("step %d: moved %v %q, want %v %q", i, moved, groupsOf(m), st.moved, st.order)
		}
		if m.current() == nil || m.current().Name != "C" {
			t.Fatalf("step %d: the selection should stay on C", i)
		}
	}
	// Up into a collapsed group opens it.
	m.setCollapsed("app", true)
	m.moveSession(-1) // to the top of cli
	m.moveSession(-1) // over the edge into app
	if got := groupsOf(m); got != "A:app C:app D:cli B:" || m.collapsed("app") {
		t.Errorf("into collapsed app: %q, collapsed %v", got, m.collapsed("app"))
	}
	// shift+home/end stay in the group.
	if !m.moveSessionToEnd(true) || groupsOf(m) != "C:app A:app D:cli B:" {
		t.Errorf("shift+home: %q", groupsOf(m))
	}
	if m.moveSessionToEnd(true) {
		t.Error("shift+home at the top of its group")
	}
	// An empty group shows (0) and a session can move into it.
	m = groupedModel(t, "A@app", "B")
	m.addGroup("empty", false)
	if got := rowsText(m); got != "[app] A [empty] [] B" {
		t.Fatalf("rows %q", got)
	}
	selectByName(t, m, "B")
	m.onHeader = false
	if !m.moveSession(-1) || groupsOf(m) != "A:app B:empty" {
		t.Errorf("into the empty group: %q", groupsOf(m))
	}
}

func TestMoveGroupWithKeys(t *testing.T) {
	m := groupedModel(t, "A@app", "D@cli", "E@docs", "B")
	selectHeader(m, "docs")
	press(m, shiftKey(tea.KeyUp))
	if got := rowsText(m); got != "[app] A [docs] E [cli] D [] B" {
		t.Errorf("shift+up on docs: %q", got)
	}
	if got := order(m); got != "A E D B" {
		t.Errorf("sessions follow their group: %q", got)
	}
	press(m, shiftKey(tea.KeyHome))
	if got := rowsText(m); got != "[docs] E [app] A [cli] D [] B" {
		t.Errorf("shift+home on docs: %q", got)
	}
	if m.moveSession(-1) {
		t.Error("the first group can't go up")
	}
	press(m, shiftKey(tea.KeyEnd))
	if got := rowsText(m); got != "[app] A [cli] D [docs] E [] B" {
		t.Errorf("shift+end on docs: %q", got)
	}
	if m.moveSession(1) {
		t.Error("(no group) stays last")
	}
	selectHeader(m, "")
	if m.moveSession(-1) {
		t.Error("(no group) can't move")
	}
	saved := loadGroups()
	if len(saved.Groups) != 3 || saved.Groups[2].Name != "docs" {
		t.Errorf("saved groups %+v", saved.Groups)
	}
}

func TestDeleteGroupKeepsSessions(t *testing.T) {
	m := groupedModel(t, "A@app", "C@app", "D@cli", "B")
	selectHeader(m, "app")
	press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.confirm == nil || !strings.Contains(m.confirm.text, `Delete the group "app"? Its 2 session(s) move to (no group)`) {
		t.Fatalf("x on a header should ask: %+v", m.confirm)
	}
	press(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if got := groupsOf(m); got != "D:cli A: C: B:" {
		t.Errorf("after deleting app: %q", got)
	}
	if len(m.sessions) != 4 || m.groupByName("app") != nil {
		t.Error("deleting a group must keep its sessions")
	}
	if got := rowsText(m); got != "[cli] D [] A C B" {
		t.Errorf("rows %q", got)
	}
	// Deleting the last group: back to a plain list.
	selectHeader(m, "cli")
	press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	press(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.grouped() || rowsText(m) != "D A C B" {
		t.Errorf("no groups left: %q", rowsText(m))
	}
	if _, err := os.Stat(groupsPath()); err == nil {
		t.Error("groups.json should go with the last group")
	}
	if loaded := loadSessions(); len(loaded) != 4 || loaded[0].Group != "" {
		t.Errorf("saved sessions: %d", len(loaded))
	}
	// x on (no group) does nothing; n answers no.
	m = groupedModel(t, "A@app", "B")
	selectHeader(m, "")
	press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.confirm != nil {
		t.Error("(no group) can't be deleted")
	}
}

func TestRenameGroup(t *testing.T) {
	m := groupedModel(t, "A@app", "C@app", "D@cli")
	selectHeader(m, "app")
	press(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.rename == nil || m.renameGroup == nil {
		t.Fatal("r on a header should rename the group")
	}
	m.rename.SetValue("web")
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := groupsOf(m); got != "A:web C:web D:cli" || m.selHeader != "web" {
		t.Errorf("renamed: %q, selected %q", got, m.selHeader)
	}
	// A taken name is refused.
	press(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m.rename.SetValue("cli")
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := groupsOf(m); got != "A:web C:web D:cli" || !strings.Contains(m.flash, "already") {
		t.Errorf("rename to a taken name: %q flash %q", got, m.flash)
	}
}

func TestAssignGroup(t *testing.T) {
	m := groupedModel(t, "A", "B", "C")
	selectByName(t, m, "B")
	press(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.assign == nil {
		t.Fatal("a should open the group box")
	}
	typeText(m, "dotU")
	ch := m.assignChoices()
	if len(ch) != 2 || !ch[0].create || ch[0].name != "dotU" || !ch[1].none {
		t.Fatalf("choices %+v", ch)
	}
	box := ansi.Strip(strings.Join(m.renderAssign(80), "\n"))
	for _, want := range []string{`Move "B" to group`, `+ new group "dotU"`, noGroupName} {
		if !strings.Contains(box, want) {
			t.Errorf("box lacks %q:\n%s", want, box)
		}
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := groupsOf(m); got != "B:dotU A: C:" || m.current() == nil || m.current().Name != "B" {
		t.Errorf("after a new group: %q, current %v", got, m.current())
	}
	if got := rowsText(m); got != "[dotU] B [] A C" {
		t.Errorf("rows %q", got)
	}
	// Into the existing group, by typing part of its name; it's collapsed,
	// so the selection goes to its header.
	m.setCollapsed("dotU", true)
	selectByName(t, m, "C")
	m.onHeader = false
	press(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	typeText(m, "dtu")
	if ch := m.assignChoices(); ch[0].name != "dotU" || ch[0].create {
		t.Fatalf("choices %+v", ch)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := groupsOf(m); got != "B:dotU C:dotU A:" || !m.onHeader || m.selHeader != "dotU" || !m.collapsed("dotU") {
		t.Errorf("into collapsed dotU: %q header %v %q", got, m.onHeader, m.selHeader)
	}
	// (no group) takes it out; ↓ picks it.
	m.setCollapsed("dotU", false)
	selectByName(t, m, "B")
	m.onHeader = false
	press(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := groupsOf(m); got != "C:dotU A: B:" {
		t.Errorf("to (no group): %q", got)
	}
	// esc closes it without a change.
	press(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.assign != nil || groupsOf(m) != "C:dotU A: B:" {
		t.Errorf("esc: %q", groupsOf(m))
	}
	// Saved: the session's group in sessions.json, the group in groups.json.
	loaded := loadSessions()
	if len(loaded) != 3 || loaded[0].Name != "C" || loaded[0].Group != "dotU" {
		t.Errorf("saved sessions %+v", loaded)
	}
}

// sessions.json from before groups loads as it did, with no groups.
func TestOldSessionsFileLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DECKHAND_HOME", home)
	t.Setenv("DECKHAND_CONFIG", filepath.Join(home, "no-config.toml"))
	old := `[{"id":"one","name":"one","agent":"pwsh","dir":"C:\\x","created":"2026-01-01T00:00:00Z"},
{"id":"two","name":"two","agent":"pwsh","dir":"C:\\y","created":"2026-01-01T00:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if m.grouped() || order(m) != "one two" || rowsText(m) != "one two" {
		t.Errorf("old file: grouped %v, order %q", m.grouped(), order(m))
	}
	if err := saveSessions(m.sessions); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "sessions.json"))
	if strings.Contains(string(data), `"group"`) {
		t.Errorf("no group field should be written for sessions in no group:\n%s", data)
	}
}

// Groups and collapsed state survive a restart, and a session naming a
// group groups.json lacks gets one.
func TestGroupsSaveAndLoad(t *testing.T) {
	m := groupedModel(t, "A@app", "B", "D@cli")
	t.Setenv("DECKHAND_CONFIG", filepath.Join(t.TempDir(), "no-config.toml"))
	m.setCollapsed("cli", true)
	m.noGroupCollapsed = true
	m.saveAll()
	m2 := newModel()
	if got := rowsText(m2); got != "[app] A ▸[cli] ▸[]" {
		t.Errorf("after a restart: %q", got)
	}
	// The first session is in a collapsed group: its header is selected.
	m.setCollapsed("app", true)
	m.saveAll()
	m2 = newModel()
	if !m2.onHeader || m2.selHeader != "app" {
		t.Errorf("start with app collapsed: header %v %q", m2.onHeader, m2.selHeader)
	}
	var list []map[string]any
	data, _ := os.ReadFile(storePath())
	_ = json.Unmarshal(data, &list)
	list[1]["group"] = "orphan"
	data, _ = json.Marshal(list)
	_ = os.WriteFile(storePath(), data, 0o600)
	m3 := newModel()
	if m3.groupByName("orphan") == nil {
		t.Errorf("a group named by a session should be added: %s", rowsText(m3))
	}
}

func TestNewSessionsJoinTheGroup(t *testing.T) {
	m := groupedModel(t, "A@app", "B")
	selectByName(t, m, "A")
	m.onHeader = false
	if got := m.defaultGroup(); got != "app" {
		t.Errorf("default group on A: %q", got)
	}
	selectHeader(m, "")
	if got := m.defaultGroup(); got != "" {
		t.Errorf("default group on (no group): %q", got)
	}
	d := &newDialog{field: fieldFolder}
	d.setGroups(m.groups, "app")
	if d.group() != "app" || !slices.Contains(d.fields(), fieldGroup) {
		t.Errorf("dialog group %q, fields %v", d.group(), d.fields())
	}
	d.field = fieldGroup
	d.update(tea.KeyPressMsg{Code: tea.KeyRight})
	if d.group() != "" {
		t.Errorf("→ on the group row: %q, want (no group)", d.group())
	}
	d.update(tea.KeyPressMsg{Code: tea.KeyRight})
	if d.group() != "app" {
		t.Errorf("→ wraps: %q", d.group())
	}
	if (&newDialog{}).group() != "" {
		t.Error("no groups: no group")
	}
}

func TestShiftEnterJoinsTheGroup(t *testing.T) {
	m := groupedModel(t, "A@app", "C@app", "B")
	old := startSession
	startSession = func(*Session, int, int, bool, *atomic.Bool) error { return nil }
	t.Cleanup(func() { startSession = old })
	selectByName(t, m, "A")
	m.onHeader = false
	m.sessions[0].Agent = "pwsh"
	m.startLikeSelected()
	if m.current() == nil || m.current().group() != "app" || m.indexOf(m.current()) != 1 {
		t.Errorf("the new session should be right below A, in app: %q (flash %q)", groupsOf(m), m.flash)
	}
}

// The mockup: two expanded groups, a collapsed one and some ungrouped
// sessions, at 24 and 40 columns.
func TestGroupsView(t *testing.T) {
	m := groupedModel(t, "fork-session@deckhand", "readme-overview@deckhand", "notes@deckhand", "a@dotUltimate", "b@dotUltimate", "c@dotUltimate", "d@dotUltimate", "scratch", "tmp")
	m.sessions[0].status = StatusWaiting
	m.sessions[4].status, m.sessions[4].unread = StatusIdle, true
	m.sessions[5].status, m.sessions[5].unread = StatusIdle, true
	m.setCollapsed("dotUltimate", true)
	selectHeader(m, "deckhand")
	for _, w := range []int{24, 40} {
		lines := m.renderSidebar(w, 39)
		var text []string
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != w {
				t.Errorf("%d columns: line %d is %d cells", w, i, got)
			}
			text = append(text, ansi.Strip(l))
		}
		want := []struct {
			line int
			text string
		}{
			{2, "▌▾ deckhand  (3)"},
			{3, "1 "},
			{3, "fork-session"},
			{9, "3 "},
			{12, ""},
			{13, " ▸ dotUltimate  (4)"},
			{14, ""},
			{15, " ▾ (no group)  (2)"},
			{16, "4 "},
			{16, "scratch"},
			{19, "5 "},
		}
		for _, c := range want {
			if !strings.Contains(text[c.line], c.text) || c.text == "" && strings.TrimSpace(text[c.line]) != "" {
				t.Errorf("%d columns: line %d = %q, want %q", w, c.line, text[c.line], c.text)
			}
		}
		if w == 40 {
			if !strings.Contains(text[2], "1") || !strings.ContainsAny(text[2], "●◉") {
				t.Errorf("deckhand's roll-up: %q", text[2])
			}
			if !strings.HasSuffix(strings.TrimRight(text[13], " "), "✓ 2") {
				t.Errorf("dotUltimate's roll-up: %q", text[13])
			}
		}
	}
	// The main pane on a header says what's in it.
	pane := ansi.Strip(strings.Join(m.renderGroupPane(80, 20), "\n"))
	for _, want := range []string{"deckhand", "3 session(s)", "fork-session", "r rename"} {
		if !strings.Contains(pane, want) {
			t.Errorf("group pane lacks %q:\n%s", want, pane)
		}
	}
}
