package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// folderSuggestion is one entry in the folder picker.
type folderSuggestion struct {
	Path   string
	Recent bool
}

const maxRecentSuggestions = 5

// folderSuggestions returns folders matching what's been typed.
//
//   - Empty input: recent folders, then the folders under cwd.
//   - A path ("C:\src\ap", "..\x", "~\code\"): folders in that directory whose
//     names match the last segment, after any recent folders that match.
//   - A plain word ("api"): recent folders and folders under cwd whose names
//     match it.
//
// Name matches rank prefix first, then substring, then letters in order
// (fuzzy), ignoring case. listDir returns the subfolder names of a directory.
func folderSuggestions(input, cwd string, recents []string, listDir func(string) []string, limit int) []folderSuggestion {
	in := strings.TrimSpace(input)
	var out []folderSuggestion
	seen := map[string]bool{}
	add := func(p string, recent bool) {
		key := strings.ToLower(filepath.Clean(p))
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, folderSuggestion{Path: p, Recent: recent})
	}

	// Work out the directory to list and the name fragment to match.
	dir, seg := cwd, in
	if isPathLike(in) {
		p := resolvePath(in, cwd)
		if endsWithSep(in) || isDriveRoot(p) {
			dir, seg = p, ""
		} else {
			dir, seg = filepath.Dir(p), filepath.Base(p)
		}
	}

	// Recent folders first.
	n := 0
	for _, r := range recents {
		if n >= maxRecentSuggestions {
			break
		}
		// For a typed path, a recent folder must sit in that directory.
		inDir := !isPathLike(in) || strings.EqualFold(filepath.Clean(filepath.Dir(r)), filepath.Clean(dir))
		if in == "" || inDir && matchScore(filepath.Base(r), seg) > 0 {
			add(r, true)
			n++
		}
	}

	type scored struct {
		name  string
		score int
	}
	var matches []scored
	showHidden := strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "$")
	for _, name := range listDir(dir) {
		if !showHidden && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$")) {
			continue
		}
		if s := matchScore(name, seg); s > 0 {
			matches = append(matches, scored{name, s})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return strings.ToLower(matches[i].name) < strings.ToLower(matches[j].name)
	})
	for _, m := range matches {
		add(filepath.Join(dir, m.name), false)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// matchScore ranks how well name matches the typed fragment: 4 exact,
// 3 prefix, 2 substring, 1 letters in order, 0 no match.
func matchScore(name, frag string) int {
	if frag == "" {
		return 1
	}
	n, f := strings.ToLower(name), strings.ToLower(frag)
	switch {
	case n == f:
		return 4
	case strings.HasPrefix(n, f):
		return 3
	case strings.Contains(n, f):
		return 2
	}
	i := 0
	for _, r := range n {
		if i < len(f) && rune(f[i]) == r {
			i++
		}
	}
	if i == len(f) {
		return 1
	}
	return 0
}

func isPathLike(s string) bool {
	return strings.ContainsAny(s, `\/`) || strings.HasPrefix(s, "~") ||
		(len(s) >= 2 && s[1] == ':') || s == "." || s == ".."
}

func endsWithSep(s string) bool { return strings.HasSuffix(s, `\`) || strings.HasSuffix(s, "/") }

func isDriveRoot(p string) bool { return len(p) <= 3 && len(p) >= 2 && p[1] == ':' }

// resolvePath expands ~ and makes the path absolute relative to cwd.
func resolvePath(s, cwd string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return cwd
	}
	if strings.HasPrefix(s, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			s = home + s[1:]
		}
	}
	if len(s) == 2 && s[1] == ':' {
		return s + `\`
	}
	if !filepath.IsAbs(s) {
		s = filepath.Join(cwd, s)
	}
	return filepath.Clean(s)
}

// parentPath returns the input with its last path segment removed, keeping a
// trailing separator so the picker lists the parent's folders.
func parentPath(input, cwd string) string {
	p := resolvePath(input, cwd)
	if strings.TrimSpace(input) == "" || isDriveRoot(p) {
		return withSep(p)
	}
	// "C:\src\ap" and "C:\src\api\" both go to "C:\src\".
	return withSep(filepath.Dir(p))
}

func withSep(p string) string {
	if endsWithSep(p) {
		return p
	}
	return p + `\`
}

// dirLister lists subfolder names, caching each directory for the life of
// the dialog so typing stays fast.
type dirLister struct {
	cache map[string][]string
}

func newDirLister() *dirLister { return &dirLister{cache: map[string][]string{}} }

func (l *dirLister) list(dir string) []string {
	key := strings.ToLower(filepath.Clean(dir))
	if names, ok := l.cache[key]; ok {
		return names
	}
	entries, err := os.ReadDir(dir)
	var names []string
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
				continue
			}
			// Symlinks and junctions that point at folders.
			if e.Type()&os.ModeSymlink != 0 {
				if fi, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && fi.IsDir() {
					names = append(names, e.Name())
				}
			}
		}
	}
	l.cache[key] = names
	return names
}
