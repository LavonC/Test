package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/unicompute/obj1/pkg/codeexec"
	"github.com/unicompute/obj1/pkg/gpu"
	"github.com/unicompute/obj1/pkg/instrumentation"
	"github.com/unicompute/obj1/pkg/nat"
	"github.com/unicompute/obj1/pkg/node"
	"github.com/unicompute/obj1/pkg/remoteshell"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ServerState encapsulates the active node and GUI runtime state.
type ServerState struct {
	mu             sync.RWMutex
	node           *node.Node
	nodeConfig     node.NodeConfig
	gpuEnabled     bool
	gpuConfig      gpu.ManagerConfig
	coordinatorURL string
	wsClients      map[*websocket.Conn]bool
	wsMu           sync.Mutex
	eventLog       []EventMessage
	pendingReqs    map[string]string // peerID -> status
}

// EventMessage represents a real-time event sent to the GUI.
type EventMessage struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	PeerID    string    `json:"peer_id,omitempty"`
	Message   string    `json:"message"`
	Payload   any       `json:"payload,omitempty"`
}

func NewServerState(n *node.Node, nCfg node.NodeConfig, gpuEnabled bool, gCfg gpu.ManagerConfig) *ServerState {
	s := &ServerState{
		node:           n,
		nodeConfig:     nCfg,
		gpuEnabled:     gpuEnabled,
		gpuConfig:      gCfg,
		coordinatorURL: nCfg.SignalingURL,
		wsClients:      make(map[*websocket.Conn]bool),
		eventLog:       make([]EventMessage, 0, 100),
		pendingReqs:    make(map[string]string),
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
		s.Broadcast("stream_message", peerID, msg, map[string]string{"from": peerID, "text": msg})
	})

	n.SetOnChunkReceived(func(peerID, chunkID string, size int64) {
		s.Broadcast("chunk_received", peerID, fmt.Sprintf("Received chunk %s (%d bytes) from %s", chunkID[:8], size, peerID), map[string]any{
			"chunk_id": chunkID,
			"size":     size,
		})
	})

	return s
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

	for conn := range s.wsClients {
		_ = conn.WriteJSON(evt)
	}
	s.wsMu.Unlock()
}

// SetupRoutes registers all REST and WebSocket API endpoints.
func (s *ServerState) SetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/coordinator/connect", s.handleCoordinatorConnect)
	mux.HandleFunc("/api/fleet", s.handleFleet)
	mux.HandleFunc("/api/peers", s.handlePeers)
	mux.HandleFunc("/api/peer/connect", s.handlePeerConnect)
	mux.HandleFunc("/api/peer/disconnect", s.handlePeerDisconnect)
	mux.HandleFunc("/api/peer/ping", s.handlePeerPing)
	mux.HandleFunc("/api/peer/stream", s.handlePeerStream)
	mux.HandleFunc("/api/gpu", s.handleGPU)
	mux.HandleFunc("/api/gpu/hipify", s.handleGPUHipify)
	mux.HandleFunc("/api/gpu/run", s.handleGPURun)
	mux.HandleFunc("/api/code/run", s.handleCodeRun)
	mux.HandleFunc("/api/shell/exec", s.handleShellExec)
	mux.HandleFunc("/api/storage/blobs", s.handleStorageBlobs)
	mux.HandleFunc("/api/storage/get", s.handleStorageGet)
	mux.HandleFunc("/api/storage/put", s.handleStoragePut)
	mux.HandleFunc("/api/storage/del", s.handleStorageDel)
	mux.HandleFunc("/api/storage/scrub", s.handleStorageScrub)
	mux.HandleFunc("/api/samples", s.handleSamples)
	mux.HandleFunc("/api/events", s.handleEventsWS)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *ServerState) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	usage := s.node.StorageUsage()
	natRep := s.node.NATReport()
	if natRep == nil {
		natRep = &nat.NATReport{NATType: nat.NATTypeUnknown}
	}
	fleet, _ := s.node.GetFleetNodes(context.Background())
	peers := s.node.GetConnectedPeers()

	gpuProf, gpuActive := s.node.GPUProfile()
	wdStat, _ := s.node.GPUWatchdogStatus()

	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":          s.nodeConfig.NodeID,
		"pool_id":          s.nodeConfig.PoolID,
		"coordinator_url":  s.coordinatorURL,
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
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		http.Error(w, "invalid coordinator url", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.coordinatorURL = strings.TrimSpace(body.URL)
	s.mu.Unlock()

	s.Broadcast("coordinator_reconnect", "", fmt.Sprintf("Connecting to coordinator: %s", s.coordinatorURL), body.URL)
	writeJSON(w, http.StatusOK, map[string]any{"status": "reconnecting", "url": s.coordinatorURL})
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
	type PeerInfo struct {
		PeerID   string                       `json:"peer_id"`
		IsDirect bool                         `json:"is_direct"`
		Metrics  *instrumentation.PathMetrics `json:"metrics,omitempty"`
	}

	result := make([]PeerInfo, 0, len(connectedPeers))
	for _, p := range connectedPeers {
		info := PeerInfo{PeerID: p}
		if m, ok := s.node.GetPathMetrics(p); ok {
			info.IsDirect = m.IsDirect
			info.Metrics = &m
		}
		result = append(result, info)
	}

	writeJSON(w, http.StatusOK, map[string]any{"peers": result})
}

func (s *ServerState) handlePeerConnect(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		PeerID string `json:"peer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PeerID == "" {
		http.Error(w, "peer_id required", http.StatusBadRequest)
		return
	}

	start := time.Now()
	nowNano := fmt.Sprintf("%d", time.Now().UnixNano())
	err := s.node.SendStream(body.PeerID, []byte("__PING__:"+nowNano))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ping_sent",
		"peer_id":     body.PeerID,
		"duration_ms": time.Since(start).Milliseconds(),
	})
}

func (s *ServerState) handlePeerStream(w http.ResponseWriter, r *http.Request) {
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

	resp, err := s.node.ExecuteGPUTask(ctx, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "resp": resp})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *ServerState) handleCodeRun(w http.ResponseWriter, r *http.Request) {
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(body.Timeout+8)*time.Second)
	defer cancel()

	// If local node or local worker is selected:
	if body.PeerID == "local" || body.PeerID == s.nodeConfig.NodeID || body.PeerID == "" {
		if !s.node.HasCodeExecutor() {
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
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "resp": resp})
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Remote peer execution over WebRTC DataChannel
	resp, err := s.node.ExecuteRemoteCode(ctx, body.PeerID, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "resp": resp})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *ServerState) handleShellExec(w http.ResponseWriter, r *http.Request) {
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

	if body.PeerID == "local" || body.PeerID == s.nodeConfig.NodeID || body.PeerID == "" {
		executor := remoteshell.NewSystemExecutor()
		res := executor.Execute(ctx, remoteshell.ShellRequest{
			Command:   body.Command,
			ShellType: remoteshell.ShellAuto,
		})
		writeJSON(w, http.StatusOK, res)
		return
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
	usage := s.node.StorageUsage()
	writeJSON(w, http.StatusOK, map[string]any{"blobs": blobs, "usage": usage})
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
		"blob_id":  blobID,
		"content":  content,
		"size":     n,
		"verified": isValid,
	})
}

func (s *ServerState) handleStoragePut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}

	// Compute hash as blobID
	hasher := uuid.New().String()[:12]
	blobID := "blob-" + hasher
	size, err := s.node.PutBlob(blobID, strings.NewReader(body.Content))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	s.Broadcast("storage_put", "", fmt.Sprintf("Stored blob %s (%d bytes)", blobID, size), map[string]any{
		"blob_id": blobID,
		"size":    size,
	})
	writeJSON(w, http.StatusOK, map[string]any{"blob_id": blobID, "size": size})
}

func (s *ServerState) handleStorageDel(w http.ResponseWriter, r *http.Request) {
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
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	s.wsMu.Lock()
	s.wsClients[conn] = true
	// Send recent backlog
	for _, evt := range s.eventLog {
		_ = conn.WriteJSON(evt)
	}
	s.wsMu.Unlock()

	defer func() {
		s.wsMu.Lock()
		delete(s.wsClients, conn)
		s.wsMu.Unlock()
	}()

	// Read pump to detect close
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}
