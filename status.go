package main

import "time"

// Status is the state shown next to a session in the sidebar.
type Status int

const (
	StatusStarting Status = iota // process launched, nothing heard yet
	StatusIdle                   // ready, or finished its turn: your move
	StatusWorking                // thinking or running tools
	StatusWaiting                // blocked on you: permission prompt or question
	StatusError                  // failed to start or crashed
	StatusExited                 // process has ended
)

func (s Status) String() string {
	switch s {
	case StatusStarting:
		return "starting"
	case StatusIdle:
		return "idle"
	case StatusWorking:
		return "working"
	case StatusWaiting:
		return "needs you"
	case StatusError:
		return "error"
	case StatusExited:
		return "exited"
	}
	return "unknown"
}

// hookEvent is what `deckhand hook` records from a Claude Code hook payload.
type hookEvent struct {
	Seq              int64  `json:"seq"` // unix nanos, used for ordering
	Event            string `json:"event"`
	Tool             string `json:"tool,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`
	ClaudeSessionID  string `json:"claude_session_id,omitempty"`
	Transcript       string `json:"transcript,omitempty"` // path of Claude's transcript (JSONL)
	Summary          string `json:"summary,omitempty"`    // what the tool works on, e.g. the Bash command
}

// statusFromHook maps a Claude Code hook event to a sidebar status.
// ok is false when the event shouldn't change the status.
func statusFromHook(ev hookEvent) (st Status, detail string, ok bool) {
	switch ev.Event {
	case "SessionStart":
		return StatusIdle, "ready", true
	case "UserPromptSubmit":
		return StatusWorking, "thinking", true
	case "PreToolUse":
		if ev.Tool == "AskUserQuestion" {
			return StatusWaiting, "question", true
		}
		return StatusWorking, toolDetail(ev.Tool), true
	case "PostToolUse":
		return StatusWorking, "thinking", true
	case "PermissionRequest":
		return StatusWaiting, "permission: " + ev.Tool, true
	case "Notification":
		// permission_prompt is covered (sooner) by PermissionRequest, and a
		// late copy could overwrite a newer "working" state, so ignore it.
		if ev.NotificationType == "elicitation_dialog" {
			return StatusWaiting, "input requested", true
		}
		return 0, "", false
	case "Stop":
		return StatusIdle, detailYourTurn, true
	}
	return 0, "", false
}

// detailYourTurn is the detail after a Stop. The sidebar shows the reply
// in its place once it has been read from the transcript.
const detailYourTurn = "your turn"

func toolDetail(tool string) string {
	if tool == "" {
		return "working"
	}
	return tool
}

// Heuristics for sessions that don't send hook events (codex, gemini, shells),
// and a safety net for Claude when a hook never arrives (e.g. Esc interrupts
// a turn, so Stop doesn't fire).
const (
	activeWindow       = 2 * time.Second  // output this recent counts as working
	stuckWorkingWindow = 20 * time.Second // hooked session "working" but silent this long
)

// heuristicStatus is the status for a session without hooks, from output activity.
func heuristicStatus(lastOutput, now time.Time, bell bool) (Status, string) {
	if bell {
		return StatusWaiting, "bell"
	}
	if !lastOutput.IsZero() && now.Sub(lastOutput) < activeWindow {
		return StatusWorking, "output"
	}
	return StatusIdle, ""
}
