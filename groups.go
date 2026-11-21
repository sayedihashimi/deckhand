package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Groups in the session list (#47): sessions can be filed under named
// groups with headers you can collapse. A collapsed header still shows
// what needs you inside it. With no groups the list is exactly as before:
// no headers at all.
//
// The list is rows of two kinds, headers and sessions (rows). m.sessions
// is always kept in display order, group by group (normalize): each named
// group's sessions in m.groups order, then the ones in no group. So the
// sessions of a group are next to each other, and the code that moves a
// session by index (moveSelectedTo) keeps working inside a group.
//
// The selection is a session (m.sel) or, with m.onHeader, a header
// (m.selHeader, "" for "(no group)"). current() is nil on a header.
//
// Each session's group is saved in sessions.json ("group"). The groups'
// order and which are collapsed are in groups.json beside it, so a
// deckhand from before groups still reads sessions.json.

// noGroupName is the header of the sessions in no group.
const noGroupName = "(no group)"

type group struct {
	Name      string `json:"name"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

type groupsFile struct {
	Groups           []group `json:"groups"`
	NoGroupCollapsed bool    `json:"no_group_collapsed,omitempty"`
}

func groupsPath() string { return filepath.Join(dataDir(), "groups.json") }

func loadGroups() groupsFile {
	var f groupsFile
	data, err := os.ReadFile(groupsPath())
	if err != nil || json.Unmarshal(data, &f) != nil {
		return groupsFile{}
	}
	return f
}

// saveGroups writes groups.json, or removes it when there's nothing to keep.
func (m *model) saveGroups() error {
	f := groupsFile{NoGroupCollapsed: m.noGroupCollapsed}
	for _, g := range m.groups {
		f.Groups = append(f.Groups, *g)
	}
	if len(f.Groups) == 0 && !f.NoGroupCollapsed {
		if err := os.Remove(groupsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	tmp := groupsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, groupsPath())
}

// saveAll saves the sessions and the groups.
func (m *model) saveAll() {
	_ = saveSessions(m.sessions)
	_ = m.saveGroups()
}

func (s *Session) group() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Group
}

func (s *Session) setGroup(g string) {
	s.mu.Lock()
	s.Group = g
	s.mu.Unlock()
}

// setGroups sets the groups (from groups.json) and makes them agree with
// the sessions: a group a session names that isn't in the list is added at
// the end, and duplicate or blank names are dropped. Then the sessions are
// put in display order.
func (m *model) setGroups(f groupsFile) {
	m.groups = nil
	m.noGroupCollapsed = f.NoGroupCollapsed
	for _, g := range f.Groups {
		m.addGroup(g.Name, g.Collapsed)
	}
	for _, s := range m.sessions {
		if g := s.group(); g != "" && m.groupByName(g) == nil {
			if m.addGroup(g, false) == nil {
				s.setGroup("")
			}
		}
	}
	m.normalize()
}

// validGroupName trims a name and says whether it can be a group's.
func validGroupName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	return name, name != "" && !strings.EqualFold(name, noGroupName)
}

// addGroup adds a group at the end, unless the name is taken or invalid
// (then it returns the existing group, or nil).
func (m *model) addGroup(name string, collapsed bool) *group {
	name, ok := validGroupName(name)
	if !ok {
		return nil
	}
	if g := m.groupByName(name); g != nil {
		return g
	}
	g := &group{Name: name, Collapsed: collapsed}
	m.groups = append(m.groups, g)
	return g
}

func (m *model) groupByName(name string) *group {
	for _, g := range m.groups {
		if g.Name == name {
			return g
		}
	}
	return nil
}

func (m *model) grouped() bool { return len(m.groups) > 0 }

// groupRank is a group's place in the list; "(no group)" is last.
func (m *model) groupRank(name string) int {
	for i, g := range m.groups {
		if g.Name == name {
			return i
		}
	}
	return len(m.groups)
}

// collapsed reports whether a group's sessions are folded away ("" is
// "(no group)").
func (m *model) collapsed(name string) bool {
	if name == "" {
		return m.noGroupCollapsed
	}
	g := m.groupByName(name)
	return g != nil && g.Collapsed
}

func (m *model) setCollapsed(name string, c bool) {
	if name == "" {
		m.noGroupCollapsed = c
	} else if g := m.groupByName(name); g != nil {
		g.Collapsed = c
	}
}

// normalize puts the sessions in display order, group by group, keeping
// their order within each group. The selection stays on its session.
func (m *model) normalize() {
	if !m.grouped() {
		return
	}
	var cur *Session
	if m.sel >= 0 && m.sel < len(m.sessions) {
		cur = m.sessions[m.sel]
	}
	rank := map[*Session]int{}
	for _, s := range m.sessions {
		rank[s] = m.groupRank(s.group())
	}
	slices.SortStableFunc(m.sessions, func(a, b *Session) int { return cmp.Compare(rank[a], rank[b]) })
	if i := m.indexOf(cur); i >= 0 {
		m.sel = i
	}
}

// Rows ------------------------------------------------------------------------

type rowKind int

const (
	rowSession rowKind = iota
	rowHeader
)

// listRow is one row of the session list.
type listRow struct {
	kind  rowKind
	idx   int    // a session row: its index in m.sessions
	group string // the group it's in, or the header's group; "" is "(no group)"
	temp  bool   // the "(no group)" drop target shown only while dragging
}

// rows are the list's rows, top to bottom. With no groups they're just the
// sessions the filter shows. With groups: each group's header, then its
// sessions unless it's collapsed; then "(no group)" and its sessions, when
// there are any. With a filter on, sessions in collapsed groups show too,
// and headers with nothing matching don't. While a session is dragged and
// every session is in a group, a "(no group)" header is added at the end
// as a drop target.
func (m *model) rows() []listRow {
	vis := m.visible()
	if !m.grouped() {
		out := make([]listRow, len(vis))
		for p, i := range vis {
			out[p] = listRow{kind: rowSession, idx: i}
		}
		return out
	}
	shown := map[string][]int{}
	for _, i := range vis {
		g := m.sessions[i].group()
		shown[g] = append(shown[g], i)
	}
	filtering := m.filtering()
	out := make([]listRow, 0, len(vis)+len(m.groups)+1)
	add := func(name string) {
		out = append(out, listRow{kind: rowHeader, group: name})
		if m.collapsed(name) && !filtering {
			return
		}
		for _, i := range shown[name] {
			out = append(out, listRow{kind: rowSession, idx: i, group: name})
		}
	}
	for _, g := range m.groups {
		if !filtering || len(shown[g.Name]) > 0 {
			add(g.Name)
		}
	}
	switch {
	case len(shown[""]) > 0:
		add("")
	case m.draggingSession():
		out = append(out, listRow{kind: rowHeader, temp: true})
	}
	return out
}

// rowHeight is how many lines row r takes: three for a session; one for a
// header, plus a blank line above it unless it's the first row.
func rowHeight(rows []listRow, r int) int {
	if rows[r].kind == rowSession {
		return rowsPerSession
	}
	if r > 0 {
		return 2
	}
	return 1
}

// listHeight is how many lines the sidebar has for rows.
func (m *model) listHeight() int { return max(m.height-1-listTop, rowsPerSession) }

// shownEnd is the end (exclusive) of the rows that fit from m.scroll.
func shownEnd(rows []listRow, scroll, avail int) int {
	used := 0
	r := scroll
	for ; r < len(rows); r++ {
		h := rowHeight(rows, r)
		if used+h > avail && r > scroll {
			break
		}
		used += h
	}
	return r
}

// rowAt is the row at screen line y and the line within it. Above the
// rows it's (-1, 0); past the last row shown, (the first row not shown, 0).
func (m *model) rowAt(rows []listRow, y int) (r, line int) {
	if y < listTop {
		return -1, 0
	}
	at := listTop
	end := shownEnd(rows, m.scroll, m.listHeight())
	for r = m.scroll; r < end; r++ {
		h := rowHeight(rows, r)
		if y < at+h {
			return r, y - at
		}
		at += h
	}
	return end, 0
}

// maxRowScroll is the furthest the list can scroll: the last row at the
// bottom.
func (m *model) maxRowScroll(rows []listRow) int {
	used, avail := 0, m.listHeight()
	for r := len(rows) - 1; r >= 0; r-- {
		used += rowHeight(rows, r)
		if used > avail {
			return r + 1
		}
	}
	return 0
}

// selRow is the selected row's index in rows, or -1 if it isn't shown.
func (m *model) selRow(rows []listRow) int {
	for r, row := range rows {
		switch {
		case row.temp:
		case m.onHeader && row.kind == rowHeader && row.group == m.selHeader:
			return r
		case !m.onHeader && row.kind == rowSession && row.idx == m.sel:
			return r
		}
	}
	return -1
}

// selectRow selects a row.
func (m *model) selectRow(row listRow) {
	if row.kind == rowHeader {
		if !row.temp {
			m.onHeader, m.selHeader = true, row.group
		}
		return
	}
	m.onHeader, m.sel = false, row.idx
}

// selectSession selects a session by index.
func (m *model) selectSession(i int) { m.onHeader, m.sel = false, i }

// selectedIdx is the selected session's index, or -1 on a header.
func (m *model) selectedIdx() int {
	if m.onHeader {
		return -1
	}
	return m.sel
}

// headerIndex is the index of a group's header in rows, or -1.
func headerIndex(rows []listRow, name string) int {
	for r, row := range rows {
		if row.kind == rowHeader && !row.temp && row.group == name {
			return r
		}
	}
	return -1
}

// groupCount is how many sessions a group has, shown or not.
func (m *model) groupCount(name string) int {
	n := 0
	for _, s := range m.sessions {
		if s.group() == name {
			n++
		}
	}
	return n
}

// groupEnd is where a session goes to be last in a group: after the
// group's last session, or where the group's sessions would start.
func (m *model) groupEnd(name string) int {
	rank := m.groupRank(name)
	at := 0
	for i, s := range m.sessions {
		if r := m.groupRank(s.group()); r <= rank {
			at = i + 1
		}
	}
	return at
}

// firstIn is the first session of a group, or nil.
func (m *model) firstIn(name string) *Session {
	for _, s := range m.sessions {
		if s.group() == name {
			return s
		}
	}
	return nil
}

// place moves session s into a group: right after `after` or right before
// `before` (sessions in that group, either may be nil), or at the end of the
// group when both are nil. It follows the rule of shift+↑/↓ (FD13): moving
// down it lands just after `after`, moving up just before `before`, so
// hidden sessions keep their order. s becomes the selection. It reports
// whether anything changed.
func (m *model) place(s *Session, name string, after, before *Session) bool {
	from := m.indexOf(s)
	if from < 0 {
		return false
	}
	oldGroup := s.group()
	if oldGroup == name && (after == s || before == s) {
		m.selectSession(from)
		return false
	}
	down := after != nil && from < m.indexOf(after)
	m.sessions = slices.Delete(m.sessions, from, from+1)
	s.setGroup(name)
	var at int
	switch {
	case after != nil && (down || before == nil):
		at = m.indexOf(after) + 1
	case before != nil:
		at = m.indexOf(before)
	default:
		at = m.groupEnd(name)
	}
	m.sessions = slices.Insert(m.sessions, at, s)
	m.sel = at
	m.normalize()
	m.selectSession(m.indexOf(s))
	return oldGroup != name || m.sel != from
}

// Keys on groups --------------------------------------------------------------

// toggleHeader collapses or expands the selected header's group. open is
// 1 to expand, -1 to collapse and 0 to switch.
func (m *model) toggleHeader(open int) {
	if !m.onHeader {
		return
	}
	c := !m.collapsed(m.selHeader)
	if open != 0 {
		c = open < 0
	}
	if c == m.collapsed(m.selHeader) {
		return
	}
	m.setCollapsed(m.selHeader, c)
	_ = m.saveGroups()
}

// selectOwnHeader selects the header of the selected session's group.
func (m *model) selectOwnHeader() {
	if s := m.current(); s != nil && m.grouped() {
		m.onHeader, m.selHeader = true, s.group()
	}
}

// moveGroup moves the selected group one place up or down past the next
// group shown (delta < 0 is up), or to the top or the bottom (toEnd). The
// "(no group)" header stays last. It reports whether anything moved.
func (m *model) moveGroup(delta int, toEnd bool) bool {
	g := m.groupByName(m.selHeader)
	if !m.onHeader || g == nil {
		return false
	}
	var shown []*group
	for _, row := range m.rows() {
		if row.kind == rowHeader && row.group != "" && !row.temp {
			shown = append(shown, m.groupByName(row.group))
		}
	}
	p := slices.Index(shown, g)
	q := p + 1
	if delta < 0 {
		q = p - 1
	}
	if toEnd {
		q = len(shown) - 1
		if delta < 0 {
			q = 0
		}
	}
	if p < 0 || q < 0 || q >= len(shown) || q == p {
		return false
	}
	m.moveGroupNextTo(g, shown[q], q < p)
	return true
}

// moveGroupNextTo moves g just before (above) or just after `next`.
func (m *model) moveGroupNextTo(g, next *group, above bool) {
	m.groups = slices.DeleteFunc(m.groups, func(x *group) bool { return x == g })
	at := slices.Index(m.groups, next)
	if !above {
		at++
	}
	m.groups = slices.Insert(m.groups, at, g)
	m.normalize()
	m.saveAll()
}

// moveSessionGrouped is shift+↑/↓ with groups: past the next session shown
// in its group, or over the group's edge into the group above (at its end)
// or below (at its top). A collapsed group it moves into is opened, so the
// session stays in view.
func (m *model) moveSessionGrouped(delta int) bool {
	rows := m.rows()
	r := m.selRow(rows)
	if r < 0 || m.onHeader {
		return false
	}
	s := m.sessions[m.sel]
	own := rows[r].group
	if delta < 0 {
		if r-1 >= 0 && rows[r-1].kind == rowSession {
			m.moveSelectedTo(rows[r-1].idx)
			return true
		}
		// rows[r-1] is our header: go to the end of the group above it.
		if r-2 < 0 {
			return false
		}
		above := rows[r-2]
		m.setCollapsed(above.group, false)
		var after *Session
		if above.kind == rowSession {
			after = m.sessions[above.idx]
		}
		return m.place(s, above.group, after, nil)
	}
	if r+1 < len(rows) && rows[r+1].kind == rowSession && rows[r+1].group == own {
		m.moveSelectedTo(rows[r+1].idx)
		return true
	}
	if r+1 >= len(rows) || rows[r+1].temp {
		return false
	}
	next := rows[r+1].group // the next header
	m.setCollapsed(next, false)
	var before *Session
	if r+2 < len(rows) && rows[r+2].kind == rowSession && rows[r+2].group == next {
		before = m.sessions[rows[r+2].idx]
	} else {
		before = m.firstIn(next)
	}
	return m.place(s, next, nil, before)
}

// moveSessionInGroup is shift+home/end with groups: to the top or the
// bottom of its own group.
func (m *model) moveSessionInGroup(top bool) bool {
	rows := m.rows()
	r := m.selRow(rows)
	if r < 0 || m.onHeader {
		return false
	}
	own := rows[r].group
	target := -1
	for _, row := range rows {
		if row.kind == rowSession && row.group == own {
			if target < 0 || !top {
				target = row.idx
			}
		}
	}
	if target < 0 || target == m.sel {
		return false
	}
	m.moveSelectedTo(target)
	return true
}

// startRenameGroup opens the rename box for the selected group.
func (m *model) startRenameGroup() tea.Cmd {
	g := m.groupByName(m.selHeader)
	if !m.onHeader || g == nil {
		return nil
	}
	ti := textinput.New()
	ti.Prompt = " Rename group: "
	ti.SetValue(g.Name)
	ti.CursorEnd()
	ti.SetWidth(40)
	m.rename = &ti
	m.renameGroup = g
	return m.rename.Focus()
}

// renameGroupTo renames a group and the sessions in it.
func (m *model) renameGroupTo(g *group, name string) {
	name, ok := validGroupName(name)
	if !ok || name == g.Name {
		return
	}
	if m.groupByName(name) != nil {
		m.setFlash(fmt.Sprintf("There's already a group called %q", name))
		return
	}
	for _, s := range m.sessions {
		if s.group() == g.Name {
			s.setGroup(name)
		}
	}
	if m.onHeader && m.selHeader == g.Name {
		m.selHeader = name
	}
	g.Name = name
	m.saveAll()
}

// askDeleteGroup asks before deleting the selected group. Its sessions
// move to "(no group)"; no session is removed.
func (m *model) askDeleteGroup() {
	g := m.groupByName(m.selHeader)
	if !m.onHeader || g == nil {
		return
	}
	n := m.groupCount(g.Name)
	text := fmt.Sprintf("Delete the group %q? y/n", g.Name)
	if n > 0 {
		text = fmt.Sprintf("Delete the group %q? Its %d session(s) move to %s. y/n", g.Name, n, noGroupName)
	}
	m.confirm = &confirmPrompt{text: text, action: func(m *model) tea.Cmd {
		m.deleteGroup(g) // by pointer: it may have been renamed meanwhile (it can't, but still)
		return nil
	}}
}

func (m *model) deleteGroup(g *group) {
	i := slices.Index(m.groups, g)
	if i < 0 {
		return
	}
	// Its sessions go to the start of "(no group)", in their order.
	var moved []*Session
	for _, s := range m.sessions {
		if s.group() == g.Name {
			s.setGroup("")
			moved = append(moved, s)
		}
	}
	m.groups = slices.Delete(m.groups, i, i+1)
	var cur *Session
	if !m.onHeader && m.sel >= 0 && m.sel < len(m.sessions) {
		cur = m.sessions[m.sel]
	}
	rest := slices.DeleteFunc(slices.Clone(m.sessions), func(s *Session) bool { return slices.Contains(moved, s) })
	at := m.groupEndIn(rest, "") - countIn(rest, "")
	m.sessions = slices.Insert(rest, at, moved...)
	m.normalize()
	if cur != nil {
		m.sel = m.indexOf(cur)
	}
	if m.onHeader && m.selHeader == g.Name {
		m.selHeader = ""
		if len(moved) > 0 {
			m.selectSession(m.indexOf(moved[0]))
		}
	}
	m.keepSelectionVisible()
	m.saveAll()
}

// groupEndIn is groupEnd over another list.
func (m *model) groupEndIn(list []*Session, name string) int {
	rank := m.groupRank(name)
	at := 0
	for i, s := range list {
		if m.groupRank(s.group()) <= rank {
			at = i + 1
		}
	}
	return at
}

func countIn(list []*Session, name string) int {
	n := 0
	for _, s := range list {
		if s.group() == name {
			n++
		}
	}
	return n
}

// showSelected selects the session m.sel, opening its group if it's
// collapsed: for a new session.
func (m *model) showSelected() {
	m.onHeader = false
	if m.sel >= 0 && m.sel < len(m.sessions) {
		if g := m.sessions[m.sel].group(); m.grouped() && m.collapsed(g) {
			m.setCollapsed(g, false)
			_ = m.saveGroups()
		}
	}
}

// defaultGroup is the group a new session from n joins: the selected
// session's, or the selected header's.
func (m *model) defaultGroup() string {
	if m.onHeader {
		return m.selHeader
	}
	if s := m.current(); s != nil {
		return s.group()
	}
	return ""
}

// Assigning a group (a) ---------------------------------------------------------

type assignBox struct {
	s     *Session
	input textinput.Model
	hi    int
}

type assignChoice struct {
	name   string // the group; "" with none
	create bool   // a new group called name
	none   bool   // "(no group)"
}

func (m *model) openAssign() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Placeholder = "group name"
	ti.SetWidth(30)
	m.assign = &assignBox{s: s, input: ti}
	return m.assign.input.Focus()
}

// assignChoices are the groups matching what's typed, then a new group
// with that name (unless there's one), then "(no group)".
func (m *model) assignChoices() []assignChoice {
	text := strings.TrimSpace(m.assign.input.Value())
	var out []assignChoice
	exact := false
	for _, g := range m.groups {
		if text == "" || matchesFilter(text, g.Name) {
			out = append(out, assignChoice{name: g.Name})
		}
		exact = exact || strings.EqualFold(g.Name, text)
	}
	if name, ok := validGroupName(text); ok && !exact {
		out = append(out, assignChoice{name: name, create: true})
	}
	return append(out, assignChoice{none: true})
}

func (m *model) assignKey(k tea.KeyPressMsg) tea.Cmd {
	a := m.assign
	choices := m.assignChoices()
	switch k.String() {
	case "esc":
		m.assign = nil
	case "up":
		a.hi = max(a.hi-1, 0)
	case "down", "tab":
		a.hi = min(a.hi+1, len(choices)-1)
	case "enter":
		m.assign = nil
		m.assignTo(a.s, choices[min(a.hi, len(choices)-1)])
	default:
		before := a.input.Value()
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(k)
		if a.input.Value() != before {
			a.hi = 0
		}
		return cmd
	}
	return nil
}

// assignTo moves a session to the end of a group (making it first if
// it's new), or to "(no group)". A collapsed group stays collapsed, and
// the selection goes to its header.
func (m *model) assignTo(s *Session, c assignChoice) {
	name := c.name
	if c.create {
		if m.addGroup(name, false) == nil {
			return
		}
	}
	if s.group() == name || m.indexOf(s) < 0 {
		return
	}
	m.place(s, name, nil, nil)
	if !m.grouped() {
		m.saveAll()
		return
	}
	label := name
	if c.none {
		label = noGroupName
	}
	m.setFlash(fmt.Sprintf("Moved %q to %s", s.Name, label))
	m.keepSelectionVisible()
	m.saveAll()
}

func (m *model) renderAssign(w int) []string {
	a := m.assign
	bw := min(max(36, ansi.StringWidth(a.s.Name)+24), w-2)
	inner := bw - 4
	title := fmt.Sprintf(" Move %q to group ", a.s.Name)
	top := "┌" + title + strings.Repeat("─", max(bw-2-ansi.StringWidth(title), 0)) + "┐"
	line := func(s string) string {
		return stBorder.Render("│") + " " + fit(s, inner) + " " + stBorder.Render("│")
	}
	out := []string{stBorder.Render(fitPlain(top, bw))}
	out = append(out, line(a.input.View()))
	for i, c := range m.assignChoices() {
		text := c.name
		switch {
		case c.none:
			text = noGroupName
		case c.create:
			text = fmt.Sprintf("+ new group %q", c.name)
		}
		if i == a.hi {
			text = lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("› " + text)
		} else {
			text = "  " + text
		}
		out = append(out, line(text))
	}
	out = append(out, stBorder.Render("└"+strings.Repeat("─", bw-2)+"┘"))
	return out
}

// fitPlain is a plain box border cut to w cells, keeping its last cell.
func fitPlain(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	r := []rune(s)
	last := string(r[len(r)-1])
	return ansi.Truncate(s, w-1, "") + last
}

// overlayAssign draws the assign box over the main pane's top left.
func (m *model) overlayAssign(behind []string, w int) []string {
	box := m.renderAssign(w)
	out := slices.Clone(behind)
	const left, top = 1, 1
	for i, l := range box {
		y := top + i
		if y >= len(out) {
			break
		}
		bw := ansi.StringWidth(l)
		out[y] = ansi.Cut(behind[y], 0, left) + "\x1b[0m" + l + "\x1b[0m" + ansi.Cut(behind[y], left+bw, w)
	}
	return out
}

// Drawing -------------------------------------------------------------------------

// rollup is a header's summary of its sessions: how many need you (the
// blinking ●), a spinner if any is working, and how many finished unread.
func (m *model) rollup(name string) string {
	waiting, working, unread := 0, false, 0
	for _, s := range m.sessions {
		if s.group() != name {
			continue
		}
		sn := s.Snapshot()
		switch {
		case sn.status == StatusWaiting:
			waiting++
		case sn.status == StatusWorking:
			working = true
		case sn.unread:
			unread++
		}
	}
	var parts []string
	if waiting > 0 {
		parts = append(parts, m.statusIcon(snapshot{status: StatusWaiting})+stWaitText.Render(fmt.Sprintf(" %d", waiting)))
	}
	if working {
		parts = append(parts, m.statusIcon(snapshot{status: StatusWorking}))
	}
	if unread > 0 {
		parts = append(parts, m.statusIcon(snapshot{status: StatusIdle, unread: true})+lipgloss.NewStyle().Foreground(colIdle).Bold(true).Render(fmt.Sprintf(" %d", unread)))
	}
	return strings.Join(parts, " ")
}

// renderHeader draws a group header: "▾ name  (3)", with the roll-up on the
// right. hot is a drop target: highlighted like the selection.
func (m *model) renderHeader(row listRow, w int, selected, hot bool) string {
	name := row.group
	label := lipgloss.NewStyle().Bold(true).Render(name)
	if name == "" {
		label = stMuted.Render(noGroupName)
	}
	arrow := "▾"
	if m.collapsed(name) && !m.filtering() && !row.temp {
		arrow = "▸"
	}
	count := stMuted.Render(fmt.Sprintf("  (%d)", m.groupCount(name)))
	if row.temp {
		count = stMuted.Render("  drop here")
	}
	left := stMuted.Render(arrow) + " " + label + count
	right := m.rollup(name)
	if right != "" {
		right += " "
	}
	if !selected && !hot {
		return fit(" "+fit(left, w-1-ansi.StringWidth(right))+right, w)
	}
	marker := lipgloss.NewStyle().Foreground(colAccent).Render("▌")
	if selected && !hot && m.focus == focusTerminal {
		marker = stMuted.Render("▌")
	}
	return marker + onBackground(fit(left, w-1-ansi.StringWidth(right))+right, selBgSeq)
}

// renderGroupPane is the main pane while a header is selected.
func (m *model) renderGroupPane(w, h int) []string {
	name := m.selHeader
	title := name
	if name == "" {
		title = noGroupName
	}
	n := m.groupCount(name)
	lines := []string{fit(" "+lipgloss.NewStyle().Bold(true).Render(title)+stMuted.Render(fmt.Sprintf("  %d session(s)", n))+"  "+m.rollup(name), w), ""}
	for _, s := range m.sessions {
		if s.group() == name {
			sn := s.Snapshot()
			d, _ := sessionDetail(sn)
			lines = append(lines, fit("   "+m.statusIcon(sn)+" "+s.Name+stMuted.Render("  "+s.Agent+" · "+d), w))
		}
	}
	if n == 0 {
		lines = append(lines, fit(stMuted.Render("   No sessions. Drag one here, or press a on a session."), w))
	}
	hint := "   space/enter fold · ⇧↑↓ move the group · r rename · x delete the group (its sessions stay)"
	if name == "" {
		hint = "   space/enter fold"
	}
	lines = append(lines, "", fit(stMuted.Render(hint), w))
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:h]
}
