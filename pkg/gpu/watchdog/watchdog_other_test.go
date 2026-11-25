//go:build !windows

package watchdog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsLinuxOnBattery_ScansMainsSupplies(t *testing.T) {
	base := t.TempDir()

	// 1. Battery-only entry (no Mains) -> false
	batDir := filepath.Join(base, "BAT0")
	if err := os.MkdirAll(batDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(batDir, "type"), []byte("Battery\n"), 0644)
	if isLinuxOnBattery(base) {
		t.Fatalf("expected false when no Mains power supply exists")
	}

	// 2. Mains supply ADP1 offline (0) -> on battery (true)
	adpDir := filepath.Join(base, "ADP1")
	if err := os.MkdirAll(adpDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(adpDir, "type"), []byte("Mains\n"), 0644)
	_ = os.WriteFile(filepath.Join(adpDir, "online"), []byte("0\n"), 0644)
	if !isLinuxOnBattery(base) {
		t.Fatalf("expected true when Mains supply ADP1 is offline (0)")
	}

	// 3. Second Mains supply ACAD online (1) -> on AC power (false)
	acadDir := filepath.Join(base, "ACAD")
	if err := os.MkdirAll(acadDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(acadDir, "type"), []byte("Mains\n"), 0644)
	_ = os.WriteFile(filepath.Join(acadDir, "online"), []byte("1\n"), 0644)
	if isLinuxOnBattery(base) {
		t.Fatalf("expected false when at least one Mains supply is online (1)")
	}
}
