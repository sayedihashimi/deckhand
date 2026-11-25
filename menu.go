package main

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The right-click menu (#74): right-clicking a session or a group header in
// the list selects it and opens a menu of its actions next to the pointer.
// Each item shows its key, and picking it presses that key (asKey), so
// it does exactly what the key does, with the same confirmations and
// refusals. Items that can't apply are dimmed and can't be picked. Group ▸
// opens a submenu of the groups.
//
// A click or enter picks, esc, a click outside or another right-click
// closes it, and any other key closes it and does what it does in the list
// (so pressing an item's key picks it too).

type menuItem struct {
	label string
	key   string // the key it does the same as, shown on the right
	off   bool   // doesn't apply now: dimmed, can't be picked
	sep   bool   // a line between items
	run   func(m *model) tea.Cmd
	sub   []menuItem // a submenu, opened instead of run
}

// ctxMenu is an open menu, drawn with its top left corner at screen cell
// (x, y), with its submenu when one is open.
type ctxMenu struct {
	items []menuItem
	hi    int
	x, y  int
	sub   *ctxMenu
}

// asKey is an item's action: the key press it stands for.
func asKey(k string) func(m *model) tea.Cmd {
	return func(m *model) tea.Cmd {
		msg := tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		if k == "space" {
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		}
		_, cmd := m.handleKey(msg)
		return cmd
	}
}

// sessionMenu is the menu for a session, which is selected.
func (m *model) sessionMenu(s *Session) []menuItem {
	items := []menuItem{
		{label: "Color…", key: "C", run: asKey("C")},
		{label: "Rename", key: "r", run: asKey("r")},
		{label: "Fork", key: "f", off: !canFork(s), run: asKey("f")},
		m.groupItem(s),
		{sep: true},
		{label: "Open folder", key: "o", run: asKey("o")},
		{label: "Open terminal", key: "t", run: asKey("t")},
	}
	if s.hasWorktree() {
		items = append(items,
			menuItem{sep: true},
			menuItem{label: "Diff", key: "d", run: asKey("d")},
			menuItem{label: "Create PR", key: "p", run: asKey("p")},
			menuItem{label: "Merge", key: "m", run: asKey("m")},
		)
	}
	return append(items, menuItem{sep: true}, menuItem{label: "Remove…", key: "x", run: asKey("x")})
}

// canFork reports whether f could fork s at some point: a Claude agent
// that can resume, and a conversation to copy. Mid-turn it still can, and
// f says to wait.
func canFork(s *Session) bool {
	a, ok := s.agent()
	if !ok || !a.Claude || !a.CanResume() || s.preparing.Load() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Started && s.ClaudeSessionID != ""
}

// groupItem is Group: with groups, a submenu of them, "(no group)" and a
// new group (a); without any, or in a temporary window, a itself.
func (m *model) groupItem(s *Session) menuItem {
	if m.temp || !m.grouped() {
		return menuItem{label: "Group…", key: "a", run: asKey("a")}
	}
	var sub []menuItem
	cur := s.group()
	for _, g := range m.groups {
		name := g.Name
		sub = append(sub, menuItem{label: name, off: cur == name, run: func(m *model) tea.Cmd {
			m.assignTo(s, assignChoice{name: name})
			return nil
		}})
	}
	sub = append(sub,
		menuItem{label: noGroupName, off: cur == "", run: func(m *model) tea.Cmd {
			m.assignTo(s, assignChoice{none: true})
			return nil
		}},
		menuItem{sep: true},
		menuItem{label: "New group…", key: "a", run: asKey("a")},
	)
	return menuItem{label: "Group", key: "a", sub: sub}
}

// headerMenu is the menu for a group header, which is selected.
func (m *model) headerMenu(name string) []menuItem {
	fold := "Collapse"
	if m.collapsed(name) {
		fold = "Expand"
	}
	none := name == ""
	return []menuItem{
		{label: "Color…", key: "C", off: none, run: asKey("C")},
		{label: "Rename", key: "r", off: none, run: asKey("r")},
		{label: fold, key: "space", run: asKey("space")},
		{sep: true},
		{label: "Delete group", key: "x", off: none, run: asKey("x")},
	}
}

// openMenu opens the menu for the selected session or header, next to the
// pointer at (x, y).
func (m *model) openMenu(x, y int) {
	var items []menuItem
	if m.onHeader {
		items = m.headerMenu(m.selHeader)
	} else if s := m.current(); s != nil {
		items = m.sessionMenu(s)
	}
	if len(items) == 0 {
		return
	}
	mu := &ctxMenu{items: items, hi: -1}
	mu.move(1)
	mu.x, mu.y = m.placeMenu(mu, x+1, y)
	m.menu = mu
}

// placeMenu keeps a menu with its top left at (x, y) on screen, above the
// footer: moved left or up when it would go past the right or the bottom.
func (m *model) placeMenu(mu *ctxMenu, x, y int) (int, int) {
	w, h := mu.size()
	x = max(min(x, m.width-w), 0)
	y = max(min(y, m.height-1-h), 0)
	return x, y
}

// size is the menu's box, in cells.
func (mu *ctxMenu) size() (w, h int) {
	lw, kw := 0, 0
	for _, it := range mu.items {
		lw = max(lw, ansi.StringWidth(it.label))
		kw = max(kw, ansi.StringWidth(it.hint()))
	}
	return lw + kw + 8, len(mu.items) + 2
}

// hint is the item's key, with ▸ for a submenu.
func (it menuItem) hint() string {
	if it.sub != nil {
		return strings.TrimSpace(it.key + " ▸")
	}
	return it.key
}

func (it menuItem) pickable() bool { return !it.sep && !it.off }

// move moves the highlight to the next item that can be picked (delta 1)
// or the previous one (-1), wrapping around.
func (mu *ctxMenu) move(delta int) {
	n := len(mu.items)
	for i := 1; i <= n; i++ {
		j := ((mu.hi+delta*i)%n + n) % n
		if mu.items[j].pickable() {
			mu.hi = j
			return
		}
	}
}

// deepest is the open submenu furthest in, or the menu itself.
func (mu *ctxMenu) deepest() *ctxMenu {
	for mu.sub != nil {
		mu = mu.sub
	}
	return mu
}

// openSub opens item i's submenu beside the menu: to the right, or to the
// left when there's no room.
func (m *model) openSub(mu *ctxMenu, i int) {
	sub := &ctxMenu{items: mu.items[i].sub, hi: -1}
	sub.move(1)
	w, _ := mu.size()
	sw, _ := sub.size()
	x := mu.x + w - 1
	if x+sw > m.width {
		x = mu.x - sw + 1
	}
	sub.x, sub.y = m.placeMenu(sub, x, mu.y+i)
	mu.sub = sub
}

// pick runs item i of mu, or opens its submenu.
func (m *model) pick(mu *ctxMenu, i int) tea.Cmd {
	it := mu.items[i]
	if !it.pickable() {
		return nil
	}
	mu.hi = i
	if it.sub != nil {
		m.openSub(mu, i)
		return nil
	}
	m.menu = nil
	return it.run(m)
}

// menuKey handles a key while the menu is open. handled is false for a
// key that isn't the menu's: the menu closes and the key does what it
// does in the list.
func (m *model) menuKey(key string) (handled bool, cmd tea.Cmd) {
	cur := m.menu.deepest()
	parent := m.menu
	for parent.sub != nil && parent.sub != cur {
		parent = parent.sub
	}
	switch key {
	case "esc":
		if cur == m.menu {
			m.menu = nil
		} else {
			parent.sub = nil
		}
	case "up", "k", "shift+tab":
		cur.move(-1)
	case "down", "j", "tab":
		cur.move(1)
	case "left", "h":
		if cur != m.menu {
			parent.sub = nil
		}
	case "right", "l":
		if cur.hi >= 0 && cur.items[cur.hi].sub != nil {
			m.openSub(cur, cur.hi)
		}
	case "enter":
		if cur.hi >= 0 {
			return true, m.pick(cur, cur.hi)
		}
	default:
		m.menu = nil
		return false, nil
	}
	return true, nil
}

// itemAt is the item of mu at screen cell (x, y): its index, or -1 on the
// border or a separator; in is false outside the menu.
func (mu *ctxMenu) itemAt(x, y int) (i int, in bool) {
	w, h := mu.size()
	if x < mu.x || x >= mu.x+w || y < mu.y || y >= mu.y+h {
		return -1, false
	}
	i = y - mu.y - 1
	if i < 0 || i >= len(mu.items) {
		return -1, true
	}
	return i, true
}

// menuMouse handles the mouse while the menu is open: a left click on an
// item picks it, a click outside the menus or a right-click closes them.
func (m *model) menuMouse(msg tea.MouseMsg) tea.Cmd {
	click, ok := msg.(tea.MouseClickMsg)
	if !ok {
		return nil
	}
	if click.Button != tea.MouseLeft {
		m.menu = nil
		return nil
	}
	// The submenus are on top.
	var open []*ctxMenu
	for mu := m.menu; mu != nil; mu = mu.sub {
		open = append([]*ctxMenu{mu}, open...)
	}
	for _, mu := range open {
		if i, in := mu.itemAt(click.X, click.Y); in {
			if i >= 0 {
				mu.sub = nil
				return m.pick(mu, i)
			}
			return nil
		}
	}
	m.menu = nil
	return nil
}

var stMenuOff = lipgloss.NewStyle().Foreground(lipgloss.Color("#4E5575"))

// render draws the menu's box.
func (mu *ctxMenu) render() []string {
	w, _ := mu.size()
	inner := w - 4
	line := func(s string) string {
		return stBorder.Render("│") + " " + fit(s, inner) + " " + stBorder.Render("│")
	}
	out := []string{stBorder.Render("┌" + strings.Repeat("─", w-2) + "┐")}
	for i, it := range mu.items {
		if it.sep {
			out = append(out, stBorder.Render("├"+strings.Repeat("─", w-2)+"┤"))
			continue
		}
		hint := it.hint()
		gap := strings.Repeat(" ", max(inner-2-ansi.StringWidth(it.label)-ansi.StringWidth(hint), 1))
		switch {
		case it.off:
			out = append(out, line("  "+stMenuOff.Render(it.label+gap+hint)))
		case i == mu.hi:
			hi := lipgloss.NewStyle().Foreground(colAccent).Bold(true)
			out = append(out, stBorder.Render("│")+onBackground(" "+fit(hi.Render("› "+it.label)+gap+stMuted.Render(hint), inner)+" ", selBgSeq)+stBorder.Render("│"))
		default:
			out = append(out, line("  "+it.label+gap+stMuted.Render(hint)))
		}
	}
	return append(out, stBorder.Render("└"+strings.Repeat("─", w-2)+"┘"))
}

// overlayMenu draws the open menus over the screen's lines.
func (m *model) overlayMenu(screen string) string {
	lines := strings.Split(screen, "\n")
	for mu := m.menu; mu != nil; mu = mu.sub {
		for i, l := range mu.render() {
			y := mu.y + i
			if y < 0 || y >= len(lines) {
				continue
			}
			bw := ansi.StringWidth(l)
			lines[y] = ansi.Cut(lines[y], 0, mu.x) + "\x1b[0m" + l + "\x1b[0m" + ansi.Cut(lines[y], mu.x+bw, m.width)
		}
	}
	return strings.Join(lines, "\n")
}
