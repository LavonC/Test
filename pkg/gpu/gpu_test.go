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

func TestExecuteTask_PathTraversalRejection(t *testing.T) {
	mgr, err := NewManager(ManagerConfig{
		StorageDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer mgr.Close()

	ctx := context.Background()

	// 1. Invalid JobIDs with path traversal
	badJobIDs := []string{"", "../escaped", "subdir/job1", "subdir\\job2", ".."}
	for _, id := range badJobIDs {
		resp := mgr.ExecuteTask(ctx, GPUTaskRequest{
			JobID: id,
		})
		if resp.Status != "rejected" {
			t.Errorf("expected JobID %q to be rejected, got status %q", id, resp.Status)
		}
	}

	// 2. Invalid CheckpointBlob with path traversal
	resp := mgr.ExecuteTask(ctx, GPUTaskRequest{
		JobID:          "valid-job",
		CheckpointBlob: "../outside.ckpt",
	})
	if resp.Status != "rejected" {
		t.Errorf("expected path traversal in CheckpointBlob to be rejected, got %q", resp.Status)
	}
}
