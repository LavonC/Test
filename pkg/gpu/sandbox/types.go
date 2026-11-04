package sandbox

// SandboxConfig controls OS-level process isolation and resource governance.
type SandboxConfig struct {
	MaxMemoryBytes uint64 // Hard memory ceiling
	MaxProcesses   uint32 // Maximum child processes allowed
	IdlePriority   bool   // Enforce IDLE_PRIORITY_CLASS on Windows / nice 19 on Linux
	KillOnClose    bool   // Automatically terminate all assigned processes on sandbox close
}

// Sandbox defines the isolation boundary for GPU and CPU compute runners.
type Sandbox interface {
	AssignProcess(pid int) error
	Close() error
}
