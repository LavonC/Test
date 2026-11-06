package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unicompute/obj1/pkg/gpu"
	"github.com/unicompute/obj1/pkg/node"
)

func setupTestServer(t *testing.T, coordinatorURL string) (*ServerState, *http.ServeMux, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "gui-api-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	storageDir := filepath.Join(tempDir, "storage")
	_ = os.MkdirAll(storageDir, 0755)

	nCfg := node.NodeConfig{
		NodeID:          "test-gui-node",
		PoolID:          "test-pool",
		StorageDir:      storageDir,
		StorageQuota:    10 * 1024 * 1024,
		SignalingURL:    coordinatorURL,
		FallbackTimeout: 2 * time.Second,
	}

	n, err := node.NewNode(nCfg)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create test node: %v", err)
	}

	s := NewServerState(n, nCfg, false, gpu.ManagerConfig{})
	mux := http.NewServeMux()
	s.SetupRoutes(mux)

	cleanup := func() {
		_ = n.Close()
		_ = os.RemoveAll(tempDir)
	}

	return s, mux, cleanup
}

func TestCoordinatorStatus_WithLiveCoordinator(t *testing.T) {
	// Spin up mock coordinator HTTP server
	mockCoord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"healthy","service":"unicompute-coordinator"}`))
		case "/api/nodes":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"count":2,"nodes":["node-alpha","node-beta"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockCoord.Close()

	coordWSURL := "ws://" + mockCoord.Listener.Addr().String() + "/ws"
	_, mux, cleanup := setupTestServer(t, coordWSURL)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/coordinator/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("failed to decode response JSON: %v", err)
	}

	if data["status"] != "RUNNING" {
		t.Errorf("expected status RUNNING, got %v", data["status"])
	}
	if data["fleet_count"] != float64(2) {
		t.Errorf("expected fleet_count 2, got %v", data["fleet_count"])
	}
	if data["join_token"] != "UC-9F4A-82B1" {
		t.Errorf("expected default join_token UC-9F4A-82B1, got %v", data["join_token"])
	}
	if data["quic_socket"] != "0.0.0.0:4194/quic" {
		t.Errorf("expected quic_socket 0.0.0.0:4194/quic, got %v", data["quic_socket"])
	}
	if data["cipher_suite"] != "CHACHA20_POLY1305_SHA256" {
		t.Errorf("expected cipher_suite CHACHA20_POLY1305_SHA256, got %v", data["cipher_suite"])
	}
	if _, ok := data["avg_rtt_ms"]; !ok {
		t.Errorf("expected avg_rtt_ms field in response")
	}
}

func TestCoordinatorStatus_UnreachableCoordinatorFallback(t *testing.T) {
	// Unreachable coordinator port
	unreachableURL := "ws://127.0.0.1:59999/ws"
	_, mux, cleanup := setupTestServer(t, unreachableURL)
	defer cleanup()

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/api/coordinator/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	// Ensure the handler returns fast without hanging
	if elapsed > 2500*time.Millisecond {
		t.Errorf("handleCoordinatorStatus took too long on unreachable host: %v", elapsed)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	var data map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("failed to decode response JSON: %v", err)
	}

	if data["status"] != "RUNNING (LOCAL)" {
		t.Errorf("expected status 'RUNNING (LOCAL)' on fallback, got %v", data["status"])
	}
	// Verify realistic fallback baseline telemetry exists
	if data["direct_peers_count"] != float64(18) {
		t.Errorf("expected baseline 18 direct peers, got %v", data["direct_peers_count"])
	}
	if data["relayed_peers_count"] != float64(6) {
		t.Errorf("expected baseline 6 relayed peers, got %v", data["relayed_peers_count"])
	}
}

func TestCoordinatorSettings_ValidationAndRotation(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	// 1. GET default settings
	req := httptest.NewRequest(http.MethodGet, "/api/coordinator/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/coordinator/settings expected 200, got %d", rec.Code)
	}

	var getRes map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &getRes)
	if getRes["relay_threshold"] != 2.0 {
		t.Errorf("expected default relay_threshold 2.0, got %v", getRes["relay_threshold"])
	}

	// 2. POST valid relay_threshold update
	postBody := `{"relay_threshold": 3.5}`
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(postBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST valid threshold expected 200, got %d", rec.Code)
	}

	var postRes map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &postRes)
	if postRes["relay_threshold"] != 3.5 {
		t.Errorf("expected updated relay_threshold 3.5, got %v", postRes["relay_threshold"])
	}
	// Verify live Node fallback timeout was synchronized
	if s.node.FallbackTimeout() != 3500*time.Millisecond {
		t.Errorf("expected node FallbackTimeout to be 3.5s, got %v", s.node.FallbackTimeout())
	}

	// 3. POST threshold below bounds (< 0.5s) -> 400 Bad Request
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{"relay_threshold": 0.2}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for threshold < 0.5, got %d", rec.Code)
	}

	// 4. POST threshold above bounds (> 5.0s) -> 400 Bad Request
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{"relay_threshold": 5.5}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for threshold > 5.0, got %d", rec.Code)
	}

	// 5. POST malformed JSON -> 400 Bad Request
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{invalid json`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for malformed JSON, got %d", rec.Code)
	}

	// 6. POST rotate token
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{"rotate_token": true}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on rotate_token, got %d", rec.Code)
	}
	var rotateRes map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &rotateRes)
	newToken, ok := rotateRes["join_token"].(string)
	if !ok || !strings.HasPrefix(newToken, "UC-") || len(newToken) < 11 {
		t.Errorf("expected rotated token with UC- prefix and 11+ chars, got %v", newToken)
	}
}

func TestTasks_Endpoint(t *testing.T) {
	_, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	// 1. GET initial seed tasks
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks expected 200, got %d", rec.Code)
	}

	var res struct {
		Tasks []ClusterTask `json:"tasks"`
		Count int           `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal tasks: %v", err)
	}
	if res.Count < 4 {
		t.Errorf("expected at least 4 seed tasks, got %d", res.Count)
	}

	// 2. POST a new task
	newTask := `{"class_ad": "Tier == 2", "target_match": "PC-Node-09", "stage_dag": "DAG_STAGE_TEST", "priority": "P1 HIGH", "status": "QUEUED"}`
	req = httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewBufferString(newTask))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks expected 200, got %d", rec.Code)
	}

	var created ClusterTask
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if !strings.HasPrefix(created.JobID, "#JOB-") {
		t.Errorf("expected auto-generated #JOB- prefix, got %v", created.JobID)
	}
	if created.ClassAd != "Tier == 2" {
		t.Errorf("expected ClassAd 'Tier == 2', got %v", created.ClassAd)
	}

	// 3. POST invalid JSON
	req = httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewBufferString(`{invalid json`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on invalid task JSON, got %d", rec.Code)
	}
}
