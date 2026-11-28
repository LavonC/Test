package signaling

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/unicompute/obj1/pkg/protocol"
	"github.com/unicompute/obj1/pkg/relay"
)

var ErrUnauthorized = errors.New("signaling unauthorized (HTTP 401)")

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // Non-browser Go CLI / daemon clients
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		if r.Host != "" && u.Host == r.Host {
			return true
		}
		host := u.Hostname()
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	},
}

const (
	MaxHubClients    = 1000
	MaxHubPinnedKeys = 5000
)

// HubConfig configures a signaling Hub.
type HubConfig struct {
	ListenAddr     string
	AuthToken      string
	PinnedKeysPath string // Optional JSON file path for persistent TOFU pinned keys (empty = in-memory)
	TURNURL        string // Optional TURN server URL (e.g. "turn:1.2.3.4:3478")
	TURNSecret     string // Shared secret for time-limited TURN REST credentials
	TURNTTLSeconds int64  // TURN credential TTL in seconds (default 3600)
	MaxClients     int    // Optional client cap (default MaxHubClients = 1000)
	MaxPinnedKeys  int    // Optional pinned key cap (default MaxHubPinnedKeys = 5000)
}

// Hub manages WebSocket signaling connections and routes messages between peers.
type Hub struct {
	mu             sync.RWMutex
	persistMu      sync.Mutex
	clients        map[string]*clientConn
	pinnedKeys     map[string]string // nodeID -> hex-encoded ed25519 public key (TOFU/pre-seeded)
	pinnedKeysPath string
	turnURL        string
	turnSecret     string
	turnTTLSeconds int64
	maxClients     int
	maxPinnedKeys  int
	httpServer     *http.Server
	listener       net.Listener
	addr           string
	authToken      string
	closed         bool
}

type clientConn struct {
	nodeID         string
	conn           *websocket.Conn
	sendChan       chan []byte
	writeMu        sync.Mutex
	hub            *Hub
	done           chan struct{}
	doneOnce       sync.Once
	challengeNonce string
	challengeUsed  bool
}

// NewHub starts a signaling hub HTTP server on the given address, optionally requiring an auth token.
func NewHub(listenAddr string, authTokens ...string) (*Hub, error) {
	cfg := HubConfig{
		ListenAddr: listenAddr,
	}
	if len(authTokens) > 0 {
		cfg.AuthToken = authTokens[0]
	}
	return NewHubWithConfig(cfg)
}

// NewHubWithConfig starts a signaling hub HTTP server using the supplied HubConfig.
func NewHubWithConfig(cfg HubConfig) (*Hub, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	if cfg.TURNTTLSeconds <= 0 {
		cfg.TURNTTLSeconds = 3600
	}
	maxClients := cfg.MaxClients
	if maxClients <= 0 {
		maxClients = MaxHubClients
	}
	maxPinnedKeys := cfg.MaxPinnedKeys
	if maxPinnedKeys <= 0 {
		maxPinnedKeys = MaxHubPinnedKeys
	}

	pinned := make(map[string]string)
	if cfg.PinnedKeysPath != "" {
		data, err := os.ReadFile(cfg.PinnedKeysPath)
		if err == nil {
			if len(strings.TrimSpace(string(data))) > 0 {
				var loaded map[string]string
				if err := json.Unmarshal(data, &loaded); err != nil {
					return nil, fmt.Errorf("failed to parse pinned keys file %s: %w", cfg.PinnedKeysPath, err)
				}
				for k, v := range loaded {
					if k != "" && v != "" {
						if len(pinned) >= maxPinnedKeys {
							break
						}
						pinned[k] = strings.ToLower(v)
					}
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read pinned keys file %s: %w", cfg.PinnedKeysPath, err)
		}
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", cfg.ListenAddr, err)
	}

	h := &Hub{
		clients:        make(map[string]*clientConn),
		pinnedKeys:     pinned,
		pinnedKeysPath: cfg.PinnedKeysPath,
		turnURL:        strings.TrimSpace(cfg.TURNURL),
		turnSecret:     cfg.TURNSecret,
		turnTTLSeconds: cfg.TURNTTLSeconds,
		maxClients:     maxClients,
		maxPinnedKeys:  maxPinnedKeys,
		listener:       ln,
		addr:           ln.Addr().String(),
		authToken:      cfg.AuthToken,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.handleWebSocket)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"healthy","service":"unicompute-coordinator"}`))
	})
	mux.HandleFunc("/api/nodes", func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		token := h.authToken
		h.mu.RUnlock()

		if token != "" {
			reqToken := ""
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				reqToken = strings.TrimPrefix(authHeader, "Bearer ")
			}
			if subtle.ConstantTimeCompare([]byte(reqToken), []byte(token)) != 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		h.mu.RLock()
		nodes := make([]string, 0, len(h.clients))
		pubKeys := make(map[string]string, len(h.clients))
		for id := range h.clients {
			nodes = append(nodes, id)
			if pk, ok := h.pinnedKeys[id]; ok {
				pubKeys[id] = pk
			}
		}
		h.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"count":       len(nodes),
			"nodes":       nodes,
			"public_keys": pubKeys,
		})
	})

	h.httpServer = &http.Server{
		Handler: mux,
	}

	go func() {
		_ = h.httpServer.Serve(ln)
	}()

	return h, nil
}

// Addr returns the signaling hub's network address.
func (h *Hub) Addr() string {
	return h.addr
}

// URL returns the WebSocket URL to connect to.
func (h *Hub) URL() string {
	return fmt.Sprintf("ws://%s/ws", h.addr)
}

// Close shuts down the signaling hub.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	for _, c := range h.clients {
		_ = c.conn.Close()
	}
	h.clients = make(map[string]*clientConn)
	h.mu.Unlock()

	return h.httpServer.Close()
}

// SetAuthToken configures or updates the cluster auth token for the hub.
func (h *Hub) SetAuthToken(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authToken = token
}

// PinPublicKey pre-seeds or pins a trusted ed25519 public key for a node ID.
func (h *Hub) PinPublicKey(nodeID string, pubKey ed25519.PublicKey) error {
	if nodeID == "" || len(pubKey) != ed25519.PublicKeySize {
		return errors.New("invalid nodeID or ed25519 public key")
	}
	h.mu.Lock()
	if _, exists := h.pinnedKeys[nodeID]; !exists && len(h.pinnedKeys) >= h.maxPinnedKeys {
		h.mu.Unlock()
		return fmt.Errorf("hub pinned keys capacity exceeded (max %d)", h.maxPinnedKeys)
	}
	h.pinnedKeys[nodeID] = strings.ToLower(hex.EncodeToString(pubKey))
	shouldPersist := h.pinnedKeysPath != ""
	h.mu.Unlock()

	if shouldPersist {
		return h.persistPinnedKeys()
	}
	return nil
}

func (h *Hub) persistPinnedKeys() error {
	if h.pinnedKeysPath == "" {
		return nil
	}
	h.persistMu.Lock()
	defer h.persistMu.Unlock()

	h.mu.RLock()
	snapshot := make(map[string]string, len(h.pinnedKeys))
	for k, v := range h.pinnedKeys {
		snapshot[k] = v
	}
	h.mu.RUnlock()

	dir := filepath.Dir(h.pinnedKeysPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pinned-keys-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, h.pinnedKeysPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (h *Hub) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	token := h.authToken
	h.mu.RUnlock()

	if token != "" {
		reqToken := ""
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			reqToken = strings.TrimPrefix(authHeader, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(reqToken), []byte(token)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(256 * 1024)

	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		_ = conn.Close()
		return
	}
	nonce := hex.EncodeToString(nonceBytes)

	c := &clientConn{
		conn:           conn,
		sendChan:       make(chan []byte, 100),
		hub:            h,
		done:           make(chan struct{}),
		challengeNonce: nonce,
	}

	chalPayload, _ := json.Marshal(protocol.ChallengePayload{Nonce: nonce})
	chalEnv, _ := json.Marshal(protocol.Envelope{
		Type:     protocol.MsgChallenge,
		SenderID: "coordinator",
		Payload:  chalPayload,
	})
	c.sendChan <- chalEnv

	go c.writePump()
	c.readPump()
}

func (c *clientConn) readPump() {
	defer func() {
		c.doneOnce.Do(func() {
			close(c.done)
		})
		if c.nodeID != "" {
			c.hub.mu.Lock()
			delete(c.hub.clients, c.nodeID)
			// Broadcast peer left notification to all remaining nodes
			leftPayload, _ := json.Marshal(protocol.PeerEventPayload{NodeID: c.nodeID})
			leftEnv, _ := json.Marshal(protocol.Envelope{
				Type:     protocol.MsgPeerLeft,
				SenderID: "coordinator",
				Payload:  leftPayload,
			})
			for _, other := range c.hub.clients {
				select {
				case other.sendChan <- leftEnv:
				default:
				}
			}
			c.hub.mu.Unlock()
		}
		_ = c.conn.Close()
	}()

	// Section 6.3: "Fix: pong handler, read deadline, 20 s ping ticker"
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			break
		}

		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}

		if env.Type == protocol.MsgRegister {
			if c.nodeID != "" {
				c.rejectRegister(c.nodeID, "connection already registered")
				return
			}
			if c.challengeUsed || c.challengeNonce == "" {
				c.rejectRegister("", "registration challenge already used")
				return
			}
			c.challengeUsed = true

			var reg protocol.RegisterPayload
			if err := json.Unmarshal(env.Payload, &reg); err != nil {
				c.rejectRegister("", "invalid registration payload")
				return
			}
			targetNodeID := reg.NodeID
			if targetNodeID == "" {
				targetNodeID = env.SenderID
			}
			if targetNodeID == "" {
				c.rejectRegister("", "nodeID is required")
				return
			}
			if env.SenderID != "" && env.SenderID != targetNodeID {
				c.rejectRegister(targetNodeID, "sender_id mismatch on registration")
				return
			}
			if reg.Nonce == "" || subtle.ConstantTimeCompare([]byte(reg.Nonce), []byte(c.challengeNonce)) != 1 {
				c.rejectRegister(targetNodeID, "invalid or replayed registration nonce")
				return
			}

			pubBytes, err := hex.DecodeString(reg.PublicKey)
			if err != nil || len(pubBytes) != ed25519.PublicKeySize {
				c.rejectRegister(targetNodeID, "missing or invalid ed25519 public key")
				return
			}
			sigBytes, err := hex.DecodeString(reg.Signature)
			if err != nil || len(sigBytes) != ed25519.SignatureSize {
				c.rejectRegister(targetNodeID, "missing or invalid registration signature")
				return
			}
			signedMsg := []byte(targetNodeID + ":" + c.challengeNonce)
			if !ed25519.Verify(ed25519.PublicKey(pubBytes), signedMsg, sigBytes) {
				c.rejectRegister(targetNodeID, "invalid node registration signature")
				return
			}
			normPubKey := strings.ToLower(hex.EncodeToString(pubBytes))

			c.hub.mu.Lock()
			if existing, exists := c.hub.clients[targetNodeID]; exists && existing != c {
				c.hub.mu.Unlock()
				c.rejectRegister(targetNodeID, "duplicate nodeID registration rejected")
				return
			}
			if len(c.hub.clients) >= c.hub.maxClients {
				c.hub.mu.Unlock()
				c.rejectRegister(targetNodeID, "hub client capacity exceeded")
				return
			}
			pinnedPub, pinned := c.hub.pinnedKeys[targetNodeID]
			if pinned && pinnedPub != normPubKey {
				c.hub.mu.Unlock()
				c.rejectRegister(targetNodeID, "nodeID public key mismatch")
				return
			}
			if !pinned && len(c.hub.pinnedKeys) >= c.hub.maxPinnedKeys {
				c.hub.mu.Unlock()
				c.rejectRegister(targetNodeID, "hub pinned keys capacity exceeded")
				return
			}
			newlyPinned := !pinned
			c.hub.pinnedKeys[targetNodeID] = normPubKey
			shouldPersist := newlyPinned && c.hub.pinnedKeysPath != ""
			c.nodeID = targetNodeID
			c.hub.clients[c.nodeID] = c

			// Broadcast peer joined notification to all OTHER nodes
			joinPayload, _ := json.Marshal(protocol.PeerEventPayload{
				NodeID:    c.nodeID,
				PublicKey: normPubKey,
			})
			joinEnv, _ := json.Marshal(protocol.Envelope{
				Type:     protocol.MsgPeerJoined,
				SenderID: "coordinator",
				Payload:  joinPayload,
			})
			for id, other := range c.hub.clients {
				if id != c.nodeID {
					select {
					case other.sendChan <- joinEnv:
					default:
					}
				}
			}
			turnURL := c.hub.turnURL
			turnSecret := c.hub.turnSecret
			turnTTL := c.hub.turnTTLSeconds
			c.hub.mu.Unlock()

			if shouldPersist {
				_ = c.hub.persistPinnedKeys()
			}

			ackPayloadObj := protocol.RegisterAckPayload{
				NodeID: c.nodeID,
			}
			if turnURL != "" && turnSecret != "" {
				u, p := relay.GenerateRESTCredentials(c.nodeID, turnSecret, turnTTL)
				ackPayloadObj.TURNServers = []protocol.TURNServerInfo{
					{
						URL:        turnURL,
						Username:   u,
						Credential: p,
					},
				}
				ackPayloadObj.TURNTTLSeconds = turnTTL
			}
			ackPayload, _ := json.Marshal(ackPayloadObj)
			ackEnv := protocol.Envelope{
				Type:     protocol.MsgRegisterAck,
				SenderID: "coordinator",
				TargetID: c.nodeID,
				Payload:  ackPayload,
			}
			data, _ := json.Marshal(ackEnv)
			select {
			case c.sendChan <- data:
			default:
			}
			continue
		}

		if env.Type == protocol.MsgTURNRefresh {
			if c.nodeID == "" {
				continue
			}
			c.hub.mu.RLock()
			turnURL := c.hub.turnURL
			turnSecret := c.hub.turnSecret
			turnTTL := c.hub.turnTTLSeconds
			c.hub.mu.RUnlock()

			refreshPayloadObj := protocol.TURNRefreshAckPayload{
				TURNTTLSeconds: turnTTL,
			}
			if turnURL != "" && turnSecret != "" {
				u, p := relay.GenerateRESTCredentials(c.nodeID, turnSecret, turnTTL)
				refreshPayloadObj.TURNServers = []protocol.TURNServerInfo{
					{
						URL:        turnURL,
						Username:   u,
						Credential: p,
					},
				}
			}
			refreshPayload, _ := json.Marshal(refreshPayloadObj)
			refreshEnv := protocol.Envelope{
				Type:     protocol.MsgTURNRefreshAck,
				SenderID: "coordinator",
				TargetID: c.nodeID,
				Payload:  refreshPayload,
			}
			data, _ := json.Marshal(refreshEnv)
			select {
			case c.sendChan <- data:
			default:
			}
			continue
		}

		if env.Type == protocol.MsgPeerListRequest {
			if c.nodeID == "" {
				continue
			}
			c.hub.mu.RLock()
			nodes := make([]string, 0, len(c.hub.clients))
			pubKeys := make(map[string]string, len(c.hub.clients))
			for id := range c.hub.clients {
				nodes = append(nodes, id)
				if pk, ok := c.hub.pinnedKeys[id]; ok {
					pubKeys[id] = pk
				}
			}
			c.hub.mu.RUnlock()

			respPayload, _ := json.Marshal(protocol.PeerListPayload{
				Nodes:      nodes,
				PublicKeys: pubKeys,
			})
			respEnv := protocol.Envelope{
				Type:     protocol.MsgPeerListResponse,
				SenderID: "coordinator",
				TargetID: c.nodeID,
				Payload:  respPayload,
			}
			data, _ := json.Marshal(respEnv)
			select {
			case c.sendChan <- data:
			default:
			}
			continue
		}

		// Route message to target peer
		if env.TargetID != "" {
			if c.nodeID == "" {
				continue
			}
			if env.SenderID != "" && env.SenderID != c.nodeID {
				continue
			}
			env.SenderID = c.nodeID
			routedPayload, err := json.Marshal(env)
			if err != nil {
				continue
			}

			c.hub.mu.RLock()
			target, ok := c.hub.clients[env.TargetID]
			c.hub.mu.RUnlock()
			if ok {
				select {
				case target.sendChan <- routedPayload:
				default:
				}
			}
		}
	}
}

func (c *clientConn) writePump() {
	ticker := time.NewTicker(20 * time.Second) // 20s ping ticker as specified in Section 6.3
	defer func() {
		ticker.Stop()
		c.writeMu.Lock()
		_ = c.conn.Close()
		c.writeMu.Unlock()
	}()

	for {
		select {
		case <-c.done:
			return
		case msg, ok := <-c.sendChan:
			c.writeMu.Lock()
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				c.writeMu.Unlock()
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := c.conn.WriteMessage(websocket.TextMessage, msg)
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		case <-ticker.C:
			c.writeMu.Lock()
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := c.conn.WriteMessage(websocket.PingMessage, nil)
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// rejectRegister notifies the client of a registration rejection and closes the WebSocket with ClosePolicyViolation.
func (c *clientConn) rejectRegister(targetNodeID, reason string) {
	payloadBytes, _ := json.Marshal(map[string]string{"error": reason})
	errMsg, _ := json.Marshal(protocol.Envelope{
		Type:     protocol.MsgRegisterAck,
		SenderID: "coordinator",
		TargetID: targetNodeID,
		Payload:  payloadBytes,
	})
	select {
	case c.sendChan <- errMsg:
	default:
	}
	time.Sleep(50 * time.Millisecond)
	c.writeMu.Lock()
	_ = c.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason))
	c.writeMu.Unlock()
}

// Client connects to the signaling hub and handles peer negotiation.
type Client struct {
	mu            sync.Mutex
	writeMu       sync.Mutex // Serializes all writes to WebSocket connection
	url           string
	nodeID        string
	authToken     string
	privKey       ed25519.PrivateKey
	pubKey        ed25519.PublicKey
	peerKeys      map[string]string // nodeID -> hex-encoded ed25519 public key
	turnServers   []protocol.TURNServerInfo
	onTURNRefresh func([]protocol.TURNServerInfo)
	turnRefreshCh chan time.Duration
	conn          *websocket.Conn
	handler       func(protocol.Envelope)
	reconnectCtx  context.Context
	cancel        context.CancelFunc
	sendQueue     chan []byte
	closed        bool
}

// NewClient initializes a signaling client with an ephemeral ed25519 keypair, auto-reconnection, and keepalive.
func NewClient(wsURL string, nodeID string, handler func(protocol.Envelope), authTokens ...string) (*Client, error) {
	return NewClientWithKey(wsURL, nodeID, nil, handler, authTokens...)
}

// NewClientWithKey initializes a signaling client using the provided ed25519 private key (or generates one if nil).
func NewClientWithKey(wsURL string, nodeID string, privKey ed25519.PrivateKey, handler func(protocol.Envelope), authTokens ...string) (*Client, error) {
	c, err := newClientWithKey(wsURL, nodeID, privKey, handler, false, authTokens...)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// NewClientAsync initializes a signaling client that keeps reconnecting in the background
// if the initial dial fails (except on HTTP 401 unauthorized, which fails immediately with nil client).
func NewClientAsync(wsURL string, nodeID string, handler func(protocol.Envelope), authTokens ...string) (*Client, error) {
	return NewClientAsyncWithKey(wsURL, nodeID, nil, handler, authTokens...)
}

// NewClientAsyncWithKey initializes a signaling client with the given ed25519 key that
// retains background reconnection on transient initial dial failures (returning both client and initial error),
// while failing immediately on HTTP 401 Unauthorized.
func NewClientAsyncWithKey(wsURL string, nodeID string, privKey ed25519.PrivateKey, handler func(protocol.Envelope), authTokens ...string) (*Client, error) {
	return newClientWithKey(wsURL, nodeID, privKey, handler, true, authTokens...)
}

func newClientWithKey(wsURL string, nodeID string, privKey ed25519.PrivateKey, handler func(protocol.Envelope), allowAsyncDial bool, authTokens ...string) (*Client, error) {
	if len(privKey) != ed25519.PrivateKeySize {
		_, genPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("failed to generate node identity key: %w", err)
		}
		privKey = genPriv
	}
	pubKey := privKey.Public().(ed25519.PublicKey)

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		url:           wsURL,
		nodeID:        nodeID,
		privKey:       privKey,
		pubKey:        pubKey,
		peerKeys:      make(map[string]string),
		turnRefreshCh: make(chan time.Duration, 1),
		handler:       handler,
		reconnectCtx:  ctx,
		cancel:        cancel,
		sendQueue:     make(chan []byte, 100),
	}
	if len(authTokens) > 0 {
		c.authToken = authTokens[0]
	}

	if err := c.connect(); err != nil {
		if !allowAsyncDial || errors.Is(err, ErrUnauthorized) {
			cancel()
			return nil, err
		}
		go c.readLoop()
		go c.writeLoop()
		return c, err
	}

	go c.readLoop()
	go c.writeLoop()
	return c, nil
}

func (c *Client) connect() error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	header := make(http.Header)
	if c.authToken != "" {
		header.Set("Authorization", "Bearer "+c.authToken)
	}

	conn, resp, err := dialer.DialContext(c.reconnectCtx, c.url, header)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
		return err
	}

	// Read server-issued challenge nonce before registering
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, chalMsg, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to read signaling challenge: %w", err)
	}
	var chalEnv protocol.Envelope
	if err := json.Unmarshal(chalMsg, &chalEnv); err != nil || chalEnv.Type != protocol.MsgChallenge {
		_ = conn.Close()
		return errors.New("expected signaling challenge message")
	}
	var chal protocol.ChallengePayload
	if err := json.Unmarshal(chalEnv.Payload, &chal); err != nil || chal.Nonce == "" {
		_ = conn.Close()
		return errors.New("invalid signaling challenge nonce")
	}
	_ = conn.SetReadDeadline(time.Time{})

	// Sign nodeID + ":" + nonce with Ed25519 identity key
	sig := ed25519.Sign(c.privKey, []byte(c.nodeID+":"+chal.Nonce))
	regPayload, _ := json.Marshal(protocol.RegisterPayload{
		NodeID:    c.nodeID,
		Nonce:     chal.Nonce,
		PublicKey: hex.EncodeToString(c.pubKey),
		Signature: hex.EncodeToString(sig),
	})
	regEnv := protocol.Envelope{
		Type:     protocol.MsgRegister,
		SenderID: c.nodeID,
		Payload:  regPayload,
	}
	data, _ := json.Marshal(regEnv)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return errors.New("signaling client closed")
	}
	c.conn = conn
	c.mu.Unlock()

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		c.mu.Lock()
		if c.conn == conn {
			_ = c.conn.Close()
			c.conn = nil
		}
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *Client) readLoop() {
	for {
		c.mu.Lock()
		conn := c.conn
		closed := c.closed
		c.mu.Unlock()

		if closed {
			return
		}

		if conn == nil {
			c.reconnect()
			continue
		}

		// Keepalive: read deadline and pong handler
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		_, msg, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			if !c.closed && c.conn == conn {
				_ = c.conn.Close()
				c.conn = nil
			}
			c.mu.Unlock()
			c.reconnect()
			continue
		}

		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err == nil {
			c.processIncomingMetadata(env)
			if c.handler != nil {
				c.handler(env)
			}
		}
	}
}

func (c *Client) scheduleTURNRefresh(ttlSeconds int64) {
	if ttlSeconds <= 0 {
		ttlSeconds = 3600
	}
	delay := time.Duration(ttlSeconds) * 750 * time.Millisecond
	if delay < 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	select {
	case <-c.turnRefreshCh:
	default:
	}
	select {
	case c.turnRefreshCh <- delay:
	default:
	}
}

// SetOnTURNRefresh sets an optional callback invoked whenever coordinator TURN credentials refresh.
func (c *Client) SetOnTURNRefresh(fn func([]protocol.TURNServerInfo)) {
	c.mu.Lock()
	c.onTURNRefresh = fn
	c.mu.Unlock()
}

func (c *Client) processIncomingMetadata(env protocol.Envelope) {
	switch env.Type {
	case protocol.MsgRegisterAck:
		var ack protocol.RegisterAckPayload
		if json.Unmarshal(env.Payload, &ack) == nil && ack.Error == "" {
			c.mu.Lock()
			c.turnServers = append([]protocol.TURNServerInfo(nil), ack.TURNServers...)
			c.mu.Unlock()
			if len(ack.TURNServers) > 0 {
				c.scheduleTURNRefresh(ack.TURNTTLSeconds)
			}
		}
	case protocol.MsgTURNRefreshAck:
		var ack protocol.TURNRefreshAckPayload
		if json.Unmarshal(env.Payload, &ack) == nil && ack.Error == "" {
			c.mu.Lock()
			c.turnServers = append([]protocol.TURNServerInfo(nil), ack.TURNServers...)
			cb := c.onTURNRefresh
			c.mu.Unlock()
			if len(ack.TURNServers) > 0 {
				c.scheduleTURNRefresh(ack.TURNTTLSeconds)
			}
			if cb != nil {
				cp := append([]protocol.TURNServerInfo(nil), ack.TURNServers...)
				cb(cp)
			}
		}
	case protocol.MsgPeerJoined:
		var ev protocol.PeerEventPayload
		if json.Unmarshal(env.Payload, &ev) == nil && ev.NodeID != "" && ev.PublicKey != "" {
			norm := strings.ToLower(ev.PublicKey)
			c.mu.Lock()
			if existing, ok := c.peerKeys[ev.NodeID]; !ok || existing == norm {
				c.peerKeys[ev.NodeID] = norm
			}
			c.mu.Unlock()
		}
	case protocol.MsgPeerListResponse:
		var list protocol.PeerListPayload
		if json.Unmarshal(env.Payload, &list) == nil && len(list.PublicKeys) > 0 {
			c.mu.Lock()
			for id, pk := range list.PublicKeys {
				if id != "" && pk != "" {
					norm := strings.ToLower(pk)
					if existing, ok := c.peerKeys[id]; !ok || existing == norm {
						c.peerKeys[id] = norm
					}
				}
			}
			c.mu.Unlock()
		}
	}
}

// PublicKey returns a copy of this client's Ed25519 public key.
func (c *Client) PublicKey() ed25519.PublicKey {
	cp := make(ed25519.PublicKey, len(c.pubKey))
	copy(cp, c.pubKey)
	return cp
}

// Sign signs data with the client's Ed25519 private key.
func (c *Client) Sign(data []byte) []byte {
	return ed25519.Sign(c.privKey, data)
}

// GetPeerPublicKey returns the known/pinned Ed25519 public key for a peer, if available.
func (c *Client) GetPeerPublicKey(nodeID string) (ed25519.PublicKey, bool) {
	c.mu.Lock()
	hexKey, ok := c.peerKeys[nodeID]
	c.mu.Unlock()
	if !ok || hexKey == "" {
		return nil, false
	}
	b, err := hex.DecodeString(hexKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, false
	}
	return ed25519.PublicKey(b), true
}

// TURNServers returns the TURN server credentials issued by the coordinator in MsgRegisterAck.
func (c *Client) TURNServers() []protocol.TURNServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := make([]protocol.TURNServerInfo, len(c.turnServers))
	copy(res, c.turnServers)
	return res
}

// IsConnected reports whether the client currently holds an active WebSocket connection.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.conn != nil
}

func (c *Client) reconnect() {
	// Section 10: "agents retry with jittered backoff capped at 8 s"
	backoff := 500 * time.Millisecond
	maxBackoff := 8 * time.Second

	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		jitter := time.Duration(mrand.Int63n(int64(250 * time.Millisecond)))
		sleepDuration := backoff + jitter
		if sleepDuration > maxBackoff {
			sleepDuration = maxBackoff
		}

		select {
		case <-c.reconnectCtx.Done():
			return
		case <-time.After(sleepDuration):
		}

		if err := c.connect(); err == nil {
			return
		} else if errors.Is(err, ErrUnauthorized) {
			_ = c.Close()
			return
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) writeLoop() {
	pingTicker := time.NewTicker(20 * time.Second)
	var turnTimer *time.Timer
	var turnTimerC <-chan time.Time
	defer func() {
		pingTicker.Stop()
		if turnTimer != nil {
			turnTimer.Stop()
		}
	}()

	for {
		select {
		case <-c.reconnectCtx.Done():
			return
		case delay := <-c.turnRefreshCh:
			if turnTimer != nil {
				turnTimer.Stop()
			}
			turnTimer = time.NewTimer(delay)
			turnTimerC = turnTimer.C
		case <-turnTimerC:
			turnTimerC = nil
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()
			if conn != nil {
				reqBytes, _ := json.Marshal(protocol.Envelope{
					Type:     protocol.MsgTURNRefresh,
					SenderID: c.nodeID,
					TargetID: "coordinator",
				})
				c.writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err := conn.WriteMessage(websocket.TextMessage, reqBytes)
				c.writeMu.Unlock()
				if err != nil {
					c.mu.Lock()
					if !c.closed && c.conn == conn {
						_ = c.conn.Close()
						c.conn = nil
					}
					c.mu.Unlock()
				}
			}
		case msg := <-c.sendQueue:
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()
			if conn != nil {
				c.writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err := conn.WriteMessage(websocket.TextMessage, msg)
				c.writeMu.Unlock()
				if err != nil {
					c.mu.Lock()
					if !c.closed && c.conn == conn {
						_ = c.conn.Close()
						c.conn = nil
					}
					c.mu.Unlock()
				}
			}
		case <-pingTicker.C:
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()
			if conn != nil {
				c.writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				c.writeMu.Unlock()
				if err != nil {
					c.mu.Lock()
					if !c.closed && c.conn == conn {
						_ = c.conn.Close()
						c.conn = nil
					}
					c.mu.Unlock()
				}
			}
		}
	}
}

// Send sends an envelope to the signaling server.
func (c *Client) Send(env protocol.Envelope) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("signaling client closed")
	}
	c.mu.Unlock()

	data, err := json.Marshal(env)
	if err != nil {
		return err
	}

	select {
	case c.sendQueue <- data:
		return nil
	default:
		return errors.New("send queue full")
	}
}

// Close closes the signaling connection.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.cancel()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()

	if conn != nil {
		c.writeMu.Lock()
		_ = conn.Close()
		c.writeMu.Unlock()
	}
	return nil
}

// URL returns the WebSocket URL the client connects to.
func (c *Client) URL() string {
	return c.url
}

func newQueryHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Authorization")
			if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return fmt.Errorf("cross-host redirect refused")
			}
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// QueryPeers requests the current list of online node IDs from the coordinator.
func (c *Client) QueryPeers(ctx context.Context) ([]string, error) {
	// First attempt via HTTP /api/nodes if reachable
	httpURL := c.url
	if strings.HasPrefix(httpURL, "ws://") {
		httpURL = "http://" + strings.TrimPrefix(httpURL, "ws://")
	} else if strings.HasPrefix(httpURL, "wss://") {
		httpURL = "https://" + strings.TrimPrefix(httpURL, "wss://")
	}
	httpURL = strings.TrimSuffix(httpURL, "/ws")

	req, err := http.NewRequestWithContext(ctx, "GET", httpURL+"/api/nodes", nil)
	if err == nil {
		if c.authToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.authToken)
		}
		client := newQueryHTTPClient()
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var result struct {
					Count      int               `json:"count"`
					Nodes      []string          `json:"nodes"`
					PublicKeys map[string]string `json:"public_keys,omitempty"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
					if result.Nodes == nil {
						result.Nodes = []string{}
					}
					if len(result.PublicKeys) > 0 {
						c.mu.Lock()
						for id, pk := range result.PublicKeys {
							if id != "" && pk != "" {
								norm := strings.ToLower(pk)
								if existing, ok := c.peerKeys[id]; !ok || existing == norm {
									c.peerKeys[id] = norm
								}
							}
						}
						c.mu.Unlock()
					}
					return result.Nodes, nil
				}
			}
		}
	}

	// Also request via WebSocket peer_list_req
	_ = c.Send(protocol.Envelope{
		Type:     protocol.MsgPeerListRequest,
		SenderID: c.nodeID,
		TargetID: "coordinator",
	})

	return nil, nil
}
