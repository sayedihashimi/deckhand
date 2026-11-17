package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseOpenConfig(t *testing.T) {
	cfg, err := parseOpenConfig(`
[open]
editor   = 'rider64.exe "{dir}"'
terminal = 'wt.exe -w 0 nt -d "{dir}"'
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Editor != `rider64.exe "{dir}"` || cfg.Terminal != `wt.exe -w 0 nt -d "{dir}"` {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg, err := parseOpenConfig("[notifications]\nwhen = \"always\"\n"); err != nil || cfg != (openConfig{}) {
		t.Errorf("no [open]: %+v %v", cfg, err)
	}
	for _, bad := range []struct{ text, want string }{
		{"[open]\neditor = 'code \"{dir}'", "unclosed"},
		{"[open]\neditor = 'code'", "must contain {dir}"},
		{"[open]\nterminal = '{dir}'", "program name"},
		{"[open]\neditor = 42", "editor"},
		{"[open\n", ""},
	} {
		_, err := parseOpenConfig(bad.text)
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: err = %v, want %q", bad.text, err, bad.want)
		}
	}
	if _, err := parseOpenConfig("[open]\nterminal = 'x'"); err == nil || !strings.Contains(err.Error(), "open.terminal") {
		t.Errorf("error should name the setting: %v", err)
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`code "{dir}"`, []string{"code", "{dir}"}},
		{`wt.exe  -w 0 nt -d '{dir}'`, []string{"wt.exe", "-w", "0", "nt", "-d", "{dir}"}},
		{`"C:\Program Files\JetBrains\rider64.exe" {dir}`, []string{`C:\Program Files\JetBrains\rider64.exe`, "{dir}"}},
		{`app --path="{dir}\src" ''`, []string{"app", `--path={dir}\src`, ""}},
		{`app "it's" 'say "hi"'`, []string{"app", "it's", `say "hi"`}},
		{"  ", nil},
	}
	for _, c := range cases {
		got, err := splitCommand(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCommand(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := splitCommand(`app "open`); err == nil {
		t.Error("unclosed quote should fail")
	}
}

func TestExpandTemplate(t *testing.T) {
	// The folder is substituted after splitting: spaces and quotes in it
	// stay inside one argument.
	dir := `C:\my "odd" dir\it's -x & y`
	prog, args, err := expandTemplate(`wt.exe -w 0 nt -d "{dir}"`, dir)
	if err != nil {
		t.Fatal(err)
	}
	if prog != "wt.exe" || !reflect.DeepEqual(args, []string{"-w", "0", "nt", "-d", dir}) {
		t.Errorf("got %q %q", prog, args)
	}
	_, args, _ = expandTemplate(`idea64.exe {dir}`, `C:\a b`)
	if !reflect.DeepEqual(args, []string{`C:\a b`}) {
		t.Errorf("unquoted {dir} with a space: %q", args)
	}
	if _, _, err := expandTemplate("code", dir); err == nil {
		t.Error("a template without {dir} should fail")
	}
}

func TestEditorTemplate(t *testing.T) {
	found := func(string) (string, error) { return `C:\bin\code.cmd`, nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }

	if tmpl, fb := editorTemplate(openConfig{Editor: `rider64.exe "{dir}"`}, found); tmpl != `rider64.exe "{dir}"` || fb {
		t.Errorf("configured: %q %v", tmpl, fb)
	}
	if tmpl, fb := editorTemplate(openConfig{}, found); tmpl != vscodeEditor || fb {
		t.Errorf("code on PATH: %q %v", tmpl, fb)
	}
	if tmpl, fb := editorTemplate(openConfig{}, missing); tmpl != defaultExplorer || !fb {
		t.Errorf("no code: %q %v", tmpl, fb)
	}
	if got := terminalTemplate(openConfig{}); got != defaultTerminal {
		t.Errorf("default terminal = %q", got)
	}
}

func TestCmdLine(t *testing.T) {
	got, err := cmdLine(`C:\bin\code.cmd`, []string{`C:\a & b`})
	if err != nil || got != `"C:\bin\code.cmd" "C:\a & b"` {
		t.Errorf("cmdLine = %q, %v", got, err)
	}
	for _, bad := range []string{`C:\100%PATH%`, `a"b`} {
		if _, err := cmdLine(`x.cmd`, []string{bad}); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// TestLaunchOpen runs a real program (recordapp) for a folder with a space
// and an ampersand, and checks the arguments it got.
func TestLaunchOpen(t *testing.T) {
	bin := t.TempDir()
	app := filepath.Join(bin, "recordapp")
	if runtime.GOOS == "windows" {
		app += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", app, "./testdata/recordapp").CombinedOutput(); err != nil {
		t.Fatalf("build recordapp: %v\n%s", err, out)
	}
	dir := filepath.Join(t.TempDir(), "my project & co")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "log.txt")
	tmpl := `"` + app + `" "` + log + `" --folder "{dir}"`
	if err := launchOpen(tmpl, dir); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Cwd  string   `json:"cwd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(recordLine(t, log)), &rec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.Args, []string{"--folder", dir}) || rec.Cwd != dir {
		t.Errorf("recorded %+v", rec)
	}
	// A .cmd file (like VS Code's code.cmd) runs through cmd.exe; the & in
	// the folder name must not start a second command.
	if runtime.GOOS == "windows" {
		shim := filepath.Join(bin, "edit.cmd")
		_ = os.WriteFile(shim, []byte("@\""+app+"\" %*\r\n"), 0o644)
		cmdLog := filepath.Join(t.TempDir(), "cmd.txt")
		if err := launchOpen(`"`+shim+`" "`+cmdLog+`" "{dir}"`, dir); err != nil {
			t.Fatal(err)
		}
		if got := recordLine(t, cmdLog); !strings.Contains(got, strings.TrimSuffix(strings.TrimPrefix(mustJSON(t, dir), `"`), `"`)+`"]`) {
			t.Errorf(".cmd recorded %s", got)
		}
	}
	if err := launchOpen(tmpl, filepath.Join(dir, "gone")); err == nil || !strings.Contains(err.Error(), "folder not found") {
		t.Errorf("missing folder: %v", err)
	}
	if err := launchOpen(`no-such-program-xyz "{dir}"`, dir); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing program: %v", err)
	}
}

// recordLine waits for recordapp to write its log line.
func recordLine(t *testing.T, log string) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		if data, err := os.ReadFile(log); err == nil && strings.HasSuffix(string(data), "\n") {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s was never written", log)
	return ""
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
