package instrumentation

import (
	"sync"
	"time"
)

// CandidatePairType represents ICE candidate connection combinations.
type CandidatePairType string

const (
	PairHostHost   CandidatePairType = "host-host"
	PairHostSrflx  CandidatePairType = "host-srflx"
	PairSrflxSrflx CandidatePairType = "srflx-srflx"
	PairPrflx      CandidatePairType = "prflx"
	PairRelay      CandidatePairType = "relay"
	PairUnknown    CandidatePairType = "unknown"
)

// PathMetrics captures telemetry for a specific peer-to-peer connection.
type PathMetrics struct {
	PeerID            string            `json:"peer_id"`
	IsDirect          bool              `json:"is_direct"`
	IsRelayed         bool              `json:"is_relayed"`
	PairType          CandidatePairType `json:"pair_type"`
	LocalCandidate    string            `json:"local_candidate"`
	RemoteCandidate   string            `json:"remote_candidate"`
	RTT               time.Duration     `json:"rtt"`
	BytesSent         int64             `json:"bytes_sent"`
	BytesReceived     int64             `json:"bytes_received"`
	ConnectedDuration time.Duration     `json:"connected_duration"`
	ConnectedAt       time.Time         `json:"connected_at"`
	IsBehindCGNAT     bool              `json:"is_behind_cgnat"`
}

// FleetNATStats aggregates fleet-wide connection metrics.
// "No measurement of CGNAT prevalence on Indian ISPs exists at all. Adding a 100.64.0.0/10 check
// plus ICE candidate-pair type reporting, and aggregating across the fleet, would make this
// project the source of figures that do not currently exist." (PDF Section 6.3 & S3)
type FleetNATStats struct {
	TotalNodes          int     `json:"total_nodes"`
	NodesBehindCGNAT    int     `json:"nodes_behind_cgnat"`
	CGNATPrevalencePct  float64 `json:"cgnat_prevalence_pct"`
	TotalConnections    int     `json:"total_connections"`
	DirectConnections   int     `json:"direct_connections"`
	RelayedConnections  int     `json:"relayed_connections"`
	DirectConnectionPct float64 `json:"direct_connection_pct"`
}

// Tracker collects and analyzes path metrics for active peer connections.
type Tracker struct {
	mu          sync.RWMutex
	metrics     map[string]*PathMetrics
	knownCGNATs map[string]bool
}

// NewTracker creates a new telemetry tracker.
func NewTracker() *Tracker {
	return &Tracker{
		metrics:     make(map[string]*PathMetrics),
		knownCGNATs: make(map[string]bool),
	}
}

// RecordConnection records or updates the path metrics of a peer connection.
func (t *Tracker) RecordConnection(m *PathMetrics) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.metrics[m.PeerID] = m
	t.knownCGNATs[m.PeerID] = m.IsBehindCGNAT
}

// RecordDataTransfer increments bytes sent and received for a peer.
func (t *Tracker) RecordDataTransfer(peerID string, sent int64, received int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if m, ok := t.metrics[peerID]; ok {
		m.BytesSent += sent
		m.BytesReceived += received
	}
}

// GetMetrics returns telemetry for a single peer.
func (t *Tracker) GetMetrics(peerID string) (PathMetrics, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if m, ok := t.metrics[peerID]; ok {
		copy := *m
		if !copy.ConnectedAt.IsZero() {
			copy.ConnectedDuration = time.Since(copy.ConnectedAt)
		}
		return copy, true
	}
	return PathMetrics{}, false
}

// GetAllMetrics returns telemetry for all active peer connections.
func (t *Tracker) GetAllMetrics() []PathMetrics {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]PathMetrics, 0, len(t.metrics))
	for _, m := range t.metrics {
		copy := *m
		if !copy.ConnectedAt.IsZero() {
			copy.ConnectedDuration = time.Since(copy.ConnectedAt)
		}
		result = append(result, copy)
	}
	return result
}

// AggregateFleetStats calculates fleet-wide direct vs relayed percentages and CGNAT prevalence.
func (t *Tracker) AggregateFleetStats() FleetNATStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stats := FleetNATStats{
		TotalNodes:       len(t.knownCGNATs),
		TotalConnections: len(t.metrics),
	}

	for _, isCGNAT := range t.knownCGNATs {
		if isCGNAT {
			stats.NodesBehindCGNAT++
		}
	}
	if stats.TotalNodes > 0 {
		stats.CGNATPrevalencePct = (float64(stats.NodesBehindCGNAT) / float64(stats.TotalNodes)) * 100.0
	}

	for _, m := range t.metrics {
		if m.IsDirect {
			stats.DirectConnections++
		} else if m.IsRelayed {
			stats.RelayedConnections++
		}
	}
	if stats.TotalConnections > 0 {
		stats.DirectConnectionPct = (float64(stats.DirectConnections) / float64(stats.TotalConnections)) * 100.0
	}

	return stats
}

// DetermineCandidatePairType analyzes local and remote candidate descriptions.
func DetermineCandidatePairType(localType, remoteType string) (CandidatePairType, bool) {
	if localType == "relay" || remoteType == "relay" {
		return PairRelay, false // relayed
	}
	if localType == "host" && remoteType == "host" {
		return PairHostHost, true // direct
	}
	if (localType == "host" && remoteType == "srflx") || (localType == "srflx" && remoteType == "host") {
		return PairHostSrflx, true // direct NAT-punched
	}
	if localType == "srflx" && remoteType == "srflx" {
		return PairSrflxSrflx, true // direct NAT-punched
	}
	if localType == "prflx" || remoteType == "prflx" {
		return PairPrflx, true // direct peer reflexive
	}
	return PairUnknown, true
}
