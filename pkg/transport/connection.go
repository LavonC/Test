package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v3"
	"github.com/unicompute/obj1/pkg/instrumentation"
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
)

// Config configures the WebRTC transport engine.
type Config struct {
	NodeID          string
	STUNServers     []string
	TURNServers     []TURNServerConfig
	FallbackTimeout time.Duration // Fallback ladder timeout (default 2.0s as per spec)
	EnableLogging   bool
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

	// Event callbacks
	OnChunkReceived func(chunkID string, data []byte)
	OnStreamMessage func(msg []byte)
	OnChunkRequest  func(chunkID string)
	OnStateChange   func(state webrtc.PeerConnectionState)

	onInternalStateChange func(state webrtc.PeerConnectionState)

	// Instrumentation
	metrics   *instrumentation.PathMetrics
	tracker   *instrumentation.Tracker
	stopKeep  chan struct{}
	keepAlive *time.Ticker

	// In-flight chunk transfers: map[chunkID]*inFlightChunk
	inFlightMu         sync.Mutex
	inFlight           map[string]*inFlightChunk
	totalInFlightBytes uint64
}

type inFlightChunk struct {
	chunkID      string
	totalSize    uint64
	received     uint64
	data         []byte
	doneCh       chan []byte
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
		SessionID:    sessionID,
		LocalPeerID:  localPeerID,
		RemotePeerID: remotePeerID,
		PC:           pc,
		streamReady:  make(chan struct{}),
		chunkReady:   make(chan struct{}),
		connectedCh:  make(chan struct{}),
		stopKeep:     make(chan struct{}),
		tracker:      tracker,
		inFlight:     make(map[string]*inFlightChunk),
		metrics: &instrumentation.PathMetrics{
			PeerID:      remotePeerID,
			ConnectedAt: time.Now(),
		},
	}

	return conn
}

func (c *PeerConnection) deleteInFlightLocked(chunkID string) (*inFlightChunk, bool) {
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
	return inflight, true
}

func (c *PeerConnection) evictStaleInFlightLocked(now time.Time) {
	for id, inflight := range c.inFlight {
		if !inflight.lastActivity.IsZero() && now.Sub(inflight.lastActivity) > InFlightChunkTTL {
			select {
			case inflight.errCh <- ErrTimeout:
			default:
			}
			c.deleteInFlightLocked(id)
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
	defer c.inFlightMu.Unlock()
	for id, inflight := range c.inFlight {
		select {
		case inflight.errCh <- err:
		default:
		}
		c.deleteInFlightLocked(id)
	}
}

func (c *PeerConnection) setupHandlers() {
	c.PC.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		c.mu.Lock()
		if state == webrtc.PeerConnectionStateConnected && !c.connected {
			c.connected = true
			c.inspectCandidatePair()
			close(c.connectedCh)
			go c.startKeepAlive()
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

// inspectCandidatePair inspects the selected ICE candidate pair to determine direct vs relayed
// and measures connection parameters.
func (c *PeerConnection) inspectCandidatePair() {
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

		if c.tracker != nil {
			c.tracker.RecordConnection(c.metrics)
		}
	}
}

// startKeepAlive sends periodic ping binding frames every 15 seconds to prevent NAT timeouts.
func (c *PeerConnection) startKeepAlive() {
	ticker := time.NewTicker(15 * time.Second) // 15s keepalive as specified in Section 5 & 6.1
	defer ticker.Stop()

	for {
		select {
		case <-c.stopKeep:
			return
		case <-ticker.C:
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

func (c *PeerConnection) handleChunkMessage(data []byte) {
	if len(data) == 0 {
		return
	}

	msgType := data[0]

	switch msgType {
	case protocol.DataMsgKeepalive:
		// Keepalive ping received; no-op or ack
		return

	case protocol.DataMsgChunkResponse:
		if len(data) < protocol.ChunkHeaderSize {
			return
		}

		flags := data[1]
		offset := binary.BigEndian.Uint64(data[2:10])
		totalSize := binary.BigEndian.Uint64(data[10:18])
		rawID := string(data[18:82])
		chunkID := strings.TrimRight(rawID, "\x00")
		payload := data[protocol.ChunkHeaderSize:]

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
			inflight = &inFlightChunk{
				chunkID:      chunkID,
				totalSize:    totalSize,
				data:         make([]byte, totalSize),
				doneCh:       make(chan []byte, 1),
				errCh:        make(chan error, 1),
				createdAt:    now,
				lastActivity: now,
			}
			c.inFlight[chunkID] = inflight
			c.totalInFlightBytes += totalSize
			justCreated = true
		} else if len(inflight.data) == 0 && inflight.totalSize == 0 && totalSize > 0 {
			if flags&protocol.FlagStart == 0 {
				c.inFlightMu.Unlock()
				return
			}
			if totalSize > MaxInFlightBytesPerPeer-c.totalInFlightBytes {
				c.inFlightMu.Unlock()
				return
			}
			inflight.totalSize = totalSize
			inflight.data = make([]byte, totalSize)
			c.totalInFlightBytes += totalSize
		} else if totalSize != inflight.totalSize {
			c.inFlightMu.Unlock()
			return
		}

		dataLen := uint64(len(inflight.data))
		payloadLen := uint64(len(payload))
		if offset > dataLen || payloadLen > dataLen-offset {
			if justCreated {
				c.deleteInFlightLocked(chunkID)
			}
			c.inFlightMu.Unlock()
			return
		}

		// Require sequential, non-overlapping offsets so duplicate frames cannot cause
		// premature completion with zeroed holes.
		if offset != inflight.received {
			if justCreated {
				c.deleteInFlightLocked(chunkID)
			}
			c.inFlightMu.Unlock()
			return
		}

		copy(inflight.data[offset:], payload)
		inflight.received += payloadLen
		inflight.lastActivity = now

		completed := (inflight.totalSize == 0 && (flags&protocol.FlagEnd != 0)) ||
			(inflight.totalSize > 0 && inflight.received >= inflight.totalSize)

		var doneData []byte
		var hadWaiter bool
		var doneCh chan []byte
		if completed {
			c.deleteInFlightLocked(chunkID)
			hadWaiter = inflight.hasWaiter
			doneData = inflight.data
			doneCh = inflight.doneCh
		}
		c.inFlightMu.Unlock()

		if completed {
			select {
			case doneCh <- doneData:
			default:
			}

			if !hadWaiter {
				c.mu.RLock()
				handler := c.OnChunkReceived
				c.mu.RUnlock()
				if handler != nil {
					handler(chunkID, doneData)
				}
			}
		}

	case protocol.DataMsgChunkRequest:
		if len(data) >= 2 {
			idLen := int(data[1])
			if len(data) >= 2+idLen {
				reqID := string(data[2 : 2+idLen])
				c.mu.RLock()
				handler := c.OnChunkRequest
				c.mu.RUnlock()
				if handler != nil {
					go handler(reqID)
				}
			}
		}

	case protocol.DataMsgChunkError:
		if len(data) >= 2 {
			idLen := int(data[1])
			if len(data) >= 2+idLen {
				chunkID := string(data[2 : 2+idLen])
				errMsg := string(data[2+idLen:])
				c.inFlightMu.Lock()
				inflight, ok := c.deleteInFlightLocked(chunkID)
				c.inFlightMu.Unlock()
				if ok && inflight != nil {
					select {
					case inflight.errCh <- errors.New(errMsg):
					default:
					}
				}
			}
		}
	}
}

// SendChunk streams chunk bytes in 64 KiB - 256 KiB frames over the reliable SCTP DataChannel.
func (c *PeerConnection) SendChunk(ctx context.Context, chunkID string, r io.Reader, totalSize int64) error {
	c.mu.RLock()
	dc := c.ChunkDC
	c.mu.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return ErrChannelNotReady
	}

	// 64-byte padded chunkID (e.g. SHA-256 hex string)
	idBytes := make([]byte, 64)
	copy(idBytes, []byte(chunkID))

	// Handle 0-byte chunk transfer
	if totalSize == 0 {
		frame := make([]byte, protocol.ChunkHeaderSize)
		frame[0] = protocol.DataMsgChunkResponse
		frame[1] = protocol.FlagStart | protocol.FlagEnd
		binary.BigEndian.PutUint64(frame[2:10], 0)
		binary.BigEndian.PutUint64(frame[10:18], 0)
		copy(frame[18:82], idBytes)

		if err := dc.Send(frame); err != nil {
			return fmt.Errorf("failed to send chunk frame: %w", err)
		}
		if c.tracker != nil {
			c.tracker.RecordDataTransfer(c.RemotePeerID, int64(len(frame)), 0)
		}
		return nil
	}

	// Frame payload size: 60 KiB to stay safely within SCTP frame limits with ChunkHeaderSize (PDF Section 6.3)
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

			frame := make([]byte, protocol.ChunkHeaderSize+nr)
			frame[0] = protocol.DataMsgChunkResponse
			frame[1] = flags
			binary.BigEndian.PutUint64(frame[2:10], offset)
			binary.BigEndian.PutUint64(frame[10:18], uint64(totalSize))
			copy(frame[18:82], idBytes)
			copy(frame[protocol.ChunkHeaderSize:], buf[:nr])

			// Backpressure: wait if SCTP send buffer exceeds threshold (e.g. 4 MiB)
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
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	return nil
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
	if len(idBytes) > 255 {
		return fmt.Errorf("chunkID too long: %d", len(idBytes))
	}

	frame := make([]byte, 2+len(idBytes))
	frame[0] = protocol.DataMsgChunkRequest
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
	if len(idBytes) > 255 {
		idBytes = idBytes[:255]
	}
	errBytes := []byte(errMsg)
	frame := make([]byte, 2+len(idBytes)+len(errBytes))
	frame[0] = protocol.DataMsgChunkError
	frame[1] = byte(len(idBytes))
	copy(frame[2:2+len(idBytes)], idBytes)
	copy(frame[2+len(idBytes):], errBytes)
	return dc.Send(frame)
}

// FetchChunk registers an in-flight expectation, transmits a chunk request (0x10),
// and waits for chunk completion, error frame, or context cancellation.
func (c *PeerConnection) FetchChunk(ctx context.Context, chunkID string, totalSize int64) ([]byte, error) {
	const MaxAllowedChunkSize int64 = 64 * 1024 * 1024
	if totalSize < 0 || totalSize > MaxAllowedChunkSize {
		return nil, fmt.Errorf("invalid chunk size: %d", totalSize)
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
		var buf []byte
		if totalSize > 0 {
			buf = make([]byte, totalSize)
		}
		inflight = &inFlightChunk{
			chunkID:      chunkID,
			totalSize:    uint64(totalSize),
			data:         buf,
			doneCh:       make(chan []byte, 1),
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

	defer func() {
		c.inFlightMu.Lock()
		c.deleteInFlightLocked(chunkID)
		c.inFlightMu.Unlock()
	}()

	if err := c.RequestChunk(chunkID); err != nil {
		return nil, err
	}

	select {
	case data := <-inflight.doneCh:
		return data, nil
	case err := <-inflight.errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ReceiveChunk registers an in-flight expectation and blocks until full chunk bytes are received.
func (c *PeerConnection) ReceiveChunk(ctx context.Context, chunkID string, totalSize int64) ([]byte, error) {
	const MaxAllowedChunkSize int64 = 64 * 1024 * 1024
	if totalSize < 0 || totalSize > MaxAllowedChunkSize {
		return nil, fmt.Errorf("invalid chunk size: %d", totalSize)
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
		var buf []byte
		if totalSize > 0 {
			buf = make([]byte, totalSize)
		}
		inflight = &inFlightChunk{
			chunkID:      chunkID,
			totalSize:    uint64(totalSize),
			data:         buf,
			doneCh:       make(chan []byte, 1),
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

	defer func() {
		c.inFlightMu.Lock()
		c.deleteInFlightLocked(chunkID)
		c.inFlightMu.Unlock()
	}()

	select {
	case data := <-inflight.doneCh:
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
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitChannelsReady waits until both Stream and Chunk DataChannels are open.
func (c *PeerConnection) WaitChannelsReady(ctx context.Context) error {
	select {
	case <-c.streamReady:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-c.chunkReady:
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
