package transport

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v3"
	"github.com/unicompute/obj1/pkg/instrumentation"
	"github.com/unicompute/obj1/pkg/nat"
	"github.com/unicompute/obj1/pkg/protocol"
)

var (
	ErrConnectionClosed = errors.New("peer connection closed")
	ErrTimeout          = errors.New("connection timeout")
	ErrChannelNotReady  = errors.New("data channel not ready")
)

const (
	MaxInFlightChunksPerPeer        = 8
	MaxInFlightBytesPerPeer  uint64 = 64 * 1024 * 1024 // 64 MiB total across all active inFlight entries per peer
	InFlightChunkTTL                = 30 * time.Second
	MaxConcurrentChunkReqs          = 4
	chunkReqQueueSize               = 64
)

// Config configures the WebRTC transport engine.
type Config struct {
	NodeID        string
	STUNServers   []string
	TURNServers   []TURNServerConfig
	PrivateKey    ed25519.PrivateKey
	IsBehindCGNAT bool
	ChunkTempDir  string
	EnableLogging bool
}

// TURNServerConfig holds WebRTC TURN server details.
type TURNServerConfig struct {
	URL        string
	Username   string
	Credential string
}

// PeerConnection wraps Pion WebRTC connection with dual-channel and path instrumentation.
type PeerConnection struct {
	mu           sync.RWMutex
	SessionID    string
	LocalPeerID  string
	RemotePeerID string
	PC           *webrtc.PeerConnection

	StreamDC *webrtc.DataChannel
	ChunkDC  *webrtc.DataChannel

	streamReady chan struct{}
	chunkReady  chan struct{}
	connectedCh chan struct{}
	connected   bool
	closed      bool

	localCGNAT   bool
	remoteCGNAT  bool
	chunkTempDir string

	// Glare superseding support
	supersededMu sync.Mutex
	supersededBy *PeerConnection
	supersededCh chan struct{}
	isLocalOffer bool

	// ICE restart debouncing
	restartMu  sync.Mutex
	restarting bool

	// Event callbacks
	OnChunkReceived     func(chunkID string, data []byte)
	OnChunkFileReceived func(chunkID string, filePath string, size int64)
	OnStreamMessage     func(msg []byte)
	OnChunkRequest      func(chunkID string)
	OnStateChange       func(state webrtc.PeerConnectionState)

	onInternalStateChange func(state webrtc.PeerConnectionState)

	// Instrumentation
	metrics  *instrumentation.PathMetrics
	tracker  *instrumentation.Tracker
	stopKeep chan struct{}

	// Bounded per-peer chunk request workers (4 concurrent slots)
	chunkReqOnce  sync.Once
	chunkReqQueue chan string

	// In-flight chunk transfers: map[chunkID]*inFlightChunk
	inFlightMu         sync.Mutex
	inFlight           map[string]*inFlightChunk
	totalInFlightBytes uint64

	// Pending chunk ACK/error waiters for outbound SendChunk
	ackMu      sync.Mutex
	ackWaiters map[string]chan error
}

type inFlightChunk struct {
	chunkID      string
	totalSize    uint64
	received     uint64
	tmpFile      *os.File
	tmpPath      string
	doneCh       chan string
	errCh        chan error
	hasWaiter    bool
	createdAt    time.Time
	lastActivity time.Time
}

func newPeerConnection(sessionID, localPeerID, remotePeerID string, pc *webrtc.PeerConnection, tracker *instrumentation.Tracker) *PeerConnection {
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	conn := &PeerConnection{
		SessionID:     sessionID,
		LocalPeerID:   localPeerID,
		RemotePeerID:  remotePeerID,
		PC:            pc,
		streamReady:   make(chan struct{}),
		chunkReady:    make(chan struct{}),
		connectedCh:   make(chan struct{}),
		supersededCh:  make(chan struct{}),
		stopKeep:      make(chan struct{}),
		tracker:       tracker,
		chunkReqQueue: make(chan string, chunkReqQueueSize),
		inFlight:      make(map[string]*inFlightChunk),
		ackWaiters:    make(map[string]chan error),
		metrics: &instrumentation.PathMetrics{
			PeerID:      remotePeerID,
			ConnectedAt: time.Now(),
		},
	}

	return conn
}

func (c *PeerConnection) markSuperseded(winner *PeerConnection) {
	c.supersededMu.Lock()
	if c.supersededBy == nil {
		c.supersededBy = winner
		close(c.supersededCh)
	}
	c.supersededMu.Unlock()
}

func (c *PeerConnection) getSupersededBy() *PeerConnection {
	c.supersededMu.Lock()
	defer c.supersededMu.Unlock()
	return c.supersededBy
}

func (c *PeerConnection) isConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

func (c *PeerConnection) setLocalOffer(v bool) {
	c.mu.Lock()
	c.isLocalOffer = v
	c.mu.Unlock()
}

func (c *PeerConnection) isOutboundOfferPending() bool {
	c.mu.RLock()
	localOffer := c.isLocalOffer
	connected := c.connected
	pc := c.PC
	c.mu.RUnlock()
	if connected {
		return false
	}
	if localOffer {
		return true
	}
	return pc != nil && pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer
}

func (c *PeerConnection) createTempChunkFile() (*os.File, error) {
	if c.chunkTempDir != "" {
		if err := os.MkdirAll(c.chunkTempDir, 0755); err != nil {
			return nil, err
		}
	}
	return os.CreateTemp(c.chunkTempDir, "uc-chunk-*")
}

func (c *PeerConnection) deleteInFlightLocked(chunkID string, removeFile bool) (*inFlightChunk, bool) {
	inflight, ok := c.inFlight[chunkID]
	if !ok {
		return nil, false
	}
	delete(c.inFlight, chunkID)
	if c.totalInFlightBytes >= inflight.totalSize {
		c.totalInFlightBytes -= inflight.totalSize
	} else {
		c.totalInFlightBytes = 0
	}
	if inflight.tmpFile != nil {
		_ = inflight.tmpFile.Close()
		inflight.tmpFile = nil
	}
	if removeFile && inflight.tmpPath != "" {
		_ = os.Remove(inflight.tmpPath)
		inflight.tmpPath = ""
	}
	return inflight, true
}

func (c *PeerConnection) evictStaleInFlightLocked(now time.Time) {
	for id, inflight := range c.inFlight {
		if !inflight.lastActivity.IsZero() && now.Sub(inflight.lastActivity) > InFlightChunkTTL {
			select {
			case inflight.errCh <- ErrTimeout:
			default:
			}
			c.deleteInFlightLocked(id, true)
		}
	}
}

func (c *PeerConnection) evictStaleInFlight(now time.Time) {
	c.inFlightMu.Lock()
	c.evictStaleInFlightLocked(now)
	c.inFlightMu.Unlock()
}

func (c *PeerConnection) cleanupInFlight(err error) {
	c.inFlightMu.Lock()
	for id, inflight := range c.inFlight {
		select {
		case inflight.errCh <- err:
		default:
		}
		c.deleteInFlightLocked(id, true)
	}
	c.inFlightMu.Unlock()

	c.ackMu.Lock()
	for id, ch := range c.ackWaiters {
		select {
		case ch <- err:
		default:
		}
		delete(c.ackWaiters, id)
	}
	c.ackMu.Unlock()
}

func (c *PeerConnection) setupHandlers() {
	c.PC.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		c.mu.Lock()
		if state == webrtc.PeerConnectionStateConnected && !c.connected {
			c.connected = true
			c.inspectCandidatePairLocked()
			close(c.connectedCh)
			go c.startKeepAlive()
			go func() {
				c.updateRTTFromStats()
				time.Sleep(150 * time.Millisecond)
				c.updateRTTFromStats()
			}()
		} else if state == webrtc.PeerConnectionStateConnected && c.connected {
			c.inspectCandidatePairLocked()
			go c.updateRTTFromStats()
		} else if state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed {
			c.closed = true
			select {
			case <-c.stopKeep:
			default:
				close(c.stopKeep)
			}
			go c.cleanupInFlight(ErrConnectionClosed)
		}
		handler := c.OnStateChange
		internalHandler := c.onInternalStateChange
		c.mu.Unlock()

		if internalHandler != nil {
			internalHandler(state)
		}
		if handler != nil {
			handler(state)
		}
	})
}

// SetOnChunkReceived safely sets the chunk received callback.
func (c *PeerConnection) SetOnChunkReceived(fn func(chunkID string, data []byte)) {
	c.mu.Lock()
	c.OnChunkReceived = fn
	c.mu.Unlock()
}

// SetOnChunkFileReceived safely sets the disk-streamed chunk received callback.
func (c *PeerConnection) SetOnChunkFileReceived(fn func(chunkID string, filePath string, size int64)) {
	c.mu.Lock()
	c.OnChunkFileReceived = fn
	c.mu.Unlock()
}

// SetOnChunkRequest safely sets the chunk request callback.
func (c *PeerConnection) SetOnChunkRequest(fn func(chunkID string)) {
	c.mu.Lock()
	c.OnChunkRequest = fn
	c.mu.Unlock()
}

// SetOnStreamMessage safely sets the stream message callback.
func (c *PeerConnection) SetOnStreamMessage(fn func(msg []byte)) {
	c.mu.Lock()
	c.OnStreamMessage = fn
	c.mu.Unlock()
}

// SetOnStateChange safely sets the state change callback.
func (c *PeerConnection) SetOnStateChange(fn func(state webrtc.PeerConnectionState)) {
	c.mu.Lock()
	c.OnStateChange = fn
	c.mu.Unlock()
}

// setOnInternalStateChange safely sets the internal state change callback.
func (c *PeerConnection) setOnInternalStateChange(fn func(state webrtc.PeerConnectionState)) {
	c.mu.Lock()
	c.onInternalStateChange = fn
	c.mu.Unlock()
}

func (c *PeerConnection) setCGNATFlags(localCGNAT, remoteCGNAT bool) {
	c.mu.Lock()
	c.localCGNAT = localCGNAT || c.localCGNAT
	c.remoteCGNAT = remoteCGNAT || c.remoteCGNAT
	if c.localCGNAT || c.remoteCGNAT {
		c.metrics.IsBehindCGNAT = true
		if c.connected && c.tracker != nil {
			cp := *c.metrics
			c.tracker.RecordConnection(&cp)
		}
	}
	c.mu.Unlock()
}

// inspectCandidatePairLocked inspects the selected ICE candidate pair to determine direct vs relayed
// and measures connection parameters. Caller must hold c.mu.
func (c *PeerConnection) inspectCandidatePairLocked() {
	if c.PC == nil {
		return
	}
	sctp := c.PC.SCTP()
	if sctp == nil || sctp.Transport() == nil || sctp.Transport().ICETransport() == nil {
		return
	}

	iceTransport := sctp.Transport().ICETransport()
	pair, err := iceTransport.GetSelectedCandidatePair()
	if err == nil && pair != nil {
		localType := pair.Local.Typ.String()
		remoteType := pair.Remote.Typ.String()

		pairType, isDirect := instrumentation.DetermineCandidatePairType(localType, remoteType)
		c.metrics.IsDirect = isDirect
		c.metrics.IsRelayed = !isDirect
		c.metrics.PairType = pairType
		c.metrics.LocalCandidate = fmt.Sprintf("%s:%d (%s)", pair.Local.Address, pair.Local.Port, pair.Local.Typ)
		c.metrics.RemoteCandidate = fmt.Sprintf("%s:%d (%s)", pair.Remote.Address, pair.Remote.Port, pair.Remote.Typ)
		c.metrics.IsBehindCGNAT = c.localCGNAT || c.remoteCGNAT ||
			nat.IsCGNAT(net.ParseIP(pair.Local.Address)) ||
			nat.IsCGNAT(net.ParseIP(pair.Remote.Address))

		if c.tracker != nil {
			cp := *c.metrics
			c.tracker.RecordConnection(&cp)
		}
	}
}

func (c *PeerConnection) updateRTTFromStats() {
	c.mu.RLock()
	pc := c.PC
	tracker := c.tracker
	closed := c.closed
	c.mu.RUnlock()

	if pc == nil || tracker == nil || closed {
		return
	}

	stats := pc.GetStats()
	var rttSeconds float64
	found := false
	hasSubTickResponse := false
	for _, s := range stats {
		pairStats, ok := s.(webrtc.ICECandidatePairStats)
		if !ok {
			continue
		}
		if pairStats.Nominated {
			if pairStats.CurrentRoundTripTime > 0 {
				rttSeconds = pairStats.CurrentRoundTripTime
				found = true
				break
			}
			if pairStats.TotalRoundTripTime > 0 && pairStats.ResponsesReceived > 0 {
				rttSeconds = pairStats.TotalRoundTripTime / float64(pairStats.ResponsesReceived)
				found = true
				break
			}
			if pairStats.ResponsesReceived > 0 {
				hasSubTickResponse = true
			}
		}
		if !found && pairStats.State == webrtc.StatsICECandidatePairStateSucceeded {
			if pairStats.CurrentRoundTripTime > 0 {
				rttSeconds = pairStats.CurrentRoundTripTime
				found = true
			} else if pairStats.TotalRoundTripTime > 0 && pairStats.ResponsesReceived > 0 {
				rttSeconds = pairStats.TotalRoundTripTime / float64(pairStats.ResponsesReceived)
				found = true
			} else if pairStats.ResponsesReceived > 0 {
				hasSubTickResponse = true
			}
		}
	}
	if !found && !hasSubTickResponse {
		return
	}

	rtt := time.Microsecond
	if found {
		rtt = time.Duration(rttSeconds * float64(time.Second))
		if rtt <= 0 {
			rtt = time.Microsecond
		}
	}
	c.mu.Lock()
	c.metrics.RTT = rtt
	c.mu.Unlock()
	tracker.RecordRTT(c.RemotePeerID, rtt)
}

// startKeepAlive sends periodic ping binding frames every 15 seconds to prevent NAT timeouts
// and polls PC.GetStats() every 5 seconds for automatic RTT telemetry.
func (c *PeerConnection) startKeepAlive() {
	keepTicker := time.NewTicker(15 * time.Second) // 15s keepalive as specified in Section 5 & 6.1
	statsTicker := time.NewTicker(5 * time.Second)
	defer keepTicker.Stop()
	defer statsTicker.Stop()

	for {
		select {
		case <-c.stopKeep:
			return
		case <-statsTicker.C:
			c.updateRTTFromStats()
		case <-keepTicker.C:
			c.evictStaleInFlight(time.Now())
			c.mu.RLock()
			dc := c.ChunkDC
			c.mu.RUnlock()
			if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
				pingFrame := []byte{protocol.DataMsgKeepalive}
				_ = dc.Send(pingFrame)
			}
		}
	}
}

// bindDataChannel hooks message handlers and readiness channels for a DataChannel.
func (c *PeerConnection) bindDataChannel(dc *webrtc.DataChannel) {
	label := dc.Label()
	dc.OnOpen(func() {
		if label == "stream" {
			c.mu.Lock()
			c.StreamDC = dc
			c.mu.Unlock()
			select {
			case <-c.streamReady:
			default:
				close(c.streamReady)
			}
		} else if label == "chunk" {
			c.mu.Lock()
			c.ChunkDC = dc
			c.mu.Unlock()
			select {
			case <-c.chunkReady:
			default:
				close(c.chunkReady)
			}
		}
	})

	dc.OnClose(func() {
		c.mu.Lock()
		internalHandler := c.onInternalStateChange
		c.mu.Unlock()
		if internalHandler != nil {
			internalHandler(webrtc.PeerConnectionStateClosed)
		}
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		data := msg.Data
		if len(data) == 0 {
			return
		}

		if c.tracker != nil {
			c.tracker.RecordDataTransfer(c.RemotePeerID, 0, int64(len(data)))
		}

		if label == "stream" {
			c.mu.RLock()
			handler := c.OnStreamMessage
			c.mu.RUnlock()
			if handler != nil {
				handler(data)
			}
			return
		}

		// Chunk channel message handling
		c.handleChunkMessage(data)
	})
}

var ErrSessionSuperseded = errors.New("session superseded by glare tie-breaker")

func (c *PeerConnection) enqueueChunkRequest(reqID string) {
	c.chunkReqOnce.Do(func() {
		for i := 0; i < MaxConcurrentChunkReqs; i++ {
			go c.chunkRequestWorker()
		}
	})
	select {
	case c.chunkReqQueue <- reqID:
	default:
		go func() {
			_ = c.SendChunkError(reqID, "peer chunk request queue full")
		}()
	}
}

func (c *PeerConnection) chunkRequestWorker() {
	for {
		select {
		case <-c.stopKeep:
			return
		case reqID := <-c.chunkReqQueue:
			c.mu.RLock()
			closed := c.closed
			handler := c.OnChunkRequest
			c.mu.RUnlock()
			if !closed && handler != nil {
				handler(reqID)
			}
		}
	}
}

func (c *PeerConnection) handleChunkMessage(data []byte) {
	if len(data) == 0 {
		return
	}

	msgType := data[0]

	switch msgType {
	case protocol.DataMsgKeepalive:
		// Keepalive ping received; no-op
		return

	case protocol.DataMsgChunkResponse:
		if len(data) < protocol.ChunkHeaderFixedSize {
			return
		}

		flags := data[1]
		offset := binary.BigEndian.Uint64(data[2:10])
		totalSize := binary.BigEndian.Uint64(data[10:18])
		idLen := int(data[18])
		if idLen < 1 || idLen > protocol.MaxChunkIDLen || len(data) < protocol.ChunkHeaderFixedSize+idLen {
			return
		}
		chunkID := string(data[protocol.ChunkHeaderFixedSize : protocol.ChunkHeaderFixedSize+idLen])
		payload := data[protocol.ChunkHeaderFixedSize+idLen:]

		if totalSize > MaxInFlightBytesPerPeer {
			return
		}

		now := time.Now()
		c.inFlightMu.Lock()
		c.evictStaleInFlightLocked(now)

		inflight, ok := c.inFlight[chunkID]
		justCreated := false
		if !ok {
			if flags&protocol.FlagStart == 0 {
				c.inFlightMu.Unlock()
				return
			}
			if len(c.inFlight) >= MaxInFlightChunksPerPeer {
				c.inFlightMu.Unlock()
				return
			}
			if totalSize > MaxInFlightBytesPerPeer-c.totalInFlightBytes {
				c.inFlightMu.Unlock()
				return
			}
			tmpFile, err := c.createTempChunkFile()
			if err != nil {
				c.inFlightMu.Unlock()
				return
			}
			inflight = &inFlightChunk{
				chunkID:      chunkID,
				totalSize:    totalSize,
				tmpFile:      tmpFile,
				tmpPath:      tmpFile.Name(),
				doneCh:       make(chan string, 1),
				errCh:        make(chan error, 1),
				createdAt:    now,
				lastActivity: now,
			}
			c.inFlight[chunkID] = inflight
			c.totalInFlightBytes += totalSize
			justCreated = true
		} else if inflight.tmpFile == nil {
			if flags&protocol.FlagStart == 0 {
				c.inFlightMu.Unlock()
				return
			}
			if inflight.totalSize == 0 && totalSize > 0 {
				if totalSize > MaxInFlightBytesPerPeer-c.totalInFlightBytes {
					if inflight.hasWaiter {
						select {
						case inflight.errCh <- fmt.Errorf("in-flight chunk capacity exceeded"):
						default:
						}
						c.deleteInFlightLocked(chunkID, true)
					}
					c.inFlightMu.Unlock()
					return
				}
				inflight.totalSize = totalSize
				c.totalInFlightBytes += totalSize
			} else if totalSize != inflight.totalSize {
				if inflight.hasWaiter {
					select {
					case inflight.errCh <- fmt.Errorf("chunk size mismatch: expected %d, got %d", inflight.totalSize, totalSize):
					default:
					}
					c.deleteInFlightLocked(chunkID, true)
				}
				c.inFlightMu.Unlock()
				return
			}
			tmpFile, err := c.createTempChunkFile()
			if err != nil {
				if inflight.hasWaiter {
					select {
					case inflight.errCh <- err:
					default:
					}
				}
				c.deleteInFlightLocked(chunkID, true)
				c.inFlightMu.Unlock()
				return
			}
			inflight.tmpFile = tmpFile
			inflight.tmpPath = tmpFile.Name()
		} else if totalSize != inflight.totalSize {
			if inflight.hasWaiter {
				select {
				case inflight.errCh <- fmt.Errorf("chunk size mismatch: expected %d, got %d", inflight.totalSize, totalSize):
				default:
				}
				c.deleteInFlightLocked(chunkID, true)
			}
			c.inFlightMu.Unlock()
			return
		}

		payloadLen := uint64(len(payload))
		if offset > inflight.totalSize || payloadLen > inflight.totalSize-offset {
			if justCreated {
				c.deleteInFlightLocked(chunkID, true)
			}
			c.inFlightMu.Unlock()
			return
		}

		// Require sequential, non-overlapping offsets so duplicate frames cannot cause
		// premature completion with zeroed holes.
		if offset != inflight.received {
			if justCreated {
				c.deleteInFlightLocked(chunkID, true)
			}
			c.inFlightMu.Unlock()
			return
		}

		if payloadLen > 0 {
			if _, err := inflight.tmpFile.Write(payload); err != nil {
				c.deleteInFlightLocked(chunkID, true)
				c.inFlightMu.Unlock()
				go func() {
					_ = c.SendChunkError(chunkID, fmt.Sprintf("disk write error: %v", err))
				}()
				return
			}
		}
		inflight.received += payloadLen
		inflight.lastActivity = now

		completed := (inflight.totalSize == 0 && (flags&protocol.FlagEnd != 0)) ||
			(inflight.totalSize > 0 && inflight.received >= inflight.totalSize)

		var donePath string
		var doneSize int64
		var hadWaiter bool
		var doneCh chan string
		if completed {
			hadWaiter = inflight.hasWaiter
			donePath = inflight.tmpPath
			doneSize = int64(inflight.received)
			doneCh = inflight.doneCh
			c.deleteInFlightLocked(chunkID, false)
		}
		c.inFlightMu.Unlock()

		if completed {
			if hadWaiter {
				select {
				case doneCh <- donePath:
				default:
					_ = os.Remove(donePath)
				}
				return
			}

			c.mu.RLock()
			fileHandler := c.OnChunkFileReceived
			legacyHandler := c.OnChunkReceived
			c.mu.RUnlock()

			if fileHandler != nil {
				go fileHandler(chunkID, donePath, doneSize)
			} else if legacyHandler != nil {
				fileBytes, readErr := os.ReadFile(donePath)
				_ = os.Remove(donePath)
				if readErr == nil {
					legacyHandler(chunkID, fileBytes)
					_ = c.SendChunkAck(chunkID)
				} else {
					_ = c.SendChunkError(chunkID, readErr.Error())
				}
			} else {
				_ = os.Remove(donePath)
			}
		}

	case protocol.DataMsgChunkRequest:
		if len(data) >= 2 {
			idLen := int(data[1])
			if idLen > 0 && idLen <= protocol.MaxChunkIDLen && len(data) >= 2+idLen {
				reqID := string(data[2 : 2+idLen])
				c.enqueueChunkRequest(reqID)
			}
		}

	case protocol.DataMsgChunkAck:
		if len(data) >= 2 {
			idLen := int(data[1])
			if idLen > 0 && idLen <= protocol.MaxChunkIDLen && len(data) >= 2+idLen {
				chunkID := string(data[2 : 2+idLen])
				c.ackMu.Lock()
				ch, ok := c.ackWaiters[chunkID]
				if ok {
					delete(c.ackWaiters, chunkID)
				}
				c.ackMu.Unlock()
				if ok && ch != nil {
					select {
					case ch <- nil:
					default:
					}
				}
			}
		}

	case protocol.DataMsgChunkError:
		if len(data) >= 2 {
			idLen := int(data[1])
			if idLen > 0 && idLen <= protocol.MaxChunkIDLen && len(data) >= 2+idLen {
				chunkID := string(data[2 : 2+idLen])
				errMsg := string(data[2+idLen:])
				c.inFlightMu.Lock()
				inflight, ok := c.deleteInFlightLocked(chunkID, true)
				c.inFlightMu.Unlock()
				if ok && inflight != nil {
					select {
					case inflight.errCh <- errors.New(errMsg):
					default:
					}
				}

				c.ackMu.Lock()
				ackCh, hasAck := c.ackWaiters[chunkID]
				if hasAck {
					delete(c.ackWaiters, chunkID)
				}
				c.ackMu.Unlock()
				if hasAck && ackCh != nil {
					select {
					case ackCh <- errors.New(errMsg):
					default:
					}
				}
			}
		}
	}
}

// SendChunk streams chunk bytes in 60 KiB frames over the reliable SCTP DataChannel
// and waits for receiver DataMsgChunkAck (0x13) or DataMsgChunkError (0x14).
func (c *PeerConnection) SendChunk(ctx context.Context, chunkID string, r io.Reader, totalSize int64) error {
	if totalSize < 0 {
		return fmt.Errorf("invalid totalSize: %d", totalSize)
	}
	idBytes := []byte(chunkID)
	if len(idBytes) == 0 || len(idBytes) > protocol.MaxChunkIDLen {
		return fmt.Errorf("invalid chunkID length: %d", len(idBytes))
	}

	c.mu.RLock()
	dc := c.ChunkDC
	c.mu.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return ErrChannelNotReady
	}

	ackCh := make(chan error, 1)
	c.ackMu.Lock()
	if _, exists := c.ackWaiters[chunkID]; exists {
		c.ackMu.Unlock()
		return fmt.Errorf("concurrent SendChunk already active for chunk %s", chunkID)
	}
	c.ackWaiters[chunkID] = ackCh
	c.ackMu.Unlock()
	defer func() {
		c.ackMu.Lock()
		if cur, ok := c.ackWaiters[chunkID]; ok && cur == ackCh {
			delete(c.ackWaiters, chunkID)
		}
		c.ackMu.Unlock()
	}()

	headerLen := protocol.ChunkHeaderFixedSize + len(idBytes)

	// Handle 0-byte chunk transfer
	if totalSize == 0 {
		frame := make([]byte, headerLen)
		frame[0] = protocol.DataMsgChunkResponse
		frame[1] = protocol.FlagStart | protocol.FlagEnd
		binary.BigEndian.PutUint64(frame[2:10], 0)
		binary.BigEndian.PutUint64(frame[10:18], 0)
		frame[18] = byte(len(idBytes))
		copy(frame[protocol.ChunkHeaderFixedSize:], idBytes)

		if err := dc.Send(frame); err != nil {
			return fmt.Errorf("failed to send chunk frame: %w", err)
		}
		if c.tracker != nil {
			c.tracker.RecordDataTransfer(c.RemotePeerID, int64(len(frame)), 0)
		}
	} else {
		// Frame payload size: 60 KiB to stay safely within SCTP frame limits
		framePayloadSize := 60 * 1024
		buf := make([]byte, framePayloadSize)
		offset := uint64(0)

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			nr, readErr := r.Read(buf)
			if nr > 0 {
				var flags byte = protocol.FlagData
				if offset == 0 {
					flags |= protocol.FlagStart
				}
				if offset+uint64(nr) >= uint64(totalSize) || readErr == io.EOF {
					flags |= protocol.FlagEnd
				}

				frame := make([]byte, headerLen+nr)
				frame[0] = protocol.DataMsgChunkResponse
				frame[1] = flags
				binary.BigEndian.PutUint64(frame[2:10], offset)
				binary.BigEndian.PutUint64(frame[10:18], uint64(totalSize))
				frame[18] = byte(len(idBytes))
				copy(frame[protocol.ChunkHeaderFixedSize:headerLen], idBytes)
				copy(frame[headerLen:], buf[:nr])

				// Backpressure: wait if SCTP send buffer exceeds threshold (4 MiB)
				for dc.BufferedAmount() > 4*1024*1024 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(5 * time.Millisecond):
					}
					c.mu.RLock()
					closed := c.closed
					c.mu.RUnlock()
					if closed {
						return ErrConnectionClosed
					}
				}

				if err := dc.Send(frame); err != nil {
					return fmt.Errorf("failed to send chunk frame: %w", err)
				}

				if c.tracker != nil {
					c.tracker.RecordDataTransfer(c.RemotePeerID, int64(len(frame)), 0)
				}

				offset += uint64(nr)
			}

			if readErr == io.EOF {
				if offset < uint64(totalSize) {
					return io.ErrUnexpectedEOF
				}
				break
			}
			if readErr != nil {
				return readErr
			}
		}
	}

	select {
	case err := <-ackCh:
		return err
	case <-c.stopKeep:
		return ErrConnectionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RequestChunk transmits a DataMsgChunkRequest (0x10) frame over ChunkDC.
func (c *PeerConnection) RequestChunk(chunkID string) error {
	c.mu.RLock()
	dc := c.ChunkDC
	c.mu.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return ErrChannelNotReady
	}

	idBytes := []byte(chunkID)
	if len(idBytes) == 0 || len(idBytes) > protocol.MaxChunkIDLen {
		return fmt.Errorf("invalid chunkID length: %d", len(idBytes))
	}

	frame := make([]byte, 2+len(idBytes))
	frame[0] = protocol.DataMsgChunkRequest
	frame[1] = byte(len(idBytes))
	copy(frame[2:], idBytes)
	return dc.Send(frame)
}

// SendChunkAck transmits a DataMsgChunkAck (0x13) frame over ChunkDC.
func (c *PeerConnection) SendChunkAck(chunkID string) error {
	c.mu.RLock()
	dc := c.ChunkDC
	c.mu.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return ErrChannelNotReady
	}

	idBytes := []byte(chunkID)
	if len(idBytes) == 0 || len(idBytes) > protocol.MaxChunkIDLen {
		return fmt.Errorf("invalid chunkID length: %d", len(idBytes))
	}

	frame := make([]byte, 2+len(idBytes))
	frame[0] = protocol.DataMsgChunkAck
	frame[1] = byte(len(idBytes))
	copy(frame[2:], idBytes)
	return dc.Send(frame)
}

// SendChunkError transmits a DataMsgChunkError (0x14) frame over ChunkDC.
func (c *PeerConnection) SendChunkError(chunkID string, errMsg string) error {
	c.mu.RLock()
	dc := c.ChunkDC
	c.mu.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return ErrChannelNotReady
	}

	idBytes := []byte(chunkID)
	if len(idBytes) > protocol.MaxChunkIDLen {
		idBytes = idBytes[:protocol.MaxChunkIDLen]
	}
	errBytes := []byte(errMsg)
	frame := make([]byte, 2+len(idBytes)+len(errBytes))
	frame[0] = protocol.DataMsgChunkError
	frame[1] = byte(len(idBytes))
	copy(frame[2:2+len(idBytes)], idBytes)
	copy(frame[2+len(idBytes):], errBytes)
	return dc.Send(frame)
}

// FetchChunkToFile registers an in-flight expectation, transmits a chunk request (0x10),
// and streams the incoming frames to a temporary file on disk without allocating the full
// chunk in Go heap. Caller is responsible for removing the returned file path and calling
// SendChunkAck or SendChunkError after committing to storage.
func (c *PeerConnection) FetchChunkToFile(ctx context.Context, chunkID string, totalSize int64) (string, error) {
	const MaxAllowedChunkSize int64 = 64 * 1024 * 1024
	if totalSize < 0 || totalSize > MaxAllowedChunkSize {
		return "", fmt.Errorf("invalid chunk size: %d", totalSize)
	}
	if len(chunkID) == 0 || len(chunkID) > protocol.MaxChunkIDLen {
		return "", fmt.Errorf("invalid chunkID length: %d", len(chunkID))
	}

	now := time.Now()
	c.inFlightMu.Lock()
	c.evictStaleInFlightLocked(now)
	inflight, ok := c.inFlight[chunkID]
	if !ok {
		if len(c.inFlight) >= MaxInFlightChunksPerPeer || uint64(totalSize) > MaxInFlightBytesPerPeer-c.totalInFlightBytes {
			c.inFlightMu.Unlock()
			return "", fmt.Errorf("in-flight chunk capacity exceeded")
		}
		inflight = &inFlightChunk{
			chunkID:      chunkID,
			totalSize:    uint64(totalSize),
			doneCh:       make(chan string, 1),
			errCh:        make(chan error, 1),
			hasWaiter:    true,
			createdAt:    now,
			lastActivity: now,
		}
		c.inFlight[chunkID] = inflight
		c.totalInFlightBytes += uint64(totalSize)
	} else {
		inflight.hasWaiter = true
		inflight.lastActivity = now
	}
	c.inFlightMu.Unlock()

	cleanupOnFail := true
	defer func() {
		if cleanupOnFail {
			c.inFlightMu.Lock()
			c.deleteInFlightLocked(chunkID, true)
			c.inFlightMu.Unlock()
		}
	}()

	if err := c.RequestChunk(chunkID); err != nil {
		return "", err
	}

	select {
	case tmpPath := <-inflight.doneCh:
		cleanupOnFail = false
		return tmpPath, nil
	case err := <-inflight.errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// FetchChunk registers an in-flight expectation, transmits a chunk request (0x10),
// waits for chunk completion, sends DataMsgChunkAck (0x13), and returns the chunk bytes.
func (c *PeerConnection) FetchChunk(ctx context.Context, chunkID string, totalSize int64) ([]byte, error) {
	tmpPath, err := c.FetchChunkToFile(ctx, chunkID, totalSize)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpPath)

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		_ = c.SendChunkError(chunkID, err.Error())
		return nil, err
	}
	_ = c.SendChunkAck(chunkID)
	return data, nil
}

// ReceiveChunk registers an in-flight expectation, blocks until full chunk bytes are streamed
// to disk, sends DataMsgChunkAck (0x13), and returns the chunk bytes.
func (c *PeerConnection) ReceiveChunk(ctx context.Context, chunkID string, totalSize int64) ([]byte, error) {
	const MaxAllowedChunkSize int64 = 64 * 1024 * 1024
	if totalSize < 0 || totalSize > MaxAllowedChunkSize {
		return nil, fmt.Errorf("invalid chunk size: %d", totalSize)
	}
	if len(chunkID) == 0 || len(chunkID) > protocol.MaxChunkIDLen {
		return nil, fmt.Errorf("invalid chunkID length: %d", len(chunkID))
	}

	now := time.Now()
	c.inFlightMu.Lock()
	c.evictStaleInFlightLocked(now)
	inflight, ok := c.inFlight[chunkID]
	if !ok {
		if len(c.inFlight) >= MaxInFlightChunksPerPeer || uint64(totalSize) > MaxInFlightBytesPerPeer-c.totalInFlightBytes {
			c.inFlightMu.Unlock()
			return nil, fmt.Errorf("in-flight chunk capacity exceeded")
		}
		inflight = &inFlightChunk{
			chunkID:      chunkID,
			totalSize:    uint64(totalSize),
			doneCh:       make(chan string, 1),
			errCh:        make(chan error, 1),
			hasWaiter:    true,
			createdAt:    now,
			lastActivity: now,
		}
		c.inFlight[chunkID] = inflight
		c.totalInFlightBytes += uint64(totalSize)
	} else {
		inflight.hasWaiter = true
		inflight.lastActivity = now
	}
	c.inFlightMu.Unlock()

	cleanupOnFail := true
	defer func() {
		if cleanupOnFail {
			c.inFlightMu.Lock()
			c.deleteInFlightLocked(chunkID, true)
			c.inFlightMu.Unlock()
		}
	}()

	select {
	case tmpPath := <-inflight.doneCh:
		cleanupOnFail = false
		defer os.Remove(tmpPath)
		data, err := os.ReadFile(tmpPath)
		if err != nil {
			_ = c.SendChunkError(chunkID, err.Error())
			return nil, err
		}
		_ = c.SendChunkAck(chunkID)
		return data, nil
	case err := <-inflight.errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SendStreamMessage sends a low-latency message across the stream DataChannel.
func (c *PeerConnection) SendStreamMessage(msg []byte) error {
	c.mu.RLock()
	dc := c.StreamDC
	c.mu.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return ErrChannelNotReady
	}
	return dc.Send(msg)
}

// WaitConnected waits until WebRTC peer connection reaches Connected state.
func (c *PeerConnection) WaitConnected(ctx context.Context) error {
	c.mu.RLock()
	if c.connected {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	select {
	case <-c.connectedCh:
		return nil
	case <-c.supersededCh:
		return ErrSessionSuperseded
	case <-c.stopKeep:
		return ErrConnectionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitChannelsReady waits until both Stream and Chunk DataChannels are open.
func (c *PeerConnection) WaitChannelsReady(ctx context.Context) error {
	select {
	case <-c.streamReady:
	case <-c.supersededCh:
		return ErrSessionSuperseded
	case <-c.stopKeep:
		return ErrConnectionClosed
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-c.chunkReady:
	case <-c.supersededCh:
		return ErrSessionSuperseded
	case <-c.stopKeep:
		return ErrConnectionClosed
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// Close terminates the peer connection.
func (c *PeerConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	select {
	case <-c.stopKeep:
	default:
		close(c.stopKeep)
	}

	if c.StreamDC != nil {
		_ = c.StreamDC.Close()
	}
	if c.ChunkDC != nil {
		_ = c.ChunkDC.Close()
	}
	c.cleanupInFlight(ErrConnectionClosed)
	if c.PC != nil {
		return c.PC.Close()
	}
	return nil
}
