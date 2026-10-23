package relay

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestContributorRelay_EndToEnd(t *testing.T) {
	relay, err := NewContributorRelay(ServerConfig{
		ListenAddr:     "127.0.0.1:0",
		SessionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to start contributor relay: %v", err)
	}
	defer relay.Close()

	relayAddr := relay.Addr().String()
	token := GenerateSessionToken()

	client := &RelayClient{}

	var conn1, conn2 io.ReadWriteCloser
	var err1, err2 error
	var wg sync.WaitGroup

	wg.Add(2)

	// Peer 1 creates session
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn1, err1 = client.CreateRelayedSession(ctx, relayAddr, token)
	}()

	// Slight delay before Peer 2 joins
	time.Sleep(50 * time.Millisecond)

	// Peer 2 joins session
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn2, err2 = client.JoinRelayedSession(ctx, relayAddr, token)
	}()

	wg.Wait()

	if err1 != nil {
		t.Fatalf("Peer 1 CreateRelayedSession failed: %v", err1)
	}
	defer conn1.Close()

	if err2 != nil {
		t.Fatalf("Peer 2 JoinRelayedSession failed: %v", err2)
	}
	defer conn2.Close()

	// Exchange data bidirectionally
	msg1 := []byte("hello from peer 1 across blind relay")
	msg2 := []byte("hello back from peer 2 across blind relay")

	_, err = conn1.Write(msg1)
	if err != nil {
		t.Fatalf("conn1 write failed: %v", err)
	}

	buf2 := make([]byte, len(msg1))
	_, err = io.ReadFull(conn2, buf2)
	if err != nil {
		t.Fatalf("conn2 read failed: %v", err)
	}
	if !bytes.Equal(buf2, msg1) {
		t.Fatalf("conn2 received %q, want %q", string(buf2), string(msg1))
	}

	_, err = conn2.Write(msg2)
	if err != nil {
		t.Fatalf("conn2 write failed: %v", err)
	}

	buf1 := make([]byte, len(msg2))
	_, err = io.ReadFull(conn1, buf1)
	if err != nil {
		t.Fatalf("conn1 read failed: %v", err)
	}
	if !bytes.Equal(buf1, msg2) {
		t.Fatalf("conn1 received %q, want %q", string(buf1), string(msg2))
	}

	time.Sleep(50 * time.Millisecond)
	total := relay.TotalRelayedBytes()
	expectedTotal := int64(len(msg1) + len(msg2))
	if total < expectedTotal {
		t.Errorf("expected at least %d relayed bytes, got %d", expectedTotal, total)
	}
}

func TestContributorRelay_NotFoundOrTimeout(t *testing.T) {
	relay, err := NewContributorRelay(ServerConfig{
		ListenAddr:     "127.0.0.1:0",
		SessionTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("failed to start contributor relay: %v", err)
	}
	defer relay.Close()

	relayAddr := relay.Addr().String()
	client := &RelayClient{}

	// Join nonexistent session
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err = client.JoinRelayedSession(ctx, relayAddr, "nonexistent-token")
	if err == nil {
		t.Fatalf("expected error joining nonexistent session, got nil")
	}

	// Create session and let it timeout without peer
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	_, err = client.CreateRelayedSession(ctx2, relayAddr, "timeout-token")
	if err == nil {
		t.Fatalf("expected error on session timeout, got nil")
	}
}

func TestEmbeddedTURN_Lifecycle(t *testing.T) {
	turnSrv, err := NewEmbeddedTURN(TURNConfig{
		PublicIP: "127.0.0.1",
		Port:     0, // ephemeral port
		Realm:    "test.local",
		Users: map[string]string{
			"testuser": "testpass",
		},
	})
	if err != nil {
		t.Fatalf("failed to start embedded TURN: %v", err)
	}

	if turnSrv.Addr() == "" {
		t.Fatalf("expected non-empty turn address")
	}

	err = turnSrv.Close()
	if err != nil {
		t.Fatalf("failed to close embedded TURN: %v", err)
	}
}

func TestEmbeddedTURN_RESTCredentials(t *testing.T) {
	secret := "unicompute-super-secret-key-2026"
	username := "volunteer-node-42"
	user, pw := GenerateRESTCredentials(username, secret, 3600)

	if pw == secret {
		t.Fatalf("password must be HMAC-SHA1 digest, not raw secret")
	}
	if len(pw) == 0 {
		t.Fatalf("expected non-empty HMAC-SHA1 password")
	}

	turnSrv, err := NewEmbeddedTURN(TURNConfig{
		PublicIP: "127.0.0.1",
		Port:     0,
		Realm:    "unicompute.org",
		Secret:   secret,
	})
	if err != nil {
		t.Fatalf("failed to start TURN with secret: %v", err)
	}
	defer turnSrv.Close()

	if turnSrv.Addr() == "" {
		t.Fatalf("expected active turn address")
	}
	_ = user
}
