package codeexec

import (
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	want := Request{
		JobID:          "job-1",
		Language:       "CPP",
		Source:         "#include <iostream>\nint main(){ std::cout << \"ok\"; }",
		Stdin:          "sample input\n",
		TimeoutSeconds: 5,
	}
	wire, err := EncodeRequest(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRequest(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "cpp" || got.Source != want.Source || got.Stdin != want.Stdin || got.JobID != want.JobID {
		t.Fatalf("round-trip mismatch: %#v", got)
	}
}

func TestRejectOversizeSource(t *testing.T) {
	req := Request{JobID: "job-2", Language: "python", Source: strings.Repeat("x", MaxSourceBytes+1)}
	if _, err := EncodeRequest(req); err == nil {
		t.Fatal("expected oversized source to be rejected")
	}
}

func TestResponseRoundTrip(t *testing.T) {
	want := Response{JobID: "job-3", Language: "python", Stdout: "hello\n", ExitCode: 0, DurationMs: 42}
	wire, err := EncodeResponse(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponse(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID != want.JobID || got.Stdout != want.Stdout || got.ExitCode != want.ExitCode {
		t.Fatalf("round-trip mismatch: %#v", got)
	}
}

func TestDockerSandboxSecurityAndCppConfig(t *testing.T) {
	exec := NewDockerExecutor(Config{})
	if exec.cfg.MemoryLimit != "256m" {
		t.Fatalf("expected default MemoryLimit 256m, got %q", exec.cfg.MemoryLimit)
	}

	srcName, image, cmd := languageRuntime("cpp")
	if srcName != "main.cpp" || image != "gcc:14" {
		t.Fatalf("unexpected cpp runtime: %q, %q", srcName, image)
	}
	if strings.Contains(cmd, "-O2") || !strings.Contains(cmd, "-O1") {
		t.Fatalf("expected C++ compile command to use -O1 instead of -O2, got %q", cmd)
	}

	args := exec.buildDockerArgs("test-container", "/tmp/work", image, cmd)
	joined := strings.Join(args, " ")

	required := []string{
		"--network none",
		"--memory 256m",
		"--memory-swap 256m",
		"--cap-drop ALL",
		"--read-only",
		"--tmpfs /tmp:rw,exec,nosuid,nodev,size=64m",
		"--user 65534:65534",
		"--security-opt no-new-privileges:true",
	}
	for _, flag := range required {
		if !strings.Contains(joined, flag) {
			t.Errorf("expected docker args to contain %q; got %q", flag, joined)
		}
	}

	pySrc, pyImg, pyCmd := languageRuntime("python")
	if pySrc != "main.py" || pyImg != "python:3.13-slim" {
		t.Fatalf("unexpected python runtime: %q, %q", pySrc, pyImg)
	}
	pyArgs := exec.buildDockerArgsForLang("python", "test-py", "/tmp/work", pyImg, pyCmd)
	pyJoined := strings.Join(pyArgs, " ")
	for _, flag := range []string{"--ipc none", "--tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m"} {
		if !strings.Contains(pyJoined, flag) {
			t.Errorf("expected python docker args to contain %q; got %q", flag, pyJoined)
		}
	}
}

