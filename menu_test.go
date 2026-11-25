package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// menuText is a menu's items: "Rename r", "-" for a separator, a dimmed
// one in brackets: "[Fork f]".
func menuText(items []menuItem) string {
	var out []string
	for _, it := range items {
		switch {
		case it.sep:
			out = append(out, "-")
		case it.off:
			out = append(out, "["+it.label+" "+it.hint()+"]")
		default:
			out = append(out, it.label+" "+it.hint())
		}
	}
	return strings.Join(out, " | ")
}

func rightClick(m *model, x, y int) {
	m.handleMouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	m.handleMouse(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseRight})
}

// clickItem clicks the item with this label in an open menu.
func clickItem(t *testing.T, m *model, mu *ctxMenu, label string) tea.Cmd {
	t.Helper()
	for i, it := range mu.items {
		if it.label == label {
			cmd := m.handleMouse(tea.MouseClickMsg{X: mu.x + 3, Y: mu.y + 1 + i, Button: tea.MouseLeft})
			m.handleMouse(tea.MouseReleaseMsg{X: mu.x + 3, Y: mu.y + 1 + i, Button: tea.MouseLeft})
			return cmd
		}
	}
	t.Fatalf("no %q in the menu: %s", label, menuText(mu.items))
	return nil
}

// sessionY is the screen line of session i's first row, with no groups.
func sessionY(i int) int { return listTop + i*rowsPerSession }

func menuModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	m := claudeModel()
	m.width, m.height = 120, 40
	return m
}

func TestMenuForSessions(t *testing.T) {
	m := menuModel(t)
	// A Claude session that has run: it can fork.
	if got, want := menuText(m.sessionMenu(m.sessions[0])), "Color… C | Rename r | Fork f | Group… a | - | Open folder o | Open terminal t | - | Remove… x"; got != want {
		t.Errorf("plain:\n got %s\nwant %s", got, want)
	}
	// Exited and never started, or not Claude: Fork is dimmed.
	m.sessions[1].Started = false
	if got := menuText(m.sessionMenu(m.sessions[1])); !strings.Contains(got, "[Fork f]") {
		t.Errorf("never started: %s", got)
	}
	m.sessions[2].Agent = "pwsh"
	if got := menuText(m.sessionMenu(m.sessions[2])); !strings.Contains(got, "[Fork f]") {
		t.Errorf("a shell: %s", got)
	}
	// Mid-turn it isn't dimmed: f refuses, with its message.
	m.sessions[0].running, m.sessions[0].status = true, StatusWorking
	if got := menuText(m.sessionMenu(m.sessions[0])); !strings.Contains(got, "| Fork f |") {
		t.Errorf("mid-turn: %s", got)
	}
	// A worktree session has Diff, Create PR and Merge.
	m.sessions[0].Worktree = `C:\x\api-wt\otter`
	if got := menuText(m.sessionMenu(m.sessions[0])); !strings.Contains(got, "Open terminal t | - | Diff d | Create PR p | Merge m | - | Remove… x") {
		t.Errorf("worktree: %s", got)
	}
	if got := menuText(m.sessionMenu(m.sessions[1])); strings.Contains(got, "Diff") {
		t.Errorf("no worktree, but: %s", got)
	}
}

func TestMenuForGroups(t *testing.T) {
	m := groupedModel(t, "api@app", "docs@cli", "web")
	if got, want := menuText(m.headerMenu("app")), "Color… C | Rename r | Collapse space | - | Delete group x"; got != want {
		t.Errorf("app:\n got %s\nwant %s", got, want)
	}
	m.setCollapsed("app", true)
	if got := menuText(m.headerMenu("app")); !strings.Contains(got, "Expand space") {
		t.Errorf("collapsed: %s", got)
	}
	if got, want := menuText(m.headerMenu("")), "[Color… C] | [Rename r] | Collapse space | - | [Delete group x]"; got != want {
		t.Errorf("(no group):\n got %s\nwant %s", got, want)
	}
	// A session's Group is a submenu of the groups, its own dimmed.
	g := m.groupItem(m.sessions[0])
	if g.sub == nil || g.hint() != "a ▸" {
		t.Fatalf("Group: %+v", g)
	}
	if got, want := menuText(g.sub), "[app ] | cli  | (no group)  | - | New group… a"; got != want {
		t.Errorf("Group ▸:\n got %s\nwant %s", got, want)
	}
	// In a temporary window it's a, which says groups are off.
	m.temp = true
	if g := m.groupItem(m.sessions[0]); g.sub != nil || g.key != "a" {
		t.Errorf("temp: %+v", g)
	}
}

// A right-click selects the row and opens its menu; picking Rename opens
// the rename box, as r does.
func TestMenuRename(t *testing.T) {
	m := menuModel(t)
	m.focus = focusTerminal
	rightClick(m, 5, sessionY(1)+1)
	if m.menu == nil || m.sel != 1 || m.focus != focusSidebar {
		t.Fatalf("right-click: menu %v, sel %d, focus %v", m.menu, m.sel, m.focus)
	}
	if m.drag != nil {
		t.Error("a right-click started a drag")
	}
	if m.menu.x != 6 || m.menu.y != sessionY(1)+1 {
		t.Errorf("menu at %d,%d", m.menu.x, m.menu.y)
	}
	screen := ansi.Strip(m.overlayMenu(strings.Repeat(strings.Repeat(" ", 120)+"\n", 39) + "footer"))
	if !strings.Contains(screen, "Rename") || !strings.Contains(screen, "Remove…") {
		t.Errorf("menu not drawn:\n%s", screen)
	}
	clickItem(t, m, m.menu, "Rename")
	if m.menu != nil || m.rename == nil || m.rename.Value() != "docs" {
		t.Fatalf("Rename: menu %v, rename %v", m.menu, m.rename)
	}
}

// Remove… asks, with x's question; Fork mid-turn refuses as f does.
func TestMenuSameAsKeys(t *testing.T) {
	m := menuModel(t)
	rightClick(m, 5, sessionY(0))
	clickItem(t, m, m.menu, "Remove…")
	if m.confirm == nil || m.confirm.text != `Remove "api" from the list? Y/n` || len(m.sessions) != 3 {
		t.Fatalf("Remove…: %+v", m.confirm)
	}
	m.handleKey(key("n"))

	forkStart(t)
	m.sessions[1].running, m.sessions[1].status = true, StatusWorking
	rightClick(m, 5, sessionY(1))
	clickItem(t, m, m.menu, "Fork")
	if m.flash != "Wait for it to finish its turn" || len(m.sessions) != 3 {
		t.Errorf("Fork mid-turn: %q", m.flash)
	}
	// A dimmed item can't be picked: the menu stays open.
	m.sessions[2].Agent = "pwsh"
	rightClick(m, 5, sessionY(2))
	clickItem(t, m, m.menu, "Fork")
	if m.menu == nil || len(m.sessions) != 3 {
		t.Errorf("dimmed Fork was picked")
	}
	// Color… opens the picker for it.
	m.menu = nil
	rightClick(m, 5, sessionY(2))
	clickItem(t, m, m.menu, "Color…")
	if m.colorPick == nil || m.colorPick.s != m.sessions[2] {
		t.Errorf("Color…: %+v", m.colorPick)
	}
}

func TestMenuKeys(t *testing.T) {
	m := groupedModel(t, "api@app", "docs@cli", "web")
	y := rowLine(m, rowOf(t, m, "api"), 0)
	rightClick(m, 4, y)
	if m.menu == nil || m.current() != m.sessions[0] {
		t.Fatalf("no menu, or api isn't selected")
	}
	// ↓ skips Fork (dimmed: a shell can't fork); Group ▸ opens with →,
	// then esc closes just it.
	for range 2 {
		m.handleKey(key("down"))
	}
	if it := m.menu.items[m.menu.hi]; it.label != "Group" {
		t.Fatalf("highlighted %q", it.label)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.menu.sub == nil {
		t.Fatal("→ should open the submenu")
	}
	m.handleKey(key("esc"))
	if m.menu == nil || m.menu.sub != nil {
		t.Fatal("esc should close the submenu only")
	}
	// enter opens it too; ↓ to cli (app is dimmed), enter moves api there.
	m.handleKey(key("enter"))
	if m.menu.sub == nil || m.menu.sub.items[m.menu.sub.hi].label != "cli" {
		t.Fatalf("submenu: %+v", m.menu.sub)
	}
	m.handleKey(key("enter"))
	if m.menu != nil || m.sessions[m.indexOf(m.current())].group() != "cli" {
		t.Errorf("api's group: %q", groupsOf(m))
	}
	// Another key closes the menu and does what it does: r renames.
	rightClick(m, 4, rowLine(m, rowOf(t, m, "web"), 0))
	m.handleKey(key("r"))
	if m.menu != nil || m.rename == nil || m.rename.Value() != "web" {
		t.Errorf("r in the menu: menu %v, rename %v", m.menu, m.rename)
	}
	m.handleKey(key("esc"))
	// esc closes it.
	rightClick(m, 4, rowLine(m, rowOf(t, m, "web"), 0))
	m.handleKey(key("esc"))
	if m.menu != nil {
		t.Error("esc should close the menu")
	}
}

func TestMenuGroupHeader(t *testing.T) {
	m := groupedModel(t, "api@app", "docs@cli", "web")
	rightClick(m, 4, rowLine(m, rowOf(t, m, "[cli]"), 0))
	if m.menu == nil || !m.onHeader || m.selHeader != "cli" {
		t.Fatalf("header right-click: menu %v, header %v %q", m.menu, m.onHeader, m.selHeader)
	}
	clickItem(t, m, m.menu, "Collapse")
	if !m.collapsed("cli") {
		t.Error("Collapse didn't fold it")
	}
	rightClick(m, 4, rowLine(m, rowOf(t, m, "[cli]"), 0))
	clickItem(t, m, m.menu, "Delete group")
	if m.confirm == nil || !strings.HasPrefix(m.confirm.text, `Delete the group "cli"?`) {
		t.Fatalf("Delete group: %+v", m.confirm)
	}
	m.handleKey(key("y"))
	if m.groupByName("cli") != nil {
		t.Error("cli wasn't deleted")
	}
	rightClick(m, 4, rowLine(m, rowOf(t, m, "[app]"), 0))
	clickItem(t, m, m.menu, "Rename")
	if m.rename == nil || m.renameGroup == nil || m.rename.Value() != "app" {
		t.Errorf("Rename group: %v", m.rename)
	}
}

// A click outside, or another right-click, closes the menu and does
// nothing else. The wheel does nothing.
func TestMenuCloses(t *testing.T) {
	m := menuModel(t)
	rightClick(m, 5, sessionY(0))
	m.handleMouse(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	if m.menu == nil || m.sel != 0 {
		t.Fatalf("wheel: menu %v, sel %d", m.menu, m.sel)
	}
	m.handleMouse(tea.MouseClickMsg{X: 100, Y: 30, Button: tea.MouseLeft})
	if m.menu != nil || m.focus != focusSidebar || m.drag != nil {
		t.Errorf("click outside: menu %v, focus %v, drag %v", m.menu, m.focus, m.drag)
	}
	rightClick(m, 5, sessionY(0))
	rightClick(m, 5, sessionY(2))
	if m.menu != nil || m.sel != 0 {
		t.Errorf("second right-click: menu %v, sel %d", m.menu, m.sel)
	}
	// The left drag still works afterwards.
	m.handleMouse(tea.MouseClickMsg{X: 5, Y: sessionY(0), Button: tea.MouseLeft})
	if m.drag == nil {
		t.Error("a left press should start a drag")
	}
	m.cancelDrag()
	// A right-click on the terminal isn't the list's.
	rightClick(m, 60, 10)
	if m.menu != nil {
		t.Error("right-click on the terminal opened the menu")
	}
}

// Near the right and bottom edges the menu and its submenu stay on screen.
func TestMenuStaysOnScreen(t *testing.T) {
	m := groupedModel(t, "api@app", "docs@cli", "web")
	m.width, m.height = 40, 14
	y := rowLine(m, rowOf(t, m, "api"), 2)
	rightClick(m, 20, y)
	mu := m.menu
	if mu == nil {
		t.Fatal("no menu")
	}
	w, h := mu.size()
	if mu.x+w > m.width || mu.y+h > m.height-1 || mu.x < 0 || mu.y < 0 {
		t.Errorf("menu %dx%d at %d,%d on a %dx%d screen", w, h, mu.x, mu.y, m.width, m.height)
	}
	for i, it := range mu.items {
		if it.sub != nil {
			m.pick(mu, i)
		}
	}
	sub := mu.sub
	if sub == nil {
		t.Fatal("no submenu")
	}
	sw, sh := sub.size()
	if sub.x+sw > m.width || sub.y+sh > m.height-1 || sub.x < 0 || sub.y < 0 {
		t.Errorf("submenu %dx%d at %d,%d", sw, sh, sub.x, sub.y)
	}
	// A click on the submenu's item picks it.
	for i, it := range sub.items {
		if it.label == "cli" {
			m.handleMouse(tea.MouseClickMsg{X: sub.x + 2, Y: sub.y + 1 + i, Button: tea.MouseLeft})
		}
	}
	if m.menu != nil || m.sessions[m.indexOf(m.current())].group() != "cli" {
		t.Errorf("clicked cli: %s", groupsOf(m))
	}
}
