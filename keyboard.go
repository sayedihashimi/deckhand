package main

import (
	"fmt"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// keyProtocol tracks the keyboard enhancements an agent asks its terminal
// for, so that keys the legacy encoding can't tell apart (shift+enter,
// ctrl+enter, ...) reach it in a form it reads. x/vt only sends legacy
// sequences, where shift+enter is a plain "\r" and Claude Code submits.
//
// Claude Code pushes kitty keyboard flags (CSI > 5 u) and turns on xterm
// modifyOtherKeys (CSI > 4 ; 2 m) at startup. deckhand doesn't answer the
// kitty query (CSI ? u), so apps keep sending and reading legacy keys for
// everything else; only the keys below are encoded differently.
type keyProtocol struct {
	mu              sync.Mutex
	kitty           [2][]int // kitty flag stacks: main screen, alternate screen
	modifyOtherKeys int      // xterm modifyOtherKeys level
}

// Kitty keeps at most this many entries per stack, dropping the oldest.
const maxKittyStack = 64

// register hooks the protocol's CSI sequences into the emulator. Handlers
// run while the emulator processes the agent's output.
func (p *keyProtocol) register(emu *vt.Emulator) {
	screen := func() int {
		if emu.IsAltScreen() {
			return 1
		}
		return 0
	}
	// CSI > flags u: push flags.
	emu.RegisterCsiHandler(ansi.Command('>', 0, 'u'), func(params ansi.Params) bool {
		flags, _, _ := params.Param(0, 0)
		p.push(screen(), flags)
		return true
	})
	// CSI < n u: pop n entries.
	emu.RegisterCsiHandler(ansi.Command('<', 0, 'u'), func(params ansi.Params) bool {
		n, _, _ := params.Param(0, 1)
		p.pop(screen(), n)
		return true
	})
	// CSI = flags ; mode u: set (1), add (2) or remove (3) flags in place.
	emu.RegisterCsiHandler(ansi.Command('=', 0, 'u'), func(params ansi.Params) bool {
		flags, _, _ := params.Param(0, 0)
		mode, _, _ := params.Param(1, 1)
		p.set(screen(), flags, mode)
		return true
	})
	// CSI > 4 ; level m: xterm modifyOtherKeys (CSI > 4 m resets it).
	emu.RegisterCsiHandler(ansi.Command('>', 0, 'm'), func(params ansi.Params) bool {
		res, _, _ := params.Param(0, 0)
		if res != 4 {
			return false
		}
		level, _, _ := params.Param(1, 0)
		p.mu.Lock()
		p.modifyOtherKeys = level
		p.mu.Unlock()
		return true
	})
}

func (p *keyProtocol) push(scr, flags int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := append(p.kitty[scr], flags)
	if len(st) > maxKittyStack {
		st = st[len(st)-maxKittyStack:]
	}
	p.kitty[scr] = st
}

func (p *keyProtocol) pop(scr, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.kitty[scr]
	if n >= len(st) {
		st = st[:0]
	} else if n > 0 {
		st = st[:len(st)-n]
	}
	p.kitty[scr] = st
}

func (p *keyProtocol) set(scr, flags, mode int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.kitty[scr]
	if len(st) == 0 {
		st = append(st, 0)
	}
	cur := &st[len(st)-1]
	switch mode {
	case 1:
		*cur = flags
	case 2:
		*cur |= flags
	case 3:
		*cur &^= flags
	}
	p.kitty[scr] = st
}

// state returns the kitty flags in effect on the given screen and the
// modifyOtherKeys level.
func (p *keyProtocol) state(alt bool) (kittyFlags, modifyOtherKeys int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	scr := 0
	if alt {
		scr = 1
	}
	if st := p.kitty[scr]; len(st) > 0 {
		kittyFlags = st[len(st)-1]
	}
	return kittyFlags, p.modifyOtherKeys
}

// sendKey writes a key to the emulator's input, which goes to the agent.
func sendKey(emu *vt.SafeEmulator, keys *keyProtocol, k uv.KeyPressEvent) {
	if keys != nil {
		kitty, mok := keys.state(emu.IsAltScreen())
		if seq, ok := encodeExtendedKey(k, kitty, mok); ok {
			emu.SendText(seq)
			return
		}
	}
	emu.SendKey(k)
}

// encodeExtendedKey returns the sequence for a key the legacy encoding
// can't express, in the protocol the app asked for: kitty CSI u when it
// pushed any kitty flags, else xterm modifyOtherKeys, else the plain key
// (as xterm does; x/vt would send nothing). It returns false for every
// other key.
//
// Covered: enter with shift and/or ctrl (alt on its own stays ESC CR, which
// legacy already tells apart), and ctrl+tab / ctrl+shift+tab. shift+tab is
// CSI Z in every mode.
func encodeExtendedKey(k uv.KeyPressEvent, kittyFlags, modifyOtherKeys int) (string, bool) {
	mods := k.Mod & (uv.ModShift | uv.ModAlt | uv.ModCtrl | uv.ModSuper)
	var code int
	var legacy string
	switch k.Code {
	case uv.KeyEnter:
		if mods&(uv.ModShift|uv.ModCtrl|uv.ModSuper) == 0 {
			return "", false
		}
		code, legacy = 13, "\r"
	case uv.KeyTab:
		if mods&(uv.ModCtrl|uv.ModSuper) == 0 {
			return "", false
		}
		code, legacy = 9, "\t"
	default:
		return "", false
	}
	m := 1
	if mods&uv.ModShift != 0 {
		m += 1
	}
	if mods&uv.ModAlt != 0 {
		m += 2
	}
	if mods&uv.ModCtrl != 0 {
		m += 4
	}
	if mods&uv.ModSuper != 0 {
		m += 8
	}
	switch {
	case kittyFlags != 0:
		return fmt.Sprintf("\x1b[%d;%du", code, m), true
	case modifyOtherKeys > 0:
		return fmt.Sprintf("\x1b[27;%d;%d~", m, code), true
	case mods&uv.ModAlt != 0:
		return "\x1b" + legacy, true
	}
	return legacy, true
}

// wantKeyRecords reports whether deckhand should ask its console for
// win32-input-mode (see enableKeyRecords): only directly in Windows
// Terminal (WT_SESSION set). In this mode the console also passes on raw VT
// key sequences from terminals that don't send records, and Bubble Tea's
// reader garbles some of those (an arrow arrives as "up" plus stray
// characters). Windows Terminal sends keys as records, so it's fine there,
// but a deckhand started inside a deckhand session (DECKHAND_SESSION_ID
// set) runs under deckhand's own emulator, which sends plain VT, even
// though it inherits WT_SESSION.
func wantKeyRecords(getenv func(string) string) bool {
	return getenv("WT_SESSION") != "" && getenv("DECKHAND_SESSION_ID") == ""
}
