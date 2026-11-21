package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A transcript with the cases that matter: a reply written as two lines
// (one per content block, same message ID and usage), a sub-agent's reply,
// a 1-hour cache write, an unknown model, a user line and a line still
// being written.
func TestTranscriptCost(t *testing.T) {
	yesterday := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	lines := []string{
		`{"type":"user","message":{"content":"hi"}}`,
		`{"type":"assistant","timestamp":"` + yesterday + `","requestId":"r1","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"thinking"}],"usage":{"input_tokens":1000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":10000,"output_tokens":500}}}`,
		`{"type":"assistant","timestamp":"` + yesterday + `","requestId":"r1","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":10000,"output_tokens":500}}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"` + now + `","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":99999,"output_tokens":99999}}}`,
		`{"type":"assistant","timestamp":"` + now + `","message":{"id":"m3","model":"claude-sonnet-5-5-20261001","usage":{"input_tokens":0,"cache_creation_input_tokens":1000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":1000},"cache_read_input_tokens":0,"output_tokens":100}}}`,
		`{"type":"assistant","timestamp":"` + now + `","message":{"id":"m4","model":"some-other-model","usage":{"input_tokens":10,"output_tokens":20}}}`,
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	partial := `{"type":"assistant","message":{"id":"m5","model":"claude-opus-5-5","usage":{"input_tokens":1000000`
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	var tc transcriptCost
	added, err := tc.read(path, builtinPrices, time.Now())
	if err != nil || !added {
		t.Fatalf("read: added %v, err %v", added, err)
	}
	// m1 once: 1000*4 + 2000*5 + 10000*0.2 + 500*20 = 26000 µ$.
	// m3: 1000 at the 1-hour write price (2*2) + 100*10 = 5000 µ$.
	// m4: unknown model, tokens only.
	wantUSD := (26000.0 + 5000) / 1e6
	wantTokens := (1000 + 2000 + 500) + (1000 + 100) + (10 + 20)
	if !near(tc.sum.usd, wantUSD) || tc.sum.tokens != wantTokens {
		t.Errorf("sum = %v $, %d tokens; want %v $, %d", tc.sum.usd, tc.sum.tokens, wantUSD, wantTokens)
	}
	if !near(tc.before.usd, 26000.0/1e6) || tc.before.tokens != 3500 {
		t.Errorf("before midnight = %+v, want m1 only", tc.before)
	}

	// Nothing new: no change. Then the partial line is finished.
	if added, _ := tc.read(path, builtinPrices, time.Now()); added {
		t.Error("an unchanged transcript added something")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`,"output_tokens":0}}}` + "\n")
	_ = f.Close()
	if added, _ := tc.read(path, builtinPrices, time.Now()); !added || !near(tc.sum.usd, wantUSD+4) {
		t.Errorf("after the line was finished: added %v, sum %v; want %v", added, tc.sum.usd, wantUSD+4)
	}
}

func TestPriceFor(t *testing.T) {
	for _, tc := range []struct {
		model string
		in    float64
		ok    bool
	}{
		{"claude-opus-5-5", 4, true},
		{"claude-opus-5-5[1m]", 4, true},
		{"claude-opus-4-1-20250805", 15, true},
		{"claude-opus-4-8", 5, true},
		{"claude-sonnet-4-5-20250929", 3, true},
		{"claude-haiku-4-5", 1, true},
		{"gpt-5", 0, false},
		{"", 0, false},
	} {
		p, ok := priceFor(builtinPrices, tc.model)
		if ok != tc.ok || p.Input != tc.in {
			t.Errorf("priceFor(%q) = %v, %v; want input %v, %v", tc.model, p.Input, ok, tc.in, tc.ok)
		}
	}
}

func TestParseCostConfig(t *testing.T) {
	c, err := parseCostConfig("")
	if err != nil || c.show != costDollars || !c.statusLine {
		t.Fatalf("default = %+v, %v", c, err)
	}
	c, err = parseCostConfig(`[cost]
show = "Tokens"
statusline = false
[cost.prices]
"claude-opus-5-5" = { input = 3, output = 15 }
"my-model" = { input = 1, output = 2, cache_read = 0.5 }
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.show != costTokens || c.statusLine {
		t.Errorf("show %q statusline %v", c.show, c.statusLine)
	}
	if p, _ := priceFor(c.prices, "claude-opus-5-5"); p.Input != 3 || !near(p.CacheRead, 0.3) || !near(p.CacheWrite, 3.75) || p.CacheWrite1h != 6 {
		t.Errorf("override = %+v", p)
	}
	if p, ok := priceFor(c.prices, "my-model-2"); !ok || p.CacheRead != 0.5 {
		t.Errorf("added model = %+v, %v", p, ok)
	}
	if p, _ := priceFor(c.prices, "claude-sonnet-5-5"); p.Input != 2 {
		t.Errorf("built-in prices should still apply: %+v", p)
	}
	if builtinPrices["claude-opus-5-5"].Input != 4 {
		t.Error("the override changed the built-in table")
	}
	for _, bad := range []string{`[cost]` + "\n" + `show = "euros"`, `[cost.prices]` + "\n" + `"x" = { input = 1 }`, `[cost.prices]` + "\n" + `"x" = { input = -1, output = 1 }`} {
		if _, err := parseCostConfig(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestFormatCost(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		est  bool
		want string
	}{
		{0.04, false, "$0.04"},
		{0.001, false, "<$0.01"},
		{1.844, false, "$1.84"},
		{9.999, false, "$10"},
		{12.4, false, "$12"},
		{1840, false, "$1.8k"},
		{1.84, true, "~$1.84"},
	} {
		if got := formatUSD(tc.v, tc.est); got != tc.want {
			t.Errorf("formatUSD(%v, %v) = %q, want %q", tc.v, tc.est, got, tc.want)
		}
	}
	if got := formatUSDLong(12.4, false); got != "$12.40" {
		t.Errorf("formatUSDLong = %q", got)
	}
	if got := formatUSDLong(3.1, true); got != "~$3.10" {
		t.Errorf("formatUSDLong estimate = %q", got)
	}
	c := &sessionCost{Convs: map[string]*convCost{"a": {USD: 1.5, EstUSD: 1.4, Tokens: 1_200_000}}}
	for show, want := range map[string]string{costDollars: "$1.50", costTokens: "1.2M tok", costOff: ""} {
		if got := costLabel(c, show); got != want {
			t.Errorf("costLabel(%s) = %q, want %q", show, got, want)
		}
	}
	if got := costLabel(nil, costDollars); got != "" {
		t.Errorf("no cost = %q", got)
	}
}

// Claude's number wins unless it's well below the estimate (it restarted).
func TestConvCostValue(t *testing.T) {
	for _, tc := range []struct {
		c    convCost
		want float64
		est  bool
	}{
		{convCost{USD: 2, EstUSD: 1.9}, 2, false},
		{convCost{USD: 1.85, EstUSD: 2}, 1.85, false},
		{convCost{USD: 0.2, EstUSD: 2}, 2, true},
		{convCost{EstUSD: 2}, 2, true},
		{convCost{USD: 3}, 3, false},
	} {
		got, est := tc.c.value()
		if got != tc.want || est != tc.est {
			t.Errorf("%+v: %v %v, want %v %v", tc.c, got, est, tc.want, tc.est)
		}
	}
}

func TestSessionCostToday(t *testing.T) {
	day1 := time.Date(2026, 11, 20, 18, 0, 0, 0, time.Local)
	day2 := day1.Add(10 * time.Hour) // 04:00 the next day
	c := &sessionCost{}
	// First seen on day 1: $1 of the conversation was from before.
	c.update(day1, "a", func(cc *convCost) { cc.EstUSD, cc.Tokens = 3, 300 }, &usage{usd: 1, tokens: 100})
	if u, n := c.today(day1); !near(u, 2) || n != 200 {
		t.Errorf("day 1: %v, %d; want 2, 200", u, n)
	}
	c.update(day1, "a", func(cc *convCost) { cc.USD = 4 }, nil)
	if u, _ := c.today(day1); !near(u, 3) {
		t.Errorf("day 1 after Claude's number: %v, want 3", u)
	}
	// Nothing yet on day 2.
	if u, n := c.today(day2); u != 0 || n != 0 {
		t.Errorf("day 2 before any change: %v, %d", u, n)
	}
	c.update(day2, "b", func(cc *convCost) { cc.USD = 0.5 }, nil)
	if u, _ := c.today(day2); !near(u, 0.5) {
		t.Errorf("day 2: %v, want 0.5", u)
	}
	if u, _, est := c.total(); !near(u, 4.5) || est {
		t.Errorf("total %v est %v", u, est)
	}
	// It survives sessions.json.
	data, _ := json.Marshal(c)
	var back sessionCost
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if u, _ := back.today(day2); !near(u, 0.5) {
		t.Errorf("after a round trip: %v (%s)", u, data)
	}
}

func TestInfoLineCost(t *testing.T) {
	s := &Session{Name: "api", Agent: "claude", Dir: `C:\src\api`}
	sn := snapshot{
		info: sessionInfo{git: gitInfo{ok: true, branch: "main", dirty: true}, model: "claude-opus-5-5", ctxTokens: 142000},
		cost: "$1.84",
	}
	for _, tc := range []struct {
		w    int
		want string
	}{
		{40, "main* · opus 5.5 · 142k · $1.84"},
		{26, "main* · 142k · $1.84"}, // the model goes first
		{16, "main* · 142k"},         // then the cost
		{8, "main*"},                 // then the context
	} {
		if got := ansi.Strip(infoLine(s, sn, tc.w)); got != tc.want {
			t.Errorf("width %d: %q, want %q", tc.w, got, tc.want)
		}
	}
	// Outside git: just the details, dropped the same way.
	sn.info.git = gitInfo{}
	if got := ansi.Strip(infoLine(s, sn, 14)); got != "142k · $1.84" {
		t.Errorf("outside git: %q", got)
	}
	// A long branch keeps 8 cells before the details are dropped.
	sn.info.git = gitInfo{ok: true, branch: "feature/login-redirect"}
	if got := ansi.Strip(infoLine(s, sn, 26)); got != "feature/lo… · 142k · $1.84" {
		t.Errorf("long branch: %q", got)
	}
}

func TestHeaderToday(t *testing.T) {
	m := testModel("api", "docs")
	now := time.Now()
	m.sessions[0].Cost = &sessionCost{Day: localDay(now), DayUSD: 1, Convs: map[string]*convCost{"a": {USD: 13.4}}}
	m.sessions[1].Cost = &sessionCost{Day: "2001-01-01", Convs: map[string]*convCost{"b": {USD: 99}}} // nothing today
	head := ansi.Strip(m.renderSidebar(32, 10)[0])
	if !strings.HasPrefix(head, " deckhand") || !strings.HasSuffix(head, "$12.40 today ") {
		t.Errorf("header %q", head)
	}
	// A session that needs you: both when they fit, else "need you".
	m.sessions[1].status = StatusWaiting
	if head := ansi.Strip(m.renderSidebar(40, 10)[0]); !strings.Contains(head, "$12.40 today  1 need you") {
		t.Errorf("wide header %q", head)
	}
	if head := ansi.Strip(m.renderSidebar(32, 10)[0]); head != " deckhand    $12.40  1 need you " {
		t.Errorf("header without room for \"today\": %q", head)
	}
	if head := ansi.Strip(m.renderSidebar(24, 10)[0]); strings.Contains(head, "$") || !strings.Contains(head, "1 need you") {
		t.Errorf("narrow header %q", head)
	}
	defer setCostSettings(costConfigNow())
	setCostSettings(costSettings{show: costOff, prices: builtinPrices})
	if head := ansi.Strip(m.renderSidebar(40, 10)[0]); strings.Contains(head, "today") {
		t.Errorf("cost off: %q", head)
	}
}

func TestUserStatusLine(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	proj := t.TempDir()
	write := func(path, text string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := userStatusLine([]string{proj}); ok {
		t.Fatal("found a status line with no settings")
	}
	write(filepath.Join(cfg, "settings.json"), `{"statusLine":{"type":"command","command":"user-line","padding":2,"refreshInterval":5}}`)
	if sl, ok := userStatusLine([]string{proj}); !ok || sl.Command != "user-line" {
		t.Errorf("user settings: %+v %v", sl, ok)
	}
	write(filepath.Join(proj, ".claude", "settings.json"), `{"statusLine":{"type":"command","command":"project-line"}}`)
	if sl, _ := userStatusLine([]string{proj}); sl.Command != "project-line" {
		t.Errorf("project settings should win: %+v", sl)
	}
	write(filepath.Join(proj, ".claude", "settings.local.json"), `{"statusLine":{"type":"command","command":"C:/x/deckhand.exe statusline --session abc"}}`)
	if sl, _ := userStatusLine([]string{proj}); sl.Command != "project-line" {
		t.Errorf("deckhand's own status line must be skipped: %+v", sl)
	}
	write(filepath.Join(proj, ".claude", "settings.local.json"), `{"statusLine":{"type":"command","command":"local-line"}}`)
	if sl, _ := userStatusLine([]string{proj}); sl.Command != "local-line" {
		t.Errorf("local settings should win: %+v", sl)
	}

	// The settings file: deckhand's command, with the user's padding and
	// refresh interval; none with statusline = false.
	t.Setenv("DECKHAND_HOME", t.TempDir())
	path, err := writeClaudeSettings("s1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		StatusLine *statusLineSetting `json:"statusLine"`
	}
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &got)
	if got.StatusLine == nil || !strings.HasSuffix(got.StatusLine.Command, " statusline --session s1") || got.StatusLine.Type != "command" ||
		got.StatusLine.Padding == nil || *got.StatusLine.Padding != 2 || got.StatusLine.RefreshInterval == nil || *got.StatusLine.RefreshInterval != 5 {
		t.Errorf("statusLine in settings: %s", data)
	}
	defer setCostSettings(costConfigNow())
	setCostSettings(costSettings{show: costDollars, statusLine: false, prices: builtinPrices})
	path, _ = writeClaudeSettings("s1", "")
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "statusLine") {
		t.Errorf("statusline = false still adds it: %s", data)
	}
}

func TestStatusRecord(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	var p statusPayload
	payload := `{"session_id":"c1","model":{"id":"claude-opus-5-5"},"cost":{"total_cost_usd":1.23},
		"context_window":{"total_input_tokens":15500,"context_window_size":200000,"used_percentage":8,
		"current_usage":{"input_tokens":8500,"cache_creation_input_tokens":5000,"cache_read_input_tokens":2000}}}`
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if err := writeStatusRecord("s1", p.record(7)); err != nil {
		t.Fatal(err)
	}
	r, err := readStatusRecord("s1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Seq != 7 || r.ClaudeSessionID != "c1" || r.CostUSD == nil || *r.CostUSD != 1.23 || r.CtxWindow != 200000 || r.CtxTokens != 15500 || r.CtxUsedPct == nil || *r.CtxUsedPct != 8 {
		t.Errorf("record %+v", r)
	}
	// Early in a session: no cost or context yet.
	p = statusPayload{}
	_ = json.Unmarshal([]byte(`{"session_id":"c1","context_window":{"used_percentage":null,"current_usage":null}}`), &p)
	if r := p.record(8); r.CostUSD != nil || r.CtxUsedPct != nil {
		t.Errorf("empty record %+v", r)
	}
}

// The user's command gets the JSON and its output is passed through.
func TestRunUserStatusLine(t *testing.T) {
	if exe, _ := statusLineShell("x"); exe == "" {
		t.Skip("no shell")
	}
	var out, errOut bytes.Buffer
	if code := runUserStatusLine("echo from-user", []byte(`{}`), &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != "from-user" {
		t.Errorf("exit %d, out %q, err %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := runUserStatusLine("exit 3", nil, &out, &errOut); code != 3 {
		t.Errorf("exit code %d, want 3", code)
	}
}

// Saving from the config screen keeps [cost.prices] and statusline.
func TestEncodeConfigKeepsCostTables(t *testing.T) {
	text := `[cost]
statusline = false
[cost.prices]
"my-model" = { input = 1, output = 2 }
`
	cfg, raw, err := decodeConfig(text)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cost.Show = costTokens
	out, err := encodeConfig(cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseCostConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	if c.show != costTokens || c.statusLine {
		t.Errorf("show %q statusline %v in\n%s", c.show, c.statusLine, out)
	}
	if _, ok := priceFor(c.prices, "my-model"); !ok {
		t.Errorf("prices lost:\n%s", out)
	}
	// The default isn't written.
	cfg, raw, _ = decodeConfig("")
	cfg.Cost.Show = costDollars
	if out, _ := encodeConfig(cfg, raw); strings.Contains(out, "cost") {
		t.Errorf("default written:\n%s", out)
	}
}

func TestConfigScreenCost(t *testing.T) {
	keepGlobals(t)
	defer setCostSettings(costConfigNow())
	t.Setenv("DECKHAND_HOME", t.TempDir())
	p := tempConfig(t, "[cost.prices]\n\"my-model\" = { input = 1, output = 2 }\n")
	m := testModel("api")
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	m.cfg.focusRow(m.cfg.rowOf(rowListCost))
	w, _ := m.termSize()
	screen := ansi.Strip(strings.Join(m.renderConfig(w, m.height-1), "\n"))
	if !strings.Contains(screen, "Cost") || !strings.Contains(screen, "dollars") || !strings.Contains(screen, "tokens") {
		t.Errorf("config screen lacks the cost row:\n%s", screen)
	}
	m.handleKey(key("right")) // tokens
	m.handleKey(key("ctrl+s"))
	if m.cfg.statusErr {
		t.Fatalf("save: %q", m.cfg.status)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `show = "tokens"`) || !strings.Contains(string(data), "my-model") {
		t.Errorf("config.toml:\n%s", data)
	}
	if costConfigNow().show != costTokens {
		t.Errorf("not applied: %+v", costConfigNow().show)
	}
	m.handleKey(key("right")) // off
	if m.cfg.settings().Cost.Show != costOff {
		t.Errorf("show = %q, want off", m.cfg.settings().Cost.Show)
	}
}
