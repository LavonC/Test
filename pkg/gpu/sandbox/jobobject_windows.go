//go:build windows

package sandbox

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW          = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject  = kernel32.NewProc("AssignProcessToJobObject")
	procOpenProcess               = kernel32.NewProc("OpenProcess")
	procCloseHandle               = kernel32.NewProc("CloseHandle")
)

const (
	JobObjectExtendedLimitInformation = 9

	JOB_OBJECT_LIMIT_ACTIVE_PROCESS    = 0x00000008
	JOB_OBJECT_LIMIT_PRIORITY_CLASS    = 0x00000020
	JOB_OBJECT_LIMIT_PROCESS_MEMORY    = 0x00000100
	JOB_OBJECT_LIMIT_JOB_MEMORY        = 0x00000200
	JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x00002000

	IDLE_PRIORITY_CLASS = 0x00000040

	PROCESS_SET_QUOTA = 0x0100
	PROCESS_TERMINATE = 0x0001
)

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// JobObjectSandbox encapsulates a Win32 JobObject.
type JobObjectSandbox struct {
	mu     sync.Mutex
	handle syscall.Handle
	cfg    SandboxConfig
	closed bool
}

// NewJobObjectSandbox creates and configures a Win32 JobObject with strict governance limits.
func NewJobObjectSandbox(cfg SandboxConfig) (*JobObjectSandbox, error) {
	if cfg.MaxProcesses == 0 {
		cfg.MaxProcesses = 4
	}

	ret, _, err := procCreateJobObjectW.Call(0, 0)
	if ret == 0 {
		return nil, fmt.Errorf("CreateJobObjectW failed: %w", err)
	}
	jobHandle := syscall.Handle(ret)

	var info jobObjectExtendedLimitInformation
	var limitFlags uint32 = 0

	if cfg.KillOnClose {
		limitFlags |= JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	if cfg.IdlePriority {
		limitFlags |= JOB_OBJECT_LIMIT_PRIORITY_CLASS
		info.BasicLimitInformation.PriorityClass = IDLE_PRIORITY_CLASS
	}
	if cfg.MaxProcesses > 0 {
		limitFlags |= JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		info.BasicLimitInformation.ActiveProcessLimit = cfg.MaxProcesses
	}
	if cfg.MaxMemoryBytes > 0 {
		limitFlags |= (JOB_OBJECT_LIMIT_PROCESS_MEMORY | JOB_OBJECT_LIMIT_JOB_MEMORY)
		info.ProcessMemoryLimit = uintptr(cfg.MaxMemoryBytes)
		info.JobMemoryLimit = uintptr(cfg.MaxMemoryBytes)
	}

	info.BasicLimitInformation.LimitFlags = limitFlags

	setRet, _, setErr := procSetInformationJobObject.Call(
		uintptr(jobHandle),
		uintptr(JobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
	if setRet == 0 {
		_, _, _ = procCloseHandle.Call(uintptr(jobHandle))
		return nil, fmt.Errorf("SetInformationJobObject failed: %w", setErr)
	}

	return &JobObjectSandbox{
		handle: jobHandle,
		cfg:    cfg,
	}, nil
}

// AssignProcess assigns an OS process ID to the JobObject.
func (s *JobObjectSandbox) AssignProcess(pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("cannot assign process to closed JobObject")
	}

	// Open process handle with required permissions
	desiredAccess := PROCESS_SET_QUOTA | PROCESS_TERMINATE
	pHandleRet, _, err := procOpenProcess.Call(uintptr(desiredAccess), 0, uintptr(pid))
	if pHandleRet == 0 {
		return fmt.Errorf("OpenProcess(pid=%d) failed: %w", pid, err)
	}
	pHandle := syscall.Handle(pHandleRet)
	defer func() { _, _, _ = procCloseHandle.Call(uintptr(pHandle)) }()

	assignRet, _, assignErr := procAssignProcessToJobObject.Call(uintptr(s.handle), uintptr(pHandle))
	if assignRet == 0 {
		return fmt.Errorf("AssignProcessToJobObject(pid=%d) failed: %w", pid, assignErr)
	}

	return nil
}

// Close terminates all processes in the job (if KillOnClose is set) and releases the handle.
func (s *JobObjectSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	ret, _, err := procCloseHandle.Call(uintptr(s.handle))
	if ret == 0 {
		return fmt.Errorf("CloseHandle failed: %w", err)
	}
	return nil
}
