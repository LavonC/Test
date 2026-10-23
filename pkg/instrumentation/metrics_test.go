package instrumentation

import (
	"testing"
	"time"
)

func TestPathMetrics_TrackingAndAggregation(t *testing.T) {
	tracker := NewTracker()

	// Peer 1: Direct NAT-punched connection
	p1 := &PathMetrics{
		PeerID:          "node-1",
		IsDirect:        true,
		IsRelayed:       false,
		PairType:        PairSrflxSrflx,
		LocalCandidate:  "srflx",
		RemoteCandidate: "srflx",
		RTT:             25 * time.Millisecond,
		ConnectedAt:     time.Now().Add(-10 * time.Second),
		IsBehindCGNAT:   true,
	}
	tracker.RecordConnection(p1)
	tracker.RecordDataTransfer("node-1", 1024*1024, 2048*1024)

	// Peer 2: Relayed connection
	p2 := &PathMetrics{
		PeerID:          "node-2",
		IsDirect:        false,
		IsRelayed:       true,
		PairType:        PairRelay,
		LocalCandidate:  "relay",
		RemoteCandidate: "relay",
		RTT:             80 * time.Millisecond,
		ConnectedAt:     time.Now().Add(-5 * time.Second),
		IsBehindCGNAT:   false,
	}
	tracker.RecordConnection(p2)
	tracker.RecordDataTransfer("node-2", 512*1024, 512*1024)

	// Peer 3: Direct Host-to-Host connection
	p3 := &PathMetrics{
		PeerID:          "node-3",
		IsDirect:        true,
		IsRelayed:       false,
		PairType:        PairHostHost,
		LocalCandidate:  "host",
		RemoteCandidate: "host",
		RTT:             2 * time.Millisecond,
		ConnectedAt:     time.Now().Add(-20 * time.Second),
		IsBehindCGNAT:   false,
	}
	tracker.RecordConnection(p3)

	// Verify single peer metrics
	m1, ok := tracker.GetMetrics("node-1")
	if !ok {
		t.Fatalf("expected metrics for node-1")
	}
	if m1.BytesSent != 1024*1024 || m1.BytesReceived != 2048*1024 {
		t.Errorf("unexpected byte transfer count for node-1: %d / %d", m1.BytesSent, m1.BytesReceived)
	}

	// Verify fleet aggregation
	stats := tracker.AggregateFleetStats()
	if stats.TotalNodes != 3 {
		t.Errorf("expected 3 total nodes, got %d", stats.TotalNodes)
	}
	if stats.NodesBehindCGNAT != 1 {
		t.Errorf("expected 1 node behind CGNAT, got %d", stats.NodesBehindCGNAT)
	}
	if stats.TotalConnections != 3 {
		t.Errorf("expected 3 connections, got %d", stats.TotalConnections)
	}
	if stats.DirectConnections != 2 {
		t.Errorf("expected 2 direct connections, got %d", stats.DirectConnections)
	}
	if stats.RelayedConnections != 1 {
		t.Errorf("expected 1 relayed connection, got %d", stats.RelayedConnections)
	}

	expectedDirectPct := (2.0 / 3.0) * 100.0
	if stats.DirectConnectionPct < expectedDirectPct-0.1 || stats.DirectConnectionPct > expectedDirectPct+0.1 {
		t.Errorf("expected direct connection pct around %.2f, got %.2f", expectedDirectPct, stats.DirectConnectionPct)
	}
}

func TestDetermineCandidatePairType(t *testing.T) {
	tests := []struct {
		local    string
		remote   string
		wantType CandidatePairType
		wantDir  bool
	}{
		{"host", "host", PairHostHost, true},
		{"host", "srflx", PairHostSrflx, true},
		{"srflx", "srflx", PairSrflxSrflx, true},
		{"prflx", "host", PairPrflx, true},
		{"relay", "host", PairRelay, false},
		{"host", "relay", PairRelay, false},
		{"relay", "relay", PairRelay, false},
	}

	for _, tt := range tests {
		gotType, gotDir := DetermineCandidatePairType(tt.local, tt.remote)
		if gotType != tt.wantType || gotDir != tt.wantDir {
			t.Errorf("DetermineCandidatePairType(%q, %q) = (%v, %v); want (%v, %v)",
				tt.local, tt.remote, gotType, gotDir, tt.wantType, tt.wantDir)
		}
	}
}
