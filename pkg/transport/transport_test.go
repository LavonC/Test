package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

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
