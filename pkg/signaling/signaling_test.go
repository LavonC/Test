package signaling

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	readChallenge := func(c *websocket.Conn) string {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read MsgChallenge: %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil || env.Type != protocol.MsgChallenge {
			t.Fatalf("expected MsgChallenge, got %s (err=%v)", string(raw), err)
		}
		var ch protocol.ChallengePayload
		if err := json.Unmarshal(env.Payload, &ch); err != nil || ch.Nonce == "" {
			t.Fatalf("invalid challenge payload: %s", string(env.Payload))
		}
		return ch.Nonce
	}

	nonce := readChallenge(conn)

	sendReg := func(nodeID, regNonce string, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
		sig := ed25519.Sign(priv, []byte(nodeID+":"+regNonce))
		pl, _ := json.Marshal(protocol.RegisterPayload{
			NodeID:    nodeID,
			Nonce:     regNonce,
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

	sendReg("dup-conn-node-1", nonce, pubA, privA)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, ackBytes, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected initial RegisterAck, got err: %v", err)
	}
	if strings.Contains(string(ackBytes), "error") {
		t.Fatalf("initial registration unexpectedly rejected: %s", string(ackBytes))
	}

	// Second MsgRegister on the same WebSocket connection must be rejected and clean up node-1
	sendReg("dup-conn-node-2", nonce, pubB, privB)
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

func TestHub_NonceReplayRejection(t *testing.T) {
	hub, err := NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Connection 1 obtains nonce1
	conn1, _, err := websocket.DefaultDialer.Dial(hub.URL(), nil)
	if err != nil {
		t.Fatalf("Dial 1 failed: %v", err)
	}
	defer conn1.Close()

	_ = conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw1, err := conn1.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage 1 failed: %v", err)
	}
	var env1 protocol.Envelope
	_ = json.Unmarshal(raw1, &env1)
	var ch1 protocol.ChallengePayload
	_ = json.Unmarshal(env1.Payload, &ch1)
	if len(ch1.Nonce) != 64 {
		t.Fatalf("expected 64-char hex 32-byte nonce, got %q", ch1.Nonce)
	}

	// Sign nodeID + ":" + nonce1
	sig1 := ed25519.Sign(priv, []byte("replay-node:"+ch1.Nonce))
	regPayload, _ := json.Marshal(protocol.RegisterPayload{
		NodeID:    "replay-node",
		Nonce:     ch1.Nonce,
		PublicKey: hex.EncodeToString(pub),
		Signature: hex.EncodeToString(sig1),
	})
	regEnv, _ := json.Marshal(protocol.Envelope{
		Type:     protocol.MsgRegister,
		SenderID: "replay-node",
		Payload:  regPayload,
	})

	// Connection 2 gets nonce2, but attacker replays signed registration from nonce1!
	conn2, _, err := websocket.DefaultDialer.Dial(hub.URL(), nil)
	if err != nil {
		t.Fatalf("Dial 2 failed: %v", err)
	}
	defer conn2.Close()
	_ = conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn2.ReadMessage() // drain conn2's own MsgChallenge
	if err != nil {
		t.Fatalf("ReadMessage 2 failed: %v", err)
	}

	if err := conn2.WriteMessage(websocket.TextMessage, regEnv); err != nil {
		t.Fatalf("WriteMessage replay failed: %v", err)
	}

	rejected := false
	for {
		_, msg, readErr := conn2.ReadMessage()
		if readErr != nil {
			if closeErr, ok := readErr.(*websocket.CloseError); ok && closeErr.Code == websocket.ClosePolicyViolation {
				rejected = true
			}
			break
		}
		if strings.Contains(string(msg), "invalid or replayed registration nonce") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("expected replayed nonce on conn2 to be rejected")
	}

	hub.mu.RLock()
	_, registered := hub.clients["replay-node"]
	hub.mu.RUnlock()
	if registered {
		t.Fatalf("expected replay-node NOT to be registered after nonce replay")
	}
}

func TestHub_PersistentTOFUAndTURNCredentials(t *testing.T) {
	pinnedPath := filepath.Join(t.TempDir(), "pinned_keys.json")
	const turnSecret = "hub-turn-secret-999"
	const turnURL = "turn:198.51.100.10:3478"

	hub1, err := NewHubWithConfig(HubConfig{
		ListenAddr:     "127.0.0.1:0",
		PinnedKeysPath: pinnedPath,
		TURNURL:        turnURL,
		TURNSecret:     turnSecret,
		TURNTTLSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("NewHubWithConfig 1 failed: %v", err)
	}

	_, legitPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, attackerPriv, _ := ed25519.GenerateKey(rand.Reader)

	ackCh := make(chan protocol.Envelope, 2)
	c1, err := NewClientWithKey(hub1.URL(), "persist-node", legitPriv, func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			ackCh <- env
		}
	})
	if err != nil {
		t.Fatalf("NewClientWithKey failed: %v", err)
	}

	select {
	case ack := <-ackCh:
		var ackPayload protocol.RegisterAckPayload
		if err := json.Unmarshal(ack.Payload, &ackPayload); err != nil {
			t.Fatalf("failed to unmarshal RegisterAckPayload: %v", err)
		}
		if len(ackPayload.TURNServers) != 1 {
			t.Fatalf("expected 1 TURN server in RegisterAck, got %d", len(ackPayload.TURNServers))
		}
		ts := ackPayload.TURNServers[0]
		if ts.URL != turnURL {
			t.Errorf("expected TURN URL %q, got %q", turnURL, ts.URL)
		}
		mac := hmac.New(sha1.New, []byte(turnSecret))
		mac.Write([]byte(ts.Username))
		expectedCred := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		if ts.Credential != expectedCred {
			t.Errorf("TURN credential mismatch: got %q, want %q", ts.Credential, expectedCred)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for RegisterAck with TURN credentials")
	}

	if len(c1.TURNServers()) != 1 {
		t.Fatalf("expected Client.TURNServers() to store 1 TURN server")
	}
	_ = c1.Close()
	_ = hub1.Close()

	// Restart a new Hub loading from the same pinnedPath
	hub2, err := NewHubWithConfig(HubConfig{
		ListenAddr:     "127.0.0.1:0",
		PinnedKeysPath: pinnedPath,
	})
	if err != nil {
		t.Fatalf("NewHubWithConfig 2 failed: %v", err)
	}
	defer hub2.Close()

	// Attacker with different key must be rejected by persisted TOFU key
	attAck := make(chan protocol.Envelope, 2)
	attClient, err := NewClientWithKey(hub2.URL(), "persist-node", attackerPriv, func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			attAck <- env
		}
	})
	if err != nil {
		t.Fatalf("attacker dial failed: %v", err)
	}
	defer attClient.Close()

	select {
	case ack := <-attAck:
		if !strings.Contains(string(ack.Payload), "nodeID public key mismatch") {
			t.Fatalf("expected persisted TOFU rejection, got: %s", string(ack.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for persisted TOFU rejection")
	}

	// Legitimate node reconnects with original key
	legitAck := make(chan protocol.Envelope, 2)
	c2, err := NewClientWithKey(hub2.URL(), "persist-node", legitPriv, func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			legitAck <- env
		}
	})
	if err != nil {
		t.Fatalf("legitimate reconnect failed: %v", err)
	}
	defer c2.Close()

	select {
	case ack := <-legitAck:
		if strings.Contains(string(ack.Payload), "error") {
			t.Fatalf("legitimate reconnect rejected: %s", string(ack.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for legitimate reconnect ack")
	}
}

func TestHub_ReadLimitEnforced(t *testing.T) {
	hub, err := NewHub("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	conn, _, err := websocket.DefaultDialer.Dial(hub.URL(), nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Drain MsgChallenge
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("failed reading challenge: %v", err)
	}

	// Send > 256 KiB payload; Hub SetReadLimit(256*1024) must close the connection
	oversized := make([]byte, 300*1024)
	for i := range oversized {
		oversized[i] = 'A'
	}
	_ = conn.WriteMessage(websocket.TextMessage, oversized)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, readErr := conn.ReadMessage()
	if readErr == nil {
		t.Fatalf("expected connection to be closed after exceeding 256 KiB read limit")
	}
}

func TestHub_ConcurrentPersistentTOFU_NoLostKeys(t *testing.T) {
	pinnedPath := filepath.Join(t.TempDir(), "concurrent_pinned.json")
	hub, err := NewHubWithConfig(HubConfig{
		ListenAddr:     "127.0.0.1:0",
		PinnedKeysPath: pinnedPath,
	})
	if err != nil {
		t.Fatalf("NewHubWithConfig failed: %v", err)
	}
	defer hub.Close()

	const totalNodes = 12
	var wg sync.WaitGroup
	clients := make([]*Client, totalNodes)
	for i := 0; i < totalNodes; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ackCh := make(chan struct{}, 1)
			c, err := NewClient(hub.URL(), "conc-node-"+string(rune('A'+idx)), func(env protocol.Envelope) {
				if env.Type == protocol.MsgRegisterAck {
					select {
					case ackCh <- struct{}{}:
					default:
					}
				}
			})
			if err != nil {
				t.Errorf("NewClient %d failed: %v", idx, err)
				return
			}
			clients[idx] = c
			select {
			case <-ackCh:
			case <-time.After(3 * time.Second):
				t.Errorf("timed out waiting for RegisterAck %d", idx)
			}
		}(i)
	}
	wg.Wait()
	for _, c := range clients {
		if c != nil {
			_ = c.Close()
		}
	}

	raw, err := os.ReadFile(pinnedPath)
	if err != nil {
		t.Fatalf("ReadFile pinned keys failed: %v", err)
	}
	var saved map[string]string
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("Unmarshal pinned keys failed: %v", err)
	}
	if len(saved) != totalNodes {
		t.Fatalf("expected %d persisted pinned keys without overwrite loss, got %d (%v)", totalNodes, len(saved), saved)
	}
}

func TestClient_ReconnectStopsOnUnauthorized401(t *testing.T) {
	hub, err := NewHub("127.0.0.1:0", "valid-secret")
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	defer hub.Close()

	var attempts atomic.Int32
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer proxyLn.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		}),
	}
	go func() { _ = srv.Serve(proxyLn) }()
	defer srv.Close()

	c, err := NewClient("ws://"+proxyLn.Addr().String()+"/ws", "bad-auth-node", nil, "wrong")
	if err == nil || c != nil {
		t.Fatalf("expected NewClient to fail with 401, got c=%v err=%v", c, err)
	}

	// Simulate reconnect loop hitting 401 after initial connection succeeded
	c2, err := NewClient(hub.URL(), "reconn-401-node", nil, "valid-secret")
	if err != nil {
		t.Fatalf("NewClient valid failed: %v", err)
	}
	defer c2.Close()

	// Point client at 401 endpoint and drop conn to trigger reconnect
	c2.mu.Lock()
	c2.url = "ws://" + proxyLn.Addr().String() + "/ws"
	oldConn := c2.conn
	c2.conn = nil
	c2.mu.Unlock()
	if oldConn != nil {
		_ = oldConn.Close()
	}

	// Wait for 1 reconnect attempt (~500ms-750ms) and verify client stops retrying (closed=true)
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		c2.mu.Lock()
		closed := c2.closed
		c2.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	c2.mu.Lock()
	closed := c2.closed
	c2.mu.Unlock()
	if !closed {
		t.Fatalf("expected signaling client to close and stop reconnecting after HTTP 401")
	}
	attemptsBefore := attempts.Load()
	time.Sleep(900 * time.Millisecond)
	if attemptsAfter := attempts.Load(); attemptsAfter != attemptsBefore {
		t.Fatalf("expected no further reconnect attempts after 401 stop, before=%d after=%d", attemptsBefore, attemptsAfter)
	}
}

func TestSignaling_TURNCredentialRefresh(t *testing.T) {
	const turnSecret = "refresh-turn-secret-123"
	const turnURL = "turn:198.51.100.20:3478"

	hub, err := NewHubWithConfig(HubConfig{
		ListenAddr:     "127.0.0.1:0",
		TURNURL:        turnURL,
		TURNSecret:     turnSecret,
		TURNTTLSeconds: 2, // 75% = 1.5s refresh interval
	})
	if err != nil {
		t.Fatalf("NewHubWithConfig failed: %v", err)
	}
	defer hub.Close()

	refreshAckCh := make(chan protocol.TURNRefreshAckPayload, 4)
	regAckCh := make(chan struct{}, 1)

	client, err := NewClient(hub.URL(), "turn-refresh-node", func(env protocol.Envelope) {
		switch env.Type {
		case protocol.MsgRegisterAck:
			select {
			case regAckCh <- struct{}{}:
			default:
			}
		case protocol.MsgTURNRefreshAck:
			var pl protocol.TURNRefreshAckPayload
			if json.Unmarshal(env.Payload, &pl) == nil {
				refreshAckCh <- pl
			}
		}
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	select {
	case <-regAckCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for initial RegisterAck")
	}

	initServers := client.TURNServers()
	if len(initServers) != 1 {
		t.Fatalf("expected 1 initial TURN server, got %d", len(initServers))
	}
	initUser := initServers[0].Username
	initExpiryStr := strings.SplitN(initUser, ":", 2)[0]
	initExpiry, err := strconv.ParseInt(initExpiryStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid initial TURN username %q: %v", initUser, err)
	}

	// Wait for automatic refresh at ~75% of 2s (= 1.5s)
	var refreshed protocol.TURNRefreshAckPayload
	select {
	case refreshed = <-refreshAckCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for automatic MsgTURNRefreshAck")
	}

	if len(refreshed.TURNServers) != 1 {
		t.Fatalf("expected 1 refreshed TURN server, got %d", len(refreshed.TURNServers))
	}
	ts := refreshed.TURNServers[0]
	if ts.URL != turnURL {
		t.Errorf("expected TURN URL %q, got %q", turnURL, ts.URL)
	}
	mac := hmac.New(sha1.New, []byte(turnSecret))
	mac.Write([]byte(ts.Username))
	expectedCred := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if ts.Credential != expectedCred {
		t.Errorf("refreshed TURN credential mismatch: got %q, want %q", ts.Credential, expectedCred)
	}

	newExpiryStr := strings.SplitN(ts.Username, ":", 2)[0]
	newExpiry, err := strconv.ParseInt(newExpiryStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid refreshed TURN username %q: %v", ts.Username, err)
	}
	if newExpiry <= initExpiry {
		t.Errorf("expected refreshed expiry (%d) > initial expiry (%d)", newExpiry, initExpiry)
	}

	latest := client.TURNServers()
	if len(latest) != 1 || latest[0].Username != ts.Username || latest[0].Credential != ts.Credential {
		t.Errorf("expected Client.TURNServers() to update to refreshed creds %+v, got %+v", ts, latest)
	}
}

func TestClient_QueryPeers_RedirectTokenStripping(t *testing.T) {
	const secretToken = "query-peers-secret-token-xyz"

	// 1. Cross-host redirect must be refused without hitting external host
	var evilHit atomic.Bool
	var evilAuth atomic.Value
	evilSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHit.Store(true)
		evilAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count":0,"nodes":[]}`))
	}))
	defer evilSrv.Close()

	crossCoord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/nodes" {
			http.Redirect(w, r, evilSrv.URL+"/steal", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer crossCoord.Close()

	c1 := &Client{
		url:       "ws://" + strings.TrimPrefix(crossCoord.URL, "http://") + "/ws",
		nodeID:    "query-node-1",
		authToken: secretToken,
		sendQueue: make(chan []byte, 4),
	}
	_, _ = c1.QueryPeers(context.Background())
	if evilHit.Load() {
		t.Fatalf("security violation: cross-host redirect target was hit")
	}
	if v, ok := evilAuth.Load().(string); ok && v != "" {
		t.Fatalf("security violation: Authorization leaked across cross-host redirect: %q", v)
	}

	// 2. Same-host redirect must strip Authorization header
	var sameHostAuth atomic.Value
	var sameHostHit atomic.Bool
	sameCoord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/nodes":
			http.Redirect(w, r, "/redirected-nodes", http.StatusFound)
		case "/redirected-nodes":
			sameHostHit.Store(true)
			sameHostAuth.Store(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"count":1,"nodes":["peer-redir"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer sameCoord.Close()

	c2 := &Client{
		url:       "ws://" + strings.TrimPrefix(sameCoord.URL, "http://") + "/ws",
		nodeID:    "query-node-2",
		authToken: secretToken,
		sendQueue: make(chan []byte, 4),
	}
	nodes, err := c2.QueryPeers(context.Background())
	if err != nil {
		t.Fatalf("QueryPeers same-host redirect failed: %v", err)
	}
	if !sameHostHit.Load() {
		t.Fatalf("expected same-host redirect target to be reached")
	}
	if v, _ := sameHostAuth.Load().(string); v != "" {
		t.Fatalf("security violation: Authorization header was not stripped on same-host redirect: %q", v)
	}
	if len(nodes) != 1 || nodes[0] != "peer-redir" {
		t.Fatalf("expected [peer-redir], got %v", nodes)
	}
}

func TestHub_BoundedClientAndPinnedKeyCaps(t *testing.T) {
	hub, err := NewHubWithConfig(HubConfig{
		ListenAddr:    "127.0.0.1:0",
		MaxClients:    2,
		MaxPinnedKeys: 3,
	})
	if err != nil {
		t.Fatalf("NewHubWithConfig failed: %v", err)
	}
	defer hub.Close()

	c1, err := NewClient(hub.URL(), "cap-node-1", nil)
	if err != nil {
		t.Fatalf("c1 failed: %v", err)
	}
	c2, err := NewClient(hub.URL(), "cap-node-2", nil)
	if err != nil {
		t.Fatalf("c2 failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 3rd concurrent client exceeds MaxClients=2
	c3Ack := make(chan string, 1)
	c3, err := NewClient(hub.URL(), "cap-node-3", func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			c3Ack <- string(env.Payload)
		}
	})
	if err != nil {
		t.Fatalf("c3 dial failed: %v", err)
	}
	select {
	case pl := <-c3Ack:
		if !strings.Contains(pl, "hub client capacity exceeded") {
			t.Fatalf("expected client capacity exceeded error, got %s", pl)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for client capacity rejection")
	}
	_ = c3.Close()
	_ = c1.Close()
	_ = c2.Close()
	time.Sleep(100 * time.Millisecond)

	// Register 3rd unique node (now pinnedKeys = 3 == MaxPinnedKeys)
	c3b, err := NewClient(hub.URL(), "cap-node-3", nil)
	if err != nil {
		t.Fatalf("c3b failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	_ = c3b.Close()
	time.Sleep(100 * time.Millisecond)

	// 4th unique node exceeds MaxPinnedKeys=3
	c4Ack := make(chan string, 1)
	c4, err := NewClient(hub.URL(), "cap-node-4", func(env protocol.Envelope) {
		if env.Type == protocol.MsgRegisterAck {
			c4Ack <- string(env.Payload)
		}
	})
	if err != nil {
		t.Fatalf("c4 dial failed: %v", err)
	}
	defer c4.Close()
	select {
	case pl := <-c4Ack:
		if !strings.Contains(pl, "hub pinned keys capacity exceeded") {
			t.Fatalf("expected pinned keys capacity exceeded error, got %s", pl)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for pinned keys capacity rejection")
	}

	pub4, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := hub.PinPublicKey("cap-node-5", pub4); err == nil {
		t.Fatalf("expected PinPublicKey to fail when pinned keys cap reached")
	}
	_ = fmt.Sprintf("verified")
}
