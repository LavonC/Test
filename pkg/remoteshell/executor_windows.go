//go:build windows

package remoteshell

import (
	"context"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
	processSetQuota                        = 0x0100
	processTerminate                       = 0x0001
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

func createKillOnCloseJobObject() (syscall.Handle, bool) {
	ret, _, _ := procCreateJobObjectW.Call(0, 0)
	if ret == 0 {
		return 0, false
	}
	jobHandle := syscall.Handle(ret)

	var info jobObjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose

	setRet, _, _ := procSetInformationJobObject.Call(
		uintptr(jobHandle),
		uintptr(jobObjectExtendedLimitInformationClass),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
	if setRet == 0 {
		_, _, _ = procCloseHandle.Call(uintptr(jobHandle))
		return 0, false
	}
	return jobHandle, true
}

func assignPidToJob(jobHandle syscall.Handle, pid int) bool {
	pHandleRet, _, _ := procOpenProcess.Call(uintptr(processSetQuota|processTerminate), 0, uintptr(pid))
	if pHandleRet == 0 {
		return false
	}
	pHandle := syscall.Handle(pHandleRet)
	defer func() { _, _, _ = procCloseHandle.Call(uintptr(pHandle)) }()

	assignRet, _, _ := procAssignProcessToJobObject.Call(uintptr(jobHandle), uintptr(pHandle))
	return assignRet != 0
}

func runCommandWithProcessGroup(ctx context.Context, cmd *exec.Cmd) error {
	jobHandle, hasJob := createKillOnCloseJobObject()
	var jobMu sync.Mutex
	var assigned bool

	closeJob := func() {
		jobMu.Lock()
		h := jobHandle
		jobHandle = 0
		jobMu.Unlock()
		if hasJob && h != 0 {
			_, _, _ = procCloseHandle.Call(uintptr(h))
		}
	}
	defer closeJob()

	killTree := func() {
		jobMu.Lock()
		isAssigned := assigned
		h := jobHandle
		if isAssigned && h != 0 {
			_, _, _ = procTerminateJobObject.Call(uintptr(h), 1)
			jobHandle = 0
		}
		jobMu.Unlock()

		if isAssigned {
			if h != 0 {
				_, _, _ = procCloseHandle.Call(uintptr(h))
			}
			return
		}
		if cmd.Process != nil {
			_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
			_ = cmd.Process.Kill()
		}
	}

	cmd.Cancel = func() error {
		killTree()
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Start(); err != nil {
		return err
	}

	if hasJob && cmd.Process != nil {
		jobMu.Lock()
		if jobHandle != 0 && assignPidToJob(jobHandle, cmd.Process.Pid) {
			assigned = true
		}
		jobMu.Unlock()
	}

	if ctx.Err() != nil {
		killTree()
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		killTree()
	}
	return waitErr
}
