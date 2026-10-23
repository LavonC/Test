#!/usr/bin/env bash
# ==============================================================================
# UniCompute Cloud Coordinator Auto-Provisioning Script (Ubuntu 22.04 / 24.04 LTS)
# Specifications matching Section 9 of the UniCompute System Architecture
# ==============================================================================
set -euo pipefail

echo "==================================================================="
echo " UniCompute Coordinator Installation & Setup"
echo "==================================================================="

# 1. Update OS packages
sudo apt-get update && sudo apt-get upgrade -y
sudo apt-get install -y curl wget git build-essential ufw debian-keyring debian-archive-keyring apt-transport-https

# 2. Configure Firewall (UFW)
echo "[INFO] Configuring firewall rules..."
sudo ufw allow 22/tcp comment 'SSH'
sudo ufw allow 80/tcp comment 'HTTP (ACME)'
sudo ufw allow 443/tcp comment 'HTTPS / WSS Signaling'
sudo ufw allow 3478/udp comment 'STUN / Embedded TURN'
sudo ufw --force enable

# 3. Detect Public IP
PUBLIC_IP=$(curl -s https://api.ipify.org || curl -s https://ifconfig.me || echo "127.0.0.1")
echo "[INFO] Detected Public IP: ${PUBLIC_IP}"

# 4. Install Go 1.22+ if not installed
if ! command -v go &> /dev/null; then
    echo "[INFO] Installing Go 1.22.8..."
    wget -q https://go.dev/dl/go1.22.8.linux-amd64.tar.gz
    sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.22.8.linux-amd64.tar.gz
    rm go1.22.8.linux-amd64.tar.gz
    echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
    export PATH=$PATH:/usr/local/go/bin
fi

# 5. Create unicompute service user and directory
sudo useradd -r -s /bin/false -d /opt/unicompute unicompute || true
sudo mkdir -p /opt/unicompute
sudo chown -R unicompute:unicompute /opt/unicompute

# 6. Build coordinator binary
echo "[INFO] Compiling unicompute-coordinator..."
CGO_ENABLED=0 go build -o /tmp/unicompute-coordinator ./cmd/unicompute-coordinator
sudo mv /tmp/unicompute-coordinator /opt/unicompute/unicompute-coordinator
sudo chmod +x /opt/unicompute/unicompute-coordinator
sudo chown unicompute:unicompute /opt/unicompute/unicompute-coordinator

# 7. Install Environment Config
sudo bash -c "cat <<EOF > /etc/default/unicompute-coordinator
PUBLIC_IP=${PUBLIC_IP}
UNICOMPUTE_LISTEN=0.0.0.0:8080
UNICOMPUTE_PUBLIC_IP=${PUBLIC_IP}
UNICOMPUTE_REALM=unicompute.edu
EOF"

# 8. Install Systemd Service
sudo cp deploy/unicompute-coordinator.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable unicompute-coordinator
sudo systemctl restart unicompute-coordinator

echo "[INFO] Service status:"
sudo systemctl status unicompute-coordinator --no-pager

echo ""
echo "==================================================================="
echo " Coordinator successfully deployed and running!"
echo " Public IP:     ${PUBLIC_IP}"
echo " Signaling URL: ws://${PUBLIC_IP}:8080/ws"
echo " Health Check:  http://${PUBLIC_IP}:8080/health"
echo "==================================================================="
