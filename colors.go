package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"
)

// Session and group colors (#65). C in the list opens a picker: a named
// color from the palette, none, or any hex color. A colored session gets a
// one-column bar (┃) at the left of its three rows in the list, and its
// name in the session header is drawn in the color. A session with no
// color of its own takes its group's.
//
// The color is saved as given: a palette name ("red") or "#rrggbb", in
// sessions.json ("color") and groups.json. A name's look comes from the
// palette, which [colors] in config.toml can change; it can't add names.
// The bar's column is only there while some session or group has a color,
// so the list looks as before until one is colored.

// colorNone is the picker's choice for no color.
const colorNone = "none"

// palette is each palette name's hex color: defaultPalette (view.go) with
// [colors] applied.
var palette = defaultPaletteMap()

func defaultPaletteMap() map[string]string {
	out := map[string]string{}
	for _, c := range defaultPalette {
		out[c.name] = c.hex
	}
	return out
}

// paletteNames are the palette's names in the picker's order.
func paletteNames() []string {
	out := make([]string, len(defaultPalette))
	for i, c := range defaultPalette {
		out[i] = c.name
	}
	return out
}

// parseHex reads "#rrggbb" or "#rgb" (the # is optional) as "#rrggbb".
func parseHex(s string) (string, bool) {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return "", false
	}
	if _, err := strconv.ParseUint(s, 16, 32); err != nil {
		return "", false
	}
	return "#" + s, true
}

// parseColor reads a color as the picker or a saved file gives it: a
// palette name, a hex color, or none (""). ok is false for anything else.
func parseColor(s string) (c string, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "" || s == colorNone:
		return "", true
	case palette[s] != "":
		return s, true
	}
	return parseHex(s)
}

// colorHex is how a saved color is drawn, "#rrggbb", or "" for none (or
// a value this build can't read).
func colorHex(c string) string {
	if c == "" {
		return ""
	}
	if h, ok := palette[strings.ToLower(c)]; ok {
		return h
	}
	h, _ := parseHex(c)
	return h
}

// rgbOf is a "#rrggbb" color's parts.
func rgbOf(hex string) (r, g, b uint8) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return uint8(v >> 16), uint8(v >> 8), uint8(v)
}

// dimHex is a color halfway to the background, for an exited session's
// bar (faint text can be hard to read, so it isn't drawn faint).
func dimHex(hex string) string {
	r, g, b := rgbOf(hex)
	br, bg, bb := rgbOf(dimBackground)
	mix := func(a, b uint8) uint8 { return uint8((int(a) + int(b)) / 2) }
	return fmt.Sprintf("#%02x%02x%02x", mix(r, br), mix(g, bg), mix(b, bb))
}

// Who has which color ------------------------------------------------------------

func (s *Session) color() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Color
}

func (s *Session) setColor(c string) {
	s.mu.Lock()
	s.Color = c
	s.mu.Unlock()
}

// groupColor is a group's color as drawn, or "".
func (m *model) groupColor(name string) string {
	if g := m.groupByName(name); g != nil {
		return colorHex(g.Color)
	}
	return ""
}

// sessionColor is the color a session is drawn in: its own, else its
// group's, else "".
func (m *model) sessionColor(s *Session) string {
	if h := colorHex(s.color()); h != "" {
		return h
	}
	return m.groupColor(s.group())
}

// colorsInUse reports whether any session or group has a color, so the
// list has the bar's column.
func (m *model) colorsInUse() bool {
	for _, g := range m.groups {
		if colorHex(g.Color) != "" {
			return true
		}
	}
	for _, s := range m.sessions {
		if colorHex(s.color()) != "" {
			return true
		}
	}
	return false
}

// colorBar is the bar's cell: ┃ in the color, dimmed for an exited
// session, on the selection's background when selected; a blank with no
// color.
func colorBar(hex string, selected, exited bool) string {
	st := lipgloss.NewStyle()
	if selected {
		st = st.Background(colSelBg)
	}
	if hex == "" {
		return st.Render(" ")
	}
	if exited {
		hex = dimHex(hex)
	}
	return st.Foreground(lipgloss.Color(hex)).Render("┃")
}

// The picker (C) ---------------------------------------------------------------------

// colorPick is the color picker for a session (s) or a group (g).
type colorPick struct {
	s     *Session
	g     *group
	hi    int  // the highlighted choice in colorChoices
	onHex bool // typing in the hex box
	hex   textinput.Model
	err   string // why the hex box's value was refused
}

// colorChoices are the picker's rows: the palette, then none.
func colorChoices() []string { return append(paletteNames(), colorNone) }

// openColorPick opens the picker for the selected session or group header.
func (m *model) openColorPick() tea.Cmd {
	p := &colorPick{}
	cur := ""
	switch {
	case m.onHeader:
		p.g = m.groupByName(m.selHeader)
		if p.g == nil {
			m.setFlash(noGroupName + " can't have a color: color a group or a session")
			return nil
		}
		cur = p.g.Color
	default:
		p.s = m.current()
		if p.s == nil {
			return nil
		}
		cur = p.s.color()
	}
	p.hex = textinput.New()
	p.hex.Prompt = ""
	p.hex.Placeholder = "#rrggbb"
	p.hex.CharLimit = 7
	p.hex.SetWidth(9)
	choices := colorChoices()
	p.hi = len(choices) - 1 // none
	c, _ := parseColor(cur)
	switch {
	case c == "":
	case slices.Contains(choices, c):
		p.hi = slices.Index(choices, c)
	default:
		p.hex.SetValue(c)
		p.hex.CursorEnd()
		p.onHex = true
	}
	m.colorPick = p
	if p.onHex {
		return p.hex.Focus()
	}
	return nil
}

// title is what the picker colors: "api" or the group "app".
func (p *colorPick) title() string {
	if p.g != nil {
		return fmt.Sprintf(" Color for the group %q ", p.g.Name)
	}
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	return fmt.Sprintf(" Color for %q ", p.s.Name)
}

func (m *model) colorPickKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.colorPick
	choices := colorChoices()
	key := k.String()
	switch key {
	case "esc":
		m.colorPick = nil
		return nil
	case "tab", "shift+tab":
		p.onHex = !p.onHex
		p.err = ""
		if p.onHex {
			return p.hex.Focus()
		}
		p.hex.Blur()
		return nil
	case "enter":
		if !p.onHex {
			m.applyColor(p, choices[p.hi])
			return nil
		}
		c, ok := parseColor(p.hex.Value())
		if !ok || strings.TrimSpace(p.hex.Value()) == "" {
			p.err = "not a color: use #rrggbb or #rgb"
			return nil
		}
		m.applyColor(p, c)
		return nil
	}
	if p.onHex {
		before := p.hex.Value()
		var cmd tea.Cmd
		p.hex, cmd = p.hex.Update(k)
		if p.hex.Value() != before {
			p.err = ""
		}
		return cmd
	}
	switch key {
	case "up", "left", "k":
		p.hi = (p.hi + len(choices) - 1) % len(choices)
	case "down", "right", "j":
		p.hi = (p.hi + 1) % len(choices)
	case "home":
		p.hi = 0
	case "end":
		p.hi = len(choices) - 1
	default:
		// A name's first letter: the next choice starting with it, so
		// pressing p again goes from purple to pink.
		if len(key) == 1 {
			for i := 1; i <= len(choices); i++ {
				j := (p.hi + i) % len(choices)
				if strings.HasPrefix(choices[j], key) {
					p.hi = j
					break
				}
			}
		}
	}
	return nil
}

// applyColor sets the color ("" or "none" clears it) and closes the
// picker.
func (m *model) applyColor(p *colorPick, c string) {
	m.colorPick = nil
	if c == colorNone {
		c = ""
	}
	if p.g != nil {
		if slices.Contains(m.groups, p.g) {
			p.g.Color = c
			_ = m.saveGroups()
		}
		return
	}
	if m.indexOf(p.s) < 0 {
		return // removed meanwhile
	}
	p.s.setColor(c)
	_ = m.saveSessions()
}

// swatch is a choice's sample: two cells of the color, or a muted dot
// pair for none.
func swatch(hex string) string {
	if hex == "" {
		return stMuted.Render("··")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render("██")
}

// Lines of the picker's box, from its top border (0).
const (
	pickFirstChoice = 1 // the first color
)

// pickHexLine is the box line of the hex box.
func pickHexLine() int { return pickFirstChoice + len(colorChoices()) }

func (m *model) renderColorPick(w int) []string {
	p := m.colorPick
	title := p.title()
	bw := min(max(ansi.StringWidth(title)+4, 40), w-2)
	inner := bw - 4
	top := "┌" + title + strings.Repeat("─", max(bw-2-ansi.StringWidth(title), 0)) + "┐"
	line := func(s string) string {
		return stBorder.Render("│") + " " + fit(s, inner) + " " + stBorder.Render("│")
	}
	hiStyle := lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	out := []string{stBorder.Render(fitPlain(top, bw))}
	for i, c := range colorChoices() {
		hex := ""
		if c != colorNone {
			hex = palette[c]
		}
		text := swatch(hex) + " " + c
		if i == p.hi && !p.onHex {
			text = hiStyle.Render("› ") + swatch(hex) + " " + hiStyle.Render(c)
		} else {
			text = "  " + text
		}
		out = append(out, line(text))
	}
	label := "  hex "
	if p.onHex {
		label = hiStyle.Render("› hex ")
	}
	preview := stMuted.Render("  ")
	if h, ok := parseHex(p.hex.Value()); ok {
		preview = swatch(h)
	}
	out = append(out, line(label+preview+" "+p.hex.View()))
	if p.err != "" {
		out = append(out, line(lipgloss.NewStyle().Foreground(colError).Render("  "+p.err)))
	}
	out = append(out, stBorder.Render("└"+strings.Repeat("─", bw-2)+"┘"))
	return out
}

// colorPickAt is where the picker is drawn: the main pane's top left.
const colorPickLeft, colorPickTop = 1, 1

// overlayColorPick draws the picker over the main pane.
func (m *model) overlayColorPick(behind []string, w int) []string {
	box := m.renderColorPick(w)
	out := slices.Clone(behind)
	for i, l := range box {
		y := colorPickTop + i
		if y >= len(out) {
			break
		}
		bw := ansi.StringWidth(l)
		out[y] = ansi.Cut(behind[y], 0, colorPickLeft) + "\x1b[0m" + l + "\x1b[0m" + ansi.Cut(behind[y], colorPickLeft+bw, w)
	}
	return out
}

// colorPickMouse handles a click while the picker is open: on a color it
// applies it, on the hex box it starts typing there, and outside the box
// it closes the picker.
func (m *model) colorPickMouse(msg tea.MouseMsg) tea.Cmd {
	click, ok := msg.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft {
		return nil
	}
	p := m.colorPick
	box := m.renderColorPick(m.width - m.mainLeft())
	x := click.X - m.mainLeft() - colorPickLeft
	y := click.Y - colorPickTop
	if y < 0 || y >= len(box) || x < 0 || x >= ansi.StringWidth(box[0]) {
		m.colorPick = nil
		return nil
	}
	choices := colorChoices()
	switch i := y - pickFirstChoice; {
	case i >= 0 && i < len(choices):
		p.hi, p.onHex = i, false
		m.applyColor(p, choices[i])
	case y == pickHexLine():
		p.onHex = true
		return p.hex.Focus()
	}
	return nil
}

// [colors] ------------------------------------------------------------------------------

// loadColorsConfig reads [colors] from the config file: the palette.
func loadColorsConfig() (map[string]string, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultPaletteMap(), nil
	}
	if err != nil {
		return defaultPaletteMap(), err
	}
	return parseColorsConfig(string(data))
}

// parseColorsConfig is the palette with [colors] applied: each key is a
// palette name and each value a hex color.
func parseColorsConfig(text string) (map[string]string, error) {
	out := defaultPaletteMap()
	var cfg struct {
		Colors map[string]string `toml:"colors"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return out, fmt.Errorf("%s: %w", configPath(), err)
	}
	names := make([]string, 0, len(cfg.Colors))
	for k := range cfg.Colors {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		name := strings.ToLower(k)
		if _, ok := out[name]; !ok {
			return defaultPaletteMap(), fmt.Errorf("%s: colors.%s isn't a palette name (%s); give other colors as hex in the picker", configPath(), k, strings.Join(paletteNames(), ", "))
		}
		h, ok := parseHex(cfg.Colors[k])
		if !ok {
			return defaultPaletteMap(), fmt.Errorf("%s: colors.%s must be a hex color such as \"#e06c75\", not %q", configPath(), k, cfg.Colors[k])
		}
		out[name] = h
	}
	return out, nil
}
