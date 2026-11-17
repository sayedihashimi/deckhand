package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// The config screen ("c"): agents, notifications, [list], [history] and [open], edited in the
// main area and saved to config.toml. It works on a copy; nothing changes
// until ctrl+s, and esc leaves without saving.

type cfgRowKind int

const (
	rowAgent cfgRowKind = iota
	rowAddAgent
	rowNotifyEnabled
	rowNotifyWhen
	rowNotifyTest
	rowListKey
	rowListStatus
	rowHistorySave
	rowHistoryLines
	rowEditor
	rowEditorPreset
	rowTerminal
	rowTerminalPreset
	rowTryOpen
	rowSave
)

type cfgRow struct {
	kind cfgRowKind
	idx  int // the agent, for rowAgent
}

// openPreset is a ready-made [open] command. The first of progs found on
// PATH is used, else the first.
type openPreset struct {
	label string
	progs []string
	args  string
}

var editorPresets = []openPreset{
	{"VS Code", []string{"code"}, `"{dir}"`},
	{"Rider", []string{"rider", "rider64.exe"}, `"{dir}"`},
	{"IntelliJ IDEA", []string{"idea", "idea64.exe"}, `"{dir}"`},
	{"VS Code Insiders", []string{"code-insiders"}, `"{dir}"`},
}

var terminalPresets = []openPreset{
	{"Windows Terminal, new tab", []string{"wt.exe"}, `-w 0 nt -d "{dir}"`},
	{"Windows Terminal, new window", []string{"wt.exe"}, `-w -1 nt -d "{dir}"`},
}

func (p openPreset) command(lookPath func(string) (string, error)) string {
	prog := p.progs[0]
	for _, c := range p.progs {
		if _, err := lookPath(c); err == nil {
			prog = c
			break
		}
	}
	return prog + " " + p.args
}

type configScreen struct {
	path     string
	raw      map[string]any // every table in the file, so unknown ones are kept
	loadErr  string         // the file isn't valid TOML: saving is off
	checkErr string         // the file loads with an error (fix it here)
	comments bool           // the file has comments of its own, which a save loses
	exists   bool

	agents     []agentConfig
	notifyOn   bool
	notifyWhen string
	listKey    textinput.Model // [list] toggle_key
	listStatus bool            // [list] status_line
	histSave   bool            // [history] save
	histLines  textinput.Model // [history] lines
	editor     textinput.Model
	terminal   textinput.Model

	saved     string // the encoded settings when opened or last saved
	row       int
	presetHi  [2]int // highlighted editor and terminal preset
	tryHi     int    // 0 editor, 1 terminal
	status    string
	statusErr bool
	width     int

	form    *agentForm
	picker  *profilePicker
	testAsk *testAsk // keep or discard the test session? (agenttest.go)
}

func newConfigScreen(width int) *configScreen {
	c := &configScreen{path: configPath(), raw: map[string]any{}}
	c.listKey = newCfgInput(`default: ` + keyLabel(defaultToggleKey))
	c.histLines = newCfgInput(fmt.Sprintf("default: %d", maxHistoryLines))
	c.editor = newCfgInput(`e.g. code "{dir}"`)
	c.terminal = newCfgInput(`default: ` + defaultTerminal)
	text := ""
	data, err := os.ReadFile(c.path)
	switch {
	case err == nil:
		text, c.exists = string(data), true
	case !errors.Is(err, os.ErrNotExist):
		c.loadErr = err.Error()
	}
	cfg, raw, err := decodeConfig(text)
	if err != nil {
		c.loadErr = err.Error()
	} else {
		c.raw = raw
		if err := checkConfigText(text); err != nil {
			c.checkErr = err.Error()
		}
	}
	c.comments = hasOwnComments(text)
	c.agents = cfg.Agents
	n := cfg.Notifications
	c.notifyOn = n.Enabled == nil || *n.Enabled
	c.notifyWhen = n.When
	if c.notifyWhen != notifyAlways {
		c.notifyWhen = notifyUnfocused
	}
	c.listKey.SetValue(cfg.List.ToggleKey)
	c.listStatus = cfg.List.StatusLine == nil || *cfg.List.StatusLine
	c.histSave = cfg.History.Save == nil || *cfg.History.Save
	if cfg.History.Lines != 0 {
		c.histLines.SetValue(strconv.Itoa(cfg.History.Lines))
	}
	c.editor.SetValue(cfg.Open.Editor)
	c.terminal.SetValue(cfg.Open.Terminal)
	c.saved, _ = c.encode()
	c.setWidth(width)
	return c
}

func newCfgInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	return ti
}

func (c *configScreen) setWidth(w int) {
	c.width = w
	iw := min(max(w-20, 10), 80)
	c.editor.SetWidth(iw)
	c.terminal.SetWidth(iw)
	c.listKey.SetWidth(min(iw, 24))
	c.histLines.SetWidth(min(iw, 24))
	if c.form != nil {
		c.form.setWidth(iw)
	}
	if c.picker != nil {
		c.picker.filter.SetWidth(iw)
	}
}

// settings is the screen's state as a config file.
func (c *configScreen) settings() configFile {
	on := c.notifyOn
	status := c.listStatus
	listKey := strings.TrimSpace(c.listKey.Value())
	if k, err := checkToggleKey(listKey); err == nil {
		listKey = k
	}
	save := c.histSave
	return configFile{
		Agents:        c.agents,
		Notifications: notifyConfig{Enabled: &on, When: c.notifyWhen},
		List:          listConfig{ToggleKey: listKey, StatusLine: &status},
		History:       historyConfig{Save: &save, Lines: c.historyLines()},
		Open: openConfig{
			Editor:   strings.TrimSpace(c.editor.Value()),
			Terminal: strings.TrimSpace(c.terminal.Value()),
		},
	}
}

// historyLines is the lines box as a number: 0 when it's empty (the
// default), -1 when it isn't a number (validate says so).
func (c *configScreen) historyLines() int {
	v := strings.TrimSpace(c.histLines.Value())
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}

// historyLinesErr is what's wrong with the lines box, if anything.
func (c *configScreen) historyLinesErr() error {
	n := c.historyLines()
	if n == 0 && strings.TrimSpace(c.histLines.Value()) != "" {
		n = -1 // "0" isn't the default; empty is
	}
	if n < 0 {
		return fmt.Errorf("must be a number between 1 and %d", maxHistoryLines)
	}
	return checkHistoryLines(n)
}

func (c *configScreen) encode() (string, error) { return encodeConfig(c.settings(), c.raw) }

// dirty reports whether there are unsaved changes.
func (c *configScreen) dirty() bool {
	text, err := c.encode()
	return err != nil || text != c.saved
}

func (c *configScreen) rows() []cfgRow {
	var rows []cfgRow
	for i := range c.agents {
		rows = append(rows, cfgRow{rowAgent, i})
	}
	for _, k := range []cfgRowKind{rowAddAgent, rowNotifyEnabled, rowNotifyWhen, rowNotifyTest,
		rowListKey, rowListStatus, rowHistorySave, rowHistoryLines, rowEditor, rowEditorPreset, rowTerminal, rowTerminalPreset, rowTryOpen, rowSave} {
		rows = append(rows, cfgRow{kind: k})
	}
	return rows
}

func (c *configScreen) current() cfgRow {
	rows := c.rows()
	c.row = min(max(c.row, 0), len(rows)-1)
	return rows[c.row]
}

// focusRow moves to a row and focuses its text box, if it has one.
func (c *configScreen) focusRow(i int) tea.Cmd {
	c.row = i
	c.editor.Blur()
	c.terminal.Blur()
	c.listKey.Blur()
	c.histLines.Blur()
	switch c.current().kind {
	case rowListKey:
		return c.listKey.Focus()
	case rowHistoryLines:
		return c.histLines.Focus()
	case rowEditor:
		return c.editor.Focus()
	case rowTerminal:
		return c.terminal.Focus()
	}
	return nil
}

func (c *configScreen) move(d int) tea.Cmd {
	n := len(c.rows())
	return c.focusRow((c.row + d + n) % n)
}

func (c *configScreen) setStatus(s string, isErr bool) { c.status, c.statusErr = s, isErr }

// validate checks everything before a save, moving to the first problem.
func (c *configScreen) validate() error {
	seen := map[string]bool{}
	for i, a := range c.agents {
		if _, err := a.toAgent(); err != nil {
			c.row = i
			return fmt.Errorf("agent %q: %w", a.Name, err)
		}
		if seen[a.Name] {
			c.row = i
			return fmt.Errorf("two agents are named %q", a.Name)
		}
		seen[a.Name] = true
	}
	if _, err := checkToggleKey(c.listKey.Value()); err != nil {
		c.row = c.rowOf(rowListKey)
		return fmt.Errorf("toggle key %w", err)
	}
	if err := c.historyLinesErr(); err != nil {
		c.row = c.rowOf(rowHistoryLines)
		return fmt.Errorf("history lines %w", err)
	}
	for _, f := range []struct {
		kind cfgRowKind
		name string
		in   *textinput.Model
	}{{rowEditor, "editor", &c.editor}, {rowTerminal, "terminal", &c.terminal}} {
		if v := strings.TrimSpace(f.in.Value()); v != "" {
			if err := checkTemplate(v); err != nil {
				c.row = c.rowOf(f.kind)
				return fmt.Errorf("%s: %w", f.name, err)
			}
		}
	}
	return nil
}

func (c *configScreen) rowOf(kind cfgRowKind) int {
	for i, r := range c.rows() {
		if r.kind == kind {
			return i
		}
	}
	return 0
}

// Keys ------------------------------------------------------------------------

// configStatusMsg reports the result of something the screen ran in the
// background (a test notification).
type configStatusMsg struct {
	text  string
	isErr bool
}

// profileCommandsMsg delivers the profile's functions and aliases.
type profileCommandsMsg struct {
	cmds []profileCommand
	err  error
}

func (m *model) openConfigScreen() tea.Cmd {
	w, _ := m.termSize()
	m.cfg = newConfigScreen(w)
	m.focus = focusConfig
	switch {
	case m.cfg.loadErr != "":
		m.cfg.setStatus("Can't read the config file, so saving is off: "+m.cfg.loadErr, true)
	case m.cfg.checkErr != "":
		m.cfg.setStatus("The config file has an error. Fix it here and save: "+m.cfg.checkErr, true)
	}
	return nil
}

func (m *model) closeConfigScreen() {
	m.cfg = nil
	m.focus = focusSidebar
}

func (m *model) configKey(k tea.KeyPressMsg) tea.Cmd {
	c := m.cfg
	if c.testAsk != nil {
		// Keep or discard the test session; esc decides later.
		ask := c.testAsk
		switch k.String() {
		case "k", "x":
			c.testAsk = nil
			if k.String() == "k" {
				m.keepTest()
			} else {
				m.discardTest()
			}
			if ask.then != nil {
				return ask.then(m)
			}
		case "esc":
			c.testAsk = nil
		}
		return nil
	}
	if k.String() == "ctrl+t" && m.test != nil && !m.test.stopping {
		m.askTest(nil)
		return nil
	}
	if c.picker != nil {
		return m.pickerKey(k)
	}
	if c.form != nil {
		return m.formKey(k)
	}
	key := k.String()
	switch key {
	case "esc":
		if m.test != nil && !m.test.stopping {
			// Keep or discard the test first, then leave.
			m.askTest(func(m *model) tea.Cmd { return m.leaveConfigScreen() })
			return nil
		}
		return m.leaveConfigScreen()
	case "ctrl+s":
		return m.saveConfigScreen()
	case "up", "shift+tab":
		return c.move(-1)
	case "down", "tab":
		return c.move(1)
	}

	row := c.current()
	switch row.kind {
	case rowAgent:
		i := row.idx
		switch key {
		case "enter", "e":
			return c.openForm(i)
		case "a":
			return c.openForm(-1)
		case "t":
			return m.startTest(c.agents[i])
		case "d", "delete":
			name := c.agents[i].Name
			c.agents = append(c.agents[:i:i], c.agents[i+1:]...)
			c.setStatus(fmt.Sprintf("Deleted %q. ctrl+s saves, esc discards.", name), false)
		case "shift+up", "K":
			if i > 0 {
				c.agents[i-1], c.agents[i] = c.agents[i], c.agents[i-1]
				c.row--
			}
		case "shift+down", "J":
			if i < len(c.agents)-1 {
				c.agents[i+1], c.agents[i] = c.agents[i], c.agents[i+1]
				c.row++
			}
		}
	case rowAddAgent:
		if key == "enter" || key == "a" {
			return c.openForm(-1)
		}
	case rowNotifyEnabled:
		if isToggleKey(key) {
			c.notifyOn = !c.notifyOn
		}
	case rowNotifyWhen:
		if isToggleKey(key) {
			if c.notifyWhen == notifyAlways {
				c.notifyWhen = notifyUnfocused
			} else {
				c.notifyWhen = notifyAlways
			}
		}
	case rowNotifyTest:
		if key == "enter" {
			c.setStatus("Sending a test notification…", false)
			return func() tea.Msg {
				if err := sendTestNotification(); err != nil {
					return configStatusMsg{"Notification failed: " + err.Error(), true}
				}
				return configStatusMsg{"Sent a test notification. If it didn't appear, check Windows' notification settings and Do Not Disturb.", false}
			}
		}
	case rowListKey:
		if key == "enter" {
			return c.move(1)
		}
		var cmd tea.Cmd
		c.listKey, cmd = c.listKey.Update(k)
		return cmd
	case rowListStatus:
		if isToggleKey(key) {
			c.listStatus = !c.listStatus
		}
	case rowHistorySave:
		if isToggleKey(key) {
			c.histSave = !c.histSave
		}
	case rowHistoryLines:
		if key == "enter" {
			return c.move(1)
		}
		var cmd tea.Cmd
		c.histLines, cmd = c.histLines.Update(k)
		return cmd
	case rowEditor, rowTerminal:
		if key == "enter" {
			return c.move(1)
		}
		in := &c.editor
		if row.kind == rowTerminal {
			in = &c.terminal
		}
		var cmd tea.Cmd
		*in, cmd = in.Update(k)
		return cmd
	case rowEditorPreset, rowTerminalPreset:
		which, presets, in := 0, editorPresets, &c.editor
		if row.kind == rowTerminalPreset {
			which, presets, in = 1, terminalPresets, &c.terminal
		}
		switch key {
		case "left", "h":
			c.presetHi[which] = (c.presetHi[which] + len(presets) - 1) % len(presets)
		case "right", "l", "space":
			c.presetHi[which] = (c.presetHi[which] + 1) % len(presets)
		case "enter":
			in.SetValue(presets[c.presetHi[which]].command(exec.LookPath))
			in.CursorEnd()
			c.setStatus("Set to "+presets[c.presetHi[which]].label+".", false)
		}
	case rowTryOpen:
		switch key {
		case "left", "right", "h", "l", "space":
			c.tryHi = 1 - c.tryHi
		case "enter":
			m.tryOpen()
		}
	case rowSave:
		if key == "enter" {
			return m.saveConfigScreen()
		}
	}
	return nil
}

// leaveConfigScreen closes the config screen, asking first when there are
// unsaved changes.
func (m *model) leaveConfigScreen() tea.Cmd {
	if m.cfg.dirty() {
		m.confirm = &confirmPrompt{text: "Leave the config screen and discard your changes? y/n", action: func(m *model) tea.Cmd {
			m.closeConfigScreen()
			return nil
		}}
		return nil
	}
	m.closeConfigScreen()
	return nil
}

func isToggleKey(key string) bool {
	switch key {
	case "enter", "space", "left", "right", "h", "l":
		return true
	}
	return false
}

// tryOpen runs the editor or terminal command (as typed, unsaved) on the
// selected session's folder.
func (m *model) tryOpen() {
	c := m.cfg
	s := m.current()
	if s == nil {
		c.setStatus("Select a session first: the command runs on its folder.", true)
		return
	}
	s.mu.Lock()
	dir := s.Dir
	s.mu.Unlock()
	cfg := c.settings().Open
	tmpl, _ := editorTemplate(cfg, exec.LookPath)
	what := "editor"
	if c.tryHi == 1 {
		tmpl, what = terminalTemplate(cfg), "terminal"
	}
	if err := launchOpen(tmpl, dir); err != nil {
		c.setStatus("Couldn't open: "+err.Error(), true)
		return
	}
	c.setStatus("Opened the "+what+": "+tmpl, false)
}

// saveConfigScreen checks and writes the config file, then applies it.
func (m *model) saveConfigScreen() tea.Cmd {
	c := m.cfg
	if c.loadErr != "" {
		c.setStatus("Saving is off: the config file can't be read ("+c.loadErr+"). Fix it by hand.", true)
		return nil
	}
	if !c.dirty() && c.exists && c.checkErr == "" {
		c.setStatus("No changes to save.", false)
		return nil
	}
	if err := c.validate(); err != nil {
		c.setStatus("Not saved: "+err.Error(), true)
		return c.focusRow(c.row)
	}
	text, err := c.encode()
	if err == nil {
		err = checkConfigText(text)
	}
	if err != nil {
		c.setStatus("Not saved: "+err.Error(), true)
		return nil
	}
	if c.comments {
		m.confirm = &confirmPrompt{
			text: "Saving rewrites config.toml without your comments (the old file is kept as config.toml.bak). Save? y/n",
			action: func(m *model) tea.Cmd {
				m.writeConfigScreen(text)
				return nil
			},
		}
		return nil
	}
	m.writeConfigScreen(text)
	return nil
}

func (m *model) writeConfigScreen(text string) {
	c := m.cfg
	backedUp, err := writeConfigFile(c.path, text)
	if err != nil {
		c.setStatus("Couldn't save: "+err.Error(), true)
		return
	}
	c.saved, c.comments, c.exists, c.checkErr = text, false, true, ""
	m.haveConfig = true
	if err := reloadConfig(); err != nil {
		c.setStatus("Saved, but it didn't load: "+err.Error(), true)
		return
	}
	m.agents = availableAgents()
	msg := "Saved and applied."
	if backedUp {
		msg += " The previous file is in config.toml.bak."
	}
	c.setStatus(msg, false)
}

// Agent form ------------------------------------------------------------------

const (
	ffName = iota
	ffKind
	ffPick
	ffCommand
	ffArgs
	ffType
	ffResume
	ffTest
)

type agentForm struct {
	index    int // in the agent list; -1 for a new agent
	field    int
	pwsh     bool
	claude   bool
	name     textinput.Model
	command  textinput.Model
	args     textinput.Model
	resume   textinput.Model
	err      string
	errField int
}

func newAgentForm(index int, a agentConfig) *agentForm {
	f := &agentForm{index: index, pwsh: a.Command == "", claude: a.Type == "claude", errField: -1}
	f.name = newCfgInput("shown in the agent row, e.g. smc")
	f.command = newCfgInput("")
	f.args = newCfgInput(`e.g. --model opus (quote arguments with spaces)`)
	f.resume = newCfgInput("e.g. smc -Resume {id}")
	f.name.SetValue(a.Name)
	f.command.SetValue(a.Pwsh)
	if !f.pwsh {
		f.command.SetValue(a.Command)
	}
	f.args.SetValue(joinArgs(a.Args))
	f.resume.SetValue(a.Resume)
	return f
}

func (f *agentForm) setWidth(w int) {
	for _, in := range []*textinput.Model{&f.name, &f.command, &f.args, &f.resume} {
		in.SetWidth(w)
	}
}

// fields are the form's rows for its current kind.
func (f *agentForm) fields() []int {
	if f.pwsh {
		return []int{ffName, ffKind, ffPick, ffCommand, ffType, ffResume, ffTest}
	}
	return []int{ffName, ffKind, ffCommand, ffArgs, ffType, ffTest}
}

func (f *agentForm) input(field int) *textinput.Model {
	switch field {
	case ffName:
		return &f.name
	case ffCommand:
		return &f.command
	case ffArgs:
		return &f.args
	case ffResume:
		return &f.resume
	}
	return nil
}

func (f *agentForm) focus(field int) tea.Cmd {
	f.field = field
	for _, in := range []*textinput.Model{&f.name, &f.command, &f.args, &f.resume} {
		in.Blur()
	}
	if in := f.input(field); in != nil {
		return in.Focus()
	}
	return nil
}

func (f *agentForm) move(d int) tea.Cmd {
	fs := f.fields()
	p := 0
	for i, x := range fs {
		if x == f.field {
			p = i
		}
	}
	return f.focus(fs[(p+d+len(fs))%len(fs)])
}

// config is the agent the form describes.
func (f *agentForm) config() (agentConfig, error) {
	a := agentConfig{Name: strings.TrimSpace(f.name.Value())}
	if f.claude {
		a.Type = "claude"
	}
	cmd := strings.TrimSpace(f.command.Value())
	if f.pwsh {
		a.Pwsh, a.Resume = cmd, strings.TrimSpace(f.resume.Value())
		return a, nil
	}
	a.Command = cmd
	args, err := splitCommand(f.args.Value())
	if err != nil {
		return a, err
	}
	if len(args) > 0 {
		a.Args = args
	}
	return a, nil
}

// check validates the form with the same rules as loading the file, and
// says which field is wrong.
func (f *agentForm) check(others []agentConfig) (agentConfig, bool) {
	f.err, f.errField = "", -1
	a, err := f.config()
	if err != nil {
		f.err, f.errField = err.Error(), ffArgs
		return a, false
	}
	if _, err := a.toAgent(); err != nil {
		f.err, f.errField = formError(err, f.pwsh)
		return a, false
	}
	for i, o := range others {
		if i != f.index && o.Name == a.Name {
			f.err, f.errField = fmt.Sprintf("there's already an agent named %q", a.Name), ffName
			return a, false
		}
	}
	return a, true
}

// formError turns a loading error into a message for a form field.
func formError(err error, pwsh bool) (string, int) {
	msg := err.Error()
	switch {
	case msg == "missing name":
		return "enter a name", ffName
	case strings.Contains(msg, `"resume"`):
		return `must contain {id}, e.g. "smc -Resume {id}"`, ffResume
	case strings.Contains(msg, `"command"`):
		if pwsh {
			return "enter a PowerShell command, function or alias", ffCommand
		}
		return "enter a program or script", ffCommand
	case strings.Contains(msg, "type"):
		return msg, ffType
	}
	return msg, ffName
}

// joinArgs shows arguments as one line that splitCommand reads back.
func joinArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		switch {
		case a == "":
			out[i] = `""`
		case !strings.ContainsAny(a, " \t\"'"):
			out[i] = a
		case !strings.Contains(a, `"`):
			out[i] = `"` + a + `"`
		default:
			out[i] = "'" + a + "'"
		}
	}
	return strings.Join(out, " ")
}

func (c *configScreen) openForm(index int) tea.Cmd {
	a := agentConfig{}
	if index >= 0 {
		a = c.agents[index]
	}
	c.form = newAgentForm(index, a)
	c.form.setWidth(min(max(c.width-20, 10), 80))
	return c.form.focus(ffName)
}

func (m *model) formKey(k tea.KeyPressMsg) tea.Cmd {
	c := m.cfg
	f := c.form
	key := k.String()
	switch key {
	case "esc":
		c.form = nil
		return c.focusRow(c.row)
	case "tab", "down":
		return f.move(1)
	case "shift+tab", "up":
		return f.move(-1)
	case "enter":
		if f.field == ffPick {
			return m.openPicker()
		}
		a, ok := f.check(c.agents)
		if !ok {
			return f.focus(f.errField)
		}
		if f.field == ffTest {
			return m.startTest(a) // as edited; the form stays open
		}
		if f.index < 0 {
			c.agents = append(c.agents, a)
			c.row = len(c.agents) - 1
			c.setStatus(fmt.Sprintf("Added %q. ctrl+s saves.", a.Name), false)
		} else {
			c.agents[f.index] = a
			c.row = f.index
			c.setStatus(fmt.Sprintf("Changed %q. ctrl+s saves.", a.Name), false)
		}
		c.form = nil
		return c.focusRow(c.row)
	case "ctrl+p":
		if f.pwsh {
			return m.openPicker()
		}
	}
	switch f.field {
	case ffKind, ffType:
		if isToggleKey(key) {
			if f.field == ffKind {
				f.pwsh = !f.pwsh
			} else {
				f.claude = !f.claude
			}
			f.err, f.errField = "", -1
		}
		return nil
	case ffPick, ffTest:
		return nil
	}
	in := f.input(f.field)
	before := in.Value()
	var cmd tea.Cmd
	*in, cmd = in.Update(k)
	if in.Value() != before && f.errField == f.field {
		f.err, f.errField = "", -1
	}
	return cmd
}

// Profile picker --------------------------------------------------------------

type profilePicker struct {
	filter textinput.Model
	hi     int
}

func (m *model) openPicker() tea.Cmd {
	c := m.cfg
	p := &profilePicker{filter: newCfgInput("type to filter")}
	p.filter.SetWidth(min(max(c.width-20, 10), 80))
	c.picker = p
	cmds := []tea.Cmd{p.filter.Focus()}
	if m.profile == nil && !m.profileLoading {
		m.profileLoading = true
		cmds = append(cmds, func() tea.Msg {
			list, err := loadProfileCommands()
			return profileCommandsMsg{list, err}
		})
	}
	return tea.Batch(cmds...)
}

// pickerMatches are the profile commands that match the filter.
func (m *model) pickerMatches() []profileCommand {
	if m.profile == nil || m.cfg == nil || m.cfg.picker == nil {
		return nil
	}
	// Matches on the name come first, then aliases whose target matches.
	var byName, byTarget []profileCommand
	q := m.cfg.picker.filter.Value()
	for _, pc := range m.profile.cmds {
		switch {
		case matchesFilter(q, pc.Name):
			byName = append(byName, pc)
		case matchesFilter(q, pc.Name, pc.Target):
			byTarget = append(byTarget, pc)
		}
	}
	return append(byName, byTarget...)
}

func (m *model) pickerKey(k tea.KeyPressMsg) tea.Cmd {
	c := m.cfg
	p := c.picker
	matches := m.pickerMatches()
	switch k.String() {
	case "esc":
		c.picker = nil
		return c.form.focus(c.form.field)
	case "down", "tab":
		if p.hi < len(matches)-1 {
			p.hi++
		}
		return nil
	case "up", "shift+tab":
		if p.hi > 0 {
			p.hi--
		}
		return nil
	case "enter":
		if p.hi >= len(matches) {
			return nil
		}
		c.form.pick(matches[p.hi])
		c.picker = nil
		return c.form.focus(ffName)
	}
	before := p.filter.Value()
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(k)
	if p.filter.Value() != before {
		p.hi = 0
	}
	return cmd
}

// pick fills the form from a profile function or alias. A function with a
// -Resume parameter is taken to be a Claude wrapper.
func (f *agentForm) pick(pc profileCommand) {
	f.pwsh = true
	f.command.SetValue(pc.Name)
	if strings.TrimSpace(f.name.Value()) == "" {
		f.name.SetValue(pc.Name)
	}
	if hint := pc.resumeHint(); hint != "" {
		f.claude = true
		f.resume.SetValue(hint)
	}
	f.err, f.errField = "", -1
}

// updateInputs passes other messages (cursor blinks) to the focused box.
func (c *configScreen) updateInputs(msg tea.Msg) tea.Cmd {
	var in *textinput.Model
	switch {
	case c.picker != nil:
		in = &c.picker.filter
	case c.form != nil:
		in = c.form.input(c.form.field)
	default:
		switch c.current().kind {
		case rowListKey:
			in = &c.listKey
		case rowHistoryLines:
			in = &c.histLines
		case rowEditor:
			in = &c.editor
		case rowTerminal:
			in = &c.terminal
		}
	}
	if in == nil {
		return nil
	}
	var cmd tea.Cmd
	*in, cmd = in.Update(msg)
	return cmd
}
