// keyapp is a stand-in for Claude Code's keyboard handling in deckhand's e2e
// tests: it asks for the kitty keyboard protocol and xterm modifyOtherKeys
// the way Claude does at startup, and bracketed paste, then prints every
// input sequence it gets.
package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func main() {
	in := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(in, &mode); err == nil {
		mode &^= windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT
		mode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
		_ = windows.SetConsoleMode(in, mode)
	}
	out := windows.Handle(os.Stdout.Fd())
	if err := windows.GetConsoleMode(out, &mode); err == nil {
		_ = windows.SetConsoleMode(out, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}

	fmt.Print("\x1b[<u\x1b[>5u\x1b[>4;2m\x1b[?2004h")
	fmt.Print("keyapp ready\r\n")
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		s := string(buf[:n])
		if s == "q" {
			fmt.Print("\x1b[<u\x1b[>4m\x1b[?2004l")
			return
		}
		fmt.Printf("got %q\r\n", s)
	}
}
