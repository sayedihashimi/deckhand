package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestLaunchExtras(t *testing.T) {
	// The start folder is in the repo: the agent runs in the worktree and
	// needs no extra folder.
	s := &Session{Dir: `C:\src\api-wt\quiet-otter\src`, CodeRepo: `C:\src\api\src`, RepoRoot: `C:\src\api`,
		Worktree: `C:\src\api-wt\quiet-otter`, Branch: "quiet-otter"}
	x := s.launchExtrasLocked()
	if x.addDir != "" || x.prompt != "" || s.separateRepoLocked() {
		t.Errorf("worktree start folder: %+v", x)
	}
	if !slices.Contains(x.env, `DECKHAND_WORKTREE=C:\src\api-wt\quiet-otter`) || !slices.Contains(x.env, "DECKHAND_BRANCH=quiet-otter") {
		t.Errorf("env: %q", x.env)
	}
	if s.gitDirLocked() != s.Worktree {
		t.Errorf("git dir %s", s.gitDirLocked())
	}

	// A notes folder with a separate code repo.
	s = &Session{Dir: `C:\notes`, CodeRepo: `C:\src\api`, RepoRoot: `C:\src\api`,
		Worktree: `C:\src\api-wt\quiet-otter`, Branch: "quiet-otter"}
	x = s.launchExtrasLocked()
	if !s.separateRepoLocked() || x.addDir != s.Worktree || !strings.Contains(x.prompt, s.Worktree) ||
		!strings.Contains(x.prompt, "quiet-otter") || strings.Contains(x.prompt, `"`) {
		t.Errorf("notes folder: %+v", x)
	}
	if s.gitDirLocked() != s.Worktree || s.repoNameLocked() != "api" {
		t.Errorf("git dir %s, repo %s", s.gitDirLocked(), s.repoNameLocked())
	}

	// A code repo without a worktree: the branch line uses it, and Claude
	// gets the repo itself.
	s = &Session{Dir: `C:\notes`, CodeRepo: `C:\src\api`, RepoRoot: `C:\src\api`}
	x = s.launchExtrasLocked()
	if x.addDir != `C:\src\api` || !strings.Contains(x.prompt, `C:\src\api`) || strings.Contains(x.prompt, `"`) ||
		strings.Contains(x.prompt, "worktree") || len(x.env) != 0 || s.gitDirLocked() != `C:\src\api` {
		t.Errorf("code repo only: %+v", x)
	}
	// The code repo holds the start folder: nothing to add.
	if x := (&Session{Dir: `C:\src\api\sub`, CodeRepo: `C:\src\api`, RepoRoot: `C:\src\api`}).launchExtrasLocked(); x.addDir != "" {
		t.Errorf("code repo holding the start folder: %+v", x)
	}
	if (&Session{Dir: `C:\src\api`}).gitDirLocked() != `C:\src\api` {
		t.Error("a plain session's git dir is its folder")
	}
}

func TestBuildLaunchWorktreeExtras(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	x := launchExtras{addDir: `C:\src\api-wt\quiet-otter`, prompt: "Work in the worktree.", env: []string{"DECKHAND_WORKTREE=w"}}
	spec, err := buildLaunch(Agent{Name: "claude", Exe: "git", Claude: true}, "s1", "c1", false, x)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(spec.args, "|")
	if !strings.HasPrefix(args, `--add-dir|C:\src\api-wt\quiet-otter|--append-system-prompt|Work in the worktree.|--session-id|c1`) {
		t.Errorf("args: %q", spec.args)
	}
	if !slices.Contains(spec.env, "DECKHAND_WORKTREE=w") {
		t.Errorf("env: %q", spec.env)
	}
	spec, _ = buildLaunch(Agent{Name: "claude", Exe: "git", Claude: true}, "s1", "c1", true, x)
	if args := strings.Join(spec.args, "|"); !strings.Contains(args, "--add-dir|") || !strings.Contains(args, "--append-system-prompt|") || !strings.Contains(args, "--resume|c1") {
		t.Errorf("resume args: %q", spec.args)
	}
	// Other CLIs get only the environment.
	spec, _ = buildLaunch(Agent{Name: "codex", Exe: "git"}, "s1", "", false, x)
	if len(spec.args) != 0 || !slices.Contains(spec.env, "DECKHAND_WORKTREE=w") {
		t.Errorf("other CLI: %q %q", spec.args, spec.env)
	}

	if pwshPath() == "" {
		return
	}
	// A wrapper agent: the claude shim adds them.
	spec, _ = buildLaunch(Agent{Name: "smc", Pwsh: "smc", Resume: "smc -Resume {id}", Claude: true}, "s1", "c1", true, x)
	env := strings.Join(spec.env, "\n")
	if !strings.Contains(env, `DECKHAND_CLAUDE_ADD_DIR=C:\src\api-wt\quiet-otter`) || !strings.Contains(env, "DECKHAND_CLAUDE_PROMPT=Work in the worktree.") {
		t.Errorf("shim env: %s", env)
	}
	for _, want := range []string{"DECKHAND_CLAUDE_ADD_DIR", "DECKHAND_CLAUDE_PROMPT", "'--add-dir'", "'--append-system-prompt'"} {
		if !strings.Contains(claudeShim, want) {
			t.Errorf("the shim should use %s", want)
		}
	}
	if strings.Index(claudeShim, "'--add-dir'") > strings.Index(claudeShim, "'--settings'") {
		t.Error("--add-dir takes several folders, so it must come before a flag with a value")
	}
}

func TestSnapshotWorktreeLabel(t *testing.T) {
	s := &Session{Name: "quiet-otter", Agent: "claude", Dir: `C:\notes`, CodeRepo: `C:\src\api`, RepoRoot: `C:\src\api`,
		Worktree: `C:\src\api-wt\quiet-otter`, Branch: "quiet-otter"}
	s.info.git = gitInfo{ok: true, branch: "quiet-otter", ahead: 2}
	sn := s.Snapshot()
	if !sn.worktree || sn.repoName != "api" {
		t.Fatalf("snapshot: %+v", sn)
	}
	if got := ansi.Strip(infoLine(s, sn, 26)); got != "⎇ api:quiet-otter↑2" {
		t.Errorf("info line %q", got)
	}
	// Narrow: the branch is cut before the repo name.
	if got := ansi.Strip(infoLine(s, sn, 12)); !strings.HasPrefix(got, "⎇ api:") || ansi.StringWidth(got) > 12 {
		t.Errorf("narrow info line %q", got)
	}
	// With Claude's model and context: the model goes before the repo name.
	s.info.model, s.info.ctxTokens = "claude-opus-5-5", 64100
	if got := ansi.Strip(infoLine(s, s.Snapshot(), 26)); got != "⎇ api:quiet-ot… · ▰▱▱▱▱ 6%" {
		t.Errorf("info line with a model %q", got)
	}
	if got := ansi.Strip(infoLine(s, s.Snapshot(), 46)); got != "⎇ api:quiet-otter↑2 · opus 5.5 · ▰▱▱▱▱ 6%" {
		t.Errorf("wide info line %q", got)
	}
	s.info.model, s.info.ctxTokens = "", 0
	// In the repo itself: no repo name.
	s.Dir = `C:\src\api-wt\quiet-otter`
	if got := ansi.Strip(infoLine(s, s.Snapshot(), 26)); got != "⎇ quiet-otter↑2" {
		t.Errorf("info line %q", got)
	}
}

// A fake git repo for the dialog, which only looks for .git.
func fakeGitDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDialogCodeRepo(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	root := t.TempDir()
	notes := filepath.Join(root, "notes")
	_ = os.MkdirAll(notes, 0o755)
	api := fakeGitDir(t, filepath.Join(root, "api"))
	p := prefs{}
	p.setCodeRepo(notes, api)
	_ = p.save()

	d := newNewDialog([]Agent{{Name: "claude"}}, 100, nil)
	d.setInput(api)
	if !d.folderIsGit() || d.showRepo() || d.gitRepo() != api || !slices.Contains(d.fields(), fieldWorktree) {
		t.Fatalf("git folder: fields %v", d.fields())
	}
	ctrlT := tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	if ctrlT.String() != "ctrl+t" {
		t.Fatalf("ctrl+t is %q", ctrlT.String())
	}
	d.update(ctrlT)
	if !d.useWorktree() {
		t.Error("ctrl+t should turn the worktree on")
	}

	// A notes folder: the repo row shows, filled in with the repo last used there.
	d.setInput(notes)
	d.update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if d.folderIsGit() || !d.showRepo() || d.repo.Value() != displayInput(api) || d.gitRepo() != api || !d.useWorktree() {
		t.Fatalf("notes folder: repo %q, git %q, fields %v", d.repo.Value(), d.gitRepo(), d.fields())
	}
	// ↓ past the suggestions goes to the repo row, then the worktree row.
	for range maxFolderSuggestions + 1 {
		d.update(tea.KeyPressMsg{Code: tea.KeyDown})
		if d.field != fieldFolder {
			break
		}
	}
	if d.field != fieldRepo {
		t.Fatalf("field %d, want the repo row", d.field)
	}
	for range maxFolderSuggestions + 1 {
		d.update(tea.KeyPressMsg{Code: tea.KeyDown})
		if d.field != fieldRepo {
			break
		}
	}
	if d.field != fieldWorktree {
		t.Fatalf("field %d, want the worktree row", d.field)
	}
	d.update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if d.useWorktree() {
		t.Error("space should turn the worktree off")
	}
	if d.target() != notes || d.repoTarget() != api {
		t.Errorf("target %s, repo %s", d.target(), d.repoTarget())
	}

	// Not a repo: no worktree line, and ctrl+t says why.
	d.setField(fieldRepo)
	d.setInput(notes)
	if d.gitRepo() != "" || slices.Contains(d.fields(), fieldWorktree) {
		t.Errorf("repo that isn't git: fields %v", d.fields())
	}
	d.update(ctrlT)
	if d.err == "" {
		t.Error("ctrl+t without a repo should explain")
	}
}

func TestDialogRendersWorktreeRows(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	root := t.TempDir()
	notes := filepath.Join(root, "notes")
	_ = os.MkdirAll(notes, 0o755)
	api := fakeGitDir(t, filepath.Join(root, "api"))
	m := testModel()
	m.width, m.height = 140, 40
	m.dialog = newNewDialog([]Agent{{Name: "claude"}}, 100, nil)
	m.dialog.setInput(notes)
	m.dialog.setField(fieldRepo)
	m.dialog.setInput(api)
	m.dialog.worktree = true
	screen := ansi.Strip(strings.Join(m.renderDialog(120), "\n"))
	for _, want := range []string{"Repo", "Worktree", "a new branch and folder for api", "Code repo " + displayPath(api), "Worktree  in " + displayPath(api) + "-wt"} {
		if !strings.Contains(screen, want) {
			t.Errorf("missing %q:\n%s", want, screen)
		}
	}
}

func TestWorktreeLikeSelectedNeedsGit(t *testing.T) {
	fakeStart(t)
	m := testModel("notes")
	m.sessions[0].Dir = t.TempDir()
	if cmd := m.worktreeLikeSelected(); cmd != nil || !strings.Contains(m.flash, "git repo") || len(m.sessions) != 1 {
		t.Errorf("W outside git: %q", m.flash)
	}
}

// runCmd runs a command and feeds its message back to the model, as Bubble
// Tea would, until there's nothing more to run.
func runCmd(m *model, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return
		}
		_, cmd = m.Update(msg)
	}
}

func TestWorktreeSessionLifecycle(t *testing.T) {
	clone := testRepo(t)
	fakeGH(t, noPR)
	started := fakeStart(t)
	worktreeSettings = worktreeConfig{}
	t.Cleanup(func() { worktreeSettings = worktreeConfig{} })

	// W on a session in a subfolder of the repo.
	m := testModel("api", "web")
	m.width, m.height = 120, 32
	m.sessions[0].Agent = "claude"
	m.sessions[0].Dir = filepath.Join(clone, "src", "app")
	_, cmd := m.handleKey(tea.KeyPressMsg{Code: 'W', Text: "W"})
	if len(m.sessions) != 3 || m.sel != 1 || cmd == nil {
		t.Fatalf("W should add a session below the selected one: %d sessions, sel %d", len(m.sessions), m.sel)
	}
	s := m.sessions[1]
	if !s.preparing.Load() || len(s.PrepLog()) == 0 {
		t.Error("the new session shows its worktree being made")
	}
	if err := m.saveSessions(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(storePath()); strings.Contains(string(data), s.ID) {
		t.Error("a session whose worktree doesn't exist yet shouldn't be saved")
	}
	m.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.confirm != nil || !strings.Contains(m.flash, "Still creating") {
		t.Errorf("x while the worktree is made: %q", m.flash)
	}
	// Bubble Tea runs this in the background.
	runCmd(m, cmd)
	m2 := m
	if len(*started) != 1 || (*started)[0] != s {
		t.Fatalf("the agent should start once the worktree is made (started %d)", len(*started))
	}
	r, _ := findRepo(clone)
	if s.RepoRoot != r.root || s.Branch == "" || s.Name != s.Branch || s.BaseBranch != "main" || s.BaseCommit == "" {
		t.Errorf("session: %+v", s)
	}
	if s.Worktree != filepath.Join(r.root+"-wt", s.Branch) || s.Dir != filepath.Join(s.Worktree, "src", "app") {
		t.Errorf("dir %s, worktree %s", s.Dir, s.Worktree)
	}
	if s.preparing.Load() || m2.focus != focusTerminal {
		t.Errorf("preparing %v, focus %v", s.preparing.Load(), m2.focus)
	}
	data, _ := os.ReadFile(storePath())
	for _, want := range []string{`"worktree"`, `"branch": "` + s.Branch, `"base_branch": "main"`, `"base_commit"`, `"repo_root"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("sessions.json is missing %s:\n%s", want, data)
		}
	}
	// Saved and loaded again, it's the same.
	loaded := loadSessions()
	if len(loaded) != 3 || loaded[1].Worktree != s.Worktree || loaded[1].BaseCommit != s.BaseCommit {
		t.Errorf("reloaded: %+v", loaded[1])
	}

	// x when it isn't merged: the warning, then k keeps the worktree.
	s.running = false
	commitFile(t, s.Worktree, "feature.txt", "x")
	m2.focus = focusSidebar
	m2.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m2.confirm == nil {
		t.Fatal("x should ask first")
	}
	_, cmd = m2.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(m2, cmd)
	if m2.closing == nil {
		t.Fatal("unmerged work should show the warning")
	}
	screen := ansi.Strip(strings.Join(m2.renderClose(100, 30), "\n"))
	for _, want := range []string{"isn't merged into main", "Commits not on main (1)", "Change feature.txt", "no PR", "delete anyway",
		"The 1 commit not on main will be lost: " + s.Branch + " isn't on origin (not pushed)."} {
		if !strings.Contains(screen, want) {
			t.Errorf("warning is missing %q:\n%s", want, screen)
		}
	}
	m2.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m2.closing != nil || len(m2.sessions) != 3 {
		t.Error("esc should cancel")
	}
	m2.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	_, cmd = m2.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(m2, cmd)
	m2.handleKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if len(m2.sessions) != 2 || !isDir(s.Worktree) || !refExists(r.root, "refs/heads/"+s.Branch) {
		t.Errorf("k should remove only the session (%d sessions)", len(m2.sessions))
	}

	// A merged one is removed without asking more.
	m2.sel = 0
	runCmd(m2, m2.worktreeLikeSelected())
	s2 := m2.sessions[1]
	s2.running = false
	commitFile(t, s2.Worktree, "done.txt", "x")
	git(t, s2.Worktree, "push", "-q", "origin", "HEAD:main")
	m2.focus = focusSidebar
	m2.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	_, cmd = m2.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(m2, cmd)
	if m2.closing != nil || slices.Contains(m2.sessions, s2) || isDir(s2.Worktree) || refExists(r.root, "refs/heads/"+s2.Branch) {
		t.Errorf("merged: should be removed (flash %q)", m2.flash)
	}
	if !isDir(clone) {
		t.Fatal("the repo itself must never be touched")
	}

	// D deletes anyway, uncommitted files and all.
	m2.sel = 0
	runCmd(m2, m2.worktreeLikeSelected())
	s3 := m2.sessions[1]
	s3.running = false
	_ = os.WriteFile(filepath.Join(s3.Worktree, "wip.txt"), []byte("x"), 0o644)
	m2.focus = focusSidebar
	m2.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	_, cmd = m2.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(m2, cmd)
	if m2.closing == nil || !strings.Contains(ansi.Strip(strings.Join(m2.renderClose(100, 30), "\n")), "wip.txt") {
		t.Fatal("dirty: the warning should list the file")
	}
	_, cmd = m2.handleKey(tea.KeyPressMsg{Code: 'D', Text: "D"})
	runCmd(m2, cmd)
	if slices.Contains(m2.sessions, s3) || isDir(s3.Worktree) {
		t.Errorf("D should delete it (flash %q)", m2.flash)
	}
}

func TestWorktreeSetupFailure(t *testing.T) {
	clone := testRepo(t)
	started := fakeStart(t)
	worktreeSettings = worktreeConfig{Setup: "exit 3"}
	t.Cleanup(func() { worktreeSettings = worktreeConfig{} })
	m := testModel("api")
	m.width, m.height = 120, 32
	m.sessions[0].Agent = "claude"
	m.sessions[0].Dir = clone
	runCmd(m, m.worktreeLikeSelected())
	s := m.sessions[1]
	if len(*started) != 0 || s.preparing.Load() || s.Worktree == "" {
		t.Fatalf("a failed setup shouldn't start the agent (started %d)", len(*started))
	}
	log := strings.Join(s.PrepLog(), "\n")
	if !strings.Contains(log, "Setup failed") || !strings.Contains(log, "Press enter to start the agent anyway") {
		t.Errorf("log:\n%s", log)
	}
	if sn := s.Snapshot(); sn.status != StatusExited || !strings.Contains(sn.detail, "setup failed") {
		t.Errorf("status %v %q", sn.status, sn.detail)
	}
	if !strings.Contains(m.flash, "Setup failed") {
		t.Errorf("flash %q", m.flash)
	}
}

// A code repo without a worktree reaches Claude too: --add-dir and the
// prompt, on resume as well, and through the shim for wrapper agents.
func TestBuildLaunchCodeRepoOnly(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "api")
	s := &Session{Dir: filepath.Join(t.TempDir(), "notes"), CodeRepo: repo, RepoRoot: repo}
	x := s.launchExtrasLocked()
	for _, resume := range []bool{false, true} {
		spec, err := buildLaunch(Agent{Name: "claude", Exe: "git", Claude: true}, "s1", "c1", resume, x)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.Index(spec.args, "--add-dir")
		j := slices.Index(spec.args, "--append-system-prompt")
		if i != 0 || spec.args[1] != repo || j != 2 || !strings.Contains(spec.args[3], repo) {
			t.Errorf("resume=%v: args %q", resume, spec.args)
		}
	}
	if pwshPath() == "" {
		return
	}
	for _, resume := range []bool{false, true} {
		spec, _ := buildLaunch(Agent{Name: "smc", Pwsh: "smc", Resume: "smc -Resume {id}", Claude: true}, "s1", "c1", resume, x)
		env := strings.Join(spec.env, "\n")
		if !strings.Contains(env, "DECKHAND_CLAUDE_ADD_DIR="+repo) || !strings.Contains(env, "DECKHAND_CLAUDE_PROMPT=This session works on the git repository at "+repo) {
			t.Errorf("resume=%v: shim env %s", resume, env)
		}
	}
}

func TestDeleteWarning(t *testing.T) {
	text := func(ck closeCheck) string {
		return ansi.Strip(strings.Join(deleteWarning(ck, "quiet-otter", "main"), " "))
	}
	two := []string{"a1 One", "b2 Two"}
	if got := text(closeCheck{commits: two, ahead: 2}); got != "The 2 commits not on main will be lost: quiet-otter isn't on origin (not pushed)." {
		t.Errorf("not pushed: %q", got)
	}
	if got := text(closeCheck{commits: two[:1], ahead: 1, onOrigin: true, pushed: true}); got != "The 1 commit not on main is kept on origin/quiet-otter, which isn't deleted." {
		t.Errorf("pushed: %q", got)
	}
	if got := text(closeCheck{commits: two, ahead: 25, onOrigin: true}); !strings.Contains(got, "will be lost") || !strings.Contains(got, "25 commits") {
		t.Errorf("pushed earlier: %q", got)
	}
	if got := text(closeCheck{merged: true, onOrigin: true}); !strings.Contains(got, "kept") || strings.Contains(got, "lost") {
		t.Errorf("merged: %q", got)
	}
}
