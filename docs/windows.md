# UniCompute: Objectives 1, 2, 3 & Desktop GUI — Windows 10/11 Deployment, Operation & Testing Guide

This guide provides end-to-end instructions for installing prerequisites, building, configuring, operating, and verifying the **UniCompute Volunteer Compute & Distributed Storage Fleet (Objectives 1, 2, and 3)** alongside the **Interactive Desktop GUI Control Room** on Microsoft Windows 10 and Windows 11 (64-bit AMD64/ARM64).

---

## Table of Contents
1. [Architecture Overview & Binaries](#1-architecture-overview--binaries)
2. [Prerequisites Installation on Windows 10/11](#2-prerequisites-installation-on-windows-1011)
   - [2.1 Git for Windows](#21-git-for-windows)
   - [2.2 Go Programming Language (1.22+)](#22-go-programming-language-122)
   - [2.3 Docker Desktop with WSL2 Backend](#23-docker-desktop-with-wsl2-backend)
   - [2.4 GPU Drivers & Acceleration Runtime (DirectML / CUDA)](#24-gpu-drivers--acceleration-runtime-directml--cuda)
   - [2.5 Browser Runtime for Desktop GUI Window (Edge / Chrome)](#25-browser-runtime-for-desktop-gui-window-edge--chrome)
3. [Windows Security, Code Signing & Smart App Control](#3-windows-security-code-signing--smart-app-control)
   - [3.1 PowerShell Execution Policy](#31-powershell-execution-policy)
   - [3.2 Unblocking Downloaded Files](#32-unblocking-downloaded-files)
   - [3.3 Authenticode Code-Signing (`build.ps1`)](#33-authenticode-code-signing-buildps1)
   - [3.4 Temporary Execution Directory (`GOTMPDIR`)](#34-temporary-execution-directory-gotmpdir)
4. [Cloning the Repository & Building Binaries](#4-cloning-the-repository--building-binaries)
5. [Running the Automated Master Verification Suite (`test_all.ps1`)](#5-running-the-automated-master-verification-suite-test_allps1)
6. [Pre-Pulling Docker Sandbox Images](#6-pre-pulling-docker-sandbox-images)
7. [Running a 2-Node Cluster on Windows](#7-running-a-2-node-cluster-on-windows)
   - [Option A: Public Live Coordinator on Render](#option-a-public-live-coordinator-on-render)
   - [Option B: Local Coordinator on Localhost](#option-b-local-coordinator-on-localhost)
8. [Comprehensive CLI Command Reference & Feature Walkthrough](#8-comprehensive-cli-command-reference--feature-walkthrough)
   - [8.1 WebRTC P2P Signaling & Path Telemetry](#81-webrtc-p2p-signaling--path-telemetry)
   - [8.2 Content-Addressed Storage (CAS) Vault](#82-content-addressed-storage-cas-vault)
   - [8.3 Remote Terminal Execution (`exec`)](#83-remote-terminal-execution-exec)
   - [8.4 Air-Gapped Docker Sandbox Code Execution (`run`)](#84-air-gapped-docker-sandbox-code-execution-run)
   - [8.5 Objective 3: GPU Acceleration & Resource Governance](#85-objective-3-gpu-acceleration--resource-governance)
9. [Sample Programs & Security Boundary Tests](#9-sample-programs--security-boundary-tests)
   - [9.1 Python Data Processing with Stdin (`calc.py`)](#91-python-data-processing-with-stdin-calcpy)
   - [9.2 C++ High-Precision Computation (`fib.cpp`)](#92-c-high-precision-computation-fibcpp)
   - [9.3 C ISO C17 Runtime Benchmark (`hello.c`)](#93-c-iso-c17-runtime-benchmark-helloc)
   - [9.4 Security Test: Execution Timeout Enforcement (`timeout_test.py`)](#94-security-test-execution-timeout-enforcement-timeout_testpy)
   - [9.5 Security Test: Air-Gap Network Leak Block (`network_leak_test.py`)](#95-security-test-air-gap-network-leak-block-network_leak_testpy)
   - [9.6 Security Test: 256 MB RAM Limit Enforcement (`mem_limit_test.py`)](#96-security-test-256-mb-ram-limit-enforcement-mem_limit_testpy)
   - [9.7 GPU Sample: Portable Vector Addition (`portable_vector_add.cu`)](#97-gpu-sample-portable-vector-addition-portable_vector_addcu)
   - [9.8 GPU Sample: Warp Reduction Blocker (`warp_reduction.cu`)](#98-gpu-sample-warp-reduction-blocker-warp_reductioncu)
   - [9.9 GPU Sample: Inline PTX Assembly Blocker (`inline_ptx.cu`)](#99-gpu-sample-inline-ptx-assembly-blocker-inline_ptxcu)
   - [9.10 GPU Sample: Model Capsule & Checkpoint (`test_model.json`)](#910-gpu-sample-model-capsule--checkpoint-test_modeljson)
10. [Desktop GUI Control Room Operation](#10-desktop-gui-control-room-operation)
    - [10.1 Launching the Desktop Window](#101-launching-the-desktop-window)
    - [10.2 Sidebar Workstation Views](#102-sidebar-workstation-views)
    - [10.3 Walkthrough: Standalone Single-Node Testing (No External Peer Needed)](#103-walkthrough-standalone-single-node-testing-no-external-peer-needed)
    - [10.4 Walkthrough: Two-Node Distributed Cluster Testing](#104-walkthrough-two-node-distributed-cluster-testing)
11. [Troubleshooting & Windows Diagnostics](#11-troubleshooting--windows-diagnostics)

---

## 1. Architecture Overview & Binaries

UniCompute integrates distributed volunteer compute, storage, and cross-platform hardware acceleration across three primary objectives:
- **Objective 1 (P2P Transport & Storage Substrate)**: Dual-stack ICE, CGNAT RFC 6598 hole punching, standalone/legacy relay primitives (`pkg/relay`), WebRTC SCTP multiplexing (`stream` and `chunk`, DTLS 1.2 via `pion/dtls/v2`), and content-addressed storage (CAS) disk vault with SHA-256 scrub auditing.
- **Objective 2 (Isolated Compute Engine)**: Air-gapped Docker container execution (`--network none`, `--memory 256m`, `--cpus 1.0`, `--read-only`, `--ipc=none`, dropping all capabilities), remote shell execution, and language runtimes (C/C++, Python).
- **Objective 3 (Cross-Platform GPU Acceleration & Governance)**: Hardware probing (DirectX 12 / DirectML, CUDA, DXGI, NVML), 50 ms battery/activity watchdog preemption, Win32 JobObject memory/priority limits for external child PIDs assigned via `JobObjectSandbox.AssignProcess`, regex-based CUDA-to-AMD HIPIFY compatibility analysis & symbol translation, and pure-Go simulated ONNX training checkpoint persistence (`Simulated: true`).
- **Desktop Workstation GUI**: A standalone desktop web app (`cmd/unicompute-gui`) featuring a responsive Tailwind sidebar control room across Coordinator (Overview, Network Topology SVG mesh, CPU Sandbox & Shell, GPU Compute, Task Queue, Storage & Blobs), Worker (Worker Allowlist, Nodes), and System (Logs, Settings, Architecture) views.

The project compiles into the following binaries in the `bin/` directory:

| Binary | Description |
|---|---|
| `bin\unicompute-gui.exe` | **Desktop Workstation GUI**. Embedded frontend assets (`//go:embed web/*`), interactive SVG topology mesh, and Coordinator/Worker/System views for Objectives 1, 2, and 3. Protected with cryptographic session token auth and strict loopback Origin checks. |
| `bin\unicompute-node.exe` | **Contributor Peer Node Daemon & CLI**. Manages local CAS vault, WebRTC SCTP connections, Docker sandbox execution, remote shell (opt-in + allowlist), and GPU acceleration. |
| `bin\unicompute-coordinator.exe` | **Signaling Hub & Embedded TURN Server**. Relays WebSocket SDP offers/answers, ICE candidates, and fleet registry. Protected via cluster auth tokens (`-auth-token`) and duplicate node ID hijacking rejection. |
| `bin\unicompute-relay.exe` | **Contributor Relay Daemon (Deprecated)**. Legacy byte relay fallback; modern deployments rely on native Pion ICE and embedded/external TURN. |
| `bin\verify-obj3.exe` | **Objective 3 Automated Hardware Verification Suite**. 7-point validation suite for physical GPU, watchdog, sandboxing, and simulated ONNX execution. |

**Live Deployment**: A production coordinator is hosted and live on Render at:
`wss://unicompute-coordinator.onrender.com/ws`

**Desktop GUI Launchers**:
- Batch Launcher: `.\run-gui.bat`
- PowerShell Launcher: `.\run-gui.ps1`
- Master Verification Suite: `.\test_all.ps1`

---

## 2. Prerequisites Installation on Windows 10/11

Ensure your computer has:
- Windows 10 (Build 19041+) or Windows 11 (64-bit).
- Hardware Virtualization enabled in BIOS/UEFI (Intel VT-x or AMD-V).
- At least 8 GB RAM and 15 GB available disk space.

Open **PowerShell as Administrator** for all installation commands below.

### 2.1 Git for Windows
Install Git using Windows Package Manager (`winget`):

```powershell
winget install --id Git.Git -e --source winget
```

*Alternative*: Download the official installer from [git-scm.com/download/win](https://git-scm.com/download/win) and accept default settings (enabling Git from the command prompt and Git Bash).

Verify installation:
```powershell
git --version
# Expected: git version 2.40+ or newer
```

### 2.2 Go Programming Language (1.22+)
Install Go using `winget`:

```powershell
winget install --id GoLang.Go -e --source winget
```

*Alternative*: Download the MSI installer from [go.dev/dl](https://go.dev/dl/).

Restart your PowerShell terminal to reload `$env:Path`, then verify:
```powershell
go version
# Expected: go version go1.22+ or go1.23+ windows/amd64
```

### 2.3 Docker Desktop with WSL2 Backend
Worker nodes execute untrusted user code in isolated Linux containers. Docker Desktop backed by Windows Subsystem for Linux (WSL2) is required.

#### Step 1: Install or Update WSL2
In Administrator PowerShell:
```powershell
wsl --install --no-distribution
wsl --update
wsl --status
```
*(If prompted, reboot your PC to finish enabling the Virtual Machine Platform feature.)*

#### Step 2: Install Docker Desktop
```powershell
winget install --id Docker.DockerDesktop -e --source winget
```
*Alternative*: Download installer from [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop/).

#### Step 3: Configure Linux Container Mode
1. Launch **Docker Desktop** from the Start Menu.
2. Complete the initial setup wizard, selecting **Use the WSL 2 based engine**.
3. Right-click the Docker whale icon in the Windows notification area (system tray).
   - If the menu shows **"Switch to Windows containers..."**, Docker is already in **Linux container mode** (Correct).
   - If it shows **"Switch to Linux containers..."**, click it to switch to Linux mode.
4. Verify Docker daemon access from PowerShell:
```powershell
docker version
```
Look for:
```text
Client: Docker Engine
 OS/Arch: windows/amd64
Server: Docker Engine - Community
 OS/Arch: linux/amd64
```

### 2.4 GPU Drivers & Acceleration Runtime (DirectML / CUDA)
For Objective 3 GPU acceleration and telemetry:
- **DirectX 12 / DirectML**: DirectML is pre-installed on Windows 10 (version 1903+) and Windows 11. It provides hardware acceleration across NVIDIA, AMD Radeon, and Intel Arc GPUs without requiring vendor-specific SDKs.
- **NVIDIA GPU (CUDA)**: Install the latest NVIDIA Game Ready or Studio Driver (v535+). Verify CUDA driver support:
  ```powershell
  nvidia-smi
  # Look for CUDA Version: 12.x or 11.x in the top right header
  ```
- **AMD Radeon (HIP/ROCm)**: For AMD devices on Windows, UniCompute uses DirectML as the primary hardware acceleration target, bypassing ROCm Linux-only driver constraints.

### 2.5 Browser Runtime for Desktop GUI Window (Edge / Chrome)
The Desktop GUI Control Room launches as a borderless native application window using:
- **Microsoft Edge** (installed by default on Windows 10/11: `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`).
- **Google Chrome** (alternative: `C:\Program Files\Google\Chrome\Application\chrome.exe`).
No external Electron or WebView2 runtime installation is required—the native browser app engine is used directly.

---

## 3. Windows Security, Code Signing & Smart App Control

Windows 11 introduces strict security protections including **Smart App Control (SAC)**, **Windows Defender SmartScreen**, and **Execution Policies** that require deliberate handling during local development.

### 3.1 PowerShell Execution Policy
By default, Windows blocks script execution (`.ps1`). Configure your session to allow running project scripts:

```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force
```
*Note*: `-Scope Process` affects only the current PowerShell window and leaves system-wide security settings intact.

### 3.2 Unblocking Downloaded Files
If the repository was downloaded as a `.zip` archive from GitHub via a web browser, Windows stamps files with an NTFS `Zone.Identifier` stream (Mark of the Web), causing SmartScreen to block them.

Unblock all files recursively:
```powershell
Get-ChildItem -Path . -Recurse | Unblock-File
```

### 3.3 Authenticode Code-Signing (`build.ps1`) & Smart App Control (SAC)
On Windows 11 with Smart App Control (SAC) enabled in "Enforce" mode, unsigned local executables or newly compiled binaries lacking cloud reputation may trigger:
```text
Program 'unicompute-node.exe' failed to run: An Application Control policy has blocked this file
```

The UniCompute `build.ps1` script automatically signs compiled binaries by:
1. Detecting or generating a dedicated self-signed code-signing certificate in `Cert:\CurrentUser\My`.
2. Applying an Authenticode signature (`Set-AuthenticodeSignature`) to all compiled `.exe` files in `bin\`.

To manually create a certificate and sign any custom binary:
```powershell
$cert = Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Select-Object -First 1
if (-not $cert) {
    $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject "CN=UniCompute Dev" -CertStoreLocation "Cert:\CurrentUser\My"
}
Set-AuthenticodeSignature -Certificate $cert -FilePath .\bin\unicompute-node.exe
```

### 3.4 Zero-Friction Developer Execution (`go run`)
If Windows 11 Smart App Control (SAC) blocks local standalone binaries because the self-signed root certificate is not in the system's trusted root store, you can run the UniCompute node directly using the Go toolchain:
```powershell
go run ./cmd/unicompute-node [arguments...]
```
Because `go.exe` is signed with established Microsoft reputation, running via `go run` executes without triggering SAC Application Control blocks.

### 3.5 Temporary Execution Directory (`GOTMPDIR`) & Unit Testing
When running `go test`, Go writes temporary test runner binaries to `%TEMP%` (e.g. `signaling.test.exe`). On Windows 11 with SAC, these unsigned temporary test binaries are blocked from running in `%TEMP%` or `.tmp`.

**Recommended Solution**: Always use the included `run_tests.ps1` verification script:
```powershell
powershell -ExecutionPolicy Bypass -File .\run_tests.ps1
```
`run_tests.ps1` compiles each package's test binary directly into `bin\`, signs it with an Authenticode certificate, and executes it cleanly.

---

## 4. Cloning the Repository & Building Binaries

Open a PowerShell terminal in your preferred directory (e.g. `C:\Projects`):

```powershell
git clone https://github.com/unicompute/obj1.git UniComputeOBJ1
cd UniComputeOBJ1
```

### 4.1 Dependency Check & Version Alignment
The module uses Go 1.22+. Run `go mod tidy` to verify all Pion WebRTC, WebSocket, and cryptographic dependencies:

```powershell
go mod tidy
```

### 4.2 Automated Build & Signing
Execute the automated PowerShell build script:

```powershell
powershell -ExecutionPolicy Bypass -File .\build.ps1
```

Output:
```text
Building UniCompute binaries...
Signing binaries for Windows Smart App Control / Code Integrity...

[SUCCESS] Binaries built and signed successfully in .\bin\
```

This compiles all five core executables:
- `bin\unicompute-node.exe` (Node daemon & worker)
- `bin\unicompute-coordinator.exe` (Signaling & TURN hub)
- `bin\unicompute-relay.exe` (Blind contributor relay)
- `bin\verify-obj3.exe` (Objective 3 hardware test suite)
- `bin\unicompute-gui.exe` (Desktop GUI control room)

### 4.3 Manual Compilation Alternative
If you prefer building each binary manually:

```powershell
New-Item -ItemType Directory -Force -Path "bin" | Out-Null
go build -o bin/unicompute-node.exe ./cmd/unicompute-node
go build -o bin/unicompute-coordinator.exe ./cmd/unicompute-coordinator
go build -o bin/unicompute-relay.exe ./cmd/unicompute-relay
go build -o bin/verify-obj3.exe ./cmd/verify-obj3
go build -o bin/unicompute-gui.exe ./cmd/unicompute-gui
```

Verify that the binaries exist in `.\bin\`:
```powershell
Get-ChildItem .\bin\*.exe
```

---

## 5. Running the Automated Master Verification Suite (`test_all.ps1`)

UniCompute provides a unified master test automation suite covering `go vet`, all testable Go packages, physical GPU hardware, 50 ms watchdog radar, standalone samples, GUI REST/WebSocket endpoints, and frontend JS syntax:

```powershell
powershell -ExecutionPolicy Bypass -File .\test_all.ps1
```

### What is verified (Across 5 Phases):
1. **Phase 1: Static Check (`go vet ./...`) & Subsystem Unit Tests (All Testable Go Packages)**:
   - **`cmd/unicompute-gui`**: REST/WebSocket auth, Origin checks, SSRF/redirect token hygiene, CAS lifecycle, HIPIFY/GPU, code run, and frontend static XSS/asset checks.
   - **`cmd/unicompute-node`**: Interactive CLI quoted argument and Windows/POSIX path parsing (`splitConsoleArgs`).
   - **`pkg/blobstore`**: Atomic chunk persistence, SHA-256 validation, quota enforcement, bit-rot scrub audit, traversal prevention.
   - **`pkg/codeexec`**: Bounded code request/response framing, 12 KiB source and 4 KiB stdin ceiling checks.
   - **`pkg/gpu`**: Hardware probe (NVIDIA RTX 4050 / DirectML / CUDA / NVML discovery).
   - **`pkg/gpu/hipify`**: Regex-based pre-flight compatibility analysis, 32-thread warp blocker detection, and CUDA-to-HIP symbol translation.
   - **`pkg/gpu/onnx`**: Pure-Go simulated ONNX runner (`Simulated: true`), checkpoint serialization, SHA-256 state hashing, interruptible preemption loop.
   - **`pkg/gpu/watchdog`**: 50 ms battery/user-activity preemption and sustained-idle resume state transitions.
   - **`pkg/instrumentation`**: In-memory `Tracker` collecting per-peer `PathMetrics` (direct vs relayed pair types, RTT, bytes sent/received) and `FleetNATStats` CGNAT prevalence aggregation.
   - **`pkg/nat`**: RFC 6598 CGNAT detection (`100.64.0.0/10`), dual-stack STUN probing, and Hyper-V/WSL adapter pruning.
   - **`pkg/relay`**: Standalone/legacy Syncthing relay-v1 end-to-end byte proxying and embedded TURN server lifecycle.
   - **`pkg/remoteshell`**: Windows PowerShell command execution, concurrency limits, SCTP wire-frame truncation, and timeout enforcement.
   - **`pkg/signaling`**: WebSocket registration with Ed25519 identity signature verification, keepalive tickers, 401 reconnect termination, and client message dispatch.
   - **`pkg/transport`**: WebRTC PeerConnection, 16 MiB SCTP receive buffer, TTL eviction, and dual DataChannels (`stream` and `chunk`).
   - **`pkg/node`**: End-to-end chunk exchange, remote code allowlist/limits, graceful job cancellation, and **Invariant CP-1 (Data Plane Independence)**.
2. **Phase 2: Objective 3 Physical Hardware & Governance Suite**:
   - `cmd/verify-obj3`: 7-point live test suite validating physical GPU detection, Win32 JobObject memory ceiling, 50 ms battery/idle watchdog preemption, and checkpoint rollback.
3. **Phase 3: Standalone Sample Executions (10 Files Individually Verified)**:
   - `samples/c/hello.c` (compiled via native MSYS2 GCC 16.1 -> FNV-1a hash verification).
   - `samples/cpp/fib.cpp` (compiled via MSYS2 G++ 16.1 -> 45 Fibonacci steps and Golden Ratio convergence).
   - `samples/python/calc.py` (executed via Python 3.11 with `samples/python/input.txt` -> 32 records processed).
   - `samples/security/timeout_test.py` (infinite loop bounded by runtime timeout cap).
   - `samples/gpu/portable_vector_add.cu` (standalone HIPIFY -> 7 replacements to portable HIP C++).
   - `samples/gpu/warp_reduction.cu` (standalone HIPIFY -> quarantined under `EXECUTE_NVIDIA_ONLY`).
   - `samples/gpu/inline_ptx.cu` (standalone HIPIFY -> quarantined under `REJECT_RECOMMEND_ONNX`).
   - `samples/gpu/test_model.json` (ONNX capsule parsing, weight loading & SHA-256 checkpoint seal).
   - `samples/security/network_leak_test.py` (air-gap socket probe protocol encapsulation).
   - `samples/security/mem_limit_test.py` (256 MB RAM capping audit protocol encapsulation).
4. **Phase 4: Desktop GUI Daemon, API & JS Syntax Verification (Port 23098)**:
   - Validates live `/api/status`, `/api/gpu`, `/api/samples`, `/api/gpu/hipify`, `/api/storage/put`, `/api/storage/scrub`, `/api/gpu/run`, `/api/shell/exec` (401 unauthenticated), `/api/coordinator/settings`, `/api/worker/settings`, Desktop Workstation GUI bundle serving (`GET /`), and `node --check` on active JS assets.
5. **Phase 5: Collation & Scorecard Generation**:
   - Outputs a clean terminal table summarizing all passed tests (38/38).

---

## 6. Pre-Pulling Docker Sandbox Images

Worker nodes execute code submitted by peers in unprivileged Docker containers. The worker pulls these images once so jobs execute instantly without download delay:

```powershell
docker pull gcc:14
docker pull python:3.13-slim
```

Verify that both images are present:
```powershell
docker images | Select-String "gcc|python"
```

---

## 7. Running a 2-Node Cluster on Windows

To experience the full peer-to-peer distributed system, open **two separate PowerShell windows**:
- **Window 1 (Worker Node B)**: Enables Docker sandbox code execution, contributes storage, and serves code execution requests.
- **Window 2 (Client Node A)**: Connects to Node B, transfers files into the CAS vault, issues shell commands, and submits jobs.

> [!NOTE]
> **Coordinator Architecture & Authentication**:
> - The Coordinator (`unicompute-coordinator` or the live Render cloud deployment) is a **headless signaling hub and embedded TURN server**. It intentionally has **no standalone web GUI**, as volunteer compute networks require zero overhead on the signaling plane.
> - **Cluster Authentication (`-auth-token`)**: When exposed on non-loopback addresses, the coordinator enforces cluster secret token authentication to prevent unauthorized signaling, node hijacking, and enumeration. Pass `-auth-token <secret>` or set `UNICOMPUTE_AUTH_TOKEN`. (For offline local testing without authentication, pass `-insecure-no-auth`).
> - **Verifying Coordinator Health via API**:
>   ```powershell
>   # Health & Uptime Check (Live Render):
>   curl.exe -s https://unicompute-coordinator.onrender.com/health
>   # Discovered Fleet Registry (add Bearer header if auth is enabled):
>   curl.exe -s -H "Authorization: Bearer <token>" https://unicompute-coordinator.onrender.com/api/nodes
>   # Local Coordinator:
>   curl.exe -s http://127.0.0.1:8080/health
>   curl.exe -s http://127.0.0.1:8080/api/nodes
>   ```
> - **Visualizing the Coordinator in the Desktop GUI**: The visual representation of the coordinator lives inside the **Desktop Workstation GUI** (`unicompute-gui.exe`). When you launch the GUI, the **Network Topology** sidebar view renders the coordinator, local workstation, and connected WebRTC peers in an interactive SVG P2P mesh.

### Option A: Public Live Coordinator on Render
Both nodes connect over the Internet via the production coordinator on Render (`wss://unicompute-coordinator.onrender.com/ws`).

#### Terminal 1 — Start Worker Node B
```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
# Method 1 (Compiled Binary):
# Enables Docker code execution sandbox for node-A AND allows remote shell execution for node-A
.\bin\unicompute-node.exe -id node-B -signaling wss://unicompute-coordinator.onrender.com/ws -enable-code-execution -code-allowlist node-A -code-timeout 15 -enable-remote-shell -shell-allowlist node-A

# Method 2 (Go Toolchain - Use if Windows SAC blocks standalone binary):
go run ./cmd/unicompute-node -id node-B -signaling wss://unicompute-coordinator.onrender.com/ws -enable-code-execution -code-allowlist node-A -code-timeout 15 -enable-remote-shell -shell-allowlist node-A
```

*Worker Node B logs output:*
```text
===================================================================
 UniCompute Objective 1: Peer-to-Peer Storage Substrate
 Node ID:        node-B
 Pool ID:        default-pool
 Storage Quota:  10 GiB
 Storage Path:   C:\Users\<User>\.unicompute\storage\node-B
 Signaling URL:  wss://unicompute-coordinator.onrender.com/ws
 STUN Server(s): stun.l.google.com:19302
===================================================================
[CODEEXEC] Docker sandbox enabled; allowed submitting peers: node-A
[INFO] Remote shell execution enabled for peers: [node-A]
[INFO] Starting node and classifying network topology...
[FLEET] Connected to coordinator. Waiting for other laptops to connect...
Interactive Mode Active! Type 'help' to see commands.
>
```

#### Terminal 2 — Start Client Node A
```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
# Method 1 (Compiled Binary):
.\bin\unicompute-node.exe -id node-A -signaling wss://unicompute-coordinator.onrender.com/ws

# Method 2 (Go Toolchain - Use if Windows SAC blocks standalone binary):
go run ./cmd/unicompute-node -id node-A -signaling wss://unicompute-coordinator.onrender.com/ws
```

*Both terminals will announce peer discovery:*
```text
[FLEET] Peer 'node-B' joined the fleet! (Type 'connect node-B' to establish P2P link)
```

---

### Option B: Local Coordinator on Localhost
If you are developing completely offline without Internet access, run the local coordinator.

#### Terminal 0 — Start Local Coordinator
```powershell
# Method 1 (Compiled Binary - with explicit auth token):
.\bin\unicompute-coordinator.exe -listen 0.0.0.0:8080 -auth-token mysecrettoken -insecure-no-auth=false

# Method 2 (Go Toolchain - local dev without auth):
go run ./cmd/unicompute-coordinator -listen 127.0.0.1:8080 -insecure-no-auth
```

#### Terminal 1 — Start Worker Node B
```powershell
# Method 1 (Compiled Binary):
.\bin\unicompute-node.exe -id node-B -signaling ws://127.0.0.1:8080/ws -auth-token mysecrettoken -enable-code-execution -code-allowlist node-A -code-timeout 15 -enable-remote-shell -shell-allowlist node-A

# Method 2 (Go Toolchain):
go run ./cmd/unicompute-node -id node-B -signaling ws://127.0.0.1:8080/ws -auth-token mysecrettoken -enable-code-execution -code-allowlist node-A -code-timeout 15 -enable-remote-shell -shell-allowlist node-A
```

#### Terminal 2 — Start Client Node A
```powershell
# Method 1 (Compiled Binary):
.\bin\unicompute-node.exe -id node-A -signaling ws://127.0.0.1:8080/ws -auth-token mysecrettoken

# Method 2 (Go Toolchain):
go run ./cmd/unicompute-node -id node-A -signaling ws://127.0.0.1:8080/ws -auth-token mysecrettoken
```

---

## 8. Comprehensive CLI Command Reference & Feature Walkthrough

From **Terminal 2 (Client Node A)**, execute each of the commands below.

### 8.1 WebRTC P2P Signaling & Path Telemetry

#### 1. List fleet nodes
```text
> peers
```
*Displays online nodes reported by the signaling hub and active WebRTC DataChannels.*

#### 2. Establish direct WebRTC P2P DataChannel link
```text
> connect node-B
```
*Output:*
```text
Establishing WebRTC P2P link with node-B...
[EVENT] Peer node-B connected via WebRTC DataChannel
[METRICS] Peer node-B path: direct=true, pair=host-host, local=192.168.1.15:58321, remote=192.168.1.20:59104
WebRTC DataChannel connected to node-B!
```

#### 3. SCTP Keepalive Ping
```text
> ping node-B
```
*Output:*
```text
Ping sent to node-B (ping ID: 1732304859)...
[PONG] Reply from node-B: Pong received! RTT = 4.2ms (ping ID: 1732304859)
```

#### 4. Live Stream Control Message
```text
> stream node-B Hello from Client Node A!
```
*Node B instantly receives and prints the stream message.*

#### 5. Inspect Path Telemetry & Fleet Metrics
```text
> metrics
```
*Shows total connections, direct vs relayed ratio, and CGNAT prevalence across the fleet.*

```text
> nat
```
*Displays local NAT classification (Full Cone, Restricted, Symmetric), public IP, and CGNAT status.*

---

### 8.2 Content-Addressed Storage (CAS) Vault

The CAS vault provides cryptographically verified, quota-enforced local storage.

#### 1. Store a local file in the vault (`put`)
```text
> put samples\python\input.txt
```
*Output:*
```text
Successfully stored blob: 66a2cbdb7d04a60155609462eec4c5fa33f3801ea558b8d960f5e1ef2ecbfcb6 (200 bytes)
```
*(Copy your returned SHA-256 hash for the next steps).*

#### 2. List stored blobs (`list`)
```text
> list
```
*Shows all blobs stored locally, their exact byte size, and modification timestamp.*

#### 3. Inspect blob metadata (`stat`)
```text
> stat <blobID>
```
*Shows size, modification timestamp, and verified SHA-256 digest.*

#### 4. Transfer blob over WebRTC DataChannel to Node B (`transfer`)
```text
> transfer node-B <blobID>
```
*Output on Node A:*
```text
Transferred blob 66a2cbdb... to node-B in 42ms
```
*Output on Node B:*
```text
[EVENT] Chunk 66a2cbdb... (200 bytes) received from peer node-A and stored
```

#### 5. Fetch blob from Node B (`fetch`)
Pulls a chunk from Node B over WebRTC DataChannel using the wire protocol `DataMsgChunkRequest` (0x10), writes it to the local CAS vault, and verifies its cryptographic SHA-256 hash:
```text
> fetch node-B <blobID>
```
*Output:*
```text
Fetching chunk 66a2cbdb... from peer node-B...
[EVENT] Chunk 66a2cbdb... fetched and stored successfully in 38ms (SHA-256 Verified).
```

#### 6. Export blob back to a local file (`get`)
```text
> get <blobID> exported_data.txt
```
*Exports the blob to `exported_data.txt` and verifies byte integrity.*

#### 7. Audit Vault for Bitrot (`scrub`)
```text
> scrub
```
*Output:*
```text
Scrub complete: all stored blobs passed cryptographic SHA-256 verification.
```

#### 8. Delete a blob from vault (`del`)
```text
> del <blobID>
```

---

### 8.3 Remote Terminal Execution (`exec`)

UniCompute provides secure host terminal command execution over WebRTC DataChannels using PowerShell on Windows targets.

> [!IMPORTANT]
> **Security Gate — Opt-In & Peer Allowlist Required**:
> Remote shell execution is **disabled by default** to prevent unauthorized command execution by connected peers.
> - The target node (Worker Node B) **must** be launched with:
>   `-enable-remote-shell -shell-allowlist <peerID>[,<peerID>]` (or `-shell-allowlist "*"`).
> - If remote shell is disabled or the calling peer is not allowlisted, the request is immediately rejected with:
>   `remote shell disabled on this node` or `peer not in shell allowlist`.

#### 1. Execute command on Worker Node B
```text
> exec node-B hostname
> exec node-B whoami
> exec node-B Get-Process
```
*Output:*
```text
[EXEC] Executing 'hostname' on remote PC 'node-B'...
=================================================================
 Remote Node: node-B | Exit Code: 0 | Duration: 45 ms
 --- STDOUT ---
 DESKTOP-WORKER-B
=================================================================
```

#### 2. Broadcast command to all connected peers
```text
> exec all Get-Date
```

---

### 8.4 Air-Gapped Docker Sandbox Code Execution (`run`)

The `run` command reads a local source file on the client machine, packages it into a bounded job envelope, and sends it over the WebRTC `stream` DataChannel to the worker. The worker executes the code in an isolated Docker container with:
- **`--network none`**: Disables network access (air-gapped).
- **`--read-only`**: Read-only container root filesystem.
- **`--memory 256m --memory-swap 256m`**: 256 MB RAM ceiling.
- **`--cpus 1.0`**: CPU capped at 1 core.
- **`--pids-limit 64`**: Fork-bomb mitigation.
- **`--ipc=none`**: Isolates POSIX IPC namespaces.
- **`--cap-drop ALL`**: Drops all Linux kernel capabilities.
- **`--security-opt no-new-privileges:true`**: Prevents privilege escalation.
- **`--tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m`**: Ephemeral memory-only scratch space (`noexec` for Python; `exec` enabled only for C/C++ compilation).

---

### 8.5 Objective 3: GPU Acceleration & Resource Governance

Objective 3 adds hardware acceleration discovery and background student governance. When running `unicompute-node.exe`, activate GPU support using:

```powershell
.\bin\unicompute-node.exe -id node-A -signaling wss://unicompute-coordinator.onrender.com/ws -enable-gpu -gpu-backend auto -gpu-mem-limit-mb 2048 -gpu-watchdog-poll-ms 50
```

#### Node Flags for GPU Subsystem:
| Flag | Default | Description |
|---|---|---|
| `-enable-gpu` | `false` | Activates DXGI/NVML/DirectML/CUDA hardware detection, 50 ms watchdog, and Win32 JobObject sandbox. |
| `-gpu-backend` | `auto` | Preferred compute backend: `auto`, `directml`, `cuda`. |
| `-gpu-mem-limit-mb` | `2048` | Hard commit memory limit enforced via Win32 JobObject. |
| `-gpu-watchdog-poll-ms` | `50` | Watchdog polling interval for AC power and student keyboard/mouse idle monitoring. |
| `-gpu-ignore-battery` | `false` | Ignore battery state (useful for lab machines or desktop test rigs). |

#### Interactive CLI Commands for GPU:
1. **`gpu`**: Probes and displays real-time hardware telemetry and watchdog radar:
   ```text
   > gpu
   ======================= Objective 3: GPU Hardware & Governance =======================
    Device Name:        NVIDIA GeForce RTX 4050 Laptop GPU
    Vendor:             NVIDIA
    Total VRAM:         6141 MB (6.00 GB)
    Available VRAM:     5920 MB
    Driver Version:     32.0.15.9282
    GPU Temperature:    45.0 °C | Power: 7.9 W
    DirectML Support:   true (DirectX 12 feature level verified)
    CUDA Support:       true
    Backends Detected:  [CPU DirectML CUDA]
   --------------------------------- 50 ms Watchdog State ---------------------------------
    Watchdog Status:    running
    Power Source:       OnBattery=false (100% remaining)
    User Activity:      Active=false (Idle: 12m40s | Sustained Idle: 12m40s)
   =======================================================================================
   ```

2. **`hipify <file.cu>`**: Analyzes CUDA kernels for 32-thread warp assumptions (`threadIdx.x % 32`, `__shfl_down_sync`), inline PTX assembly, and translates portable kernels to HIP C++:
   ```text
   > hipify samples\gpu\portable_vector_add.cu
   [SUCCESS] Kernel successfully translated to portable HIP C++ (7 replacements made).
   ```
   If warp blockers are detected (e.g. `warp_reduction.cu`):
   ```text
   > hipify samples\gpu\warp_reduction.cu
   [BLOCKED] cannot safely translate to HIP: 2 fatal compatibility issue(s) detected
    * Line 4: Hardcoded modulo 32 indexing assumes 32-thread warp size.
    * Line 9: NVIDIA 32-thread warp synchronous intrinsic does not map to AMD 64-thread wavefronts.
   ```

3. **`gpu-run <c|cpp|onnx> <file> [batches]`**: Executes local simulated GPU workloads (`Simulated: true`) using the pure-Go ONNX runner and CUDA pre-flight analyzer.
   *(Note: The pure-Go ONNX runner executes in-process and is explicitly labeled `Simulated: true` to validate checkpoint serialization, preemption loops, and SHA-256 state hashing without external CGO/ONNX Runtime C-bindings. Win32 `JobObjectSandbox` applies to external child PIDs assigned via `AssignProcess`, as verified in `cmd/verify-obj3`.)*

---

### 9. Sample Programs & Security Boundary Tests

The repository includes ready-to-use sample programs in the `samples\` directory. You can use the existing files directly or create them using the PowerShell commands below.

### 9.1 Python Data Processing with Stdin (`calc.py` & `input.txt`)
Reads numbers from standard input, calculates mean, standard deviation, median, min, and max, and outputs descriptive statistics.

#### Creation Commands:
```powershell
New-Item -ItemType Directory -Force -Path "samples\python" | Out-Null

Set-Content -Path "samples\python\input.txt" -Value @'
128.50 94.20 15.00 482.10 39.40 88.00 12.34 500.00
73.15 62.40 81.90 190.25 45.60 99.10 312.80 5.40
23.70 87.65 142.10 67.89 44.55 98.70 215.30 63.20
11.45 78.90 34.50 156.70 89.20 45.10 120.00 67.50
'@

Set-Content -Path "samples\python\calc.py" -Value @'
#!/usr/bin/env python3
import sys
import math

def process_stream():
    numbers = []
    print("==================================================")
    print("   UniCompute Worker: Python Data Processing Job  ")
    print("==================================================")
    raw_input = sys.stdin.read()
    if not raw_input.strip():
        print("[WARN] No input received on stdin. Generating default test data.")
        numbers = [42.0, 17.5, 88.2, 9.1, 104.6, 55.0, 31.4, 76.9]
    else:
        for token in raw_input.split():
            try:
                numbers.append(float(token))
            except ValueError:
                pass

    if not numbers:
        print("[ERROR] No valid numbers found in input stream.")
        return

    n = len(numbers)
    total_sum = sum(numbers)
    mean = total_sum / n
    variance = sum((x - mean) ** 2 for x in numbers) / n
    std_dev = math.sqrt(variance)
    min_val = min(numbers)
    max_val = max(numbers)
    sorted_nums = sorted(numbers)
    median = sorted_nums[n // 2] if n % 2 != 0 else (sorted_nums[n // 2 - 1] + sorted_nums[n // 2]) / 2.0

    print(f"Records Processed : {n}")
    print(f"Sum               : {total_sum:.4f}")
    print(f"Mean (Average)    : {mean:.4f}")
    print(f"Median            : {median:.4f}")
    print(f"Standard Dev      : {std_dev:.4f}")
    print(f"Minimum Value     : {min_val:.4f}")
    print(f"Maximum Value     : {max_val:.4f}")
    print(f"Sorted First 5    : {sorted_nums[:5]}")
    print(f"Sorted Last 5     : {sorted_nums[-5:]}")
    print("==================================================")
    print("Status: Calculation completed successfully.")

if __name__ == "__main__":
    process_stream()
'@
```

#### Run from Client Node A:
```text
> run node-B python samples\python\calc.py samples\python\input.txt
```

Expected Output:
```text
[RUN] Submitting python job job-7f31... to node-B...
=================================================================
 Job ID: job-7f31... | Worker: node-B | Language: python
 Status: Completed
 --- STDOUT ---
==================================================
   UniCompute Worker: Python Data Processing Job  
==================================================
Records Processed : 32
Sum               : 3576.5800
Mean (Average)    : 111.7681
Median            : 80.4000
Standard Dev      : 116.7023
Minimum Value     : 5.4000
Maximum Value     : 500.0000
Sorted First 5    : [5.4, 11.45, 12.34, 15.0, 23.7]
Sorted Last 5     : [190.25, 215.3, 312.8, 482.1, 500.0]
==================================================
Status: Calculation completed successfully.
 Exit code: 0 | Worker execution: 382 ms | Round trip: 412 ms
=================================================================
```

---

### 9.2 C++ High-Precision Computation (`fib.cpp`)
Compiles with GCC 14 (`g++ -std=c++17 -O1 -pipe`) and computes the first 45 Fibonacci numbers and Golden Ratio convergence.

#### Creation Command:
```powershell
New-Item -ItemType Directory -Force -Path "samples\cpp" | Out-Null

Set-Content -Path "samples\cpp\fib.cpp" -Value @'
#include <iostream>
#include <vector>
#include <iomanip>
#include <chrono>

void computeFibonacci(int count) {
    std::cout << "==================================================" << std::endl;
    std::cout << "    UniCompute Worker: C++ Fibonacci Benchmark    " << std::endl;
    std::cout << "==================================================" << std::endl;

    if (count <= 0) return;
    
    std::vector<unsigned long long> fib;
    fib.reserve(count);
    fib.push_back(0);
    if (count > 1) fib.push_back(1);

    auto start = std::chrono::high_resolution_clock::now();

    for (int i = 2; i < count; ++i) {
        fib.push_back(fib[i - 1] + fib[i - 2]);
    }

    auto elapsed = std::chrono::duration_cast<std::chrono::microseconds>(
        std::chrono::high_resolution_clock::now() - start
    ).count();

    std::cout << "First " << count << " Fibonacci numbers computed in " << elapsed << " microseconds:\n\n";
    for (int i = 0; i < count; ++i) {
        std::cout << std::setw(3) << i << ": " << std::setw(20) << fib[i];
        if (i > 1 && fib[i - 1] > 0) {
            double ratio = static_cast<double>(fib[i]) / static_cast<double>(fib[i - 1]);
            std::cout << "  (Ratio: " << std::fixed << std::setprecision(8) << ratio << ")";
        }
        std::cout << "\n";
    }

    std::cout << "==================================================" << std::endl;
    std::cout << "Approaching Golden Ratio (phi): 1.6180339887..." << std::endl;
    std::cout << "Status: C++ computation completed successfully." << std::endl;
}

int main() {
    computeFibonacci(45);
    return 0;
}
'@
```

#### Run from Client Node A:
```text
> run node-B cpp samples\cpp\fib.cpp
```

Expected Output:
```text
=================================================================
 Job ID: job-9a1b... | Worker: node-B | Language: cpp
 Status: Completed
 --- STDOUT ---
==================================================
    UniCompute Worker: C++ Fibonacci Benchmark    
==================================================
First 45 Fibonacci numbers computed in 1 microseconds:

  0:                    0
  1:                    1
  2:                    1  (Ratio: 1.00000000)
  3:                    2  (Ratio: 2.00000000)
...
 44:            701408733  (Ratio: 1.61803399)
==================================================
Approaching Golden Ratio (phi): 1.6180339887...
Status: C++ computation completed successfully.
 Exit code: 0 | Worker execution: 740 ms | Round trip: 765 ms
=================================================================
```

---

### 9.3 C ISO C17 Runtime Benchmark (`hello.c`)
Compiles with GCC 14 (`gcc -std=c17 -O1 -pipe`) and calculates 32-bit FNV-1a cryptographic hashes of UniCompute system strings.

#### Creation Command:
```powershell
New-Item -ItemType Directory -Force -Path "samples\c" | Out-Null

Set-Content -Path "samples\c\hello.c" -Value @'
#include <stdio.h>
#include <stdint.h>
#include <string.h>

uint32_t fnv1a_32(const char* s) {
    uint32_t hash = 2166136261u;
    while (*s) {
        hash ^= (uint8_t)(*s++);
        hash *= 16777619u;
    }
    return hash;
}

int main(void) {
    const char* messages[] = {
        "UniCompute Objective 1: P2P Transport Substrate",
        "Dual-Stack ICE & CGNAT Hole-Punching Subsystem",
        "Content-Addressed CAS Blob Store Vault",
        "Air-Gapped Docker Container Code Sandbox",
        "WebRTC Bidirectional DataChannel Multiplexing"
    };
    int count = sizeof(messages) / sizeof(messages[0]);

    printf("==================================================\n");
    printf("     UniCompute Worker: C Runtime Verification    \n");
    printf("==================================================\n");
    printf("C Standard : ISO C17 (GCC 14)\n");
    printf("Target OS  : Linux Container (x86_64 / arm64)\n");
    printf("--------------------------------------------------\n");

    for (int i = 0; i < count; i++) {
        uint32_t hash = fnv1a_32(messages[i]);
        printf("[%d] %-48s | FNV-1a: 0x%08X\n", i + 1, messages[i], hash);
    }

    printf("==================================================\n");
    printf("Status: C worker execution passed.\n");
    return 0;
}
'@
```

#### Run from Client Node A:
```text
> run node-B c samples\c\hello.c
```

Expected Output:
```text
=================================================================
 Job ID: job-4c8d... | Worker: node-B | Language: c
 Status: Completed
 --- STDOUT ---
==================================================
     UniCompute Worker: C Runtime Verification    
==================================================
C Standard : ISO C17 (GCC 14)
Target OS  : Linux Container (x86_64 / arm64)
--------------------------------------------------
[1] UniCompute Objective 1: P2P Transport Substrate  | FNV-1a: 0x2A638BC4
[2] Dual-Stack ICE & CGNAT Hole-Punching Subsystem   | FNV-1a: 0x22BF5A74
[3] Content-Addressed CAS Blob Store Vault           | FNV-1a: 0x0FC7CB43
[4] Air-Gapped Docker Container Code Sandbox         | FNV-1a: 0xC88613A8
[5] WebRTC Bidirectional DataChannel Multiplexing    | FNV-1a: 0x3AB8CFEC
==================================================
Status: C worker execution passed.
 Exit code: 0 | Worker execution: 480 ms | Round trip: 510 ms
=================================================================
```

---

### 9.4 Security Test: Execution Timeout Enforcement (`timeout_test.py`)
Demonstrates that the worker automatically forcibly terminates a runaway container when it exceeds the configured `-code-timeout` (e.g. 15 seconds).

#### Creation Command:
```powershell
New-Item -ItemType Directory -Force -Path "samples\security" | Out-Null

Set-Content -Path "samples\security\timeout_test.py" -Value @'
#!/usr/bin/env python3
import time
import sys

print("[SECURITY TEST] Starting infinite compute/sleep loop...", flush=True)
print("[SECURITY TEST] Worker policy must terminate container after timeout.", flush=True)

elapsed = 0
while True:
    time.sleep(1)
    elapsed += 1
    print(f"[SECURITY TEST] Elapsed: {elapsed} seconds (still running)...", flush=True)
'@
```

#### Run from Client Node A:
```text
> run node-B python samples\security\timeout_test.py
```

Expected Output:
```text
[RUN] Submitting python job job-e21b... to node-B...
=================================================================
 Job ID: job-e21b... | Worker: node-B | Language: python
 Status: Timed out
 Error: execution exceeded 15s timeout
 --- STDOUT ---
[SECURITY TEST] Starting infinite compute/sleep loop...
[SECURITY TEST] Worker policy must terminate container after timeout.
[SECURITY TEST] Elapsed: 1 seconds (still running)...
[SECURITY TEST] Elapsed: 2 seconds (still running)...
...
 Exit code: -1 | Worker execution: 15008 ms | Round trip: 15042 ms
=================================================================
```

---

### 9.5 Security Test: Air-Gap Network Leak Block (`network_leak_test.py`)
Verifies that the container is strictly air-gapped with `--network none`. Attempts outbound TCP socket connections to `1.1.1.1:80`, DNS hostname lookups, and HTTP requests to `http://example.com`.

#### Creation Command:
```powershell
Set-Content -Path "samples\security\network_leak_test.py" -Value @'
#!/usr/bin/env python3
import socket
import sys
import urllib.request
import urllib.error

print("==================================================")
print("  UniCompute Security: Network Air-Gap Audit     ")
print("==================================================")

passed = True

# Test 1: Raw TCP socket connect to 1.1.1.1:80
print("[TEST 1] Probing outbound TCP socket to 1.1.1.1:80 ...")
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(2.0)
try:
    s.connect(("1.1.1.1", 80))
    print("[FAIL] CRITICAL SECURITY ISSUE: Outbound socket connection succeeded!")
    passed = False
except (OSError, socket.error) as e:
    print(f"[PASS] Socket connection blocked as expected: {e}")
finally:
    s.close()

# Test 2: Outbound DNS resolution for public hostname
print("[TEST 2] Probing outbound DNS resolution for example.com ...")
try:
    resolved_ip = socket.gethostbyname("example.com")
    print(f"[FAIL] CRITICAL SECURITY ISSUE: DNS resolution succeeded (resolved to {resolved_ip})!")
    passed = False
except (socket.gaierror, OSError) as e:
    print(f"[PASS] DNS resolution blocked as expected: {e}")

# Test 3: HTTP request attempt via urllib
print("[TEST 3] Probing outbound HTTP request to http://example.com ...")
try:
    with urllib.request.urlopen("http://example.com", timeout=2.0) as resp:
        print("[FAIL] CRITICAL SECURITY ISSUE: HTTP request succeeded!")
        passed = False
except Exception as e:
    print(f"[PASS] HTTP request blocked as expected: {type(e).__name__}: {e}")

print("==================================================")
if passed:
    print("RESULT: AIR-GAP VERIFICATION PASSED. Container is completely isolated.")
    sys.exit(0)
else:
    print("RESULT: AIR-GAP VERIFICATION FAILED. Container leaked network packets.")
    sys.exit(1)
'@
```

#### Run from Client Node A:
```text
> run node-B python samples\security\network_leak_test.py
```

Expected Output:
```text
=================================================================
 Job ID: job-aa42... | Worker: node-B | Language: python
 Status: Completed
 --- STDOUT ---
==================================================
  UniCompute Security: Network Air-Gap Audit     
==================================================
[TEST 1] Probing outbound TCP socket to 1.1.1.1:80 ...
[PASS] Socket connection blocked as expected: [Errno 101] Network is unreachable
[TEST 2] Probing outbound DNS resolution for example.com ...
[PASS] DNS resolution blocked as expected: [Errno -3] Temporary failure in name resolution
[TEST 3] Probing outbound HTTP request to http://example.com ...
[PASS] HTTP request blocked as expected: URLError: <urlopen error [Errno 101] Network is unreachable>
==================================================
RESULT: AIR-GAP VERIFICATION PASSED. Container is completely isolated.
 Exit code: 0 | Worker execution: 395 ms | Round trip: 420 ms
=================================================================
```

---

### 9.6 Security Test: 256 MB RAM Limit Enforcement (`mem_limit_test.py`)
Verifies that the Docker container enforces `--memory 256m --memory-swap 256m`. Allocates memory in 10 MB increments. When memory approaches 256 MB, the Linux kernel OOM killer terminates the container with exit code 137.

#### Creation Command:
```powershell
Set-Content -Path "samples\security\mem_limit_test.py" -Value @'
#!/usr/bin/env python3
import sys
import time

print("==================================================")
print("  UniCompute Security: 256 MB RAM Limit Audit     ")
print("==================================================")
print("Allocating memory in 10 MB chunks...")

chunks = []
total_mb = 0

try:
    for step in range(1, 30):
        chunk = bytearray(10 * 1024 * 1024)
        for i in range(0, len(chunk), 4096):
            chunk[i] = 0xFF
        chunks.append(chunk)
        total_mb += 10
        print(f"[ALLOCATION] Successfully allocated {total_mb} MB RAM", flush=True)
        time.sleep(0.05)

    print(f"[FAIL] Allocated {total_mb} MB without hitting the 256 MB container cap!")
    sys.exit(1)

except MemoryError:
    print(f"\n[PASS] Python raised MemoryError after allocating {total_mb} MB.")
    print("RESULT: MEMORY CAPPING ENFORCED SUCCESSFULLY.")
    sys.exit(0)
'@
```

#### Run from Client Node A:
```text
> run node-B python samples\security\mem_limit_test.py
```

Expected Output:
```text
=================================================================
 Job ID: job-ff91... | Worker: node-B | Language: python
 Status: Failed
 --- STDOUT ---
==================================================
  UniCompute Security: 256 MB RAM Limit Audit     
==================================================
Allocating memory in 10 MB chunks...
[ALLOCATION] Successfully allocated 10 MB RAM
[ALLOCATION] Successfully allocated 20 MB RAM
...
[ALLOCATION] Successfully allocated 240 MB RAM
 Exit code: 137 | Worker execution: 610 ms | Round trip: 635 ms
=================================================================
```
*(Exit code 137 = 128 + 9 = SIGKILL by the Linux OOM Killer, proving the container was strictly bounded at 256 MB).*

---

### 9.7 GPU Sample: Portable Vector Addition (`portable_vector_add.cu`)
Demonstrates clean CUDA code that maps cleanly to AMD HIP C++ without hardware incompatibilities.

**File**: `samples\gpu\portable_vector_add.cu`
```cuda
#include <cuda_runtime.h>
#include <stdio.h>

__global__ void vectorAdd(const float *a, const float *b, float *c, int n) {
    int i = blockDim.x * blockIdx.x + threadIdx.x;
    if (i < n) c[i] = a[i] + b[i];
}

int main() {
    int n = 1024; size_t size = n * sizeof(float);
    float *d_a, *d_b, *d_c;
    cudaMalloc(&d_a, size); cudaMalloc(&d_b, size); cudaMalloc(&d_c, size);
    cudaFree(d_a); cudaFree(d_b); cudaFree(d_c);
    return 0;
}
```

Verify translation with HIPIFY:
```powershell
go run ./cmd/unicompute-node -id test -signaling ""
# In CLI:
> hipify samples\gpu\portable_vector_add.cu
# Result: 7 replacements made (cudaMalloc -> hipMalloc, cudaFree -> hipFree, hip/hip_runtime.h)
```

---

### 9.8 GPU Sample: Warp Reduction Blocker (`warp_reduction.cu`)
Demonstrates architectural blockers that assume a 32-thread warp size, preventing execution on AMD 64-thread wavefronts.

**File**: `samples\gpu\warp_reduction.cu`
```cuda
#include <cuda_runtime.h>

__global__ void warpReduction(float *d_out, float *d_in) {
    int lane = threadIdx.x % 32; // Blocker: modulo 32 assumes 32-thread warp
    float val = d_in[threadIdx.x];
    for (int offset = 16; offset > 0; offset /= 2) {
        val += __shfl_down_sync(0xffffffff, val, offset); // Blocker: 32-thread warp sync intrinsic
    }
    if (lane == 0) d_out[blockIdx.x] = val;
}
```

Verify blocker detection:
```text
> hipify samples\gpu\warp_reduction.cu
[BLOCKED] cannot safely translate to HIP: 2 fatal compatibility issue(s) detected
 * Line 4: Hardcoded modulo 32 indexing assumes 32-thread warp size.
 * Line 9: NVIDIA 32-thread warp synchronous intrinsic does not map to AMD 64-thread wavefronts.
Action: EXECUTE_NVIDIA_ONLY
```

---

### 9.9 GPU Sample: Inline PTX Assembly Blocker (`inline_ptx.cu`)
Demonstrates assembly-level NVIDIA hardware locking (`asm volatile("mov.u32 %0, %smid;" ...)`).

**File**: `samples\gpu\inline_ptx.cu`
```text
> hipify samples\gpu\inline_ptx.cu
[BLOCKED] cannot safely translate to HIP: 1 fatal compatibility issue(s) detected
 * Line 6: NVIDIA Parallel Thread Execution (PTX) assembly cannot be compiled by AMD ROCm/HIP.
Action: REJECT_RECOMMEND_ONNX
```

---

### 9.10 GPU Sample: Model Capsule & Checkpoint (`test_model.json`)
A portable neural network definition with pre-initialized weight tensors and bias terms:

```json
{
  "model_name": "toy_neural_net",
  "layer_type": "Dense",
  "input_shape": [1, 4],
  "weights": [0.25, -0.5, 0.75, 1.0],
  "bias": [0.1]
}
```

---

### 9.11 Standalone Automated Sample Runner (`cmd/test-samples`)
You can run automated programmatic checks across sample files in one command:

```powershell
go run ./cmd/test-samples
```

---

### 9.12 Python Matrix Multiplication Benchmark (`matrix_multiply.py`)
Computes floating-point matrix multiplication with verification checksums and optional stdin dimension override.

**File**: `samples\python\matrix_multiply.py`
```powershell
# Run from Client Node A or in GUI:
> run node-B python samples\python\matrix_multiply.py
```
*Expected Output:*
```text
==================================================
 UniCompute Worker: Matrix Multiplication (O2)   
==================================================
[INFO] Using default dimension: 64x64
Allocating two 64x64 floating-point matrices...
Matrix Dimension : 64 x 64
Total Operations : 524,288 FLOPs
Compute Duration : 12.40 ms
Status: Matrix multiplication completed successfully.
```

---

### 9.13 C Memory & Hash Benchmark (`system_info.c`)
Tests heap memory allocation, bitwise manipulation, and Murmur3-32 hashing over a 64K integer buffer.

**File**: `samples\c\system_info.c`
```powershell
# Run from Client Node A or in GUI:
> run node-B c samples\c\system_info.c
```
*Expected Output:*
```text
==================================================
   UniCompute Worker: C Memory & Hash Benchmark   
==================================================
Word Size     : 64 bits (8 bytes)
Allocating    : 262144 bytes (256.00 KiB) heap buffer...
Murmur3 Check : 0x7A14CF9B
Status: C worker execution passed.
```

---

### 9.14 GPU Sample: Shared Memory Matrix Transpose (`matrix_transpose.cu`)
Demonstrates a 2D shared memory tile transpose avoiding shared memory bank conflicts (`tile[TILE_DIM][TILE_DIM + 1]`). Translates seamlessly to AMD HIP.

**File**: `samples\gpu\matrix_transpose.cu`
```powershell
# Analyze in CLI or GUI:
> hipify samples\gpu\matrix_transpose.cu
[SUCCESS] Kernel successfully translated to portable HIP C++.
```

---

### 9.15 Adding Custom Samples Dynamically
The UniCompute daemon includes a recursive sample discovery engine that automatically indexes user scripts:
1. Simply drop any `.py`, `.c`, `.cpp`, or `.cu` file into `samples\` or any subfolder (e.g., `samples\my_benchmark\test.py`).
2. The daemon immediately scans the folder hierarchy via `/api/samples`.
3. In the Desktop GUI, open the **CPU Sandbox & Shell** or **GPU Compute** view — your new sample will be automatically available in the sample dropdown list without restarting the application or rebuilding.

---

## 10. Desktop GUI Control Room Operation

UniCompute features a dedicated desktop workstation interface with a responsive Tailwind sidebar layout, real-time hardware telemetry, and zero mock data.

### 10.1 Launching the Desktop Application

Launch the desktop workstation using one of the following methods:

#### Method 1: Desktop Batch Launcher
```powershell
.\run-gui.bat
```

#### Method 2: PowerShell Launcher
```powershell
.\run-gui.ps1
```

#### Method 3: Direct Go Toolchain / Binary Execution
```powershell
# Standalone binary:
.\bin\unicompute-gui.exe -id node-desktop -signaling wss://unicompute-coordinator.onrender.com/ws -port 23080

# Or via Go run:
go run ./cmd/unicompute-gui -id node-desktop -signaling wss://unicompute-coordinator.onrender.com/ws -port 23080
```
This boots the local HTTP/WebSocket daemon and launches a native borderless application window using Microsoft Edge or Google Chrome (`msedge.exe --app="http://127.0.0.1:23080/#token=<sessionToken>"`).

> [!NOTE]
> **GUI Daemon Security Model**:
> - At startup, `unicompute-gui` generates a 32-byte cryptographically random session token stored in memory and passed into the desktop app via `#token=<token>` URL fragment.
> - The embedded UI frontend injects `Authorization: Bearer <token>` on all API requests and `?token=<token>` on WebSocket event streams.
> - Mutating `/api/*` routes enforce strict loopback Origin validation (`http://127.0.0.1:<port>` or `http://localhost:<port>`), Bearer token validation, and `application/json` Content-Type requirements.
> - Non-loopback `-listen` binds (e.g. `0.0.0.0`) are refused unless `-allow-remote-gui` is explicitly supplied.

---

### 10.2 Sidebar Workstation Views

The GUI organizes Coordinator, Worker, and System controls in a left navigation sidebar:

| Section | View | Primary Capabilities |
|---|---|---|
| **Coordinator** | **Overview** | Cluster status pill, node/NAT telemetry, storage usage, and quick task creation. |
| **Coordinator** | **Network Topology** | Interactive SVG P2P mesh showing the coordinator, local workstation, connected WebRTC peers, and live RTT/ping controls. |
| **Coordinator** | **CPU Sandbox & Shell** | Multi-language editor (Python, C, C++), recursive sample loader, stdin stream injection, execution timeout slider, target peer selection (`Local Worker` default), and remote shell console. |
| **Coordinator** | **GPU Compute** | Live DXGI/DirectML hardware telemetry, 50 ms AC power/idle watchdog radar, CUDA-to-AMD HIPIFY analyzer/translator (`hipify-clang` + built-in fallback), and pure-Go Simulated ONNX Runner (`simulated: true`). |
| **Coordinator** | **Task Queue** | Dynamic task queue tracking CPU and GPU jobs across the fleet (initialized empty with 0 fake seed tasks). |
| **Coordinator** | **Storage & Blobs** | Content-addressed storage vault (`local-vault-cas-blobs`), SHA-256 verification modal, blob deletion, and Bit-Rot Integrity Scrub audit. |
| **Worker** | **Worker Allowlist** | Live management of `code_allowlist` peer IDs and wildcard `*` permissions. |
| **Worker** | **Nodes** | Fleet peer discovery, P2P connect/disconnect controls, and path telemetry. |
| **System** | **Logs / Settings / Architecture** | Real-time `/api/events` WebSocket log feed, coordinator URL / `UNICOMPUTE_AUTH_TOKEN` settings, and Objective 1–3 architecture summary. |

---

### 10.3 Walkthrough: Standalone Single-Node Testing (No External Peer Needed)

You can verify all capabilities on a single machine without spinning up multiple terminals or external nodes:

1. **Launch the Workstation**:
   Run `.\run-gui.bat` and wait for the application window to open.
2. **Verify Overview & Settings**:
   - In **Overview** and **Settings**, observe live node ID, NAT classification, storage usage, and coordinator connection state.
3. **Test Objective 1 (Storage & Blobs)**:
   - Open **Storage & Blobs** in the sidebar.
   - Click **Scrub** to audit local CAS blobs via `/api/storage/scrub` using SHA-256 verification.
   - Stash a test string (`UniCompute Sovereign Sharded Storage Test`), confirm the SHA-256 blob ID appears in the table, and inspect its verification status.
4. **Test Objective 3 (GPU Compute & HIPIFY)**:
   - Open **GPU Compute** in the sidebar.
   - Observe real-time GPU telemetry (device name, VRAM, DirectML/CUDA support) and the 50 ms Watchdog status.
   - In the HIPIFY analyzer, select `portable_vector_add.cu` and click **Analyze & Translate to HIP**: verify CUDA calls translate (`cudaMalloc` -> `hipMalloc`) with a portable verdict.
   - Run a simulated ONNX batch and verify a task is recorded.
5. **Test Objective 2 (CPU Sandbox & Task Queue)**:
   - Open **CPU Sandbox & Shell** in the sidebar.
   - Select `matrix_multiply.py (python)`, enter `48` in stdin, and run it against `Local Worker`.
   - Open **Task Queue** to confirm the dispatched CPU and GPU jobs are recorded dynamically.
6. **Test Logs**:
   - Open **Logs** in the sidebar and verify that previous actions produced live WebSocket event entries.

---

### 10.4 Walkthrough: Two-Node Distributed Cluster Testing

To test real WebRTC P2P DataChannels, remote container execution, and live network metrics across two nodes:

1. **Terminal 1 — Start Remote Worker Node B (CLI)**:
   ```powershell
   go run ./cmd/unicompute-node -id node-B -signaling wss://unicompute-coordinator.onrender.com/ws -enable-code-execution -code-allowlist node-desktop -enable-remote-shell -shell-allowlist node-desktop
   ```
2. **Terminal 2 — Start Desktop Workstation Node A (GUI)**:
   ```powershell
   go run ./cmd/unicompute-gui -id node-desktop -signaling wss://unicompute-coordinator.onrender.com/ws -port 23080
   ```
3. **P2P Connection Workflows (Automatic vs. Manual)**:
   - **Workflow A: Automatic On-Demand Connection (Recommended)**: In **CPU Sandbox & Shell**, choose `node-B` in the **Target Peer** selector and run code or shell commands. The GUI automatically establishes the direct WebRTC DataChannel via the coordinator before executing the job.
   - **Workflow B: Manual Interactive Connection**: Open **Network Topology** (or **Nodes**), select `node-B`, click **Connect**, and click **Ping** to measure SCTP round-trip latency and inspect connection telemetry.
4. **Execute Remote Code & Shell over WebRTC DataChannel**:
   - In **CPU Sandbox & Shell**, with `node-B` selected, load `calc.py` and run it inside `node-B`'s air-gapped Docker container.
   - Run `whoami` via the remote shell console to verify allowlist-gated execution on `node-B`.

---

## 11. Troubleshooting & Windows Diagnostics

### 1. "Docker is not ready: error during connect"
- **Cause**: Docker Desktop is not running or the WSL2 engine has stopped.
- **Fix**: Launch Docker Desktop from the Start Menu and wait until the tray icon turns steady white/blue. Run `docker ps` to verify responsiveness.

### 2. "Application Control policy has blocked this file"
- **Cause**: Windows 11 Smart App Control (SAC) in "Enforce" mode blocked a newly compiled binary that has not accumulated cloud reputation.
- **Fix**: Run the node directly via Go without invoking a standalone binary:
  ```powershell
  go run ./cmd/unicompute-node [arguments...]
  ```
  Alternatively, re-run `powershell -ExecutionPolicy Bypass -File .\build.ps1` to sign all executables.

### 3. "The '<' operator is reserved for future use"
- **Cause**: Windows PowerShell does not use `<` for standard input redirection.
- **Fix**: In PowerShell, pipe stdin using `Get-Content <file> | ...`, or when using the UniCompute CLI `run` command, pass the stdin file as the 5th argument:
  `run node-B python samples\python\calc.py samples\python\input.txt`

### 4. PowerShell `curl` Parameter Errors
- **Cause**: In Windows PowerShell, `curl` is a built-in alias for `Invoke-WebRequest`, which causes standard curl flags like `-s` or `-Iv` to throw `PositionalParameterNotFound`.
- **Fix**: Explicitly use `curl.exe` to invoke the real cURL binary:
  ```powershell
  curl.exe -s https://unicompute-coordinator.onrender.com/health
  ```

### 5. "Connection to node-B failed: timeout"
- **Cause**: Windows Defender Firewall blocked UDP hole-punching packets, or both nodes are behind strict symmetric CGNAT without an accessible TURN relay.
- **Fix**:
  1. In the Windows Firewall popup, click **Allow access** for `unicompute-node.exe` on Private networks.
  2. If on mobile hotspot or university Wi-Fi with symmetric CGNAT, configure an external TURN server (e.g. Coturn) or run an embedded TURN coordinator with a public UDP port.

### 6. Render Coordinator Cold-Start Delay & UDP TURN Limitations
- **Cause**: Render free-tier instances spin down after 15 minutes of inactivity. Additionally, Render's reverse proxy only supports TCP (HTTP/WSS) and does not route UDP packets.
- **Fix**:
  1. If connecting to `wss://unicompute-coordinator.onrender.com/ws` takes 30-45 seconds on the first attempt, wait for the container to wake up, or probe the health endpoint:
     ```powershell
     curl.exe -s https://unicompute-coordinator.onrender.com/health
     ```
  2. Because Render does not route UDP traffic, embedded UDP TURN servers cannot operate on Render; peers must establish direct WebRTC connections or configure external TURN relays.

