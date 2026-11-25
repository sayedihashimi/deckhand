package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"
)

// [header]: the session header above the terminal.

// defaultReplyWidth is the share of the header, in percent, that Claude's
// latest reply can take.
const defaultReplyWidth = 40

type headerSettings struct {
	replyWidth int // percent of the header width; 0 hides the reply
}

func defaultHeaderSettings() headerSettings {
	return headerSettings{replyWidth: defaultReplyWidth}
}

var headerOpts = defaultHeaderSettings()

type headerConfig struct {
	ReplyWidth *int `toml:"reply_width,omitempty"`
}

// loadHeaderConfig reads [header] from the config file.
func loadHeaderConfig() (headerSettings, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultHeaderSettings(), nil
	}
	if err != nil {
		return defaultHeaderSettings(), err
	}
	return parseHeaderConfig(string(data))
}

func parseHeaderConfig(text string) (headerSettings, error) {
	out := defaultHeaderSettings()
	var cfg struct {
		Header headerConfig `toml:"header"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return out, fmt.Errorf("%s: %w", configPath(), err)
	}
	if p := cfg.Header.ReplyWidth; p != nil {
		if *p < 0 || *p > 100 {
			return out, fmt.Errorf("%s: header.reply_width must be a percentage from 0 to 100, not %d", configPath(), *p)
		}
		out.replyWidth = *p
	}
	return out, nil
}

// headerReply is the reply part of the session header, "  · " and the
// reply's lines joined, cut with "…" to pct percent of the header's w
// cells. It's empty when there's no reply or no room for it.
func headerReply(reply []string, w, pct int) string {
	if len(reply) == 0 || pct <= 0 || w <= 0 {
		return ""
	}
	const lead = "  · "
	room := w*pct/100 - ansi.StringWidth(lead)
	if room < 2 {
		return ""
	}
	text := ""
	for i, l := range reply {
		if i > 0 {
			text += " "
		}
		text += l
	}
	return lead + ansi.Truncate(text, room, "…")
}
