//go:build windows

package watchdog

import (
	"testing"
	"time"
)

func TestComputeTickElapsed_Wraparound(t *testing.T) {
	// Simulate 49.7-day uint32 GetTickCount wraparound:
	// lastInputTick = 0xFFFFFF00 (256 ms before wrap), currentTick = 0x00000100 (256 ms after wrap)
	elapsed := computeTickElapsed(0x00000100, 0xFFFFFF00)
	want := 512 * time.Millisecond
	if elapsed != want {
		t.Fatalf("expected %v across uint32 wraparound, got %v", want, elapsed)
	}
}
