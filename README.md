# deckhand

A terminal UI for running several AI agent CLIs side by side on Windows. The left pane lists your sessions with a live status; the right pane is the selected session's real terminal.

```
 deckhand          1 need you │ ● api-refactor  claude  C:\src\api
──────────────────────────────│
▌1 ● api-refactor          12s│  Do you want to make this edit to Program.cs?
▌    claude · Edit: C:\src\ap…│  ❯ 1. Yes
 2 ⠹ docs                   3m│    2. Yes, and don't ask again
     claude · Bash            │    3. No
 3 ✓ tests                  1m│
     claude · All 42 tests pa…│
```

It runs your normal Windows `claude.exe` (and `codex`, `gemini`, `pwsh`, `cmd`) in a ConPTY pseudo console, so your own Claude settings, login and proxy configuration apply unchanged. No tmux or WSL is needed.

## Status icons

| Icon | Meaning | Source (Claude) |
|---|---|---|
| ⠹ (spinner) | Working: thinking or running a tool | `UserPromptSubmit`, `PreToolUse`, `PostToolUse` hooks |
| ● (blinking, orange) | Needs you: a permission prompt or a question | `PermissionRequest`, `PreToolUse` on `AskUserQuestion` |
| ✓ (bold green) | Finished its turn, and you haven't looked yet | `Stop` |
| ○ | Idle and seen | |
| ■ | Not running (press enter to resume, shift+enter to start a new one) | process exit |
| ◌ | Starting | |

- **Claude:** status comes from hooks. deckhand passes a per-session settings file with `claude --settings`, so your `~/.claude/settings.json` is never modified.
- **Other CLIs:** status is guessed from output activity, and a terminal bell counts as "needs you".
- **Esc interrupts:** if a Claude turn is interrupted with Esc, no `Stop` hook fires. After 20 s of silence the status drops back to idle and shows "interrupted?".

## Session details

The second line under each session is the agent and what it's doing. For Claude it also shows:

- **Finished:** the start of Claude's reply, in place of "your turn" (the ✓ already says it's your turn). It's the first line of the reply with Markdown removed, cut to fit, and bold until you look at the session. It shows once the turn has ended, never while Claude is still working, so an old reply isn't mistaken for a new one. The session header shows as much of the reply as fits, before the folder.
- **Needs you:** what the tool wants to do, such as `Bash: npm test`.

The third line under each session shows the git branch, then for Claude the model and how much context the conversation uses:

```
▌2 ✓ api-refactor           1m
▌    claude · Fixed the redire…
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

Press `?` in the session list, or `F1` anywhere (also inside a session), for a summary of the keys, the status icons and where the config file is. `esc`, `?`, `F1` or `q` closes it.

**Session list**

| Key | Action |
|---|---|
| ↑/↓ or j/k | Select a session (its terminal shows on the right) |
| home/end or g/G | Select the first or last session |
| enter | Type into the session; starts or resumes it if it isn't running |
| shift+enter | Start a new session with the same agent and folder, right below the selected one. The selected session is left as it is, so its conversation can still be resumed. Claude gets a fresh conversation; a custom agent runs its normal command, not its `resume` one. Where a terminal sends plain enter for shift+enter, it acts like enter |
| 1–9 | Jump straight into session N |
| tab | Select the next session that needs you, then the next one that finished |
| / | Filter the list as you type. Matches the name, agent, folder and branch, ignoring case; letters in order also match, and every word must match. enter keeps the filter (or opens the session if only one matches), tab keeps it, esc clears it |
| @ | Show only sessions that need you or finished since you looked (press again to show all) |
| esc | Clear the filter |
| n | New session (see below) |
| o | Open the session's folder in your editor (see [Opening a session's folder](#opening-a-sessions-folder)) |
| O | Open the session's folder in Explorer |
| t | Open a new Windows Terminal tab in the session's folder |
| r | Rename the session |
| K | End the session's process (it stays in the list) |
| x (or delete) | End and remove the session |
| ? or F1 | Show the keys |
| q | Quit (Claude sessions can be resumed next time) |

**Inside a session**

| Key | Action |
|---|---|
| ctrl+q (or ctrl+]) | Back to the session list. Every other key except F1 goes to the agent. |
| shift+PgUp / shift+PgDn | Scroll back through the output a page at a time |
| F1 | Show the keys. deckhand keeps F1, so it doesn't reach the agent. `?` is typed text and goes to the agent. |

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

## Opening a session's folder

To look at what an agent changed, press `o` to open the selected session's folder in your editor, `O` to open it in Explorer, or `t` to open a new tab in the current Windows Terminal window, starting in that folder. This works whether the session is running or not.

| Key | Default |
|---|---|
| `o` | `code "{dir}"` if VS Code is on PATH, otherwise Explorer. The first time it falls back to Explorer, the footer says how to set an editor. |
| `O` | `explorer.exe "{dir}"` |
| `t` | `wt.exe -w 0 nt -d "{dir}"` |

Set your own editor or terminal command in the config file:

```toml
[open]
editor   = 'rider64.exe "{dir}"'        # or 'code "{dir}"', 'idea64.exe "{dir}"', ...
terminal = 'wt.exe -w 0 nt -d "{dir}"'
```

- `{dir}` is the session's folder. Each command must contain it.
- deckhand splits the command into a program and its arguments first (double or single quotes group words with spaces), then fills in `{dir}`. A folder name with spaces or other characters stays one argument and can't add arguments of its own. No shell is used. To run a PowerShell command, write `pwsh -c ...` yourself.
- The program runs in the background without a console window, and deckhand doesn't wait for it. If it can't start, the footer shows "Couldn't open: ...".
- A `.cmd` or `.bat` file (such as VS Code's `code.cmd`) runs through `cmd.exe` with every argument quoted. A folder with `%` in its name can't be passed to one safely, so deckhand refuses and shows an error.

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
