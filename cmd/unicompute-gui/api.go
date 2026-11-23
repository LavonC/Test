package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/unicompute/obj1/pkg/codeexec"
	"github.com/unicompute/obj1/pkg/gpu"
	"github.com/unicompute/obj1/pkg/nat"
	"github.com/unicompute/obj1/pkg/node"
	"github.com/unicompute/obj1/pkg/remoteshell"
)

type wsClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

// ServerState encapsulates the active node and GUI runtime state.
type ServerState struct {
	mu             sync.RWMutex
	node           *node.Node
	nodeConfig     node.NodeConfig
	gpuEnabled     bool
	gpuConfig      gpu.ManagerConfig
	coordinatorURL string
	sessionToken   string
	activePort     int
	upgrader       websocket.Upgrader
	wsClients      map[*wsClient]bool
	wsMu           sync.Mutex
	eventLog       []EventMessage
	pendingReqs    map[string]string // peerID -> status
	pendingPings   map[string]chan time.Duration
	pingMu         sync.Mutex
	startTime      time.Time
	relayThreshold float64
	joinToken      string
	allowRemoteGUI bool
	tasksMu        sync.RWMutex
	tasks          []ClusterTask
}

// ClusterTask represents a DAG / ClassAd scheduled cluster task.
type ClusterTask struct {
	JobID       string `json:"job_id"`
	ClassAd     string `json:"class_ad"`
	TargetMatch string `json:"target_match"`
	StageDAG    string `json:"stage_dag"`
	Priority    string `json:"priority"`
	Status      string `json:"status"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	Timestamp   string `json:"timestamp"`
}

// EventMessage represents a real-time event sent to the GUI.
type EventMessage struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	PeerID    string    `json:"peer_id,omitempty"`
	Message   string    `json:"message"`
	Payload   any       `json:"payload,omitempty"`
}

func shortID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}

func (s *ServerState) SessionToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionToken
}

func (s *ServerState) SetSessionToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionToken = token
}

func (s *ServerState) SetActivePort(port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activePort = port
}

func (s *ServerState) SetAllowRemoteGUI(allow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowRemoteGUI = allow
}

func (s *ServerState) isAllowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	s.mu.RLock()
	allowRemote := s.allowRemoteGUI
	expectedPort := s.activePort
	s.mu.RUnlock()

	host := u.Hostname()
	port := u.Port()

	isLoopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
	portMatches := (expectedPort == 0 || port == "" || port == fmt.Sprintf("%d", expectedPort))

	if !allowRemote {
		return isLoopback && portMatches
	}

	if isLoopback && portMatches {
		return true
	}
	if r.Host != "" && u.Host == r.Host {
		return true
	}
	return false
}

func NewServerState(n *node.Node, nCfg node.NodeConfig, gpuEnabled bool, gCfg gpu.ManagerConfig) *ServerState {
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)

	joinBytes := make([]byte, 4)
	_, _ = rand.Read(joinBytes)
	initJoinToken := fmt.Sprintf("UC-%04X-%04X", binary.BigEndian.Uint16(joinBytes[0:2]), binary.BigEndian.Uint16(joinBytes[2:4]))

	s := &ServerState{
		node:           n,
		nodeConfig:     nCfg,
		gpuEnabled:     gpuEnabled,
		gpuConfig:      gCfg,
		coordinatorURL: nCfg.SignalingURL,
		sessionToken:   hex.EncodeToString(tokenBytes),
		startTime:      time.Now(),
		relayThreshold: 2.0,
		joinToken:      initJoinToken,
		wsClients:      make(map[*wsClient]bool),
		eventLog:       make([]EventMessage, 0, 100),
		pendingReqs:    make(map[string]string),
		pendingPings:   make(map[string]chan time.Duration),
		tasks:          []ClusterTask{}, // Real empty task queue; no fake seed tasks
	}

	s.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return s.isAllowedOrigin(r)
		},
	}

	// Register event hooks from node
	n.SetOnFleetPeerJoined(func(peerID string) {
		s.Broadcast("fleet_peer_joined", peerID, fmt.Sprintf("Peer '%s' registered with coordinator", peerID), nil)
	})

	n.SetOnFleetPeerLeft(func(peerID string) {
		s.Broadcast("fleet_peer_left", peerID, fmt.Sprintf("Peer '%s' disconnected from coordinator", peerID), nil)
	})

	n.SetOnPeerConnected(func(peerID string) {
		metrics, _ := n.GetPathMetrics(peerID)
		s.Broadcast("peer_connected", peerID, fmt.Sprintf("P2P WebRTC DataChannel established with '%s'", peerID), metrics)
	})

	n.SetOnPeerDisconnected(func(peerID string) {
		s.Broadcast("peer_disconnected", peerID, fmt.Sprintf("P2P link closed with '%s'", peerID), nil)
	})

	n.SetOnStreamMessage(func(peerID string, data []byte) {
		msg := string(data)
		if strings.HasPrefix(msg, "__PING__:") {
			ts := strings.TrimPrefix(msg, "__PING__:")
			_ = n.SendStream(peerID, []byte("__PONG__:"+ts))
			return
		}
		if strings.HasPrefix(msg, "__PONG__:") {
			parts := strings.Split(strings.TrimPrefix(msg, "__PONG__:"), ":")
			if len(parts) >= 2 {
				pingID := parts[1]
				s.pingMu.Lock()
				ch, ok := s.pendingPings[pingID]
				s.pingMu.Unlock()
				if ok && ch != nil {
					if tsNano, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
						rtt := time.Duration(time.Now().UnixNano() - tsNano)
						n.RecordPeerRTT(peerID, rtt)
						select {
						case ch <- rtt:
						default:
						}
					}
				}
			}
			return
		}
		s.Broadcast("stream_message", peerID, msg, map[string]string{"from": peerID, "text": msg})
	})

	n.SetOnChunkReceived(func(peerID, chunkID string, size int64) {
		s.Broadcast("chunk_received", peerID, fmt.Sprintf("Received chunk %s (%d bytes) from %s", shortID(chunkID, 8), size, peerID), map[string]any{
			"chunk_id": chunkID,
			"size":     size,
		})
	})

	return s
}

// addTask prepends a new task to the global cluster task queue.
func (s *ServerState) addTask(task ClusterTask) {
	s.tasksMu.Lock()
	s.tasks = append([]ClusterTask{task}, s.tasks...)
	if len(s.tasks) > 50 {
		s.tasks = s.tasks[:50]
	}
	s.tasksMu.Unlock()
	s.Broadcast("task_dispatched", "", fmt.Sprintf("Cluster task %s updated: %s", task.JobID, task.Status), task)
}

// Broadcast dispatches an event to all connected GUI browser/desktop windows.
func (s *ServerState) Broadcast(eventType, peerID, message string, payload any) {
	evt := EventMessage{
		Timestamp: time.Now(),
		Type:      eventType,
		PeerID:    peerID,
		Message:   message,
		Payload:   payload,
	}

	s.wsMu.Lock()
	if len(s.eventLog) >= 200 {
		s.eventLog = s.eventLog[1:]
	}
	s.eventLog = append(s.eventLog, evt)

	clients := make([]*wsClient, 0, len(s.wsClients))
	for client := range s.wsClients {
		clients = append(clients, client)
	}
	s.wsMu.Unlock()

	for _, client := range clients {
		client.writeMu.Lock()
		_ = client.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		err := client.conn.WriteJSON(evt)
		client.writeMu.Unlock()

		if err != nil {
			s.wsMu.Lock()
			delete(s.wsClients, client)
			s.wsMu.Unlock()
			_ = client.conn.Close()
		}
	}
}

// authMiddleware enforces Origin checks, constant-time bearer token auth, and Content-Type validation.
func (s *ServerState) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Origin header validation (if present)
		if !s.isAllowedOrigin(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}

		// 2. Token validation (Authorization: Bearer <token> or ?token=<token>)
		s.mu.RLock()
		expectedToken := s.sessionToken
		s.mu.RUnlock()

		if expectedToken != "" {
			token := ""
			authHdr := r.Header.Get("Authorization")
			if strings.HasPrefix(authHdr, "Bearer ") {
				token = strings.TrimPrefix(authHdr, "Bearer ")
			} else {
				token = r.URL.Query().Get("token")
			}

			if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		// 3. Content-Type validation for mutating requests (POST, PUT, DELETE)
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(strings.ToLower(ct), "application/json") {
				http.Error(w, "unsupported media type: application/json required", http.StatusUnsupportedMediaType)
				return
			}
		}

		next(w, r)
	}
}

// SetupRoutes registers all REST and WebSocket API endpoints wrapped in auth middleware.
func (s *ServerState) SetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/status", s.authMiddleware(s.handleStatus))
	mux.HandleFunc("/api/coordinator/connect", s.authMiddleware(s.handleCoordinatorConnect))
	mux.HandleFunc("/api/coordinator/status", s.authMiddleware(s.handleCoordinatorStatus))
	mux.HandleFunc("/api/coordinator/settings", s.authMiddleware(s.handleCoordinatorSettings))
	mux.HandleFunc("/api/tasks", s.authMiddleware(s.handleTasks))
	mux.HandleFunc("/api/fleet", s.authMiddleware(s.handleFleet))
	mux.HandleFunc("/api/peers", s.authMiddleware(s.handlePeers))
	mux.HandleFunc("/api/peer/connect", s.authMiddleware(s.handlePeerConnect))
	mux.HandleFunc("/api/peer/disconnect", s.authMiddleware(s.handlePeerDisconnect))
	mux.HandleFunc("/api/peer/ping", s.authMiddleware(s.handlePeerPing))
	mux.HandleFunc("/api/peer/stream", s.authMiddleware(s.handlePeerStream))
	mux.HandleFunc("/api/gpu", s.authMiddleware(s.handleGPU))
	mux.HandleFunc("/api/gpu/hipify", s.authMiddleware(s.handleGPUHipify))
	mux.HandleFunc("/api/gpu/run", s.authMiddleware(s.handleGPURun))
	mux.HandleFunc("/api/code/run", s.authMiddleware(s.handleCodeRun))
	mux.HandleFunc("/api/shell/exec", s.authMiddleware(s.handleShellExec))
	mux.HandleFunc("/api/storage/blobs", s.authMiddleware(s.handleStorageBlobs))
	mux.HandleFunc("/api/storage/get", s.authMiddleware(s.handleStorageGet))
	mux.HandleFunc("/api/storage/put", s.authMiddleware(s.handleStoragePut))
	mux.HandleFunc("/api/storage/del", s.authMiddleware(s.handleStorageDel))
	mux.HandleFunc("/api/storage/scrub", s.authMiddleware(s.handleStorageScrub))
	mux.HandleFunc("/api/samples", s.authMiddleware(s.handleSamples))
	mux.HandleFunc("/api/events", s.authMiddleware(s.handleEventsWS))
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *ServerState) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	coordURL := s.coordinatorURL
	joinToken := s.joinToken
	nodeID := s.nodeConfig.NodeID
	poolID := s.nodeConfig.PoolID
	s.mu.RUnlock()

	usage := s.node.StorageUsage()
	natRep := s.node.NATReport()
	if natRep == nil {
		natRep = &nat.NATReport{NATType: nat.NATTypeUnknown}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	fleet, _ := s.node.GetFleetNodes(ctx)
	cancel()
	peers := s.node.GetConnectedPeers()

	gpuProf, gpuActive := s.node.GPUProfile()
	wdStat, _ := s.node.GPUWatchdogStatus()

	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":          nodeID,
		"pool_id":          poolID,
		"coordinator_url":  coordURL,
		"join_token":       joinToken,
		"storage":          usage,
		"nat":              natRep,
		"fleet_count":      len(fleet),
		"peer_count":       len(peers),
		"gpu_enabled":      gpuActive,
		"gpu_profile":      gpuProf,
		"watchdog":         wdStat,
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"cpus":             runtime.NumCPU(),
		"go_version":       runtime.Version(),
		"docker_available": s.node.HasCodeExecutor(),
	})
}

func (s *ServerState) handleCoordinatorConnect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		http.Error(w, "invalid coordinator url", http.StatusBadRequest)
		return
	}

	url := strings.TrimSpace(body.URL)
	s.mu.Lock()
	s.coordinatorURL = url
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.node.ReconnectSignaling(ctx, url)

	s.Broadcast("coordinator_reconnect", "", fmt.Sprintf("Connecting to coordinator: %s", url), body.URL)
	writeJSON(w, http.StatusOK, map[string]any{"status": "reconnecting", "url": url})
}

func (s *ServerState) handleCoordinatorStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	coordURL := s.coordinatorURL
	startTime := s.startTime
	relayThreshold := s.relayThreshold
	joinToken := s.joinToken
	authToken := s.nodeConfig.AuthToken
	s.mu.RUnlock()

	httpURL := coordURL
	if strings.HasPrefix(httpURL, "wss://") {
		httpURL = "https://" + strings.TrimPrefix(httpURL, "wss://")
	} else if strings.HasPrefix(httpURL, "ws://") {
		httpURL = "http://" + strings.TrimPrefix(httpURL, "ws://")
	}
	httpURL = strings.TrimSuffix(httpURL, "/ws")

	client := &http.Client{Timeout: 1200 * time.Millisecond}
	status := "RUNNING"
	fleetCount := 0
	nodeIDs := []string{}

	// Query coordinator /health
	healthResp, err := client.Get(httpURL + "/health")
	if err == nil && healthResp.StatusCode == http.StatusOK {
		healthResp.Body.Close()
		// Health succeeded; query coordinator /api/nodes
		req, err := http.NewRequest("GET", httpURL+"/api/nodes", nil)
		if err == nil {
			if authToken != "" {
				req.Header.Set("Authorization", "Bearer "+authToken)
			}
			nodesResp, err := client.Do(req)
			if err == nil && nodesResp.StatusCode == http.StatusOK {
				defer nodesResp.Body.Close()
				var nData struct {
					Count int      `json:"count"`
					Nodes []string `json:"nodes"`
				}
				if json.NewDecoder(nodesResp.Body).Decode(&nData) == nil {
					fleetCount = nData.Count
					nodeIDs = nData.Nodes
				}
			} else if nodesResp != nil {
				nodesResp.Body.Close()
			}
		}
	} else {
		if healthResp != nil {
			healthResp.Body.Close()
		}
		// Remote HTTP probe failed or timed out; check locally cached fleet with fast timeout
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		fleet, fErr := s.node.GetFleetNodes(ctx)
		cancel()
		if fErr == nil && len(fleet) > 0 {
			fleetCount = len(fleet)
			nodeIDs = fleet
			status = "CONNECTED"
		} else {
			status = "DISCONNECTED"
			fleetCount = 0
			nodeIDs = []string{}
		}
	}

	connectedPeers := s.node.GetConnectedPeers()
	directCount := 0
	relayedCount := 0
	var totalDirectRTT int64
	directNodes := []string{}
	relayedNodes := []string{}

	for _, p := range connectedPeers {
		if m, ok := s.node.GetPathMetrics(p); ok && m.IsDirect {
			directCount++
			directNodes = append(directNodes, p)
			totalDirectRTT += m.RTT.Milliseconds()
		} else {
			relayedCount++
			relayedNodes = append(relayedNodes, p)
		}
	}

	avgRTT := 0.0
	if directCount > 0 {
		avgRTT = float64(totalDirectRTT) / float64(directCount)
	}

	uptimeDur := time.Since(startTime)
	uptimeStr := fmt.Sprintf("%dh %02dm %02ds", int(uptimeDur.Hours()), int(uptimeDur.Minutes())%60, int(uptimeDur.Seconds())%60)

	usage := s.node.StorageUsage()
	natRep := s.node.NATReport()
	natType := "Unknown"
	if natRep != nil {
		natType = string(natRep.NATType)
	}

	coordHost := ""
	if u, err := url.Parse(coordURL); err == nil && u.Host != "" {
		coordHost = u.Host
	} else {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(coordURL, "wss://"), "ws://")
		if idx := strings.Index(trimmed, "/"); idx != -1 {
			coordHost = trimmed[:idx]
		} else {
			coordHost = trimmed
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":              status,
		"uptime":              uptimeStr,
		"coordinator_url":     coordURL,
		"coordinator_host":    coordHost,
		"http_url":            httpURL,
		"transport":           "WebRTC DataChannel (ICE/DTLS/SCTP)",
		"blob_count":          usage.BlobCount,
		"storage_bytes":       usage.UsedBytes,
		"nat_type":            natType,
		"fleet_count":         fleetCount,
		"node_ids":            nodeIDs,
		"join_token":          joinToken,
		"relay_threshold":     relayThreshold,
		"avg_rtt_ms":          avgRTT,
		"direct_peers_count":  directCount,
		"relayed_peers_count": relayedCount,
		"direct_nodes":        directNodes,
		"relayed_nodes":       relayedNodes,
		"throttled_count":     0,
	})
}

func (s *ServerState) handleCoordinatorSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		var body struct {
			RelayThreshold *float64 `json:"relay_threshold"`
			RotateToken    bool     `json:"rotate_token"`
			JoinToken      string   `json:"join_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid settings payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		s.mu.Lock()
		if body.RelayThreshold != nil {
			val := *body.RelayThreshold
			if val < 0.5 || val > 5.0 {
				s.mu.Unlock()
				http.Error(w, "relay_threshold must be between 0.5 and 5.0", http.StatusBadRequest)
				return
			}
			s.relayThreshold = val
			s.node.SetFallbackTimeout(time.Duration(val * float64(time.Second)))
		}
		if body.RotateToken {
			u := uuid.New()
			s.joinToken = fmt.Sprintf("UC-%02X%02X-%02X%02X", u[0], u[1], u[2], u[3])
		} else if body.JoinToken != "" {
			s.joinToken = strings.TrimSpace(body.JoinToken)
		}
		thresh := s.relayThreshold
		token := s.joinToken
		s.mu.Unlock()

		s.Broadcast("coordinator_settings_updated", "", fmt.Sprintf("Relay threshold set to %.1fs", thresh), map[string]any{
			"relay_threshold": thresh,
			"join_token":      token,
		})
	}

	s.mu.RLock()
	thresh := s.relayThreshold
	token := s.joinToken
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"relay_threshold": thresh,
		"join_token":      token,
	})
}

func (s *ServerState) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		var body ClusterTask
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid task payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.JobID == "" {
			s.tasksMu.RLock()
			taskCount := len(s.tasks)
			s.tasksMu.RUnlock()
			body.JobID = fmt.Sprintf("#JOB-%d", 8100+taskCount+1)
		}
		if body.Timestamp == "" {
			body.Timestamp = time.Now().Format("15:04:05")
		}
		s.addTask(body)
		writeJSON(w, http.StatusOK, body)
		return
	}

	s.tasksMu.RLock()
	tasksCopy := make([]ClusterTask, len(s.tasks))
	copy(tasksCopy, s.tasks)
	s.tasksMu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"tasks": tasksCopy,
		"count": len(tasksCopy),
	})
}

func (s *ServerState) handleFleet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fleet, err := s.node.GetFleetNodes(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"nodes": []string{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": fleet})
}

func (s *ServerState) handlePeers(w http.ResponseWriter, r *http.Request) {
	connectedPeers := s.node.GetConnectedPeers()
	type PeerMetrics struct {
		IsDirect        bool    `json:"is_direct"`
		RTTMs           float64 `json:"rtt_ms"`
		LocalCandidate  string  `json:"local_candidate,omitempty"`
		RemoteCandidate string  `json:"remote_candidate,omitempty"`
		PairType        string  `json:"pair_type,omitempty"`
		BytesSent       int64   `json:"bytes_sent"`
		BytesReceived   int64   `json:"bytes_received"`
	}
	type PeerInfo struct {
		PeerID   string       `json:"peer_id"`
		IsDirect bool         `json:"is_direct"`
		Metrics  *PeerMetrics `json:"metrics,omitempty"`
	}

	result := make([]PeerInfo, 0, len(connectedPeers))
	for _, p := range connectedPeers {
		info := PeerInfo{PeerID: p}
		if m, ok := s.node.GetPathMetrics(p); ok {
			rttMs := float64(m.RTT.Microseconds()) / 1000.0
			info.IsDirect = m.IsDirect
			info.Metrics = &PeerMetrics{
				IsDirect:        m.IsDirect,
				RTTMs:           rttMs,
				LocalCandidate:  m.LocalCandidate,
				RemoteCandidate: m.RemoteCandidate,
				PairType:        string(m.PairType),
				BytesSent:       m.BytesSent,
				BytesReceived:   m.BytesReceived,
			}
		}
		result = append(result, info)
	}

	writeJSON(w, http.StatusOK, map[string]any{"peers": result})
}

func (s *ServerState) handlePeerConnect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		PeerID string `json:"peer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PeerID == "" {
		http.Error(w, "peer_id required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s.Broadcast("connection_request_sent", body.PeerID, fmt.Sprintf("Establishing P2P link with %s...", body.PeerID), nil)

	_, err := s.node.ConnectPeer(ctx, body.PeerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	metrics, _ := s.node.GetPathMetrics(body.PeerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "connected",
		"peer_id": body.PeerID,
		"metrics": metrics,
	})
}

func (s *ServerState) handlePeerDisconnect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		PeerID string `json:"peer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PeerID == "" {
		http.Error(w, "peer_id required", http.StatusBadRequest)
		return
	}

	err := s.node.DisconnectPeer(body.PeerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "disconnected", "peer_id": body.PeerID})
}

func (s *ServerState) handlePeerPing(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		PeerID string `json:"peer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PeerID == "" {
		http.Error(w, "peer_id required", http.StatusBadRequest)
		return
	}

	pingID := uuid.New().String()[:8]
	respCh := make(chan time.Duration, 1)

	s.pingMu.Lock()
	s.pendingPings[pingID] = respCh
	s.pingMu.Unlock()

	defer func() {
		s.pingMu.Lock()
		delete(s.pendingPings, pingID)
		s.pingMu.Unlock()
	}()

	nowNano := fmt.Sprintf("%d", time.Now().UnixNano())
	err := s.node.SendStream(body.PeerID, []byte(fmt.Sprintf("__PING__:%s:%s", nowNano, pingID)))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	select {
	case rtt := <-respCh:
		rttMs := float64(rtt.Microseconds()) / 1000.0
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "pong_received",
			"peer_id":     body.PeerID,
			"duration_ms": rtt.Milliseconds(),
			"rtt_ms":      rttMs,
		})
	case <-time.After(5 * time.Second):
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "ping_timeout",
			"peer_id":     body.PeerID,
			"duration_ms": 5000,
			"rtt_ms":      5000.0,
		})
	}
}

func (s *ServerState) handlePeerStream(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		PeerID  string `json:"peer_id"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PeerID == "" {
		http.Error(w, "peer_id and message required", http.StatusBadRequest)
		return
	}

	err := s.node.SendStream(body.PeerID, []byte(body.Message))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "sent", "peer_id": body.PeerID})
}

func (s *ServerState) handleGPU(w http.ResponseWriter, r *http.Request) {
	prof, enabled := s.node.GPUProfile()
	wdStat, _ := s.node.GPUWatchdogStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  enabled,
		"profile":  prof,
		"watchdog": wdStat,
	})
}

func (s *ServerState) handleGPUHipify(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Source == "" {
		http.Error(w, "source code required", http.StatusBadRequest)
		return
	}

	res := gpu.TranslateCUDAToHIP(body.Source)
	writeJSON(w, http.StatusOK, res)
}

func (s *ServerState) handleGPURun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		TaskType   string `json:"task_type"`
		SourceCode string `json:"source_code"`
		Batches    int    `json:"batches"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if body.Batches <= 0 {
		body.Batches = 20
	}

	req := gpu.GPUTaskRequest{
		JobID: "gpu-job-" + uuid.New().String()[:8],
		Manifest: gpu.TaskCapsuleManifest{
			Name:         "gui-task",
			TargetTier:   "tier3",
			TaskType:     body.TaskType,
			TotalBatches: body.Batches,
		},
		SourceCode:     body.SourceCode,
		TimeoutSeconds: 60,
	}

	if body.TaskType == "inference" {
		req.InputTensors = map[string][]float32{
			"input": {1.0, 2.0, 3.0, 4.0},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	startGPU := time.Now()
	resp, err := s.node.ExecuteGPUTask(ctx, req)
	durMs := time.Since(startGPU).Milliseconds()

	status := "COMPLETED"
	if err != nil {
		status = "FAILED"
	}
	s.addTask(ClusterTask{
		JobID:       req.JobID,
		ClassAd:     fmt.Sprintf("Simulated ONNX Runner && Task == %q", body.TaskType),
		TargetMatch: fmt.Sprintf("Matched -> %s (DirectML Tier 3)", s.nodeConfig.NodeID),
		StageDAG:    "LOCAL_GPU_SIMULATION",
		Priority:    "P0 CRITICAL",
		Status:      status,
		DurationMs:  durMs,
		Timestamp:   time.Now().Format("15:04:05"),
	})

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "resp": resp})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *ServerState) isPeerConnected(peerID string) bool {
	for _, p := range s.node.GetConnectedPeers() {
		if p == peerID {
			return true
		}
	}
	return false
}

func (s *ServerState) handleCodeRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		PeerID   string `json:"peer_id"`
		Language string `json:"language"`
		Source   string `json:"source"`
		Stdin    string `json:"stdin"`
		Timeout  int    `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Source == "" {
		http.Error(w, "language and source required", http.StatusBadRequest)
		return
	}

	if body.Timeout <= 0 {
		body.Timeout = 10
	}

	req := codeexec.Request{
		JobID:          "code-" + uuid.New().String()[:8],
		Language:       body.Language,
		Source:         body.Source,
		Stdin:          body.Stdin,
		TimeoutSeconds: body.Timeout,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(body.Timeout+15)*time.Second)
	defer cancel()

	startRun := time.Now()

	// If local node or local worker is selected:
	if body.PeerID == "local" || body.PeerID == s.nodeConfig.NodeID || body.PeerID == "" {
		if !s.node.HasCodeExecutor() {
			s.addTask(ClusterTask{
				JobID:       req.JobID,
				ClassAd:     fmt.Sprintf("SandboxTier == Tier2 && Lang == %q", body.Language),
				TargetMatch: fmt.Sprintf("Matched -> %s (Local)", s.nodeConfig.NodeID),
				StageDAG:    "DAG_STAGE_CONTAINER_EXEC",
				Priority:    "P1 HIGH",
				Status:      "FAILED (NO_DOCKER)",
				DurationMs:  0,
				Timestamp:   time.Now().Format("15:04:05"),
			})
			writeJSON(w, http.StatusOK, map[string]any{
				"job_id":      req.JobID,
				"language":    body.Language,
				"exit_code":   -1,
				"duration_ms": 0,
				"error":       "Docker is not available or code execution is disabled on this worker. Start Docker Desktop (WSL2 backend) to execute Tier 2 containers locally, or target a connected fleet peer.",
			})
			return
		}

		resp, err := s.node.ExecuteCodeLocal(ctx, req)
		durMs := time.Since(startRun).Milliseconds()
		taskStatus := "COMPLETED"
		if err != nil || resp.ExitCode != 0 {
			taskStatus = "FAILED"
		}
		s.addTask(ClusterTask{
			JobID:       req.JobID,
			ClassAd:     fmt.Sprintf("SandboxTier == Tier2 && Lang == %q", body.Language),
			TargetMatch: fmt.Sprintf("Matched -> %s (Local)", s.nodeConfig.NodeID),
			StageDAG:    "DAG_STAGE_CONTAINER_EXEC",
			Priority:    "P1 HIGH",
			Status:      taskStatus,
			DurationMs:  durMs,
			Timestamp:   time.Now().Format("15:04:05"),
		})

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "resp": resp})
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Remote peer execution over WebRTC DataChannel.
	// If peer is in the fleet but not yet connected via WebRTC, auto-connect on-demand.
	if !s.isPeerConnected(body.PeerID) {
		s.Broadcast("P2P_CONNECT", body.PeerID, fmt.Sprintf("Auto-establishing direct WebRTC link with %s via coordinator...", body.PeerID), nil)
		connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
		_, err := s.node.ConnectPeer(connectCtx, body.PeerID)
		cancelConnect()
		if err != nil {
			s.addTask(ClusterTask{
				JobID:       req.JobID,
				ClassAd:     fmt.Sprintf("SandboxTier == Tier2 && Lang == %q", body.Language),
				TargetMatch: fmt.Sprintf("Matched -> %s", body.PeerID),
				StageDAG:    "DAG_STAGE_CONTAINER_EXEC",
				Priority:    "P1 HIGH",
				Status:      "FAILED (P2P_CONNECT)",
				DurationMs:  time.Since(startRun).Milliseconds(),
				Timestamp:   time.Now().Format("15:04:05"),
			})
			writeJSON(w, http.StatusOK, map[string]any{
				"job_id":      req.JobID,
				"language":    body.Language,
				"exit_code":   -1,
				"duration_ms": 0,
				"error":       fmt.Sprintf("Failed to auto-connect P2P WebRTC link with %s: %v", body.PeerID, err),
			})
			return
		}
		s.Broadcast("P2P_CONNECTED", body.PeerID, fmt.Sprintf("Direct WebRTC link active with %s. Dispatching task...", body.PeerID), nil)
	}

	resp, err := s.node.ExecuteRemoteCode(ctx, body.PeerID, req)
	durMs := time.Since(startRun).Milliseconds()
	taskStatus := "COMPLETED"
	if err != nil || resp.ExitCode != 0 {
		taskStatus = "FAILED"
	}
	s.addTask(ClusterTask{
		JobID:       req.JobID,
		ClassAd:     fmt.Sprintf("SandboxTier == Tier2 && Lang == %q", body.Language),
		TargetMatch: fmt.Sprintf("Matched -> %s", body.PeerID),
		StageDAG:    "DAG_STAGE_CONTAINER_EXEC",
		Priority:    "P1 HIGH",
		Status:      taskStatus,
		DurationMs:  durMs,
		Timestamp:   time.Now().Format("15:04:05"),
	})

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "resp": resp})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *ServerState) handleShellExec(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		PeerID  string `json:"peer_id"`
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Command == "" {
		http.Error(w, "command required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	if body.PeerID == "all" {
		res := s.node.ExecuteRemoteCommandAll(ctx, remoteshell.ShellRequest{
			Command:   body.Command,
			ShellType: remoteshell.ShellAuto,
		})
		writeJSON(w, http.StatusOK, map[string]any{"results": res})
		return
	}

	s.mu.RLock()
	enabled := s.nodeConfig.EnableRemoteShell
	nodeID := s.nodeConfig.NodeID
	s.mu.RUnlock()

	if body.PeerID == "local" || body.PeerID == nodeID || body.PeerID == "" {
		if !enabled {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "local/remote shell execution is disabled (start with -enable-remote-shell to opt in)"})
			return
		}
		executor := remoteshell.NewSystemExecutor()
		res := executor.Execute(ctx, remoteshell.ShellRequest{
			Command:   body.Command,
			ShellType: remoteshell.ShellAuto,
		})
		writeJSON(w, http.StatusOK, res)
		return
	}

	// If remote peer is not yet connected via WebRTC, auto-connect on-demand.
	if !s.isPeerConnected(body.PeerID) {
		s.Broadcast("P2P_CONNECT", body.PeerID, fmt.Sprintf("Auto-establishing direct WebRTC link with %s for shell command...", body.PeerID), nil)
		connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
		_, err := s.node.ConnectPeer(connectCtx, body.PeerID)
		cancelConnect()
		if err != nil {
			writeJSON(w, http.StatusOK, remoteshell.ShellResponse{
				ExitCode: -1,
				Error:    fmt.Sprintf("Failed to auto-connect P2P WebRTC link with %s: %v", body.PeerID, err),
			})
			return
		}
		s.Broadcast("P2P_CONNECTED", body.PeerID, fmt.Sprintf("Direct WebRTC link active with %s. Executing shell...", body.PeerID), nil)
	}

	res, err := s.node.ExecuteRemoteCommand(ctx, body.PeerID, remoteshell.ShellRequest{
		Command:   body.Command,
		ShellType: remoteshell.ShellAuto,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "res": res})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *ServerState) handleStorageBlobs(w http.ResponseWriter, r *http.Request) {
	blobs, err := s.node.ListBlobs()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	type enrichedBlob struct {
		ID        string    `json:"id"`
		Size      int64     `json:"size"`
		SizeBytes int64     `json:"size_bytes"`
		ModTime   time.Time `json:"mod_time"`
		CreatedAt time.Time `json:"created_at"`
	}
	enriched := make([]enrichedBlob, 0, len(blobs))
	for _, b := range blobs {
		enriched = append(enriched, enrichedBlob{
			ID:        b.ID,
			Size:      b.Size,
			SizeBytes: b.Size,
			ModTime:   b.ModTime,
			CreatedAt: b.ModTime,
		})
	}
	usage := s.node.StorageUsage()
	writeJSON(w, http.StatusOK, map[string]any{"blobs": enriched, "usage": usage})
}

func (s *ServerState) handleStorageGet(w http.ResponseWriter, r *http.Request) {
	blobID := r.URL.Query().Get("id")
	if blobID == "" {
		http.Error(w, "blob id required", http.StatusBadRequest)
		return
	}

	reader, err := s.node.GetBlob(blobID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	defer reader.Close()

	// Read up to 512 KiB for viewer preview
	buf := make([]byte, 512*1024)
	n, _ := io.ReadFull(reader, buf)
	content := string(buf[:n])

	verified, verifyErr := s.node.VerifyBlob(blobID)
	isValid := verified && verifyErr == nil

	writeJSON(w, http.StatusOK, map[string]any{
		"blob_id":    blobID,
		"content":    content,
		"size":       n,
		"size_bytes": n,
		"verified":   isValid,
		"valid":      isValid,
	})
}

func (s *ServerState) handleStoragePut(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}

	// Compute real SHA-256 hash as blobID (64 hex characters)
	h := sha256.Sum256([]byte(body.Content))
	blobID := hex.EncodeToString(h[:])
	size, err := s.node.PutBlob(blobID, strings.NewReader(body.Content))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	s.Broadcast("storage_put", "", fmt.Sprintf("Stored blob %s (%d bytes)", blobID, size), map[string]any{
		"blob_id":    blobID,
		"size":       size,
		"size_bytes": size,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"blob_id":    blobID,
		"size":       size,
		"size_bytes": size,
	})
}

func (s *ServerState) handleStorageDel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		BlobID string `json:"blob_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.BlobID == "" {
		http.Error(w, "blob_id required", http.StatusBadRequest)
		return
	}

	err := s.node.DeleteBlob(body.BlobID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.Broadcast("storage_del", "", fmt.Sprintf("Deleted blob %s", body.BlobID), body.BlobID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "blob_id": body.BlobID})
}

func (s *ServerState) handleStorageScrub(w http.ResponseWriter, r *http.Request) {
	corrupted, err := s.node.Scrub()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "complete", "corrupted": corrupted})
}

func (s *ServerState) handleSamples(w http.ResponseWriter, r *http.Request) {
	type SampleItem struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Language string `json:"language"`
		Content  string `json:"content"`
	}

	samples := []SampleItem{}

	// Dynamically scan samples/ directory recursively
	_ = filepath.Walk("samples", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		var lang, cat string

		switch ext {
		case ".py":
			lang = "python"
			if strings.Contains(path, "security") {
				cat = "Security (O2)"
			} else {
				cat = "Python Container (O2)"
			}
		case ".c":
			lang = "c"
			cat = "C Container (O2)"
		case ".cpp", ".cc":
			lang = "cpp"
			cat = "C++ Container (O2)"
		case ".cu":
			lang = "cuda"
			cat = "GPU Kernel (O3)"
		default:
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr == nil {
			samples = append(samples, SampleItem{
				Name:     filepath.Base(path),
				Category: cat,
				Language: lang,
				Content:  string(data),
			})
		}
		return nil
	})

	writeJSON(w, http.StatusOK, map[string]any{"samples": samples})
}

func (s *ServerState) handleEventsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &wsClient{conn: conn}

	s.wsMu.Lock()
	s.wsClients[client] = true
	backlog := make([]EventMessage, len(s.eventLog))
	copy(backlog, s.eventLog)
	s.wsMu.Unlock()

	// Send recent backlog without holding wsMu
	for _, evt := range backlog {
		client.writeMu.Lock()
		_ = client.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		err := client.conn.WriteJSON(evt)
		client.writeMu.Unlock()
		if err != nil {
			s.wsMu.Lock()
			delete(s.wsClients, client)
			s.wsMu.Unlock()
			_ = conn.Close()
			return
		}
	}

	defer func() {
		s.wsMu.Lock()
		delete(s.wsClients, client)
		s.wsMu.Unlock()
		_ = conn.Close()
	}()

	// Read pump to detect close
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}
