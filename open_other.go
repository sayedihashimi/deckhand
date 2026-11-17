//go:build !windows

package main

import "os/exec"

func openCommand(path string, args []string) (*exec.Cmd, error) {
	return exec.Command(path, args...), nil
}
