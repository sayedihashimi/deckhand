package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Writing the config file, for the config screen ("c"). The TOML library
// can't keep comments, so a save rewrites the whole file: a short header,
// then the settings. The previous file is kept as config.toml.bak.

const configHeader = `# deckhand config. Written by deckhand's config screen (press c in the
# session list); the previous file is kept next to this one as
# config.toml.bak. You can still edit it by hand: see the README.

`

// reloadConfig reads the config file and applies it: agents, notifications,
// [open], [list], [history], [worktree], [cost], [info], [header] and [[paths.alias]]. Settings that fail to load fall back to their defaults;
// the first error is returned.
func reloadConfig() error {
	agents, err := loadAgents()
	allAgents = agents
	mode, notifyErr := loadNotifyMode()
	notifyMode = mode
	openCfg, openErr := loadOpenConfig()
	openSettings = openCfg
	listCfg, listErr := loadListConfig()
	listOpts = listCfg
	histCfg, histErr := loadHistoryConfig()
	setHistorySettings(histCfg)
	wtCfg, wtErr := loadWorktreeConfig()
	worktreeSettings = wtCfg
	costCfg, costErr := loadCostConfig()
	setCostSettings(costCfg)
	infoCfg, infoErr := loadInfoConfig()
	infoOpts = infoCfg
	headerCfg, headerErr := loadHeaderConfig()
	headerOpts = headerCfg
	aliases, pathsErr := loadPathsConfig()
	pathAliases = aliases
	for _, e := range []error{err, notifyErr, openErr, listErr, histErr, wtErr, costErr, infoErr, headerErr, pathsErr} {
		if e != nil {
			return e
		}
	}
	return nil
}

// configFile is the part of the config file the config screen edits.
type configFile struct {
	Agents        []agentConfig `toml:"agents"`
	Notifications notifyConfig  `toml:"notifications"`
	Open          openConfig    `toml:"open"`
	List          listConfig    `toml:"list"`
	History       historyConfig `toml:"history"`
	Cost          costConfig    `toml:"cost"`
	Info          infoConfig    `toml:"info"`
}

// decodeConfig reads the edited settings and, separately, every table in
// the file, so tables the screen doesn't know about are written back.
func decodeConfig(text string) (configFile, map[string]any, error) {
	var cfg configFile
	if _, err := toml.Decode(text, &cfg); err != nil {
		return cfg, nil, err
	}
	raw := map[string]any{}
	if _, err := toml.Decode(text, &raw); err != nil {
		return cfg, nil, err
	}
	return cfg, raw, nil
}

// encodeConfig writes the settings, plus the other tables in raw, as the
// text of a config file.
func encodeConfig(cfg configFile, raw map[string]any) (string, error) {
	out := map[string]any{}
	for k, v := range raw {
		out[k] = v
	}
	delete(out, "agents")
	delete(out, "notifications")
	delete(out, "open")
	delete(out, "list")
	delete(out, "history")
	if len(cfg.Agents) > 0 {
		out["agents"] = cfg.Agents
	}
	if cfg.Notifications.Enabled != nil || cfg.Notifications.When != "" {
		out["notifications"] = cfg.Notifications
	}
	if cfg.Open != (openConfig{}) {
		out["open"] = cfg.Open
	}
	if cfg.List.ToggleKey != "" || cfg.List.BackKey != "" || cfg.List.StatusLine != nil {
		out["list"] = cfg.List
	}
	if cfg.History.Save != nil || cfg.History.Lines != 0 {
		out["history"] = cfg.History
	}
	// [cost] and [info] keep their other keys ([cost.prices], statusline,
	// context_window, [info.context_windows]): only one key of each is
	// edited here.
	setTableKey(out, raw, "cost", "show", cfg.Cost.Show, costDollars)
	setTableKey(out, raw, "info", "context", cfg.Info.Context, ctxShowMeter)
	var b strings.Builder
	b.WriteString(configHeader)
	enc := toml.NewEncoder(&b)
	enc.Indent = ""
	if err := enc.Encode(out); err != nil {
		return "", err
	}
	return b.String(), nil
}

// checkConfigText loads a config the same way deckhand does at startup.
func checkConfigText(text string) error {
	if _, err := parseAgents(text); err != nil {
		return err
	}
	if _, err := parseNotifyMode(text); err != nil {
		return err
	}
	if _, err := parseOpenConfig(text); err != nil {
		return err
	}
	if _, err := parseListConfig(text); err != nil {
		return err
	}
	if _, err := parseHistoryConfig(text); err != nil {
		return err
	}
	if _, err := parseWorktreeConfig(text); err != nil {
		return err
	}
	if _, err := parseCostConfig(text); err != nil {
		return err
	}
	if _, err := parseInfoConfig(text); err != nil {
		return err
	}
	if _, err := parseHeaderConfig(text); err != nil {
		return err
	}
	_, err := parsePathsConfig(text)
	return err
}

// writeConfigFile replaces the file at path with text. The previous file
// is copied to path+".bak" first, and the new one is written to a temporary
// file and renamed over the old one, so a failed write leaves it alone.
// backedUp reports whether there was a previous file.
func writeConfigFile(path, text string) (backedUp bool, err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := os.WriteFile(path+".bak", old, 0o644); err != nil {
			return false, fmt.Errorf("back up the old file: %w", err)
		}
		backedUp = true
	case !errors.Is(err, os.ErrNotExist):
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return backedUp, err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return backedUp, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return backedUp, err
	}
	if err := tmp.Close(); err != nil {
		return backedUp, err
	}
	return backedUp, os.Rename(tmp.Name(), path)
}

// hasOwnComments reports whether a config file has comments that a save
// would lose: any comment that isn't a line of the sample config or of the
// header deckhand writes.
func hasOwnComments(text string) bool {
	known := map[string]bool{}
	for _, l := range strings.Split(sampleConfig+configHeader, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "#") {
			known[l] = true
		}
	}
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			if !known[l] {
				return true
			}
			continue
		}
		if inlineComment(l) {
			return true
		}
	}
	return false
}

// inlineComment reports whether a line of TOML has a "#" outside a string.
// Multi-line strings aren't handled; at worst that's a needless warning.
func inlineComment(line string) bool {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '"' && c == '\\':
			i++ // skip the escaped character
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return true
		}
	}
	return false
}

// setTableKey sets one key of a table the config screen only partly edits,
// keeping the table's other keys. The default value isn't written, unless
// the file already had the key.
func setTableKey(out, raw map[string]any, table, key, value, def string) {
	t := map[string]any{}
	old, _ := raw[table].(map[string]any)
	for k, v := range old {
		t[k] = v
	}
	delete(t, key)
	switch {
	case value != "" && value != def:
		t[key] = value
	case value == def && old[key] != nil:
		t[key] = def // it was set: keep it set
	}
	delete(out, table)
	if len(t) > 0 {
		out[table] = t
	}
}
