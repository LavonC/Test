package codeexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config controls the sandbox limits. Use conservative defaults for code from
// other peers; increase these only for a fleet you trust.
type Config struct {
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxConcurrent  int
	MemoryLimit    string
	CPULimit       string
}

// DockerExecutor runs code in short-lived Linux containers managed by Docker.
type DockerExecutor struct {
	cfg   Config
	slots chan struct{}
}

func NewDockerExecutor(cfg Config) *DockerExecutor {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 10 * time.Second
	}
	if cfg.MaxTimeout <= 0 || cfg.MaxTimeout > MaxTimeoutSeconds*time.Second {
		cfg.MaxTimeout = MaxTimeoutSeconds * time.Second
	}
	if cfg.DefaultTimeout > cfg.MaxTimeout {
		cfg.DefaultTimeout = cfg.MaxTimeout
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.MemoryLimit == "" {
		cfg.MemoryLimit = "128m"
	}
	if cfg.CPULimit == "" {
		cfg.CPULimit = "1.0"
	}
	return &DockerExecutor{cfg: cfg, slots: make(chan struct{}, cfg.MaxConcurrent)}
}

// Execute compiles/runs one job inside a constrained container. The Docker host
// itself is still part of the trusted computing base; this is not a VM boundary.
func (e *DockerExecutor) Execute(parent context.Context, req Request) Response {
	started := time.Now()
	resp := Response{JobID: req.JobID, Language: strings.ToLower(strings.TrimSpace(req.Language)), ExitCode: -1}
	if err := ValidateRequest(req); err != nil {
		resp.Error = err.Error()
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}

	timeout := e.cfg.DefaultTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	if timeout > e.cfg.MaxTimeout {
		timeout = e.cfg.MaxTimeout
	}
	jobCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	// Bound concurrent containers and include queue waiting in the job timeout.
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-jobCtx.Done():
		resp.TimedOut = errors.Is(jobCtx.Err(), context.DeadlineExceeded)
		resp.Error = "timed out waiting for an available worker slot"
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}

	workDir, err := os.MkdirTemp("", "unicompute-codejob-*")
	if err != nil {
		resp.Error = fmt.Sprintf("create temporary source directory: %v", err)
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}
	defer os.RemoveAll(workDir)

	sourceName, image, command := languageRuntime(req.Language)
	if sourceName == "" {
		resp.Error = "unsupported language"
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}
	sourcePath := filepath.Join(workDir, sourceName)
	if err := os.WriteFile(sourcePath, []byte(req.Source), 0644); err != nil {
		resp.Error = fmt.Sprintf("write source file: %v", err)
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}
	// Docker runs as an unprivileged UID, so make the bind-mounted source
	// directory traversable and the source file readable before mounting it RO.
	if err := os.Chmod(workDir, 0755); err != nil {
		resp.Error = fmt.Sprintf("set source directory permissions: %v", err)
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}
	if err := os.Chmod(sourcePath, 0644); err != nil {
		resp.Error = fmt.Sprintf("set source file permissions: %v", err)
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		resp.Error = fmt.Sprintf("resolve source directory: %v", err)
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}
	absDir, err = filepath.EvalSymlinks(absDir)
	if err != nil {
		resp.Error = fmt.Sprintf("resolve source directory symlinks: %v", err)
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}

	containerName, err := randomContainerName()
	if err != nil {
		resp.Error = fmt.Sprintf("generate container name: %v", err)
		resp.DurationMs = time.Since(started).Milliseconds()
		return resp
	}
	args := []string{
		"run", "--rm", "-i",
		"--name", containerName,
		"--network", "none",
		"--memory", e.cfg.MemoryLimit,
		"--memory-swap", e.cfg.MemoryLimit,
		"--cpus", e.cfg.CPULimit,
		"--pids-limit", "64",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=64m",
		"--ulimit", "fsize=16777216:16777216",
		"--mount", "type=bind,source=" + absDir + ",target=/src,readonly",
		"--workdir", "/tmp",
		"--env", "HOME=/tmp",
		"--env", "TMPDIR=/tmp",
		"--user", "65534:65534",
		image,
		"sh", "-lc", command,
	}
	cmd := exec.CommandContext(jobCtx, "docker", args...)
	cmd.Stdin = strings.NewReader(req.Stdin)
	stdout := newCappedBuffer(MaxOutputBytes)
	stderr := newCappedBuffer(MaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	// A timeout can terminate the Docker CLI before it can remove the container.
	// Explicitly remove this uniquely named job container with a short independent
	// context so timed-out programs do not continue consuming resources.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", containerName).Run()
	cleanupCancel()

	resp.Stdout = stdout.String()
	resp.Stderr = stderr.String()
	resp.OutputTruncated = stdout.Truncated() || stderr.Truncated()
	if runErr != nil {
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			resp.TimedOut = true
			resp.Error = fmt.Sprintf("execution exceeded %s timeout", timeout)
		} else if errors.Is(jobCtx.Err(), context.Canceled) {
			resp.Error = "execution canceled"
		} else {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				resp.ExitCode = exitErr.ExitCode()
			} else {
				resp.Error = fmt.Sprintf("could not run Docker job (is Docker installed and running, and is the image available?): %v", runErr)
			}
		}
	} else {
		resp.ExitCode = 0
	}
	resp.DurationMs = time.Since(started).Milliseconds()
	return resp
}

func randomContainerName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "unicompute-job-" + hex.EncodeToString(random[:]), nil
}

func languageRuntime(language string) (sourceName, image, command string) {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "c":
		return "main.c", "gcc:14", "gcc -std=c17 -O2 -pipe /src/main.c -o /tmp/program && /tmp/program"
	case "cpp":
		return "main.cpp", "gcc:14", "g++ -std=c++17 -O2 -pipe /src/main.cpp -o /tmp/program && /tmp/program"
	case "python":
		return "main.py", "python:3.13-slim", "python -I -B /src/main.py"
	default:
		return "", "", ""
	}
}

type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

// Write keeps only the first limit bytes while reporting the entire input as
// consumed, so a noisy program cannot grow the worker process's output buffers.
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
	return b.buf.String()
}

func (b *cappedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// CheckDocker reports whether the Docker CLI can reach a Docker daemon.
func CheckDocker(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(bytes.TrimSpace(out)) > 0 {
			return fmt.Errorf("Docker is not ready: %s", strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("Docker is not ready: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("Docker returned an empty server version")
	}
	return nil
}

// Ensure this implementation obeys io.Writer's conventional contract.
var _ io.Writer = (*cappedBuffer)(nil)
