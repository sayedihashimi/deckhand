package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// How paths are shown. [[paths.alias]] entries replace a path prefix with a
// short name, for demos and screen sharing, and the home folder shows as ~.
// It's display only: stored paths and the commands deckhand runs keep the
// real paths.

type pathAlias struct {
	prefix string // cleaned, no trailing separator (except a drive root)
	name   string
}

// pathAliases is the [[paths.alias]] list, longest prefix first.
var pathAliases []pathAlias

type pathAliasConfig struct {
	Prefix string `toml:"prefix"`
	Name   string `toml:"name"`
}

type pathsConfig struct {
	Alias []pathAliasConfig `toml:"alias,omitempty"`
}

// loadPathsConfig reads [[paths.alias]] from the config file.
func loadPathsConfig() ([]pathAlias, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parsePathsConfig(string(data))
}

func parsePathsConfig(text string) ([]pathAlias, error) {
	var cfg struct {
		Paths pathsConfig `toml:"paths"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath(), err)
	}
	var out []pathAlias
	for i, a := range cfg.Paths.Alias {
		prefix, name := strings.TrimSpace(a.Prefix), strings.TrimSpace(a.Name)
		if prefix == "" || name == "" {
			return nil, fmt.Errorf("%s: paths.alias %d needs a prefix and a name", configPath(), i+1)
		}
		if strings.ContainsAny(name, `\/`) {
			return nil, fmt.Errorf("%s: paths.alias %d: the name %q can't contain \\ or /", configPath(), i+1, name)
		}
		out = append(out, pathAlias{prefix: cleanPrefix(expandHome(prefix)), name: name})
	}
	sortAliases(out)
	return out, nil
}

// sortAliases puts the longest prefixes first, so the longest match wins.
func sortAliases(a []pathAlias) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && len(a[j].prefix) > len(a[j-1].prefix); j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// expandHome turns a leading ~ into the home folder.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, `~\`) || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home + p[1:]
		}
	}
	return p
}

// cleanPrefix drops trailing separators, but keeps a drive root's ("C:\").
func cleanPrefix(p string) string {
	p = strings.TrimRight(p, `\/`)
	if len(p) == 2 && p[1] == ':' {
		p += `\`
	}
	return p
}

func isSep(c byte) bool { return c == '\\' || c == '/' }

// prefixAt reports whether s[i:] starts with prefix, ignoring case and
// treating \ and / alike, at a folder boundary: the prefix is followed by
// the end of s, a separator, or (with until) a character that ends a path
// in free text. It returns the prefix's length in s.
func prefixAt(s string, i int, prefix string, until func(string, int) bool) (int, bool) {
	n := len(prefix)
	if len(s)-i < n {
		return 0, false
	}
	for k := 0; k < n; k++ {
		a, b := s[i+k], prefix[k]
		if isSep(a) && isSep(b) {
			continue
		}
		if a != b && !strings.EqualFold(string(a), string(b)) {
			// Bytes of non-ASCII letters: compare them as a whole below.
			if a < 0x80 || b < 0x80 {
				return 0, false
			}
			if !strings.EqualFold(s[i:i+n], prefix) {
				return 0, false
			}
			break
		}
	}
	switch {
	case i+n == len(s), isSep(prefix[n-1]), isSep(s[i+n]):
		return n, true
	case until != nil && until(s, i+n):
		return n, true
	}
	return 0, false
}

// displayRules are the replacements in the order they're tried: the
// aliases (longest first), then the home folder as ~.
func displayRules() []pathAlias {
	rules := pathAliases
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		rules = append(rules[:len(rules):len(rules)], pathAlias{prefix: cleanPrefix(home), name: "~"})
	}
	return rules
}

// applyRule returns the short name and the rest of p after a matched
// prefix. A prefix with a trailing separator (a drive root) keeps it.
func applyRule(r pathAlias, rest string) string {
	if isSep(r.prefix[len(r.prefix)-1]) {
		return r.name + `\` + rest
	}
	return r.name + rest
}

// displayPath is how deckhand shows a path: with the longest matching
// alias's name in place of its prefix, else with ~ for the home folder,
// else as it is. Every path deckhand shows goes through it (or through
// displayPathsIn, for messages that contain paths).
func displayPath(p string) string {
	if p == "" {
		return p
	}
	for _, r := range displayRules() {
		if n, ok := prefixAt(p, 0, r.prefix, nil); ok {
			return applyRule(r, p[n:])
		}
	}
	return p
}

// endChars end a path in free text: spaces, quotes and punctuation that a
// path is unlikely to contain.
const endChars = " \t\r\n\"'`,;:)]}>|?!"

// pathEnd reports whether s[j] ends a path in free text. A "." does only at
// the end of a sentence, so "notes.old" isn't taken for "notes".
func pathEnd(s string, j int) bool {
	if s[j] == '.' {
		return j+1 == len(s) || strings.IndexByte(endChars, s[j+1]) >= 0
	}
	return strings.IndexByte(endChars, s[j]) >= 0
}

// pathStart reports whether a path can start after c in free text.
func pathStart(c byte) bool {
	return strings.IndexByte(endChars, c) >= 0 || strings.IndexByte("([{<=", c) >= 0
}

// displayPathsIn applies displayPath to every path inside a message, such
// as an error from git: each matching prefix that starts at a word
// boundary and ends at a folder boundary is replaced.
func displayPathsIn(text string) string {
	rules := displayRules()
	if len(rules) == 0 || text == "" {
		return text
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(text); i++ {
		if i > 0 && !pathStart(text[i-1]) {
			continue
		}
		for _, r := range rules {
			if n, ok := prefixAt(text, i, r.prefix, pathEnd); ok {
				b.WriteString(text[last:i])
				b.WriteString(r.name)
				if isSep(r.prefix[len(r.prefix)-1]) {
					b.WriteString(`\`)
				}
				i += n - 1
				last = i + 1
				break
			}
		}
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

// displayInput is a path as deckhand fills it into a text box of the
// new-session dialog: shown the same way, and still something the box
// understands (resolvePath expands it back). An alias on its own gets a
// trailing \, so it reads as a path, not a name to search for.
func displayInput(p string) string {
	d := displayPath(p)
	if d == "~" {
		return `~\`
	}
	for _, r := range pathAliases {
		if d == r.name {
			return d + `\`
		}
	}
	return d
}

// expandAlias turns a typed path that starts with an alias name and a
// separator ("notes\x", "notes\") back into the real path. Other input,
// such as a plain "notes" (a folder name to search for), is returned as
// it is.
func expandAlias(s string) string {
	for _, r := range pathAliases {
		if n := len(r.name); len(s) > n && strings.EqualFold(s[:n], r.name) && isSep(s[n]) {
			rest := s[n:]
			if isSep(r.prefix[len(r.prefix)-1]) {
				rest = strings.TrimLeft(rest, `\/`)
			}
			return r.prefix + rest
		}
	}
	return s
}
