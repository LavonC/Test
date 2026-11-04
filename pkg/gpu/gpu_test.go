package gpu

import (
	"context"
	"testing"
	"time"
)

func TestProbeHardware(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	profile, err := ProbeHardware(ctx)
	if err != nil {
		t.Fatalf("ProbeHardware failed: %v", err)
	}

	t.Logf("Discovered GPU Profile: Vendor=%s, Device=%s, VRAM=%d MB, DirectML=%v, CUDA=%v, Backends=%v",
		profile.Vendor, profile.DeviceName, profile.TotalVRAMBytes/(1024*1024),
		profile.DirectMLSupported, profile.CUDASupported, profile.SupportedBackends)

	if len(profile.SupportedBackends) == 0 {
		t.Errorf("Expected at least one supported backend (even CPU fallback)")
	}
}
