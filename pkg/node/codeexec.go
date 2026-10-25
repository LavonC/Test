package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/unicompute/obj1/pkg/codeexec"
)

type pendingCodeResult struct {
	peerID string
	ch     chan codeexec.Response
}

// SetCodeExecutor enables remote code jobs on this node. Only the listed peer
// IDs are allowed to submit jobs; an empty allowlist intentionally denies all
// remote code requests. Do not use an open allowlist on an untrusted network.
func (n *Node) SetCodeExecutor(executor codeexec.Executor, allowedPeerIDs ...string) {
	allowed := make(map[string]struct{}, len(allowedPeerIDs))
	for _, id := range allowedPeerIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			allowed[id] = struct{}{}
		}
	}

	n.mu.Lock()
	n.codeExecutor = executor
	n.codeAllowedPeers = allowed
	n.mu.Unlock()
}

// ExecuteRemoteCode sends a source-code job to an already-connected peer and
// waits for its execution result. This uses the stream DataChannel protocol;
// the source is deliberately size-limited to fit in a single message.
func (n *Node) ExecuteRemoteCode(ctx context.Context, peerID string, req codeexec.Request) (codeexec.Response, error) {
	if req.JobID == "" {
		req.JobID = "job-" + uuid.New().String()
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = 10
	}
	wire, err := codeexec.EncodeRequest(req)
	if err != nil {
		return codeexec.Response{}, err
	}
	if strings.TrimSpace(peerID) == "" {
		return codeexec.Response{}, errors.New("peer ID is required")
	}

	respCh := make(chan codeexec.Response, 1)
	n.pendingCodeMu.Lock()
	if n.pendingCode == nil {
		n.pendingCode = make(map[string]pendingCodeResult)
	}
	if _, exists := n.pendingCode[req.JobID]; exists {
		n.pendingCodeMu.Unlock()
		return codeexec.Response{}, fmt.Errorf("job ID %q is already pending", req.JobID)
	}
	n.pendingCode[req.JobID] = pendingCodeResult{peerID: peerID, ch: respCh}
	n.pendingCodeMu.Unlock()

	removePending := func() {
		n.pendingCodeMu.Lock()
		delete(n.pendingCode, req.JobID)
		n.pendingCodeMu.Unlock()
	}
	if err := n.SendStream(peerID, wire); err != nil {
		removePending()
		return codeexec.Response{}, fmt.Errorf("send code job to %s: %w", peerID, err)
	}

	wait := time.Duration(req.TimeoutSeconds+8) * time.Second
	timer := time.NewTimer(wait)
	defer timer.Stop()
	defer removePending()

	select {
	case <-ctx.Done():
		return codeexec.Response{JobID: req.JobID, Language: req.Language, ExitCode: -1, Error: ctx.Err().Error()}, ctx.Err()
	case <-timer.C:
		return codeexec.Response{JobID: req.JobID, Language: req.Language, ExitCode: -1, Error: "timed out waiting for the remote worker response"}, errors.New("timed out waiting for the remote worker response")
	case resp := <-respCh:
		return resp, nil
	}
}

// handleCodeStreamMessage processes code-job request and response protocol
// frames. It returns true for recognized code-job frames so they do not leak
// into user-defined stream callbacks.
func (n *Node) handleCodeStreamMessage(peerID string, data []byte) bool {
	if bytes.HasPrefix(data, []byte(codeexec.ResponsePrefix)) {
		resp, err := codeexec.DecodeResponse(data)
		if err != nil {
			return true
		}
		n.pendingCodeMu.Lock()
		pending, ok := n.pendingCode[resp.JobID]
		if ok && pending.peerID == peerID {
			delete(n.pendingCode, resp.JobID)
		} else {
			ok = false
		}
		n.pendingCodeMu.Unlock()
		if ok && pending.ch != nil {
			select {
			case pending.ch <- resp:
			default:
			}
		}
		return true
	}

	if !bytes.HasPrefix(data, []byte(codeexec.RequestPrefix)) {
		return false
	}

	req, err := codeexec.DecodeRequest(data)
	if err != nil {
		// Invalid frames are consumed rather than exposed as ordinary messages.
		return true
	}

	go func() {
		n.mu.RLock()
		executor := n.codeExecutor
		_, allowed := n.codeAllowedPeers[peerID]
		n.mu.RUnlock()

		resp := codeexec.Response{
			JobID:    req.JobID,
			Language: req.Language,
			ExitCode: -1,
		}
		switch {
		case executor == nil:
			resp.Error = "remote code execution is disabled on this worker"
		case !allowed:
			resp.Error = "submitting peer is not in this worker's code execution allowlist"
		default:
			resp = executor.Execute(context.Background(), req)
			if resp.JobID == "" {
				resp.JobID = req.JobID
			}
			if resp.Language == "" {
				resp.Language = req.Language
			}
		}

		wireResp, encodeErr := codeexec.EncodeResponse(resp)
		if encodeErr != nil {
			// Last-resort small error response if a custom executor returned an
			// oversized/unencodable result.
			wireResp, encodeErr = codeexec.EncodeResponse(codeexec.Response{
				JobID: req.JobID, Language: req.Language, ExitCode: -1,
				Error: "worker could not encode execution response",
			})
		}
		if encodeErr == nil {
			_ = n.SendStream(peerID, wireResp)
		}
	}()
	return true
}
