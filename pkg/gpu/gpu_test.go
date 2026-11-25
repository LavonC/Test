package gpu

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/unicompute/obj1/pkg/gpu/onnx"
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

func TestExecuteTask_RelativeCheckpointBlob(t *testing.T) {
	storageDir := t.TempDir()
	mgr, err := NewManager(ManagerConfig{
		StorageDir: storageDir,
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer mgr.Close()

	ctx := context.Background()

	// 1. Valid relative path inside StorageDir must NOT be rejected with path traversal error
	respValid := mgr.ExecuteTask(ctx, GPUTaskRequest{
		JobID:          "valid-job-rel",
		CheckpointBlob: "checkpoints/job1.bin",
	})
	if respValid.Status == "rejected" {
		t.Errorf("expected relative path inside StorageDir to be accepted, got rejected: %v", respValid.Error)
	}

	// 2. Traversal ../escape.bin must be rejected
	respEscape := mgr.ExecuteTask(ctx, GPUTaskRequest{
		JobID:          "valid-job-rel",
		CheckpointBlob: "../escape.bin",
	})
	if respEscape.Status != "rejected" {
		t.Errorf("expected ../escape.bin to be rejected, got: %s", respEscape.Status)
	}
}

func TestExecuteTask_RelativeCheckpointResumeFromDifferentCWD(t *testing.T) {
	storageDir := t.TempDir()
	otherCWD := t.TempDir()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	if err := os.Chdir(otherCWD); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	ckptRel := filepath.Join("checkpoints", "prev-job", "checkpoint.ckpt")
	ckptAbs := filepath.Join(storageDir, ckptRel)
	_, err = onnx.SaveCheckpoint(onnx.Checkpoint{
		JobID:        "prev-job",
		Step:         42,
		Epoch:        3,
		Loss:         0.25,
		LearningRate: 0.001,
		Weights: map[string][]float32{
			"w1": {0.5, -0.25},
		},
	}, ckptAbs)
	if err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}

	mgr, err := NewManager(ManagerConfig{
		StorageDir: storageDir,
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer mgr.Close()

	resp := mgr.ExecuteTask(context.Background(), GPUTaskRequest{
		JobID:          "resumed-job",
		CheckpointBlob: ckptRel,
		Manifest: TaskCapsuleManifest{
			TaskType:        "train",
			TotalBatches:    5,
			CheckpointEvery: 5,
		},
	})
	if resp.Status != "completed" {
		t.Fatalf("expected completed status, got %q (err: %s)", resp.Status, resp.Error)
	}
	if resp.ProcessedBatches != 47 {
		t.Fatalf("expected checkpoint resume from step 42 + 5 batches = 47, got %d", resp.ProcessedBatches)
	}
}

