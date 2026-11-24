//go:build !windows

package watchdog

import (
	"os"
	"strings"
	"time"
)

func isInputTelemetrySupported() bool {
	return false
}

// Start begins the polling loop on Linux/macOS.
func (w *Watchdog) Start() {
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
	// Check sysfs power_supply status
	data, err := os.ReadFile("/sys/class/power_supply/AC/online")
	if err == nil {
		return strings.TrimSpace(string(data)) == "0"
	}
	return false
}
