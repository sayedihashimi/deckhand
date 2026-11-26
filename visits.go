package main

import tea "charm.land/bubbletea/v2"

// Going back and forward through the sessions you've visited, like a
// browser's history (issue #75). Every change of the selected session is a
// visit, however it happens: arrows, a click, a jump, a new session or a
// fork. A group header isn't a visit, and selecting the same session again
// adds nothing. The history is kept in memory only; a restart starts empty.
//
// F7 goes back and F8 goes forward ([list] history_back_key and
// history_forward_key), in the list and inside a session, where deckhand
// catches them like F5 and F6. alt+← and alt+→ also work in the list; inside
// a session they're left to the agent (they move by word in Claude's
// input). The mouse's side buttons do the same anywhere in the window, in
// terminals that send them (Windows Terminal and WezTerm don't).

// maxVisits is how many visits the history keeps; the oldest go first.
const maxVisits = 100

// visitHistory is the list of visited sessions and where you are in it.
type visitHistory struct {
	entries []*Session
	pos     int // the current entry, when there are any
}

// visit records s as the current session. Going somewhere new after going
// back drops the entries ahead, as in a browser.
func (h *visitHistory) visit(s *Session) {
	if s == nil {
		return
	}
	if len(h.entries) > 0 {
		if h.entries[h.pos] == s {
			return
		}
		h.entries = h.entries[:h.pos+1]
	}
	h.entries = append(h.entries, s)
	if over := len(h.entries) - maxVisits; over > 0 {
		h.entries = append(h.entries[:0], h.entries[over:]...)
	}
	h.pos = len(h.entries) - 1
}

// prune drops the sessions that aren't alive any more (removed with x),
// then merges entries for the same session that end up side by side. The
// current entry stays put, or moves to the nearest kept one before it.
func (h *visitHistory) prune(alive func(*Session) bool) {
	kept := h.entries[:0]
	pos := 0
	for i, s := range h.entries {
		if alive(s) && (len(kept) == 0 || kept[len(kept)-1] != s) {
			kept = append(kept, s)
		}
		if i <= h.pos && len(kept) > 0 {
			pos = len(kept) - 1
		}
	}
	clear(h.entries[len(kept):])
	h.entries, h.pos = kept, pos
}

// step moves back (dir < 0) or forward through the history to the nearest
// entry that can be shown (ok), skipping the current session cur. It
// reports the session to select, or false at either end. Going back from
// somewhere that isn't the current entry (a group header) returns to the
// current entry first.
func (h *visitHistory) step(dir int, cur *Session, ok func(*Session) bool) (*Session, bool) {
	if len(h.entries) == 0 {
		return nil, false
	}
	if s := h.entries[h.pos]; dir < 0 && s != cur && ok(s) {
		return s, true
	}
	for i := h.pos + dir; i >= 0 && i < len(h.entries); i += dir {
		if s := h.entries[i]; s != cur && ok(s) {
			h.pos = i
			return s, true
		}
	}
	return nil, false
}

// noteVisit adds the selected session to the history, if it changed. It
// runs after every message (Update).
func (m *model) noteVisit() {
	m.pruneVisits()
	m.visits.visit(m.current())
}

// pruneVisits drops removed sessions from the history.
func (m *model) pruneVisits() {
	if len(m.visits.entries) == 0 {
		return
	}
	alive := make(map[*Session]bool, len(m.sessions))
	for _, s := range m.sessions {
		alive[s] = true
	}
	m.visits.prune(func(s *Session) bool { return alive[s] })
}

// historyMove goes back (dir < 0) or forward to a visited session. Sessions
// a filter hides are skipped, and the filter stays; a session in a collapsed
// group opens its group. In the list the selection moves; inside a session
// (or its pad) you land in the other session's terminal, or in the list
// when that session isn't running.
func (m *model) historyMove(dir int) tea.Cmd {
	m.noteVisit()
	cur := m.current()
	target, ok := m.visits.step(dir, cur, func(s *Session) bool {
		i := m.indexOf(s)
		return i >= 0 && (!m.filtering() || visiblePos(m.visible(), i) >= 0)
	})
	if !ok {
		if dir < 0 {
			m.setFlash("No earlier session")
		} else {
			m.setFlash("No later session")
		}
		return nil
	}
	m.flash = ""
	m.cancelDrag()
	m.selectSession(m.indexOf(target))
	m.showSelected()
	if m.focus == focusTerminal || m.focus == focusPad {
		if cur != nil && m.focus == focusPad {
			m.padLeave(cur) // saves its notes
		}
		m.focus = focusSidebar
		if target.Running() {
			m.focus = focusTerminal
		}
	}
	return nil
}

// historyKey handles the history keys, in the list and inside a session:
// F7 and F8 by default ([list] history_back_key and history_forward_key).
func (m *model) historyKey(key string) (tea.Cmd, bool) {
	if m.historyBlocked() {
		return nil, false
	}
	switch key {
	case listOpts.historyBack():
		return m.historyMove(-1), true
	case listOpts.historyForward():
		return m.historyMove(1), true
	}
	return nil, false
}

// historyBlocked reports whether something has the keyboard and mouse that
// the history mustn't move under: the help, a prompt, a picker, the diff,
// the new-session dialog or the config screen.
func (m *model) historyBlocked() bool {
	return m.help != nil || m.diff != nil || m.confirm != nil || m.closing != nil || m.menu != nil ||
		m.rename != nil || m.filterInput != nil || m.assign != nil || m.folderPick != nil || m.colorPick != nil ||
		(m.focus != focusSidebar && m.focus != focusTerminal && m.focus != focusPad)
}

// historyButton maps the mouse's side buttons to a history move: -1 for
// back, 1 for forward, 0 for any other button.
func historyButton(b tea.MouseButton) int {
	switch b {
	case tea.MouseBackward:
		return -1
	case tea.MouseForward:
		return 1
	}
	return 0
}
