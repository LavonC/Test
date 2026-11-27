// language: Go, file: pkg/remoteshell/types.go, runtime: Go 1.22+, target: Windows/Linux/macOS
package remoteshell

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// Wire protocol prefixes for DataChannel stream multiplexing
	PrefixShellReq  = "__SHELL_REQ__:"
	PrefixShellResp = "__SHELL_RESP__:"

	MaxWireShellPayloadBytes = 60*1024 - 1 // Stay strictly below 60 KiB (< Pion's 64 KiB SCTP limit)
)

// ShellType specifies which interpreter to invoke.
type ShellType string

const (
	ShellAuto       ShellType = "auto"
	ShellPowerShell ShellType = "powershell"
	ShellCmd        ShellType = "cmd"
	ShellBash       ShellType = "bash"
	ShellSh         ShellType = "sh"
)

// ShellRequest represents a remote command execution request.
type ShellRequest struct {
	ExecID    string    `json:"exec_id"`
	Command   string    `json:"command"`
	ShellType ShellType `json:"shell_type,omitempty"`
	TimeoutMs int64     `json:"timeout_ms,omitempty"`
	WorkDir   string    `json:"work_dir,omitempty"`
	Env       []string  `json:"env,omitempty"`
}

// ShellResponse represents the result of a command execution returned by a remote peer.
type ShellResponse struct {
	ExecID     string `json:"exec_id"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// EncodeRequest serializes a ShellRequest into wire format.
func EncodeRequest(req ShellRequest) ([]byte, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal shell request: %w", err)
	}
	return []byte(PrefixShellReq + string(data)), nil
}

// DecodeRequest deserializes a ShellRequest from wire format.
func DecodeRequest(data []byte) (ShellRequest, error) {
	str := string(data)
	if !strings.HasPrefix(str, PrefixShellReq) {
		return ShellRequest{}, fmt.Errorf("invalid shell request prefix")
	}
	payload := strings.TrimPrefix(str, PrefixShellReq)
	var req ShellRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return ShellRequest{}, fmt.Errorf("failed to unmarshal shell request: %w", err)
	}
	return req, nil
}

// EncodeResponse serializes a ShellResponse into wire format, truncating oversized
// stdout/stderr/error fields so the encoded frame always fits below the 64 KiB SCTP limit.
func EncodeResponse(resp ShellResponse) ([]byte, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal shell response: %w", err)
	}
	wire := []byte(PrefixShellResp + string(data))
	if len(wire) <= MaxWireShellPayloadBytes {
		return wire, nil
	}

	const truncNotice = "\n[output truncated]"
	trimTo := func(s string, maxBytes int) string {
		if len(s) <= maxBytes {
			return s
		}
		if maxBytes <= len(truncNotice) {
			return truncNotice[:maxBytes]
		}
		cut := maxBytes - len(truncNotice)
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut] + truncNotice
	}

	budget := 24 * 1024
	for attempt := 0; attempt < 5; attempt++ {
		trimmed := resp
		trimmed.Stdout = trimTo(trimmed.Stdout, budget)
		trimmed.Stderr = trimTo(trimmed.Stderr, budget)
		if len(trimmed.Error) > 1024 {
			trimmed.Error = trimmed.Error[:1024]
		}
		data, err = json.Marshal(trimmed)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal shell response: %w", err)
		}
		wire = []byte(PrefixShellResp + string(data))
		if len(wire) <= MaxWireShellPayloadBytes {
			return wire, nil
		}
		budget /= 2
	}

	fallback, _ := json.Marshal(ShellResponse{
		ExecID:     resp.ExecID,
		ExitCode:   resp.ExitCode,
		DurationMs: resp.DurationMs,
		Error:      "shell response exceeded 60 KiB wire limit",
	})
	return []byte(PrefixShellResp + string(fallback)), nil
}

// DecodeResponse deserializes a ShellResponse from wire format.
func DecodeResponse(data []byte) (ShellResponse, error) {
	str := string(data)
	if !strings.HasPrefix(str, PrefixShellResp) {
		return ShellResponse{}, fmt.Errorf("invalid shell response prefix")
	}
	payload := strings.TrimPrefix(str, PrefixShellResp)
	var resp ShellResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		return ShellResponse{}, fmt.Errorf("failed to unmarshal shell response: %w", err)
	}
	return resp, nil
}
