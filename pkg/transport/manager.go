package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v3"
	"github.com/unicompute/obj1/pkg/instrumentation"
	"github.com/unicompute/obj1/pkg/nat"
	"github.com/unicompute/obj1/pkg/protocol"
	"github.com/unicompute/obj1/pkg/signaling"
)

// Manager manages WebRTC API lifecycle, peer connection establishment,
// and signaling coordination.
type Manager struct {
	mu           sync.Mutex
	cfg          Config
	api          *webrtc.API
	sigClient    *signaling.Client
	tracker      *instrumentation.Tracker
	connections  map[string]*PeerConnection // sessionID -> PeerConnection
	peerSessions map[string]string          // remotePeerID -> sessionID
	onNewConn    func(*PeerConnection)
	closed       bool
}

// NewManager creates a new WebRTC transport manager with PDF-specified engine tuning.
func NewManager(cfg Config, sigClient *signaling.Client, tracker *instrumentation.Tracker) (*Manager, error) {
	if cfg.FallbackTimeout == 0 {
		cfg.FallbackTimeout = 2000 * time.Millisecond // 2.0s Fast Fallback ceiling (PDF Section 5)
	}

	settingEngine := webrtc.SettingEngine{}

	// Fix 1: SCTP receive buffer at 16 MiB (PDF Section 6.3)
	settingEngine.SetSCTPMaxReceiveBufferSize(16 * 1024 * 1024)

	// Section 6.3 note: In Pion v3, SCTP transport defaults to 64 KB message size;
	// chunk streaming frames are segmented at 60 KiB in SendChunk to prevent truncation.

	// Fix 3: 15-second keepalive intervals to prevent home router NAT mapping timeouts (PDF Section 5 & 6.1)
	settingEngine.SetICETimeouts(20*time.Second, 25*time.Second, 15*time.Second)

	// Fix 4: Host candidates from virtual adapters pruned via SetInterfaceFilter (PDF Section 6.3)
	settingEngine.SetInterfaceFilter(nat.InterfaceFilter())

	// Setting up the API
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	if tracker == nil {
		tracker = instrumentation.NewTracker()
	}

	m := &Manager{
		cfg:          cfg,
		api:          api,
		sigClient:    sigClient,
		tracker:      tracker,
		connections:  make(map[string]*PeerConnection),
		peerSessions: make(map[string]string),
	}

	return m, nil
}

// SetOnNewConnection sets the handler for incoming peer connections.
func (m *Manager) SetOnNewConnection(fn func(*PeerConnection)) {
	m.mu.Lock()
	m.onNewConn = fn
	m.mu.Unlock()
}

// GetTracker returns the connection telemetry tracker.
func (m *Manager) GetTracker() *instrumentation.Tracker {
	return m.tracker
}

// buildICEServers converts config STUN and TURN entries to Pion ICEServers.
func (m *Manager) buildICEServers() []webrtc.ICEServer {
	var servers []webrtc.ICEServer

	if len(m.cfg.STUNServers) > 0 {
		servers = append(servers, webrtc.ICEServer{
			URLs: m.cfg.STUNServers,
		})
	}

	for _, turn := range m.cfg.TURNServers {
		url := turn.URL
		if !strings.HasPrefix(url, "turn:") && !strings.HasPrefix(url, "turns:") {
			url = "turn:" + url
		}
		servers = append(servers, webrtc.ICEServer{
			URLs:       []string{url},
			Username:   turn.Username,
			Credential: turn.Credential,
		})
	}

	return servers
}

// ConnectPeer initiates an outbound connection to a target peer using Trickle ICE.
func (m *Manager) ConnectPeer(ctx context.Context, targetPeerID string) (*PeerConnection, error) {
	sessionID := uuid.New().String()

	webrtcCfg := webrtc.Configuration{
		ICEServers: m.buildICEServers(),
	}

	pc, err := m.api.NewPeerConnection(webrtcCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create PeerConnection: %w", err)
	}

	conn := newPeerConnection(sessionID, m.cfg.NodeID, targetPeerID, pc, m.tracker)
	conn.setupHandlers()

	m.mu.Lock()
	m.connections[sessionID] = conn
	m.peerSessions[targetPeerID] = sessionID
	m.mu.Unlock()

	// ICE Candidate gathering & Trickle signaling
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil || m.sigClient == nil {
			return
		}
		candidateJSON := c.ToJSON()
		payload := protocol.ICECandidatePayload{
			SessionID: sessionID,
			Candidate: candidateJSON.Candidate,
		}
		if candidateJSON.SDPMid != nil {
			payload.SDPMid = *candidateJSON.SDPMid
		}
		if candidateJSON.SDPMLineIndex != nil {
			payload.SDPMLineIndex = *candidateJSON.SDPMLineIndex
		}

		rawPayload, _ := json.Marshal(payload)
		_ = m.sigClient.Send(protocol.Envelope{
			Type:     protocol.MsgICECandidate,
			SenderID: m.cfg.NodeID,
			TargetID: targetPeerID,
			Payload:  rawPayload,
		})
	})

	// Create Dual DataChannels:
	// Channel A: "stream" (ordered, maxRetransmits=3)
	maxRetransmits := uint16(3)
	streamDC, err := pc.CreateDataChannel("stream", &webrtc.DataChannelInit{
		Ordered:        func(b bool) *bool { return &b }(true),
		MaxRetransmits: &maxRetransmits,
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to create stream DataChannel: %w", err)
	}
	conn.bindDataChannel(streamDC)

	// Channel B: "chunk" (ordered, reliable, no packet loss)
	chunkDC, err := pc.CreateDataChannel("chunk", &webrtc.DataChannelInit{
		Ordered: func(b bool) *bool { return &b }(true),
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to create chunk DataChannel: %w", err)
	}
	conn.bindDataChannel(chunkDC)

	// Create SDP Offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to create offer: %w", err)
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to set local description: %w", err)
	}

	// Transmit Offer via signaling
	if m.sigClient != nil {
		sdpPayload, _ := json.Marshal(protocol.SDPPayload{
			SessionID: sessionID,
			Type:      offer.Type.String(),
			SDP:       offer.SDP,
		})
		_ = m.sigClient.Send(protocol.Envelope{
			Type:     protocol.MsgSDPOffer,
			SenderID: m.cfg.NodeID,
			TargetID: targetPeerID,
			Payload:  sdpPayload,
		})
	}

	// Fast Fallback Ladder wait with context
	fallbackCtx, cancelFallback := context.WithTimeout(ctx, m.cfg.FallbackTimeout)
	defer cancelFallback()

	connErr := conn.WaitConnected(fallbackCtx)
	if connErr != nil {
		// If timeout reached before direct connection converged, connection can either
		// continue waiting or activate relay fallback
		if errors.Is(connErr, context.DeadlineExceeded) {
			// Wait on overall context if direct connection is still progressing
			connErr = conn.WaitConnected(ctx)
		}
		if connErr != nil {
			return nil, connErr
		}
	}

	if err := conn.WaitChannelsReady(ctx); err != nil {
		return nil, err
	}

	return conn, nil
}

// HandleSignalingEnvelope processes incoming signaling messages.
func (m *Manager) HandleSignalingEnvelope(env protocol.Envelope) error {
	switch env.Type {
	case protocol.MsgSDPOffer:
		var sdp protocol.SDPPayload
		if err := json.Unmarshal(env.Payload, &sdp); err != nil {
			return err
		}
		return m.handleOffer(env.SenderID, sdp)

	case protocol.MsgSDPAnswer:
		var sdp protocol.SDPPayload
		if err := json.Unmarshal(env.Payload, &sdp); err != nil {
			return err
		}
		return m.handleAnswer(sdp)

	case protocol.MsgICECandidate:
		var cand protocol.ICECandidatePayload
		if err := json.Unmarshal(env.Payload, &cand); err != nil {
			return err
		}
		return m.handleCandidate(cand)
	}

	return nil
}

func (m *Manager) handleOffer(remotePeerID string, sdp protocol.SDPPayload) error {
	webrtcCfg := webrtc.Configuration{
		ICEServers: m.buildICEServers(),
	}

	pc, err := m.api.NewPeerConnection(webrtcCfg)
	if err != nil {
		return err
	}

	conn := newPeerConnection(sdp.SessionID, m.cfg.NodeID, remotePeerID, pc, m.tracker)
	conn.setupHandlers()

	m.mu.Lock()
	m.connections[sdp.SessionID] = conn
	m.peerSessions[remotePeerID] = sdp.SessionID
	m.mu.Unlock()

	// Answerer listens for DataChannels created by the offerer
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		conn.bindDataChannel(dc)
	})

	// Trickle ICE candidates
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil || m.sigClient == nil {
			return
		}
		candidateJSON := c.ToJSON()
		payload := protocol.ICECandidatePayload{
			SessionID: sdp.SessionID,
			Candidate: candidateJSON.Candidate,
		}
		if candidateJSON.SDPMid != nil {
			payload.SDPMid = *candidateJSON.SDPMid
		}
		if candidateJSON.SDPMLineIndex != nil {
			payload.SDPMLineIndex = *candidateJSON.SDPMLineIndex
		}

		rawPayload, _ := json.Marshal(payload)
		_ = m.sigClient.Send(protocol.Envelope{
			Type:     protocol.MsgICECandidate,
			SenderID: m.cfg.NodeID,
			TargetID: remotePeerID,
			Payload:  rawPayload,
		})
	})

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp.SDP,
	}

	if err := pc.SetRemoteDescription(offer); err != nil {
		return err
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return err
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		return err
	}

	if m.sigClient != nil {
		answerPayload, _ := json.Marshal(protocol.SDPPayload{
			SessionID: sdp.SessionID,
			Type:      answer.Type.String(),
			SDP:       answer.SDP,
		})
		_ = m.sigClient.Send(protocol.Envelope{
			Type:     protocol.MsgSDPAnswer,
			SenderID: m.cfg.NodeID,
			TargetID: remotePeerID,
			Payload:  answerPayload,
		})
	}

	m.mu.Lock()
	handler := m.onNewConn
	m.mu.Unlock()

	if handler != nil {
		go handler(conn)
	}

	return nil
}

func (m *Manager) handleAnswer(sdp protocol.SDPPayload) error {
	m.mu.Lock()
	conn, ok := m.connections[sdp.SessionID]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("unknown session: %s", sdp.SessionID)
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp.SDP,
	}

	return conn.PC.SetRemoteDescription(answer)
}

func (m *Manager) handleCandidate(cand protocol.ICECandidatePayload) error {
	m.mu.Lock()
	conn, ok := m.connections[cand.SessionID]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("unknown session: %s", cand.SessionID)
	}

	init := webrtc.ICECandidateInit{
		Candidate:     cand.Candidate,
		SDPMid:        &cand.SDPMid,
		SDPMLineIndex: &cand.SDPMLineIndex,
	}

	return conn.PC.AddICECandidate(init)
}

// GetConnection returns an active connection to a remote peer.
func (m *Manager) GetConnection(peerID string) (*PeerConnection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessionID, ok := m.peerSessions[peerID]
	if !ok {
		return nil, false
	}
	conn, ok := m.connections[sessionID]
	return conn, ok
}

// Close closes all peer connections and shuts down the transport manager.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true

	for _, conn := range m.connections {
		_ = conn.Close()
	}
	m.connections = make(map[string]*PeerConnection)
	m.peerSessions = make(map[string]string)
	m.mu.Unlock()

	return nil
}
