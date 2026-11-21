// deckhand is a terminal UI for running several AI agent CLIs side by side
// on Windows. The left pane lists sessions with live status; the right pane
// is the selected session's terminal.
package main

import (
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hook":
			os.Exit(runHook(os.Args[2:]))
		case "statusline":
			os.Exit(runStatusLine(os.Args[2:]))
		case "config":
			os.Exit(runConfig())
		case "notify-test":
			if err := sendTestNotification(); err != nil {
				fmt.Fprintln(os.Stderr, "deckhand: notification failed:", err)
				os.Exit(1)
			}
			fmt.Println("Sent a test notification.")
			return
		case "version", "--version", "-v":
			fmt.Println("deckhand", version)
			return
		case "help", "--help", "-h":
			fmt.Println(`deckhand - run several AI agent CLIs in one terminal

Usage:
  deckhand            start the TUI
  deckhand config     print the config file path (creates a commented sample).
                      To change settings without editing it, press c in deckhand.
  deckhand notify-test  show a test desktop notification
  deckhand version    print the version
  deckhand hook       (internal) receives Claude Code hook events
  deckhand statusline (internal) records Claude Code's status line JSON,
                      then runs your own statusLine command

State is kept in %LOCALAPPDATA%\deckhand (override with DECKHAND_HOME).`)
			return
		}
	}

	cfgErr := reloadConfig()
	m := newModel()
	if cfgErr != nil {
		m.setFlash("Config error: " + cfgErr.Error())
		m.flashAt = m.flashAt.Add(10 * time.Second) // keep it up longer
	}
	enableKeyRecords()
	_, err := tea.NewProgram(m).Run()
	disableKeyRecords()
	if err != nil {
		fmt.Fprintln(os.Stderr, "deckhand:", err)
		os.Exit(1)
	}
}
