package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The statusLine recorder. Claude Code runs a statusLine command after
// each reply and pipes it session JSON that includes what the session has
// cost (cost.total_cost_usd) and how full its context window is. deckhand
// sets its own statusLine in the per-session --settings file, so it gets
// that JSON without touching ~/.claude/settings.json:
//
//	deckhand statusline --session <id>
//
// It saves the numbers to the session's folder (statusline.json), then
// runs the user's own statusLine command, if they have one, with the same
// JSON and passes its output back, so their status line looks the same.
// A statusLine in --settings replaces the one in the user's settings files
// (command-line settings take precedence over user and project settings),
// which is why it has to be passed through rather than left alone.

// statusRecord is what deckhand keeps from a status line payload.
type statusRecord struct {
	Seq             int64    `json:"seq"`
	ClaudeSessionID string   `json:"session_id,omitempty"`
	Model           string   `json:"model,omitempty"`
	CostUSD         *float64 `json:"cost_usd,omitempty"`
	CtxWindow       int      `json:"context_window_size,omitempty"`
	CtxUsedPct      *float64 `json:"used_percentage,omitempty"`
	CtxTokens       int      `json:"context_tokens,omitempty"` // input + cache tokens of the last reply
	AddedDirs       []string `json:"added_dirs,omitempty"`     // the session's extra working folders (--add-dir, /add-dir)
}

// statusPayload is the part of Claude Code's status line JSON deckhand
// reads.
type statusPayload struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Model     struct {
		ID string `json:"id"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string   `json:"current_dir"`
		ProjectDir string   `json:"project_dir"`
		AddedDirs  []string `json:"added_dirs"`
	} `json:"workspace"`
	Cost *struct {
		TotalCostUSD *float64 `json:"total_cost_usd"`
	} `json:"cost"`
	ContextWindow *struct {
		TotalInputTokens  int      `json:"total_input_tokens"`
		ContextWindowSize int      `json:"context_window_size"`
		UsedPercentage    *float64 `json:"used_percentage"`
		CurrentUsage      *struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
}

func (p statusPayload) record(seq int64) statusRecord {
	r := statusRecord{Seq: seq, ClaudeSessionID: p.SessionID, Model: p.Model.ID, AddedDirs: p.Workspace.AddedDirs}
	if p.Cost != nil {
		r.CostUSD = p.Cost.TotalCostUSD
	}
	if cw := p.ContextWindow; cw != nil {
		r.CtxWindow = cw.ContextWindowSize
		r.CtxUsedPct = cw.UsedPercentage
		r.CtxTokens = cw.TotalInputTokens
		if u := cw.CurrentUsage; u != nil {
			if n := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens; n > 0 {
				r.CtxTokens = n
			}
		}
	}
	return r
}

const statusFileName = "statusline.json"

// runStatusLine implements `deckhand statusline --session <id>`. It
// always records first, so a slow or failing user command can't lose the
// numbers. Its exit code is the user's command's, since Claude Code blanks
// the status line when the command fails, as it would without deckhand.
func runStatusLine(args []string) int {
	id := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--session" {
			id = args[i+1]
		}
	}
	if id == "" {
		id = os.Getenv("DECKHAND_SESSION_ID")
	}
	data, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	var p statusPayload
	if json.Unmarshal(data, &p) == nil && id != "" && validID(id) {
		if err := writeStatusRecord(id, p.record(time.Now().UnixNano())); err != nil {
			fmt.Fprintln(os.Stderr, "deckhand statusline:", err)
		}
	}
	sl, ok := userStatusLine(statusDirs(p))
	if !ok {
		return 0 // no status line of their own: show nothing, as before
	}
	return runUserStatusLine(sl.Command, data, os.Stdout, os.Stderr)
}

// statusDirs are the folders whose .claude settings can set the status
// line: the project folder Claude was started in.
func statusDirs(p statusPayload) []string {
	for _, d := range []string{p.Workspace.ProjectDir, p.Workspace.CurrentDir, p.Cwd} {
		if d != "" {
			return []string{d}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return []string{wd}
	}
	return nil
}

func writeStatusRecord(id string, r statusRecord) error {
	dir := sessionDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line, _ := json.Marshal(r)
	tmp := filepath.Join(dir, fmt.Sprintf("statusline.%d.tmp", r.Seq))
	if err := os.WriteFile(tmp, line, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, statusFileName))
}

// readStatusRecord returns the latest status line record of a session.
func readStatusRecord(id string) (statusRecord, error) {
	var r statusRecord
	data, err := os.ReadFile(filepath.Join(sessionDir(id), statusFileName))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(data, &r)
	return r, err
}

// statusLineSetting is a statusLine entry in a Claude settings file.
type statusLineSetting struct {
	Type            string `json:"type"`
	Command         string `json:"command"`
	Padding         *int   `json:"padding,omitempty"`
	RefreshInterval *int   `json:"refreshInterval,omitempty"`
}

// claudeConfigDir is ~/.claude, or CLAUDE_CONFIG_DIR.
func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// userStatusLine finds the user's own status line, in the order Claude
// Code applies them: the project's local settings, its shared settings,
// then the user's settings. The files are only read. deckhand's own
// recorder is skipped, so it never runs itself.
func userStatusLine(dirs []string) (statusLineSetting, bool) {
	var files []string
	for _, d := range dirs {
		files = append(files, filepath.Join(d, ".claude", "settings.local.json"), filepath.Join(d, ".claude", "settings.json"))
	}
	if c := claudeConfigDir(); c != "" {
		files = append(files, filepath.Join(c, "settings.json"))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s struct {
			StatusLine *statusLineSetting `json:"statusLine"`
		}
		if json.Unmarshal(data, &s) != nil || s.StatusLine == nil {
			continue
		}
		sl := *s.StatusLine
		if (sl.Type != "" && sl.Type != "command") || strings.TrimSpace(sl.Command) == "" || isOwnStatusLine(sl.Command) {
			continue
		}
		return sl, true
	}
	return statusLineSetting{}, false
}

func isOwnStatusLine(cmd string) bool { return strings.Contains(cmd, " statusline --session ") }

// statusLineTimeout bounds the user's command. Claude Code stops waiting
// (and starts a new run) when the next update comes, so this only matters
// for commands that hang.
const statusLineTimeout = 30 * time.Second

// runUserStatusLine runs a status line command the way Claude Code does:
// in Git Bash on Windows when it's installed, else PowerShell; in sh
// elsewhere. It gets the same JSON on stdin and the same environment
// (COLUMNS, LINES, ...).
func runUserStatusLine(command string, input []byte, stdout, stderr io.Writer) int {
	exe, args := statusLineShell(command)
	if exe == "" {
		fmt.Fprintln(stderr, "deckhand statusline: no shell found to run your statusLine command")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusLineTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Stdout, cmd.Stderr = stdout, stderr
	hideWindow(cmd)
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return max(ee.ExitCode(), 1)
	}
	fmt.Fprintln(stderr, "deckhand statusline:", err)
	return 1
}

// statusLineShell is the shell and arguments for a status line command.
func statusLineShell(command string) (string, []string) {
	if runtime.GOOS != "windows" {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		return sh, []string{"-c", command}
	}
	if bash := findGitBash(); bash != "" {
		return bash, []string{"-c", command}
	}
	if ps := pwshPath(); ps != "" {
		return ps, []string{"-NoProfile", "-Command", command}
	}
	return "", nil
}

// findGitBash finds Git for Windows' bash.exe: CLAUDE_CODE_GIT_BASH_PATH
// (which Claude Code also reads), next to git.exe on PATH, or the usual
// install folders.
func findGitBash() string {
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" && isFile(p) {
		return p
	}
	var cands []string
	if git, err := exec.LookPath("git"); err == nil {
		d := filepath.Dir(git) // ...\Git\cmd, ...\Git\bin or ...\Git\mingw64\bin
		cands = append(cands, filepath.Join(filepath.Dir(d), "bin", "bash.exe"), filepath.Join(filepath.Dir(filepath.Dir(d)), "bin", "bash.exe"))
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			cands = append(cands, filepath.Join(base, "Git", "bin", "bash.exe"), filepath.Join(base, "Programs", "Git", "bin", "bash.exe"))
		}
	}
	for _, c := range cands {
		if isFile(c) {
			return c
		}
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// statusLineCommand is the command Claude Code runs for deckhand's status
// line (see hookCommand).
func statusLineCommand(sessionID string) string {
	return strings.TrimSuffix(hookCommand(sessionID), " hook --session "+sessionID) + " statusline --session " + sessionID
}
