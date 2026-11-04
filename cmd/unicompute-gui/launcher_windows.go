//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// launchDesktopWindow opens a dedicated, borderless native desktop window on Windows.
func launchDesktopWindow(url string) (*exec.Cmd, error) {
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
	}

	userDataDir := filepath.Join(os.TempDir(), "unicompute_gui_profile")

	for _, exe := range candidates {
		if _, err := os.Stat(exe); err == nil {
			args := []string{
				fmt.Sprintf("--app=%s", url),
				"--window-size=1440,900",
				"--disable-features=TranslateUI",
				"--disable-extensions",
				fmt.Sprintf("--user-data-dir=%s", userDataDir),
			}
			cmd := exec.Command(exe, args...)
			if err := cmd.Start(); err == nil {
				return cmd, nil
			}
		}
	}

	// Fallback to default shell handler if no Chromium browser path matched
	cmd := exec.Command("powershell", "-NoProfile", "-Command", fmt.Sprintf("Start-Process '%s'", url))
	return cmd, cmd.Start()
}
