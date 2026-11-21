package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusFromHook(t *testing.T) {
	cases := []struct {
		ev     hookEvent
		want   Status
		detail string
		ok     bool
	}{
		{hookEvent{Event: "SessionStart"}, StatusIdle, "ready", true},
		{hookEvent{Event: "UserPromptSubmit"}, StatusWorking, "thinking", true},
		{hookEvent{Event: "PreToolUse", Tool: "Bash"}, StatusWorking, "Bash", true},
		{hookEvent{Event: "PreToolUse", Tool: "AskUserQuestion"}, StatusWaiting, "question", true},
		{hookEvent{Event: "PermissionRequest", Tool: "Edit"}, StatusWaiting, "permission: Edit", true},
		{hookEvent{Event: "PostToolUse", Tool: "Edit"}, StatusWorking, "thinking", true},
		{hookEvent{Event: "Notification", NotificationType: "permission_prompt"}, 0, "", false},
		{hookEvent{Event: "Notification", NotificationType: "elicitation_dialog"}, StatusWaiting, "input requested", true},
		{hookEvent{Event: "Stop"}, StatusIdle, "your turn", true},
		{hookEvent{Event: "SubagentStop"}, 0, "", false},
	}
	for _, c := range cases {
		st, d, ok := statusFromHook(c.ev)
		if ok != c.ok || (ok && (st != c.want || d != c.detail)) {
			t.Errorf("%+v: got (%v, %q, %v), want (%v, %q, %v)", c.ev, st, d, ok, c.want, c.detail, c.ok)
		}
	}
}

func TestHeuristicStatus(t *testing.T) {
	now := time.Now()
	if st, _ := heuristicStatus(now.Add(-time.Second), now, false); st != StatusWorking {
		t.Errorf("recent output: got %v", st)
	}
	if st, _ := heuristicStatus(now.Add(-10*time.Second), now, false); st != StatusIdle {
		t.Errorf("old output: got %v", st)
	}
	if st, _ := heuristicStatus(time.Time{}, now, true); st != StatusWaiting {
		t.Errorf("bell: got %v", st)
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"abc-123", "6f1c2d3e-0000-4000-8000-000000000000"} {
		if !validID(id) {
			t.Errorf("%q should be valid", id)
		}
	}
	for _, id := range []string{"", "../x", `a\b`, "a b", strings.Repeat("a", 65)} {
		if validID(id) {
			t.Errorf("%q should be invalid", id)
		}
	}
}

func TestRecordAndReadEvent(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	ev := hookEvent{Seq: 42, Event: "Stop", ClaudeSessionID: "abc"}
	if err := recordEvent("s1", ev); err != nil {
		t.Fatal(err)
	}
	got, _, err := readStatusFile("s1")
	if err != nil || got != ev {
		t.Fatalf("got %+v, %v", got, err)
	}
	data, _ := os.ReadFile(filepath.Join(sessionDir("s1"), "events.jsonl"))
	if !strings.Contains(string(data), `"event":"Stop"`) {
		t.Errorf("events log missing entry: %s", data)
	}
}

func TestSessionEnv(t *testing.T) {
	got := sessionEnv([]string{"PATH=x", "CLAUDECODE=1", "Claude_Code_Child_Session=1", "ANTHROPIC_BASE_URL=https://proxy", "CLAUDE_PID=9"})
	want := []string{"PATH=x", "ANTHROPIC_BASE_URL=https://proxy"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestClaudeSettingsFile(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // not the user's own settings
	path, err := writeClaudeSettings("s1", "")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	for _, ev := range claudeHookEvents {
		if !strings.Contains(s, `"`+ev+`"`) {
			t.Errorf("settings missing %s", ev)
		}
	}
	if !strings.Contains(s, "hook --session s1") || strings.Contains(s, `\\`) {
		t.Errorf("bad hook command in %s", s)
	}
}
