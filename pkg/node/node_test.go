package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unicompute/obj1/pkg/blobstore"
	"github.com/unicompute/obj1/pkg/codeexec"
	"github.com/unicompute/obj1/pkg/remoteshell"
	"github.com/unicompute/obj1/pkg/signaling"
	"github.com/unicompute/obj1/pkg/transport"
)

func TestStubNode_Substrate(t *testing.T) {
	node1 := NewStubNode("stub-1", 10*1024*1024)
	node2 := NewStubNode("stub-2", 10*1024*1024)

	node1.ConnectStub(node2)

	// Test local blob write
	data := []byte("test stub data for objective 2/3/4/5")
	h := sha256.Sum256(data)
	cid := hex.EncodeToString(h[:])

	n, err := node1.PutBlob(cid, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PutBlob failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("expected %d bytes, got %d", len(data), n)
	}

	// Transfer from node1 to node2
	ctx := context.Background()
	err = node1.TransferChunk(ctx, "stub-2", cid)
	if err != nil {
		t.Fatalf("TransferChunk failed: %v", err)
	}

	if !node2.HasBlob(cid) {
		t.Fatalf("expected node2 to have blob %s", cid)
	}

	// Verify on node2
	valid, err := node2.VerifyBlob(cid)
	if err != nil || !valid {
		t.Fatalf("VerifyBlob failed: valid=%v, err=%v", valid, err)
	}

	// Test stream message
	err = node1.SendStream("stub-2", []byte("log line 1"))
	if err != nil {
		t.Fatalf("SendStream failed: %v", err)
	}
	if len(node2.StreamMsgs["stub-1"]) != 1 {
		t.Fatalf("expected 1 stream msg, got %d", len(node2.StreamMsgs["stub-1"]))
	}

	// Test path metrics
	metrics, ok := node1.GetPathMetrics("stub-2")
	if !ok || !metrics.IsDirect {
		t.Fatalf("expected direct connection metric")
	}

	// Test StatBlob, ListBlobs, Scrub on stub
	stat, err := node1.StatBlob(cid)
	if err != nil || stat.Size != int64(len(data)) {
		t.Fatalf("StatBlob on stub failed: %v", err)
	}
	list, err := node1.ListBlobs()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListBlobs on stub failed: %v", err)
	}
	corrupts, err := node1.Scrub()
	if err != nil || len(corrupts) != 0 {
		t.Fatalf("Scrub on stub failed: %v", err)
	}
}

func TestNode_SubstrateInterface(t *testing.T) {
	tmpDir := t.TempDir()
	n, err := NewNode(NodeConfig{
		NodeID:       "node-sub-test",
		PoolID:       "pool-sub",
		StorageDir:   tmpDir,
		StorageQuota: 10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}
	defer n.Close()

	// Compile-time and runtime check that *Node implements Substrate
	var sub Substrate = n

	data := []byte("testing Node via Substrate interface directly")
	h := sha256.Sum256(data)
	cid := hex.EncodeToString(h[:])

	nBytes, err := sub.PutBlob(cid, bytes.NewReader(data))
	if err != nil || nBytes != int64(len(data)) {
		t.Fatalf("Substrate.PutBlob failed: %v", err)
	}

	if !sub.HasBlob(cid) {
		t.Fatalf("Substrate.HasBlob returned false")
	}

	rc, err := sub.GetBlob(cid)
	if err != nil {
		t.Fatalf("Substrate.GetBlob failed: %v", err)
	}
	readData, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(readData, data) {
		t.Fatalf("Substrate.GetBlob read mismatch")
	}

	valid, err := sub.VerifyBlob(cid)
	if err != nil || !valid {
		t.Fatalf("Substrate.VerifyBlob failed: %v", err)
	}

	stat, err := sub.StatBlob(cid)
	if err != nil || stat.Size != int64(len(data)) {
		t.Fatalf("Substrate.StatBlob failed: %v", err)
	}

	list, err := sub.ListBlobs()
	if err != nil || len(list) != 1 {
		t.Fatalf("Substrate.ListBlobs failed: %v", err)
	}

	corrupts, err := sub.Scrub()
	if err != nil || len(corrupts) != 0 {
		t.Fatalf("Substrate.Scrub failed: %v", err)
	}

	usage := sub.StorageUsage()
	if usage.UsedBytes != int64(len(data)) {
		t.Fatalf("Substrate.StorageUsage mismatch: got %d", usage.UsedBytes)
	}

	err = sub.DeleteBlob(cid)
	if err != nil {
		t.Fatalf("Substrate.DeleteBlob failed: %v", err)
	}
}

func TestNode_EndToEndChunkExchange(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir1 := filepath.Join(tmpDir, "node1_storage")
	storeDir2 := filepath.Join(tmpDir, "node2_storage")

	// 1. Start Signaling Coordinator Hub
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	sigURL := hub.URL()

	// 2. Initialize Node 1 and Node 2
	n1, err := NewNode(NodeConfig{
		NodeID:       "peer-node-1",
		PoolID:       "academic-pool-01",
		StorageDir:   storeDir1,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 1: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:       "peer-node-2",
		PoolID:       "academic-pool-01",
		StorageDir:   storeDir2,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 2: %v", err)
	}
	defer n2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := n1.Start(ctx); err != nil {
		t.Fatalf("node 1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("node 2 Start failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond) // Allow signaling registration

	// Connect Node 1 -> Node 2
	conn1, err := n1.ConnectPeer(ctx, "peer-node-2")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer conn1.Close()

	time.Sleep(200 * time.Millisecond)

	// 3. Create test chunk on Node 1
	chunkPayload := bytes.Repeat([]byte("UniCompute Objective 1 Storage Substrate Test Chunk! "), 1200) // ~64 KB
	h := sha256.Sum256(chunkPayload)
	chunkCID := hex.EncodeToString(h[:])

	_, err = n1.Store().Put(chunkCID, bytes.NewReader(chunkPayload))
	if err != nil {
		t.Fatalf("Put on Node 1 failed: %v", err)
	}

	// 4. Transfer chunk from Node 1 to Node 2
	err = n1.TransferChunk(ctx, "peer-node-2", chunkCID)
	if err != nil {
		t.Fatalf("TransferChunk failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// 5. Verify chunk received and persisted on Node 2
	if !n2.Store().Has(chunkCID) {
		t.Fatalf("Node 2 did not store transferred chunk %s", chunkCID)
	}

	valid, err := n2.Store().Verify(chunkCID)
	if err != nil || !valid {
		t.Fatalf("Node 2 chunk verification failed: valid=%v, err=%v", valid, err)
	}

	// Read content back from Node 2
	rc, err := n2.Store().Get(chunkCID)
	if err != nil {
		t.Fatalf("Get on Node 2 failed: %v", err)
	}
	defer rc.Close()
	readBytes, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(readBytes, chunkPayload) {
		t.Fatalf("data mismatch on Node 2")
	}

	// 5b. Test short chunk ID (less than 64 bytes) to test null padding trimming
	shortCID := "chunk-short-id"
	shortPayload := []byte("short chunk id payload test")
	_, err = n1.Store().Put(shortCID, bytes.NewReader(shortPayload))
	if err != nil {
		t.Fatalf("Put short chunk failed: %v", err)
	}
	err = n1.TransferChunk(ctx, "peer-node-2", shortCID)
	if err != nil {
		t.Fatalf("TransferChunk short failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !n2.Store().Has(shortCID) {
		t.Fatalf("Node 2 did not store short CID chunk")
	}
	rcShort, err := n2.Store().Get(shortCID)
	if err != nil {
		t.Fatalf("Get short chunk on Node 2 failed: %v", err)
	}
	defer rcShort.Close()
	readShort, _ := io.ReadAll(rcShort)
	if !bytes.Equal(readShort, shortPayload) {
		t.Fatalf("short payload mismatch on Node 2")
	}

	// 5c. Test FetchChunk (0x10) pull from Node 2 to Node 1
	fetchPayload := []byte("UniCompute FetchChunk 0x10 protocol verification data payload!")
	hFetch := sha256.Sum256(fetchPayload)
	fetchCID := hex.EncodeToString(hFetch[:])

	_, err = n1.Store().Put(fetchCID, bytes.NewReader(fetchPayload))
	if err != nil {
		t.Fatalf("Put on Node 1 failed: %v", err)
	}

	err = n2.FetchChunk(ctx, "peer-node-1", fetchCID, int64(len(fetchPayload)))
	if err != nil {
		t.Fatalf("FetchChunk failed: %v", err)
	}

	if !n2.Store().Has(fetchCID) {
		t.Fatalf("Node 2 did not store fetched chunk %s", fetchCID)
	}
	validFetch, err := n2.Store().Verify(fetchCID)
	if err != nil || !validFetch {
		t.Fatalf("Node 2 VerifyBlob failed on fetched chunk: valid=%v, err=%v", validFetch, err)
	}
	rcFetch, err := n2.Store().Get(fetchCID)
	if err != nil {
		t.Fatalf("Get fetched chunk on Node 2 failed: %v", err)
	}
	defer rcFetch.Close()
	readFetch, _ := io.ReadAll(rcFetch)
	if !bytes.Equal(readFetch, fetchPayload) {
		t.Fatalf("fetched data mismatch on Node 2")
	}

	// 5d. Test FetchChunk for non-existent chunk returns fast error without stalling
	nonExistentCID := "0000000000000000000000000000000000000000000000000000000000000000"
	fetchStart := time.Now()
	err = n2.FetchChunk(ctx, "peer-node-1", nonExistentCID, 100)
	fetchDur := time.Since(fetchStart)
	if err == nil {
		t.Fatalf("expected FetchChunk for non-existent chunk to fail")
	}
	if fetchDur > 5*time.Second {
		t.Fatalf("FetchChunk took %v, expected fast failure (< 5s)", fetchDur)
	}

	// 6. Test Path Metrics & Fleet Telemetry
	metrics, ok := n1.GetPathMetrics("peer-node-2")
	if !ok {
		t.Fatalf("expected path metrics for peer-node-2")
	}
	if metrics.BytesSent <= 0 {
		t.Errorf("expected BytesSent > 0, got %d", metrics.BytesSent)
	}
}

// TestNode_InvariantCP1_CoordinatorFailureResilience tests that established peer connections
// and chunk data transfers survive when the coordinator/signaling service is killed.
func TestNode_InvariantCP1_CoordinatorFailureResilience(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir1 := filepath.Join(tmpDir, "node1_storage")
	storeDir2 := filepath.Join(tmpDir, "node2_storage")

	// 1. Start Signaling Coordinator Hub
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}

	sigURL := hub.URL()

	n1, err := NewNode(NodeConfig{
		NodeID:       "node-cp1-A",
		PoolID:       "pool-alpha",
		StorageDir:   storeDir1,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 1: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:       "node-cp1-B",
		PoolID:       "pool-alpha",
		StorageDir:   storeDir2,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 2: %v", err)
	}
	defer n2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := n1.Start(ctx); err != nil {
		t.Fatalf("node 1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("node 2 Start failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	// Connect Node 1 -> Node 2
	conn1, err := n1.ConnectPeer(ctx, "node-cp1-B")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer conn1.Close()

	time.Sleep(200 * time.Millisecond)

	// =========================================================================
	// INVARIANT CP-1 TEST: KILL THE COORDINATOR / SIGNALING HUB
	// =========================================================================
	err = hub.Close()
	if err != nil {
		t.Fatalf("failed to close hub: %v", err)
	}

	// Verify coordinator is completely dead
	time.Sleep(100 * time.Millisecond)

	// Transfer new chunk AFTER coordinator is dead!
	chunkPayload := []byte("DATA PLANE INDEPENDENCE VERIFIED - COORDINATOR IS TERMINATED")
	h := sha256.Sum256(chunkPayload)
	chunkCID := hex.EncodeToString(h[:])

	_, err = n1.Store().Put(chunkCID, bytes.NewReader(chunkPayload))
	if err != nil {
		t.Fatalf("Put on Node 1 failed: %v", err)
	}

	err = n1.TransferChunk(ctx, "node-cp1-B", chunkCID)
	if err != nil {
		t.Fatalf("TransferChunk failed after coordinator killed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if !n2.Store().Has(chunkCID) {
		t.Fatalf("Node 2 did not receive chunk after coordinator was terminated")
	}

	valid, err := n2.Store().Verify(chunkCID)
	if err != nil || !valid {
		t.Fatalf("Node 2 chunk invalid after coordinator death: %v", err)
	}

	rc, err := n2.Store().Get(chunkCID)
	if err != nil {
		t.Fatalf("Get on Node 2 failed: %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !bytes.Equal(data, chunkPayload) {
		t.Fatalf("data mismatch after coordinator death")
	}
}

func TestNode_ConnectPeer_WithDefaultSTUN(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir1 := filepath.Join(tmpDir, "node1_storage")
	storeDir2 := filepath.Join(tmpDir, "node2_storage")

	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	sigURL := hub.URL()

	// Initialized with default unicompute-node STUN server format ("stun.l.google.com:19302")
	n1, err := NewNode(NodeConfig{
		NodeID:        "node-stun-1",
		PoolID:        "pool-stun",
		StorageDir:    storeDir1,
		StorageQuota:  50 * 1024 * 1024,
		SignalingURL:  sigURL,
		STUNServersV4: []string{"stun.l.google.com:19302"},
	})
	if err != nil {
		t.Fatalf("failed to create node 1: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:        "node-stun-2",
		PoolID:        "pool-stun",
		StorageDir:    storeDir2,
		StorageQuota:  50 * 1024 * 1024,
		SignalingURL:  sigURL,
		STUNServersV4: []string{"stun.l.google.com:19302"},
	})
	if err != nil {
		t.Fatalf("failed to create node 2: %v", err)
	}
	defer n2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := n1.Start(ctx); err != nil {
		t.Fatalf("node 1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("node 2 Start failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	// Attempt ConnectPeer - must NOT fail with "failed to create PeerConnection: InvalidAccessError: unknown scheme type"
	conn1, err := n1.ConnectPeer(ctx, "node-stun-2")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer conn1.Close()
}

func TestNode_ConnectPeer_WithVariedSTUNAndTURN(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir1 := filepath.Join(tmpDir, "node1_storage")
	storeDir2 := filepath.Join(tmpDir, "node2_storage")

	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	sigURL := hub.URL()

	n1, err := NewNode(NodeConfig{
		NodeID:        "node-varied-1",
		PoolID:        "pool-varied",
		StorageDir:    storeDir1,
		StorageQuota:  50 * 1024 * 1024,
		SignalingURL:  sigURL,
		STUNServersV4: []string{"stun://stun.l.google.com:19302, stun1.l.google.com:19302"},
		STUNServersV6: []string{"[::1]:19302", "2001:db8::1:19302"},
		TURNServers: []transport.TURNServerConfig{
			{URL: "turn://relay.unicompute.org:3478, turns://relay.unicompute.org:5349", Username: "user", Credential: "pwd"},
		},
	})
	if err != nil {
		t.Fatalf("failed to create node 1: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:        "node-varied-2",
		PoolID:        "pool-varied",
		StorageDir:    storeDir2,
		StorageQuota:  50 * 1024 * 1024,
		SignalingURL:  sigURL,
		STUNServersV4: []string{"stun:stun.l.google.com:19302"},
	})
	if err != nil {
		t.Fatalf("failed to create node 2: %v", err)
	}
	defer n2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := n1.Start(ctx); err != nil {
		t.Fatalf("node 1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("node 2 Start failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	// Attempt ConnectPeer with mixed/varied schemes
	conn1, err := n1.ConnectPeer(ctx, "node-varied-2")
	if err != nil {
		t.Fatalf("ConnectPeer with varied STUN/TURN failed: %v", err)
	}
	defer conn1.Close()
}

func TestNode_RemoteCommandExecution(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir1 := filepath.Join(tmpDir, "node1_exec_store")
	storeDir2 := filepath.Join(tmpDir, "node2_exec_store")

	// 1. Start Signaling Coordinator Hub
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	sigURL := hub.URL()

	// 2. Initialize Node 1 and Node 2
	n1, err := NewNode(NodeConfig{
		NodeID:       "exec-node-1",
		PoolID:       "exec-pool",
		StorageDir:   storeDir1,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 1: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:       "exec-node-2",
		PoolID:       "exec-pool",
		StorageDir:   storeDir2,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 2: %v", err)
	}
	defer n2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := n1.Start(ctx); err != nil {
		t.Fatalf("node 1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("node 2 Start failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	// Connect Node 1 -> Node 2 over WebRTC
	conn1, err := n1.ConnectPeer(ctx, "exec-node-2")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer conn1.Close()

	time.Sleep(200 * time.Millisecond)

	// 3. Security gating verification: remote shell is disabled by default on Node 2
	req := remoteshell.ShellRequest{
		Command:   "echo unicompute_remote_shell_ok",
		ShellType: remoteshell.ShellAuto,
		TimeoutMs: 10000,
	}

	blockedResp, err := n1.ExecuteRemoteCommand(ctx, "exec-node-2", req)
	if err != nil {
		t.Fatalf("ExecuteRemoteCommand expected response, got err: %v", err)
	}
	if blockedResp.ExitCode != -1 || !strings.Contains(blockedResp.Error, "remote shell disabled") {
		t.Fatalf("expected remote shell to be blocked on disabled node, got: %+v", blockedResp)
	}

	// 4. Test remote shell enabled but caller not in allowlist
	n2.SetRemoteShellConfig(true, []string{"untrusted-node-99"})
	notAllowedResp, err := n1.ExecuteRemoteCommand(ctx, "exec-node-2", req)
	if err != nil {
		t.Fatalf("ExecuteRemoteCommand expected response, got err: %v", err)
	}
	if notAllowedResp.ExitCode != -1 || !strings.Contains(notAllowedResp.Error, "peer not in shell allowlist") {
		t.Fatalf("expected remote shell to be rejected for unlisted peer, got: %+v", notAllowedResp)
	}

	// 5. Opt-in with allowlist on Node 2 and verify successful execution
	n2.SetRemoteShellConfig(true, []string{"exec-node-1"})

	resp, err := n1.ExecuteRemoteCommand(ctx, "exec-node-2", req)
	if err != nil {
		t.Fatalf("ExecuteRemoteCommand failed: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (err: %s, stderr: %s)", resp.ExitCode, resp.Error, resp.Stderr)
	}

	if !strings.Contains(strings.ToLower(resp.Stdout), "unicompute_remote_shell_ok") {
		t.Fatalf("expected stdout to contain test string, got: %q", resp.Stdout)
	}

	// 5. Test ExecuteRemoteCommandAll
	allResults := n1.ExecuteRemoteCommandAll(ctx, req)
	if len(allResults) != 1 {
		t.Fatalf("expected 1 result from ExecuteRemoteCommandAll, got %d", len(allResults))
	}

	res2, ok := allResults["exec-node-2"]
	if !ok || res2.ExitCode != 0 || !strings.Contains(strings.ToLower(res2.Stdout), "unicompute_remote_shell_ok") {
		t.Fatalf("unexpected broadcast result: %+v", res2)
	}
}

func TestNode_ConnectPeer_SingleOnPeerConnectedCallback(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "node-test-peer-conn-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storeDir1 := filepath.Join(tempDir, "store1")
	storeDir2 := filepath.Join(tempDir, "store2")

	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	sigURL := hub.URL()

	n1, err := NewNode(NodeConfig{
		NodeID:       "single-cb-node-1",
		PoolID:       "cb-pool",
		StorageDir:   storeDir1,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 1: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:       "single-cb-node-2",
		PoolID:       "cb-pool",
		StorageDir:   storeDir2,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 2: %v", err)
	}
	defer n2.Close()

	var n1ConnectedCount int32
	n1.SetOnPeerConnected(func(peerID string) {
		if peerID == "single-cb-node-2" {
			atomic.AddInt32(&n1ConnectedCount, 1)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := n1.Start(ctx); err != nil {
		t.Fatalf("node 1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("node 2 Start failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	conn1, err := n1.ConnectPeer(ctx, "single-cb-node-2")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer conn1.Close()

	time.Sleep(300 * time.Millisecond)

	count := atomic.LoadInt32(&n1ConnectedCount)
	if count != 1 {
		t.Fatalf("expected SetOnPeerConnected to fire exactly 1 time, got %d", count)
	}
}

func TestNode_SetOnPeerDisconnected(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "node-test-peer-disc-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storeDir1 := filepath.Join(tempDir, "store1")
	storeDir2 := filepath.Join(tempDir, "store2")

	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	sigURL := hub.URL()

	n1, err := NewNode(NodeConfig{
		NodeID:       "disc-cb-node-1",
		PoolID:       "cb-pool",
		StorageDir:   storeDir1,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 1: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:       "disc-cb-node-2",
		PoolID:       "cb-pool",
		StorageDir:   storeDir2,
		StorageQuota: 50 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create node 2: %v", err)
	}
	defer n2.Close()

	discCh := make(chan string, 1)
	n1.SetOnPeerDisconnected(func(peerID string) {
		if peerID == "disc-cb-node-2" {
			select {
			case discCh <- peerID:
			default:
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := n1.Start(ctx); err != nil {
		t.Fatalf("node 1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("node 2 Start failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	conn1, err := n1.ConnectPeer(ctx, "disc-cb-node-2")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	_ = conn1.Close()

	select {
	case peerID := <-discCh:
		if peerID != "disc-cb-node-2" {
			t.Fatalf("expected disconnected peer 'disc-cb-node-2', got %q", peerID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for SetOnPeerDisconnected callback")
	}
}

type stubCodeExecutor struct{}

func (s *stubCodeExecutor) Execute(_ context.Context, req codeexec.Request) codeexec.Response {
	return codeexec.Response{
		JobID:    req.JobID,
		Language: req.Language,
		Stdout:   "dispatched:" + req.Stdin,
		ExitCode: 0,
	}
}

func TestNode_StartSignaling401ThenReconnect_TransportReadyAndDispatch(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir1 := filepath.Join(tmpDir, "node1_storage")
	storeDir2 := filepath.Join(tmpDir, "node2_storage")
	storeDir3 := filepath.Join(tmpDir, "node3_storage")

	const clusterSecret = "d1-test-cluster-secret"
	hub, err := signaling.NewHub("127.0.0.1:0", clusterSecret)
	if err != nil {
		t.Fatalf("failed to start auth-protected signaling hub: %v", err)
	}
	defer hub.Close()

	sigURL := hub.URL()

	// Boot worker n2 with valid token
	n2, err := NewNode(NodeConfig{
		NodeID:       "d1-worker-node",
		PoolID:       "d1-pool",
		StorageDir:   storeDir2,
		StorageQuota: 10 * 1024 * 1024,
		SignalingURL: sigURL,
		AuthToken:    clusterSecret,
	})
	if err != nil {
		t.Fatalf("failed to create worker node: %v", err)
	}
	defer n2.Close()
	n2.SetCodeExecutor(&stubCodeExecutor{}, "d1-caller-node")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := n2.Start(ctx); err != nil {
		t.Fatalf("worker Start failed: %v", err)
	}

	// Boot caller n1 WITHOUT token against 401-protected coordinator
	n1, err := NewNode(NodeConfig{
		NodeID:       "d1-caller-node",
		PoolID:       "d1-pool",
		StorageDir:   storeDir1,
		StorageQuota: 10 * 1024 * 1024,
		SignalingURL: sigURL,
	})
	if err != nil {
		t.Fatalf("failed to create caller node: %v", err)
	}
	defer n1.Close()

	startErr := n1.Start(ctx)
	if startErr == nil {
		t.Fatalf("expected Start without auth token to return signaling error against 401 hub")
	}

	// Transport must still be initialized despite initial signaling failure
	n1.mu.RLock()
	tmAfterStart := n1.transport
	startedAfterStart := n1.started
	n1.mu.RUnlock()
	if tmAfterStart == nil {
		t.Fatalf("expected n.transport != nil after Start() signaling failure")
	}
	if !startedAfterStart {
		t.Fatalf("expected n.started == true after Start() signaling failure")
	}

	// Provide valid token and reconnect signaling
	n1.SetAuthToken(clusterSecret)
	if err := n1.ReconnectSignaling(ctx, sigURL); err != nil {
		t.Fatalf("ReconnectSignaling with valid token failed: %v", err)
	}

	n1.mu.RLock()
	tmAfterReconn := n1.transport
	n1.mu.RUnlock()
	if tmAfterReconn == nil {
		t.Fatalf("expected n.transport != nil after ReconnectSignaling")
	}

	time.Sleep(150 * time.Millisecond)

	// Connect peer and dispatch a code job over WebRTC DataChannel
	conn, err := n1.ConnectPeer(ctx, "d1-worker-node")
	if err != nil {
		t.Fatalf("ConnectPeer after ReconnectSignaling failed: %v", err)
	}
	defer conn.Close()

	time.Sleep(200 * time.Millisecond)

	codeResp, err := n1.ExecuteRemoteCode(ctx, "d1-worker-node", codeexec.Request{
		JobID:          "d1-dispatch-job",
		Language:       "python",
		Source:         "print('ok')",
		Stdin:          "hello-d1",
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("ExecuteRemoteCode failed: %v", err)
	}
	if codeResp.ExitCode != 0 || codeResp.Stdout != "dispatched:hello-d1" {
		t.Fatalf("unexpected ExecuteRemoteCode response: %+v", codeResp)
	}

	// Also verify ReconnectSignaling lazily initializes n.transport if Start() was never called
	n3, err := NewNode(NodeConfig{
		NodeID:       "d1-lazy-node",
		PoolID:       "d1-pool",
		StorageDir:   storeDir3,
		StorageQuota: 10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create lazy node: %v", err)
	}
	defer n3.Close()
	n3.SetAuthToken(clusterSecret)
	if err := n3.ReconnectSignaling(ctx, sigURL); err != nil {
		t.Fatalf("lazy ReconnectSignaling failed: %v", err)
	}
	n3.mu.RLock()
	lazyTM := n3.transport
	n3.mu.RUnlock()
	if lazyTM == nil {
		t.Fatalf("expected ReconnectSignaling to lazily initialize n.transport when nil")
	}
}

func TestNode_FailedConnectPeerDoesNotFireOnPeerConnected(t *testing.T) {
	tmpDir := t.TempDir()
	n, err := NewNode(NodeConfig{
		NodeID:       "fail-cb-node",
		PoolID:       "pool-cb",
		StorageDir:   tmpDir,
		StorageQuota: 10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	var fired int32
	n.SetOnPeerConnected(func(peerID string) {
		atomic.AddInt32(&fired, 1)
	})

	if err := n.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Attempt ConnectPeer to a nonexistent peer with a short canceled/expired context
	connCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = n.ConnectPeer(connCtx, "nonexistent-peer")
	if err == nil {
		t.Fatalf("expected ConnectPeer to nonexistent peer to fail")
	}

	if got := atomic.LoadInt32(&fired); got != 0 {
		t.Fatalf("expected onPeerConnected NOT to fire on failed ConnectPeer, got %d", got)
	}
}

func TestNode_UnsolicitedChunkGuards(t *testing.T) {
	tmpDir := t.TempDir()
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	nSender, err := NewNode(NodeConfig{
		NodeID:       "unsol-sender",
		PoolID:       "pool-u",
		StorageDir:   filepath.Join(tmpDir, "sender"),
		StorageQuota: 10 * 1024 * 1024,
		SignalingURL: hub.URL(),
	})
	if err != nil {
		t.Fatalf("NewNode sender failed: %v", err)
	}
	defer nSender.Close()

	// Receiver 1: DisableUnsolicitedChunks = true
	nBlocked, err := NewNode(NodeConfig{
		NodeID:                   "unsol-blocked",
		PoolID:                   "pool-u",
		StorageDir:               filepath.Join(tmpDir, "blocked"),
		StorageQuota:             10 * 1024 * 1024,
		SignalingURL:             hub.URL(),
		DisableUnsolicitedChunks: true,
	})
	if err != nil {
		t.Fatalf("NewNode blocked failed: %v", err)
	}
	defer nBlocked.Close()

	// Receiver 2: Allow unsolicited up to 100 bytes/peer
	nCapped, err := NewNode(NodeConfig{
		NodeID:                          "unsol-capped",
		PoolID:                          "pool-u",
		StorageDir:                      filepath.Join(tmpDir, "capped"),
		StorageQuota:                    10 * 1024 * 1024,
		SignalingURL:                    hub.URL(),
		MaxUnsolicitedChunkBytesPerPeer: 100,
	})
	if err != nil {
		t.Fatalf("NewNode capped failed: %v", err)
	}
	defer nCapped.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := nSender.Start(ctx); err != nil {
		t.Fatalf("sender Start failed: %v", err)
	}
	if err := nBlocked.Start(ctx); err != nil {
		t.Fatalf("blocked Start failed: %v", err)
	}
	if err := nCapped.Start(ctx); err != nil {
		t.Fatalf("capped Start failed: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Test DisableUnsolicitedChunks: unsolicited push must NOT persist and returns ACK rejection error
	if _, err := nSender.ConnectPeer(ctx, "unsol-blocked"); err != nil {
		t.Fatalf("ConnectPeer to blocked failed: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	p1 := []byte("blocked unsolicited payload")
	h1 := sha256.Sum256(p1)
	cid1 := hex.EncodeToString(h1[:])
	_, _ = nSender.PutBlob(cid1, bytes.NewReader(p1))
	if err := nSender.TransferChunk(ctx, "unsol-blocked", cid1); err == nil {
		t.Fatalf("expected TransferChunk to blocked node to return rejection error")
	}
	time.Sleep(100 * time.Millisecond)
	if nBlocked.HasBlob(cid1) {
		t.Fatalf("expected DisableUnsolicitedChunks=true to reject unsolicited chunk write")
	}

	// Test MaxUnsolicitedChunkBytesPerPeer = 100: 60-byte chunk accepted, second 60-byte chunk rejected
	if _, err := nSender.ConnectPeer(ctx, "unsol-capped"); err != nil {
		t.Fatalf("ConnectPeer to capped failed: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	pSmall := bytes.Repeat([]byte("a"), 60)
	hSmall := sha256.Sum256(pSmall)
	cidSmall := hex.EncodeToString(hSmall[:])
	_, _ = nSender.PutBlob(cidSmall, bytes.NewReader(pSmall))
	if err := nSender.TransferChunk(ctx, "unsol-capped", cidSmall); err != nil {
		t.Fatalf("TransferChunk small failed: %v", err)
	}
	if !nCapped.HasBlob(cidSmall) {
		t.Fatalf("expected first 60-byte chunk under 100-byte cap to be stored")
	}

	pOver := bytes.Repeat([]byte("b"), 60)
	hOver := sha256.Sum256(pOver)
	cidOver := hex.EncodeToString(hOver[:])
	_, _ = nSender.PutBlob(cidOver, bytes.NewReader(pOver))
	if err := nSender.TransferChunk(ctx, "unsol-capped", cidOver); err == nil {
		t.Fatalf("expected TransferChunk exceeding cap to return rejection error")
	}
	time.Sleep(100 * time.Millisecond)
	if nCapped.HasBlob(cidOver) {
		t.Fatalf("expected second 60-byte chunk exceeding 100-byte cap to be rejected")
	}
}

func TestNode_GetFleetNodesReconcilesOfflinePeers(t *testing.T) {
	tmpDir := t.TempDir()
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	n1, err := NewNode(NodeConfig{
		NodeID:       "fleet-n1",
		PoolID:       "pool-f",
		StorageDir:   filepath.Join(tmpDir, "n1"),
		StorageQuota: 10 * 1024 * 1024,
		SignalingURL: hub.URL(),
	})
	if err != nil {
		t.Fatalf("NewNode n1 failed: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:       "fleet-n2",
		PoolID:       "pool-f",
		StorageDir:   filepath.Join(tmpDir, "n2"),
		StorageQuota: 10 * 1024 * 1024,
		SignalingURL: hub.URL(),
	})
	if err != nil {
		t.Fatalf("NewNode n2 failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n1.Start(ctx); err != nil {
		t.Fatalf("n1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("n2 Start failed: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	nodes, err := n1.GetFleetNodes(ctx)
	if err != nil {
		t.Fatalf("GetFleetNodes failed: %v", err)
	}
	if len(nodes) != 1 || nodes[0] != "fleet-n2" {
		t.Fatalf("expected [fleet-n2], got %v", nodes)
	}

	// Close n2 and verify GetFleetNodes reconciles and removes fleet-n2 even if event missed
	_ = n2.Close()
	time.Sleep(150 * time.Millisecond)
	nodesAfter, err := n1.GetFleetNodes(ctx)
	if err != nil {
		t.Fatalf("GetFleetNodes after close failed: %v", err)
	}
	if len(nodesAfter) != 0 {
		t.Fatalf("expected empty fleet after n2 closed, got %v", nodesAfter)
	}
}

func TestNode_ChunkAckFailurePropagation(t *testing.T) {
	tmpDir := t.TempDir()
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	nSender, err := NewNode(NodeConfig{
		NodeID:       "ack-fail-sender",
		PoolID:       "pool-ack",
		StorageDir:   filepath.Join(tmpDir, "sender"),
		StorageQuota: 10 * 1024 * 1024,
		SignalingURL: hub.URL(),
	})
	if err != nil {
		t.Fatalf("NewNode sender failed: %v", err)
	}
	defer nSender.Close()

	// Receiver has tiny 10-byte quota so Put fails with ErrQuotaExceeded
	nRecv, err := NewNode(NodeConfig{
		NodeID:       "ack-fail-recv",
		PoolID:       "pool-ack",
		StorageDir:   filepath.Join(tmpDir, "recv"),
		StorageQuota: 10,
		SignalingURL: hub.URL(),
	})
	if err != nil {
		t.Fatalf("NewNode recv failed: %v", err)
	}
	defer nRecv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := nSender.Start(ctx); err != nil {
		t.Fatalf("sender Start failed: %v", err)
	}
	if err := nRecv.Start(ctx); err != nil {
		t.Fatalf("recv Start failed: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	if _, err := nSender.ConnectPeer(ctx, "ack-fail-recv"); err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	payload := bytes.Repeat([]byte("X"), 64)
	h := sha256.Sum256(payload)
	cid := hex.EncodeToString(h[:])
	if _, err := nSender.PutBlob(cid, bytes.NewReader(payload)); err != nil {
		t.Fatalf("PutBlob failed: %v", err)
	}

	err = nSender.TransferChunk(ctx, "ack-fail-recv", cid)
	if err == nil {
		t.Fatalf("expected TransferChunk to fail when receiver blobstore.Put fails with quota exceeded")
	}
	if !strings.Contains(err.Error(), "quota") {
		t.Fatalf("expected quota error propagated via DataMsgChunkError, got: %v", err)
	}
}

func TestNode_CGNATTelemetryAndAutoRTT(t *testing.T) {
	tmpDir := t.TempDir()
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	n1, err := NewNode(NodeConfig{
		NodeID:       "cgnat-n1",
		PoolID:       "pool-cgnat",
		StorageDir:   filepath.Join(tmpDir, "n1"),
		StorageQuota: 10 * 1024 * 1024,
		SignalingURL: hub.URL(),
	})
	if err != nil {
		t.Fatalf("NewNode n1 failed: %v", err)
	}
	defer n1.Close()

	n2, err := NewNode(NodeConfig{
		NodeID:       "cgnat-n2",
		PoolID:       "pool-cgnat",
		StorageDir:   filepath.Join(tmpDir, "n2"),
		StorageQuota: 10 * 1024 * 1024,
		SignalingURL: hub.URL(),
	})
	if err != nil {
		t.Fatalf("NewNode n2 failed: %v", err)
	}
	defer n2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := n1.Start(ctx); err != nil {
		t.Fatalf("n1 Start failed: %v", err)
	}
	if err := n2.Start(ctx); err != nil {
		t.Fatalf("n2 Start failed: %v", err)
	}

	// Simulate n1 behind CGNAT before establishing P2P connection
	n1.mu.Lock()
	n1.natReport.IsBehindCGNAT = true
	n1.transport.SetLocalCGNAT(true)
	n1.mu.Unlock()

	time.Sleep(150 * time.Millisecond)

	if _, err := n1.ConnectPeer(ctx, "cgnat-n2"); err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}

	// Verify both n1 and n2 record IsBehindCGNAT = true and FleetStats().CGNATPrevalencePct > 0
	m1, ok1 := n1.GetPathMetrics("cgnat-n2")
	if !ok1 || !m1.IsBehindCGNAT {
		t.Fatalf("expected n1 PathMetrics.IsBehindCGNAT=true, got ok=%v metrics=%+v", ok1, m1)
	}
	stats1 := n1.FleetStats()
	if stats1.CGNATPrevalencePct <= 0 {
		t.Fatalf("expected n1 FleetStats.CGNATPrevalencePct > 0, got %.2f%%", stats1.CGNATPrevalencePct)
	}

	m2, ok2 := n2.GetPathMetrics("cgnat-n1")
	if !ok2 || !m2.IsBehindCGNAT {
		t.Fatalf("expected n2 PathMetrics.IsBehindCGNAT=true from peer SDP propagation, got ok=%v metrics=%+v", ok2, m2)
	}
	stats2 := n2.FleetStats()
	if stats2.CGNATPrevalencePct <= 0 {
		t.Fatalf("expected n2 FleetStats.CGNATPrevalencePct > 0, got %.2f%%", stats2.CGNATPrevalencePct)
	}

	// Verify PathMetrics.RTT is automatically populated from Pion PC.GetStats() without manual __PING__
	deadline := time.Now().Add(4 * time.Second)
	var rtt time.Duration
	for time.Now().Before(deadline) {
		if m, ok := n1.GetPathMetrics("cgnat-n2"); ok && m.RTT > 0 {
			rtt = m.RTT
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if rtt <= 0 {
		t.Fatalf("expected PathMetrics.RTT > 0 automatically populated from PC.GetStats(), got %v", rtt)
	}
}

func TestStubNode_PutBlobAlignment(t *testing.T) {
	stub := NewStubNode("stub-align", 1024)

	// Invalid IDs rejected
	for _, bad := range []string{"../escape", "bad id", "", strings.Repeat("x", 129)} {
		if _, err := stub.PutBlob(bad, bytes.NewReader([]byte("abc"))); !errors.Is(err, blobstore.ErrInvalidID) {
			t.Errorf("PutBlob(%q) expected ErrInvalidID, got %v", bad, err)
		}
	}

	// Non-hex 64-char ID accepted without checksum mismatch
	nonHex64 := strings.Repeat("z", 64)
	if _, err := stub.PutBlob(nonHex64, bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("PutBlob non-hex 64-char ID failed: %v", err)
	}

	// Uppercase 64-char SHA-256 hex ID accepted and deduplicated without double-counting quota
	payload := []byte("uppercase stub payload")
	h := sha256.Sum256(payload)
	upperCID := strings.ToUpper(hex.EncodeToString(h[:]))
	if _, err := stub.PutBlob(upperCID, bytes.NewReader(payload)); err != nil {
		t.Fatalf("PutBlob uppercase SHA-256 failed: %v", err)
	}
	usedBefore := stub.StorageUsage().UsedBytes
	if _, err := stub.PutBlob(upperCID, bytes.NewReader(payload)); err != nil {
		t.Fatalf("duplicate PutBlob failed: %v", err)
	}
	if stub.StorageUsage().UsedBytes != usedBefore {
		t.Fatalf("duplicate PutBlob double-counted UsedBytes: before=%d after=%d", usedBefore, stub.StorageUsage().UsedBytes)
	}
}


