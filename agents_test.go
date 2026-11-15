package main

import (
	"strings"
	"testing"
)

func TestParseAgents(t *testing.T) {
	agents, err := parseAgents(`
[[agents]]
name   = "smc"
pwsh   = "smc"
type   = "claude"
resume = "smc -Resume {id}"

[[agents]]
name    = "script"
command = "C:\\scripts\\it's.ps1"
args    = ["-Fast"]

[[agents]]
name    = "codex"
command = 'C:\tools\codex.exe'
`)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, a := range agents {
		names = append(names, a.Name)
	}
	// Custom agents first; "codex" replaces the built-in.
	if got := strings.Join(names, ","); got != "smc,script,codex,claude,gemini,pwsh,powershell,cmd" {
		t.Errorf("order = %s", got)
	}
	smc := agents[0]
	if smc.Pwsh != "smc" || !smc.Claude || !smc.CanResume() || !smc.Custom {
		t.Errorf("smc = %+v", smc)
	}
	if got := agents[1].Pwsh; got != `& 'C:\scripts\it''s.ps1' '-Fast'` {
		t.Errorf(".ps1 should run in PowerShell, got %q", got)
	}
	if agents[2].Exe != `C:\tools\codex.exe` || agents[2].Claude {
		t.Errorf("codex = %+v", agents[2])
	}
}

func TestParseAgentsErrors(t *testing.T) {
	for _, text := range []string{
		`[[agents]]` + "\n" + `pwsh = "x"`,                                                  // no name
		`[[agents]]` + "\n" + `name = "a"`,                                                  // no command
		`[[agents]]` + "\n" + `name = "a"` + "\n" + `pwsh = "x"` + "\n" + `command = "y"`,   // both
		`[[agents]]` + "\n" + `name = "a"` + "\n" + `pwsh = "x"` + "\n" + `type = "gpt"`,    // bad type
		`[[agents]]` + "\n" + `name = "a"` + "\n" + `pwsh = "x"` + "\n" + `resume = "x -r"`, // no {id}
		`[[agents]]` + "\n" + `name = "a"` + "\n" + `command = "x"` + "\n" + `resume = "{id}"`,
		`[[agents]]` + "\n" + `name = "a"` + "\n" + `pwsh = "x"` + "\n" + `[[agents]]` + "\n" + `name = "a"` + "\n" + `pwsh = "y"`,
		`not toml =`,
	} {
		agents, err := parseAgents(text)
		if err == nil {
			t.Errorf("expected an error for:\n%s", text)
		}
		if len(agents) != len(builtinAgents) {
			t.Errorf("built-ins should still be returned on error")
		}
	}
}

func TestBuildLaunchPwshClaude(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	if pwshPath() == "" {
		t.Skip("PowerShell not found")
	}
	a := Agent{Name: "smc", Pwsh: "smc", Resume: "smc -Resume {id}", Claude: true}

	spec, err := buildLaunch(a, "s1", "c1", false)
	if err != nil {
		t.Fatal(err)
	}
	script := spec.args[len(spec.args)-1]
	if script != ". $env:DECKHAND_SHIM; smc; exit $LASTEXITCODE" {
		t.Errorf("script = %q", script)
	}
	env := strings.Join(spec.env, "\n")
	for _, want := range []string{"DECKHAND_SHIM=", "DECKHAND_CLAUDE_SETTINGS=", "DECKHAND_CLAUDE_SESSION_ID=c1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %s:\n%s", want, env)
		}
	}

	spec, _ = buildLaunch(a, "s1", "c1", true)
	if script := spec.args[len(spec.args)-1]; script != ". $env:DECKHAND_SHIM; smc -Resume c1; exit $LASTEXITCODE" {
		t.Errorf("resume script = %q", script)
	}
	if strings.Contains(strings.Join(spec.env, "\n"), "DECKHAND_CLAUDE_SESSION_ID") {
		t.Errorf("resume shouldn't set a new session ID")
	}
}

func TestBuildLaunchPlainPwsh(t *testing.T) {
	t.Setenv("DECKHAND_HOME", t.TempDir())
	if pwshPath() == "" {
		t.Skip("PowerShell not found")
	}
	spec, err := buildLaunch(Agent{Name: "x", Pwsh: "Get-Date"}, "s1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if script := spec.args[len(spec.args)-1]; script != "Get-Date; exit $LASTEXITCODE" || len(spec.env) != 0 {
		t.Errorf("non-Claude agents get no shim: %q %q", script, spec.env)
	}
}
