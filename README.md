# deckhand

A terminal UI for running several AI agent CLIs side by side on Windows. The left pane lists your sessions with a live status; the right pane is the selected session's real terminal.

```
 deckhand          1 need you │ ● api-refactor  claude  C:\src\api
──────────────────────────────│
▌1 ● api-refactor          12s│  Do you want to make this edit to Program.cs?
▌    claude · permission: Edit│  ❯ 1. Yes
 2 ⠹ docs                   3m│    2. Yes, and don't ask again
     claude · Bash            │    3. No
 3 ✓ tests                  1m│
     claude · your turn       │
```

It runs your normal Windows `claude.exe` (and `codex`, `gemini`, `pwsh`, `cmd`) in a ConPTY pseudo console, so your own Claude settings, login and proxy configuration apply unchanged. No tmux or WSL is needed.

## Status icons

| Icon | Meaning | Source (Claude) |
|---|---|---|
| ⠹ (spinner) | Working: thinking or running a tool | `UserPromptSubmit`, `PreToolUse`, `PostToolUse` hooks |
| ● (blinking, orange) | Needs you: a permission prompt or a question | `PermissionRequest`, `PreToolUse` on `AskUserQuestion` |
| ✓ (bold green) | Finished its turn, and you haven't looked yet | `Stop` |
| ○ | Idle and seen | |
| ■ | Not running (press enter to resume) | process exit |
| ◌ | Starting | |

- **Claude:** status comes from hooks. deckhand passes a per-session settings file with `claude --settings`, so your `~/.claude/settings.json` is never modified.
- **Other CLIs:** status is guessed from output activity, and a terminal bell counts as "needs you".
- **Esc interrupts:** if a Claude turn is interrupted with Esc, no `Stop` hook fires. After 20 s of silence the status drops back to idle and shows "interrupted?".

## Session details

The third line under each session shows the git branch, then for Claude the model and how much context the conversation uses:

```
▌2 ✓ api-refactor           1m
▌    claude · your turn
▌    feature/login*↑2 · opus 5.5 · 84k
```

- **Branch:** `*` means uncommitted changes (untracked files count), `↑2↓1` is commits ahead of and behind the upstream. Outside a git repo the line shows the folder.
- **Model and context:** read from the end of the Claude transcript. The number is the input tokens of the latest reply, including cached ones, which is how much context the next turn starts with. deckhand can't tell the model's context window from the transcript, so it shows tokens rather than a percentage.
- Details refresh every 3 s for running and selected sessions and every 30 s for the rest, in the background. The session header shows them in full.

## Notifications

deckhand shows a Windows notification when a session needs you (a permission prompt, a question, or a bell from another CLI) or a Claude session finishes its turn. A permission notification says what the tool wants to do (for example the Bash command), and a "finished" one quotes the start of Claude's reply.

By default it only notifies while deckhand's window isn't focused (the terminal reports focus changes). If the terminal doesn't report focus, it notifies for every session except the one on screen. Change this in the config file:

```toml
[notifications]
enabled = true          # false turns them off
when    = "unfocused"   # or "always"
```

Run `deckhand notify-test` to check that notifications appear. If they don't, check Windows' notification settings and Do Not Disturb. Notifications come from Windows PowerShell, so that's the name they show.

## Keys

**Session list**

| Key | Action |
|---|---|
| ↑/↓ or j/k | Select a session (its terminal shows on the right) |
| enter | Type into the session; starts or resumes it if it isn't running |
| 1–9 | Jump straight into session N |
| tab | Select the next session that needs you, then the next one that finished |
| / | Filter the list as you type. Matches the name, agent, folder and branch, ignoring case; letters in order also match, and every word must match. enter keeps the filter (or opens the session if only one matches), tab keeps it, esc clears it |
| @ | Show only sessions that need you or finished since you looked (press again to show all) |
| esc | Clear the filter |
| n | New session (see below) |
| r | Rename the session |
| K | End the session's process (it stays in the list) |
| x | End and remove the session |
| q | Quit (Claude sessions can be resumed next time) |

**Inside a session**

| Key | Action |
|---|---|
| ctrl+q (or ctrl+]) | Back to the session list. Every other key goes to the agent. |
| shift+PgUp / shift+PgDn | Scroll back through the output a page at a time |

Modified keys that plain terminal input can't express, such as shift+enter (a new line in Claude), ctrl+enter and ctrl+tab, go to the agent in the keyboard protocol it asked for (kitty `CSI u`, or xterm modifyOtherKeys), and as plain enter/tab to apps that asked for neither. For deckhand to see those modifiers, the terminal it runs in has to report them. In Windows Terminal, deckhand turns on win32-input-mode for this. In other terminals, shift+enter may arrive as plain enter; Claude's `\` then enter still adds a new line.

**Mouse**

- **Wheel over the terminal:** scrolls back through the output. Any key, or wheeling back down, returns to the live view. If the app turns on mouse reporting itself (a fullscreen TUI such as Claude's fullscreen renderer), the wheel and clicks go to the app instead, so it scrolls its own view.
- **Wheel or click in the session list:** moves the selection. Clicking the terminal focuses it.
- **Selecting text:** deckhand uses mouse reporting, so in Windows Terminal hold **shift** while dragging.

**New session dialog**

The folder box lists matching folders as you type. Recent folders come first, then the folders in the directory you're typing in. Matching ignores case and also finds letters typed in order (`dnw` finds `dotnet-new-web`). A plain word searches the current folder and your recent folders. The "Start in" line shows exactly which folder will be used.

| Key | Action |
|---|---|
| tab | Complete the highlighted (or best) match and list its subfolders |
| ↑/↓ | Choose from the list (↑ from the top moves to the agent row) |
| ctrl+w | Up one level |
| shift+tab | Switch to the agent row, then ←/→ to change agent |
| enter | Start the session (with an empty box: the current folder) |
| esc | Cancel |

The last agent you used is preselected. Sessions are named after their folder; press `r` to rename one.

## Custom agents (your own scripts and profile functions)

Define your own agents in `~/.config/deckhand/config.toml` (`deckhand config` prints the path and creates a commented sample). They appear first in the agent row.

```toml
# A PowerShell function or alias from your profile:
[[agents]]
name   = "smc"
pwsh   = "smc"                # runs in PowerShell with your profile loaded
type   = "claude"             # it starts Claude: status from hooks, resumable
resume = "smc -Resume {id}"   # how to resume; {id} is the Claude session ID

# A program (on PATH or a full path), .cmd/.bat or .ps1 script:
[[agents]]
name    = "claude-opus"
command = "claude"
args    = ["--model", "opus"]
type    = "claude"
```

**How status works when your script starts Claude:** for a `pwsh` agent (and a `.ps1` script) with `type = "claude"`, deckhand defines a PowerShell function named `claude` for that session only (`%LOCALAPPDATA%\deckhand\claude-shim.ps1`). PowerShell prefers functions to programs, so when your function or script calls `claude`, it calls the shim. The shim runs the real `claude.exe` and adds deckhand's `--settings` (the hooks) and `--session-id`, leaving out `--session-id` when you're resuming. Your script doesn't need to forward arguments, and neither it nor `~/.claude/settings.json` is changed.

A `command` agent that is a program or `.cmd` file gets the extra arguments on its command line, so it must pass them on to `claude`.

## Resuming

The session list is saved in `%LOCALAPPDATA%\deckhand\sessions.json`. Each Claude session is started with its own `--session-id`, so after you quit and restart deckhand, pressing enter on a Claude session runs `claude --resume <id>`.

## Build

```powershell
go install github.com/sayedihashimi/deckhand@latest          # or, from a clone:
go build -ldflags "-X main.version=0.1.0" -o $HOME\.local\bin\deckhand.exe .
go test ./...                                              # unit tests
go test -tags e2e -run TestE2EShell -v                     # drives deckhand in a real ConPTY
$env:DECKHAND_E2E_CLAUDE=1; go test -tags e2e -run TestE2EClaude -v   # sends one tiny prompt
```

Install the binary in a folder without spaces in its path. Hook commands then don't need quoting, so they work whichever shell Claude runs them in.

## Debugging

Set `DECKHAND_DEBUG=C:\path\to\log.txt` to log key presses, the bytes forwarded to each agent, and notification errors. `DECKHAND_NOTIFY_LOG=C:\path\to\file` writes notifications to that file instead of showing them (the tests use this). Each session's hook events are in `%LOCALAPPDATA%\deckhand\sessions\<id>\events.jsonl`.
