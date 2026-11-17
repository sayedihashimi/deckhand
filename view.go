package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

var (
	colAccent  = lipgloss.Color("#7AA2F7")
	colWorking = lipgloss.Color("#E0AF68")
	colWaiting = lipgloss.Color("#FF7A45")
	colIdle    = lipgloss.Color("#9ECE6A")
	colError   = lipgloss.Color("#F7768E")
	colMuted   = lipgloss.Color("#737AA2")
	colSelBg   = lipgloss.Color("#2A2F45")

	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stMuted    = lipgloss.NewStyle().Foreground(colMuted)
	stBorder   = lipgloss.NewStyle().Foreground(lipgloss.Color("#3B4261"))
	stWaitText = lipgloss.NewStyle().Bold(true).Foreground(colWaiting)
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *model) statusIcon(sn snapshot) string {
	switch sn.status {
	case StatusWorking:
		return lipgloss.NewStyle().Foreground(colWorking).Render(spinner[(m.frame/2)%len(spinner)])
	case StatusWaiting:
		// Blink between two shapes so it catches the eye.
		ch := "●"
		if (m.frame/10)%2 == 1 {
			ch = "◉"
		}
		return lipgloss.NewStyle().Foreground(colWaiting).Bold(true).Render(ch)
	case StatusIdle:
		if sn.unread {
			return lipgloss.NewStyle().Foreground(colIdle).Bold(true).Render("✓")
		}
		return lipgloss.NewStyle().Foreground(colIdle).Render("○")
	case StatusError:
		return lipgloss.NewStyle().Foreground(colError).Render("✗")
	case StatusExited:
		return stMuted.Render("■")
	}
	return stMuted.Render("◌")
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// fit truncates or pads a styled string to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func (m *model) renderSidebar(w, h int) []string {
	lines := make([]string, 0, h)
	waiting := 0
	for _, s := range m.sessions {
		if s.Snapshot().status == StatusWaiting {
			waiting++
		}
	}
	head := stTitle.Render(" deckhand")
	switch {
	case m.filterInput != nil:
		head = " " + m.filterInput.View()
	case m.filtering():
		var parts []string
		if m.attentionOnly {
			parts = append(parts, "@ needs you")
		}
		if m.filter != "" {
			parts = append(parts, "/"+m.filter)
		}
		head = " " + lipgloss.NewStyle().Foreground(colAccent).Render(strings.Join(parts, " ")) + stMuted.Render(" · esc clears")
	}
	if waiting > 0 && m.filterInput == nil {
		badge := stWaitText.Render(fmt.Sprintf("%d need you ", waiting))
		head = fit(head, w-ansi.StringWidth(badge)) + badge
	}
	lines = append(lines, fit(head, w), stBorder.Render(strings.Repeat("─", w)))

	vis := m.visible()
	switch {
	case len(m.sessions) == 0:
		lines = append(lines, strings.Repeat(" ", w), fit(stMuted.Render(" No sessions yet."), w), fit(stMuted.Render(" Press n to start one."), w))
	case len(vis) == 0:
		lines = append(lines, strings.Repeat(" ", w), fit(stMuted.Render(" No sessions match."), w), fit(stMuted.Render(" esc clears the filter."), w))
	}

	// Three lines per session; keep the selection in view.
	visible := (h - len(lines)) / rowsPerSession
	if visible < 1 {
		visible = 1
	}
	if p := visiblePos(vis, m.sel); p >= 0 {
		if p < m.scroll {
			m.scroll = p
		}
		if p >= m.scroll+visible {
			m.scroll = p - visible + 1
		}
	}
	if m.scroll > len(vis)-1 {
		m.scroll = max(0, len(vis)-1)
	}
	now := time.Now()
	for p := m.scroll; p < len(vis) && p < m.scroll+visible; p++ {
		i := vis[p]
		s := m.sessions[i]
		sn := s.Snapshot()
		selected := i == m.sel

		age := ""
		if !sn.since.IsZero() {
			age = shortAge(now.Sub(sn.since))
		}
		nameStyle := lipgloss.NewStyle()
		if sn.unread || sn.status == StatusWaiting {
			nameStyle = nameStyle.Bold(true)
		}
		num := " "
		if p < 9 {
			num = fmt.Sprintf("%d", p+1)
		}
		left := stMuted.Render(num) + " " + m.statusIcon(sn) + " " + nameStyle.Render(s.Name)
		row1 := fit(left, w-len(age)-1) + stMuted.Render(age) + " "

		detail, isReply := sessionDetail(sn)
		if sn.status == StatusExited {
			detail = exitedDetail(detail, w-6-ansi.StringWidth(s.Agent+" · "))
		}
		detailStyle := stMuted
		switch {
		case sn.status == StatusWaiting:
			detailStyle = stWaitText
		case isReply && sn.unread:
			detailStyle = stMuted.Bold(true)
		}
		row2 := fit("     "+stMuted.Render(s.Agent+" · ")+detailStyle.Render(detail), w)
		row3 := fit("     "+infoLine(s, sn.info, w-6), w)

		if selected {
			marker := lipgloss.NewStyle().Foreground(colAccent).Render("▌")
			if m.focus == focusTerminal {
				marker = lipgloss.NewStyle().Foreground(colMuted).Render("▌")
			}
			row1 = marker + onBackground(fit(left, w-len(age)-2)+stMuted.Render(age)+" ", selBgSeq)
			row2 = marker + onBackground(fit("    "+stMuted.Render(s.Agent+" · ")+detailStyle.Render(detail), w-1), selBgSeq)
			row3 = marker + onBackground(fit("    "+infoLine(s, sn.info, w-6), w-1), selBgSeq)
		}
		lines = append(lines, row1, row2, row3)
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:h]
}

// exitedDetail adds the shift+enter hint to an exited session's detail
// ("exited · ⏎ resume · ⇧⏎ new"), dropping "exited" and then the separator
// when the sidebar is too narrow. With no room for it, the detail is kept.
func exitedDetail(detail string, room int) string {
	if !strings.Contains(detail, "⏎") {
		return detail
	}
	short := strings.TrimPrefix(detail, "exited · ")
	for _, d := range []string{detail + " · ⇧⏎ new", short + " · ⇧⏎ new", short + " ⇧⏎ new"} {
		if ansi.StringWidth(d) <= room {
			return d
		}
	}
	return detail
}

// sessionDetail is the text after "claude · " on a session's second
// sidebar line. A finished turn shows the start of the reply instead of
// "your turn" (the ✓ already says it's your turn), and a permission prompt
// shows what the tool wants to do ("Bash: npm test"). isReply is true for
// the reply, which is bold while unread.
func sessionDetail(sn snapshot) (text string, isReply bool) {
	const perm = "permission: "
	switch {
	case len(sn.reply) > 0:
		return sn.reply[0], true
	case sn.status == StatusWaiting && strings.HasPrefix(sn.detail, perm) && sn.summary != "":
		tool := strings.TrimPrefix(sn.detail, perm)
		if tool == "" {
			tool = "permission"
		}
		return tool + ": " + sn.summary, false
	case sn.detail != "":
		return sn.detail, false
	}
	return sn.status.String(), false
}

// selBgSeq is the SGR sequence for the selected row's background (colSelBg).
const selBgSeq = "\x1b[48;2;42;47;69m"

// onBackground puts an already-styled string on a background. Wrapping it
// in another lipgloss style doesn't work: each inner piece ends with a
// reset, which also clears the outer background, so only parts of the row
// were highlighted. This sets the background again after every reset.
func onBackground(s, bgSeq string) string {
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+bgSeq)
	s = strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+bgSeq)
	return bgSeq + s + "\x1b[m"
}

// rowsPerSession is how many sidebar lines each session takes.
const rowsPerSession = 3

var stBranch = lipgloss.NewStyle().Foreground(lipgloss.Color("#BB9AF7"))

// infoLine is a session's third sidebar line: the git branch, then the
// Claude model and context size ("main* · opus 5.5 · 84k"). The branch is
// shortened first so the model and context stay visible. Outside git and
// Claude it's the folder.
func infoLine(s *Session, in sessionInfo, w int) string {
	var right []string
	if in.model != "" {
		right = append(right, shortModel(in.model))
	}
	if in.ctxTokens > 0 {
		right = append(right, shortTokens(in.ctxTokens))
	}
	tail := strings.Join(right, " · ")
	branch := gitLabel(in.git)
	if branch == "" {
		if tail != "" {
			return stMuted.Render(tail)
		}
		return stMuted.Render(shortDir(s.Dir, w))
	}
	if tail == "" {
		return stBranch.Render(clip(branch, max(w, 2)))
	}
	room := w - ansi.StringWidth(tail) - 3
	if room < 4 {
		room = 4
	}
	return stBranch.Render(clip(branch, room)) + stMuted.Render(" · "+tail)
}

// shortDir shows a folder in w cells, keeping its end ("…\src\api").
func shortDir(dir string, w int) string {
	if home, _ := os.UserHomeDir(); home != "" && len(dir) >= len(home) && strings.EqualFold(dir[:len(home)], home) {
		dir = "~" + dir[len(home):]
	}
	r := []rune(dir)
	if w > 1 && len(r) > w {
		return "…" + string(r[len(r)-w+1:])
	}
	return dir
}

// renderMain returns the session header and terminal (or dialog) lines.
func (m *model) renderMain(w, h int) ([]string, *tea.Cursor) {
	lines := make([]string, 0, h)
	if m.dialog != nil {
		lines = append(lines, fit(stTitle.Render(" New session"), w))
		lines = append(lines, m.renderDialog(w)...)
		for len(lines) < h {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return lines[:h], nil
	}

	s := m.current()
	if s == nil && len(m.sessions) > 0 {
		lines = append(lines, "", "", fit(stMuted.Render("  No session selected. esc clears the filter."), w))
		for len(lines) < h {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return lines[:h], nil
	}
	if s == nil {
		lines = append(lines, "")
		for _, l := range []string{
			"",
			"  Welcome to deckhand.",
			"",
			"  n        start a session (claude, codex, gemini or a shell)",
			"  ↑/↓      pick a session; its terminal shows here",
			"  enter    type into it; ctrl+q comes back to the list",
			"  tab      jump to the next session that needs you",
			"  / and @  filter the list; @ shows only sessions that need you",
			"  ? or F1  all the keys",
		} {
			lines = append(lines, fit(stMuted.Render(l), w))
		}
		for len(lines) < h {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return lines[:h], nil
	}

	sn := s.Snapshot()
	off := s.ScrollOffset()
	// Short details first, so a long folder path is what gets cut off.
	header := " " + m.statusIcon(sn) + " " + lipgloss.NewStyle().Bold(true).Render(s.Name) +
		stMuted.Render("  "+s.Agent)
	if b := gitLabel(sn.info.git); b != "" {
		header += "  " + stBranch.Render(b)
	}
	if sn.info.model != "" {
		header += stMuted.Render("  " + shortModel(sn.info.model) + " · " + shortTokens(sn.info.ctxTokens) + " context")
	}
	if len(sn.reply) > 0 {
		// Before the folder: real paths are long and would push the reply
		// out of view. Whatever doesn't fit is cut off.
		header += stMuted.Render("  · " + strings.Join(sn.reply, " "))
	}
	header += stMuted.Render("  " + s.Dir)
	if !sn.running && sn.exitInfo != "" {
		verb := "restart"
		if a, ok := agentByName(s.Agent); ok && a.CanResume() {
			verb = "resume"
		}
		header += lipgloss.NewStyle().Foreground(colWorking).Render("  " + sn.exitInfo + " · press enter to " + verb + ", shift+enter to start a new one")
	} else if sn.title != "" {
		header += stMuted.Render("  — " + sn.title)
	}
	if off > 0 {
		header = lipgloss.NewStyle().Foreground(colWorking).Bold(true).
			Render(fmt.Sprintf(" ↑ %d lines back", off)) +
			stMuted.Render(" · wheel down, shift+PgDn or any key to return") +
			stMuted.Render("   "+s.Name)
	}
	lines = append(lines, fit(header, w))

	emu := s.Emulator()
	if emu == nil {
		msg := "  Not running. Press enter to start it, or shift+enter to start a new one."
		if a, ok := agentByName(s.Agent); ok && a.CanResume() && s.Started {
			msg = "  Not running. Press enter to resume this Claude conversation, or shift+enter to start a new one."
		}
		lines = append(lines, "", fit(stMuted.Render(msg), w))
		for len(lines) < h {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return lines[:h], nil
	}

	screen := strings.Split(strings.ReplaceAll(emu.Render(), "\r\n", "\n"), "\n")
	if off > 0 {
		// The view is the scrollback followed by the screen; show h-1 rows
		// ending `off` lines above the bottom.
		sb := emu.ScrollbackLen()
		top := sb - off
		for i := 0; i < h-1; i++ {
			row := top + i
			l := ""
			switch {
			case row < 0:
			case row < sb:
				l = scrollbackLine(emu, row)
			case row-sb < len(screen):
				l = screen[row-sb]
			}
			lines = append(lines, fit(l, w)+"\x1b[0m")
		}
		return lines, nil
	}
	for i := 0; i < h-1; i++ {
		l := ""
		if i < len(screen) {
			l = screen[i]
		}
		lines = append(lines, fit(l, w)+"\x1b[0m")
	}

	var cur *tea.Cursor
	if m.focus == focusTerminal && sn.running && !emu.CursorHidden() {
		p := emu.CursorPosition()
		cur = tea.NewCursor(m.sidebarWidth()+1+p.X, 1+p.Y)
	}
	return lines, cur
}

// scrollbackLine renders one line of the emulator's scrollback with styles.
func scrollbackLine(emu *vt.SafeEmulator, row int) string {
	w := emu.Width()
	line := make(uv.Line, w)
	for x := 0; x < w; x++ {
		if c := emu.ScrollbackCellAt(x, row); c != nil {
			line[x] = *c
		} else {
			line[x] = uv.EmptyCell
		}
	}
	return line.Render()
}

func (m *model) renderDialog(w int) []string {
	d := m.dialog
	label := func(field int, s string) string {
		if d.field == field {
			return lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("› " + s)
		}
		return stMuted.Render("  " + s)
	}

	var agentParts []string
	for i, a := range d.agents {
		switch {
		case i == d.agentIdx && d.field == fieldAgent:
			agentParts = append(agentParts, lipgloss.NewStyle().Reverse(true).Foreground(colAccent).Render(" "+a.Name+" "))
		case i == d.agentIdx:
			agentParts = append(agentParts, lipgloss.NewStyle().Reverse(true).Render(" "+a.Name+" "))
		default:
			agentParts = append(agentParts, stMuted.Render(" "+a.Name+" "))
		}
	}
	agentHint := ""
	if d.field == fieldAgent {
		agentHint = stMuted.Render("  ←/→ change · ↓ back to folder")
	}

	out := []string{
		"",
		fit(" "+label(fieldAgent, "Agent ")+"  "+strings.Join(agentParts, " ")+agentHint, w),
		"",
		fit(" "+label(fieldFolder, "Folder")+"  "+d.dir.View(), w),
	}

	// Suggestions, indented under the input.
	const indent = "             "
	if len(d.sugg) == 0 {
		out = append(out, fit(stMuted.Render(indent+"no matching folders"), w))
	}
	home, _ := os.UserHomeDir()
	for i, sg := range d.sugg {
		// Folders in the listed directory show just their name; recent
		// folders show their full path (with ~ for the home folder).
		path := filepath.Base(sg.Path) + `\`
		tag := ""
		if sg.Recent {
			tag = "  recent"
			path = sg.Path
			if home != "" && len(path) >= len(home) && strings.EqualFold(path[:len(home)], home) {
				path = "~" + path[len(home):]
			}
		}
		avail := w - len(indent) - 3 - len(tag)
		if ansi.StringWidth(path) > avail && avail > 4 {
			path = "…" + path[len(path)-avail+1:]
		}
		line := indent + "  " + path
		style := lipgloss.NewStyle()
		if i == d.hi {
			line = indent + lipgloss.NewStyle().Foreground(colAccent).Render("▸ ") + path
			style = style.Bold(true)
		}
		out = append(out, fit(style.Render(line)+stMuted.Render(tag), w))
	}

	// Exactly where the session will start.
	target := d.target()
	mark := lipgloss.NewStyle().Foreground(colIdle).Render("✓")
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		mark = lipgloss.NewStyle().Foreground(colError).Render("✗ not found")
	}
	out = append(out, "",
		fit(stMuted.Render("   Start in  ")+target+"  "+mark, w),
		"",
		fit(stMuted.Render("   tab complete · ↑↓ choose · ctrl+w up a level · shift+tab agent · enter start · esc cancel"), w),
	)
	if d.err != "" {
		out = append(out, "", fit(lipgloss.NewStyle().Foreground(colError).Render("   "+d.err), w))
	}
	return out
}

func (m *model) renderFooter(w int) string {
	if m.confirm != nil {
		return fit(stWaitText.Render(" "+m.confirm.text), w)
	}
	if m.rename != nil {
		return fit(m.rename.View()+stMuted.Render("   enter save · esc cancel"), w)
	}
	if m.help != nil {
		return fit(stMuted.Render(m.helpFooter()), w)
	}
	if m.flash != "" && time.Since(m.flashAt) < 4*time.Second {
		return fit(lipgloss.NewStyle().Foreground(colError).Render(" "+m.flash), w)
	}
	var help string
	switch m.focus {
	case focusTerminal:
		help = footerHints(helpTerminal, false)
	case focusDialog:
		help = " enter start · esc cancel · F1 help"
	default:
		help = footerHints(helpList, false)
		if ansi.StringWidth(help) > w {
			help = footerHints(helpList, true)
		}
		if m.filterInput != nil {
			help = " type to filter · enter keep · esc clear"
		}
	}
	return fit(stMuted.Render(help), w)
}

func (m *model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	if m.width == 0 || m.height == 0 {
		v := tea.NewView("starting…")
		v.AltScreen = true
		return v
	}
	sw := m.sidebarWidth()
	mw := m.width - sw - 1
	bodyH := m.height - 1

	side := m.renderSidebar(sw, bodyH)
	main, cur := m.renderMain(mw, bodyH)
	if m.help != nil {
		main, cur = m.overlayHelp(main, mw, bodyH), nil
	}
	sep := stBorder.Render("│")

	var b strings.Builder
	for i := 0; i < bodyH; i++ {
		b.WriteString(side[i])
		b.WriteString("\x1b[0m")
		b.WriteString(sep)
		b.WriteString(main[i])
		b.WriteString("\n")
	}
	b.WriteString(m.renderFooter(m.width))

	v := tea.NewView(b.String())
	v.AltScreen = true
	// Mouse reporting is needed for the wheel. In Windows Terminal, hold
	// shift while dragging to select text.
	v.MouseMode = tea.MouseModeCellMotion
	v.Cursor = cur
	v.ReportFocus = true // for "notify only when deckhand isn't focused"
	v.WindowTitle = m.windowTitle()
	return v
}

func (m *model) windowTitle() string {
	waiting := 0
	for _, s := range m.sessions {
		if s.Snapshot().status == StatusWaiting {
			waiting++
		}
	}
	if waiting > 0 {
		return fmt.Sprintf("deckhand (%d need you)", waiting)
	}
	return "deckhand"
}
