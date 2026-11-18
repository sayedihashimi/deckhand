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
// reads handleKey, newDialog.update, diffKey and the config screen's
// configKey, formKey and pickerKey, and fails for any key that isn't in the
// table. Give it a footer hint if it should show in the footer, and update
// README.md's Keys section.
//
// The help is context-aware: it opens with the group for where it was
// opened from (helpContextFor) first and in full, then the others.

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
	helpConfig   = "Config screen (c)"
)

// Footer conditions for the config screen's form and profile picker. Their
// hints show only in their own footer (footerOnly).
const (
	hintForm   = "form"
	hintPicker = "picker"
	hintTest   = "test" // the keep-or-discard question for a test session
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
		{keys: "d p m", desc: "diff; p: push, PR; m: merge", match: []string{"d", "p", "m"}, footer: "d diff", short: "-"},
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
		// The toggle and back keys are configurable ([list] toggle_key and
		// back_key); withListKeys shows the configured ones in place of F3
		// and F5.
		{keys: "F3", desc: "hide / show this list", match: []string{defaultToggleKey, defaultBackKey, "ctrl+q", "ctrl+]"}, footer: "F3 list"},
		{keys: "? F1", desc: "this help", match: []string{"?", "f1"}, footer: "? help"},
		{keys: "q", desc: "quit; Claude sessions resume", match: []string{"q", "ctrl+c"}, footer: "q quit"},
	}},
	{helpTerminal, []helpEntry{
		{keys: "F5 ctrl+q ctrl+]", desc: "back to the list", match: []string{defaultBackKey, "ctrl+q", "ctrl+]"}, footer: "F5 list"},
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
	// configKey, then the agent form (formKey) and the profile picker
	// (pickerKey). Hints with no when are the screen's own footer.
	{helpConfig, []helpEntry{
		{keys: "↑↓ tab", desc: "move (shift+tab back)", match: []string{"up", "down", "tab", "shift+tab"}, footer: "↑↓/tab move"},
		{keys: "enter e", desc: "edit agent; toggle, run row", match: []string{"enter", "e"}},
		{keys: "←→ space", desc: "change a choice (also h l)", match: []string{"left", "right", "h", "l", "space"}},
		{keys: "a", desc: "add an agent", match: []string{"a"}},
		{keys: "d del", desc: "delete the agent", match: []string{"d", "delete"}},
		{keys: "shift+↑↓", desc: "move the agent (also K J)", match: []string{"shift+up", "shift+down", "K", "J"}},
		{keys: "t ctrl+t", desc: "test it; ctrl+t: keep, discard", match: []string{"t", "ctrl+t"}},
		{keys: "ctrl+s", desc: "save and apply", match: []string{"ctrl+s"}, footer: "ctrl+s save"},
		{keys: "esc", desc: "leave without saving", match: []string{"esc"}, footer: "esc leave without saving"},
		{keys: "F1", desc: "this help", match: []string{"f1"}, footer: "F1 help"},
		{keys: "↑↓ tab", desc: "form: next / previous field", match: []string{"up", "down", "tab", "shift+tab"}, footer: "↑↓/tab move", when: hintForm},
		{keys: "enter esc", desc: "form: done / cancel", match: []string{"enter", "esc"}, footer: "enter done · esc cancel", when: hintForm},
		{keys: "ctrl+p", desc: "form: pick from your profile", match: []string{"ctrl+p"}, when: hintForm},
		{keys: "type", desc: "picker: filter the list", footer: "type to filter", when: hintPicker},
		{keys: "↑↓ enter", desc: "picker: choose, pick", match: []string{"up", "down", "tab", "shift+tab", "enter"}, footer: "↑↓ choose · enter pick", when: hintPicker},
		{keys: "esc", desc: "picker: back to the form", match: []string{"esc"}, footer: "esc back", when: hintPicker},
		{keys: "k x esc", desc: "test: keep, discard, not now", match: []string{"k", "x", "esc"}, footer: "k keep it as a session · x discard it · esc not now", when: hintTest},
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
	return joinHints(title, short, func(e helpEntry) bool {
		return e.when == "" || slices.Contains(show, e.when)
	})
}

// footerOnly joins the hints of a group's entries whose condition is
// exactly when: "" for the config screen itself, hintForm for its form.
func footerOnly(title, when string) string {
	return joinHints(title, false, func(e helpEntry) bool { return e.when == when })
}

func joinHints(title string, short bool, keep func(helpEntry) bool) string {
	var parts []string
	for _, e := range helpGroupByTitle(title).entries {
		if !keep(e) {
			continue
		}
		h := e.footer
		if short && e.short != "" {
			h = e.short
		}
		if h != "" && h != "-" {
			parts = append(parts, withListKeys(h))
		}
	}
	return " " + strings.Join(parts, " · ")
}

// withListKeys puts the configured toggle and back keys in place of the
// defaults (F3 and F5) in a help text. It's one pass, so swapped keys stay
// swapped.
func withListKeys(s string) string {
	return strings.NewReplacer(
		keyLabel(defaultToggleKey), keyLabel(listOpts.toggleKey),
		keyLabel(defaultBackKey), keyLabel(listOpts.back()),
	).Replace(s)
}

// helpOverlay is the open help panel.
type helpOverlay struct {
	context    string // the group shown first: where it was opened from
	scroll     int    // first content line shown, when it doesn't fit
	scrollable bool   // it didn't fit when last drawn
}

func (m *model) openHelp() { m.help = &helpOverlay{context: m.helpContextFor()} }

// helpContextFor is the help group for where the keys go now: the config
// screen, the new-session dialog, a session or the list.
func (m *model) helpContextFor() string {
	switch m.focus {
	case focusConfig:
		return helpConfig
	case focusDialog:
		return helpDialog
	case focusTerminal:
		return helpTerminal
	}
	return helpList
}

// helpOrder is helpGroups with the context's group first.
func helpOrder(context string) []helpGroup {
	out := make([]helpGroup, 0, len(helpGroups))
	for _, g := range helpGroups {
		if g.title == context {
			out = append(out, g)
		}
	}
	for _, g := range helpGroups {
		if g.title != context {
			out = append(out, g)
		}
	}
	return out
}

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
	w := 0
	for _, g := range groups {
		keyW, descW := 0, 0
		for _, e := range g.entries {
			keyW = max(keyW, ansi.StringWidth(e.keys))
			descW = max(descW, ansi.StringWidth(e.desc))
		}
		w = max(w, keyW+2+descW, ansi.StringWidth(g.title))
	}
	return w
}

// helpColumn renders groups as "key  description" rows, width cells wide,
// each group with its keys lined up. Long descriptions wrap under
// themselves.
func helpColumn(groups []helpGroup, width int) []string {
	out, _ := helpColumnEnds(groups, width)
	return out
}

// helpColumnEnds is helpColumn, and the line after each group's last.
func helpColumnEnds(groups []helpGroup, width int) ([]string, []int) {
	var out []string
	var ends []int
	for i, g := range groups {
		if i > 0 {
			out = append(out, fit("", width))
		}
		keyW := 0
		for _, e := range g.entries {
			keyW = max(keyW, ansi.StringWidth(e.keys))
		}
		keyW += 2
		descW := max(width-keyW, 8)
		out = append(out, fit(stHelpGroup.Render(g.title), width))
		for _, e := range g.entries {
			for j, l := range strings.Split(ansi.Wrap(e.desc, descW, ""), "\n") {
				k := ""
				if j == 0 {
					k = withListKeys(e.keys)
				}
				out = append(out, fit(stHelpKey.Render(fit(k, keyW))+l, width))
			}
		}
		ends = append(ends, len(out))
	}
	return out, ends
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

// helpLayout is the overlay's content: its lines, and the line after each
// key group's last, so the scroll line can say how many groups are below.
type helpLayout struct {
	lines []string
	ends  []int
}

// helpGap is the space between the two columns, and helpMinCol the
// narrowest a column gets.
const (
	helpGap    = 3
	helpMinCol = 32
)

// addColumns appends groups side by side, in columns w[0] and w[1] wide.
func (l *helpLayout) addColumns(left, right []helpGroup, w [2]int) {
	top := len(l.lines)
	ll, le := helpColumnEnds(left, w[0])
	rl, re := helpColumnEnds(right, w[1])
	for i := 0; i < max(len(ll), len(rl)); i++ {
		a, b := strings.Repeat(" ", w[0]), ""
		if i < len(ll) {
			a = ll[i]
		}
		if i < len(rl) {
			b = rl[i]
		}
		l.lines = append(l.lines, a+strings.Repeat(" ", helpGap)+b)
	}
	for _, e := range append(le, re...) {
		l.ends = append(l.ends, top+e)
	}
}

// addColumn appends groups one under another, width wide.
func (l *helpLayout) addColumn(groups []helpGroup, width int) {
	top := len(l.lines)
	lines, ends := helpColumnEnds(groups, width)
	l.lines = append(l.lines, lines...)
	for _, e := range ends {
		l.ends = append(l.ends, top+e)
	}
}

// packHelp splits groups into two columns w[0] and w[1] wide, each at most
// limit lines tall (no limit when it's 0). The first group goes left; each
// other one goes in the shorter column, or the other if it doesn't fit
// there. Groups that fit in neither are left over, in order.
func packHelp(groups []helpGroup, w [2]int, limit int) (left, right, rest []helpGroup) {
	var h [2]int
	cols := [2]*[]helpGroup{&left, &right}
	for i, g := range groups {
		add := func(c int) int {
			n := len(helpColumn([]helpGroup{g}, w[c]))
			if h[c] > 0 {
				n++ // the blank line between groups
			}
			return n
		}
		fits := func(c int) bool { return i == 0 || limit == 0 || h[c]+add(c) <= limit }
		c := 0
		if i > 0 && h[1] < h[0] {
			c = 1
		}
		if !fits(c) {
			c = 1 - c
		}
		if !fits(c) {
			rest = append(rest, g)
			continue
		}
		h[c] += add(c)
		*cols[c] = append(*cols[c], g)
	}
	return left, right, rest
}

// helpContent is the overlay's lines, inner cells wide, for a panel with
// room for rows lines. The context's group comes first, in full: in the
// left column, sized to it, when there's room for two. The others follow in
// two columns as far as they fit above the scroll line, and the rest come
// after, to scroll to.
func (m *model) helpContent(inner, rows int) helpLayout {
	context := helpList
	if m.help != nil && m.help.context != "" {
		context = m.help.context
	}
	title := stTitle.Render("Keys") + stMuted.Render(" · "+context+"   esc, ? or F1 closes")
	l := helpLayout{lines: []string{title, ""}}
	groups := helpOrder(context)
	tail := m.helpTail(inner)
	if inner-helpGap < 2*helpMinCol {
		l.addColumn(groups, inner)
	} else {
		// The context's group gets the width it needs, the right column
		// what the widest other group needs, and the left any extra.
		ctxW := min(max(helpColumnWidth(groups[:1]), helpMinCol), inner-helpGap-helpMinCol)
		rightW := min(max(helpColumnWidth(groups[1:]), helpMinCol), inner-helpGap-ctxW)
		w := [2]int{inner - helpGap - rightW, rightW}
		left, right, _ := packHelp(groups, w, 0)
		tall := max(len(helpColumn(left, w[0])), len(helpColumn(right, w[1])))
		if len(l.lines)+tall+len(tail) <= rows {
			l.addColumns(left, right, w) // it all fits
		} else {
			// As many groups as fit above the scroll line, then the rest.
			var rest []helpGroup
			left, right, rest = packHelp(groups, w, rows-1-len(l.lines))
			l.addColumns(left, right, w)
			if len(rest) > 0 {
				left, right, _ = packHelp(rest, w, 0)
				l.lines = append(l.lines, "")
				l.addColumns(left, right, w)
			}
		}
	}
	l.lines = append(l.lines, tail...)
	for i := range l.lines {
		l.lines[i] = fit(l.lines[i], inner)
	}
	return l
}

// helpTail is what follows the keys: the status icons and the config path.
func (m *model) helpTail(inner int) []string {
	out := []string{"", stHelpGroup.Render("Status icons")}
	out = append(out, m.statusLegend(inner)...)
	return append(out, "", stMuted.Render("Config file: ")+configPath()+stMuted.Render("  (c edits it here; deckhand config creates it)"))
}

// overlayHelp draws the help panel over the main pane's lines (w×h),
// dimming what's behind it. When it doesn't fit it scrolls.
func (m *model) overlayHelp(behind []string, w, h int) []string {
	pw := min(w-2, 100)
	if pw < 20 {
		pw = w
	}
	inner := max(pw-4, 1) // border and one space of padding each side
	rows := max(h-2, 1)   // room inside the border
	layout := m.helpContent(inner, rows)
	content := layout.lines

	m.help.scrollable = len(content) > rows
	if m.help.scrollable {
		// Scroll, keeping the last row for "↑↓ scroll".
		view := max(rows-1, 1)
		m.help.scroll = min(m.help.scroll, len(content)-view)
		end := m.help.scroll + view
		more := stMuted.Render("↑↓ scroll")
		groups := 0 // key groups below the view, or cut off by it
		for _, e := range layout.ends {
			if e > end {
				groups++
			}
		}
		switch n := len(content) - end; {
		case groups == 1:
			more = stMuted.Render("↓ 1 more group · ↑↓ scroll")
		case groups > 1:
			more = stMuted.Render(fmt.Sprintf("↓ %d more groups · ↑↓ scroll", groups))
		case n > 0:
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
