package main

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Dragging sessions in the list with the mouse (#46).
//
// Pressing the left button on a session selects it, as a click always has.
// Moving the pointer to another line with the button held starts a drag: a
// drop line shows where the session will land, and releasing the button
// moves it there with the same code as shift+↑/↓ (moveSelectedTo), so a
// filter works the same way: positions count only the sessions you can see,
// and hidden ones keep their order. esc, or releasing over the terminal
// pane, cancels. Holding the pointer on the list's header or its last row
// scrolls the list, so a session can go past the edge.

// listTop is the sidebar line where the first session row starts, below
// the title and its border.
const listTop = 2

// autoScrollFrames is how many frames (50 ms each) the pointer has to stay
// on an edge of the list for it to scroll by one session.
const autoScrollFrames = 4

// dropKind says what a drop lands on. Only dropAt exists so far; groups
// (#47) add a header (the end of that group) and the "(no group)" target.
type dropKind int

const (
	dropNone dropKind = iota // nowhere: releasing cancels the drag
	dropAt                   // a gap in the visible list (gap)
)

// dropTarget is where a dragged session would land if released now.
type dropTarget struct {
	kind dropKind
	// gap is the place in the visible list: 0 is above the first session
	// shown with the current filter, len(visible()) below the last.
	gap int
	// group is the group the drop lands in ("" for none). It's always ""
	// until groups (#47).
	group string
}

// dragState is a press on a session that may become a drag.
type dragState struct {
	from   *Session   // the session pressed on; tracked by pointer, not index
	startY int        // the screen line pressed on
	active bool       // the pointer has moved to another line: it's a drag
	x, y   int        // the pointer's last position
	target dropTarget // where it would land now
	edge   int        // frames the pointer has spent on an edge of the list
}

// listRows is how many session rows fit in the sidebar.
func (m *model) listRows() int {
	n := (m.height - 1 - listTop) / rowsPerSession
	return max(n, 1)
}

// maxScroll is the furthest the list can scroll: the last session at the
// bottom.
func (m *model) maxScroll(nvis int) int {
	return max(0, nvis-m.listRows())
}

// startDrag records a press on the session at visible position p.
func (m *model) startDrag(p, x, y int) {
	vis := m.visible()
	if p < 0 || p >= len(vis) {
		return
	}
	m.drag = &dragState{from: m.sessions[vis[p]], startY: y, x: x, y: y}
}

// cancelDrag forgets a drag (or a press that hasn't become one).
func (m *model) cancelDrag() { m.drag = nil }

// dragging reports whether a drag is under way (past the first move).
func (m *model) dragging() bool { return m.drag != nil && m.drag.active }

// dragMouse handles the mouse while a press or drag is under way, wherever
// the pointer is: none of it goes to the terminal pane.
func (m *model) dragMouse(msg tea.MouseMsg) {
	ms := msg.Mouse()
	d := m.drag
	switch msg.(type) {
	case tea.MouseMotionMsg:
		if ms.Button != tea.MouseLeft {
			// Cell-motion mode only reports motion with a button held, so
			// this means the release went missing (outside the window).
			m.cancelDrag()
			return
		}
		d.x, d.y = ms.X, ms.Y
		if !d.active && ms.Y != d.startY {
			d.active = true
		}
		if d.active {
			d.target = m.dropTargetAt(ms.X, ms.Y)
		}
	case tea.MouseReleaseMsg:
		if d.active {
			d.target = m.dropTargetAt(ms.X, ms.Y)
			m.drop()
		}
		m.cancelDrag()
	case tea.MouseWheelMsg:
		// The wheel scrolls the list under the dragged session.
		if d.active && m.inSidebar(ms.X) {
			delta := map[tea.MouseButton]int{tea.MouseWheelUp: -1, tea.MouseWheelDown: 1}[ms.Button]
			m.scroll = min(max(m.scroll+delta, 0), m.maxScroll(len(m.visible())))
			d.target = m.dropTargetAt(d.x, d.y)
		}
	case tea.MouseClickMsg:
		// Another press without a release: start over.
		m.cancelDrag()
	}
}

// inSidebar reports whether screen column x is over the session list.
func (m *model) inSidebar(x int) bool {
	return !m.listHidden && x >= 0 && x < m.sidebarWidth()
}

// dropTargetAt is where a drop at screen cell (x, y) would land. Over the
// terminal pane it's nowhere. On the header it's above the first row shown;
// below the last row, below the last session. On a session, the top of its
// three lines puts it above that session and the bottom one below; the
// middle line counts as the top.
func (m *model) dropTargetAt(x, y int) dropTarget {
	if !m.inSidebar(x) {
		return dropTarget{}
	}
	vis := m.visible()
	if len(vis) == 0 {
		return dropTarget{}
	}
	if y < listTop {
		return dropTarget{kind: dropAt, gap: min(m.scroll, len(vis))}
	}
	line := y - listTop
	p := m.scroll + line/rowsPerSession
	if p >= len(vis) || line/rowsPerSession >= m.listRows() {
		return dropTarget{kind: dropAt, gap: min(len(vis), m.scroll+m.listRows())}
	}
	gap := p
	if line%rowsPerSession == rowsPerSession-1 {
		gap = p + 1
	}
	return dropTarget{kind: dropAt, gap: gap}
}

// edgeDir is which way the list should scroll with the pointer at line y:
// -1 on the header, 1 on the last row shown, 0 elsewhere.
func (m *model) edgeDir(x, y int) int {
	if !m.inSidebar(x) {
		return 0
	}
	if y < listTop {
		return -1
	}
	rows := m.listRows()
	last := listTop + (rows-1)*rowsPerSession
	if y >= last && y < last+rowsPerSession {
		return 1
	}
	return 0
}

// dragTick runs every frame: holding the pointer on an edge of the list
// scrolls it by one session every autoScrollFrames frames, and the drop
// target follows.
func (m *model) dragTick() {
	if !m.dragging() {
		return
	}
	d := m.drag
	dir := m.edgeDir(d.x, d.y)
	if dir == 0 {
		d.edge = 0
		return
	}
	d.edge++
	if d.edge%autoScrollFrames != 0 {
		return
	}
	n := len(m.visible())
	m.scroll = min(max(m.scroll+dir, 0), m.maxScroll(n))
	d.target = m.dropTargetAt(d.x, d.y)
}

// drop moves the dragged session to the drop target, selects it and saves
// the order. A drop where it already is changes nothing.
func (m *model) drop() {
	d := m.drag
	if d.target.kind != dropAt {
		return
	}
	from := slices.Index(m.sessions, d.from)
	if from < 0 {
		return // removed meanwhile
	}
	m.sel = from
	m.focus = focusSidebar
	if m.moveToGap(d.target.gap) {
		_ = saveSessions(m.sessions)
	}
}

// moveToGap moves the selected session to a gap in the visible list (see
// dropTarget.gap), by the rules of shift+↑/↓: moving up it lands just
// before the visible session below the gap; moving down, just after the one
// above it. Hidden sessions keep their order. It reports whether anything
// moved.
func (m *model) moveToGap(gap int) bool {
	vis := m.visible()
	p := visiblePos(vis, m.sel)
	if p < 0 || gap < 0 || gap > len(vis) || gap == p || gap == p+1 {
		return false
	}
	if gap < p {
		m.moveSelectedTo(vis[gap])
	} else {
		m.moveSelectedTo(vis[gap-1])
	}
	return true
}

// dropLine is the accent line drawn at the drop target.
func dropLine(w int) string {
	return lipgloss.NewStyle().Foreground(colAccent).Render(strings.Repeat("━", w))
}

// dropGap is the gap to draw the drop line at, or -1 for none.
func (m *model) dropGap() int {
	if !m.dragging() || m.drag.target.kind != dropAt {
		return -1
	}
	return m.drag.target.gap
}
