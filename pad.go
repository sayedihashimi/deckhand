package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// The scratch pad: every session has a small text editor for notes, in a
// pane below its terminal. You can draft prompts there while the agent
// works and send them into the session (ctrl+enter).
//
//   - e in the list opens the selected session's pad; F6 ([pad] key) in a
//     session opens it and moves between the pad and the terminal. esc goes
//     back to the terminal, and esc again soon after closes the pad.
//   - The agent's terminal gets the rows the pad doesn't use, so its PTY is
//     resized when the pad opens or closes. Whether the pad is open is kept
//     per session (Session.PadOpen, in sessions.json).
//   - The notes are one Markdown file per session, notes\<id>.md in the data
//     folder, saved a second after typing stops, when focus leaves the pad
//     and on quit. An empty pad has no file. A temporary window doesn't
//     save them. Removing a session (x) archives its notes to
//     notes\archive\<id> <name>.md unless you ask to delete them.
//   - ctrl+o opens the file in the configured editor; deckhand reloads the
//     pad when the file changes.

// padSettings come from [pad] in the config file.
type padSettings struct {
	key   string  // opens the pad and switches focus, as Bubble Tea names it
	share float64 // the pad's share of the rows below the header, or 0
	lines int     // or its height in lines, when set
}

const (
	defaultPadKey   = "f6"
	defaultPadShare = 0.33
)

var padOpts = defaultPadSettings()

func defaultPadSettings() padSettings {
	return padSettings{key: defaultPadKey, share: defaultPadShare}
}

type padConfig struct {
	Key    string `toml:"key,omitempty"`
	Height any    `toml:"height,omitempty"` // a share such as 0.33, or a number of lines
}

// loadPadConfig reads [pad] from the config file. It's read after [list],
// whose keys it can't clash with.
func loadPadConfig() (padSettings, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultPadSettings(), nil
	}
	if err != nil {
		return defaultPadSettings(), err
	}
	return parsePadConfig(string(data), listOpts)
}

func parsePadConfig(text string, list listSettings) (padSettings, error) {
	out := defaultPadSettings()
	var cfg struct {
		Pad padConfig `toml:"pad"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return out, fmt.Errorf("%s: %w", configPath(), err)
	}
	if err := setPadHeight(&out, cfg.Pad.Height); err != nil {
		return out, fmt.Errorf("%s: pad.height %w", configPath(), err)
	}
	key, err := checkListKey(cfg.Pad.Key, "f6 or f7")
	if err != nil {
		return out, fmt.Errorf("%s: pad.key %w", configPath(), err)
	}
	if key != "" {
		out.key = key
	}
	if out.key == list.toggleKey || out.key == list.back() {
		// The default can clash too, with a [list] key set to F6: then the
		// pad has no key until one is set (e still opens it).
		bad := out.key
		out.key = ""
		return out, fmt.Errorf("%s: pad.key is %s, the same as a [list] key; they must be different keys", configPath(), keyLabel(bad))
	}
	return out, nil
}

// setPadHeight reads [pad] height: a share of the rows (0.33) or a number
// of lines (12).
func setPadHeight(o *padSettings, v any) error {
	switch h := v.(type) {
	case nil:
		return nil
	case int64:
		if h < 3 {
			return fmt.Errorf("must be at least 3 lines, or a share such as 0.33, not %d", h)
		}
		o.lines, o.share = int(h), 0
	case float64:
		switch {
		case h > 0 && h < 1:
			o.share = h
		case h >= 3 && h == math.Trunc(h):
			o.lines, o.share = int(h), 0
		default:
			return fmt.Errorf("must be a share from 0 to 1 such as 0.33, or a number of lines, not %v", h)
		}
	default:
		return fmt.Errorf("must be a number, not %v", v)
	}
	return nil
}

// padRowsFor is how many rows the pad takes of the h rows below the
// session header, its title line included. The terminal keeps at least 3.
func padRowsFor(h int) int {
	n := padOpts.lines
	if n == 0 {
		n = int(math.Round(padOpts.share * float64(h)))
	}
	n = min(max(n, 3), h-3)
	if n < 2 {
		return 0 // no room
	}
	return n
}

// Storage --------------------------------------------------------------------

func notesDir() string { return filepath.Join(dataDir(), "notes") }

func padPath(id string) string { return filepath.Join(notesDir(), id+".md") }

// fileStamp tells whether a file changed: its size and modification time.
type fileStamp struct {
	size int64
	mod  time.Time
	ok   bool
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{fi.Size(), fi.ModTime(), true}
}

// writePadFile writes a pad's notes atomically, through a temporary file
// and a rename, as sessions.json is. Empty notes remove the file, unless
// keep (the external editor needs a file to open).
func writePadFile(id, text string, keep bool) error {
	if !validID(id) {
		return fmt.Errorf("bad session ID %q", id)
	}
	p := padPath(id)
	if text == "" && !keep {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(notesDir(), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func readPadFile(id string) (string, error) {
	if !validID(id) {
		return "", nil
	}
	data, err := os.ReadFile(padPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return cleanPadText(string(data)), err
}

// archiveName is the file name kept notes get: "<id> <name>.md", with
// characters a file name can't have replaced.
func archiveName(id, name string) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return id + ".md"
	}
	return id + " " + name + ".md"
}

// The pads -------------------------------------------------------------------

// scratchPad is one session's pad, loaded from disk the first time it's
// needed.
type scratchPad struct {
	ed       *padEditor
	saved    int       // the editor's version when it was last saved or loaded
	editedAt time.Time // the last change, for saving once typing stops
	stamp    fileStamp // the file as last written or read
	watch    bool      // opened in the external editor: reload when it changes
	checked  time.Time // when the file was last checked for changes

	note    string // a short message in the footer ("sent 3 lines")
	noteAt  time.Time
	noteErr bool
}

// padState is the model's pads, by session ID, and the pad's mouse and esc
// state.
type padState struct {
	pads map[string]*scratchPad

	escAt     time.Time // esc left the pad; another esc soon after closes it
	dragging  bool      // a press in the pad: motion selects
	clickAt   time.Time // the last click, for a double click
	clickX    int
	clickY    int
	deleteFor string // the session x is removing with its notes deleted
}

// padSaveDelay is how long after typing stops the pad is saved.
const padSaveDelay = time.Second

// padEscWindow is how soon a second esc (in the terminal) closes the pad.
// Later, esc goes to the agent as usual.
const padEscWindow = 1500 * time.Millisecond

// padWatchEvery is how often a pad open in the external editor is checked.
const padWatchEvery = 300 * time.Millisecond

func (m *model) padStore() *padState {
	if m.pads == nil {
		m.pads = &padState{pads: map[string]*scratchPad{}}
	}
	return m.pads
}

// pad is a session's pad, loading it the first time.
func (m *model) pad(s *Session) *scratchPad {
	ps := m.padStore()
	if p := ps.pads[s.ID]; p != nil {
		return p
	}
	p := &scratchPad{}
	text := ""
	if !m.temp {
		var err error
		if text, err = readPadFile(s.ID); err != nil {
			p.setNote("Couldn't read the notes: "+err.Error(), true)
		}
		p.stamp = stampOf(padPath(s.ID))
	}
	p.ed = newPadEditor(text)
	p.saved = p.ed.version
	ps.pads[s.ID] = p
	return p
}

// padHasNotes reports whether a session's pad has any text, for the list's
// ✎ and the x prompt.
func (m *model) padHasNotes(s *Session) bool {
	return len(m.pad(s).ed.text) > 0
}

// padMark goes before the age on a session's first row in the list: ✎ when
// its pad has notes, with a space before the age, otherwise nothing.
func (m *model) padMark(s *Session, age string) string {
	switch {
	case !m.padHasNotes(s):
		return ""
	case age == "":
		return stMuted.Render("✎")
	}
	return stMuted.Render("✎") + " "
}

func (p *scratchPad) setNote(text string, isErr bool) {
	p.note, p.noteAt, p.noteErr = text, time.Now(), isErr
}

// savePad writes a pad if it changed since it was last saved. keep writes
// even empty notes, for the external editor.
func (m *model) savePad(id string, p *scratchPad, keep bool) {
	if p.ed.version == p.saved && !keep {
		return
	}
	p.saved = p.ed.version
	if m.temp {
		return // a temporary window's pads aren't saved
	}
	if err := writePadFile(id, p.ed.Value(), keep); err != nil {
		p.setNote("Couldn't save the notes: "+err.Error(), true)
		return
	}
	p.stamp = stampOf(padPath(id))
}

// saveAllPads saves every pad with unsaved changes (on quit).
func (m *model) saveAllPads() {
	if m.pads == nil {
		return
	}
	for id, p := range m.pads.pads {
		m.savePad(id, p, false)
	}
}

// padFocused is the pad that has the keyboard, if any.
func (m *model) padFocused() (*Session, *scratchPad) {
	if m.focus != focusPad {
		return nil, nil
	}
	s := m.current()
	if s == nil || !s.PadOpen {
		return nil, nil
	}
	return s, m.pad(s)
}

// padTick runs every frame: it saves pads once typing has stopped, or as
// soon as focus leaves them, and reloads pads whose file the external editor
// changed.
func (m *model) padTick(now time.Time) {
	if m.pads == nil {
		return
	}
	_, focused := m.padFocused()
	for id, p := range m.pads.pads {
		if p.watch && !m.temp && now.Sub(p.checked) >= padWatchEvery {
			p.checked = now
			m.reloadPad(id, p)
		}
		if p.ed.version != p.saved && (p != focused || now.Sub(p.editedAt) >= padSaveDelay) {
			m.savePad(id, p, false)
		}
	}
}

// reloadPad loads the pad's file again if it changed since deckhand last
// wrote or read it. Unsaved edits in the pad give way to the file's text;
// ctrl+z brings them back.
func (m *model) reloadPad(id string, p *scratchPad) {
	st := stampOf(padPath(id))
	if st == p.stamp || !st.ok {
		return
	}
	text, err := readPadFile(id)
	if err != nil {
		return // being written; try again next time
	}
	p.stamp = st
	if text == p.ed.Value() {
		return
	}
	p.ed.setValue(text)
	p.saved = p.ed.version
	p.setNote("reloaded from the editor", false)
	if m.dirty != nil {
		m.dirty.Store(true)
	}
}

// setPadOpen opens or closes a session's pad, resizing its terminal to the
// rows that are left, and remembers it in sessions.json.
func (m *model) setPadOpen(s *Session, open bool) {
	if !open {
		m.savePad(s.ID, m.pad(s), false)
	}
	if s.PadOpen == open {
		return
	}
	s.mu.Lock()
	s.PadOpen = open
	s.mu.Unlock()
	if m.width > 0 && m.height > 0 {
		s.Resize(m.sessionSize(s))
	}
	_ = m.saveSessions()
}

// padRows is how many rows the session's pad takes, or 0 when it's closed.
func (m *model) padRows(s *Session) int {
	if s == nil || !s.PadOpen {
		return 0
	}
	_, h := m.termSize()
	return padRowsFor(h)
}

// sessionSize is the size of a session's terminal: termSize, less its pad.
func (m *model) sessionSize(s *Session) (int, int) {
	w, h := m.termSize()
	return w, h - m.padRows(s)
}

// openPad (e in the list) opens the selected session's pad and focuses it.
// The session isn't started: the notes are there for an exited session too.
func (m *model) openPad() tea.Cmd {
	s := m.current()
	if s == nil || s.preparing.Load() {
		return nil
	}
	m.setPadOpen(s, true)
	m.focus = focusPad
	return nil
}

// padLeave moves focus from the pad to the terminal, or to the list when
// the session isn't running.
func (m *model) padLeave(s *Session) {
	m.savePad(s.ID, m.pad(s), false)
	m.pad(s).ed.endGroup()
	m.pads.dragging = false
	if s.Running() {
		m.focus = focusTerminal
	} else {
		m.focus = focusSidebar
	}
}

// padTerminalKey handles the pad's keys in a session's terminal: the pad
// key opens or focuses the pad, and esc right after leaving the pad closes
// it. Every other key, and a later esc, goes to the agent.
func (m *model) padTerminalKey(key string) bool {
	s := m.current()
	if s == nil {
		return false
	}
	ps := m.padStore()
	escAt := ps.escAt
	ps.escAt = time.Time{}
	switch {
	case padOpts.key != "" && key == padOpts.key:
		m.setPadOpen(s, true)
		m.focus = focusPad
		return true
	case key == "esc" && s.PadOpen && time.Since(escAt) < padEscWindow:
		m.setPadOpen(s, false)
		return true
	}
	return false
}

// padKey handles a key while the pad has focus.
func (m *model) padKey(k tea.KeyPressMsg) tea.Cmd {
	s, p := m.padFocused()
	if s == nil {
		m.focus = focusSidebar
		return nil
	}
	key := k.String()
	switch {
	case key == listOpts.back() || key == "ctrl+q" || key == "ctrl+]":
		m.savePad(s.ID, p, false)
		m.setListHidden(false)
		m.focus = focusSidebar
		return nil
	case padOpts.key != "" && key == padOpts.key:
		m.padLeave(s)
		return nil
	}
	switch key {
	case "esc":
		if p.ed.hasSelection() {
			p.ed.anchor = -1 // first, esc drops the selection
			return nil
		}
		m.padLeave(s)
		m.pads.escAt = time.Now()
		return nil
	case "ctrl+enter", "alt+enter", "ctrl+j":
		// ctrl+j: a console without key records sends ctrl+enter as LF.
		m.padSend(s, p)
		return nil
	case "ctrl+o":
		m.padOpenEditor(s, p)
		return nil
	}
	before := p.ed.version
	p.ed.editKey(k)
	if p.ed.version != before {
		p.editedAt = time.Now()
	}
	return nil
}

// padPaste pastes into the focused pad (bracketed paste, or ctrl+v that the
// terminal turned into one).
func (m *model) padPaste(text string) {
	if _, p := m.padFocused(); p != nil {
		p.ed.insert(text, false)
		p.editedAt = time.Now()
	}
}

// Sending --------------------------------------------------------------------

// padPayload is what sending text types into a session: the text with
// Enter's \r for each line end, inside bracketed paste markers when the
// program turned that mode on, so a multi-line prompt is one message.
func padPayload(text string, bracketed bool) string {
	t := strings.ReplaceAll(text, "\n", "\r")
	if bracketed {
		return ansi.BracketedPasteStart + t + ansi.BracketedPasteEnd
	}
	return t
}

// padSendText is the text ctrl+enter sends: the selection, or the whole pad,
// without blank lines and spaces at its ends.
func padSendText(e *padEditor) string {
	text := e.selectedText()
	if text == "" {
		text = e.Value()
	}
	text = strings.TrimRight(text, " \t\n")
	return strings.TrimLeft(text, "\n")
}

// padEnterDelay is the pause between the text and Enter, so a program that
// reads a paste in pieces has it all before Enter submits it.
var padEnterDelay = 80 * time.Millisecond

// SendInput types text into the agent and presses Enter; false when the
// session isn't running.
func (s *Session) SendInput(text string) bool {
	s.mu.Lock()
	emu, keys, running, bracketed := s.emu, s.keys, s.running, s.bracketed
	s.mu.Unlock()
	if emu == nil || !running {
		return false
	}
	s.ScrollToBottom()
	emu.SendText(padPayload(text, bracketed))
	go func() {
		time.Sleep(padEnterDelay)
		sendKey(emu, keys, uv.KeyPressEvent{Code: uv.KeyEnter})
	}()
	return true
}

// padSend sends the selection, or the whole pad, to the session. Mid-turn
// it sends anyway: Claude queues it, as it does typing.
func (m *model) padSend(s *Session, p *scratchPad) {
	if !s.Running() {
		p.setNote("session has exited · ⏎ resume first", true)
		return
	}
	text := padSendText(p.ed)
	if strings.TrimSpace(text) == "" {
		p.setNote("nothing to send", true)
		return
	}
	if !s.SendInput(text) {
		p.setNote("session has exited · ⏎ resume first", true)
		return
	}
	n := strings.Count(text, "\n") + 1
	if n == 1 {
		p.setNote("sent 1 line", false)
	} else {
		p.setNote(fmt.Sprintf("sent %d lines", n), false)
	}
	p.ed.endGroup()
}

// The external editor ----------------------------------------------------

// padEditorTemplate is the command ctrl+o runs: the configured editor (as
// o uses it), else VS Code if it's on PATH, else Notepad. {dir} is the
// notes file.
func padEditorTemplate(cfg openConfig, lookPath func(string) (string, error)) string {
	tmpl, fallback := editorTemplate(cfg, lookPath)
	if fallback {
		return `notepad.exe "{dir}"`
	}
	return tmpl
}

// padOpenEditor (ctrl+o) saves the pad, unsaved edits first, and opens its
// file in the editor. deckhand then reloads the pad when the file changes.
func (m *model) padOpenEditor(s *Session, p *scratchPad) {
	if m.temp {
		p.setNote("a temporary window doesn't save its notes, so there's no file to open", true)
		return
	}
	m.savePad(s.ID, p, true)
	path := padPath(s.ID)
	prog, args, err := expandTemplate(padEditorTemplate(openSettings, exec.LookPath), path)
	if err == nil {
		err = openStart(prog, args, notesDir())
	}
	if err != nil {
		p.setNote("Couldn't open the editor: "+err.Error(), true)
		return
	}
	p.watch = true
	p.stamp = stampOf(path)
	p.setNote("opened in the editor · saved changes reload here", false)
}

// Removing ---------------------------------------------------------------------

// padConfirmRemove adds the notes to x's confirm prompt when the pad has
// any: y (or enter) keeps them in the archive, D deletes them.
func (m *model) padConfirmRemove(s *Session) {
	if m.confirm == nil || !m.padHasNotes(s) {
		return
	}
	c := m.confirm
	c.text = strings.TrimSuffix(c.text, " Y/n") + " Y/n · notes are kept (D deletes them)"
	remove := c.action
	c.alt = map[string]func(m *model) tea.Cmd{"D": func(m *model) tea.Cmd {
		m.padStore().deleteFor = s.ID
		return remove(m)
	}}
}

// padRemoved archives a removed session's notes, or deletes them when x was
// answered with D, and forgets its pad.
func (m *model) padRemoved(s *Session) {
	ps := m.padStore()
	del := ps.deleteFor == s.ID
	ps.deleteFor = ""
	p := m.pad(s)
	delete(ps.pads, s.ID)
	if m.temp || !validID(s.ID) {
		return
	}
	if del {
		_ = os.Remove(padPath(s.ID))
		return
	}
	m.savePad(s.ID, p, false)
	if _, err := os.Stat(padPath(s.ID)); err != nil {
		return // no notes
	}
	dir := filepath.Join(notesDir(), "archive")
	if err := os.MkdirAll(dir, 0o700); err == nil {
		err = os.Rename(padPath(s.ID), filepath.Join(dir, archiveName(s.ID, s.Name)))
		if err != nil {
			m.setFlash("Couldn't archive the notes: " + err.Error())
		}
	}
}

// Drawing --------------------------------------------------------------------

// padShown is the session whose pad is drawn, and its rows: the main pane
// shows the selected session's terminal (not a dialog or another screen).
func (m *model) padShown() (*Session, int) {
	if m.cfg != nil || m.closing != nil || m.diff != nil || m.dialog != nil {
		return nil, 0
	}
	s := m.current()
	if s == nil || s.preparing.Load() {
		return nil, 0
	}
	return s, m.padRows(s)
}

var (
	stPadTitle = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stPadOK    = lipgloss.NewStyle().Foreground(colIdle)
)

// overlayPad draws the selected session's pad over the bottom rows of the
// main pane. While the pad has focus, the cursor is the pad's.
func (m *model) overlayPad(main []string, w int, cur *tea.Cursor) ([]string, *tea.Cursor) {
	s, rows := m.padShown()
	if rows == 0 || rows >= len(main) {
		return main, cur
	}
	p := m.pad(s)
	focused := m.focus == focusPad
	top := len(main) - rows

	// The title line is the border between the panes: highlighted while
	// the pad has focus.
	border := stBorder
	title := stMuted.Render(" scratch pad ")
	if focused {
		border = lipgloss.NewStyle().Foreground(colAccent)
		title = stPadTitle.Render(" scratch pad ")
	}
	// Its key, while it doesn't have focus; then the footer has its keys.
	hint := ""
	if !focused && padOpts.key != "" {
		hint = " " + keyLabel(padOpts.key) + " notes "
	}
	fill := w - 2 - ansi.StringWidth(title) - ansi.StringWidth(hint) - 2
	line := border.Render("──") + title + border.Render(strings.Repeat("─", max(fill, 0))) + stMuted.Render(hint) + border.Render("──")
	main[top] = fit(line, w) + "\x1b[0m"

	ed := p.ed
	ed.setSize(w-1, rows-1)
	cx, cy := ed.cursorPos()
	body := ed.view()
	if len(ed.text) == 0 {
		body[0] = fit(stMuted.Render("Notes for this session. "+padEmptyHint(focused)), w-1)
	}
	for i, l := range body {
		main[top+1+i] = " " + l + "\x1b[0m"
	}
	if focused {
		cur = tea.NewCursor(m.mainLeft()+1+cx, top+1+cy)
	}
	return main, cur
}

func padEmptyHint(focused bool) string {
	if focused {
		return "ctrl+enter sends them to the agent, ctrl+o opens your editor."
	}
	return keyLabel(padOpts.key) + " to write here."
}

// padFooter is the footer while the pad has focus: its last message, then
// its keys.
func (m *model) padFooter(w int) string {
	hints := stMuted.Render(footerHints(helpPad, false))
	if ansi.StringWidth(hints) > w {
		hints = stMuted.Render(footerHints(helpPad, true))
	}
	if _, p := m.padFocused(); p != nil && p.note != "" && time.Since(p.noteAt) < 4*time.Second {
		st := stPadOK
		if p.noteErr {
			st = lipgloss.NewStyle().Foreground(colError)
		}
		return " " + st.Render(displayPathsIn(p.note)) + stMuted.Render(" ·") + hints
	}
	return hints
}

// Mouse ----------------------------------------------------------------------

// padDoubleClick is how soon a second click on the same cell selects a word.
const padDoubleClick = 400 * time.Millisecond

// padMouse handles the mouse over the pad: a click places the cursor and
// focuses the pad, a drag selects, a double click selects a word and the
// wheel scrolls. Once a press starts in the pad, its motion and release are
// the pad's wherever they happen. It reports whether it took the event.
func (m *model) padMouse(msg tea.MouseMsg) bool {
	ps := m.padStore()
	s, rows := m.padShown()
	if rows == 0 {
		ps.dragging = false
		return false
	}
	p := m.pad(s)
	ms := msg.Mouse()
	bodyH := m.height - 1
	top := bodyH - rows // the title line
	x, y := ms.X-m.mainLeft()-1, ms.Y-top-1
	in := ms.X >= m.mainLeft() && ms.Y >= top && ms.Y < bodyH

	switch msg.(type) {
	case tea.MouseMotionMsg:
		if !ps.dragging {
			return in
		}
		if y < 0 {
			p.ed.scroll(-1)
		} else if y >= p.ed.height {
			p.ed.scroll(1)
		}
		p.ed.moveTo(p.ed.indexAt(x, y), true)
		return true
	case tea.MouseReleaseMsg:
		if ps.dragging {
			ps.dragging = false
			return true
		}
		return in
	}
	if !in {
		return false
	}
	switch msg.(type) {
	case tea.MouseWheelMsg:
		switch ms.Button {
		case tea.MouseWheelUp:
			p.ed.scroll(-wheelLines)
		case tea.MouseWheelDown:
			p.ed.scroll(wheelLines)
		}
	case tea.MouseClickMsg:
		m.focus = focusPad
		if ms.Button != tea.MouseLeft || y < 0 {
			return true
		}
		i := p.ed.indexAt(x, y)
		now := time.Now()
		if now.Sub(ps.clickAt) < padDoubleClick && ps.clickX == ms.X && ps.clickY == ms.Y {
			a, b := p.ed.wordAt(i)
			p.ed.selectRange(a, b)
			ps.clickAt = time.Time{}
			return true
		}
		ps.clickAt, ps.clickX, ps.clickY = now, ms.X, ms.Y
		p.ed.moveTo(i, false)
		ps.dragging = true
	}
	return true
}

// padLines fills lines with blank ones to h, below a terminal that's
// shorter than the pane because of its pad.
func padLines(lines []string, w, h int) []string {
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:h]
}
