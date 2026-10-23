package relay

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Magic bytes identifying the UniCompute Contributor Relay protocol (Syncthing relay-v1 inspired)
const (
	RelayMagic   uint32 = 0x554E4952 // 'UNIR'
	RelayVersion byte   = 0x01

	MsgTypeCreateSession byte = 0x01
	MsgTypeJoinSession   byte = 0x02
	MsgTypeSessionReady  byte = 0x03
	MsgTypeError         byte = 0x04
	MsgTypePing          byte = 0x05
	MsgTypePong          byte = 0x06
)

var (
	ErrSessionNotFound   = errors.New("relay session not found")
	ErrSessionExpired    = errors.New("relay session expired")
	ErrSessionOccupied   = errors.New("relay session already occupied")
	ErrInvalidMagic      = errors.New("invalid relay magic")
	ErrRateLimitExceeded = errors.New("relay rate limit exceeded")
)

// GenerateSessionToken creates a secure random session token for relay coordination.
func GenerateSessionToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ServerConfig configures the Contributor Relay Server.
type ServerConfig struct {
	ListenAddr     string
	RateLimitBytes int64         // Maximum bytes/sec per connection (0 for unlimited)
	SessionTimeout time.Duration // Time to wait for peer to join before expiring
	MaxSessions    int
}

// ContributorRelay implements a peer-elected, high-performance opaque forwarding relay.
// The relay forwards encrypted bytes without inspection (blind relaying), matching pairs
// via cryptographic session tokens.
type ContributorRelay struct {
	mu           sync.Mutex
	cfg          ServerConfig
	listener     net.Listener
	sessions     map[string]*relaySession
	running      bool
	closeCh      chan struct{}
	totalRelayed int64
}

type relaySession struct {
	token     string
	creatorID string
	creator   net.Conn
	created   time.Time
	readyCh   chan net.Conn
}

// NewContributorRelay starts a contributor relay server.
func NewContributorRelay(cfg ServerConfig) (*ContributorRelay, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	if cfg.SessionTimeout == 0 {
		cfg.SessionTimeout = 30 * time.Second
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 1000
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", cfg.ListenAddr, err)
	}

	r := &ContributorRelay{
		cfg:      cfg,
		listener: ln,
		sessions: make(map[string]*relaySession),
		closeCh:  make(chan struct{}),
		running:  true,
	}

	go r.acceptLoop()
	go r.cleanupLoop()

	return r, nil
}

// Addr returns the listener's network address.
func (r *ContributorRelay) Addr() net.Addr {
	return r.listener.Addr()
}

func (r *ContributorRelay) acceptLoop() {
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			select {
			case <-r.closeCh:
				return
			default:
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}

		go r.handleConn(conn)
	}
}

func (r *ContributorRelay) handleConn(conn net.Conn) {
	// Read handshake header:
	// [0..3]: Magic (0x554E4952)
	// [4]: Version (0x01)
	// [5]: MsgType (CreateSession = 0x01, JoinSession = 0x02)
	// [6..7]: TokenLength (uint16)
	// [8..8+len]: Token string
	header := make([]byte, 8)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, header); err != nil {
		_ = conn.Close()
		return
	}

	magic := binary.BigEndian.Uint32(header[0:4])
	if magic != RelayMagic {
		_ = conn.Close()
		return
	}

	version := header[4]
	if version != RelayVersion {
		_ = conn.Close()
		return
	}

	msgType := header[5]
	tokenLen := binary.BigEndian.Uint16(header[6:8])
	if tokenLen == 0 || tokenLen > 256 {
		_ = conn.Close()
		return
	}

	tokenBuf := make([]byte, tokenLen)
	if _, err := io.ReadFull(conn, tokenBuf); err != nil {
		_ = conn.Close()
		return
	}
	token := string(tokenBuf)

	_ = conn.SetDeadline(time.Time{}) // Clear deadline

	r.mu.Lock()
	switch msgType {
	case MsgTypeCreateSession:
		if len(r.sessions) >= r.cfg.MaxSessions {
			r.mu.Unlock()
			_ = conn.Close()
			return
		}

		session := &relaySession{
			token:   token,
			creator: conn,
			created: time.Now(),
			readyCh: make(chan net.Conn, 1),
		}
		r.sessions[token] = session
		r.mu.Unlock()

		// Wait for joiner or timeout
		select {
		case joiner, ok := <-session.readyCh:
			if !ok || joiner == nil {
				return
			}
			// Both peers connected! Notify creator
			_ = sendControlMsg(conn, MsgTypeSessionReady)
			r.bridgeConns(conn, joiner)
		case <-time.After(r.cfg.SessionTimeout):
			r.mu.Lock()
			delete(r.sessions, token)
			r.mu.Unlock()
			_ = sendControlMsg(conn, MsgTypeError)
			_ = conn.Close()
		case <-r.closeCh:
			_ = conn.Close()
		}

	case MsgTypeJoinSession:
		session, ok := r.sessions[token]
		if !ok {
			r.mu.Unlock()
			_ = sendControlMsg(conn, MsgTypeError)
			_ = conn.Close()
			return
		}
		delete(r.sessions, token)
		r.mu.Unlock()

		// Send ready to joiner
		_ = sendControlMsg(conn, MsgTypeSessionReady)
		session.readyCh <- conn

	default:
		r.mu.Unlock()
		_ = conn.Close()
	}
}

func sendControlMsg(conn net.Conn, msgType byte) error {
	buf := make([]byte, 6)
	binary.BigEndian.PutUint32(buf[0:4], RelayMagic)
	buf[4] = RelayVersion
	buf[5] = msgType
	_, err := conn.Write(buf)
	return err
}

func (r *ContributorRelay) bridgeConns(c1, c2 net.Conn) {
	defer c1.Close()
	defer c2.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	pipe := func(dst, src net.Conn) {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			nr, err := src.Read(buf)
			if nr > 0 {
				r.mu.Lock()
				r.totalRelayed += int64(nr)
				rateLimit := r.cfg.RateLimitBytes
				r.mu.Unlock()

				if rateLimit > 0 {
					delay := time.Duration(float64(nr) / float64(rateLimit) * float64(time.Second))
					if delay > 0 {
						time.Sleep(delay)
					}
				}

				nw, ew := dst.Write(buf[0:nr])
				if ew != nil || nw != nr {
					break
				}
			}
			if err != nil {
				break
			}
		}
		// Send half-close if supported
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}

	go pipe(c1, c2)
	go pipe(c2, c1)

	wg.Wait()
}

func (r *ContributorRelay) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.mu.Lock()
			now := time.Now()
			for token, session := range r.sessions {
				if now.Sub(session.created) > r.cfg.SessionTimeout {
					_ = session.creator.Close()
					delete(r.sessions, token)
				}
			}
			r.mu.Unlock()
		case <-r.closeCh:
			return
		}
	}
}

// TotalRelayedBytes returns the cumulative number of bytes relayed through this node.
func (r *ContributorRelay) TotalRelayedBytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalRelayed
}

// Close stops the contributor relay server.
func (r *ContributorRelay) Close() error {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return nil
	}
	r.running = false
	close(r.closeCh)
	err := r.listener.Close()

	for _, session := range r.sessions {
		_ = session.creator.Close()
	}
	r.sessions = make(map[string]*relaySession)
	r.mu.Unlock()
	return err
}

// RelayClient provides client-side functionality to establish connections via a Contributor Relay.
type RelayClient struct{}

// CreateRelayedSession registers a session on the relay and waits for the peer to join.
func (c *RelayClient) CreateRelayedSession(ctx context.Context, relayAddr string, token string) (net.Conn, error) {
	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial relay %s: %w", relayAddr, err)
	}

	// Send CreateSession header
	header := make([]byte, 8+len(token))
	binary.BigEndian.PutUint32(header[0:4], RelayMagic)
	header[4] = RelayVersion
	header[5] = MsgTypeCreateSession
	binary.BigEndian.PutUint16(header[6:8], uint16(len(token)))
	copy(header[8:], []byte(token))

	if _, err := conn.Write(header); err != nil {
		_ = conn.Close()
		return nil, err
	}

	// Read session ready response
	resp := make([]byte, 6)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(conn, resp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("waiting for peer failed: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})

	if binary.BigEndian.Uint32(resp[0:4]) != RelayMagic || resp[5] != MsgTypeSessionReady {
		_ = conn.Close()
		return nil, errors.New("relay session rejected or failed")
	}

	return conn, nil
}

// JoinRelayedSession connects to the relay and joins an existing session initiated by a peer.
func (c *RelayClient) JoinRelayedSession(ctx context.Context, relayAddr string, token string) (net.Conn, error) {
	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial relay %s: %w", relayAddr, err)
	}

	// Send JoinSession header
	header := make([]byte, 8+len(token))
	binary.BigEndian.PutUint32(header[0:4], RelayMagic)
	header[4] = RelayVersion
	header[5] = MsgTypeJoinSession
	binary.BigEndian.PutUint16(header[6:8], uint16(len(token)))
	copy(header[8:], []byte(token))

	if _, err := conn.Write(header); err != nil {
		_ = conn.Close()
		return nil, err
	}

	// Read session ready response
	resp := make([]byte, 6)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, resp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("joining relay session failed: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})

	if binary.BigEndian.Uint32(resp[0:4]) != RelayMagic || resp[5] != MsgTypeSessionReady {
		_ = conn.Close()
		return nil, errors.New("relay session join rejected")
	}

	return conn, nil
}
