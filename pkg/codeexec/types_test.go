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
