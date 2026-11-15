package main

import (
	"os"
	"path/filepath"
	"strings"
)

// dataDir is where deckhand keeps its state: %LOCALAPPDATA%\deckhand on
// Windows. DECKHAND_HOME overrides it (used by tests).
func dataDir() string {
	if d := os.Getenv("DECKHAND_HOME"); d != "" {
		return d
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "deckhand")
}

func sessionDir(id string) string { return filepath.Join(dataDir(), "sessions", id) }

// hookCommand is the command Claude Code runs for each hook. Forward slashes
// and no quotes (when possible) keep it valid in bash, cmd and PowerShell.
func hookCommand(sessionID string) string {
	exe, err := os.Executable()
	if err != nil {
		exe = "deckhand"
	}
	exe = filepath.ToSlash(exe)
	if strings.ContainsAny(exe, " \t") {
		exe = `"` + exe + `"`
	}
	return exe + " hook --session " + sessionID
}
