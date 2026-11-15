// mouseapp is a stand-in for a fullscreen TUI in deckhand's e2e tests: it
// switches to the alternate screen, turns on SGR mouse reporting and prints
// every input sequence it receives.
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

	fmt.Print("\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[H")
	fmt.Print("mouseapp ready\r\n")
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		s := string(buf[:n])
		if s == "q" {
			fmt.Print("\x1b[?1000l\x1b[?1006l\x1b[?1049l")
			return
		}
		fmt.Printf("got %q\r\n", s)
	}
}
