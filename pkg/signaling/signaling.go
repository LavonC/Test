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
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/unicompute/obj1/pkg/protocol"
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

// Hub manages WebSocket signaling connections and routes messages between peers.
type Hub struct {
	mu         sync.RWMutex
	clients    map[string]*clientConn
	pinnedKeys map[string]string // nodeID -> hex-encoded ed25519 public key (TOFU/pre-seeded)
	httpServer *http.Server
	listener   net.Listener
	addr       string
	authToken  string
	closed     bool
}

type clientConn struct {
	nodeID   string
	conn     *websocket.Conn
	sendChan chan []byte
	writeMu  sync.Mutex
	hub      *Hub
	done     chan struct{}
	doneOnce sync.Once
}

// NewHub starts a signaling hub HTTP server on the given address, optionally requiring an auth token.
func NewHub(listenAddr string, authTokens ...string) (*Hub, error) {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}

	h := &Hub{
		clients:    make(map[string]*clientConn),
		pinnedKeys: make(map[string]string),
		listener:   ln,
		addr:       ln.Addr().String(),
	}
	if len(authTokens) > 0 {
		h.authToken = authTokens[0]
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
		for id := range h.clients {
			nodes = append(nodes, id)
		}
		h.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"count": len(nodes),
			"nodes": nodes,
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
	defer h.mu.Unlock()
	h.pinnedKeys[nodeID] = strings.ToLower(hex.EncodeToString(pubKey))
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

	c := &clientConn{
		conn:     conn,
		sendChan: make(chan []byte, 100),
		hub:      h,
		done:     make(chan struct{}),
	}

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
			if !ed25519.Verify(ed25519.PublicKey(pubBytes), []byte(targetNodeID), sigBytes) {
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
			if pinnedPub, pinned := c.hub.pinnedKeys[targetNodeID]; pinned && pinnedPub != normPubKey {
				c.hub.mu.Unlock()
				c.rejectRegister(targetNodeID, "nodeID public key mismatch")
				return
			}
			c.hub.pinnedKeys[targetNodeID] = normPubKey
			c.nodeID = targetNodeID
			c.hub.clients[c.nodeID] = c

			// Broadcast peer joined notification to all OTHER nodes
			joinPayload, _ := json.Marshal(protocol.PeerEventPayload{NodeID: c.nodeID})
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
			c.hub.mu.Unlock()

			ackEnv := protocol.Envelope{
				Type:     protocol.MsgRegisterAck,
				SenderID: "coordinator",
				TargetID: c.nodeID,
			}
			data, _ := json.Marshal(ackEnv)
			select {
			case c.sendChan <- data:
			default:
			}
			continue
		}

		if env.Type == protocol.MsgPeerListRequest {
			c.hub.mu.RLock()
			nodes := make([]string, 0, len(c.hub.clients))
			for id := range c.hub.clients {
				nodes = append(nodes, id)
			}
			c.hub.mu.RUnlock()

			respPayload, _ := json.Marshal(protocol.PeerListPayload{Nodes: nodes})
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
	mu           sync.Mutex
	writeMu      sync.Mutex // Serializes all writes to WebSocket connection
	url          string
	nodeID       string
	authToken    string
	privKey      ed25519.PrivateKey
	pubKey       ed25519.PublicKey
	conn         *websocket.Conn
	handler      func(protocol.Envelope)
	reconnectCtx context.Context
	cancel       context.CancelFunc
	sendQueue    chan []byte
	closed       bool
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
		url:          wsURL,
		nodeID:       nodeID,
		privKey:      privKey,
		pubKey:       pubKey,
		handler:      handler,
		reconnectCtx: ctx,
		cancel:       cancel,
		sendQueue:    make(chan []byte, 100),
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

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return errors.New("signaling client closed")
	}
	c.conn = conn
	c.mu.Unlock()

	// Register immediately under write lock with ed25519 signature
	sig := ed25519.Sign(c.privKey, []byte(c.nodeID))
	regPayload, _ := json.Marshal(protocol.RegisterPayload{
		NodeID:    c.nodeID,
		PublicKey: hex.EncodeToString(c.pubKey),
		Signature: hex.EncodeToString(sig),
	})
	regEnv := protocol.Envelope{
		Type:     protocol.MsgRegister,
		SenderID: c.nodeID,
		Payload:  regPayload,
	}
	data, _ := json.Marshal(regEnv)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, data)
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
		if err := json.Unmarshal(msg, &env); err == nil && c.handler != nil {
			c.handler(env)
		}
	}
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
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) writeLoop() {
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-c.reconnectCtx.Done():
			return
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
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var result struct {
					Count int      `json:"count"`
					Nodes []string `json:"nodes"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
					if result.Nodes == nil {
						result.Nodes = []string{}
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
