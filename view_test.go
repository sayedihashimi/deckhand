package main

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// The selected session's three rows must be highlighted across the whole
// width, not just between the inner styled pieces.
func TestSelectedRowBackground(t *testing.T) {
	m := testModel("api", "docs")
	m.sessions[0].info = sessionInfo{git: gitInfo{ok: true, branch: "feature/login", dirty: true}, model: "claude-opus-5-5", ctxTokens: 84000}
	m.sessions[0].status = StatusWaiting
	m.sessions[0].detail = "permission: Bash"
	m.sel = 0
	const w, h = 30, 12
	lines := m.renderSidebar(w, h)

	emu := vt.NewEmulator(w, h)
	_, _ = emu.Write([]byte(strings.Join(lines, "\r\n")))
	want := color.RGBA{42, 47, 69, 255}
	for y := 2; y < 2+rowsPerSession; y++ { // after the title and the rule
		for x := 1; x < w; x++ { // column 0 is the ▌ marker
			c := emu.CellAt(x, y)
			if c == nil || c.Style.Bg == nil {
				t.Fatalf("row %d col %d has no background; row: %q", y, x, lines[y])
			}
			r, g, b, _ := c.Style.Bg.RGBA()
			if got := (color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}); got != want {
				t.Fatalf("row %d col %d background %v, want %v", y, x, got, want)
			}
		}
	}
	// The unselected session's rows have no background.
	if c := emu.CellAt(5, 2+rowsPerSession); c != nil && c.Style.Bg != nil {
		t.Errorf("unselected row has background %v", c.Style.Bg)
	}
}
