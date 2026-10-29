# UniCompute Objective 1: Windows 10/11 Deployment, Operation & Testing Guide

This guide provides end-to-end instructions for installing prerequisites, building, configuring, operating, and verifying the **UniCompute Objective 1 (Peer-to-Peer Storage Substrate & Contributed Compute Fleet)** on Microsoft Windows 10 and Windows 11 (64-bit AMD64/ARM64).

---

## Table of Contents
1. [Architecture Overview & Binaries](#1-architecture-overview--binaries)
2. [Prerequisites Installation on Windows 10/11](#2-prerequisites-installation-on-windows-1011)
   - [2.1 Git for Windows](#21-git-for-windows)
   - [2.2 Go Programming Language (1.22+)](#22-go-programming-language-122)
   - [2.3 Docker Desktop with WSL2 Backend](#23-docker-desktop-with-wsl2-backend)
3. [Windows Security, Code Signing & Smart App Control](#3-windows-security-code-signing--smart-app-control)
   - [3.1 PowerShell Execution Policy](#31-powershell-execution-policy)
   - [3.2 Unblocking Downloaded Files](#32-unblocking-downloaded-files)
   - [3.3 Authenticode Code-Signing (`build.ps1`)](#33-authenticode-code-signing-buildps1)
   - [3.4 Temporary Execution Directory (`GOTMPDIR`)](#34-temporary-execution-directory-gotmpdir)
4. [Cloning the Repository & Building Binaries](#4-cloning-the-repository--building-binaries)
5. [Running the Automated Verification Suite](#5-running-the-automated-verification-suite)
6. [Pre-Pulling Docker Sandbox Images](#6-pre-pulling-docker-sandbox-images)
7. [Running a 2-Node Cluster on Windows](#7-running-a-2-node-cluster-on-windows)
   - [Option A: Public Live Coordinator on Render](#option-a-public-live-coordinator-on-render)
   - [Option B: Local Coordinator on Localhost](#option-b-local-coordinator-on-localhost)
8. [Comprehensive CLI Command Reference & Feature Walkthrough](#8-comprehensive-cli-command-reference--feature-walkthrough)
   - [8.1 WebRTC P2P Signaling & Path Telemetry](#81-webrtc-p2p-signaling--path-telemetry)
   - [8.2 Content-Addressed Storage (CAS) Vault](#82-content-addressed-storage-cas-vault)
   - [8.3 Remote Terminal Execution (`exec`)](#83-remote-terminal-execution-exec)
   - [8.4 Air-Gapped Docker Sandbox Code Execution (`run`)](#84-air-gapped-docker-sandbox-code-execution-run)
9. [Sample Programs & Security Boundary Tests](#9-sample-programs--security-boundary-tests)
   - [9.1 Python Data Processing with Stdin (`calc.py`)](#91-python-data-processing-with-stdin-calcpy)
   - [9.2 C++ High-Precision Computation (`fib.cpp`)](#92-c-high-precision-computation-fibcpp)
   - [9.3 C ISO C17 Runtime Benchmark (`hello.c`)](#93-c-iso-c17-runtime-benchmark-helloc)
   - [9.4 Security Test: Execution Timeout Enforcement (`timeout_test.py`)](#94-security-test-execution-timeout-enforcement-timeout_testpy)
   - [9.5 Security Test: Air-Gap Network Leak Block (`network_leak_test.py`)](#95-security-test-air-gap-network-leak-block-network_leak_testpy)
   - [9.6 Security Test: 128 MB RAM Limit Enforcement (`mem_limit_test.py`)](#96-security-test-128-mb-ram-limit-enforcement-mem_limit_testpy)
10. [Troubleshooting & Windows Diagnostics](#10-troubleshooting--windows-diagnostics)

---

## 1. Architecture Overview & Binaries

UniCompute Objective 1 implements the peer-to-peer transport substrate, content-addressed storage (CAS) vault, WebRTC SCTP multiplexing, dual-stack NAT/CGNAT hole punching, remote host execution, and air-gapped Docker code execution for volunteer computing clusters.

The project compiles into three discrete binaries in the `bin/` directory:

| Binary | Description |
|---|---|
| `bin\unicompute-node.exe` | Contributor peer node daemon and interactive CLI shell. Manages local CAS vault, WebRTC connections, remote command dispatch, and Docker sandbox execution. |
| `bin\unicompute-coordinator.exe` | Central WebSocket signaling hub and optional embedded RFC 5766 TURN server. Relays SDP offers/answers and ICE candidates. |
| `bin\unicompute-relay.exe` | Standalone peer-contributor relay (Syncthing relay-v1 model) providing blind opaque byte relay fallback when symmetric NATs prevent direct hole punching. |

**Live Deployment**: A production coordinator is hosted and live on Render at:
`wss://unicompute-coordinator.onrender.com/ws`

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
Building UniCompute Objective 1 binaries...
Signing binaries for Windows Smart App Control / Code Integrity...

[SUCCESS] Binaries built and signed successfully in .\bin\
```

### 4.3 Manual Compilation Alternative
If you prefer building each binary manually:

```powershell
New-Item -ItemType Directory -Force -Path "bin" | Out-Null
go build -o bin/unicompute-node.exe ./cmd/unicompute-node
go build -o bin/unicompute-coordinator.exe ./cmd/unicompute-coordinator
go build -o bin/unicompute-relay.exe ./cmd/unicompute-relay
```

Verify that the binaries exist in `.\bin\`:
```powershell
Get-ChildItem .\bin\*.exe
```

---

## 5. Running the Automated Verification Suite

Run the full Objective 1 verification test suite to ensure your Windows environment passes all component specifications:

```powershell
powershell -ExecutionPolicy Bypass -File .\run_tests.ps1
```

*(Note: On Windows 11 with Smart App Control enforced, use `run_tests.ps1` rather than direct `go test ./pkg/...`, as `run_tests.ps1` signs each package's test runner binary to ensure compliance with Windows Application Control policies).*

### What is verified:
- **`pkg/blobstore`**: Atomic file writes, SHA-256 validation, quota enforcement, bitrot detection (`scrub`), directory traversal prevention.
- **`pkg/codeexec`**: Bounded code request/response framing, 12 KiB source and 4 KiB stdin ceiling checks.
- **`pkg/nat`**: RFC 6598 CGNAT detection (`100.64.0.0/10`), dual-stack STUN probing, Hyper-V/WSL virtual adapter pruning, and Windows `WSAECONNRESET`/`WSAENETRESET` transient socket error handling.
- **`pkg/relay`**: Syncthing relay-v1 end-to-end byte proxying and embedded TURN server lifecycle.
- **`pkg/remoteshell`**: Windows PowerShell command execution and timeout enforcement.
- **`pkg/signaling`**: WebSocket registration, keepalive tickers, and client message dispatch.
- **`pkg/transport`**: WebRTC PeerConnection, 16 MiB SCTP receive buffer, and dual DataChannels (`stream` and `chunk`).
- **`pkg/node`**: End-to-end chunk exchange and **Invariant CP-1 (Data Plane Independence)** proving that active P2P data transfers continue uninterrupted if the central coordinator crashes.

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

### Option A: Public Live Coordinator on Render
Both nodes connect over the Internet via the production coordinator on Render (`wss://unicompute-coordinator.onrender.com/ws`).

#### Terminal 1 — Start Worker Node B
```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
# Method 1 (Compiled Binary):
.\bin\unicompute-node.exe -id node-B -signaling wss://unicompute-coordinator.onrender.com/ws -enable-code-execution -code-allowlist node-A -code-timeout 15

# Method 2 (Go Toolchain - Use if Windows SAC blocks standalone binary):
go run ./cmd/unicompute-node -id node-B -signaling wss://unicompute-coordinator.onrender.com/ws -enable-code-execution -code-allowlist node-A -code-timeout 15
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
# Method 1 (Compiled Binary):
.\bin\unicompute-coordinator.exe -listen 0.0.0.0:8080

# Method 2 (Go Toolchain):
go run ./cmd/unicompute-coordinator -listen 0.0.0.0:8080
```

#### Terminal 1 — Start Worker Node B
```powershell
# Method 1 (Compiled Binary):
.\bin\unicompute-node.exe -id node-B -signaling ws://127.0.0.1:8080/ws -enable-code-execution -code-allowlist node-A -code-timeout 15

# Method 2 (Go Toolchain):
go run ./cmd/unicompute-node -id node-B -signaling ws://127.0.0.1:8080/ws -enable-code-execution -code-allowlist node-A -code-timeout 15
```

#### Terminal 2 — Start Client Node A
```powershell
# Method 1 (Compiled Binary):
.\bin\unicompute-node.exe -id node-A -signaling ws://127.0.0.1:8080/ws

# Method 2 (Go Toolchain):
go run ./cmd/unicompute-node -id node-A -signaling ws://127.0.0.1:8080/ws
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
Ping sent to node-B...
[PONG] Reply from node-B: Pong received!
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
```text
> fetch node-B <blobID>
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
- **`--memory 128m --memory-swap 128m`**: 128 MB RAM ceiling.
- **`--cpus 1.0`**: CPU capped at 1 core.
- **`--pids-limit 64`**: Fork-bomb mitigation.
- **`--cap-drop ALL`**: Drops all Linux kernel capabilities.
- **`--security-opt no-new-privileges:true`**: Prevents privilege escalation.
- **`--tmpfs /tmp:rw,nosuid,nodev,size=64m`**: Ephemeral memory-only scratch space.

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
Compiles with GCC 14 (`g++ -std=c++17 -O2 -pipe`) and computes the first 45 Fibonacci numbers and Golden Ratio convergence.

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
Compiles with GCC 14 (`gcc -std=c17 -O2 -pipe`) and calculates 32-bit FNV-1a cryptographic hashes of UniCompute system strings.

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

### 9.6 Security Test: 128 MB RAM Limit Enforcement (`mem_limit_test.py`)
Verifies that the Docker container enforces `--memory 128m --memory-swap 128m`. Allocates memory in 10 MB increments. When memory approaches 128 MB, the Linux kernel OOM killer terminates the container with exit code 137.

#### Creation Command:
```powershell
Set-Content -Path "samples\security\mem_limit_test.py" -Value @'
#!/usr/bin/env python3
import sys
import time

print("==================================================")
print("  UniCompute Security: 128 MB RAM Limit Audit     ")
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

    print(f"[FAIL] Allocated {total_mb} MB without hitting the 128 MB container cap!")
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
  UniCompute Security: 128 MB RAM Limit Audit     
==================================================
Allocating memory in 10 MB chunks...
[ALLOCATION] Successfully allocated 10 MB RAM
[ALLOCATION] Successfully allocated 20 MB RAM
...
[ALLOCATION] Successfully allocated 120 MB RAM
 Exit code: 137 | Worker execution: 610 ms | Round trip: 635 ms
=================================================================
```
*(Exit code 137 = 128 + 9 = SIGKILL by the Linux OOM Killer, proving the container was strictly bounded at 128 MB).*

---

## 10. Troubleshooting & Windows Diagnostics

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
- **Cause**: Windows Defender Firewall blocked UDP hole-punching packets, or both nodes are behind strict symmetric CGNAT without a relay.
- **Fix**:
  1. In the Windows Firewall popup, click **Allow access** for `unicompute-node.exe` on Private networks.
  2. If on mobile hotspot or university Wi-Fi with symmetric CGNAT, start a contributor relay:
     ```powershell
     .\bin\unicompute-relay.exe -listen 0.0.0.0:22067
     ```
     And start nodes with `-relay <relayIP>:22067`.

### 6. Render Coordinator Cold-Start Delay
- **Cause**: Render free instances spin down after 15 minutes of inactivity.
- **Fix**: If connecting to `wss://unicompute-coordinator.onrender.com/ws` takes 30-45 seconds on the very first attempt, wait for the container to wake up, or probe the health endpoint:
  ```powershell
  curl.exe -s https://unicompute-coordinator.onrender.com/health
  ```

