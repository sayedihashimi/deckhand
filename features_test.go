package main

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Notifications ---------------------------------------------------------------

func TestNoticeFor(t *testing.T) {
	cases := []struct {
		prev, next Status
		detail     string
		want       noticeKind // 0 for none
	}{
		{StatusWorking, StatusWaiting, "permission: Bash", noticeWaiting},
		{StatusIdle, StatusWaiting, "bell", noticeWaiting},
		{StatusWaiting, StatusWaiting, "permission: Edit", 0}, // already told
		{StatusWorking, StatusIdle, "your turn", noticeFinished},
		{StatusWorking, StatusIdle, "interrupted?", 0},
		{StatusStarting, StatusIdle, "ready", 0},
		{StatusIdle, StatusIdle, "your turn", 0},
		{StatusWorking, StatusExited, "exited · ⏎ resume", 0},
	}
	for _, c := range cases {
		n, ok := noticeFor(c.prev, c.next, c.detail, "")
		if (c.want == 0) == ok || (ok && n.kind != c.want) {
			t.Errorf("%v -> %v (%q): got %+v, %v; want kind %v", c.prev, c.next, c.detail, n, ok, c.want)
		}
	}
}

func TestShouldNotify(t *testing.T) {
	cases := []struct {
		mode                        string
		focusKnown, focused, screen bool
		want                        bool
	}{
		{notifyOff, false, false, false, false},
		{notifyAlways, true, true, true, true},
		{notifyUnfocused, true, true, false, false},  // you're looking at deckhand
		{notifyUnfocused, true, false, true, true},   // deckhand is in the background
		{notifyUnfocused, false, false, true, false}, // no focus reports: the session is on screen
		{notifyUnfocused, false, false, false, true}, // no focus reports: another session
	}
	for _, c := range cases {
		if got := shouldNotify(c.mode, c.focusKnown, c.focused, c.screen); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestParseNotifyMode(t *testing.T) {
	cases := map[string]string{
		"":                                     notifyUnfocused,
		"[notifications]\nenabled = false\n":   notifyOff,
		"[notifications]\nwhen = \"always\"\n": notifyAlways,
		"[[agents]]\nname = \"x\"\ncommand = \"x\"": notifyUnfocused,
	}
	for text, want := range cases {
		got, err := parseNotifyMode(text)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", text, got, err, want)
		}
	}
	if _, err := parseNotifyMode("[notifications]\nwhen = \"sometimes\"\n"); err == nil {
		t.Error("bad when: want an error")
	}
	// The agents parser must still accept a config with [notifications].
	if _, err := parseAgents("[notifications]\nwhen = \"always\"\n"); err != nil {
		t.Errorf("parseAgents with [notifications]: %v", err)
	}
}

func TestNoticeText(t *testing.T) {
	title, body := noticeText("api", "claude", notice{kind: noticeWaiting, detail: "permission: Bash", summary: "npm test"}, "")
	if title != "api needs you" || body != "permission: Bash: npm test" {
		t.Errorf("waiting: %q / %q", title, body)
	}
	title, body = noticeText("api", "claude", notice{kind: noticeFinished}, "Done.\n\nAll   tests pass.")
	if title != "api finished" || body != "Done. All tests pass." {
		t.Errorf("finished: %q / %q", title, body)
	}
	if _, body = noticeText("sh", "pwsh", notice{kind: noticeWaiting, detail: "bell"}, ""); body != "pwsh rang the bell" {
		t.Errorf("bell: %q", body)
	}
	if got := xmlEscape(`a<b & "c"`); got != "a&lt;b &amp; &quot;c&quot;" {
		t.Errorf("xmlEscape: %q", got)
	}
}

func TestToolSummary(t *testing.T) {
	cases := map[string]string{
		`{"command":"npm   test\n--watch","description":"Run tests"}`: "npm test --watch",
		`{"file_path":"C:\\src\\a.go","old_string":"x"}`:              `C:\src\a.go`,
		`{"questions":[{"question":"?"}]}`:                            "",
		``:                                                            "",
	}
	for in, want := range cases {
		if got := toolSummary(json.RawMessage(in)); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

// Sidebar details ------------------------------------------------------------

func TestParseGitStatus(t *testing.T) {
	out := "# branch.oid abc\n# branch.head feature/x\n# branch.upstream origin/feature/x\n# branch.ab +2 -1\n1 .M N... 100644 100644 100644 a b README.md\n"
	g := parseGitStatus([]byte(out))
	if !g.ok || g.branch != "feature/x" || !g.dirty || g.ahead != 2 || g.behind != 1 {
		t.Errorf("got %+v", g)
	}
	if got := gitLabel(g); got != "feature/x*↑2↓1" {
		t.Errorf("label %q", got)
	}
	clean := parseGitStatus([]byte("# branch.oid abc\n# branch.head main\n"))
	if clean.dirty || gitLabel(clean) != "main" {
		t.Errorf("clean: %+v", clean)
	}
	if gitLabel(parseGitStatus([]byte("# branch.head (detached)\n"))) != "detached" {
		t.Error("detached")
	}
	if gitLabel(gitInfo{}) != "" {
		t.Error("not a repo should have no label")
	}
}

func TestParseTranscriptTail(t *testing.T) {
	lines := []string{
		`truncated line from the middle of a tool result"}`,
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"Old reply"}],"usage":{"input_tokens":1,"cache_creation_input_tokens":10,"cache_read_input_tokens":100}}}`,
		`{"type":"user","message":{"content":"next"}}`,
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"All done."}],"usage":{"input_tokens":2,"cache_creation_input_tokens":3000,"cache_read_input_tokens":80000}}}`,
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-haiku-5-5","content":[{"type":"text","text":"sub-agent"}],"usage":{"input_tokens":5}}}`,
		`{"type":"assistant","isSidechain":false,"message":{"model":"<synthetic>","content":[{"type":"text","text":"x"}]}}`,
	}
	model, tokens, text := parseTranscriptTail([]byte(strings.Join(lines[:5], "\n") + "\n"))
	if model != "claude-opus-5-5" || tokens != 83002 || text != "All done." {
		t.Errorf("got %q %d %q", model, tokens, text)
	}
	// A synthetic message (e.g. an API error) has text but no usage: keep
	// looking for the model further up.
	model, tokens, text = parseTranscriptTail([]byte(strings.Join(lines, "\n")))
	if model != "claude-opus-5-5" || tokens != 83002 || text != "x" {
		t.Errorf("with synthetic: got %q %d %q", model, tokens, text)
	}
}

func TestShortModelAndTokens(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5":            "opus 5.5",
		"claude-sonnet-4-5-20250929": "sonnet 4.5",
		"claude-fable-5":             "fable 5",
		"gpt-x":                      "gpt x",
	} {
		if got := shortModel(in); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[int]string{950: "950", 83002: "83k", 999_600: "1000k", 1_234_000: "1.2M"} {
		if got := shortTokens(in); got != want {
			t.Errorf("shortTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessTranscriptPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", `C:\cfg`)
	got := guessTranscriptPath(`C:\data\my_code\api`, "abc")
	if want := `C:\cfg\projects\C--data-my-code-api\abc.jsonl`; got != want && !strings.HasSuffix(got, "C--data-my-code-api/abc.jsonl") {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Filtering ----------------------------------------------------------------

func TestMatchesFilter(t *testing.T) {
	fields := []string{"api-refactor", "claude", "api", `C:\src\api`, "feature/login"}
	for filter, want := range map[string]bool{
		"":             true,
		"api":          true,
		"API claude":   true,
		"login":        true,
		"arf":          true, // letters in order
		"codex":        false,
		"api codex":    false,
		"  claude   ":  true,
		`src\api`:      true,
		"feature/logx": false,
	} {
		if got := matchesFilter(filter, fields...); got != want {
			t.Errorf("%q: got %v", filter, got)
		}
	}
}

func testModel(names ...string) *model {
	m := &model{}
	for _, n := range names {
		m.sessions = append(m.sessions, &Session{ID: n, Name: n, Agent: "pwsh", Dir: `C:\x\` + n})
	}
	return m
}

func TestFilterNavigation(t *testing.T) {
	m := testModel("api", "docs", "api-tests", "web")
	m.filter = "api"
	if got := m.visible(); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("visible = %v", got)
	}
	m.sel = 0
	m.moveSel(1)
	if m.sel != 2 {
		t.Errorf("down: sel = %d, want 2 (docs is hidden)", m.sel)
	}
	m.moveSel(1)
	if m.sel != 2 {
		t.Errorf("down at the end: sel = %d", m.sel)
	}
	if !m.selectVisible(0) || m.sel != 0 {
		t.Errorf("selectVisible(0): sel = %d", m.sel)
	}
	if m.selectVisible(5) {
		t.Error("selectVisible past the end should fail")
	}

	// The filter hides the selection: it moves to the first match.
	m.filter = ""
	m.sel = 3
	m.filter = "docs"
	m.keepSelectionVisible()
	if m.sel != 1 {
		t.Errorf("keepSelectionVisible: sel = %d", m.sel)
	}
	// Nothing matches: no current session.
	m.filter = "zzz"
	m.keepSelectionVisible()
	if m.current() != nil {
		t.Error("current() should be nil when the filter hides everything")
	}
	m.clearFilter()
	if m.current() == nil || m.filtering() {
		t.Error("clearFilter should show everything again")
	}
}

func TestAttentionFilter(t *testing.T) {
	m := testModel("a", "b", "c")
	m.sessions[1].status = StatusWaiting
	m.sessions[2].unread = true
	m.sel = 0
	m.attentionOnly = true
	// The selected session stays even though it doesn't need you.
	if got := m.visible(); len(got) != 3 {
		t.Errorf("visible with selection kept = %v", got)
	}
	m.sel = 1
	if got := m.visible(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("visible = %v", got)
	}
}

func TestFilterKeys(t *testing.T) {
	m := testModel("api", "docs", "web")
	press := func(s string) {
		var k tea.KeyPressMsg
		switch s {
		case "esc":
			k = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			k = tea.KeyPressMsg{Code: tea.KeyEnter}
		default:
			r := []rune(s)[0]
			k = tea.KeyPressMsg{Code: r, Text: s}
		}
		m.handleKey(k)
	}
	press("/")
	if m.filterInput == nil {
		t.Fatal("/ should open the filter")
	}
	for _, r := range "we" {
		press(string(r))
	}
	if m.filter != "we" || m.sel != 2 {
		t.Errorf("typing: filter %q, sel %d", m.filter, m.sel)
	}
	press("esc")
	if m.filter != "" || m.filterInput != nil {
		t.Errorf("esc: filter %q", m.filter)
	}
	press("@")
	if !m.attentionOnly {
		t.Error("@ should turn on the attention filter")
	}
	press("esc")
	if m.attentionOnly {
		t.Error("esc in the list should clear the attention filter")
	}
}
