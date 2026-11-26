package protocol

import "encoding/json"

// MessageType represents signaling and protocol message identifiers.
type MessageType string

const (
	// Signaling messages
	MsgChallenge        MessageType = "challenge"
	MsgRegister         MessageType = "register"
	MsgRegisterAck      MessageType = "register_ack"
	MsgSDPOffer         MessageType = "sdp_offer"
	MsgSDPAnswer        MessageType = "sdp_answer"
	MsgICECandidate     MessageType = "ice_candidate"
	MsgPing             MessageType = "ping"
	MsgPong             MessageType = "pong"
	MsgPeerListRequest  MessageType = "peer_list_req"
	MsgPeerListResponse MessageType = "peer_list_resp"
	MsgPeerJoined       MessageType = "peer_joined"
	MsgPeerLeft         MessageType = "peer_left"

	// DataChannel protocol messages
	DataMsgChunkRequest  byte = 0x10
	DataMsgChunkResponse byte = 0x11
	DataMsgChunkAnnounce byte = 0x12
	DataMsgChunkAck      byte = 0x13
	DataMsgChunkError    byte = 0x14
	DataMsgStreamLog     byte = 0x20
	DataMsgKeepalive     byte = 0x30

	// Objective 3 GPU protocol messages
	DataMsgGPUTaskRequest        byte = 0x40
	DataMsgGPUTaskResponse       byte = 0x41
	DataMsgGPUPreemptAlert       byte = 0x42
	DataMsgGPUCheckpointAnnounce byte = 0x43
)

// Envelope wraps all WebSocket signaling control plane messages.
type Envelope struct {
	Type     MessageType     `json:"type"`
	SenderID string          `json:"sender_id"`
	TargetID string          `json:"target_id,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

// ChallengePayload carries a single-use random hex nonce issued by the coordinator upon WS upgrade.
type ChallengePayload struct {
	Nonce string `json:"nonce"`
}

// SDPPayload contains session description protocol data and DTLS fingerprint binding.
type SDPPayload struct {
	SessionID     string `json:"session_id"`
	Type          string `json:"type"` // "offer" or "answer"
	SDP           string `json:"sdp"`
	PublicKey     string `json:"public_key,omitempty"`
	Signature     string `json:"signature,omitempty"`
	IsBehindCGNAT bool   `json:"is_behind_cgnat,omitempty"`
}

// ICECandidatePayload contains ICE candidate parameters.
type ICECandidatePayload struct {
	SessionID     string `json:"session_id"`
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdp_mid"`
	SDPMLineIndex uint16 `json:"sdp_mline_index"`
}

// TURNServerInfo carries coordinator-issued TURN server configuration and REST credentials.
type TURNServerInfo struct {
	URL        string `json:"url"`
	Username   string `json:"username"`
	Credential string `json:"credential"`
}

// RegisterPayload contains registration metadata advertised by a node.
type RegisterPayload struct {
	NodeID        string `json:"node_id"`
	Nonce         string `json:"nonce,omitempty"`
	PoolID        string `json:"pool_id"`
	StorageQuota  int64  `json:"storage_quota"`
	IsBehindCGNAT bool   `json:"is_behind_cgnat"`
	IPv6Supported bool   `json:"ipv6_supported"`
	PublicIPv4    string `json:"public_ipv4,omitempty"`
	PublicIPv6    string `json:"public_ipv6,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	Signature     string `json:"signature,omitempty"`
}

// RegisterAckPayload is returned by the coordinator upon registration acceptance or rejection.
type RegisterAckPayload struct {
	NodeID      string           `json:"node_id,omitempty"`
	TURNServers []TURNServerInfo `json:"turn_servers,omitempty"`
	Error       string           `json:"error,omitempty"`
}

// PeerListPayload contains a list of online nodes on the network.
type PeerListPayload struct {
	Nodes      []string          `json:"nodes"`
	PublicKeys map[string]string `json:"public_keys,omitempty"`
}

// PeerEventPayload contains a node ID for join/leave events.
type PeerEventPayload struct {
	NodeID    string `json:"node_id"`
	PublicKey string `json:"public_key,omitempty"`
}

// ChunkRequestFrame models a request for a specific chunk or byte range.
type ChunkRequestFrame struct {
	ChunkID string `json:"chunk_id"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"` // 0 means entire chunk
}

// ChunkHeader is the binary wire prefix on the DataChannel chunk stream:
// [0]: DataMsgType (0x10 = Request, 0x11 = Response, 0x13 = Ack, 0x14 = Error, 0x30 = Keepalive)
// [1]: Flags (0x01 = Start, 0x02 = Data, 0x04 = End, 0x08 = Error)
// [2..9]: Offset (uint64)
// [10..17]: TotalSize (uint64)
// [18]: ChunkIDLen (uint8, 1..128)
// [19..19+ChunkIDLen]: ChunkID bytes (up to 128 bytes)
// [19+ChunkIDLen..]: Payload bytes
const (
	FlagStart byte = 0x01
	FlagData  byte = 0x02
	FlagEnd   byte = 0x04
	FlagError byte = 0x08

	ChunkHeaderFixedSize = 19
	MaxChunkIDLen        = 128
)
