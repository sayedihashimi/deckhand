package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Testing an agent from the config screen: t on an agent, or [Test] in its
// form, starts it as edited (saved or not) in a temporary session, "test ·
// <name>", in the selected session's folder. The config screen stays open
// and shows how the test is going at the top. Then you keep the session (it
// becomes a normal one, named after its folder) or discard it.

// testHookTimeout is how long a Claude test waits for its SessionStart hook
// before saying there are none. A discard while Claude is still starting
// waits this long at most.
const testHookTimeout = 30 * time.Second

// agentTest is the running test.
type agentTest struct {
	s        *Session
	agent    Agent
	started  time.Time
	hooked   bool     // the SessionStart hook arrived
	stopping bool     // discarded while Claude was starting: ends when it's ready
	prevSel  *Session // selected before the test, selected again after a discard
}

// testAsk is the keep-or-discard question. then runs after k or x (leaving
// the config screen, when that's what asked).
type testAsk struct {
	then func(m *model) tea.Cmd
}

// testProgress is what the status line is worked out from.
type testProgress struct {
	claude, pwsh bool
	running      bool
	status       Status
	exitCode     int
	hooked       bool
	elapsed      time.Duration
	stopping     bool
}

// Test results, for the status line's colour.
const (
	testPending = iota
	testOK
	testFailed
)

// testStatusText is the test's status line, a hint for when it fails, and
// whether it's still going, worked or failed.
func testStatusText(p testProgress) (text, hint string, result int) {
	switch {
	case p.stopping && p.running:
		return "stopping when Claude is ready…", "", testPending
	case !p.running:
		code := "unknown"
		if p.exitCode >= 0 {
			code = fmt.Sprint(p.exitCode)
		}
		return "✗ it exited, exit code " + code, "", testFailed
	case !p.claude && p.status == StatusStarting:
		return "starting…", "", testPending
	case !p.claude:
		return "running", "", testOK
	case p.hooked:
		return "✓ hooks work (SessionStart received)", "", testOK
	case p.elapsed < testHookTimeout:
		return "waiting for Claude's SessionStart hook…", "", testPending
	}
	hint = "A program agent must pass deckhand's arguments (--session-id, --settings) on to claude."
	if p.pwsh {
		hint = "A PowerShell agent must call claude, not claude.exe: deckhand's claude function (the shim) adds the hooks."
	}
	return "✗ no hook events yet", hint, testFailed
}

func (t *agentTest) progress(now time.Time) testProgress {
	sn := t.s.Snapshot()
	return testProgress{
		claude:   t.agent.Claude,
		pwsh:     t.agent.Pwsh != "",
		running:  sn.running,
		status:   sn.status,
		exitCode: t.s.ExitCode(),
		hooked:   t.hooked,
		elapsed:  now.Sub(t.started),
		stopping: t.stopping,
	}
}

// canStop reports whether the test session can be ended now. A Claude
// session isn't ended while it's starting: Claude counts that as a failed
// start and turns off its fullscreen renderer. It's ready once its hook
// arrives or its status settles, and after testHookTimeout it's ended
// anyway.
func (t *agentTest) canStop(now time.Time) bool {
	p := t.progress(now)
	return !p.claude || !p.running || p.hooked || p.status != StatusStarting || p.elapsed >= testHookTimeout
}

// testFolder is where a test runs: the selected session's folder, or the
// current folder.
func (m *model) testFolder() string {
	if s := m.current(); s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.Dir
	}
	dir, _ := os.Getwd()
	return dir
}

// startTest starts a test of an agent as edited. With a test already
// running it asks whether to keep that one instead.
func (m *model) startTest(cfg agentConfig) tea.Cmd {
	c := m.cfg
	if m.test != nil {
		if m.test.stopping {
			c.setStatus("Still stopping the last test session (Claude is starting).", true)
			return nil
		}
		m.askTest(nil)
		return nil
	}
	a, err := cfg.toAgent()
	if err != nil {
		c.setStatus(fmt.Sprintf("Can't test %q: %v", cfg.Name, err), true)
		return nil
	}
	dir := m.testFolder()
	s := newSession(a, dir, "test · "+a.Name)
	s.override = &a
	s.testing.Store(true)
	w, h := m.termSize()
	if err := startSession(s, w, h, false, m.dirty); err != nil {
		c.setStatus("Couldn't start the test: "+err.Error(), true)
		return nil
	}
	m.test = &agentTest{s: s, agent: a, started: time.Now(), prevSel: m.current()}
	m.clearFilter() // so the test session shows
	m.sessions = append(m.sessions, s)
	m.sel = len(m.sessions) - 1
	m.showSelected() // in "(no group)", at the end
	c.setStatus(fmt.Sprintf("Testing %q in %s. Nothing is sent to it.", a.Name, shortDir(dir, 50)), false)
	return nil
}

// askTest asks whether to keep or discard the test session.
func (m *model) askTest(then func(m *model) tea.Cmd) {
	if m.cfg != nil {
		m.cfg.testAsk = &testAsk{then: then}
	}
}

// keepTest makes the test session a normal one, named after its folder.
func (m *model) keepTest() {
	t := m.test
	if t == nil {
		return
	}
	m.test = nil
	s := t.s
	s.mu.Lock()
	s.Name, s.NameSource = sessionName(s.Dir), nameFolder
	name := s.Name
	s.mu.Unlock()
	s.testing.Store(false)
	_ = m.saveSessions()
	if m.cfg != nil {
		m.cfg.setStatus(fmt.Sprintf("Kept the test session as %q.", name), false)
	}
}

// discardTest ends the test session and removes it, once it can be ended
// (see canStop). Until then it shows "stopping when Claude is ready…".
func (m *model) discardTest() {
	t := m.test
	if t == nil {
		return
	}
	t.stopping = true
	if t.canStop(time.Now()) {
		m.finishDiscard()
		return
	}
	if m.cfg != nil {
		m.cfg.setStatus("Discarding the test session: stopping when Claude is ready…", false)
	}
}

func (m *model) finishDiscard() {
	t := m.test
	m.test = nil
	m.removeSession(t.s)
	if i := m.indexOf(t.prevSel); i >= 0 {
		m.selectSession(i)
	}
	if m.cfg != nil {
		m.cfg.setStatus("Discarded the test session.", false)
	}
}

// tickTest checks for the SessionStart hook, and ends a discarded test once
// it can be ended.
func (m *model) tickTest(now time.Time) {
	t := m.test
	if t == nil {
		return
	}
	if t.agent.Claude && !t.hooked {
		t.hooked = sawHookEvent(t.s.ID, "SessionStart")
	}
	if t.stopping && t.canStop(now) {
		m.finishDiscard()
	}
}

// sawHookEvent reports whether a session's hooks have recorded an event:
// the latest one (status.json), or any in its log (events.jsonl).
func sawHookEvent(id, event string) bool {
	if ev, _, err := readStatusFile(id); err == nil && ev.Event == event {
		return true
	}
	f, err := os.Open(filepath.Join(sessionDir(id), "events.jsonl"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var ev hookEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Event == event {
			return true
		}
	}
	return false
}

// testLines are the config screen's lines about the test, under its title.
func (m *model) testLines(w int) []string {
	t := m.test
	if t == nil {
		return nil
	}
	text, hint, result := testStatusText(t.progress(time.Now()))
	st := stWaitText
	switch result {
	case testOK:
		st = stOKText
	case testFailed:
		st = stErrText
	}
	line := " " + stSection.Render("Testing") + " " + t.agent.Name + stMuted.Render(" · ") + st.Render(displayPathsIn(text))
	if !t.stopping {
		line += stMuted.Render("   ctrl+t keep or discard")
	}
	out := []string{fit(line, w)}
	if hint != "" {
		out = append(out, fit(stMuted.Render("      "+displayPathsIn(hint)), w))
	}
	if result == testFailed {
		for _, l := range t.s.LastLines(3) {
			out = append(out, fit(stMuted.Render("      │ ")+strings.TrimSpace(l), w))
		}
	}
	return out
}
