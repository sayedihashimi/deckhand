package main

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestForkName(t *testing.T) {
	taken := map[string]bool{}
	is := func(n string) bool { return taken[n] }
	cases := []struct {
		name, want string
		taken      []string
	}{
		{"api", "api (fork)", nil},
		{"api", "api (fork 2)", []string{"api (fork)"}},
		{"api", "api (fork 3)", []string{"api (fork)", "api (fork 2)"}},
		{"api (fork)", "api (fork 2)", []string{"api (fork)"}},
		{"api (fork 2)", "api (fork)", nil},
		{"my (forked) idea", "my (forked) idea (fork)", nil},
	}
	for _, c := range cases {
		clear(taken)
		for _, n := range c.taken {
			taken[n] = true
		}
		if got := forkName(c.name, is); got != c.want {
			t.Errorf("forkName(%q) with %q taken = %q, want %q", c.name, c.taken, got, c.want)
		}
	}
}

func TestBuildLaunchForkClaude(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	a := Agent{Name: "claude", Exe: "git", Args: []string{"--model", "opus"}, Claude: true}
	spec, err := buildLaunch(a, "s1", "new-id", false, launchExtras{forkFrom: "old-id"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(spec.args, "|")
	if !strings.HasPrefix(args, "--model|opus|--resume|old-id|--fork-session|--session-id|new-id|--settings|") {
		t.Errorf("args: %q", spec.args)
	}
	// resume is ignored for a fork: the fork's own ID isn't resumed.
	spec, _ = buildLaunch(a, "s1", "new-id", true, launchExtras{forkFrom: "old-id"})
	if got := strings.Join(spec.args, "|"); got != args {
		t.Errorf("fork with resume: %q", spec.args)
	}
	// Agents that aren't Claude have nothing to fork.
	spec, _ = buildLaunch(Agent{Name: "codex", Exe: "git"}, "s1", "", false, launchExtras{forkFrom: "old-id"})
	if len(spec.args) != 0 {
		t.Errorf("codex: %q", spec.args)
	}
}

func TestBuildLaunchForkPwsh(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	if pwshPath() == "" {
		t.Skip("PowerShell not found")
	}
	a := Agent{Name: "smc", Pwsh: "smc", Resume: "smc -Resume {id}", Claude: true}
	spec, err := buildLaunch(a, "s1", "new-id", false, launchExtras{forkFrom: "old-id"})
	if err != nil {
		t.Fatal(err)
	}
	if script := spec.args[len(spec.args)-1]; script != ". $env:DECKHAND_SHIM; smc -Resume old-id; exit $LASTEXITCODE" {
		t.Errorf("script = %q", script)
	}
	for _, want := range []string{"DECKHAND_CLAUDE_FORK=1", "DECKHAND_CLAUDE_SESSION_ID=new-id"} {
		if !slices.Contains(spec.env, want) {
			t.Errorf("env missing %s: %q", want, spec.env)
		}
	}
	// A normal resume doesn't fork.
	spec, _ = buildLaunch(a, "s1", "old-id", true, launchExtras{})
	if slices.Contains(spec.env, "DECKHAND_CLAUDE_FORK=1") {
		t.Errorf("resume env: %q", spec.env)
	}
	// Without a resume command there's no way to load the conversation.
	if _, err := buildLaunch(Agent{Name: "w", Pwsh: "w", Claude: true}, "s1", "new-id", false, launchExtras{forkFrom: "old-id"}); err != errForkNeedsResume {
		t.Errorf("no resume command: err = %v", err)
	}
	for _, want := range []string{"DECKHAND_CLAUDE_FORK", "'--fork-session'"} {
		if !strings.Contains(claudeShim, want) {
			t.Errorf("the shim should use %s", want)
		}
	}
}

// forkStart replaces startSession and records the fork's launch extras.
func forkStart(t *testing.T) *[]*Session {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	var started []*Session
	old := startSession
	startSession = func(s *Session, w, h int, resume bool, dirty *atomic.Bool) error {
		if resume {
			t.Errorf("a fork started with resume")
		}
		started = append(started, s)
		s.mu.Lock()
		s.running, s.Started = true, true
		s.mu.Unlock()
		return nil
	}
	t.Cleanup(func() { startSession = old })
	return &started
}

func forkKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'f', Text: "f"} }

func claudeModel() *model {
	m := testModel("api", "docs", "web")
	for _, s := range m.sessions {
		s.Agent = "claude"
		s.ClaudeSessionID = "conv-" + s.Name
		s.Started = true
		s.status = StatusExited
	}
	return m
}

func TestForkSelected(t *testing.T) {
	for _, running := range []bool{false, true} {
		started := forkStart(t)
		m := claudeModel()
		orig := m.sessions[1]
		orig.running = running
		if running {
			orig.status = StatusIdle
		}
		m.sel = 1
		m.filter = "docs"

		m.handleKey(forkKey())

		if len(*started) != 1 || len(m.sessions) != 4 {
			t.Fatalf("running=%v: started %d, %d sessions", running, len(*started), len(m.sessions))
		}
		n := m.sessions[2]
		if n != (*started)[0] || m.sel != 2 || m.focus != focusTerminal || m.filter != "" {
			t.Fatalf("running=%v: the fork should be selected right below the original (sel %d, filter %q)", running, m.sel, m.filter)
		}
		if n.Name != "docs (fork)" || n.Agent != "claude" || n.Dir != orig.Dir || n.ForkedFrom != "docs" {
			t.Errorf("running=%v: fork %+v", running, n)
		}
		if n.ID == orig.ID || n.ClaudeSessionID == "" || n.ClaudeSessionID == "conv-docs" {
			t.Errorf("running=%v: the fork needs its own IDs: %s / %s", running, n.ID, n.ClaudeSessionID)
		}
		if n.forkOf != "conv-docs" || n.forkHint != "docs" {
			t.Errorf("running=%v: forkOf %q, forkHint %q", running, n.forkOf, n.forkHint)
		}
		if orig.ClaudeSessionID != "conv-docs" || orig.Name != "docs" || orig.running != running || orig.ForkedFrom != "" {
			t.Errorf("running=%v: original changed: %+v", running, orig)
		}

		// Forking the original again picks the next name.
		m.sel, m.focus = 1, focusSidebar
		m.handleKey(forkKey())
		if len(m.sessions) != 5 || m.sessions[2].Name != "docs (fork 2)" || m.sessions[3].Name != "docs (fork)" {
			t.Errorf("running=%v: second fork: %v", running, names(m))
		}
	}
}

func names(m *model) []string {
	var out []string
	for _, s := range m.sessions {
		out = append(out, s.Name)
	}
	return out
}

func TestForkRefused(t *testing.T) {
	allAgents = append(slices.Clone(builtinAgents),
		Agent{Name: "wrap", Pwsh: "wrap", Claude: true, Custom: true},
		Agent{Name: "smc", Pwsh: "smc", Resume: "smc -Resume {id}", Claude: true, Custom: true})
	t.Cleanup(func() { allAgents = builtinAgents })
	cases := []struct {
		what  string
		setup func(s *Session)
		flash string
	}{
		{"not Claude", func(s *Session) { s.Agent = "codex" }, "Fork needs a Claude agent"},
		{"wrapper without resume", func(s *Session) { s.Agent = "wrap" }, "Fork needs a resume command for wrap"},
		{"never started", func(s *Session) { s.Started = false }, "Nothing to fork yet: start docs first"},
		{"mid-turn", func(s *Session) { s.running, s.status = true, StatusWorking }, "Wait for it to finish its turn"},
		{"permission prompt", func(s *Session) { s.running, s.status = true, StatusWaiting }, "Wait for it to finish its turn"},
		{"unknown agent", func(s *Session) { s.Agent = "gone" }, "Couldn't fork: unknown agent"},
	}
	for _, c := range cases {
		started := forkStart(t)
		m := claudeModel()
		m.sel = 1
		c.setup(m.sessions[1])
		m.handleKey(forkKey())
		if len(*started) != 0 || len(m.sessions) != 3 {
			t.Errorf("%s: forked anyway", c.what)
		}
		if !strings.HasPrefix(m.flash, c.flash) {
			t.Errorf("%s: flash %q, want %q", c.what, m.flash, c.flash)
		}
	}

	// A wrapper agent with a resume command can fork.
	started := forkStart(t)
	m := claudeModel()
	m.sel = 1
	m.sessions[1].Agent = "smc"
	m.handleKey(forkKey())
	if len(*started) != 1 || m.sessions[2].Agent != "smc" {
		t.Errorf("smc: started %d, %v", len(*started), names(m))
	}
}

// A fork of a worktree session shares the worktree but doesn't own it.
func TestForkWorktreeSession(t *testing.T) {
	forkStart(t)
	m := claudeModel()
	s := m.sessions[0]
	s.Dir = `C:\src\api-wt\quiet-otter`
	s.RepoRoot, s.Worktree, s.Branch, s.CodeRepo = `C:\src\api`, `C:\src\api-wt\quiet-otter`, "quiet-otter", `C:\src\api`
	m.handleKey(forkKey())
	n := m.sessions[1]
	if n.Dir != s.Dir || n.Worktree != "" || n.Branch != "" || n.hasWorktree() {
		t.Errorf("fork: %+v", n)
	}
	n.mu.Lock()
	gitDir, extra := n.gitDirLocked(), n.launchExtrasLocked()
	n.mu.Unlock()
	if gitDir != s.Worktree || extra.addDir != "" {
		t.Errorf("fork git dir %q, add-dir %q", gitDir, extra.addDir)
	}
}

func TestForkSavedAndLoaded(t *testing.T) {
	forkStart(t)
	m := claudeModel()
	m.handleKey(forkKey())
	loaded := loadSessions()
	if len(loaded) != 4 {
		t.Fatalf("loaded %d sessions", len(loaded))
	}
	f := loaded[1]
	if f.Name != "api (fork)" || f.ForkedFrom != "api" || f.ClaudeSessionID != m.sessions[1].ClaudeSessionID || !f.Started {
		t.Errorf("loaded fork: %+v", f)
	}
	// What to copy is only for the first start; after a restart the fork
	// resumes its own conversation.
	if f.forkOf != "" || f.detail != "⏎ resume" {
		t.Errorf("loaded fork: forkOf %q, detail %q", f.forkOf, f.detail)
	}
}

func TestForkSidebarHint(t *testing.T) {
	sn := snapshot{status: StatusIdle, detail: "ready", forkOf: "docs"}
	if got, _ := sessionDetail(sn); got != "⑂ docs" {
		t.Errorf("ready fork: %q", got)
	}
	sn.reply = []string{"Done."}
	if got, _ := sessionDetail(sn); got != "Done." {
		t.Errorf("a reply wins: %q", got)
	}
	sn = snapshot{status: StatusWaiting, detail: "permission: Bash", summary: "npm test", forkOf: "docs"}
	if got, _ := sessionDetail(sn); got != "Bash: npm test" {
		t.Errorf("a permission prompt wins: %q", got)
	}
	// The hint goes once the fork's own turn starts.
	s := &Session{forkHint: "docs", status: StatusIdle}
	s.mu.Lock()
	s.setStatusLocked(StatusWorking, "thinking")
	s.mu.Unlock()
	if s.Snapshot().forkOf != "" {
		t.Error("the hint should go when the fork starts working")
	}
}

// A fork of a session in a group lands right below it, in the same group,
// so the list keeps its group-by-group order.
func TestForkKeepsGroup(t *testing.T) {
	started := forkStart(t)
	m := claudeModel()
	m.groups = []*group{{Name: "app"}}
	for i, s := range m.sessions {
		if i < 2 {
			s.Group = "app"
		}
	}
	m.sel = 1
	m.handleKey(forkKey())
	if len(*started) != 1 {
		t.Fatalf("started %d", len(*started))
	}
	n := m.sessions[2]
	if n.Group != "app" || m.sel != 2 {
		t.Fatalf("fork group %q, sel %d: %v", n.Group, m.sel, names(m))
	}
	before := names(m)
	m.normalize()
	if got := names(m); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Errorf("normalize moved sessions: %v -> %v", before, got)
	}
}
