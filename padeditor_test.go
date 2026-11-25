package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// typeText types s into the editor a character at a time, as keys.
func edType(e *padEditor, s string) {
	for _, r := range s {
		if r == '\n' {
			e.editKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			continue
		}
		e.editKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func padKeyMsg(code rune, mod uv.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func TestPadEditorUndoGroupsWords(t *testing.T) {
	e := newPadEditor("")
	edType(e, "ask about retries")
	if e.Value() != "ask about retries" {
		t.Fatalf("typed %q", e.Value())
	}
	ctrlZ := padKeyMsg('z', tea.ModCtrl)
	for _, want := range []string{"ask about ", "ask ", ""} {
		e.editKey(ctrlZ)
		if e.Value() != want {
			t.Fatalf("undo: %q, want %q", e.Value(), want)
		}
	}
	if e.Undo() {
		t.Error("nothing left to undo")
	}
	ctrlY := padKeyMsg('y', tea.ModCtrl)
	for _, want := range []string{"ask ", "ask about ", "ask about retries"} {
		e.editKey(ctrlY)
		if e.Value() != want {
			t.Fatalf("redo: %q, want %q", e.Value(), want)
		}
	}
	if e.cur != len(e.text) {
		t.Errorf("cursor %d after redo, want the end", e.cur)
	}

	// Backspaces in a row are one step; typing after an undo drops the redo.
	e.editKey(padKeyMsg(tea.KeyBackspace, 0))
	e.editKey(padKeyMsg(tea.KeyBackspace, 0))
	if e.Value() != "ask about retri" {
		t.Fatalf("backspace: %q", e.Value())
	}
	e.editKey(ctrlZ)
	if e.Value() != "ask about retries" {
		t.Fatalf("undo backspaces: %q", e.Value())
	}
	edType(e, "!")
	if e.Redo() {
		t.Error("typing should clear the redo stack")
	}

	// Moving the cursor ends the step, even mid-word.
	e = newPadEditor("")
	edType(e, "ab")
	e.editKey(padKeyMsg(tea.KeyLeft, 0))
	edType(e, "x")
	e.Undo()
	if e.Value() != "ab" {
		t.Errorf("undo after a move: %q, want ab", e.Value())
	}
}

func TestPadEditorKeyboardSelection(t *testing.T) {
	e := newPadEditor("one two\nthree")
	e.cur = 0
	shiftRight := padKeyMsg(tea.KeyRight, tea.ModShift)
	e.editKey(shiftRight)
	e.editKey(shiftRight)
	e.editKey(shiftRight)
	if got := e.selectedText(); got != "one" {
		t.Fatalf("shift+right ×3: %q", got)
	}
	e.editKey(padKeyMsg(tea.KeyRight, tea.ModCtrl|tea.ModShift))
	if got := e.selectedText(); got != "one " { // to the next word's start
		t.Fatalf("ctrl+shift+right: %q", got)
	}
	e.editKey(padKeyMsg(tea.KeyRight, tea.ModCtrl|tea.ModShift))
	if got := e.selectedText(); got != "one two" {
		t.Fatalf("ctrl+shift+right: %q", got)
	}
	e.editKey(padKeyMsg(tea.KeyDown, tea.ModShift))
	if got := e.selectedText(); got != "one two\nthree" {
		t.Fatalf("shift+down: %q", got)
	}
	e.editKey(padKeyMsg(tea.KeyHome, tea.ModCtrl|tea.ModShift))
	if got := e.selectedText(); got != "" || e.cur != 0 {
		t.Fatalf("ctrl+shift+home back to the anchor: %q at %d", got, e.cur)
	}
	e.editKey(padKeyMsg(tea.KeyEnd, tea.ModShift))
	if got := e.selectedText(); got != "one two" {
		t.Fatalf("shift+end: %q", got)
	}
	// Typing replaces the selection; ← without shift collapses it.
	edType(e, "X")
	if e.Value() != "X\nthree" {
		t.Fatalf("typing over the selection: %q", e.Value())
	}
	e.selectAll()
	e.editKey(padKeyMsg(tea.KeyLeft, 0))
	if e.hasSelection() || e.cur != 0 {
		t.Errorf("← should collapse the selection to its start, cur %d", e.cur)
	}
	// ctrl+a, then delete.
	e.editKey(padKeyMsg('a', tea.ModCtrl))
	e.editKey(padKeyMsg(tea.KeyDelete, 0))
	if e.Value() != "" {
		t.Errorf("ctrl+a, delete: %q", e.Value())
	}
}

func TestPadEditorWrap(t *testing.T) {
	e := newPadEditor("ask about retries then run the tests")
	e.setSize(11, 5) // 10 columns of text, one for the cursor
	var got []string
	for _, l := range e.lines() {
		got = append(got, string(e.text[l.start:l.end]))
	}
	want := []string{"ask about ", "retries ", "then run ", "the tests"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapped %q, want %q", got, want)
	}
	// A word longer than the line is cut.
	e = newPadEditor("abcdefghijklmnop")
	e.setSize(6, 5)
	got = nil
	for _, l := range e.lines() {
		got = append(got, string(e.text[l.start:l.end]))
	}
	if strings.Join(got, "|") != "abcde|fghij|klmno|p" {
		t.Fatalf("long word: %q", got)
	}
	// The view is exactly the area, and ↓ keeps the column.
	for _, l := range e.view() {
		if ansi.StringWidth(l) != 6 {
			t.Errorf("view line %q is %d wide", l, ansi.StringWidth(l))
		}
	}
	e.cur = 2
	e.editKey(padKeyMsg(tea.KeyDown, 0))
	if e.cur != 7 {
		t.Errorf("↓ from column 2: index %d, want 7", e.cur)
	}
	if x, y := e.cursorPos(); x != 2 || y != 1 {
		t.Errorf("cursor at %d,%d, want 2,1", x, y)
	}
	// Scrolling: a small area follows the cursor.
	e.setSize(6, 2)
	e.editKey(padKeyMsg(tea.KeyEnd, tea.ModCtrl))
	if _, y := e.cursorPos(); y != 1 || e.top != 2 {
		t.Errorf("at the end: cursor row %d, top %d", y, e.top)
	}
}

func TestPadEditorMouseSelection(t *testing.T) {
	e := newPadEditor("ask about retries\nthen run tests")
	e.setSize(30, 5)
	if i := e.indexAt(4, 0); i != 4 {
		t.Errorf("click at 4,0: %d", i)
	}
	if i := e.indexAt(50, 0); i != 17 {
		t.Errorf("click past the line's end: %d, want its end", i)
	}
	if i := e.indexAt(3, 1); i != 21 {
		t.Errorf("click at 3,1: %d", i)
	}
	if i := e.indexAt(0, 9); i != len(e.text) {
		t.Errorf("click below the text: %d", i)
	}
	a, b := e.wordAt(6)
	if string(e.text[a:b]) != "about" {
		t.Errorf("word at 6: %q", string(e.text[a:b]))
	}
	a, b = e.wordAt(17) // the line's end: the word before it
	if string(e.text[a:b]) != "retries" {
		t.Errorf("word at a line end: %q", string(e.text[a:b]))
	}
}

func TestPadEditorClipboard(t *testing.T) {
	clip := ""
	oldW, oldR := padClipboardWrite, padClipboardRead
	padClipboardWrite = func(s string) { clip = s }
	padClipboardRead = func() (string, error) { return clip, nil }
	t.Cleanup(func() { padClipboardWrite, padClipboardRead = oldW, oldR })

	e := newPadEditor("one two three")
	e.selectRange(4, 7)
	e.editKey(padKeyMsg('c', tea.ModCtrl))
	if clip != "two" || e.Value() != "one two three" {
		t.Fatalf("copy: clipboard %q, text %q", clip, e.Value())
	}
	e.selectRange(0, 4)
	e.editKey(padKeyMsg('x', tea.ModCtrl))
	if clip != "one " || e.Value() != "two three" {
		t.Fatalf("cut: clipboard %q, text %q", clip, e.Value())
	}
	e.editKey(padKeyMsg(tea.KeyEnd, tea.ModCtrl))
	e.editKey(padKeyMsg('v', tea.ModCtrl))
	if e.Value() != "two threeone " {
		t.Fatalf("paste: %q", e.Value())
	}
	e.Undo()
	if e.Value() != "two three" {
		t.Errorf("undo the paste: %q", e.Value())
	}
	// ctrl+c with nothing selected leaves the clipboard alone.
	clip = "kept"
	e.editKey(padKeyMsg('c', tea.ModCtrl))
	if clip != "kept" {
		t.Errorf("ctrl+c without a selection set the clipboard to %q", clip)
	}
	// A bracketed paste (insert) cleans line ends and control characters.
	e = newPadEditor("")
	e.insert("a\r\nb\x07c", false)
	if e.Value() != "a\nbc" {
		t.Errorf("paste cleaned to %q", e.Value())
	}
}
