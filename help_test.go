package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// handledKeys reads model.go, diff.go and configscreen.go and returns the
// key strings handleKey (and the new-session dialog, the diff preview and
// the config screen) handle, by the help group they belong in.
func handledKeys(t *testing.T) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	df, err := parser.ParseFile(fset, "diff.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := parser.ParseFile(fset, "configscreen.go", nil, 0)
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
	for _, d := range df.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "diffKey" {
			got[helpDiff] = collect(fn.Body)
		}
	}
	// The config screen, its agent form and profile picker, and the keys
	// that toggle a choice.
	configFuncs := map[string]bool{"configKey": true, "formKey": true, "pickerKey": true, "isToggleKey": true}
	for _, d := range cf.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && configFuncs[fn.Name.Name] {
			delete(configFuncs, fn.Name.Name)
			got[helpConfig] = append(got[helpConfig], collect(fn.Body)...)
		}
	}
	for name := range configFuncs {
		t.Errorf("configscreen.go has no %s; update handledKeys", name)
	}
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
							got[helpConfig] = append(got[helpConfig], s)
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
	for _, title := range []string{helpList, helpTerminal, helpDialog, helpDiff, helpConfig} {
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
	for _, k := range []string{"?", "f1", "shift+pgup", "ctrl+q", "o", "K", "ctrl+s", "ctrl+p", "shift+down"} {
		found := false
		for _, g := range got {
			for _, h := range g {
				found = found || h == k
			}
		}
		if !found {
			t.Errorf("key %q not found in model.go or configscreen.go; the parser missed it", k)
		}
	}
}

// The back key (F5) is compared with listOpts, not a literal, so
// handledKeys can't see it; check its help entries directly.
func TestHelpCoversBackKey(t *testing.T) {
	for _, title := range []string{helpList, helpTerminal} {
		found := false
		for _, e := range helpGroupByTitle(title).entries {
			found = found || slices.Contains(e.match, defaultBackKey)
		}
		if !found {
			t.Errorf("%s: F5 isn't in the help table", title)
		}
	}
	if f := footerHints(helpTerminal, false); !strings.Contains(f, "F5 list") || strings.Contains(f, "ctrl+q") {
		t.Errorf("session footer should show F5, not ctrl+q: %q", f)
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
		{"F1 in the config screen then esc", focusConfig, f1, tea.KeyPressMsg{Code: tea.KeyEscape}},
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

// helpScreen draws the help over an empty main pane, w×h.
func helpScreen(m *model, w, h int) (string, []string) {
	lines := m.overlayHelp(make([]string, h), w, h)
	return ansi.Strip(strings.Join(lines, "\n")), lines
}

// At 120×32, the screenshot size, the help opened from each context shows
// that context's group first and in full, then as many others as fit; the
// rest scroll, and the last line says how many groups are left.
func TestHelpRender(t *testing.T) {
	for _, c := range []struct {
		focus   focusArea
		context string
	}{
		{focusSidebar, helpList},
		{focusTerminal, helpTerminal},
		{focusDialog, helpDialog},
		{focusConfig, helpConfig},
	} {
		t.Run(c.context, func(t *testing.T) {
			m := testModel("api")
			m.width, m.height = 120, 32
			m.focus = c.focus
			m.openHelp()
			if m.help.context != c.context {
				t.Fatalf("context %q, want %q", m.help.context, c.context)
			}
			sw := m.sidebarWidth()
			mw, h := m.width-sw-1, m.height-1
			screen, lines := helpScreen(m, mw, h)
			if len(lines) != h {
				t.Fatalf("%d lines, want %d", len(lines), h)
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != mw {
					t.Errorf("line %d is %d cells, want %d: %q", i, w, mw, ansi.Strip(l))
				}
			}
			// The context's group is first, right under the title, and
			// every entry of it shows.
			rows := strings.Split(screen, "\n")
			first := -1
			for i, r := range rows {
				if strings.Contains(r, "Keys") {
					first = i + 2
					break
				}
			}
			if first < 0 || first >= len(rows) || !strings.Contains(rows[first], c.context) {
				t.Fatalf("%q should be the first group:\n%s", c.context, screen)
			}
			if !strings.Contains(rows[first-2], "Keys · "+c.context) {
				t.Errorf("title should name the context: %q", rows[first-2])
			}
			g := helpGroupByTitle(c.context)
			for _, e := range g.entries {
				if !strings.Contains(screen, e.keys) || !strings.Contains(screen, e.desc[:min(len(e.desc), 12)]) {
					t.Errorf("%s: missing %q %q:\n%s", c.context, e.keys, e.desc, screen)
				}
			}
			// Two columns: at least one other group shows beside it.
			others := 0
			for _, o := range helpGroups {
				if o.title != c.context && strings.Contains(screen, o.title) {
					others++
				}
			}
			if others == 0 {
				t.Errorf("no other group shows:\n%s", screen)
			}
			// All the groups don't fit, so the last line counts the rest.
			last := rows[len(rows)-2] // above the bottom border
			if !strings.Contains(last, "more group") {
				t.Errorf("last line should say how many more groups: %q\n%s", last, screen)
			}
			// Scrolling shows every group and entry, and the tail.
			seen := screen
			for i := 0; i < 100; i++ {
				m.helpKey("down")
				s, _ := helpScreen(m, mw, h)
				seen += s
			}
			for _, o := range helpGroups {
				if !strings.Contains(seen, o.title) {
					t.Errorf("group %q never shows", o.title)
				}
				for _, e := range o.entries {
					if !strings.Contains(seen, e.keys) {
						t.Errorf("%s: key %q never shows", o.title, e.keys)
					}
				}
			}
			end, _ := helpScreen(m, mw, h)
			for _, want := range []string{"Status icons", "not running", "Config file:"} {
				if !strings.Contains(end, want) {
					t.Errorf("scrolled to the end, missing %q:\n%s", want, end)
				}
			}
			if strings.Contains(end, "more") {
				t.Errorf("scrolled to the end, nothing should be left:\n%s", end)
			}
		})
	}

	// Wide and tall: everything fits without scrolling.
	m := testModel("api")
	m.openHelp()
	if s, _ := helpScreen(m, 120, 60); strings.Contains(s, "↑↓ scroll") || !strings.Contains(s, "Config file:") || !strings.Contains(s, helpConfig) {
		t.Errorf("120×60 should fit:\n%s", s)
	}

	// Small: one column, it scrolls.
	m.help.context = helpConfig
	s, small := helpScreen(m, 60, 15)
	if len(small) != 15 || !strings.Contains(s, "more group") || !strings.Contains(s, "Keys") || !strings.Contains(s, helpConfig) {
		t.Fatalf("small:\n%s", s)
	}
	for i := 0; i < 200; i++ {
		m.helpKey("down")
	}
	s, _ = helpScreen(m, 60, 15)
	if !strings.Contains(s, "Config file:") || strings.Contains(s, "more") {
		t.Errorf("scrolled to the end:\n%s", s)
	}
	m.helpKey("up")
	if m.help.scroll <= 0 {
		t.Errorf("scroll after up = %d", m.help.scroll)
	}
}

// The config screen's footers come from the help table.
func TestConfigFooter(t *testing.T) {
	t.Setenv("DECKHAND_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	m := testModel("api")
	m.width, m.height = 120, 32
	m.openConfigScreen()
	for _, c := range []struct {
		name string
		set  func()
		want []string
	}{
		{"screen", func() {}, []string{"↑↓/tab move", "ctrl+s save", "esc leave without saving", "F1 help"}},
		{"form", func() { m.cfg.openForm(-1) }, []string{"↑↓/tab move", "enter done", "esc cancel"}},
		{"picker", func() { m.cfg.picker = &profilePicker{} }, []string{"↑↓ choose", "enter pick", "esc back"}},
	} {
		c.set()
		f := ansi.Strip(m.renderFooter(120))
		for _, w := range c.want {
			if !strings.Contains(f, w) {
				t.Errorf("%s footer %q: missing %q", c.name, f, w)
			}
		}
		if c.name != "screen" && strings.Contains(f, "ctrl+s") {
			t.Errorf("%s footer has the screen's hints: %q", c.name, f)
		}
	}
}
