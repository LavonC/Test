//go:build !windows

package watchdog

import (
	"os"
	"strings"
	"time"
)

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
			w.status.OnBattery = onBattery
			if onBattery && !w.cfg.IgnoreBattery {
				if w.state == StateRunning {
					w.state = StatePausedBattery
					w.status.State = StatePausedBattery
					w.status.LastPauseReason = "Host switched to battery power"
					w.status.LastPauseTime = time.Now()
					w.mu.Unlock()
					if w.onPause != nil {
						w.onPause(w.status.LastPauseReason)
					}
					continue
				}
			} else if !onBattery && w.state == StatePausedBattery {
				w.state = StateRunning
				w.status.State = StateRunning
				w.mu.Unlock()
				if w.onResume != nil {
					w.onResume()
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
