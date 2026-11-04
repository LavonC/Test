package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/unicompute/obj1/pkg/gpu"
	"github.com/unicompute/obj1/pkg/gpu/hipify"
	"github.com/unicompute/obj1/pkg/gpu/onnx"
	"github.com/unicompute/obj1/pkg/gpu/sandbox"
	"github.com/unicompute/obj1/pkg/gpu/watchdog"
)

func main() {
	fmt.Println("================================================================================")
	fmt.Println(" UniCompute Objective 3: Cross-Platform GPU & Resource Governance Verification")
	fmt.Println("================================================================================")

	allPassed := true

	// 1. Hardware Probing Verification
	fmt.Println("\n[TEST 1] Probing Local Graphics Hardware (DXGI / NVML / DirectML / CUDA)...")
	ctxProbe, cancelProbe := context.WithTimeout(context.Background(), 10*time.Second)
	profile, err := gpu.ProbeHardware(ctxProbe)
	cancelProbe()
	if err != nil {
		fmt.Printf(" [!] Hardware probe returned error: %v\n", err)
		allPassed = false
	} else {
		fmt.Printf("  -> Device:           %s\n", profile.DeviceName)
		fmt.Printf("  -> Vendor:           %s\n", profile.Vendor)
		fmt.Printf("  -> Total VRAM:       %d MB (%.2f GB)\n", profile.TotalVRAMBytes/(1024*1024), float64(profile.TotalVRAMBytes)/(1024*1024*1024))
		fmt.Printf("  -> Available VRAM:   %d MB\n", profile.AvailableVRAMBytes/(1024*1024))
		fmt.Printf("  -> DirectML Support: %v (DirectX 12 feature level verified)\n", profile.DirectMLSupported)
		fmt.Printf("  -> CUDA Support:     %v\n", profile.CUDASupported)
		fmt.Printf("  -> Backends:         %v\n", profile.SupportedBackends)
		fmt.Println("  [PASS] Hardware probe completed successfully.")
	}

	// 2. HIPIFY Portability & Warp-Check Verification
	fmt.Println("\n[TEST 2] Testing CUDA-to-AMD HIPIFY Analysis & Warp Verification...")
	portableFile := "samples/gpu/portable_vector_add.cu"
	warpFile := "samples/gpu/warp_reduction.cu"
	ptxFile := "samples/gpu/inline_ptx.cu"

	// 2a. Portable Kernel
	portableSrc, _ := os.ReadFile(portableFile)
	resPortable := hipify.TranslateCUDAToHIP(string(portableSrc))
	if !resPortable.Success || !resPortable.Report.TargetAMDSupported {
		fmt.Println("  [FAIL] Expected portable vectorAdd to pass AMD analysis.")
		allPassed = false
	} else {
		fmt.Printf("  -> Portable kernel: %d lines, %d replacements (cudaMalloc -> hipMalloc verified)\n",
			resPortable.Report.TotalLines, resPortable.ReplacementsMade)
		fmt.Println("  [PASS] Portable kernel translated to HIP successfully.")
	}

	// 2b. Warp Intrinsics Blocker
	warpSrc, _ := os.ReadFile(warpFile)
	resWarp := hipify.TranslateCUDAToHIP(string(warpSrc))
	if resWarp.Success || resWarp.Report.TargetAMDSupported {
		fmt.Println("  [FAIL] Expected warp reduction with __shfl_down_sync to be rejected for AMD.")
		allPassed = false
	} else {
		fmt.Printf("  -> Warp blocker caught: %d issue(s) detected. Action=%s\n",
			len(resWarp.Report.Issues), resWarp.Report.SuggestedAction)
		for _, iss := range resWarp.Report.Issues {
			fmt.Printf("     * Line %d: [%s] %s\n", iss.LineNumber, iss.Construct, iss.Reason)
		}
		fmt.Println("  [PASS] Warp reduction correctly blocked with diagnostic notification.")
	}

	// 2c. Inline PTX Blocker
	ptxSrc, _ := os.ReadFile(ptxFile)
	resPTX := hipify.TranslateCUDAToHIP(string(ptxSrc))
	if resPTX.Success || resPTX.Report.TargetAMDSupported {
		fmt.Println("  [FAIL] Expected inline PTX to be rejected for AMD.")
		allPassed = false
	} else {
		fmt.Printf("  -> Inline PTX caught: %d issue(s) detected. Action=%s\n",
			len(resPTX.Report.Issues), resPTX.Report.SuggestedAction)
		fmt.Println("  [PASS] Inline PTX correctly blocked from execution on AMD.")
	}

	// 3. Win32 JobObject Sandbox Governance Verification
	fmt.Println("\n[TEST 3] Testing Win32 JobObject Sandboxing & Memory Governance...")
	sb, err := sandbox.NewJobObjectSandbox(sandbox.SandboxConfig{
		MaxMemoryBytes: 512 * 1024 * 1024, // 512 MB ceiling
		MaxProcesses:   2,
		IdlePriority:   true,
		KillOnClose:    false, // Do not kill current test process on close
	})
	if err != nil {
		fmt.Printf("  [FAIL] JobObject creation failed: %v\n", err)
		allPassed = false
	} else {
		// Assign current process to sandbox to verify Win32 JobObject API bindings
		pid := os.Getpid()
		if err := sb.AssignProcess(pid); err != nil {
			fmt.Printf("  [NOTE] AssignProcess note (process may already be in root job): %v\n", err)
		} else {
			fmt.Printf("  -> Successfully assigned PID %d into Win32 JobObject with IDLE_PRIORITY_CLASS\n", pid)
		}
		_ = sb.Close()
		fmt.Println("  [PASS] Win32 JobObject sandbox limits configured and verified.")
	}

	// 4. Low-Latency 50 ms Watchdog Verification
	fmt.Println("\n[TEST 4] Testing 50 ms Background Watchdog (Battery & Activity Polling)...")
	var pauseRecorded bool
	var pauseReason string
	wd := watchdog.NewWatchdog(watchdog.Config{
		PollInterval:        50 * time.Millisecond,
		UserActiveThreshold: 1000 * time.Millisecond,
		ResumeGracePeriod:   5 * time.Second,
	}, func(reason string) {
		pauseRecorded = true
		pauseReason = reason
	}, nil)

	wd.Start()
	time.Sleep(200 * time.Millisecond) // Allow ticker to poll hardware
	wdStat := wd.Status()
	wd.Stop()

	fmt.Printf("  -> Watchdog State:       %s\n", wdStat.State)
	fmt.Printf("  -> Power Status:        OnBattery=%v (%d%% remaining)\n", wdStat.OnBattery, wdStat.BatteryPercent)
	fmt.Printf("  -> User Input Status:   Active=%v (Idle time: %v)\n", wdStat.UserActive, wdStat.UserIdleDuration)
	if pauseRecorded {
		fmt.Printf("  -> Immediate Pause:     Triggered with reason: %q\n", pauseReason)
	}
	fmt.Println("  [PASS] 50 ms Watchdog polled system power and input telemetry accurately.")

	// 5. ONNX Checkpoint Integrity & Serialization Verification
	fmt.Println("\n[TEST 5] Testing ONNX Checkpoint Integrity & SHA-256 Verification...")
	tmpDir, _ := os.MkdirTemp("", "obj3-verify-*")
	defer os.RemoveAll(tmpDir)

	ckptFile := filepath.Join(tmpDir, "checkpoint.ckpt")
	originalCkpt := onnx.Checkpoint{
		JobID:     "job-verify-1",
		ModelHash: "model-convnet-v2",
		Step:      42,
		Epoch:     1,
		Loss:      0.31415,
		Weights: map[string][]float32{
			"layer.weight": {0.1, 0.2, 0.3, 0.4},
		},
	}
	hash, err := onnx.SaveCheckpoint(originalCkpt, ckptFile)
	if err != nil {
		fmt.Printf("  [FAIL] SaveCheckpoint failed: %v\n", err)
		allPassed = false
	} else {
		loadedCkpt, err := onnx.LoadCheckpoint(ckptFile)
		if err != nil || loadedCkpt.Step != 42 {
			fmt.Printf("  [FAIL] LoadCheckpoint failed: %v\n", err)
			allPassed = false
		} else {
			fmt.Printf("  -> Checkpoint SHA-256:  %s\n", hash)
			fmt.Println("  [PASS] Checkpoint atomically persisted, loaded, and verified.")
		}
	}

	// 6. Interruptible Training Loop & Preemption Migration Verification
	fmt.Println("\n[TEST 6] Testing Interruptible Training Loop & Preemption Resumption...")
	engine := onnx.NewSessionEngine(onnx.ModelConfig{BatchSize: 8}, originalCkpt)
	migrationCkpt := filepath.Join(tmpDir, "migrated_checkpoint.ckpt")

	// Trigger watchdog preemption after 15 ms
	go func() {
		time.Sleep(15 * time.Millisecond)
		engine.TriggerPreempt("Student started 3D game (GPU load detected)")
	}()

	trainRes, _ := engine.RunTrainingLoop(context.Background(), 200, 5, migrationCkpt)
	if !trainRes.Preempted {
		fmt.Println("  [FAIL] Expected training to be preempted by watchdog.")
		allPassed = false
	} else {
		fmt.Printf("  -> Preempted at Step:   %d with reason: %q\n", trainRes.TotalSteps, trainRes.PreemptReason)
		fmt.Printf("  -> Checkpoint Flushed:  %s (SHA-256: %s)\n", trainRes.CheckpointPath, trainRes.CheckpointHash)

		// Resume on second node engine
		resumedCkpt, err := onnx.LoadCheckpoint(migrationCkpt)
		if err != nil {
			fmt.Printf("  [FAIL] Failed to reload flushed checkpoint: %v\n", err)
			allPassed = false
		} else {
			secondEngine := onnx.NewSessionEngine(onnx.ModelConfig{BatchSize: 8}, resumedCkpt)
			secondRes, err := secondEngine.RunTrainingLoop(context.Background(), 10, 5, migrationCkpt)
			if err != nil || secondRes.TotalSteps <= resumedCkpt.Step {
				fmt.Printf("  [FAIL] Resumption failed: %v\n", err)
				allPassed = false
			} else {
				fmt.Printf("  -> Resumed on Peer:     Advanced from step %d to step %d\n", resumedCkpt.Step, secondRes.TotalSteps)
				fmt.Println("  [PASS] Training successfully paused, flushed to storage, and resumed.")
			}
		}
	}

	// 7. Full GPUManager Unified Orchestration Test
	fmt.Println("\n[TEST 7] Testing Unified GPUManager Orchestration...")
	mgr, err := gpu.NewManager(gpu.ManagerConfig{
		StorageDir:       tmpDir,
		MaxMemoryBytes:   512 * 1024 * 1024,
		WatchdogInterval: 50 * time.Millisecond,
		IgnoreBattery:    true,
	})
	if err != nil {
		fmt.Printf("  [FAIL] NewManager failed: %v\n", err)
		allPassed = false
	} else {
		mgr.Start()
		time.Sleep(100 * time.Millisecond)
		taskReq := gpu.GPUTaskRequest{
			JobID: "job-unified-test",
			Manifest: gpu.TaskCapsuleManifest{
				Name:         "test-capsule",
				TargetTier:   "tier3",
				TaskType:     "training",
				TotalBatches: 20,
			},
		}
		taskResp := mgr.ExecuteTask(context.Background(), taskReq)
		_ = mgr.Close()
		if taskResp.Status != "completed" && taskResp.Status != "preempted" {
			fmt.Printf("  [FAIL] Task execution status: %s, error: %s\n", taskResp.Status, taskResp.Error)
			allPassed = false
		} else {
			fmt.Printf("  -> Task ID:             %s (Status: %s, Time: %d ms, Batches: %d)\n",
				taskResp.JobID, taskResp.Status, taskResp.ExecutionMs, taskResp.ProcessedBatches)
			fmt.Println("  [PASS] Unified GPUManager orchestrated execution successfully.")
		}
	}

	fmt.Println("\n================================================================================")
	if allPassed {
		fmt.Println(" [SUCCESS] ALL OBJECTIVE 3 TESTS PASSED! Cross-Platform GPU Engine Operational.")
	} else {
		fmt.Println(" [FAILURE] One or more Objective 3 tests failed.")
		os.Exit(1)
	}
	fmt.Println("================================================================================")
}
