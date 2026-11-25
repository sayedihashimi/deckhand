package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Transcript lines, as Claude writes them.
func customLine(t string) string {
	return `{"type":"custom-title","customTitle":"` + t + `","sessionId":"s1"}` + "\n"
}
func aiLine(t string) string {
	return `{"type":"ai-title","aiTitle":"` + t + `","sessionId":"s1"}` + "\n"
}

const (
	userLine  = `{"type":"user","message":{"role":"user","content":"hi"},"sessionId":"s1"}` + "\n"
	replyLine = `{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"hello"}]},"sessionId":"s1"}` + "\n"
)

func TestParseTitles(t *testing.T) {
	cases := []struct {
		what    string
		data    string
		renames int
		rename  string
		ai      string
	}{
		{"nothing", userLine + replyLine, 0, "", ""},
		{"ai-title only", userLine + aiLine("Fix the login bug") + replyLine, 0, "", "Fix the login bug"},
		{"the latest ai-title", userLine + aiLine("one") + replyLine + aiLine("two"), 0, "", "two"},
		{"/rename", userLine + replyLine + customLine("auth work"), 1, "auth work", ""},
		{"/rename, then the same again on resume", userLine + customLine("auth work") + replyLine + customLine("auth work"), 1, "auth work", ""},
		{"the launch name doesn't count", customLine("wrapped") + userLine + aiLine("Fix it") + replyLine, 0, "", "Fix it"},
		{"nor its copy on resume", customLine("wrapped") + userLine + replyLine + customLine("wrapped") + aiLine("Fix it"), 0, "", "Fix it"},
		{"a /rename after the launch name", customLine("wrapped") + userLine + replyLine + customLine("auth work"), 1, "auth work", ""},
		{"the latest /rename wins", userLine + customLine("one") + replyLine + customLine("two") + customLine("two") + customLine("three"), 3, "three", ""},
		{"back to an earlier name", userLine + customLine("one") + customLine("two") + customLine("one"), 3, "one", ""},
		{"empty titles are skipped", userLine + customLine("  ") + aiLine("") + customLine("x"), 1, "x", ""},
		{"whitespace collapses", userLine + customLine(`a\n  b`), 1, "a b", ""},
		{"a cut-off line", `pe":"custom-title","customTitle":"x"}` + "\n" + userLine, 0, "", ""},
	}
	for _, c := range cases {
		got := parseTitles(c.data)
		if got.renames != c.renames || got.rename != c.rename || got.ai != c.ai {
			t.Errorf("%s: %+v, want %d %q ai %q", c.what, got, c.renames, c.rename, c.ai)
		}
	}
}

// The scan reads only what was added, and a line still being written
// waits for the next read.
func TestTitleScanGrows(t *testing.T) {
	p := filepath.Join(t.TempDir(), "11111111-2222-3333-4444-555555555555.jsonl")
	write := func(s string) {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(s)
		_ = f.Close()
	}
	var ts titleScan
	write(customLine("wrapped") + userLine + aiLine("First"))
	got, err := ts.read(p)
	if err != nil || got.ai != "First" || got.renames != 0 || got.conv != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("first read: %+v %v", got, err)
	}
	half := customLine("renamed")
	write(half[:10])
	if got, _ = ts.read(p); got.renames != 0 {
		t.Fatalf("half a line counted: %+v", got)
	}
	write(half[10:])
	if got, _ = ts.read(p); got.renames != 1 || got.rename != "renamed" {
		t.Fatalf("after the rest of the line: %+v", got)
	}
	// Rewritten shorter: start over.
	_ = os.WriteFile(p, []byte(userLine+aiLine("Other")), 0o644)
	if got, _ = ts.read(p); got.renames != 0 || got.ai != "Other" {
		t.Fatalf("rewritten: %+v", got)
	}
}

// titleSession is a Claude session with a transcript that the test writes.
func titleSession(t *testing.T) (*Session, func(string)) {
	t.Helper()
	withListOpts(t, defaultListSettings())
	dir := t.TempDir()
	s := newSession(Agent{Name: "claude", Claude: true}, dir, sessionName(dir))
	s.Agent = "claude"
	s.TranscriptPath = filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	add := func(text string) {
		f, err := os.OpenFile(s.TranscriptPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(text)
		_ = f.Close()
	}
	return s, add
}

// poll is what the poll goroutine and the next frame do.
func poll(s *Session) bool {
	s.pollTitle(s.TranscriptPath)
	return s.applyTitle()
}

func (s *Session) nameAndSource() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Name, s.NameSource
}

func wantName(t *testing.T, s *Session, step, name, src string) {
	t.Helper()
	if n, so := s.nameAndSource(); n != name || so != src {
		t.Errorf("%s: name %q (%s), want %q (%s)", step, n, so, name, src)
	}
}

// renameWithR is what r does.
func renameWithR(s *Session, name string) {
	m := &model{sessions: []*Session{s}}
	m.handleKey(key("r"))
	m.rename.SetValue(name)
	m.handleKey(key("enter"))
}

// folder → ai-title → /rename → r → /rename again.
func TestTitlePrecedence(t *testing.T) {
	s, add := titleSession(t)
	folder := s.Name

	add(customLine("wrapped") + userLine)
	poll(s)
	wantName(t, s, "launch name only", folder, nameFolder)

	add(aiLine("Fix the login bug") + replyLine)
	if !poll(s) {
		t.Error("a new name should be saved")
	}
	wantName(t, s, "ai-title", "Fix the login bug", nameAI)

	add(aiLine("Fix login and logout"))
	poll(s)
	wantName(t, s, "a newer ai-title", "Fix login and logout", nameAI)

	add(customLine("auth work"))
	poll(s)
	wantName(t, s, "/rename", "auth work", nameClaude)

	add(aiLine("Something else"))
	poll(s)
	wantName(t, s, "an ai-title after /rename", "auth work", nameClaude)

	renameWithR(s, "mine")
	wantName(t, s, "r", "mine", nameUser)
	if poll(s) {
		t.Error("nothing new in the transcript: nothing to save")
	}
	add(customLine("auth work") + aiLine("Yet another"))
	poll(s)
	wantName(t, s, "the same /rename re-written after r", "mine", nameUser)

	add(customLine("auth v2"))
	poll(s)
	wantName(t, s, "/rename after r", "auth v2", nameClaude)

	renameWithR(s, "mine again")
	poll(s)
	wantName(t, s, "r again", "mine again", nameUser)
}

// After a restart the transcript is read from the start again: the saved
// name and source stay, and no older title comes back.
func TestTitleRestart(t *testing.T) {
	s, add := titleSession(t)
	add(userLine + aiLine("Old title") + replyLine + customLine("auth work") + aiLine("Newer title"))
	poll(s)
	wantName(t, s, "before", "auth work", nameClaude)

	reload := func() *Session {
		t.Helper()
		if err := writeSessionsFile([]*Session{s}, nil); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(storePath())
		var saved []map[string]any
		_ = json.Unmarshal(data, &saved)
		if saved[0]["name_source"] == nil || saved[0]["claude_title"] == nil {
			t.Errorf("sessions.json: %s", data)
		}
		l := loadSessions()
		if len(l) != 1 {
			t.Fatalf("loaded %d", len(l))
		}
		return l[0]
	}
	s = reload()
	wantName(t, s, "loaded", "auth work", nameClaude)
	poll(s)
	wantName(t, s, "after reading the transcript again", "auth work", nameClaude)

	// r, restart: the transcript's /rename doesn't come back.
	renameWithR(s, "mine")
	s = reload()
	poll(s)
	wantName(t, s, "r, then a restart", "mine", nameUser)

	// A new /rename after the restart still counts.
	add(customLine("auth work") + customLine("auth v2"))
	poll(s)
	wantName(t, s, "a /rename after the restart", "auth v2", nameClaude)
}

func TestTitleSettingOff(t *testing.T) {
	s, add := titleSession(t)
	listOpts.followTitle = false
	add(userLine + aiLine("Fix it") + customLine("auth work"))
	if poll(s) {
		t.Error("nothing should change")
	}
	wantName(t, s, "off", filepath.Base(s.Dir), nameFolder)

	parsed, err := parseListConfig("[list]\nfollow_claude_title = false\n")
	if err != nil || parsed.followTitle {
		t.Errorf("parse: %+v %v", parsed, err)
	}
	if parsed, _ := parseListConfig(""); !parsed.followTitle {
		t.Error("on by default")
	}
	if _, err := parseListConfig("[list]\nfollow_claude_title = \"no\"\n"); err == nil {
		t.Error("follow_claude_title must be a boolean")
	}
}

func TestTitleOnlyClaude(t *testing.T) {
	s, add := titleSession(t)
	s.Agent = "codex"
	add(userLine + aiLine("Fix it"))
	poll(s)
	wantName(t, s, "codex", filepath.Base(s.Dir), nameFolder)

	// A wrapper agent of type claude does follow it.
	allAgents = append(allAgents, Agent{Name: "wrap", Pwsh: "wrap", Claude: true, Custom: true})
	t.Cleanup(func() { allAgents = builtinAgents })
	s.Agent = "wrap"
	poll(s)
	wantName(t, s, "wrapper", "Fix it", nameAI)

	// An agent test keeps its "test · <name>".
	s2, add2 := titleSession(t)
	s2.Name = "test · claude"
	s2.testing.Store(true)
	add2(userLine + aiLine("Fix it"))
	poll(s2)
	if n, _ := s2.nameAndSource(); n != "test · claude" {
		t.Errorf("test session renamed to %q", n)
	}
}

// Sessions saved before the source was: the folder's name follows the
// title, a name given with r stays until a new /rename.
func TestTitleLegacySessions(t *testing.T) {
	s, add := titleSession(t)
	s.NameSource = ""
	add(userLine + customLine("old rename") + aiLine("Fix it"))
	poll(s)
	wantName(t, s, "folder name", "old rename", nameClaude)

	s2, add2 := titleSession(t)
	s2.Name, s2.NameSource = "my name", ""
	add2(userLine + customLine("old rename") + aiLine("Fix it"))
	poll(s2)
	wantName(t, s2, "renamed with r", "my name", nameUser)
	add2(customLine("new rename"))
	poll(s2)
	wantName(t, s2, "then /rename", "new rename", nameClaude)

	s3 := &Session{Name: "quiet-otter", Branch: "quiet-otter", Dir: `C:\x\api-wt\quiet-otter-1`}
	if src := s3.nameSourceLocked(); src != nameFolder {
		t.Errorf("worktree branch name: %s", src)
	}
}

// A fork keeps its "(fork)" name: its transcript starts as a copy of the
// original's, so neither the copied ai-title nor the copied /rename
// renames it. A /rename in the fork does.
func TestTitleFork(t *testing.T) {
	forkStart(t)
	withListOpts(t, defaultListSettings())
	m := claudeModel()
	orig := m.sessions[0]
	dir := t.TempDir()
	orig.TranscriptPath = filepath.Join(dir, "orig.jsonl")
	copied := userLine + aiLine("Fix it") + replyLine + customLine("auth work") + replyLine
	_ = os.WriteFile(orig.TranscriptPath, []byte(copied), 0o644)
	poll(orig)
	wantName(t, orig, "original", "auth work", nameClaude)

	m.sel = 0
	m.handleKey(forkKey())
	f := m.sessions[1]
	wantName(t, f, "new fork", "auth work (fork)", nameUser)

	f.TranscriptPath = filepath.Join(dir, "fork.jsonl")
	_ = os.WriteFile(f.TranscriptPath, []byte(copied+aiLine("Fix it another way")), 0o644)
	poll(f)
	wantName(t, f, "copied titles", "auth work (fork)", nameUser)

	add := func(s string) {
		fh, _ := os.OpenFile(f.TranscriptPath, os.O_APPEND|os.O_WRONLY, 0o644)
		_, _ = fh.WriteString(s)
		_ = fh.Close()
	}
	add(customLine("auth, other way"))
	poll(f)
	wantName(t, f, "/rename in the fork", "auth, other way", nameClaude)

	// A fork whose transcript doesn't copy the titles: its first /rename
	// counts.
	m.sel, m.focus = 0, focusSidebar
	m.handleKey(forkKey())
	f2 := m.sessions[1]
	f2.TranscriptPath = filepath.Join(dir, "fork2.jsonl")
	_ = os.WriteFile(f2.TranscriptPath, []byte(userLine+replyLine+aiLine("x")), 0o644)
	poll(f2)
	wantName(t, f2, "fork 2", "auth work (fork)", nameUser) // the first fork has its own name now
	_ = os.WriteFile(f2.TranscriptPath, []byte(userLine+replyLine+aiLine("x")+customLine("second try")), 0o644)
	poll(f2)
	wantName(t, f2, "fork 2 /rename", "second try", nameClaude)
}

// The config screen's Title row turns the setting off and saves it.
func TestConfigScreenTitleRow(t *testing.T) {
	keepGlobals(t)
	t.Setenv("DECKHAND_HOME", t.TempDir())
	p := tempConfig(t, "")
	m := testModel("api")
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	if m.cfg == nil {
		t.Fatal("no config screen")
	}
	m.cfg.focusRow(m.cfg.rowOf(rowListTitle))
	if !m.cfg.listTitle {
		t.Fatal("on by default")
	}
	m.handleKey(key("right"))
	m.handleKey(key("ctrl+s"))
	if m.cfg.statusErr {
		t.Fatalf("save: %q", m.cfg.status)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "follow_claude_title = false") {
		t.Errorf("config.toml:\n%s", data)
	}
	if listOpts.followTitle {
		t.Error("listOpts not reloaded")
	}
}
