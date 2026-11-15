package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// claudeHookEvents are the Claude Code hook events deckhand listens to.
var claudeHookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"PermissionRequest",
	"Notification",
	"Stop",
}

// toolEvents take a tool-name matcher.
var toolEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true, "PermissionRequest": true}

// writeClaudeSettings writes the per-session settings file passed to
// `claude --settings`. It only adds hooks; the user's own settings (including
// any proxy configuration) still load as normal.
func writeClaudeSettings(sessionID string) (string, error) {
	type hookCmd struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Matcher string    `json:"matcher,omitempty"`
		Hooks   []hookCmd `json:"hooks"`
	}
	hooks := map[string][]matcher{}
	cmd := hookCommand(sessionID)
	for _, ev := range claudeHookEvents {
		m := matcher{Hooks: []hookCmd{{Type: "command", Command: cmd, Timeout: 10}}}
		if toolEvents[ev] {
			m.Matcher = "*"
		}
		hooks[ev] = []matcher{m}
	}
	data, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return "", err
	}
	dir := sessionDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "claude-settings.json")
	return path, os.WriteFile(path, data, 0o600)
}

// runHook implements `deckhand hook --session <id>`: Claude Code pipes the
// hook payload on stdin, and we record the latest event for the TUI to pick
// up. It never prints anything (stdout would be read as a hook decision) and
// always exits 0 so it can't block or break the agent.
func runHook(args []string) int {
	var id string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--session" {
			id = args[i+1]
		}
	}
	if id == "" {
		id = os.Getenv("DECKHAND_SESSION_ID")
	}
	if id == "" || !validID(id) {
		return 0
	}
	var payload struct {
		HookEventName    string `json:"hook_event_name"`
		ToolName         string `json:"tool_name"`
		NotificationType string `json:"notification_type"`
		SessionID        string `json:"session_id"`
	}
	data, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err := json.Unmarshal(data, &payload); err != nil || payload.HookEventName == "" {
		return 0
	}
	ev := hookEvent{
		Seq:              time.Now().UnixNano(),
		Event:            payload.HookEventName,
		Tool:             payload.ToolName,
		NotificationType: payload.NotificationType,
		ClaudeSessionID:  payload.SessionID,
	}
	if err := recordEvent(id, ev); err != nil {
		fmt.Fprintln(os.Stderr, "deckhand hook:", err)
	}
	return 0
}

// recordEvent writes the latest event atomically and appends it to a log.
func recordEvent(id string, ev hookEvent) error {
	dir := sessionDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line, _ := json.Marshal(ev)
	if f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
	tmp := filepath.Join(dir, fmt.Sprintf("status.%d.tmp", ev.Seq))
	if err := os.WriteFile(tmp, line, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "status.json"))
}

// readStatusFile returns the latest recorded hook event for a session.
func readStatusFile(id string) (hookEvent, time.Time, error) {
	path := filepath.Join(sessionDir(id), "status.json")
	fi, err := os.Stat(path)
	if err != nil {
		return hookEvent{}, time.Time{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return hookEvent{}, time.Time{}, err
	}
	var ev hookEvent
	err = json.Unmarshal(data, &ev)
	return ev, fi.ModTime(), err
}

// validID guards against path tricks in --session values.
func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
