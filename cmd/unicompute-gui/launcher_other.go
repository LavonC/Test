//go:build !windows && !linux

package main

import (
	"os/exec"
)

func launchDesktopWindow(url string) (*exec.Cmd, error) {
	cmd := exec.Command("open", url)
	return cmd, cmd.Start()
}
