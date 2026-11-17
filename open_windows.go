//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// openCommand builds the command for launchOpen, without a console window.
// Unlike hideWindow it doesn't ask for a hidden window: the editor or
// Explorer window should show, and programs started from a .cmd file
// inherit that request.
//
// A .cmd or .bat file (such as VS Code's code.cmd) runs through cmd.exe with
// every argument quoted, so characters like & in a folder name stay text.
func openCommand(path string, args []string) (*exec.Cmd, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		line, err := cmdLine(path, args)
		if err != nil {
			return nil, err
		}
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		cmd := exec.Command(comspec)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: createNoWindow,
			CmdLine:       `"` + comspec + `" /d /v:off /s /c "` + line + `"`,
		}
		return cmd, nil
	}
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return cmd, nil
}
