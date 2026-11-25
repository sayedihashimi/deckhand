package main

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

// The clipboard, for ctrl+c, ctrl+x and ctrl+v in the pad. Tests replace
// them.
var (
	padClipboardWrite = func(s string) { _ = clipboard.WriteAll(s) }
	padClipboardRead  = clipboard.ReadAll
)

// padEditor is the scratch pad's text editor (pad.go): a multi-line editor
// with word wrap, a selection, undo and redo, and mouse positions.
//
// It isn't bubbles' textarea: that has no undo, no selection and no mouse
// support, and its cursor, wrapping and scroll state are private, so a
// selection couldn't be drawn or a click placed without copying it. The
// pad's text is small, so the editor keeps it as one slice of runes and
// works out the wrapped lines when it needs them.
type padEditor struct {
	text   []rune
	cur    int // the cursor, an index into text
	anchor int // the other end of the selection, or -1 for none
	goal   int // the column ↑ and ↓ aim for, or -1 to use the cursor's

	width, height int // the text area, in cells
	top           int // the first wrapped line shown

	undo, redo []padSnap
	// What the last change was, so typing a word is one undo step: more
	// typing (or deleting) right where it left off joins it.
	group    padGroup
	groupEnd int  // where the cursor was after it
	lastRune rune // the last character typed

	version int // goes up on every change to the text
}

type padSnap struct {
	text        []rune
	cur, anchor int
}

type padGroup int

const (
	groupNone padGroup = iota
	groupType
	groupDelete
)

const maxPadUndo = 500

func newPadEditor(text string) *padEditor {
	e := &padEditor{anchor: -1, goal: -1, width: 40, height: 5}
	e.text = []rune(cleanPadText(text))
	return e
}

// cleanPadText makes text fit for the editor: \n line ends, and no control
// characters but tabs.
func cleanPadText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
}

func (e *padEditor) Value() string { return string(e.text) }

func (e *padEditor) setSize(w, h int) {
	e.width, e.height = max(w, 2), max(h, 1)
}

// setValue replaces the text, as an undoable change (a reload from the
// external editor). The cursor stays where it was, or as near as it can.
func (e *padEditor) setValue(s string) {
	s = cleanPadText(s)
	if s == string(e.text) {
		return
	}
	e.save()
	e.text = []rune(s)
	e.cur = min(e.cur, len(e.text))
	e.anchor = -1
	e.changed()
}

// Undo -----------------------------------------------------------------------

func (e *padEditor) snapshot() padSnap {
	return padSnap{append([]rune(nil), e.text...), e.cur, e.anchor}
}

// save records the text before a change that starts a new undo step.
func (e *padEditor) save() {
	e.undo = append(e.undo, e.snapshot())
	if len(e.undo) > maxPadUndo {
		e.undo = e.undo[len(e.undo)-maxPadUndo:]
	}
	e.redo = nil
	e.group = groupNone
}

func (e *padEditor) changed() {
	e.version++
	e.goal = -1
}

// endGroup stops the next change joining the last one's undo step.
func (e *padEditor) endGroup() { e.group = groupNone }

func (e *padEditor) restore(st padSnap) {
	e.text, e.cur, e.anchor = st.text, st.cur, st.anchor
	e.group = groupNone
	e.changed()
}

// Undo undoes the last step; false when there's nothing to undo.
func (e *padEditor) Undo() bool {
	if len(e.undo) == 0 {
		return false
	}
	e.redo = append(e.redo, e.snapshot())
	st := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.restore(st)
	return true
}

// Redo redoes the last undone step; false when there's nothing to redo.
func (e *padEditor) Redo() bool {
	if len(e.redo) == 0 {
		return false
	}
	e.undo = append(e.undo, e.snapshot())
	st := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.restore(st)
	return true
}

// Selection ----------------------------------------------------------------

// selection is the selected range [a, b), if there is one.
func (e *padEditor) selection() (a, b int, ok bool) {
	if e.anchor < 0 || e.anchor == e.cur {
		return 0, 0, false
	}
	return min(e.anchor, e.cur), max(e.anchor, e.cur), true
}

func (e *padEditor) selectedText() string {
	a, b, ok := e.selection()
	if !ok {
		return ""
	}
	return string(e.text[a:b])
}

func (e *padEditor) selectAll() {
	e.anchor, e.cur = 0, len(e.text)
	e.endGroup()
}

// selectRange selects [a, b), with the cursor at b.
func (e *padEditor) selectRange(a, b int) {
	e.anchor, e.cur = clampInt(a, 0, len(e.text)), clampInt(b, 0, len(e.text))
	e.goal = -1
	e.endGroup()
}

// wordAt is the word (or run of spaces, or punctuation) around index i.
func (e *padEditor) wordAt(i int) (a, b int) {
	if len(e.text) == 0 {
		return 0, 0
	}
	i = clampInt(i, 0, len(e.text)-1)
	if e.text[i] == '\n' && i > 0 {
		i-- // the end of a line: the word before it
	}
	class := runeClass(e.text[i])
	a, b = i, i+1
	for a > 0 && runeClass(e.text[a-1]) == class && e.text[a-1] != '\n' {
		a--
	}
	for b < len(e.text) && runeClass(e.text[b]) == class && e.text[b] != '\n' {
		b++
	}
	return a, b
}

// runeClass groups characters for word moves: 0 spaces, 1 word characters,
// 2 the rest (punctuation).
func runeClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
		return 1
	}
	return 2
}

// Editing --------------------------------------------------------------------

// deleteSelection removes the selected text; false when there's none.
func (e *padEditor) deleteSelection() bool {
	a, b, ok := e.selection()
	if !ok {
		e.anchor = -1
		return false
	}
	e.text = append(e.text[:a:a], e.text[b:]...)
	e.cur, e.anchor = a, -1
	return true
}

// insert puts s at the cursor, replacing the selection. typed is one
// character typed: it joins the undo step of the typing before it, until
// a new word starts.
func (e *padEditor) insert(s string, typed bool) {
	s = cleanPadText(s)
	if s == "" && !e.hasSelection() {
		return
	}
	rs := []rune(s)
	_, _, sel := e.selection()
	joins := typed && !sel && len(rs) == 1 && e.group == groupType && e.cur == e.groupEnd &&
		!(unicode.IsSpace(e.lastRune) && !unicode.IsSpace(rs[0]))
	if !joins {
		e.save()
	}
	e.deleteSelection()
	e.text = append(e.text[:e.cur:e.cur], append(rs, e.text[e.cur:]...)...)
	e.cur += len(rs)
	e.changed()
	if typed && len(rs) == 1 {
		e.group, e.groupEnd, e.lastRune = groupType, e.cur, rs[0]
	}
}

func (e *padEditor) hasSelection() bool {
	_, _, ok := e.selection()
	return ok
}

// deleteTo deletes from the cursor to i (either side of it), or the
// selection if there is one. Single deletes in a row are one undo step.
func (e *padEditor) deleteTo(i int, single bool) {
	if e.hasSelection() {
		e.save()
		e.deleteSelection()
		e.changed()
		return
	}
	e.anchor = -1
	i = clampInt(i, 0, len(e.text))
	if i == e.cur {
		return
	}
	if !(single && e.group == groupDelete && e.cur == e.groupEnd) {
		e.save()
	}
	a, b := min(i, e.cur), max(i, e.cur)
	e.text = append(e.text[:a:a], e.text[b:]...)
	e.cur = a
	e.changed()
	if single {
		e.group, e.groupEnd = groupDelete, e.cur
	}
}

// cut removes the selection and returns it.
func (e *padEditor) cut() string {
	s := e.selectedText()
	if s != "" {
		e.save()
		e.deleteSelection()
		e.changed()
	}
	return s
}

// Moving ---------------------------------------------------------------------

// moveTo puts the cursor at i, extending the selection with sel, otherwise
// clearing it.
func (e *padEditor) moveTo(i int, sel bool) {
	i = clampInt(i, 0, len(e.text))
	switch {
	case sel && e.anchor < 0:
		e.anchor = e.cur
	case !sel:
		e.anchor = -1
	}
	e.cur = i
	e.endGroup()
}

func (e *padEditor) wordLeft(i int) int {
	for i > 0 && unicode.IsSpace(e.text[i-1]) {
		i--
	}
	if i == 0 {
		return 0
	}
	class := runeClass(e.text[i-1])
	for i > 0 && runeClass(e.text[i-1]) == class {
		i--
	}
	return i
}

func (e *padEditor) wordRight(i int) int {
	n := len(e.text)
	if i < n && !unicode.IsSpace(e.text[i]) {
		class := runeClass(e.text[i])
		for i < n && runeClass(e.text[i]) == class {
			i++
		}
	}
	for i < n && unicode.IsSpace(e.text[i]) && e.text[i] != '\n' {
		i++
	}
	if i < n && i == e.cur && e.text[i] == '\n' {
		i++
	}
	return i
}

// lineStart and lineEnd are the ends of the text line (not the wrapped
// line) around i.
func (e *padEditor) lineStart(i int) int {
	for i > 0 && e.text[i-1] != '\n' {
		i--
	}
	return i
}

func (e *padEditor) lineEnd(i int) int {
	for i < len(e.text) && e.text[i] != '\n' {
		i++
	}
	return i
}

// vertical moves the cursor dy wrapped lines, keeping its column.
func (e *padEditor) vertical(dy int, sel bool) {
	lines := e.lines()
	li := lineOf(lines, e.cur)
	goal := e.goal
	if goal < 0 {
		goal = e.colOf(lines[li], e.cur)
	}
	to := li + dy
	var i int
	switch {
	case to < 0:
		i = 0
	case to >= len(lines):
		i = len(e.text)
	default:
		i = e.indexAtCol(lines, to, goal)
	}
	e.moveTo(i, sel)
	e.goal = goal
}

// editKey handles an editing or moving key; false if it isn't one. Copy,
// cut and paste use the clipboard (padClipboard).
func (e *padEditor) editKey(k tea.KeyPressMsg) bool {
	key := k.String()
	sel := strings.Contains(key, "shift+")
	move := strings.Replace(key, "shift+", "", 1)
	switch move {
	case "up", "down", "pgup", "pgdown":
	default:
		e.goal = -1 // only ↑↓ keep aiming for the same column
	}
	switch move {
	case "left":
		if a, _, ok := e.selection(); ok && !sel {
			e.moveTo(a, false)
			return true
		}
		e.moveTo(e.cur-1, sel)
	case "right":
		if _, b, ok := e.selection(); ok && !sel {
			e.moveTo(b, false)
			return true
		}
		e.moveTo(e.cur+1, sel)
	case "ctrl+left":
		e.moveTo(e.wordLeft(e.cur), sel)
	case "ctrl+right":
		e.moveTo(e.wordRight(e.cur), sel)
	case "up":
		e.vertical(-1, sel)
	case "down":
		e.vertical(1, sel)
	case "pgup":
		e.vertical(-max(e.height-1, 1), sel)
	case "pgdown":
		e.vertical(max(e.height-1, 1), sel)
	case "home":
		e.moveTo(e.lineStart(e.cur), sel)
	case "end":
		e.moveTo(e.lineEnd(e.cur), sel)
	case "ctrl+home":
		e.moveTo(0, sel)
	case "ctrl+end":
		e.moveTo(len(e.text), sel)
	default:
		return e.changeKey(k)
	}
	return true
}

// changeKey handles the keys that change the text or the clipboard.
func (e *padEditor) changeKey(k tea.KeyPressMsg) bool {
	switch key := k.String(); key {
	case "enter", "shift+enter":
		e.insert("\n", true)
	case "tab":
		e.insert("    ", false)
	case "backspace", "shift+backspace":
		e.deleteTo(e.cur-1, true)
	case "delete":
		e.deleteTo(e.cur+1, true)
	case "ctrl+backspace", "alt+backspace", "ctrl+w", "ctrl+h": // ctrl+h: what terminals send for ctrl+backspace
		e.deleteTo(e.wordLeft(e.cur), false)
	case "ctrl+delete", "alt+delete":
		e.deleteTo(e.wordRight(e.cur), false)
	case "ctrl+a":
		e.selectAll()
	case "ctrl+z":
		e.Undo()
	case "ctrl+y", "ctrl+shift+z":
		e.Redo()
	case "ctrl+c", "ctrl+insert":
		if s := e.selectedText(); s != "" {
			padClipboardWrite(s)
		}
	case "ctrl+x", "shift+delete":
		if s := e.cut(); s != "" {
			padClipboardWrite(s)
		}
	case "ctrl+v", "shift+insert":
		if s, err := padClipboardRead(); err == nil && s != "" {
			e.insert(s, false)
		}
	default:
		if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
			return false
		}
		for _, r := range k.Text {
			e.insert(string(r), true)
		}
	}
	return true
}

// Wrapping and drawing -----------------------------------------------------

// padLine is one wrapped line: text[start:end], without its \n.
type padLine struct{ start, end int }

func padRuneWidth(r rune) int {
	if r == '\t' {
		return 1
	}
	return ansi.StringWidth(string(r))
}

// lines wraps the text to the width, at spaces where it can. One column is
// kept free for the cursor at the end of a full line.
func (e *padEditor) lines() []padLine {
	w := max(e.width-1, 1)
	var out []padLine
	start := 0
	for {
		end := start
		for end < len(e.text) && e.text[end] != '\n' {
			end++
		}
		out = append(out, wrapLine(e.text, start, end, w)...)
		if end >= len(e.text) {
			return out
		}
		start = end + 1
	}
}

// wrapLine wraps text[start:end], a line without \n, to w cells.
func wrapLine(text []rune, start, end, w int) []padLine {
	if start == end {
		return []padLine{{start, end}}
	}
	var out []padLine
	for start < end {
		col, i, space := 0, start, -1
		for i < end {
			rw := padRuneWidth(text[i])
			if col+rw > w && i > start {
				break
			}
			if unicode.IsSpace(text[i]) {
				space = i
			}
			col += rw
			i++
		}
		brk := i
		if i < end && (space >= start || unicode.IsSpace(text[i])) {
			if unicode.IsSpace(text[i]) {
				// The line ends at a space: break after the spaces.
				for i < end && unicode.IsSpace(text[i]) {
					i++
				}
				brk = i
			} else {
				brk = space + 1
			}
		}
		if brk >= end {
			out = append(out, padLine{start, end})
			break
		}
		out = append(out, padLine{start, brk})
		start = brk
	}
	return out
}

// lineOf is the wrapped line the cursor at i shows on: at a wrap, the next
// line's start.
func lineOf(lines []padLine, i int) int {
	li := 0
	for j, l := range lines {
		if l.start <= i {
			li = j
		}
	}
	return li
}

func (e *padEditor) colOf(l padLine, i int) int {
	col := 0
	for j := l.start; j < min(i, l.end); j++ {
		col += padRuneWidth(e.text[j])
	}
	return col
}

// indexAtCol is the index on wrapped line li nearest column col. On a line
// that wraps, it stays before the wrap.
func (e *padEditor) indexAtCol(lines []padLine, li, col int) int {
	l := lines[li]
	last := l.end
	if li+1 < len(lines) && lines[li+1].start == l.end && l.end > l.start {
		last = l.end - 1 // a soft wrap: l.end is the next line's start
	}
	c := 0
	for i := l.start; i < last; i++ {
		rw := padRuneWidth(e.text[i])
		if c+rw > col {
			if col-c >= (rw+1)/2 && i+1 <= last {
				return i + 1
			}
			return i
		}
		c += rw
	}
	return last
}

// cursorPos is the cursor's place in the text area, scrolling it into view.
func (e *padEditor) cursorPos() (x, y int) {
	lines := e.lines()
	li := lineOf(lines, e.cur)
	e.scrollTo(li, len(lines))
	// Spaces at a wrap can run past the edge; keep the cursor in the pane.
	return min(e.colOf(lines[li], e.cur), e.width-1), li - e.top
}

func (e *padEditor) scrollTo(li, n int) {
	if li < e.top {
		e.top = li
	}
	if li >= e.top+e.height {
		e.top = li - e.height + 1
	}
	e.top = clampInt(e.top, 0, max(n-e.height, 0))
}

// scroll moves the view dy lines without moving the cursor.
func (e *padEditor) scroll(dy int) {
	n := len(e.lines())
	e.top = clampInt(e.top+dy, 0, max(n-e.height, 0))
}

// indexAt is the text index at a place in the text area (a click).
func (e *padEditor) indexAt(x, y int) int {
	lines := e.lines()
	li := e.top + y
	switch {
	case li < 0:
		return 0
	case li >= len(lines):
		return len(e.text)
	}
	return e.indexAtCol(lines, li, max(x, 0))
}

var stPadSel = "\x1b[48;2;61;89;161m" // the selection's background

// view draws the text area: height lines of width cells, with the
// selection highlighted. It doesn't scroll to the cursor (cursorPos does).
func (e *padEditor) view() []string {
	lines := e.lines()
	a, b, sel := e.selection()
	e.top = clampInt(e.top, 0, max(len(lines)-1, 0))
	out := make([]string, 0, e.height)
	for li := e.top; li < len(lines) && len(out) < e.height; li++ {
		l := lines[li]
		var sb strings.Builder
		in := false
		col := 0
		for i := l.start; i < l.end; i++ {
			on := sel && i >= a && i < b
			if on != in {
				if on {
					sb.WriteString(stPadSel)
				} else {
					sb.WriteString("\x1b[m")
				}
				in = on
			}
			r := e.text[i]
			if r == '\t' {
				r = ' '
			}
			if col+padRuneWidth(r) > e.width {
				break
			}
			col += padRuneWidth(r)
			sb.WriteRune(r)
		}
		// A selected line end shows as one selected cell.
		if sel && l.end < len(e.text) && e.text[l.end] == '\n' && l.end >= a && l.end < b && col < e.width {
			if !in {
				sb.WriteString(stPadSel)
				in = true
			}
			sb.WriteByte(' ')
		}
		if in {
			sb.WriteString("\x1b[m")
		}
		out = append(out, fit(sb.String(), e.width))
	}
	for len(out) < e.height {
		out = append(out, strings.Repeat(" ", e.width))
	}
	return out
}

func clampInt(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
