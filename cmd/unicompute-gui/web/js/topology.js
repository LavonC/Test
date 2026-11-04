/**
 * UniCompute Fleet Topology Canvas Engine
 * Renders the central Coordinator Mainframe and orbiting Workstation Rigs with
 * dual Compute/Storage cores, glowing fiber conduits, and animated P2P laser beams.
 */

class TopologyRenderer {
  constructor(canvasId, containerId) {
    this.canvas = document.getElementById(canvasId);
    this.container = document.getElementById(containerId);
    this.ctx = this.canvas.getContext('2d');

    this.nodes = [];         // Fleet peer rigs
    this.peers = [];         // Connected P2P pairs
    this.particles = [];     // Animated data packets along P2P links
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
      radius: 46,
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

  updateFleet(fleetNodes, connectedPeers, localId) {
    const orbitRadius = Math.min(this.width, this.height) * 0.35;
    const count = fleetNodes.length;
    const peerMap = new Set(connectedPeers.map(p => p.peer_id || p));

    const existingMap = new Map(this.nodes.map(n => [n.id, n]));
    const updated = [];

    fleetNodes.forEach((nodeId, idx) => {
      const angle = (idx / Math.max(1, count)) * Math.PI * 2 - Math.PI / 2;
      const targetX = this.coordinator.x + Math.cos(angle) * orbitRadius;
      const targetY = this.coordinator.y + Math.sin(angle) * orbitRadius;

      let node = existingMap.get(nodeId);
      if (!node) {
        node = {
          id: nodeId,
          x: targetX,
          y: targetY,
          width: 140,
          height: 80,
          isLocal: nodeId === localId,
          connectedP2P: peerMap.has(nodeId),
          gpuActive: true,
          dockerActive: true,
          storageMB: Math.floor(Math.random() * 2048) + 256,
          quotaMB: 10240,
          blobs: Math.floor(Math.random() * 12) + 2
        };
      } else {
        node.connectedP2P = peerMap.has(nodeId);
        // Smoothly adjust towards target orbit slot
        node.x += (targetX - node.x) * 0.1;
        node.y += (targetY - node.y) * 0.1;
      }
      updated.push(node);
    });

    this.nodes = updated;
    this.peers = connectedPeers;

    // Spawn animated data/code packets along direct P2P mesh links between remote nodes
    if (this.nodes.length >= 2 && Math.random() < 0.3) {
      const idxA = Math.floor(Math.random() * this.nodes.length);
      const idxB = (idxA + 1) % this.nodes.length;
      const nodeA = this.nodes[idxA];
      const nodeB = this.nodes[idxB];
      if (nodeA && nodeB) {
        this.particles.push({
          x: nodeA.x,
          y: nodeA.y,
          targetX: nodeB.x,
          targetY: nodeB.y,
          progress: 0,
          color: Math.random() > 0.5 ? '#10B981' : '#00F2FE',
          speed: 0.025
        });
      }
    }
  }

  hitTest(x, y) {
    // Check central coordinator
    const dxCoord = x - this.coordinator.x;
    const dyCoord = y - this.coordinator.y;
    if (Math.sqrt(dxCoord * dxCoord + dyCoord * dyCoord) <= this.coordinator.radius) {
      return this.coordinator;
    }

    // Check rigs
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
    // Apply pan and zoom around center
    this.ctx.translate(this.coordinator.x + this.panX, this.coordinator.y + this.panY);
    this.ctx.scale(this.zoom, this.zoom);
    this.ctx.translate(-this.coordinator.x, -this.coordinator.y);

    // 1. Draw solid signaling conduits (Health / Conn from Central Coordinator)
    this.drawSignalingConduits();

    // 2. Draw dashed P2P perimeter mesh links (Direct Code/Data Channels between Remote PCs)
    this.drawP2PMeshLinks();

    // 3. Draw streaming data/code packets along P2P mesh
    this.drawDataParticles();

    // 4. Draw Central Coordinator Mainframe Tower (Health & Connection Hub)
    this.drawCoordinatorTower();

    // 5. Draw Orbiting Remote Nodes (Split Code / Data Compartments)
    this.drawPeerRigs();

    this.ctx.restore();
  }

  drawSignalingConduits() {
    // Solid control plane lines radiating from central Health/Conn Coordinator to all Remote Nodes
    for (const node of this.nodes) {
      this.ctx.save();
      this.ctx.beginPath();
      this.ctx.moveTo(this.coordinator.x, this.coordinator.y);
      this.ctx.lineTo(node.x, node.y);
      this.ctx.strokeStyle = 'rgba(0, 242, 254, 0.35)';
      this.ctx.lineWidth = 2;
      this.ctx.stroke();
      this.ctx.restore();
    }
  }

  drawP2PMeshLinks() {
    // Dashed data plane lines connecting remote nodes directly to each other in a P2P mesh
    const n = this.nodes.length;
    if (n < 2) return;

    this.ctx.save();
    this.ctx.strokeStyle = '#10B981';
    this.ctx.lineWidth = 2.5;
    this.ctx.setLineDash([8, 6]);
    this.ctx.lineDashOffset = -Date.now() / 35;
    this.ctx.shadowColor = 'rgba(16, 185, 129, 0.4)';
    this.ctx.shadowBlur = 8;

    for (let i = 0; i < n; i++) {
      const nextIdx = (i + 1) % n;
      const nodeA = this.nodes[i];
      const nodeB = this.nodes[nextIdx];

      // Draw dashed direct P2P link between remote nodes
      this.ctx.beginPath();
      this.ctx.moveTo(nodeA.x, nodeA.y);
      this.ctx.lineTo(nodeB.x, nodeB.y);
      this.ctx.stroke();
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
      this.ctx.arc(curX, curY, 4.5, 0, Math.PI * 2);
      this.ctx.fillStyle = p.color;
      this.ctx.shadowColor = p.color;
      this.ctx.shadowBlur = 10;
      this.ctx.fill();
      this.ctx.restore();
    }
  }

  drawCoordinatorTower() {
    const c = this.coordinator;
    c.pulse = (c.pulse + 0.03) % (Math.PI * 2);

    this.ctx.save();

    // Concentric pulsing radar rings
    for (let r = 1; r <= 3; r++) {
      const ringRadius = c.radius + r * 14 + Math.sin(c.pulse) * 4;
      this.ctx.beginPath();
      this.ctx.arc(c.x, c.y, ringRadius, 0, Math.PI * 2);
      this.ctx.strokeStyle = `rgba(0, 242, 254, ${0.15 / r})`;
      this.ctx.lineWidth = 1;
      this.ctx.stroke();
    }

    // Outer Hub Glow
    this.ctx.beginPath();
    this.ctx.arc(c.x, c.y, c.radius, 0, Math.PI * 2);
    this.ctx.fillStyle = '#0F172A';
    this.ctx.strokeStyle = '#00F2FE';
    this.ctx.lineWidth = 2.5;
    this.ctx.shadowColor = '#00F2FE';
    this.ctx.shadowBlur = 18;
    this.ctx.fill();
    this.ctx.stroke();

    // Central Circle Labels matching architecture: HEALTH / CONN / COORDINATOR
    this.ctx.font = '800 11px Inter, sans-serif';
    this.ctx.fillStyle = '#10B981';
    this.ctx.textAlign = 'center';
    this.ctx.fillText('HEALTH', c.x, c.y - 12);

    this.ctx.font = '800 11px Inter, sans-serif';
    this.ctx.fillStyle = '#00F2FE';
    this.ctx.fillText('CONN', c.x, c.y + 2);

    this.ctx.font = '700 9px JetBrains Mono, monospace';
    this.ctx.fillStyle = '#94A3B8';
    this.ctx.fillText('COORDINATOR', c.x, c.y + 17);

    // Bottom Status Pill
    this.ctx.font = '700 8.5px JetBrains Mono, monospace';
    this.ctx.fillStyle = '#38BDF8';
    this.ctx.fillText('RENDEZVOUS HUB', c.x, c.y + c.radius + 18);

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

      // Aura on Hover / Selection / Connected
      if (isHovered || isSelected || node.connectedP2P) {
        this.ctx.shadowColor = node.connectedP2P ? '#10B981' : '#00F2FE';
        this.ctx.shadowBlur = isSelected ? 20 : 12;
      }

      // Outer Tag: REMOTE PC [ID]
      this.ctx.font = '700 9px Inter, sans-serif';
      this.ctx.fillStyle = '#94A3B8';
      this.ctx.textAlign = 'center';
      this.ctx.fillText(`REMOTE PC • ${node.id.length > 10 ? node.id.substring(0, 8) + '…' : node.id}`, x, y - halfH - 8);

      // Chassis Body Background
      this.ctx.beginPath();
      this.roundRect(x - halfW, y - halfH, w, h, 8);
      this.ctx.fillStyle = '#0B1120';
      this.ctx.fill();

      // Border: Emerald if connected P2P, Cyan if selected, subtle slate otherwise
      this.ctx.strokeStyle = node.connectedP2P ? '#10B981' : (isSelected ? '#00F2FE' : 'rgba(255, 255, 255, 0.18)');
      this.ctx.lineWidth = isSelected || node.connectedP2P ? 2 : 1;
      this.ctx.stroke();

      // Horizontal Divider separating CODE compartment (top) and DATA compartment (bottom)
      this.ctx.beginPath();
      this.ctx.moveTo(x - halfW, y);
      this.ctx.lineTo(x + halfW, y);
      this.ctx.strokeStyle = 'rgba(255, 255, 255, 0.15)';
      this.ctx.lineWidth = 1;
      this.ctx.stroke();

      // ---- UPPER COMPARTMENT: CODE ----
      this.ctx.font = '800 10px JetBrains Mono, monospace';
      this.ctx.fillStyle = '#A78BFA'; // Violet
      this.ctx.textAlign = 'left';
      this.ctx.fillText('CODE', x - halfW + 10, y - halfH + 16);

      // Compute tier badge
      this.ctx.font = '700 8px Inter, sans-serif';
      if (node.gpuActive) {
        this.ctx.fillStyle = 'rgba(139, 92, 246, 0.25)';
        this.roundRect(x + halfW - 48, y - halfH + 6, 40, 14, 3);
        this.ctx.fill();
        this.ctx.fillStyle = '#DDD6FE';
        this.ctx.textAlign = 'center';
        this.ctx.fillText('GPU T3', x + halfW - 28, y - halfH + 16);
      } else {
        this.ctx.fillStyle = 'rgba(59, 130, 246, 0.25)';
        this.roundRect(x + halfW - 54, y - halfH + 6, 46, 14, 3);
        this.ctx.fill();
        this.ctx.fillStyle = '#BFDBFE';
        this.ctx.textAlign = 'center';
        this.ctx.fillText('CPU/DOCKER', x + halfW - 31, y - halfH + 16);
      }

      this.ctx.font = '500 8.5px Inter, sans-serif';
      this.ctx.fillStyle = '#94A3B8';
      this.ctx.textAlign = 'left';
      this.ctx.fillText('EXEC ENGINE • IDLE', x - halfW + 10, y - 7);

      // ---- LOWER COMPARTMENT: DATA ----
      this.ctx.font = '800 10px JetBrains Mono, monospace';
      this.ctx.fillStyle = '#34D399'; // Emerald
      this.ctx.fillText('DATA', x - halfW + 10, y + 16);

      // Storage quota progress bar
      const barW = w - 20;
      const barH = 4;
      const barX = x - halfW + 10;
      const barY = y + 21;
      this.ctx.fillStyle = 'rgba(255, 255, 255, 0.08)';
      this.roundRect(barX, barY, barW, barH, 2);
      this.ctx.fill();

      const pct = Math.min(1, node.storageMB / node.quotaMB);
      this.ctx.fillStyle = '#10B981';
      this.roundRect(barX, barY, barW * pct, barH, 2);
      this.ctx.fill();

      // Storage text info
      this.ctx.font = '600 8px JetBrains Mono, monospace';
      this.ctx.fillStyle = '#94A3B8';
      this.ctx.fillText(`${(node.storageMB / 1024).toFixed(1)}GB • ${node.blobs} Chunks`, x - halfW + 10, y + 33);

      // P2P Connected Indicator Pill
      if (node.connectedP2P) {
        this.ctx.fillStyle = '#10B981';
        this.ctx.beginPath();
        this.ctx.arc(x + halfW - 10, y + 27, 4, 0, Math.PI * 2);
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
    this.ctx.arcTo(x, y, x + w, y, r);
    this.ctx.closePath();
  }

  center() {
    this.panX = 0;
    this.panY = 0;
    this.zoom = 1.0;
  }
}
