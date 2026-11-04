//go:build !windows

package sandbox

import (
	"syscall"
)

// JobObjectSandbox stub for Linux/macOS.
type JobObjectSandbox struct {
	cfg SandboxConfig
}

func NewJobObjectSandbox(cfg SandboxConfig) (*JobObjectSandbox, error) {
	return &JobObjectSandbox{cfg: cfg}, nil
}

func (s *JobObjectSandbox) AssignProcess(pid int) error {
	// On Unix/Linux, apply nice priority if IdlePriority requested
	if s.cfg.IdlePriority {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 19)
	}
	return nil
}

func (s *JobObjectSandbox) Close() error {
	return nil
}
