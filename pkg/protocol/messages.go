package protocol

import "encoding/json"

// MessageType represents signaling and protocol message identifiers.
type MessageType string

const (
	// Signaling messages
	MsgRegister     MessageType = "register"
	MsgRegisterAck  MessageType = "register_ack"
	MsgSDPOffer     MessageType = "sdp_offer"
	MsgSDPAnswer    MessageType = "sdp_answer"
	MsgICECandidate MessageType = "ice_candidate"
	MsgRelayInvite  MessageType = "relay_invite"
	MsgPing         MessageType = "ping"
	MsgPong         MessageType = "pong"

	// DataChannel protocol messages
	DataMsgChunkRequest  byte = 0x10
	DataMsgChunkResponse byte = 0x11
	DataMsgChunkAnnounce byte = 0x12
	DataMsgChunkAck      byte = 0x13
	DataMsgStreamLog     byte = 0x20
	DataMsgKeepalive     byte = 0x30
)

// Envelope wraps all WebSocket signaling control plane messages.
type Envelope struct {
	Type     MessageType     `json:"type"`
	SenderID string          `json:"sender_id"`
	TargetID string          `json:"target_id,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

// SDPPayload contains session description protocol data.
type SDPPayload struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"` // "offer" or "answer"
	SDP       string `json:"sdp"`
}

// ICECandidatePayload contains ICE candidate parameters.
type ICECandidatePayload struct {
	SessionID     string `json:"session_id"`
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdp_mid"`
	SDPMLineIndex uint16 `json:"sdp_mline_index"`
}

// RelayInvitePayload contains details for falling back to an elected contributor relay.
type RelayInvitePayload struct {
	SessionID    string `json:"session_id"`
	RelayAddress string `json:"relay_address"`
	SessionToken string `json:"session_token"`
}

// RegisterPayload contains registration metadata advertised by a node.
type RegisterPayload struct {
	NodeID        string `json:"node_id"`
	PoolID        string `json:"pool_id"`
	StorageQuota  int64  `json:"storage_quota"`
	IsBehindCGNAT bool   `json:"is_behind_cgnat"`
	IPv6Supported bool   `json:"ipv6_supported"`
	PublicIPv4    string `json:"public_ipv4,omitempty"`
	PublicIPv6    string `json:"public_ipv6,omitempty"`
}

// ChunkRequestFrame models a request for a specific chunk or byte range.
type ChunkRequestFrame struct {
	ChunkID string `json:"chunk_id"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"` // 0 means entire chunk
}

// ChunkHeader is the binary wire prefix on the DataChannel chunk stream:
// [0]: DataMsgType (0x10 = Request, 0x11 = Response, 0x13 = Ack, 0x30 = Keepalive)
// [1]: Flags (0x01 = Start, 0x02 = Data, 0x04 = End, 0x08 = Error)
// [2..9]: Offset (uint64)
// [10..17]: TotalSize (uint64)
// [18..81]: ChunkID / SHA256 hex string (64 bytes)
// [82..]: Payload bytes
const (
	FlagStart byte = 0x01
	FlagData  byte = 0x02
	FlagEnd   byte = 0x04
	FlagError byte = 0x08

	ChunkHeaderSize = 82
)
