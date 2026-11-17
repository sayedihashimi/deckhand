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
| c | Config screen: agents, notifications, the session list and open commands (see [The config screen](#the-config-screen)) |
| K | End the session's process (it stays in the list) |
| x (or delete) | End and remove the session |
| `ctrl+\` | Hide or show the session list (see [Hiding the session list](#hiding-the-session-list)) |
| ? or F1 | Show the keys |
| q | Quit (Claude sessions can be resumed next time) |

**Inside a session**

| Key | Action |
|---|---|
| ctrl+q (or ctrl+]) | Back to the session list, showing it again if it's hidden. Every other key except `ctrl+\` and F1 goes to the agent. |
| `ctrl+\` | Hide or show the session list, so the session gets the full width |
| shift+PgUp / shift+PgDn | Scroll back through the output a page at a time |
| F1 | Show the keys. deckhand keeps F1, so it doesn't reach the agent. `?` is typed text and goes to the agent. |

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

## The config screen

Press `c` in the session list to set deckhand up without editing `config.toml`. It opens in the main area, like the new-session dialog. The welcome screen suggests it when there's no config file yet.

- **Agents:** your custom agents, in the order the new-session dialog shows them, then the built-in ones. `enter` edits one, `a` adds one, `d` deletes it and `shift+↑/↓` moves it. The form asks for a name, the kind (a PowerShell command, or a program or script), the command, arguments (for a program), the type (Claude Code or other) and, for a PowerShell command, how to resume it (with `{id}`). It checks the agent with the same rules as loading the file, and shows any problem under the field.
- **Pick from your PowerShell profile:** for a PowerShell agent, "Pick from your PowerShell profile…" (or `ctrl+p`) lists the functions and aliases your profile defines, leaving out PowerShell's own. Type to filter, `enter` to pick. If the function has a `-Resume` parameter, deckhand assumes it starts Claude and suggests `<name> -Resume {id}` as the resume command. Listing them runs PowerShell once with your profile, in the background (at most 10 s).
- **Notifications:** on or off, when (only while deckhand isn't focused, or always), and a button that sends a test notification.
- **Session list:** the key that hides and shows the list (empty means `ctrl+\`), and whether the status line shows while it's hidden. See [Hiding the session list](#hiding-the-session-list).
- **Open:** the editor and terminal commands, with presets (VS Code, Rider, IntelliJ IDEA, VS Code Insiders; Windows Terminal in a new tab or a new window). "Try it" runs the command as typed on the selected session's folder.

Keys: `↑/↓` or `tab`/`shift+tab` move, `←/→` change a choice, `enter` confirms, `ctrl+s` saves, and `esc` leaves (asking first if there are unsaved changes). The title says "unsaved changes" until you save.

**Saving** writes the file atomically (a temporary file, then a rename) and keeps the previous one as `config.toml.bak`. The new settings apply straight away, without a restart: new agents show up in the new-session dialog, and running sessions keep running. A session whose agent you renamed or deleted keeps working until it exits.

> [!NOTE]
> Saving rewrites the whole file, so comments you added by hand are lost (the TOML library can't keep them). If the file has comments other than the sample's, the screen says so and asks before the first save. The old file, comments included, is in `config.toml.bak`. Tables the screen doesn't edit are kept.

## Custom agents (your own scripts and profile functions)

Define your own agents in `~/.config/deckhand/config.toml` (`deckhand config` prints the path and creates a commented sample), or add them on the [config screen](#the-config-screen). They appear first in the agent row.

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

## Hiding the session list

Press `ctrl+\` to hide the session list and give the selected session the full window width. Press it again to show the list. It works the same in the list and inside a session, where `ctrl+\` no longer reaches the agent (Claude Code, Codex and Gemini don't use it). It isn't `F2` because Codex uses `F2` to open its warnings, but you can choose `F2` or another key in the config (below).

- Every session is resized to the new width, as when the window is resized.
- `ctrl+q` (or `ctrl+]`) still goes back to the list, and shows it again if it's hidden. `/` shows it too, since the filter is typed at the top of the list.
- The wheel and clicks all go to the terminal while the list is hidden.
- The end of the session header shows which sessions need you, by the same rules as the list's badge and `@`: `1 need you · 1 finished · ctrl+\ list`. The window title still says "(n need you)".
- deckhand remembers whether the list is hidden (in `%LOCALAPPDATA%\deckhand\prefs.json`) and starts the same way next time.

Change the key, or turn off the status line, on the [config screen](#the-config-screen) or in the config file:

```toml
[list]
toggle_key  = 'ctrl+\'  # or "f2", "f3", ... (single quotes keep the \ as it is)
status_line = true     # false: no "n need you" in the header while the list is hidden
```

The key can't be a plain character, or a key deckhand or the agents need (`enter`, `esc`, `tab`, the arrows, `ctrl+q`, `ctrl+]`, `ctrl+c`, `F1`).

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
