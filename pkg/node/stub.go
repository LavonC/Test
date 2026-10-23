package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/unicompute/obj1/pkg/blobstore"
	"github.com/unicompute/obj1/pkg/instrumentation"
	"github.com/unicompute/obj1/pkg/nat"
)

// Substrate defines the interface that Objectives 2, 3, 4, and 5 consume from Objective 1.
type Substrate interface {
	PutBlob(id string, r io.Reader) (int64, error)
	GetBlob(id string) (io.ReadCloser, error)
	HasBlob(id string) bool
	DeleteBlob(id string) error
	StatBlob(id string) (blobstore.BlobInfo, error)
	ListBlobs() ([]blobstore.BlobInfo, error)
	VerifyBlob(id string) (bool, error)
	Scrub() ([]string, error)
	StorageUsage() blobstore.StorageUsage

	TransferChunk(ctx context.Context, peerID string, chunkID string) error
	FetchChunk(ctx context.Context, peerID string, chunkID string, expectedSize int64) error
	SendStream(peerID string, data []byte) error

	GetPathMetrics(peerID string) (instrumentation.PathMetrics, bool)
	FleetStats() instrumentation.FleetNATStats
	NATReport() *nat.NATReport

	GetConnectedPeers() []string
	GetFleetNodes(ctx context.Context) ([]string, error)

	Close() error
}

var _ Substrate = (*StubNode)(nil)

// StubNode provides an in-memory stub implementation of Objective 1 for Month 1
// integration by owners of Objectives 2 (Repair), 3 (Chunking), 4 (Encryption), and 5 (Scheduler).
type StubNode struct {
	mu         sync.RWMutex
	NodeID     string
	Storage    map[string][]byte
	QuotaBytes int64
	UsedBytes  int64
	Peers      map[string]*StubNode
	StreamMsgs map[string][][]byte
	NATInfo    *nat.NATReport
	Metrics    map[string]instrumentation.PathMetrics
}

// NewStubNode creates an in-memory mock node for rapid testing.
func NewStubNode(nodeID string, quotaBytes int64) *StubNode {
	if quotaBytes <= 0 {
		quotaBytes = 10 * 1024 * 1024 * 1024
	}
	return &StubNode{
		NodeID:     nodeID,
		Storage:    make(map[string][]byte),
		QuotaBytes: quotaBytes,
		Peers:      make(map[string]*StubNode),
		StreamMsgs: make(map[string][][]byte),
		Metrics:    make(map[string]instrumentation.PathMetrics),
		NATInfo: &nat.NATReport{
			NATType:           nat.NATTypeOpenDirect,
			IPv4Supported:     true,
			IPv6Supported:     true,
			HolePunchFeasible: true,
			Timestamp:         time.Now(),
		},
	}
}

// ConnectStub links two stub nodes directly in-memory.
func (s *StubNode) ConnectStub(peer *StubNode) {
	s.mu.Lock()
	peer.mu.Lock()
	defer s.mu.Unlock()
	defer peer.mu.Unlock()

	s.Peers[peer.NodeID] = peer
	peer.Peers[s.NodeID] = s

	metric := instrumentation.PathMetrics{
		PeerID:          peer.NodeID,
		IsDirect:        true,
		PairType:        instrumentation.PairHostHost,
		LocalCandidate:  "127.0.0.1:0 (host)",
		RemoteCandidate: "127.0.0.1:0 (host)",
		RTT:             1 * time.Millisecond,
		ConnectedAt:     time.Now(),
	}
	s.Metrics[peer.NodeID] = metric

	metricRev := metric
	metricRev.PeerID = s.NodeID
	peer.Metrics[s.NodeID] = metricRev
}

func (s *StubNode) PutBlob(id string, r io.Reader) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}

	size := int64(len(data))
	if s.UsedBytes+size > s.QuotaBytes {
		return 0, blobstore.ErrQuotaExceeded
	}

	// Verify SHA256 if ID is a hash
	if len(id) == 64 {
		h := sha256.Sum256(data)
		calc := hex.EncodeToString(h[:])
		if calc != id {
			return 0, blobstore.ErrChecksumMismatch
		}
	}

	s.Storage[id] = data
	s.UsedBytes += size
	return size, nil
}

func (s *StubNode) GetBlob(id string) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, ok := s.Storage[id]
	if !ok {
		return nil, blobstore.ErrBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *StubNode) HasBlob(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.Storage[id]
	return ok
}

func (s *StubNode) DeleteBlob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, ok := s.Storage[id]
	if !ok {
		return blobstore.ErrBlobNotFound
	}

	s.UsedBytes -= int64(len(data))
	delete(s.Storage, id)
	return nil
}

func (s *StubNode) StatBlob(id string) (blobstore.BlobInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, ok := s.Storage[id]
	if !ok {
		return blobstore.BlobInfo{}, blobstore.ErrBlobNotFound
	}
	h := sha256.Sum256(data)
	return blobstore.BlobInfo{
		ID:      id,
		Size:    int64(len(data)),
		ModTime: time.Now(),
		SHA256:  hex.EncodeToString(h[:]),
	}, nil
}

func (s *StubNode) ListBlobs() ([]blobstore.BlobInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]blobstore.BlobInfo, 0, len(s.Storage))
	for id, data := range s.Storage {
		h := sha256.Sum256(data)
		list = append(list, blobstore.BlobInfo{
			ID:      id,
			Size:    int64(len(data)),
			ModTime: time.Now(),
			SHA256:  hex.EncodeToString(h[:]),
		})
	}
	return list, nil
}

func (s *StubNode) VerifyBlob(id string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, ok := s.Storage[id]
	if !ok {
		return false, blobstore.ErrBlobNotFound
	}
	if len(id) == 64 {
		h := sha256.Sum256(data)
		calc := hex.EncodeToString(h[:])
		return calc == id, nil
	}
	return true, nil
}

func (s *StubNode) Scrub() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var corruptIDs []string
	for id, data := range s.Storage {
		if len(id) == 64 {
			h := sha256.Sum256(data)
			if hex.EncodeToString(h[:]) != id {
				corruptIDs = append(corruptIDs, id)
			}
		}
	}
	return corruptIDs, nil
}

func (s *StubNode) StorageUsage() blobstore.StorageUsage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return blobstore.StorageUsage{
		QuotaBytes:     s.QuotaBytes,
		AllocatedBytes: s.QuotaBytes,
		UsedBytes:      s.UsedBytes,
		FreeBytes:      s.QuotaBytes - s.UsedBytes,
		BlobCount:      int64(len(s.Storage)),
	}
}

func (s *StubNode) TransferChunk(ctx context.Context, peerID string, chunkID string) error {
	s.mu.RLock()
	peer, ok := s.Peers[peerID]
	data, hasData := s.Storage[chunkID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("peer %s not connected", peerID)
	}
	if !hasData {
		return fmt.Errorf("chunk %s not found", chunkID)
	}

	_, err := peer.PutBlob(chunkID, bytes.NewReader(data))
	return err
}

func (s *StubNode) FetchChunk(ctx context.Context, peerID string, chunkID string, expectedSize int64) error {
	s.mu.RLock()
	peer, ok := s.Peers[peerID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("peer %s not connected", peerID)
	}

	rc, err := peer.GetBlob(chunkID)
	if err != nil {
		return err
	}
	defer rc.Close()

	_, err = s.PutBlob(chunkID, rc)
	return err
}

func (s *StubNode) SendStream(peerID string, data []byte) error {
	s.mu.RLock()
	peer, ok := s.Peers[peerID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("peer %s not connected", peerID)
	}

	peer.mu.Lock()
	peer.StreamMsgs[s.NodeID] = append(peer.StreamMsgs[s.NodeID], data)
	peer.mu.Unlock()
	return nil
}

func (s *StubNode) GetPathMetrics(peerID string) (instrumentation.PathMetrics, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.Metrics[peerID]
	return m, ok
}

func (s *StubNode) FleetStats() instrumentation.FleetNATStats {
	return instrumentation.FleetNATStats{
		TotalNodes:          len(s.Peers) + 1,
		DirectConnections:   len(s.Peers),
		DirectConnectionPct: 100.0,
	}
}

func (s *StubNode) NATReport() *nat.NATReport {
	return s.NATInfo
}

func (s *StubNode) GetConnectedPeers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	peers := make([]string, 0, len(s.Peers))
	for p := range s.Peers {
		peers = append(peers, p)
	}
	return peers
}

func (s *StubNode) GetFleetNodes(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes := make([]string, 0, len(s.Peers)+1)
	nodes = append(nodes, s.NodeID)
	for p := range s.Peers {
		nodes = append(nodes, p)
	}
	return nodes, nil
}

func (s *StubNode) Close() error {
	return nil
}
