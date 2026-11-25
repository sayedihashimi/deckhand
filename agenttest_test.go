package main

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestTestStatusText(t *testing.T) {
	for _, c := range []struct {
		name   string
		p      testProgress
		want   string
		hint   string
		result int
	}{
		{"program starting", testProgress{running: true, status: StatusStarting}, "starting…", "", testPending},
		{"program running", testProgress{running: true, status: StatusIdle}, "running", "", testOK},
		{"claude waiting", testProgress{claude: true, running: true, status: StatusStarting, elapsed: 5 * time.Second}, "waiting for Claude's SessionStart hook…", "", testPending},
		{"claude hooked", testProgress{claude: true, running: true, status: StatusIdle, hooked: true}, "✓ hooks work (SessionStart received)", "", testOK},
		{"pwsh no hooks", testProgress{claude: true, pwsh: true, running: true, status: StatusIdle, elapsed: testHookTimeout}, "✗ no hook events yet", "must call claude, not claude.exe", testFailed},
		{"program no hooks", testProgress{claude: true, running: true, status: StatusIdle, elapsed: time.Minute}, "✗ no hook events yet", "must pass deckhand's arguments", testFailed},
		{"exited", testProgress{claude: true, exitCode: 3, status: StatusExited}, "✗ it exited, exit code 3", "", testFailed},
		{"exited, code unknown", testProgress{exitCode: -1, status: StatusExited}, "✗ it exited, exit code unknown", "", testFailed},
		{"stopping", testProgress{claude: true, running: true, stopping: true}, "stopping when Claude is ready…", "", testPending},
	} {
		text, hint, result := testStatusText(c.p)
		if text != c.want || result != c.result || (c.hint == "") != (hint == "") || !strings.Contains(hint, c.hint) {
			t.Errorf("%s: got %q, %q, %d; want %q, hint with %q, %d", c.name, text, hint, result, c.want, c.hint, c.result)
		}
	}
}

// fakeTestStart replaces startSession with one that starts nothing: the
// session is running and starting.
func fakeTestStart(t *testing.T) *[]*Session {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	var started []*Session
	old := startSession
	startSession = func(s *Session, w, h int, resume bool, dirty *atomic.Bool) error {
		started = append(started, s)
		s.mu.Lock()
		s.running, s.Started = true, true
		s.setStatusLocked(StatusStarting, "")
		s.mu.Unlock()
		return nil
	}
	t.Cleanup(func() { startSession = old })
	return &started
}

const testAgentsConfig = `[[agents]]
name    = "fake"
command = "cmd"
type    = "claude"
`

// t on an agent tests it as edited: the session is added and selected,
// and neither the config file nor the saved sessions change.
func TestAgentTestStartsUnsaved(t *testing.T) {
	keepGlobals(t)
	p := tempConfig(t, testAgentsConfig)
	started := fakeTestStart(t)
	m := testModel("api", "web")
	m.width, m.height = 120, 32
	m.sel = 1
	m.handleKey(key("c"))
	m.cfg.agents[0].Args = []string{"--edited"} // an unsaved edit
	m.handleKey(key("t"))
	if len(*started) != 1 || m.test == nil {
		t.Fatalf("t should start a test: started %d, status %q", len(*started), m.cfg.status)
	}
	s := (*started)[0]
	if s.Name != "test · fake" || s.Dir != `C:\x\web` || m.sessions[m.sel] != s || !s.testing.Load() {
		t.Errorf("test session %q in %q, selected %v", s.Name, s.Dir, m.sessions[m.sel] == s)
	}
	if a, _ := s.agent(); len(a.Args) != 1 || a.Args[0] != "--edited" || !a.Claude {
		t.Errorf("should run the agent as edited: %+v", a)
	}
	if m.cfg == nil || m.focus != focusConfig {
		t.Error("the config screen should stay open")
	}
	screen := ansi.Strip(strings.Join(m.renderConfig(90, 31), "\n"))
	if !strings.Contains(screen, "Testing fake · waiting for Claude's SessionStart hook…") {
		t.Errorf("status line missing:\n%s", screen)
	}
	if data, _ := os.ReadFile(p); string(data) != testAgentsConfig {
		t.Errorf("config.toml changed:\n%s", data)
	}
	if err := m.saveSessions(); err != nil {
		t.Fatal(err)
	}
	if loaded := loadSessions(); len(loaded) != 2 {
		t.Errorf("the test session shouldn't be saved: %d saved", len(loaded))
	}

	// t again asks to keep or discard it rather than starting another.
	m.handleKey(key("t"))
	if len(*started) != 1 || m.cfg.testAsk == nil {
		t.Fatalf("second t: started %d, asking %v", len(*started), m.cfg.testAsk != nil)
	}
	if f := ansi.Strip(m.renderFooter(120)); !strings.Contains(f, "k keep it as a session · x discard it") {
		t.Errorf("footer: %q", f)
	}
	m.handleKey(key("esc"))
	if m.cfg.testAsk != nil || m.test == nil || m.cfg == nil {
		t.Error("esc should close the question and leave the test running")
	}
}

// The form's [Test] button tests the agent as typed, and the form stays.
func TestAgentTestFromForm(t *testing.T) {
	keepGlobals(t)
	tempConfig(t, "")
	started := fakeTestStart(t)
	m := testModel()
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	m.handleKey(key("enter")) // + Add agent
	typeText(m, "typed")
	m.handleKey(key("tab")) // kind: PowerShell -> program
	m.handleKey(key(" "))
	m.handleKey(key("tab")) // program
	typeText(m, "cmd")
	for m.cfg.form.field != ffTest {
		m.handleKey(key("tab"))
	}
	m.handleKey(key("enter"))
	if len(*started) != 1 || m.cfg.form == nil {
		t.Fatalf("[Test]: started %d, form open %v, status %q", len(*started), m.cfg.form != nil, m.cfg.status)
	}
	if a, _ := (*started)[0].agent(); a.Name != "typed" || a.Exe != "cmd" {
		t.Errorf("agent %+v", a)
	}
	if len(m.cfg.agents) != 0 {
		t.Error("testing shouldn't add the agent")
	}
	cwd, _ := os.Getwd()
	if (*started)[0].Dir != cwd {
		t.Errorf("with no session selected it should run in the current folder, not %q", (*started)[0].Dir)
	}
}

// Keeping makes it a normal session named after its folder; leaving the
// config screen with a test asks first.
func TestAgentTestKeepOnLeave(t *testing.T) {
	keepGlobals(t)
	tempConfig(t, testAgentsConfig)
	fakeTestStart(t)
	m := testModel("api")
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	m.handleKey(key("t"))
	s := m.test.s
	m.handleKey(key("esc"))
	if m.cfg == nil || m.cfg.testAsk == nil {
		t.Fatal("esc with a test running should ask first")
	}
	m.handleKey(key("k"))
	if m.test != nil || m.cfg != nil || s.testing.Load() || s.Name != "api" {
		t.Errorf("keep: test %v, screen open %v, testing %v, name %q", m.test != nil, m.cfg != nil, s.testing.Load(), s.Name)
	}
	if loaded := loadSessions(); len(loaded) != 2 {
		t.Errorf("the kept session should be saved: %d saved", len(loaded))
	}
}

// Discarding a program's test ends it straight away and selects the
// session that was selected before.
func TestAgentTestDiscardProgram(t *testing.T) {
	keepGlobals(t)
	tempConfig(t, "[[agents]]\nname = \"sh\"\ncommand = \"cmd\"\n")
	fakeTestStart(t)
	m := testModel("api", "web")
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	m.handleKey(key("t"))
	m.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m.handleKey(key("x"))
	if m.test != nil || len(m.sessions) != 2 || m.sel != 0 {
		t.Errorf("discard: test %v, %d sessions, sel %d", m.test != nil, len(m.sessions), m.sel)
	}
}

// A Claude test isn't ended while Claude is starting: discard waits until
// its SessionStart hook arrives, or the timeout.
func TestAgentTestDiscardWaitsForClaude(t *testing.T) {
	for _, how := range []string{"hook", "timeout"} {
		t.Run(how, func(t *testing.T) {
			keepGlobals(t)
			tempConfig(t, testAgentsConfig)
			fakeTestStart(t)
			m := testModel("api")
			m.width, m.height = 120, 32
			m.handleKey(key("c"))
			m.handleKey(key("t"))
			test := m.test
			m.handleKey(key("esc"))
			m.handleKey(key("x"))
			if m.test == nil || len(m.sessions) != 2 || !test.stopping {
				t.Fatal("discard shouldn't end Claude while it's starting")
			}
			if m.cfg != nil {
				t.Error("leaving should go ahead; the test stops later")
			}
			m.tickTest(time.Now())
			if m.test == nil {
				t.Fatal("still starting: it should wait")
			}
			if text, _, _ := testStatusText(test.progress(time.Now())); text != "stopping when Claude is ready…" {
				t.Errorf("status %q", text)
			}
			if how == "hook" {
				if err := recordEvent(test.s.ID, hookEvent{Seq: 1, Event: "SessionStart"}); err != nil {
					t.Fatal(err)
				}
				if err := recordEvent(test.s.ID, hookEvent{Seq: 2, Event: "UserPromptSubmit"}); err != nil {
					t.Fatal(err)
				}
			} else {
				test.started = time.Now().Add(-testHookTimeout)
			}
			m.tickTest(time.Now())
			if m.test != nil || len(m.sessions) != 1 || m.sessions[0].Name != "api" {
				t.Errorf("should be discarded now: test %v, %d sessions", m.test != nil, len(m.sessions))
			}
		})
	}
}
