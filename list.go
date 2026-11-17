package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
)

// Hiding the session list: one key (F3 by default) hides and shows the list
// from anywhere, so a session gets the full window width. While the list is
// hidden, the session header row ends with a short status line
// ("2 need you · 1 finished · F3 list"), which can be turned off.
//
// Why F3: Claude Code binds no function keys by default, Codex opens its
// warnings with F2, and Gemini CLI uses F4, F6-F9 and F12. Neither Windows
// Terminal nor WezTerm binds F3. The first default, ctrl+\, is 1Password's
// global shortcut on Windows, so it never reached deckhand on machines
// running 1Password.

const defaultToggleKey = "f3"

// listSettings come from [list] in the config file.
type listSettings struct {
	toggleKey  string // as Bubble Tea names it, e.g. "f3" or "ctrl+\\"
	statusLine bool   // show the status line while the list is hidden
}

var listOpts = defaultListSettings()

func defaultListSettings() listSettings {
	return listSettings{toggleKey: defaultToggleKey, statusLine: true}
}

type listConfig struct {
	ToggleKey  string `toml:"toggle_key,omitempty"`
	StatusLine *bool  `toml:"status_line,omitempty"`
}

// loadListConfig reads [list] from the config file.
func loadListConfig() (listSettings, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultListSettings(), nil
	}
	if err != nil {
		return defaultListSettings(), err
	}
	return parseListConfig(string(data))
}

// reservedToggleKeys can't hide the list: deckhand or the agents need them.
var reservedToggleKeys = map[string]bool{
	"ctrl+q": true, "ctrl+]": true, "ctrl+c": true, "enter": true, "esc": true,
	"tab": true, "shift+tab": true, "space": true, "backspace": true,
	"up": true, "down": true, "left": true, "right": true,
	"f1": true, // help
}

func parseListConfig(text string) (listSettings, error) {
	out := defaultListSettings()
	var cfg struct {
		List listConfig `toml:"list"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return out, fmt.Errorf("%s: %w", configPath(), err)
	}
	if cfg.List.StatusLine != nil {
		out.statusLine = *cfg.List.StatusLine
	}
	key, err := checkToggleKey(cfg.List.ToggleKey)
	if err != nil {
		return out, fmt.Errorf("%s: list.toggle_key %w", configPath(), err)
	}
	if key != "" {
		out.toggleKey = key
	}
	return out, nil
}

// checkToggleKey normalizes a toggle_key value ("F2" becomes "f2"; empty
// stays empty, meaning the default) and refuses keys it can't be.
func checkToggleKey(s string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	if key != "" && (utf8.RuneCountInString(key) == 1 || reservedToggleKeys[key]) {
		// A plain character would stop you typing it into a session.
		return "", fmt.Errorf("can't be %q (it's needed for typing or by deckhand); try f3, f2 or ctrl+\\", s)
	}
	return key, nil
}

// keyLabel is how a key is shown in hints: "f2" becomes "F2".
func keyLabel(key string) string {
	if len(key) >= 2 && key[0] == 'f' && key[1] >= '0' && key[1] <= '9' {
		return "F" + key[1:]
	}
	return key
}

// attentionCounts counts the sessions that need you and those that finished
// a turn you haven't seen, by the same rules as the sidebar badge and "@".
func attentionCounts(sessions []*Session) (waiting, finished int) {
	for _, s := range sessions {
		sn := s.Snapshot()
		switch {
		case sn.status == StatusWaiting:
			waiting++
		case sn.unread:
			finished++
		}
	}
	return waiting, finished
}

// statusLineParts are the pieces of the status line shown while the list is
// hidden: the counts that aren't zero, then the key that shows the list.
func statusLineParts(waiting, finished int, key string) (need, done, hint string) {
	if waiting > 0 {
		need = fmt.Sprintf("%d need you", waiting)
	}
	if finished > 0 {
		done = fmt.Sprintf("%d finished", finished)
	}
	return need, done, keyLabel(key) + " list"
}

// statusLine renders the status line, ending with a space.
func (m *model) statusLine() string {
	waiting, finished := attentionCounts(m.sessions)
	need, done, hint := statusLineParts(waiting, finished, listOpts.toggleKey)
	sep := stMuted.Render(" · ")
	var parts []string
	if need != "" {
		parts = append(parts, stWaitText.Render(need))
	}
	if done != "" {
		parts = append(parts, lipgloss.NewStyle().Bold(true).Foreground(colIdle).Render(done))
	}
	parts = append(parts, stMuted.Render(hint))
	return strings.Join(parts, sep) + " "
}

// mainLeft is the column where the main pane (session header and terminal)
// starts: right of the sidebar and its border, or 0 while the list is hidden.
func (m *model) mainLeft() int {
	if m.listHidden {
		return 0
	}
	return m.sidebarWidth() + 1
}

// setListHidden hides or shows the session list, resizes every session to
// the new terminal width and remembers the choice in prefs.json.
func (m *model) setListHidden(hidden bool) {
	if m.listHidden == hidden {
		return
	}
	m.listHidden = hidden
	if m.width > 0 && m.height > 0 {
		m.resizeAll()
	}
	p := loadPrefs()
	p.ListHidden = hidden
	_ = p.save()
}
