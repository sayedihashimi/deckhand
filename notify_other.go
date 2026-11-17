//go:build !windows

package main

import (
	"errors"
	"os/exec"
)

func hideWindow(*exec.Cmd) {}

func showToast(title, body, tag string) error {
	return errors.New("desktop notifications are only supported on Windows")
}
