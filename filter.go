package main

import (
	"path/filepath"
	"slices"
	"strings"
)

// Filtering the session list: "/" filters by text, "@" shows only the
// sessions that need you or have finished since you last looked.

// matchesFilter reports whether every word of the filter matches the
// session's name, agent, folder or git branch (ignoring case, letters in
// order count as a match, as in the folder picker).
func matchesFilter(filter string, fields ...string) bool {
	for _, word := range strings.Fields(filter) {
		found := false
		for _, f := range fields {
			if f != "" && matchScore(f, word) > 0 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// filterFields are the texts a session is matched on.
func filterFields(s *Session) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []string{s.Name, s.Agent, filepath.Base(s.Dir), s.Dir, s.info.git.branch}
}

// visible returns the indexes of the sessions shown in the list. With "@",
// the selected session stays in the list even once it's been seen, so it
// doesn't vanish as soon as you select it.
func (m *model) visible() []int {
	out := make([]int, 0, len(m.sessions))
	for i, s := range m.sessions {
		if m.attentionOnly && i != m.selectedIdx() {
			sn := s.Snapshot()
			if !(sn.status == StatusWaiting || sn.unread) {
				continue
			}
		}
		if m.filter != "" && !matchesFilter(m.filter, filterFields(s)...) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func (m *model) filtering() bool { return m.filter != "" || m.attentionOnly }

// visiblePos is the position of the selection in the visible list, or -1.
func visiblePos(vis []int, sel int) int {
	for p, i := range vis {
		if i == sel {
			return p
		}
	}
	return -1
}

// moveSel moves the selection by delta rows: sessions, and group headers
// when there are groups.
func (m *model) moveSel(delta int) {
	rows := m.rows()
	if len(rows) == 0 {
		return
	}
	r := m.selRow(rows)
	switch {
	case r < 0:
		r = 0
	default:
		r += delta
	}
	r = min(max(r, 0), len(rows)-1)
	m.selectRow(rows[r])
}

// selectRowAt selects the r'th row (negative counts from the end).
func (m *model) selectRowAt(r int) bool {
	rows := m.rows()
	if r < 0 {
		r += len(rows)
	}
	if r < 0 || r >= len(rows) || rows[r].temp {
		return false
	}
	m.selectRow(rows[r])
	return true
}

// selectVisible selects the p'th session row shown (negative counts from
// the end); headers and sessions in collapsed groups don't count. These
// are the numbers 1-9 in the list.
func (m *model) selectVisible(p int) bool {
	var shown []int
	for _, row := range m.rows() {
		if row.kind == rowSession {
			shown = append(shown, row.idx)
		}
	}
	if p < 0 {
		p += len(shown)
	}
	if p < 0 || p >= len(shown) {
		return false
	}
	m.selectSession(shown[p])
	return true
}

// moveSession moves the selected session one place up (delta < 0) or down
// in the visible list: past the next visible session in that direction,
// landing just before or after it. Hidden sessions in between are skipped,
// so with a filter on it never moves past one you can't see. The selection
// follows the session. It reports whether anything moved (nothing does at
// either end). With groups, see moveSessionGrouped; on a header it moves
// the group.
func (m *model) moveSession(delta int) bool {
	if m.onHeader {
		return m.moveGroup(delta, false)
	}
	if m.grouped() {
		return m.moveSessionGrouped(delta)
	}
	vis := m.visible()
	p := visiblePos(vis, m.sel)
	if p < 0 || delta == 0 {
		return false
	}
	q := p + 1
	if delta < 0 {
		q = p - 1
	}
	if q < 0 || q >= len(vis) {
		return false
	}
	m.moveSelectedTo(vis[q])
	return true
}

// moveSessionToEnd moves the selected session to just before the first
// visible session (top) or just after the last one. Hidden sessions keep
// their places relative to each other.
func (m *model) moveSessionToEnd(top bool) bool {
	if m.onHeader {
		return m.moveGroup(map[bool]int{true: -1, false: 1}[top], true)
	}
	if m.grouped() {
		return m.moveSessionInGroup(top)
	}
	vis := m.visible()
	if visiblePos(vis, m.sel) < 0 {
		return false
	}
	target := vis[len(vis)-1]
	if top {
		target = vis[0]
	}
	if target == m.sel {
		return false
	}
	m.moveSelectedTo(target)
	return true
}

// moveSelectedTo moves the selected session next to the session at index
// target: before it when it's above the selection, after it when it's
// below. Either way the session ends up at index target, which becomes the
// selection.
func (m *model) moveSelectedTo(target int) {
	s := m.sessions[m.sel]
	m.sessions = slices.Delete(m.sessions, m.sel, m.sel+1)
	m.sessions = slices.Insert(m.sessions, target, s)
	m.sel = target
}

// keepSelectionVisible moves the selection to the first session shown if
// the filter hid it, or to its group's header if its group is collapsed.
func (m *model) keepSelectionVisible() {
	rows := m.rows()
	if m.selRow(rows) >= 0 {
		return
	}
	if !m.onHeader && m.sel >= 0 && m.sel < len(m.sessions) && m.grouped() {
		if g := m.sessions[m.sel].group(); headerIndex(rows, g) >= 0 && m.collapsed(g) && !m.filtering() {
			m.onHeader, m.selHeader = true, g
			return
		}
	}
	for _, row := range rows {
		if row.kind == rowSession {
			m.selectSession(row.idx)
			return
		}
	}
	if len(rows) > 0 && !rows[0].temp {
		m.selectRow(rows[0])
	} else {
		m.onHeader = false
	}
}

func (m *model) clearFilter() {
	m.filter = ""
	m.attentionOnly = false
	m.filterInput = nil
}
