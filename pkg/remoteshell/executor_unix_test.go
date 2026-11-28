//go:build !windows

package remoteshell

import "syscall"

func isProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}
