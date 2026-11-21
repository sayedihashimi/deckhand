package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
)

// How full a Claude session's context window is, on its third line: a
// meter and a percentage ("▰▰▰▰▱ 71%"), the percentage alone, or tokens
// ("142k/200k"), coloured by how full it is.
//
// The window size comes from, in order:
//  1. Claude's status line JSON (context_window.context_window_size),
//     which the statusLine recorder saves (statusline.go). It's exact.
//  2. [info.context_windows] in config.toml, for that model.
//  3. The model ID: a "[1m]" suffix or a model whose window is 1M by
//     default means 1,000,000.
//  4. [info] context_window, then 200,000 for other Claude models.
//
// Without one (another agent's model), the plain token count is shown.

// Context display modes ([info] context).
const (
	ctxShowTokens  = "tokens"
	ctxShowPercent = "percent"
	ctxShowMeter   = "meter"
)

const (
	defaultContextWindow = 200_000
	oneMillion           = 1_000_000
)

// oneMillionModels are model families whose context window is 1M tokens by
// default. A model ID matches when it is one or starts with one and "-".
var oneMillionModels = []string{"claude-fable-5", "claude-mythos-5", "claude-opus-5", "claude-sonnet-5", "claude-haiku-5"}

// infoSettings come from [info] in the config file.
type infoSettings struct {
	context string         // ctxShowMeter (the default), ctxShowPercent or ctxShowTokens
	window  int            // [info] context_window; 0 means the default
	windows map[string]int // [info.context_windows], by model ID (lower case)
}

func defaultInfoSettings() infoSettings { return infoSettings{context: ctxShowMeter} }

var infoOpts = defaultInfoSettings()

type infoConfig struct {
	Context        string         `toml:"context,omitempty"`
	ContextWindow  int            `toml:"context_window,omitempty"`
	ContextWindows map[string]int `toml:"context_windows,omitempty"`
}

func loadInfoConfig() (infoSettings, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultInfoSettings(), nil
	}
	if err != nil {
		return defaultInfoSettings(), err
	}
	return parseInfoConfig(string(data))
}

func parseInfoConfig(text string) (infoSettings, error) {
	out := defaultInfoSettings()
	var cfg struct {
		Info infoConfig `toml:"info"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return out, fmt.Errorf("%s: %w", configPath(), err)
	}
	show, err := checkContextShow(cfg.Info.Context)
	if err != nil {
		return out, fmt.Errorf("%s: info.context %w", configPath(), err)
	}
	if show != "" {
		out.context = show
	}
	if cfg.Info.ContextWindow < 0 {
		return defaultInfoSettings(), fmt.Errorf("%s: info.context_window must be a number of tokens", configPath())
	}
	out.window = cfg.Info.ContextWindow
	for model, n := range cfg.Info.ContextWindows {
		if n <= 0 {
			return defaultInfoSettings(), fmt.Errorf("%s: info.context_windows.%q must be a number of tokens", configPath(), model)
		}
		if out.windows == nil {
			out.windows = map[string]int{}
		}
		out.windows[strings.ToLower(strings.TrimSpace(model))] = n
	}
	return out, nil
}

// checkContextShow normalizes an [info] context value; empty means the
// default.
func checkContextShow(s string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "", ctxShowTokens, ctxShowPercent, ctxShowMeter:
		return v, nil
	}
	return "", fmt.Errorf(`must be "meter", "percent" or "tokens", not %q`, s)
}

// modelMatches reports whether a model ID is key, or key followed by "-"
// (and a version or date).
func modelMatches(model, key string) bool {
	return model == key || strings.HasPrefix(model, key+"-")
}

// contextWindow is the context window size for a session, or 0 when it
// isn't known. statusModel and statusWindow come from the latest status
// line record.
func contextWindow(o infoSettings, model, statusModel string, statusWindow int) int {
	m := strings.ToLower(strings.TrimSpace(model))
	if statusWindow > 0 && (statusModel == "" || m == "" || strings.EqualFold(strings.TrimSuffix(statusModel, "[1m]"), model)) {
		return statusWindow
	}
	if m == "" {
		m = strings.ToLower(statusModel)
	}
	if m == "" {
		return 0
	}
	oneM := strings.HasSuffix(m, "[1m]")
	m = modelDate.ReplaceAllString(strings.TrimSuffix(m, "[1m]"), "")
	// [info.context_windows]: the longest entry that matches.
	keys := make([]string, 0, len(o.windows))
	for k := range o.windows {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if modelMatches(m, strings.TrimSuffix(k, "[1m]")) {
			return o.windows[k]
		}
	}
	if oneM {
		return oneMillion
	}
	for _, k := range oneMillionModels {
		if modelMatches(m, k) {
			return oneMillion
		}
	}
	if o.window > 0 {
		return o.window
	}
	if strings.HasPrefix(m, "claude-") {
		return defaultContextWindow
	}
	return 0
}

// contextPct is how full the window is, in percent (0 when unknown).
func contextPct(tokens, window int) float64 {
	if window <= 0 || tokens <= 0 {
		return 0
	}
	return float64(tokens) * 100 / float64(window)
}

// contextStyle colours the context by how full it is: muted under 60%,
// yellow to 80%, then orange.
func contextStyle(pct float64) lipgloss.Style {
	switch {
	case pct > 80:
		return lipgloss.NewStyle().Foreground(colWaiting)
	case pct >= 60:
		return lipgloss.NewStyle().Foreground(colWorking)
	}
	return stMuted
}

// meterCells is how many cells the meter has.
const meterCells = 5

// contextMeter is a bar of meterCells cells: "▰▰▰▰▱".
func contextMeter(pct float64) string {
	n := int(math.Round(pct * meterCells / 100))
	n = min(max(n, 0), meterCells)
	if pct > 0 && n == 0 {
		n = 1
	}
	return strings.Repeat("▰", n) + strings.Repeat("▱", meterCells-n)
}

// contextVariants are the ways to show the context, longest first, for the
// info line to pick from as room runs out: the meter goes first, then the
// window size. Without a window size it's the plain token count.
func contextVariants(mode string, tokens, window int) []string {
	if tokens <= 0 {
		return nil
	}
	short := shortTokens(tokens)
	if window <= 0 {
		return []string{short}
	}
	pct := fmt.Sprintf("%.0f%%", contextPct(tokens, window))
	switch mode {
	case ctxShowTokens:
		return []string{short + "/" + shortTokens(window), short}
	case ctxShowPercent:
		return []string{pct}
	}
	return []string{contextMeter(contextPct(tokens, window)) + " " + pct, pct}
}
