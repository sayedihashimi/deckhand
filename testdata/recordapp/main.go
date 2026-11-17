// recordapp stands in for an editor or terminal in deckhand's e2e tests. Its
// first argument is a log file; it appends one line per run with its working
// folder and the rest of its arguments, as JSON, then exits.
package main

import (
	"encoding/json"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	cwd, _ := os.Getwd()
	line, _ := json.Marshal(map[string]any{"cwd": cwd, "args": os.Args[2:]})
	f, err := os.OpenFile(os.Args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
