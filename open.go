package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/BurntSushi/toml"
)

// Opening a session's folder in an editor, Explorer or a new terminal tab.
// The commands come from [open] in the config file:
//
//	[open]
//	editor   = 'rider64.exe "{dir}"'
//	terminal = 'wt.exe -w 0 nt -d "{dir}"'
//
// A command is split into a program and arguments first, and {dir} is
// substituted into each argument afterwards, so a folder name can't add
// arguments of its own. No shell is involved.

const (
	defaultExplorer = `explorer.exe "{dir}"`
	defaultTerminal = `wt.exe -w 0 nt -d "{dir}"`
	vscodeEditor    = `code "{dir}"`
)

type openConfig struct {
	Editor   string `toml:"editor,omitempty"`
	Terminal string `toml:"terminal,omitempty"`
}

// openSettings is [open] from the config file.
var openSettings openConfig

// loadOpenConfig reads [open] from the config file.
func loadOpenConfig() (openConfig, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return openConfig{}, nil
	}
	if err != nil {
		return openConfig{}, err
	}
	return parseOpenConfig(string(data))
}

func parseOpenConfig(text string) (openConfig, error) {
	var cfg struct {
		Open openConfig `toml:"open"`
	}
	if _, err := toml.Decode(text, &cfg); err != nil {
		return openConfig{}, fmt.Errorf("%s: %w", configPath(), err)
	}
	o := cfg.Open
	for _, f := range []struct {
		name string
		val  *string
	}{{"editor", &o.Editor}, {"terminal", &o.Terminal}} {
		*f.val = strings.TrimSpace(*f.val)
		if *f.val == "" {
			continue
		}
		if err := checkTemplate(*f.val); err != nil {
			return openConfig{}, fmt.Errorf("%s: open.%s: %w", configPath(), f.name, err)
		}
	}
	return o, nil
}

// checkTemplate reports whether a command template can be used.
func checkTemplate(tmpl string) error {
	parts, err := splitCommand(tmpl)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return errors.New("empty command")
	}
	if strings.Contains(parts[0], "{dir}") {
		return errors.New("{dir} can't be part of the program name")
	}
	if !strings.Contains(tmpl, "{dir}") {
		return errors.New(`must contain {dir}, e.g. 'code "{dir}"'`)
	}
	return nil
}

// splitCommand splits a command line into words. Spaces separate words;
// double or single quotes group text with spaces (the quotes are removed).
// Backslashes are ordinary characters, so Windows paths need no escaping.
func splitCommand(s string) ([]string, error) {
	var (
		words   []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		quoteAt int
	)
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, quoteAt, inWord = r, i, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c at position %d", quote, quoteAt+1)
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// expandTemplate splits a command template and puts dir in place of {dir}.
func expandTemplate(tmpl, dir string) (string, []string, error) {
	if err := checkTemplate(tmpl); err != nil {
		return "", nil, err
	}
	parts, _ := splitCommand(tmpl)
	args := make([]string, len(parts)-1)
	for i, p := range parts[1:] {
		args[i] = strings.ReplaceAll(p, "{dir}", dir)
	}
	return parts[0], args, nil
}

// editorTemplate is the command for "o": the configured editor, else VS Code
// if it's on PATH, else Explorer. fallback is true for Explorer, so the
// caller can say how to set an editor.
func editorTemplate(cfg openConfig, lookPath func(string) (string, error)) (tmpl string, fallback bool) {
	if cfg.Editor != "" {
		return cfg.Editor, false
	}
	if _, err := lookPath("code"); err == nil {
		return vscodeEditor, false
	}
	return defaultExplorer, true
}

func terminalTemplate(cfg openConfig) string {
	if cfg.Terminal != "" {
		return cfg.Terminal
	}
	return defaultTerminal
}

// launchOpen runs a command template for dir, detached and without a
// console window. It doesn't wait for the program.
func launchOpen(tmpl, dir string) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("folder not found: %s", dir)
	}
	prog, args, err := expandTemplate(tmpl, dir)
	if err != nil {
		return err
	}
	path, err := exec.LookPath(prog)
	if err != nil {
		return fmt.Errorf("%s not found", prog)
	}
	cmd, err := openCommand(path, args)
	if err != nil {
		return err
	}
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it; explorer.exe exits 1 even when it works
	return nil
}

// cmdLine quotes a program and its arguments for cmd.exe /s /c. Inside
// double quotes cmd.exe treats & | < > ^ as text, but it still expands
// %VAR% and can't escape a quote, so those are refused.
func cmdLine(path string, args []string) (string, error) {
	var b strings.Builder
	for i, a := range append([]string{path}, args...) {
		if strings.ContainsAny(a, `"%`) {
			return "", fmt.Errorf("can't pass %q to a .cmd or .bat file safely", a)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(`"` + a + `"`)
	}
	return b.String(), nil
}
