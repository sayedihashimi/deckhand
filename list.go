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

// Going back to the list from inside a session: F5 by default, shown in
// footers and hints. ctrl+q and ctrl+] do the same and can't be changed;
// they're for keyboards where F keys need Fn, terminals that take F keys,
// and muscle memory. Only the help shows them.
//
// Why F5: by the same check as F3, Claude Code binds no function keys,
// Codex uses F2 and Gemini CLI uses F4, F6-F9 and F12. Windows Terminal
// binds F11 (fullscreen) and WezTerm binds none. That left F5 and F10; F5
// is next to F1 and F3, and F10 means "menu" in other apps.
const defaultBackKey = "f5"

// listSettings come from [list] in the config file.
type listSettings struct {
	toggleKey  string // as Bubble Tea names it, e.g. "f3" or "ctrl+\\"
	backKey    string // back to the list from a session; empty means F5
	statusLine bool   // show the status line while the list is hidden
	// followTitle: a Claude session's name follows Claude's title (title.go).
	followTitle bool
}

var listOpts = defaultListSettings()

func defaultListSettings() listSettings {
	return listSettings{toggleKey: defaultToggleKey, backKey: defaultBackKey, statusLine: true, followTitle: true}
}

// back is the key that goes back to the list ([list] back_key).
func (o listSettings) back() string {
	if o.backKey == "" {
		return defaultBackKey
	}
	return o.backKey
}

type listConfig struct {
	ToggleKey  string `toml:"toggle_key,omitempty"`
	BackKey    string `toml:"back_key,omitempty"`
	StatusLine *bool  `toml:"status_line,omitempty"`
	// FollowTitle: Claude sessions take their name from Claude's title.
	FollowTitle *bool `toml:"follow_claude_title,omitempty"`
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

// reservedListKeys can't be the toggle key or the back key: deckhand or the
// agents need them. Each also can't be the other one (checkListKeys).
var reservedListKeys = map[string]bool{
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
	if cfg.List.FollowTitle != nil {
		out.followTitle = *cfg.List.FollowTitle
	}
	key, err := checkToggleKey(cfg.List.ToggleKey)
	if err != nil {
		return out, fmt.Errorf("%s: list.toggle_key %w", configPath(), err)
	}
	back, err := checkBackKey(cfg.List.BackKey)
	if err != nil {
		return out, fmt.Errorf("%s: list.back_key %w", configPath(), err)
	}
	if err := checkListKeys(key, back); err != nil {
		return out, fmt.Errorf("%s: list.toggle_key and back_key %w", configPath(), err)
	}
	if key != "" {
		out.toggleKey = key
	}
	if back != "" {
		out.backKey = back
	}
	return out, nil
}

// checkToggleKey normalizes a toggle_key value ("F2" becomes "f2"; empty
// stays empty, meaning the default) and refuses keys it can't be.
func checkToggleKey(s string) (string, error) {
	return checkListKey(s, `f3, f2 or ctrl+\`)
}

// checkBackKey is checkToggleKey for back_key.
func checkBackKey(s string) (string, error) {
	return checkListKey(s, "f5 or f10")
}

func checkListKey(s, try string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	if key != "" && (utf8.RuneCountInString(key) == 1 || reservedListKeys[key]) {
		// A plain character would stop you typing it into a session.
		return "", fmt.Errorf("can't be %q (it's needed for typing or by deckhand); try %s", s, try)
	}
	return key, nil
}

// checkListKeys refuses a toggle key and back key that are the same key,
// once empty values become the defaults. Both are already normalized. The
// error reads after "toggle_key and back_key".
func checkListKeys(toggle, back string) error {
	if toggle == "" {
		toggle = defaultToggleKey
	}
	if back == "" {
		back = defaultBackKey
	}
	if toggle == back {
		return fmt.Errorf("are both %s; they must be different keys", keyLabel(back))
	}
	return nil
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
	_ = updatePrefs(func(p *prefs) { p.ListHidden = hidden })
}
