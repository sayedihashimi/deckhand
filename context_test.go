package main

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestContextWindow(t *testing.T) {
	cfg, err := parseInfoConfig(`[info]
context_window = 300000
[info.context_windows]
"claude-opus-4-6" = 1000000
"my-model" = 32000
`)
	if err != nil {
		t.Fatal(err)
	}
	def := defaultInfoSettings()
	for _, tc := range []struct {
		name           string
		o              infoSettings
		model, stModel string
		stWindow       int
		want           int
	}{
		{"status line wins", def, "claude-opus-5-5", "claude-opus-5-5", 200000, 200000},
		{"status line with [1m]", def, "claude-opus-4-6", "claude-opus-4-6[1m]", 1000000, 1000000},
		{"status line for another model is ignored", def, "claude-sonnet-4-5", "claude-opus-5-5", 1000000, 200000},
		{"status line before the transcript", def, "", "claude-opus-5-5", 200000, 200000},
		{"1M model", def, "claude-opus-5-5", "", 0, 1000000},
		{"1M family", def, "claude-fable-5-2", "", 0, 1000000},
		{"[1m] suffix", def, "claude-opus-4-6[1m]", "", 0, 1000000},
		{"other Claude", def, "claude-sonnet-4-5-20250929", "", 0, 200000},
		{"not Claude", def, "gpt-5", "", 0, 0},
		{"no model", def, "", "", 0, 0},
		{"per-model config", cfg, "claude-opus-4-6", "", 0, 1000000},
		{"per-model config, any model", cfg, "my-model-v2", "", 0, 32000},
		{"config default", cfg, "claude-sonnet-4-5", "", 0, 300000},
		{"config default, not Claude", cfg, "gpt-5", "", 0, 300000},
		{"1M beats the config default", cfg, "claude-opus-5-5", "", 0, 1000000},
	} {
		if got := contextWindow(tc.o, tc.model, tc.stModel, tc.stWindow); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	for _, bad := range []string{`[info]` + "\ncontext = \"bars\"", `[info]` + "\ncontext_window = -1", `[info.context_windows]` + "\n\"x\" = 0"} {
		if _, err := parseInfoConfig(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestContextFormat(t *testing.T) {
	if got := contextVariants(ctxShowMeter, 142000, 200000); strings.Join(got, "|") != "▰▰▰▰▱ 71%|71%" {
		t.Errorf("meter: %q", got)
	}
	if got := contextVariants(ctxShowPercent, 142000, 200000); strings.Join(got, "|") != "71%" {
		t.Errorf("percent: %q", got)
	}
	if got := contextVariants(ctxShowTokens, 142000, 200000); strings.Join(got, "|") != "142k/200k|142k" {
		t.Errorf("tokens: %q", got)
	}
	if got := contextVariants(ctxShowMeter, 142000, 0); strings.Join(got, "|") != "142k" {
		t.Errorf("no window: %q", got)
	}
	if got := contextVariants(ctxShowMeter, 0, 200000); got != nil {
		t.Errorf("no context: %q", got)
	}
	for _, tc := range []struct {
		pct  float64
		want string
	}{{0, "▱▱▱▱▱"}, {1, "▰▱▱▱▱"}, {50, "▰▰▰▱▱"}, {70, "▰▰▰▰▱"}, {100, "▰▰▰▰▰"}, {130, "▰▰▰▰▰"}} {
		if got := contextMeter(tc.pct); got != tc.want {
			t.Errorf("meter(%v) = %q, want %q", tc.pct, got, tc.want)
		}
	}
	// The colour thresholds.
	if contextStyle(59.9).GetForeground() != colMuted || contextStyle(60).GetForeground() != colWorking ||
		contextStyle(80).GetForeground() != colWorking || contextStyle(80.1).GetForeground() != colWaiting {
		t.Error("colour thresholds")
	}
}

// The info line at the sidebar's widths: the model goes first, then the
// meter, then the cost, then the percentage.
func TestInfoLineContext(t *testing.T) {
	s := &Session{Name: "api", Agent: "claude", Dir: `C:\src\api`}
	sn := snapshot{
		info:      sessionInfo{git: gitInfo{ok: true, branch: "main", dirty: true}, model: "claude-sonnet-4-5", ctxTokens: 142000},
		ctxWindow: 200000,
		cost:      "$1.84",
	}
	for _, tc := range []struct {
		w    int
		want string
	}{
		{50, "main* · sonnet 4.5 · ▰▰▰▰▱ 71% · $1.84"},
		{30, "main* · ▰▰▰▰▱ 71% · $1.84"},
		{24, "main* · 71% · $1.84"},
		{16, "main* · 71%"},
		{8, "main*"},
	} {
		if got := ansi.Strip(infoLine(s, sn, tc.w)); got != tc.want {
			t.Errorf("width %d: %q, want %q", tc.w, got, tc.want)
		}
	}
	defer func(o infoSettings) { infoOpts = o }(infoOpts)
	infoOpts.context = ctxShowTokens
	if got := ansi.Strip(infoLine(s, sn, 26)); got != "main* · 142k/200k · $1.84" {
		t.Errorf("tokens at 26: %q", got)
	}
	if got := ansi.Strip(infoLine(s, sn, 24)); got != "main* · 142k · $1.84" {
		t.Errorf("tokens at 24: %q", got)
	}
	// Coloured by how full it is: orange over 80%.
	sn.info.ctxTokens = 170000
	infoOpts.context = ctxShowPercent
	if got := infoLine(s, sn, 24); !strings.Contains(got, contextStyle(85).Render("85%")) {
		t.Errorf("85%% should be orange: %q", got)
	}
}

// The sidebar at 24 and 40 columns, from a session's own snapshot.
func TestSidebarContext(t *testing.T) {
	m := testModel("api")
	s := m.sessions[0]
	s.Agent = "claude"
	s.info = sessionInfo{git: gitInfo{ok: true, branch: "main"}, model: "claude-sonnet-4-5", ctxTokens: 130000, compacted: true}
	s.status = StatusIdle
	for _, tc := range []struct {
		w    int
		want string
	}{{24, "main · ▰▰▰▱▱ 65%"}, {40, "main · sonnet 4.5 · ▰▰▰▱▱ 65%"}} {
		lines := m.renderSidebar(tc.w, 10)
		row3 := strings.TrimSpace(ansi.Strip(lines[4]))
		if !strings.HasSuffix(row3, tc.want) {
			t.Errorf("width %d: third line %q, want %q", tc.w, row3, tc.want)
		}
		if row2 := ansi.Strip(lines[3]); !strings.Contains(row2, "↻ compact") {
			t.Errorf("width %d: second line %q lacks the compacted mark", tc.w, row2)
		}
	}
	// The header shows the window size and the percentage.
	m.width, m.height = 120, 20
	w, _ := m.termSize()
	main, _ := m.renderMain(w, 10)
	if !strings.Contains(ansi.Strip(main[0]), "130k/200k context · 65%") {
		t.Errorf("header %q", ansi.Strip(main[0]))
	}
}

func TestCompaction(t *testing.T) {
	reply := `{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":5,"cache_read_input_tokens":150000}}}`
	boundary := `{"type":"system","subtype":"compact_boundary","content":"Conversation compacted"}`
	summary := `{"type":"user","isCompactSummary":true,"message":{"content":"summary"}}`
	if compactedAfterReply([]byte(reply + "\n")) {
		t.Error("no compaction yet")
	}
	if !compactedAfterReply([]byte(reply + "\n" + boundary + "\n" + summary + "\n")) {
		t.Error("compaction after the reply not seen")
	}
	if compactedAfterReply([]byte(boundary + "\n" + summary + "\n" + reply + "\n")) {
		t.Error("a reply after the compaction ends it")
	}
	for _, tc := range []struct {
		before, after int
		want          bool
	}{{150000, 30000, true}, {150000, 90000, false}, {30000, 10000, true}, {20000, 5000, false}, {0, 1000, false}} {
		if got := compactDrop(tc.before, tc.after); got != tc.want {
			t.Errorf("compactDrop(%d, %d) = %v", tc.before, tc.after, got)
		}
	}
}

// The drop heuristic holds until the next reply changes the context.
func TestCompactedUntilNextReply(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	dir := t.TempDir()
	tr := dir + `\t.jsonl`
	line := func(tokens int) string {
		return `{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":0,"cache_read_input_tokens":` + strconv.Itoa(tokens) + `}}}` + "\n"
	}
	s := &Session{ID: "c1", Name: "api", Agent: "claude", Dir: dir, TranscriptPath: tr}
	poll := func(tokens int) bool {
		f, _ := os.OpenFile(tr, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		_, _ = f.WriteString(line(tokens))
		_ = f.Close()
		s.lastPoll = s.lastPoll.AddDate(-1, 0, 0)
		s.MaybePoll(s.lastPoll.AddDate(1, 0, 1), true)
		for s.polling.Load() {
		}
		return s.Snapshot().info.compacted
	}
	if poll(150000) {
		t.Error("compacted before any drop")
	}
	if !poll(25000) {
		t.Error("a sharp drop should show compacted")
	}
	if poll(31000) {
		t.Error("the next reply should clear it")
	}
}

func TestConfigScreenContext(t *testing.T) {
	keepGlobals(t)
	defer func(o infoSettings) { infoOpts = o }(infoOpts)
	t.Setenv("DECKHAND_HOME", t.TempDir())
	p := tempConfig(t, "[info]\ncontext_window = 300000\n[info.context_windows]\n\"my-model\" = 32000\n")
	m := testModel("api")
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	m.cfg.focusRow(m.cfg.rowOf(rowListContext))
	w, _ := m.termSize()
	screen := ansi.Strip(strings.Join(m.renderConfig(w, m.height-1), "\n"))
	if !strings.Contains(screen, "Context") || !strings.Contains(screen, "meter") || !strings.Contains(screen, "percent") {
		t.Errorf("config screen lacks the context row:\n%s", screen)
	}
	m.handleKey(key("right")) // percent
	m.handleKey(key("ctrl+s"))
	if m.cfg.statusErr {
		t.Fatalf("save: %q", m.cfg.status)
	}
	data, _ := os.ReadFile(p)
	for _, want := range []string{`context = "percent"`, "context_window = 300000", "my-model"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config.toml lacks %q:\n%s", want, data)
		}
	}
	if infoOpts.context != ctxShowPercent || infoOpts.windows["my-model"] != 32000 {
		t.Errorf("not applied: %+v", infoOpts)
	}
}
