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

## Keys

**Session list**

| Key | Action |
|---|---|
| ↑/↓ or j/k | Select a session (its terminal shows on the right) |
| enter | Type into the session; starts or resumes it if it isn't running |
| 1–9 | Jump straight into session N |
| tab | Select the next session that needs you, then the next one that finished |
| n | New session (see below) |
| r | Rename the session |
| K | End the session's process (it stays in the list) |
| x | End and remove the session |
| q | Quit (Claude sessions can be resumed next time) |

**Inside a session**

| Key | Action |
|---|---|
| ctrl+q (or ctrl+]) | Back to the session list. Every other key goes to the agent. |

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

Set `DECKHAND_DEBUG=C:\path\to\log.txt` to log key presses and the bytes forwarded to each agent. Each session's hook events are in `%LOCALAPPDATA%\deckhand\sessions\<id>\events.jsonl`.
