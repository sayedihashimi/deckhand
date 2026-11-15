// deckhand is a terminal UI for running several AI agent CLIs side by side
// on Windows. The left pane lists sessions with live status; the right pane
// is the selected session's terminal.
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hook":
			os.Exit(runHook(os.Args[2:]))
		case "version", "--version", "-v":
			fmt.Println("deckhand", version)
			return
		case "help", "--help", "-h":
			fmt.Println(`deckhand - run several AI agent CLIs in one terminal

Usage:
  deckhand            start the TUI
  deckhand version    print the version
  deckhand hook       (internal) receives Claude Code hook events

State is kept in %LOCALAPPDATA%\deckhand (override with DECKHAND_HOME).`)
			return
		}
	}

	if _, err := tea.NewProgram(newModel()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "deckhand:", err)
		os.Exit(1)
	}
}
