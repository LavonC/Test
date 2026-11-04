package onnx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpoint_SaveLoadAndIntegrity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "onnx-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ckptPath := filepath.Join(tmpDir, "model_checkpoint.ckpt")

	original := Checkpoint{
		JobID:        "job-12345",
		ModelHash:    "deadbeefcafebabe",
		Step:         100,
		Epoch:        2,
		Loss:         0.4523,
		LearningRate: 0.001,
		Weights: map[string][]float32{
			"layer1.weight": {0.1, -0.2, 0.5, 0.8},
			"layer1.bias":   {0.01, 0.02},
		},
		OptimizerState: map[string][]float32{
			"m.layer1.weight": {0.001, 0.002, 0.003, 0.004},
		},
	}

	hash, err := SaveCheckpoint(original, ckptPath)
	if err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}
	if hash == "" {
		t.Fatal("Expected non-empty SHA-256 hash")
	}

	loaded, err := LoadCheckpoint(ckptPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint failed: %v", err)
	}

	if loaded.JobID != original.JobID || loaded.Step != original.Step || len(loaded.Weights) != 2 {
		t.Errorf("Loaded checkpoint does not match original: %+v", loaded)
	}

	// Test tampering detection
	data, _ := os.ReadFile(ckptPath)
	// Mutate one byte in data without updating checksum
	corrupted := append([]byte(nil), data...)
	for i := 0; i < len(corrupted)-10; i++ {
		if corrupted[i] == '1' && corrupted[i+1] == '0' && corrupted[i+2] == '0' {
			corrupted[i] = '9' // Change step 100 to 900
			break
		}
	}
	_ = os.WriteFile(ckptPath, corrupted, 0644)

	_, err = LoadCheckpoint(ckptPath)
	if err == nil {
		t.Error("Expected integrity check error on corrupted checkpoint, got nil")
	}
}

func TestTrainingLoop_PreemptionAndCheckpointResumption(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "onnx-train-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ckptPath := filepath.Join(tmpDir, "updated_checkpoint.ckpt")

	initialCkpt := Checkpoint{
		JobID:     "job-training-test",
		ModelHash: "hash-model-v1",
		Step:      0,
		Loss:      2.0,
		Weights: map[string][]float32{
			"w": {0.5, 0.5},
		},
	}

	engine := NewSessionEngine(ModelConfig{BatchSize: 16}, initialCkpt)

	// Simulate watchdog preemption after 20ms
	go func() {
		time.Sleep(20 * time.Millisecond)
		engine.TriggerPreempt("Student started 3D game (GPU contention)")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := engine.RunTrainingLoop(ctx, 1000, 5, ckptPath)
	if err != nil {
		t.Fatalf("RunTrainingLoop returned unexpected error: %v", err)
	}

	if !result.Preempted {
		t.Fatal("Expected training loop to be preempted by simulated watchdog")
	}
	if result.PreemptReason != "Student started 3D game (GPU contention)" {
		t.Errorf("Unexpected preempt reason: %s", result.PreemptReason)
	}

	// Verify that checkpoint was written and can be loaded
	resumedCkpt, err := LoadCheckpoint(ckptPath)
	if err != nil {
		t.Fatalf("Failed to load saved preemption checkpoint: %v", err)
	}

	if resumedCkpt.Step <= 0 {
		t.Errorf("Expected resumed checkpoint step > 0, got %d", resumedCkpt.Step)
	}

	// Resuming on a second engine (simulating migration to next laptop)
	secondEngine := NewSessionEngine(ModelConfig{BatchSize: 16}, resumedCkpt)
	secondResult, err := secondEngine.RunTrainingLoop(context.Background(), 10, 5, ckptPath)
	if err != nil {
		t.Fatalf("Second engine failed: %v", err)
	}

	if secondResult.TotalSteps <= resumedCkpt.Step {
		t.Errorf("Expected second engine to advance beyond step %d, got %d", resumedCkpt.Step, secondResult.TotalSteps)
	}
}
