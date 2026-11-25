package main

import (
	"encoding/json"
	"image/color"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestParseColor(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"red", "red", true},
		{" Teal ", "teal", true},
		{"none", "", true},
		{"", "", true},
		{"#FF00aa", "#ff00aa", true},
		{"#f0a", "#ff00aa", true},
		{"f0a", "#ff00aa", true},
		{"#12", "", false},
		{"#12345", "", false},
		{"#gg0000", "", false},
		{"cyan", "", false}, // not a palette name
	}
	for _, c := range cases {
		got, ok := parseColor(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseColor(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	if colorHex("red") != "#f7768e" || colorHex("#ABCDEF") != "#abcdef" || colorHex("cyan") != "" || colorHex("") != "" {
		t.Errorf("colorHex: %q %q %q", colorHex("red"), colorHex("#ABCDEF"), colorHex("cyan"))
	}
	if got := dimHex("#ffffff"); got != "#8c8d92" {
		t.Errorf("dimHex = %q", got)
	}
}

// Colors are saved in sessions.json and groups.json, and read back; with
// none, no field is written.
func TestColorsSavedAndLoaded(t *testing.T) {
	m := groupedModel(t, "api@app", "docs@app", "web")
	m.sessions[0].Color = "red"
	m.sessions[2].Color = "#ff00aa"
	m.groupByName("app").Color = "teal"
	m.saveAll()

	data, _ := os.ReadFile(storePath())
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw[0]["color"] != "red" || raw[2]["color"] != "#ff00aa" {
		t.Errorf("sessions.json: %s", data)
	}
	if _, ok := raw[1]["color"]; ok {
		t.Errorf("docs has no color, but it's saved: %s", data)
	}
	gdata, _ := os.ReadFile(groupsPath())
	if !strings.Contains(string(gdata), `"color": "teal"`) {
		t.Errorf("groups.json: %s", gdata)
	}

	m2 := &model{width: 120, height: 40}
	m2.sessions = loadSessions()
	m2.setGroups(loadGroups())
	got := map[string]string{}
	for _, s := range m2.sessions {
		got[s.Name] = s.Color + "/" + m2.sessionColor(s)
	}
	want := map[string]string{"api": "red/#f7768e", "docs": "/#73daca", "web": "#ff00aa/#ff00aa"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	if g := m2.groupByName("app"); g == nil || g.Color != "teal" {
		t.Errorf("group color not loaded: %+v", g)
	}
}

// pickModel is a model with sessions api, docs and web, api selected.
func pickModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("DECKHAND_HOME", t.TempDir())
	m := testModel("api", "docs", "web")
	m.width, m.height = 120, 40
	return m
}

func TestColorPickKeys(t *testing.T) {
	m := pickModel(t)
	s := m.sessions[0]
	press(m, key("C"))
	if m.colorPick == nil || m.colorPick.hi != len(colorChoices())-1 {
		t.Fatalf("C should open the picker on none: %+v", m.colorPick)
	}
	// Arrows wrap around: down from none is red.
	press(m, key("down"))
	press(m, key("enter"))
	if m.colorPick != nil || s.color() != "red" {
		t.Fatalf("enter: picker %v, color %q", m.colorPick, s.color())
	}
	// It opens on the current color; a first letter picks, again goes to
	// the next one with it.
	press(m, key("C"))
	if m.colorPick.hi != 0 {
		t.Errorf("opened on %d, want red (0)", m.colorPick.hi)
	}
	press(m, key("p"))
	press(m, key("p"))
	press(m, key("enter"))
	if s.color() != "pink" {
		t.Errorf("p p: %q, want pink", s.color())
	}
	// esc changes nothing.
	press(m, key("C"))
	press(m, key("g"))
	press(m, key("esc"))
	if m.colorPick != nil || s.color() != "pink" {
		t.Errorf("esc: picker %v, color %q", m.colorPick, s.color())
	}
	// n is none: it clears the color.
	press(m, key("C"))
	press(m, key("n"))
	press(m, key("enter"))
	if s.color() != "" {
		t.Errorf("none: %q", s.color())
	}
	// Saved right away.
	data, _ := os.ReadFile(storePath())
	if strings.Contains(string(data), `"color"`) {
		t.Errorf("cleared color still saved: %s", data)
	}
}

func TestColorPickHex(t *testing.T) {
	m := pickModel(t)
	s := m.sessions[0]
	press(m, key("C"))
	press(m, key("tab"))
	if !m.colorPick.onHex {
		t.Fatal("tab should move to the hex box")
	}
	typeText(m, "#12")
	press(m, key("enter"))
	if m.colorPick == nil || m.colorPick.err == "" || s.color() != "" {
		t.Fatalf("an invalid hex color should be refused with a hint: %+v, color %q", m.colorPick, s.color())
	}
	box := ansi.Strip(strings.Join(m.renderColorPick(80), "\n"))
	if !strings.Contains(box, "not a color") {
		t.Errorf("no hint in the picker:\n%s", box)
	}
	// The hex box shows a swatch of what's typed once it's a color.
	press(m, key("a"))
	if !strings.Contains(strings.Join(m.renderColorPick(80), "\n"), "██") || m.colorPick.err != "" {
		t.Errorf("no preview for #12a")
	}
	press(m, key("enter"))
	if m.colorPick != nil || s.color() != "#1122aa" {
		t.Fatalf("hex: picker %v, color %q", m.colorPick, s.color())
	}
	// Reopened, it's in the hex box with the value.
	press(m, key("C"))
	if !m.colorPick.onHex || m.colorPick.hex.Value() != "#1122aa" {
		t.Errorf("reopened: onHex %v, %q", m.colorPick.onHex, m.colorPick.hex.Value())
	}
	// Pasting goes into the hex box.
	m.colorPick.hex.SetValue("")
	m.Update(tea.PasteMsg{Content: "#00ff00"})
	press(m, key("enter"))
	if s.color() != "#00ff00" {
		t.Errorf("pasted: %q", s.color())
	}
}

func TestColorPickMouse(t *testing.T) {
	m := pickModel(t)
	s := m.sessions[0]
	left := m.mainLeft() + colorPickLeft
	press(m, key("C"))
	// The fourth color (green) is on the box's line 4.
	m.handleMouse(tea.MouseClickMsg{X: left + 4, Y: colorPickTop + pickFirstChoice + 3, Button: tea.MouseLeft})
	if m.colorPick != nil || s.color() != "green" {
		t.Fatalf("click on green: picker %v, color %q", m.colorPick, s.color())
	}
	// A click on the hex box starts typing there.
	press(m, key("C"))
	m.handleMouse(tea.MouseClickMsg{X: left + 4, Y: colorPickTop + pickHexLine(), Button: tea.MouseLeft})
	if m.colorPick == nil || !m.colorPick.onHex {
		t.Fatalf("click on the hex box: %+v", m.colorPick)
	}
	// A click outside closes it, unchanged.
	m.handleMouse(tea.MouseClickMsg{X: left + 60, Y: 30, Button: tea.MouseLeft})
	if m.colorPick != nil || s.color() != "green" {
		t.Errorf("click outside: picker %v, color %q", m.colorPick, s.color())
	}
	// The wheel does nothing while it's open.
	press(m, key("C"))
	m.handleMouse(tea.MouseWheelMsg{X: 3, Y: 5, Button: tea.MouseWheelDown})
	if m.colorPick == nil || m.sel != 0 {
		t.Errorf("wheel: picker %v, sel %d", m.colorPick, m.sel)
	}
}

// A group's color is its sessions' unless they have their own; clearing
// the session's makes it inherit again, clearing the group's removes it.
func TestGroupColorInherited(t *testing.T) {
	m := groupedModel(t, "api@app", "docs@app", "web")
	selectHeader(m, "app")
	press(m, key("C"))
	if m.colorPick == nil || m.colorPick.g == nil {
		t.Fatal("C on a group header should open the picker for the group")
	}
	press(m, key("t")) // teal
	press(m, key("enter"))
	api, docs, web := m.sessions[0], m.sessions[1], m.sessions[2]
	teal := palette["teal"]
	if m.sessionColor(api) != teal || m.sessionColor(docs) != teal || m.sessionColor(web) != "" {
		t.Fatalf("inherited: %q %q %q", m.sessionColor(api), m.sessionColor(docs), m.sessionColor(web))
	}
	api.setColor("red")
	if m.sessionColor(api) != palette["red"] {
		t.Errorf("own color should win: %q", m.sessionColor(api))
	}
	api.setColor("")
	if m.sessionColor(api) != teal {
		t.Errorf("cleared: %q", m.sessionColor(api))
	}
	selectHeader(m, "app")
	press(m, key("C"))
	press(m, key("n"))
	press(m, key("enter"))
	if m.sessionColor(api) != "" || m.colorsInUse() {
		t.Errorf("group cleared: %q", m.sessionColor(api))
	}
	// "(no group)" can't have one.
	selectHeader(m, "")
	press(m, key("C"))
	if m.colorPick != nil || !strings.Contains(m.flash, "can't have a color") {
		t.Errorf("(no group): picker %v, flash %q", m.colorPick, m.flash)
	}
}

// cellFg is the foreground of a cell of the sidebar drawn in an emulator.
func sidebarCells(t *testing.T, m *model, w, h int) *vt.Emulator {
	t.Helper()
	emu := vt.NewEmulator(w, h)
	_, _ = emu.Write([]byte(strings.Join(m.renderSidebar(w, h), "\r\n")))
	return emu
}

func rgb(c color.Color) color.RGBA {
	if c == nil {
		return color.RGBA{}
	}
	r, g, b, _ := c.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
}

func hexRGB(h string) color.RGBA {
	r, g, b := rgbOf(h)
	return color.RGBA{r, g, b, 255}
}

// The bar is in column 0 of all three rows: in the color, over the
// selection's background when selected, dimmed when exited, blank for a
// session with no color. The status icon keeps its own color.
func TestColorBarRendering(t *testing.T) {
	m := testModel("api", "docs", "web", "plain")
	m.sessions[0].Color = "red"     // selected
	m.sessions[1].Color = "#00ff00" // plain row
	m.sessions[2].Color = "blue"    // exited
	m.sessions[2].status = StatusExited
	m.sessions[0].status = StatusWaiting
	m.sel = 0
	const w, h = 30, 16
	emu := sidebarCells(t, m, w, h)
	line := func(row, i int) int { return listTop + row*rowsPerSession + i }
	for i := 0; i < rowsPerSession; i++ {
		c := emu.CellAt(0, line(0, i))
		if c.Content != "┃" || rgb(c.Style.Fg) != hexRGB(palette["red"]) || rgb(c.Style.Bg) != (color.RGBA{42, 47, 69, 255}) {
			t.Errorf("selected row %d: %q fg %v bg %v", i, c.Content, rgb(c.Style.Fg), rgb(c.Style.Bg))
		}
		if m := emu.CellAt(1, line(0, i)); m.Content != "▌" {
			t.Errorf("selected row %d: the selection marker is gone: %q", i, m.Content)
		}
		if c := emu.CellAt(0, line(1, i)); c.Content != "┃" || rgb(c.Style.Fg) != hexRGB("#00ff00") {
			t.Errorf("plain row %d: %q %v", i, c.Content, rgb(c.Style.Fg))
		}
		if c := emu.CellAt(0, line(2, i)); c.Content != "┃" || rgb(c.Style.Fg) != hexRGB(dimHex(palette["blue"])) {
			t.Errorf("exited row %d: %q %v", i, c.Content, rgb(c.Style.Fg))
		}
		if c := emu.CellAt(0, line(3, i)); c.Content != " " && c.Content != "" {
			t.Errorf("uncolored row %d: %q", i, c.Content)
		}
	}
	// The status icon (●, waiting) keeps its color, after the bar, the
	// marker and the number.
	if c := emu.CellAt(4, line(0, 0)); rgb(c.Style.Fg) != rgb(colWaiting) {
		t.Errorf("status icon %q fg %v", c.Content, rgb(c.Style.Fg))
	}
	// Names line up, colored or not; the details stay where they were.
	text := strings.Split(ansi.Strip(strings.Join(m.renderSidebar(w, h), "\n")), "\n")
	at := func(l, sub string) int { return len([]rune(l[:max(strings.Index(l, sub), 0)])) }
	if a, b := at(text[line(1, 0)], "docs"), at(text[line(3, 0)], "plain"); a != b {
		t.Errorf("names don't line up: %d vs %d\n%s", a, b, strings.Join(text, "\n"))
	}
	if a, b := at(text[line(1, 1)], "pwsh"), at(text[line(0, 1)], "pwsh"); a != 5 || b != 5 {
		t.Errorf("details at %d and %d, want 5", a, b)
	}

	// Without any colors there's no bar column.
	plain := testModel("api", "docs")
	ptext := ansi.Strip(strings.Join(plain.renderSidebar(w, 9), "\n"))
	if !strings.Contains(ptext, "\n2 ◌ docs") {
		t.Errorf("no colors: the list should look as before:\n%s", ptext)
	}
}

// A group header with a color has the bar, and the session header's name
// is drawn in the session's color.
func TestColorHeaderAndGroup(t *testing.T) {
	m := groupedModel(t, "api@app", "web")
	m.groupByName("app").Color = "purple"
	emu := sidebarCells(t, m, 30, 16)
	if c := emu.CellAt(0, listTop); c.Content != "┃" || rgb(c.Style.Fg) != hexRGB(palette["purple"]) {
		t.Errorf("group header bar: %q %v", c.Content, rgb(c.Style.Fg))
	}
	m.sel = 0
	lines, _ := m.renderMain(80, 10)
	memu := vt.NewEmulator(80, 10)
	_, _ = memu.Write([]byte(strings.Join(lines, "\r\n")))
	i := strings.Index(ansi.Strip(lines[0]), "api")
	if i < 0 {
		t.Fatalf("header: %q", ansi.Strip(lines[0]))
	}
	if c := memu.CellAt(len([]rune(ansi.Strip(lines[0])[:i])), 0); rgb(c.Style.Fg) != hexRGB(palette["purple"]) {
		t.Errorf("header name fg %v", rgb(c.Style.Fg))
	}
}

// Fork (f) and shift+enter keep the session's own color.
func TestForkAndShiftEnterKeepColor(t *testing.T) {
	started := forkStart(t)
	m := claudeModel()
	m.sessions[1].Color = "orange"
	m.sel = 1
	m.handleKey(forkKey())
	if len(*started) != 1 || m.sessions[2].Color != "orange" {
		t.Fatalf("fork color %q", m.sessions[2].Color)
	}

	fakeStart(t)
	m2 := testModel("api", "docs")
	m2.sessions[0].Color = "#123456"
	m2.sel = 0
	m2.handleKey(shiftEnterKey())
	if len(m2.sessions) != 3 || m2.sessions[1].Color != "#123456" {
		t.Fatalf("shift+enter color %q", m2.sessions[1].Color)
	}
}

// A temporary window colors sessions but saves nothing.
func TestTempColorsNotSaved(t *testing.T) {
	m := pickModel(t)
	m.temp = true
	press(m, key("C"))
	press(m, key("r"))
	press(m, key("enter"))
	if m.sessions[0].color() != "red" {
		t.Errorf("color %q", m.sessions[0].color())
	}
	if _, err := os.Stat(storePath()); err == nil {
		t.Error("a temporary window saved sessions.json")
	}
}

func TestColorsConfig(t *testing.T) {
	got, err := parseColorsConfig("[colors]\nred = \"#e06c75\"\nBlue = \"#abc\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["red"] != "#e06c75" || got["blue"] != "#aabbcc" || got["green"] != "#9ece6a" {
		t.Errorf("palette: %v", got)
	}
	if _, err := parseColorsConfig("[colors]\ncyan = \"#00ffff\"\n"); err == nil || !strings.Contains(err.Error(), "isn't a palette name") {
		t.Errorf("unknown name: %v", err)
	}
	if _, err := parseColorsConfig("[colors]\nred = \"crimson\"\n"); err == nil || !strings.Contains(err.Error(), "hex color") {
		t.Errorf("bad hex: %v", err)
	}
	if _, err := parseColorsConfig(sampleConfig); err != nil {
		t.Errorf("sample config: %v", err)
	}
	// The sample shows a [colors] that works once uncommented.
	var sample []string
	in := false
	for _, l := range strings.Split(sampleConfig, "\n") {
		switch {
		case l == "# [colors]":
			in = true
		case in && strings.HasPrefix(l, "# ") && strings.Contains(l, "="):
			sample = append(sample, strings.TrimPrefix(l, "# "))
		case in:
			in = false
		}
	}
	if len(sample) == 0 {
		t.Fatal("no [colors] in the sample config")
	}
	if _, err := parseColorsConfig("[colors]\n" + strings.Join(sample, "\n")); err != nil {
		t.Errorf("sample [colors]: %v", err)
	}
	if err := checkConfigText("[colors]\nred = 1\n"); err == nil {
		t.Error("checkConfigText should refuse a bad [colors]")
	}
}
