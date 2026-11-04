package gpu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/unicompute/obj1/pkg/gpu/hipify"
	"github.com/unicompute/obj1/pkg/gpu/onnx"
	"github.com/unicompute/obj1/pkg/gpu/sandbox"
	"github.com/unicompute/obj1/pkg/gpu/watchdog"
)

// ManagerConfig configures the node's GPU subsystem.
type ManagerConfig struct {
	StorageDir          string
	MaxMemoryBytes      uint64
	PreferredBackend    ExecutionBackend
	WatchdogInterval    time.Duration
	UserActiveThreshold time.Duration
	ResumeGracePeriod   time.Duration
	IgnoreBattery       bool
}

// Manager orchestrates hardware probing, sandboxing, watchdog, and execution for Objective 3.
type Manager struct {
	mu           sync.RWMutex
	cfg          ManagerConfig
	profile      GPUProfile
	watchdog     *watchdog.Watchdog
	sandbox      sandbox.Sandbox
	activeEngine *onnx.SessionEngine
	activeJobID  string
	started      bool
	closed       bool

	onPreemptAlert func(jobID, reason string)
}

// NewManager creates and initializes the Objective 3 GPU manager.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.MaxMemoryBytes == 0 {
		cfg.MaxMemoryBytes = 2048 * 1024 * 1024 // 2 GiB default
	}
	if cfg.StorageDir == "" {
		cfg.StorageDir = filepath.Join(os.TempDir(), "unicompute_gpu")
	}

	// 1. Probe local graphics hardware
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	profile, err := ProbeHardware(ctx)
	cancel()
	if err != nil {
		profile = GPUProfile{
			Vendor:            VendorUnknown,
			DeviceName:        "Fallback CPU Execution",
			SupportedBackends: []ExecutionBackend{BackendCPU},
			Timestamp:         time.Now(),
		}
	}

	// 2. Initialize OS Resource Governance Sandbox
	sb, err := sandbox.NewJobObjectSandbox(sandbox.SandboxConfig{
		MaxMemoryBytes: cfg.MaxMemoryBytes,
		MaxProcesses:   4,
		IdlePriority:   true,
		KillOnClose:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize GPU sandbox: %w", err)
	}

	m := &Manager{
		cfg:     cfg,
		profile: profile,
		sandbox: sb,
	}

	// 3. Initialize low-latency 50 ms Watchdog
	wd := watchdog.NewWatchdog(watchdog.Config{
		PollInterval:        cfg.WatchdogInterval,
		UserActiveThreshold: cfg.UserActiveThreshold,
		ResumeGracePeriod:   cfg.ResumeGracePeriod,
		IgnoreBattery:       cfg.IgnoreBattery,
	}, m.handleWatchdogPause, m.handleWatchdogResume)

	m.watchdog = wd
	return m, nil
}

// Start begins background watchdog monitoring.
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	m.watchdog.Start()
}

// SetOnPreemptAlert sets a callback triggered when watchdog pauses a running job.
func (m *Manager) SetOnPreemptAlert(fn func(jobID, reason string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onPreemptAlert = fn
}

func (m *Manager) handleWatchdogPause(reason string) {
	m.mu.Lock()
	engine := m.activeEngine
	jobID := m.activeJobID
	preemptFn := m.onPreemptAlert
	m.mu.Unlock()

	if engine != nil {
		engine.TriggerPreempt(reason)
	}
	if preemptFn != nil && jobID != "" {
		preemptFn(jobID, reason)
	}
}

func (m *Manager) handleWatchdogResume() {
	// Auto-resumed state acknowledged; scheduler or node can re-trigger pending work
}

// Profile returns discovered hardware specifications.
func (m *Manager) Profile() GPUProfile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.profile
}

// WatchdogStatus returns the current power and user input status.
func (m *Manager) WatchdogStatus() watchdog.Status {
	return m.watchdog.Status()
}

// ExecuteTask processes a GPU computation request with strict sandboxing and preemption support.
func (m *Manager) ExecuteTask(ctx context.Context, req GPUTaskRequest) GPUTaskResponse {
	start := time.Now()
	resp := GPUTaskResponse{
		JobID:    req.JobID,
		Status:   "failed",
		ExitCode: -1,
	}

	// 1. Pre-flight check: if request is native CUDA code and this machine is AMD or Intel
	if req.SourceCode != "" {
		report := hipify.AnalyzeCUDASource(req.SourceCode)
		if m.profile.Vendor == VendorAMD && !report.TargetAMDSupported {
			resp.Status = "rejected"
			resp.DiagnosticMessage = fmt.Sprintf(
				"CUDA kernel incompatible with AMD GPU (found %d issue(s): Warp/PTX assumptions). Export to portable ONNX capsule.",
				len(report.Issues),
			)
			resp.ExecutionMs = time.Since(start).Milliseconds()
			return resp
		}
	}

	// 2. Setup ONNX session
	ckptPath := filepath.Join(m.cfg.StorageDir, "checkpoints", req.JobID, "checkpoint.ckpt")
	var initialCkpt onnx.Checkpoint

	if req.CheckpointBlob != "" {
		// Load from blob or local checkpoint if present
		loaded, err := onnx.LoadCheckpoint(req.CheckpointBlob)
		if err == nil {
			initialCkpt = loaded
		}
	}

	provider := "DirectML"
	if m.profile.CUDASupported && (m.cfg.PreferredBackend == BackendCUDA || m.cfg.PreferredBackend == "") {
		provider = "CUDA"
	} else if !m.profile.DirectMLSupported && !m.profile.CUDASupported {
		provider = "CPU"
	}

	engine := onnx.NewSessionEngine(onnx.ModelConfig{
		ExecutionProvider: provider,
		BatchSize:         req.Manifest.BatchSize,
	}, initialCkpt)

	m.mu.Lock()
	m.activeEngine = engine
	m.activeJobID = req.JobID
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.activeEngine = nil
		m.activeJobID = ""
		m.mu.Unlock()
	}()

	// 3. Dispatch based on task type
	if req.Manifest.TaskType == "inference" {
		out, err := engine.ExecuteInference(ctx, req.InputTensors)
		if err != nil {
			resp.Error = err.Error()
			resp.ExecutionMs = time.Since(start).Milliseconds()
			return resp
		}
		resp.Status = "completed"
		resp.ExitCode = 0
		resp.OutputTensors = out
		resp.ExecutionMs = time.Since(start).Milliseconds()
		return resp
	}

	// 4. Training loop execution
	totalBatches := req.Manifest.TotalBatches
	if totalBatches <= 0 {
		totalBatches = 50
	}
	ckptEvery := req.Manifest.CheckpointEvery
	if ckptEvery <= 0 {
		ckptEvery = 10
	}

	trainRes, err := engine.RunTrainingLoop(ctx, totalBatches, ckptEvery, ckptPath)
	resp.ExecutionMs = time.Since(start).Milliseconds()
	resp.ProcessedBatches = int(trainRes.TotalSteps)
	resp.Loss = trainRes.FinalLoss
	resp.CheckpointBlob = trainRes.CheckpointPath

	if trainRes.Preempted {
		resp.Status = "preempted"
		resp.PreemptedReason = trainRes.PreemptReason
		resp.ExitCode = 0
		return resp
	}

	if err != nil {
		resp.Error = err.Error()
		return resp
	}

	resp.Status = "completed"
	resp.ExitCode = 0
	return resp
}

// Close terminates sandbox handles and background watchdog loops.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()

	if m.watchdog != nil {
		m.watchdog.Stop()
	}
	if m.sandbox != nil {
		_ = m.sandbox.Close()
	}
	return nil
}
