package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// order is the session names in list order, joined by spaces.
func order(m *model) string {
	names := make([]string, len(m.sessions))
	for i, s := range m.sessions {
		names[i] = s.Name
	}
	return strings.Join(names, " ")
}

func selectByName(t *testing.T, m *model, name string) {
	t.Helper()
	for i, s := range m.sessions {
		if s.Name == name {
			m.sel = i
			return
		}
	}
	t.Fatalf("no session %q", name)
}

func shiftKey(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: tea.ModShift} }

func TestMoveSessionKeyStrings(t *testing.T) {
	for code, want := range map[rune]string{
		tea.KeyUp: "shift+up", tea.KeyDown: "shift+down",
		tea.KeyHome: "shift+home", tea.KeyEnd: "shift+end",
	} {
		if got := shiftKey(code).String(); got != want {
			t.Errorf("key %d with shift is %q, want %q", code, got, want)
		}
	}
}

func TestMoveSessionNoFilter(t *testing.T) {
	m := testModel("A", "B", "C", "D")
	steps := []struct {
		name  string
		move  func() bool
		moved bool
		order string
		sel   string
	}{
		{"up from C", func() bool { return m.moveSession(-1) }, true, "A C B D", "C"},
		{"up again", func() bool { return m.moveSession(-1) }, true, "C A B D", "C"},
		{"up at the top", func() bool { return m.moveSession(-1) }, false, "C A B D", "C"},
		{"top at the top", func() bool { return m.moveSessionToEnd(true) }, false, "C A B D", "C"},
		{"down", func() bool { return m.moveSession(1) }, true, "A C B D", "C"},
		{"bottom", func() bool { return m.moveSessionToEnd(false) }, true, "A B D C", "C"},
		{"down at the bottom", func() bool { return m.moveSession(1) }, false, "A B D C", "C"},
		{"bottom at the bottom", func() bool { return m.moveSessionToEnd(false) }, false, "A B D C", "C"},
		{"top", func() bool { return m.moveSessionToEnd(true) }, true, "C A B D", "C"},
	}
	selectByName(t, m, "C")
	for _, st := range steps {
		if moved := st.move(); moved != st.moved {
			t.Errorf("%s: moved = %v, want %v", st.name, moved, st.moved)
		}
		if got := order(m); got != st.order {
			t.Errorf("%s: order %q, want %q", st.name, got, st.order)
		}
		if got := m.sessions[m.sel].Name; got != st.sel {
			t.Errorf("%s: selected %q, want %q", st.name, got, st.sel)
		}
	}
}

func TestMoveSessionSingleAndEmpty(t *testing.T) {
	m := testModel()
	if m.moveSession(-1) || m.moveSession(1) || m.moveSessionToEnd(true) || m.moveSessionToEnd(false) {
		t.Error("nothing to move in an empty list")
	}
	m = testModel("only")
	if m.moveSession(-1) || m.moveSession(1) || m.moveSessionToEnd(true) || m.moveSessionToEnd(false) {
		t.Error("a single session can't move")
	}
}

// The issue's example: the full list is A b C d E and the filter shows
// A C E. Moves skip the hidden sessions.
func TestMoveSessionFiltered(t *testing.T) {
	setups := map[string]func(m *model){
		"/": func(m *model) { m.filter = "keep" },
		"@": func(m *model) {
			for _, s := range m.sessions {
				if strings.HasPrefix(s.Name, "keep") {
					s.status = StatusWaiting
				}
			}
			m.attentionOnly = true
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			fresh := func() *model {
				m := testModel("keep-A", "b", "keep-C", "d", "keep-E")
				setup(m)
				selectByName(t, m, "keep-E")
				if got := m.visible(); len(got) != 3 || got[0] != 0 || got[1] != 2 || got[2] != 4 {
					t.Fatalf("visible = %v, want [0 2 4]", got)
				}
				return m
			}

			m := fresh()
			if !m.moveSession(-1) {
				t.Fatal("shift+up on E should move it")
			}
			if got := order(m); got != "keep-A b keep-E keep-C d" {
				t.Errorf("shift+up on E: %q, want A b E C d", got)
			}
			if m.current() == nil || m.current().Name != "keep-E" {
				t.Errorf("selection should stay on E: %v", m.current())
			}
			// Down again puts it back past C, before the hidden d.
			if !m.moveSession(1) || order(m) != "keep-A b keep-C keep-E d" {
				t.Errorf("shift+down on E: %q, want A b C E d", order(m))
			}
			// d is hidden, so E is the last visible one: nothing happens.
			if m.moveSession(1) || order(m) != "keep-A b keep-C keep-E d" {
				t.Errorf("shift+down on the last visible: %q", order(m))
			}

			m = fresh()
			if !m.moveSessionToEnd(true) {
				t.Fatal("shift+home on E should move it")
			}
			if got := order(m); got != "keep-E keep-A b keep-C d" {
				t.Errorf("shift+home on E: %q, want E A b C d", got)
			}
			if m.current() == nil || m.current().Name != "keep-E" {
				t.Errorf("selection should stay on E: %v", m.current())
			}
			if m.moveSession(-1) || m.moveSessionToEnd(true) {
				t.Error("E is first: nothing should move")
			}

			// shift+end on A goes just after the last visible session (E),
			// and the hidden ones keep their order.
			m = fresh()
			selectByName(t, m, "keep-A")
			if !m.moveSessionToEnd(false) || order(m) != "b keep-C d keep-E keep-A" {
				t.Errorf("shift+end on A: %q, want b C d E A", order(m))
			}
		})
	}
}

// A hidden selection (the filter hides everything selected) doesn't move.
func TestMoveSessionHiddenSelection(t *testing.T) {
	m := testModel("api", "docs", "web")
	m.sel = 1
	m.filter = "web"
	if m.moveSession(-1) || m.moveSessionToEnd(true) || order(m) != "api docs web" {
		t.Errorf("a hidden selection moved: %q", order(m))
	}
}

// The keys move the session, the selection and the focus stay with it, and
// the new order is saved and loads back the same.
func TestMoveSessionKeysSaveOrder(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	m := testModel("one", "two", "three", "four")
	selectByName(t, m, "three")
	moving := m.sessions[m.sel]

	press(m, shiftKey(tea.KeyUp))
	if got := order(m); got != "one three two four" {
		t.Fatalf("shift+up: %q", got)
	}
	if m.current() != moving || m.focus != focusSidebar {
		t.Errorf("shift+up: selected %v, focus %v", m.current(), m.focus)
	}
	press(m, shiftKey(tea.KeyEnd))
	if got := order(m); got != "one two four three" {
		t.Fatalf("shift+end: %q", got)
	}
	press(m, shiftKey(tea.KeyHome))
	press(m, shiftKey(tea.KeyDown))
	if got := order(m); got != "one three two four" {
		t.Fatalf("shift+home, shift+down: %q", got)
	}
	if m.current() != moving {
		t.Errorf("the selection should follow the session")
	}

	loaded := loadSessions()
	names := make([]string, len(loaded))
	for i, s := range loaded {
		names[i] = s.Name
	}
	if got := strings.Join(names, " "); got != "one three two four" {
		t.Errorf("saved order: %q, want %q", got, "one three two four")
	}
}

// A test session moves like any other and stays unsaved.
func TestMoveTestSessionStaysUnsaved(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	m := testModel("one", "two", "test · fake")
	m.sessions[2].testing.Store(true)
	m.sel = 2
	press(m, shiftKey(tea.KeyHome))
	if got := order(m); got != "test · fake one two" {
		t.Fatalf("shift+home on the test session: %q", got)
	}
	loaded := loadSessions()
	if len(loaded) != 2 || loaded[0].Name != "one" || loaded[1].Name != "two" {
		t.Errorf("saved %d sessions; the test session should stay unsaved", len(loaded))
	}
}

// x asks before removing; the session it removes is the one it asked
// about, even if the list changes before the answer.
func TestRemoveConfirmUsesTheSessionAskedAbout(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	m := testModel("one", "two", "three")
	m.sel = 2
	press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.confirm == nil {
		t.Fatal("x should ask first")
	}
	// Something else changes the selection while it asks (a discarded
	// agent test finishing, say, puts back the selection it had).
	m.sel = 0
	press(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if got := order(m); got != "one two" {
		t.Errorf("after y: %q, want %q", got, "one two")
	}
}

// x's prompt defaults to yes: enter removes the session, running or not,
// and the text says so with Y/n. n and esc still cancel.
func TestRemoveConfirmEnterIsYes(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	for _, running := range []bool{false, true} {
		m := testModel("one", "two")
		m.sessions[1].running = running // no process: Kill does nothing
		m.sel = 1
		want := `Remove "two" from the list? Y/n`
		if running {
			want = `End "two" and remove it? Y/n`
		}
		for _, cancel := range []tea.KeyPressMsg{{Code: 'n', Text: "n"}, {Code: tea.KeyEscape}} {
			press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
			if m.confirm == nil || m.confirm.text != want {
				t.Fatalf("running=%v: prompt %+v, want %q", running, m.confirm, want)
			}
			press(m, cancel)
			if m.confirm != nil || order(m) != "one two" {
				t.Fatalf("running=%v: %q should cancel: %q", running, cancel.String(), order(m))
			}
		}
		press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
		press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.confirm != nil || order(m) != "one" {
			t.Errorf("running=%v: after enter: %q, want %q", running, order(m), "one")
		}
	}
}

// Other prompts have no default: enter on K's cancels.
func TestEndConfirmEnterCancels(t *testing.T) {
	m := testModel("one")
	m.sessions[0].running = true
	press(m, tea.KeyPressMsg{Code: 'K', Text: "K"})
	if m.confirm == nil || m.confirm.defaultYes || !strings.HasSuffix(m.confirm.text, " y/n") {
		t.Fatalf("K prompt: %+v", m.confirm)
	}
	ended := false
	action := m.confirm.action
	m.confirm.action = func(m *model) tea.Cmd { ended = true; return action(m) }
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.confirm != nil || ended {
		t.Errorf("enter should cancel K: confirm %+v, ended %v", m.confirm, ended)
	}
}
