# UniCompute Objective 1: Peer-to-Peer Storage Substrate & Contributed Capacity

An independent, fully-featured, tested Go module implementing **Objective 1 (Member A)** of the UniCompute distributed volunteer computing and storage platform, as specified in the UniCompute Architecture Blueprint and Project Objectives specifications.

---

## 1. Executive Summary & Objective Scope

Objective 1 owns the **peer-to-peer data transport substrate** and **contributed storage capacity accounting**. It provides the underlying data plane upon which all other objectives (O2 Durability/Erasure Coding, O3 Chunking/Deduplication, O4 Client Encryption, and O5 Locality-Aware Scheduling) operate.

### Scope of Work (as specified in PDF)
1. **Transport & Connection Lifecycle**: Full WebRTC PeerConnection management (ICE, DTLS 1.3, SCTP) with dual-channel multiplexing (`stream` for logs/control, `chunk` for bulk data).
2. **Dual-Stack NAT & CGNAT Detection**: Automatic discovery of local IPv4/IPv6 interfaces, RFC 6598 Carrier-Grade NAT (`100.64.0.0/10`) detection, and dual-stack STUN candidate harvesting.
3. **Direct Connection Establishment & Hole Punching**: Direct peer-to-peer hole punching across consumer NATs without requiring port forwarding.
4. **Relay Fallback**: Embedded TURN server (`pion/turn/v2`) and peer-elected contributor relays (Syncthing relay-v1 model) for pairs that cannot connect directly.
5. **Keepalive & Session Resumption**: 15-second ICE keepalive binding indications, 20-second WebSocket ping tickers with 60-second read deadlines, and certificate/UUID-based node identity surviving IP address churn.
6. **Node-Local Blob Store & Quota Accounting**: Content-addressed and chunk-addressed local storage vault enforcing disk quotas, atomic commits via temporary files, and SHA-256 integrity verification.
7. **Per-Connection Path Instrumentation**: Real-time telemetry reporting candidate pair types (`host-host`, `host-srflx`, `srflx-srflx`, `prflx`, `relay`), direct vs relayed ratio, RTT, throughput, and fleet-wide CGNAT prevalence.
8. **Invariant CP-1 (Data Plane Independence)**: Strict plane separation ensuring established P2P connections, chunk transfers, and reads continue uninterrupted if the central coordinator/signaling hub fails or terminates.

---

## 2. Package Architecture

```
UniComputeOBJ1/
├── cmd/
│   ├── unicompute-node/          # Contributor peer storage node CLI
│   ├── unicompute-coordinator/   # Signaling hub & embedded TURN coordinator CLI
│   └── unicompute-relay/         # Standalone peer-elected contributor relay CLI
├── pkg/
│   ├── blobstore/                # Local disk vault, quota enforcement, atomic commits, SHA-256 validation
│   ├── nat/                      # Dual-stack STUN probing, CGNAT detection, virtual interface filtering
│   ├── relay/                    # Contributor relay (Syncthing relay-v1) and embedded TURN server
│   ├── transport/                # WebRTC manager, 16 MiB SCTP buffer, dual DataChannels, trickle ICE
│   ├── instrumentation/          # Path metrics, candidate pair classification, fleet NAT telemetry
│   ├── protocol/                 # Framing, envelopes, chunk transfer wire protocol
│   ├── signaling/                # Resilient WebSocket hub and auto-reconnecting client (8s backoff)
│   └── node/                     # Unified Node facade and Month 1 StubNode for Objectives 2, 3, 4, 5
├── go.mod
└── go.sum
```

---

## 3. Interfaces Provided to Higher Objectives

Objective 1 exposes the `node.Substrate` interface (and `node.StubNode` for early testing):

```go
type Substrate interface {
    // Blob Storage & Quotas (consumed by O2 Cauchy RS shards & O3 FastCDC chunks)
    PutBlob(id string, r io.Reader) (int64, error)
    GetBlob(id string) (io.ReadCloser, error)
    HasBlob(id string) bool
    DeleteBlob(id string) error
    VerifyBlob(id string) (bool, error)
    StorageUsage() blobstore.StorageUsage

    // P2P Data Plane (consumed by O2 repair multicasts, O3 transfers, O5 data movement)
    TransferChunk(ctx context.Context, peerID string, chunkID string) error
    FetchChunk(ctx context.Context, peerID string, chunkID string, expectedSize int64) error
    SendStream(peerID string, data []byte) error

    // Telemetry & Network Intelligence (consumed by O5 Convex Locality Scheduler)
    GetPathMetrics(peerID string) (instrumentation.PathMetrics, bool)
    FleetStats() instrumentation.FleetNATStats
    NATReport() *nat.NATReport

    Close() error
}
```

### Integration Matrix
| Objective | Interaction with Objective 1 Substrate |
|---|---|
| **O2 (Durability & Repair)** | Writes and reads Cauchy Reed-Solomon $(k=32, n=80)$ shards via `PutBlob` / `GetBlob`; multicasts repair shards across peers via `TransferChunk`. |
| **O3 (Chunking & Deduplication)** | Chunks datasets with FastCDC; checks `HasBlob` for local deduplication; exchanges missing unique chunks via `TransferChunk` / `FetchChunk`. |
| **O4 (Confidentiality & Encryption)** | Applies authenticated convergent encryption (MLE AES-256-GCM); passes ciphertext to `PutBlob` and `TransferChunk`. |
| **O5 (Locality-Aware Scheduler)** | Queries `GetPathMetrics` and `StorageUsage` to evaluate locality coefficients ($L$ and $R$) and route computation near resident chunks. |

---

## 4. Empirical Defect Fixes & Windows-Specific Engineering (Section 6.3 & 6.4)

This implementation directly addresses and resolves all six architectural defects identified in Section 6.3 of the UniCompute specification:

1. **WebSocket Keepalive & Deadlines**:
   - Implemented 20-second ping ticker, 60-second read deadlines, and pong handlers to prevent the 74% of consumer NATs that drop idle UDP/TCP state within 60 seconds from causing silent disconnects.
2. **SCTP Receive Buffer Raised to 16 MiB**:
   - `settingEngine.SetSCTPMaxReceiveBufferSize(16 * 1024 * 1024)`: Prevents throughput capping on high-latency residential links (raising max line speed from ~336 Mbps at 100ms RTT to >1 Gbps).
3. **Max Message Size & Backpressure**:
   - Configured chunk frames at 128 KiB with flow control and SCTP buffer threshold monitoring (`dc.BufferedAmount() > 4 MiB`) to prevent memory exhaustion during bulk transfers.
4. **Dual-Stack STUN Probing**:
   - Probes both IPv4 and IPv6 STUN endpoints. If a peer has native IPv6 (78.6% of residential broadband in India), direct IPv6 peer links bypass IPv4 CGNAT entirely.
5. **Carrier-Grade NAT (CGNAT) Detection**:
   - Enforces an RFC 6598 range check (`100.64.0.0/10`). Distinguishes between standard home routers (where UDP hole punching succeeds) and CGNAT gateways (where IPv6 or contributor relay fallback is mandatory).
6. **Virtual Adapter Pruning (`SetInterfaceFilter`)**:
   - Filters out Hyper-V (`vEthernet`), WSL2 virtual adapters, Docker bridges, VirtualBox, TAP, and VPN interfaces that would otherwise generate unroutable host candidates and stall ICE checks.
7. **Windows Socket Reset Error Handling (Section 6.4)**:
   - Detects `WSAENETRESET` (10052) and `WSAECONNRESET` (10054) arising from unreached hole-punch targets and treats them as transient errors rather than terminating the socket.

---

## 5. Verification Record & Invariant CP-1 Testing

All test suites pass natively using standard Go testing:

```powershell
$env:GOTMPDIR="c:\Users\HP\Desktop\Noval\Projects\UniComputeOBJ1\.tmp"
go test -v ./...
```

### Verified Test Matrix
- **`pkg/blobstore`**:
  - `TestBlobStore_BasicPutGet`: Atomic temporary-file write, hash calculation, read validation.
  - `TestBlobStore_QuotaEnforcement`: Strict rejection when quota is exceeded; quota reclamation on delete.
  - `TestBlobStore_ChecksumMismatch`: Detection of corrupted or forged content IDs.
  - `TestBlobStore_CorruptionDetection`: Local bit-rot and scrub audit detection.
  - `TestBlobStore_PersistenceReload`: Reopening store correctly indexes existing blobs and byte usage.
  - `TestBlobStore_ConcurrentAccess`: Thread-safe concurrent reads and writes.
- **`pkg/nat`**:
  - `TestIsCGNAT`: Validates RFC 6598 `100.64.0.0/10` boundary conditions.
  - `TestIsPrivateIPv4`: RFC 1918 classification.
  - `TestIsVirtualInterface`: Detection and pruning of Hyper-V, WSL, Docker, Tailscale, VPN adapters.
  - `TestIsTransientSocketError`: Windows error 10052/10054 resilience.
  - `TestSTUNProbingAndClassifier`: Dual-stack STUN probe against mock STUN server.
- **`pkg/relay`**:
  - `TestContributorRelay_EndToEnd`: Full blind relay pairing (Syncthing relay-v1 model) with bidirectional data transfer and byte counting.
  - `TestContributorRelay_NotFoundOrTimeout`: Session timeout and eviction.
  - `TestEmbeddedTURN_Lifecycle`: In-process TURN server startup and port binding.
- **`pkg/signaling`**:
  - `TestSignaling_HubAndClientDispatch`: Client registration and SDP offer/answer relaying.
- **`pkg/transport`**:
  - `TestTransport_WebRTCConnectionAndChunkTransfer`: Dual-channel establishment (`stream` and `chunk`), 16 MiB receive buffer, bulk chunk transfer, and path instrumentation.
- **`pkg/node`**:
  - `TestStubNode_Substrate`: Verification of the Month 1 mock stub for O2, O3, O4, O5 owners.
  - `TestNode_EndToEndChunkExchange`: Full P2P lifecycle across two independent nodes.
  - `TestNode_InvariantCP1_CoordinatorFailureResilience`: **Critical proof of Invariant CP-1**. Node 1 and Node 2 connect via the coordinator. The coordinator is explicitly terminated. Node 1 transfers a new chunk directly to Node 2 over the WebRTC DataChannel. The chunk is received, hash-verified, and stored with the coordinator completely dead.

---

## 6. Engineering Considerations & Assumptions Made

1. **Pion ICE Keepalives**: Configured via `SetICETimeouts(20s, 25s, 15s)` alongside application-layer DataChannel keepalive ping messages (`0x30`) transmitted every 15 seconds.
2. **Relay Token Matching**: Relayed sessions use 128-bit random hex tokens exchanged via signaling so that relays remain blind to payload data.
3. **Storage Vault Directory Structure**: Separates active blobs into a `data/` directory and staged uploads into a `tmp/` directory to guarantee atomic POSIX/NTFS renames.
4. **Windows Temp Binary Execution**: Addressed Windows Application Control policies by configuring a local `.tmp` directory for Go test binaries.
