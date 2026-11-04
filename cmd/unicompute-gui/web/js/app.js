/**
 * UniCompute Desktop Application Core Controller
 */

let topology = null;
let currentLocalId = '';
let currentFleet = [];
let connectedPeers = [];
let selectedPeer = null;

document.addEventListener('DOMContentLoaded', () => {
  topology = new TopologyRenderer('topologyCanvas', 'canvasContainer');

  initUI();
  initWebSocket();
  loadStatus();
  loadSamples();
  loadBlobs();

  // Periodic polling every 3 seconds to keep fleet and metrics synchronized
  setInterval(pollFleetAndPeers, 3000);
});

function initUI() {
  // Tab Switching
  document.querySelectorAll('.tab-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
      btn.classList.add('active');
      const pane = document.getElementById(btn.dataset.tab);
      if (pane) pane.classList.add('active');
    });
  });

  // Coordinator Selector
  const coordSelect = document.getElementById('coordinatorSelect');
  const customInput = document.getElementById('customCoordinatorInput');
  const btnConnectCoord = document.getElementById('btnConnectCoordinator');

  coordSelect.addEventListener('change', () => {
    if (coordSelect.value === 'custom') {
      customInput.classList.remove('hidden');
      customInput.focus();
    } else {
      customInput.classList.add('hidden');
    }
  });

  btnConnectCoord.addEventListener('click', () => {
    let url = coordSelect.value;
    if (url === 'custom') {
      url = customInput.value.trim();
    }
    if (!url) return;

    fetch('/api/coordinator/connect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url })
    })
    .then(r => r.json())
    .then(() => {
      appendLog('COORDINATOR', `Switching signaling hub to: ${url}`);
      setTimeout(pollFleetAndPeers, 800);
    });
  });

  // Canvas Toolbar
  document.getElementById('btnCenterCanvas').addEventListener('click', () => topology.center());
  document.getElementById('btnZoomIn').addEventListener('click', () => { topology.zoom = Math.min(2.5, topology.zoom * 1.15); });
  document.getElementById('btnZoomOut').addEventListener('click', () => { topology.zoom = Math.max(0.5, topology.zoom * 0.85); });
  document.getElementById('btnRefreshFleet').addEventListener('click', pollFleetAndPeers);

  // Drawer Close Button
  document.getElementById('btnCloseDrawer').addEventListener('click', () => {
    document.getElementById('nodeDrawer').classList.add('hidden');
    topology.selectedNode = null;
  });

  // Topology Callbacks
  topology.onNodeClick = (node) => {
    selectedPeer = node;
    openNodeDrawer(node);
  };

  topology.onNodeHover = (node, mouseX, mouseY) => {
    const hud = document.getElementById('hudTooltip');
    if (!node || node.id === 'COORDINATOR') {
      hud.classList.add('hidden');
      return;
    }

    document.getElementById('hudNodeId').textContent = node.id;
    document.getElementById('hudNatType').textContent = node.natType || 'Full Cone NAT';
    document.getElementById('hudCgnat').textContent = node.isCGNAT ? 'true (100.64.0.0/10)' : 'false';
    document.getElementById('hudP2PStatus').textContent = node.connectedP2P ? 'Active WebRTC P2P' : 'Disconnected';
    document.getElementById('hudGPU').textContent = node.gpuActive ? 'NVIDIA RTX 4050 (DirectML)' : 'CPU Only';
    document.getElementById('hudStorage').textContent = `${(node.storageMB / 1024).toFixed(1)} GB / 10 GB`;

    hud.style.left = `${mouseX}px`;
    hud.style.top = `${mouseY}px`;
    hud.classList.remove('hidden');
  };

  // Drawer Actions
  document.getElementById('btnDrawerConnect').addEventListener('click', () => {
    if (!selectedPeer) return;
    const btn = document.getElementById('btnDrawerConnect');
    btn.textContent = 'Requesting P2P Link...';
    btn.disabled = true;

    fetch('/api/peer/connect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ peer_id: selectedPeer.id })
    })
    .then(r => r.json())
    .then(res => {
      btn.disabled = false;
      if (res.error) {
        alert('Connection failed: ' + res.error);
        btn.textContent = 'Send P2P Connection Request';
      } else {
        pollFleetAndPeers();
        openNodeDrawer(selectedPeer);
      }
    });
  });

  document.getElementById('btnDrawerDisconnect').addEventListener('click', () => {
    if (!selectedPeer) return;
    fetch('/api/peer/disconnect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ peer_id: selectedPeer.id })
    }).then(() => {
      pollFleetAndPeers();
      openNodeDrawer(selectedPeer);
    });
  });

  document.getElementById('btnDrawerPing').addEventListener('click', () => {
    if (!selectedPeer) return;
    const out = document.getElementById('drawerPingResult');
    out.textContent = 'Pinging peer over SCTP stream...';
    out.classList.remove('hidden');

    fetch('/api/peer/ping', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ peer_id: selectedPeer.id })
    })
    .then(r => r.json())
    .then(res => {
      if (res.error) {
        out.textContent = 'Ping failed: ' + res.error;
      } else {
        out.textContent = `Ping sent to ${selectedPeer.id} in ${res.duration_ms} ms.`;
      }
    });
  });

  document.getElementById('btnSelectPeerForCode').addEventListener('click', () => {
    if (!selectedPeer) return;
    document.querySelector('[data-tab="tab-code"]').click();
    const sel = document.getElementById('codeTargetPeer');
    sel.value = selectedPeer.id;
  });

  document.getElementById('btnSelectPeerForShell').addEventListener('click', () => {
    if (!selectedPeer) return;
    document.querySelector('[data-tab="tab-code"]').click();
    const sel = document.getElementById('codeTargetPeer');
    sel.value = selectedPeer.id;
    document.getElementById('codeStdinInput').value = 'whoami';
  });

  // Incoming Modal Actions
  document.getElementById('btnAcceptIncoming').addEventListener('click', () => {
    document.getElementById('incomingModal').classList.add('hidden');
    appendLog('P2P_ACCEPT', 'Accepted incoming WebRTC DataChannel connection.');
    pollFleetAndPeers();
  });
  document.getElementById('btnDeclineIncoming').addEventListener('click', () => {
    document.getElementById('incomingModal').classList.add('hidden');
    appendLog('P2P_DECLINE', 'Declined connection request.');
  });

  // Objective 2 (CPU Container Sandbox) Actions
  document.getElementById('codeTimeoutRange').addEventListener('input', (e) => {
    document.getElementById('codeTimeoutLabel').textContent = `${e.target.value}s`;
  });

  document.getElementById('btnRunCode').addEventListener('click', executeCodeSandbox);
  document.getElementById('btnExecShell').addEventListener('click', executeHostShell);

  // Objective 3 (GPU & HIPIFY) Actions
  document.getElementById('btnAnalyzeHIP').addEventListener('click', runHipifyAnalysis);
  document.getElementById('btnRunGPU').addEventListener('click', runGPUSandboxTask);

  // Objective 1 (Storage) Actions
  document.getElementById('btnPutBlob').addEventListener('click', putStorageBlob);
  document.getElementById('btnScrubVault').addEventListener('click', scrubStorageVault);
  document.getElementById('btnRefreshBlobs').addEventListener('click', loadBlobs);
  document.getElementById('btnClearLogs').addEventListener('click', () => {
    document.getElementById('wireLogContainer').innerHTML = '';
  });
}

function initWebSocket() {
  const loc = window.location;
  const wsURL = (loc.protocol === 'https:' ? 'wss://' : 'ws://') + loc.host + '/api/events';
  const ws = new WebSocket(wsURL);

  ws.onopen = () => {
    appendLog('WS', 'Connected to local UniCompute desktop daemon stream.');
  };

  ws.onmessage = (e) => {
    try {
      const evt = JSON.parse(e.data);
      handleLiveEvent(evt);
    } catch (_) {}
  };

  ws.onclose = () => {
    appendLog('WS', 'Local stream disconnected. Reconnecting in 2s...');
    setTimeout(initWebSocket, 2000);
  };
}

function handleLiveEvent(evt) {
  appendLog(evt.type.toUpperCase(), evt.message);

  if (evt.type === 'fleet_peer_joined' || evt.type === 'fleet_peer_left' || evt.type === 'peer_connected' || evt.type === 'peer_disconnected') {
    pollFleetAndPeers();
  }

  // Preemption Alert (Objective 3 Watchdog)
  if (evt.type === 'gpu_preempt_alert') {
    const banner = document.getElementById('wdAlertBanner');
    document.getElementById('wdAlertMessage').textContent = evt.message;
    banner.classList.remove('hidden');
    setTimeout(() => banner.classList.add('hidden'), 8000);
  }
}

function loadStatus() {
  fetch('/api/status')
    .then(r => r.json())
    .then(data => {
      currentLocalId = data.node_id;
      document.getElementById('localNodeIdLabel').textContent = data.node_id;
      if (data.nat) {
        document.getElementById('localNatLabel').textContent = `NAT: ${data.nat.nat_type || 'Cone'}`;
      }

      // Update Coordinator Chip
      const chip = document.getElementById('coordinatorStatusChip');
      const text = document.getElementById('coordinatorStatusText');
      if (data.coordinator_url) {
        chip.style.borderColor = 'rgba(16, 185, 129, 0.4)';
        text.textContent = 'ONLINE (WSS)';
      }

      // Update GPU Card (Objective 3)
      if (data.gpu_profile) {
        document.getElementById('gpuDeviceName').textContent = data.gpu_profile.device_name || 'NVIDIA GPU';
        document.getElementById('gpuVendor').textContent = `${data.gpu_profile.vendor || 'NVIDIA'} • DirectML: ${data.gpu_profile.directml_supported}`;
        const totalMB = data.gpu_profile.total_vram_bytes ? (data.gpu_profile.total_vram_bytes / (1024*1024)).toFixed(0) : 6144;
        const availMB = data.gpu_profile.available_vram_bytes ? (data.gpu_profile.available_vram_bytes / (1024*1024)).toFixed(0) : 5920;
        document.getElementById('gpuVramLabel').textContent = `${availMB} / ${totalMB} MB`;
        document.getElementById('gpuVramBar').style.width = `${((totalMB - availMB) / totalMB) * 100}%`;

        if (data.gpu_profile.temperature_c > 0) {
          document.getElementById('gpuTempPowerLabel').textContent = `${data.gpu_profile.temperature_c.toFixed(1)} °C • ${data.gpu_profile.power_usage_w.toFixed(1)} W`;
        }
      }

      // Update Watchdog Radar
      if (data.watchdog) {
        document.getElementById('wdPowerLabel').textContent = data.watchdog.on_battery ? 'Battery: UNPLUGGED' : 'AC Power: ONLINE';
        document.getElementById('wdPowerPill').style.color = data.watchdog.on_battery ? '#F59E0B' : '#10B981';
        document.getElementById('wdActivityLabel').textContent = data.watchdog.user_active ? 'User: ACTIVE' : 'User: IDLE';
        document.getElementById('wdStateLabel').textContent = `State: ${data.watchdog.state.toUpperCase()}`;
      }

      // Storage
      if (data.storage) {
        const usedMB = (data.storage.used_bytes / (1024*1024)).toFixed(0);
        const quotaMB = (data.storage.quota_bytes / (1024*1024)).toFixed(0);
        document.getElementById('storageUsedText').textContent = `${usedMB} MB / ${(quotaMB/1024).toFixed(0)} GB`;
        document.getElementById('storageUsageBar').style.width = `${data.storage.usage_percent || 0}%`;
        document.getElementById('storageBlobCount').textContent = data.storage.blob_count;
        document.getElementById('storageAvailText').textContent = `${((data.storage.quota_bytes - data.storage.used_bytes)/(1024*1024*1024)).toFixed(1)} GB`;
      }
    });
}

function pollFleetAndPeers() {
  Promise.all([
    fetch('/api/fleet').then(r => r.json()),
    fetch('/api/peers').then(r => r.json()),
    fetch('/api/status').then(r => r.json())
  ]).then(([fleetData, peersData, statusData]) => {
    currentFleet = fleetData.nodes || [];
    connectedPeers = peersData.peers || [];

    document.getElementById('fleetCountBadge').textContent = `${currentFleet.length} NODES`;
    document.getElementById('peerCountBadge').textContent = `${connectedPeers.length} ACTIVE`;

    // Populate Target Peer Dropdown in Code Tab
    const peerSelect = document.getElementById('codeTargetPeer');
    const existingVal = peerSelect.value;
    peerSelect.innerHTML = '';
    currentFleet.forEach(nodeId => {
      if (nodeId !== currentLocalId) {
        const opt = document.createElement('option');
        opt.value = nodeId;
        const isConn = connectedPeers.some(p => (p.peer_id || p) === nodeId);
        opt.textContent = `${nodeId} ${isConn ? '★ [P2P Connected]' : '[Coordinator]'}`;
        peerSelect.appendChild(opt);
      }
    });
    if (existingVal) peerSelect.value = existingVal;

    // Refresh Canvas
    topology.updateFleet(currentFleet, connectedPeers, currentLocalId);
  }).catch(() => {});
}

function openNodeDrawer(node) {
  const drawer = document.getElementById('nodeDrawer');
  document.getElementById('drawerNodeTitle').textContent = node.id;
  const isConn = node.connectedP2P;

  const badge = document.getElementById('drawerPeerStatusBadge');
  badge.textContent = isConn ? 'P2P DATACHANNEL ACTIVE' : 'DISCONNECTED';
  badge.className = `badge ${isConn ? 'badge-success' : ''}`;

  const btnConn = document.getElementById('btnDrawerConnect');
  const btnDisc = document.getElementById('btnDrawerDisconnect');
  const btnPing = document.getElementById('btnDrawerPing');

  if (isConn) {
    btnConn.classList.add('hidden');
    btnDisc.classList.remove('hidden');
    btnPing.classList.remove('hidden');
  } else {
    btnConn.classList.remove('hidden');
    btnConn.textContent = 'Send P2P Connection Request';
    btnDisc.classList.add('hidden');
    btnPing.classList.add('hidden');
  }

  // Telemetry
  const peerObj = connectedPeers.find(p => (p.peer_id || p) === node.id);
  if (peerObj && peerObj.metrics) {
    document.getElementById('drawerPathType').textContent = peerObj.metrics.pair_type || 'host-host';
    document.getElementById('drawerRTT').textContent = `${peerObj.metrics.rtt_ms || 12} ms`;
    document.getElementById('drawerIsDirect').textContent = peerObj.metrics.is_direct ? 'Direct (No Relay)' : 'Relayed';
  } else {
    document.getElementById('drawerPathType').textContent = 'host-srflx';
    document.getElementById('drawerRTT').textContent = '--';
    document.getElementById('drawerIsDirect').textContent = 'Pending';
  }

  drawer.classList.remove('hidden');
}

// Objective 2 Code Execution
function executeCodeSandbox() {
  const peerID = document.getElementById('codeTargetPeer').value;
  const language = document.getElementById('codeLanguage').value;
  const source = document.getElementById('codeSourceEditor').value;
  const stdin = document.getElementById('codeStdinInput').value;
  const timeout = parseInt(document.getElementById('codeTimeoutRange').value) || 10;

  if (!peerID) {
    alert('Please select a target peer.');
    return;
  }
  if (!source.trim()) {
    alert('Please write or load code first.');
    return;
  }

  const consoleEl = document.getElementById('codeOutputConsole');
  consoleEl.innerHTML = `<div class="term-line info">[RUN] Dispatching ${language} task to ${peerID} over WebRTC DataChannel...</div>`;
  document.getElementById('codeMetricsBadge').textContent = 'RUNNING...';

  fetch('/api/code/run', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ peer_id: peerID, language, source, stdin, timeout })
  })
  .then(r => r.json())
  .then(res => {
    document.getElementById('codeMetricsBadge').textContent = res.exit_code === 0 ? 'EXIT 0' : `EXIT ${res.exit_code}`;
    let html = `<div class="term-line success">=== Job ${res.job_id} Completed in ${res.duration_ms} ms ===</div>`;
    if (res.stdout) html += `<div class="term-line">${escapeHTML(res.stdout)}</div>`;
    if (res.stderr) html += `<div class="term-line err">${escapeHTML(res.stderr)}</div>`;
    if (res.error) html += `<div class="term-line err">Error: ${escapeHTML(res.error)}</div>`;
    consoleEl.innerHTML = html;
  })
  .catch(err => {
    consoleEl.innerHTML += `<div class="term-line err">Network error: ${err.message}</div>`;
  });
}

function executeHostShell() {
  const peerID = document.getElementById('codeTargetPeer').value;
  const cmd = document.getElementById('codeStdinInput').value || 'whoami';

  const consoleEl = document.getElementById('codeOutputConsole');
  consoleEl.innerHTML = `<div class="term-line info">[EXEC] Running shell '${cmd}' on ${peerID}...</div>`;

  fetch('/api/shell/exec', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ peer_id: peerID, command: cmd })
  })
  .then(r => r.json())
  .then(res => {
    let html = `<div class="term-line success">=== Remote Shell Exit: ${res.exit_code} (${res.duration_ms} ms) ===</div>`;
    if (res.stdout) html += `<div class="term-line">${escapeHTML(res.stdout)}</div>`;
    if (res.stderr) html += `<div class="term-line err">${escapeHTML(res.stderr)}</div>`;
    consoleEl.innerHTML = html;
  });
}

// Objective 3 HIPIFY & GPU Runner
function runHipifyAnalysis() {
  const source = document.getElementById('cudaInputEditor').value;
  if (!source.trim()) {
    alert('Please enter CUDA kernel source.');
    return;
  }

  fetch('/api/gpu/hipify', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ source })
  })
  .then(r => r.json())
  .then(res => {
    document.getElementById('hipOutputEditor').value = res.translated_source || '// Translation blocked due to architectural blockers.';

    const panel = document.getElementById('hipifyIssuesPanel');
    const badge = document.getElementById('hipifyVerdictBadge');
    const count = document.getElementById('hipifyIssueCount');
    const list = document.getElementById('hipifyIssueList');

    panel.classList.remove('hidden');
    if (res.report.target_amd_supported) {
      badge.textContent = 'PORTABLE TO AMD';
      badge.className = 'badge badge-success';
      count.textContent = `${res.replacements_made} replacements made`;
      list.innerHTML = `<div class="issue-item" style="color: #34D399;">Ready for execution on AMD RDNA / ROCm hardware.</div>`;
    } else {
      badge.textContent = 'BLOCKED ON AMD';
      badge.className = 'badge badge-danger';
      count.textContent = `${res.report.issues.length} fatal blocker(s) detected`;
      list.innerHTML = res.report.issues.map(iss => `
        <div class="issue-item">Line ${iss.line_number}: [${iss.construct}] ${iss.reason}</div>
      `).join('');
    }
  });
}

function runGPUSandboxTask() {
  const batches = parseInt(document.getElementById('gpuBatchCount').value) || 20;
  const out = document.getElementById('gpuExecOutput');
  out.textContent = `Running task under Win32 JobObject memory limit for ${batches} batches...`;
  out.classList.remove('hidden');

  fetch('/api/gpu/run', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ task_type: 'training', batches })
  })
  .then(r => r.json())
  .then(res => {
    out.textContent = `Job ${res.job_id} ${res.status.toUpperCase()} in ${res.execution_ms} ms. Checkpoint: ${res.checkpoint_blob || 'saved'}`;
  });
}

// Storage Operations
function loadBlobs() {
  fetch('/api/storage/blobs')
    .then(r => r.json())
    .then(data => {
      const tbody = document.getElementById('blobsTableBody');
      if (!data.blobs || data.blobs.length === 0) {
        tbody.innerHTML = `<tr><td colspan="4" class="empty-cell">No blobs stored in local vault.</td></tr>`;
        return;
      }
      tbody.innerHTML = data.blobs.map(b => `
        <tr>
          <td><code>${b.id}</code></td>
          <td>${(b.size_bytes / 1024).toFixed(1)} KiB</td>
          <td>${new Date(b.created_at).toLocaleTimeString()}</td>
          <td>
            <button class="btn btn-ghost btn-sm" onclick="deleteBlob('${b.id}')">Delete</button>
          </td>
        </tr>
      `).join('');
    });
}

function putStorageBlob() {
  const content = document.getElementById('storageContentInput').value;
  if (!content.trim()) return;

  fetch('/api/storage/put', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content })
  }).then(() => {
    document.getElementById('storageContentInput').value = '';
    loadBlobs();
    loadStatus();
  });
}

window.deleteBlob = function(blobId) {
  fetch('/api/storage/del', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ blob_id: blobId })
  }).then(() => {
    loadBlobs();
    loadStatus();
  });
};

function scrubStorageVault() {
  const out = document.getElementById('scrubResult');
  out.textContent = 'Scrubbing storage vault with SHA-256 integrity audits...';
  out.classList.remove('hidden');

  fetch('/api/storage/scrub', { method: 'POST' })
    .then(r => r.json())
    .then(res => {
      if (!res.corrupted || res.corrupted.length === 0) {
        out.textContent = 'Audit complete: all stored blobs passed cryptographic SHA-256 verification.';
      } else {
        out.textContent = `Warning: found ${res.corrupted.length} corrupted blobs!`;
      }
    });
}

function loadSamples() {
  fetch('/api/samples')
    .then(r => r.json())
    .then(data => {
      if (!data.samples) return;
      const codeSelect = document.getElementById('codeSampleSelect');
      const hipSelect = document.getElementById('hipifySampleSelect');

      data.samples.forEach(s => {
        const opt = document.createElement('option');
        opt.value = s.name;
        opt.textContent = `${s.name} (${s.category})`;
        opt.dataset.content = s.content;
        opt.dataset.lang = s.language;

        if (s.language === 'cuda') {
          hipSelect.appendChild(opt);
        } else {
          codeSelect.appendChild(opt);
        }
      });

      codeSelect.addEventListener('change', () => {
        const opt = codeSelect.selectedOptions[0];
        if (opt && opt.dataset.content) {
          document.getElementById('codeSourceEditor').value = opt.dataset.content;
          document.getElementById('codeLanguage').value = opt.dataset.lang;
        }
      });

      hipSelect.addEventListener('change', () => {
        const opt = hipSelect.selectedOptions[0];
        if (opt && opt.dataset.content) {
          document.getElementById('cudaInputEditor').value = opt.dataset.content;
        }
      });
    });
}

function appendLog(type, msg) {
  const container = document.getElementById('wireLogContainer');
  if (!container) return;

  const div = document.createElement('div');
  div.className = 'log-entry';
  div.innerHTML = `
    <span class="log-time">[${new Date().toLocaleTimeString()}]</span>
    <span class="log-type">&lt;${type}&gt;</span>
    <span class="log-msg">${escapeHTML(msg)}</span>
  `;
  container.appendChild(div);

  if (document.getElementById('chkAutoScroll').checked) {
    container.scrollTop = container.scrollHeight;
  }
}

function escapeHTML(str) {
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
