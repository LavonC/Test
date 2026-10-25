package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/unicompute/obj1/pkg/blobstore"
	"github.com/unicompute/obj1/pkg/codeexec"
	"github.com/unicompute/obj1/pkg/instrumentation"
	"github.com/unicompute/obj1/pkg/nat"
	"github.com/unicompute/obj1/pkg/protocol"
	"github.com/unicompute/obj1/pkg/relay"
	"github.com/unicompute/obj1/pkg/remoteshell"
	"github.com/unicompute/obj1/pkg/signaling"
	"github.com/unicompute/obj1/pkg/transport"
)

var _ Substrate = (*Node)(nil)

// NodeConfig specifies configuration parameters for an Objective 1 Peer Node.
type NodeConfig struct {
	NodeID          string
	PoolID          string
	StorageDir      string
	StorageQuota    int64 // Quota in bytes
	SignalingURL    string
	STUNServersV4   []string
	STUNServersV6   []string
	TURNServers     []transport.TURNServerConfig
	RelayAddr       string        // Optional contributor relay address
	FallbackTimeout time.Duration // Fast fallback threshold (default 2s)
}

// Node represents a full Objective 1 peer node combining storage substrate,
// NAT classification, WebRTC P2P transport, relay fallback, and path instrumentation.
type Node struct {
	mu          sync.RWMutex
	cfg         NodeConfig
	store       blobstore.Store
	transport   *transport.Manager
	signaling   *signaling.Client
	classifier  *nat.Classifier
	natReport   *nat.NATReport
	tracker     *instrumentation.Tracker
	relayClient *relay.RelayClient

	onChunkRecv func(peerID, chunkID string, size int64)
	onStreamMsg func(peerID string, data []byte)
	onPeerConn  func(peerID string)
	onPeerDisc  func(peerID string)

	fleetNodes      map[string]bool
	onFleetPeerJoin func(peerID string)
	onFleetPeerLeft func(peerID string)

	shellExecutor  remoteshell.Executor
	codeExecutor      codeexec.Executor
	codeAllowedPeers map[string]struct{}
	pendingCodeMu    sync.Mutex
	pendingCode      map[string]pendingCodeResult
	pendingExecsMu sync.Mutex
	pendingExecs   map[string]chan remoteshell.ShellResponse

	started bool
	closed  bool
}

// NewNode initializes a new Objective 1 peer storage node.
func NewNode(cfg NodeConfig) (*Node, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("node ID is required")
	}
	if cfg.StorageDir == "" {
		return nil, errors.New("storage directory is required")
	}

	// 1. Initialize BlobStore with quota accounting
	store, err := blobstore.NewDiskBlobStore(blobstore.Config{
		RootDir:    cfg.StorageDir,
		QuotaBytes: cfg.StorageQuota,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init blobstore: %w", err)
	}

	tracker := instrumentation.NewTracker()
	classifier := nat.NewClassifier(cfg.STUNServersV4, cfg.STUNServersV6)

	n := &Node{
		cfg:           cfg,
		store:         store,
		tracker:       tracker,
		classifier:    classifier,
		relayClient:   &relay.RelayClient{},
		fleetNodes:    make(map[string]bool),
		shellExecutor:   remoteshell.NewSystemExecutor(),
		codeAllowedPeers: make(map[string]struct{}),
		pendingCode:      make(map[string]pendingCodeResult),
		pendingExecs:  make(map[string]chan remoteshell.ShellResponse),
	}

	return n, nil
}

// Start boots the node, classifies NAT, registers with signaling, and initializes P2P transport.
func (n *Node) Start(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.started {
		return nil
	}

	// 1. Run dual-stack NAT and CGNAT classification
	natCtx, cancelNAT := context.WithTimeout(ctx, 3*time.Second)
	report, err := n.classifier.Classify(natCtx)
	cancelNAT()
	if err != nil || report == nil {
		report = &nat.NATReport{
			NATType:   nat.NATTypeUnknown,
			Timestamp: time.Now(),
		}
	}
	n.natReport = report

	// 2. Connect to signaling hub (if URL provided)
	if n.cfg.SignalingURL != "" {
		sigClient, err := signaling.NewClient(n.cfg.SignalingURL, n.cfg.NodeID, n.handleSignalingEnvelope)
		if err != nil {
			// Invariant CP-1: Signaling failure must not prevent local operations,
			// but we return the warning/error for awareness
			return fmt.Errorf("failed to connect to signaling hub: %w", err)
		}
		n.signaling = sigClient
	}

	// 3. Initialize WebRTC P2P Transport Manager
	stunList := n.cfg.STUNServersV4
	if len(n.cfg.STUNServersV6) > 0 {
		stunList = append(stunList, n.cfg.STUNServersV6...)
	}

	tm, err := transport.NewManager(transport.Config{
		NodeID:          n.cfg.NodeID,
		STUNServers:     stunList,
		TURNServers:     n.cfg.TURNServers,
		FallbackTimeout: n.cfg.FallbackTimeout,
	}, n.signaling, n.tracker)
	if err != nil {
		return fmt.Errorf("failed to create transport manager: %w", err)
	}
	n.transport = tm

	// Hook incoming peer connections
	tm.SetOnNewConnection(n.bindIncomingPeerConnection)

	n.started = true
	return nil
}

func (n *Node) handleSignalingEnvelope(env protocol.Envelope) {
	if env.Type == protocol.MsgPeerJoined {
		var p protocol.PeerEventPayload
		_ = json.Unmarshal(env.Payload, &p)
		if p.NodeID != "" && p.NodeID != n.cfg.NodeID {
			n.mu.Lock()
			n.fleetNodes[p.NodeID] = true
			cb := n.onFleetPeerJoin
			n.mu.Unlock()
			if cb != nil {
				cb(p.NodeID)
			}
		}
		return
	}

	if env.Type == protocol.MsgPeerLeft {
		var p protocol.PeerEventPayload
		_ = json.Unmarshal(env.Payload, &p)
		if p.NodeID != "" {
			n.mu.Lock()
			delete(n.fleetNodes, p.NodeID)
			cb := n.onFleetPeerLeft
			n.mu.Unlock()
			if cb != nil {
				cb(p.NodeID)
			}
		}
		return
	}

	if env.Type == protocol.MsgPeerListResponse {
		var list protocol.PeerListPayload
		_ = json.Unmarshal(env.Payload, &list)
		n.mu.Lock()
		for _, id := range list.Nodes {
			if id != n.cfg.NodeID {
				n.fleetNodes[id] = true
			}
		}
		n.mu.Unlock()
		return
	}

	n.mu.RLock()
	tm := n.transport
	n.mu.RUnlock()

	if tm != nil {
		_ = tm.HandleSignalingEnvelope(env)
	}
}

func (n *Node) bindIncomingPeerConnection(pc *transport.PeerConnection) {
	peerID := pc.RemotePeerID

	pc.SetOnChunkReceived(func(chunkID string, data []byte) {
		// Save received chunk into local blob store
		_, err := n.store.Put(chunkID, bytes.NewReader(data))
		n.mu.RLock()
		cb := n.onChunkRecv
		n.mu.RUnlock()
		if err == nil && cb != nil {
			cb(peerID, chunkID, int64(len(data)))
		}
	})

	pc.SetOnStreamMessage(func(data []byte) {
		if n.handleInternalStreamMessage(peerID, data) {
			return
		}

		n.mu.RLock()
		cb := n.onStreamMsg
		n.mu.RUnlock()
		if cb != nil {
			cb(peerID, data)
		}
	})

	n.mu.RLock()
	cb := n.onPeerConn
	n.mu.RUnlock()
	if cb != nil {
		cb(peerID)
	}
}

// ConnectPeer establishes a direct or relayed WebRTC data plane connection to a target peer.
func (n *Node) ConnectPeer(ctx context.Context, targetPeerID string) (*transport.PeerConnection, error) {
	n.mu.RLock()
	tm := n.transport
	n.mu.RUnlock()

	if tm == nil {
		return nil, errors.New("node transport not started")
	}

	pc, err := tm.ConnectPeer(ctx, targetPeerID)
	if err != nil {
		return nil, err
	}

	n.bindIncomingPeerConnection(pc)
	return pc, nil
}

// TransferChunk streams a local chunk to a remote peer over WebRTC DataChannel.
func (n *Node) TransferChunk(ctx context.Context, peerID string, chunkID string) error {
	n.mu.RLock()
	tm := n.transport
	n.mu.RUnlock()

	if tm == nil {
		return errors.New("node transport not started")
	}

	conn, ok := tm.GetConnection(peerID)
	if !ok {
		return fmt.Errorf("no active connection to peer %s", peerID)
	}

	stat, err := n.store.Stat(chunkID)
	if err != nil {
		return fmt.Errorf("chunk %s not found in local store: %w", chunkID, err)
	}

	rc, err := n.store.Get(chunkID)
	if err != nil {
		return err
	}
	defer rc.Close()

	return conn.SendChunk(ctx, chunkID, rc, stat.Size)
}

// FetchChunk requests and receives a chunk from a remote peer, verifying SHA-256 and storing it.
func (n *Node) FetchChunk(ctx context.Context, peerID string, chunkID string, expectedSize int64) error {
	n.mu.RLock()
	tm := n.transport
	n.mu.RUnlock()

	if tm == nil {
		return errors.New("node transport not started")
	}

	conn, ok := tm.GetConnection(peerID)
	if !ok {
		return fmt.Errorf("no active connection to peer %s", peerID)
	}

	// Wait for chunk over DataChannel
	data, err := conn.ReceiveChunk(ctx, chunkID, expectedSize)
	if err != nil {
		return fmt.Errorf("failed to receive chunk %s: %w", chunkID, err)
	}

	// Commit to local store with SHA256 integrity check and quota accounting
	_, err = n.store.Put(chunkID, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to store fetched chunk %s: %w", chunkID, err)
	}

	return nil
}

// SendStream transmits an execution log or out-of-band message across the stream DataChannel.
func (n *Node) SendStream(peerID string, data []byte) error {
	n.mu.RLock()
	tm := n.transport
	n.mu.RUnlock()

	if tm == nil {
		return errors.New("node transport not started")
	}

	conn, ok := tm.GetConnection(peerID)
	if !ok {
		return fmt.Errorf("no active connection to peer %s", peerID)
	}

	return conn.SendStreamMessage(data)
}

// handleInternalStreamMessage dispatches protocol-level messages such as remote shell execution.
func (n *Node) handleInternalStreamMessage(peerID string, data []byte) bool {
	if n.handleCodeStreamMessage(peerID, data) {
		return true
	}
	// 1. Remote Shell Request
	if bytes.HasPrefix(data, []byte(remoteshell.PrefixShellReq)) {
		req, err := remoteshell.DecodeRequest(data)
		if err != nil {
			return true
		}

		go func() {
			n.mu.RLock()
			exec := n.shellExecutor
			n.mu.RUnlock()
			if exec == nil {
				exec = remoteshell.NewSystemExecutor()
			}

			resp := exec.Execute(context.Background(), req)
			respBytes, err := remoteshell.EncodeResponse(resp)
			if err == nil {
				_ = n.SendStream(peerID, respBytes)
			}
		}()
		return true
	}

	// 2. Remote Shell Response
	if bytes.HasPrefix(data, []byte(remoteshell.PrefixShellResp)) {
		resp, err := remoteshell.DecodeResponse(data)
		if err != nil {
			return true
		}

		n.pendingExecsMu.Lock()
		ch, ok := n.pendingExecs[resp.ExecID]
		if ok {
			delete(n.pendingExecs, resp.ExecID)
		}
		n.pendingExecsMu.Unlock()

		if ok && ch != nil {
			select {
			case ch <- resp:
			default:
			}
		}
		return true
	}

	return false
}

// SetShellExecutor configures a custom shell executor for handling remote command execution.
func (n *Node) SetShellExecutor(exec remoteshell.Executor) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.shellExecutor = exec
}

// ExecuteRemoteCommand transmits a shell command execution request to a connected peer and awaits result.
func (n *Node) ExecuteRemoteCommand(ctx context.Context, peerID string, req remoteshell.ShellRequest) (remoteshell.ShellResponse, error) {
	if req.ExecID == "" {
		req.ExecID = "exec-" + uuid.New().String()[:8]
	}

	n.mu.RLock()
	tm := n.transport
	n.mu.RUnlock()

	if tm == nil {
		return remoteshell.ShellResponse{}, errors.New("node transport not started")
	}

	_, ok := tm.GetConnection(peerID)
	if !ok {
		return remoteshell.ShellResponse{}, fmt.Errorf("no active connection to peer %s", peerID)
	}

	respCh := make(chan remoteshell.ShellResponse, 1)

	n.pendingExecsMu.Lock()
	n.pendingExecs[req.ExecID] = respCh
	n.pendingExecsMu.Unlock()

	defer func() {
		n.pendingExecsMu.Lock()
		delete(n.pendingExecs, req.ExecID)
		n.pendingExecsMu.Unlock()
	}()

	wireBytes, err := remoteshell.EncodeRequest(req)
	if err != nil {
		return remoteshell.ShellResponse{}, fmt.Errorf("failed to encode shell request: %w", err)
	}

	if err := n.SendStream(peerID, wireBytes); err != nil {
		return remoteshell.ShellResponse{}, fmt.Errorf("failed to send shell request to %s: %w", peerID, err)
	}

	timeout := 35 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs)*time.Millisecond + 5*time.Second
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return remoteshell.ShellResponse{
			ExecID:   req.ExecID,
			ExitCode: -1,
			Error:    ctx.Err().Error(),
		}, ctx.Err()
	case <-timer.C:
		return remoteshell.ShellResponse{
			ExecID:   req.ExecID,
			ExitCode: -1,
			Error:    "remote command execution response timed out",
		}, errors.New("remote execution timed out waiting for peer response")
	case resp := <-respCh:
		return resp, nil
	}
}

// ExecuteRemoteCommandAll executes the command across all active connected peers in parallel.
func (n *Node) ExecuteRemoteCommandAll(ctx context.Context, req remoteshell.ShellRequest) map[string]remoteshell.ShellResponse {
	peers := n.GetConnectedPeers()
	results := make(map[string]remoteshell.ShellResponse)
	if len(peers) == 0 {
		return results
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, p := range peers {
		wg.Add(1)
		go func(peerID string) {
			defer wg.Done()
			pReq := req
			pReq.ExecID = fmt.Sprintf("%s-%s", peerID, uuid.New().String()[:6])
			resp, err := n.ExecuteRemoteCommand(ctx, peerID, pReq)
			if err != nil && resp.Error == "" {
				resp.Error = err.Error()
				resp.ExitCode = -1
			}
			mu.Lock()
			results[peerID] = resp
			mu.Unlock()
		}(p)
	}

	wg.Wait()
	return results
}

// Store returns the underlying local blob store.
func (n *Node) Store() blobstore.Store {
	return n.store
}

// PutBlob writes a blob to the local substrate store.
func (n *Node) PutBlob(id string, r io.Reader) (int64, error) {
	return n.store.Put(id, r)
}

// GetBlob reads a blob from the local substrate store.
func (n *Node) GetBlob(id string) (io.ReadCloser, error) {
	return n.store.Get(id)
}

// HasBlob checks presence of a blob in the local substrate store.
func (n *Node) HasBlob(id string) bool {
	return n.store.Has(id)
}

// DeleteBlob removes a blob from the local substrate store.
func (n *Node) DeleteBlob(id string) error {
	return n.store.Delete(id)
}

// StatBlob retrieves metadata for a local blob.
func (n *Node) StatBlob(id string) (blobstore.BlobInfo, error) {
	return n.store.Stat(id)
}

// ListBlobs lists all local blobs.
func (n *Node) ListBlobs() ([]blobstore.BlobInfo, error) {
	return n.store.List()
}

// VerifyBlob checks cryptographic SHA-256 integrity of a blob.
func (n *Node) VerifyBlob(id string) (bool, error) {
	return n.store.Verify(id)
}

// Scrub scans and verifies all local blobs against bitrot or tampering.
func (n *Node) Scrub() ([]string, error) {
	return n.store.Scrub()
}

// StorageUsage returns current quota and storage usage stats.
func (n *Node) StorageUsage() blobstore.StorageUsage {
	return n.store.Usage()
}

// NATReport returns the node's NAT and CGNAT classification report.
func (n *Node) NATReport() *nat.NATReport {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.natReport
}

// GetPathMetrics returns path telemetry for a connected peer.
func (n *Node) GetPathMetrics(peerID string) (instrumentation.PathMetrics, bool) {
	return n.tracker.GetMetrics(peerID)
}

// FleetStats aggregates connection metrics across all peers.
func (n *Node) FleetStats() instrumentation.FleetNATStats {
	return n.tracker.AggregateFleetStats()
}

// Event callbacks for higher objectives (O2, O3, O4, O5)
func (n *Node) SetOnChunkReceived(fn func(peerID, chunkID string, size int64)) {
	n.mu.Lock()
	n.onChunkRecv = fn
	n.mu.Unlock()
}

func (n *Node) SetOnStreamMessage(fn func(peerID string, data []byte)) {
	n.mu.Lock()
	n.onStreamMsg = fn
	n.mu.Unlock()
}

func (n *Node) SetOnPeerConnected(fn func(peerID string)) {
	n.mu.Lock()
	n.onPeerConn = fn
	n.mu.Unlock()
}

func (n *Node) SetOnPeerDisconnected(fn func(peerID string)) {
	n.mu.Lock()
	n.onPeerDisc = fn
	n.mu.Unlock()
}

// GetConnectedPeers returns a list of peer IDs with active WebRTC connections.
func (n *Node) GetConnectedPeers() []string {
	n.mu.RLock()
	tm := n.transport
	n.mu.RUnlock()

	if tm == nil {
		return nil
	}
	return tm.ActivePeers()
}

// GetFleetNodes returns all online nodes currently registered with the coordinator.
func (n *Node) GetFleetNodes(ctx context.Context) ([]string, error) {
	n.mu.RLock()
	sig := n.signaling
	n.mu.RUnlock()

	if sig != nil {
		nodes, err := sig.QueryPeers(ctx)
		if err == nil && len(nodes) > 0 {
			n.mu.Lock()
			for _, id := range nodes {
				if id != n.cfg.NodeID {
					n.fleetNodes[id] = true
				}
			}
			n.mu.Unlock()
		}
	}

	n.mu.RLock()
	defer n.mu.RUnlock()
	res := make([]string, 0, len(n.fleetNodes))
	for id := range n.fleetNodes {
		res = append(res, id)
	}
	return res, nil
}

// DisconnectPeer cleanly disconnects an active WebRTC peer.
func (n *Node) DisconnectPeer(peerID string) error {
	n.mu.RLock()
	tm := n.transport
	n.mu.RUnlock()

	if tm == nil {
		return errors.New("node transport not started")
	}
	return tm.DisconnectPeer(peerID)
}

// SetOnFleetPeerJoined sets callback when a new node joins the coordinator fleet.
func (n *Node) SetOnFleetPeerJoined(fn func(string)) {
	n.mu.Lock()
	n.onFleetPeerJoin = fn
	n.mu.Unlock()
}

// SetOnFleetPeerLeft sets callback when a node disconnects from the coordinator fleet.
func (n *Node) SetOnFleetPeerLeft(fn func(string)) {
	n.mu.Lock()
	n.onFleetPeerLeft = fn
	n.mu.Unlock()
}

// Close gracefully terminates all connections, closes the blobstore, and leaves the pool.
func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed {
		return nil
	}
	n.closed = true

	if n.transport != nil {
		_ = n.transport.Close()
	}
	if n.signaling != nil {
		_ = n.signaling.Close()
	}
	if n.store != nil {
		_ = n.store.Close()
	}

	return nil
}
