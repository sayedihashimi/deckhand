package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
)

// padModel is a model with sessions in a temporary data folder, sized like
// a terminal, with the first session selected.
func padModel(t *testing.T, names ...string) *model {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	m := testModel(names...)
	m.width, m.height = 120, 32
	return m
}

func padType(m *model, s string) {
	for _, r := range s {
		m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestParsePadConfig(t *testing.T) {
	list := defaultListSettings()
	for _, c := range []struct {
		text  string
		want  padSettings
		isErr bool
	}{
		{"", padSettings{key: "f6", share: 0.33}, false},
		{"[pad]\nkey = 'F10'\nheight = 0.5\n", padSettings{key: "f10", share: 0.5}, false},
		{"[pad]\nkey = 'f7'\n", padSettings{share: 0.33}, true}, // the history back key
		{"[pad]\nkey = 'f8'\n", padSettings{share: 0.33}, true}, // the history forward key
		{"[pad]\nheight = 12\n", padSettings{key: "f6", lines: 12}, false},
		{"[pad]\nheight = 12.0\n", padSettings{key: "f6", lines: 12}, false},
		{"[pad]\nheight = 2\n", padSettings{key: "f6", share: 0.33}, true},
		{"[pad]\nheight = 1.5\n", padSettings{key: "f6", share: 0.33}, true},
		{"[pad]\nheight = 'big'\n", padSettings{key: "f6", share: 0.33}, true},
		{"[pad]\nkey = 'f3'\n", padSettings{share: 0.33}, true},            // the toggle key
		{"[pad]\nkey = 'f5'\n", padSettings{share: 0.33}, true},            // the back key
		{"[pad]\nkey = 'f1'\n", padSettings{key: "f6", share: 0.33}, true}, // help
		{"[pad]\nkey = 'e'\n", padSettings{key: "f6", share: 0.33}, true},  // a character
	} {
		got, err := parsePadConfig(c.text, list)
		if (err != nil) != c.isErr || got != c.want {
			t.Errorf("%q: got %+v, %v; want %+v, error %v", c.text, got, err, c.want, c.isErr)
		}
	}
	// A [list] key set to F6 takes the pad's default.
	list.toggleKey = "f6"
	if got, err := parsePadConfig("", list); err == nil || got.key != "" {
		t.Errorf("default F6 against toggle_key F6: %+v, %v", got, err)
	}
}

func TestPadRows(t *testing.T) {
	old := padOpts
	t.Cleanup(func() { padOpts = old })
	padOpts = defaultPadSettings()
	if got := padRowsFor(30); got != 10 {
		t.Errorf("a third of 30: %d", got)
	}
	if got := padRowsFor(6); got != 3 {
		t.Errorf("small: %d, want 3 (the terminal keeps 3)", got)
	}
	if got := padRowsFor(4); got != 0 {
		t.Errorf("no room: %d", got)
	}
	padOpts.lines = 25
	if got := padRowsFor(30); got != 25 {
		t.Errorf("25 lines: %d", got)
	}
	if got := padRowsFor(20); got != 17 {
		t.Errorf("25 lines in 20: %d, want 17", got)
	}
}

// Opening the pad shrinks the session's terminal, closing it gives the
// rows back; the pad and its title line are drawn below the terminal.
func TestPadLayout(t *testing.T) {
	m := padModel(t, "api")
	s := m.sessions[0]
	w, h := m.termSize()
	if gw, gh := m.sessionSize(s); gw != w || gh != h {
		t.Fatalf("closed: %d×%d, want %d×%d", gw, gh, w, h)
	}
	m.handleKey(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if !s.PadOpen || m.focus != focusPad {
		t.Fatalf("e: open %v, focus %v", s.PadOpen, m.focus)
	}
	rows := padRowsFor(h)
	if _, gh := m.sessionSize(s); gh != h-rows {
		t.Errorf("open: %d rows, want %d", gh, h-rows)
	}
	padType(m, "ask about retries")
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	title := lines[len(lines)-1-rows]
	if !strings.Contains(title, "scratch pad") || !strings.Contains(lines[len(lines)-rows], "ask about retries") {
		t.Fatalf("pad not drawn at the bottom:\n%s", v.Content)
	}
	if v.Cursor == nil || v.Cursor.Y != m.height-1-rows+1 || v.Cursor.X != m.mainLeft()+1+len("ask about retries") {
		t.Errorf("cursor %+v", v.Cursor)
	}
	if !strings.Contains(lines[len(lines)-1], "ctrl+enter send") {
		t.Errorf("footer: %q", lines[len(lines)-1])
	}
	// The list marks a session with notes.
	if !strings.Contains(lines[2], "✎") {
		t.Errorf("no ✎ on the session's row: %q", lines[2])
	}
	// esc to the list (it isn't running), then F6... is a terminal key:
	// esc esc in the terminal closes it.
	s.running = true
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.focus != focusTerminal {
		t.Fatalf("esc: focus %v, want the terminal", m.focus)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.PadOpen {
		t.Fatal("esc esc should close the pad")
	}
	if _, gh := m.sessionSize(s); gh != h {
		t.Errorf("closed again: %d rows", gh)
	}
	// F6 opens it again and focuses it; F6 goes back to the terminal.
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF6})
	if !s.PadOpen || m.focus != focusPad {
		t.Fatalf("F6: open %v, focus %v", s.PadOpen, m.focus)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF6})
	if m.focus != focusTerminal || !s.PadOpen {
		t.Fatalf("F6 in the pad: focus %v", m.focus)
	}
	// A late esc goes to the agent and leaves the pad open.
	m.padStore().escAt = time.Now().Add(-2 * padEscWindow)
	m.padTerminalKey("esc")
	if !s.PadOpen {
		t.Error("a late esc closed the pad")
	}
}

// emuInput collects what a session's emulator sends to the program.
type emuInput struct {
	mu  sync.Mutex
	buf strings.Builder
}

func readEmu(emu *vt.SafeEmulator) *emuInput {
	in := &emuInput{}
	go func() {
		b := make([]byte, 1024)
		for {
			n, err := emu.Read(b)
			in.mu.Lock()
			in.buf.Write(b[:n])
			in.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return in
}

func (in *emuInput) waitFor(t *testing.T, want string) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		in.mu.Lock()
		got := in.buf.String()
		in.mu.Unlock()
		if got == want {
			return
		}
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	t.Fatalf("program got %q, want %q", in.buf.String(), want)
}

func TestPadSend(t *testing.T) {
	old := padEnterDelay
	padEnterDelay = 0
	t.Cleanup(func() { padEnterDelay = old })
	ctrlEnter := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}

	for _, bracketed := range []bool{false, true} {
		m := padModel(t, "api")
		s := m.sessions[0]
		emu := vt.NewSafeEmulator(80, 24)
		s.emu, s.running, s.bracketed = emu, true, bracketed
		in := readEmu(emu)
		m.openPad()
		padType(m, "one")
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		padType(m, "two\n")

		// The whole pad, without the blank line at its end, then Enter.
		m.handleKey(ctrlEnter)
		want := "one\rtwo\r"
		if bracketed {
			want = "\x1b[200~one\rtwo\x1b[201~\r"
		}
		in.waitFor(t, want)
		_, p := m.padFocused()
		if p.note != "sent 2 lines" || p.ed.Value() != "one\ntwo\n" || m.focus != focusPad {
			t.Errorf("after sending: note %q, text %q, focus %v", p.note, p.ed.Value(), m.focus)
		}

		// The selection only, with alt+enter.
		p.ed.selectRange(0, 3)
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
		sel := "one\r"
		if bracketed {
			sel = "\x1b[200~one\x1b[201~\r"
		}
		in.waitFor(t, want+sel)
		if p.note != "sent 1 line" {
			t.Errorf("selection: note %q", p.note)
		}
	}
}

func TestPadSendRefusesExited(t *testing.T) {
	m := padModel(t, "api")
	m.openPad()
	padType(m, "hello")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	_, p := m.padFocused()
	if p.note != "session has exited · ⏎ resume first" || !p.noteErr {
		t.Errorf("note %q", p.note)
	}
	if f := m.renderFooter(120); !strings.Contains(f, "resume first") {
		t.Errorf("footer: %q", f)
	}
}

func TestPadAutosave(t *testing.T) {
	m := padModel(t, "s1", "s2")
	s := m.sessions[0]
	m.openPad()
	padType(m, "note")
	path := padPath(s.ID)
	_, p := m.padFocused()
	m.padTick(p.editedAt.Add(500 * time.Millisecond))
	if _, err := os.Stat(path); err == nil {
		t.Fatal("saved while still typing")
	}
	m.padTick(p.editedAt.Add(padSaveDelay))
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "note" {
		t.Fatalf("after a second: %q, %v", data, err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temporary file is left behind")
	}
	// Leaving the pad saves right away.
	padType(m, "s")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF5})
	if data, _ := os.ReadFile(path); string(data) != "notes" {
		t.Errorf("after leaving: %q", data)
	}
	// An empty pad has no file.
	m.handleKey(tea.KeyPressMsg{Code: 'e', Text: "e"})
	m.handleKey(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF5})
	if _, err := os.Stat(path); err == nil {
		t.Error("an empty pad should have no file")
	}
	// Quitting saves unsaved notes, and a restart loads them.
	m.handleKey(tea.KeyPressMsg{Code: 'e', Text: "e"})
	padType(m, "kept")
	m.quit()
	m2 := testModel("s1")
	if got := m2.pad(m2.sessions[0]).ed.Value(); got != "kept" {
		t.Errorf("after a restart: %q", got)
	}
}

func TestPadTemporaryWindowSavesNothing(t *testing.T) {
	m := padModel(t, "s1")
	m.temp = true
	m.openPad()
	padType(m, "draft")
	m.padTick(time.Now().Add(time.Hour))
	m.quit()
	if entries, _ := os.ReadDir(notesDir()); len(entries) > 0 {
		t.Errorf("a temporary window wrote %v", entries)
	}
}

func TestPadRemoveArchivesNotes(t *testing.T) {
	for _, del := range []bool{false, true} {
		m := padModel(t, "s1", "s2")
		s := m.sessions[0]
		s.Name = `api: "retries"`
		m.openPad()
		padType(m, "ask about retries")
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyF5})
		m.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
		if m.confirm == nil || !strings.Contains(m.confirm.text, "notes are kept (D deletes them)") {
			t.Fatalf("confirm: %+v", m.confirm)
		}
		if del {
			m.handleKey(tea.KeyPressMsg{Code: 'D', Text: "D"})
		} else {
			m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		if len(m.sessions) != 1 {
			t.Fatalf("delete=%v: %d sessions left", del, len(m.sessions))
		}
		if _, err := os.Stat(padPath("s1")); err == nil {
			t.Errorf("delete=%v: the notes are still in notes", del)
		}
		archived := filepath.Join(notesDir(), "archive", `s1 api_ _retries_.md`)
		data, err := os.ReadFile(archived)
		switch {
		case del && err == nil:
			t.Error("D should delete the notes, not archive them")
		case !del && string(data) != "ask about retries":
			t.Errorf("archive: %q, %v", data, err)
		}
	}
	// Without notes, the prompt is as before.
	m := padModel(t, "s1")
	m.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.confirm == nil || strings.Contains(m.confirm.text, "notes") || m.confirm.alt != nil {
		t.Errorf("confirm without notes: %+v", m.confirm)
	}
}

// A fork (f) and shift+enter start with an empty, closed pad.
func TestPadNewSessionsStartEmpty(t *testing.T) {
	started := fakeStart(t)
	m := testModel("api", "docs")
	m.width, m.height = 120, 32
	m.sel = 1
	m.openPad()
	padType(m, "docs notes")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF5})
	m.handleKey(shiftEnterKey())
	if len(*started) != 1 {
		t.Fatalf("started %d", len(*started))
	}
	n := (*started)[0]
	if n.PadOpen || m.padHasNotes(n) {
		t.Errorf("shift+enter: open %v, notes %q", n.PadOpen, m.pad(n).ed.Value())
	}
	orig := m.sessions[1]
	f := m.newFork(Agent{Name: "claude", Claude: true}, orig)
	if f.PadOpen || m.padHasNotes(f) {
		t.Errorf("fork: open %v, notes %q", f.PadOpen, m.pad(f).ed.Value())
	}
	if !orig.PadOpen || m.pad(orig).ed.Value() != "docs notes" {
		t.Errorf("the original's pad changed")
	}
}

func TestPadExternalEditorReloads(t *testing.T) {
	m := padModel(t, "s1")
	var ran []string
	oldStart := openStart
	openStart = func(prog string, args []string, dir string) error {
		ran = append([]string{prog}, args...)
		return nil
	}
	t.Cleanup(func() { openStart = oldStart })
	oldOpen := openSettings
	openSettings = openConfig{Editor: `myedit.exe --new "{dir}"`}
	t.Cleanup(func() { openSettings = oldOpen })

	m.openPad()
	padType(m, "draft") // not saved yet: ctrl+o saves it first
	m.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	path := padPath("s1")
	if strings.Join(ran, " ") != "myedit.exe --new "+path {
		t.Fatalf("ran %q", ran)
	}
	if data, _ := os.ReadFile(path); string(data) != "draft" {
		t.Fatalf("not saved before opening: %q", data)
	}
	// The editor saves; the pad reloads.
	if err := os.WriteFile(path, []byte("draft\r\nmore from the editor\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(path, later, later)
	m.padTick(time.Now().Add(time.Second))
	_, p := m.padFocused()
	if got := p.ed.Value(); got != "draft\nmore from the editor\n" {
		t.Fatalf("after the editor saved: %q", got)
	}
	if p.ed.version != p.saved {
		t.Error("a reload shouldn't count as unsaved")
	}
	p.ed.Undo()
	if got := p.ed.Value(); got != "draft" {
		t.Errorf("undo the reload: %q", got)
	}

	// An empty pad gets a file to open; without a configured editor or
	// VS Code, Notepad.
	if got := padEditorTemplate(openConfig{}, func(string) (string, error) { return "", os.ErrNotExist }); got != `notepad.exe "{dir}"` {
		t.Errorf("fallback editor: %q", got)
	}
}

func TestPadMouse(t *testing.T) {
	m := padModel(t, "api")
	s := m.sessions[0]
	m.setPadOpen(s, true)
	m.focus = focusSidebar
	p := m.pad(s)
	p.ed = newPadEditor("ask about retries")
	m.View() // lays the pad out
	_, rows := m.padShown()
	y := m.height - 1 - rows + 1 // the pad's first text row
	x0 := m.mainLeft() + 1

	click := func(x int) tea.MouseMsg { return tea.MouseClickMsg{X: x0 + x, Y: y, Button: tea.MouseLeft} }
	m.handleMouse(click(4))
	if m.focus != focusPad || p.ed.cur != 4 || p.ed.hasSelection() {
		t.Fatalf("click: focus %v, cursor %d", m.focus, p.ed.cur)
	}
	m.handleMouse(tea.MouseMotionMsg{X: x0 + 9, Y: y, Button: tea.MouseLeft})
	m.handleMouse(tea.MouseReleaseMsg{X: x0 + 9, Y: y, Button: tea.MouseLeft})
	if got := p.ed.selectedText(); got != "about" {
		t.Fatalf("drag: %q", got)
	}
	// A double click selects the word.
	m.handleMouse(click(12))
	m.handleMouse(tea.MouseReleaseMsg{X: x0 + 12, Y: y, Button: tea.MouseLeft})
	m.handleMouse(click(12))
	if got := p.ed.selectedText(); got != "retries" {
		t.Fatalf("double click: %q", got)
	}
	// A click above the pad is the terminal's.
	s.running = true
	m.handleMouse(tea.MouseClickMsg{X: x0, Y: 3, Button: tea.MouseLeft})
	if m.focus == focusPad {
		t.Error("a click on the terminal kept the pad focused")
	}
}
