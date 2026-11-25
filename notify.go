package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Desktop notifications: a Windows toast when a session needs you or a
// Claude session finishes its turn, while you aren't looking at deckhand.

type noticeKind int

const (
	noticeWaiting  noticeKind = iota + 1 // permission prompt, question or bell
	noticeFinished                       // Claude finished its turn
)

// notice is a status change worth telling the user about.
type notice struct {
	kind    noticeKind
	detail  string // the sidebar detail, e.g. "permission: Bash"
	summary string // what the tool wants to do, e.g. the Bash command
}

// noticeFor reports whether a status change deserves a notification.
func noticeFor(prev, next Status, detail, summary string) (notice, bool) {
	switch {
	case next == StatusWaiting && prev != StatusWaiting:
		return notice{kind: noticeWaiting, detail: detail, summary: summary}, true
	case next == StatusIdle && prev == StatusWorking && detail == detailYourTurn:
		// Only the Stop hook says "your turn"; the silence heuristic says
		// "interrupted?" and shouldn't make noise.
		return notice{kind: noticeFinished, detail: detail}, true
	}
	return notice{}, false
}

// Notification settings, from [notifications] in the config file.
const (
	notifyOff       = "off"
	notifyUnfocused = "unfocused" // default
	notifyAlways    = "always"
)

var notifyMode = notifyUnfocused

type notifyConfig struct {
	Enabled *bool  `toml:"enabled,omitempty"`
	When    string `toml:"when,omitempty"`
}

// loadNotifyMode reads [notifications] from the config file.
func loadNotifyMode() (string, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return notifyUnfocused, nil
	}
	if err != nil {
		return notifyUnfocused, err
	}
	return parseNotifyMode(string(data))
}

func parseNotifyMode(text string) (string, error) {
	var cfg struct {
		Notifications notifyConfig `toml:"notifications"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return notifyUnfocused, fmt.Errorf("%s: %w", configPath(), err)
	}
	n := cfg.Notifications
	if n.Enabled != nil && !*n.Enabled {
		return notifyOff, nil
	}
	switch n.When {
	case "":
		return notifyUnfocused, nil
	case notifyUnfocused, notifyAlways:
		return n.When, nil
	}
	return notifyUnfocused, fmt.Errorf("%s: notifications.when must be %q or %q, not %q", configPath(), notifyUnfocused, notifyAlways, n.When)
}

// shouldNotify decides whether to show a toast.
//
//   - "unfocused": only while deckhand's window doesn't have focus. If the
//     terminal never reports focus, notify unless the session is the one on
//     screen.
//   - "always": every time.
func shouldNotify(mode string, focusKnown, windowFocused, onScreen bool) bool {
	switch mode {
	case notifyOff:
		return false
	case notifyAlways:
		return true
	}
	if focusKnown {
		return !windowFocused
	}
	return !onScreen
}

// noticeText is the toast's title and body.
func noticeText(name, agent string, n notice, lastMessage string) (title, body string) {
	switch n.kind {
	case noticeWaiting:
		title = name + " needs you"
		body = n.detail
		if n.detail == "bell" {
			body = agent + " rang the bell"
		}
		if n.summary != "" {
			body += ": " + n.summary
		}
	default:
		title = name + " finished"
		body = agent + " is waiting for your next prompt"
		if lastMessage != "" {
			body = lastMessage
		}
	}
	return displayPathsIn(title), clip(oneLine(displayPathsIn(body)), 200)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip shortens s to at most n runes, adding "…".
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// xmlEscape escapes text for the toast XML.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// sendTestNotification shows the notification "deckhand notify-test" and
// the config screen send. DECKHAND_NOTIFY_LOG records it instead (tests).
func sendTestNotification() error {
	title, body := "deckhand", "Notifications work. You'll see one like this when a session needs you."
	if p := os.Getenv("DECKHAND_NOTIFY_LOG"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintf(f, "%s\t%s\n", title, body)
		return err
	}
	return showToast(title, body, "test")
}
