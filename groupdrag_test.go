package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The rows of groupedModel("A@app", "C@app", "D@cli", "B") at 40 lines:
//
//	2  ▾ app (2)
//	3  A          (3-5)
//	6  C          (6-8)
//	9  (blank)
//	10 ▾ cli (1)
//	11 D          (11-13)
//	14 (blank)
//	15 ▾ (no group) (1)
//	16 B          (16-18)
func fourInGroups(t *testing.T) *model {
	t.Helper()
	m := groupedModel(t, "A@app", "C@app", "D@cli", "B")
	if got := rowsText(m); got != "[app] A C [cli] D [] B" {
		t.Fatalf("rows %q", got)
	}
	return m
}

func TestDragIntoGroupAtPosition(t *testing.T) {
	m := fourInGroups(t)
	if y := rowLine(m, rowOf(t, m, "D"), 0); y != 11 {
		t.Fatalf("D starts on line %d, want 11", y)
	}
	mouseDown(m, 5, 16) // B
	mouseMove(m, 5, 11) // D's top line: above D, in cli
	lines := m.renderSidebar(m.sidebarWidth(), m.height-1)
	if got := ansi.Strip(lines[11]); !strings.HasPrefix(got, "  ━") {
		t.Errorf("the drop line should be indented under cli on line 11: %q", got)
	}
	mouseUp(m, 5, 11)
	if got := groupsOf(m); got != "A:app C:app B:cli D:cli" {
		t.Errorf("B above D: %q", got)
	}
	if m.current() == nil || m.current().Name != "B" {
		t.Errorf("B should be selected: %v", m.current())
	}
	if loaded := loadSessions(); loaded[2].Name != "B" || loaded[2].Group != "cli" {
		t.Errorf("not saved: %+v", loaded[2])
	}

	// Right under a header: the top line of the group's first session.
	dragRows(m, rowLine(m, rowOf(t, m, "D"), 0), rowLine(m, rowOf(t, m, "A"), 1))
	if got := groupsOf(m); got != "D:app A:app C:app B:cli" {
		t.Errorf("D to the top of app: %q", got)
	}
	// The bottom line of a group's last session: the end of that group.
	dragRows(m, rowLine(m, rowOf(t, m, "B"), 0), rowLine(m, rowOf(t, m, "C"), 2))
	if got := groupsOf(m); got != "D:app A:app C:app B:app" {
		t.Errorf("B after C: %q", got)
	}
}

func TestDropOnHeader(t *testing.T) {
	m := fourInGroups(t)
	mouseDown(m, 5, 3)  // A
	mouseMove(m, 5, 10) // cli's header
	if m.drag.target.kind != dropHeader || m.drag.target.group != "cli" {
		t.Fatalf("target %+v, want cli's header", m.drag.target)
	}
	lines := m.renderSidebar(m.sidebarWidth(), m.height-1)
	if got := ansi.Strip(lines[10]); !strings.HasPrefix(got, "▌▾ cli") {
		t.Errorf("the target header should be highlighted: %q", got)
	}
	if strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "━") {
		t.Error("no drop line on a header: the header is the target")
	}
	mouseUp(m, 5, 10)
	if got := groupsOf(m); got != "C:app D:cli A:cli B:" {
		t.Errorf("A to the end of cli: %q", got)
	}

	// A collapsed group: the drop adds to the end and it stays collapsed.
	m = fourInGroups(t)
	m.setCollapsed("cli", true)
	y := headerLine(t, m, "cli")
	mouseDown(m, 5, rowLine(m, rowOf(t, m, "B"), 0))
	mouseMove(m, 5, y)
	mouseUp(m, 5, y)
	if got := groupsOf(m); got != "A:app C:app D:cli B:cli" {
		t.Errorf("B onto collapsed cli: %q", got)
	}
	if !m.collapsed("cli") || !m.onHeader || m.selHeader != "cli" {
		t.Errorf("cli should stay collapsed with its header selected: %v %v %q", m.collapsed("cli"), m.onHeader, m.selHeader)
	}
	if got := ansi.Strip(m.renderHeader(listRow{kind: rowHeader, group: "cli"}, 30, false, false)); !strings.Contains(got, "▸ cli  (2)") {
		t.Errorf("header %q, want the count up to 2", got)
	}
}

func TestHoverOpensCollapsedGroup(t *testing.T) {
	m := fourInGroups(t)
	m.setCollapsed("cli", true)
	y := headerLine(t, m, "cli")
	mouseDown(m, 5, rowLine(m, rowOf(t, m, "B"), 0))
	mouseMove(m, 5, y)
	for i := 0; i < hoverOpenFrames-1; i++ {
		m.dragTick()
	}
	if !m.collapsed("cli") {
		t.Fatal("opened too soon")
	}
	m.dragTick()
	if m.collapsed("cli") {
		t.Fatalf("hovering %d frames should open cli", hoverOpenFrames)
	}
	// Onto D's bottom line, inside the opened group.
	dy := rowLine(m, rowOf(t, m, "D"), 2)
	mouseMove(m, 5, dy)
	if m.collapsed("cli") || m.drag.target.kind != dropAt || m.drag.target.group != "cli" {
		t.Fatalf("inside the opened group: collapsed %v target %+v", m.collapsed("cli"), m.drag.target)
	}
	// Moving on without dropping closes it again.
	mouseMove(m, 5, 3)
	if !m.collapsed("cli") {
		t.Error("leaving the group should close it again")
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.drag != nil || groupsOf(m) != "A:app C:app D:cli B:" || !m.collapsed("cli") {
		t.Errorf("esc: %q collapsed %v", groupsOf(m), m.collapsed("cli"))
	}

	// Dropping inside it keeps it open.
	mouseDown(m, 5, rowLine(m, rowOf(t, m, "B"), 0))
	mouseMove(m, 5, y)
	for i := 0; i < hoverOpenFrames; i++ {
		m.dragTick()
	}
	dy = rowLine(m, rowOf(t, m, "D"), 2)
	mouseMove(m, 5, dy)
	mouseUp(m, 5, dy)
	if got := groupsOf(m); got != "A:app C:app D:cli B:cli" || m.collapsed("cli") {
		t.Errorf("dropped after D: %q, collapsed %v", got, m.collapsed("cli"))
	}
	if loadGroups().Groups[1].Collapsed {
		t.Error("the opened group should be saved open")
	}

	// esc while it's open closes it again.
	m = fourInGroups(t)
	m.setCollapsed("cli", true)
	mouseDown(m, 5, rowLine(m, rowOf(t, m, "B"), 0))
	mouseMove(m, 5, y)
	for i := 0; i < hoverOpenFrames; i++ {
		m.dragTick()
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.collapsed("cli") {
		t.Error("esc should close a group hovering opened")
	}
}

func TestDragOutOfGroups(t *testing.T) {
	m := fourInGroups(t)
	y := headerLine(t, m, "")
	dragRows(m, 3, y) // A onto (no group)
	if got := groupsOf(m); got != "C:app D:cli B: A:" {
		t.Errorf("A to (no group): %q", got)
	}
	// Above B, in (no group).
	dragRows(m, rowLine(m, rowOf(t, m, "C"), 0), rowLine(m, rowOf(t, m, "B"), 0))
	if got := groupsOf(m); got != "D:cli C: B: A:" {
		t.Errorf("C above B: %q", got)
	}
	// app is empty now and stays, as (0).
	if got := rowsText(m); got != "[app] [cli] D [] C B A" {
		t.Errorf("rows %q", got)
	}
	if got := ansi.Strip(m.renderHeader(listRow{kind: rowHeader, group: "app"}, 30, false, false)); !strings.Contains(got, "▾ app  (0)") {
		t.Errorf("empty group header %q", got)
	}
	// And it takes a session back on its header.
	dragRows(m, rowLine(m, rowOf(t, m, "A"), 0), headerLine(t, m, "app"))
	if got := groupsOf(m); got != "A:app D:cli C: B:" {
		t.Errorf("A back into app: %q", got)
	}
}

// With every session in a group, a "(no group)" target shows at the bottom
// while dragging, and only then.
func TestDragToTemporaryNoGroup(t *testing.T) {
	m := groupedModel(t, "A@app", "D@cli")
	if got := rowsText(m); got != "[app] A [cli] D" {
		t.Fatalf("rows %q", got)
	}
	mouseDown(m, 5, 3) // A
	if strings.Contains(rowsText(m), "(drop)") {
		t.Error("no target before the drag starts")
	}
	mouseMove(m, 5, 5)
	if got := rowsText(m); got != "[app] A [cli] D [(drop)]" {
		t.Fatalf("while dragging: %q", got)
	}
	y := rowLine(m, len(m.rows())-1, 1)
	mouseMove(m, 5, y)
	text := ansi.Strip(strings.Join(m.renderSidebar(m.sidebarWidth(), m.height-1), "\n"))
	if !strings.Contains(text, "(no group)  drop here") {
		t.Errorf("the temporary target should show:\n%s", text)
	}
	if m.drag.target.kind != dropHeader || m.drag.target.group != "" {
		t.Fatalf("target %+v", m.drag.target)
	}
	mouseUp(m, 5, y)
	if got := groupsOf(m); got != "D:cli A:" {
		t.Errorf("A out of every group: %q", got)
	}
	if got := rowsText(m); got != "[app] [cli] D [] A" {
		t.Errorf("rows %q", got)
	}
	// Below the list does the same.
	m = groupedModel(t, "A@app", "D@cli")
	dragRows(m, 3, 30)
	if got := groupsOf(m); got != "D:cli A:" {
		t.Errorf("A below the list: %q", got)
	}
}

func TestDragGroupHeader(t *testing.T) {
	// 2 [app] 3 A, 6/7 [cli] 8 D, 11/12 [docs] 13 E, 16/17 [] 18 B
	fresh := func() *model {
		m := groupedModel(t, "A@app", "D@cli", "E@docs", "B")
		if y := headerLine(t, m, "docs"); y != 12 {
			t.Fatalf("docs header on %d", y)
		}
		return m
	}
	groupsText := func(m *model) string {
		var out []string
		for _, g := range m.groups {
			out = append(out, g.Name)
		}
		return strings.Join(out, " ")
	}

	m := fresh()
	mouseDown(m, 5, 12)
	mouseMove(m, 5, 2) // onto app's header: above app
	if m.drag.target.kind != dropGroup || m.drag.target.beforeGroup != m.groupByName("app") {
		t.Fatalf("target %+v", m.drag.target)
	}
	lines := m.renderSidebar(m.sidebarWidth(), m.height-1)
	if got := ansi.Strip(lines[2]); !strings.HasPrefix(got, "━") {
		t.Errorf("a full-width drop line above app: %q", got)
	}
	mouseUp(m, 5, 2)
	if got := groupsText(m); got != "docs app cli" || order(m) != "E A D B" {
		t.Errorf("docs to the top: %q, sessions %q", got, order(m))
	}
	if !m.onHeader || m.selHeader != "docs" {
		t.Errorf("docs's header should be selected")
	}
	if saved := loadGroups(); saved.Groups[0].Name != "docs" {
		t.Errorf("not saved: %+v", saved.Groups)
	}

	// Groups don't nest: on a session of cli, app goes after cli.
	m = fresh()
	mouseDown(m, 5, 2)
	mouseMove(m, 5, 9) // D, inside cli
	if m.drag.target.kind != dropGroup || m.drag.target.beforeGroup != m.groupByName("docs") {
		t.Fatalf("target %+v, want above docs", m.drag.target)
	}
	mouseUp(m, 5, 9)
	if got := groupsText(m); got != "cli app docs" {
		t.Errorf("app after cli: %q", got)
	}
	for _, s := range m.sessions {
		if s.Name == "A" && s.group() != "app" {
			t.Error("a group's sessions go with it")
		}
	}

	// On (no group), or below the list: after the last group.
	m = fresh()
	dragRows(m, 2, 17)
	if got := groupsText(m); got != "cli docs app" {
		t.Errorf("app to the end: %q", got)
	}
	// Where it is: nothing changes.
	m = fresh()
	dragRows(m, 7, 8) // cli onto its own session: after cli, where it is
	if got := groupsText(m); got != "app cli docs" {
		t.Errorf("in place: %q", got)
	}
	// esc cancels.
	m = fresh()
	mouseDown(m, 5, 12)
	mouseMove(m, 5, 2)
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	mouseUp(m, 5, 2)
	if got := groupsText(m); got != "app cli docs" {
		t.Errorf("after esc: %q", got)
	}
	// "(no group)" can't be dragged.
	m = fresh()
	mouseDown(m, 5, 17)
	if m.drag != nil {
		t.Error("(no group) isn't draggable")
	}
}
