package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Extra details shown under each session in the sidebar: the git branch and
// whether the working tree has changes, and for Claude the model and how
// many tokens of context the conversation uses. They're gathered in the
// background, a few seconds apart, never on the UI goroutine.

type gitInfo struct {
	ok     bool // the folder is in a git work tree
	branch string
	dirty  bool
	ahead  int
	behind int
}

// sessionInfo is the latest background-gathered detail for a session.
type sessionInfo struct {
	git       gitInfo
	model     string // e.g. "claude-opus-5-5"
	ctxTokens int    // input + cached tokens of the latest main-chain reply
}

const (
	pollActive = 3 * time.Second  // running or selected sessions
	pollIdle   = 30 * time.Second // the rest
	gitTimeout = 5 * time.Second
)

// inGitRepo reports whether dir or a parent has a .git entry (a folder, or
// a file for worktrees and submodules), so git isn't run for other folders.
func inGitRepo(dir string) bool {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		p := filepath.Dir(d)
		if p == d {
			return false
		}
		d = p
	}
}

func readGitInfo(dir string) gitInfo {
	if dir == "" || !inGitRepo(dir) {
		return gitInfo{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", dir, "status", "--porcelain=v2", "--branch")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return gitInfo{}
	}
	return parseGitStatus(out)
}

// parseGitStatus reads `git status --porcelain=v2 --branch` output.
func parseGitStatus(out []byte) gitInfo {
	g := gitInfo{ok: true}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			g.branch = strings.TrimPrefix(line, "# branch.head ")
			if g.branch == "(detached)" {
				g.branch = "detached"
			}
		case strings.HasPrefix(line, "# branch.ab "):
			_, _ = fmt.Sscanf(strings.TrimPrefix(line, "# branch.ab "), "+%d -%d", &g.ahead, &g.behind)
		case strings.HasPrefix(line, "#"), line == "":
		default:
			g.dirty = true
		}
	}
	return g
}

// transcriptTail is how much of the end of a transcript is read. A reply's
// usage is in the last few lines; tool results can make lines long.
const transcriptTail = 512 << 10

// readTranscriptInfo finds the model, context size and text of the latest
// main-chain assistant reply in a Claude Code transcript (JSONL).
func readTranscriptInfo(path string) (model string, ctxTokens int, lastText string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, "", err
	}
	start := fi.Size() - transcriptTail
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", 0, "", err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", 0, "", err
	}
	model, ctxTokens, lastText = parseTranscriptTail(data)
	return model, ctxTokens, lastText, nil
}

type transcriptEntry struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Model   string            `json:"model"`
		Content []json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func parseTranscriptTail(data []byte) (model string, ctxTokens int, lastText string) {
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || !bytes.Contains(line, []byte(`"assistant"`)) {
			continue
		}
		var e transcriptEntry
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.IsSidechain {
			continue // the first line may be cut off; sub-agents don't count
		}
		if lastText == "" {
			lastText = replyText(e.Message.Content)
		}
		if model == "" && e.Message.Usage != nil && e.Message.Model != "" && e.Message.Model != "<synthetic>" {
			u := e.Message.Usage
			model = e.Message.Model
			ctxTokens = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
		}
		if model != "" && lastText != "" {
			break
		}
	}
	return model, ctxTokens, lastText
}

func replyText(content []json.RawMessage) string {
	var parts []string
	for _, c := range content {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(c, &b) == nil && b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

// guessTranscriptPath is where Claude Code keeps a session's transcript:
// ~/.claude/projects/<folder with non-alphanumerics as "-">/<id>.jsonl.
// The path from the hooks is preferred; this covers sessions from before
// deckhand recorded it.
func guessTranscriptPath(dir, claudeID string) string {
	if dir == "" || claudeID == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(home, ".claude")
	}
	return filepath.Join(base, "projects", nonAlnum.ReplaceAllString(dir, "-"), claudeID+".jsonl")
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

var modelDate = regexp.MustCompile(`-\d{8}$`)

// shortModel turns "claude-opus-5-5" into "opus 5.5" and
// "claude-sonnet-4-5-20250929" into "sonnet 4.5".
func shortModel(m string) string {
	m = strings.TrimPrefix(m, "claude-")
	m = modelDate.ReplaceAllString(m, "")
	family, ver, ok := strings.Cut(m, "-")
	if !ok {
		return m
	}
	return family + " " + strings.ReplaceAll(ver, "-", ".")
}

// shortTokens formats a token count: 950, 84k, 1.2M.
func shortTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}

// gitLabel is the branch with markers: "main*" (changes), "↑2↓1".
func gitLabel(g gitInfo) string {
	if !g.ok || g.branch == "" {
		return ""
	}
	s := g.branch
	if g.dirty {
		s += "*"
	}
	if g.ahead > 0 {
		s += fmt.Sprintf("↑%d", g.ahead)
	}
	if g.behind > 0 {
		s += fmt.Sprintf("↓%d", g.behind)
	}
	return s
}
