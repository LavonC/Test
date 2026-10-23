package relay

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/turn/v2"
)

// TURNConfig holds settings for an embedded TURN server.
type TURNConfig struct {
	PublicIP string
	Port     int
	Realm    string
	Secret   string            // Shared secret for coturn-compatible REST API credentials
	Users    map[string]string // Static username -> password
	MinPort  uint16
	MaxPort  uint16
}

// EmbeddedTURN wraps pion/turn/v2 for local and coordinator-embedded relay capabilities.
type EmbeddedTURN struct {
	mu     sync.Mutex
	server *turn.Server
	cfg    TURNConfig
	addr   string
}

// NewEmbeddedTURN spins up a local or public embedded TURN server.
func NewEmbeddedTURN(cfg TURNConfig) (*EmbeddedTURN, error) {
	if cfg.Port == 0 {
		cfg.Port = 3478
	}
	if cfg.Realm == "" {
		cfg.Realm = "unicompute.local"
	}
	if cfg.PublicIP == "" {
		cfg.PublicIP = "127.0.0.1"
	}

	udpListener, err := net.ListenPacket("udp4", "0.0.0.0:"+strconv.Itoa(cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("failed to create TURN UDP listener on port %d: %w", cfg.Port, err)
	}

	turnServer, err := turn.NewServer(turn.ServerConfig{
		Realm: cfg.Realm,
		AuthHandler: func(username string, realm string, srcAddr net.Addr) ([]byte, bool) {
			// Check static configured users first
			if pw, ok := cfg.Users[username]; ok {
				return turn.GenerateAuthKey(username, realm, pw), true
			}

			// Support standard coturn-compatible REST credentials if secret is configured
			if cfg.Secret != "" {
				parts := strings.SplitN(username, ":", 2)
				if len(parts) == 2 {
					expiry, err := strconv.ParseInt(parts[0], 10, 64)
					if err == nil && expiry >= time.Now().Unix() {
						mac := hmac.New(sha1.New, []byte(cfg.Secret))
						mac.Write([]byte(username))
						pw := base64.StdEncoding.EncodeToString(mac.Sum(nil))
						return turn.GenerateAuthKey(username, realm, pw), true
					}
				}
			}

			return nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: udpListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP(cfg.PublicIP),
					Address:      "0.0.0.0",
				},
			},
		},
	})
	if err != nil {
		_ = udpListener.Close()
		return nil, fmt.Errorf("failed to initialize pion/turn server: %w", err)
	}

	return &EmbeddedTURN{
		server: turnServer,
		cfg:    cfg,
		addr:   udpListener.LocalAddr().String(),
	}, nil
}

// Addr returns the TURN listening address.
func (t *EmbeddedTURN) Addr() string {
	return t.addr
}

// Close terminates the TURN server.
func (t *EmbeddedTURN) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.server != nil {
		return t.server.Close()
	}
	return nil
}

// GenerateRESTCredentials generates coturn-compatible time-limited HMAC-SHA1 credentials
// for WebRTC ICE TURN configuration (draft-uberti-behave-turn-rest).
func GenerateRESTCredentials(username, secret string, ttlSeconds int64) (string, string) {
	expiry := time.Now().Unix() + ttlSeconds
	user := fmt.Sprintf("%d:%s", expiry, username)
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(user))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return user, password
}
