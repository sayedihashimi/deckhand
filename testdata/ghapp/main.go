// ghapp stands in for the GitHub CLI (gh.exe) in deckhand's e2e tests. It
// never talks to GitHub. Its state is files in GHAPP_DIR:
//
//	noauth                gh auth status fails (signed out)
//	pr-<branch>.json      the branch's PR, as gh pr view --json prints it
//	                      ("/" in the branch is "_")
//
// pr create writes pr-<branch>.json for --head, with passing checks. Every
// call is appended to GHAPP_LOG as a JSON line: the arguments and folder.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	args := os.Args[1:]
	dir := os.Getenv("GHAPP_DIR")
	if p := os.Getenv("GHAPP_LOG"); p != "" {
		cwd, _ := os.Getwd()
		line, _ := json.Marshal(map[string]any{"args": args, "cwd": cwd})
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	call := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(call, "auth status"):
		if exists(filepath.Join(dir, "noauth")) {
			fail("You are not logged into any GitHub hosts. To log in, run: gh auth login")
		}
		fmt.Println("github.com\n  ✓ Logged in to github.com account dev (keyring)")
	case strings.HasPrefix(call, "pr view") && slices.Contains(args, "--web"):
		fmt.Fprintf(os.Stderr, "Opening %s in your browser.\n", args[2])
	case strings.HasPrefix(call, "pr view") && len(args) > 2:
		data, err := os.ReadFile(prFile(dir, args[2]))
		if err != nil {
			fail(fmt.Sprintf("no pull requests found for branch %q", args[2]))
		}
		fmt.Println(strings.TrimSpace(string(data)))
	case strings.HasPrefix(call, "pr create"):
		head, base := flag(args, "--head"), flag(args, "--base")
		if head == "" || base == "" {
			fail("ghapp wants --head and --base")
		}
		n := 1
		if m, _ := filepath.Glob(filepath.Join(dir, "pr-*.json")); len(m) > 0 {
			n = len(m) + 1
		}
		oid, _ := exec.Command("git", "rev-parse", head).Output()
		url := fmt.Sprintf("https://github.com/example/api/pull/%d", n)
		pr, _ := json.Marshal(map[string]any{
			"number": n, "state": "OPEN", "url": url, "isDraft": false,
			"headRefOid":        strings.TrimSpace(string(oid)),
			"statusCheckRollup": []map[string]string{{"__typename": "CheckRun", "status": "COMPLETED", "conclusion": "SUCCESS"}},
		})
		if err := os.WriteFile(prFile(dir, head), pr, 0o644); err != nil {
			fail(err.Error())
		}
		fmt.Println(url)
	default:
		fail("ghapp: unknown command " + call)
	}
}

func prFile(dir, branch string) string {
	return filepath.Join(dir, "pr-"+strings.ReplaceAll(branch, "/", "_")+".json")
}

func flag(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
