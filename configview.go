package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Drawing the config screen.

var (
	stSection = lipgloss.NewStyle().Bold(true)
	stFocus   = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stErrText = lipgloss.NewStyle().Foreground(colError)
	stOKText  = lipgloss.NewStyle().Foreground(colIdle)
)

// cfgLabel is a row's label, highlighted when it has focus.
func cfgLabel(focused bool, s string, w int) string {
	s = fmt.Sprintf("%-*s", w, s)
	if focused {
		return stFocus.Render("› " + s)
	}
	return stMuted.Render("  " + s)
}

// chips shows a choice: the selected option reversed, the others muted.
// sel -1 shows them all muted.
func chips(options []string, sel int, focused bool) string {
	var parts []string
	for i, o := range options {
		switch {
		case i == sel && focused:
			parts = append(parts, lipgloss.NewStyle().Reverse(true).Foreground(colAccent).Render(" "+o+" "))
		case i == sel:
			parts = append(parts, lipgloss.NewStyle().Reverse(true).Render(" "+o+" "))
		default:
			parts = append(parts, stMuted.Render(" "+o+" "))
		}
	}
	return strings.Join(parts, " ")
}

func button(s string, focused bool) string {
	if focused {
		return lipgloss.NewStyle().Reverse(true).Foreground(colAccent).Render(" " + s + " ")
	}
	return lipgloss.NewStyle().Foreground(colAccent).Render("[" + s + "]")
}

// renderConfig draws the config screen in w×h cells.
func (m *model) renderConfig(w, h int) []string {
	c := m.cfg
	title := " Settings"
	switch {
	case c.picker != nil:
		title += " › Pick from your PowerShell profile"
	case c.form != nil && c.form.index < 0:
		title += " › New agent"
	case c.form != nil:
		title += fmt.Sprintf(" › Edit %q", c.agents[c.form.index].Name)
	}
	path := stMuted.Render("  " + shortDir(c.path, max(w/2, 20)))
	head := stTitle.Render(title) + path
	if c.dirty() {
		head = stTitle.Render(title) + "  " + stWaitText.Render("● unsaved changes") + path
	}

	var body []string
	focusLine := 0
	var hint string
	switch {
	case c.picker != nil:
		body, focusLine, hint = m.renderPicker(w)
	case c.form != nil:
		body, focusLine, hint = c.renderForm(w)
	default:
		body, focusLine, hint = m.renderConfigMain(w)
	}

	heading := append([]string{fit(head, w)}, m.testLines(w)...)
	foot := []string{""}
	if c.status != "" {
		st := stOKText
		if c.statusErr {
			st = stErrText
		}
		foot = append(foot, fit(st.Render("  "+displayPathsIn(c.status)), w))
	}
	if m.temp {
		foot = append(foot, fit(stWaitText.Render("  "+tempConfigNote), w))
	}
	foot = append(foot, fit(stMuted.Render("  "+hint), w))

	// Scroll the body so the focused row stays in view.
	room := max(h-len(heading)-len(foot), 1)
	top := 0
	if len(body) > room {
		top = min(max(focusLine-room/2, 0), len(body)-room)
		body = body[top : top+room]
	}
	lines := append(heading, body...)
	for len(lines) < h-len(foot) {
		lines = append(lines, "")
	}
	lines = append(lines, foot...)
	for i := range lines {
		lines[i] = fit(lines[i], w)
	}
	return lines[:h]
}

func (m *model) renderConfigMain(w int) (lines []string, focusLine int, hint string) {
	c := m.cfg
	cur := c.current()
	rowIdx := 0
	add := func(s string, isRow bool) {
		if isRow {
			if rowIdx == c.row {
				focusLine = len(lines)
			}
			rowIdx++
		}
		lines = append(lines, fit(s, w))
	}
	const lw = 10
	if c.comments {
		add(stWaitText.Render("  Your config.toml has comments of its own. Saving rewrites the file without them;"), false)
		add(stWaitText.Render("  the old file is kept as config.toml.bak."), false)
	}

	add("", false)
	add(" "+stSection.Render("Agents")+stMuted.Render("  in new-session order; custom agents come before the built-in ones"), false)
	nameW := 4
	for _, a := range c.agents {
		nameW = max(nameW, min(len([]rune(a.Name)), 20))
	}
	for i, a := range c.agents {
		focused := cur.kind == rowAgent && cur.idx == i
		add(" "+cfgLabel(focused, clip(a.Name, 20), nameW)+"  "+agentSummary(a), true)
	}
	if len(c.agents) == 0 {
		add(stMuted.Render("   No custom agents yet."), false)
	}
	add(" "+cfgLabel(cur.kind == rowAddAgent, "", 0)+button("+ Add agent", cur.kind == rowAddAgent), true)
	var builtins []string
	for _, b := range builtinAgents {
		builtins = append(builtins, b.Name)
	}
	add(stMuted.Render("     Built-in: "+strings.Join(builtins, " · ")), false)

	add("", false)
	add(" "+stSection.Render("Notifications"), false)
	on := 1
	if c.notifyOn {
		on = 0
	}
	add(" "+cfgLabel(cur.kind == rowNotifyEnabled, "Enabled", lw)+chips([]string{"on", "off"}, on, cur.kind == rowNotifyEnabled), true)
	when := 0
	if c.notifyWhen == notifyAlways {
		when = 1
	}
	whenText := chips([]string{"when deckhand isn't focused", "always"}, when, cur.kind == rowNotifyWhen)
	if !c.notifyOn {
		whenText = stMuted.Render(" (notifications are off)")
	}
	add(" "+cfgLabel(cur.kind == rowNotifyWhen, "When", lw)+whenText, true)
	add(" "+cfgLabel(cur.kind == rowNotifyTest, "", lw)+button("Send a test notification", cur.kind == rowNotifyTest), true)

	add("", false)
	add(" "+stSection.Render("Session list")+stMuted.Render("  one key hides and shows it, in the list and inside a session"), false)
	keyLine := " " + cfgLabel(cur.kind == rowListKey, "Hide key", lw) + c.listKey.View()
	if _, err := checkToggleKey(c.listKey.Value()); err != nil {
		keyLine += "  " + stErrText.Render("✗ "+err.Error())
	}
	add(keyLine, true)
	backLine := " " + cfgLabel(cur.kind == rowListBackKey, "Back key", lw) + c.backKey.View()
	if err := c.backKeyErr(); err != nil {
		backLine += "  " + stErrText.Render("✗ "+err.Error())
	}
	add(backLine, true)
	status := 1
	if c.listStatus {
		status = 0
	}
	add(" "+cfgLabel(cur.kind == rowListStatus, "Status", lw)+chips([]string{"on", "off"}, status, cur.kind == rowListStatus)+
		stMuted.Render(`  "n need you" in the header while the list is hidden`), true)
	title := 1
	if c.listTitle {
		title = 0
	}
	add(" "+cfgLabel(cur.kind == rowListTitle, "Title", lw)+chips([]string{"on", "off"}, title, cur.kind == rowListTitle)+
		stMuted.Render("  Claude sessions take their name from /rename and Claude's title"), true)

	costSel := map[string]int{costDollars: 0, costTokens: 1, costOff: 2}[c.costShow]
	add(" "+cfgLabel(cur.kind == rowListCost, "Cost", lw)+chips([]string{"dollars", "tokens", "off"}, costSel, cur.kind == rowListCost)+
		stMuted.Render("  each Claude session's cost, and today's total in the header"), true)
	ctxSel := map[string]int{ctxShowMeter: 0, ctxShowPercent: 1, ctxShowTokens: 2}[c.ctxShow]
	add(" "+cfgLabel(cur.kind == rowListContext, "Context", lw)+chips([]string{"meter", "percent", "tokens"}, ctxSel, cur.kind == rowListContext)+
		stMuted.Render("  how full each Claude session's context window is"), true)

	add("", false)
	add(" "+stSection.Render("History")+stMuted.Render("  exited sessions show their last output after a restart"), false)
	save := 1
	if c.histSave {
		save = 0
	}
	add(" "+cfgLabel(cur.kind == rowHistorySave, "Save", lw)+chips([]string{"on", "off"}, save, cur.kind == rowHistorySave)+
		stMuted.Render(`  to sessions\<id>\screen.ans; it can include secrets printed on screen`), true)
	linesLine := " " + cfgLabel(cur.kind == rowHistoryLines, "Lines", lw) + c.histLines.View()
	if err := c.historyLinesErr(); err != nil {
		linesLine += "  " + stErrText.Render("✗ "+err.Error())
	}
	add(linesLine, true)

	add("", false)
	add(" "+stSection.Render("Open")+stMuted.Render("  what o (editor) and t (terminal) run; {dir} is the session's folder"), false)
	for _, f := range []struct {
		row, preset cfgRowKind
		label       string
		in          string
		presets     []openPreset
		which       int
	}{
		{rowEditor, rowEditorPreset, "Editor", c.editor.View(), editorPresets, 0},
		{rowTerminal, rowTerminalPreset, "Terminal", c.terminal.View(), terminalPresets, 1},
	} {
		line := " " + cfgLabel(cur.kind == f.row, f.label, lw) + f.in
		val := c.editor.Value()
		if f.which == 1 {
			val = c.terminal.Value()
		}
		if v := strings.TrimSpace(val); v != "" {
			if err := checkTemplate(v); err != nil {
				line += "  " + stErrText.Render("✗ "+err.Error())
			}
		}
		add(line, true)
		var labels []string
		for _, p := range f.presets {
			labels = append(labels, p.label)
		}
		hi := -1
		if cur.kind == f.preset {
			hi = c.presetHi[f.which]
		}
		add(" "+cfgLabel(cur.kind == f.preset, "  presets", lw)+chips(labels, hi, cur.kind == f.preset), true)
	}
	multiSel := map[string]int{multiAsk: 0, multiAll: 1, multiFirst: 2}[c.openMulti]
	add(" "+cfgLabel(cur.kind == rowOpenMulti, "Folders", lw)+chips([]string{"ask", "all", "first"}, multiSel, cur.kind == rowOpenMulti)+
		stMuted.Render("  o and t on a session with more than one folder"), true)
	tryOn := "select a session first"
	if s := m.current(); s != nil {
		tryOn = "on " + shortDir(s.Dir, max(w-50, 10))
	}
	tryHi := -1
	if cur.kind == rowTryOpen {
		tryHi = c.tryHi
	}
	add(" "+cfgLabel(cur.kind == rowTryOpen, "Try it", lw)+chips([]string{"editor", "terminal"}, tryHi, cur.kind == rowTryOpen)+stMuted.Render("  "+tryOn), true)

	add("", false)
	add(" "+stSection.Render("Path aliases")+stMuted.Render("  shown in place of a folder's path; ~ is the home folder. Edit [[paths.alias]] in config.toml"), false)
	if len(c.aliases) == 0 {
		add(stMuted.Render("   None."), false)
	} else {
		// Only the names: the prefixes are what the aliases hide.
		var names []string
		for _, a := range c.aliases {
			names = append(names, a.name)
		}
		add("   "+strings.Join(names, stMuted.Render(" · ")), false)
	}

	add("", false)
	add(" "+cfgLabel(cur.kind == rowSave, "", lw)+button("Save", cur.kind == rowSave)+stMuted.Render("  (ctrl+s)"), true)

	switch cur.kind {
	case rowAgent:
		hint = "enter edit · a add · d delete · t test it · shift+↑/↓ move up or down"
	case rowAddAgent:
		hint = "enter add an agent: a PowerShell command from your profile, or a program"
	case rowNotifyEnabled, rowNotifyWhen:
		hint = "←/→ change"
	case rowNotifyTest:
		hint = "enter send one now"
	case rowListKey:
		hint = `a key name, e.g. ctrl+\, f2 or f3; empty: ` + keyLabel(defaultToggleKey)
	case rowListBackKey:
		hint = `back to the list from a session (ctrl+q and ctrl+] always work too), e.g. f5 or f10; empty: ` + keyLabel(defaultBackKey)
	case rowListStatus, rowHistorySave:
		hint = "←/→ change"
	case rowListTitle:
		hint = "←/→ change · off: a session keeps its folder's name until you rename it with r"
	case rowListCost:
		hint = "←/→ change · dollars at API prices (notional on a Pro or Max plan), tokens, or off"
	case rowListContext:
		hint = "←/→ change · ▰▰▰▱▱ 57%, 57%, or 114k/200k; the meter and window size go first when there's no room"
	case rowHistoryLines:
		hint = fmt.Sprintf("lines of output kept per session, 1 to %d; empty: %d", maxHistoryLines, maxHistoryLines)
	case rowEditor:
		hint = "empty: VS Code (code) if it's on PATH, else Explorer"
	case rowTerminal:
		hint = "empty: " + defaultTerminal
	case rowEditorPreset, rowTerminalPreset:
		hint = "←/→ choose · enter use it"
	case rowOpenMulti:
		hint = "←/→ change · ask: a picker, All folders first; all: every folder (VS Code: one workspace); first: the same folder as before, the worktree if there is one, otherwise the start folder"
	case rowTryOpen:
		hint = "←/→ choose · enter run it (as typed, unsaved) on the selected session's folder"
	case rowSave:
		hint = "enter save and apply"
	}
	return lines, focusLine, hint
}

// agentSummary describes an agent on one line: what runs, and whether it's
// Claude.
func agentSummary(a agentConfig) string {
	var parts []string
	if a.Pwsh != "" {
		parts = append(parts, stMuted.Render("PowerShell ")+displayPathsIn(a.Pwsh))
	} else {
		cmd := a.Command
		if len(a.Args) > 0 {
			cmd += " " + joinArgs(a.Args)
		}
		parts = append(parts, stMuted.Render("program ")+displayPathsIn(cmd))
	}
	if a.Type == "claude" {
		parts = append(parts, lipgloss.NewStyle().Foreground(colAccent).Render("claude"))
	}
	if a.Resume != "" {
		parts = append(parts, stMuted.Render("resume ")+displayPathsIn(a.Resume))
	}
	if _, err := a.toAgent(); err != nil {
		parts = append(parts, stErrText.Render("✗ "+displayPathsIn(err.Error())))
	}
	return strings.Join(parts, stMuted.Render(" · "))
}

func (c *configScreen) renderForm(w int) (lines []string, focusLine int, hint string) {
	f := c.form
	const lw = 10
	errLine := func(field int) {
		if f.errField == field && f.err != "" {
			lines = append(lines, fit(stErrText.Render(strings.Repeat(" ", lw+4)+"✗ "+f.err), w))
		}
	}
	lines = append(lines, "")
	for _, field := range f.fields() {
		focused := f.field == field
		if focused {
			focusLine = len(lines)
		}
		var line string
		switch field {
		case ffName:
			line = cfgLabel(focused, "Name", lw) + f.name.View()
		case ffKind:
			sel := 1
			if f.pwsh {
				sel = 0
			}
			line = cfgLabel(focused, "Kind", lw) + chips([]string{"PowerShell command", "program or script"}, sel, focused)
		case ffPick:
			line = cfgLabel(focused, "", lw) + button("Pick from your PowerShell profile…", focused)
		case ffCommand:
			label := "Program"
			if f.pwsh {
				label = "Command"
			}
			line = cfgLabel(focused, label, lw) + f.command.View()
		case ffArgs:
			line = cfgLabel(focused, "Arguments", lw) + f.args.View()
		case ffType:
			sel := 1
			if f.claude {
				sel = 0
			}
			line = cfgLabel(focused, "Type", lw) + chips([]string{"Claude Code", "other"}, sel, focused)
		case ffResume:
			line = cfgLabel(focused, "Resume", lw) + f.resume.View()
		case ffTest:
			lines = append(lines, "")
			if focused {
				focusLine = len(lines)
			}
			line = cfgLabel(focused, "", lw) + button("Test", focused)
		}
		lines = append(lines, fit(" "+line, w))
		errLine(field)
	}

	switch f.field {
	case ffName:
		hint = "shown in the new-session dialog and stored with each session"
	case ffKind:
		hint = "←/→ change · PowerShell: runs with your profile loaded · program: an .exe, .cmd, .bat or .ps1"
	case ffPick:
		hint = "enter list the functions and aliases your profile defines (ctrl+p from any field)"
	case ffCommand:
		hint = "what to run, e.g. smc"
		if !f.pwsh {
			hint = "on PATH or a full path, e.g. claude or C:\\tools\\agent.cmd"
		}
	case ffArgs:
		hint = "extra arguments for the program; quote ones with spaces"
	case ffType:
		hint = "←/→ change · Claude Code: status comes from Claude's hooks, and sessions can be resumed"
	case ffResume:
		hint = "the command that resumes a Claude session; {id} is the session ID. Empty: can't resume"
	case ffTest:
		hint = "enter start it as edited (unsaved) in a test session, in the selected session's folder"
	}
	return lines, focusLine, hint
}

func (m *model) renderPicker(w int) (lines []string, focusLine int, hint string) {
	p := m.cfg.picker
	lines = append(lines, "", fit(" "+cfgLabel(true, "Filter", 8)+p.filter.View(), w), "")
	hint = "your profile's functions and aliases; PowerShell's own are left out"
	switch {
	case m.profile == nil:
		lines = append(lines, fit(stMuted.Render("   Loading your PowerShell profile…"), w))
		return lines, 1, hint
	case m.profile.err != nil:
		lines = append(lines, fit(stErrText.Render("   Couldn't list your profile's commands: "+m.profile.err.Error()), w),
			fit(stMuted.Render("   esc goes back; type the command in the form instead."), w))
		return lines, 1, hint
	}
	matches := m.pickerMatches()
	if len(matches) == 0 {
		lines = append(lines, fit(stMuted.Render("   No matching functions or aliases."), w))
	}
	nameW := 4
	for _, pc := range matches {
		nameW = max(nameW, min(len([]rune(pc.Name)), 28))
	}
	focusLine = 1
	for i, pc := range matches {
		desc := strings.ToLower(pc.Kind)
		if pc.Target != "" {
			desc += " → " + pc.Target
		}
		if pc.Source != "" {
			desc += " · " + pc.Source
		}
		if pc.Resume {
			desc += " · has -Resume (Claude wrapper?)"
		}
		name := fmt.Sprintf("%-*s", nameW, clip(pc.Name, 28))
		line := "     " + name + "  " + stMuted.Render(desc)
		if i == p.hi {
			focusLine = len(lines)
			line = "   " + stFocus.Render("▸ "+name) + "  " + stMuted.Render(desc)
		}
		lines = append(lines, fit(line, w))
	}
	return lines, focusLine, hint
}
