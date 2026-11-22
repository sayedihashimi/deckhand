package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// dragModel is a model 120 columns wide (a 30-column sidebar) and h lines
// high, with these sessions.
func dragModel(t *testing.T, h int, names ...string) *model {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	m := testModel(names...)
	m.width, m.height = 120, h
	return m
}

// rowY is the screen line of line `line` (0-2) of the session shown at
// row r of the list.
func rowY(r, line int) int { return listTop + r*rowsPerSession + line }

func mouseDown(m *model, x, y int) {
	m.handleMouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func mouseMove(m *model, x, y int) {
	m.handleMouse(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func mouseUp(m *model, x, y int) {
	m.handleMouse(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
}

// dragRows presses on line `from`, moves through each line in `via` and
// releases on the last one, all in the sidebar's column 5.
func dragRows(m *model, from int, via ...int) {
	mouseDown(m, 5, from)
	for _, y := range via {
		mouseMove(m, 5, y)
	}
	mouseUp(m, 5, via[len(via)-1])
}

func savedNames(t *testing.T) string {
	t.Helper()
	var names []string
	for _, s := range loadSessions() {
		names = append(names, s.Name)
	}
	return strings.Join(names, " ")
}

func TestClickSelectsWithoutDragging(t *testing.T) {
	m := dragModel(t, 32, "A", "B", "C")
	m.focus = focusTerminal
	mouseDown(m, 5, rowY(1, 1))
	if m.sel != 1 || m.focus != focusSidebar {
		t.Fatalf("press: sel %d focus %v, want B selected in the list", m.sel, m.focus)
	}
	if m.drag == nil || m.dragging() {
		t.Fatalf("a press should wait for a move before dragging: %+v", m.drag)
	}
	mouseUp(m, 5, rowY(1, 1))
	if m.drag != nil || order(m) != "A B C" || m.sel != 1 {
		t.Errorf("a click shouldn't move anything: %q sel %d drag %+v", order(m), m.sel, m.drag)
	}
	if got := savedNames(t); got != "" {
		t.Errorf("a click shouldn't save: %q", got)
	}
}

func TestDragToTopAndDown(t *testing.T) {
	m := dragModel(t, 32, "A", "B", "C", "D")
	// The issue's example: session 3 to the top, through B, onto A's top line.
	dragRows(m, rowY(2, 0), rowY(1, 1), rowY(0, 0))
	if got := order(m); got != "C A B D" {
		t.Fatalf("C to the top: %q", got)
	}
	if m.sessions[m.sel].Name != "C" || m.drag != nil {
		t.Errorf("C should be selected and the drag over: sel %q drag %+v", m.sessions[m.sel].Name, m.drag)
	}
	if got := savedNames(t); got != "C A B D" {
		t.Errorf("saved %q", got)
	}

	// The bottom line of a session puts it below that session.
	dragRows(m, rowY(0, 1), rowY(2, 2)) // C onto B's last line
	if got := order(m); got != "A B C D" {
		t.Errorf("C below B: %q", got)
	}
	// The middle line counts as the top.
	dragRows(m, rowY(0, 0), rowY(3, 1)) // A onto D's middle line
	if got := order(m); got != "B C A D" {
		t.Errorf("A above D: %q", got)
	}
	// Below the last session.
	dragRows(m, rowY(0, 0), rowY(6, 0))
	if got := order(m); got != "C A D B" {
		t.Errorf("B to the end: %q", got)
	}
}

// Moving within the session's own rows, or next to itself, is a drag that
// changes nothing.
func TestDragInPlace(t *testing.T) {
	m := dragModel(t, 32, "A", "B", "C")
	mouseDown(m, 5, rowY(1, 0))
	mouseMove(m, 5, rowY(1, 2))
	if !m.dragging() {
		t.Fatal("moving a line should start a drag")
	}
	mouseUp(m, 5, rowY(1, 2))
	dragRows(m, rowY(1, 0), rowY(0, 2)) // just below A: where it is
	if got := order(m); got != "A B C" {
		t.Errorf("order %q", got)
	}
	if got := savedNames(t); got != "" {
		t.Errorf("nothing moved, so nothing should be saved: %q", got)
	}
}

// With a filter on, positions count only the sessions shown, as with
// shift+↑/↓ (FD13): the list is A b C d E and the filter shows A C E.
func TestDragFiltered(t *testing.T) {
	fresh := func() *model {
		m := dragModel(t, 32, "keep-A", "b", "keep-C", "d", "keep-E")
		m.filter = "keep"
		return m
	}
	m := fresh()
	dragRows(m, rowY(2, 0), rowY(1, 0)) // E above C
	if got := order(m); got != "keep-A b keep-E keep-C d" {
		t.Errorf("E above C: %q, want A b E C d (as shift+up)", got)
	}
	m = fresh()
	dragRows(m, rowY(0, 0), rowY(2, 2)) // A below E
	if got := order(m); got != "b keep-C d keep-E keep-A" {
		t.Errorf("A below E: %q, want b C d E A (as shift+end)", got)
	}
	m = fresh()
	dragRows(m, rowY(0, 0), rowY(1, 2)) // A below C: before the hidden d
	if got := order(m); got != "b keep-C keep-A d keep-E" {
		t.Errorf("A below C: %q, want b C A d E (as shift+down)", got)
	}
	if m.filter != "keep" {
		t.Error("the filter should stay on")
	}
}

func TestDragEscCancels(t *testing.T) {
	m := dragModel(t, 32, "A", "B", "C")
	mouseDown(m, 5, rowY(2, 0))
	mouseMove(m, 5, rowY(0, 0))
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.drag != nil {
		t.Fatal("esc should end the drag")
	}
	mouseUp(m, 5, rowY(0, 0))
	if got := order(m); got != "A B C" {
		t.Errorf("after esc, the release moved it: %q", got)
	}

	// esc cancels the drag, not the filter.
	m.filter = "B"
	mouseDown(m, 5, rowY(0, 0))
	mouseMove(m, 5, rowY(0, 2))
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.filter != "B" {
		t.Error("esc during a drag shouldn't clear the filter")
	}
}

func TestDragReleaseOverTerminalCancels(t *testing.T) {
	m := dragModel(t, 32, "A", "B", "C")
	mouseDown(m, 5, rowY(2, 0))
	mouseMove(m, 5, rowY(0, 0))
	mouseMove(m, 60, rowY(0, 0))
	if m.drag.target.kind != dropNone || dropGapOf(m) >= 0 {
		t.Errorf("over the terminal there's no drop target: %+v", m.drag.target)
	}
	mouseUp(m, 60, rowY(0, 0))
	if got := order(m); got != "A B C" || m.drag != nil || m.focus != focusSidebar {
		t.Errorf("a release over the terminal should cancel: %q drag %+v focus %v", got, m.drag, m.focus)
	}
}

// Motion with no button held means the release went missing.
func TestDragMotionWithoutButtonCancels(t *testing.T) {
	m := dragModel(t, 32, "A", "B", "C")
	mouseDown(m, 5, rowY(2, 0))
	mouseMove(m, 5, rowY(0, 0))
	m.handleMouse(tea.MouseMotionMsg{X: 5, Y: rowY(0, 0)})
	if m.drag != nil {
		t.Error("motion without a button should end the drag")
	}
}

// Holding the pointer on the last row scrolls down one session every
// autoScrollFrames frames, up to the end; on the header it scrolls back.
func TestDragAutoScroll(t *testing.T) {
	m := dragModel(t, 14, "A", "B", "C", "D", "E", "F") // 3 rows fit
	if m.listRows() != 3 {
		t.Fatalf("rows = %d, want 3", m.listRows())
	}
	m.renderSidebar(m.sidebarWidth(), m.height-1)
	mouseDown(m, 5, rowY(0, 0))
	mouseMove(m, 5, rowY(2, 2))
	for i := 0; i < autoScrollFrames-1; i++ {
		m.dragTick()
	}
	if m.scroll != 0 {
		t.Fatalf("scrolled after %d frames", autoScrollFrames-1)
	}
	m.dragTick()
	if m.scroll != 1 {
		t.Fatalf("scroll = %d after %d frames, want 1", m.scroll, autoScrollFrames)
	}
	// The selection (the dragged session) is out of view now; drawing the
	// list mustn't scroll back to it.
	m.renderSidebar(m.sidebarWidth(), m.height-1)
	if m.scroll != 1 {
		t.Fatalf("drawing scrolled back to the dragged session: %d", m.scroll)
	}
	for i := 0; i < 10*autoScrollFrames; i++ {
		m.dragTick()
	}
	if m.scroll != 3 {
		t.Fatalf("scroll = %d, want 3 (F at the bottom)", m.scroll)
	}
	if m.drag.target.gap != 6 {
		t.Errorf("gap = %d, want 6 (below F)", m.drag.target.gap)
	}
	// Away from the edge it stops.
	mouseMove(m, 5, rowY(1, 0))
	for i := 0; i < 2*autoScrollFrames; i++ {
		m.dragTick()
	}
	if m.scroll != 3 {
		t.Errorf("scrolled away from the edge: %d", m.scroll)
	}
	// On the header it scrolls up.
	mouseMove(m, 5, 0)
	for i := 0; i < autoScrollFrames; i++ {
		m.dragTick()
	}
	if m.scroll != 2 || m.drag.target.gap != 2 {
		t.Errorf("on the header: scroll %d gap %d, want 2 and 2", m.scroll, m.drag.target.gap)
	}
	mouseUp(m, 5, 0)
	if got := order(m); got != "B A C D E F" {
		t.Errorf("dropped on the header: %q, want A above the first row shown", got)
	}
}

func TestDragDrawsDropLine(t *testing.T) {
	m := dragModel(t, 32, "A", "B", "C")
	w := m.sidebarWidth()
	lines := m.renderSidebar(w, m.height-1)
	for _, l := range lines {
		if strings.Contains(l, "━") {
			t.Fatal("drop line without a drag")
		}
	}
	mouseDown(m, 5, rowY(0, 0))
	mouseMove(m, 5, rowY(1, 2)) // below B
	lines = m.renderSidebar(w, m.height-1)
	at := -1
	for i, l := range lines {
		if strings.Contains(l, "━") {
			at = i
		}
	}
	if at != rowY(2, 0) {
		t.Fatalf("drop line on line %d, want %d:\n%s", at, rowY(2, 0), ansi.Strip(strings.Join(lines, "\n")))
	}
	if !strings.Contains(ansi.Strip(lines[at+1]), "C") {
		t.Errorf("C should follow the drop line: %q", ansi.Strip(lines[at+1]))
	}
	if got := ansi.StringWidth(lines[at]); got != w {
		t.Errorf("drop line is %d cells, want %d", got, w)
	}
	// Below the last session.
	mouseMove(m, 5, rowY(5, 0))
	lines = m.renderSidebar(w, m.height-1)
	if !strings.Contains(lines[rowY(3, 0)], "━") {
		t.Errorf("drop line below the last session missing:\n%s", ansi.Strip(strings.Join(lines, "\n")))
	}
}

// The dragged session is tracked by pointer: if the list changes during
// the drag (a session is removed), the right one still moves.
func TestDragFollowsTheSession(t *testing.T) {
	m := dragModel(t, 32, "A", "B", "C", "D")
	mouseDown(m, 5, rowY(3, 0)) // D
	mouseMove(m, 5, rowY(1, 0))
	m.sessions = m.sessions[1:] // A goes away: B C D
	m.sel = 0
	mouseUp(m, 5, rowY(1, 0)) // above C
	if got := order(m); got != "B D C" {
		t.Errorf("order %q, want B D C", got)
	}
}

// dropGapOf is the row the drop line is drawn above, or -1.
func dropGapOf(m *model) int {
	gap, _ := m.dropLineAt()
	return gap
}
