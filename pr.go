package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Pull requests: p pushes a session's branch and opens a pull request for
// it with gh, or opens the one it already has, and the sidebar shows the
// PR's number and state. The git and gh work is here; the keys, the
// confirmation and the polling are in prui.go.
//
// Only GitHub is supported. A repo counts as on GitHub when origin's URL
// (as configured, before any insteadOf) is on github.com, a *.ghe.com
// host or $GH_HOST. The default branch is never pushed.

const (
	prPollEvery = 60 * time.Second // the PR badge, for sessions with a branch
	prPollOff   = 5 * time.Minute  // when gh or a GitHub remote is missing, or on the default branch
)

// prInfo is the part of `gh pr view --json` deckhand uses.
type prInfo struct {
	Number     int       `json:"number"`
	State      string    `json:"state"` // OPEN, CLOSED or MERGED
	HeadRefOid string    `json:"headRefOid"`
	URL        string    `json:"url"`
	IsDraft    bool      `json:"isDraft"`
	Checks     []prCheck `json:"statusCheckRollup"`

	// Only m asks for these (prMergeFields).
	Title            string `json:"title,omitempty"`
	BaseRefName      string `json:"baseRefName,omitempty"`
	Mergeable        string `json:"mergeable,omitempty"`        // MERGEABLE, CONFLICTING or UNKNOWN
	MergeStateStatus string `json:"mergeStateStatus,omitempty"` // CLEAN, BLOCKED, BEHIND, DIRTY, ...
}

// prCheck is one entry of statusCheckRollup: a check run (status and
// conclusion) or a commit status (state).
type prCheck struct {
	Typename   string `json:"__typename"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

const prFields = "number,state,headRefOid,url,isDraft,statusCheckRollup"

func parsePR(out string) (prInfo, error) {
	var pr prInfo
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return pr, fmt.Errorf("gh pr view: %w", err)
	}
	return pr, nil
}

// prForBranch asks GitHub for the branch's pull request. ok is false when
// there's none.
func prForBranch(dir, branch string) (pr prInfo, ok bool, err error) {
	return prView(dir, branch, prFields)
}

// prView is prForBranch with the given --json fields.
func prView(dir, branch, fields string) (pr prInfo, ok bool, err error) {
	out, err := ghRun(dir, "pr", "view", branch, "--json", fields)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no pull requests found") {
			return pr, false, nil
		}
		return pr, false, err
	}
	pr, err = parsePR(out)
	if err != nil {
		return pr, false, err
	}
	return pr, pr.Number != 0, nil
}

type checksState int

const (
	checksNone checksState = iota
	checksPending
	checksPassing
	checksFailing
)

// checks sums up the PR's checks: failing if any failed, else pending if
// any haven't finished, else passing. Skipped and neutral ones pass.
func (p prInfo) checks() checksState {
	if len(p.Checks) == 0 {
		return checksNone
	}
	pending := false
	for _, c := range p.Checks {
		switch strings.ToUpper(c.Conclusion) {
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
			return checksFailing
		}
		switch strings.ToUpper(c.State) {
		case "FAILURE", "ERROR":
			return checksFailing
		case "PENDING", "EXPECTED":
			pending = true
		}
		if c.Typename != "StatusContext" && c.State == "" && !strings.EqualFold(c.Status, "COMPLETED") {
			pending = true
		}
	}
	if pending {
		return checksPending
	}
	return checksPassing
}

// prState is "open", "draft", "merged" or "closed".
func (p prInfo) prState() string {
	switch s := strings.ToLower(p.State); {
	case s == "open" && p.IsDraft:
		return "draft"
	case s == "":
		return "open"
	default:
		return s
	}
}

// prLabel is the PR's state in words, for the session header:
// "PR #12 open · checks passing".
func prLabel(p prInfo) string {
	s := fmt.Sprintf("PR #%d %s", p.Number, p.prState())
	if st := p.prState(); st == "open" || st == "draft" {
		switch p.checks() {
		case checksPassing:
			s += " · checks passing"
		case checksFailing:
			s += " · checks failing"
		case checksPending:
			s += " · checks running"
		}
	}
	return s
}

// prBadge is the PR's state in a few cells, for the sidebar: the number
// and a mark for the checks while it's open ("#12✓", "#12✗", "#12…"), or
// the state when it isn't ("#12 merged").
func prBadge(p prInfo) (number, mark string) {
	number = fmt.Sprintf("#%d", p.Number)
	switch st := p.prState(); st {
	case "open", "draft":
		switch p.checks() {
		case checksPassing:
			mark = "✓"
		case checksFailing:
			mark = "✗"
		case checksPending:
			mark = "…"
		}
		if st == "draft" {
			mark = " draft" + mark
		}
	default:
		mark = " " + st
	}
	return number, mark
}

// GitHub ----------------------------------------------------------------------

// originURL is origin's URL as configured (insteadOf isn't applied), or
// "" without an origin.
func originURL(dir string) string {
	u, err := gitRun(dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u)
}

// remoteHost is the host of a git remote URL: https://host/..., ssh://
// user@host/... or the scp form user@host:path. Local paths have none.
func remoteHost(u string) string {
	if strings.Contains(u, "://") {
		p, err := url.Parse(u)
		if err != nil || p.Scheme == "file" {
			return ""
		}
		return strings.ToLower(p.Hostname())
	}
	host, _, ok := strings.Cut(u, ":")
	if !ok || len(host) < 2 || strings.ContainsAny(host, `/\`) {
		return "" // C:\repo or a relative path
	}
	if _, h, ok := strings.Cut(host, "@"); ok {
		host = h
	}
	return strings.ToLower(host)
}

// isGitHubHost reports whether a host is GitHub: github.com, a GitHub
// Enterprise Cloud host (*.ghe.com), or the host gh is set to ($GH_HOST).
func isGitHubHost(host, ghHost string) bool {
	if host == "" {
		return false
	}
	return host == "github.com" || host == "ssh.github.com" || strings.HasSuffix(host, ".ghe.com") ||
		(ghHost != "" && strings.EqualFold(host, ghHost))
}

// githubHost is origin's host when it's on GitHub, or an error saying why
// the repo has no GitHub remote.
func githubHost(dir string) (string, error) {
	u := originURL(dir)
	if u == "" {
		return "", errors.New("the repo has no origin remote, so there's nowhere to push or open a PR")
	}
	h := remoteHost(u)
	if !isGitHubHost(h, os.Getenv("GH_HOST")) {
		return "", fmt.Errorf("origin (%s) isn't on GitHub, and PRs need a GitHub remote", u)
	}
	if h == "ssh.github.com" {
		h = "github.com"
	}
	return h, nil
}

// githubRepo is origin's repo for gh -R, as HOST/OWNER/REPO, or "" when
// the URL doesn't name one.
func githubRepo(u string) string {
	host := remoteHost(u)
	if host == "" {
		return ""
	}
	if host == "ssh.github.com" {
		host = "github.com"
	}
	path := ""
	if strings.Contains(u, "://") {
		p, err := url.Parse(u)
		if err != nil {
			return ""
		}
		path = p.Path
	} else {
		_, path, _ = strings.Cut(u, ":")
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	owner, name, ok := strings.Cut(path, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return host + "/" + owner + "/" + name
}

var errNeedGH = errors.New("this needs the GitHub CLI (gh), which isn't installed: get it from https://cli.github.com")

// ghReady checks that the repo is on GitHub and that gh is installed and
// signed in to its host. It returns the host.
func ghReady(dir string) (string, error) {
	host, err := githubHost(dir)
	if err != nil {
		return "", err
	}
	if _, err := ghRun(dir, "auth", "status", "--hostname", host); err != nil {
		if errors.Is(err, errNoGH) {
			return "", errNeedGH
		}
		return "", fmt.Errorf("gh isn't signed in to %s: run gh auth login", host)
	}
	return host, nil
}

// Planning a push ---------------------------------------------------------------

// prPlan is what p found out before it asks.
type prPlan struct {
	dir    string
	branch string
	base   string
	pr     prInfo
	hasPR  bool
	title  string // the first commit's subject, which gh pr create --fill-first uses
	ahead  int    // commits not on the base branch
	push   bool   // origin doesn't have the branch at the local tip
	dirty  int    // uncommitted files, which the push doesn't include
}

// planPR works out what p should do for the branch checked out in dir.
// want is the session's branch (a worktree session), which must still be
// the one checked out; base is the branch it started from, or "" for the
// repo's default branch. It refuses the default branch, a detached HEAD,
// a repo that isn't on GitHub, and gh missing or signed out.
func planPR(dir, want, base string) (prPlan, error) {
	p := prPlan{dir: dir}
	cur, err := gitRun(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return p, fmt.Errorf("%s isn't in a git repo with commits", dir)
	}
	switch {
	case cur == "HEAD":
		return p, errors.New("HEAD is detached: check out a branch to push")
	case want != "" && cur != want:
		return p, fmt.Errorf("the worktree is on %s, not the session's branch %s: switch back to push it", cur, want)
	}
	p.branch = cur
	if base == "" {
		if base, err = defaultBranch(worktreeSettings, dir); err != nil {
			return p, err
		}
	}
	p.base = base
	if protectedBranch(dir, cur, base) {
		return p, fmt.Errorf("%s is the default branch, and p never pushes it: work on another branch (W starts a worktree session)", cur)
	}
	if _, err := ghReady(dir); err != nil {
		return p, err
	}
	pr, ok, err := prForBranch(dir, cur)
	if err != nil {
		return p, err
	}
	p.pr, p.hasPR = pr, ok

	ref := baseRef(dir, base)
	if out, err := gitRun(dir, "rev-list", "--count", ref+"..HEAD"); err == nil {
		_, _ = fmt.Sscanf(out, "%d", &p.ahead)
	}
	if out, err := gitRun(dir, "log", "--reverse", "--format=%s", ref+"..HEAD"); err == nil {
		if l := outLines(out); len(l) > 0 {
			p.title = strings.TrimSpace(l[0])
		}
	}
	tip, _ := gitRun(dir, "rev-parse", "HEAD")
	p.push = true
	if out, err := gitRun(dir, "ls-remote", "--heads", "origin", "refs/heads/"+cur); err == nil && out != "" {
		p.push = !strings.EqualFold(strings.Fields(out)[0], tip)
	}
	if out, err := gitRun(dir, "status", "--porcelain"); err == nil {
		p.dirty = len(outLines(out))
	}
	return p, nil
}

// protectedBranch reports whether branch is the default branch: the base,
// or what origin/HEAD points to.
func protectedBranch(dir, branch, base string) bool {
	if branch == base {
		return true
	}
	if out, err := gitRun(dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(out, "origin/") == branch
	}
	return false
}

// prQuestion is the confirmation p asks before pushing, or "" when there's
// nothing to push and the PR can just be opened. none is set when there's
// no PR and nothing to put in one.
func prQuestion(p prPlan) (q string, none bool) {
	dirty := uncommittedNote(p.dirty)
	if p.hasPR {
		if !p.push || p.pr.prState() == "merged" || p.pr.prState() == "closed" {
			return "", false
		}
		return fmt.Sprintf("Push %s to origin, then open PR #%d?%s y/n", p.branch, p.pr.Number, dirty), false
	}
	if p.ahead == 0 {
		return "", true
	}
	return fmt.Sprintf("Push %s to origin and open a PR into %s, %q?%s y/n", p.branch, p.base, p.title, dirty), false
}

// uncommittedNote is " (n uncommitted files aren't included)", or "" for
// none, for a question about pushing or merging commits.
func uncommittedNote(n int) string {
	switch {
	case n == 1:
		return " (1 uncommitted file isn't included)"
	case n > 1:
		return fmt.Sprintf(" (%d uncommitted files aren't included)", n)
	}
	return ""
}

// Pushing and creating ----------------------------------------------------------

// pushBranch pushes the branch to the same name on origin and makes that
// its upstream. It never forces.
func pushBranch(dir, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("%q isn't a branch to push", branch)
	}
	if _, err := gitRun(dir, "push", "-u", "origin", branch); err != nil {
		return fmt.Errorf("push rejected, not retried with --force: %s", pushMessage(err))
	}
	return nil
}

// pushMessage is git's reason a push failed, on one line: the rejected
// refs and what the remote said, else the errors, without the hints.
func pushMessage(err error) string {
	text := strings.TrimPrefix(err.Error(), "git push: ")
	var refs, errs []string
	for _, l := range outLines(text) {
		l = strings.Join(strings.Fields(l), " ")
		switch {
		case strings.Contains(l, "[rejected]"), strings.Contains(l, "[remote rejected]"), strings.HasPrefix(l, "remote:"):
			refs = append(refs, l)
		case strings.HasPrefix(l, "error:"), strings.HasPrefix(l, "fatal:"):
			errs = append(errs, l)
		}
	}
	switch {
	case len(refs) > 0:
		return strings.Join(refs, " · ")
	case len(errs) > 0:
		return strings.Join(errs, " · ")
	}
	return firstLine(text)
}

// createPR opens a pull request for the pushed branch into base, with the
// first commit's subject and body, and returns it.
func createPR(dir, branch, base string) (prInfo, error) {
	out, err := ghRun(dir, "pr", "create", "--fill-first", "--base", base, "--head", branch)
	if err != nil {
		return prInfo{}, fmt.Errorf("couldn't create the PR: %s", firstLine(strings.TrimPrefix(err.Error(), "gh pr: ")))
	}
	lines := outLines(out)
	link := ""
	if len(lines) > 0 {
		link = strings.TrimSpace(lines[len(lines)-1])
	}
	if pr, ok, err := prForBranch(dir, branch); err == nil && ok {
		return pr, nil
	}
	return prInfo{State: "OPEN", URL: link, Number: prNumberFromURL(link)}, nil
}

// prNumberFromURL reads the number at the end of a PR's URL (".../pull/12").
func prNumberFromURL(u string) int {
	n := 0
	if i := strings.LastIndex(u, "/"); i >= 0 {
		_, _ = fmt.Sscanf(u[i+1:], "%d", &n)
	}
	return n
}

// openPR opens the PR in the browser.
func openPR(dir string, pr prInfo) error {
	target := pr.URL
	if target == "" {
		target = fmt.Sprint(pr.Number)
	}
	_, err := ghRun(dir, "pr", "view", target, "--web")
	return err
}

// Polling -------------------------------------------------------------------------

// prPoll is one poll of a branch's PR.
type prPoll struct {
	pr    *prInfo       // nil: no PR
	keep  bool          // couldn't tell (gh failed): keep what's shown
	retry time.Duration // when to poll again
}

// pollPR looks up the branch's PR for the sidebar. It doesn't run gh when
// the repo isn't on GitHub, gh isn't installed, or the branch is the
// default branch.
func pollPR(dir, branch, base string) prPoll {
	if _, err := githubHost(dir); err != nil {
		return prPoll{retry: prPollOff}
	}
	if base == "" {
		b, err := defaultBranch(worktreeSettings, dir)
		if err != nil {
			return prPoll{retry: prPollOff}
		}
		base = b
	}
	if protectedBranch(dir, branch, base) {
		return prPoll{retry: prPollOff}
	}
	pr, ok, err := prForBranch(dir, branch)
	switch {
	case errors.Is(err, errNoGH):
		return prPoll{retry: prPollOff}
	case err != nil:
		return prPoll{keep: true, retry: prPollEvery}
	case !ok:
		return prPoll{retry: prPollEvery}
	}
	return prPoll{pr: &pr, retry: prPollEvery}
}
