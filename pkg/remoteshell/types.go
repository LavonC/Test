// language: Go, file: pkg/remoteshell/types.go, runtime: Go 1.22+, target: Windows/Linux/macOS
package remoteshell

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// Wire protocol prefixes for DataChannel stream multiplexing
	PrefixShellReq  = "__SHELL_REQ__:"
	PrefixShellResp = "__SHELL_RESP__:"
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

// EncodeResponse serializes a ShellResponse into wire format.
func EncodeResponse(resp ShellResponse) ([]byte, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal shell response: %w", err)
	}
	return []byte(PrefixShellResp + string(data)), nil
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
