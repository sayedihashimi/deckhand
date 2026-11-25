package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// setAliases sets the path aliases from a [[paths.alias]] config and the
// home folder, for one test.
func setAliases(t *testing.T, home string, aliases ...[2]string) {
	t.Helper()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	var b strings.Builder
	for _, a := range aliases {
		b.WriteString("[[paths.alias]]\nprefix = '" + a[0] + "'\nname = '" + a[1] + "'\n")
	}
	got, err := parsePathsConfig(b.String())
	if err != nil {
		t.Fatal(err)
	}
	old := pathAliases
	pathAliases = got
	t.Cleanup(func() { pathAliases = old })
}

func TestDisplayPath(t *testing.T) {
	setAliases(t, `C:\Users\dev`,
		[2]string{`C:\src\notes`, "notes"},
		[2]string{`C:\src\notes\features\`, "features"},
		[2]string{`C:\Users\dev\work`, "work"},
		[2]string{`~\demo`, "demo"},
		[2]string{`D:\`, "data"})
	for _, c := range []struct{ in, want string }{
		{`C:\src\notes`, `notes`},
		{`C:\src\notes\plans\x`, `notes\plans\x`},
		{`c:\SRC\Notes\plans`, `notes\plans`},             // case
		{`C:/src/notes/plans`, `notes/plans`},             // forward slashes
		{`C:\src\notes2`, `C:\src\notes2`},                // only at a folder boundary
		{`C:\src\notes\features\login`, `features\login`}, // the longest match wins
		{`C:\src\notes\featuresx`, `notes\featuresx`},
		{`C:\Users\dev\work\api`, `work\api`}, // an alias before ~
		{`C:\Users\dev\demo\a`, `demo\a`},     // a prefix written with ~
		{`C:\Users\dev\code\api`, `~\code\api`},
		{`C:\Users\dev`, `~`},
		{`C:\Users\dev2\code`, `C:\Users\dev2\code`},
		{`D:\x\y`, `data\x\y`}, // a drive root
		{`E:\x`, `E:\x`},
		{``, ``},
	} {
		if got := displayPath(c.in); got != c.want {
			t.Errorf("displayPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDisplayPathHomeOnly(t *testing.T) {
	setAliases(t, `C:\Users\dev`)
	if got := displayPath(`C:\Users\dev\code`); got != `~\code` {
		t.Errorf("no aliases: got %q, want ~\\code", got)
	}
	if got := shortDir(`C:\Users\dev\code\api`, 40); got != `~\code\api` {
		t.Errorf("shortDir = %q", got)
	}
}

func TestDisplayPathsIn(t *testing.T) {
	setAliases(t, `C:\Users\dev`, [2]string{`C:\src\notes`, "notes"})
	for _, c := range []struct{ in, want string }{
		{`folder not found: C:\src\notes\x`, `folder not found: notes\x`},
		{`C:\src\notes isn't in a git repo`, `notes isn't in a git repo`},
		{`fatal: 'C:/src/notes/wt' is a missing worktree`, `fatal: 'notes/wt' is a missing worktree`},
		{`(C:\src\notes\a), C:\Users\dev\b; C:\src\notes2`, `(notes\a), ~\b; C:\src\notes2`},
		{`xC:\src\notes`, `xC:\src\notes`}, // not at a word start
		{`Merge it in C:\src\notes? y/n`, `Merge it in notes? y/n`},
		{`Kept C:\src\notes. Done.`, `Kept notes. Done.`},
		{`C:\src\notes.old is another folder`, `C:\src\notes.old is another folder`},
		{`no paths here`, `no paths here`},
	} {
		if got := displayPathsIn(c.in); got != c.want {
			t.Errorf("displayPathsIn(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A path deckhand fills into the new-session dialog is shown aliased and
// still resolves to the real folder.
func TestDisplayInputRoundTrip(t *testing.T) {
	setAliases(t, `C:\Users\dev`, [2]string{`C:\src\notes`, "notes"})
	for _, p := range []string{`C:\src\notes`, `C:\src\notes\plans`, `C:\Users\dev`, `C:\Users\dev\code`, `C:\other`} {
		in := displayInput(p)
		if strings.Contains(strings.ToLower(in), `c:\src\notes`) {
			t.Errorf("displayInput(%q) = %q shows the prefix", p, in)
		}
		if got := resolvePath(in, `C:\cwd`); !strings.EqualFold(got, p) {
			t.Errorf("resolvePath(displayInput(%q) = %q) = %q", p, in, got)
		}
	}
	// A plain word is still a folder name under cwd, not the alias.
	if got := resolvePath("notes", `C:\cwd`); got != `C:\cwd\notes` {
		t.Errorf("resolvePath(notes) = %q", got)
	}
}

func TestParsePathsConfig(t *testing.T) {
	for _, bad := range []string{
		"[[paths.alias]]\nprefix = 'C:\\src'\n",
		"[[paths.alias]]\nname = 'src'\n",
		"[[paths.alias]]\nprefix = 'C:\\src'\nname = 'a\\b'\n",
	} {
		if _, err := parsePathsConfig(bad); err == nil {
			t.Errorf("no error for %q", bad)
		}
		if err := checkConfigText(bad); err == nil {
			t.Errorf("checkConfigText: no error for %q", bad)
		}
	}
	got, err := parsePathsConfig("[[paths.alias]]\nprefix = 'C:\\a'\nname = 'a'\n[[paths.alias]]\nprefix = 'C:\\a\\b\\'\nname = 'b'\n")
	if err != nil || len(got) != 2 || got[0].name != "b" || got[0].prefix != `C:\a\b` {
		t.Errorf("got %+v, %v; want the longest prefix first, without its trailing \\", got, err)
	}
	if got, err := parsePathsConfig(""); err != nil || got != nil {
		t.Errorf("empty: %+v, %v", got, err)
	}
	if _, err := parsePathsConfig(sampleConfig); err != nil {
		t.Errorf("sample config: %v", err)
	}
}

// No screen shows an aliased prefix: the list, the header, the footer's
// messages, the new-session dialog and its suggestions, the config screen,
// help, the diff, the worktree close panel and log, and notifications.
func TestNoScreenShowsAliasedPrefix(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DECKHAND_HOME", filepath.Join(root, "state"))
	notes := filepath.Join(root, "src", "notes")
	login := filepath.Join(notes, "features", "login")
	wt := filepath.Join(notes, "wt", "quiet-otter")
	for _, d := range []string{login, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setAliases(t, filepath.Join(root, "home"), [2]string{notes, "notes"})
	cfg := filepath.Join(root, "config.toml")
	_ = os.WriteFile(cfg, []byte("[[agents]]\nname = 'review'\ncommand = '"+filepath.Join(notes, "review.cmd")+
		"'\n\n[[paths.alias]]\nprefix = '"+notes+"'\nname = 'notes'\n"), 0o644)
	t.Setenv("DECKHAND_CONFIG", cfg)
	p := prefs{}
	p.useDir(login)
	if err := p.save(); err != nil {
		t.Fatal(err)
	}

	m := testModel("login", "plans")
	m.width, m.height = 220, 40
	s := m.sessions[0]
	s.Agent, s.Dir = "claude", login
	s.Worktree, s.RepoRoot, s.CodeRepo, s.Branch = wt, notes, notes, "quiet-otter"
	s.info.git = gitInfo{ok: true, branch: "quiet-otter"}
	s.status, s.detail, s.since = StatusIdle, detailYourTurn, time.Now()
	s.info.lastText = []string{"Updated " + filepath.Join(login, "README.md") + "."}
	s.info.textAt = time.Now().Add(time.Second)
	s.title = "PS " + login
	s.prepLog = []string{"git worktree add -b quiet-otter " + wt + " main"}
	o := m.sessions[1]
	o.Dir = filepath.Join(notes, "plans")
	o.status, o.detail, o.summary = StatusWaiting, "permission: Edit", filepath.Join(notes, "plans", "todo.md")

	screens := map[string]string{}
	view := func(name string) { screens[name] = ansi.Strip(m.View().Content) }
	m.sel = 0
	view("list and header")
	m.sel = 1
	view("list, a permission prompt")
	m.sel = 0
	m.setFlash("folder not found: " + login)
	view("flash")
	m.flash = ""
	m.confirm = &confirmPrompt{text: "Merge quiet-otter into main in " + notes + "? y/n"}
	view("confirm")
	m.confirm = nil
	m.closing = &closePrompt{s: s, check: closeCheck{fetchErr: "fatal: " + filepath.ToSlash(wt) + " is gone"}}
	view("close panel")
	m.closing = nil

	m.dialog = newNewDialog([]Agent{{Name: "claude"}}, 200, m.sessions)
	m.focus = focusDialog
	view("dialog")
	press(m, tea.KeyPressMsg{Code: tea.KeyTab}) // completes the recent folder
	view("dialog, tab")
	if got := m.dialog.target(); !strings.EqualFold(got, login) {
		t.Errorf("after tab the dialog starts in %q, want %q", got, login)
	}
	m.dialog.err = "Folder not found: " + login
	view("dialog error")
	m.dialog, m.focus = nil, focusSidebar

	m.cfg = newConfigScreen(200)
	m.focus = focusConfig
	m.cfg.setStatus("Testing \"review\" in "+login, false)
	view("config screen")
	if !strings.Contains(screens["config screen"], "Path aliases") || !strings.Contains(screens["config screen"], "notes") {
		t.Errorf("the config screen doesn't list the alias:\n%s", screens["config screen"])
	}
	m.cfg, m.focus = nil, focusSidebar

	m.diff = &diffView{name: "login", dir: login, loading: true}
	view("diff")
	m.diff = nil
	m.openHelp()
	view("help")
	m.help = nil

	screens["prep log"] = ansi.Strip(strings.Join(renderPrep(s.prepLog, 200, 5), "\n"))
	title, body := noticeText("login", "claude", notice{kind: noticeWaiting, detail: "permission: Edit", summary: o.summary}, "")
	screens["notification"] = title + "\n" + body

	raw := []string{strings.ToLower(notes), strings.ToLower(filepath.ToSlash(notes))}
	for name, text := range screens {
		low := strings.ToLower(text)
		for _, r := range raw {
			if strings.Contains(low, r) {
				t.Errorf("%s shows the aliased prefix %s:\n%s", name, r, text)
			}
		}
	}
	for name, want := range map[string]string{
		"list and header":           `notes\features\login`,
		"list, a permission prompt": `Edit: notes\plans`,
		"flash":                     `folder not found: notes\features\login`,
		"confirm":                   "main in notes?",
		"close panel":               `notes\wt\quiet-otter`,
		"dialog":                    `notes\features\login`,
		"dialog, tab":               `notes\features\login\`,
		"prep log":                  `notes\wt\quiet-otter`,
		"notification":              `notes\plans\todo.md`,
	} {
		if !strings.Contains(screens[name], want) {
			t.Errorf("%s doesn't show %q:\n%s", name, want, screens[name])
		}
	}
}
