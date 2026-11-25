// language: Go, file: pkg/remoteshell/executor_test.go, runtime: Go 1.22+, target: Windows/Linux/macOS
package remoteshell

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEncodeDecodeRequest(t *testing.T) {
	orig := ShellRequest{
		ExecID:    "exec-test-123",
		Command:   "whoami",
		ShellType: ShellPowerShell,
		TimeoutMs: 5000,
		WorkDir:   "C:\\Temp",
		Env:       []string{"FOO=BAR"},
	}

	encoded, err := EncodeRequest(orig)
	if err != nil {
		t.Fatalf("EncodeRequest failed: %v", err)
	}

	decoded, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatalf("DecodeRequest failed: %v", err)
	}

	if decoded.ExecID != orig.ExecID || decoded.Command != orig.Command || decoded.ShellType != orig.ShellType {
		t.Fatalf("mismatch in decoded request: %+v vs %+v", decoded, orig)
	}
}

func TestEncodeDecodeResponse(t *testing.T) {
	orig := ShellResponse{
		ExecID:     "exec-test-123",
		ExitCode:   0,
		Stdout:     "output line 1\noutput line 2",
		Stderr:     "",
		DurationMs: 42,
		Error:      "",
	}

	encoded, err := EncodeResponse(orig)
	if err != nil {
		t.Fatalf("EncodeResponse failed: %v", err)
	}

	decoded, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatalf("DecodeResponse failed: %v", err)
	}

	if decoded.ExecID != orig.ExecID || decoded.Stdout != orig.Stdout || decoded.ExitCode != orig.ExitCode {
		t.Fatalf("mismatch in decoded response: %+v vs %+v", decoded, orig)
	}
}

func TestSystemExecutor_Echo(t *testing.T) {
	exec := NewSystemExecutor()
	ctx := context.Background()

	cmdStr := "echo unicompute_test_success"
	req := ShellRequest{
		ExecID:    "test-echo",
		Command:   cmdStr,
		ShellType: ShellAuto,
		TimeoutMs: 10000,
	}

	resp := exec.Execute(ctx, req)
	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d, err: %s, stderr: %s", resp.ExitCode, resp.Error, resp.Stderr)
	}

	if !strings.Contains(strings.ToLower(resp.Stdout), "unicompute_test_success") {
		t.Fatalf("expected stdout to contain test string, got: %q", resp.Stdout)
	}
}

func TestSystemExecutor_ExitCode(t *testing.T) {
	exec := NewSystemExecutor()
	ctx := context.Background()

	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = "exit 7"
	} else {
		cmdStr = "exit 7"
	}

	req := ShellRequest{
		ExecID:    "test-exit",
		Command:   cmdStr,
		ShellType: ShellAuto,
		TimeoutMs: 5000,
	}

	resp := exec.Execute(ctx, req)
	if resp.ExitCode != 7 {
		t.Fatalf("expected exit code 7, got %d", resp.ExitCode)
	}
}

func TestSystemExecutor_Timeout(t *testing.T) {
	exec := NewSystemExecutor()
	ctx := context.Background()

	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = "Start-Sleep -Seconds 5"
	} else {
		cmdStr = "sleep 5"
	}

	req := ShellRequest{
		ExecID:    "test-timeout",
		Command:   cmdStr,
		ShellType: ShellAuto,
		TimeoutMs: 500, // 500ms timeout vs 5s sleep
	}

	start := time.Now()
	resp := exec.Execute(ctx, req)
	elapsed := time.Since(start)

	if resp.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code on timeout")
	}
	if !strings.Contains(resp.Error, "timed out") {
		t.Fatalf("expected timeout error, got: %s", resp.Error)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("execution took too long to timeout: %v", elapsed)
	}
}

func TestSystemExecutor_OutputCapped(t *testing.T) {
	buf := newCappedBuffer(1024)
	largeData := make([]byte, 5000)
	for i := range largeData {
		largeData[i] = 'A'
	}
	n, err := buf.Write(largeData)
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if n != len(largeData) {
		t.Errorf("expected %d bytes reported written, got %d", len(largeData), n)
	}
	str := buf.String()
	if !strings.HasSuffix(str, "\n[output truncated]") {
		t.Errorf("expected string to end with truncation notice")
	}
	if buf.buf.Len() > 1024 {
		t.Errorf("expected buffer len <= 1024, got %d", buf.buf.Len())
	}

	executor := NewSystemExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = "powershell -Command \"Write-Output ('X' * 300000)\""
	} else {
		cmdStr = "python3 -c \"print('X' * 300000)\" || printf '%*s' 300000 ''"
	}

	req := ShellRequest{
		ExecID:    "test-cap",
		Command:   cmdStr,
		ShellType: ShellAuto,
		TimeoutMs: 5000,
	}

	resp := executor.Execute(ctx, req)
	if len(resp.Stdout) > MaxShellOutputBytes+100 {
		t.Errorf("expected stdout to be capped at ~%d bytes, got %d", MaxShellOutputBytes, len(resp.Stdout))
	}
	if !strings.Contains(resp.Stdout, "[output truncated]") {
		t.Errorf("expected stdout to contain truncation notice")
	}
}

func TestSystemExecutor_ConcurrencyLimitBusy(t *testing.T) {
	exec := NewSystemExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = "Start-Sleep -Milliseconds 400"
	} else {
		cmdStr = "sleep 0.4"
	}

	var wg sync.WaitGroup
	for i := 0; i < DefaultMaxConcurrentShells; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			exec.Execute(ctx, ShellRequest{
				ExecID:    "busy-fill",
				Command:   cmdStr,
				ShellType: ShellAuto,
				TimeoutMs: 3000,
			})
		}(i)
	}

	// Wait briefly until the 2 slots are occupied
	deadline := time.Now().Add(2 * time.Second)
	for len(exec.slots) < DefaultMaxConcurrentShells && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	resp := exec.Execute(ctx, ShellRequest{
		ExecID:    "busy-3rd",
		Command:   "echo should_fail",
		ShellType: ShellAuto,
		TimeoutMs: 1000,
	})
	if resp.ExitCode != -1 || !strings.Contains(resp.Error, "worker shell busy") {
		t.Fatalf("expected immediate 'worker shell busy' error when slots full, got: %+v", resp)
	}
	wg.Wait()
}
