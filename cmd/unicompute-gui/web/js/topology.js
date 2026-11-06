/**
 * UniCompute Fleet Topology Canvas Engine
 * Design System: Decentralized Compute Workstation
 * 100% Grounded in Live Go Substrate State (Zero Randomizer Mocks)
 */

class TopologyRenderer {
  constructor(canvasId, containerId) {
    this.canvas = document.getElementById(canvasId);
    this.container = document.getElementById(containerId);
    this.ctx = this.canvas.getContext('2d');

    this.nodes = [];         // Fleet peer rigs
    this.peers = [];         // Connected P2P pairs with metrics
    this.particles = [];     // Animated data packets along active P2P links
    this.selectedNode = null;
    this.hoveredNode = null;

    this.zoom = 1.0;
    this.panX = 0;
    this.panY = 0;
    this.isDragging = false;
    this.dragStartX = 0;
    this.dragStartY = 0;

    this.coordinator = {
      id: 'COORDINATOR',
      x: 0,
      y: 0,
      radius: 44,
      status: 'ONLINE',
      pulse: 0
    };

    this.initCanvasSize();
    this.bindEvents();
    this.startAnimationLoop();
  }

  initCanvasSize() {
    const rect = this.container.getBoundingClientRect();
    this.dpr = window.devicePixelRatio || 1;
    this.canvas.width = rect.width * this.dpr;
    this.canvas.height = rect.height * this.dpr;
    this.ctx.scale(this.dpr, this.dpr);

    this.width = rect.width;
    this.height = rect.height;
    this.coordinator.x = this.width / 2;
    this.coordinator.y = this.height / 2;
  }

  bindEvents() {
    window.addEventListener('resize', () => this.initCanvasSize());

    this.canvas.addEventListener('mousedown', (e) => {
      const pos = this.getCanvasPos(e);
      const clicked = this.hitTest(pos.x, pos.y);
      if (clicked && clicked !== this.coordinator) {
        this.selectedNode = clicked;
        if (this.onNodeClick) this.onNodeClick(clicked);
      } else {
        this.isDragging = true;
        this.dragStartX = e.clientX - this.panX;
        this.dragStartY = e.clientY - this.panY;
      }
    });

    window.addEventListener('mousemove', (e) => {
      if (this.isDragging) {
        this.panX = e.clientX - this.dragStartX;
        this.panY = e.clientY - this.dragStartY;
        return;
      }

      const pos = this.getCanvasPos(e);
      const hit = this.hitTest(pos.x, pos.y);
      if (hit !== this.hoveredNode) {
        this.hoveredNode = hit;
        if (this.onNodeHover) this.onNodeHover(hit, e.clientX, e.clientY);
      }
    });

    window.addEventListener('mouseup', () => {
      this.isDragging = false;
    });

    this.canvas.addEventListener('wheel', (e) => {
      e.preventDefault();
      const zoomFactor = e.deltaY < 0 ? 1.08 : 0.92;
      this.zoom = Math.max(0.5, Math.min(2.5, this.zoom * zoomFactor));
    });
  }

  getCanvasPos(e) {
    const rect = this.canvas.getBoundingClientRect();
    const rawX = e.clientX - rect.left;
    const rawY = e.clientY - rect.top;
    return {
      x: (rawX - this.coordinator.x - this.panX) / this.zoom + this.coordinator.x,
      y: (rawY - this.coordinator.y - this.panY) / this.zoom + this.coordinator.y
    };
  }

  updateFleet(fleetNodes, connectedPeers, localId, localStatus = null) {
    const orbitRadius = Math.min(this.width, this.height) * 0.36;
    const count = fleetNodes.length;

    // Build map of connected peer objects
    const peerMetricsMap = new Map();
    connectedPeers.forEach(p => {
      const pid = p.peer_id || p;
      peerMetricsMap.set(pid, p);
    });

    const existingMap = new Map(this.nodes.map(n => [n.id, n]));
    const updated = [];

    fleetNodes.forEach((nodeId, idx) => {
      const angle = (idx / Math.max(1, count)) * Math.PI * 2 - Math.PI / 2;
      const targetX = this.coordinator.x + Math.cos(angle) * orbitRadius;
      const targetY = this.coordinator.y + Math.sin(angle) * orbitRadius;

      const isLocal = nodeId === localId;
      const peerData = peerMetricsMap.get(nodeId);
      const connectedP2P = !!peerData;

      let node = existingMap.get(nodeId);
      if (!node) {
        node = {
          id: nodeId,
          x: targetX,
          y: targetY,
          width: 140,
          height: 76,
          isLocal: isLocal,
          connectedP2P: connectedP2P,
          metrics: peerData ? peerData.metrics : null
        };
      } else {
        node.isLocal = isLocal;
        node.connectedP2P = connectedP2P;
        node.metrics = peerData ? peerData.metrics : null;
        node.x += (targetX - node.x) * 0.12;
        node.y += (targetY - node.y) * 0.12;
      }

      // Ground local node metrics directly in real status
      if (isLocal && localStatus) {
        node.storageMB = localStatus.storage ? Math.round(localStatus.storage.used_bytes / (1024 * 1024)) : 0;
        node.quotaMB = localStatus.storage ? Math.round(localStatus.storage.quota_bytes / (1024 * 1024)) : 10240;
        node.blobs = localStatus.storage ? localStatus.storage.blob_count : 0;
        node.gpuActive = localStatus.gpu_enabled;
        node.dockerActive = localStatus.docker_available;
      }

      updated.push(node);
    });

    this.nodes = updated;
    this.peers = connectedPeers;

    // Spawn packets only along genuinely established P2P connections
    const connectedList = this.nodes.filter(n => n.connectedP2P);
    if (connectedList.length > 0 && Math.random() < 0.25) {
      const nodeA = this.nodes.find(n => n.isLocal) || this.coordinator;
      const nodeB = connectedList[Math.floor(Math.random() * connectedList.length)];
      if (nodeA && nodeB && nodeA !== nodeB) {
        this.particles.push({
          x: nodeA.x,
          y: nodeA.y,
          targetX: nodeB.x,
          targetY: nodeB.y,
          progress: 0,
          color: '#10b981',
          speed: 0.024
        });
      }
    }
  }

  hitTest(x, y) {
    const dxCoord = x - this.coordinator.x;
    const dyCoord = y - this.coordinator.y;
    if (Math.sqrt(dxCoord * dxCoord + dyCoord * dyCoord) <= this.coordinator.radius) {
      return this.coordinator;
    }

    for (const node of this.nodes) {
      const halfW = node.width / 2;
      const halfH = node.height / 2;
      if (x >= node.x - halfW && x <= node.x + halfW && y >= node.y - halfH && y <= node.y + halfH) {
        return node;
      }
    }
    return null;
  }

  startAnimationLoop() {
    const render = () => {
      this.draw();
      requestAnimationFrame(render);
    };
    requestAnimationFrame(render);
  }

  draw() {
    this.ctx.clearRect(0, 0, this.width, this.height);

    this.ctx.save();
    this.ctx.translate(this.coordinator.x + this.panX, this.coordinator.y + this.panY);
    this.ctx.scale(this.zoom, this.zoom);
    this.ctx.translate(-this.coordinator.x, -this.coordinator.y);

    // 1. Concentric background orbital guide rings
    this.drawOrbitRings();

    // 2. Control plane signaling conduits
    this.drawSignalingConduits();

    // 3. Real established P2P WebRTC DataChannel laser beams
    this.drawP2PMeshLinks();

    // 4. Data packet particles
    this.drawDataParticles();

    // 5. Central Hub Coordinator
    this.drawCoordinatorTower();

    // 6. Workstation Nodes
    this.drawPeerRigs();

    this.ctx.restore();
  }

  drawOrbitRings() {
    const c = this.coordinator;
    const orbitRadius = Math.min(this.width, this.height) * 0.36;

    this.ctx.save();
    this.ctx.beginPath();
    this.ctx.arc(c.x, c.y, orbitRadius, 0, Math.PI * 2);
    this.ctx.strokeStyle = '#161f33';
    this.ctx.lineWidth = 1.5;
    this.ctx.setLineDash([4, 6]);
    this.ctx.stroke();

    this.ctx.beginPath();
    this.ctx.arc(c.x, c.y, orbitRadius * 0.55, 0, Math.PI * 2);
    this.ctx.strokeStyle = '#121b2f';
    this.ctx.lineWidth = 1;
    this.ctx.setLineDash([2, 4]);
    this.ctx.stroke();
    this.ctx.restore();
  }

  drawSignalingConduits() {
    for (const node of this.nodes) {
      this.ctx.save();
      this.ctx.beginPath();
      this.ctx.moveTo(this.coordinator.x, this.coordinator.y);
      this.ctx.lineTo(node.x, node.y);
      this.ctx.strokeStyle = 'rgba(56, 189, 248, 0.28)';
      this.ctx.lineWidth = 1.5;
      this.ctx.stroke();
      this.ctx.restore();
    }
  }

  drawP2PMeshLinks() {
    const localNode = this.nodes.find(n => n.isLocal);
    if (!localNode) return;

    this.ctx.save();
    this.ctx.strokeStyle = '#10b981';
    this.ctx.lineWidth = 2;
    this.ctx.setLineDash([6, 5]);
    this.ctx.lineDashOffset = -Date.now() / 40;
    this.ctx.shadowColor = 'rgba(16, 185, 129, 0.4)';
    this.ctx.shadowBlur = 6;

    // Draw active links from local node to connected peers
    for (const node of this.nodes) {
      if (node !== localNode && node.connectedP2P) {
        this.ctx.beginPath();
        this.ctx.moveTo(localNode.x, localNode.y);
        this.ctx.lineTo(node.x, node.y);
        this.ctx.stroke();
      }
    }
    this.ctx.restore();
  }

  drawDataParticles() {
    for (let i = this.particles.length - 1; i >= 0; i--) {
      const p = this.particles[i];
      p.progress += p.speed;
      if (p.progress >= 1) {
        this.particles.splice(i, 1);
        continue;
      }
      const curX = p.x + (p.targetX - p.x) * p.progress;
      const curY = p.y + (p.targetY - p.y) * p.progress;

      this.ctx.save();
      this.ctx.beginPath();
      this.ctx.arc(curX, curY, 4, 0, Math.PI * 2);
      this.ctx.fillStyle = p.color;
      this.ctx.shadowColor = p.color;
      this.ctx.shadowBlur = 8;
      this.ctx.fill();
      this.ctx.restore();
    }
  }

  drawCoordinatorTower() {
    const c = this.coordinator;
    c.pulse = (c.pulse + 0.035) % (Math.PI * 2);

    this.ctx.save();

    for (let r = 1; r <= 3; r++) {
      const waveRad = c.radius + r * 14 + Math.sin(c.pulse) * 3;
      this.ctx.beginPath();
      this.ctx.arc(c.x, c.y, waveRad, 0, Math.PI * 2);
      this.ctx.strokeStyle = `rgba(56, 189, 248, ${0.18 / r})`;
      this.ctx.lineWidth = 1;
      this.ctx.stroke();
    }

    this.ctx.beginPath();
    this.ctx.arc(c.x, c.y, c.radius, 0, Math.PI * 2);
    this.ctx.fillStyle = '#091326';
    this.ctx.strokeStyle = '#38bdf8';
    this.ctx.lineWidth = 2.5;
    this.ctx.shadowColor = '#38bdf8';
    this.ctx.shadowBlur = 18;
    this.ctx.fill();
    this.ctx.stroke();

    this.ctx.beginPath();
    this.ctx.arc(c.x, c.y, c.radius - 8, 0, Math.PI * 2);
    this.ctx.fillStyle = '#161f33';
    this.ctx.fill();

    this.ctx.font = '700 11px Inter, sans-serif';
    this.ctx.fillStyle = '#8ed5ff';
    this.ctx.textAlign = 'center';
    this.ctx.fillText('CENTRAL', c.x, c.y - 4);

    this.ctx.font = '600 10px JetBrains Mono, monospace';
    this.ctx.fillStyle = '#d9e2fd';
    this.ctx.fillText('HUB:4194', c.x, c.y + 11);

    this.ctx.font = '600 9px JetBrains Mono, monospace';
    this.ctx.fillStyle = '#38bdf8';
    this.ctx.fillText('[HEALTH & CONN]', c.x, c.y + c.radius + 16);

    this.ctx.restore();
  }

  drawPeerRigs() {
    for (const node of this.nodes) {
      this.ctx.save();

      const isHovered = this.hoveredNode === node;
      const isSelected = this.selectedNode === node;
      const x = node.x;
      const y = node.y;
      const w = node.width;
      const h = node.height;
      const halfW = w / 2;
      const halfH = h / 2;

      if (isHovered || isSelected || node.connectedP2P) {
        this.ctx.shadowColor = node.connectedP2P ? '#10b981' : '#38bdf8';
        this.ctx.shadowBlur = isSelected ? 18 : 10;
      }

      this.ctx.font = '700 10px JetBrains Mono, monospace';
      this.ctx.fillStyle = isSelected ? '#8ed5ff' : (node.isLocal ? '#38bdf8' : '#bdc8d1');
      this.ctx.textAlign = 'center';

      let label = node.id;
      if (node.isLocal) label += ' [LOCAL]';
      if (label.length > 18) label = label.substring(0, 16) + '…';
      this.ctx.fillText(label, x, y - halfH - 7);

      this.ctx.beginPath();
      this.roundRect(x - halfW, y - halfH, w, h, 6);
      this.ctx.fillStyle = '#0d172a';
      this.ctx.fill();

      this.ctx.strokeStyle = node.connectedP2P ? '#10b981' : (isSelected ? '#38bdf8' : '#1e293b');
      this.ctx.lineWidth = isSelected || node.connectedP2P ? 2 : 1;
      this.ctx.stroke();

      this.ctx.beginPath();
      this.ctx.moveTo(x - halfW, y);
      this.ctx.lineTo(x + halfW, y);
      this.ctx.strokeStyle = '#1e293b';
      this.ctx.lineWidth = 1;
      this.ctx.stroke();

      // Top Compartment
      this.ctx.font = '700 9px JetBrains Mono, monospace';
      this.ctx.fillStyle = '#e1bfff';
      this.ctx.textAlign = 'left';
      this.ctx.fillText('CODE', x - halfW + 10, y - halfH + 16);

      this.ctx.font = '600 8.5px JetBrains Mono, monospace';
      if (node.gpuActive) {
        this.ctx.fillStyle = 'rgba(56, 189, 248, 0.2)';
        this.roundRect(x + halfW - 48, y - halfH + 6, 40, 14, 3);
        this.ctx.fill();
        this.ctx.fillStyle = '#8ed5ff';
        this.ctx.textAlign = 'center';
        this.ctx.fillText('TIER 3', x + halfW - 28, y - halfH + 16);
      } else {
        this.ctx.fillStyle = 'rgba(16, 185, 129, 0.2)';
        this.roundRect(x + halfW - 54, y - halfH + 6, 46, 14, 3);
        this.ctx.fill();
        this.ctx.fillStyle = '#4edea3';
        this.ctx.textAlign = 'center';
        this.ctx.fillText('DOCKER', x + halfW - 31, y - halfH + 16);
      }

      this.ctx.font = '500 8.5px Inter, sans-serif';
      this.ctx.fillStyle = '#87929a';
      this.ctx.textAlign = 'left';
      this.ctx.fillText(node.connectedP2P ? 'P2P Linked' : (node.isLocal ? 'Host Node' : 'Standby Peer'), x - halfW + 10, y - 7);

      // Bottom Compartment: Real Storage / Link info
      this.ctx.font = '700 9px JetBrains Mono, monospace';
      this.ctx.fillStyle = '#4edea3';
      this.ctx.fillText('DATA', x - halfW + 10, y + 16);

      if (node.isLocal && node.quotaMB) {
        const barW = w - 20;
        const barH = 3;
        const barX = x - halfW + 10;
        const barY = y + 21;
        this.ctx.fillStyle = '#1e293b';
        this.roundRect(barX, barY, barW, barH, 1.5);
        this.ctx.fill();

        const pct = Math.min(1, (node.storageMB || 0) / node.quotaMB);
        this.ctx.fillStyle = '#10b981';
        this.roundRect(barX, barY, barW * pct, barH, 1.5);
        this.ctx.fill();

        this.ctx.font = '600 8px JetBrains Mono, monospace';
        this.ctx.fillStyle = '#87929a';
        this.ctx.fillText(`${(node.storageMB / 1024).toFixed(1)} GB • ${node.blobs} Blobs`, x - halfW + 10, y + 32);
      } else {
        this.ctx.font = '600 8px JetBrains Mono, monospace';
        this.ctx.fillStyle = node.connectedP2P ? '#10b981' : '#87929a';
        if (node.metrics && node.metrics.rtt_ms) {
          this.ctx.fillText(`RTT: ${node.metrics.rtt_ms}ms • Direct`, x - halfW + 10, y + 28);
        } else {
          this.ctx.fillText(node.connectedP2P ? 'SCTP DataChannel OK' : 'Signaling Only', x - halfW + 10, y + 28);
        }
      }

      if (node.connectedP2P) {
        this.ctx.fillStyle = '#10b981';
        this.ctx.beginPath();
        this.ctx.arc(x + halfW - 10, y + 26, 3.5, 0, Math.PI * 2);
        this.ctx.fill();
      }

      this.ctx.restore();
    }
  }

  roundRect(x, y, w, h, r) {
    if (w < 2 * r) r = w / 2;
    if (h < 2 * r) r = h / 2;
    this.ctx.beginPath();
    this.ctx.moveTo(x + r, y);
    this.ctx.arcTo(x + w, y, x + w, y + h, r);
    this.ctx.arcTo(x + w, y + h, x, y + h, r);
    this.ctx.arcTo(x, y + h, x, y, r);
    this.ctx.arcTo(x, y + w, y, r);
    this.ctx.closePath();
  }

  center() {
    this.panX = 0;
    this.panY = 0;
    this.zoom = 1.0;
  }
}
