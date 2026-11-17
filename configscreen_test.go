package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// keepGlobals restores the settings a save applies.
func keepGlobals(t *testing.T) {
	agents, mode, open := allAgents, notifyMode, openSettings
	t.Cleanup(func() { allAgents, notifyMode, openSettings = agents, mode, open })
}

func tempConfig(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if text != "" {
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DECKHAND_CONFIG", p)
	return p
}

func TestConfigRoundTrip(t *testing.T) {
	tempConfig(t, "")
	text := `
[[agents]]
name   = "smc"
pwsh   = "smc"
type   = "claude"
resume = "smc -Resume {id}"

[[agents]]
name    = "opus"
command = "claude"
args    = ["--model", "opus 5", "it's"]
type    = "claude"

[notifications]
enabled = false
when    = "always"

[open]
editor = 'rider64.exe "{dir}"'

[list]
status_line = true
`
	cfg, raw, err := decodeConfig(text)
	if err != nil {
		t.Fatal(err)
	}
	out, err := encodeConfig(cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "# deckhand config") {
		t.Errorf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "[list]") || !strings.Contains(out, "status_line = true") {
		t.Errorf("unknown tables should be kept:\n%s", out)
	}
	for _, f := range []func(string) (any, error){
		func(s string) (any, error) { return parseAgents(s) },
		func(s string) (any, error) { return parseNotifyMode(s) },
		func(s string) (any, error) { return parseOpenConfig(s) },
	} {
		a, errA := f(text)
		b, errB := f(out)
		if errA != nil || errB != nil {
			t.Fatalf("parse: %v / %v\n%s", errA, errB, out)
		}
		if strings.TrimSpace(toString(a)) != strings.TrimSpace(toString(b)) {
			t.Errorf("round trip changed %v to %v\n%s", a, b, out)
		}
	}
	// Encoding again gives the same text.
	cfg2, raw2, err := decodeConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	if out2, _ := encodeConfig(cfg2, raw2); out2 != out {
		t.Errorf("not stable:\n%s\n---\n%s", out, out2)
	}
}

func toString(v any) string { return fmt.Sprintf("%+v", v) }

func TestWriteConfigFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "config.toml")
	backed, err := writeConfigFile(p, "a = 1\n")
	if err != nil || backed {
		t.Fatalf("first write: backed=%v err=%v", backed, err)
	}
	if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
		t.Errorf("no .bak expected for a new file")
	}
	backed, err = writeConfigFile(p, "a = 2\n")
	if err != nil || !backed {
		t.Fatalf("second write: backed=%v err=%v", backed, err)
	}
	if b, _ := os.ReadFile(p + ".bak"); string(b) != "a = 1\n" {
		t.Errorf(".bak = %q", b)
	}
	if b, _ := os.ReadFile(p); string(b) != "a = 2\n" {
		t.Errorf("file = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 2 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("temporary file left behind: %v", names)
	}
}

func TestHasOwnComments(t *testing.T) {
	for text, want := range map[string]bool{
		"":                                    false,
		sampleConfig:                          false,
		configHeader + "[open]\neditor = 'x'": false,
		sampleConfig + "\n# my own note\n":    true,
		"[open] # inline\n":                   true,
		`name = "a # not a comment"`:          false,
		`name = 'a#b' # but this is`:          true,
		`name = "esc \" # still a string"`:    false,
	} {
		if got := hasOwnComments(text); got != want {
			t.Errorf("hasOwnComments(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestParseProfileCommands(t *testing.T) {
	out := "Welcome from my profile!\r\nLoaded posh-git\r\n" + profileMarker + "\r\n" +
		`[{"Name":"smc","Kind":"Function","Target":"","Source":"","Resume":true},` +
		`{"Name":"gt","Kind":"Alias","Target":"Get-Thing","Source":"","Resume":false},` +
		`{"Name":"SMC","Kind":"Function","Target":"","Source":"","Resume":false},` +
		`{"Name":"Alpha","Kind":"Function","Target":"","Source":"tools","Resume":false}]` + "\r\n"
	list, err := parseProfileCommands(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range list {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "Alpha,gt,smc" {
		t.Errorf("names = %s", got)
	}
	if list[2].resumeHint() != "smc -Resume {id}" || list[1].resumeHint() != "" {
		t.Errorf("hints: %q %q", list[2].resumeHint(), list[1].resumeHint())
	}

	one, err := parseProfileCommands(profileMarker + `{"Name":"solo","Kind":"Alias"}`)
	if err != nil || len(one) != 1 || one[0].Name != "solo" {
		t.Errorf("single object: %v %v", one, err)
	}
	if _, err := parseProfileCommands("WARNING: something broke"); err == nil {
		t.Error("no marker should be an error")
	}
	if empty, err := parseProfileCommands(profileMarker + "\n[]"); err != nil || len(empty) != 0 {
		t.Errorf("empty list: %v %v", empty, err)
	}
}

func TestAgentFormCheck(t *testing.T) {
	others := []agentConfig{{Name: "smc", Pwsh: "smc"}}
	cases := []struct {
		setup func(f *agentForm)
		field int
	}{
		{func(f *agentForm) {}, ffName},
		{func(f *agentForm) { f.name.SetValue("x") }, ffCommand},
		{func(f *agentForm) { f.name.SetValue("smc"); f.command.SetValue("smc") }, ffName},
		{func(f *agentForm) { f.name.SetValue("x"); f.command.SetValue("x"); f.resume.SetValue("x -Resume") }, ffResume},
		{func(f *agentForm) {
			f.pwsh = false
			f.name.SetValue("x")
			f.command.SetValue("x")
			f.args.SetValue(`"unclosed`)
		}, ffArgs},
	}
	for i, c := range cases {
		f := newAgentForm(-1, agentConfig{})
		c.setup(f)
		if _, ok := f.check(others); ok || f.errField != c.field {
			t.Errorf("case %d: ok=%v field=%d (%s), want field %d", i, ok, f.errField, f.err, c.field)
		}
	}

	// Editing an agent may keep its own name.
	f := newAgentForm(0, others[0])
	if a, ok := f.check(others); !ok || a.Name != "smc" || a.Pwsh != "smc" {
		t.Errorf("edit: %+v ok=%v err=%s", a, ok, f.err)
	}

	// A program with arguments, read back as the same list.
	f = newAgentForm(-1, agentConfig{Name: "opus", Command: "claude", Args: []string{"--model", "opus 5", `say "hi"`}, Type: "claude"})
	if f.pwsh || !f.claude {
		t.Fatalf("form kind: pwsh=%v claude=%v", f.pwsh, f.claude)
	}
	a, ok := f.check(nil)
	if !ok || strings.Join(a.Args, "|") != `--model|opus 5|say "hi"` || a.Type != "claude" {
		t.Errorf("program: %+v ok=%v err=%s", a, ok, f.err)
	}

	// Picking a Claude wrapper fills in the rest.
	f = newAgentForm(-1, agentConfig{})
	f.pick(profileCommand{Name: "smc", Kind: "Function", Resume: true})
	a, ok = f.check(nil)
	if !ok || a.Name != "smc" || a.Pwsh != "smc" || a.Type != "claude" || a.Resume != "smc -Resume {id}" {
		t.Errorf("picked: %+v ok=%v err=%s", a, ok, f.err)
	}
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func typeText(m *model, s string) {
	for _, r := range s {
		m.handleKey(key(string(r)))
	}
}

// Adding an agent through the screen, saving, and the settings applying
// straight away.
func TestConfigScreenSaveAndReload(t *testing.T) {
	keepGlobals(t)
	p := tempConfig(t, "# my notes\n[open]\nterminal = 'wt.exe -d \"{dir}\"'\n")
	m := testModel("api")
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	if m.cfg == nil || m.focus != focusConfig {
		t.Fatal("c should open the config screen")
	}
	if !m.cfg.comments {
		t.Error("the file's own comment should be noticed")
	}
	m.handleKey(key("enter")) // the first row is "+ Add agent"
	if m.cfg.form == nil {
		t.Fatal("enter on + Add agent should open the form")
	}
	typeText(m, "mine")
	m.handleKey(key("tab")) // kind
	m.handleKey(key("tab")) // pick
	m.handleKey(key("tab")) // command
	typeText(m, "Start-Mine")
	m.handleKey(key("enter"))
	if m.cfg.form != nil || len(m.cfg.agents) != 1 || !m.cfg.dirty() {
		t.Fatalf("agent not added: form=%v agents=%+v", m.cfg.form, m.cfg.agents)
	}
	if strings.Contains(strings.Join(m.renderConfig(90, 31), "\n"), "unsaved changes") == false {
		t.Error("should say there are unsaved changes")
	}

	m.handleKey(key("ctrl+s"))
	if m.confirm == nil || !strings.Contains(m.confirm.text, "comments") {
		t.Fatal("should warn about losing comments before the first save")
	}
	m.handleKey(key("y"))
	if m.cfg.dirty() || m.cfg.statusErr {
		t.Fatalf("after save: dirty=%v status=%q", m.cfg.dirty(), m.cfg.status)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `name = "mine"`) || !strings.Contains(string(data), `pwsh = "Start-Mine"`) {
		t.Errorf("config.toml:\n%s", data)
	}
	if bak, _ := os.ReadFile(p + ".bak"); !strings.Contains(string(bak), "# my notes") {
		t.Errorf(".bak = %q", bak)
	}
	if a, ok := agentByName("mine"); !ok || a.Pwsh != "Start-Mine" {
		t.Errorf("allAgents not reloaded: %+v", allAgents)
	}
	if openSettings.Terminal != `wt.exe -d "{dir}"` {
		t.Errorf("openSettings = %+v", openSettings)
	}

	// Turn notifications off; esc then asks before discarding.
	m.cfg.row = m.cfg.rowOf(rowNotifyEnabled)
	m.handleKey(key("enter"))
	m.handleKey(key("esc"))
	if m.confirm == nil || m.cfg == nil {
		t.Fatal("esc with changes should ask first")
	}
	m.handleKey(key("n"))
	m.handleKey(key("ctrl+s")) // no comments now, so no question
	if m.confirm != nil || notifyMode != notifyOff {
		t.Errorf("save: confirm=%v notifyMode=%s", m.confirm, notifyMode)
	}
	m.handleKey(key("esc"))
	if m.cfg != nil || m.focus != focusSidebar {
		t.Error("esc without changes should leave")
	}
}

func TestConfigScreenRejectsBadOpen(t *testing.T) {
	keepGlobals(t)
	p := tempConfig(t, "")
	m := testModel()
	m.width, m.height = 120, 32
	m.handleKey(key("c"))
	m.cfg.editor.SetValue("code")
	m.handleKey(key("ctrl+s"))
	if !m.cfg.statusErr || m.cfg.current().kind != rowEditor {
		t.Errorf("status %q, row %v", m.cfg.status, m.cfg.current())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("nothing should be written")
	}
}

func TestFooterHasConfigKey(t *testing.T) {
	m := testModel("api")
	for _, w := range []int{120, 200} {
		f := m.renderFooter(w)
		if !strings.Contains(ansi.Strip(f), "c config") || ansi.StringWidth(f) > w {
			t.Errorf("footer at %d: %q", w, ansi.Strip(f))
		}
	}
}
