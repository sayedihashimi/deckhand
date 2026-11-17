package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Worktree sessions: a session can run in its own git worktree, on its own
// branch, so several agents can work on one repo without changing each
// other's files.
//
// A session has a start folder (Session.Dir, where the agent runs) and
// optionally a code repo. When the start folder is in a git repo, the code
// repo is that repo and the agent runs in the same subfolder of the
// worktree. When it isn't (a notes folder, say), the agent stays in the
// start folder and is told about the worktree: Claude through --add-dir and
// --append-system-prompt, other CLIs through DECKHAND_WORKTREE and
// DECKHAND_BRANCH.
//
// Every git and gh call goes through gitRun and ghRun, which tests can
// replace, and never runs on the UI goroutine.

// worktreeConfig is [worktree] in the config file.
type worktreeConfig struct {
	Root  string `toml:"root,omitempty"`  // where worktrees go; {repo} is the main checkout
	Base  string `toml:"base,omitempty"`  // the branch to start from; default: the repo's default branch
	Setup string `toml:"setup,omitempty"` // run in each new worktree before the agent starts
}

const defaultWorktreeRoot = "{repo}-wt"

// worktreeSettings is [worktree] from the config file.
var worktreeSettings worktreeConfig

func loadWorktreeConfig() (worktreeConfig, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return worktreeConfig{}, nil
	}
	if err != nil {
		return worktreeConfig{}, err
	}
	return parseWorktreeConfig(string(data))
}

func parseWorktreeConfig(text string) (worktreeConfig, error) {
	var cfg struct {
		Worktree worktreeConfig `toml:"worktree"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return worktreeConfig{}, fmt.Errorf("%s: %w", configPath(), err)
	}
	w := cfg.Worktree
	w.Root = strings.TrimSpace(w.Root)
	w.Base = strings.TrimSpace(w.Base)
	w.Setup = strings.TrimSpace(w.Setup)
	if strings.ContainsAny(w.Base, " \t~^:?*[\\") || strings.HasPrefix(w.Base, "-") {
		return worktreeConfig{}, fmt.Errorf("%s: worktree.base %q isn't a branch name", configPath(), w.Base)
	}
	return w, nil
}

// worktreeRoot is the folder new worktrees of a repo go in: [worktree]
// root with {repo} replaced by the main checkout's path. A relative root is
// relative to the folder that holds the repo.
func worktreeRoot(cfg worktreeConfig, repoRoot string) string {
	tmpl := cfg.Root
	if tmpl == "" {
		tmpl = defaultWorktreeRoot
	}
	r := strings.ReplaceAll(tmpl, "{repo}", repoRoot)
	if !filepath.IsAbs(r) {
		r = filepath.Join(filepath.Dir(repoRoot), r)
	}
	return filepath.Clean(r)
}

// Running git and gh -----------------------------------------------------------

const (
	gitCommandTimeout = 2 * time.Minute // a fetch over a slow network
	ghCommandTimeout  = 30 * time.Second
)

// gitRun runs git in dir and returns its output without the final newline.
// The error carries git's message. Tests can replace it.
var gitRun = func(dir string, args ...string) (string, error) {
	return runTool("git", gitCommandTimeout, dir, append([]string{"-C", dir}, args...)...)
}

// ghRun runs the GitHub CLI in dir. errNoGH means it isn't installed.
var ghRun = func(dir string, args ...string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", errNoGH
	}
	return runTool("gh", ghCommandTimeout, dir, args...)
}

var errNoGH = errors.New("gh isn't installed")

func runTool(name string, timeout time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Never wait for a password prompt nobody can see.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	text := strings.TrimRight(string(out), "\r\n")
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() != nil {
			msg = "timed out"
		}
		sub := ""
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && a != dir {
				sub = " " + a
				break
			}
		}
		return text, fmt.Errorf("%s%s: %s", name, sub, msg)
	}
	return text, nil
}

func outLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func refExists(repo, ref string) bool {
	_, err := gitRun(repo, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func hasOrigin(repo string) bool {
	_, err := gitRun(repo, "remote", "get-url", "origin")
	return err == nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// within reports whether path is dir or inside it (ignoring case, as
// Windows does).
func within(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	p, d := strings.ToLower(filepath.Clean(path)), strings.ToLower(filepath.Clean(dir))
	return p == d || strings.HasPrefix(p, strings.TrimSuffix(d, string(filepath.Separator))+string(filepath.Separator))
}

// Finding the repo ------------------------------------------------------------

// repoInfo is where a folder sits in its git repo.
type repoInfo struct {
	root string // the main checkout, even when the folder is in a worktree
	sub  string // the folder relative to its work tree's top ("" at the top)
}

func findRepo(dir string) (repoInfo, error) {
	common, err := gitRun(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repoInfo{}, fmt.Errorf("%s isn't in a git repo", dir)
	}
	common = filepath.Clean(filepath.FromSlash(common))
	if !strings.EqualFold(filepath.Base(common), ".git") {
		return repoInfo{}, fmt.Errorf("%s: bare repos aren't supported", dir)
	}
	prefix, _ := gitRun(dir, "rev-parse", "--show-prefix")
	return repoInfo{
		root: filepath.Dir(common),
		sub:  filepath.FromSlash(strings.TrimSuffix(prefix, "/")),
	}, nil
}

// defaultBranch is the branch new worktrees start from: [worktree] base,
// else origin/HEAD, else main, else master.
func defaultBranch(cfg worktreeConfig, repo string) (string, error) {
	if cfg.Base != "" {
		return cfg.Base, nil
	}
	if out, err := gitRun(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && out != "" {
		return strings.TrimPrefix(out, "origin/"), nil
	}
	for _, b := range []string{"main", "master"} {
		if refExists(repo, "refs/remotes/origin/"+b) || refExists(repo, "refs/heads/"+b) {
			return b, nil
		}
	}
	return "", errors.New("couldn't find the default branch (no origin/HEAD, main or master); set [worktree] base in the config")
}

// baseRef is what "on the default branch" is measured against: origin/<base>
// when there is one, else the local branch.
func baseRef(repo, base string) string {
	if refExists(repo, "refs/remotes/origin/"+base) {
		return "origin/" + base
	}
	return base
}

// Names -----------------------------------------------------------------------

var nameAdjectives = []string{
	"amber", "brave", "bright", "calm", "clever", "crisp", "eager", "fancy",
	"gentle", "glad", "golden", "happy", "humble", "jolly", "keen", "kind",
	"lively", "lucky", "merry", "mighty", "misty", "nimble", "noble", "plucky",
	"proud", "quick", "quiet", "rapid", "rosy", "rusty", "shiny", "silent",
	"silver", "sleepy", "snowy", "sunny", "swift", "tidy", "witty", "zesty",
}

var nameNouns = []string{
	"badger", "beaver", "bison", "crane", "falcon", "ferret", "finch", "fox",
	"gecko", "heron", "ibex", "jackal", "koala", "lemur", "lynx", "marten",
	"moose", "newt", "ocelot", "otter", "owl", "panda", "pelican", "puffin",
	"quail", "rabbit", "raven", "robin", "salmon", "seal", "sparrow", "stoat",
	"swan", "tapir", "tiger", "toad", "walrus", "weasel", "wren", "yak",
}

// newWorktreeName picks a readable two-word name ("quiet-otter") that taken
// says isn't in use. rnd(n) returns a number in [0, n).
func newWorktreeName(taken func(string) bool, rnd func(int) int) (string, error) {
	for range 50 {
		n := nameAdjectives[rnd(len(nameAdjectives))] + "-" + nameNouns[rnd(len(nameNouns))]
		if !taken(n) {
			return n, nil
		}
	}
	base := nameAdjectives[rnd(len(nameAdjectives))] + "-" + nameNouns[rnd(len(nameNouns))]
	for i := 2; i < 100; i++ {
		if n := fmt.Sprintf("%s-%d", base, i); !taken(n) {
			return n, nil
		}
	}
	return "", errors.New("couldn't find an unused name")
}

// nameTaken reports whether a branch (local or on origin) or a folder in the
// worktree root already has the name.
func nameTaken(repo, root, name string) bool {
	if _, err := os.Stat(filepath.Join(root, name)); err == nil {
		return true
	}
	return refExists(repo, "refs/heads/"+name) || refExists(repo, "refs/remotes/origin/"+name)
}

// Creating --------------------------------------------------------------------

// newWorktree is a worktree made for a session.
type newWorktree struct {
	repoRoot   string
	path       string // the worktree folder
	sub        string // the picked folder's subfolder, to start in
	branch     string
	base       string
	baseCommit string
}

// createWorktree makes a worktree on a new branch for the repo that dir is
// in, starting from the default branch on origin (or the local one when
// that can't be fetched). log gets a line for each step.
func createWorktree(cfg worktreeConfig, dir string, log func(string)) (newWorktree, error) {
	var w newWorktree
	r, err := findRepo(dir)
	if err != nil {
		return w, err
	}
	w.repoRoot, w.sub = r.root, r.sub
	if w.base, err = defaultBranch(cfg, r.root); err != nil {
		return w, err
	}
	start := "origin/" + w.base
	switch {
	case !hasOrigin(r.root):
		log("No origin remote: starting from the local " + w.base + " branch.")
		start = w.base
	default:
		log("git fetch origin " + w.base)
		if _, err := gitRun(r.root, "fetch", "origin", w.base); err != nil {
			log("Couldn't fetch (offline?): " + firstLine(err.Error()))
			if refExists(r.root, "refs/heads/"+w.base) {
				log("Starting from the local " + w.base + " branch instead.")
				start = w.base
			} else {
				log("Starting from the last fetched origin/" + w.base + " instead.")
			}
		}
	}
	if !refExists(r.root, "refs/remotes/"+start) && !refExists(r.root, "refs/heads/"+start) {
		return w, fmt.Errorf("%s doesn't exist in %s", start, r.root)
	}
	root := worktreeRoot(cfg, r.root)
	w.branch, err = newWorktreeName(func(n string) bool { return nameTaken(r.root, root, n) }, rand.IntN)
	if err != nil {
		return w, err
	}
	w.path = filepath.Join(root, w.branch)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return w, err
	}
	log(fmt.Sprintf("git worktree add -b %s %s %s", w.branch, w.path, start))
	// --no-track: the branch's upstream isn't the default branch, so a
	// later push can't go to it by mistake.
	if _, err := gitRun(r.root, "worktree", "add", "--no-track", "-b", w.branch, w.path, start); err != nil {
		return w, err
	}
	w.baseCommit, _ = gitRun(w.path, "rev-parse", "HEAD")
	log("Created " + w.path + " on branch " + w.branch + ".")
	return w, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(l)
}

// runSetup runs [worktree] setup in a new worktree, sending each line of
// its output to log.
func runSetup(dir, command string, log func(string)) error {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS != "windows":
		cmd = exec.Command("sh", "-c", command)
	case pwshPath() != "":
		// Stop on a failing cmdlet; pass on a program's exit code.
		cmd = exec.Command(pwshPath(), "-NoLogo", "-NoProfile", "-Command",
			"$ErrorActionPreference = 'Stop'; "+command+"; if ($LASTEXITCODE) { exit $LASTEXITCODE }")
	default:
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		cmd = exec.Command(comspec, "/c", command)
	}
	cmd.Dir = dir
	hideWindow(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			log(sc.Text())
		}
		_, _ = io.Copy(io.Discard, pr)
		close(done)
	}()
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	return err
}

// The closing check -------------------------------------------------------------

// closeCheck is what x found out about a worktree session's work.
type closeCheck struct {
	merged   bool     // the branch tip is in the default branch, or its PR was merged
	how      string   // "in origin/main" or "PR #12 merged"
	dirty    []string // uncommitted files (git status --porcelain lines)
	commits  []string // commits not on the default branch ("abc1234 Fix it")
	pr       string   // the PR state, for the warning
	fetchErr string   // the fetch failed; the result may be stale
	noBranch bool     // the local branch is gone
	missing  bool     // the worktree folder is gone
}

// safe reports whether the worktree and branch can be removed without
// asking: the work is merged and nothing is uncommitted.
func (c closeCheck) safe() bool { return c.merged && len(c.dirty) == 0 && !c.noBranch }

// prInfo is the part of `gh pr view --json` the check needs.
type prInfo struct {
	Number     int    `json:"number"`
	State      string `json:"state"` // OPEN, CLOSED or MERGED
	HeadRefOid string `json:"headRefOid"`
	URL        string `json:"url"`
}

// prForBranch asks GitHub for the branch's pull request. ok is false when
// there's none.
func prForBranch(dir, branch string) (pr prInfo, ok bool, err error) {
	out, err := ghRun(dir, "pr", "view", branch, "--json", "number,state,headRefOid,url")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no pull requests found") {
			return pr, false, nil
		}
		return pr, false, err
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return pr, false, fmt.Errorf("gh pr view: %w", err)
	}
	return pr, pr.Number != 0, nil
}

// checkMerged fetches the default branch and works out whether a worktree
// session's branch is merged: its tip is in origin/<base> (a merge or
// fast-forward), or GitHub has a merged PR whose head is the tip (a squash
// or rebase merge, which git can't see). It also lists uncommitted files
// and commits that aren't on the default branch.
func checkMerged(repoRoot, wt, branch, base string) closeCheck {
	var c closeCheck
	dir := wt
	if !isDir(wt) {
		c.missing, dir = true, repoRoot
	}
	if hasOrigin(repoRoot) {
		if _, err := gitRun(repoRoot, "fetch", "origin", base); err != nil {
			c.fetchErr = firstLine(err.Error())
		}
	}
	ref := baseRef(repoRoot, base)
	tip, err := gitRun(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil || tip == "" {
		c.noBranch = true
	} else {
		if _, err := gitRun(repoRoot, "merge-base", "--is-ancestor", tip, ref); err == nil {
			c.merged, c.how = true, "in "+ref
		}
		if out, err := gitRun(repoRoot, "log", "--format=%h %s", "-n", "20", ref+".."+tip); err == nil {
			c.commits = outLines(out)
		}
	}

	switch pr, ok, err := prForBranch(dir, branch); {
	case errors.Is(err, errNoGH):
		c.pr = "gh isn't installed, so a squash-merged PR can't be seen"
	case err != nil:
		c.pr = "PR state unknown: " + firstLine(err.Error())
	case !ok:
		c.pr = "no PR"
	default:
		c.pr = fmt.Sprintf("PR #%d %s", pr.Number, strings.ToLower(pr.State))
		if pr.State == "MERGED" && tip != "" {
			if strings.EqualFold(pr.HeadRefOid, tip) {
				if !c.merged {
					c.merged, c.how = true, fmt.Sprintf("PR #%d merged", pr.Number)
				}
			} else {
				c.pr += ", but the branch has commits after it"
			}
		}
	}

	if !c.missing {
		if out, err := gitRun(wt, "status", "--porcelain", "--untracked-files=all"); err == nil {
			c.dirty = outLines(out)
		} else {
			c.dirty = []string{"?? (git status failed: " + firstLine(err.Error()) + ")"}
		}
	}
	return c
}

// isWorktreeOf reports whether path is one of repoRoot's linked worktrees
// (not the main checkout), so nothing else is ever removed.
func isWorktreeOf(repoRoot, path string) bool {
	out, err := gitRun(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	first := true
	for _, l := range outLines(out) {
		p, ok := strings.CutPrefix(l, "worktree ")
		if !ok {
			continue
		}
		if !first && strings.EqualFold(filepath.Clean(filepath.FromSlash(p)), filepath.Clean(path)) {
			return true
		}
		first = false
	}
	return false
}

// removeWorktree removes a session's worktree and its local branch, and the
// branch on origin when deleteRemote is set. force removes a worktree with
// uncommitted changes (only after "delete anyway"). It returns what it did.
func removeWorktree(repoRoot, wt, branch string, force, deleteRemote bool) ([]string, error) {
	var done []string
	switch {
	case isDir(wt):
		if within(repoRoot, wt) || !isWorktreeOf(repoRoot, wt) {
			return done, fmt.Errorf("%s isn't a worktree of %s; not removing it", wt, repoRoot)
		}
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		if _, err := gitRun(repoRoot, append(args, wt)...); err != nil {
			return done, err
		}
		done = append(done, "removed "+wt)
	default:
		_, _ = gitRun(repoRoot, "worktree", "prune")
	}
	if refExists(repoRoot, "refs/heads/"+branch) {
		if _, err := gitRun(repoRoot, "branch", "-D", branch); err != nil {
			return done, err
		}
		done = append(done, "deleted branch "+branch)
	}
	if deleteRemote && hasOrigin(repoRoot) {
		if _, err := gitRun(repoRoot, "ls-remote", "--exit-code", "--heads", "origin", branch); err == nil {
			if _, err := gitRun(repoRoot, "push", "origin", "--delete", branch); err != nil {
				return done, err
			}
			done = append(done, "deleted origin/"+branch)
		}
	}
	return done, nil
}

// The system prompt -------------------------------------------------------------

// worktreePrompt is appended to Claude's system prompt when it runs in a
// start folder outside the code repo. It has no double quotes, which
// Windows PowerShell would mangle on the way to claude.exe.
func worktreePrompt(repoRoot, wt, branch string) string {
	return fmt.Sprintf("This session works on the git repository whose main checkout is %s. "+
		"It has its own git worktree for that repository at %s, on the branch %s. "+
		"Make every code change for that repository in the worktree, on that branch, and commit there. "+
		"Don't edit files in the main checkout or switch its branch. "+
		"Your working folder is separate and isn't part of that repository.", repoRoot, wt, branch)
}

// repoRootGuess finds a folder's main checkout from the files alone, for
// showing in the dialog without running git: the nearest folder with a
// .git folder, or for a worktree, where its .git file points.
func repoRootGuess(dir string) string {
	for d := dir; ; {
		gitPath := filepath.Join(d, ".git")
		if fi, err := os.Stat(gitPath); err == nil {
			if fi.IsDir() {
				return d
			}
			data, _ := os.ReadFile(gitPath)
			if g, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:"); ok {
				g = filepath.Clean(filepath.FromSlash(strings.TrimSpace(g)))
				// <root>\.git\worktrees\<name>
				if wts := filepath.Dir(g); strings.EqualFold(filepath.Base(wts), "worktrees") {
					return filepath.Dir(filepath.Dir(wts))
				}
			}
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			return dir
		}
		d = p
	}
}
