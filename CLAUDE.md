# deckhand: working on this repo

deckhand is a Windows terminal UI for running several AI agent CLIs side by side. See README.md for what it does. This file covers how changes get made.

## Every change goes through a pull request

Don't commit to `main` directly. For each change:

1. **Branch** from an up-to-date `main`:
   ```powershell
   git switch main; git pull --ff-only
   git switch -c <type>/<short-name>     # e.g. feature/notifications, fix/sidebar-highlight
   ```
2. **Make the change**, and keep these up to date along with it:
   - The tests: unit tests for logic, and an e2e test for anything you can see or type (see below).
   - README.md, if keys, config, status icons or behaviour change.
   - The sample config in `agents.go` (`sampleConfig`), if the config file changes.
   - **The screenshot scene** in `screenshot_e2e_test.go`, if the change adds something visible that the scene doesn't show yet. The scene is what reviewers see in the PR, so it should show the new feature.
3. **Check locally**, all of which CI also runs:
   ```powershell
   gofmt -l .                      # must print nothing
   go vet ./...; go vet -tags e2e ./...
   go test ./...
   go test -tags e2e -v -timeout 20m  # real ConPTY; ~4 min on CI, can pass 10 min locally
   $env:DECKHAND_SCREENSHOT="$env:TEMP\deckhand.png"; go test -tags e2e -run TestE2EScreenshot -v
   ```
   Open the PNG and look at it before you push.
4. **Commit**. The first line is a short imperative summary ("Add desktop notifications"), then a blank line, then a body that says what changed and why. Put separate concerns in separate commits.
5. **Push and open the PR**:
   ```powershell
   git push -u origin HEAD
   gh pr create --base main --title "<summary>" --body-file <file>
   ```
   The PR body has these parts:
   - **Summary:** what changed and why, a few bullets.
   - **Testing:** which tests were added or run, and anything checked by hand. Also say what wasn't verified.
   - **Notes:** follow-ups, known limitations, anything a reviewer should look at.
6. **Check the screenshot.** The `screenshot` workflow runs on each push to the PR. It renders the scene, commits the PNG to the `screenshots` branch and adds a "Screenshot" comment to the PR, or updates the existing one. Wait for it (`gh pr checks --watch`), then open the comment and check the image. If the workflow fails, fix it before asking for review.
7. **Don't merge** unless the user asks. After a merge, build and install the new version (below).

## How the PR screenshot works

- `screenshot_e2e_test.go` (`TestE2EScreenshot`) starts deckhand at 120×32 in a pseudo console, with four saved sessions. Three are a fake `claude.exe` (`testdata/hookapp`) running a script from `hookapp.txt` in each session folder: one needs permission, one has finished, and one is working. The fourth is an exited shell. The folders are git repos, so the branch line shows.
- `internal/termshot` turns the emulator's cells into a PNG. It uses Consolas, falls back to Segoe UI Symbol (for braille spinners, ✓ and so on), and draws box and block characters itself. Set `TERMSHOT_FONTS` to override the fonts.
- `.github/workflows/screenshot.yml` runs the test on `windows-latest` and uploads the PNG as an artifact. `.github/scripts/publish-screenshot.sh` commits it to `screenshots` as `pr-<number>/<short-sha>.png` and posts or updates the comment, which is found by its `<!-- deckhand-screenshot -->` marker.
- The `screenshots` branch shares **no history** with `main`. Never merge it, rebase onto it or branch from it. It's safe to squash or reset if it grows: old PR comments lose their images, nothing else breaks.
- Pull requests from forks get no screenshot, because their token can't push.

### Changing the scene

`hookapp.txt` commands:
- `say <text>`: print a line. `\e` is ESC, for colours.
- `print <text>`: print without a newline. `\e` is ESC and `\a` is BEL, for escape sequences such as a window title.
- `reply <tokens> <text>`: add a transcript reply. This sets the model and context shown in the sidebar.
- `event <HookName> [Tool] [| input]`: send a hook event, such as `event PermissionRequest Bash | npm test`.
- `sleep <ms>`

Keep the scene realistic but generic: no real names, paths or other products' branding.

## Build and install

```powershell
go build -ldflags "-X main.version=<version>" -o $HOME\.local\bin\deckhand.exe .
```

Bump the minor version for new features and the patch version for fixes. An install while deckhand is running replaces the file, but the running copy keeps the old version until it's restarted.

## Code notes

- Go 1.27, Bubble Tea v2, `x/vt` and `x/xpty` (ConPTY). It's Windows-first, but keep `GOOS=linux go build ./...` working: put Windows-only code in `_windows.go` files with a stub in `_other.go`.
- Never edit the user's `~/.claude/settings.json`. Claude status comes from per-session hooks passed with `--settings`.
- e2e tests must not pick up the user's real config. `envWith` sets `DECKHAND_CONFIG` to an empty file, so tests that need a config append their own `DECKHAND_CONFIG`.
- Tests must not exit or kill a real Claude during startup. Claude counts that against its fullscreen renderer and turns it off.
