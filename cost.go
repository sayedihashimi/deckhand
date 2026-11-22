package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Cost tracking: what each Claude session has cost so far, and today's
// total for all sessions. There are two sources:
//
//   - Claude's own number, cost.total_cost_usd from its status line JSON.
//     deckhand's statusLine recorder (statusline.go) saves it to the
//     session's folder.
//   - An estimate from the transcript: each main-chain reply's usage,
//     priced by model from builtinPrices (which [cost.prices] can
//     override). It's shown with "~".
//
// Claude's number is used when there is one. Tokens are always counted
// from the transcript.

// Cost display modes ([cost] show).
const (
	costDollars = "dollars"
	costTokens  = "tokens"
	costOff     = "off"
)

// modelPrice is what a model costs in US dollars per million tokens.
type modelPrice struct {
	Input, Output, CacheWrite, CacheWrite1h, CacheRead float64
}

// price builds a modelPrice from the input, output and cache-read prices,
// with the usual cache-write multipliers: 1.25x input for the 5-minute
// cache and 2x for the 1-hour cache.
func price(in, out, read float64) modelPrice {
	return modelPrice{Input: in, Output: out, CacheWrite: in * 1.25, CacheWrite1h: in * 2, CacheRead: read}
}

// builtinPrices are Anthropic's list prices (November 2026), by model ID
// without a date suffix. A model ID matches the longest entry it starts
// with, so "claude-opus-4-1-20250805" uses "claude-opus-4-1". They go out
// of date: [cost.prices] in config.toml overrides or adds to them.
var builtinPrices = map[string]modelPrice{
	"claude-fable-5-2":  price(8, 40, 0.20),
	"claude-mythos-5-2": price(8, 40, 0.20),
	"claude-fable-5-1":  price(10, 50, 0.25),
	"claude-mythos-5-1": price(10, 50, 0.25),
	"claude-fable-5":    price(10, 50, 1),
	"claude-mythos-5":   price(10, 50, 1),
	"claude-opus-5-5":   price(4, 20, 0.20),
	"claude-opus-5":     price(5, 25, 0.50),
	"claude-opus-4-8":   price(5, 25, 0.50),
	"claude-opus-4-7":   price(5, 25, 0.50),
	"claude-opus-4-6":   price(5, 25, 0.50),
	"claude-opus-4-5":   price(5, 25, 0.50),
	"claude-opus-4-1":   price(15, 75, 1.50),
	"claude-opus-4":     price(15, 75, 1.50),
	"claude-sonnet-5-5": price(2, 10, 0.10),
	"claude-sonnet-5":   price(2, 10, 0.20),
	"claude-sonnet-4-6": price(3, 15, 0.30),
	"claude-sonnet-4-5": price(3, 15, 0.30),
	"claude-sonnet-4":   price(3, 15, 0.30),
	"claude-3-7-sonnet": price(3, 15, 0.30),
	"claude-haiku-5-5":  price(0.10, 0.50, 0.01),
	"claude-haiku-4-5":  price(1, 5, 0.10),
	"claude-3-5-haiku":  price(0.80, 4, 0.08),
}

// costSettings come from [cost] in the config file.
type costSettings struct {
	show       string                // costDollars, costTokens or costOff
	statusLine bool                  // add deckhand's statusLine recorder to Claude sessions
	prices     map[string]modelPrice // [cost.prices], merged over builtinPrices
}

func defaultCostSettings() costSettings {
	return costSettings{show: costDollars, statusLine: true, prices: builtinPrices}
}

var (
	costMu   sync.RWMutex
	costOpts = defaultCostSettings()
)

// costConfigNow is the current [cost] settings. Transcripts are priced in
// the background, so they're read under a lock.
func costConfigNow() costSettings {
	costMu.RLock()
	defer costMu.RUnlock()
	return costOpts
}

func setCostSettings(c costSettings) {
	costMu.Lock()
	costOpts = c
	costMu.Unlock()
}

type costConfig struct {
	Show       string                 `toml:"show,omitempty"`
	StatusLine *bool                  `toml:"statusline,omitempty"`
	Prices     map[string]priceConfig `toml:"prices,omitempty"`
}

// priceConfig is one [cost.prices] entry, in dollars per million tokens.
// The cache prices default to 1.25x (cache_write), 2x (cache_write_1h)
// and 0.1x (cache_read) the input price.
type priceConfig struct {
	Input        *float64 `toml:"input"`
	Output       *float64 `toml:"output"`
	CacheWrite   *float64 `toml:"cache_write"`
	CacheWrite1h *float64 `toml:"cache_write_1h"`
	CacheRead    *float64 `toml:"cache_read"`
}

func loadCostConfig() (costSettings, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultCostSettings(), nil
	}
	if err != nil {
		return defaultCostSettings(), err
	}
	return parseCostConfig(string(data))
}

func parseCostConfig(text string) (costSettings, error) {
	out := defaultCostSettings()
	var cfg struct {
		Cost costConfig `toml:"cost"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return out, fmt.Errorf("%s: %w", configPath(), err)
	}
	show, err := checkCostShow(cfg.Cost.Show)
	if err != nil {
		return out, fmt.Errorf("%s: cost.show %w", configPath(), err)
	}
	if show != "" {
		out.show = show
	}
	if cfg.Cost.StatusLine != nil {
		out.statusLine = *cfg.Cost.StatusLine
	}
	if len(cfg.Cost.Prices) > 0 {
		prices := make(map[string]modelPrice, len(builtinPrices)+len(cfg.Cost.Prices))
		for k, v := range builtinPrices {
			prices[k] = v
		}
		for model, p := range cfg.Cost.Prices {
			mp, err := p.toPrice()
			if err != nil {
				return defaultCostSettings(), fmt.Errorf("%s: cost.prices.%q %w", configPath(), model, err)
			}
			prices[strings.ToLower(strings.TrimSpace(model))] = mp
		}
		out.prices = prices
	}
	return out, nil
}

// checkCostShow normalizes a [cost] show value; empty means the default.
func checkCostShow(s string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "", costDollars, costTokens, costOff:
		return v, nil
	}
	return "", fmt.Errorf(`must be "dollars", "tokens" or "off", not %q`, s)
}

func (p priceConfig) toPrice() (modelPrice, error) {
	if p.Input == nil || p.Output == nil {
		return modelPrice{}, errors.New(`needs "input" and "output" (dollars per million tokens)`)
	}
	mp := price(*p.Input, *p.Output, *p.Input*0.1)
	for _, f := range []struct {
		v   *float64
		dst *float64
	}{{p.CacheWrite, &mp.CacheWrite}, {p.CacheWrite1h, &mp.CacheWrite1h}, {p.CacheRead, &mp.CacheRead}} {
		if f.v != nil {
			*f.dst = *f.v
		}
	}
	for _, v := range []float64{mp.Input, mp.Output, mp.CacheWrite, mp.CacheWrite1h, mp.CacheRead} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return modelPrice{}, errors.New("prices can't be negative")
		}
	}
	return mp, nil
}

// priceFor finds a model's price: the longest entry the model ID (without
// a date or "[1m]" suffix) is or starts with, followed by "-".
func priceFor(prices map[string]modelPrice, model string) (modelPrice, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimSuffix(m, "[1m]")
	m = modelDate.ReplaceAllString(m, "")
	keys := make([]string, 0, len(prices))
	for k := range prices {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if m == k || strings.HasPrefix(m, k+"-") {
			return prices[k], true
		}
	}
	return modelPrice{}, false
}

// Persisted cost ----------------------------------------------------------------

// sessionCost is a session's cost, saved in sessions.json so an exited
// session keeps showing it after a restart. Each conversation (Claude
// session ID) has its own numbers; a session's cost is their sum.
type sessionCost struct {
	Convs map[string]*convCost `json:"convs,omitempty"`
	// Day is the local date the day's spend counts from, and DayUSD and
	// DayTokens the session's totals when it started: today's spend is
	// the total minus them.
	Day       string  `json:"day,omitempty"`
	DayUSD    float64 `json:"day_usd,omitempty"`
	DayTokens int     `json:"day_tokens,omitempty"`
}

type convCost struct {
	USD    float64 `json:"usd,omitempty"`     // Claude's own number, from the status line
	EstUSD float64 `json:"est_usd,omitempty"` // estimated from the transcript
	Tokens int     `json:"tokens,omitempty"`  // from the transcript, without cache reads
}

// value is the conversation's cost: Claude's number when there is one,
// else the estimate. Claude's number starts again from 0 when a session is
// resumed in some cases; while it's well below the estimate, the estimate
// is used.
func (c *convCost) value() (usd float64, estimate bool) {
	if c.USD > 0 && c.USD >= 0.9*c.EstUSD {
		return c.USD, false
	}
	if c.EstUSD > 0 {
		return c.EstUSD, true
	}
	return c.USD, false
}

// total is the session's cost so far over all its conversations.
func (c *sessionCost) total() (usd float64, tokens int, estimate bool) {
	if c == nil {
		return 0, 0, false
	}
	for _, cc := range c.Convs {
		v, est := cc.value()
		usd += v
		tokens += cc.Tokens
		if est && v > 0 {
			estimate = true
		}
	}
	return usd, tokens, estimate
}

// localDay is the local date of t, for the day's total.
func localDay(t time.Time) string { return t.Local().Format("2006-01-02") }

// today is what the session spent today (since local midnight).
func (c *sessionCost) today(now time.Time) (usd float64, tokens int) {
	if c == nil || c.Day != localDay(now) {
		return 0, 0
	}
	u, n, _ := c.total()
	return max(u-c.DayUSD, 0), max(n-c.DayTokens, 0)
}

// update changes one conversation's numbers. On the first change of a day,
// the totals before it become the day's starting point. before is what a
// conversation seen for the first time had spent before today (from the
// transcript's timestamps), so an old conversation resumed today doesn't
// count as today's spend.
func (c *sessionCost) update(now time.Time, conv string, change func(*convCost), before *usage) {
	if c.Convs == nil {
		c.Convs = map[string]*convCost{}
	}
	if day := localDay(now); c.Day != day {
		c.DayUSD, c.DayTokens, _ = c.total()
		c.Day = day
	}
	cc := c.Convs[conv]
	if cc == nil {
		cc = &convCost{}
		c.Convs[conv] = cc
		if before != nil {
			c.DayUSD += before.usd
			c.DayTokens += before.tokens
		}
	}
	change(cc)
}

// Transcript usage ----------------------------------------------------------------

// usage is the priced sum of some transcript entries.
type usage struct {
	usd    float64
	tokens int // input, cache writes and output; cache reads aren't counted
}

func (u *usage) add(v usage) { u.usd += v.usd; u.tokens += v.tokens }

// costEntry is the part of a transcript line that cost tracking reads.
type costEntry struct {
	Type        string    `json:"type"`
	IsSidechain bool      `json:"isSidechain"`
	RequestID   string    `json:"requestId"`
	Timestamp   time.Time `json:"timestamp"`
	Message     struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// costEntryUsage prices one entry. ok is false for entries that don't
// count: not an assistant reply, a sub-agent's (sidechain), or no usage.
// A model with no price still counts its tokens.
func costEntryUsage(e costEntry, prices map[string]modelPrice) (u usage, ok bool) {
	if e.Type != "assistant" || e.IsSidechain || e.Message.Usage == nil {
		return usage{}, false
	}
	us := e.Message.Usage
	u.tokens = us.InputTokens + us.CacheCreationInputTokens + us.OutputTokens
	p, known := priceFor(prices, e.Message.Model)
	if !known {
		return u, true
	}
	w5, w1 := us.CacheCreationInputTokens, 0
	if cc := us.CacheCreation; cc != nil && cc.Ephemeral5m+cc.Ephemeral1h == us.CacheCreationInputTokens {
		w5, w1 = cc.Ephemeral5m, cc.Ephemeral1h
	}
	u.usd = (float64(us.InputTokens)*p.Input +
		float64(w5)*p.CacheWrite +
		float64(w1)*p.CacheWrite1h +
		float64(us.CacheReadInputTokens)*p.CacheRead +
		float64(us.OutputTokens)*p.Output) / 1e6
	return u, true
}

// transcriptCost sums a transcript as it grows: each read adds the lines
// written since the last one. Claude writes one line per content block of
// a reply, each with the reply's usage, so lines are counted once per
// message ID.
type transcriptCost struct {
	path   string
	offset int64
	seen   map[string]bool
	sum    usage // every counted entry
	before usage // entries from before midnight (of the read that saw them)
}

// read adds what's new in the transcript at path; a different path starts
// over. It reports whether anything was added.
func (tc *transcriptCost) read(path string, prices map[string]modelPrice, now time.Time) (bool, error) {
	if path != tc.path {
		*tc = transcriptCost{path: path}
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	if fi.Size() < tc.offset { // rewritten: start over
		*tc = transcriptCost{path: path}
	}
	if fi.Size() == tc.offset {
		return false, nil
	}
	if _, err := f.Seek(tc.offset, io.SeekStart); err != nil {
		return false, err
	}
	if tc.seen == nil {
		tc.seen = map[string]bool{}
	}
	y, mo, d := now.Local().Date()
	midnight := time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
	r := bufio.NewReaderSize(f, 256<<10)
	added := false
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A line without its newline is still being written: read it
			// next time.
			break
		}
		tc.offset += int64(len(line))
		if !bytes.Contains(line, []byte(`"usage"`)) {
			continue
		}
		var e costEntry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		u, ok := costEntryUsage(e, prices)
		if !ok {
			continue
		}
		if key := e.Message.ID + "|" + e.RequestID; key != "|" {
			if tc.seen[key] {
				continue
			}
			tc.seen[key] = true
		}
		tc.sum.add(u)
		if !e.Timestamp.IsZero() && e.Timestamp.Before(midnight) {
			tc.before.add(u)
		}
		added = true
	}
	return added, nil
}

// Formatting --------------------------------------------------------------------

// formatUSD is a session's cost, short: "$0.04", "$1.84", "$12", "$1.2k".
// Estimates start with "~".
func formatUSD(v float64, estimate bool) string {
	s := ""
	switch {
	case v <= 0:
		s = "$0"
	case v < 0.005:
		s = "<$0.01"
	case v < 9.995:
		s = fmt.Sprintf("$%.2f", v)
	case v < 999.5:
		s = fmt.Sprintf("$%.0f", v)
	default:
		s = fmt.Sprintf("$%.1fk", v/1000)
	}
	if estimate {
		s = "~" + s
	}
	return s
}

// formatUSDLong is a total, with cents: "$12.40".
func formatUSDLong(v float64, estimate bool) string {
	s := fmt.Sprintf("$%.2f", max(v, 0))
	if v >= 10000 {
		s = fmt.Sprintf("$%.0f", v)
	}
	if estimate {
		s = "~" + s
	}
	return s
}

// formatTokens is a token count for the cost setting "tokens": "1.2M tok".
func formatTokens(n int) string { return shortTokens(n) + " tok" }

// costLabel is the session's cost as the setting shows it, or "".
func costLabel(c *sessionCost, show string) string {
	usd, tokens, est := c.total()
	switch show {
	case costOff:
		return ""
	case costTokens:
		if tokens == 0 {
			return ""
		}
		return formatTokens(tokens)
	}
	if usd <= 0 {
		return ""
	}
	return formatUSD(usd, est)
}

// todayLabel is all sessions' spend today, for the list header: "$12.40
// today", "~$3.10 today" (when any of it is estimated) or "1.2M tok today".
func todayLabel(sessions []*Session, show string, now time.Time) string {
	if show == costOff {
		return ""
	}
	var usd float64
	var tokens int
	est := false
	for _, s := range sessions {
		s.mu.Lock()
		u, n := s.Cost.today(now)
		_, _, e := s.Cost.total()
		s.mu.Unlock()
		usd += u
		tokens += n
		if e && u > 0 {
			est = true
		}
	}
	if show == costTokens {
		if tokens == 0 {
			return ""
		}
		return formatTokens(tokens) + " today"
	}
	if usd <= 0 {
		return ""
	}
	return formatUSDLong(usd, est) + " today"
}
