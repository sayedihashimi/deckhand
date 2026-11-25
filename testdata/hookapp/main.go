// hookapp is a fake claude.exe for the e2e tests. It reads the hook command
// from the --settings file deckhand passes, writes a small transcript, and
// sends hook events. Then it waits until it's killed.
//
// Without a script it sends one turn: start, prompt, a permission request
// and stop. A hookapp.txt in the working folder replaces that with a script,
// one command per line:
//
//	say <text>                     print a line (\e is ESC, for colours)
//	print <text>                   print without a newline (\e is ESC, \a is BEL)
//	reply <tokens> <text>          add an assistant reply to the transcript
//	                               (\n starts a new line)
//	title custom|ai <text>         add a custom-title (/rename) or ai-title
//	                               line to the transcript
//	event <Name> [Tool] [| input]  send a hook event; input becomes the
//	                               tool's command (or file_path for Edit/Write/Read)
//	statusline <usd> [<window> <tokens>]
//	                               run the statusLine command with a payload of
//	                               that cost (and context window size and tokens
//	                               used), and print "statusline: <its output>"
//	                               when it prints something
//	sleep <ms>
//	input <file>                   read the keyboard raw (as VT input) and
//	                               append everything typed to <file>
//
// With --fork-session (deckhand's f), it acts as Claude does: the
// conversation from --resume continues under a new session ID, the one from
// --session-id (or a made-up one without it), and it prints
// "hookapp forked <from> as <new>". A hookapp-fork.txt in the working
// folder is then the script, if there is one.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

var (
	command, statusCommand, sessionID, transcript string
)

func main() {
	var settings, newID, resumeID string
	for i := 1; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--settings":
			settings = os.Args[i+1]
		case "--session-id":
			newID = os.Args[i+1]
		case "--resume":
			resumeID = os.Args[i+1]
		}
	}
	sessionID = newID
	if resumeID != "" {
		sessionID = resumeID
	}
	scriptFile := "hookapp.txt"
	if resumeID != "" && slices.Contains(os.Args[1:], "--fork-session") {
		if _, err := os.Stat("hookapp-fork.txt"); err == nil {
			scriptFile = "hookapp-fork.txt"
		}
		sessionID = newID
		if sessionID == "" {
			sessionID = fmt.Sprintf("fork-%d", time.Now().UnixNano())
		}
		fmt.Println("hookapp forked", resumeID, "as", sessionID)
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
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	_ = json.Unmarshal(data, &cfg)
	statusCommand = cfg.StatusLine.Command
	command = cfg.Hooks["Stop"][0].Hooks[0].Command
	transcript = filepath.Join(filepath.Dir(settings), "transcript.jsonl")
	_ = os.WriteFile(transcript, []byte(`{"type":"user","message":{"content":"fix it"}}`+"\n"), 0o600)

	if script, err := os.ReadFile(scriptFile); err == nil {
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
		case "print":
			fmt.Print(strings.NewReplacer(`\e`, "\x1b", `\a`, "\a").Replace(rest))
		case "reply":
			n, text, _ := strings.Cut(rest, " ")
			tokens, _ := strconv.Atoi(n)
			reply(tokens, strings.ReplaceAll(text, `\n`, "\n"))
		case "title":
			kind, text, _ := strings.Cut(rest, " ")
			title(kind, text)
		case "statusline":
			statusLine(strings.Fields(rest))
		case "sleep":
			ms, _ := strconv.Atoi(rest)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		case "input":
			go recordInput(strings.TrimSpace(rest))
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

// title adds a custom-title (/rename, or a launch name) or an ai-title
// line to the transcript.
func title(kind, text string) {
	entry := map[string]any{"type": "ai-title", "aiTitle": text, "sessionId": sessionID}
	if kind == "custom" {
		entry = map[string]any{"type": "custom-title", "customTitle": text, "sessionId": sessionID}
	}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
}

// statusLine runs the statusLine command as Claude Code would, with a
// payload of the cost and context in args.
func statusLine(args []string) {
	if statusCommand == "" {
		fmt.Println("statusline: none set")
		return
	}
	num := func(i int) float64 {
		if i >= len(args) {
			return 0
		}
		v, _ := strconv.ParseFloat(args[i], 64)
		return v
	}
	wd, _ := os.Getwd()
	p := map[string]any{
		"session_id": sessionID, "transcript_path": transcript, "cwd": wd,
		"model":     map[string]any{"id": "claude-opus-5-5", "display_name": "Opus"},
		"workspace": map[string]any{"current_dir": wd, "project_dir": wd},
		"cost":      map[string]any{"total_cost_usd": num(0)},
	}
	if size := num(1); size > 0 {
		used := num(2)
		p["context_window"] = map[string]any{
			"context_window_size": size, "total_input_tokens": used,
			"used_percentage": used * 100 / size, "remaining_percentage": 100 - used*100/size,
			"current_usage": map[string]any{"input_tokens": 2, "cache_creation_input_tokens": 0, "cache_read_input_tokens": used - 2},
		}
	}
	body, _ := json.Marshal(p)
	parts := strings.Fields(statusCommand)
	cmd := exec.Command(strings.Trim(parts[0], `"`), parts[1:]...)
	cmd.Stdin = strings.NewReader(string(body))
	out, err := cmd.Output()
	if err != nil {
		fmt.Println("statusline: error:", err)
		return
	}
	if line := strings.TrimSpace(string(out)); line != "" { // scripts with no status line of their own stay quiet
		fmt.Println("statusline:", line)
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

// recordInput puts the console in raw VT input mode, as Claude Code does,
// and appends every byte typed to path.
func recordInput(path string) {
	in := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(in, &mode); err == nil {
		mode &^= windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT
		mode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
		_ = windows.SetConsoleMode(in, mode)
	}
	buf := make([]byte, 1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if f, ferr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); ferr == nil {
				_, _ = f.Write(buf[:n])
				_ = f.Close()
			}
		}
		if err != nil {
			return
		}
	}
}
