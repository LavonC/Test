package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/unicompute/obj1/pkg/protocol"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local & peer testing
	},
}

// Hub manages WebSocket signaling connections and routes messages between peers.
type Hub struct {
	mu         sync.RWMutex
	clients    map[string]*clientConn
	httpServer *http.Server
	listener   net.Listener
	addr       string
	closed     bool
}

type clientConn struct {
	nodeID   string
	conn     *websocket.Conn
	sendChan chan []byte
	hub      *Hub
}

// NewHub starts a signaling hub HTTP server on the given address.
func NewHub(listenAddr string) (*Hub, error) {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}

	h := &Hub{
		clients:  make(map[string]*clientConn),
		listener: ln,
		addr:     ln.Addr().String(),
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

func (h *Hub) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	c := &clientConn{
		conn:     conn,
		sendChan: make(chan []byte, 100),
		hub:      h,
	}

	go c.writePump()
	c.readPump()
}

func (c *clientConn) readPump() {
	defer func() {
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
			var reg protocol.RegisterPayload
			_ = json.Unmarshal(env.Payload, &reg)
			c.nodeID = reg.NodeID
			if c.nodeID == "" {
				c.nodeID = env.SenderID
			}
			c.hub.mu.Lock()
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
			c.sendChan <- data
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
			c.hub.mu.RLock()
			target, ok := c.hub.clients[env.TargetID]
			c.hub.mu.RUnlock()
			if ok {
				target.sendChan <- msg
			}
		}
	}
}

func (c *clientConn) writePump() {
	ticker := time.NewTicker(20 * time.Second) // 20s ping ticker as specified in Section 6.3
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.sendChan:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Client connects to the signaling hub and handles peer negotiation.
type Client struct {
	mu           sync.Mutex
	writeMu      sync.Mutex // Serializes all writes to WebSocket connection
	url          string
	nodeID       string
	conn         *websocket.Conn
	handler      func(protocol.Envelope)
	reconnectCtx context.Context
	cancel       context.CancelFunc
	sendQueue    chan []byte
	closed       bool
}

// NewClient initializes a signaling client with auto-reconnection and keepalive.
func NewClient(wsURL string, nodeID string, handler func(protocol.Envelope)) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		url:          wsURL,
		nodeID:       nodeID,
		handler:      handler,
		reconnectCtx: ctx,
		cancel:       cancel,
		sendQueue:    make(chan []byte, 100),
	}

	if err := c.connect(); err != nil {
		cancel()
		return nil, err
	}

	go c.readLoop()
	go c.writeLoop()
	return c, nil
}

func (c *Client) connect() error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	conn, _, err := dialer.DialContext(c.reconnectCtx, c.url, nil)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	// Register immediately under write lock
	regPayload, _ := json.Marshal(protocol.RegisterPayload{
		NodeID: c.nodeID,
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
			if !c.closed {
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

		jitter := time.Duration(rand.Int63n(int64(250 * time.Millisecond)))
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
				_ = conn.WriteMessage(websocket.TextMessage, msg)
				c.writeMu.Unlock()
			}
		case <-pingTicker.C:
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()
			if conn != nil {
				c.writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.PingMessage, nil)
				c.writeMu.Unlock()
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
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			var result struct {
				Count int      `json:"count"`
				Nodes []string `json:"nodes"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
				return result.Nodes, nil
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
