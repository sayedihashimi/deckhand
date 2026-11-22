package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFS maps a lower-cased directory to its subfolders.
type fakeFS map[string][]string

func (f fakeFS) list(dir string) []string { return f[strings.ToLower(filepath.Clean(dir))] }

var testFS = fakeFS{
	`c:\`:              {"data", "Users", "$Recycle.Bin", "Windows"},
	`c:\data`:          {"mycode", "Documents", "tools"},
	`c:\data\mycode`:   {"deckhand", "dotnet-new-web", "eShop", "agent-deck", ".git"},
	`c:\data\mycode\x`: {},
}

func paths(s []folderSuggestion) []string {
	var out []string
	for _, x := range s {
		p := x.Path
		if x.Recent {
			p += " (recent)"
		}
		out = append(out, p)
	}
	return out
}

func TestFolderSuggestions(t *testing.T) {
	cwd := `C:\data\mycode`
	recents := []string{`C:\data\mycode\deckhand`, `C:\data\tools`}
	cases := []struct {
		name, input string
		want        []string
	}{
		{"empty shows recents then cwd", "", []string{
			`C:\data\mycode\deckhand (recent)`, `C:\data\tools (recent)`,
			`C:\data\mycode\agent-deck`, `C:\data\mycode\dotnet-new-web`, `C:\data\mycode\eShop`,
		}},
		{"plain word prefix ranks first", "d", []string{
			`C:\data\mycode\deckhand (recent)`, `C:\data\mycode\dotnet-new-web`, `C:\data\mycode\agent-deck`,
		}},
		{"plain word fuzzy", "dnw", []string{`C:\data\mycode\dotnet-new-web`}},
		{"case-insensitive path prefix", `c:\data\MY`, []string{`c:\data\mycode`}},
		{"trailing separator lists children", `C:\data\`, []string{`C:\data\tools (recent)`, `C:\data\Documents`, `C:\data\mycode`}},
		{"drive root, hidden skipped", `C:`, []string{`C:\data`, `C:\Users`, `C:\Windows`}},
		{"relative path", `..\to`, []string{`C:\data\tools (recent)`}},
		{"hidden shown when typed", `.g`, []string{`C:\data\mycode\.git`}},
		{"no match", "zzz", nil},
	}
	for _, c := range cases {
		got := paths(folderSuggestions(c.input, cwd, recents, testFS.list, 8))
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s (%q):\n got  %q\n want %q", c.name, c.input, got, c.want)
		}
	}
}

func TestParentPath(t *testing.T) {
	cwd := `C:\data\mycode`
	cases := map[string]string{
		`C:\data\mycode\deck`: `C:\data\mycode\`,
		`C:\data\mycode\`:     `C:\data\`,
		`C:\data`:             `C:\`,
		`C:\`:                 `C:\`,
		"":                    `C:\data\mycode\`,
		`deck`:                `C:\data\mycode\`,
	}
	for in, want := range cases {
		if got := parentPath(in, cwd); got != want {
			t.Errorf("parentPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolvePath(t *testing.T) {
	cwd := `C:\data\mycode`
	home, _ := os.UserHomeDir()
	cases := map[string]string{
		"":          cwd,
		"deckhand":  `C:\data\mycode\deckhand`,
		`..\tools\`: `C:\data\tools`,
		"D:":        `D:\`,
		`~\code`:    filepath.Join(home, "code"),
		`C:\x\y\`:   `C:\x\y`,
	}
	for in, want := range cases {
		if got := resolvePath(in, cwd); got != want {
			t.Errorf("resolvePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrefsRecentDirs(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	a, b := t.TempDir(), t.TempDir()
	var p prefs
	p.useDir(a)
	p.useDir(b)
	p.useDir(strings.ToUpper(a)) // same folder, different case: moves to front
	if len(p.RecentDirs) != 2 || !strings.EqualFold(p.RecentDirs[0], a) {
		t.Fatalf("recent dirs = %q", p.RecentDirs)
	}
	if err := p.save(); err != nil {
		t.Fatal(err)
	}
	got := recentDirs(loadPrefs(), []*Session{{Dir: `C:\does\not\exist`}})
	if len(got) != 2 {
		t.Errorf("recentDirs = %q (missing folders should be dropped)", got)
	}
}
