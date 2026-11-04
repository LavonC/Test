//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// launchDesktopWindow opens a dedicated native desktop window on Linux (Ubuntu 18.04, 20.04, 24.04).
func launchDesktopWindow(url string) (*exec.Cmd, error) {
	// If running headless or without X11/Wayland display server, return nil
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, nil
	}

	userDataDir := filepath.Join(os.TempDir(), "unicompute_gui_profile")

	browsers := []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium-browser",
		"chromium",
		"microsoft-edge",
		"microsoft-edge-stable",
		"brave-browser",
	}

	for _, b := range browsers {
		if path, err := exec.LookPath(b); err == nil {
			args := []string{
				fmt.Sprintf("--app=%s", url),
				"--window-size=1440,900",
				"--class=UniCompute",
				"--disable-features=TranslateUI",
				fmt.Sprintf("--user-data-dir=%s", userDataDir),
			}
			cmd := exec.Command(path, args...)
			if err := cmd.Start(); err == nil {
				return cmd, nil
			}
		}
	}

	// Fallback to xdg-open on standard desktop environments
	if path, err := exec.LookPath("xdg-open"); err == nil {
		cmd := exec.Command(path, url)
		return cmd, cmd.Start()
	}

	return nil, nil
}
