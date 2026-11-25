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
	s.SetActivePort(8080)
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
	s, mux, cleanup := setupTestServer(t, coordWSURL)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/coordinator/status", nil)
	req.Header.Set("Authorization", "Bearer "+s.SessionToken())
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
	if data["transport"] != "WebRTC DataChannel (ICE/DTLS/SCTP)" {
		t.Errorf("expected transport 'WebRTC DataChannel (ICE/DTLS/SCTP)', got %v", data["transport"])
	}
	if _, ok := data["blob_count"]; !ok {
		t.Errorf("expected blob_count field in response")
	}
	if _, ok := data["storage_bytes"]; !ok {
		t.Errorf("expected storage_bytes field in response")
	}
	if _, ok := data["avg_rtt_ms"]; !ok {
		t.Errorf("expected avg_rtt_ms field in response")
	}
}

func TestCoordinatorStatus_AuthTokenHeader(t *testing.T) {
	var receivedAuth string
	mockCoord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"healthy","service":"unicompute-coordinator"}`))
		case "/api/nodes":
			receivedAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"count":1,"nodes":["node-auth-test"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockCoord.Close()

	coordWSURL := "ws://" + mockCoord.Listener.Addr().String() + "/ws"
	s, mux, cleanup := setupTestServer(t, coordWSURL)
	defer cleanup()

	s.nodeConfig.AuthToken = "test-cluster-token-xyz"

	req := httptest.NewRequest(http.MethodGet, "/api/coordinator/status", nil)
	req.Header.Set("Authorization", "Bearer "+s.SessionToken())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if receivedAuth != "Bearer test-cluster-token-xyz" {
		t.Errorf("expected Authorization 'Bearer test-cluster-token-xyz', got '%s'", receivedAuth)
	}
}

func TestCoordinatorStatus_UnreachableCoordinatorFallback(t *testing.T) {
	// Unreachable coordinator port
	unreachableURL := "ws://127.0.0.1:59999/ws"
	s, mux, cleanup := setupTestServer(t, unreachableURL)
	defer cleanup()

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/api/coordinator/status", nil)
	req.Header.Set("Authorization", "Bearer "+s.SessionToken())
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

	if data["status"] != "DISCONNECTED" {
		t.Errorf("expected status 'DISCONNECTED' on unreachable coordinator, got %v", data["status"])
	}
	if data["fleet_count"] != float64(0) {
		t.Errorf("expected fleet_count 0, got %v", data["fleet_count"])
	}
	if data["direct_peers_count"] != float64(0) {
		t.Errorf("expected 0 direct peers, got %v", data["direct_peers_count"])
	}
	if data["relayed_peers_count"] != float64(0) {
		t.Errorf("expected 0 relayed peers, got %v", data["relayed_peers_count"])
	}
	if data["avg_rtt_ms"] != float64(0) {
		t.Errorf("expected 0 avg_rtt_ms, got %v", data["avg_rtt_ms"])
	}
}

func TestCoordinatorSettings_ValidationAndRotation(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()

	// 1. GET default settings
	req := httptest.NewRequest(http.MethodGet, "/api/coordinator/settings", nil)
	req.Header.Set("Authorization", "Bearer "+token)
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
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST valid threshold expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var postRes map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &postRes)
	if postRes["relay_threshold"] != 3.5 {
		t.Errorf("expected updated relay_threshold 3.5, got %v", postRes["relay_threshold"])
	}
	if s.node.FallbackTimeout() != 3500*time.Millisecond {
		t.Errorf("expected node FallbackTimeout to be 3.5s, got %v", s.node.FallbackTimeout())
	}

	// 3. POST threshold below bounds (< 0.5s) -> 400 Bad Request
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{"relay_threshold": 0.2}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for threshold < 0.5, got %d", rec.Code)
	}

	// 4. POST threshold above bounds (> 5.0s) -> 400 Bad Request
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{"relay_threshold": 5.5}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for threshold > 5.0, got %d", rec.Code)
	}

	// 5. POST malformed JSON -> 400 Bad Request
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{invalid json`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for malformed JSON, got %d", rec.Code)
	}

	// 6. POST rotate token
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{"rotate_token": true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
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
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()

	// 1. GET initial tasks: count must be 0 (no fake seed tasks)
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
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
	if res.Count != 0 {
		t.Errorf("expected initial task count 0, got %d", res.Count)
	}
	if len(res.Tasks) != 0 {
		t.Errorf("expected empty tasks slice, got %d tasks", len(res.Tasks))
	}

	// 2. POST a new task
	newTask := `{"class_ad": "Tier == 2", "target_match": "PC-Node-09", "stage_dag": "DAG_STAGE_TEST", "priority": "P1 HIGH", "status": "QUEUED"}`
	req = httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewBufferString(newTask))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var created ClusterTask
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if !strings.HasPrefix(created.JobID, "#JOB-") {
		t.Errorf("expected auto-generated #JOB- prefix, got %v", created.JobID)
	}
	if created.ClassAd != "Tier == 2" {
		t.Errorf("expected ClassAd 'Tier == 2', got %v", created.ClassAd)
	}

	// 3. GET tasks again, count must now be 1
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks expected 200, got %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal tasks: %v", err)
	}
	if res.Count != 1 {
		t.Errorf("expected task count 1 after creation, got %d", res.Count)
	}

	// 4. POST invalid JSON
	req = httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewBufferString(`{invalid json`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on invalid task JSON, got %d", rec.Code)
	}
}

func TestAPI_SecurityAuthAndOrigin(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()

	// 1. Missing Authorization header returns 401 Unauthorized
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for missing token, got %d", rec.Code)
	}

	// 2. Invalid Bearer token returns 401 Unauthorized
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid token, got %d", rec.Code)
	}

	// 3. POST with Content-Type: text/plain returns 415 Unsupported Media Type
	req = httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewBufferString(`{"relay_threshold": 3.0}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415 Unsupported Media Type for text/plain, got %d", rec.Code)
	}

	// 4. Request with forbidden external Origin returns 403 Forbidden
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "http://evil.com")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for external origin, got %d", rec.Code)
	}

	// 5. Request with matching loopback Origin succeeds
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for valid origin, got %d", rec.Code)
	}

	// 6. Unauthenticated POST to /api/shell/exec returns 401 Unauthorized (GUI RCE blocked)
	req = httptest.NewRequest(http.MethodPost, "/api/shell/exec", bytes.NewBufferString(`{"command":"whoami"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized on unauthenticated /api/shell/exec, got %d", rec.Code)
	}

	// 7. Mutating POST to /api/shell/exec with Content-Type: text/plain returns 415 Unsupported Media Type
	req = httptest.NewRequest(http.MethodPost, "/api/shell/exec", bytes.NewBufferString(`{"command":"whoami"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415 Unsupported Media Type on /api/shell/exec with text/plain, got %d", rec.Code)
	}
}

func TestStorageCAS_PutAndGet(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()
	testPayload := "hello unicompute storage verification cas content"

	// 1. POST /api/storage/put
	putBody, _ := json.Marshal(map[string]string{"content": testPayload})
	req := httptest.NewRequest(http.MethodPost, "/api/storage/put", bytes.NewReader(putBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/storage/put expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var putRes map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &putRes); err != nil {
		t.Fatalf("failed to decode put response: %v", err)
	}

	blobID, ok := putRes["blob_id"].(string)
	if !ok || len(blobID) != 64 {
		t.Fatalf("expected 64-char hex SHA-256 blob_id, got %v", putRes["blob_id"])
	}

	// 2. GET /api/storage/get?id=<blobID>
	req = httptest.NewRequest(http.MethodGet, "/api/storage/get?id="+blobID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/storage/get expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var getRes map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &getRes); err != nil {
		t.Fatalf("failed to decode get response: %v", err)
	}

	if getRes["blob_id"] != blobID {
		t.Errorf("expected blob_id %s, got %v", blobID, getRes["blob_id"])
	}
	if getRes["content"] != testPayload {
		t.Errorf("expected content %q, got %q", testPayload, getRes["content"])
	}
	if getRes["verified"] != true {
		t.Errorf("expected verified == true, got %v", getRes["verified"])
	}
	if getRes["valid"] != true {
		t.Errorf("expected valid == true, got %v", getRes["valid"])
	}
}

func TestAPI_LocalShellBlockedByDefault(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()
	reqBody := `{"peer_id":"local","command":"echo hi"}`

	req := httptest.NewRequest(http.MethodPost, "/api/shell/exec", bytes.NewBufferString(reqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when EnableRemoteShell is false, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	expectedErr := "local/remote shell execution is disabled (start with -enable-remote-shell to opt in)"
	if res["error"] != expectedErr {
		t.Errorf("expected error %q, got %v", expectedErr, res["error"])
	}
}

func TestAPI_OriginAllowRemoteGUI(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()

	// 1. allowRemoteGUI == false: non-loopback Origin matching r.Host is rejected with 403 Forbidden
	s.SetAllowRemoteGUI(false)

	req1 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req1.Host = "192.168.1.50:23080"
	req1.Header.Set("Origin", "http://192.168.1.50:23080")
	req1.Header.Set("Authorization", "Bearer "+token)
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when allowRemoteGUI == false for non-loopback Origin, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// 2. allowRemoteGUI == true: non-loopback Origin matching r.Host is accepted (200 OK)
	s.SetAllowRemoteGUI(true)

	req2 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req2.Host = "192.168.1.50:23080"
	req2.Header.Set("Origin", "http://192.168.1.50:23080")
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK when allowRemoteGUI == true for same-origin non-loopback Origin, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestAPI_CoordinatorAuthToken_NeverEchoed(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()
	secretAuthToken := "super-secret-cluster-auth-key-999"

	// 1. POST /api/coordinator/settings with auth_token
	postBody, _ := json.Marshal(map[string]any{
		"auth_token": secretAuthToken,
	})
	reqPost := httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewReader(postBody))
	reqPost.Header.Set("Authorization", "Bearer "+token)
	reqPost.Header.Set("Content-Type", "application/json")
	recPost := httptest.NewRecorder()
	mux.ServeHTTP(recPost, reqPost)

	if recPost.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on POST /api/coordinator/settings, got %d: %s", recPost.Code, recPost.Body.String())
	}

	postRespStr := recPost.Body.String()
	if strings.Contains(postRespStr, secretAuthToken) {
		t.Fatalf("security violation: raw auth_token was echoed in POST response: %s", postRespStr)
	}

	var postData map[string]any
	if err := json.Unmarshal(recPost.Body.Bytes(), &postData); err != nil {
		t.Fatalf("failed to decode POST response: %v", err)
	}
	if postData["auth_token_set"] != true {
		t.Errorf("expected auth_token_set: true in POST response, got %v", postData["auth_token_set"])
	}

	// 2. GET /api/coordinator/settings: verify auth_token_set: true and no raw token
	reqGet := httptest.NewRequest(http.MethodGet, "/api/coordinator/settings", nil)
	reqGet.Header.Set("Authorization", "Bearer "+token)
	recGet := httptest.NewRecorder()
	mux.ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on GET /api/coordinator/settings, got %d: %s", recGet.Code, recGet.Body.String())
	}

	getRespStr := recGet.Body.String()
	if strings.Contains(getRespStr, secretAuthToken) {
		t.Fatalf("security violation: raw auth_token was echoed in GET response: %s", getRespStr)
	}

	var getData map[string]any
	if err := json.Unmarshal(recGet.Body.Bytes(), &getData); err != nil {
		t.Fatalf("failed to decode GET response: %v", err)
	}
	if getData["auth_token_set"] != true {
		t.Errorf("expected auth_token_set: true in GET response, got %v", getData["auth_token_set"])
	}

	// 3. Clear token
	clearBody, _ := json.Marshal(map[string]any{
		"clear_auth_token": true,
	})
	reqClear := httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewReader(clearBody))
	reqClear.Header.Set("Authorization", "Bearer "+token)
	reqClear.Header.Set("Content-Type", "application/json")
	recClear := httptest.NewRecorder()
	mux.ServeHTTP(recClear, reqClear)

	if recClear.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on clear token, got %d: %s", recClear.Code, recClear.Body.String())
	}

	var clearData map[string]any
	if err := json.Unmarshal(recClear.Body.Bytes(), &clearData); err != nil {
		t.Fatalf("failed to decode clear response: %v", err)
	}
	if clearData["auth_token_set"] != false {
		t.Errorf("expected auth_token_set: false after clear, got %v", clearData["auth_token_set"])
	}
}

func TestAPI_WorkerAllowlist_GetAndSet(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()

	// 1. Unauthenticated request must return 401
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/worker/settings", nil)
	unauthRec := httptest.NewRecorder()
	mux.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized without Bearer token, got %d", unauthRec.Code)
	}

	// 2. Initial GET returns empty allowlist and wildcard_allowed: false
	getReq := httptest.NewRequest(http.MethodGet, "/api/worker/settings", nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var initData struct {
		CodeAllowlist   []string `json:"code_allowlist"`
		DockerActive    bool     `json:"docker_active"`
		WildcardAllowed bool     `json:"wildcard_allowed"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &initData); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if len(initData.CodeAllowlist) != 0 {
		t.Errorf("expected empty initial allowlist, got %v", initData.CodeAllowlist)
	}
	if initData.WildcardAllowed {
		t.Errorf("expected wildcard_allowed: false initially")
	}

	// 3. Valid POST /api/worker/settings
	validBody, _ := json.Marshal(map[string]any{
		"code_allowlist": []string{"peer-b", "peer-a"},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/api/worker/settings", bytes.NewReader(validBody))
	postReq.Header.Set("Authorization", "Bearer "+token)
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	mux.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on POST, got %d: %s", postRec.Code, postRec.Body.String())
	}

	// Verify node.CodeAllowlist() is updated and sorted
	allowed := s.node.CodeAllowlist()
	if len(allowed) != 2 || allowed[0] != "peer-a" || allowed[1] != "peer-b" {
		t.Errorf("expected sorted [peer-a, peer-b], got %v", allowed)
	}

	// 4. Wildcard POST
	wildBody, _ := json.Marshal(map[string]any{
		"code_allowlist": []string{"*"},
	})
	wildReq := httptest.NewRequest(http.MethodPost, "/api/worker/settings", bytes.NewReader(wildBody))
	wildReq.Header.Set("Authorization", "Bearer "+token)
	wildReq.Header.Set("Content-Type", "application/json")
	wildRec := httptest.NewRecorder()
	mux.ServeHTTP(wildRec, wildReq)
	if wildRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on wildcard POST, got %d: %s", wildRec.Code, wildRec.Body.String())
	}
	var wildData struct {
		CodeAllowlist   []string `json:"code_allowlist"`
		WildcardAllowed bool     `json:"wildcard_allowed"`
	}
	if err := json.Unmarshal(wildRec.Body.Bytes(), &wildData); err != nil {
		t.Fatalf("failed to decode wildcard response: %v", err)
	}
	if !wildData.WildcardAllowed {
		t.Errorf("expected wildcard_allowed: true")
	}

	// 5. Rejects invalid payload (non-array, nil)
	badReq := httptest.NewRequest(http.MethodPost, "/api/worker/settings", strings.NewReader(`{"code_allowlist": "not-an-array"}`))
	badReq.Header.Set("Authorization", "Bearer "+token)
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	mux.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on invalid payload, got %d", badRec.Code)
	}

	// 6. Rejects oversized entry (> 128 chars)
	longEntry := strings.Repeat("x", 129)
	oversizedBody, _ := json.Marshal(map[string]any{
		"code_allowlist": []string{longEntry},
	})
	overReq := httptest.NewRequest(http.MethodPost, "/api/worker/settings", bytes.NewReader(oversizedBody))
	overReq.Header.Set("Authorization", "Bearer "+token)
	overReq.Header.Set("Content-Type", "application/json")
	overRec := httptest.NewRecorder()
	mux.ServeHTTP(overRec, overReq)
	if overRec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on oversized entry, got %d", overRec.Code)
	}

	// 7. Rejects oversized list (> 100 entries)
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "peer"
	}
	tooManyBody, _ := json.Marshal(map[string]any{
		"code_allowlist": tooMany,
	})
	tooManyReq := httptest.NewRequest(http.MethodPost, "/api/worker/settings", bytes.NewReader(tooManyBody))
	tooManyReq.Header.Set("Authorization", "Bearer "+token)
	tooManyReq.Header.Set("Content-Type", "application/json")
	tooManyRec := httptest.NewRecorder()
	mux.ServeHTTP(tooManyRec, tooManyReq)
	if tooManyRec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on >100 entries, got %d", tooManyRec.Code)
	}
}

func TestCoordinatorStatus_401AuthRequired(t *testing.T) {
	// Mock coordinator where /health returns 200 OK, but /api/nodes returns 401 Unauthorized
	mockCoord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"healthy","service":"unicompute-coordinator"}`))
		case "/api/nodes":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized: cluster auth token required"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockCoord.Close()

	coordWSURL := "ws://" + mockCoord.Listener.Addr().String() + "/ws"
	s, mux, cleanup := setupTestServer(t, coordWSURL)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/coordinator/status", nil)
	req.Header.Set("Authorization", "Bearer "+s.SessionToken())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("failed to decode response JSON: %v", err)
	}

	if data["status"] != "AUTH_REQUIRED" {
		t.Errorf("expected status 'AUTH_REQUIRED' on 401, got %v", data["status"])
	}
	if data["auth_required"] != true {
		t.Errorf("expected auth_required: true on 401, got %v", data["auth_required"])
	}
}

func TestAPI_CoordinatorConnect_WithAuthToken(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:8080/ws")
	defer cleanup()

	token := s.SessionToken()
	secret := "connect-secret-token-777"

	postBody, _ := json.Marshal(map[string]any{
		"url":        "ws://127.0.0.1:9090/ws",
		"auth_token": secret,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/coordinator/connect", bytes.NewReader(postBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	respStr := rec.Body.String()
	if strings.Contains(respStr, secret) {
		t.Fatalf("security violation: raw auth_token echoed in /api/coordinator/connect response: %s", respStr)
	}

	var respData map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &respData); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if respData["auth_token_set"] != true {
		t.Errorf("expected auth_token_set: true, got %v", respData["auth_token_set"])
	}
	if !s.node.HasAuthToken() {
		t.Errorf("expected node.HasAuthToken() == true after connect")
	}
	if errMsg, ok := respData["reconnect_error"].(string); !ok || errMsg == "" {
		t.Errorf("expected non-empty reconnect_error when coordinator is unreachable, got %v", respData["reconnect_error"])
	}
}

func TestAPI_CoordinatorSettings_ReconnectErrorReturnedAndTokenNeverEchoed(t *testing.T) {
	s, mux, cleanup := setupTestServer(t, "ws://127.0.0.1:59998/ws")
	defer cleanup()

	token := s.SessionToken()
	secret := "settings-reconnect-secret-42"

	postBody, _ := json.Marshal(map[string]any{
		"auth_token": secret,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/coordinator/settings", bytes.NewReader(postBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("security violation: raw auth_token echoed in /api/coordinator/settings response")
	}

	var respData map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &respData); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if errMsg, ok := respData["reconnect_error"].(string); !ok || errMsg == "" {
		t.Errorf("expected non-empty reconnect_error when ReconnectSignaling fails, got %v", respData["reconnect_error"])
	}
}



