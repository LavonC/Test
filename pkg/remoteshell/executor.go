// language: Go, file: pkg/remoteshell/executor.go, runtime: Go 1.22+, target: Windows/Linux/macOS
package remoteshell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Executor defines interface for executing shell commands locally.
type Executor interface {
	Execute(ctx context.Context, req ShellRequest) ShellResponse
}

// SystemExecutor executes commands on the underlying operating system.
type SystemExecutor struct{}

// NewSystemExecutor instantiates a cross-platform system shell executor.
func NewSystemExecutor() *SystemExecutor {
	return &SystemExecutor{}
}

// Execute runs the requested command in a subshell, capturing stdout, stderr, exit code, and runtime.
func (e *SystemExecutor) Execute(ctx context.Context, req ShellRequest) ShellResponse {
	start := time.Now()

	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 30000 // default 30-second timeout
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := buildCommand(execCtx, req.Command, req.ShellType)

	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}

	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	elapsed := time.Since(start).Milliseconds()

	resp := ShellResponse{
		ExecID:     req.ExecID,
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		DurationMs: elapsed,
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			resp.ExitCode = -1
		}

		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			resp.Error = "command execution timed out"
		} else {
			resp.Error = runErr.Error()
		}
	} else {
		resp.ExitCode = 0
	}

	return resp
}

// buildCommand selects the appropriate interpreter based on OS and requested shell type.
func buildCommand(ctx context.Context, command string, requestedShell ShellType) *exec.Cmd {
	if runtime.GOOS == "windows" {
		switch requestedShell {
		case ShellCmd:
			return exec.CommandContext(ctx, "cmd.exe", "/c", command)
		case ShellPowerShell, ShellAuto, "":
			return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
		default:
			// Fallback to powershell on Windows
			return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
		}
	}

	// Linux, macOS, Unix
	switch requestedShell {
	case ShellSh:
		return exec.CommandContext(ctx, "/bin/sh", "-c", command)
	case ShellBash, ShellAuto, "":
		if bashPath, err := exec.LookPath("bash"); err == nil {
			return exec.CommandContext(ctx, bashPath, "-c", command)
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", command)
	default:
		return exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
}
