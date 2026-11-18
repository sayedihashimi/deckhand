package main

// stringFilter works around a parser bug in charmbracelet/x/ansi v0.11.9
// (charmbracelet/x#848): inside an OSC or DCS string it treats byte 0x9C as
// the 8-bit String Terminator even when the byte is part of a UTF-8
// character. Claude's idle window title, ESC ] 0 ; ✳ name BEL, has one:
// ✳ (U+2733) is E2 9C B3. The parser ends the title at 0x9C and prints the
// rest (" name") at the cursor, which is in Claude's input box. SOS, PM and
// APC strings are worse: any UTF-8 character ends them.
//
// The filter sits between the agent's output and the emulator. Inside one
// of those strings it drops each UTF-8 character the parser would misread:
// in OSC and DCS, a character with 0x9C among its bytes; in SOS, PM and APC,
// every non-ASCII character. Everything else passes through unchanged,
// including a real 8-bit ST (a 0x9C that isn't part of a character). Its
// state carries over between calls, because a sequence can be split across
// two reads.
//
// Only 7-bit introducers (ESC ], ESC P, ESC X, ESC ^, ESC _) start a
// string: ConPTY re-encodes the agent's output and doesn't send 8-bit C1
// controls. Remove the filter once a fixed x/ansi is released.
type stringFilter struct {
	state byte    // filterGround, filterEsc or filterString
	kind  byte    // the string's introducer: ']', 'P', 'X', '^' or '_'
	pend  [4]byte // a UTF-8 character in progress inside a string
	npend int
	need  int  // continuation bytes still expected for pend
	bad   bool // pend has a 0x9C
}

const (
	filterGround = iota
	filterEsc
	filterString
)

// Filter appends the filtered form of p to dst and returns it.
func (f *stringFilter) Filter(dst, p []byte) []byte {
	for _, b := range p {
		dst = f.step(dst, b)
	}
	return dst
}

func (f *stringFilter) step(dst []byte, b byte) []byte {
	switch f.state {
	case filterGround:
		if b == 0x1b {
			f.state = filterEsc
		}
		return append(dst, b)
	case filterEsc:
		switch b {
		case ']', 'P', 'X', '^', '_':
			f.state, f.kind = filterString, b
		case 0x1b:
		default:
			f.state = filterGround
		}
		return append(dst, b)
	}

	// Inside a string.
	if f.need > 0 {
		if b >= 0x80 && b <= 0xbf {
			f.pend[f.npend] = b
			f.npend++
			f.need--
			if b == 0x9c {
				f.bad = true
			}
			if f.need == 0 {
				dst = f.flush(dst)
			}
			return dst
		}
		// Not a continuation byte: the character is cut short. Pass on
		// what the parser handles safely, then look at b on its own.
		dst = f.flush(dst)
	}
	switch {
	case b >= 0xc2 && b <= 0xf4:
		f.pend[0], f.npend, f.bad = b, 1, false
		switch {
		case b < 0xe0:
			f.need = 1
		case b < 0xf0:
			f.need = 2
		default:
			f.need = 3
		}
		return dst
	case b == 0x1b:
		f.state = filterEsc
	case b == 0x07 && f.kind == ']', b == 0x18, b == 0x1a, b == 0x9c:
		// BEL (OSC only), CAN, SUB and a real ST end the string.
		f.state = filterGround
	}
	return append(dst, b)
}

// flush passes on the pending character, unless the parser would misread it.
func (f *stringFilter) flush(dst []byte) []byte {
	keep := !f.bad && (f.kind == ']' || f.kind == 'P')
	if keep {
		dst = append(dst, f.pend[:f.npend]...)
	}
	f.npend, f.need, f.bad = 0, 0, false
	return dst
}
