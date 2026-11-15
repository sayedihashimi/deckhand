package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	if waiting > 0 {
		badge := stWaitText.Render(fmt.Sprintf("%d need you ", waiting))
		head = fit(head, w-ansi.StringWidth(badge)) + badge
	}
	lines = append(lines, fit(head, w), stBorder.Render(strings.Repeat("─", w)))

	if len(m.sessions) == 0 {
		lines = append(lines, "", fit(stMuted.Render(" No sessions yet."), w), fit(stMuted.Render(" Press n to start one."), w))
	}

	// Two lines per session; keep the selection in view.
	perRow := 2
	visible := (h - len(lines)) / perRow
	if visible < 1 {
		visible = 1
	}
	if m.sel < m.scroll {
		m.scroll = m.sel
	}
	if m.sel >= m.scroll+visible {
		m.scroll = m.sel - visible + 1
	}
	now := time.Now()
	for i := m.scroll; i < len(m.sessions) && i < m.scroll+visible; i++ {
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
		if i < 9 {
			num = fmt.Sprintf("%d", i+1)
		}
		left := stMuted.Render(num) + " " + m.statusIcon(sn) + " " + nameStyle.Render(s.Name)
		row1 := fit(left, w-len(age)-1) + stMuted.Render(age) + " "

		detail := sn.detail
		if detail == "" {
			detail = sn.status.String()
		}
		detailStyle := stMuted
		if sn.status == StatusWaiting {
			detailStyle = stWaitText
		}
		row2 := fit("     "+stMuted.Render(s.Agent+" · ")+detailStyle.Render(detail), w)

		if selected {
			bg := lipgloss.NewStyle().Background(colSelBg)
			marker := lipgloss.NewStyle().Foreground(colAccent).Render("▌")
			if m.focus == focusTerminal {
				marker = lipgloss.NewStyle().Foreground(colMuted).Render("▌")
			}
			row1 = marker + bg.Render(fit(left, w-len(age)-2)+stMuted.Render(age)+" ")
			row2 = marker + bg.Render(fit("    "+stMuted.Render(s.Agent+" · ")+detailStyle.Render(detail), w-1))
		}
		lines = append(lines, row1, row2)
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:h]
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
		} {
			lines = append(lines, fit(stMuted.Render(l), w))
		}
		for len(lines) < h {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return lines[:h], nil
	}

	sn := s.Snapshot()
	header := " " + m.statusIcon(sn) + " " + lipgloss.NewStyle().Bold(true).Render(s.Name) +
		stMuted.Render("  "+s.Agent+"  "+s.Dir)
	if sn.title != "" {
		header += stMuted.Render("  — " + sn.title)
	}
	lines = append(lines, fit(header, w))

	emu := s.Emulator()
	if emu == nil {
		msg := "  Not running. Press enter to start it."
		if s.Agent == "claude" && s.Started {
			msg = "  Not running. Press enter to resume this Claude conversation."
		}
		lines = append(lines, "", fit(stMuted.Render(msg), w))
		for len(lines) < h {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return lines[:h], nil
	}

	screen := strings.Split(strings.ReplaceAll(emu.Render(), "\r\n", "\n"), "\n")
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

func (m *model) renderDialog(w int) []string {
	d := m.dialog
	label := func(i int, s string) string {
		if d.field == i {
			return lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("› " + s)
		}
		return stMuted.Render("  " + s)
	}
	var agentParts []string
	for i, a := range d.agents {
		if i == d.agentIdx {
			agentParts = append(agentParts, lipgloss.NewStyle().Reverse(true).Render(" "+a.Name+" "))
		} else {
			agentParts = append(agentParts, " "+a.Name+" ")
		}
	}
	out := []string{
		"",
		fit(" "+label(0, "Agent  ")+"  "+strings.Join(agentParts, " "), w),
		"",
		fit(" "+label(1, "Folder ")+"  "+d.dir.View(), w),
		"",
		fit(" "+label(2, "Name   ")+"  "+d.name.View(), w),
		"",
		fit(stMuted.Render("   tab next field · ←/→ change agent · enter start · esc cancel"), w),
	}
	if d.err != "" {
		out = append(out, "", fit(lipgloss.NewStyle().Foreground(colError).Render("   "+d.err), w))
	}
	return out
}

func (m *model) renderFooter(w int) string {
	if m.confirm != nil {
		return fit(stWaitText.Render(" "+m.confirm.text), w)
	}
	if m.flash != "" && time.Since(m.flashAt) < 4*time.Second {
		return fit(lipgloss.NewStyle().Foreground(colError).Render(" "+m.flash), w)
	}
	var help string
	switch m.focus {
	case focusTerminal:
		help = " ctrl+q back to the list"
	case focusDialog:
		help = " enter start · esc cancel"
	default:
		help = " ↑↓ select · enter open · n new · tab next needing you · K end · x remove · q quit"
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
	v.Cursor = cur
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
