package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// prefs remembers recently used folders, the last agent chosen, the code
// repo used with each start folder and whether the session list is hidden.
type prefs struct {
	RecentDirs []string `json:"recent_dirs"`
	LastAgent  string   `json:"last_agent,omitempty"`
	ListHidden bool     `json:"list_hidden,omitempty"`
	// CodeRepos is the code repo last used with each start folder that
	// isn't in git, keyed by dirKey.
	CodeRepos map[string]string `json:"code_repos,omitempty"`

	extra extraFields // fields this build doesn't know, kept (store.go)
}

func dirKey(dir string) string { return strings.ToLower(filepath.Clean(dir)) }

// codeRepoFor is the code repo last used with a start folder.
func (p prefs) codeRepoFor(dir string) string { return p.CodeRepos[dirKey(dir)] }

// setCodeRepo remembers the code repo used with a start folder; "" forgets it.
func (p *prefs) setCodeRepo(dir, repo string) {
	if repo == "" {
		delete(p.CodeRepos, dirKey(dir))
		return
	}
	if p.CodeRepos == nil {
		p.CodeRepos = map[string]string{}
	}
	p.CodeRepos[dirKey(dir)] = repo
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
	tmp := prefsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, prefsPath())
}

// updatePrefs changes prefs.json: it re-reads the file, applies change and
// writes it back, holding a lock so that instances sharing the data folder
// (instance.go) don't undo each other's changes. So each change applies on
// top of the latest file: recent folders are merged (the one used moves to
// the front of everyone's), and other settings are last write wins.
func updatePrefs(change func(p *prefs)) error {
	return withFileLock(prefsPath()+".lock", func() error {
		p := loadPrefs()
		change(&p)
		return p.save()
	})
}

// prefsJSON is prefs without its methods, so the (un)marshalers below
// don't call themselves.
type prefsJSON prefs

var prefsKeys = jsonKeys(reflect.TypeOf(prefsJSON{}))

func (p *prefs) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*prefsJSON)(p)); err != nil {
		return err
	}
	p.extra = unknownFields(data, prefsKeys)
	return nil
}

func (p prefs) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(prefsJSON(p))
	if err != nil {
		return nil, err
	}
	return withExtra(data, p.extra)
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
