package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// handledKeys reads model.go and returns the key strings handleKey (and the
// new-session dialog) handle, by the help group they belong in.
func handledKeys(t *testing.T) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	isKey := func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.Ident:
			return e.Name == "key"
		case *ast.CallExpr: // k.String()
			if s, ok := e.Fun.(*ast.SelectorExpr); ok {
				return s.Sel.Name == "String"
			}
		}
		return false
	}
	lit := func(e ast.Expr) (string, bool) {
		if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.STRING {
			s, err := strconv.Unquote(b.Value)
			return s, err == nil
		}
		return "", false
	}
	// collect returns every literal compared with the key under n: switch
	// cases and key == "...".
	collect := func(n ast.Node) []string {
		var out []string
		ast.Inspect(n, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SwitchStmt:
				if n.Tag != nil && isKey(n.Tag) {
					for _, c := range n.Body.List {
						for _, e := range c.(*ast.CaseClause).List {
							if s, ok := lit(e); ok {
								out = append(out, s)
							}
						}
					}
				}
			case *ast.BinaryExpr:
				if n.Op == token.EQL && isKey(n.X) {
					if s, ok := lit(n.Y); ok {
						out = append(out, s)
					}
				}
			}
			return true
		})
		return out
	}

	got := map[string][]string{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "update": // newDialog.update
			got[helpDialog] = append(got[helpDialog], collect(fn.Body)...)
		case "handleKey":
			for _, st := range fn.Body.List {
				switch st := st.(type) {
				case *ast.IfStmt:
					// Keys handled before anything else (F1), not the
					// prompts' own keys.
					if b, ok := st.Cond.(*ast.BinaryExpr); ok && b.Op == token.EQL && isKey(b.X) {
						if s, ok := lit(b.Y); ok {
							got[helpList] = append(got[helpList], s)
							got[helpTerminal] = append(got[helpTerminal], s)
						}
					}
				case *ast.SwitchStmt:
					if isKey(st.Tag) { // the sidebar
						got[helpList] = append(got[helpList], collect(st)...)
						continue
					}
					for _, c := range st.Body.List { // switch m.focus
						cc := c.(*ast.CaseClause)
						for _, e := range cc.List {
							id, _ := e.(*ast.Ident)
							switch {
							case id == nil:
							case id.Name == "focusTerminal":
								got[helpTerminal] = append(got[helpTerminal], collect(cc)...)
							case id.Name == "focusDialog":
								got[helpDialog] = append(got[helpDialog], collect(cc)...)
							}
						}
					}
				}
			}
		}
	}
	return got
}

// Every key handleKey handles must be in the help table, in the right
// group. If this fails after adding a key, add it to helpGroups in help.go.
func TestHelpCoversHandledKeys(t *testing.T) {
	got := handledKeys(t)
	for _, title := range []string{helpList, helpTerminal, helpDialog} {
		if len(got[title]) < 3 {
			t.Fatalf("%s: found only %q in model.go; has handleKey changed shape?", title, got[title])
		}
		inTable := map[string]bool{}
		for _, e := range helpGroupByTitle(title).entries {
			for _, k := range e.match {
				inTable[k] = true
			}
		}
		for _, k := range got[title] {
			if !inTable[k] {
				t.Errorf("%s: key %q is handled but not in the help table (helpGroups in help.go)", title, k)
			}
		}
	}
	for _, k := range []string{"?", "f1", "shift+pgup", "ctrl+q", "o", "K"} {
		found := false
		for _, g := range got {
			for _, h := range g {
				found = found || h == k
			}
		}
		if !found {
			t.Errorf("key %q not found in model.go; the parser missed it", k)
		}
	}
}

func TestFooterFits(t *testing.T) {
	full, short := footerHints(helpList, false), footerHints(helpList, true)
	if !strings.Contains(full, "? help") || !strings.Contains(short, "? help") {
		t.Errorf("footer should mention ? help: %q", full)
	}
	if w := ansi.StringWidth(short); w > 120 {
		t.Errorf("short footer is %d cells, want ≤ 120: %q", w, short)
	}
	if !strings.Contains(footerHints(helpTerminal, false), "F1 help") {
		t.Errorf("session footer: %q", footerHints(helpTerminal, false))
	}
	m := testModel("api")
	m.width, m.height = 120, 32
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, "? help") || !strings.Contains(f, "q quit") {
		t.Errorf("footer at 120 cells: %q", f)
	}
}

func press(m *model, k tea.KeyPressMsg) { m.handleKey(k) }

func TestHelpOpenClose(t *testing.T) {
	m := testModel("api")
	m.width, m.height = 120, 32
	f1 := tea.KeyPressMsg{Code: tea.KeyF1}
	if f1.String() != "f1" {
		t.Fatalf("F1 is %q", f1.String())
	}
	for _, c := range []struct {
		name  string
		focus focusArea
		open  tea.KeyPressMsg
		close tea.KeyPressMsg
	}{
		{"? then esc", focusSidebar, tea.KeyPressMsg{Code: '?', Text: "?"}, tea.KeyPressMsg{Code: tea.KeyEscape}},
		{"F1 then ?", focusSidebar, f1, tea.KeyPressMsg{Code: '?', Text: "?"}},
		{"F1 then q", focusSidebar, f1, tea.KeyPressMsg{Code: 'q', Text: "q"}},
		// The session isn't running, so stay put rather than going through
		// the terminal branch: F1 is checked before it.
		{"F1 in a session then F1", focusTerminal, f1, f1},
		{"F1 in the dialog then esc", focusDialog, f1, tea.KeyPressMsg{Code: tea.KeyEscape}},
	} {
		m.focus, m.help = c.focus, nil
		press(m, c.open)
		if m.help == nil {
			t.Errorf("%s: help didn't open", c.name)
			continue
		}
		press(m, tea.KeyPressMsg{Code: tea.KeyDown}) // scrolls, doesn't close
		press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
		if m.help == nil || m.confirm != nil {
			t.Errorf("%s: other keys should be swallowed", c.name)
		}
		press(m, c.close)
		if m.help != nil {
			t.Errorf("%s: help didn't close", c.name)
		}
		if m.focus != c.focus {
			t.Errorf("%s: focus %v, want %v", c.name, m.focus, c.focus)
		}
	}
	// ? while typing a filter is text, not help.
	m.focus = focusSidebar
	press(m, tea.KeyPressMsg{Code: '/', Text: "/"})
	press(m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if m.help != nil || m.filterInput == nil || m.filterInput.Value() != "?" {
		t.Errorf("? in the filter: help=%v", m.help != nil)
	}
}

// The overlay fits at 120×32, the screenshot size, and scrolls when the
// window is smaller.
func TestHelpRender(t *testing.T) {
	m := testModel("api")
	m.width, m.height = 120, 32
	m.openHelp()
	sw := m.sidebarWidth()
	mw, h := m.width-sw-1, m.height-1
	lines := m.overlayHelp(make([]string, h), mw, h)
	if len(lines) != h {
		t.Fatalf("%d lines, want %d", len(lines), h)
	}
	screen := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"Session list", "Inside a session", "Mouse", "New-session dialog", "Status icons", "Config file:", "shift+drag", "ctrl+w", "not running"} {
		if !strings.Contains(screen, want) {
			t.Errorf("missing %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "more lines") {
		t.Errorf("should fit without scrolling at 120×32:\n%s", screen)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != mw {
			t.Errorf("line %d is %d cells, want %d: %q", i, w, mw, ansi.Strip(l))
		}
	}
	// Every entry is listed.
	for _, g := range helpGroups {
		for _, e := range g.entries {
			if !strings.Contains(screen, e.keys) {
				t.Errorf("missing key %q", e.keys)
			}
		}
	}

	// Small: it scrolls.
	small := m.overlayHelp(make([]string, 15), 60, 15)
	s := ansi.Strip(strings.Join(small, "\n"))
	if len(small) != 15 || !strings.Contains(s, "more lines") || !strings.Contains(s, "Keys") {
		t.Fatalf("small:\n%s", s)
	}
	for i := 0; i < 200; i++ {
		m.helpKey("down")
	}
	s = ansi.Strip(strings.Join(m.overlayHelp(make([]string, 15), 60, 15), "\n"))
	if !strings.Contains(s, "Config file:") || strings.Contains(s, "more lines") {
		t.Errorf("scrolled to the end:\n%s", s)
	}
	m.helpKey("up")
	if m.help.scroll <= 0 {
		t.Errorf("scroll after up = %d", m.help.scroll)
	}
}
