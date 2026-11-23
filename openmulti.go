package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Opening a session with more than one folder (o and t).
//
// A session's folders are its start folder, the code repo beside it or its
// worktree (never the main checkout of a worktree's repo), and the folders
// added to a Claude session with /add-dir. With more than one, o and t show
// a picker whose first row, "All folders", is preselected. [open] multi =
// "all" skips it and opens them all; "first" skips it and opens the folder
// o and t opened before: the worktree if there is one, else the start
// folder.
//
// "All folders" in VS Code opens one window with a multi-root workspace,
// written to the session's folder in deckhand's data dir. Other editors
// run once per folder. In Windows Terminal it opens one tab per folder
// with a single wt.exe; another terminal runs once per folder.

// sessionFolder is one folder o or t can open.
type sessionFolder struct {
	dir   string
	label string // its role: "start folder", "code repo", "worktree · <branch>", "added (/add-dir)"
}

const addedLabel = "added (/add-dir)"

// openFolders are the session's folders for o and t, in order: the start
// folder, then its code repo or worktree, then the folders added with
// /add-dir that still exist.
func (s *Session) openFolders() []sessionFolder {
	s.mu.Lock()
	var base []sessionFolder
	wtLabel := "worktree"
	if s.Branch != "" {
		wtLabel += " · " + s.Branch
	}
	switch {
	case s.Worktree != "" && s.separateRepoLocked():
		base = []sessionFolder{{s.Dir, "start folder"}, {s.Worktree, wtLabel}}
	case s.Worktree != "":
		// The start folder is in the repo: the agent runs in the worktree.
		base = []sessionFolder{{s.Worktree, wtLabel}}
	case s.separateRepoLocked():
		base = []sessionFolder{{s.Dir, "start folder"}, {s.CodeRepo, "code repo"}}
	default:
		base = []sessionFolder{{s.Dir, "start folder"}}
	}
	id, dir, tr := s.ID, s.Dir, s.transcriptPathLocked()
	s.mu.Unlock()

	var added []string
	if rec, err := readStatusRecord(id); err == nil {
		added = rec.AddedDirs
	}
	if tr != "" {
		added = append(added, transcriptAddedDirs(tr)...)
	}
	return withAddedDirs(base, added, dir)
}

// mainFolder is the folder o and t open with [open] multi = "first": the
// worktree if there is one, otherwise the start folder (what they always
// opened before the picker).
func (s *Session) mainFolder() sessionFolder {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Worktree != "" {
		label := "worktree"
		if s.Branch != "" {
			label += " · " + s.Branch
		}
		return sessionFolder{s.Worktree, label}
	}
	return sessionFolder{s.Dir, "start folder"}
}

// withAddedDirs adds the added folders to base, resolving relative ones
// against dir, skipping ones that aren't folders, and leaving out
// duplicates (ignoring case and trailing separators).
func withAddedDirs(base []sessionFolder, added []string, dir string) []sessionFolder {
	out := slices.Clone(base)
	seen := map[string]bool{}
	for _, f := range out {
		seen[folderKey(f.dir)] = true
	}
	for _, a := range added {
		a = resolveAddedDir(a, dir)
		if a == "" || seen[folderKey(a)] {
			continue
		}
		if fi, err := os.Stat(a); err != nil || !fi.IsDir() {
			continue
		}
		seen[folderKey(a)] = true
		out = append(out, sessionFolder{a, addedLabel})
	}
	return out
}

// folderKey compares folders: cleaned, lower case, without a trailing
// separator.
func folderKey(d string) string { return strings.TrimRight(dirKey(d), `\/`) }

// resolveAddedDir is an /add-dir argument as a folder: without quotes,
// with ~ for the home folder, relative to dir.
func resolveAddedDir(a, dir string) string {
	a = strings.TrimSpace(a)
	if len(a) >= 2 && (a[0] == '"' || a[0] == '\'') && a[len(a)-1] == a[0] {
		a = a[1 : len(a)-1]
	}
	if a == "" {
		return ""
	}
	if a == "~" || strings.HasPrefix(a, `~\`) || strings.HasPrefix(a, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		a = filepath.Join(home, a[1:])
	}
	if !filepath.IsAbs(a) {
		if dir == "" {
			return ""
		}
		a = filepath.Join(dir, a)
	}
	return filepath.Clean(a)
}

var addDirRe = regexp.MustCompile(`<command-name>/add-dir</command-name>[\s\S]*?<command-args>([\s\S]*?)</command-args>`)

// transcriptAddedDirs are the folders added with /add-dir in a Claude
// transcript, in order. Claude records the command as a user entry:
//
//	<command-name>/add-dir</command-name> ... <command-args>C:\src\notes</command-args>
func transcriptAddedDirs(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte("add-dir")) { // a quick check before parsing it
			out = append(out, addedDirsInEntry(line)...)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return out
			}
			break
		}
	}
	return out
}

// addedDirsInEntry reads the /add-dir arguments in one transcript line, if
// it's a user entry.
func addedDirsInEntry(line []byte) []string {
	var e struct {
		Type        string `json:"type"`
		IsSidechain bool   `json:"isSidechain"`
		Message     struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "user" || e.IsSidechain {
		return nil
	}
	var text string
	if json.Unmarshal(e.Message.Content, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			return nil
		}
		for _, b := range blocks {
			if b.Type == "text" {
				text += b.Text + "\n"
			}
		}
	}
	var out []string
	for _, m := range addDirRe.FindAllStringSubmatch(text, -1) {
		if a := strings.TrimSpace(m[1]); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// VS Code workspaces ----------------------------------------------------------

// isVSCodeTemplate reports whether an editor command runs VS Code, which
// opens several folders as one multi-root workspace.
func isVSCodeTemplate(tmpl string) bool {
	parts, err := splitCommand(tmpl)
	if err != nil || len(parts) == 0 {
		return false
	}
	switch progName(parts[0]) {
	case "code", "code-insiders":
		return true
	}
	return false
}

// progName is a program's file name in lower case, without .exe, .cmd or
// .bat.
func progName(prog string) string {
	name := strings.ToLower(filepath.Base(strings.ReplaceAll(prog, `\`, "/")))
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

// workspaceJSON is a .code-workspace file listing the folders, each named
// after its folder and role, e.g. "notes (start folder)".
func workspaceJSON(folders []sessionFolder) []byte {
	type folder struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	ws := struct {
		Folders  []folder       `json:"folders"`
		Settings map[string]any `json:"settings"`
	}{Settings: map[string]any{}}
	for _, f := range folders {
		role := f.label
		if role == addedLabel {
			role = "added"
		}
		ws.Folders = append(ws.Folders, folder{Name: filepath.Base(f.dir) + " (" + role + ")", Path: f.dir})
	}
	data, _ := json.MarshalIndent(ws, "", "  ")
	return append(data, '\n')
}

// workspaceName is the workspace file's name, after the session (VS Code
// shows it in the window title).
func workspaceName(session string) string {
	name := strings.Map(func(r rune) rune {
		if r < ' ' || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '-'
		}
		return r
	}, session)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "session"
	}
	return name + ".code-workspace"
}

// writeWorkspace writes the session's .code-workspace file to its folder in
// deckhand's data dir, never to the user's folders, replacing any it wrote
// before (under an older name too).
func writeWorkspace(id, session string, folders []sessionFolder) (string, error) {
	dir := sessionDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, workspaceName(session))
	old, _ := filepath.Glob(filepath.Join(dir, "*.code-workspace"))
	for _, o := range old {
		if !strings.EqualFold(o, path) {
			_ = os.Remove(o)
		}
	}
	if err := os.WriteFile(path, workspaceJSON(folders), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Windows Terminal tabs --------------------------------------------------------

// wtSubcommands start a wt.exe subcommand.
var wtSubcommands = map[string]bool{"nt": true, "new-tab": true, "sp": true, "split-pane": true, "ft": true, "focus-tab": true}

// wtTabsArgs turns a wt.exe command's arguments (with {dir} in them) into
// one command line that opens a tab per folder:
//
//	-w 0 nt -d "{dir}"  →  -w 0 nt -d A ; nt -d B
//
// The window options (-w 0) go first, once; the tab's arguments repeat
// after each ";". Without a subcommand, wt opens a new tab, so the
// repeats start with nt.
func wtTabsArgs(args []string, dirs []string) []string {
	i := slices.IndexFunc(args, func(a string) bool { return wtSubcommands[strings.ToLower(a)] })
	var global, tab, more []string
	if i >= 0 {
		global, tab = args[:i], args[i:]
		more = tab
	} else {
		j := 0
		for j < len(args) {
			switch args[j] {
			case "-w", "--window", "--pos", "--size":
				j += 2
				continue
			case "-M", "--maximized", "-F", "--fullscreen", "-f", "--focus":
				j++
				continue
			}
			break
		}
		j = min(j, len(args))
		global, tab = args[:j], args[j:]
		more = append([]string{"nt"}, tab...)
	}
	fill := func(parts []string, dir string) []string {
		out := make([]string, len(parts))
		for k, p := range parts {
			out[k] = strings.ReplaceAll(p, "{dir}", dir)
		}
		return out
	}
	out := fill(global, dirs[0])
	out = append(out, fill(tab, dirs[0])...)
	for _, d := range dirs[1:] {
		out = append(append(out, ";"), fill(more, d)...)
	}
	return out
}

// Opening -----------------------------------------------------------------------

// openFolder opens the selected session's folder in the editor, Explorer or
// a new terminal tab. It works whether or not the session is running. For
// o and t, a session with more than one folder shows the picker, unless
// [open] multi says to open all of them or the first.
func (m *model) openFolder(target openTarget) {
	s := m.current()
	if s == nil {
		return
	}
	if target == openExplorer {
		s.mu.Lock()
		dir := s.Dir
		s.mu.Unlock()
		if err := launchOpen(defaultExplorer, dir); err != nil {
			m.setFlash("Couldn't open: " + err.Error())
		}
		return
	}
	if openSettings.Multi == multiFirst {
		// The folder o and t opened before there was a picker.
		m.openIn(s, target, []sessionFolder{s.mainFolder()})
		return
	}
	folders := s.openFolders()
	switch {
	case len(folders) == 1:
		m.openIn(s, target, folders)
	case openSettings.Multi == multiAll:
		m.openIn(s, target, folders)
	default:
		m.folderPick = &folderPick{s: s, target: target, folders: folders}
	}
}

// openIn opens folders in the editor (o) or the terminal (t): one folder,
// or several at once.
func (m *model) openIn(s *Session, target openTarget, folders []sessionFolder) {
	var errs []error
	if len(folders) > 1 {
		// Leave out folders that are gone, and say so.
		var ok []sessionFolder
		for _, f := range folders {
			if fi, err := os.Stat(f.dir); err != nil || !fi.IsDir() {
				errs = append(errs, fmt.Errorf("folder not found: %s", f.dir))
				continue
			}
			ok = append(ok, f)
		}
		folders = ok
	}
	fallback := false
	switch {
	case len(folders) == 0:
	case target == openEditor:
		var tmpl string
		tmpl, fallback = editorTemplate(openSettings, exec.LookPath)
		if len(folders) > 1 && isVSCodeTemplate(tmpl) {
			s.mu.Lock()
			id, name := s.ID, s.Name
			s.mu.Unlock()
			errs = append(errs, openWorkspace(tmpl, id, name, folders))
			break
		}
		for _, f := range folders {
			errs = append(errs, launchOpen(tmpl, f.dir))
		}
	case target == openTerminal:
		tmpl := terminalTemplate(openSettings)
		if parts, err := splitCommand(tmpl); err == nil && len(parts) > 1 && len(folders) > 1 && progName(parts[0]) == "wt" {
			var dirs []string
			for _, f := range folders {
				dirs = append(dirs, f.dir)
			}
			errs = append(errs, openStart(parts[0], wtTabsArgs(parts[1:], dirs), dirs[0]))
			break
		}
		for _, f := range folders {
			errs = append(errs, launchOpen(tmpl, f.dir))
		}
	}
	if err := errors.Join(errs...); err != nil {
		m.setFlash("Couldn't open: " + strings.ReplaceAll(err.Error(), "\n", "; "))
		return
	}
	if fallback && !m.editorHinted {
		m.editorHinted = true
		m.setFlash("Opened in Explorer: no editor set and VS Code isn't on PATH. Set [open] editor in the config (deckhand config).")
	}
}

// openWorkspace writes the session's workspace file and opens it with the
// VS Code command, in place of {dir}.
func openWorkspace(tmpl, id, name string, folders []sessionFolder) error {
	path, err := writeWorkspace(id, name, folders)
	if err != nil {
		return err
	}
	prog, args, err := expandTemplate(tmpl, path)
	if err != nil {
		return err
	}
	return openStart(prog, args, folders[0].dir)
}

// The picker -----------------------------------------------------------------------

// folderPick is the picker o and t show for a session with more than one
// folder. Row 0 is "All folders".
type folderPick struct {
	s       *Session
	target  openTarget
	folders []sessionFolder
	hi      int
}

func (m *model) folderPickKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.folderPick
	n := len(p.folders) + 1
	switch k.String() {
	case "esc", "q":
		m.folderPick = nil
	case "up", "k", "shift+tab":
		p.hi = (p.hi + n - 1) % n
	case "down", "j", "tab":
		p.hi = (p.hi + 1) % n
	case "home":
		p.hi = 0
	case "end":
		p.hi = n - 1
	case "enter":
		m.folderPick = nil
		if p.hi == 0 {
			m.openIn(p.s, p.target, p.folders)
		} else {
			m.openIn(p.s, p.target, p.folders[p.hi-1:p.hi])
		}
	}
	return nil
}

func (m *model) renderFolderPick(w int) []string {
	p := m.folderPick
	p.s.mu.Lock()
	name := p.s.Name
	p.s.mu.Unlock()
	what := "the editor"
	if p.target == openTerminal {
		what = "a terminal"
	}
	title := fmt.Sprintf(" Open %q in %s ", name, what)
	lw := len("All folders")
	for _, f := range p.folders {
		lw = max(lw, ansi.StringWidth(f.label))
	}
	longest := 0
	for _, f := range p.folders {
		longest = max(longest, ansi.StringWidth(f.dir))
	}
	bw := min(max(ansi.StringWidth(title)+4, lw+longest+10), w-2)
	inner := bw - 4
	top := "┌" + title + strings.Repeat("─", max(bw-2-ansi.StringWidth(title), 0)) + "┐"
	line := func(s string) string {
		return stBorder.Render("│") + " " + fit(s, inner) + " " + stBorder.Render("│")
	}
	row := func(i int, label, dir string) string {
		text := label + strings.Repeat(" ", lw-ansi.StringWidth(label))
		if dir != "" {
			text += "  " + shortDir(dir, max(inner-lw-4, 10))
		}
		if i == p.hi {
			return line(lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("› " + text))
		}
		return line("  " + text)
	}
	out := []string{stBorder.Render(fitPlain(top, bw))}
	out = append(out, row(0, "All folders", ""))
	for i, f := range p.folders {
		out = append(out, row(i+1, f.label, f.dir))
	}
	out = append(out, stBorder.Render("└"+strings.Repeat("─", bw-2)+"┘"))
	return out
}

// overlayFolderPick draws the picker over the main pane's top left.
func (m *model) overlayFolderPick(behind []string, w int) []string {
	box := m.renderFolderPick(w)
	out := slices.Clone(behind)
	const left, top = 1, 1
	for i, l := range box {
		y := top + i
		if y >= len(out) {
			break
		}
		bw := ansi.StringWidth(l)
		out[y] = ansi.Cut(behind[y], 0, left) + "\x1b[0m" + l + "\x1b[0m" + ansi.Cut(behind[y], left+bw, w)
	}
	return out
}
