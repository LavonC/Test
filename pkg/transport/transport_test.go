package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
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

	var mgrA, mgrB *Manager

	// Signaling client for Node A
	sigA, err := signaling.NewClient(wsURL, "node-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck && mgrA != nil {
			_ = mgrA.HandleSignalingEnvelope(env)
		}
	})
	if err != nil {
		t.Fatalf("failed to start sigA: %v", err)
	}
	defer sigA.Close()

	// Signaling client for Node B
	sigB, err := signaling.NewClient(wsURL, "node-B", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck && mgrB != nil {
			_ = mgrB.HandleSignalingEnvelope(env)
		}
	})
	if err != nil {
		t.Fatalf("failed to start sigB: %v", err)
	}
	defer sigB.Close()

	mgrA, err = NewManager(Config{
		NodeID: "node-A",
	}, sigA, trackerA)
	if err != nil {
		t.Fatalf("failed to create mgrA: %v", err)
	}
	defer mgrA.Close()

	mgrB, err = NewManager(Config{
		NodeID: "node-B",
	}, sigB, trackerB)
	if err != nil {
		t.Fatalf("failed to create mgrB: %v", err)
	}
	defer mgrB.Close()

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
	connB.OnStreamMessage = func(msg []byte) {
		receivedStreamCh <- msg
	}

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
