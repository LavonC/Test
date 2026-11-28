package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
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
	mu                 sync.Mutex
	cfg                Config
	api                *webrtc.API
	sigClient          *signaling.Client
	tracker            *instrumentation.Tracker
	privKey            ed25519.PrivateKey
	pubKey             ed25519.PublicKey
	pinnedPeerKeys     map[string]string // remotePeerID -> lowercase hex ed25519 pubkey
	dynamicTURNServers []TURNServerConfig
	connections        map[string]*PeerConnection       // sessionID -> PeerConnection
	peerSessions       map[string]string                // remotePeerID -> sessionID
	pendingCandidates  map[string][]webrtc.ICECandidateInit // sessionID -> early ICE candidates
	remoteDescSet      map[string]bool                  // sessionID -> whether SetRemoteDescription succeeded
	onNewConn          func(*PeerConnection)
	closed             bool
}

const maxPendingCandidatesPerSession = 64

// NewManager creates a new WebRTC transport manager with PDF-specified engine tuning.
func NewManager(cfg Config, sigClient *signaling.Client, tracker *instrumentation.Tracker) (*Manager, error) {
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

	var privKey ed25519.PrivateKey
	var pubKey ed25519.PublicKey
	if len(cfg.PrivateKey) == ed25519.PrivateKeySize {
		privKey = cfg.PrivateKey
		pubKey = privKey.Public().(ed25519.PublicKey)
	} else if sigClient != nil {
		pubKey = sigClient.PublicKey()
	} else {
		genPub, genPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("failed to generate transport identity key: %w", err)
		}
		privKey = genPriv
		pubKey = genPub
	}

	m := &Manager{
		cfg:               cfg,
		api:               api,
		sigClient:         sigClient,
		tracker:           tracker,
		privKey:           privKey,
		pubKey:            pubKey,
		pinnedPeerKeys:    make(map[string]string),
		connections:       make(map[string]*PeerConnection),
		peerSessions:      make(map[string]string),
		pendingCandidates: make(map[string][]webrtc.ICECandidateInit),
		remoteDescSet:     make(map[string]bool),
	}
	m.bindSignalingTURNRefresh(sigClient)

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

func (m *Manager) removeSession(peerID, sessionID string) {
	m.mu.Lock()
	removedActivePeer := false
	if cur, ok := m.peerSessions[peerID]; ok && cur == sessionID {
		delete(m.peerSessions, peerID)
		removedActivePeer = true
	}
	delete(m.connections, sessionID)
	delete(m.pendingCandidates, sessionID)
	delete(m.remoteDescSet, sessionID)
	m.mu.Unlock()

	if removedActivePeer && m.tracker != nil {
		m.tracker.RemoveConnection(peerID)
	}
}

func (m *Manager) getSignalingClient() *signaling.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sigClient
}

// formatICEURL formats and normalizes STUN/TURN URLs to standard RFC 7064/7065 URI schemes
// (e.g., stun:host:port, turn:host:port), resolving missing schemes, trimming double-slash syntax,
// bracketing IPv6 literals, and handling query parameters.
func formatICEURL(rawURL string, defaultScheme string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}

	scheme := strings.TrimSuffix(strings.ToLower(defaultScheme), ":")
	rest := rawURL

	lower := strings.ToLower(rawURL)
	switch {
	case strings.HasPrefix(lower, "stun://"):
		scheme = "stun"
		rest = rawURL[7:]
	case strings.HasPrefix(lower, "stuns://"):
		scheme = "stuns"
		rest = rawURL[8:]
	case strings.HasPrefix(lower, "turn://"):
		scheme = "turn"
		rest = rawURL[7:]
	case strings.HasPrefix(lower, "turns://"):
		scheme = "turns"
		rest = rawURL[8:]
	case strings.HasPrefix(lower, "stun:"):
		scheme = "stun"
		rest = rawURL[5:]
	case strings.HasPrefix(lower, "stuns:"):
		scheme = "stuns"
		rest = rawURL[6:]
	case strings.HasPrefix(lower, "turn:"):
		scheme = "turn"
		rest = rawURL[5:]
	case strings.HasPrefix(lower, "turns:"):
		scheme = "turns"
		rest = rawURL[6:]
	default:
		if strings.HasPrefix(rawURL, "//") {
			rest = rawURL[2:]
		}
	}

	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ""
	}

	// Extract query parameter if present (e.g., ?transport=tcp)
	var query string
	if qIdx := strings.Index(rest, "?"); qIdx != -1 {
		query = rest[qIdx:]
		rest = strings.TrimSpace(rest[:qIdx])
		if rest == "" {
			return ""
		}
	}

	// Normalize IPv6 literals to ensure proper bracketing required by RFC 7064/7065 and Pion
	if !strings.HasPrefix(rest, "[") {
		if ip := net.ParseIP(rest); ip != nil && strings.Contains(rest, ":") {
			// Unbracketed IPv6 without port (e.g., 2001:db8::1 or ::1)
			rest = "[" + rest + "]"
		} else if lastColon := strings.LastIndex(rest, ":"); lastColon != -1 {
			// Check for unbracketed IPv6 with port (e.g., 2001:db8::1:19302)
			possibleIP := rest[:lastColon]
			possiblePort := rest[lastColon+1:]
			if ip := net.ParseIP(possibleIP); ip != nil && strings.Contains(possibleIP, ":") {
				if _, err := strconv.Atoi(possiblePort); err == nil {
					rest = "[" + possibleIP + "]:" + possiblePort
				}
			}
		}
	}

	return scheme + ":" + rest + query
}

// buildICEServers converts config STUN and TURN entries (plus coordinator-issued TURN credentials) to Pion ICEServers.
func (m *Manager) buildICEServers() []webrtc.ICEServer {
	var servers []webrtc.ICEServer

	if len(m.cfg.STUNServers) > 0 {
		var stunURLs []string
		for _, s := range m.cfg.STUNServers {
			for _, part := range strings.Split(s, ",") {
				formatted := formatICEURL(part, "stun")
				if formatted != "" {
					stunURLs = append(stunURLs, formatted)
				}
			}
		}
		if len(stunURLs) > 0 {
			servers = append(servers, webrtc.ICEServer{
				URLs: stunURLs,
			})
		}
	}

	m.mu.Lock()
	allTURN := make([]TURNServerConfig, 0, len(m.cfg.TURNServers)+len(m.dynamicTURNServers))
	allTURN = append(allTURN, m.cfg.TURNServers...)
	allTURN = append(allTURN, m.dynamicTURNServers...)
	sig := m.sigClient
	m.mu.Unlock()

	if sig != nil {
		for _, ts := range sig.TURNServers() {
			allTURN = append(allTURN, TURNServerConfig{
				URL:        ts.URL,
				Username:   ts.Username,
				Credential: ts.Credential,
			})
		}
	}

	seenTURN := make(map[string]struct{}, len(allTURN))
	for _, turn := range allTURN {
		var turnURLs []string
		for _, part := range strings.Split(turn.URL, ",") {
			formatted := formatICEURL(part, "turn")
			if formatted != "" {
				turnURLs = append(turnURLs, formatted)
			}
		}
		if len(turnURLs) > 0 {
			key := strings.Join(turnURLs, ",") + "|" + turn.Username + "|" + turn.Credential
			if _, exists := seenTURN[key]; exists {
				continue
			}
			seenTURN[key] = struct{}{}
			servers = append(servers, webrtc.ICEServer{
				URLs:       turnURLs,
				Username:   turn.Username,
				Credential: turn.Credential,
			})
		}
	}

	return servers
}

// PublicKey returns the Ed25519 public key used by this transport Manager for DTLS identity binding.
func (m *Manager) PublicKey() ed25519.PublicKey {
	m.mu.Lock()
	pub := m.pubKey
	sig := m.sigClient
	m.mu.Unlock()
	if len(pub) == ed25519.PublicKeySize {
		cp := make(ed25519.PublicKey, ed25519.PublicKeySize)
		copy(cp, pub)
		return cp
	}
	if sig != nil {
		return sig.PublicKey()
	}
	return nil
}

// PinPeerKey pre-seeds or pins a remote peer's Ed25519 public key for DTLS fingerprint verification.
func (m *Manager) PinPeerKey(peerID string, pubKey ed25519.PublicKey) error {
	if peerID == "" || len(pubKey) != ed25519.PublicKeySize {
		return errors.New("invalid peerID or ed25519 public key")
	}
	m.mu.Lock()
	m.pinnedPeerKeys[peerID] = strings.ToLower(hex.EncodeToString(pubKey))
	m.mu.Unlock()
	return nil
}

// extractDTLSFingerprints extracts deterministic sorted unique `a=fingerprint:` attribute values from an SDP.
func extractDTLSFingerprints(sdp string) string {
	lines := strings.Split(sdp, "\n")
	seen := make(map[string]struct{})
	var fps []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(strings.ToLower(trimmed), "a=fingerprint:") {
			val := strings.TrimSpace(trimmed[len("a=fingerprint:"):])
			if val != "" {
				if _, ok := seen[val]; !ok {
					seen[val] = struct{}{}
					fps = append(fps, val)
				}
			}
		}
	}
	sort.Strings(fps)
	return strings.Join(fps, "\n")
}

func (m *Manager) signSDPPayload(sessionID, sdpType, sdpStr string) (protocol.SDPPayload, error) {
	fp := extractDTLSFingerprints(sdpStr)
	if fp == "" {
		return protocol.SDPPayload{}, errors.New("local SDP missing DTLS fingerprint")
	}

	m.mu.Lock()
	priv := m.privKey
	pub := m.pubKey
	sigClient := m.sigClient
	localCGNAT := m.cfg.IsBehindCGNAT
	m.mu.Unlock()

	var sigBytes []byte
	if len(priv) == ed25519.PrivateKeySize {
		sigBytes = ed25519.Sign(priv, []byte(fp))
	} else if sigClient != nil {
		pub = sigClient.PublicKey()
		sigBytes = sigClient.Sign([]byte(fp))
	} else {
		return protocol.SDPPayload{}, errors.New("no ed25519 identity key available to sign DTLS fingerprint")
	}

	return protocol.SDPPayload{
		SessionID:     sessionID,
		Type:          sdpType,
		SDP:           sdpStr,
		PublicKey:     hex.EncodeToString(pub),
		Signature:     hex.EncodeToString(sigBytes),
		IsBehindCGNAT: localCGNAT,
	}, nil
}

func (m *Manager) verifyAndPinDTLS(remotePeerID string, sdp protocol.SDPPayload) error {
	if remotePeerID == "" {
		return errors.New("remote peer ID is required for DTLS identity verification")
	}
	fp := extractDTLSFingerprints(sdp.SDP)
	if fp == "" {
		return errors.New("remote SDP missing DTLS fingerprint")
	}

	pubBytes, err := hex.DecodeString(sdp.PublicKey)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return errors.New("missing or invalid remote ed25519 public key in SDP")
	}
	sigBytes, err := hex.DecodeString(sdp.Signature)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return errors.New("missing or invalid DTLS fingerprint signature in SDP")
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), []byte(fp), sigBytes) {
		return errors.New("DTLS fingerprint signature verification failed")
	}

	normPub := strings.ToLower(hex.EncodeToString(pubBytes))

	m.mu.Lock()
	sigClient := m.sigClient
	m.mu.Unlock()

	if sigClient != nil {
		if knownPub, ok := sigClient.GetPeerPublicKey(remotePeerID); ok {
			if !bytes.Equal(knownPub, pubBytes) {
				return fmt.Errorf("DTLS public key mismatch for peer %s against signaling identity", remotePeerID)
			}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if pinnedPub, ok := m.pinnedPeerKeys[remotePeerID]; ok && pinnedPub != normPub {
		return fmt.Errorf("DTLS public key mismatch for peer %s against pinned key", remotePeerID)
	}
	m.pinnedPeerKeys[remotePeerID] = normPub
	return nil
}

func (m *Manager) bindPeerConnectionLifecycle(conn *PeerConnection, remotePeerID, sessionID string) {
	conn.setOnInternalStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateDisconnected:
			sig := m.getSignalingClient()
			if sig != nil && sig.IsConnected() {
				go m.triggerICERestart(remotePeerID, sessionID, conn)
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			m.removeSession(remotePeerID, sessionID)
		}
	})
}

func (m *Manager) triggerICERestart(remotePeerID, sessionID string, conn *PeerConnection) {
	if conn == nil || conn.PC == nil {
		return
	}
	conn.restartMu.Lock()
	if conn.restarting {
		conn.restartMu.Unlock()
		return
	}
	conn.restarting = true
	conn.restartMu.Unlock()

	defer func() {
		conn.restartMu.Lock()
		conn.restarting = false
		conn.restartMu.Unlock()
	}()

	m.mu.Lock()
	curSession, ok := m.peerSessions[remotePeerID]
	curConn := m.connections[sessionID]
	closed := m.closed
	m.mu.Unlock()
	if closed || !ok || curSession != sessionID || curConn != conn {
		return
	}

	sig := m.getSignalingClient()
	if sig == nil || !sig.IsConnected() {
		return
	}

	if conn.PC.SignalingState() != webrtc.SignalingStateStable {
		return
	}

	offer, err := conn.PC.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		return
	}
	if err := conn.PC.SetLocalDescription(offer); err != nil {
		return
	}
	sdpPayloadObj, err := m.signSDPPayload(sessionID, offer.Type.String(), offer.SDP)
	if err != nil {
		return
	}
	raw, _ := json.Marshal(sdpPayloadObj)
	_ = sig.Send(protocol.Envelope{
		Type:     protocol.MsgSDPOffer,
		SenderID: m.cfg.NodeID,
		TargetID: remotePeerID,
		Payload:  raw,
	})
}

// ConnectPeer initiates an outbound connection to a target peer using Trickle ICE.
func (m *Manager) ConnectPeer(ctx context.Context, targetPeerID string) (*PeerConnection, error) {
	if targetPeerID == "" || targetPeerID == m.cfg.NodeID {
		return nil, fmt.Errorf("invalid target peer ID: %q", targetPeerID)
	}

	sessionID := uuid.New().String()

	webrtcCfg := webrtc.Configuration{
		ICEServers: m.buildICEServers(),
	}

	pc, err := m.api.NewPeerConnection(webrtcCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create PeerConnection: %w", err)
	}

	conn := newPeerConnection(sessionID, m.cfg.NodeID, targetPeerID, pc, m.tracker)
	conn.chunkTempDir = m.cfg.ChunkTempDir
	conn.setLocalOffer(true)
	conn.setCGNATFlags(m.cfg.IsBehindCGNAT, false)
	conn.setupHandlers()

	m.mu.Lock()
	if oldSessionID, exists := m.peerSessions[targetPeerID]; exists && oldSessionID != sessionID {
		if oldConn, ok := m.connections[oldSessionID]; ok {
			delete(m.connections, oldSessionID)
			go func(c *PeerConnection) {
				_ = c.Close()
			}(oldConn)
		}
	}
	m.connections[sessionID] = conn
	m.peerSessions[targetPeerID] = sessionID
	onConnHandler := m.onNewConn
	m.mu.Unlock()

	m.bindPeerConnectionLifecycle(conn, targetPeerID, sessionID)

	// Register event callbacks (storage, streams) before creating DataChannels and Offer
	if onConnHandler != nil {
		onConnHandler(conn)
	}

	// ICE Candidate gathering & Trickle signaling
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		sig := m.getSignalingClient()
		if c == nil || sig == nil {
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
		_ = sig.Send(protocol.Envelope{
			Type:     protocol.MsgICECandidate,
			SenderID: m.cfg.NodeID,
			TargetID: targetPeerID,
			Payload:  rawPayload,
		})
	})

	// Create Dual DataChannels:
	// Channel A: "stream" is reliable because it carries control and job messages.
	streamDC, err := pc.CreateDataChannel("stream", &webrtc.DataChannelInit{
		Ordered: func(b bool) *bool { return &b }(true),
	})
	if err != nil {
		m.removeSession(targetPeerID, sessionID)
		_ = conn.Close()
		return nil, fmt.Errorf("failed to create stream DataChannel: %w", err)
	}
	conn.bindDataChannel(streamDC)

	// Channel B: "chunk" (ordered, reliable, no packet loss)
	chunkDC, err := pc.CreateDataChannel("chunk", &webrtc.DataChannelInit{
		Ordered: func(b bool) *bool { return &b }(true),
	})
	if err != nil {
		m.removeSession(targetPeerID, sessionID)
		_ = conn.Close()
		return nil, fmt.Errorf("failed to create chunk DataChannel: %w", err)
	}
	conn.bindDataChannel(chunkDC)

	waitSupersededWinner := func(winner *PeerConnection) (*PeerConnection, error) {
		if wErr := winner.WaitConnected(ctx); wErr != nil {
			return nil, fmt.Errorf("webrtc ICE connection to %s failed after glare resolution: %w", targetPeerID, wErr)
		}
		if wErr := winner.WaitChannelsReady(ctx); wErr != nil {
			return nil, wErr
		}
		return winner, nil
	}

	// Create SDP Offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		if winner := conn.getSupersededBy(); winner != nil {
			return waitSupersededWinner(winner)
		}
		m.removeSession(targetPeerID, sessionID)
		_ = conn.Close()
		return nil, fmt.Errorf("failed to create offer: %w", err)
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		if winner := conn.getSupersededBy(); winner != nil {
			return waitSupersededWinner(winner)
		}
		m.removeSession(targetPeerID, sessionID)
		_ = conn.Close()
		return nil, fmt.Errorf("failed to set local description: %w", err)
	}

	// Transmit Offer via signaling with DTLS fingerprint Ed25519 signature (unless already superseded by glare)
	if winner := conn.getSupersededBy(); winner != nil {
		return waitSupersededWinner(winner)
	}
	if sig := m.getSignalingClient(); sig != nil {
		sdpPayloadObj, err := m.signSDPPayload(sessionID, offer.Type.String(), offer.SDP)
		if err != nil {
			m.removeSession(targetPeerID, sessionID)
			_ = conn.Close()
			return nil, fmt.Errorf("failed to sign SDP offer: %w", err)
		}
		sdpPayload, _ := json.Marshal(sdpPayloadObj)
		_ = sig.Send(protocol.Envelope{
			Type:     protocol.MsgSDPOffer,
			SenderID: m.cfg.NodeID,
			TargetID: targetPeerID,
			Payload:  sdpPayload,
		})
	}

	// Wait directly on ctx for WebRTC ICE/DTLS convergence.
	if err := conn.WaitConnected(ctx); err != nil {
		if errors.Is(err, ErrSessionSuperseded) {
			if winner := conn.getSupersededBy(); winner != nil {
				return waitSupersededWinner(winner)
			}
		}
		m.removeSession(targetPeerID, sessionID)
		_ = conn.Close()
		return nil, fmt.Errorf("webrtc ICE connection to %s failed: %w", targetPeerID, err)
	}

	if err := conn.WaitChannelsReady(ctx); err != nil {
		m.removeSession(targetPeerID, sessionID)
		_ = conn.Close()
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
		return m.handleAnswer(env.SenderID, sdp)

	case protocol.MsgICECandidate:
		var cand protocol.ICECandidatePayload
		if err := json.Unmarshal(env.Payload, &cand); err != nil {
			return err
		}
		return m.handleCandidate(env.SenderID, cand)
	}

	return nil
}

func (m *Manager) handleOffer(remotePeerID string, sdp protocol.SDPPayload) error {
	if err := m.verifyAndPinDTLS(remotePeerID, sdp); err != nil {
		return err
	}

	// Check for existing session renegotiation (ICE restart) or simultaneous offer glare.
	m.mu.Lock()
	if existingConn, sameSession := m.connections[sdp.SessionID]; sameSession && existingConn != nil && existingConn.PC != nil {
		if existingConn.RemotePeerID != remotePeerID {
			m.mu.Unlock()
			return fmt.Errorf("session %s belongs to peer %s, not %s", sdp.SessionID, existingConn.RemotePeerID, remotePeerID)
		}
		if existingConn.PC.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
			polite := m.cfg.NodeID < remotePeerID
			if !polite {
				m.mu.Unlock()
				return nil
			}
			m.mu.Unlock()
			if err := existingConn.PC.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback}); err != nil {
				return err
			}
		} else {
			m.mu.Unlock()
		}

		existingConn.setCGNATFlags(m.cfg.IsBehindCGNAT, sdp.IsBehindCGNAT)
		offer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  sdp.SDP,
		}
		if err := existingConn.PC.SetRemoteDescription(offer); err != nil {
			return err
		}
		answer, err := existingConn.PC.CreateAnswer(nil)
		if err != nil {
			return err
		}
		if err := existingConn.PC.SetLocalDescription(answer); err != nil {
			return err
		}
		if sig := m.getSignalingClient(); sig != nil {
			ansObj, err := m.signSDPPayload(sdp.SessionID, answer.Type.String(), answer.SDP)
			if err != nil {
				return err
			}
			answerPayload, _ := json.Marshal(ansObj)
			_ = sig.Send(protocol.Envelope{
				Type:     protocol.MsgSDPAnswer,
				SenderID: m.cfg.NodeID,
				TargetID: remotePeerID,
				Payload:  answerPayload,
			})
		}
		return nil
	}

	var supersededConn *PeerConnection
	if oldSessionID, exists := m.peerSessions[remotePeerID]; exists && oldSessionID != sdp.SessionID {
		if oldConn, ok := m.connections[oldSessionID]; ok && oldConn != nil {
			if oldConn.isOutboundOfferPending() {
				// Simultaneous offer glare: lexicographical LocalPeerID < RemotePeerID is polite (yields).
				polite := m.cfg.NodeID < remotePeerID
				if !polite {
					m.mu.Unlock()
					return nil
				}
			}
			supersededConn = oldConn
		}
	}
	m.mu.Unlock()

	webrtcCfg := webrtc.Configuration{
		ICEServers: m.buildICEServers(),
	}

	pc, err := m.api.NewPeerConnection(webrtcCfg)
	if err != nil {
		return err
	}

	conn := newPeerConnection(sdp.SessionID, m.cfg.NodeID, remotePeerID, pc, m.tracker)
	conn.chunkTempDir = m.cfg.ChunkTempDir
	conn.setCGNATFlags(m.cfg.IsBehindCGNAT, sdp.IsBehindCGNAT)
	conn.setupHandlers()

	m.mu.Lock()
	if oldSessionID, exists := m.peerSessions[remotePeerID]; exists && oldSessionID != sdp.SessionID {
		if oldConn, ok := m.connections[oldSessionID]; ok && oldConn != nil {
			if oldConn.isOutboundOfferPending() && !(m.cfg.NodeID < remotePeerID) {
				m.mu.Unlock()
				_ = conn.Close()
				return nil
			}
			delete(m.connections, oldSessionID)
			supersededConn = oldConn
		}
	}
	m.connections[sdp.SessionID] = conn
	m.peerSessions[remotePeerID] = sdp.SessionID
	onConnHandler := m.onNewConn
	m.mu.Unlock()

	if supersededConn != nil {
		supersededConn.markSuperseded(conn)
		go func(c *PeerConnection) {
			_ = c.Close()
		}(supersededConn)
	}

	m.bindPeerConnectionLifecycle(conn, remotePeerID, sdp.SessionID)

	// Register event callbacks (storage, streams) synchronously before receiving early DataChannel messages
	if onConnHandler != nil {
		onConnHandler(conn)
	}

	// Answerer listens for DataChannels created by the offerer
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		conn.bindDataChannel(dc)
	})

	// Trickle ICE candidates
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		sig := m.getSignalingClient()
		if c == nil || sig == nil {
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
		_ = sig.Send(protocol.Envelope{
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
		m.removeSession(remotePeerID, sdp.SessionID)
		_ = conn.Close()
		return err
	}

	m.mu.Lock()
	m.remoteDescSet[sdp.SessionID] = true
	queuedCandidates := m.pendingCandidates[sdp.SessionID]
	delete(m.pendingCandidates, sdp.SessionID)
	m.mu.Unlock()

	for _, init := range queuedCandidates {
		if err := pc.AddICECandidate(init); err != nil {
			m.removeSession(remotePeerID, sdp.SessionID)
			_ = conn.Close()
			return err
		}
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		m.removeSession(remotePeerID, sdp.SessionID)
		_ = conn.Close()
		return err
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		m.removeSession(remotePeerID, sdp.SessionID)
		_ = conn.Close()
		return err
	}

	if sig := m.getSignalingClient(); sig != nil {
		ansObj, err := m.signSDPPayload(sdp.SessionID, answer.Type.String(), answer.SDP)
		if err != nil {
			m.removeSession(remotePeerID, sdp.SessionID)
			_ = conn.Close()
			return err
		}
		answerPayload, _ := json.Marshal(ansObj)
		_ = sig.Send(protocol.Envelope{
			Type:     protocol.MsgSDPAnswer,
			SenderID: m.cfg.NodeID,
			TargetID: remotePeerID,
			Payload:  answerPayload,
		})
	}

	return nil
}

func (m *Manager) handleAnswer(remotePeerID string, sdp protocol.SDPPayload) error {
	m.mu.Lock()
	conn, ok := m.connections[sdp.SessionID]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("unknown session: %s", sdp.SessionID)
	}

	if remotePeerID != "" && conn.RemotePeerID != remotePeerID {
		return fmt.Errorf("session %s belongs to peer %s, not %s", sdp.SessionID, conn.RemotePeerID, remotePeerID)
	}
	expectedPeer := conn.RemotePeerID
	if expectedPeer == "" {
		expectedPeer = remotePeerID
	}
	if err := m.verifyAndPinDTLS(expectedPeer, sdp); err != nil {
		return err
	}

	conn.setCGNATFlags(m.cfg.IsBehindCGNAT, sdp.IsBehindCGNAT)

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp.SDP,
	}

	if err := conn.PC.SetRemoteDescription(answer); err != nil {
		return err
	}

	m.mu.Lock()
	m.remoteDescSet[sdp.SessionID] = true
	queuedCandidates := m.pendingCandidates[sdp.SessionID]
	delete(m.pendingCandidates, sdp.SessionID)
	m.mu.Unlock()

	for _, init := range queuedCandidates {
		if err := conn.PC.AddICECandidate(init); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) handleCandidate(remotePeerID string, cand protocol.ICECandidatePayload) error {
	init := webrtc.ICECandidateInit{
		Candidate:     cand.Candidate,
		SDPMid:        &cand.SDPMid,
		SDPMLineIndex: &cand.SDPMLineIndex,
	}

	m.mu.Lock()
	conn, ok := m.connections[cand.SessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown session: %s", cand.SessionID)
	}
	if remotePeerID != "" && conn.RemotePeerID != remotePeerID {
		m.mu.Unlock()
		return fmt.Errorf("candidate for session %s belongs to peer %s, not %s", cand.SessionID, conn.RemotePeerID, remotePeerID)
	}
	if !m.remoteDescSet[cand.SessionID] {
		if len(m.pendingCandidates[cand.SessionID]) < maxPendingCandidatesPerSession {
			m.pendingCandidates[cand.SessionID] = append(m.pendingCandidates[cand.SessionID], init)
		}
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

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

// ActivePeers returns a slice of remote peer IDs that currently have a peer session.
func (m *Manager) ActivePeers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	peers := make([]string, 0, len(m.peerSessions))
	for peerID := range m.peerSessions {
		peers = append(peers, peerID)
	}
	return peers
}

// DisconnectPeer terminates the WebRTC connection to a specific peer.
func (m *Manager) DisconnectPeer(peerID string) error {
	m.mu.Lock()
	sessionID, ok := m.peerSessions[peerID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no connection to peer %s", peerID)
	}
	conn, hasConn := m.connections[sessionID]
	delete(m.peerSessions, peerID)
	delete(m.connections, sessionID)
	delete(m.pendingCandidates, sessionID)
	delete(m.remoteDescSet, sessionID)
	m.mu.Unlock()

	if m.tracker != nil {
		m.tracker.RemoveConnection(peerID)
	}

	if hasConn && conn != nil {
		return conn.Close()
	}
	return nil
}

// Close closes all peer connections and shuts down the transport manager.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true

	peerIDs := make([]string, 0, len(m.peerSessions))
	for peerID := range m.peerSessions {
		peerIDs = append(peerIDs, peerID)
	}
	for _, conn := range m.connections {
		_ = conn.Close()
	}
	m.connections = make(map[string]*PeerConnection)
	m.peerSessions = make(map[string]string)
	m.pendingCandidates = make(map[string][]webrtc.ICECandidateInit)
	m.remoteDescSet = make(map[string]bool)
	m.mu.Unlock()

	if m.tracker != nil {
		for _, peerID := range peerIDs {
			m.tracker.RemoveConnection(peerID)
		}
	}

	return nil
}

func (m *Manager) bindSignalingTURNRefresh(sig *signaling.Client) {
	if sig == nil {
		return
	}
	sig.SetOnTURNRefresh(func(servers []protocol.TURNServerInfo) {
		turns := make([]TURNServerConfig, 0, len(servers))
		for _, ts := range servers {
			turns = append(turns, TURNServerConfig{
				URL:        ts.URL,
				Username:   ts.Username,
				Credential: ts.Credential,
			})
		}
		m.SetDynamicTURNServers(turns)
	})
}

// SetSignalingClient dynamically updates the signaling client instance.
func (m *Manager) SetSignalingClient(sig *signaling.Client) {
	m.mu.Lock()
	m.sigClient = sig
	if len(m.privKey) == 0 && sig != nil {
		m.pubKey = sig.PublicKey()
	}
	m.mu.Unlock()
	m.bindSignalingTURNRefresh(sig)
}

// SetDynamicTURNServers replaces coordinator-issued TURN server configurations.
func (m *Manager) SetDynamicTURNServers(servers []TURNServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dynamicTURNServers = append([]TURNServerConfig(nil), servers...)
}

// DynamicTURNServers returns a copy of coordinator-issued dynamic TURN servers.
func (m *Manager) DynamicTURNServers() []TURNServerConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]TURNServerConfig, len(m.dynamicTURNServers))
	copy(res, m.dynamicTURNServers)
	return res
}

// SetLocalCGNAT updates whether the local node is behind RFC 6598 CGNAT.
func (m *Manager) SetLocalCGNAT(isCGNAT bool) {
	m.mu.Lock()
	m.cfg.IsBehindCGNAT = isCGNAT
	conns := make([]*PeerConnection, 0, len(m.connections))
	for _, c := range m.connections {
		conns = append(conns, c)
	}
	m.mu.Unlock()

	for _, c := range conns {
		c.setCGNATFlags(isCGNAT, false)
	}
}
