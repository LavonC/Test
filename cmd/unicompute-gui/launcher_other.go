//go:build !windows && !linux

package main

import (
	"os"
	"os/exec"
)

func launchDesktopWindow(url string, _ string) (*exec.Cmd, error) {
	cmd := exec.Command("open", url)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return nil, nil
}

func createGUIProfileDir() (string, error) {
	return os.MkdirTemp("", "unicompute-gui-profile-*")
}
