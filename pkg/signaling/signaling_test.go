package signaling

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func TestSignaling_RejectsImpersonationOfOfflineNodeWithDifferentKey(t *testing.T) {
	const authToken = "cluster-shared-token"
	hub, err := NewHub("127.0.0.1:0", authToken)
	if err != nil {
		t.Fatalf("failed to start hub: %v", err)
	}
	defer hub.Close()

	wsURL := hub.URL()

	_, legitPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate legit key: %v", err)
	}
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate attacker key: %v", err)
	}

	// 1. Legitimate node registers with its ed25519 keypair, then goes offline.
	legitAck := make(chan protocol.Envelope, 2)
	legitClient, err := NewClientWithKey(wsURL, "trusted-worker", legitPriv, func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			legitAck <- env
		}
	}, authToken)
	if err != nil {
		t.Fatalf("legit client failed to connect: %v", err)
	}

	select {
	case ack := <-legitAck:
		if strings.Contains(string(ack.Payload), "error") {
			t.Fatalf("legitimate registration unexpectedly failed: %s", string(ack.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for legitimate RegisterAck")
	}

	_ = legitClient.Close()
	time.Sleep(100 * time.Millisecond)

	// 2. Attacker with valid cluster token but different key tries to hijack offline "trusted-worker".
	attackerAck := make(chan protocol.Envelope, 2)
	attackerClient, err := NewClientWithKey(wsURL, "trusted-worker", attackerPriv, func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			attackerAck <- env
		}
	}, authToken)
	if err != nil {
		t.Fatalf("attacker websocket dial failed: %v", err)
	}
	defer attackerClient.Close()

	select {
	case ack := <-attackerAck:
		if !strings.Contains(string(ack.Payload), "nodeID public key mismatch") {
			t.Fatalf("expected 'nodeID public key mismatch' rejection, got: %s", string(ack.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for impersonation rejection ack")
	}

	// 3. Also verify unsigned raw MsgRegister is rejected with ClosePolicyViolation
	header := map[string][]string{"Authorization": {"Bearer " + authToken}}
	rawConn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("raw dial failed: %v", err)
	}
	defer rawConn.Close()
	unsignedReg, _ := json.Marshal(protocol.Envelope{
		Type:     protocol.MsgRegister,
		SenderID: "unsigned-node",
		Payload:  []byte(`{"node_id":"unsigned-node"}`),
	})
	if err := rawConn.WriteMessage(websocket.TextMessage, unsignedReg); err != nil {
		t.Fatalf("failed to write unsigned register: %v", err)
	}
	_ = rawConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	gotReject := false
	for {
		_, msg, readErr := rawConn.ReadMessage()
		if readErr != nil {
			if closeErr, ok := readErr.(*websocket.CloseError); ok && closeErr.Code == websocket.ClosePolicyViolation {
				gotReject = true
			}
			break
		}
		if strings.Contains(string(msg), "missing or invalid ed25519 public key") {
			gotReject = true
		}
	}
	if !gotReject {
		t.Fatalf("expected unsigned MsgRegister to be rejected with policy violation")
	}

	// 4. Legitimate node reconnects with the original keypair and succeeds.
	reconnAck := make(chan protocol.Envelope, 2)
	reconnClient, err := NewClientWithKey(wsURL, "trusted-worker", legitPriv, func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			reconnAck <- env
		}
	}, authToken)
	if err != nil {
		t.Fatalf("legitimate reconnect failed: %v", err)
	}
	defer reconnClient.Close()

	select {
	case ack := <-reconnAck:
		if strings.Contains(string(ack.Payload), "error") {
			t.Fatalf("legitimate reconnect failed: %s", string(ack.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for legitimate reconnect ack")
	}
}

func TestSignaling_OriginCheckAndAsyncReconnect(t *testing.T) {
	hub, err := NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start hub: %v", err)
	}
	wsURL := hub.URL()

	// 1. Cross-origin browser request must be rejected by CheckOrigin
	badHeaders := http.Header{"Origin": {"http://evil.example.com"}}
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, badHeaders)
	if err == nil {
		t.Fatalf("expected cross-origin WebSocket dial to fail")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for cross-origin dial, got %d", resp.StatusCode)
	}

	// 2. Same-host / loopback origin must succeed
	goodHeaders := http.Header{"Origin": {"http://127.0.0.1:8080"}}
	cGood, _, err := websocket.DefaultDialer.Dial(wsURL, goodHeaders)
	if err != nil {
		t.Fatalf("expected loopback Origin dial to succeed, got %v", err)
	}
	_ = cGood.Close()
	_ = hub.Close()

	// 3. Test NewClientAsync when coordinator starts AFTER client boots
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve port: %v", err)
	}
	reservedAddr := ln.Addr().String()
	_ = ln.Close()

	lateWSURL := "ws://" + reservedAddr + "/ws"
	ackCh := make(chan protocol.Envelope, 2)
	asyncClient, initErr := NewClientAsync(lateWSURL, "late-node", func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			ackCh <- env
		}
	})
	if initErr == nil || asyncClient == nil {
		t.Fatalf("expected non-nil async client and initial dial error when coordinator offline, got client=%v err=%v", asyncClient, initErr)
	}
	defer asyncClient.Close()

	// Boot hub on reservedAddr shortly after
	lateHub, err := NewHub(reservedAddr)
	if err != nil {
		t.Fatalf("failed to start late hub on %s: %v", reservedAddr, err)
	}
	defer lateHub.Close()

	select {
	case ack := <-ackCh:
		if strings.Contains(string(ack.Payload), "error") {
			t.Fatalf("late async registration failed: %s", string(ack.Payload))
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for async signaling client to reconnect when coordinator started")
	}
}

func TestClient_WriteFailureDoesNotPanicReadLoopAndReconnects(t *testing.T) {
	hub, err := NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	ackCh := make(chan protocol.Envelope, 8)
	client, err := NewClient(hub.URL(), "write-fail-node", func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			ackCh <- env
		}
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	select {
	case <-ackCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for initial RegisterAck")
	}

	// Simulate writeLoop encountering a write error and clearing c.conn while readLoop is active.
	client.mu.Lock()
	deadConn := client.conn
	if deadConn != nil {
		_ = deadConn.Close()
		client.conn = nil
	}
	client.mu.Unlock()

	// Client must not panic in readLoop and must reconnect cleanly to the hub.
	select {
	case ack := <-ackCh:
		if strings.Contains(string(ack.Payload), "error") {
			t.Fatalf("reconnect registration failed: %s", string(ack.Payload))
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for client reconnect after write failure")
	}
}

func TestHub_RejectsDuplicateRegisterOnSameConnection(t *testing.T) {
	hub, err := NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)

	conn, _, err := websocket.DefaultDialer.Dial(hub.URL(), nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	sendReg := func(nodeID string, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
		sig := ed25519.Sign(priv, []byte(nodeID))
		pl, _ := json.Marshal(protocol.RegisterPayload{
			NodeID:    nodeID,
			PublicKey: hex.EncodeToString(pub),
			Signature: hex.EncodeToString(sig),
		})
		env, _ := json.Marshal(protocol.Envelope{
			Type:     protocol.MsgRegister,
			SenderID: nodeID,
			Payload:  pl,
		})
		if err := conn.WriteMessage(websocket.TextMessage, env); err != nil {
			t.Fatalf("WriteMessage failed: %v", err)
		}
	}

	sendReg("dup-conn-node-1", pubA, privA)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, ackBytes, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected initial RegisterAck, got err: %v", err)
	}
	if strings.Contains(string(ackBytes), "error") {
		t.Fatalf("initial registration unexpectedly rejected: %s", string(ackBytes))
	}

	// Second MsgRegister on the same WebSocket connection must be rejected and clean up node-1
	sendReg("dup-conn-node-2", pubB, privB)
	gotReject := false
	for {
		_, msg, readErr := conn.ReadMessage()
		if readErr != nil {
			if closeErr, ok := readErr.(*websocket.CloseError); ok && closeErr.Code == websocket.ClosePolicyViolation {
				gotReject = true
			}
			break
		}
		if strings.Contains(string(msg), "connection already registered") {
			gotReject = true
		}
	}
	if !gotReject {
		t.Fatalf("expected second MsgRegister on same connection to be rejected")
	}

	// Wait briefly for readPump defer cleanup
	time.Sleep(50 * time.Millisecond)
	hub.mu.RLock()
	_, has1 := hub.clients["dup-conn-node-1"]
	_, has2 := hub.clients["dup-conn-node-2"]
	hub.mu.RUnlock()
	if has1 || has2 {
		t.Fatalf("expected neither node to remain in hub.clients after rejection, got has1=%v has2=%v", has1, has2)
	}
}
