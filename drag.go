package main

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Dragging in the list with the mouse (#46, and groups in #47).
//
// Pressing the left button on a row selects it, as a click always has.
// Moving the pointer to another line with the button held starts a drag: a
// drop line shows where the session will land, and releasing the button
// moves it there by the rules of shift+↑/↓ (place), so a filter works the
// same way: positions count only the sessions you can see, and hidden ones
// keep their order. esc, or releasing over the terminal pane, cancels.
// Holding the pointer on the list's header or its last row scrolls the
// list, so a session can go past the edge.
//
// With groups, a session can be dropped between sessions of a group (the
// drop line is indented under it), or on a group's header to go at the end
// of that group, collapsed or not. Hovering over a collapsed header opens
// it until the pointer leaves the group. When every session is in a group,
// a "(no group)" header shows at the bottom while dragging, as a target.
// Dragging a group's header moves the whole group between the others.

// listTop is the sidebar line where the first row starts, below the title
// and its border.
const listTop = 2

// autoScrollFrames is how many frames (50 ms each) the pointer has to stay
// on an edge of the list for it to scroll by one row.
const autoScrollFrames = 4

// hoverOpenFrames is how long (in 50 ms frames) a session has to hover
// over a collapsed group's header for the group to open.
const hoverOpenFrames = 20

// dropKind says what a drop lands on.
type dropKind int

const (
	dropNone   dropKind = iota // nowhere: releasing cancels the drag
	dropAt                     // a session, between two rows (gap) in group
	dropHeader                 // a session, on group's header: the end of that group ("" is "(no group)")
	dropGroup                  // a whole group, above the header at gap (beforeGroup), or after the last group
)

// dropTarget is where the dragged session or group would land if released
// now.
type dropTarget struct {
	kind dropKind
	// gap is the row the drop line goes above, as an index into rows();
	// len(rows) is below the last row.
	gap int
	// group is the group a session lands in ("" for none).
	group string
	// after and before are the sessions either side of the gap that are in
	// group (nil when there's none).
	after, before *Session
	// beforeGroup is the group a dragged group goes above; nil puts it
	// after the last one.
	beforeGroup *group
}

// dragState is a press on a row that may become a drag.
type dragState struct {
	from   *Session   // the session pressed on (tracked by pointer, not index)
	grp    *group     // or the group whose header was pressed on
	startY int        // the screen line pressed on
	active bool       // the pointer has moved to another line: it's a drag
	x, y   int        // the pointer's last position
	target dropTarget // where it would land now
	edge   int        // frames the pointer has spent on an edge of the list
	hover  int        // frames spent on a collapsed group's header
	opened *string    // the collapsed group hovering opened, to close again
}

// listRows is how many session rows fit in the sidebar.
func (m *model) listRows() int {
	return max(m.listHeight()/rowsPerSession, 1)
}

// startDrag records a press on a row. Only a session or a named group's
// header can be dragged.
func (m *model) startDrag(row listRow, x, y int) {
	d := &dragState{startY: y, x: x, y: y}
	switch {
	case row.kind == rowSession:
		d.from = m.sessions[row.idx]
	case row.group != "":
		d.grp = m.groupByName(row.group)
	}
	if d.from == nil && d.grp == nil {
		return
	}
	m.drag = d
}

// cancelDrag forgets a drag (or a press that hasn't become one), closing a
// group that hovering opened.
func (m *model) cancelDrag() {
	if d := m.drag; d != nil && d.opened != nil {
		m.setCollapsed(*d.opened, true)
	}
	m.drag = nil
}

// dragging reports whether a drag is under way (past the first move).
func (m *model) dragging() bool { return m.drag != nil && m.drag.active }

// draggingSession reports whether a session is being dragged.
func (m *model) draggingSession() bool { return m.dragging() && m.drag.from != nil }

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
			m.retarget()
		}
	case tea.MouseReleaseMsg:
		if d.active {
			d.x, d.y = ms.X, ms.Y
			m.retarget()
			m.drop()
		}
		m.cancelDrag()
	case tea.MouseWheelMsg:
		// The wheel scrolls the list under the dragged row.
		if d.active && m.inSidebar(ms.X) {
			delta := map[tea.MouseButton]int{tea.MouseWheelUp: -1, tea.MouseWheelDown: 1}[ms.Button]
			m.scroll = min(max(m.scroll+delta, 0), m.maxRowScroll(m.rows()))
			m.retarget()
		}
	case tea.MouseClickMsg:
		// Another press without a release: start over.
		m.cancelDrag()
	}
}

// retarget works out the drop target at the pointer, and closes a group
// that hovering opened once the pointer leaves it.
func (m *model) retarget() {
	d := m.drag
	old := d.target
	d.target = m.dropTargetAt(d.x, d.y)
	if d.opened != nil && (d.target.kind == dropNone || d.target.group != *d.opened) {
		m.setCollapsed(*d.opened, true)
		d.opened = nil
		d.target = m.dropTargetAt(d.x, d.y)
	}
	if d.target.kind != dropHeader || old.kind != dropHeader || old.group != d.target.group {
		d.hover = 0
	}
}

// inSidebar reports whether screen column x is over the session list.
func (m *model) inSidebar(x int) bool {
	return !m.listHidden && x >= 0 && x < m.sidebarWidth()
}

// dropTargetAt is where a drop at screen cell (x, y) would land. Over the
// terminal pane it's nowhere.
func (m *model) dropTargetAt(x, y int) dropTarget {
	if !m.inSidebar(x) || m.drag == nil {
		return dropTarget{}
	}
	rows := m.rows()
	if len(rows) == 0 {
		return dropTarget{}
	}
	r, line := m.rowAt(rows, y)
	if r < 0 { // on the list's header: the top of the first row shown
		r, line = min(m.scroll, len(rows)-1), 0
	}
	if m.drag.grp != nil {
		return m.groupTargetAt(rows, r)
	}
	if r >= len(rows) || r >= shownEnd(rows, m.scroll, m.listHeight()) {
		// Below the last row shown: as on the last line of that row.
		r = min(r, len(rows)) - 1
		line = rowHeight(rows, r) - 1
	}
	row := rows[r]
	if row.kind == rowHeader {
		return dropTarget{kind: dropHeader, gap: r, group: row.group}
	}
	gap := r
	if line == rowsPerSession-1 {
		gap = r + 1 // the bottom line: below it
	}
	return m.gapTarget(rows, gap, row.group)
}

// gapTarget is a drop of a session at a gap between rows, in group g.
func (m *model) gapTarget(rows []listRow, gap int, g string) dropTarget {
	t := dropTarget{kind: dropAt, gap: gap, group: g}
	if gap > 0 && rows[gap-1].kind == rowSession && rows[gap-1].group == g {
		t.after = m.sessions[rows[gap-1].idx]
	}
	if gap < len(rows) && rows[gap].kind == rowSession && rows[gap].group == g {
		t.before = m.sessions[rows[gap].idx]
	}
	return t
}

// groupTargetAt is where a dragged group would land with the pointer on
// row r: above a group when on its header, below it when on one of its
// sessions, and after the last group on "(no group)" or below the list.
func (m *model) groupTargetAt(rows []listRow, r int) dropTarget {
	// The rows where each named group's header is, in order.
	var heads []int
	for i, row := range rows {
		if row.kind == rowHeader && row.group != "" {
			heads = append(heads, i)
		}
	}
	end := dropTarget{kind: dropGroup, gap: len(rows)}
	if i := headerIndex(rows, ""); i >= 0 {
		end.gap = i
	} else if n := len(rows); n > 0 && rows[n-1].temp {
		end.gap = n - 1
	}
	if r >= len(rows) || rows[r].group == "" {
		return end
	}
	h := headerIndex(rows, rows[r].group)
	if rows[r].kind == rowSession { // below its group: above the next one
		if k := slices.Index(heads, h); k+1 < len(heads) {
			h = heads[k+1]
		} else {
			return end
		}
	}
	return dropTarget{kind: dropGroup, gap: h, beforeGroup: m.groupByName(rows[h].group)}
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
	rows := m.rows()
	end := shownEnd(rows, m.scroll, m.listHeight())
	if end == 0 {
		return 0
	}
	if r, _ := m.rowAt(rows, y); r == end-1 {
		return 1
	}
	return 0
}

// dragTick runs every frame: holding the pointer on an edge of the list
// scrolls it by one row every autoScrollFrames frames, and hovering a
// session over a collapsed group's header for hoverOpenFrames opens it.
func (m *model) dragTick() {
	if !m.dragging() {
		return
	}
	d := m.drag
	if t := d.target; d.from != nil && t.kind == dropHeader && m.collapsed(t.group) && d.opened == nil && m.grouped() && !m.filtering() {
		d.hover++
		if d.hover >= hoverOpenFrames {
			name := t.group
			m.setCollapsed(name, false)
			d.opened = &name
			d.hover = 0
			m.retarget()
		}
	}
	dir := m.edgeDir(d.x, d.y)
	if dir == 0 {
		d.edge = 0
		return
	}
	d.edge++
	if d.edge%autoScrollFrames != 0 {
		return
	}
	m.scroll = min(max(m.scroll+dir, 0), m.maxRowScroll(m.rows()))
	m.retarget()
}

// drop moves the dragged session or group to the drop target, selects it
// and saves. A drop where it already is changes nothing.
func (m *model) drop() {
	d := m.drag
	t := d.target
	switch {
	case t.kind == dropGroup && d.grp != nil:
		m.dropGroupAt(d.grp, t.beforeGroup)
	case d.from == nil:
	case t.kind == dropAt || t.kind == dropHeader:
		if m.indexOf(d.from) < 0 {
			return // removed meanwhile
		}
		if t.kind == dropAt && m.collapsed(t.group) && !m.filtering() {
			return // closed meanwhile
		}
		d.opened = nil // dropped where hovering opened: it stays open
		m.focus = focusSidebar
		var changed bool
		if t.kind == dropHeader {
			changed = m.place(d.from, t.group, nil, nil)
		} else {
			changed = m.place(d.from, t.group, t.after, t.before)
		}
		m.keepSelectionVisible() // on a collapsed group's header: select the header
		if changed {
			m.saveAll()
		} else if m.grouped() {
			_ = m.saveGroups() // a group hovering opened stays open
		}
	}
}

// dropGroupAt moves group g above `before` (nil: after the last group).
func (m *model) dropGroupAt(g, before *group) {
	if before == g {
		return
	}
	i := slices.Index(m.groups, g)
	if i < 0 {
		return
	}
	j := len(m.groups)
	if before != nil {
		j = slices.Index(m.groups, before)
	}
	if j == i+1 { // already just above it
		return
	}
	m.groups = slices.Delete(m.groups, i, i+1)
	j = len(m.groups)
	if before != nil {
		j = slices.Index(m.groups, before)
	}
	m.groups = slices.Insert(m.groups, j, g)
	m.normalize()
	m.onHeader, m.selHeader = true, g.Name
	m.focus = focusSidebar
	m.saveAll()
}

// dropLine is the accent line drawn at the drop target, indented by
// indent cells.
func dropLine(w, indent int) string {
	return strings.Repeat(" ", indent) + lipgloss.NewStyle().Foreground(colAccent).Render(strings.Repeat("━", max(w-indent, 1)))
}

// dropLineAt is the row to draw the drop line above (-1 for none) and its
// indent: under a group's header for a session dropped in a group.
func (m *model) dropLineAt() (gap, indent int) {
	if !m.dragging() {
		return -1, 0
	}
	switch t := m.drag.target; t.kind {
	case dropAt:
		if m.grouped() {
			return t.gap, 2
		}
		return t.gap, 0
	case dropGroup:
		return t.gap, 0
	}
	return -1, 0
}

// hotHeader is the group whose header is the drop target, if any.
func (m *model) hotHeader() (string, bool) {
	if !m.dragging() || m.drag.target.kind != dropHeader {
		return "", false
	}
	return m.drag.target.group, true
}
