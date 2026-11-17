package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The diff preview ("d" in the list): a read-only, scrollable view of what
// the selected session changed, against the merge base with the repo's
// default branch. It includes commits on the branch, staged and unstaged
// changes, and untracked files (shown as added). git runs in the
// background; the view says "Loading" until it's done.

const (
	// maxDiffBytes caps how much of `git diff` is read. The rest is left
	// out, with a note, so a huge diff can't stall deckhand.
	maxDiffBytes = 2 << 20
	// maxDiffLines caps the lines of the rendered view.
	maxDiffLines = 30000
	// Untracked files: how many are shown, and how much of each is read.
	maxUntrackedFiles = 200
	maxUntrackedBytes = 64 << 10
	maxUntrackedLines = 1000
	diffTimeout       = 15 * time.Second
	// emptyTree is git's empty tree, the base for a repo with no commits.
	emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

// gitDir is the folder whose git repo the diff preview (and later the PR
// and merge) use. For now it's the session's folder.
func (s *Session) gitDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Dir
}

// diffFile is one file's part of a diff.
type diffFile struct {
	path      string // the new path ("" never; the old one for a deletion)
	oldPath   string // for a rename, the old path
	status    byte   // 'M', 'A', 'D', 'R', or '?' for untracked
	adds      int
	dels      int
	binary    bool
	body      []string // hunk lines ("@@ ...", "+...", "-...", " ...") and notes
	truncated bool     // an untracked file that was cut short
}

// diffResult is what loadDiff found.
type diffResult struct {
	dir       string
	notGit    bool
	err       string
	base      string // the branch compared with ("origin/main"), "" if none
	mergeBase string // short commit, or "" for none
	note      string // how the base was chosen, when it's not the usual way
	files     []diffFile
	truncated bool // git diff's output was cut at maxDiffBytes
	untracked int  // untracked files left out (over maxUntrackedFiles)
}

// diffView is the open diff preview.
type diffView struct {
	name    string // the session's name
	dir     string
	loading bool
	res     diffResult
	lines   []string // rendered, styled lines (without the header)
	starts  []int    // the line where each file's section starts
	scroll  int
	rows    int // rows the content had when last drawn
}

type diffLoadedMsg struct {
	view *diffView
	res  diffResult
}

// openDiff opens the diff preview for the selected session and starts
// loading it.
func (m *model) openDiff() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	v := &diffView{name: s.Name, dir: s.gitDir(), loading: true}
	m.diff = v
	return func() tea.Msg { return diffLoadedMsg{view: v, res: loadDiff(v.dir)} }
}

// setResult fills the view with a loaded diff.
func (v *diffView) setResult(res diffResult) {
	v.loading = false
	v.res = res
	v.lines, v.starts = renderDiff(res)
}

// runGit runs git in dir and returns its output, at most limit bytes of it
// (0: no limit). truncated says it was cut.
func runGit(ctx context.Context, dir string, limit int, args ...string) (out []byte, truncated bool, err error) {
	full := append([]string{"--no-optional-locks", "-c", "core.quotepath=false", "-c", "color.ui=false", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	hideWindow(cmd)
	if limit <= 0 {
		out, err = cmd.Output()
		return out, false, err
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	out, err = io.ReadAll(io.LimitReader(pipe, int64(limit)+1))
	if len(out) > limit {
		out, truncated = out[:limit], true
		_ = cmd.Process.Kill()
	}
	werr := cmd.Wait()
	if truncated {
		return out, true, nil
	}
	if err == nil {
		err = werr
	}
	return out, false, err
}

func gitLine(ctx context.Context, dir string, args ...string) (string, bool) {
	out, _, err := runGit(ctx, dir, 0, args...)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// defaultBase finds the branch to compare with: origin/HEAD, then
// origin/main and origin/master, then (with no remote branch) the local
// main or master. ok is false when there's none.
func defaultBase(ctx context.Context, dir string) (string, bool) {
	if ref, ok := gitLine(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); ok && ref != "" {
		return ref, true
	}
	for _, ref := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"} {
		if _, ok := gitLine(ctx, dir, "rev-parse", "--verify", "--quiet", ref); ok {
			ref = strings.TrimPrefix(ref, "refs/remotes/")
			return strings.TrimPrefix(ref, "refs/heads/"), true
		}
	}
	return "", false
}

// loadDiff reads the diff of dir's work tree against the merge base with
// the default branch, plus its untracked files.
func loadDiff(dir string) diffResult {
	res := diffResult{dir: dir}
	if dir == "" || !inGitRepo(dir) {
		res.notGit = true
		return res
	}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	root, ok := gitLine(ctx, dir, "rev-parse", "--show-toplevel")
	if !ok || root == "" {
		res.notGit = true
		return res
	}
	root = filepath.FromSlash(root)

	base := emptyTree
	_, hasHead := gitLine(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD")
	branch, found := defaultBase(ctx, root)
	switch {
	case !hasHead:
		res.note = "no commits yet"
	case !found:
		base, res.note = "HEAD", "no main or master branch, so only uncommitted changes"
	default:
		res.base = branch
		if mb, ok := gitLine(ctx, root, "merge-base", branch, "HEAD"); ok && mb != "" {
			base = mb
			res.mergeBase = shortHash(mb)
		} else {
			base, res.note = "HEAD", "no common commit with "+branch+", so only uncommitted changes"
		}
	}

	out, truncated, err := runGit(ctx, root, maxDiffBytes, "diff", "--no-color", "--no-ext-diff", "--no-textconv",
		"-M", "--src-prefix=a/", "--dst-prefix=b/", base, "--")
	if err != nil {
		res.err = gitError(err)
		return res
	}
	res.truncated = truncated
	res.files = parseDiff(out)

	list, _, err := runGit(ctx, root, 0, "ls-files", "--others", "--exclude-standard", "-z")
	if err == nil {
		n := 0
		for _, p := range strings.Split(string(list), "\x00") {
			switch {
			case p == "":
			case n >= maxUntrackedFiles:
				res.untracked++
			default:
				n++
				res.files = append(res.files, untrackedFile(root, p))
			}
		}
	}
	return res
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

func gitError(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return "git: " + oneLine(string(ee.Stderr))
	}
	return "git: " + err.Error()
}

// untrackedFile reads an untracked file as an added one, capped at
// maxUntrackedBytes and maxUntrackedLines.
func untrackedFile(root, rel string) diffFile {
	f := diffFile{path: rel, status: '?'}
	fh, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		f.body = []string{"(can't read it: " + err.Error() + ")"}
		return f
	}
	defer fh.Close()
	data, _ := io.ReadAll(io.LimitReader(fh, maxUntrackedBytes+1))
	if len(data) > maxUntrackedBytes {
		data, f.truncated = data[:maxUntrackedBytes], true
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		f.binary = true
		f.body = []string{"Binary file"}
		return f
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		f.body = []string{"(empty file)"}
		return f
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxUntrackedLines {
		lines, f.truncated = lines[:maxUntrackedLines], true
	}
	f.body = append(f.body, fmt.Sprintf("@@ -0,0 +1,%d @@", len(lines)))
	for _, l := range lines {
		f.body = append(f.body, "+"+l)
	}
	f.adds = len(lines)
	return f
}

// parseDiff splits `git diff` output into files, counting added and
// removed lines.
func parseDiff(out []byte) []diffFile {
	var files []diffFile
	var cur *diffFile
	inHunk := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "diff --git ") {
			files = append(files, diffFile{status: 'M'})
			cur = &files[len(files)-1]
			cur.oldPath, cur.path = splitDiffHeader(strings.TrimPrefix(line, "diff --git "))
			inHunk = false
			continue
		}
		if cur == nil {
			continue
		}
		if inHunk {
			switch {
			case strings.HasPrefix(line, "@@"):
			case strings.HasPrefix(line, "+"):
				cur.adds++
			case strings.HasPrefix(line, "-"):
				cur.dels++
			case strings.HasPrefix(line, " "), strings.HasPrefix(line, `\`):
			case line == "":
				continue // the end of the output
			}
			cur.body = append(cur.body, line)
			continue
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			cur.body = append(cur.body, line)
		case strings.HasPrefix(line, "new file mode"):
			cur.status = 'A'
		case strings.HasPrefix(line, "deleted file mode"):
			cur.status = 'D'
		case strings.HasPrefix(line, "rename from "):
			cur.status = 'R'
			cur.oldPath = unquotePath(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			cur.path = unquotePath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "--- "):
			if p := strings.TrimPrefix(line, "--- "); p != "/dev/null" {
				cur.oldPath = strings.TrimPrefix(unquotePath(p), "a/")
			}
		case strings.HasPrefix(line, "+++ "):
			if p := strings.TrimPrefix(line, "+++ "); p != "/dev/null" {
				cur.path = strings.TrimPrefix(unquotePath(p), "b/")
			}
		case strings.HasPrefix(line, "Binary files "):
			cur.binary = true
			cur.body = append(cur.body, "Binary file")
		case strings.HasPrefix(line, "old mode "), strings.HasPrefix(line, "new mode "):
			cur.body = append(cur.body, line)
		}
	}
	for i := range files {
		f := &files[i]
		if f.status == 'D' {
			f.path = f.oldPath
		}
		if f.status != 'R' {
			f.oldPath = ""
		}
	}
	return files
}

// splitDiffHeader reads the two paths of "a/x b/x" from a diff --git line.
func splitDiffHeader(s string) (oldPath, newPath string) {
	if strings.HasPrefix(s, `"`) {
		// Quoted: "a/x y" "b/x y", or a mix of quoted and plain.
		if end := closingQuote(s); end > 0 {
			a := unquotePath(s[:end+1])
			b := unquotePath(strings.TrimSpace(s[end+1:]))
			return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(b, "b/")
		}
	}
	if strings.HasSuffix(s, `"`) {
		if i := strings.LastIndex(s, ` "`); i > 0 {
			return strings.TrimPrefix(s[:i], "a/"), strings.TrimPrefix(unquotePath(s[i+1:]), "b/")
		}
	}
	// Unquoted, the same path twice: split in the middle.
	if n := len(s); n%2 == 1 {
		a, b := s[:n/2], s[n/2+1:]
		if strings.HasPrefix(a, "a/") && strings.HasPrefix(b, "b/") && a[2:] == b[2:] {
			return a[2:], b[2:]
		}
	}
	if i := strings.LastIndex(s, " b/"); i > 0 {
		return strings.TrimPrefix(s[:i], "a/"), s[i+3:]
	}
	return s, s
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// unquotePath undoes git's C-style quoting of unusual file names.
func unquotePath(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// Rendering -------------------------------------------------------------------

var (
	stDiffAdd  = lipgloss.NewStyle().Foreground(colIdle)
	stDiffDel  = lipgloss.NewStyle().Foreground(colError)
	stDiffHunk = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DCFFF"))
	stDiffFile = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
)

// counts is "+12 −3".
func counts(adds, dels int) string {
	return stDiffAdd.Render(fmt.Sprintf("+%d", adds)) + " " + stDiffDel.Render(fmt.Sprintf("−%d", dels))
}

func (f diffFile) label() string {
	if f.oldPath != "" && f.oldPath != f.path {
		return f.oldPath + " → " + f.path
	}
	return f.path
}

func statusTag(f diffFile) string {
	switch f.status {
	case 'A':
		return stDiffAdd.Render("A")
	case '?':
		return stDiffAdd.Render("?")
	case 'D':
		return stDiffDel.Render("D")
	case 'R':
		return stWaitText.Render("R")
	}
	return lipgloss.NewStyle().Foreground(colWorking).Render("M")
}

// renderDiff turns a result into styled lines: a summary, the file list,
// then each file's hunks. starts is where each file's section begins.
func renderDiff(res diffResult) (lines []string, starts []int) {
	switch {
	case res.notGit:
		return []string{"", stMuted.Render("  Not a git repository: " + res.dir),
			stMuted.Render("  The diff preview needs the session's folder to be in a git repo.")}, nil
	case res.err != "":
		return []string{"", lipgloss.NewStyle().Foreground(colError).Render("  Couldn't read the diff. " + clean(res.err))}, nil
	case len(res.files) == 0:
		return []string{"", stMuted.Render("  No changes" + againstText(res) + ".")}, nil
	}
	adds, dels := 0, 0
	for _, f := range res.files {
		adds, dels = adds+f.adds, dels+f.dels
	}
	files := "files"
	if len(res.files) == 1 {
		files = "file"
	}
	lines = append(lines, " "+lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%d %s changed", len(res.files), files))+"  "+counts(adds, dels)+stMuted.Render(againstText(res)))
	if res.note != "" {
		lines = append(lines, stMuted.Render(" ("+res.note+")"))
	}
	lines = append(lines, "")
	nameW := 0
	for _, f := range res.files {
		nameW = max(nameW, ansi.StringWidth(clean(f.label())))
	}
	nameW = min(nameW, 70)
	for _, f := range res.files {
		extra := ""
		switch {
		case f.status == '?':
			extra = stMuted.Render("  untracked")
		case f.binary:
			extra = stMuted.Render("  binary")
		}
		lines = append(lines, "   "+statusTag(f)+"  "+fit(clean(f.label()), nameW)+"  "+counts(f.adds, f.dels)+extra)
	}
	if res.untracked > 0 {
		lines = append(lines, stMuted.Render(fmt.Sprintf("   … and %d more untracked files, not shown", res.untracked)))
	}
	if res.truncated {
		lines = append(lines, "", stWaitText.Render(fmt.Sprintf(" The diff is over %d MB, so only the start is shown and the counts are for that part.", maxDiffBytes>>20)))
	}

	for _, f := range res.files {
		lines = append(lines, "")
		starts = append(starts, len(lines))
		head := " " + statusTag(f) + " " + stDiffFile.Render(clean(f.label())) + "  " + counts(f.adds, f.dels)
		if f.status == '?' {
			head += stMuted.Render("  untracked")
		}
		lines = append(lines, head)
		for _, b := range f.body {
			lines = append(lines, styleDiffLine(b))
			if len(lines) >= maxDiffLines {
				return append(lines, "", stWaitText.Render(fmt.Sprintf(" Only the first %d lines are shown.", maxDiffLines))), starts
			}
		}
		if f.truncated {
			lines = append(lines, stMuted.Render(" … the rest of this file isn't shown"))
		}
	}
	if res.truncated {
		lines = append(lines, "", stWaitText.Render(" … the rest of the diff isn't shown (it's too big)"))
	}
	return lines, starts
}

func againstText(res diffResult) string {
	switch {
	case res.base != "" && res.mergeBase != "":
		return " vs " + res.base + " (merge base " + res.mergeBase + ")"
	case res.base != "":
		return " vs " + res.base
	}
	return ""
}

// styleDiffLine colours one hunk line, indented by a space.
func styleDiffLine(l string) string {
	c := clean(l)
	switch {
	case strings.HasPrefix(l, "@@"):
		return " " + stDiffHunk.Render(c)
	case strings.HasPrefix(l, "+"):
		return " " + stDiffAdd.Render(c)
	case strings.HasPrefix(l, "-"):
		return " " + stDiffDel.Render(c)
	case strings.HasPrefix(l, " "):
		return " " + c
	}
	return " " + stMuted.Render(c)
}

// clean makes file text safe to draw: tabs become spaces (to the next
// multiple of 4), and control characters, which could move the cursor or
// change colours, become "?".
func clean(s string) string {
	ok := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c >= 0x80 {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch {
		case r == '\t':
			n := 4 - col%4
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		case r == utf8.RuneError, r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			r = '?'
		}
		b.WriteRune(r)
		col += max(ansi.StringWidth(string(r)), 1)
	}
	return b.String()
}

// Keys, mouse and drawing ----------------------------------------------------

// diffKey handles a key while the diff preview is open. Everything else
// is ignored: the preview is read-only.
func (m *model) diffKey(key string) {
	v := m.diff
	page := max(v.rows-1, 1)
	switch key {
	case "esc", "q", "d":
		m.diff = nil
		return
	case "up", "k":
		v.scroll--
	case "down", "j":
		v.scroll++
	case "pgup", "shift+pgup":
		v.scroll -= page
	case "pgdown", "shift+pgdown", "space":
		v.scroll += page
	case "home", "g":
		v.scroll = 0
	case "end", "G":
		v.scroll = len(v.lines)
	case "n":
		for _, s := range v.starts {
			if s > v.scroll {
				v.scroll = s
				break
			}
		}
	case "N":
		for i := len(v.starts) - 1; i >= 0; i-- {
			if v.starts[i] < v.scroll {
				v.scroll = v.starts[i]
				break
			}
		}
	}
	v.clamp()
}

func (v *diffView) clamp() {
	v.scroll = min(v.scroll, len(v.lines)-v.rows)
	v.scroll = max(v.scroll, 0)
}

// diffMouse scrolls the preview with the wheel.
func (m *model) diffMouse(msg tea.MouseMsg) {
	if _, ok := msg.(tea.MouseWheelMsg); !ok {
		return
	}
	switch msg.Mouse().Button {
	case tea.MouseWheelUp:
		m.diff.scroll -= wheelLines
	case tea.MouseWheelDown:
		m.diff.scroll += wheelLines
	}
	m.diff.clamp()
}

// renderDiffView draws the preview in the main pane (w×h): a header, then
// the lines.
func (m *model) renderDiffView(w, h int) []string {
	v := m.diff
	v.rows = max(h-1, 1)
	v.clamp()
	header := " " + stTitle.Render("Diff") + "  " + lipgloss.NewStyle().Bold(true).Render(v.name)
	if n := len(v.lines); n > v.rows {
		end := min(v.scroll+v.rows, n)
		header += stMuted.Render(fmt.Sprintf("  lines %d–%d of %d", v.scroll+1, end, n))
	}
	header += stMuted.Render("  " + v.dir)
	out := []string{fit(header, w)}
	body, start := v.lines, v.scroll
	if v.loading {
		body, start = []string{"", stMuted.Render("  Loading the diff…")}, 0
	}
	for i := 0; i < v.rows; i++ {
		l := ""
		if j := start + i; j < len(body) {
			l = body[j]
		}
		out = append(out, fit(l, w)+"\x1b[0m")
	}
	return out
}

// diffFooter is the footer while the preview is open.
func (m *model) diffFooter() string {
	return " ↑↓ PgUp/PgDn scroll · n/N next/previous file · home/end · esc close"
}
