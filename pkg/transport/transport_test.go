package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/unicompute/obj1/pkg/instrumentation"
	"github.com/unicompute/obj1/pkg/protocol"
	"github.com/unicompute/obj1/pkg/relay"
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
	idBytes := []byte(chunkID)
	headerLen := protocol.ChunkHeaderFixedSize + len(idBytes)
	frame := make([]byte, headerLen+len(payload))
	frame[0] = protocol.DataMsgChunkResponse
	frame[1] = flags
	binary.BigEndian.PutUint64(frame[2:10], offset)
	binary.BigEndian.PutUint64(frame[10:18], totalSize)
	frame[18] = byte(len(idBytes))
	copy(frame[protocol.ChunkHeaderFixedSize:headerLen], idBytes)
	copy(frame[headerLen:], payload)
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

	ansSigned, err := mgrB.signSDPPayload(sessionID, answer.Type.String(), answer.SDP)
	if err != nil {
		t.Fatalf("signSDPPayload failed: %v", err)
	}
	ansPayload, _ := json.Marshal(ansSigned)
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

func TestHandleChunkMessage_LongChunkID(t *testing.T) {
	conn := newPeerConnection("sess-long-id", "local", "remote", nil, nil)
	defer conn.Close()

	longID := strings.Repeat("a1b2c3d4", 16) // 128 bytes
	if len(longID) != 128 {
		t.Fatalf("expected 128-byte ID, got %d", len(longID))
	}

	var gotID string
	var gotData []byte
	conn.SetOnChunkReceived(func(chunkID string, data []byte) {
		gotID = chunkID
		gotData = append([]byte(nil), data...)
	})

	payload := []byte("128-byte chunk ID test payload")
	frame := buildTestChunkFrame(protocol.FlagStart|protocol.FlagEnd, 0, uint64(len(payload)), longID, payload)
	conn.handleChunkMessage(frame)

	if gotID != longID {
		t.Fatalf("chunkID truncated or altered: got len=%d %q, want len=%d %q", len(gotID), gotID, len(longID), longID)
	}
	if !bytes.Equal(gotData, payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestManager_DTLSFingerprintVerification(t *testing.T) {
	mgrA, err := NewManager(Config{NodeID: "dtls-A"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager A failed: %v", err)
	}
	defer mgrA.Close()

	mgrB, err := NewManager(Config{NodeID: "dtls-B"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager B failed: %v", err)
	}
	defer mgrB.Close()

	pcA, err := mgrA.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection A failed: %v", err)
	}
	defer pcA.Close()
	_, _ = pcA.CreateDataChannel("stream", nil)
	offer, err := pcA.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer failed: %v", err)
	}

	// 1. Unsigned offer must be rejected
	unsignedPayload, _ := json.Marshal(protocol.SDPPayload{
		SessionID: "sess-unsigned",
		Type:      "offer",
		SDP:       offer.SDP,
	})
	if err := mgrB.HandleSignalingEnvelope(protocol.Envelope{
		Type:     protocol.MsgSDPOffer,
		SenderID: "dtls-A",
		TargetID: "dtls-B",
		Payload:  unsignedPayload,
	}); err == nil {
		t.Fatalf("expected unsigned SDP offer to be rejected")
	}

	// 2. Tampered DTLS fingerprint with original signature must be rejected
	signedOffer, err := mgrA.signSDPPayload("sess-tamper", "offer", offer.SDP)
	if err != nil {
		t.Fatalf("signSDPPayload failed: %v", err)
	}
	origFP := extractDTLSFingerprints(offer.SDP)
	tamperedSDP := strings.Replace(offer.SDP, origFP, "sha-256 FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF:FF", 1)
	tamperedPayload, _ := json.Marshal(protocol.SDPPayload{
		SessionID: "sess-tamper",
		Type:      "offer",
		SDP:       tamperedSDP,
		PublicKey: signedOffer.PublicKey,
		Signature: signedOffer.Signature,
	})
	if err := mgrB.HandleSignalingEnvelope(protocol.Envelope{
		Type:     protocol.MsgSDPOffer,
		SenderID: "dtls-A",
		TargetID: "dtls-B",
		Payload:  tamperedPayload,
	}); err == nil {
		t.Fatalf("expected tampered DTLS fingerprint in offer to be rejected")
	}
	if _, ok := mgrB.GetConnection("sess-tamper"); ok {
		t.Fatalf("tampered session must not be accepted")
	}

	// 3. Different Ed25519 key for pinned peer must be rejected
	if err := mgrB.PinPeerKey("dtls-A", mgrA.PublicKey()); err != nil {
		t.Fatalf("PinPeerKey failed: %v", err)
	}
	_, roguePriv, _ := ed25519.GenerateKey(rand.Reader)
	roguePub := roguePriv.Public().(ed25519.PublicKey)
	rogueSig := ed25519.Sign(roguePriv, []byte(origFP))
	roguePayload, _ := json.Marshal(protocol.SDPPayload{
		SessionID: "sess-rogue",
		Type:      "offer",
		SDP:       offer.SDP,
		PublicKey: hex.EncodeToString(roguePub),
		Signature: hex.EncodeToString(rogueSig),
	})
	if err := mgrB.HandleSignalingEnvelope(protocol.Envelope{
		Type:     protocol.MsgSDPOffer,
		SenderID: "dtls-A",
		TargetID: "dtls-B",
		Payload:  roguePayload,
	}); err == nil {
		t.Fatalf("expected SDP offer signed by mismatched Ed25519 key to be rejected")
	}
}

func TestManager_GlareTieBreaker(t *testing.T) {
	// Part 1: Unit test lexicographical LocalPeerID < RemotePeerID tie-breaker on handleOffer
	mgrPolite, err := NewManager(Config{NodeID: "peer-alpha"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager polite failed: %v", err)
	}
	defer mgrPolite.Close()

	mgrImpolite, err := NewManager(Config{NodeID: "peer-zeta"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager impolite failed: %v", err)
	}
	defer mgrImpolite.Close()

	makePendingOffer := func(mgr *Manager, remoteID, sessID string) protocol.SDPPayload {
		pc, err := mgr.api.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatalf("NewPeerConnection failed: %v", err)
		}
		_, _ = pc.CreateDataChannel("stream", nil)
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			t.Fatalf("CreateOffer failed: %v", err)
		}
		if err := pc.SetLocalDescription(offer); err != nil {
			t.Fatalf("SetLocalDescription failed: %v", err)
		}
		conn := newPeerConnection(sessID, mgr.cfg.NodeID, remoteID, pc, nil)
		conn.setupHandlers()
		mgr.mu.Lock()
		mgr.connections[sessID] = conn
		mgr.peerSessions[remoteID] = sessID
		mgr.mu.Unlock()
		signed, err := mgr.signSDPPayload(sessID, "offer", offer.SDP)
		if err != nil {
			t.Fatalf("signSDPPayload failed: %v", err)
		}
		return signed
	}

	offerFromPolite := makePendingOffer(mgrPolite, "peer-zeta", "sess-alpha")
	offerFromImpolite := makePendingOffer(mgrImpolite, "peer-alpha", "sess-zeta")

	// Impolite ("peer-zeta") receives colliding offer from "peer-alpha": MUST ignore it and keep "sess-zeta"
	if err := mgrImpolite.handleOffer("peer-alpha", offerFromPolite); err != nil {
		t.Fatalf("impolite handleOffer failed: %v", err)
	}
	mgrImpolite.mu.Lock()
	impoliteSess := mgrImpolite.peerSessions["peer-alpha"]
	mgrImpolite.mu.Unlock()
	if impoliteSess != "sess-zeta" {
		t.Fatalf("expected impolite peer to keep its own session 'sess-zeta', got %q", impoliteSess)
	}

	// Polite ("peer-alpha") receives colliding offer from "peer-zeta": MUST yield and adopt "sess-zeta"
	if err := mgrPolite.handleOffer("peer-zeta", offerFromImpolite); err != nil {
		t.Fatalf("polite handleOffer failed: %v", err)
	}
	mgrPolite.mu.Lock()
	politeSess := mgrPolite.peerSessions["peer-zeta"]
	mgrPolite.mu.Unlock()
	if politeSess != "sess-zeta" {
		t.Fatalf("expected polite peer to yield and adopt 'sess-zeta', got %q", politeSess)
	}

	// Part 2: End-to-end simultaneous ConnectPeer glare resolution over live signaling
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	var m1Ptr, m2Ptr atomic.Pointer[Manager]
	sig1, err := signaling.NewClient(hub.URL(), "glare-1", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := m1Ptr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("sig1 failed: %v", err)
	}
	defer sig1.Close()

	sig2, err := signaling.NewClient(hub.URL(), "glare-2", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := m2Ptr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("sig2 failed: %v", err)
	}
	defer sig2.Close()

	m1, _ := NewManager(Config{NodeID: "glare-1"}, sig1, nil)
	defer m1.Close()
	m1Ptr.Store(m1)
	m2, _ := NewManager(Config{NodeID: "glare-2"}, sig2, nil)
	defer m2.Close()
	m2Ptr.Store(m2)

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var c1, c2 *PeerConnection
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c1, err1 = m1.ConnectPeer(ctx, "glare-2")
	}()
	go func() {
		defer wg.Done()
		c2, err2 = m2.ConnectPeer(ctx, "glare-1")
	}()
	wg.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("simultaneous ConnectPeer glare failed: err1=%v err2=%v", err1, err2)
	}
	if c1.SessionID != c2.SessionID {
		t.Fatalf("expected both peers to converge on the same SessionID, got %s vs %s", c1.SessionID, c2.SessionID)
	}
}

func TestPeerConnection_ChunkAckAndErrorPropagation(t *testing.T) {
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	var mAPtr, mBPtr atomic.Pointer[Manager]
	sigA, err := signaling.NewClient(hub.URL(), "ack-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mAPtr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("sigA failed: %v", err)
	}
	defer sigA.Close()
	sigB, err := signaling.NewClient(hub.URL(), "ack-B", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mBPtr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("sigB failed: %v", err)
	}
	defer sigB.Close()

	mA, _ := NewManager(Config{NodeID: "ack-A"}, sigA, nil)
	defer mA.Close()
	mAPtr.Store(mA)
	mB, _ := NewManager(Config{NodeID: "ack-B"}, sigB, nil)
	defer mB.Close()
	mBPtr.Store(mB)

	connBCh := make(chan *PeerConnection, 1)
	mB.SetOnNewConnection(func(pc *PeerConnection) {
		pc.SetOnChunkFileReceived(func(chunkID string, filePath string, size int64) {
			_ = pc.SendChunkError(chunkID, "simulated disk write failure")
		})
		connBCh <- pc
	})

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connA, err := mA.ConnectPeer(ctx, "ack-B")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	connB := <-connBCh
	if err := connB.WaitChannelsReady(ctx); err != nil {
		t.Fatalf("connB channels not ready: %v", err)
	}

	payload := []byte("chunk that will fail on receiver")
	err = connA.SendChunk(ctx, "fail-chunk-id", bytes.NewReader(payload), int64(len(payload)))
	if err == nil || !strings.Contains(err.Error(), "simulated disk write failure") {
		t.Fatalf("expected SendChunk to fail with receiver error, got: %v", err)
	}
}

func TestPeerConnection_BoundedChunkRequestConcurrency(t *testing.T) {
	conn := newPeerConnection("sess-sem", "local", "remote", nil, nil)
	defer conn.Close()

	var current int32
	var maxObserved int32
	var totalDone int32
	allDone := make(chan struct{})

	const totalRequests = 12
	conn.SetOnChunkRequest(func(chunkID string) {
		cur := atomic.AddInt32(&current, 1)
		for {
			prev := atomic.LoadInt32(&maxObserved)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxObserved, prev, cur) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		if atomic.AddInt32(&totalDone, 1) == totalRequests {
			close(allDone)
		}
	})

	for i := 0; i < totalRequests; i++ {
		cid := fmt.Sprintf("req-%d", i)
		frame := make([]byte, 2+len(cid))
		frame[0] = protocol.DataMsgChunkRequest
		frame[1] = byte(len(cid))
		copy(frame[2:], cid)
		conn.handleChunkMessage(frame)
	}

	select {
	case <-allDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for bounded chunk requests to finish (completed %d/%d)", atomic.LoadInt32(&totalDone), totalRequests)
	}

	if max := atomic.LoadInt32(&maxObserved); max > MaxConcurrentChunkReqs {
		t.Fatalf("expected max concurrent chunk request handlers <= %d, observed %d", MaxConcurrentChunkReqs, max)
	}
}

func TestManager_CoordinatorTURNCredentialsInICEServers(t *testing.T) {
	hub, err := signaling.NewHubWithConfig(signaling.HubConfig{
		ListenAddr:     "127.0.0.1:0",
		TURNURL:        "turn:203.0.113.5:3478",
		TURNSecret:     "coord-turn-secret",
		TURNTTLSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("NewHubWithConfig failed: %v", err)
	}
	defer hub.Close()

	ackReceived := make(chan struct{})
	sig, err := signaling.NewClient(hub.URL(), "turn-node", func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			close(ackReceived)
		}
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer sig.Close()

	select {
	case <-ackReceived:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for MsgRegisterAck")
	}

	mgr, err := NewManager(Config{NodeID: "turn-node"}, sig, nil)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer mgr.Close()

	iceServers := mgr.buildICEServers()
	foundTURN := false
	for _, srv := range iceServers {
		for _, u := range srv.URLs {
			if u == "turn:203.0.113.5:3478" && srv.Username != "" && srv.Credential != "" {
				foundTURN = true
			}
		}
	}
	if !foundTURN {
		t.Fatalf("expected coordinator-issued TURN server credentials in buildICEServers(), got: %+v", iceServers)
	}
}

func TestManager_ICERestartOnDisconnected(t *testing.T) {
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	var mAPtr, mBPtr atomic.Pointer[Manager]
	sigA, err := signaling.NewClient(hub.URL(), "ice-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mAPtr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("sigA failed: %v", err)
	}
	defer sigA.Close()

	var restartOfferCount int32
	sigB, err := signaling.NewClient(hub.URL(), "ice-B", func(env protocol.Envelope) {
		if env.Type == protocol.MsgSDPOffer {
			atomic.AddInt32(&restartOfferCount, 1)
		}
		if env.Type != protocol.MsgRegisterAck {
			if m := mBPtr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("sigB failed: %v", err)
	}
	defer sigB.Close()

	mA, _ := NewManager(Config{NodeID: "ice-A"}, sigA, nil)
	defer mA.Close()
	mAPtr.Store(mA)
	mB, _ := NewManager(Config{NodeID: "ice-B"}, sigB, nil)
	defer mB.Close()
	mBPtr.Store(mB)

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connA, err := mA.ConnectPeer(ctx, "ice-B")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	defer connA.Close()

	before := atomic.LoadInt32(&restartOfferCount)
	// Simulate PeerConnectionStateDisconnected firing on connA while signaling is connected
	connA.mu.RLock()
	internalFn := connA.onInternalStateChange
	connA.mu.RUnlock()
	if internalFn == nil {
		t.Fatalf("expected internal state change callback to be wired")
	}
	internalFn(webrtc.PeerConnectionStateDisconnected)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&restartOfferCount) > before {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected ICE restart SDP offer to be sent upon PeerConnectionStateDisconnected")
}

func TestManager_RejectsCrossPeerSessionSpoofing(t *testing.T) {
	mgrA, err := NewManager(Config{NodeID: "spoof-A"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager A failed: %v", err)
	}
	defer mgrA.Close()

	mgrB, err := NewManager(Config{NodeID: "spoof-B"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager B failed: %v", err)
	}
	defer mgrB.Close()

	mgrC, err := NewManager(Config{NodeID: "spoof-C"}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager C failed: %v", err)
	}
	defer mgrC.Close()

	pcA, err := mgrA.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection A failed: %v", err)
	}
	_, _ = pcA.CreateDataChannel("stream", nil)
	offer, err := pcA.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer A failed: %v", err)
	}
	if err := pcA.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription A failed: %v", err)
	}

	const sessID = "sess-ab-only"
	connA := newPeerConnection(sessID, "spoof-A", "spoof-B", pcA, nil)
	connA.setupHandlers()
	mgrA.mu.Lock()
	mgrA.connections[sessID] = connA
	mgrA.peerSessions["spoof-B"] = sessID
	mgrA.mu.Unlock()

	// Peer C signs its own valid SDP offer/answer/candidate targeting sessID
	pcC, err := mgrC.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection C failed: %v", err)
	}
	defer pcC.Close()
	if err := pcC.SetRemoteDescription(offer); err != nil {
		t.Fatalf("SetRemoteDescription C failed: %v", err)
	}
	ansC, err := pcC.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("CreateAnswer C failed: %v", err)
	}
	signedAnsC, err := mgrC.signSDPPayload(sessID, "answer", ansC.SDP)
	if err != nil {
		t.Fatalf("signSDPPayload C failed: %v", err)
	}
	ansPayload, _ := json.Marshal(signedAnsC)
	if err := mgrA.HandleSignalingEnvelope(protocol.Envelope{
		Type:     protocol.MsgSDPAnswer,
		SenderID: "spoof-C",
		TargetID: "spoof-A",
		Payload:  ansPayload,
	}); err == nil || !strings.Contains(err.Error(), "belongs to peer") {
		t.Fatalf("expected cross-peer MsgSDPAnswer to be rejected, got: %v", err)
	}

	// Cross-peer same-session MsgSDPOffer must also be rejected
	signedOfferC, err := mgrC.signSDPPayload(sessID, "offer", offer.SDP)
	if err != nil {
		t.Fatalf("signSDPPayload offer C failed: %v", err)
	}
	offerPayload, _ := json.Marshal(signedOfferC)
	if err := mgrA.HandleSignalingEnvelope(protocol.Envelope{
		Type:     protocol.MsgSDPOffer,
		SenderID: "spoof-C",
		TargetID: "spoof-A",
		Payload:  offerPayload,
	}); err == nil || !strings.Contains(err.Error(), "belongs to peer") {
		t.Fatalf("expected cross-peer same-session MsgSDPOffer to be rejected, got: %v", err)
	}

	// Cross-peer MsgICECandidate must also be rejected
	candPayload, _ := json.Marshal(protocol.ICECandidatePayload{
		SessionID: sessID,
		Candidate: "candidate:1 1 udp 2122260223 127.0.0.1 50000 typ host",
	})
	if err := mgrA.HandleSignalingEnvelope(protocol.Envelope{
		Type:     protocol.MsgICECandidate,
		SenderID: "spoof-C",
		TargetID: "spoof-A",
		Payload:  candPayload,
	}); err == nil || !strings.Contains(err.Error(), "belongs to peer") {
		t.Fatalf("expected cross-peer MsgICECandidate to be rejected, got: %v", err)
	}
}

func TestPeerConnection_MultiFrameDiskStreamingAndEOFGuards(t *testing.T) {
	tempDir := t.TempDir()
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	var mAPtr, mBPtr atomic.Pointer[Manager]
	sigA, err := signaling.NewClient(hub.URL(), "stream-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mAPtr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("sigA failed: %v", err)
	}
	defer sigA.Close()

	sigB, err := signaling.NewClient(hub.URL(), "stream-B", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mBPtr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	if err != nil {
		t.Fatalf("sigB failed: %v", err)
	}
	defer sigB.Close()

	chunkTmpA := filepath.Join(tempDir, "tmpA")
	chunkTmpB := filepath.Join(tempDir, "tmpB")
	mA, _ := NewManager(Config{NodeID: "stream-A", ChunkTempDir: chunkTmpA}, sigA, nil)
	defer mA.Close()
	mAPtr.Store(mA)
	mB, _ := NewManager(Config{NodeID: "stream-B", ChunkTempDir: chunkTmpB}, sigB, nil)
	defer mB.Close()
	mBPtr.Store(mB)

	connBCh := make(chan *PeerConnection, 1)
	fileDoneCh := make(chan struct{}, 1)
	mB.SetOnNewConnection(func(pc *PeerConnection) {
		pc.SetOnChunkFileReceived(func(chunkID string, filePath string, size int64) {
			defer func() {
				_ = os.Remove(filePath)
				select {
				case fileDoneCh <- struct{}{}:
				default:
				}
			}()
			got, readErr := os.ReadFile(filePath)
			_ = os.Remove(filePath)
			if readErr != nil {
				_ = pc.SendChunkError(chunkID, readErr.Error())
				return
			}
			if int64(len(got)) != size {
				_ = pc.SendChunkError(chunkID, "size mismatch")
				return
			}
			_ = pc.SendChunkAck(chunkID)
		})
		connBCh <- pc
	})

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connA, err := mA.ConnectPeer(ctx, "stream-B")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}
	connB := <-connBCh
	if err := connB.WaitChannelsReady(ctx); err != nil {
		t.Fatalf("connB channels not ready: %v", err)
	}

	// 1. Multi-frame (> 60 KiB) chunk streaming: 256 KiB across 5 SCTP frames
	largePayload := bytes.Repeat([]byte("0123456789abcdef"), 16*1024) // 256 KiB
	if err := connA.SendChunk(ctx, "large-256k-chunk", bytes.NewReader(largePayload), int64(len(largePayload))); err != nil {
		t.Fatalf("SendChunk 256 KiB failed: %v", err)
	}
	select {
	case <-fileDoneCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for receiver file cleanup")
	}
	// Verify no temp files leaked in chunkTmpB
	entries, err := os.ReadDir(chunkTmpB)
	if err != nil {
		t.Fatalf("ReadDir chunkTmpB failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 leftover temp files in %s, got %d", chunkTmpB, len(entries))
	}

	// 2. Truncated reader in SendChunk must return io.ErrUnexpectedEOF immediately instead of hanging
	shortReader := bytes.NewReader([]byte("short"))
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shortCancel()
	if err := connA.SendChunk(shortCtx, "short-eof-chunk", shortReader, 1024); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF for short reader, got: %v", err)
	}

	// 3. Size mismatch in FetchChunkToFile must fail fast instead of hanging until timeout
	connB.SetOnChunkRequest(func(chunkID string) {
		_ = connB.SendChunk(ctx, chunkID, bytes.NewReader([]byte("actual-len-13")), 13)
	})
	mismatchCtx, mismatchCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer mismatchCancel()
	if _, err := connA.FetchChunkToFile(mismatchCtx, "mismatch-size-chunk", 999); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected chunk size mismatch error, got: %v", err)
	}
}

func TestManager_EmbeddedTURNRelayWithCoordinatorCredentials(t *testing.T) {
	const turnSecret = "e2e-turn-rest-secret"
	turnSrv, err := relay.NewEmbeddedTURN(relay.TURNConfig{
		PublicIP: "127.0.0.1",
		Port:     0,
		Realm:    "unicompute.test",
		Secret:   turnSecret,
	})
	if err != nil {
		t.Fatalf("NewEmbeddedTURN failed: %v", err)
	}
	defer turnSrv.Close()

	addr := turnSrv.Addr()
	portIdx := strings.LastIndex(addr, ":")
	turnURL := "turn:127.0.0.1" + addr[portIdx:]
	hub, err := signaling.NewHubWithConfig(signaling.HubConfig{
		ListenAddr:     "127.0.0.1:0",
		TURNURL:        turnURL,
		TURNSecret:     turnSecret,
		TURNTTLSeconds: 300,
	})
	if err != nil {
		t.Fatalf("NewHubWithConfig failed: %v", err)
	}
	defer hub.Close()

	var mAPtr, mBPtr atomic.Pointer[Manager]
	ackA := make(chan struct{}, 1)
	ackB := make(chan struct{}, 1)
	sigA, err := signaling.NewClient(hub.URL(), "turn-e2e-A", func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			select {
			case ackA <- struct{}{}:
			default:
			}
			return
		}
		if m := mAPtr.Load(); m != nil {
			_ = m.HandleSignalingEnvelope(env)
		}
	})
	if err != nil {
		t.Fatalf("sigA failed: %v", err)
	}
	defer sigA.Close()

	sigB, err := signaling.NewClient(hub.URL(), "turn-e2e-B", func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			select {
			case ackB <- struct{}{}:
			default:
			}
			return
		}
		if m := mBPtr.Load(); m != nil {
			_ = m.HandleSignalingEnvelope(env)
		}
	})
	if err != nil {
		t.Fatalf("sigB failed: %v", err)
	}
	defer sigB.Close()

	select {
	case <-ackA:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for sigA RegisterAck")
	}
	select {
	case <-ackB:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for sigB RegisterAck")
	}

	trackerA := instrumentation.NewTracker()
	mA, _ := NewManager(Config{NodeID: "turn-e2e-A"}, sigA, trackerA)
	defer mA.Close()
	mAPtr.Store(mA)

	mB, _ := NewManager(Config{NodeID: "turn-e2e-B"}, sigB, nil)
	defer mB.Close()
	mBPtr.Store(mB)

	// Verify coordinator-issued TURN credentials authenticate against EmbeddedTURN and allow a relayed DataChannel message
	iceServers := mA.buildICEServers()
	pcA, err := mA.api.NewPeerConnection(webrtc.Configuration{
		ICEServers:         iceServers,
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	})
	if err != nil {
		t.Fatalf("NewPeerConnection A failed: %v", err)
	}
	defer pcA.Close()
	pcB, err := mB.api.NewPeerConnection(webrtc.Configuration{
		ICEServers:         mB.buildICEServers(),
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	})
	if err != nil {
		t.Fatalf("NewPeerConnection B failed: %v", err)
	}
	defer pcB.Close()

	msgRecv := make(chan string, 1)
	pcB.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			msgRecv <- string(msg.Data)
		})
	})

	pcA.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = pcB.AddICECandidate(c.ToJSON())
		}
	})
	pcB.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = pcA.AddICECandidate(c.ToJSON())
		}
	})

	dc, err := pcA.CreateDataChannel("relay-test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel failed: %v", err)
	}
	dc.OnOpen(func() {
		_ = dc.SendText("hello-via-turn-rest")
	})

	offer, err := pcA.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer failed: %v", err)
	}
	if err := pcA.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription A failed: %v", err)
	}
	if err := pcB.SetRemoteDescription(offer); err != nil {
		t.Fatalf("SetRemoteDescription B failed: %v", err)
	}
	answer, err := pcB.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("CreateAnswer B failed: %v", err)
	}
	if err := pcB.SetLocalDescription(answer); err != nil {
		t.Fatalf("SetLocalDescription B failed: %v", err)
	}
	if err := pcA.SetRemoteDescription(answer); err != nil {
		t.Fatalf("SetRemoteDescription A failed: %v", err)
	}

	select {
	case got := <-msgRecv:
		if got != "hello-via-turn-rest" {
			t.Fatalf("unexpected relayed message: %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for DataChannel message over EmbeddedTURN relay")
	}
}

func TestPeerConnection_ConcurrentSameChunkIDSend(t *testing.T) {
	hub, err := signaling.NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	var mAPtr, mBPtr atomic.Pointer[Manager]
	sigA, _ := signaling.NewClient(hub.URL(), "conc-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mAPtr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	defer sigA.Close()
	sigB, _ := signaling.NewClient(hub.URL(), "conc-B", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			if m := mBPtr.Load(); m != nil {
				_ = m.HandleSignalingEnvelope(env)
			}
		}
	})
	defer sigB.Close()

	mA, _ := NewManager(Config{NodeID: "conc-A"}, sigA, nil)
	defer mA.Close()
	mAPtr.Store(mA)
	mB, _ := NewManager(Config{NodeID: "conc-B"}, sigB, nil)
	defer mB.Close()
	mBPtr.Store(mB)

	// Receiver deliberately holds off ACK briefly so first SendChunk stays active in ackWaiters
	holdAck := make(chan struct{})
	mB.SetOnNewConnection(func(pc *PeerConnection) {
		pc.SetOnChunkFileReceived(func(chunkID string, filePath string, size int64) {
			defer os.Remove(filePath)
			<-holdAck
			_ = pc.SendChunkAck(chunkID)
		})
	})

	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connA, err := mA.ConnectPeer(ctx, "conc-B")
	if err != nil {
		t.Fatalf("ConnectPeer failed: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		payload := []byte("first-send-payload")
		firstDone <- connA.SendChunk(ctx, "dup-send-chunk", bytes.NewReader(payload), int64(len(payload)))
	}()

	// Wait until ackWaiters["dup-send-chunk"] is registered
	deadline := time.Now().Add(2 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		connA.ackMu.Lock()
		_, registered = connA.ackWaiters["dup-send-chunk"]
		connA.ackMu.Unlock()
		if registered {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !registered {
		close(holdAck)
		t.Fatalf("first SendChunk never registered ackWaiter")
	}

	payload2 := []byte("second-send-payload")
	err2 := connA.SendChunk(ctx, "dup-send-chunk", bytes.NewReader(payload2), int64(len(payload2)))
	if err2 == nil || !strings.Contains(err2.Error(), "concurrent SendChunk") {
		close(holdAck)
		t.Fatalf("expected concurrent SendChunk error for duplicate chunkID, got: %v", err2)
	}

	close(holdAck)
	if err1 := <-firstDone; err1 != nil {
		t.Fatalf("first SendChunk should succeed cleanly without being orphaned, got: %v", err1)
	}
}

func TestPeerConnection_InFlightTTLEviction(t *testing.T) {
	tmpDir := t.TempDir()
	conn := newPeerConnection("sess-ttl", "local", "remote", nil, nil)
	conn.chunkTempDir = tmpDir
	defer conn.Close()

	// Create two incomplete in-flight chunks
	conn.handleChunkMessage(buildTestChunkFrame(protocol.FlagStart, 0, 1024, "stale-chunk", []byte("partial-stale")))
	conn.handleChunkMessage(buildTestChunkFrame(protocol.FlagStart, 0, 2048, "fresh-chunk", []byte("partial-fresh")))

	conn.inFlightMu.Lock()
	staleEntry, okStale := conn.inFlight["stale-chunk"]
	freshEntry, okFresh := conn.inFlight["fresh-chunk"]
	if !okStale || !okFresh {
		conn.inFlightMu.Unlock()
		t.Fatalf("expected both in-flight chunks to exist, got stale=%v fresh=%v", okStale, okFresh)
	}
	stalePath := staleEntry.tmpPath
	freshPath := freshEntry.tmpPath
	// Age only stale-chunk past InFlightChunkTTL
	now := time.Now()
	staleEntry.lastActivity = now.Add(-(InFlightChunkTTL + time.Second))
	freshEntry.lastActivity = now
	conn.inFlightMu.Unlock()

	if _, err := os.Stat(stalePath); err != nil {
		t.Fatalf("expected stale temp file %s to exist before eviction: %v", stalePath, err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("expected fresh temp file %s to exist before eviction: %v", freshPath, err)
	}

	conn.evictStaleInFlight(now)

	// Stale chunk must be evicted and its temp file deleted on disk
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale temp file %s to be deleted after TTL eviction, stat err=%v", stalePath, err)
	}
	// Fresh chunk must remain active on disk and in map
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("expected fresh temp file %s to remain after TTL eviction: %v", freshPath, err)
	}

	conn.inFlightMu.Lock()
	_, stillStale := conn.inFlight["stale-chunk"]
	_, stillFresh := conn.inFlight["fresh-chunk"]
	totalBytes := conn.totalInFlightBytes
	conn.inFlightMu.Unlock()

	if stillStale {
		t.Fatalf("expected stale-chunk to be evicted from inFlight map")
	}
	if !stillFresh {
		t.Fatalf("expected fresh-chunk to remain in inFlight map")
	}
	if totalBytes != 2048 {
		t.Fatalf("expected totalInFlightBytes == 2048 after evicting 1024-byte stale chunk, got %d", totalBytes)
	}

	select {
	case err := <-staleEntry.errCh:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("expected ErrTimeout on evicted entry errCh, got %v", err)
		}
	default:
		t.Fatalf("expected ErrTimeout notification on evicted entry errCh")
	}
}
