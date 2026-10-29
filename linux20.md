# UniCompute Objective 1: Ubuntu 20.04 LTS (Focal Fossa) Deployment, Operation & Testing Guide

This guide provides end-to-end instructions for installing prerequisites, configuring modern container compatibility, compiling static CGO-free binaries, configuring firewalls, operating, and verifying the **UniCompute Objective 1 (Peer-to-Peer Storage Substrate & Contributed Compute Fleet)** specifically on **Ubuntu 20.04 LTS (Focal Fossa)** (64-bit x86_64 / amd64 and aarch64).

---

## Table of Contents
1. [Architecture Overview & Binaries](#1-architecture-overview--binaries)
2. [Ubuntu 20.04 LTS Specific Prerequisites & System Hardening](#2-ubuntu-2004-lts-specific-prerequisites--system-hardening)
   - [2.1 Package Repositories & Build Essentials](#21-package-repositories--build-essentials)
   - [2.2 Updating `libseccomp2` (>= 2.5.1) for `clone3` Syscall & Modern OCI Containers](#22-updating-libseccomp2--251-for-clone3-syscall--modern-oci-containers)
   - [2.3 Checking & Enabling Kernel Memory Swap Accounting in GRUB](#23-checking--enabling-kernel-memory-swap-accounting-in-grub)
   - [2.4 Go 1.22.8 Tarball Manual Installation (Bypassing Ancient Go 1.13 in Focal Apt)](#24-go-1228-tarball-manual-installation-bypassing-ancient-go-113-in-focal-apt)
   - [2.5 Docker Engine Installation, Service Activation & User Group](#25-docker-engine-installation-service-activation--user-group)
3. [Linux Firewall & Network Setup for WebRTC](#3-linux-firewall--network-setup-for-webrtc)
   - [3.1 UFW (Uncomplicated Firewall)](#31-ufw-uncomplicated-firewall)
   - [3.2 Kernel UDP & Docker Bridge Forwarding](#32-kernel-udp--docker-bridge-forwarding)
   - [3.3 Virtual Adapter Pruning (`docker0`, `veth`)](#33-virtual-adapter-pruning-docker0-veth)
4. [Cloning the Repository & Go Version Alignment](#4-cloning-the-repository--go-version-alignment)
   - [4.1 Git Clone](#41-git-clone)
   - [4.2 Handling `go.mod` Version Alignment (`go 1.22`)](#42-handling-gomod-version-alignment-go-122)
5. [Compiling Static CGO-Free Binaries (Bypassing Host Glibc 2.31)](#5-compiling-static-cgo-free-binaries-bypassing-host-glibc-231)
6. [Running the Automated Verification Suite](#6-running-the-automated-verification-suite)
7. [Pre-Pulling Docker Sandbox Images](#7-pre-pulling-docker-sandbox-images)
8. [Running a 2-Node Cluster on Ubuntu 20.04 LTS](#8-running-a-2-node-cluster-on-ubuntu-2004-lts)
   - [Option A: Public Live Coordinator on Render](#option-a-public-live-coordinator-on-render)
   - [Option B: Local Coordinator on Localhost](#option-b-local-coordinator-on-localhost)
9. [Comprehensive CLI Command Reference & Feature Walkthrough](#9-comprehensive-cli-command-reference--feature-walkthrough)
   - [9.1 WebRTC P2P Signaling & Path Telemetry](#91-webrtc-p2p-signaling--path-telemetry)
   - [9.2 Content-Addressed Storage (CAS) Vault](#92-content-addressed-storage-cas-vault)
   - [9.3 Remote Terminal Execution (`exec`)](#93-remote-terminal-execution-exec)
   - [9.4 Air-Gapped Docker Sandbox Code Execution (`run`)](#94-air-gapped-docker-sandbox-code-execution-run)
10. [Sample Programs & Security Boundary Tests](#10-sample-programs--security-boundary-tests)
    - [10.1 Python Data Processing with Stdin (`calc.py` & `input.txt`)](#101-python-data-processing-with-stdin-calcpy--inputtxt)
    - [10.2 C++ High-Precision Computation (`fib.cpp`)](#102-c-high-precision-computation-fibcpp)
    - [10.3 C ISO C17 Runtime Benchmark (`hello.c`)](#103-c-iso-c17-runtime-benchmark-helloc)
    - [10.4 Security Test: Execution Timeout Enforcement (`timeout_test.py`)](#104-security-test-execution-timeout-enforcement-timeout_testpy)
    - [10.5 Security Test: Air-Gap Network Leak Block (`network_leak_test.py`)](#105-security-test-air-gap-network-leak-block-network_leak_testpy)
    - [10.6 Security Test: 128 MB RAM Limit Enforcement (`mem_limit_test.py`)](#106-security-test-128-mb-ram-limit-enforcement-mem_limit_testpy)
11. [Cross-Platform Interoperability (Ubuntu 20.04 <-> Modern Linux / Windows)](#11-cross-platform-interoperability-ubuntu-2004---modern-linux--windows)
12. [Production Daemonization with Systemd](#12-production-daemonization-with-systemd)
13. [Ubuntu 20.04 Specific Troubleshooting & Diagnostics](#13-ubuntu-2004-specific-troubleshooting--diagnostics)

---

## 1. Architecture Overview & Binaries

UniCompute Objective 1 implements the peer-to-peer transport substrate, content-addressed storage (CAS) vault, WebRTC SCTP multiplexing, dual-stack NAT/CGNAT hole punching, remote host execution, and air-gapped Docker code execution for volunteer computing clusters.

The project compiles into three standalone, CGO-free Linux ELF executables in the `bin/` directory:

| Binary | Description |
|---|---|
| `bin/unicompute-node` | Contributor peer storage node daemon and interactive CLI shell. Manages local CAS vault, WebRTC connections, remote command dispatch, and Docker sandbox execution. |
| `bin/unicompute-coordinator` | Central WebSocket signaling hub and optional embedded RFC 5766 TURN server. Relays SDP offers/answers and ICE candidates. |
| `bin/unicompute-relay` | Standalone peer-contributor relay (Syncthing relay-v1 model) providing blind opaque byte relay fallback when symmetric NATs prevent direct hole punching. |

**Live Production Coordinator**: A central signaling coordinator is live on Render at:
`wss://unicompute-coordinator.onrender.com/ws`

---

## 2. Ubuntu 20.04 LTS Specific Prerequisites & System Hardening

Ubuntu 20.04 LTS (Focal Fossa) entered Expanded Security Maintenance (ESM) in April 2025. Standard repositories are functional, but specific architectural characteristics of Focal—such as its original `libseccomp2` release, default Go 1.13 package, and cgroup v1 memory accounting—require explicit alignment before running the modern UniCompute runtime.

### 2.1 Package Repositories & Build Essentials

Ensure `focal-updates` and `focal-security` are enabled in `/etc/apt/sources.list`, update the package cache, and install base build tools:

```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y git curl build-essential ca-certificates gnupg lsb-release software-properties-common
```

### 2.2 Updating `libseccomp2` (>= 2.5.1) for `clone3` Syscall & Modern OCI Containers

**Modern Container Compatibility**: Modern compiler and runtime base images (`gcc:14` and `python:3.13-slim`) are based on Debian 12 / Glibc 2.34+. These modern runtimes use the `clone3` Linux system call.
The initial release of Ubuntu 20.04 shipped with `libseccomp2 2.4.3`. Under libseccomp 2.4.3, Docker's default seccomp profile returns `EPERM` (Operation not permitted) for `clone3`. When Glibc receives `EPERM`, it crashes immediately with:
`Fatal glibc error: cannot create thread` or crashes python on startup.

Canonical patched this issue in `focal-updates` with `libseccomp2 2.5.1-1ubuntu1~20.04.2`. Verify and update `libseccomp2`:

```bash
# Update libseccomp2 from focal-updates
sudo apt update
sudo apt install -y libseccomp2

# If Docker or containerd was already running, restart them to reload the new shared library
sudo systemctl restart containerd docker 2>/dev/null || true

# Verify version is >= 2.5.1
dpkg -l | grep libseccomp2
# Expected: ii libseccomp2:amd64 2.5.1-1ubuntu1~20.04.2 (or higher)
```
With `libseccomp2 >= 2.5.1`, Docker returns `ENOSYS` or supports `clone3`, allowing modern container runtimes to execute without errors.

### 2.3 Checking & Enabling Kernel Memory Swap Accounting in GRUB

Ubuntu 20.04 kernels use cgroup v1 by default. On some Focal installations, memory cgroup swap accounting is disabled, causing Docker to emit a warning to `stderr` whenever `--memory-swap` is configured:
```text
WARNING: Your kernel does not support swap limit capabilities or the cgroup is not mounted. Memory limited without swap.
```
Because UniCompute captures stderr to report code execution diagnostics, check whether your kernel supports swap limiting:

```bash
docker info 2>/dev/null | grep -i swap
```

If the output contains `WARNING: No swap limit support`, enable it in GRUB:

```bash
# Recommended for both bare-metal and cloud VPS (AWS, GCP, Azure, DigitalOcean):
# Create a dedicated drop-in config under /etc/default/grub.d/ so cloud-init cannot override it
sudo mkdir -p /etc/default/grub.d
echo 'GRUB_CMDLINE_LINUX_DEFAULT="$GRUB_CMDLINE_LINUX_DEFAULT cgroup_enable=memory swapaccount=1"' | sudo tee /etc/default/grub.d/99-swapaccounting.cfg

# Fallback: Also append to /etc/default/grub if drop-ins are not used by the bootloader
if ! grep -q "swapaccount=1" /etc/default/grub; then
    sudo sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="/GRUB_CMDLINE_LINUX_DEFAULT="cgroup_enable=memory swapaccount=1 /' /etc/default/grub
fi

# Regenerate grub boot configuration
sudo update-grub
echo "[INFO] GRUB updated with swapaccount=1. Reboot your machine to apply: sudo reboot"
```

### 2.4 Go 1.22.8 Tarball Manual Installation (Bypassing Ancient Go 1.13 in Focal Apt)

Ubuntu 20.04's apt repository provides Go 1.13, which cannot compile `UniComputeOBJ1` (`go.mod` specifies `go 1.22`). Install official Go 1.22.8 binaries matching your machine architecture (x86_64 or aarch64):

```bash
# Detect system architecture (amd64 vs arm64)
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  GO_ARCH="amd64" ;;
    aarch64) GO_ARCH="arm64" ;;
    *)       echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Download Go 1.22.8 official release
GO_VERSION="1.22.8"
curl -OL "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"

# Clean any existing Go installation and extract tarball
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
rm "go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"

# Configure PATH in ~/.bashrc
if ! grep -q "/usr/local/go/bin" ~/.bashrc; then
    echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
fi
export PATH=$PATH:/usr/local/go/bin

# Verify Go version
go version
# Expected: go version go1.22.8 linux/amd64 (or linux/arm64)
```

### 2.5 Docker Engine Installation, Service Activation & User Group

Install Docker Engine on Ubuntu 20.04 via Docker CE official repository or Ubuntu's `docker.io` package:

#### Option 1: Official Docker CE Repository (Recommended)
```bash
# Add Docker GPG key
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

# Add Docker apt repository for Focal
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
  focal stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

#### Option 2: Ubuntu Archive `docker.io`
```bash
sudo apt install -y docker.io docker-compose
```

#### Service Activation & Non-Root User Permissions:
```bash
# Enable and start Docker systemd service
sudo systemctl enable --now docker

# Add your user to the docker group
sudo usermod -aG docker $USER

# Apply group membership immediately to current shell session (or log out and log back in)
# Note: newgrp spawns a new subshell; run verification inside it or open a new terminal
newgrp docker

# Verify that Docker runs without sudo
docker run --rm hello-world
```

---

## 3. Linux Firewall & Network Setup for WebRTC

UniCompute utilizes dual-stack STUN probing (`stun.l.google.com:19302`) and WebRTC ICE hole punching over dynamic UDP ports. On Ubuntu 20.04, ensure UFW and iptables forward rules permit traffic.

### 3.1 UFW (Uncomplicated Firewall)

If UFW is active:
```bash
# Allow outgoing UDP for STUN and WebRTC hole punching (default on UFW)
sudo ufw default allow outgoing

# If hosting a local coordinator:
sudo ufw allow 8080/tcp comment "UniCompute Coordinator WebSocket"

# If hosting a contributor relay:
sudo ufw allow 22067/tcp comment "UniCompute Contributor Relay"

# If running an embedded TURN server:
sudo ufw allow 3478/udp comment "UniCompute TURN Relay"

sudo ufw reload
sudo ufw status verbose
```

### 3.2 Kernel UDP & Docker Bridge Forwarding

Ensure the Linux kernel forwards packets across Docker bridges:
```bash
sudo sysctl -w net.ipv4.ip_forward=1
sudo iptables -P FORWARD ACCEPT
```

To persist across reboots, add to `/etc/sysctl.conf`:
```bash
echo "net.ipv4.ip_forward=1" | sudo tee -a /etc/sysctl.conf
```

### 3.3 Virtual Adapter Pruning (`docker0`, `veth`)

UniCompute's internal network classifier (`pkg/nat`) automatically prunes virtual interfaces (`docker0`, `br-*`, `veth*`, `tailscale0`, `tun*`, `tap*`) so that local Docker bridge networks do not inject unroutable private IP addresses into WebRTC SDP candidate harvesting.

---

## 4. Cloning the Repository & Go Version Alignment

### 4.1 Git Clone
```bash
git clone https://github.com/unicompute/obj1.git UniComputeOBJ1
cd UniComputeOBJ1
```

### 4.2 Handling `go.mod` Version Alignment (`go 1.22`)
The repository specifies `go 1.22`. Ensure clean module alignment:
```bash
go mod edit -go 1.22
go mod tidy
```

---

## 5. Compiling Static CGO-Free Binaries (Bypassing Host Glibc 2.31)

Ubuntu 20.04 ships with **Glibc 2.31**. Compiling with `CGO_ENABLED=0` and stripped symbol tables (`-ldflags="-s -w"`) generates 100% static, portable ELF binaries that have zero runtime dependency on the host's C library version:

```bash
mkdir -p bin

# 1. Compile Node daemon & CLI shell
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/unicompute-node ./cmd/unicompute-node

# 2. Compile Central Signaling Coordinator
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/unicompute-coordinator ./cmd/unicompute-coordinator

# 3. Compile Standalone Contributor Relay
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/unicompute-relay ./cmd/unicompute-relay

# Ensure executables have execution permissions
chmod +x bin/*
```

### Binary Verification:
```bash
ls -lh bin/
file bin/unicompute-node
# Output: bin/unicompute-node: ELF 64-bit LSB executable, x86-64, statically linked, stripped

ldd bin/unicompute-node
# Output: not a dynamic executable
```

---

## 6. Running the Automated Verification Suite

Run the full automated test suite to verify storage engines, networking, and security boundaries on Ubuntu 20.04:

```bash
go test -v ./pkg/...
```

### Test Suite Verification Matrix:
- **`pkg/blobstore`**: Atomic filesystem commits, SHA-256 validation, quota tracking, bitrot scrub audit.
- **`pkg/codeexec`**: Bounded code request/response framing, 12 KiB source and 4 KiB stdin ceiling checks.
- **`pkg/nat`**: RFC 6598 CGNAT detection (`100.64.0.0/10`), dual-stack STUN probing, and virtual adapter pruning (`docker0`, `veth`).
- **`pkg/relay`**: Syncthing relay-v1 blind end-to-end byte transfer and embedded TURN server lifecycle.
- **`pkg/remoteshell`**: Linux shell execution (`/bin/sh`, `/bin/bash`) and timeout enforcement.
- **`pkg/signaling`**: WebSocket registration, 20-second keepalives, and candidate relay dispatch.
- **`pkg/transport`**: WebRTC PeerConnection, 16 MiB SCTP receive buffer, and dual DataChannels (`stream` and `chunk`).
- **`pkg/node`**: End-to-end chunk exchange and **Invariant CP-1 (Data Plane Independence)** verifying data transfer resilience if the coordinator terminates.

---

## 7. Pre-Pulling Docker Sandbox Images

Worker nodes execute untrusted code submitted by peers in unprivileged Docker containers. Pre-pull the official compiler and interpreter images once:

```bash
docker pull gcc:14
docker pull python:3.13-slim
```

Verify that both images are downloaded:
```bash
docker images | grep -E "gcc|python"
```

---

## 8. Running a 2-Node Cluster on Ubuntu 20.04 LTS

Open **two separate terminal tabs or windows**:
- **Terminal 1 (Worker Node B)**: Contributes storage, enables Docker sandbox execution, and executes code jobs from allowed peers.
- **Terminal 2 (Client Node A)**: Connects to Node B, transfers files into the CAS vault, issues shell commands, and submits jobs.

### Option A: Public Live Coordinator on Render
Both nodes connect over the Internet via the production coordinator on Render:

#### Terminal 1 — Start Worker Node B
```bash
./bin/unicompute-node \
  -id node-B \
  -signaling wss://unicompute-coordinator.onrender.com/ws \
  -enable-code-execution \
  -code-allowlist node-A \
  -code-timeout 15
```

*Worker Node B logs output:*
```text
===================================================================
 UniCompute Objective 1: Peer-to-Peer Storage Substrate
 Node ID:        node-B
 Pool ID:        default-pool
 Storage Quota:  10 GiB
 Storage Path:   /home/<user>/.unicompute/storage/node-B
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
```bash
./bin/unicompute-node \
  -id node-A \
  -signaling wss://unicompute-coordinator.onrender.com/ws
```

*Both nodes immediately announce fleet discovery:*
```text
[FLEET] Peer 'node-B' joined the fleet! (Type 'connect node-B' to establish P2P link)
```

---

### Option B: Local Coordinator on Localhost
If testing offline or on an isolated local network:

#### Terminal 0 — Start Local Coordinator
```bash
./bin/unicompute-coordinator -listen 0.0.0.0:8080
```

#### Terminal 1 — Start Worker Node B
```bash
./bin/unicompute-node \
  -id node-B \
  -signaling ws://127.0.0.1:8080/ws \
  -enable-code-execution \
  -code-allowlist node-A \
  -code-timeout 15
```

#### Terminal 2 — Start Client Node A
```bash
./bin/unicompute-node \
  -id node-A \
  -signaling ws://127.0.0.1:8080/ws
```

---

## 9. Comprehensive CLI Command Reference & Feature Walkthrough

Execute all commands below from **Terminal 2 (Client Node A)**:

### 9.1 WebRTC P2P Signaling & Path Telemetry

#### 1. List fleet nodes
```text
> peers
```
*Lists online fleet nodes registered with the coordinator and active P2P links.*

#### 2. Establish direct WebRTC P2P DataChannel link
```text
> connect node-B
```
*Output:*
```text
Establishing WebRTC P2P link with node-B...
[EVENT] Peer node-B connected via WebRTC DataChannel
[METRICS] Peer node-B path: direct=true, pair=host-host, local=192.168.1.100:54210, remote=192.168.1.105:51882
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
> stream node-B Hello from Ubuntu 20.04 Client Node A!
```

#### 5. Inspect Path Telemetry & Fleet Metrics
```text
> metrics
```
*Displays total connections, direct vs relayed ratio, and CGNAT prevalence across the fleet.*

```text
> nat
```
*Displays local NAT classification, public IP, and CGNAT detection.*

---

### 9.2 Content-Addressed Storage (CAS) Vault

#### 1. Store a local file in the vault (`put`)
```text
> put samples/python/input.txt
```
*Output:*
```text
Successfully stored blob: 66a2cbdb7d04a60155609462eec4c5fa33f3801ea558b8d960f5e1ef2ecbfcb6 (200 bytes)
```

#### 2. List stored blobs (`list`)
```text
> list
```

#### 3. Inspect blob metadata (`stat`)
```text
> stat <blobID>
```

#### 4. Transfer blob over WebRTC DataChannel to Node B (`transfer`)
```text
> transfer node-B <blobID>
```
*Transfers the blob directly across the SCTP bulk `chunk` channel to Node B.*

#### 5. Fetch blob from Node B (`fetch`)
```text
> fetch node-B <blobID>
```

#### 6. Export blob back to a local file (`get`)
```text
> get <blobID> ./downloaded_input.txt
```

#### 7. Audit Vault for Bitrot (`scrub`)
```text
> scrub
```
*Scans all stored chunks and verifies cryptographic SHA-256 hashes against stored indices.*

#### 8. Delete a blob from vault (`del`)
```text
> del <blobID>
```

---

### 9.3 Remote Terminal Execution (`exec`)

On Linux targets, `exec` executes bash/sh commands on the remote machine.

#### 1. Execute system commands on Worker Node B
```text
> exec node-B uname -a
> exec node-B lscpu
> exec node-B df -h
```
*Output:*
```text
[EXEC] Executing 'uname -a' on remote PC 'node-B'...
=================================================================
 Remote Node: node-B | Exit Code: 0 | Duration: 18 ms
 --- STDOUT ---
 Linux worker-node 5.4.0-189-generic #209-Ubuntu SMP Fri Jun 7 14:15:30 UTC 2024 x86_64 GNU/Linux
=================================================================
```

#### 2. Broadcast command to all connected peers
```text
> exec all uptime
```

---

### 9.4 Air-Gapped Docker Sandbox Code Execution (`run`)

The `run` command reads a local source file on Client Node A, transmits the source code and optional standard input over the WebRTC `stream` DataChannel to Worker Node B, where it is compiled and executed in a sandboxed Docker container with:
- **`--network none`**: Disables container network access (air-gapped).
- **`--read-only`**: Read-only container root filesystem.
- **`--memory 128m --memory-swap 128m`**: 128 MB RAM limit.
- **`--cpus 1.0`**: 1 CPU core allocation.
- **`--pids-limit 64`**: Mitigates fork bombs.
- **`--cap-drop ALL`**: Drops all Linux capabilities.
- **`--security-opt no-new-privileges:true`**: Prevents setuid privilege elevation.
- **`--tmpfs /tmp:rw,nosuid,nodev,size=64m`**: Ephemeral memory scratchpad.

---

## 10. Sample Programs & Security Boundary Tests

The repository includes ready-to-use sample programs in the `samples/` directory. Run each from **Client Node A**:

### 10.1 Python Data Processing with Stdin (`calc.py` & `input.txt`)
Reads numbers from standard input, calculates mean, standard deviation, median, min, and max, and outputs descriptive statistics.

#### Creation Commands (if creating manually):
```bash
mkdir -p samples/python

cat << 'EOF' > samples/python/input.txt
128.50 94.20 15.00 482.10 39.40 88.00 12.34 500.00
73.15 62.40 81.90 190.25 45.60 99.10 312.80 5.40
23.70 87.65 142.10 67.89 44.55 98.70 215.30 63.20
11.45 78.90 34.50 156.70 89.20 45.10 120.00 67.50
EOF

cat << 'EOF' > samples/python/calc.py
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
EOF
```

#### Run from Client Node A:
```text
> run node-B python samples/python/calc.py samples/python/input.txt
```

Expected Output:
```text
[RUN] Submitting python job job-3a81... to node-B...
=================================================================
 Job ID: job-3a81... | Worker: node-B | Language: python
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
 Exit code: 0 | Worker execution: 360 ms | Round trip: 385 ms
=================================================================
```

---

### 10.2 C++ High-Precision Computation (`fib.cpp`)
Compiles with GCC 14 (`g++ -std=c++17 -O2 -pipe`) and calculates 45 Fibonacci numbers and Golden Ratio convergence.

#### Creation Command:
```bash
mkdir -p samples/cpp

cat << 'EOF' > samples/cpp/fib.cpp
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
EOF
```

#### Run from Client Node A:
```text
> run node-B cpp samples/cpp/fib.cpp
```

Expected Output:
```text
=================================================================
 Job ID: job-7c4d... | Worker: node-B | Language: cpp
 Status: Completed
 --- STDOUT ---
==================================================
    UniCompute Worker: C++ Fibonacci Benchmark    
==================================================
First 45 Fibonacci numbers computed in 1 microseconds:

  0:                    0
  1:                    1
  2:                    1  (Ratio: 1.00000000)
...
 44:            701408733  (Ratio: 1.61803399)
==================================================
Approaching Golden Ratio (phi): 1.6180339887...
Status: C++ computation completed successfully.
 Exit code: 0 | Worker execution: 710 ms | Round trip: 735 ms
=================================================================
```

---

### 10.3 C ISO C17 Runtime Benchmark (`hello.c`)
Compiles with GCC 14 (`gcc -std=c17 -O2 -pipe`) and calculates 32-bit FNV-1a cryptographic hashes of UniCompute system strings.

#### Creation Command:
```bash
mkdir -p samples/c

cat << 'EOF' > samples/c/hello.c
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
EOF
```

#### Run from Client Node A:
```text
> run node-B c samples/c/hello.c
```

Expected Output:
```text
=================================================================
 Job ID: job-1b2c... | Worker: node-B | Language: c
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
 Exit code: 0 | Worker execution: 460 ms | Round trip: 485 ms
=================================================================
```

---

### 10.4 Security Test: Execution Timeout Enforcement (`timeout_test.py`)
Verifies that worker policy terminates runaway code jobs after the configured timeout (e.g. 15s).

#### Creation Command:
```bash
mkdir -p samples/security

cat << 'EOF' > samples/security/timeout_test.py
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
EOF
```

#### Run from Client Node A:
```text
> run node-B python samples/security/timeout_test.py
```

Expected Output:
```text
[RUN] Submitting python job job-9f4a... to node-B...
=================================================================
 Job ID: job-9f4a... | Worker: node-B | Language: python
 Status: Timed out
 Error: execution exceeded 15s timeout
 --- STDOUT ---
[SECURITY TEST] Starting infinite compute/sleep loop...
[SECURITY TEST] Worker policy must terminate container after timeout.
[SECURITY TEST] Elapsed: 1 seconds (still running)...
...
 Exit code: -1 | Worker execution: 15004 ms | Round trip: 15038 ms
=================================================================
```

---

### 10.5 Security Test: Air-Gap Network Leak Block (`network_leak_test.py`)
Verifies that Docker container enforcement of `--network none` blocks all outbound TCP, UDP, and DNS traffic.

#### Creation Command:
```bash
cat << 'EOF' > samples/security/network_leak_test.py
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
EOF
```

#### Run from Client Node A:
```text
> run node-B python samples/security/network_leak_test.py
```

Expected Output:
```text
=================================================================
 Job ID: job-5e6f... | Worker: node-B | Language: python
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
 Exit code: 0 | Worker execution: 380 ms | Round trip: 405 ms
=================================================================
```

---

### 10.6 Security Test: 128 MB RAM Limit Enforcement (`mem_limit_test.py`)
Verifies that the Docker container enforces `--memory 128m --memory-swap 128m`. Continuously allocates 10 MB chunks until the Linux OOM killer terminates the container with exit code 137.

#### Creation Command:
```bash
cat << 'EOF' > samples/security/mem_limit_test.py
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
EOF
```

#### Run from Client Node A:
```text
> run node-B python samples/security/mem_limit_test.py
```

Expected Output:
```text
=================================================================
 Job ID: job-2d8e... | Worker: node-B | Language: python
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
 Exit code: 137 | Worker execution: 590 ms | Round trip: 615 ms
=================================================================
```
*(Exit code 137 = SIGKILL by the Linux Out-Of-Memory Killer, verifying hardware RAM capping).*

---

## 11. Cross-Platform Interoperability (Ubuntu 20.04 <-> Modern Linux / Windows)

UniCompute nodes are fully interoperable across operating systems:
- An **Ubuntu 20.04 Client Node** can connect to an **Ubuntu 24.04** or **Windows 11 Worker Node** (and vice versa).
- **Code Execution Symmetry**: Because worker nodes on both Windows (via WSL2 Docker) and Linux execute identical Linux container base images (`gcc:14` and `python:3.13-slim`), user code execution is 100% portable and deterministic across platforms.
- **Shell Discrepancies**: When using the host `exec` command:
  - Targeting a **Windows node** invokes Windows PowerShell (`Get-Process`, `dir`, `ipconfig`).
  - Targeting a **Linux node** invokes `/bin/sh` or `/bin/bash` (`ps aux`, `ls -la`, `ifconfig`/`ip a`).
- **Path Handling**: The CLI parser (`console_args.go`) handles both Windows backslashes (`\`) and POSIX forward slashes (`/`), as well as double-quoted paths containing spaces:
  ```text
  > run node-B python "./my samples/calc.py" "./my samples/input.txt"
  ```

---

## 12. Production Daemonization with Systemd

To run the UniCompute node as a continuous background daemon on Ubuntu 20.04 LTS servers:

Create `/etc/systemd/system/unicompute-node.service`:
```ini
[Unit]
Description=UniCompute Objective 1 Contributor Storage & Compute Node
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
Type=simple
User=YOUR_LINUX_USERNAME
WorkingDirectory=/home/YOUR_LINUX_USERNAME
ExecStart=/path/to/UniComputeOBJ1/bin/unicompute-node \
  -id node-focal-worker \
  -signaling wss://unicompute-coordinator.onrender.com/ws \
  -storage /var/lib/unicompute/storage \
  -quota-gb 50 \
  -enable-code-execution \
  -code-allowlist node-client-1 \
  -code-timeout 30
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

Enable and start the service:
```bash
# Ensure storage directory exists and is writable by the non-root service user
sudo mkdir -p /var/lib/unicompute/storage
sudo chown -R YOUR_LINUX_USERNAME:YOUR_LINUX_USERNAME /var/lib/unicompute

# Reload systemd, enable, and start daemon
sudo systemctl daemon-reload
sudo systemctl enable --now unicompute-node.service
sudo journalctl -u unicompute-node.service -f
```

---

## 13. Ubuntu 20.04 Specific Troubleshooting & Diagnostics

### 1. `Fatal glibc error: cannot create thread` or `Operation not permitted` in Docker
- **Cause**: Ubuntu 20.04 was released with `libseccomp2 2.4.3`. Modern container images (`gcc:14`, `python:3.13-slim`) built on Debian 12 / Glibc 2.34+ invoke `clone3`, which returns `EPERM` under libseccomp 2.4.3. If `libseccomp2` was updated while Docker was running, the running daemon may retain the old library mapped in memory.
- **Fix**: Update `libseccomp2` to version `>= 2.5.1` from `focal-updates` as detailed in [Section 2.2](#22-updating-libseccomp2--251-for-clone3-syscall--modern-oci-containers) and restart Docker:
  ```bash
  sudo apt update && sudo apt install -y libseccomp2
  sudo systemctl restart containerd docker
  ```

### 2. `go: go.mod file indicates go 1.22 (or higher), but maximum supported version is 1.13`
- **Cause**: The default Ubuntu 20.04 package repository installs Go 1.13 via `apt install golang-go`.
- **Fix**: Install Go 1.22.8 via official tarball as detailed in [Section 2.4](#24-go-1228-tarball-manual-installation-bypassing-ancient-go-113-in-focal-apt).

### 3. `WARNING: Your kernel does not support swap limit capabilities`
- **Cause**: Memory cgroup swap accounting is disabled in the kernel cmdline.
- **Fix**: Check [Section 2.3](#23-checking--enabling-kernel-memory-swap-accounting-in-grub). Append `cgroup_enable=memory swapaccount=1` to GRUB, execute `sudo update-grub`, and reboot.

### 4. `mkdir /var/lib/unicompute/storage: permission denied`
- **Cause**: The systemd service is executing under a non-root user (`User=YOUR_LINUX_USERNAME`), but `/var/lib/unicompute` was created by root without write permissions for that user.
- **Fix**: Create and chown the directory:
  ```bash
  sudo mkdir -p /var/lib/unicompute/storage
  sudo chown -R $USER:$USER /var/lib/unicompute
  ```

### 5. `could not run Docker job: Got permission denied while trying to connect to the Docker daemon socket`
- **Cause**: The active user is not in the `docker` group or the shell session has not refreshed group privileges.
- **Fix**: Execute `sudo usermod -aG docker $USER && newgrp docker`. Verify with `docker ps`.

### 6. `version 'GLIBC_2.34' not found`
- **Cause**: Attempting to run a dynamically compiled Go binary built on modern Linux (Ubuntu 22.04+) on Ubuntu 20.04 (which provides Glibc 2.31).
- **Fix**: Always compile with `CGO_ENABLED=0 go build -ldflags="-s -w"` as shown in [Section 5](#5-compiling-static-cgo-free-binaries-bypassing-host-glibc-231).

### 7. `Connection to peer failed: context deadline exceeded`
- **Cause**: Both nodes are behind strict symmetric NAT / CGNAT without open UDP hole-punching paths.
- **Fix**:
  1. Start a contributor relay on any accessible node:
     `./bin/unicompute-relay -listen 0.0.0.0:22067`
  2. Launch nodes pointing to the relay:
     `./bin/unicompute-node -id node-A -relay RELAY_IP:22067 ...`

### 8. Render Coordinator Cold-Start Wait
- **Cause**: Free Render instances sleep when idle. The first connection attempt may take 30–45 seconds while Render boots the container.
- **Fix**: Keep the instance warm with a periodic health probe (`curl https://unicompute-coordinator.onrender.com/health`).

