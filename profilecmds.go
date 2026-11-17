package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

// The functions and aliases in your PowerShell profile, for picking a
// custom agent in the config screen. deckhand runs PowerShell once with the
// profile loaded and asks for the functions and aliases it defined.

// profileCommand is a function or alias from the profile.
type profileCommand struct {
	Name   string `json:"Name"`
	Kind   string `json:"Kind"`   // "Function" or "Alias"
	Target string `json:"Target"` // what an alias points to
	Source string `json:"Source"` // the module, if any
	Resume bool   `json:"Resume"` // it has a -Resume parameter
}

const profileMarker = "<<deckhand-commands>>"

// profileScript lists the functions and aliases that are loaded, leaving
// out PowerShell's own (the default session's commands, drive functions
// such as C:, and modules that ship with PowerShell or Windows). The JSON
// comes after a marker line, because a profile can print anything.
const profileScript = `$ErrorActionPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$skip = @{}
foreach ($c in [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault2().Commands) { $skip[$c.Name] = $true }
$core = '^(Microsoft\.PowerShell\..*|Microsoft\.WSMan\..*|CimCmdlets|PSReadLine|PackageManagement|PowerShellGet|PSDiagnostics|ThreadJob)$'
$sys = @($PSHOME, $env:windir) | Where-Object { $_ } | ForEach-Object { $_.TrimEnd('\') + '\' }
$out = foreach ($c in Get-Command -ListImported -CommandType Function, Alias) {
    if ($skip[$c.Name] -or $c.Name -match '^[A-Za-z]:$') { continue }
    if ($c.Source -match $core) { continue }
    $base = if ($c.Module) { [string]$c.Module.ModuleBase } else { '' }
    if ($base -and @($sys | Where-Object { $base.StartsWith($_, [StringComparison]::OrdinalIgnoreCase) }).Count) { continue }
    $params = $null
    try { $params = if ($c.CommandType -eq 'Alias') { $c.ResolvedCommand.Parameters } else { $c.Parameters } } catch {}
    [pscustomobject]@{
        Name   = $c.Name
        Kind   = [string]$c.CommandType
        Target = if ($c.CommandType -eq 'Alias') { [string]$c.Definition } else { '' }
        Source = [string]$c.Source
        Resume = [bool]($params -and $params.ContainsKey('Resume'))
    }
}
Write-Output '` + profileMarker + `'
ConvertTo-Json -InputObject @($out) -Compress -Depth 2
`

const profileTimeout = 10 * time.Second

// loadProfileCommands runs PowerShell with your profile and returns its
// functions and aliases. Whatever the profile writes to stderr is ignored.
// DECKHAND_TEST_PROFILE names a script to load instead of the profile
// (tests use it, so they don't depend on the real profile).
func loadProfileCommands() ([]profileCommand, error) {
	shell := pwshPath()
	if shell == "" {
		return nil, errors.New("PowerShell not found")
	}
	script := profileScript
	args := []string{"-NoLogo", "-NonInteractive"}
	if p := os.Getenv("DECKHAND_TEST_PROFILE"); p != "" {
		args = append(args, "-NoProfile")
		script = ". " + psQuote(p) + "\n" + script
	}
	args = append(args, "-EncodedCommand", encodePS(script))

	ctx, cancel := context.WithTimeout(context.Background(), profileTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, args...)
	hideWindow(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("PowerShell took more than %s to load your profile", profileTimeout)
	}
	list, err := parseProfileCommands(stdout.String())
	if err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("PowerShell failed: %w", runErr)
		}
		return nil, err
	}
	return list, nil // a non-zero exit (e.g. from the profile) doesn't matter
}

// parseProfileCommands reads the JSON after the marker, sorted by name.
func parseProfileCommands(out string) ([]profileCommand, error) {
	i := strings.LastIndex(out, profileMarker)
	if i < 0 {
		return nil, errors.New("PowerShell didn't list any commands")
	}
	data := strings.TrimSpace(out[i+len(profileMarker):])
	var list []profileCommand
	if err := json.Unmarshal([]byte(data), &list); err != nil {
		var one profileCommand // older PowerShell can unwrap a one-item array
		if err2 := json.Unmarshal([]byte(data), &one); err2 != nil || one.Name == "" {
			return nil, fmt.Errorf("reading PowerShell's command list: %w", err)
		}
		list = []profileCommand{one}
	}
	seen := map[string]bool{}
	out2 := list[:0]
	for _, c := range list {
		key := strings.ToLower(c.Name)
		if c.Name == "" || seen[key] {
			continue
		}
		seen[key] = true
		out2 = append(out2, c)
	}
	sort.SliceStable(out2, func(a, b int) bool { return strings.ToLower(out2[a].Name) < strings.ToLower(out2[b].Name) })
	return out2, nil
}

// resumeHint is the resume template to suggest for a picked command.
func (c profileCommand) resumeHint() string {
	if c.Resume {
		return c.Name + " -Resume {id}"
	}
	return ""
}

// encodePS encodes a script for powershell -EncodedCommand (UTF-16LE, base64).
func encodePS(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}
