package watchdog

import (
	"sync"
	"time"
)

// State describes the current state of the watchdog.
type State string

const (
	StateRunning           State = "running"
	StatePausedBattery     State = "paused_battery"
	StatePausedUserActive  State = "paused_user_active"
	StateStopped           State = "stopped"
)

// Config configures the background hardware and activity watchdog.
type Config struct {
	PollInterval        time.Duration // High-frequency polling (default 50 ms)
	UserActiveThreshold time.Duration // Under this duration, user is considered active (default 1000 ms)
	ResumeGracePeriod   time.Duration // Required sustained idle time before auto-resuming (default 30s)
	IgnoreBattery       bool          // For lab desktops or testing without battery
	IgnoreUserActivity  bool          // For headless testing
}

// Status snapshot returned by Watchdog.
type Status struct {
	State               State         `json:"state"`
	OnBattery           bool          `json:"on_battery"`
	BatteryPercent      uint8         `json:"battery_percent"`
	UserActive          bool          `json:"user_active"`
	UserIdleDuration    time.Duration `json:"user_idle_duration"`
	SustainedIdlePeriod time.Duration `json:"sustained_idle_period"`
	LastPauseReason     string        `json:"last_pause_reason,omitempty"`
	LastPauseTime       time.Time     `json:"last_pause_time,omitempty"`
}

// Watchdog monitors power and user input to protect student laptop responsiveness.
type Watchdog struct {
	mu        sync.RWMutex
	cfg       Config
	state     State
	status    Status
	onPause   func(reason string)
	onResume  func()
	stopChan  chan struct{}
	doneChan  chan struct{}
	lastActive time.Time
}

// NewWatchdog initializes a watchdog with custom pause and resume callbacks.
func NewWatchdog(cfg Config, onPause func(reason string), onResume func()) *Watchdog {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 50 * time.Millisecond
	}
	if cfg.UserActiveThreshold <= 0 {
		cfg.UserActiveThreshold = 1000 * time.Millisecond
	}
	if cfg.ResumeGracePeriod <= 0 {
		cfg.ResumeGracePeriod = 30 * time.Second
	}

	return &Watchdog{
		cfg:        cfg,
		state:      StateRunning,
		onPause:    onPause,
		onResume:   onResume,
		stopChan:   make(chan struct{}),
		doneChan:   make(chan struct{}),
		lastActive: time.Now(),
	}
}

// Status returns a thread-safe snapshot of the watchdog metrics.
func (w *Watchdog) Status() Status {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

// Stop cleanly terminates the watchdog loop.
func (w *Watchdog) Stop() {
	w.mu.Lock()
	if w.state == StateStopped {
		w.mu.Unlock()
		return
	}
	w.state = StateStopped
	close(w.stopChan)
	w.mu.Unlock()

	<-w.doneChan
}
