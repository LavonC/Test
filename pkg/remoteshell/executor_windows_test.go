//go:build windows

package remoteshell

import (
	"syscall"
)

var procWaitForSingleObject = kernel32.NewProc("WaitForSingleObject")

const (
	processSynchronizeAndQuery = 0x00101000 // SYNCHRONIZE | PROCESS_QUERY_LIMITED_INFORMATION
	waitTimeoutStatus          = 0x00000102
)

func isProcessAlive(pid int) bool {
	hRet, _, _ := procOpenProcess.Call(uintptr(processSynchronizeAndQuery), 0, uintptr(pid))
	if hRet == 0 {
		return false
	}
	h := syscall.Handle(hRet)
	defer func() { _, _, _ = procCloseHandle.Call(uintptr(h)) }()

	res, _, _ := procWaitForSingleObject.Call(uintptr(h), 0)
	return res == waitTimeoutStatus
}
