package node

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/unicompute/obj1/pkg/gpu"
	"github.com/unicompute/obj1/pkg/gpu/watchdog"
)

// EnableGPU initializes and activates the Objective 3 GPU acceleration and governance subsystem.
func (n *Node) EnableGPU(cfg gpu.ManagerConfig) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.gpuManager != nil {
		return errors.New("GPU subsystem already enabled on this node")
	}

	if cfg.StorageDir == "" {
		cfg.StorageDir = filepath.Join(n.cfg.StorageDir, "gpu")
	}

	mgr, err := gpu.NewManager(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize GPU manager: %w", err)
	}

	mgr.SetOnPreemptAlert(func(jobID, reason string) {
		// Log preemption and notify peers
		fmt.Printf("\n[GPU-PREEMPT] Job %s preempted by watchdog: %s\n> ", jobID, reason)
	})

	mgr.Start()
	n.gpuManager = mgr
	return nil
}

// GPUProfile returns the discovered GPU hardware specs, or false if GPU is not enabled.
func (n *Node) GPUProfile() (gpu.GPUProfile, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.gpuManager == nil {
		return gpu.GPUProfile{}, false
	}
	return n.gpuManager.Profile(), true
}

// GPUWatchdogStatus returns the current power and user input status of the GPU watchdog.
func (n *Node) GPUWatchdogStatus() (watchdog.Status, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.gpuManager == nil {
		return watchdog.Status{}, false
	}
	return n.gpuManager.WatchdogStatus(), true
}

// ExecuteGPUTask runs a GPU task locally under strict Win32 JobObject governance and 50 ms watchdog.
func (n *Node) ExecuteGPUTask(ctx context.Context, req gpu.GPUTaskRequest) (gpu.GPUTaskResponse, error) {
	n.mu.RLock()
	mgr := n.gpuManager
	n.mu.RUnlock()

	if mgr == nil {
		return gpu.GPUTaskResponse{
			JobID:    req.JobID,
			Status:   "failed",
			ExitCode: -1,
			Error:    "GPU subsystem not enabled on this node",
		}, errors.New("GPU subsystem not enabled on this node")
	}

	resp := mgr.ExecuteTask(ctx, req)
	return resp, nil
}
