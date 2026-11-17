package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReplyLines(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"plain", "Fixed the redirect loop.", []string{"Fixed the redirect loop."}},
		{"empty", "  \n\n ", nil},
		{"heading and bold", "## Summary\n\n**Fixed** the `login` redirect loop; all tests pass.",
			[]string{"Summary", "Fixed the login redirect loop; all tests pass."}},
		{"list markers", "- first\n* second\n+ third\n1. fourth\n12) fifth\n- [x] done",
			[]string{"first", "second", "third", "fourth", "fifth", "done"}},
		{"whitespace collapsed", "  Ran   the\ttests  ", []string{"Ran the tests"}},
		{"quote and link", "> See [the docs](https://example.com/docs) for __more__.",
			[]string{"See the docs for more."}},
		{"rule skipped", "---\nDone.\n***", []string{"Done."}},
		{"starts with code block", "```go\nfunc main() {}\n```\nThat's the new entry point.",
			[]string{"That's the new entry point."}},
		{"only code", "```\nnpm test\n```", []string{"npm test"}},
		{"unclosed fence", "Here it is:\n```\ncode", []string{"Here it is:"}},
		{"tilde fence", "~~~\nx\n~~~\nOK", []string{"OK"}},
		{"windows newlines", "# Done\r\n\r\nAll good.\r\n", []string{"Done", "All good."}},
		{"number without marker", "2026 was a good year.", []string{"2026 was a good year."}},
	} {
		if got := replyLines(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: replyLines(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	if got := replyLines(strings.Repeat("line\n", 100)); len(got) != maxReplyLines {
		t.Errorf("long reply kept %d lines, want %d", len(got), maxReplyLines)
	}
}

func TestSessionDetail(t *testing.T) {
	for _, c := range []struct {
		name      string
		sn        snapshot
		want      string
		wantReply bool
	}{
		{"finished with reply", snapshot{status: StatusIdle, detail: "your turn", reply: []string{"Fixed it.", "More."}}, "Fixed it.", true},
		{"finished, reply not read yet", snapshot{status: StatusIdle, detail: "your turn"}, "your turn", false},
		{"permission with summary", snapshot{status: StatusWaiting, detail: "permission: Bash", summary: "npm test"}, "Bash: npm test", false},
		{"permission without summary", snapshot{status: StatusWaiting, detail: "permission: Bash"}, "permission: Bash", false},
		{"question", snapshot{status: StatusWaiting, detail: "question", summary: "x"}, "question", false},
		{"working", snapshot{status: StatusWorking, detail: "Bash", summary: "go test ./..."}, "Bash", false},
		{"exited", snapshot{status: StatusExited, detail: "exited · ⏎ resume"}, "exited · ⏎ resume", false},
		{"no detail", snapshot{status: StatusIdle}, "idle", false},
	} {
		got, isReply := sessionDetail(c.sn)
		if got != c.want || isReply != c.wantReply {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, got, isReply, c.want, c.wantReply)
		}
	}
}

// The reply only shows once the transcript has been read after the turn
// ended, so the previous turn's reply never stands in for the new one.
func TestSnapshotReplyFreshness(t *testing.T) {
	ended := time.Now()
	s := &Session{status: StatusIdle, detail: detailYourTurn, since: ended}
	s.info.lastText = []string{"Old reply."}
	s.info.textAt = ended.Add(-time.Second)
	if got := s.Snapshot().reply; got != nil {
		t.Errorf("read before the turn ended: reply = %q, want none", got)
	}
	s.info.textAt = ended.Add(time.Second)
	if got := s.Snapshot().reply; len(got) != 1 || got[0] != "Old reply." {
		t.Errorf("read after the turn ended: reply = %q", got)
	}
	s.status, s.detail = StatusWorking, "thinking"
	if got := s.Snapshot().reply; got != nil {
		t.Errorf("while working: reply = %q, want none", got)
	}
}

// Going idle makes the next MaybePoll run straight away.
func TestIdleTriggersPoll(t *testing.T) {
	s := &Session{status: StatusWorking, lastPoll: time.Now()}
	s.setStatusLocked(StatusIdle, detailYourTurn)
	if !s.lastPoll.IsZero() {
		t.Errorf("lastPoll = %v, want zero", s.lastPoll)
	}
}

func TestSidebarShowsReply(t *testing.T) {
	m := testModel("api", "docs")
	s := m.sessions[1]
	s.Agent = "claude"
	s.status, s.detail, s.since, s.unread = StatusIdle, detailYourTurn, time.Now(), true
	s.info.lastText = replyLines("**Updated** the README with the new install steps.")
	s.info.textAt = time.Now().Add(time.Second)
	m.sel = 0
	lines := strings.Join(m.renderSidebar(60, 12), "\n")
	if !strings.Contains(lines, "Updated the README with the new install steps.") {
		t.Errorf("sidebar doesn't show the reply:\n%s", lines)
	}
	if strings.Contains(lines, "your turn") {
		t.Errorf("sidebar still says \"your turn\":\n%s", lines)
	}
}
