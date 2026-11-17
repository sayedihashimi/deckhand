package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Agent is a CLI that deckhand can run in a session.
type Agent struct {
	Name string // shown in the UI and stored with the session

	// Either a program...
	Exe  string   // on PATH, or a path to an .exe, .cmd or .bat
	Args []string // extra arguments

	// ...or a PowerShell command, run with your profile loaded (so functions
	// and aliases from it work).
	Pwsh   string
	Resume string // PowerShell command to resume a Claude session; {id} is the session ID

	Claude bool // it runs Claude Code: status comes from hooks, sessions can be resumed
	Custom bool // defined in the config file
}

// CanResume reports whether a stopped session can continue its conversation.
func (a Agent) CanResume() bool {
	return a.Claude && (a.Pwsh == "" || a.Resume != "")
}

var builtinAgents = []Agent{
	{Name: "claude", Exe: "claude", Claude: true},
	{Name: "codex", Exe: "codex"},
	{Name: "gemini", Exe: "gemini"},
	{Name: "pwsh", Exe: "pwsh", Args: []string{"-NoLogo"}},
	{Name: "powershell", Exe: "powershell", Args: []string{"-NoLogo"}},
	{Name: "cmd", Exe: "cmd"},
}

// allAgents is the built-in agents plus those from the config file.
var allAgents = builtinAgents

func agentByName(name string) (Agent, bool) {
	for _, a := range allAgents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// availableAgents returns the agents that can run on this machine: custom
// agents first, then built-ins found on PATH (Windows PowerShell is dropped
// when PowerShell 7 is present).
func availableAgents() []Agent {
	var out []Agent
	havePwsh := false
	for _, a := range allAgents {
		if a.Pwsh != "" {
			if pwshPath() == "" {
				continue
			}
		} else if _, err := exec.LookPath(a.Exe); err != nil {
			continue
		}
		if a.Name == "pwsh" {
			havePwsh = true
		}
		if a.Name == "powershell" && havePwsh {
			continue
		}
		out = append(out, a)
	}
	return out
}

// pwshPath finds PowerShell 7, falling back to Windows PowerShell.
func pwshPath() string {
	for _, name := range []string{"pwsh", "powershell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// Config file -----------------------------------------------------------------

type config struct {
	Agents []agentConfig `toml:"agents"`
}

type agentConfig struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command,omitempty"`
	Args    []string `toml:"args,omitempty"`
	Pwsh    string   `toml:"pwsh,omitempty"`
	Type    string   `toml:"type,omitempty"`
	Resume  string   `toml:"resume,omitempty"`
}

// configPath is ~/.config/deckhand/config.toml (DECKHAND_CONFIG overrides).
func configPath() string {
	if p := os.Getenv("DECKHAND_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(dataDir(), "config.toml")
	}
	return filepath.Join(home, ".config", "deckhand", "config.toml")
}

// loadAgents returns the built-in agents plus those in the config file. A
// custom agent with a built-in's name replaces it. On error the built-ins
// are still returned.
func loadAgents() ([]Agent, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return builtinAgents, nil
	}
	if err != nil {
		return builtinAgents, err
	}
	return parseAgents(string(data))
}

func parseAgents(text string) ([]Agent, error) {
	var cfg config
	if _, err := toml.Decode(text, &cfg); err != nil {
		return builtinAgents, fmt.Errorf("%s: %w", configPath(), err)
	}
	var custom []Agent
	seen := map[string]bool{}
	for i, c := range cfg.Agents {
		a, err := c.toAgent()
		if err != nil {
			return builtinAgents, fmt.Errorf("%s: agent %d: %w", configPath(), i+1, err)
		}
		if seen[a.Name] {
			return builtinAgents, fmt.Errorf("%s: agent %q is defined twice", configPath(), a.Name)
		}
		seen[a.Name] = true
		custom = append(custom, a)
	}
	out := custom
	for _, b := range builtinAgents {
		if !seen[b.Name] {
			out = append(out, b)
		}
	}
	return out, nil
}

func (c agentConfig) toAgent() (Agent, error) {
	a := Agent{Name: strings.TrimSpace(c.Name), Custom: true}
	if a.Name == "" {
		return a, errors.New("missing name")
	}
	switch c.Type {
	case "", "other":
	case "claude":
		a.Claude = true
	default:
		return a, fmt.Errorf("type must be \"claude\" or left out, not %q", c.Type)
	}
	switch {
	case c.Pwsh != "" && c.Command != "":
		return a, errors.New(`set either "command" or "pwsh", not both`)
	case c.Pwsh != "":
		a.Pwsh = c.Pwsh
	case c.Command != "":
		if strings.EqualFold(filepath.Ext(c.Command), ".ps1") {
			// Run scripts inside PowerShell so the claude shim applies.
			a.Pwsh = "& " + psQuote(c.Command)
			for _, arg := range c.Args {
				a.Pwsh += " " + psQuote(arg)
			}
		} else {
			a.Exe, a.Args = c.Command, c.Args
		}
	default:
		return a, errors.New(`set "command" or "pwsh"`)
	}
	if c.Resume != "" {
		if a.Pwsh == "" {
			return a, errors.New(`"resume" only applies to "pwsh" agents (a "command" agent with type "claude" resumes with --resume)`)
		}
		if !strings.Contains(c.Resume, "{id}") {
			return a, errors.New(`"resume" must contain {id}`)
		}
		a.Resume = c.Resume
	}
	return a, nil
}

// psQuote quotes a string for PowerShell.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

const sampleConfig = `# deckhand agents: each [[agents]] entry shows up in the new-session dialog,
# before the built-in ones (claude, codex, gemini, pwsh, cmd).
# To change these settings without editing this file, press c in deckhand.
#
# A PowerShell command, function or alias from your profile:
#
# [[agents]]
# name   = "smc"                # shown in the agent row
# pwsh   = "smc"                # run in PowerShell with your profile loaded
# type   = "claude"             # it starts Claude: status from hooks, resumable
# resume = "smc -Resume {id}"   # how to resume; {id} is the Claude session ID
#
# For type = "claude", deckhand defines a PowerShell function named "claude"
# for that session. Your command calls it instead of claude.exe, and it adds
# the hook settings, so neither your script nor ~/.claude/settings.json needs
# to change.
#
# A program (on PATH or a full path), .cmd/.bat, or .ps1 script:
#
# [[agents]]
# name    = "claude-opus"
# command = "claude"
# args    = ["--model", "opus"]
# type    = "claude"

# Desktop notifications when a session needs you or a Claude session
# finishes its turn. "unfocused" (the default) only notifies while
# deckhand's window isn't focused; "always" notifies every time.
#
# [notifications]
# enabled = true
# when    = "unfocused"

# What o (editor) and t (terminal) run for the selected session's folder.
# {dir} is the folder. The command is split into words first, then {dir}
# is filled in, so a folder name can't add arguments. No shell is used.
# Without an editor, o uses VS Code (code) if it's on PATH, else Explorer.
#
# [open]
# editor   = 'rider64.exe "{dir}"'
# terminal = 'wt.exe -w 0 nt -d "{dir}"'

# The session list. toggle_key hides and shows it from anywhere (inside a
# session too, so that key no longer reaches the agent). While it's hidden,
# status_line shows how many sessions need you at the end of the header row.
#
# [list]
# toggle_key  = 'ctrl+\'      # or "f2", "f3", ...
# status_line = true
`

// runConfig implements "deckhand config": print the config path, creating
// a commented sample if there's no file yet.
func runConfig() int {
	p := configPath()
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			_ = os.WriteFile(p, []byte(sampleConfig), 0o644)
		}
	}
	fmt.Println(p)
	if _, err := loadAgents(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// The claude shim -------------------------------------------------------------

const claudeShim = `# Written by deckhand. For one session, a "claude" function wraps claude.exe
# and adds deckhand's hook settings, so status works even when Claude is
# started by your own PowerShell function or script.
function global:claude {
    $exe = Get-Command -Name claude -CommandType Application -ErrorAction Stop | Select-Object -First 1
    $extra = @()
    if ($env:DECKHAND_CLAUDE_SETTINGS) { $extra += '--settings', $env:DECKHAND_CLAUDE_SETTINGS }
    $resuming = $false
    foreach ($a in $args) { if ($a -in '--resume', '-r', '--continue', '-c') { $resuming = $true } }
    if ($env:DECKHAND_CLAUDE_SESSION_ID -and -not $resuming) {
        $extra += '--session-id', $env:DECKHAND_CLAUDE_SESSION_ID
    }
    & $exe.Source @extra @args
}
`

func writeClaudeShim() (string, error) {
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dataDir(), "claude-shim.ps1")
	return p, os.WriteFile(p, []byte(claudeShim), 0o600)
}

// launchSpec is what to run for a session.
type launchSpec struct {
	exe  string
	args []string
	env  []string
}

// buildLaunch works out the command line for an agent. claudeID is the
// Claude session ID to create (or resume, when resume is true).
func buildLaunch(a Agent, sessionID, claudeID string, resume bool) (launchSpec, error) {
	var spec launchSpec
	var settings string
	if a.Claude {
		var err error
		if settings, err = writeClaudeSettings(sessionID); err != nil {
			return spec, fmt.Errorf("write hook settings: %w", err)
		}
	}

	if a.Pwsh != "" {
		shell := pwshPath()
		if shell == "" {
			return spec, errors.New("PowerShell not found")
		}
		script := a.Pwsh
		if resume {
			script = strings.ReplaceAll(a.Resume, "{id}", claudeID)
		}
		if a.Claude {
			shim, err := writeClaudeShim()
			if err != nil {
				return spec, fmt.Errorf("write claude shim: %w", err)
			}
			spec.env = append(spec.env, "DECKHAND_SHIM="+shim, "DECKHAND_CLAUDE_SETTINGS="+settings)
			if !resume {
				spec.env = append(spec.env, "DECKHAND_CLAUDE_SESSION_ID="+claudeID)
			}
			script = ". $env:DECKHAND_SHIM; " + script
		}
		spec.exe = shell
		spec.args = []string{"-NoLogo", "-Command", script + "; exit $LASTEXITCODE"}
		return spec, nil
	}

	exe, err := exec.LookPath(a.Exe)
	if err != nil {
		return spec, fmt.Errorf("%s not found", a.Exe)
	}
	args := append([]string{}, a.Args...)
	if a.Claude {
		if resume {
			args = append(args, "--resume", claudeID)
		} else {
			args = append(args, "--session-id", claudeID)
		}
		args = append(args, "--settings", settings)
	}
	// .cmd and .bat files (e.g. npm installs) have to run through cmd.exe.
	switch strings.ToLower(filepath.Ext(exe)) {
	case ".cmd", ".bat":
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		spec.exe, spec.args = comspec, append([]string{"/c", exe}, args...)
	default:
		spec.exe, spec.args = exe, args
	}
	return spec, nil
}
