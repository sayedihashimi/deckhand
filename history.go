package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Restoring a session's output after deckhand restarts. A running session's
// scrollback and screen are saved to sessions/<id>/screen.ans when deckhand
// quits, when the session's process exits, and every 30 s while it prints.
// After a restart, an exited session shows that saved output, dimmed and
// read-only, until it's started again. For a Claude session the
// conversation from its transcript is the fallback, and v switches between
// the two.

// History settings, from [history] in the config file.
type historySettings struct {
	save  bool // write screen.ans
	lines int  // lines kept per session
}

// maxHistoryLines is the live scrollback's size, so it's also the most a
// save can hold (plus the screen).
const maxHistoryLines = 5000

func defaultHistorySettings() historySettings {
	return historySettings{save: true, lines: maxHistoryLines}
}

// historyOpts is read by background saves, so it's swapped whole.
var historyOpts atomic.Pointer[historySettings]

func currentHistory() historySettings {
	if p := historyOpts.Load(); p != nil {
		return *p
	}
	return defaultHistorySettings()
}

func setHistorySettings(h historySettings) { historyOpts.Store(&h) }

type historyConfig struct {
	Save  *bool `toml:"save,omitempty"`
	Lines int   `toml:"lines,omitempty"`
}

// loadHistoryConfig reads [history] from the config file.
func loadHistoryConfig() (historySettings, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultHistorySettings(), nil
	}
	if err != nil {
		return defaultHistorySettings(), err
	}
	return parseHistoryConfig(string(data))
}

func parseHistoryConfig(text string) (historySettings, error) {
	out := defaultHistorySettings()
	var cfg struct {
		History historyConfig `toml:"history"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return out, fmt.Errorf("%s: %w", configPath(), err)
	}
	if cfg.History.Save != nil {
		out.save = *cfg.History.Save
	}
	if err := checkHistoryLines(cfg.History.Lines); err != nil {
		return out, fmt.Errorf("%s: history.lines %w", configPath(), err)
	}
	if cfg.History.Lines != 0 {
		out.lines = cfg.History.Lines
	}
	return out, nil
}

// checkHistoryLines checks a lines value; 0 means the default.
func checkHistoryLines(n int) error {
	if n < 0 || n > maxHistoryLines {
		return fmt.Errorf("must be between 1 and %d", maxHistoryLines)
	}
	return nil
}

// Saving ----------------------------------------------------------------------

const (
	screenFile  = "screen.ans"
	screenMagic = "deckhand-screen"
	saveEvery   = 30 * time.Second
)

// savedScreen is a session's output as saved: lines of text with their SGR
// styles, oldest first.
type savedScreen struct {
	width, height int
	alt           bool // saved from the alternate screen: the visible screen only
	saved         time.Time
	lines         []string
}

// quittingAll is set once deckhand has saved every running session and is
// ending them, so their exits don't save again.
var quittingAll atomic.Bool

// saveAll saves the running sessions' output, in parallel.
func saveAll(sessions []*Session) {
	var wg sync.WaitGroup
	for _, s := range sessions {
		if s.Running() {
			wg.Go(s.SaveScreen)
		}
	}
	wg.Wait()
}

func screenPath(id string) string { return filepath.Join(sessionDir(id), screenFile) }

// captureScreen copies the emulator's scrollback and screen, keeping the
// last maxLines lines. On the alternate screen there's no scrollback, so
// it's the visible screen only. Trailing blank lines are dropped.
func captureScreen(emu *vt.SafeEmulator, maxLines int) savedScreen {
	scr := savedScreen{width: emu.Width(), height: emu.Height(), alt: emu.IsAltScreen(), saved: time.Now()}
	var lines []string
	if !scr.alt {
		sb := emu.ScrollbackLen()
		for y := max(sb-maxLines, 0); y < sb; y++ {
			lines = append(lines, scrollbackLine(emu, y))
		}
	}
	lines = append(lines, strings.Split(strings.ReplaceAll(emu.Render(), "\r\n", "\n"), "\n")...)
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ") // the rendering pads lines to the width
	}
	scr.lines = trimHistory(lines, maxLines)
	return scr
}

// trimHistory drops trailing blank lines, then keeps the last maxLines.
func trimHistory(lines []string, maxLines int) []string {
	n := len(lines)
	for n > 0 && strings.TrimSpace(ansi.Strip(lines[n-1])) == "" {
		n--
	}
	lines = lines[:n]
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

// encode is the file's text: a header line, then the lines.
func (s savedScreen) encode() []byte {
	var b bytes.Buffer
	alt := 0
	if s.alt {
		alt = 1
	}
	fmt.Fprintf(&b, "%s v1 width=%d height=%d alt=%d saved=%s\n", screenMagic, s.width, s.height, alt, s.saved.UTC().Format(time.RFC3339))
	for _, l := range s.lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func decodeScreen(data []byte) (savedScreen, error) {
	var s savedScreen
	head, body, _ := bytes.Cut(data, []byte("\n"))
	f := strings.Fields(string(head))
	if len(f) < 2 || f[0] != screenMagic || f[1] != "v1" {
		return s, errors.New("not a saved screen")
	}
	for _, kv := range f[2:] {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "width":
			s.width, _ = strconv.Atoi(v)
		case "height":
			s.height, _ = strconv.Atoi(v)
		case "alt":
			s.alt = v == "1"
		case "saved":
			s.saved, _ = time.Parse(time.RFC3339, v)
		}
	}
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) > 0 {
		for _, l := range strings.Split(string(body), "\n") {
			s.lines = append(s.lines, strings.TrimSuffix(l, "\r"))
		}
	}
	s.lines = trimHistory(s.lines, 0)
	return s, nil
}

// writeScreenFile writes a saved screen atomically: a temporary file in the
// same folder, then a rename.
func writeScreenFile(path string, s savedScreen) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".screen-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if _, err := tmp.Write(s.encode()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func loadScreenFile(path string) (*savedScreen, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := decodeScreen(data)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveScreen writes the session's output to screen.ans, unless [history]
// save is off. Saves of one session don't overlap.
func (s *Session) SaveScreen() {
	opts := currentHistory()
	if !opts.save {
		return
	}
	s.mu.Lock()
	emu, id := s.emu, s.ID
	s.mu.Unlock()
	if emu == nil || !validID(id) {
		return
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := writeScreenFile(screenPath(id), captureScreen(emu, opts.lines)); err != nil {
		debugf("save screen %s: %v", id[:min(8, len(id))], err)
	}
}

// MaybeSaveScreen saves a running session's output in the background if it
// printed something since the last save and that was saveEvery ago.
func (s *Session) MaybeSaveScreen(now time.Time) {
	s.mu.Lock()
	due := s.running && s.emu != nil && s.lastOutput.After(s.savedAt) && now.Sub(s.savedAt) >= saveEvery
	if due {
		s.savedAt = now
	}
	s.mu.Unlock()
	if !due || !currentHistory().save || !s.saving.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.saving.Store(false)
		s.SaveScreen()
	}()
}

// Showing it --------------------------------------------------------------------

// history is what an exited session can show instead of its terminal: its
// saved screen, and for Claude the conversation from its transcript.
type history struct {
	screen *savedScreen
	conv   []convEntry
	convAt time.Time // the transcript's modification time
}

// historyView is what the main pane shows for a session that isn't running.
type historyView int

const (
	viewNone         historyView = iota // the "Not running" message
	viewTerminal                        // the emulator from this run
	viewScreen                          // the saved screen
	viewConversation                    // the transcript
)

// chooseHistoryView picks what an exited session shows: the conversation
// when asked for (v) and there is one, otherwise its output (this run's
// terminal, then the saved screen), then the conversation, then the
// message.
func chooseHistoryView(hasTerminal, hasScreen, hasConv, wantConv bool) historyView {
	switch {
	case wantConv && hasConv:
		return viewConversation
	case hasTerminal:
		return viewTerminal
	case hasScreen:
		return viewScreen
	case hasConv:
		return viewConversation
	}
	return viewNone
}

// canToggleHistory reports whether v switches views: only for a Claude
// session that isn't running and has both output and a conversation.
func canToggleHistory(running, claude, hasOutput, hasConv bool) bool {
	return !running && claude && hasOutput && hasConv
}

// History returns the session's saved screen and conversation, loading them
// in the background the first time (nil until then).
func (s *Session) History() *history {
	s.mu.Lock()
	h := s.hist
	id, tr := s.ID, s.transcriptPathLocked()
	s.mu.Unlock()
	if h != nil || !s.histLoading.CompareAndSwap(false, true) {
		return h
	}
	go func() {
		defer s.histLoading.Store(false)
		h := &history{}
		if validID(id) {
			h.screen, _ = loadScreenFile(screenPath(id))
		}
		if tr != "" {
			h.conv, h.convAt, _ = readConversation(tr)
		}
		s.mu.Lock()
		s.hist = h
		s.mu.Unlock()
		if s.dirty != nil {
			s.dirty.Store(true)
		}
	}()
	return nil
}

// historyState is what the view and the v key need to know.
type historyState struct {
	view      historyView
	canToggle bool
	hist      *history
}

func (s *Session) historyState() historyState {
	running := s.Running()
	if running {
		return historyState{view: viewTerminal}
	}
	h := s.History()
	s.mu.Lock()
	hasTerm, want := s.emu != nil, s.wantConv
	s.mu.Unlock()
	a, _ := agentByName(s.Agent)
	var hasScreen, hasConv bool
	if h != nil {
		hasScreen = h.screen != nil && len(h.screen.lines) > 0
		hasConv = len(h.conv) > 0
	}
	return historyState{
		view:      chooseHistoryView(hasTerm, hasScreen, hasConv, want),
		canToggle: canToggleHistory(false, a.Claude, hasTerm || hasScreen, hasConv),
		hist:      h,
	}
}

// ToggleHistory switches between the output and the conversation, if it
// applies. It reports whether it did.
func (s *Session) ToggleHistory() bool {
	if !s.historyState().canToggle {
		return false
	}
	s.mu.Lock()
	s.wantConv = !s.wantConv
	s.scrollOff = 0
	s.mu.Unlock()
	return true
}

// historyLabel is the header text for an exited session's view:
// "last output · saved 2h ago · v conversation · ⏎ resume · ⇧⏎ new".
func historyLabel(st historyState, verb string, now time.Time) string {
	var parts []string
	switch st.view {
	case viewScreen:
		parts = append(parts, "last output")
		if st.hist != nil && st.hist.screen != nil && !st.hist.screen.saved.IsZero() {
			parts = append(parts, "saved "+shortAge(now.Sub(st.hist.screen.saved))+" ago")
		}
		if st.canToggle {
			parts = append(parts, "v conversation")
		}
	case viewConversation:
		parts = append(parts, "conversation")
		if st.canToggle {
			parts = append(parts, "v last output")
		}
	default:
		return ""
	}
	return strings.Join(append(parts, "⏎ "+verb, "⇧⏎ new"), " · ")
}

// sgrSeq matches an SGR sequence, to keep saved output faint after resets.
var sgrSeq = regexp.MustCompile(`\x1b\[([0-9;:]*)m`)

const faintSeq = "\x1b[2m"

// dimLine draws a styled line faint: the faint attribute is set at the start
// and again after anything that clears it (a reset, or 22 normal intensity).
func dimLine(l string) string {
	return faintSeq + sgrSeq.ReplaceAllStringFunc(l, func(seq string) string {
		params := seq[2 : len(seq)-1]
		if params == "" {
			return seq + faintSeq
		}
		for _, p := range strings.Split(params, ";") {
			if p == "0" || p == "00" || p == "22" {
				return seq + faintSeq
			}
		}
		return seq
	})
}

// savedView renders rows lines of saved output, w cells wide, dimmed and
// bottom-aligned, ending off lines above the last one. Lines saved at
// another width are cut or padded, not re-wrapped.
func savedView(lines []string, w, rows, off int) []string {
	off = min(max(off, 0), max(len(lines)-rows, 0))
	end := len(lines) - off
	start := end - rows
	out := make([]string, 0, rows)
	for i := start; i < end; i++ {
		l := ""
		if i >= 0 {
			l = dimLine(lines[i])
		}
		out = append(out, fit(l, w)+"\x1b[0m")
	}
	return out
}

// Conversation ------------------------------------------------------------------

// convEntry is a user prompt or an assistant reply, as plain lines.
type convEntry struct {
	user  bool
	lines []string
}

const (
	// conversationTail is how much of the end of a transcript is read for
	// the conversation view.
	conversationTail = 2 << 20
	maxConvEntries   = 12
	maxPromptLines   = 3
)

// readConversation reads the last prompts and replies from a transcript.
func readConversation(path string) ([]convEntry, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if start := fi.Size() - conversationTail; start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil, time.Time{}, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, time.Time{}, err
	}
	return parseConversation(data), fi.ModTime(), nil
}

// parseConversation keeps user prompts and assistant text from a transcript
// (JSONL). Tool calls and results, sidechains (sub-agents) and meta entries
// are left out; consecutive assistant entries make one reply.
func parseConversation(data []byte) []convEntry {
	var out []convEntry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), conversationTail+1)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e struct {
			Type        string `json:"type"`
			IsSidechain bool   `json:"isSidechain"`
			IsMeta      bool   `json:"isMeta"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.IsSidechain || e.IsMeta {
			continue // the first line may be cut off
		}
		switch e.Type {
		case "user":
			text := promptText(e.Message.Content)
			if text == "" {
				continue
			}
			out = append(out, convEntry{user: true, lines: []string{text}})
		case "assistant":
			var blocks []json.RawMessage
			if json.Unmarshal(e.Message.Content, &blocks) != nil {
				continue
			}
			lines := replyLines(replyText(blocks))
			if len(lines) == 0 {
				continue
			}
			if n := len(out); n > 0 && !out[n-1].user {
				out[n-1].lines = append(out[n-1].lines, lines...)
				continue
			}
			out = append(out, convEntry{lines: lines})
		}
	}
	if len(out) > maxConvEntries {
		out = out[len(out)-maxConvEntries:]
	}
	return out
}

// skippedPrompts are user entries that aren't something the user typed:
// slash command records, command output and injected reminders.
var skippedPrompts = []string{"<command-", "<local-command-", "<bash-", "<system-reminder>", "<task-notification>", "<user-prompt-submit-hook>"}

// promptText is a user entry's typed text: a string, or the text blocks of
// an array (tool results have none).
func promptText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) != nil {
			return ""
		}
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, " ")
	}
	text = oneLine(text)
	for _, p := range skippedPrompts {
		if strings.HasPrefix(text, p) {
			return ""
		}
	}
	return text
}

var (
	stConvPrompt = lipgloss.NewStyle().Foreground(colAccent).Faint(true)
	stConvReply  = lipgloss.NewStyle().Faint(true)
)

// conversationView renders the conversation in w×rows cells: a title, then
// the prompts and replies wrapped to the width, the latest at the bottom.
func conversationView(conv []convEntry, at time.Time, w, rows int, now time.Time) []string {
	title := "  last conversation (from the transcript"
	if !at.IsZero() {
		title += ", " + shortAge(now.Sub(at)) + " ago"
	}
	title += ")"
	blank := strings.Repeat(" ", max(w, 0))
	head := []string{blank, fit(stMuted.Render(title), w), blank}
	tw := max(w-4, 10)
	var body []string
	for _, e := range conv {
		if e.user {
			wrapped := strings.Split(ansi.Wrap("> "+e.lines[0], tw, ""), "\n")
			if len(wrapped) > maxPromptLines {
				wrapped = append(wrapped[:maxPromptLines-1], ansi.Truncate(wrapped[maxPromptLines-1], tw-1, "")+"…")
			}
			for _, l := range wrapped {
				body = append(body, fit("  "+stConvPrompt.Render(l), w))
			}
			continue
		}
		for _, p := range e.lines {
			for _, l := range strings.Split(ansi.Wrap(p, tw, ""), "\n") {
				body = append(body, fit("  "+stConvReply.Render(l), w))
			}
		}
	}
	if room := rows - len(head); len(body) > room {
		body = body[len(body)-max(room, 0):]
	}
	out := append(head, body...)
	for len(out) < rows {
		out = append(out, blank)
	}
	return out[:rows]
}
