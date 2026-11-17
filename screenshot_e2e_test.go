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

// TestE2EScreenshot sets up a scripted scene (three fake Claude sessions in
// different states, in git repos, plus an exited shell) and saves deckhand's
// screen as a PNG. The PR workflow posts it on each pull request.
//
//	$env:DECKHAND_SCREENSHOT="shot.png"; go test -tags e2e -run TestE2EScreenshot -v
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

	home := t.TempDir()
	type saved struct {
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Agent   string    `json:"agent"`
		Dir     string    `json:"dir"`
		Created time.Time `json:"created"`
		Claude  string    `json:"claude_session_id,omitempty"`
		Started bool      `json:"started,omitempty"`
	}
	now := time.Now()
	list := []saved{
		{"shot-api", "api", "claude", api, now, "11111111-1111-1111-1111-111111111111", true},
		{"shot-docs", "docs", "claude", docs, now, "22222222-2222-2222-2222-222222222222", true},
		{"shot-web", "web", "claude", web, now, "33333333-3333-3333-3333-333333333333", true},
		{"shot-notes", "notes", "pwsh", notes, now, "", true},
	}
	data, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	path := bin + ";" + filepath.Dir(pwsh) + ";" + filepath.Dir(gitExe) + ";" + os.Getenv("SystemRoot") + `\System32`
	env := append(envWith(home, path), "DECKHAND_NOTIFY_LOG="+filepath.Join(home, "notify.log"))
	h := startHarnessSize(t, exe, env, src, 120, 32)

	h.waitFor("notes", 10*time.Second)
	// Start the three Claude sessions (resuming them), coming back to the
	// list after each.
	for _, k := range []string{"3", "2", "1"} {
		h.send(k)
		time.Sleep(700 * time.Millisecond)
		h.send("\x11")
	}
	h.waitFor("permission: Bash", 15*time.Second)
	h.waitFor("claude · your turn", 15*time.Second)
	h.waitFor("opus 5.5", 10*time.Second)
	time.Sleep(3500 * time.Millisecond) // let every session's git details arrive

	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := termshot.Render(f, screenFromEmulator(h.emu), termshot.Options{}); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s\n%s", out, h.screen())

	h.send("q")
	h.send("y")
	h.waitExit(10 * time.Second)
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
