// language: Go, file: pkg/remoteshell/executor.go, runtime: Go 1.22+, target: Windows/Linux/macOS
package remoteshell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

const MaxShellOutputBytes = 256 * 1024 // 256 KiB cap per stream
const DefaultMaxConcurrentShells = 2

type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	if len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	str := b.buf.String()
	if b.truncated {
		str += "\n[output truncated]"
	}
	return str
}

// Executor defines interface for executing shell commands locally.
type Executor interface {
	Execute(ctx context.Context, req ShellRequest) ShellResponse
}

// SystemExecutor executes commands on the underlying operating system.
type SystemExecutor struct {
	slots chan struct{}
}

// NewSystemExecutor instantiates a cross-platform system shell executor with bounded concurrency.
func NewSystemExecutor() *SystemExecutor {
	return &SystemExecutor{
		slots: make(chan struct{}, DefaultMaxConcurrentShells),
	}
}

// Execute runs the requested command in a subshell, capturing stdout, stderr, exit code, and runtime.
func (e *SystemExecutor) Execute(ctx context.Context, req ShellRequest) ShellResponse {
	start := time.Now()

	if e.slots != nil {
		select {
		case e.slots <- struct{}{}:
			defer func() { <-e.slots }()
		default:
			return ShellResponse{
				ExecID:   req.ExecID,
				ExitCode: -1,
				Error:    "worker shell busy",
			}
		}
	}

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

	stdoutBuf := newCappedBuffer(MaxShellOutputBytes)
	stderrBuf := newCappedBuffer(MaxShellOutputBytes)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

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
