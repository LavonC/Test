//go:build windows

package watchdog

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	modKernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemPowerStatus = modKernel32.NewProc("GetSystemPowerStatus")
	procGetTickCount64       = modKernel32.NewProc("GetTickCount64")

	modUser32             = syscall.NewLazyDLL("user32.dll")
	procGetLastInputInfo  = modUser32.NewProc("GetLastInputInfo")
)

type systemPowerStatus struct {
	ACLineStatus        byte // 0 = Offline (battery), 1 = Online (AC power), 255 = Unknown
	BatteryFlag         byte
	BatteryLifePercent  byte // 0-100 or 255
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// Start begins the 50 ms polling loop on Windows.
func (w *Watchdog) Start() {
	go w.pollLoop()
}

func (w *Watchdog) pollLoop() {
	defer close(w.doneChan)
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	var sustainedIdleStart time.Time

	for {
		select {
		case <-w.stopChan:
			return
		case <-ticker.C:
			onBattery, batteryPct := w.queryPowerStatus()
			userIdle := w.queryUserIdle()
			userActive := userIdle < w.cfg.UserActiveThreshold

			now := time.Now()
			if userActive {
				w.lastActive = now
				sustainedIdleStart = time.Time{}
			} else {
				if sustainedIdleStart.IsZero() {
					sustainedIdleStart = now
				}
			}

			sustainedIdleDuration := time.Duration(0)
			if !sustainedIdleStart.IsZero() {
				sustainedIdleDuration = now.Sub(sustainedIdleStart)
			}

			w.mu.Lock()
			w.status.OnBattery = onBattery
			w.status.BatteryPercent = batteryPct
			w.status.UserActive = userActive
			w.status.UserIdleDuration = userIdle
			w.status.SustainedIdlePeriod = sustainedIdleDuration

			// Determine state transition
			shouldPause := false
			pauseReason := ""

			if onBattery && !w.cfg.IgnoreBattery {
				shouldPause = true
				pauseReason = "Laptop switched to battery power"
				w.state = StatePausedBattery
			} else if userActive && !w.cfg.IgnoreUserActivity {
				shouldPause = true
				pauseReason = "Student input detected (keyboard/mouse/gaming)"
				w.state = StatePausedUserActive
			}

			if shouldPause {
				if w.status.State == StateRunning {
					w.status.State = w.state
					w.status.LastPauseReason = pauseReason
					w.status.LastPauseTime = now
					w.mu.Unlock()

					if w.onPause != nil {
						w.onPause(pauseReason)
					}
					continue
				}
			} else {
				// Eligible for auto-resume if on AC power and sustained idle >= ResumeGracePeriod
				if w.status.State != StateRunning && !onBattery && sustainedIdleDuration >= w.cfg.ResumeGracePeriod {
					w.status.State = StateRunning
					w.state = StateRunning
					w.mu.Unlock()

					if w.onResume != nil {
						w.onResume()
					}
					continue
				}
			}

			w.status.State = w.state
			w.mu.Unlock()
		}
	}
}

func (w *Watchdog) queryPowerStatus() (onBattery bool, batteryPct uint8) {
	var sps systemPowerStatus
	ret, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&sps)))
	if ret == 0 {
		return false, 100 // Fallback assuming AC power if query fails
	}
	// ACLineStatus == 0 indicates battery power
	return sps.ACLineStatus == 0, sps.BatteryLifePercent
}

func (w *Watchdog) queryUserIdle() time.Duration {
	var lii lastInputInfo
	lii.cbSize = uint32(unsafe.Sizeof(lii))

	ret, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii)))
	if ret == 0 {
		return 60 * time.Second // Fallback assuming idle if query fails
	}

	tickRet, _, _ := procGetTickCount64.Call()
	currentTick := uint32(tickRet)

	if currentTick >= lii.dwTime {
		return time.Duration(currentTick-lii.dwTime) * time.Millisecond
	}
	return time.Duration(0)
}
