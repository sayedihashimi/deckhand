package main

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The help overlay (? in the list, F1 anywhere) and the footer hints are
// built from one table, helpGroups, so they can't drift apart.
//
// Adding a key: add a helpEntry to the right group, with every key string
// it handles (tea.KeyPressMsg.String()) in match. TestHelpCoversHandledKeys
// reads handleKey and newDialog.update and fails for any key that isn't in
// the table. Give it a footer hint if it should show in the footer, and
// update README.md's Keys section.

type helpEntry struct {
	keys  string   // as shown in the overlay: "↑↓ j k"
	desc  string   // what it does
	match []string // the key strings it handles, for the test
	// footer is the entry's hint in the footer ("n new"); empty leaves it
	// out. short replaces it when the full footer doesn't fit; "-" leaves
	// it out of the short footer.
	footer, short string
	// when names a condition the footer hint needs ("history"); the hint
	// shows only when footerHints is given it.
	when string
}

type helpGroup struct {
	title   string
	entries []helpEntry
}

const (
	helpList     = "Session list"
	helpTerminal = "Inside a session"
	helpDiff     = "Diff preview (d)"
	helpMouse    = "Mouse"
	helpDialog   = "New-session dialog"
)

var helpGroups = []helpGroup{
	{helpList, []helpEntry{
		{keys: "↑↓ j k", desc: "select a session", match: []string{"up", "k", "down", "j"}, footer: "↑↓ select", short: "↑↓"},
		{keys: "home end", desc: "first / last (also g, G)", match: []string{"home", "g", "end", "G"}},
		{keys: "enter", desc: "open (starts or resumes it)", match: []string{"enter", "right", "l"}, footer: "enter open", short: "⏎ open"},
		{keys: "shift+enter", desc: "new session, same folder", match: []string{"shift+enter", "alt+enter"}, footer: "⇧⏎ new here", short: "⇧⏎ here"},
		{keys: "n W", desc: "new session; W: new worktree", match: []string{"n", "W"}, footer: "n new"},
		{keys: "1–9", desc: "jump into session N", match: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}},
		{keys: "tab", desc: "next one that needs you", match: []string{"tab"}, footer: "tab next"},
		{keys: "d p", desc: "diff; p: push and open a PR", match: []string{"d", "p"}, footer: "d diff", short: "-"},
		{keys: "v", desc: "last output / conversation", match: []string{"v"}, footer: "v switch view", short: "v view", when: hintHistory},
		{keys: "⇧PgUp/Dn", desc: "scroll an exited session", match: []string{"shift+pgup", "shift+pgdown"}},
		{keys: "/", desc: "filter the list as you type", match: []string{"/"}, footer: "/ filter", short: "/@ filter"},
		{keys: "@", desc: "only sessions that need you", match: []string{"@"}, footer: "@ needs you", short: "-"},
		{keys: "esc", desc: "clear the filter", match: []string{"esc"}},
		{keys: "o O t", desc: "editor / Explorer / terminal", match: []string{"o", "O", "t"}, footer: "o editor · O explorer · t terminal", short: "o/O/t open"},
		{keys: "r", desc: "rename", match: []string{"r"}, footer: "r rename"},
		{keys: "c", desc: "settings (config screen)", match: []string{"c"}, footer: "c config"},
		{keys: "K", desc: "end it (it stays in the list)", match: []string{"K"}},
		{keys: "x del", desc: "end and remove from the list", match: []string{"x", "delete"}},
		// The toggle key is configurable ([list] toggle_key); withToggleKey
		// shows the configured one in place of F3.
		{keys: "F3", desc: "hide / show this list", match: []string{defaultToggleKey, "ctrl+q", "ctrl+]"}, footer: "F3 list"},
		{keys: "? F1", desc: "this help", match: []string{"?", "f1"}, footer: "? help"},
		{keys: "q", desc: "quit; Claude sessions resume", match: []string{"q", "ctrl+c"}, footer: "q quit"},
	}},
	{helpTerminal, []helpEntry{
		{keys: "ctrl+q ctrl+]", desc: "back to the list", match: []string{"ctrl+q", "ctrl+]"}, footer: "ctrl+q back to the list"},
		{keys: "shift+PgUp/Dn", desc: "scroll back a page", match: []string{"shift+pgup", "shift+pgdown"}},
		{keys: "F3", desc: "hide / show the list", match: []string{defaultToggleKey}, footer: "F3 hide list"},
		{keys: "F1", desc: "this help", match: []string{"f1"}, footer: "F1 help"},
	}},
	{helpDiff, []helpEntry{
		{keys: "n N", desc: "next / previous file", match: []string{"n", "N", "up", "k", "down", "j", "pgup", "shift+pgup", "pgdown", "shift+pgdown", "space", "home", "g", "end", "G"}},
		{keys: "esc d q", desc: "close", match: []string{"esc", "q", "d"}},
	}},
	{helpMouse, []helpEntry{
		{keys: "wheel", desc: "scroll output or list"},
		{keys: "click", desc: "select, focus terminal"},
		{keys: "shift+drag", desc: "select text"},
	}},
	{helpDialog, []helpEntry{
		{keys: "tab ↑↓", desc: "complete, pick a folder", match: []string{"tab", "up", "down"}},
		{keys: "ctrl+w", desc: "up a level", match: []string{"ctrl+w", "ctrl+backspace", "alt+backspace"}},
		{keys: "shift+tab", desc: "agent row, ←→ to change", match: []string{"shift+tab", "left", "h", "right", "l", "space"}},
		{keys: "ctrl+t", desc: "worktree on / off", match: []string{"ctrl+t"}},
		{keys: "enter", desc: "start the session", match: []string{"enter"}},
		{keys: "esc", desc: "cancel", match: []string{"esc"}},
	}},
}

func helpGroupByTitle(title string) helpGroup {
	for _, g := range helpGroups {
		if g.title == title {
			return g
		}
	}
	return helpGroup{}
}

// hintHistory is the condition for the v hint: the selected session can
// switch between its last output and the conversation.
const hintHistory = "history"

// footerHints joins a group's footer hints: " ↑↓ select · enter open · ...".
// Hints with a condition show only when it's in show.
func footerHints(title string, short bool, show ...string) string {
	var parts []string
	for _, e := range helpGroupByTitle(title).entries {
		if e.when != "" && !slices.Contains(show, e.when) {
			continue
		}
		h := e.footer
		if short && e.short != "" {
			h = e.short
		}
		if h != "" && h != "-" {
			parts = append(parts, withToggleKey(h))
		}
	}
	return " " + strings.Join(parts, " · ")
}

// withToggleKey puts the configured toggle key in place of the default
// (F3) in a help text.
func withToggleKey(s string) string {
	return strings.ReplaceAll(s, keyLabel(defaultToggleKey), keyLabel(listOpts.toggleKey))
}

// helpOverlay is the open help panel.
type helpOverlay struct {
	scroll     int  // first content line shown, when it doesn't fit
	scrollable bool // it didn't fit when last drawn
}

func (m *model) openHelp() { m.help = &helpOverlay{} }

// helpKey handles a key while the help is open: esc, ?, F1 or q close it
// (focus is untouched, so it goes back to where it was), ↑↓ scroll it, and
// everything else is ignored.
func (m *model) helpKey(key string) {
	switch key {
	case "esc", "?", "f1", "q":
		m.help = nil
	case "up", "k":
		m.help.scroll--
	case "down", "j":
		m.help.scroll++
	case "pgup":
		m.help.scroll -= 10
	case "pgdown", "space":
		m.help.scroll += 10
	case "home", "g":
		m.help.scroll = 0
	case "end", "G":
		m.help.scroll = 1 << 20 // clamped when drawn
	}
	if m.help != nil && m.help.scroll < 0 {
		m.help.scroll = 0
	}
}

var (
	stHelpKey   = lipgloss.NewStyle().Foreground(colAccent)
	stHelpGroup = lipgloss.NewStyle().Bold(true)
)

// helpColumnWidth is the width helpColumn needs to show groups without
// wrapping.
func helpColumnWidth(groups []helpGroup) int {
	keyW, descW, titleW := 0, 0, 0
	for _, g := range groups {
		titleW = max(titleW, ansi.StringWidth(g.title))
		for _, e := range g.entries {
			keyW = max(keyW, ansi.StringWidth(e.keys))
			descW = max(descW, ansi.StringWidth(e.desc))
		}
	}
	return max(keyW+2+descW, titleW)
}

// helpColumn renders groups as "key  description" rows, inner cells wide.
// Long descriptions wrap under themselves.
func helpColumn(groups []helpGroup, width int) []string {
	keyW := 0
	for _, g := range groups {
		for _, e := range g.entries {
			keyW = max(keyW, ansi.StringWidth(e.keys))
		}
	}
	keyW += 2
	descW := max(width-keyW, 8)
	var out []string
	for i, g := range groups {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, fit(stHelpGroup.Render(g.title), width))
		for _, e := range g.entries {
			for j, l := range strings.Split(ansi.Wrap(e.desc, descW, ""), "\n") {
				k := ""
				if j == 0 {
					k = withToggleKey(e.keys)
				}
				out = append(out, fit(stHelpKey.Render(fit(k, keyW))+l, width))
			}
		}
	}
	return out
}

// statusLegend is the status icons with their meanings, wrapped to width.
func (m *model) statusLegend(width int) []string {
	icons := []struct{ icon, text string }{
		{lipgloss.NewStyle().Foreground(colWorking).Render(spinner[2]), "working"},
		{lipgloss.NewStyle().Foreground(colWaiting).Bold(true).Render("●"), "needs you"},
		{lipgloss.NewStyle().Foreground(colIdle).Bold(true).Render("✓"), "finished (unseen)"},
		{lipgloss.NewStyle().Foreground(colIdle).Render("○"), "idle"},
		{stMuted.Render("■"), "not running"},
		{stMuted.Render("◌"), "starting"},
	}
	var out []string
	line := ""
	for _, ic := range icons {
		piece := ic.icon + " " + ic.text
		if line != "" && ansi.StringWidth(line)+2+ansi.StringWidth(piece) > width {
			out = append(out, fit(line, width))
			line = ""
		}
		if line != "" {
			line += "  "
		}
		line += piece
	}
	return append(out, fit(line, width))
}

// helpContent is the overlay's lines, inner cells wide: two columns (the
// session list on the left) when there's room, otherwise one.
func (m *model) helpContent(inner int) []string {
	title := stTitle.Render("Keys") + stMuted.Render("   esc, ? or F1 closes")
	out := []string{fit(title, inner), ""}
	const gap = 3
	// The right column gets what it needs, the session list the rest.
	rightW := min(helpColumnWidth(helpGroups[1:]), (inner-gap)/2)
	if colW := inner - gap - rightW; rightW >= 30 && colW >= 36 {
		left := helpColumn(helpGroups[:1], colW)
		right := helpColumn(helpGroups[1:], rightW)
		for i := 0; i < max(len(left), len(right)); i++ {
			l, r := strings.Repeat(" ", colW), ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			out = append(out, fit(l+strings.Repeat(" ", gap)+r, inner))
		}
	} else {
		out = append(out, helpColumn(helpGroups, inner)...)
	}
	out = append(out, "", fit(stHelpGroup.Render("Status icons"), inner))
	out = append(out, m.statusLegend(inner)...)
	out = append(out, "", fit(stMuted.Render("Config file: ")+configPath()+stMuted.Render("  (c edits it here; deckhand config creates it)"), inner))
	return out
}

// overlayHelp draws the help panel over the main pane's lines (w×h),
// dimming what's behind it. When it doesn't fit it scrolls.
func (m *model) overlayHelp(behind []string, w, h int) []string {
	pw := min(w-2, 100)
	if pw < 20 {
		pw = w
	}
	inner := max(pw-4, 1) // border and one space of padding each side
	content := m.helpContent(inner)

	rows := max(h-2, 1) // room inside the border
	m.help.scrollable = len(content) > rows
	if m.help.scrollable {
		// Scroll, keeping the last row for "↑↓ scroll".
		view := max(rows-1, 1)
		m.help.scroll = min(m.help.scroll, len(content)-view)
		end := m.help.scroll + view
		more := stMuted.Render("↑↓ scroll")
		if n := len(content) - end; n > 0 {
			more = stMuted.Render(fmt.Sprintf("↓ %d more lines · ↑↓ scroll", n))
		}
		content = append(content[m.help.scroll:end:end], fit(more, inner))
	} else {
		m.help.scroll = 0
	}

	border := lipgloss.NewStyle().Foreground(colAccent)
	panel := make([]string, 0, len(content)+2)
	panel = append(panel, border.Render("╭"+strings.Repeat("─", pw-2)+"╮"))
	for _, l := range content {
		panel = append(panel, border.Render("│")+" "+fit(l, inner)+"\x1b[0m "+border.Render("│"))
	}
	panel = append(panel, border.Render("╰"+strings.Repeat("─", pw-2)+"╯"))

	left := (w - pw) / 2
	top := max((h-len(panel))/2, 0)
	out := make([]string, h)
	for y := 0; y < h; y++ {
		back := ""
		if y < len(behind) {
			back = ansi.Strip(behind[y])
		}
		back = fit(back, w)
		if y < top || y >= top+len(panel) {
			out[y] = stMuted.Faint(true).Render(back)
			continue
		}
		out[y] = stMuted.Faint(true).Render(ansi.Cut(back, 0, left)) + "\x1b[0m" +
			panel[y-top] + "\x1b[0m" +
			stMuted.Faint(true).Render(ansi.Cut(back, left+pw, w))
	}
	return out
}

// helpFooter is the footer while the help is open.
func (m *model) helpFooter() string {
	if m.help.scrollable {
		return " ↑↓ scroll · esc, ? or F1 close"
	}
	return " esc, ? or F1 close"
}

// helpMouse scrolls the help with the wheel; other mouse events are ignored
// while it's open.
func (m *model) helpMouse(msg tea.MouseMsg) {
	if _, ok := msg.(tea.MouseWheelMsg); !ok {
		return
	}
	switch msg.Mouse().Button {
	case tea.MouseWheelUp:
		m.helpKey("up")
	case tea.MouseWheelDown:
		m.helpKey("down")
	}
}
