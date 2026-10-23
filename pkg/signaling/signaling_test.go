package signaling

import (
	"encoding/json"
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
