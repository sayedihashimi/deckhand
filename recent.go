package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// prefs remembers recently used folders, the last agent chosen and whether
// the session list is hidden.
type prefs struct {
	RecentDirs []string `json:"recent_dirs"`
	LastAgent  string   `json:"last_agent,omitempty"`
	ListHidden bool     `json:"list_hidden,omitempty"`
}

const maxRecentDirs = 20

func prefsPath() string { return filepath.Join(dataDir(), "prefs.json") }

func loadPrefs() prefs {
	var p prefs
	if data, err := os.ReadFile(prefsPath()); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	return p
}

func (p *prefs) useDir(dir string) {
	out := []string{dir}
	for _, d := range p.RecentDirs {
		if !strings.EqualFold(filepath.Clean(d), filepath.Clean(dir)) {
			out = append(out, d)
		}
	}
	if len(out) > maxRecentDirs {
		out = out[:maxRecentDirs]
	}
	p.RecentDirs = out
}

func (p prefs) save() error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(prefsPath(), data, 0o600)
}

// recentDirs merges remembered folders with those of existing sessions,
// newest first, dropping folders that no longer exist.
func recentDirs(p prefs, sessions []*Session) []string {
	var all []string
	all = append(all, p.RecentDirs...)
	for i := len(sessions) - 1; i >= 0; i-- {
		all = append(all, sessions[i].Dir)
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range all {
		key := strings.ToLower(filepath.Clean(d))
		if d == "" || seen[key] {
			continue
		}
		seen[key] = true
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out
}
