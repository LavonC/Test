package codeexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	RequestPrefix  = "__CODE_JOB_REQ__:"
	ResponsePrefix = "__CODE_JOB_RESP__:"

	// WebRTC's stream channel is intended for small control messages. Jobs are
	// kept below this ceiling rather than trying to send arbitrary source files
	// as one DataChannel message.
	MaxWireMessageBytes = 48 * 1024
	MaxSourceBytes      = 12 * 1024
	MaxStdinBytes       = 4 * 1024
	MaxOutputBytes      = 3 * 1024
	MaxTimeoutSeconds   = 30
)

// Request is a small source-code job sent to a remote worker.
type Request struct {
	JobID          string `json:"job_id"`
	Language       string `json:"language"`
	Source         string `json:"source"`
	Stdin          string `json:"stdin,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// Response is returned to the submitting node after a worker finishes a job.
type Response struct {
	JobID           string `json:"job_id"`
	Language        string `json:"language,omitempty"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	ExitCode        int    `json:"exit_code"`
	DurationMs      int64  `json:"duration_ms"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Executor runs source code in an isolated environment. Implementations must
// enforce their own runtime, memory, CPU, and output limits.
type Executor interface {
	Execute(ctx context.Context, req Request) Response
}

// ValidateRequest checks the limits that apply before a job is sent or run.
func ValidateRequest(req Request) error {
	if strings.TrimSpace(req.JobID) == "" {
		return errors.New("job_id is required")
	}
	if len(req.JobID) > 128 {
		return errors.New("job_id is too long")
	}
	lang := strings.ToLower(strings.TrimSpace(req.Language))
	if lang != "c" && lang != "cpp" && lang != "python" {
		return fmt.Errorf("unsupported language %q; supported languages are c, cpp, python", req.Language)
	}
	if len(req.Source) == 0 {
		return errors.New("source code is empty")
	}
	if len([]byte(req.Source)) > MaxSourceBytes {
		return fmt.Errorf("source exceeds maximum size of %d bytes", MaxSourceBytes)
	}
	if len([]byte(req.Stdin)) > MaxStdinBytes {
		return fmt.Errorf("stdin exceeds maximum size of %d bytes", MaxStdinBytes)
	}
	if req.TimeoutSeconds < 0 || req.TimeoutSeconds > MaxTimeoutSeconds {
		return fmt.Errorf("timeout_seconds must be between 0 and %d", MaxTimeoutSeconds)
	}
	return nil
}

// EncodeRequest adds the protocol prefix and ensures the result fits one
// stream-channel message.
func EncodeRequest(req Request) ([]byte, error) {
	req.Language = strings.ToLower(strings.TrimSpace(req.Language))
	if err := ValidateRequest(req); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal code job request: %w", err)
	}
	wire := append([]byte(RequestPrefix), payload...)
	if len(wire) > MaxWireMessageBytes {
		return nil, fmt.Errorf("encoded code job request is %d bytes; maximum is %d", len(wire), MaxWireMessageBytes)
	}
	return wire, nil
}

// DecodeRequest validates the incoming protocol frame before returning it.
func DecodeRequest(wire []byte) (Request, error) {
	if len(wire) > MaxWireMessageBytes {
		return Request{}, fmt.Errorf("code job request exceeds %d bytes", MaxWireMessageBytes)
	}
	if !strings.HasPrefix(string(wire), RequestPrefix) {
		return Request{}, errors.New("invalid code job request prefix")
	}
	var req Request
	if err := json.Unmarshal(wire[len(RequestPrefix):], &req); err != nil {
		return Request{}, fmt.Errorf("decode code job request: %w", err)
	}
	req.Language = strings.ToLower(strings.TrimSpace(req.Language))
	if err := ValidateRequest(req); err != nil {
		return Request{}, err
	}
	return req, nil
}

// EncodeResponse returns a bounded response frame.
func EncodeResponse(resp Response) ([]byte, error) {
	if resp.JobID == "" {
		return nil, errors.New("response job_id is required")
	}
	if len(resp.Stdout) > MaxOutputBytes || len(resp.Stderr) > MaxOutputBytes {
		return nil, fmt.Errorf("stdout and stderr must each be at most %d bytes", MaxOutputBytes)
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("marshal code job response: %w", err)
	}
	wire := append([]byte(ResponsePrefix), payload...)
	if len(wire) > MaxWireMessageBytes {
		return nil, fmt.Errorf("encoded code job response is %d bytes; maximum is %d", len(wire), MaxWireMessageBytes)
	}
	return wire, nil
}

// DecodeResponse validates a response frame received from a worker.
func DecodeResponse(wire []byte) (Response, error) {
	if len(wire) > MaxWireMessageBytes {
		return Response{}, fmt.Errorf("code job response exceeds %d bytes", MaxWireMessageBytes)
	}
	if !strings.HasPrefix(string(wire), ResponsePrefix) {
		return Response{}, errors.New("invalid code job response prefix")
	}
	var resp Response
	if err := json.Unmarshal(wire[len(ResponsePrefix):], &resp); err != nil {
		return Response{}, fmt.Errorf("decode code job response: %w", err)
	}
	if strings.TrimSpace(resp.JobID) == "" {
		return Response{}, errors.New("response job_id is empty")
	}
	if len(resp.Stdout) > MaxOutputBytes || len(resp.Stderr) > MaxOutputBytes {
		return Response{}, errors.New("response output exceeds configured limit")
	}
	return resp, nil
}
