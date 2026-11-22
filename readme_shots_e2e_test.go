//go:build e2e

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sayedihashimi/deckhand/internal/termshot"
)

// TestE2EReadmeShots renders the README's screenshots: one scripted scene
// at 140×36, with fake Claude sessions (testdata/hookapp) and a fake gh,
// photographed in a fixed order, one PNG per shot in DECKHAND_SCREENSHOTS.
//
//	$env:DECKHAND_SCREENSHOTS="docs/img"; go test -tags e2e -run TestE2EReadmeShots -v -timeout 20m
//
// Rerunning it changes an image only when the UI changes: the scene's
// folders are on a substituted drive (subst), so no temporary path shows,
// commits have fixed dates, the session ages in the list are pinned
// (readmeAges), and spinners and the blinking "needs you" icon are drawn
// in one frame (steady).
func TestE2EReadmeShots(t *testing.T) {
	outDir := os.Getenv("DECKHAND_SCREENSHOTS")
	if outDir == "" {
		t.Skip("set DECKHAND_SCREENSHOTS to the folder to write the PNGs to")
	}
	outDir, _ = filepath.Abs(outDir)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	root := substDrive(t, t.TempDir()) // such as S:\
	// The fake claude's path is its console title, which the session
	// header can show.
	bin := filepath.Join(root, "bin")
	for _, app := range []string{"hookapp:claude.exe", "ghapp:gh.exe"} {
		src, name, _ := strings.Cut(app, ":")
		if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, name), "./testdata/"+src).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", src, err, out)
		}
	}

	src := filepath.Join(root, "src")
	origins := filepath.Join(root, "origins")
	home := filepath.Join(root, "deckhand")
	ghState := filepath.Join(home, "gh")
	for _, d := range []string{src, origins, ghState} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	g := shotGit{t: t, exe: gitExe}

	// api: needs you (a permission prompt), on feature/login with PR #7
	// (checks failing), and work for the diff preview.
	api := filepath.Join(src, "api")
	g.init(api, "main")
	shotScript(t, api, `event SessionStart
print \e]0;✳ Login redirect loop\a
reply 84200 I'll run the tests to confirm the fix.
statusline 2.37 200000 84200
say \e[1m> fix the login redirect loop\e[0m
say
say \e[32m●\e[0m Read src/auth/login.ts
say \e[32m●\e[0m The redirect loop comes from the session check running before the
say   cookie is set. I moved it after the token refresh.
say \e[32m●\e[0m Update src/auth/login.ts (+6 -3)
say
say \e[33m?\e[0m Run \e[1mnpm test -- --watch=false\e[0m?
say   \e[36m❯ 1. Yes\e[0m
say     2. No
event UserPromptSubmit
event PreToolUse Read | src/auth/login.ts
event PostToolUse Read
event PermissionRequest Bash | npm test -- --watch=false
`)
	g.apiWork(api)
	onGitHub(t, gitExe, api, "api")
	writePR(t, ghState, "feature/login", map[string]any{"number": 7, "state": "OPEN", "url": "https://github.com/example/api/pull/7",
		"statusCheckRollup": []map[string]string{{"__typename": "CheckRun", "status": "COMPLETED", "conclusion": "FAILURE"}}})

	// docs: finished its turn. Its fork (f) waits for its first prompt.
	docs := filepath.Join(src, "docs")
	g.init(docs, "main")
	g.commit(docs, "README.md", "# docs\n", "Start the docs")
	shotScript(t, docs, `sleep 1500
event SessionStart
print \e]0;✳ Install steps\a
reply 31000 Updated the README with the new install steps.
statusline 0.42 200000 31000
say \e[1m> document the install steps\e[0m
say
say \e[32m●\e[0m Update README.md (+18 -2)
say \e[32m●\e[0m Updated the README with the new install steps.
event UserPromptSubmit
event PreToolUse Edit | README.md
event PostToolUse Edit
event Stop
`)
	// The fork's script clears hookapp's "forked ... as <id>" line.
	_ = os.WriteFile(filepath.Join(docs, "hookapp-fork.txt"), []byte(`print \e[2J\e[H
event SessionStart
print \e]0;✳ Install steps\a
say \e[1m> document the install steps\e[0m
say
say \e[32m●\e[0m Update README.md (+18 -2)
say \e[32m●\e[0m Updated the README with the new install steps.
say
say \e[2m(forked: try another approach from here)\e[0m
`), 0o644)

	// web: working, on fix/win-tests, which has a commit and no PR yet
	// (p asks before pushing). It keeps setting its window title, the same
	// one apart from a trailing space (which deckhand trims), so deckhand
	// sees output and doesn't decide the turn was interrupted.
	web := filepath.Join(src, "web")
	g.withOrigin(web, origins, "web")
	g.run(web, "switch", "-q", "-c", "fix/win-tests")
	g.commit(web, "paths_test.go", "package web\n\n// Skip the symlink tests on Windows.\n", "Skip the symlink tests on Windows")
	_ = os.WriteFile(filepath.Join(web, "changed.txt"), []byte("x"), 0o644)
	webScript := `event SessionStart
print \e]0;✳ Windows tests\a
reply 152300 Running the test suite.
statusline 3.10 200000 152300
say \e[1m> make the tests pass on Windows\e[0m
say
say \e[32m●\e[0m Update paths_test.go (+3 -0)
say \e[32m●\e[0m Bash(go test ./...)
say   \e[2mRunning…\e[0m
event UserPromptSubmit
event PreToolUse Bash | go test ./...
`
	for i := 0; i < 120; i++ {
		webScript += "sleep 2000\nprint \\e]0;✳ Windows tests \\a\nsleep 2000\nprint \\e]0;✳ Windows tests\\a\n"
	}
	shotScript(t, web, webScript)

	// quiet-otter: a worktree session of cli, run from a notes folder
	// (plans), finished, with PR #12 (checks passing, ready to merge).
	cli := filepath.Join(src, "cli")
	g.withOrigin(cli, origins, "cli")
	g.commit(cli, "upload.go", "package cli\n", "Add the upload client")
	g.run(cli, "push", "-q", "origin", "main")
	wt := filepath.Join(src, "cli-wt", "quiet-otter")
	g.run(cli, "worktree", "add", "-q", "-b", "quiet-otter", wt, "main")
	g.commit(wt, "upload.go", "package cli\n\n// Retry failed uploads.\n", "Retry failed uploads")
	g.run(wt, "push", "-q", "origin", "quiet-otter")
	tip := strings.TrimSpace(g.run(wt, "rev-parse", "HEAD"))
	writePR(t, ghState, "quiet-otter", map[string]any{"number": 12, "state": "OPEN", "url": "https://github.com/example/cli/pull/12",
		"isDraft": false, "headRefOid": tip, "title": "Retry failed uploads", "baseRefName": "main",
		"mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN",
		"statusCheckRollup": []map[string]string{{"__typename": "CheckRun", "status": "COMPLETED", "conclusion": "SUCCESS"}}})
	plans := filepath.Join(src, "plans")
	_ = os.MkdirAll(plans, 0o755)
	shotScript(t, plans, `sleep 800
event SessionStart
print \e]0;✳ Upload retries\a
reply 64100 Added retries to the upload client, committed on quiet-otter.
statusline 1.25 200000 64100
say \e[1m> add retries to the upload client\e[0m
say
say \e[32m●\e[0m Update upload.go (+24 -3)
say \e[32m●\e[0m Added retries to the upload client, committed on quiet-otter.
event UserPromptSubmit
event PreToolUse Edit | upload.go
event PostToolUse Edit
event Stop
`)
	notes := filepath.Join(src, "notes")
	_ = os.MkdirAll(notes, 0o755)

	type saved struct {
		ID         string    `json:"id"`
		Name       string    `json:"name"`
		Agent      string    `json:"agent"`
		Dir        string    `json:"dir"`
		Created    time.Time `json:"created"`
		Claude     string    `json:"claude_session_id,omitempty"`
		Started    bool      `json:"started,omitempty"`
		CodeRepo   string    `json:"code_repo,omitempty"`
		RepoRoot   string    `json:"repo_root,omitempty"`
		Worktree   string    `json:"worktree,omitempty"`
		Branch     string    `json:"branch,omitempty"`
		BaseBranch string    `json:"base_branch,omitempty"`
		Group      string    `json:"group,omitempty"`
	}
	now := time.Now()
	list := []saved{
		{ID: "shot-api", Name: "api", Agent: "claude", Dir: api, Created: now, Claude: "11111111-1111-1111-1111-111111111111", Started: true, Group: "app"},
		{ID: "shot-docs", Name: "docs", Agent: "claude", Dir: docs, Created: now, Claude: "22222222-2222-2222-2222-222222222222", Started: true, Group: "app"},
		{ID: "shot-web", Name: "web", Agent: "claude", Dir: web, Created: now, Claude: "33333333-3333-3333-3333-333333333333", Started: true, Group: "app"},
		{ID: "shot-worktree", Name: "quiet-otter", Agent: "claude", Dir: plans, Created: now, Claude: "44444444-4444-4444-4444-444444444444", Started: true,
			CodeRepo: cli, RepoRoot: cli, Worktree: wt, Branch: "quiet-otter", BaseBranch: "main", Group: "cli"},
		{ID: "shot-notes", Name: "notes", Agent: "pwsh", Dir: notes, Created: now, Started: true},
	}
	data, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "groups.json"), []byte(`{"groups":[{"name":"app"},{"name":"cli"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The exited shell's output from its last run, restored dimmed.
	prompt := "\x1b[32mPS\x1b[0m " + notes + "> "
	notesScreen := savedScreen{width: 102, height: 32, saved: now.Add(-2 * time.Hour), lines: []string{
		prompt + "\x1b[93mgit\x1b[0m log --oneline -4",
		"\x1b[33m4f2a9c1\x1b[0m Add notes from the planning review",
		"\x1b[33m9b7e3d0\x1b[0m Outline the release checklist",
		"\x1b[33m1c8d5e2\x1b[0m Move meeting notes into one folder",
		"\x1b[33m7a6f0b4\x1b[0m Start the notes repo",
		prompt + "\x1b[93mGet-ChildItem\x1b[0m \x1b[90m-Name\x1b[0m",
		"checklist.md",
		"planning-review.md",
		"release.md",
		prompt + "\x1b[93mSelect-String\x1b[0m \x1b[90m-Path\x1b[0m *.md \x1b[90m-Pattern\x1b[0m \x1b[36m'TODO'\x1b[0m",
		"",
		"\x1b[35mchecklist.md\x1b[0m:\x1b[32m12\x1b[0m:- [ ] \x1b[7mTODO\x1b[0m: confirm the release date",
		"\x1b[35mplanning-review.md\x1b[0m:\x1b[32m31\x1b[0m:\x1b[7mTODO\x1b[0m: share the notes with the team",
		"",
		prompt + "\x1b[93mexit\x1b[0m",
	}}
	if err := writeScreenFile(filepath.Join(home, "sessions", "shot-notes", screenFile), notesScreen); err != nil {
		t.Fatal(err)
	}

	// Recent folders, and cli as the code repo last used with plans, for
	// the new-session dialog.
	prefs, _ := json.Marshal(map[string]any{"recent_dirs": []string{plans, api, web, docs, notes}, "last_agent": "claude",
		"code_repos": map[string]string{strings.ToLower(plans): cli}})
	if err := os.WriteFile(filepath.Join(home, "prefs.json"), prefs, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(home, "config.toml")
	_ = os.WriteFile(cfg, []byte(`[[agents]]
name   = "review"
pwsh   = "Start-Review"
type   = "claude"
resume = "Start-Review -Resume {id}"

[[agents]]
name    = "claude-opus"
command = "claude"
args    = ["--model", "opus"]
type    = "claude"

[open]
editor = 'code "{dir}"'
`), 0o644)

	path := bin + ";" + filepath.Dir(pwsh) + ";" + filepath.Dir(gitExe) + ";" + os.Getenv("SystemRoot") + `\System32`
	env := withGitUsrBin(envWith(home, path))
	env = append(env, "DECKHAND_NOTIFY_LOG="+filepath.Join(home, "notify.log"), "DECKHAND_CONFIG="+cfg, "GHAPP_DIR="+ghState)
	h := startHarnessSize(t, exe, env, src, 140, 36)

	shot := func(name string) {
		t.Helper()
		time.Sleep(400 * time.Millisecond)
		scr := steady(readmeAges.pin(screenFromEmulator(h.emu)))
		p := filepath.Join(outDir, name+".png")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := termshot.Render(f, scr, termshot.Options{Palette: true}); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s:\n%s", p, h.screen())
	}
	down := func(n int) { h.send(strings.Repeat("\x1b[B", n)) }
	up := func(n int) { h.send(strings.Repeat("\x1b[A", n)) }

	h.waitFor("notes", 10*time.Second)
	// Start the four Claude sessions (resuming them), coming back to the
	// list after each; api, started last, stays selected.
	for _, k := range []string{"4", "3", "2", "1"} {
		h.send(k)
		time.Sleep(700 * time.Millisecond)
		h.send(keyF5)
	}
	h.waitFor("claude · Bash: npm test", 15*time.Second)
	h.waitFor("claude · Updated the", 15*time.Second)
	h.waitFor("claude · Added retries", 15*time.Second)
	h.waitFor("⎇ cli:quiet-otter↑1 #12✓", 15*time.Second)
	h.waitFor("#7✗", 10*time.Second)
	h.waitFor("$3.10", 10*time.Second)
	h.waitFor("$7.14", 10*time.Second)  // today's total
	time.Sleep(3500 * time.Millisecond) // every session's git details

	// The main view: api's permission prompt on the right.
	shot("main")

	// F3: the list hidden.
	h.send("\x1bOR")
	h.waitFor("F3 list", 5*time.Second)
	shot("hidden")
	h.send("\x1bOR")
	h.waitFor("▾ app", 5*time.Second)

	// ?: the help.
	h.send("?")
	h.waitFor("New-session dialog", 5*time.Second)
	shot("help")
	h.send("\x1b")
	h.waitGone("New-session dialog", 3*time.Second)

	// d: api's diff preview.
	h.send("d")
	h.waitFor("files changed", 10*time.Second)
	shot("diff")
	h.send("\x1b")
	h.waitGone("files changed", 3*time.Second)

	// The exited shell's restored output: past docs, web, the cli header,
	// quiet-otter and the (no group) header.
	down(6)
	h.waitFor("last output · saved 2h ago", 5*time.Second)
	h.waitFor("planning-review.md", 3*time.Second)
	shot("restored")
	up(6)
	h.waitGone("last output", 3*time.Second)

	// n on quiet-otter: the new-session dialog, for the plans notes folder
	// (typed), with the cli repo it used last time and a worktree (ctrl+t).
	down(4)
	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.send("plans")
	h.waitFor("Start in  "+plans, 5*time.Second)
	h.waitFor("Worktree", 5*time.Second)
	h.send("\x14") // ctrl+t
	h.waitFor("a new branch and folder for", 3*time.Second)
	shot("new-session")
	h.send("\x1b")
	h.waitGone("Start in", 3*time.Second)

	// p on web: the question before pushing and opening a PR.
	up(2)
	h.send("p")
	h.waitFor(`Push fix/win-tests to origin and open a PR into main, "Skip the symlink tests on Windows"`, 15*time.Second)
	shot("pr")
	h.send("n")
	h.waitGone("Push fix/win-tests", 3*time.Second)

	// m on quiet-otter: squash-merge its PR on GitHub.
	down(2)
	h.send("m")
	h.waitFor(`Squash-merge PR #12 "Retry failed uploads" into main on GitHub`, 15*time.Second)
	shot("merge")
	h.send("n")
	h.waitGone("Squash-merge", 3*time.Second)
	h.waitGone("Checking whether", 15*time.Second) // the message from before the question

	// c: the config screen, testing the second agent (t), in the selected
	// session's folder.
	h.send("c")
	h.waitFor("+ Add agent", 5*time.Second)
	h.send("\x1b[B")
	h.waitFor("› claude-opus", 3*time.Second)
	h.send("t")
	h.waitFor("test · claude-opus", 5*time.Second)
	h.waitFor("✓ hooks work (SessionStart received)", 15*time.Second)
	shot("config")
	h.send("\x14x") // ctrl+t, then discard it
	h.waitFor("Discarded the test session", 5*time.Second)
	h.send("\x1b")
	h.waitGone("+ Add agent", 3*time.Second)

	// f on docs: the fork, selected and started, below it.
	h.send("2") // into docs and back: docs selected
	time.Sleep(500 * time.Millisecond)
	h.send(keyF5)
	h.send("f")
	h.waitFor("forked: try another approach", 15*time.Second)
	h.waitFor("claude · ⑂ docs", 10*time.Second)
	shot("fork")
	h.send(keyF5)

	// /: the list filtered.
	h.send("/")
	h.send("doc")
	h.waitGone("quiet-otter", 3*time.Second)
	shot("filter")
	h.send("\x1b")
	h.waitFor("quiet-otter", 3*time.Second)

	// The cli group folded, and the notes shell being dragged into the app
	// group: the drop line is indented under app. esc cancels the drag.
	h.send("5") // into quiet-otter and back, then ← to its group's header
	time.Sleep(500 * time.Millisecond)
	h.send(keyF5)
	h.send("\x1b[D")
	h.send(" ")
	h.waitFor("▸ cli", 3*time.Second)
	// 2 app, 3-5 api, 6-8 docs, 9-11 docs (fork), 12-14 web, 16 cli,
	// 18 (no group), 19-21 notes (0-based lines; SGR rows are 1-based)
	h.send("\x1b[<0;6;20M")  // press on notes
	h.send("\x1b[<32;6;15M") // over web
	h.send("\x1b[<32;6;9M")  // docs's last line: below it
	h.waitFor("━━━━━━", 3*time.Second)
	shot("groups")
	h.send("\x1b")
	h.send("\x1b[<0;6;9m")
	h.waitGone("━━━━━━", 3*time.Second)

	h.send("q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

// readmeAges are the ages the README's shots show, by session name.
var readmeAges = ageMap{"api": "12s", "docs": "3m", "docs (fork)": "4s", "web": "1m", "quiet-otter": "8m", "test · claude-opus": "2s"}

type ageMap map[string]string

// sessionRow is a list row's first line: number, icon, name and age.
var sessionRow = regexp.MustCompile(`^▌?([1-9 ]) (\S) (.+?) +(\d+[smhd]) $`)

// pin sets each session's age in the list, which otherwise counts
// the seconds the scene took.
func (a ageMap) pin(scr termshot.Screen) termshot.Screen {
	for _, row := range scr {
		// The list ends at the first │.
		var runes []rune
		var cols []int
		for x, c := range row {
			if c.Width == 0 && c.Text == "" {
				continue
			}
			r := []rune(c.Text + " ")[0]
			if r == '│' {
				break
			}
			runes = append(runes, r)
			cols = append(cols, x)
		}
		m := sessionRow.FindStringSubmatchIndex(string(runes))
		if m == nil {
			continue
		}
		text := string(runes)
		name := strings.TrimSuffix(text[m[6]:m[7]], "…")
		age, ok := a[name]
		if !ok {
			continue
		}
		// The age ends one cell before the end of the list.
		end := len(runes) - 1
		start := end - len([]rune(text[m[8]:m[9]]))
		newStart := end - len(age)
		if newStart <= utf8.RuneCountInString(text[:m[7]]) {
			continue // no room after the name
		}
		style := row[cols[start]]
		for i := min(start, newStart); i < end; i++ {
			row[cols[i]].Text = " "
		}
		for i, r := range age {
			c := style
			c.Text = string(r)
			c.Width = 1
			row[cols[newStart+i]] = c
		}
	}
	return scr
}

// steady draws every spinner in one frame and the blinking "needs you"
// icon in its ● shape.
func steady(scr termshot.Screen) termshot.Screen {
	for _, row := range scr {
		for x, c := range row {
			switch {
			case slices.Contains(spinner, c.Text):
				row[x].Text = spinner[2]
			case c.Text == "◉":
				row[x].Text = "●"
			}
		}
	}
	return scr
}

// shotScript writes a hookapp script into dir, untracked and hidden from
// git status.
func shotScript(t *testing.T, dir, script string) {
	t.Helper()
	if isDir(filepath.Join(dir, ".git")) {
		_ = os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("hookapp*.txt\n"), 0o644)
	}
	if err := os.WriteFile(filepath.Join(dir, "hookapp.txt"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writePR is the fake gh's pull request for branch.
func writePR(t *testing.T, ghState, branch string, pr map[string]any) {
	t.Helper()
	data, _ := json.Marshal(pr)
	if err := os.WriteFile(filepath.Join(ghState, "pr-"+strings.ReplaceAll(branch, "/", "_")+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// shotGit runs git for the scene, with a fixed author and fixed dates so
// commit IDs are the same on every run.
type shotGit struct {
	t   *testing.T
	exe string
}

func (g shotGit) run(dir string, args ...string) string {
	g.t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=Dev", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command(g.exe, full...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-11-02T10:00:00Z", "GIT_COMMITTER_DATE=2026-11-02T10:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (g shotGit) init(dir, branch string) {
	g.t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		g.t.Fatal(err)
	}
	g.run(dir, "init", "-q", "-b", branch)
}

func (g shotGit) commit(dir, name, text, msg string) {
	g.t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		g.t.Fatal(err)
	}
	g.run(dir, "add", name)
	g.run(dir, "commit", "-q", "-m", msg)
}

// withOrigin makes dir a repo on main whose origin looks like
// github.com/example/<name> but is a bare repo under origins (insteadOf),
// with one commit pushed and origin/HEAD set.
func (g shotGit) withOrigin(dir, origins, name string) {
	g.t.Helper()
	bare := filepath.Join(origins, name+".git")
	g.init(bare, "main")
	g.run(bare, "config", "core.bare", "true")
	g.init(dir, "main")
	u := "https://github.com/example/" + name + ".git"
	g.run(dir, "remote", "add", "origin", u)
	g.run(dir, "config", "url."+filepath.ToSlash(bare)+".insteadOf", u)
	g.commit(dir, "README.md", "# "+name+"\n", "Start "+name)
	g.run(dir, "push", "-q", "-u", "origin", "main")
	g.run(dir, "remote", "set-head", "origin", "main")
}

// apiWork is the api repo's history for the diff preview: login.ts on
// main, the fix committed on feature/login, and a test not committed yet.
func (g shotGit) apiWork(dir string) {
	g.t.Helper()
	g.commit(dir, "src/auth/login.ts", `import { checkSession, refreshToken } from "./session";

export async function login(req: Request): Promise<Response> {
  if (!checkSession(req)) {
    return redirect("/login");
  }
  const token = await refreshToken(req);
  return withCookie(redirect("/"), token);
}
`, "Add login")
	g.commit(dir, "package.json", "{\n  \"name\": \"api\",\n  \"scripts\": { \"test\": \"vitest\" }\n}\n", "Add package.json")
	g.run(dir, "switch", "-q", "-c", "feature/login")
	g.commit(dir, "src/auth/login.ts", `import { checkSession, refreshToken } from "./session";

export async function login(req: Request): Promise<Response> {
  // Refresh first: the session check needs the new cookie.
  const token = await refreshToken(req);
  if (!checkSession(req, token)) {
    return redirect("/login?expired=1");
  }
  return withCookie(redirect("/"), token);
}
`, "Check the session after the token refresh")
	p := filepath.Join(dir, "src", "auth", "login.test.ts")
	_ = os.WriteFile(p, []byte(`import { login } from "./login";

test("doesn't loop after the token refresh", async () => {
  const res = await login(fakeRequest({ expired: true }));
  expect(res.headers.get("location")).toBe("/");
});
`), 0o644)
}

// substDrive maps a free drive letter to dir with subst for the rest of
// the test and returns its root (such as S:\), so the scene's folders show
// as S:\src\api rather than a long temporary path with a user name in it.
func substDrive(t *testing.T, dir string) string {
	t.Helper()
	for _, l := range "SRQPOTUVWXYZ" {
		drive := fmt.Sprintf("%c:", l)
		if _, err := os.Stat(drive + `\`); err == nil {
			continue // in use
		}
		if out, err := exec.Command("subst", drive, dir).CombinedOutput(); err != nil {
			t.Logf("subst %s: %v %s", drive, err, out)
			continue
		}
		t.Cleanup(func() { _ = exec.Command("subst", drive, "/d").Run() })
		return drive + `\`
	}
	t.Fatal("no free drive letter for subst")
	return ""
}
