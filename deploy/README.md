# UniCompute Cloud Coordinator Deployment Guide

This guide details how to deploy the **UniCompute Central Coordinator & Signaling Hub** to any cloud server (AWS EC2, Google Cloud Compute Engine, DigitalOcean, or a campus VM running Ubuntu Linux 22.04/24.04 LTS), matching the specification in Section 9 of the system architecture.

---

## 1. Network & Firewall Requirements

Open the following ports on your cloud provider security group / firewall:

| Port | Protocol | Purpose | Direction |
|---|---|---|---|
| **443** | TCP | Secure WebSocket Signaling (`wss://your-domain/ws`) & Web UI | Inbound |
| **80** | TCP | HTTP-01 Let's Encrypt automated TLS certificate issuance | Inbound |
| **3478** | UDP | STUN & Embedded TURN server (NAT Traversal Relay) | Inbound |
| **8080** | TCP | Direct WebSocket signaling (optional if using reverse proxy) | Inbound |

---

## 2. Option A: Docker Compose Deployment (Recommended)

Requires Docker and Docker Compose installed on your server.

### 1. Clone repository on the server
```bash
git clone https://github.com/your-org/UniComputeOBJ1.git
cd UniComputeOBJ1
```

### 2. Configure Environment
Create a `.env` file in `deploy/`:
```bash
cat <<EOF > deploy/.env
DOMAIN=coordinator.yourdomain.com
PUBLIC_IP=$(curl -s https://api.ipify.org)
REALM=unicompute.edu
TURN_SECRET=campus-volunteer-secret-2026
POSTGRES_PASSWORD=unicompute_secure_pass
EOF
```

### 3. Start Services
```bash
cd deploy
docker compose up -d --build
```

### 4. Verify Deployment
```bash
# Check container status
docker compose ps

# Check Coordinator health endpoint
curl https://coordinator.yourdomain.com/health
# Response: {"status":"healthy","service":"unicompute-coordinator"}

# Check online fleet nodes
curl https://coordinator.yourdomain.com/api/nodes
# Response: {"count":0,"nodes":[]}
```

---

## 3. Option B: Native Ubuntu VM (Bare-Metal / Systemd)

Follow these steps if you are deploying directly to an Ubuntu 22.04 or 24.04 LTS VM without Docker.

### 1. Run the Auto-Provisioning Script
```bash
chmod +x deploy/setup-ubuntu.sh
./deploy/setup-ubuntu.sh
```

The script will automatically:
1. Configure UFW firewall rules (TCP 80, 443; UDP 3478).
2. Install Go 1.22+.
3. Compile `unicompute-coordinator` into `/opt/unicompute/`.
4. Install and start the `unicompute-coordinator.service` systemd daemon.

### 2. Inspect Service Status
```bash
sudo systemctl status unicompute-coordinator
```

### 3. View Live Logs
```bash
sudo journalctl -u unicompute-coordinator -f
```

---

## 4. Connecting Volunteer Laptops to Your Cloud Coordinator

Once your cloud coordinator is online, any student laptop in the world can connect simply by supplying the cloud coordinator's address:

### Secure TLS (with Domain & Caddy/Nginx)
```powershell
.\bin\unicompute-node.exe -id student-laptop-1 -signaling wss://coordinator.yourdomain.com/ws
```

### Direct IP (No Domain)
```powershell
.\bin\unicompute-node.exe -id student-laptop-1 -signaling ws://YOUR_SERVER_PUBLIC_IP:8080/ws
```

---

## 5. Invariant CP-1 Verification in Cloud
Once two nodes connect through your cloud coordinator:
1. Establish a WebRTC P2P connection between Node A and Node B.
2. Stop the coordinator service on the cloud server: `docker compose stop coordinator` (or `sudo systemctl stop unicompute-coordinator`).
3. Notice that chunk transfers, streaming, and data operations between Node A and Node B continue over their direct WebRTC SCTP DataChannel without interruption.
