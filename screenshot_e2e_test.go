//go:build e2e

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/sayedihashimi/deckhand/internal/termshot"
)

// TestE2EScreenshot sets up a scripted scene (four fake Claude sessions in
// different states, in git repos (one in a worktree, run from a notes
// folder; two with a pull request, from a fake gh), plus an exited shell)
// and saves deckhand's
// screen as a PNG, with the exited shell's restored output, the config
// screen ("c", testing an agent with t), the help opened from it (F1) and the api session's diff
// preview ("d") below it, then a session being dragged into a group (the
// sessions are in two groups). The PR
// workflow posts it on each pull request.
//
//	$env:DECKHAND_SCREENSHOT="shot.png"; go test -tags e2e -run TestE2EScreenshot -v
//
// DECKHAND_SCREENSHOT_HELP also saves the help overlay to that PNG, and
// DECKHAND_SCREENSHOT_HIDDEN the scene with the session list hidden.
func TestE2EScreenshot(t *testing.T) {
	out := os.Getenv("DECKHAND_SCREENSHOT")
	if out == "" {
		t.Skip("set DECKHAND_SCREENSHOT to the PNG to write")
	}
	out, _ = filepath.Abs(out)
	exe := buildDeckhand(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not found")
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "claude.exe"), "./testdata/hookapp").CombinedOutput(); err != nil {
		t.Fatalf("build hookapp: %v\n%s", err, out)
	}
	// A fake gh with two pull requests, for the PR badges.
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "gh.exe"), "./testdata/ghapp").CombinedOutput(); err != nil {
		t.Fatalf("build ghapp: %v\n%s", err, out)
	}
	ghState := t.TempDir()
	for name, pr := range map[string]string{
		"pr-quiet-otter.json":   `{"number":12,"state":"OPEN","url":"https://github.com/example/cli/pull/12","statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}`,
		"pr-feature_login.json": `{"number":7,"state":"OPEN","url":"https://github.com/example/api/pull/7","statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE"}]}`,
	} {
		_ = os.WriteFile(filepath.Join(ghState, name), []byte(pr), 0o644)
	}

	src := filepath.Join(t.TempDir(), "src")
	repo := func(name, branch string, dirty bool, script string) string {
		dir := filepath.Join(src, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(gitExe, "-C", dir, "init", "-q", "-b", branch).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		// hookapp.txt itself is untracked, so hide it from git status.
		_ = os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("hookapp.txt\n"), 0o644)
		if dirty {
			_ = os.WriteFile(filepath.Join(dir, "changed.txt"), []byte("x"), 0o644)
		}
		_ = os.WriteFile(filepath.Join(dir, "hookapp.txt"), []byte(script), 0o644)
		return dir
	}

	api := repo("api", "feature/login", true, `event SessionStart
reply 84200 I'll run the tests to confirm the fix.
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
	apiWork(t, gitExe, api)
	onGitHub(t, gitExe, api, "api")
	docs := repo("docs", "main", false, `sleep 1500
event SessionStart
reply 31000 Updated the README with the new install steps.
say \e[1m> document the install steps\e[0m
event UserPromptSubmit
event PreToolUse Edit | README.md
event PostToolUse Edit
event Stop
`)
	web := repo("web", "main", true, `event SessionStart
reply 152300 Running the test suite.
say \e[1m> make the tests pass on Windows\e[0m
event UserPromptSubmit
event PreToolUse Bash | go test ./...
`)
	notes := filepath.Join(src, "notes")
	_ = os.MkdirAll(notes, 0o755)
	plans, cli, wt := worktreeScene(t, gitExe, src)
	onGitHub(t, gitExe, cli, "cli")

	home := t.TempDir()
	type saved struct {
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Agent   string    `json:"agent"`
		Dir     string    `json:"dir"`
		Created time.Time `json:"created"`
		Claude  string    `json:"claude_session_id,omitempty"`
		Started bool      `json:"started,omitempty"`

		CodeRepo   string `json:"code_repo,omitempty"`
		RepoRoot   string `json:"repo_root,omitempty"`
		Worktree   string `json:"worktree,omitempty"`
		Branch     string `json:"branch,omitempty"`
		BaseBranch string `json:"base_branch,omitempty"`

		Group string `json:"group,omitempty"`
	}
	now := time.Now()
	list := []saved{
		{ID: "shot-api", Name: "api", Agent: "claude", Dir: api, Created: now, Claude: "11111111-1111-1111-1111-111111111111", Started: true, Group: "app"},
		{ID: "shot-docs", Name: "docs", Agent: "claude", Dir: docs, Created: now, Claude: "22222222-2222-2222-2222-222222222222", Started: true, Group: "app"},
		{ID: "shot-web", Name: "web", Agent: "claude", Dir: web, Created: now, Claude: "33333333-3333-3333-3333-333333333333", Started: true, Group: "app"},
		// A worktree session run from a notes folder that isn't git, on
		// the cli repo: the sidebar shows the marker and the repo's name.
		{ID: "shot-worktree", Name: "quiet-otter", Agent: "claude", Dir: plans, Created: now, Claude: "44444444-4444-4444-4444-444444444444", Started: true,
			CodeRepo: cli, RepoRoot: cli, Worktree: wt, Branch: "quiet-otter", BaseBranch: "main", Group: "cli"},
		{ID: "shot-notes", Name: "notes", Agent: "pwsh", Dir: notes, Created: now, Started: true},
	}
	data, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Two groups; the shell is in none.
	if err := os.WriteFile(filepath.Join(home, "groups.json"), []byte(`{"groups":[{"name":"app"},{"name":"cli"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The exited shell's output from its last run, as deckhand saved it
	// when it quit: shown dimmed after the restart.
	prompt := "\x1b[32mPS\x1b[0m C:\\src\\notes> " // short, unlike the temporary folder
	notesScreen := savedScreen{width: 87, height: 30, saved: now.Add(-2 * time.Hour), lines: []string{
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

	// Two custom agents and an editor, for the config screen.
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
	env := append(envWith(home, path), "DECKHAND_NOTIFY_LOG="+filepath.Join(home, "notify.log"), "DECKHAND_CONFIG="+cfg, "GHAPP_DIR="+ghState)
	h := startHarnessSize(t, exe, env, src, 120, 32)

	h.waitFor("notes", 10*time.Second)
	// Start the four Claude sessions (resuming them), coming back to the
	// list after each.
	for _, k := range []string{"4", "3", "2", "1"} {
		h.send(k)
		time.Sleep(700 * time.Millisecond)
		h.send(keyF5)
	}
	h.waitFor("claude · Bash: npm test", 15*time.Second) // needs you: the command
	h.waitFor("claude · Updated the", 15*time.Second)    // finished: the reply
	h.waitFor("opus 5.5", 10*time.Second)
	h.waitFor("⎇ cli:quiet-otter", 10*time.Second)
	h.waitFor("#12✓", 10*time.Second) // the PR badges
	h.waitFor("#7✗", 10*time.Second)
	time.Sleep(3500 * time.Millisecond) // let every session's git details arrive

	scene := screenFromEmulator(h.emu)
	sceneText := h.screen()

	// The exited shell selected: its saved output, restored after the
	// restart. It's six rows down: past docs, web, the cli header,
	// quiet-otter and the (no group) header.
	h.send("\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B")
	h.waitFor("last output · saved 2h ago", 5*time.Second)
	h.waitFor("planning-review.md", 3*time.Second)
	time.Sleep(300 * time.Millisecond)
	restored := screenFromEmulator(h.emu)
	t.Logf("restored output:\n%s", h.screen())
	h.send("\x1b[A\x1b[A\x1b[A\x1b[A\x1b[A\x1b[A")
	h.waitGone("last output", 3*time.Second)

	// The config screen, with the second agent selected and being tested
	// (t): it runs hookapp in the api session's folder.
	h.send("c")
	h.waitFor("+ Add agent", 5*time.Second)
	h.send("\x1b[B")
	h.waitFor("› claude-opus", 3*time.Second)
	h.send("t")
	h.waitFor("test · claude-opus", 5*time.Second)
	h.waitFor("✓ hooks work (SessionStart received)", 15*time.Second)
	time.Sleep(300 * time.Millisecond)
	config := screenFromEmulator(h.emu)
	t.Logf("config screen:\n%s", h.screen())
	h.send("\x14x") // ctrl+t, then discard it
	h.waitFor("Discarded the test session", 5*time.Second)

	// F1 there: the help, with the config screen's keys first.
	h.send("\x1bOP")
	h.waitFor("Keys · Config screen (c)", 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	configHelp := screenFromEmulator(h.emu)
	t.Logf("help from the config screen:\n%s", h.screen())
	h.send("\x1b")
	h.waitGone("Keys · Config screen (c)", 3*time.Second)
	h.send("\x1b")
	h.waitGone("+ Add agent", 3*time.Second)

	// The diff preview (d) of the api session's work.
	h.send("d")
	h.waitFor("files changed", 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	diffShot := screenFromEmulator(h.emu)
	t.Logf("diff preview:\n%s", h.screen())
	h.send("\x1b")
	h.waitGone("files changed", 3*time.Second)

	// The cli group collapsed (space on its header, three rows down), its
	// header showing quiet-otter's ✓; then the notes shell dragged into the
	// app group with the mouse: the drop line, indented under app, shows
	// it would land after docs. esc cancels the drag.
	h.send("\x1b[B\x1b[B\x1b[B ")
	h.waitFor("▸ cli", 3*time.Second)
	// 2 app, 3-5 api, 6-8 docs, 9-11 web, 13 cli, 15 (no group), 16-18 notes
	h.send("\x1b[<0;6;17M")  // press on notes
	h.send("\x1b[<32;6;12M") // over web
	h.send("\x1b[<32;6;9M")  // docs's last line: below it
	h.waitFor("━━━━━━", 3*time.Second)
	time.Sleep(300 * time.Millisecond)
	dragShot := screenFromEmulator(h.emu)
	t.Logf("dragging:\n%s", h.screen())
	h.send("\x1b")
	h.send("\x1b[<0;6;9m") // the release after esc changes nothing
	h.waitGone("━━━━━━", 3*time.Second)

	// One image: the scene, the restored output, the config screen, the
	// help opened from it, the diff preview and the drag, with a blank row between
	// them.
	blank := make([]termshot.Cell, len(scene[0]))
	for i := range blank {
		blank[i] = termshot.Cell{Text: " ", Width: 1}
	}
	both := append(termshot.Screen{}, scene...)
	both = append(append(both, blank), restored...)
	both = append(append(both, blank), config...)
	both = append(append(both, blank), configHelp...)
	both = append(append(both, blank), diffShot...)
	both = append(append(both, blank), dragShot...)

	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := termshot.Render(f, both, termshot.Options{}); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s\n%s", out, sceneText)

	// Optionally the help overlay too (? in the list), to check it by hand.
	if helpOut := os.Getenv("DECKHAND_SCREENSHOT_HELP"); helpOut != "" {
		h.send("?")
		h.waitFor("New-session dialog", 5*time.Second)
		hf, err := os.Create(helpOut)
		if err != nil {
			t.Fatal(err)
		}
		defer hf.Close()
		if err := termshot.Render(hf, screenFromEmulator(h.emu), termshot.Options{}); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", helpOut)
		h.send("\x1b")
	}

	// Optionally the scene with the list hidden (ctrl+\): the selected
	// session gets the full width and the header ends with the status line.
	if hidden := os.Getenv("DECKHAND_SCREENSHOT_HIDDEN"); hidden != "" {
		h.send("\x1c") // ctrl+\
		h.waitFor(`need you · 1 finished · ctrl+\ list`, 5*time.Second)
		time.Sleep(500 * time.Millisecond)
		hf, err := os.Create(hidden)
		if err != nil {
			t.Fatal(err)
		}
		defer hf.Close()
		if err := termshot.Render(hf, screenFromEmulator(h.emu), termshot.Options{}); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s\n%s", hidden, h.screen())
		h.send("\x1c") // show it again
	}

	h.send("q")
	h.send("y")
	h.waitExit(10 * time.Second)
}

// onGitHub gives a repo an origin on GitHub, which is never fetched: the
// fake gh answers for it.
func onGitHub(t *testing.T, gitExe, dir, name string) {
	t.Helper()
	if out, err := exec.Command(gitExe, "-C", dir, "remote", "add", "origin", "https://github.com/example/"+name+".git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
}

// worktreeScene makes the cli repo with a worktree, quiet-otter, that has
// one commit more than main, and a notes folder (plans) whose fake Claude
// works on it.
func worktreeScene(t *testing.T, gitExe, src string) (plans, cli, wt string) {
	t.Helper()
	run := func(dir string, args ...string) {
		full := append([]string{"-C", dir, "-c", "user.name=Dev", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command(gitExe, full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cli = filepath.Join(src, "cli")
	wt = filepath.Join(src, "cli-wt", "quiet-otter")
	plans = filepath.Join(src, "plans")
	for _, d := range []string{cli, plans} {
		_ = os.MkdirAll(d, 0o755)
	}
	run(src, "init", "-q", "-b", "main", cli)
	_ = os.WriteFile(filepath.Join(cli, "upload.go"), []byte("package cli\n"), 0o644)
	run(cli, "add", ".")
	run(cli, "commit", "-q", "-m", "Add the upload client")
	run(cli, "worktree", "add", "-q", "-b", "quiet-otter", wt, "main")
	_ = os.WriteFile(filepath.Join(wt, "upload.go"), []byte("package cli\n\n// Retry failed uploads.\n"), 0o644)
	run(wt, "commit", "-q", "-am", "Retry failed uploads")
	_ = os.WriteFile(filepath.Join(plans, "hookapp.txt"), []byte(`sleep 800
event SessionStart
reply 64100 Added retries to the upload client, committed on quiet-otter.
say \e[1m> add retries to the upload client\e[0m
event UserPromptSubmit
event PreToolUse Edit | upload.go
event PostToolUse Edit
event Stop
`), 0o644)
	return plans, cli, wt
}

// apiWork gives the api repo some history for the diff preview: login.ts
// committed on main, the fix committed on feature/login, and a test added
// but not committed yet.
func apiWork(t *testing.T, gitExe, dir string) {
	t.Helper()
	run := func(args ...string) {
		full := append([]string{"-C", dir, "-c", "user.name=Dev", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command(gitExe, full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, text string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Remove(filepath.Join(dir, "changed.txt")) // the uncommitted test file makes it dirty instead
	run("symbolic-ref", "HEAD", "refs/heads/main")
	write("src/auth/login.ts", `import { checkSession, refreshToken } from "./session";

export async function login(req: Request): Promise<Response> {
  if (!checkSession(req)) {
    return redirect("/login");
  }
  const token = await refreshToken(req);
  return withCookie(redirect("/"), token);
}
`)
	write("package.json", "{\n  \"name\": \"api\",\n  \"scripts\": { \"test\": \"vitest\" }\n}\n")
	run("add", ".")
	run("commit", "-q", "-m", "Add login")
	run("switch", "-q", "-c", "feature/login")
	write("src/auth/login.ts", `import { checkSession, refreshToken } from "./session";

export async function login(req: Request): Promise<Response> {
  // Refresh first: the session check needs the new cookie.
  const token = await refreshToken(req);
  if (!checkSession(req, token)) {
    return redirect("/login?expired=1");
  }
  return withCookie(redirect("/"), token);
}
`)
	run("commit", "-q", "-am", "Check the session after the token refresh")
	write("src/auth/login.test.ts", `import { login } from "./login";

test("doesn't loop after the token refresh", async () => {
  const res = await login(fakeRequest({ expired: true }));
  expect(res.headers.get("location")).toBe("/");
});
`)
}

// screenFromEmulator copies the emulator's cells for rendering.
func screenFromEmulator(emu *vt.SafeEmulator) termshot.Screen {
	w, hh := emu.Width(), emu.Height()
	scr := make(termshot.Screen, hh)
	for y := 0; y < hh; y++ {
		row := make([]termshot.Cell, w)
		for x := 0; x < w; x++ {
			c := emu.CellAt(x, y)
			if c == nil {
				row[x] = termshot.Cell{Text: " ", Width: 1}
				continue
			}
			row[x] = termshot.Cell{
				Text:       c.Content,
				Width:      c.Width,
				Fg:         c.Style.Fg,
				Bg:         c.Style.Bg,
				Bold:       c.Style.Attrs&uv.AttrBold != 0,
				Faint:      c.Style.Attrs&uv.AttrFaint != 0,
				Reverse:    c.Style.Attrs&uv.AttrReverse != 0,
				Underlined: c.Style.Underline != 0,
			}
		}
		scr[y] = row
	}
	return scr
}
