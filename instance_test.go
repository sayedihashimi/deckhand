package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("DECKHAND_HOME", home)
	t.Setenv("DECKHAND_CONFIG", filepath.Join(home, "no-config.toml"))
	return home
}

const savedSessionsFile = `[{"id":"one","name":"one","agent":"pwsh","dir":"C:\\src\\one","created":"2026-01-01T00:00:00Z","group":"app"},
{"id":"two","name":"two","agent":"pwsh","dir":"C:\\src\\two","created":"2026-01-01T00:00:00Z"}]`

const savedGroupsFile = `{"groups":[{"name":"app"}]}`

// A temporary instance sharing the data folder with a main one starts
// empty, and leaves sessions.json and groups.json as they were after it
// creates, renames and ends sessions and quits.
func TestTempInstanceLeavesSavedStateAlone(t *testing.T) {
	home := tempHome(t)
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("sessions.json", savedSessionsFile)
	write("groups.json", savedGroupsFile)

	main := newInstanceModel(false)
	if len(main.sessions) != 2 || !main.grouped() {
		t.Fatalf("main: %d sessions, grouped %v", len(main.sessions), main.grouped())
	}
	tmp := newInstanceModel(true)
	if len(tmp.sessions) != 0 || tmp.grouped() {
		t.Fatalf("temporary: %d sessions, grouped %v; want an empty list", len(tmp.sessions), tmp.grouped())
	}

	// Create two, rename one, end the other, then quit.
	a := newSession(Agent{Name: "pwsh"}, `C:\src\notes`, "notes")
	b := newSession(Agent{Name: "pwsh"}, `C:\src\api`, "api")
	tmp.sessions = append(tmp.sessions, a, b)
	_ = tmp.saveSessions()
	tmp.sel = 0
	press(tmp, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if tmp.rename == nil {
		t.Fatal("r should open the rename box")
	}
	tmp.rename.SetValue("renamed")
	press(tmp, tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.Name != "renamed" {
		t.Fatalf("rename: %q", a.Name)
	}
	tmp.removeSession(b)
	tmp.noGroupCollapsed = true
	tmp.saveAll()
	tmp.quit()

	for name, want := range map[string]string{"sessions.json": savedSessionsFile, "groups.json": savedGroupsFile} {
		got, _ := os.ReadFile(filepath.Join(home, name))
		if string(got) != want {
			t.Errorf("%s changed by the temporary instance:\n%s", name, got)
		}
	}

	// The main instance still saves.
	main.sessions[1].Name = "two-renamed"
	if err := main.saveSessions(); err != nil {
		t.Fatal(err)
	}
	if loaded := loadSessions(); len(loaded) != 2 || loaded[1].Name != "two-renamed" {
		t.Errorf("main's save: %+v", loaded)
	}
}

func TestTempInstanceMarkers(t *testing.T) {
	tempHome(t)
	m := newInstanceModel(true)
	m.width, m.height = 120, 30
	if got := m.windowTitle(); got != "deckhand (temporary)" {
		t.Errorf("title %q", got)
	}
	s := newSession(Agent{Name: "pwsh"}, `C:\src\notes`, "notes")
	s.status = StatusWaiting
	m.sessions = append(m.sessions, s)
	if got := m.windowTitle(); got != "deckhand (temporary, 1 need you)" {
		t.Errorf("title %q", got)
	}
	if side := ansi.Strip(strings.Join(m.renderSidebar(32, 20), "\n")); !strings.Contains(side, "deckhand temporary") {
		t.Errorf("the top bar should say temporary:\n%s", side)
	}
	main := newInstanceModel(false)
	if strings.Contains(strings.Join(main.renderSidebar(32, 20), "\n"), "temporary") || main.windowTitle() != "deckhand" {
		t.Error("the main instance shouldn't say temporary")
	}

	// Groups are off.
	press(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.assign != nil || m.flash != tempGroupsNote {
		t.Errorf("a in a temporary window: assign %v, flash %q", m.assign != nil, m.flash)
	}

	// The help says what a temporary window is.
	m.openHelp()
	if help := ansi.Strip(strings.Join(m.helpContent(96, 200).lines, "\n")); !strings.Contains(help, "Temporary window") {
		t.Errorf("help should explain a temporary window:\n%s", help)
	}
}

// The config screen opens in a temporary window, but can't save.
func TestTempInstanceConfigReadOnly(t *testing.T) {
	home := tempHome(t)
	cfg := filepath.Join(home, "config.toml")
	t.Setenv("DECKHAND_CONFIG", cfg)
	before := "[notify]\nenabled = false\n"
	if err := os.WriteFile(cfg, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newInstanceModel(true)
	m.width, m.height = 120, 30
	_ = m.openConfigScreen()
	if m.cfg == nil {
		t.Fatal("the config screen should open")
	}
	view := ansi.Strip(strings.Join(m.renderConfig(100, 28), "\n"))
	if !strings.Contains(view, tempConfigNote) {
		t.Errorf("the footer should say config is read-only here:\n%s", view)
	}
	m.cfg.comments = false
	m.cfg.saved = "" // as if there were changes
	_ = m.saveConfigScreen()
	if m.confirm != nil {
		_ = m.confirm.action(m)
	}
	if got, _ := os.ReadFile(cfg); string(got) != before {
		t.Errorf("a temporary window saved the config:\n%s", got)
	}
	if !strings.Contains(m.cfg.status, tempConfigNote) {
		t.Errorf("status %q", m.cfg.status)
	}
}

// Fields and entries this build doesn't know survive a load and a save.
func TestSessionsUnknownFieldsRoundTrip(t *testing.T) {
	home := tempHome(t)
	in := `[
  {"id":"one","name":"one","agent":"pwsh","dir":"C:\\src\\one","created":"2026-01-01T00:00:00Z","future_field":{"a":[1,2]},"z_flag":true},
  {"kind":"workspace","title":"from a newer build"},
  "just a string",
  {"id":"two","name":"two","agent":"pwsh","dir":"C:\\src\\two","created":"2026-01-01T00:00:00Z"}
]`
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if len(m.sessions) != 2 {
		t.Fatalf("loaded %d sessions, want 2", len(m.sessions))
	}
	m.sessions[0].Name = "first"
	if err := m.saveSessions(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(storePath())
	var out []any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("saved file isn't JSON: %v\n%s", err, data)
	}
	if len(out) != 4 {
		t.Fatalf("saved %d entries, want 4:\n%s", len(out), data)
	}
	first := out[0].(map[string]any)
	if first["name"] != "first" || first["z_flag"] != true || fmt.Sprint(first["future_field"]) != "map[a:[1 2]]" {
		t.Errorf("first session lost its unknown fields or the rename:\n%s", data)
	}
	if _, ok := out[1].(map[string]any)["future_field"]; ok {
		t.Errorf("the second session shouldn't get the first one's fields:\n%s", data)
	}
	if fmt.Sprint(out[2]) != "map[kind:workspace title:from a newer build]" || out[3] != "just a string" {
		t.Errorf("unknown entries should be kept as they were:\n%s", data)
	}
	// A second load and save changes nothing more.
	m2 := newModel()
	if err := m2.saveSessions(); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(storePath()); string(again) != string(data) {
		t.Errorf("a second round trip changed the file:\n%s\nthen\n%s", data, again)
	}
}

func TestGroupsAndPrefsUnknownFieldsRoundTrip(t *testing.T) {
	home := tempHome(t)
	groups := `{"groups":[{"name":"app","color":"blue"}],"layout":"wide"}`
	prefsIn := `{"recent_dirs":["C:\\src\\one"],"sidebar_width":40}`
	_ = os.WriteFile(filepath.Join(home, "sessions.json"), []byte(savedSessionsFile), 0o600)
	_ = os.WriteFile(filepath.Join(home, "groups.json"), []byte(groups), 0o600)
	_ = os.WriteFile(filepath.Join(home, "prefs.json"), []byte(prefsIn), 0o600)
	m := newModel()
	m.setCollapsed("app", true)
	if err := m.saveGroups(); err != nil {
		t.Fatal(err)
	}
	var g map[string]any
	data, _ := os.ReadFile(groupsPath())
	_ = json.Unmarshal(data, &g)
	first := g["groups"].([]any)[0].(map[string]any)
	if g["layout"] != "wide" || first["color"] != "blue" || first["collapsed"] != true {
		t.Errorf("groups.json lost a field:\n%s", data)
	}
	if err := updatePrefs(func(p *prefs) { p.ListHidden = true }); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(prefsPath())
	if !strings.Contains(string(data), `"sidebar_width": 40`) || !strings.Contains(string(data), `"list_hidden": true`) {
		t.Errorf("prefs.json lost a field:\n%s", data)
	}
}

// Recent folders and prefs changed by instances at the same time are all
// kept.
func TestPrefsUpdatesFromBothInstancesAreKept(t *testing.T) {
	tempHome(t)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = updatePrefs(func(p *prefs) {
				p.useDir(fmt.Sprintf(`C:\src\dir%d`, i))
				time.Sleep(5 * time.Millisecond) // widen the race the lock closes
			})
		}()
	}
	wg.Wait()
	if got := len(loadPrefs().RecentDirs); got != 10 {
		t.Errorf("kept %d recent folders, want 10: %v", got, loadPrefs().RecentDirs)
	}

	// Main hides the list while a temporary instance starts a session:
	// both changes stay.
	main, tmp := newInstanceModel(false), newInstanceModel(true)
	main.setListHidden(true)
	_ = updatePrefs(func(p *prefs) { p.useDir(`C:\src\notes`) })
	tmp.setListHidden(false)
	tmp.setListHidden(true)
	p := loadPrefs()
	if !p.ListHidden || p.RecentDirs[0] != `C:\src\notes` || len(p.RecentDirs) != 11 {
		t.Errorf("prefs: %+v", p)
	}
}

func TestMainLockName(t *testing.T) {
	t.Setenv("DECKHAND_HOME", `C:\src\Deck`)
	a := mainLockName()
	t.Setenv("DECKHAND_HOME", `c:\src\deck\`)
	b := mainLockName()
	t.Setenv("DECKHAND_HOME", `C:\src\other`)
	c := mainLockName()
	if a != b {
		t.Errorf("the same folder should get the same name: %q, %q", a, b)
	}
	if a == c {
		t.Errorf("different folders should get different names: %q", a)
	}
	if !strings.HasPrefix(a, `Local\deckhand-main-`) {
		t.Errorf("name %q", a)
	}
}

// The first instance on a data folder is main; the next is temporary, and
// another data folder gets its own main instance.
func TestChooseInstance(t *testing.T) {
	tempHome(t)
	if temp, _ := chooseInstance(true); !temp {
		t.Error("--temp should be temporary")
	}
	if temp, notice := chooseInstance(false); temp || notice != "" {
		t.Fatalf("first instance: temp %v, notice %q", temp, notice)
	}
	if f := savedFormat(); f != stateFormat {
		t.Errorf("main should record format %d, got %d", stateFormat, f)
	}
	if temp, _ := chooseInstance(false); !temp {
		t.Error("a second instance should be temporary")
	}
	tempHome(t)
	if temp, _ := chooseInstance(false); temp {
		t.Error("another data folder should get its own main instance")
	}
}

// A state.json from a newer build makes this one temporary, with a notice.
func TestNewerFormatRunsTemporary(t *testing.T) {
	home := tempHome(t)
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte(`{"format": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	temp, notice := chooseInstance(false)
	if !temp || notice != newerFormatNotice {
		t.Errorf("temp %v, notice %q", temp, notice)
	}
	if f := savedFormat(); f != 99 {
		t.Errorf("state.json was changed: format %d", f)
	}
	// It didn't take the lock, so a newer build can still be main.
	if !acquireMainLock() {
		t.Error("a temporary instance shouldn't hold the main lock")
	}
}

func TestCleanupTempSessions(t *testing.T) {
	tempHome(t)
	s := newSession(Agent{Name: "pwsh"}, `C:\src\notes`, "notes")
	if err := os.MkdirAll(sessionDir(s.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sessionDir(s.ID), "events.jsonl"), []byte("{}\n"), 0o600)
	other := filepath.Join(dataDir(), "sessions", "someone-else")
	_ = os.MkdirAll(other, 0o700)
	cleanupTempSessions([]*Session{s}, time.Second)
	if _, err := os.Stat(sessionDir(s.ID)); !os.IsNotExist(err) {
		t.Errorf("the session's folder should be gone: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another instance's session folder should stay: %v", err)
	}
}
