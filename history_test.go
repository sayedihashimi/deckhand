package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// newTestEmulator is an emulator that has been fed text.
func newTestEmulator(w, h int, text string) *vt.SafeEmulator {
	emu := vt.NewSafeEmulator(w, h)
	emu.SetScrollbackSize(maxHistoryLines)
	_, _ = emu.Write([]byte(text))
	return emu
}

func stripped(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestScreenRoundTrip(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "line-%d \x1b[31mred\x1b[0m \x1b[1mbold\x1b[0m\r\n", i)
	}
	b.WriteString("PS> ")
	emu := newTestEmulator(40, 6, b.String())
	scr := captureScreen(emu, maxHistoryLines)
	if scr.width != 40 || scr.height != 6 || scr.alt {
		t.Errorf("header fields: %+v", scr)
	}
	got := stripped(scr.lines)
	if len(got) != 21 || got[0] != "line-1 red bold" || got[19] != "line-20 red bold" || got[20] != "PS>" {
		t.Fatalf("lines (scrollback then screen, trailing blanks dropped):\n%q", got)
	}
	if !strings.Contains(scr.lines[0], "\x1b[31m") || !strings.Contains(scr.lines[0], "\x1b[1m") {
		t.Errorf("styles lost: %q", scr.lines[0])
	}

	back, err := decodeScreen(scr.encode())
	if err != nil {
		t.Fatal(err)
	}
	if back.width != 40 || back.height != 6 || back.alt || !back.saved.Equal(scr.saved.Truncate(time.Second)) {
		t.Errorf("header after decode: %+v, saved %v", back, scr.saved)
	}
	if strings.Join(back.lines, "\n") != strings.Join(scr.lines, "\n") {
		t.Errorf("lines changed:\n%q\n%q", back.lines, scr.lines)
	}
	if _, err := decodeScreen([]byte("hello\nworld\n")); err == nil {
		t.Error("a file without the header should be refused")
	}
}

func TestCaptureCapAndAltScreen(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "row %d\r\n", i)
	}
	emu := newTestEmulator(20, 5, b.String())
	got := stripped(captureScreen(emu, 10).lines)
	if len(got) != 10 || got[0] != "row 41" || got[9] != "row 50" {
		t.Fatalf("capped at 10 lines: %q", got)
	}

	// The alternate screen: only what's visible, none of the scrollback.
	_, _ = emu.Write([]byte("\x1b[?1049h\x1b[H\x1b[2Jfull screen app\r\nsecond row"))
	scr := captureScreen(emu, maxHistoryLines)
	if !scr.alt {
		t.Error("alt should be recorded")
	}
	if got := stripped(scr.lines); len(got) != 2 || got[0] != "full screen app" || got[1] != "second row" {
		t.Errorf("alt screen: %q", got)
	}
	if !strings.Contains(string(scr.encode()), " alt=1 ") {
		t.Errorf("header: %q", strings.SplitN(string(scr.encode()), "\n", 2)[0])
	}
}

func TestTrimHistory(t *testing.T) {
	got := trimHistory([]string{"a", "", "b", "  ", "\x1b[0m", ""}, 0)
	if strings.Join(got, "|") != "a||b" {
		t.Errorf("trailing blanks: %q", got)
	}
	if got := trimHistory([]string{"1", "2", "3"}, 2); strings.Join(got, "|") != "2|3" {
		t.Errorf("cap: %q", got)
	}
}

func TestWriteScreenFileAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "abc")
	p := filepath.Join(dir, screenFile)
	for _, text := range []string{"first", "second"} {
		if err := writeScreenFile(p, savedScreen{width: 10, height: 3, saved: time.Now(), lines: []string{text}}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := loadScreenFile(p)
	if err != nil || len(s.lines) != 1 || s.lines[0] != "second" {
		t.Fatalf("load: %v %+v", err, s)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("only screen.ans should be left, got %q", names)
	}
}

func TestSaveScreenSetting(t *testing.T) {
	keepGlobals(t)
	t.Setenv("DECKHAND_HOME", t.TempDir())
	s := &Session{ID: "sess-1", emu: newTestEmulator(20, 4, "hello\r\n")}
	setHistorySettings(historySettings{save: false, lines: 100})
	s.SaveScreen()
	if _, err := os.Stat(screenPath(s.ID)); !os.IsNotExist(err) {
		t.Fatal("save = false shouldn't write anything")
	}
	setHistorySettings(defaultHistorySettings())
	s.SaveScreen()
	got, err := loadScreenFile(screenPath(s.ID))
	if err != nil || len(got.lines) != 1 || ansi.Strip(got.lines[0]) != "hello" {
		t.Fatalf("saved: %v %+v", err, got)
	}
}

func TestMaybeSaveScreenTiming(t *testing.T) {
	keepGlobals(t)
	t.Setenv("DECKHAND_HOME", t.TempDir())
	setHistorySettings(defaultHistorySettings())
	now := time.Now()
	s := &Session{ID: "sess-2", emu: newTestEmulator(20, 4, "hi\r\n"), running: true, savedAt: now}
	s.lastOutput = now.Add(time.Second)
	s.MaybeSaveScreen(now.Add(10 * time.Second))
	if !s.savedAt.Equal(now) {
		t.Fatal("not due before 30 s")
	}
	s.MaybeSaveScreen(now.Add(31 * time.Second))
	if !s.savedAt.Equal(now.Add(31 * time.Second)) {
		t.Fatal("due after 30 s with new output")
	}
	s.MaybeSaveScreen(now.Add(70 * time.Second))
	if !s.savedAt.Equal(now.Add(31 * time.Second)) {
		t.Error("no new output since the last save: not due")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(screenPath(s.ID)); err == nil && !s.saving.Load() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the background save didn't write screen.ans")
}

// Only default-foreground text is dimmed; coloured text keeps its colour
// and intensity, and text the line made faint itself stays faint.
func TestDimLine(t *testing.T) {
	line := "a" +
		"\x1b[31mb" + // red
		"\x1b[0mc" + // reset: default again
		"\x1b[1;38;5;196md" + // bold, 256-colour
		"\x1b[39me" + // bold, default colour
		"\x1b[0;38;2;10;200;10mf" + // truecolor
		"\x1b[38:2::1:2:3mg" + // truecolor, colon form
		"\x1b[2;32mh" + // the app's own faint green
		"\x1b[22;39mi" + // normal default
		"\x1b[32mj\x1b[m"
	emu := newTestEmulator(20, 2, dimLine(line))
	for x, want := range []struct {
		ch          string
		faint, bold bool
	}{
		{"a", true, false}, {"b", false, false}, {"c", true, false}, {"d", false, true},
		{"e", true, true}, {"f", false, false}, {"g", false, false}, {"h", true, false},
		{"i", true, false}, {"j", false, false},
	} {
		c := emu.CellAt(x, 0)
		faint, bold := c.Style.Attrs&uv.AttrFaint != 0, c.Style.Attrs&uv.AttrBold != 0
		if c.Content != want.ch || faint != want.faint || bold != want.bold {
			t.Errorf("%q: faint %v bold %v, want faint %v bold %v", c.Content, faint, bold, want.faint, want.bold)
		}
	}
	if got := dimLine("plain"); got != faintSeq+"plain" {
		t.Errorf("plain text: %q", got)
	}
	if got := dimLine("\x1b[31mred"); got != faintSeq+"\x1b[31m"+unfaintSeq+"red" {
		t.Errorf("coloured text should turn our faint off: %q", got)
	}
}

func TestSavedView(t *testing.T) {
	lines := []string{"one", "two", "three", "a much longer line than the pane"}
	// Fewer lines than rows: bottom-aligned.
	got := stripped(savedView(lines, 10, 6, 0))
	if len(got) != 6 || strings.TrimSpace(got[0]) != "" || got[2] != "one       " || got[5] != "a much lo…" {
		t.Errorf("bottom-aligned, cut and padded to 10: %q", got)
	}
	for _, l := range savedView(lines, 10, 6, 0)[2:] {
		if ansi.StringWidth(l) != 10 || !strings.Contains(l, faintSeq) {
			t.Errorf("each line is 10 cells and faint: %q", l)
		}
	}
	// Scrolled back one line, with two rows.
	got = stripped(savedView(lines, 10, 2, 1))
	if strings.TrimSpace(got[0]) != "two" || strings.TrimSpace(got[1]) != "three" {
		t.Errorf("scrolled: %q", got)
	}
	// Too far: clamped to the top.
	got = stripped(savedView(lines, 10, 2, 99))
	if strings.TrimSpace(got[0]) != "one" {
		t.Errorf("clamped: %q", got)
	}
}

const testTranscript = `{"type":"user","message":{"content":"fix the login redirect loop"}}
{"type":"assistant","isSidechain":false,"message":{"content":[{"type":"text","text":"I'll look at **login.ts**."},{"type":"tool_use","name":"Read","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"file contents"}]}}
{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"sub-agent text"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"The redirect loop comes from the session check running before the cookie is set. I moved it after the token refresh."}]}}
{"type":"user","isMeta":true,"message":{"content":"Caveat: generated by a command"}}
{"type":"user","message":{"content":"<command-name>/clear</command-name>"}}
{"type":"user","message":{"content":[{"type":"text","text":"run the tests"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"All 112 tests pass."}]}}
`

func TestParseConversation(t *testing.T) {
	conv := parseConversation([]byte("cut off line\"}\n" + testTranscript))
	var got []string
	for _, e := range conv {
		p := "A: "
		if e.user {
			p = "U: "
		}
		got = append(got, p+strings.Join(e.lines, " / "))
	}
	want := []string{
		"U: fix the login redirect loop",
		"A: I'll look at login.ts. / The redirect loop comes from the session check running before the cookie is set. I moved it after the token refresh.",
		"U: run the tests",
		"A: All 112 tests pass.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("conversation:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Only the last entries are kept.
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, `{"type":"user","message":{"content":"prompt %d"}}`+"\n", i)
	}
	conv = parseConversation([]byte(b.String()))
	if len(conv) != maxConvEntries || conv[len(conv)-1].lines[0] != "prompt 29" {
		t.Errorf("kept %d entries, last %q", len(conv), conv[len(conv)-1].lines)
	}
}

func TestConversationView(t *testing.T) {
	now := time.Now()
	conv := parseConversation([]byte(testTranscript))
	lines := conversationView(conv, now.Add(-2*time.Hour), 60, 20, now)
	if len(lines) != 20 {
		t.Fatalf("%d lines", len(lines))
	}
	text := stripped(lines)
	if !strings.Contains(text[1], "last conversation (from the transcript, 2h ago)") {
		t.Errorf("title: %q", text[1])
	}
	joined := strings.Join(text, "\n")
	for _, want := range []string{"> fix the login redirect loop", "> run the tests", "All 112 tests pass."} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q:\n%s", want, joined)
		}
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 60 {
			t.Errorf("line %d is %d cells: %q", i, w, text[i])
		}
	}
	wrapped := false
	for i := range text[:len(text)-1] {
		if strings.TrimSpace(text[i]) == "The redirect loop comes from the session check running" &&
			strings.HasPrefix(text[i+1], "  before the cookie") {
			wrapped = true
		}
	}
	if !wrapped {
		t.Errorf("replies wrap to the width:\n%s", joined)
	}
	// Not enough room: the latest stay, at the bottom.
	small := stripped(conversationView(conv, now, 40, 5, now))
	if strings.TrimSpace(small[4]) != "All 112 tests pass." || !strings.Contains(small[3], "> run the tests") {
		t.Errorf("small:\n%s", strings.Join(small, "\n"))
	}
}

func TestChooseHistoryView(t *testing.T) {
	for _, c := range []struct {
		term, screen, conv, want bool
		view                     historyView
	}{
		{false, true, true, false, viewScreen},
		{false, true, true, true, viewConversation},
		{false, false, true, false, viewConversation}, // no saved screen: the fallback
		{false, true, false, true, viewScreen},        // asked for, but no transcript
		{false, false, false, false, viewNone},
		{true, false, true, false, viewTerminal},
		{true, false, true, true, viewConversation},
	} {
		if got := chooseHistoryView(c.term, c.screen, c.conv, c.want); got != c.view {
			t.Errorf("chooseHistoryView(%v, %v, %v, %v) = %v, want %v", c.term, c.screen, c.conv, c.want, got, c.view)
		}
	}
	if !canToggleHistory(false, true, true, true) {
		t.Error("exited Claude session with output and a transcript should toggle")
	}
	for _, c := range [][4]bool{{true, true, true, true}, {false, false, true, true}, {false, true, false, true}, {false, true, true, false}} {
		if canToggleHistory(c[0], c[1], c[2], c[3]) {
			t.Errorf("canToggleHistory%v should be false", c)
		}
	}
}

// historyModel is a model with an exited Claude session that has a saved
// screen and a conversation, and a pwsh session with a saved screen.
func historyModel(t *testing.T) *model {
	t.Helper()
	m := testModel("api", "notes")
	m.width, m.height = 120, 32
	now := time.Now()
	api := m.sessions[0]
	api.Agent, api.Started = "claude", true
	api.hist = &history{
		screen: &savedScreen{saved: now.Add(-2 * time.Hour), lines: []string{"\x1b[1m> fix it\x1b[0m", "screen marker"}},
		conv:   parseConversation([]byte(testTranscript)),
		convAt: now.Add(-2 * time.Hour),
	}
	m.sessions[1].hist = &history{screen: &savedScreen{saved: now, lines: []string{"PS> notes marker"}}}
	m.resizeAll()
	return m
}

func TestHistoryToggle(t *testing.T) {
	m := historyModel(t)
	main := func() string {
		lines, _ := m.renderMain(m.width-m.mainLeft(), m.height-1)
		return ansi.Strip(strings.Join(lines, "\n"))
	}
	screen := main()
	if !strings.Contains(screen, "last output · saved 2h ago · v conversation · ⏎ resume · ⇧⏎ new") || !strings.Contains(screen, "screen marker") {
		t.Fatalf("saved screen first:\n%s", screen)
	}
	if f := ansi.Strip(m.renderFooter(200)); !strings.Contains(f, "v switch view") {
		t.Errorf("footer should offer v when it fits: %q", f)
	}
	if f := ansi.Strip(m.renderFooter(130)); !strings.Contains(f, "v view") {
		t.Errorf("short footer should offer v when it fits: %q", f)
	}
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, "? help") || ansi.StringWidth(f) != 120 {
		t.Errorf("at 120 cells the short footer still fits: %q", f)
	}

	m.handleKey(key("v"))
	screen = main()
	if !strings.Contains(screen, "conversation · v last output") || !strings.Contains(screen, "> run the tests") || strings.Contains(screen, "screen marker") {
		t.Fatalf("after v:\n%s", screen)
	}
	m.handleKey(key("v"))
	if screen = main(); !strings.Contains(screen, "screen marker") {
		t.Fatalf("v again goes back:\n%s", screen)
	}

	// A pwsh session has no transcript: v does nothing and isn't offered.
	m.sel = 1
	screen = main()
	if !strings.Contains(screen, "notes marker") || !strings.Contains(screen, "last output") || strings.Contains(screen, "v conversation") {
		t.Fatalf("pwsh:\n%s", screen)
	}
	if m.sessions[1].ToggleHistory() {
		t.Error("v shouldn't toggle a non-Claude session")
	}
	if f := ansi.Strip(m.renderFooter(200)); strings.Contains(f, "v switch") || strings.Contains(f, "v view") {
		t.Errorf("footer shouldn't offer v: %q", f)
	}
}

func TestHistoryFallbackToConversation(t *testing.T) {
	m := historyModel(t)
	m.sessions[0].hist.screen = nil
	lines, _ := m.renderMain(m.width-m.mainLeft(), m.height-1)
	screen := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(screen, "last conversation (from the transcript") || !strings.Contains(screen, "  conversation · ⏎ resume") {
		t.Fatalf("no saved screen: the conversation:\n%s", screen)
	}
	if m.sessions[0].ToggleHistory() {
		t.Error("nothing to switch to without a saved screen")
	}
	// Neither: the old message.
	m.sessions[0].hist.conv = nil
	lines, _ = m.renderMain(m.width-m.mainLeft(), m.height-1)
	if screen := ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(screen, "Not running. Press enter to resume") {
		t.Fatalf("neither:\n%s", screen)
	}
}

func TestHistoryScroll(t *testing.T) {
	m := historyModel(t)
	s := m.sessions[1]
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("saved-%d", i))
	}
	s.hist.screen.lines = lines
	m.sel = 1
	_, rows := m.termSize()
	if !s.Scroll(3) || s.ScrollOffset() != 3 {
		t.Fatalf("offset %d", s.ScrollOffset())
	}
	out, _ := m.renderMain(m.width-m.mainLeft(), m.height-1)
	text := ansi.Strip(strings.Join(out, "\n"))
	if !strings.Contains(text, "↑ 3 lines back") || !strings.Contains(text, "saved-97") || strings.Contains(text, "saved-98") {
		t.Fatalf("scrolled 3:\n%s", text)
	}
	m.handleKey(key("shift+pgup"))
	if got := s.ScrollOffset(); got != 3+rows-2 {
		t.Errorf("shift+PgUp: offset %d, want %d", got, 3+rows-2)
	}
	s.Scroll(1000)
	if got := s.ScrollOffset(); got != 100-rows {
		t.Errorf("clamped to %d, want %d", got, 100-rows)
	}
	m.handleKey(key("shift+pgdown"))
	s.Scroll(-1000)
	if s.ScrollOffset() != 0 {
		t.Error("back to the bottom")
	}
	// The conversation doesn't scroll.
	c := m.sessions[0]
	c.wantConv = true
	if c.Scroll(3) || c.ScrollOffset() != 0 {
		t.Error("the conversation view shouldn't scroll")
	}
}

func TestParseHistoryConfig(t *testing.T) {
	for _, c := range []struct {
		text  string
		want  historySettings
		isErr bool
	}{
		{"", historySettings{true, 5000}, false},
		{"[history]\nsave = false\n", historySettings{false, 5000}, false},
		{"[history]\nlines = 200\n", historySettings{true, 200}, false},
		{"[history]\nlines = 9000\n", historySettings{true, 5000}, true},
		{"[history]\nlines = -1\n", historySettings{true, 5000}, true},
	} {
		got, err := parseHistoryConfig(c.text)
		if got != c.want || (err != nil) != c.isErr {
			t.Errorf("%q: %+v, %v", c.text, got, err)
		}
	}
}

func TestConfigScreenHistory(t *testing.T) {
	keepGlobals(t)
	t.Setenv("DECKHAND_HOME", t.TempDir())
	p := tempConfig(t, "")
	m := testModel("api")
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	m.cfg.focusRow(m.cfg.rowOf(rowHistoryLines))
	w, _ := m.termSize()
	screen := ansi.Strip(strings.Join(m.renderConfig(w, m.height-1), "\n"))
	for _, want := range []string{"History", "Save", "Lines", "screen.ans"} {
		if !strings.Contains(screen, want) {
			t.Errorf("config screen lacks %q:\n%s", want, screen)
		}
	}
	typeText(m, "abc")
	m.handleKey(key("ctrl+s"))
	if !m.cfg.statusErr || m.cfg.current().kind != rowHistoryLines {
		t.Fatalf("bad lines: status %q, row %v", m.cfg.status, m.cfg.current())
	}
	m.cfg.histLines.SetValue("")
	typeText(m, "300")
	m.cfg.focusRow(m.cfg.rowOf(rowHistorySave))
	m.handleKey(key("right")) // off
	m.handleKey(key("ctrl+s"))
	if m.cfg.statusErr {
		t.Fatalf("save: %q", m.cfg.status)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "[history]") || !strings.Contains(string(data), "save = false") || !strings.Contains(string(data), "lines = 300") {
		t.Errorf("config.toml:\n%s", data)
	}
	if h := currentHistory(); h.save || h.lines != 300 {
		t.Errorf("not applied: %+v", h)
	}
	m.handleKey(key("esc"))
	m.handleKey(key("c"))
	if m.cfg.histSave || m.cfg.histLines.Value() != "300" || m.cfg.dirty() {
		t.Errorf("reopened: save %v lines %q dirty %v", m.cfg.histSave, m.cfg.histLines.Value(), m.cfg.dirty())
	}
}
