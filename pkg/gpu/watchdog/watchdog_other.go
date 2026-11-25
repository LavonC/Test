//go:build !windows

package watchdog

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func isInputTelemetrySupported() bool {
	return false
}

// Start begins the polling loop on Linux/macOS.
func (w *Watchdog) Start() {
	w.mu.Lock()
	if w.started || w.state == StateStopped {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()
	go w.pollLoop()
}

func (w *Watchdog) pollLoop() {
	defer close(w.doneChan)
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopChan:
			return
		case <-ticker.C:
			onBattery := w.queryLinuxBattery()
			w.mu.Lock()
			w.status.InputTelemetryAvailable = false
			w.status.OnBattery = onBattery
			if onBattery && !w.cfg.IgnoreBattery {
				if w.state == StateRunning {
					w.state = StatePausedBattery
					w.status.State = StatePausedBattery
					w.status.LastPauseReason = "Host switched to battery power"
					w.status.LastPauseTime = time.Now()
					reason := w.status.LastPauseReason
					cb := w.onPause
					w.mu.Unlock()
					if cb != nil {
						cb(reason)
					}
					continue
				}
			} else if !onBattery && w.state == StatePausedBattery {
				w.state = StateRunning
				w.status.State = StateRunning
				cb := w.onResume
				w.mu.Unlock()
				if cb != nil {
					cb()
				}
				continue
			}
			w.mu.Unlock()
		}
	}
}

func (w *Watchdog) queryLinuxBattery() bool {
	return isLinuxOnBattery("/sys/class/power_supply")
}

func isLinuxOnBattery(baseDir string) bool {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return false
	}
	foundMains := false
	anyOnline := false
	for _, entry := range entries {
		supplyDir := filepath.Join(baseDir, entry.Name())
		typeData, err := os.ReadFile(filepath.Join(supplyDir, "type"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(typeData)) != "Mains" {
			continue
		}
		foundMains = true
		onlineData, err := os.ReadFile(filepath.Join(supplyDir, "online"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(onlineData)) == "1" {
			anyOnline = true
			break
		}
	}
	if !foundMains {
		return false
	}
	return !anyOnline
}
