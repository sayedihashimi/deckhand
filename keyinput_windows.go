package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows Terminal sends keys to the console as win32-input-mode records,
// which carry the modifier state. In VT input mode (what Bubble Tea uses)
// the console turns them back into plain VT, and shift+enter becomes "\r".
// Asking the console for win32-input-mode (DECSET 9001) makes it pass the
// records on, so deckhand sees shift+enter, ctrl+enter and so on. See
// wantKeyRecords for when.
func enableKeyRecords() {
	debugf("startup: WT_SESSION set=%v TERM_PROGRAM=%q DECKHAND_SESSION_ID set=%v DECKHAND_KEY_RECORDS=%q keyRecords=%v",
		os.Getenv("WT_SESSION") != "", os.Getenv("TERM_PROGRAM"), os.Getenv("DECKHAND_SESSION_ID") != "",
		os.Getenv("DECKHAND_KEY_RECORDS"), wantKeyRecords(os.Getenv))
	if wantKeyRecords(os.Getenv) {
		writeConsoleVT("\x1b[?9001h")
	}
}

func disableKeyRecords() {
	if wantKeyRecords(os.Getenv) {
		writeConsoleVT("\x1b[?9001l")
	}
}

// writeConsoleVT writes a control sequence to the console, with VT
// processing on so the console reads it instead of printing it.
func writeConsoleVT(seq string) {
	out := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(out, &mode); err != nil {
		return // not a console
	}
	_ = windows.SetConsoleMode(out, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	_, _ = os.Stdout.WriteString(seq)
	_ = windows.SetConsoleMode(out, mode)
}
