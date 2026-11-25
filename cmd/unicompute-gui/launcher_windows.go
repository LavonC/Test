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
	var candidates []string

	if pf := os.Getenv("ProgramFiles"); pf != "" {
		candidates = append(candidates,
			filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(pf, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		)
	}
	if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
		candidates = append(candidates,
			filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		)
	}
	if la := os.Getenv("LocalAppData"); la != "" {
		candidates = append(candidates,
			filepath.Join(la, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(la, `Microsoft\Edge\Application\msedge.exe`),
		)
	}

	candidates = append(candidates,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
	)

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
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return nil, nil
}
