package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// The cursor sits after "> " on row 3 of a small screen, like Claude's input
// box, when the string arrives.
const inputRow = "\x1b[3;1H> "

// rowText is row y of the emulator's screen, with trailing blanks trimmed.
func rowText(emu *vt.SafeEmulator, y int) string {
	var b strings.Builder
	for x := 0; x < emu.Width(); x++ {
		if c := emu.CellAt(x, y); c != nil && c.Content != "" {
			b.WriteString(c.Content)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// feed writes chunks through one filter into a fresh emulator, as readLoop
// does, and returns the emulator and the last window title it set.
func feed(chunks ...[]byte) (*vt.SafeEmulator, string) {
	emu := vt.NewSafeEmulator(30, 5)
	var title string
	emu.Emulator.SetCallbacks(vt.Callbacks{Title: func(t string) { title = t }})
	var f stringFilter
	for _, c := range chunks {
		_, _ = emu.Write(f.Filter(nil, c))
	}
	return emu, title
}

// splits returns s whole and split in two at every byte.
func splits(s string) [][][]byte {
	out := [][][]byte{{[]byte(s)}}
	for i := 1; i < len(s); i++ {
		out = append(out, [][]byte{[]byte(s[:i]), []byte(s[i:])})
	}
	return out
}

func TestStringFilterStrings(t *testing.T) {
	cases := []struct {
		name, seq, title string
	}{
		{"osc bel", "\x1b]0;✳ my-session\a", " my-session"},
		{"osc esc st", "\x1b]0;✳ my-session\x1b\\", " my-session"},
		{"osc 8-bit st", "\x1b]0;✳ my-session\x9c", " my-session"},
		{"osc spinner kept", "\x1b]0;◐ my-session\a", "◐ my-session"},
		{"osc two dingbats", "\x1b]2;✳✜ my-session\a", " my-session"},
		{"dcs", "\x1bPq✳ my-session\x1b\\", ""},
		{"apc", "\x1b_G✳ é my-session\x1b\\", ""},
		{"pm", "\x1b^✳ my-session\x1b\\", ""},
		{"sos", "\x1bX✳ my-session\x1b\\", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunks := range splits(inputRow + tc.seq + "x") {
				emu, title := feed(chunks...)
				if got := rowText(emu, 2); got != "> x" {
					t.Fatalf("split %q: input row = %q, want %q", chunks, got, "> x")
				}
				if tc.title != "" && title != tc.title {
					t.Fatalf("split %q: title = %q, want %q", chunks, title, tc.title)
				}
			}
		})
	}
}

// Text outside strings, including characters with 0x9C, is untouched.
func TestStringFilterPassesText(t *testing.T) {
	in := "plain ✳ text\x1b[31mred\x1b[0m ✓ ok\r\n"
	for _, chunks := range splits(in) {
		var f stringFilter
		var out []byte
		for _, c := range chunks {
			out = f.Filter(out, c)
		}
		if !bytes.Equal(out, []byte(in)) {
			t.Fatalf("split %q: got %q, want %q", chunks, out, in)
		}
	}
	emu, _ := feed([]byte(inputRow + "✳ ok"))
	if got := rowText(emu, 2); got != "> ✳ ok" {
		t.Fatalf("input row = %q", got)
	}
}

// After a string ends (or is cut off by CAN), later text is printed again.
func TestStringFilterReturnsToGround(t *testing.T) {
	emu, _ := feed([]byte(inputRow + "\x1b]0;a\x18✳b"))
	if got := rowText(emu, 2); got != "> ✳b" {
		t.Fatalf("input row = %q", got)
	}
}

// The parser bug the filter works around (charmbracelet/x#848). If this
// fails after an x/ansi update, the bug is fixed upstream: remove
// stringFilter.
func TestUpstreamOSCBugStillThere(t *testing.T) {
	emu := vt.NewSafeEmulator(30, 5)
	_, _ = emu.Write([]byte(inputRow + "\x1b]0;✳ my-session\a"))
	if got := rowText(emu, 2); !strings.Contains(got, "my-session") {
		t.Skipf("x/ansi no longer prints the rest of the title (row %q); stringFilter may be unneeded", got)
	}
}
