package signaling

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/unicompute/obj1/pkg/protocol"
)

func TestSignaling_HubAndClientDispatch(t *testing.T) {
	hub, err := NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start hub: %v", err)
	}
	defer hub.Close()

	wsURL := hub.URL()

	recvA := make(chan protocol.Envelope, 10)
	recvB := make(chan protocol.Envelope, 10)

	clientA, err := NewClient(wsURL, "node-A", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			recvA <- env
		}
	})
	if err != nil {
		t.Fatalf("failed to create clientA: %v", err)
	}
	defer clientA.Close()

	clientB, err := NewClient(wsURL, "node-B", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			recvB <- env
		}
	})
	if err != nil {
		t.Fatalf("failed to create clientB: %v", err)
	}
	defer clientB.Close()

	// Wait for registration
	time.Sleep(100 * time.Millisecond)

	// Send message from A to B
	payload, _ := json.Marshal(map[string]string{"greeting": "hello node-B"})
	env := protocol.Envelope{
		Type:     protocol.MsgSDPOffer,
		SenderID: "node-A",
		TargetID: "node-B",
		Payload:  payload,
	}

	if err := clientA.Send(env); err != nil {
		t.Fatalf("clientA Send failed: %v", err)
	}

	select {
	case received := <-recvB:
		if received.Type != protocol.MsgSDPOffer {
			t.Errorf("expected MsgSDPOffer, got %v", received.Type)
		}
		if received.SenderID != "node-A" {
			t.Errorf("expected sender node-A, got %v", received.SenderID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for message at clientB")
	}
}

func TestSignaling_AuthTokenEnforcement(t *testing.T) {
	authToken := "test-secret-token-12345"
	hub, err := NewHub("127.0.0.1:0", authToken)
	if err != nil {
		t.Fatalf("failed to start hub: %v", err)
	}
	defer hub.Close()

	wsURL := hub.URL()

	// 1. Connection without token must fail
	_, err = NewClient(wsURL, "node-unauth", func(env protocol.Envelope) {})
	if err == nil {
		t.Fatalf("expected NewClient without token to fail against protected hub")
	}

	// 2. Connection with wrong token must fail
	_, err = NewClient(wsURL, "node-wrong", func(env protocol.Envelope) {}, "wrong-token")
	if err == nil {
		t.Fatalf("expected NewClient with wrong token to fail against protected hub")
	}

	// 3. Connection with valid token must succeed
	clientAuth, err := NewClient(wsURL, "node-valid", func(env protocol.Envelope) {}, authToken)
	if err != nil {
		t.Fatalf("expected NewClient with valid token to succeed, got: %v", err)
	}
	defer clientAuth.Close()
}

func TestSignaling_DuplicateNodeIDRejected(t *testing.T) {
	hub, err := NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start hub: %v", err)
	}
	defer hub.Close()

	wsURL := hub.URL()

	// First client registers "node-alpha"
	c1, err := NewClient(wsURL, "node-alpha", func(env protocol.Envelope) {})
	if err != nil {
		t.Fatalf("failed to create c1: %v", err)
	}
	defer c1.Close()

	time.Sleep(100 * time.Millisecond)

	// Second client attempts to register "node-alpha"
	c2Ack := make(chan protocol.Envelope, 5)
	c2, err := NewClient(wsURL, "node-alpha", func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			c2Ack <- env
		}
	})
	if err != nil {
		t.Fatalf("failed to create c2: %v", err)
	}
	defer c2.Close()

	select {
	case ack := <-c2Ack:
		if !strings.Contains(string(ack.Payload), "duplicate nodeID registration rejected") {
			t.Errorf("expected duplicate registration rejection error, got payload: %s", string(ack.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for duplicate rejection ack")
	}
}

func TestHub_RejectsSpoofedSenderID(t *testing.T) {
	authToken := "cluster-auth-token-xyz"
	hub, err := NewHub("127.0.0.1:0", authToken)
	if err != nil {
		t.Fatalf("failed to start hub: %v", err)
	}
	defer hub.Close()

	wsURL := hub.URL()

	recvC := make(chan protocol.Envelope, 10)

	// Register authenticated peer-A
	peerA, err := NewClient(wsURL, "peer-A", func(env protocol.Envelope) {}, authToken)
	if err != nil {
		t.Fatalf("failed to create peerA: %v", err)
	}
	defer peerA.Close()

	// Register authenticated peer-C
	peerC, err := NewClient(wsURL, "peer-C", func(env protocol.Envelope) {
		if env.Type != protocol.MsgRegisterAck {
			recvC <- env
		}
	}, authToken)
	if err != nil {
		t.Fatalf("failed to create peerC: %v", err)
	}
	defer peerC.Close()

	time.Sleep(100 * time.Millisecond)

	// 1. peer-A attempts to send a spoofed envelope claiming SenderID: "peer-B" targeting peer-C
	spoofedPayload, _ := json.Marshal(map[string]string{"attack": "impersonation"})
	spoofedEnv := protocol.Envelope{
		Type:     protocol.MsgSDPOffer,
		SenderID: "peer-B",
		TargetID: "peer-C",
		Payload:  spoofedPayload,
	}

	if err := peerA.Send(spoofedEnv); err != nil {
		t.Fatalf("peerA Send failed: %v", err)
	}

	// Verify peer-C does NOT receive the spoofed message
	select {
	case env := <-recvC:
		t.Fatalf("security violation: peer-C received spoofed message from SenderID %s: %s", env.SenderID, string(env.Payload))
	case <-time.After(300 * time.Millisecond):
		// Expected: hub dropped the spoofed message
	}

	// 2. Legitimate message from peer-A to peer-C should still succeed
	legitPayload, _ := json.Marshal(map[string]string{"status": "legit"})
	legitEnv := protocol.Envelope{
		Type:     protocol.MsgSDPOffer,
		SenderID: "peer-A",
		TargetID: "peer-C",
		Payload:  legitPayload,
	}

	if err := peerA.Send(legitEnv); err != nil {
		t.Fatalf("peerA legitimate Send failed: %v", err)
	}

	select {
	case received := <-recvC:
		if received.SenderID != "peer-A" {
			t.Errorf("expected SenderID peer-A, got: %s", received.SenderID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for legitimate message at peer-C")
	}
}
