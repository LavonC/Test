package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/unicompute/obj1/pkg/instrumentation"
	"github.com/unicompute/obj1/pkg/protocol"
	"github.com/unicompute/obj1/pkg/signaling"
)

func TestTransport_WebRTCConnectionAndChunkTransfer(t *testing.T) {
	// Start signaling hub
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	wsURL := hub.URL()

	trackerA := instrumentation.NewTracker()
	trackerB := instrumentation.NewTracker()

	var mgrAVal, mgrBVal atomic.Pointer[Manager]

	// Signaling client for Node A
	sigA, err := signaling.NewClient(wsURL, "node-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mgrAVal.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("failed to start sigA: %v", err)
	}
	defer sigA.Close()

	// Signaling client for Node B
	sigB, err := signaling.NewClient(wsURL, "node-B", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mgrBVal.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("failed to start sigB: %v", err)
	}
	defer sigB.Close()

	mgrA, err := NewManager(Config{
		NodeID: "node-A",
	}, sigA, trackerA)
	if err != nil {
		t.Fatalf("failed to create mgrA: %v", err)
	}
	defer mgrA.Close()
	mgrAVal.Store(mgrA)

	mgrB, err := NewManager(Config{
		NodeID: "node-B",
	}, sigB, trackerB)
	if err != nil {
		t.Fatalf("failed to create mgrB: %v", err)
	}
	defer mgrB.Close()
	mgrBVal.Store(mgrB)

	// Capture incoming connection on Node B
	connBChan := make(chan *PeerConnection, 1)
	mgrB.SetOnNewConnection(func(pc *PeerConnection) {
		connBChan <- pc
	})

	time.Sleep(100 * time.Millisecond) // Allow registrations to complete

	// Node A connects to Node B
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, err := mgrA.ConnectPeer(ctx, "node-B")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer connA.Close()

	var connB *PeerConnection
	select {
	case connB = <-connBChan:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for incoming connection on Node B")
	}
	defer connB.Close()

	if err := connB.WaitConnected(ctx); err != nil {
		t.Fatalf("connB failed to reach connected: %v", err)
	}
	if err := connB.WaitChannelsReady(ctx); err != nil {
		t.Fatalf("connB channels not ready: %v", err)
	}

	// 1. Test Stream Channel
	streamMsg := []byte("stream log from A to B")
	receivedStreamCh := make(chan []byte, 1)
	connB.SetOnStreamMessage(func(msg []byte) {
		receivedStreamCh <- msg
	})

	if err := connA.SendStreamMessage(streamMsg); err != nil {
		t.Fatalf("SendStreamMessage failed: %v", err)
	}

	select {
	case got := <-receivedStreamCh:
		if !bytes.Equal(got, streamMsg) {
			t.Errorf("stream message mismatch: got %q, want %q", string(got), string(streamMsg))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for stream message")
	}

	// 2. Test Chunk Channel (Bulk Data Transfer)
	chunkData := bytes.Repeat([]byte("UniCompute chunk payload verification test bytes! "), 1000) // ~50 KB
	chunkHash := sha256.Sum256(chunkData)
	chunkID := hex.EncodeToString(chunkHash[:])

	var wg sync.WaitGroup
	var receivedData []byte
	var recvErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		recvCtx, recvCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer recvCancel()
		receivedData, recvErr = connB.ReceiveChunk(recvCtx, chunkID, int64(len(chunkData)))
	}()

	time.Sleep(50 * time.Millisecond)

	sendCtx, sendCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sendCancel()
	err = connA.SendChunk(sendCtx, chunkID, bytes.NewReader(chunkData), int64(len(chunkData)))
	if err != nil {
		t.Fatalf("SendChunk failed: %v", err)
	}

	wg.Wait()

	if recvErr != nil {
		t.Fatalf("ReceiveChunk failed: %v", recvErr)
	}
	if !bytes.Equal(receivedData, chunkData) {
		t.Fatalf("received chunk payload mismatch")
	}

	// 3. Test Path Instrumentation
	time.Sleep(50 * time.Millisecond)
	mA, okA := trackerA.GetMetrics("node-B")
	if !okA {
		t.Fatalf("expected metrics for node-B on trackerA")
	}
	if mA.BytesSent <= 0 {
		t.Errorf("expected BytesSent > 0, got %d", mA.BytesSent)
	}
}

func TestUnnormalizedSTUN_ThrowsSchemeError(t *testing.T) {
	api := webrtc.NewAPI()
	// Raw unnormalized URL without scheme reproduces pion's "InvalidAccessError: unknown scheme type"
	rawCfg := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun.l.google.com:19302"}},
		},
	}
	_, err := api.NewPeerConnection(rawCfg)
	if err == nil {
		t.Fatalf("expected NewPeerConnection to fail with raw host:port, but it succeeded")
	}
	if !strings.Contains(err.Error(), "unknown scheme type") {
		t.Errorf("expected 'unknown scheme type' error, got %v", err)
	}
}

func TestBuildICEServers_AndPeerConnectionCreation(t *testing.T) {
	testCases := []struct {
		name        string
		stunServers []string
		turnServers []TURNServerConfig
		wantSTUN    []string
		wantTURN    []string
	}{
		{
			name:        "Bare host and port STUN",
			stunServers: []string{"stun.l.google.com:19302"},
			wantSTUN:    []string{"stun:stun.l.google.com:19302"},
		},
		{
			name:        "Prefixed stun: host and port",
			stunServers: []string{"stun:stun.l.google.com:19302"},
			wantSTUN:    []string{"stun:stun.l.google.com:19302"},
		},
		{
			name:        "Double slash stun:// syntax",
			stunServers: []string{"stun://stun.l.google.com:19302"},
			wantSTUN:    []string{"stun:stun.l.google.com:19302"},
		},
		{
			name:        "Uppercase STUN: scheme",
			stunServers: []string{"STUN:stun.l.google.com:19302"},
			wantSTUN:    []string{"stun:stun.l.google.com:19302"},
		},
		{
			name:        "STUNS secure scheme",
			stunServers: []string{"stuns:stun.l.google.com:5349"},
			wantSTUN:    []string{"stuns:stun.l.google.com:5349"},
		},
		{
			name:        "Double slash stuns:// syntax",
			stunServers: []string{"stuns://stun.l.google.com:5349"},
			wantSTUN:    []string{"stuns:stun.l.google.com:5349"},
		},
		{
			name:        "Comma-separated STUN entries",
			stunServers: []string{"stun.l.google.com:19302, stun1.l.google.com:19302"},
			wantSTUN:    []string{"stun:stun.l.google.com:19302", "stun:stun1.l.google.com:19302"},
		},
		{
			name: "Bare TURN URL",
			turnServers: []TURNServerConfig{
				{URL: "relay.unicompute.org:3478", Username: "user", Credential: "pwd"},
			},
			wantTURN: []string{"turn:relay.unicompute.org:3478"},
		},
		{
			name: "Double slash turn:// URL",
			turnServers: []TURNServerConfig{
				{URL: "turn://relay.unicompute.org:3478", Username: "user", Credential: "pwd"},
			},
			wantTURN: []string{"turn:relay.unicompute.org:3478"},
		},
		{
			name: "TURNS secure scheme",
			turnServers: []TURNServerConfig{
				{URL: "turns://relay.unicompute.org:5349", Username: "user", Credential: "pwd"},
			},
			wantTURN: []string{"turns:relay.unicompute.org:5349"},
		},
		{
			name: "Whitespace after stun prefix",
			stunServers: []string{"stun:   stun.l.google.com:19302"},
			wantSTUN: []string{"stun:stun.l.google.com:19302"},
		},
		{
			name: "Comma-separated TURN URLs",
			turnServers: []TURNServerConfig{
				{URL: "turn:relay1.unicompute.org:3478, turn:relay2.unicompute.org:3478", Username: "user", Credential: "pwd"},
			},
			wantTURN: []string{"turn:relay1.unicompute.org:3478", "turn:relay2.unicompute.org:3478"},
		},
		{
			name: "Empty and malformed STUN strings",
			stunServers: []string{"", "   ", "stun:", "stun://", " ,  "},
			wantSTUN: nil,
		},
		{
			name: "IPv6 STUN with brackets and port",
			stunServers: []string{"[::1]:19302"},
			wantSTUN: []string{"stun:[::1]:19302"},
		},
		{
			name: "IPv6 STUN with brackets without port",
			stunServers: []string{"[::1]"},
			wantSTUN: []string{"stun:[::1]"},
		},
		{
			name: "STUN without port",
			stunServers: []string{"stun.l.google.com"},
			wantSTUN: []string{"stun:stun.l.google.com"},
		},
		{
			name: "IPv6 bare without brackets with port",
			stunServers: []string{"2001:db8::1:19302"},
			wantSTUN: []string{"stun:[2001:db8::1]:19302"},
		},
		{
			name: "IPv6 bare without brackets without port",
			stunServers: []string{"2001:db8::1"},
			wantSTUN: []string{"stun:[2001:db8::1]"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, err := NewManager(Config{
				NodeID:      "test-node",
				STUNServers: tc.stunServers,
				TURNServers: tc.turnServers,
			}, nil, nil)
			if err != nil {
				t.Fatalf("NewManager failed: %v", err)
			}
			defer mgr.Close()

			iceServers := mgr.buildICEServers()

			// Verify that Pion NewPeerConnection succeeds without 'unknown scheme type' error
			webrtcCfg := webrtc.Configuration{
				ICEServers: iceServers,
			}
			pc, err := mgr.api.NewPeerConnection(webrtcCfg)
			if err != nil {
				t.Fatalf("NewPeerConnection failed with normalized ICEServers: %v", err)
			}
			_ = pc.Close()

			if len(tc.wantSTUN) > 0 {
				if len(iceServers) == 0 {
					t.Fatalf("expected at least 1 ICEServer for STUN")
				}
				if len(iceServers[0].URLs) != len(tc.wantSTUN) {
					t.Errorf("got %d STUN URLs, want %d", len(iceServers[0].URLs), len(tc.wantSTUN))
				}
				for i, u := range tc.wantSTUN {
					if i < len(iceServers[0].URLs) && iceServers[0].URLs[i] != u {
						t.Errorf("STUN URL[%d] = %q, want %q", i, iceServers[0].URLs[i], u)
					}
				}
			}

			if len(tc.wantTURN) > 0 {
				turnIdx := 0
				if len(tc.wantSTUN) > 0 {
					turnIdx = 1
				}
				if len(iceServers) <= turnIdx {
					t.Fatalf("expected ICEServer for TURN at index %d", turnIdx)
				}
				if len(iceServers[turnIdx].URLs) != len(tc.wantTURN) {
					t.Errorf("got %d TURN URLs, want %d", len(iceServers[turnIdx].URLs), len(tc.wantTURN))
				}
				for i, u := range tc.wantTURN {
					if i < len(iceServers[turnIdx].URLs) && iceServers[turnIdx].URLs[i] != u {
						t.Errorf("TURN URL[%d] = %q, want %q", i, iceServers[turnIdx].URLs[i], u)
					}
				}
			}
		})
	}
}

func TestPeerSessionEvictionOnClose(t *testing.T) {
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	wsURL := hub.URL()

	var mgrAVal, mgrBVal atomic.Pointer[Manager]

	sigA, err := signaling.NewClient(wsURL, "node-evict-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mgrAVal.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("failed to start sigA: %v", err)
	}
	defer sigA.Close()

	sigB, err := signaling.NewClient(wsURL, "node-evict-B", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mgrBVal.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("failed to start sigB: %v", err)
	}
	defer sigB.Close()

	mgrA, err := NewManager(Config{NodeID: "node-evict-A"}, sigA, nil)
	if err != nil {
		t.Fatalf("failed to create mgrA: %v", err)
	}
	defer mgrA.Close()
	mgrAVal.Store(mgrA)

	mgrB, err := NewManager(Config{NodeID: "node-evict-B"}, sigB, nil)
	if err != nil {
		t.Fatalf("failed to create mgrB: %v", err)
	}
	defer mgrB.Close()
	mgrBVal.Store(mgrB)

	connBChan := make(chan *PeerConnection, 1)
	mgrB.SetOnNewConnection(func(pc *PeerConnection) {
		connBChan <- pc
	})

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, err := mgrA.ConnectPeer(ctx, "node-evict-B")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer connA.Close()

	var connB *PeerConnection
	select {
	case connB = <-connBChan:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for incoming connection on Node B")
	}

	if err := connB.WaitConnected(ctx); err != nil {
		t.Fatalf("connB failed to connect: %v", err)
	}
	if err := connB.WaitChannelsReady(ctx); err != nil {
		t.Fatalf("connB channels not ready: %v", err)
	}

	// Verify active connection exists
	if _, ok := mgrA.GetConnection("node-evict-B"); !ok {
		t.Fatalf("expected mgrA to have connection to node-evict-B")
	}
	peers := mgrA.ActivePeers()
	if len(peers) == 0 || peers[0] != "node-evict-B" {
		t.Fatalf("expected node-evict-B in ActivePeers, got %v", peers)
	}

	// Close remote peer connB and verify eviction from mgrA
	_ = connB.Close()

	deadline := time.Now().Add(10 * time.Second)
	evicted := false
	for time.Now().Before(deadline) {
		_, ok := mgrA.GetConnection("node-evict-B")
		if !ok && len(mgrA.ActivePeers()) == 0 {
			evicted = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !evicted {
		t.Errorf("expected peer session to be evicted from mgrA after connection closed")
	}
	_ = connB.Close()
}

func TestZeroByteChunkTransfer(t *testing.T) {
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start signaling hub: %v", err)
	}
	defer hub.Close()

	wsURL := hub.URL()

	var mgrAVal, mgrBVal atomic.Pointer[Manager]

	sigA, err := signaling.NewClient(wsURL, "node-chunk-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mgrAVal.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("failed to start sigA: %v", err)
	}
	defer sigA.Close()

	sigB, err := signaling.NewClient(wsURL, "node-chunk-B", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mgrBVal.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("failed to start sigB: %v", err)
	}
	defer sigB.Close()

	mgrA, err := NewManager(Config{NodeID: "node-chunk-A"}, sigA, nil)
	if err != nil {
		t.Fatalf("failed to create mgrA: %v", err)
	}
	defer mgrA.Close()
	mgrAVal.Store(mgrA)

	mgrB, err := NewManager(Config{NodeID: "node-chunk-B"}, sigB, nil)
	if err != nil {
		t.Fatalf("failed to create mgrB: %v", err)
	}
	defer mgrB.Close()
	mgrBVal.Store(mgrB)

	connBChan := make(chan *PeerConnection, 1)
	mgrB.SetOnNewConnection(func(pc *PeerConnection) {
		connBChan <- pc
	})

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, err := mgrA.ConnectPeer(ctx, "node-chunk-B")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer connA.Close()

	var connB *PeerConnection
	select {
	case connB = <-connBChan:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for incoming connection on Node B")
	}
	defer connB.Close()

	if err := connB.WaitConnected(ctx); err != nil {
		t.Fatalf("connB failed to connect: %v", err)
	}
	if err := connB.WaitChannelsReady(ctx); err != nil {
		t.Fatalf("connB channels not ready: %v", err)
	}

	zeroChunkID := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	connB.SetOnChunkRequest(func(chunkID string) {
		if chunkID == zeroChunkID {
			_ = connB.SendChunk(ctx, chunkID, bytes.NewReader([]byte{}), 0)
		}
	})

	data, err := connA.FetchChunk(ctx, zeroChunkID, 0)
	if err != nil {
		t.Fatalf("FetchChunk failed for 0-byte chunk: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("expected 0 bytes, got %d bytes", len(data))
	}
}

func buildTestChunkFrame(flags byte, offset, totalSize uint64, chunkID string, payload []byte) []byte {
	frame := make([]byte, protocol.ChunkHeaderSize+len(payload))
	frame[0] = protocol.DataMsgChunkResponse
	frame[1] = flags
	binary.BigEndian.PutUint64(frame[2:10], offset)
	binary.BigEndian.PutUint64(frame[10:18], totalSize)
	idBytes := make([]byte, 64)
	copy(idBytes, []byte(chunkID))
	copy(frame[18:82], idBytes)
	copy(frame[protocol.ChunkHeaderSize:], payload)
	return frame
}

func TestHandleChunkMessage_SecurityGuards(t *testing.T) {
	conn := newPeerConnection("sess-sec", "local", "remote", nil, nil)

	// (a) offset = math.MaxUint64 must not panic with integer overflow
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("handleChunkMessage panicked on math.MaxUint64 offset: %v", r)
			}
		}()
		frameStart := buildTestChunkFrame(protocol.FlagStart, 0, 128, "chunk-overflow", []byte("abcd"))
		conn.handleChunkMessage(frameStart)

		frameOverflow := buildTestChunkFrame(protocol.FlagData, math.MaxUint64, 128, "chunk-overflow", []byte("evil"))
		conn.handleChunkMessage(frameOverflow)
	}()

	// Clean up chunk-overflow entry before testing capacity caps
	conn.cleanupInFlight(ErrConnectionClosed)

	// (b) 20 simultaneous FlagStart frames claiming 64 MiB each must reject beyond cap
	for i := 0; i < 20; i++ {
		cid := fmt.Sprintf("chunk-oom-%d", i)
		frame := buildTestChunkFrame(protocol.FlagStart, 0, 64*1024*1024, cid, []byte("x"))
		conn.handleChunkMessage(frame)
	}

	conn.inFlightMu.Lock()
	inFlightCount := len(conn.inFlight)
	inFlightBytes := conn.totalInFlightBytes
	conn.inFlightMu.Unlock()

	if inFlightCount > MaxInFlightChunksPerPeer {
		t.Fatalf("expected at most %d in-flight chunks, got %d", MaxInFlightChunksPerPeer, inFlightCount)
	}
	if inFlightBytes > MaxInFlightBytesPerPeer {
		t.Fatalf("expected total in-flight bytes <= %d, got %d", MaxInFlightBytesPerPeer, inFlightBytes)
	}
	if inFlightCount != 1 {
		t.Fatalf("expected exactly 1 64MiB chunk accepted under 64MiB peer cap, got %d", inFlightCount)
	}

	conn.cleanupInFlight(ErrConnectionClosed)

	// Also verify count cap (MaxInFlightChunksPerPeer = 8) with small chunks
	for i := 0; i < 20; i++ {
		cid := fmt.Sprintf("chunk-count-%d", i)
		frame := buildTestChunkFrame(protocol.FlagStart, 0, 1024, cid, []byte("x"))
		conn.handleChunkMessage(frame)
	}
	conn.inFlightMu.Lock()
	countOnly := len(conn.inFlight)
	conn.inFlightMu.Unlock()
	if countOnly != MaxInFlightChunksPerPeer {
		t.Fatalf("expected %d in-flight chunks at count cap, got %d", MaxInFlightChunksPerPeer, countOnly)
	}

	conn.cleanupInFlight(ErrConnectionClosed)

	// (c) Duplicate frames at offset = 0 must not trigger premature completion with zeroed holes
	var completedCount int
	var completedData []byte
	conn.SetOnChunkReceived(func(chunkID string, data []byte) {
		completedCount++
		completedData = append([]byte(nil), data...)
	})

	const chunkID = "chunk-dup-0"
	const totalSize = 10
	part1 := []byte("12345")
	conn.handleChunkMessage(buildTestChunkFrame(protocol.FlagStart, 0, totalSize, chunkID, part1))
	// Send duplicate frame at offset=0 (same length 5, which previously bumped received to 10 == totalSize)
	conn.handleChunkMessage(buildTestChunkFrame(protocol.FlagData, 0, totalSize, chunkID, part1))
	// Even with FlagEnd set on duplicate offset=0, it must be rejected
	conn.handleChunkMessage(buildTestChunkFrame(protocol.FlagEnd, 0, totalSize, chunkID, part1))

	if completedCount != 0 {
		t.Fatalf("duplicate frames at offset=0 triggered premature completion (%d times)", completedCount)
	}

	// Now send legitimate second half at offset=5
	part2 := []byte("67890")
	conn.handleChunkMessage(buildTestChunkFrame(protocol.FlagEnd, 5, totalSize, chunkID, part2))
	if completedCount != 1 {
		t.Fatalf("expected completion count 1 after sequential second half, got %d", completedCount)
	}
	if string(completedData) != "1234567890" {
		t.Fatalf("expected completed data %q, got %q", "1234567890", string(completedData))
	}
}

func TestManager_BuffersEarlyICECandidatesBeforeAnswer(t *testing.T) {
	mgrA, err := NewManager(Config{NodeID: "node-early-A"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager A failed: %v", err)
	}
	defer mgrA.Close()

	mgrB, err := NewManager(Config{NodeID: "node-early-B"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager B failed: %v", err)
	}
	defer mgrB.Close()

	pcA, err := mgrA.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection A failed: %v", err)
	}
	_, err = pcA.CreateDataChannel("stream", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel failed: %v", err)
	}
	offer, err := pcA.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer failed: %v", err)
	}
	if err := pcA.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription A failed: %v", err)
	}

	const sessionID = "sess-early-ice"
	connA := newPeerConnection(sessionID, "node-early-A", "node-early-B", pcA, nil)
	connA.setupHandlers()
	mgrA.mu.Lock()
	mgrA.connections[sessionID] = connA
	mgrA.peerSessions["node-early-B"] = sessionID
	mgrA.mu.Unlock()

	// Deliver an ICE candidate BEFORE MsgSDPAnswer arrives on mgrA
	candPayload, _ := json.Marshal(protocol.ICECandidatePayload{
		SessionID:     sessionID,
		Candidate:     "candidate:1 1 udp 2122260223 127.0.0.1 50000 typ host",
		SDPMid:        "0",
		SDPMLineIndex: 0,
	})
	if err := mgrA.HandleSignalingEnvelope(protocol.Envelope{
		Type:     protocol.MsgICECandidate,
		SenderID: "node-early-B",
		TargetID: "node-early-A",
		Payload:  candPayload,
	}); err != nil {
		t.Fatalf("expected early ICE candidate to be buffered without error, got: %v", err)
	}

	mgrA.mu.Lock()
	queued := len(mgrA.pendingCandidates[sessionID])
	mgrA.mu.Unlock()
	if queued != 1 {
		t.Fatalf("expected 1 queued candidate before answer, got %d", queued)
	}

	// Now create a real answer on pcB and deliver MsgSDPAnswer to mgrA
	pcB, err := mgrB.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection B failed: %v", err)
	}
	defer pcB.Close()
	if err := pcB.SetRemoteDescription(offer); err != nil {
		t.Fatalf("SetRemoteDescription B failed: %v", err)
	}
	answer, err := pcB.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("CreateAnswer failed: %v", err)
	}

	ansPayload, _ := json.Marshal(protocol.SDPPayload{
		SessionID: sessionID,
		Type:      answer.Type.String(),
		SDP:       answer.SDP,
	})
	if err := mgrA.HandleSignalingEnvelope(protocol.Envelope{
		Type:     protocol.MsgSDPAnswer,
		SenderID: "node-early-B",
		TargetID: "node-early-A",
		Payload:  ansPayload,
	}); err != nil {
		t.Fatalf("HandleSignalingEnvelope(MsgSDPAnswer) failed flushing buffered ICE candidate: %v", err)
	}

	mgrA.mu.Lock()
	remaining := len(mgrA.pendingCandidates[sessionID])
	descSet := mgrA.remoteDescSet[sessionID]
	mgrA.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected 0 remaining queued candidates after answer, got %d", remaining)
	}
	if !descSet {
		t.Fatalf("expected remoteDescSet[sessionID] to be true after answer")
	}
}
