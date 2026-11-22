//go:build !windows && !linux

package main

import (
	"os/exec"
)

func launchDesktopWindow(url string) (*exec.Cmd, error) {
	cmd := exec.Command("open", url)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return nil, nil
}
