package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// mkdirs makes folders under a temp dir and returns their paths.
func mkdirs(t *testing.T, names ...string) []string {
	t.Helper()
	root := t.TempDir()
	var out []string
	for _, n := range names {
		d := filepath.Join(root, n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func folderLabels(fs []sessionFolder) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.label+"="+f.dir)
	}
	return out
}

// TestOpenFoldersShapes: the folders o and t offer for each session shape.
func TestOpenFoldersShapes(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	d := mkdirs(t, "notes", "api", "api-wt/feat", "repo", "repo/sub", "repo-wt/x")
	notes, api, apiWT, repo, sub, repoWT := d[0], d[1], d[2], d[3], d[4], d[5]
	cases := []struct {
		name string
		s    *Session
		want []string
	}{
		{"one folder", &Session{ID: "a", Dir: notes}, []string{"start folder=" + notes}},
		{"code repo beside", &Session{ID: "b", Dir: notes, CodeRepo: api}, []string{"start folder=" + notes, "code repo=" + api}},
		{"code repo containing the start folder", &Session{ID: "c", Dir: sub, CodeRepo: repo}, []string{"start folder=" + sub}},
		{"worktree outside the start folder", &Session{ID: "d", Dir: notes, CodeRepo: api, RepoRoot: api, Worktree: apiWT, Branch: "feat"},
			[]string{"start folder=" + notes, "worktree · feat=" + apiWT}},
		// The start folder is in the repo: only the worktree, never the
		// main checkout.
		{"worktree of the start folder's repo", &Session{ID: "e", Dir: sub, CodeRepo: repo, RepoRoot: repo, Worktree: repoWT, Branch: "x"},
			[]string{"worktree · x=" + repoWT}},
	}
	for _, c := range cases {
		if got := folderLabels(c.s.openFolders()); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// writeTranscript writes JSONL entries.
func writeTranscript(t *testing.T, entries ...any) string {
	t.Helper()
	var b strings.Builder
	for _, e := range entries {
		line, _ := json.Marshal(e)
		b.Write(line)
		b.WriteByte('\n')
	}
	p := filepath.Join(t.TempDir(), "11111111-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func addDirEntry(arg string) map[string]any {
	return map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": "<command-name>/add-dir</command-name>\n            <command-message>add-dir</command-message>\n            <command-args>" + arg + "</command-args>"}}
}

// TestOpenFoldersAdded: folders added with /add-dir, from the transcript
// and from the status line record, after the session's own folders,
// without duplicates or folders that are gone.
func TestOpenFoldersAdded(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	d := mkdirs(t, "notes", "api", "docs", "specs", "notes/rel")
	notes, api, docs, specs, rel := d[0], d[1], d[2], d[3], d[4]
	tr := writeTranscript(t,
		map[string]any{"type": "user", "message": map[string]any{"content": "hello"}},
		addDirEntry(docs),
		addDirEntry(strings.ToUpper(api)+`\`), // the code repo again, another case and a trailing separator
		addDirEntry(filepath.Join(notes, "gone")),
		addDirEntry("rel"), // relative to the start folder
		// The same command echoed into a request blob isn't a user entry.
		map[string]any{"type": "api-request-blob", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": "<command-name>/add-dir</command-name><command-args>" + specs + "</command-args>"}}}},
		// A user entry with text blocks counts.
		map[string]any{"type": "user", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": "<command-name>/add-dir</command-name>\n<command-args>\"" + docs + "\"</command-args>"}}}},
	)
	s := &Session{ID: "added-1", Dir: notes, CodeRepo: api, TranscriptPath: tr}
	// The status line lists the code repo (deckhand's --add-dir) and specs.
	if err := writeStatusRecord(s.ID, statusRecord{Seq: 1, AddedDirs: []string{api, specs}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"start folder=" + notes, "code repo=" + api, addedLabel + "=" + specs, addedLabel + "=" + docs, addedLabel + "=" + rel}
	if got := folderLabels(s.openFolders()); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestTranscriptAddedDirs(t *testing.T) {
	tr := writeTranscript(t, addDirEntry(`C:\src\notes`), addDirEntry(`  ~/docs `), addDirEntry(""))
	if got := transcriptAddedDirs(tr); !reflect.DeepEqual(got, []string{`C:\src\notes`, `~/docs`}) {
		t.Errorf("got %q", got)
	}
	if got := transcriptAddedDirs(filepath.Join(t.TempDir(), "none.jsonl")); got != nil {
		t.Errorf("missing transcript: %q", got)
	}
	// The last line, without its newline yet, still counts.
	p := filepath.Join(t.TempDir(), "t.jsonl")
	line, _ := json.Marshal(addDirEntry(`C:\src\api`))
	_ = os.WriteFile(p, line, 0o600)
	if got := transcriptAddedDirs(p); !reflect.DeepEqual(got, []string{`C:\src\api`}) {
		t.Errorf("no newline: %q", got)
	}
}

func TestStatusPayloadAddedDirs(t *testing.T) {
	var p statusPayload
	if err := json.Unmarshal([]byte(`{"workspace":{"current_dir":"C:\\src\\notes","added_dirs":["C:\\src\\api"]}}`), &p); err != nil {
		t.Fatal(err)
	}
	if r := p.record(1); !reflect.DeepEqual(r.AddedDirs, []string{`C:\src\api`}) {
		t.Errorf("record = %+v", r)
	}
}

func TestWorkspaceFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DECKHAND_HOME", home)
	folders := []sessionFolder{{`C:\src\notes`, "start folder"}, {`C:\src\api-wt\feat`, "worktree · feat"}, {`C:\src\docs`, addedLabel}}
	old := filepath.Join(sessionDir("ws-1"), "old name.code-workspace")
	_ = os.MkdirAll(filepath.Dir(old), 0o700)
	_ = os.WriteFile(old, []byte("{}"), 0o600)

	path, err := writeWorkspace("ws-1", `api: "login"`, folders)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "sessions", "ws-1", "api- -login-.code-workspace"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the old workspace file should be gone")
	}
	data, _ := os.ReadFile(path)
	var ws struct {
		Folders []struct{ Name, Path string } `json:"folders"`
	}
	if err := json.Unmarshal(data, &ws); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	got := []string{}
	for _, f := range ws.Folders {
		got = append(got, f.Name+"|"+f.Path)
	}
	want := []string{`notes (start folder)|C:\src\notes`, `feat (worktree · feat)|C:\src\api-wt\feat`, `docs (added)|C:\src\docs`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("folders = %q", got)
	}
	if workspaceName("..") != "session.code-workspace" {
		t.Errorf("empty name: %q", workspaceName(".."))
	}
}

func TestIsVSCodeTemplate(t *testing.T) {
	for tmpl, want := range map[string]bool{
		`code "{dir}"`:                                  true,
		`code-insiders -n "{dir}"`:                      true,
		`"C:\Program Files\VS Code\bin\code.cmd" {dir}`: true,
		`rider64.exe "{dir}"`:                           false,
		`explorer.exe "{dir}"`:                          false,
		`codex "{dir}"`:                                 false,
	} {
		if got := isVSCodeTemplate(tmpl); got != want {
			t.Errorf("%q: %v", tmpl, got)
		}
	}
}

func TestWtTabsArgs(t *testing.T) {
	dirs := []string{`C:\a`, `C:\b c`, `C:\d`}
	cases := []struct {
		tmpl string
		want []string
	}{
		{`wt.exe -w 0 nt -d "{dir}"`, []string{"-w", "0", "nt", "-d", `C:\a`, ";", "nt", "-d", `C:\b c`, ";", "nt", "-d", `C:\d`}},
		{`wt -w -1 new-tab --title x -d "{dir}"`, []string{"-w", "-1", "new-tab", "--title", "x", "-d", `C:\a`, ";", "new-tab", "--title", "x", "-d", `C:\b c`, ";", "new-tab", "--title", "x", "-d", `C:\d`}},
		// No subcommand: wt opens a tab; the repeats say nt.
		{`wt.exe -w 0 -d "{dir}"`, []string{"-w", "0", "-d", `C:\a`, ";", "nt", "-d", `C:\b c`, ";", "nt", "-d", `C:\d`}},
	}
	for _, c := range cases {
		parts, _ := splitCommand(c.tmpl)
		if got := wtTabsArgs(parts[1:], dirs); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.tmpl, got, c.want)
		}
	}
}

func TestParseOpenMulti(t *testing.T) {
	for in, want := range map[string]string{"": "", "ask": "ask", " ALL ": "all", "first": "first"} {
		cfg, err := parseOpenConfig("[open]\nmulti = '" + in + "'\n")
		if err != nil || cfg.Multi != want {
			t.Errorf("%q: %q %v", in, cfg.Multi, err)
		}
	}
	if _, err := parseOpenConfig("[open]\nmulti = 'some'\n"); err == nil || !strings.Contains(err.Error(), "open.multi") {
		t.Errorf("bad multi: %v", err)
	}
}

// launch is one fake openStart call.
type launch struct {
	prog string
	args []string
	cwd  string
}

// fakeLaunches replaces openStart for the test and records each call.
func fakeLaunches(t *testing.T) *[]launch {
	t.Helper()
	var got []launch
	saved := openStart
	openStart = func(prog string, args []string, cwd string) error {
		got = append(got, launch{prog, args, cwd})
		return nil
	}
	t.Cleanup(func() { openStart = saved })
	return &got
}

// withOpenSettings sets [open] for the test.
func withOpenSettings(t *testing.T, cfg openConfig) {
	t.Helper()
	saved := openSettings
	openSettings = cfg
	t.Cleanup(func() { openSettings = saved })
}

// TestOpenPicker: o on a session with two folders shows the picker with
// All folders selected; enter opens them as one VS Code workspace, and a
// row opens only its folder. t with All opens one wt.exe with a tab per
// folder.
func TestOpenPicker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DECKHAND_HOME", home)
	d := mkdirs(t, "notes", "api")
	launches := fakeLaunches(t)
	withOpenSettings(t, openConfig{Editor: `code "{dir}"`})
	m := testModel("pick")
	m.width, m.height = 120, 32
	s := m.sessions[0]
	s.Dir, s.CodeRepo = d[0], d[1]

	m.handleKey(key("o"))
	if m.folderPick == nil || m.folderPick.hi != 0 {
		t.Fatalf("no picker, or not on All folders: %+v", m.folderPick)
	}
	view := strings.Join(m.renderFolderPick(100), "\n")
	for _, want := range []string{`Open "pick" in the editor`, "› All folders", "start folder", "code repo"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker lacks %q:\n%s", want, view)
		}
	}
	m.handleKey(key("enter"))
	ws := filepath.Join(home, "sessions", "pick", "pick.code-workspace")
	if m.folderPick != nil || len(*launches) != 1 || !reflect.DeepEqual((*launches)[0], launch{"code", []string{ws}, d[0]}) {
		t.Fatalf("All folders launched %+v", *launches)
	}
	if data, err := os.ReadFile(ws); err != nil || !strings.Contains(string(data), "api (code repo)") {
		t.Errorf("workspace: %s %v", data, err)
	}

	// The code repo's row: only that folder.
	*launches = nil
	m.handleKey(key("o"))
	m.handleKey(key("down"))
	m.handleKey(key("down"))
	m.handleKey(key("enter"))
	if len(*launches) != 1 || !reflect.DeepEqual((*launches)[0], launch{"code", []string{d[1]}, d[1]}) {
		t.Errorf("code repo row launched %+v", *launches)
	}

	// esc: nothing.
	*launches = nil
	m.handleKey(key("o"))
	m.handleKey(key("esc"))
	if m.folderPick != nil || len(*launches) != 0 {
		t.Errorf("esc: %+v %+v", m.folderPick, *launches)
	}

	// Another editor runs once per folder.
	openSettings.Editor = `rider64.exe "{dir}"`
	m.handleKey(key("o"))
	m.handleKey(key("enter"))
	if len(*launches) != 2 || (*launches)[0].args[0] != d[0] || (*launches)[1].args[0] != d[1] {
		t.Errorf("rider: %+v", *launches)
	}

	// t: the same picker; All is one wt.exe with a tab per folder.
	*launches = nil
	m.handleKey(key("t"))
	if m.folderPick == nil || m.folderPick.target != openTerminal {
		t.Fatal("t should show the picker")
	}
	if view := strings.Join(m.renderFolderPick(100), "\n"); !strings.Contains(view, "in a terminal") {
		t.Errorf("terminal picker:\n%s", view)
	}
	m.handleKey(key("enter"))
	want := launch{"wt.exe", []string{"-w", "0", "nt", "-d", d[0], ";", "nt", "-d", d[1]}, d[0]}
	if len(*launches) != 1 || !reflect.DeepEqual((*launches)[0], want) {
		t.Errorf("t All: %+v", *launches)
	}

	// Another terminal: one window per folder.
	*launches = nil
	openSettings.Terminal = `alacritty.exe --working-directory "{dir}"`
	m.handleKey(key("t"))
	m.handleKey(key("enter"))
	if len(*launches) != 2 || (*launches)[1].args[1] != d[1] {
		t.Errorf("other terminal: %+v", *launches)
	}
}

// TestOpenMultiSetting: [open] multi = all and first skip the picker, and a
// one-folder session never shows it.
func TestOpenMultiSetting(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	d := mkdirs(t, "notes", "api")
	launches := fakeLaunches(t)
	withOpenSettings(t, openConfig{Editor: `rider64.exe "{dir}"`, Multi: multiFirst})
	m := testModel("multi")
	s := m.sessions[0]
	s.Dir, s.CodeRepo = d[0], d[1]

	m.handleKey(key("o"))
	if m.folderPick != nil || len(*launches) != 1 || (*launches)[0].args[0] != d[0] {
		t.Errorf("first: %+v %+v", m.folderPick, *launches)
	}
	*launches = nil
	openSettings.Multi = multiAll
	m.handleKey(key("o"))
	if m.folderPick != nil || len(*launches) != 2 {
		t.Errorf("all: %+v %+v", m.folderPick, *launches)
	}
	*launches = nil
	openSettings.Multi = ""
	s.CodeRepo = ""
	m.handleKey(key("o"))
	if m.folderPick != nil || len(*launches) != 1 || (*launches)[0].args[0] != d[0] || (*launches)[0].cwd != d[0] {
		t.Errorf("one folder: %+v %+v", m.folderPick, *launches)
	}
	// O (Explorer) always opens the start folder.
	*launches = nil
	s.CodeRepo = d[1]
	m.handleKey(tea.KeyPressMsg{Code: 'O', Text: "O"})
	if m.folderPick != nil || len(*launches) != 1 || (*launches)[0].prog != "explorer.exe" || (*launches)[0].args[0] != d[0] {
		t.Errorf("O: %+v %+v", m.folderPick, *launches)
	}
}

// TestOpenAllSkipsMissing: All folders leaves out a folder that's gone and
// says so.
func TestOpenAllSkipsMissing(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	d := mkdirs(t, "notes")
	launches := fakeLaunches(t)
	withOpenSettings(t, openConfig{Editor: `rider64.exe "{dir}"`, Multi: multiAll})
	m := testModel("gone")
	s := m.sessions[0]
	s.Dir, s.CodeRepo = d[0], filepath.Join(d[0], "..", "missing")
	m.handleKey(key("o"))
	if len(*launches) != 1 || (*launches)[0].args[0] != d[0] || !strings.Contains(m.flash, "folder not found") {
		t.Errorf("launches %+v, flash %q", *launches, m.flash)
	}
}
