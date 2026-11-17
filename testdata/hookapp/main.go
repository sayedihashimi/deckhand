// hookapp is a fake claude.exe for the e2e tests. It reads the hook command
// from the --settings file deckhand passes, writes a small transcript, and
// sends hook events. Then it waits until it's killed.
//
// Without a script it sends one turn: start, prompt, a permission request
// and stop. A hookapp.txt in the working folder replaces that with a script,
// one command per line:
//
//	say <text>                     print a line (\e is ESC, for colours)
//	reply <tokens> <text>          add an assistant reply to the transcript
//	event <Name> [Tool] [| input]  send a hook event; input becomes the
//	                               tool's command (or file_path for Edit/Write/Read)
//	sleep <ms>
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	command, sessionID, transcript string
)

func main() {
	var settings string
	for i := 1; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--settings":
			settings = os.Args[i+1]
		case "--session-id", "--resume":
			sessionID = os.Args[i+1]
		}
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		fmt.Println("hookapp: no settings:", err)
		time.Sleep(time.Hour)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	_ = json.Unmarshal(data, &cfg)
	command = cfg.Hooks["Stop"][0].Hooks[0].Command
	transcript = filepath.Join(filepath.Dir(settings), "transcript.jsonl")
	_ = os.WriteFile(transcript, []byte(`{"type":"user","message":{"content":"fix it"}}`+"\n"), 0o600)

	if script, err := os.ReadFile("hookapp.txt"); err == nil {
		runScript(string(script))
	} else {
		reply(83002, "Fixed the bug.")
		send("SessionStart", nil)
		send("UserPromptSubmit", nil)
		send("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}})
		send("PostToolUse", map[string]any{"tool_name": "Bash"})
		send("Stop", nil)
		fmt.Println("hookapp done")
	}
	time.Sleep(time.Hour)
}

func runScript(script string) {
	sc := bufio.NewScanner(strings.NewReader(script))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		verb, rest, _ := strings.Cut(line, " ")
		switch verb {
		case "say":
			fmt.Println(strings.ReplaceAll(rest, `\e`, "\x1b"))
		case "reply":
			n, text, _ := strings.Cut(rest, " ")
			tokens, _ := strconv.Atoi(n)
			reply(tokens, text)
		case "sleep":
			ms, _ := strconv.Atoi(rest)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		case "event":
			head, input, hasInput := strings.Cut(rest, "|")
			f := strings.Fields(head)
			if len(f) == 0 {
				continue
			}
			extra := map[string]any{}
			if len(f) > 1 {
				extra["tool_name"] = f[1]
			}
			if hasInput {
				key := "command"
				if len(f) > 1 && (f[1] == "Edit" || f[1] == "Write" || f[1] == "Read") {
					key = "file_path"
				}
				extra["tool_input"] = map[string]any{key: strings.TrimSpace(input)}
			}
			sendQuiet(f[0], extra)
		}
	}
}

func reply(tokens int, text string) {
	entry := map[string]any{
		"type": "assistant", "isSidechain": false,
		"message": map[string]any{
			"model":   "claude-opus-5-5",
			"content": []map[string]any{{"type": "text", "text": text}},
			"usage":   map[string]any{"input_tokens": 2, "cache_creation_input_tokens": 1000, "cache_read_input_tokens": tokens - 1002},
		},
	}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
}

func hook(event string, extra map[string]any) {
	p := map[string]any{"hook_event_name": event, "session_id": sessionID, "transcript_path": transcript}
	for k, v := range extra {
		p[k] = v
	}
	body, _ := json.Marshal(p)
	parts := strings.Fields(command)
	cmd := exec.Command(strings.Trim(parts[0], `"`), parts[1:]...)
	cmd.Stdin = strings.NewReader(string(body))
	_ = cmd.Run()
}

// send runs a hook, says so, and pauses so deckhand sees each state.
func send(event string, extra map[string]any) {
	hook(event, extra)
	fmt.Println("hookapp sent", event)
	time.Sleep(time.Second)
}

// sendQuiet runs a hook without printing (scripts print what they want).
func sendQuiet(event string, extra map[string]any) {
	hook(event, extra)
	time.Sleep(400 * time.Millisecond)
}
