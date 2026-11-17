// argsapp stands in for claude.exe in deckhand's e2e tests: it prints each
// argument on its own line, then waits so the screen can be read. With
// ARGSAPP_LOG set it also appends its arguments, folder and worktree
// variables to that file as a JSON line, for paths too long for the screen.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	for _, a := range os.Args[1:] {
		fmt.Printf("arg: [%s]\n", a)
	}
	if p := os.Getenv("ARGSAPP_LOG"); p != "" {
		cwd, _ := os.Getwd()
		line, _ := json.Marshal(map[string]any{
			"args":     os.Args[1:],
			"cwd":      cwd,
			"worktree": os.Getenv("DECKHAND_WORKTREE"),
			"branch":   os.Getenv("DECKHAND_BRANCH"),
		})
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	fmt.Println("argsapp done")
	time.Sleep(10 * time.Minute)
}
