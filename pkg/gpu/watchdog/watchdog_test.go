package watchdog

import (
	"testing"
	"time"
)

func TestWatchdog_StopWithoutStartDoesNotDeadlock(t *testing.T) {
	w := NewWatchdog(Config{
		PollInterval: 20 * time.Millisecond,
	}, nil, nil)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("Watchdog.Stop() deadlocked when called without Start()")
	}

	if st := w.Status(); st.State != StateStopped {
		t.Fatalf("expected StateStopped after Stop(), got %q", st.State)
	}
}
