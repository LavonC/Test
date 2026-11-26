/**
 * UniCompute Desktop Application Core Controller
 * Design System: Decentralized Compute Workstation
 * Integrates Objectives 1, 2, and 3 with real Go backend APIs
 */

// Parse session token from URL hash or existing session storage
(function() {
  const hash = window.location.hash || '';
  const match = hash.match(/token=([a-fA-F0-9]+)/);
  if (match && match[1]) {
    sessionStorage.setItem('unicompute_gui_token', match[1]);
  }
})();

function getAuthToken() {
  return sessionStorage.getItem('unicompute_gui_token') || '';
}

// Intercept fetch to enforce Authorization and Content-Type on API calls
const _originalFetch = window.fetch;
window.fetch = function(url, options = {}) {
  const token = getAuthToken();
  const headers = new Headers(options.headers || {});
  if (token && !headers.has('Authorization')) {
    headers.set('Authorization', 'Bearer ' + token);
  }
  const method = (options.method || 'GET').toUpperCase();
  if ((method === 'POST' || method === 'PUT' || method === 'DELETE') && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  options.headers = headers;
  return _originalFetch(url, options);
};

function fetchWithAuth(url, options = {}) {
  return window.fetch(url, options);
}

let topology = null;
let currentLocalId = '';
let currentFleet = [];
let connectedPeers = [];
let selectedPeer = null;
let isLogPaused = false;
let currentLogFilter = 'all';
let lastStatusData = null;
let currentWorkerAllowlist = [];

document.addEventListener('DOMContentLoaded', () => {
  topology = new TopologyRenderer('topologyCanvas', 'canvasContainer');

  initUI();
  initWebSocket();
  pollFleetAndPeers();
  loadSamples();
  loadBlobs();
  loadCoordinatorStatus();
  loadWorkerSettings();
  loadTasks();

  // Periodic polling every 5 seconds to keep fleet and metrics synchronized without spamming Render
  setInterval(() => {
    pollFleetAndPeers();
    loadCoordinatorStatus();
    loadTasks();
  }, 5000);
});

function initUI() {
  // 1. Navigation Tab Switching
  document.querySelectorAll('.nav-tab-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      switchTab(btn.dataset.tab);
    });
  });

  // 2. Titlebar Controls
  initTitlebar();

  // 3. Topology Toolbar & Inspector Controls
  initTopologyControls();

  // 4. Coordinator Hub Controls
  initCoordinatorHub();

  // 5. Objective 2 (CPU Container Sandbox)
  initCodeSandbox();

  // 6. Objective 3 (GPU & HIPIFY)
  initGPUSubsystem();

  // 7. Storage & Reed-Solomon Shard Matrix (O1)
  initShardMatrix();
  initStorageVault();

  // 8. Cluster Task Queue & Matchmaking
  initTaskQueue();

  // 9. Wire Logs & Filter Controls
  initWireLogs();
}

function switchTab(tabId) {
  document.querySelectorAll('.nav-tab-btn').forEach(b => {
    if (b.dataset.tab === tabId) {
      b.classList.add('active');
    } else {
      b.classList.remove('active');
    }
  });

  document.querySelectorAll('.viewport-pane').forEach(p => {
    if (p.id === tabId) {
      p.classList.remove('hidden');
      p.classList.add('active');
    } else {
      p.classList.add('hidden');
      p.classList.remove('active');
    }
  });

  // Dual context pill synchronization
  const pillHealth = document.getElementById('pillHealthConn');
  const pillCode = document.getElementById('pillCodeData');
  if (tabId === 'tab-coordinator') {
    pillHealth?.classList.add('active');
    pillCode?.classList.remove('active');
    loadCoordinatorStatus();
    loadWorkerSettings();
  } else {
    pillCode?.classList.add('active');
    pillHealth?.classList.remove('active');
  }

  if (tabId === 'tab-topology' && topology) {
    topology.initCanvasSize();
  }
}

function initTitlebar() {
  const coordSelect = document.getElementById('coordinatorSelect');
  const customInput = document.getElementById('customCoordinatorInput');
  const btnConnectCoord = document.getElementById('btnConnectCoordinator');

  // Titlebar Dual Context Pills
  const pillHealth = document.getElementById('pillHealthConn');
  const pillCode = document.getElementById('pillCodeData');
  if (pillHealth) {
    pillHealth.addEventListener('click', () => switchTab('tab-coordinator'));
  }
  if (pillCode) {
    pillCode.addEventListener('click', () => switchTab('tab-code'));
  }

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
    .then(async r => {
      const data = await r.json().catch(() => ({}));
      if (!r.ok) {
        throw new Error(data.error || `HTTP ${r.status}`);
      }
      return data;
    })
    .then(data => {
      if (data && data.reconnect_error) {
        appendLog('COORDINATOR', `Failed to switch signaling hub to ${url}: ${data.reconnect_error}`);
      } else {
        appendLog('COORDINATOR', `Switching signaling hub to: ${url}`);
      }
      setTimeout(() => {
        pollFleetAndPeers();
        loadCoordinatorStatus();
      }, 600);
    })
    .catch(err => {
      appendLog('COORDINATOR', `Coordinator switch failed: ${err.message}`);
    });
  });

  document.getElementById('btnQuickWire').addEventListener('click', () => switchTab('tab-logs'));
  document.getElementById('btnQuickRefresh').addEventListener('click', () => {
    pollFleetAndPeers();
    loadBlobs();
    loadCoordinatorStatus();
    loadTasks();
  });

  // Window Mock Controls
  document.getElementById('winCloseBtn').addEventListener('click', () => {
    if (confirm('Close UniCompute Desktop Workstation window?')) {
      window.close();
    }
  });
  document.getElementById('winMinBtn').addEventListener('click', () => {
    appendLog('WINDOW', 'Workstation minimized to system tray.');
  });
  document.getElementById('winMaxBtn').addEventListener('click', () => {
    if (!document.fullscreenElement) {
      document.documentElement.requestFullscreen().catch(() => {});
    } else {
      document.exitFullscreen().catch(() => {});
    }
  });
}

function initTopologyControls() {
  document.getElementById('btnCenterCanvas').addEventListener('click', () => topology.center());
  document.getElementById('btnZoomIn').addEventListener('click', () => {
    topology.zoom = Math.min(2.5, topology.zoom * 1.15);
  });
  document.getElementById('btnZoomOut').addEventListener('click', () => {
    topology.zoom = Math.max(0.5, topology.zoom * 0.85);
  });
  document.getElementById('btnRefreshFleet').addEventListener('click', pollFleetAndPeers);

  // Inspector Close Button
  document.getElementById('btnCloseInspector').addEventListener('click', () => {
    document.getElementById('inspectorCard').classList.add('hidden');
    topology.selectedNode = null;
  });

  // Topology Callbacks
  topology.onNodeClick = (node) => {
    selectedPeer = node;
    openPCInspector(node);
  };

  topology.onCoordinatorClick = () => {
    switchTab('tab-coordinator');
  };

  topology.onNodeHover = (node, mouseX, mouseY) => {
    const hud = document.getElementById('hudTooltip');
    if (!node || node.id === 'COORDINATOR') {
      hud.classList.add('hidden');
      return;
    }

    document.getElementById('hudNodeId').textContent = node.id + (node.isLocal ? ' (This PC)' : '');
    document.getElementById('hudNatType').textContent = node.natType || (lastStatusData?.nat?.nat_type || 'Unknown');
    document.getElementById('hudCgnat').textContent = node.isCGNAT ? 'true (RFC 6598)' : 'false';
    document.getElementById('hudP2PStatus').textContent = node.isLocal ? 'Local Loopback' : (node.connectedP2P ? 'Active WebRTC P2P' : 'Disconnected');
    document.getElementById('hudGPU').textContent = node.gpuActive ? 'DirectML / CUDA Tier 3' : 'Tier 2 Docker';

    hud.style.left = `${Math.min(window.innerWidth - 260, mouseX + 16)}px`;
    hud.style.top = `${Math.min(window.innerHeight - 180, mouseY + 16)}px`;
    hud.classList.remove('hidden');
  };

  // Inspector Connection Actions
  document.getElementById('btnInspectorConnect').addEventListener('click', () => {
    if (!selectedPeer || selectedPeer.isLocal) return;
    const btn = document.getElementById('btnInspectorConnect');
    btn.innerHTML = '<span class="material-symbols-outlined text-sm">sync</span><span>Connecting...</span>';
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
        alert('P2P Connection failed: ' + res.error);
        btn.innerHTML = '<span class="material-symbols-outlined text-sm">link</span><span>Connect P2P</span>';
      } else {
        pollFleetAndPeers();
        openPCInspector(selectedPeer);
      }
    });
  });

  document.getElementById('btnInspectorDisconnect').addEventListener('click', () => {
    if (!selectedPeer || selectedPeer.isLocal) return;
    fetch('/api/peer/disconnect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ peer_id: selectedPeer.id })
    }).then(() => {
      pollFleetAndPeers();
      openPCInspector(selectedPeer);
    });
  });

  document.getElementById('btnInspectorPing').addEventListener('click', () => {
    if (!selectedPeer || selectedPeer.isLocal) return;
    const out = document.getElementById('inspectorPingOutput');
    out.textContent = 'Pinging peer over SCTP DataChannel...';
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
        out.style.color = '#f43f5e';
      } else if (res.status === 'ping_timeout') {
        out.textContent = `Ping timed out after ${res.duration_ms || 5000} ms`;
        out.style.color = '#f43f5e';
      } else {
        out.textContent = `SCTP ACK: RTT ${res.duration_ms} ms (Direct P2P Link Valid)`;
        out.style.color = '#10b981';
      }
    });
  });

  document.getElementById('btnSelectPeerForCode').addEventListener('click', () => {
    if (!selectedPeer) return;
    switchTab('tab-code');
    const sel = document.getElementById('codeTargetPeer');
    sel.value = selectedPeer.isLocal ? 'local' : selectedPeer.id;
  });

  document.getElementById('btnSelectPeerForShell').addEventListener('click', () => {
    if (!selectedPeer) return;
    switchTab('tab-code');
    const sel = document.getElementById('codeTargetPeer');
    sel.value = selectedPeer.isLocal ? 'local' : selectedPeer.id;
    document.getElementById('codeStdinInput').value = 'whoami';
  });
}

function openPCInspector(node) {
  const card = document.getElementById('inspectorCard');
  const isConn = node.connectedP2P;
  const isLocal = node.isLocal;

  const btnConn = document.getElementById('btnInspectorConnect');
  const btnDisc = document.getElementById('btnInspectorDisconnect');
  const btnPing = document.getElementById('btnInspectorPing');

  if (isLocal) {
    document.getElementById('inspectorNodeName').textContent = `${node.id} (This PC)`;
    document.getElementById('inspectorSubtext').textContent = 'Host Native Engine • Docker Sandbox Ready';
    btnConn.classList.add('hidden');
    btnDisc.classList.add('hidden');
    btnPing.classList.add('hidden');

    document.getElementById('inspectorPathType').textContent = 'Loopback (Host)';
    document.getElementById('inspectorLatency').textContent = '< 1 ms';
    document.getElementById('inspectorChannelStatus').textContent = 'Active (Local)';
    document.getElementById('inspectorTierBadge').textContent = (lastStatusData && lastStatusData.gpu_profile && lastStatusData.gpu_profile.directml_supported) ? 'Tier 3 DirectML' : 'Tier 2 Docker';

    const cpus = lastStatusData ? lastStatusData.cpus : 8;
    const os = lastStatusData ? lastStatusData.os : 'host';
    const arch = lastStatusData ? lastStatusData.arch : 'amd64';
    document.getElementById('inspectorCpu').textContent = `${cpus} vCPUs • ${os}/${arch}`;

    const gpuName = (lastStatusData && lastStatusData.gpu_profile) ? (lastStatusData.gpu_profile.device_name || 'DirectML Accelerated') : 'DirectML Accelerated';
    document.getElementById('inspectorGpu').textContent = gpuName;

    const natName = (lastStatusData && lastStatusData.nat) ? lastStatusData.nat.nat_type : 'Unknown';
    document.getElementById('inspectorNat').textContent = `${natName} (Local)`;

    const storedMB = (lastStatusData && lastStatusData.storage) ? (lastStatusData.storage.used_bytes / (1024 * 1024)).toFixed(1) : '0';
    const blobCount = (lastStatusData && lastStatusData.storage) ? lastStatusData.storage.blob_count : 0;
    document.getElementById('inspectorDataIO').textContent = `${storedMB} MB Stored (${blobCount} Blobs)`;
  } else {
    document.getElementById('inspectorNodeName').textContent = node.id;
    btnPing.classList.remove('hidden');

    const peerObj = connectedPeers.find(p => (p.peer_id || p) === node.id);

    if (isConn) {
      document.getElementById('inspectorSubtext').textContent = 'WebRTC SCTP DataChannel Active • Direct P2P';
      btnConn.classList.add('hidden');
      btnDisc.classList.remove('hidden');

      if (peerObj && peerObj.metrics) {
        document.getElementById('inspectorPathType').textContent = peerObj.metrics.pair_type || 'host-host';
        document.getElementById('inspectorLatency').textContent = `${peerObj.metrics.rtt_ms ?? 0} ms`;
        document.getElementById('inspectorChannelStatus').textContent = peerObj.metrics.is_direct ? 'Direct (No Relay)' : 'Relayed';
        document.getElementById('inspectorDataIO').textContent = `${formatBytes(peerObj.metrics.bytes_sent || 0)} Out / ${formatBytes(peerObj.metrics.bytes_received || 0)} In`;
      } else {
        document.getElementById('inspectorPathType').textContent = 'host-host';
        document.getElementById('inspectorLatency').textContent = '-- ms';
        document.getElementById('inspectorChannelStatus').textContent = 'Direct (No Relay)';
        document.getElementById('inspectorDataIO').textContent = '0 B Out / 0 B In';
      }

      document.getElementById('inspectorTierBadge').textContent = node.gpuActive ? 'Tier 3 GPU' : 'Tier 2 Docker';
      document.getElementById('inspectorCpu').textContent = 'Remote Peer Rig';
      document.getElementById('inspectorGpu').textContent = node.gpuActive ? 'Hardware Accelerated' : 'CPU Container';
      document.getElementById('inspectorNat').textContent = node.natType || 'Unknown';
    } else {
      document.getElementById('inspectorSubtext').textContent = 'Signaling via Coordinator Hub • Unlinked';
      btnConn.classList.remove('hidden');
      btnConn.innerHTML = '<span class="material-symbols-outlined text-sm">link</span><span>Connect P2P</span>';
      btnDisc.classList.add('hidden');

      document.getElementById('inspectorPathType').textContent = 'Coordinator Hub';
      document.getElementById('inspectorLatency').textContent = '-- ms';
      document.getElementById('inspectorChannelStatus').textContent = 'Offline';
      document.getElementById('inspectorTierBadge').textContent = 'Tier 2 Docker';
      document.getElementById('inspectorCpu').textContent = 'Remote Peer Rig';
      document.getElementById('inspectorGpu').textContent = 'Detection Pending Link';
      document.getElementById('inspectorNat').textContent = node.natType || 'Unknown';
      document.getElementById('inspectorDataIO').textContent = '0 B Out / 0 B In';
    }
  }

  card.classList.remove('hidden');
}

function initCodeSandbox() {
  document.getElementById('codeTimeoutRange').addEventListener('input', (e) => {
    document.getElementById('codeTimeoutLabel').textContent = `${e.target.value}s`;
  });

  document.getElementById('btnRunCode').addEventListener('click', executeCodeSandbox);
  document.getElementById('btnExecShell').addEventListener('click', executeHostShell);

  const codeSelect = document.getElementById('codeSampleSelect');
  codeSelect.addEventListener('change', () => {
    const opt = codeSelect.selectedOptions[0];
    if (opt && opt.dataset.content) {
      document.getElementById('codeSourceEditor').value = opt.dataset.content;
      if (opt.dataset.lang) {
        document.getElementById('codeLanguage').value = opt.dataset.lang;
      }
    }
  });
}

function executeCodeSandbox() {
  const peerID = document.getElementById('codeTargetPeer').value;
  const language = document.getElementById('codeLanguage').value;
  const source = document.getElementById('codeSourceEditor').value;
  const stdin = document.getElementById('codeStdinInput').value;
  const timeout = parseInt(document.getElementById('codeTimeoutRange').value) || 10;

  if (!peerID) {
    alert('Please select a target peer or Local Worker.');
    return;
  }
  if (!source.trim()) {
    alert('Please write or load code first.');
    return;
  }

  const targetLabel = peerID === 'local' ? 'Local Worker (This Machine)' : peerID;
  const consoleEl = document.getElementById('codeOutputConsole');
  consoleEl.textContent = '';
  const dispatchRow = document.createElement('div');
  dispatchRow.className = 'term-row term-info';
  dispatchRow.textContent = `[DISPATCH] Executing ${language} job on ${targetLabel}...`;
  consoleEl.appendChild(dispatchRow);
  document.getElementById('codeMetricsBadge').textContent = 'EXECUTING...';

  const startTime = Date.now();
  fetch('/api/code/run', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ peer_id: peerID, language, source, stdin, timeout })
  })
  .then(r => r.json())
  .then(res => {
    const elapsed = Date.now() - startTime;
    document.getElementById('lastExecDuration').textContent = `${res.duration_ms || elapsed} ms`;
    document.getElementById('codeMetricsBadge').textContent = res.exit_code === 0 ? 'EXIT 0' : `EXIT ${res.exit_code}`;

    consoleEl.textContent = '';
    const hdr = document.createElement('div');
    hdr.className = 'term-row term-success';
    hdr.textContent = `=== Job ${res.job_id || 'code-task'} Finished in ${res.duration_ms || elapsed} ms ===`;
    consoleEl.appendChild(hdr);
    if (res.stdout) {
      const rOut = document.createElement('div');
      rOut.className = 'term-row';
      rOut.textContent = res.stdout;
      consoleEl.appendChild(rOut);
    }
    if (res.stderr) {
      const rErr = document.createElement('div');
      rErr.className = 'term-row term-err';
      rErr.textContent = res.stderr;
      consoleEl.appendChild(rErr);
    }
    if (res.error) {
      const rMsg = document.createElement('div');
      rMsg.className = 'term-row term-err';
      rMsg.textContent = `Execution Error: ${res.error}`;
      consoleEl.appendChild(rMsg);
    }

    appendLog('CODEEXEC', `Task ${res.job_id || 'task'} on ${targetLabel}: exit ${res.exit_code} (${res.duration_ms || elapsed} ms)`);
    loadTasks();
  })
  .catch(err => {
    const rNet = document.createElement('div');
    rNet.className = 'term-row term-err';
    rNet.textContent = `Network error: ${err.message}`;
    consoleEl.appendChild(rNet);
  });
}

function executeHostShell() {
  const peerID = document.getElementById('codeTargetPeer').value;
  const cmd = document.getElementById('codeStdinInput').value || 'whoami';

  const targetLabel = peerID === 'local' ? 'Local Worker (This Machine)' : peerID;
  const consoleEl = document.getElementById('codeOutputConsole');
  consoleEl.textContent = '';
  const execRow = document.createElement('div');
  execRow.className = 'term-row term-info';
  execRow.textContent = `[EXEC] Running host shell '${cmd}' on ${targetLabel}...`;
  consoleEl.appendChild(execRow);

  fetch('/api/shell/exec', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ peer_id: peerID, command: cmd })
  })
  .then(r => r.json())
  .then(res => {
    consoleEl.textContent = '';
    const hdr = document.createElement('div');
    hdr.className = 'term-row term-success';
    hdr.textContent = `=== Shell Exit: ${res.exit_code} (${res.duration_ms} ms) ===`;
    consoleEl.appendChild(hdr);
    if (res.stdout) {
      const rOut = document.createElement('div');
      rOut.className = 'term-row';
      rOut.textContent = res.stdout;
      consoleEl.appendChild(rOut);
    }
    if (res.stderr) {
      const rErr = document.createElement('div');
      rErr.className = 'term-row term-err';
      rErr.textContent = res.stderr;
      consoleEl.appendChild(rErr);
    }
    if (res.error) {
      const rMsg = document.createElement('div');
      rMsg.className = 'term-row term-err';
      rMsg.textContent = `Error: ${res.error}`;
      consoleEl.appendChild(rMsg);
    }
  })
  .catch(err => {
    consoleEl.textContent = '';
    const rErr = document.createElement('div');
    rErr.className = 'term-row term-err';
    rErr.textContent = `Shell execution failed: ${err.message}`;
    consoleEl.appendChild(rErr);
  });
}

function initGPUSubsystem() {
  document.getElementById('btnRefreshGPU').addEventListener('click', loadStatus);
  document.getElementById('btnAnalyzeHIP').addEventListener('click', runHipifyAnalysis);
  document.getElementById('btnRunGPU').addEventListener('click', runGPUSandboxTask);

  const fileInput = document.getElementById('cudaFileInput');
  const btnSelectFile = document.getElementById('btnSelectCudaFile');
  const btnDownloadHIP = document.getElementById('btnDownloadHIP');
  let selectedCudaFilename = 'kernel.cu';

  if (btnSelectFile && fileInput) {
    btnSelectFile.addEventListener('click', () => fileInput.click());
    fileInput.addEventListener('change', (e) => {
      const file = e.target.files[0];
      if (!file) return;
      selectedCudaFilename = file.name;
      const reader = new FileReader();
      reader.onload = (ev) => {
        document.getElementById('cudaInputEditor').value = ev.target.result;
        runHipifyAnalysis();
      };
      reader.readAsText(file);
    });
  }

  if (btnDownloadHIP) {
    btnDownloadHIP.addEventListener('click', () => {
      const hipCode = document.getElementById('hipOutputEditor').value;
      if (!hipCode.trim()) return;
      const outName = selectedCudaFilename.replace(/\.(cu|cuh|cpp|c)$/i, '') + '.hip';
      const blob = new Blob([hipCode], { type: 'text/plain' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = outName;
      a.click();
      URL.revokeObjectURL(url);
    });
  }

  const hipSelect = document.getElementById('hipifySampleSelect');
  hipSelect.addEventListener('change', () => {
    const opt = hipSelect.selectedOptions[0];
    if (opt && opt.dataset.content) {
      selectedCudaFilename = opt.value || 'kernel.cu';
      document.getElementById('cudaInputEditor').value = opt.dataset.content;
    }
  });
}

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
    document.getElementById('hipOutputEditor').value = res.translated_source || '// Translation blocked.';
    const btnDownload = document.getElementById('btnDownloadHIP');
    if (btnDownload) {
      if (res.success && res.translated_source) {
        btnDownload.classList.remove('hidden');
      } else {
        btnDownload.classList.add('hidden');
      }
    }

    const panel = document.getElementById('hipifyIssuesPanel');
    const badge = document.getElementById('hipifyVerdictBadge');
    const count = document.getElementById('hipifyIssueCount');
    const list = document.getElementById('hipifyIssueList');

    panel.classList.remove('hidden');
    list.textContent = '';
    if (res.report && res.report.target_amd_supported) {
      badge.textContent = 'PORTABLE TO AMD';
      badge.className = 'badge-emerald font-mono text-[10px]';
      const engineLabel = res.engine ? ` via ${res.engine}` : '';
      count.textContent = `${res.replacements_made || 0} replacements made${engineLabel}`;
      const okDiv = document.createElement('div');
      okDiv.className = 'text-secondary font-mono text-xs';
      okDiv.textContent = `Ready for execution on AMD RDNA / ROCm hardware${res.engine ? ` (translated with ${res.engine})` : ''}.`;
      list.appendChild(okDiv);
    } else if (res.report) {
      badge.textContent = 'BLOCKED ON AMD';
      badge.className = 'badge-rose font-mono text-[10px]';
      const issues = res.report.issues || [];
      count.textContent = `${issues.length} fatal blocker(s) detected`;
      issues.forEach(iss => {
        const issDiv = document.createElement('div');
        issDiv.className = 'text-rose font-mono text-xs';
        issDiv.textContent = `Line ${iss.line_number}: [${iss.construct}] ${iss.reason}`;
        list.appendChild(issDiv);
      });
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
    let outputText = `Job ${res.job_id} ${res.status.toUpperCase()} in ${res.execution_ms} ms. Checkpoint: ${res.checkpoint_blob || 'saved'}`;
    if (res.simulated) {
      outputText += ' [SIMULATED MOCK RUNNER]';
    }
    out.textContent = outputText;
    appendLog('GPU', `Task ${res.job_id} completed (${res.execution_ms} ms, ${batches} batches)${res.simulated ? ' [SIMULATED]' : ''}`);
    loadTasks();
  });
}

function initStorageVault() {
  document.getElementById('btnPutBlob').addEventListener('click', putStorageBlob);
  document.getElementById('btnScrubVault').addEventListener('click', scrubStorageVault);
  document.getElementById('btnRefreshBlobs').addEventListener('click', loadBlobs);

  // Wire Modal Close and Copy Buttons
  document.getElementById('btnCloseBlobModal').addEventListener('click', () => {
    document.getElementById('blobViewModal').classList.add('hidden');
  });
  document.getElementById('btnCloseBlobModalBtn').addEventListener('click', () => {
    document.getElementById('blobViewModal').classList.add('hidden');
  });
  document.getElementById('btnCopyBlobContent').addEventListener('click', () => {
    const text = document.getElementById('viewModalContent').value;
    navigator.clipboard.writeText(text).then(() => {
      const btn = document.getElementById('btnCopyBlobContent');
      const orig = btn.innerHTML;
      btn.innerHTML = '<span class="material-symbols-outlined text-xs text-secondary">check</span><span>Copied</span>';
      setTimeout(() => { btn.innerHTML = orig; }, 1500);
    });
  });
}

function loadBlobs() {
  fetch('/api/storage/blobs')
    .then(r => r.json())
    .then(data => {
      const tbody = document.getElementById('blobsTableBody');
      const badge = document.getElementById('blobCountBadge');
      const blobs = data.blobs || [];
      if (badge) badge.textContent = `${blobs.length} Total Blobs`;

      // Update SHA-256 local CAS dataset metrics
      let totalBytes = 0;
      blobs.forEach(b => { totalBytes += (b.size_bytes !== undefined ? b.size_bytes : (b.size || 0)); });
      const casSize = document.getElementById('rsCasTotalSize');
      if (casSize) casSize.textContent = formatBytes(totalBytes);
      const casHash = document.getElementById('rsCasRootHash');
      if (casHash && blobs.length > 0) {
        const topId = blobs[0].id;
        casHash.textContent = topId.length > 14 ? topId.substring(0, 8) + '...' + topId.substring(topId.length - 4) : topId;
      }

      tbody.textContent = '';
      if (!blobs || blobs.length === 0) {
        const emptyTr = document.createElement('tr');
        const emptyTd = document.createElement('td');
        emptyTd.colSpan = 4;
        emptyTd.className = 'empty-cell';
        emptyTd.textContent = 'No blobs stored in local vault.';
        emptyTr.appendChild(emptyTd);
        tbody.appendChild(emptyTr);
        return;
      }

      blobs.forEach(b => {
        const bSize = b.size_bytes !== undefined ? b.size_bytes : (b.size || 0);
        const bDate = b.created_at || b.mod_time || new Date().toISOString();

        const tr = document.createElement('tr');

        const tdId = document.createElement('td');
        const codeEl = document.createElement('code');
        codeEl.className = 'text-primary font-mono text-xs';
        codeEl.textContent = b.id;
        tdId.appendChild(codeEl);

        const tdSize = document.createElement('td');
        tdSize.textContent = `${(bSize / 1024).toFixed(1)} KiB`;

        const tdTime = document.createElement('td');
        tdTime.textContent = new Date(bDate).toLocaleTimeString();

        const tdActions = document.createElement('td');
        tdActions.className = 'text-right space-x-2';

        const viewBtn = document.createElement('button');
        viewBtn.type = 'button';
        viewBtn.className = 'btn-tool-text text-secondary hover:text-white';
        viewBtn.textContent = 'View';
        viewBtn.addEventListener('click', () => viewBlob(b.id));

        const delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'btn-tool-text text-rose hover:text-white';
        delBtn.textContent = 'Delete';
        delBtn.addEventListener('click', () => deleteBlob(b.id));

        tdActions.append(viewBtn, delBtn);
        tr.append(tdId, tdSize, tdTime, tdActions);
        tbody.appendChild(tr);
      });
    });
}

window.viewBlob = function(blobId) {
  fetch(`/api/storage/get?id=${encodeURIComponent(blobId)}`)
    .then(r => r.json())
    .then(data => {
      if (data.error) {
        alert('Failed to retrieve blob: ' + data.error);
        return;
      }
      document.getElementById('viewModalBlobId').textContent = data.blob_id;
      const bSize = data.size_bytes !== undefined ? data.size_bytes : (data.size || 0);
      const sizeKiB = (bSize / 1024).toFixed(2);
      const isValid = data.valid !== undefined ? data.valid : (data.verified !== undefined ? data.verified : false);
      document.getElementById('viewModalSize').textContent = `${sizeKiB} KiB • SHA-256 Valid: ${isValid ? 'YES' : 'CORRUPTED'}`;
      document.getElementById('viewModalContent').value = data.content || '(empty)';
      document.getElementById('blobViewModal').classList.remove('hidden');
    })
    .catch(err => {
      alert('Network error retrieving blob: ' + err.message);
    });
};

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
    appendLog('STORAGE', `Stored new content-addressed blob into local disk vault.`);
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
    appendLog('STORAGE', `Deleted blob ${blobId} from vault.`);
  });
};

function scrubStorageVault() {
  const out = document.getElementById('scrubResult');
  out.textContent = 'Scrubbing storage vault with SHA-256 cryptographic audits...';
  out.classList.remove('hidden');

  fetch('/api/storage/scrub', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}'
  })
    .then(r => {
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      return r.json();
    })
    .then(res => {
      out.textContent = '';
      const span = document.createElement('span');
      if (!res.corrupted || res.corrupted.length === 0) {
        span.className = 'text-secondary';
        span.textContent = '✓ Cryptographic Audit Complete: 100% of stored blobs passed SHA-256 verification with zero bit-rot.';
      } else {
        span.className = 'text-rose';
        span.textContent = `⚠ Warning: detected ${res.corrupted.length} corrupted blob(s)!`;
      }
      out.appendChild(span);
    })
    .catch(err => {
      out.textContent = '';
      const span = document.createElement('span');
      span.className = 'text-rose';
      span.textContent = `⚠ Cryptographic scrub error: ${err.message}`;
      out.appendChild(span);
    });
}

function initWireLogs() {
  document.querySelectorAll('.filter-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      currentLogFilter = btn.dataset.filter;
      applyLogFilter();
    });
  });

  const btnPause = document.getElementById('btnPauseLogs');
  btnPause.addEventListener('click', () => {
    isLogPaused = !isLogPaused;
    document.getElementById('pauseLogLabel').textContent = isLogPaused ? 'Resume' : 'Pause';
    btnPause.style.color = isLogPaused ? '#f59e0b' : '';
  });

  document.getElementById('btnClearLogs').addEventListener('click', () => {
    document.getElementById('wireLogContainer').innerHTML = '';
  });
}

function applyLogFilter() {
  const rows = document.querySelectorAll('#wireLogContainer .log-row');
  rows.forEach(r => {
    if (currentLogFilter === 'all') {
      r.classList.remove('hidden');
    } else {
      const cat = r.dataset.category || 'all';
      if (cat === currentLogFilter) {
        r.classList.remove('hidden');
      } else {
        r.classList.add('hidden');
      }
    }
  });
}

function initWebSocket() {
  const loc = window.location;
  const token = getAuthToken();
  const tokenParam = token ? '?token=' + encodeURIComponent(token) : '';
  const wsURL = (loc.protocol === 'https:' ? 'wss://' : 'ws://') + loc.host + '/api/events' + tokenParam;
  const ws = new WebSocket(wsURL);

  ws.onopen = () => {
    appendLog('DAEMON', 'Connected to UniCompute desktop daemon WebSocket event bus.');
    document.getElementById('footerSocketStatus').innerHTML = '<span class="text-secondary">● WebSocket Event Bus Connected</span>';
  };

  ws.onmessage = (e) => {
    try {
      const evt = JSON.parse(e.data);
      handleLiveEvent(evt);
    } catch (_) {}
  };

  ws.onclose = () => {
    document.getElementById('footerSocketStatus').innerHTML = '<span class="text-amber">○ Reconnecting WebSocket...</span>';
    setTimeout(initWebSocket, 2000);
  };
}

function handleLiveEvent(evt) {
  let category = 'coordinator';
  const t = (evt.type || '').toLowerCase();
  if (t.includes('peer') || t.includes('stream') || t.includes('chunk')) category = 'p2p';
  if (t.includes('code') || t.includes('shell')) category = 'code';
  if (t.includes('gpu')) category = 'gpu';
  if (t.includes('storage') || t.includes('blob')) category = 'storage';

  appendLog(evt.type.toUpperCase(), evt.message, category);

  // Spawn visual particle on real chunk and stream data events
  if (topology) {
    if (t === 'chunk_received' && evt.peer_id) {
      topology.spawnEventPacket(evt.peer_id, currentLocalId, '#10b981');
    } else if (t === 'stream_message' && evt.peer_id) {
      topology.spawnEventPacket(evt.peer_id, currentLocalId, '#38bdf8');
    }
  }

  if (t.includes('peer') || t.includes('coordinator')) {
    pollFleetAndPeers();
    loadCoordinatorStatus();
  }

  if (t === 'worker_settings_updated') {
    loadWorkerSettings();
  }

  if (t.includes('task')) {
    loadTasks();
  }

  // Preemption Alert (Objective 3 Watchdog)
  if (evt.type === 'gpu_preempt_alert') {
    const banner = document.getElementById('wdAlertBanner');
    document.getElementById('wdAlertMessage').textContent = evt.message;
    banner.classList.remove('hidden');
    setTimeout(() => banner.classList.add('hidden'), 8000);
  }
}

function applyStatusData(data) {
  if (!data || !data.node_id) return;
  lastStatusData = data;
  currentLocalId = data.node_id;
  document.getElementById('localNodeIdLabel').textContent = data.node_id;

  if (data.nat) {
    document.getElementById('localNatLabel').textContent = `NAT: ${data.nat.nat_type || 'Unknown'}`;
  }

  // Update GPU Card (Objective 3)
  if (data.gpu_profile) {
    if (!data.gpu_profile.total_vram_bytes || data.gpu_profile.total_vram_bytes === 0) {
      document.getElementById('gpuDeviceName').textContent = data.gpu_profile.device_name || 'CPU Fallback (No Dedicated GPU)';
      document.getElementById('gpuVendor').textContent = `${data.gpu_profile.vendor || 'System'} • DirectML: ${data.gpu_profile.directml_supported}`;
      document.getElementById('gpuVramLabel').textContent = '0 / 0 MB';
      document.getElementById('gpuVramAlloc').textContent = '0 MB';
      document.getElementById('gpuVramBar').style.width = '0%';
    } else {
      document.getElementById('gpuDeviceName').textContent = data.gpu_profile.device_name || 'Dedicated GPU';
      document.getElementById('gpuVendor').textContent = `${data.gpu_profile.vendor || 'Hardware'} • DirectML: ${data.gpu_profile.directml_supported}`;
      const totalMB = (data.gpu_profile.total_vram_bytes / (1024*1024)).toFixed(0);
      const availMB = data.gpu_profile.available_vram_bytes ? (data.gpu_profile.available_vram_bytes / (1024*1024)).toFixed(0) : totalMB;
      document.getElementById('gpuVramLabel').textContent = `${availMB} / ${totalMB} MB`;
      document.getElementById('gpuVramAlloc').textContent = `${Math.max(0, totalMB - availMB)} MB`;
      document.getElementById('gpuVramBar').style.width = `${totalMB > 0 ? ((totalMB - availMB) / totalMB) * 100 : 0}%`;
    }

    if (data.gpu_profile.temperature_c > 0) {
      document.getElementById('gpuTempPowerLabel').textContent = `${data.gpu_profile.temperature_c.toFixed(1)} °C • ${data.gpu_profile.power_usage_w.toFixed(1)} W`;
    }
  }

  // Update Watchdog Radar
  if (data.watchdog) {
    document.getElementById('wdPowerLabel').textContent = data.watchdog.on_battery ? 'Battery: UNPLUGGED' : 'AC Power: ONLINE';
    document.getElementById('wdActivityLabel').textContent = data.watchdog.user_active ? 'User: ACTIVE' : 'User: IDLE';
    document.getElementById('wdStateLabel').textContent = `State: ${data.watchdog.state.toUpperCase()}`;
    document.getElementById('navWatchdogBadge').textContent = `${data.watchdog.state.toUpperCase()} (50ms)`;
  }

  // Storage
  if (data.storage) {
    const usedMB = (data.storage.used_bytes / (1024*1024)).toFixed(0);
    const quotaMB = (data.storage.quota_bytes / (1024*1024)).toFixed(0);
    document.getElementById('storageUsedText').textContent = `${usedMB} MB / ${(quotaMB/1024).toFixed(0)} GB`;
    document.getElementById('navStorageBadge').textContent = `${usedMB} MB / ${(quotaMB/1024).toFixed(0)} GB`;
    const pct = data.storage.quota_bytes > 0 ? Math.min(100, (data.storage.used_bytes / data.storage.quota_bytes) * 100) : 0;
    document.getElementById('storageUsageBar').style.width = pct.toFixed(1) + '%';
    document.getElementById('storageBlobCount').textContent = `${data.storage.blob_count} Blobs`;
    document.getElementById('storageAvailText').textContent = `${((data.storage.quota_bytes - data.storage.used_bytes)/(1024*1024*1024)).toFixed(1)} GB`;
  }
}

function loadStatus() {
  fetch('/api/status')
    .then(r => r.json())
    .then(data => {
      applyStatusData(data);
      // Immediately synchronize local node on topology canvas
      if (topology) {
        topology.updateFleet(currentFleet, connectedPeers, currentLocalId, data);
      }
    });
}

function pollFleetAndPeers() {
  Promise.all([
    fetch('/api/fleet').then(r => r.json()).catch(() => ({ nodes: [] })),
    fetch('/api/peers').then(r => r.json()).catch(() => ({ peers: [] })),
    fetch('/api/status').then(r => r.json()).catch(() => lastStatusData || {})
  ]).then(([fleetData, peersData, statusData]) => {
    applyStatusData(statusData);
    currentFleet = (fleetData && fleetData.nodes) || [];
    connectedPeers = (peersData && peersData.peers) || [];

    document.getElementById('fleetCountBadge').textContent = `${currentFleet.length} Nodes`;
    document.getElementById('peerCountBadge').textContent = `${connectedPeers.length} Active`;
    document.getElementById('footerP2PCount').textContent = `${connectedPeers.length} P2P Links Active`;

    // Populate Target Peer Dropdown in Code Tab
    const peerSelect = document.getElementById('codeTargetPeer');
    if (peerSelect) {
      const existingVal = peerSelect.value;
      peerSelect.innerHTML = '';

      const localOpt = document.createElement('option');
      localOpt.value = 'local';
      localOpt.textContent = 'Local Worker (This Machine)';
      peerSelect.appendChild(localOpt);

      currentFleet.forEach(nodeId => {
        if (nodeId !== currentLocalId) {
          const opt = document.createElement('option');
          opt.value = nodeId;
          const isConn = connectedPeers.some(p => (p.peer_id || p) === nodeId);
          opt.textContent = `${nodeId} ${isConn ? '★ [P2P Direct]' : '[Coordinator]'}`;
          peerSelect.appendChild(opt);
        }
      });

      if (existingVal && (existingVal === 'local' || currentFleet.includes(existingVal))) {
        peerSelect.value = existingVal;
      } else {
        peerSelect.value = 'local';
      }
    }

    // Refresh Canvas with real status including operator local node
    if (topology) {
      topology.updateFleet(currentFleet, connectedPeers, currentLocalId, lastStatusData);
    }
    updateFleetSuggestions();
  }).catch(() => {});
}

function loadSamples() {
  fetch('/api/samples')
    .then(r => r.json())
    .then(data => {
      if (!data.samples) return;
      const codeSelect = document.getElementById('codeSampleSelect');
      const hipSelect = document.getElementById('hipifySampleSelect');

      while (codeSelect.options.length > 1) codeSelect.remove(1);
      while (hipSelect.options.length > 1) hipSelect.remove(1);

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
    });
}

function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function appendLog(type, msg, category = 'all') {
  if (isLogPaused) return;

  const container = document.getElementById('wireLogContainer');
  if (!container) return;

  const div = document.createElement('div');
  div.className = 'log-row';
  div.dataset.category = category;

  if (currentLogFilter !== 'all' && currentLogFilter !== category) {
    div.classList.add('hidden');
  }

  let typeColor = 'text-primary';
  if (type.includes('P2P') || type.includes('CHUNK')) typeColor = 'text-secondary';
  if (type.includes('CODE') || type.includes('SHELL')) typeColor = 'text-tertiary';
  if (type.includes('STORAGE') || type.includes('BLOB')) typeColor = 'text-primary';

  const timeSpan = document.createElement('span');
  timeSpan.className = 'text-outline';
  timeSpan.textContent = `[${new Date().toLocaleTimeString()}] `;

  const typeSpan = document.createElement('span');
  typeSpan.className = `${typeColor} font-semibold font-mono`;
  typeSpan.textContent = `[${type}] `;

  const msgSpan = document.createElement('span');
  msgSpan.className = 'text-on-surface';
  msgSpan.textContent = msg == null ? '' : String(msg);

  div.append(timeSpan, typeSpan, msgSpan);
  container.appendChild(div);

  if (document.getElementById('chkAutoScroll').checked) {
    container.scrollTop = container.scrollHeight;
  }
}

function escapeHTML(str) {
  if (str === null || str === undefined) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

/* =========================================================================
   COORDINATOR HUB, REED-SOLOMON SHARD MATRIX & CLUSTER TASK QUEUE LOGIC
   ========================================================================= */

function initCoordinatorHub() {
  const syncBtn = document.getElementById('btnSyncCoordinatorConfig');
  if (syncBtn) {
    syncBtn.addEventListener('click', () => {
      loadCoordinatorStatus();
      appendLog('COORDINATOR', 'Syncing coordinator configuration and topology health...');
    });
  }

  const restartBtn = document.getElementById('btnRestartDaemon');
  if (restartBtn) {
    restartBtn.addEventListener('click', () => {
      const orig = restartBtn.innerHTML;
      restartBtn.innerHTML = '<span class="material-symbols-outlined text-base animate-spin">refresh</span><span>Reconnecting...</span>';
      fetch('/api/coordinator/status')
        .then(r => r.json())
        .then(st => {
          const url = (st && st.coordinator_url) ? st.coordinator_url : (document.getElementById('coordinatorSelect')?.value || 'wss://unicompute-coordinator.onrender.com/ws');
          return fetch('/api/coordinator/connect', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ url })
          });
        })
        .then(async r => {
          const data = await r.json().catch(() => ({}));
          if (!r.ok) {
            throw new Error(data.error || `HTTP ${r.status}`);
          }
          return data;
        })
        .then(data => {
          if (data && data.reconnect_error) {
            appendLog('DAEMON', `Coordinator reconnect failed: ${data.reconnect_error}`);
          } else {
            appendLog('DAEMON', 'Reconnecting to signaling coordinator...');
          }
          setTimeout(() => {
            restartBtn.innerHTML = orig;
            loadCoordinatorStatus();
          }, 1000);
        })
        .catch(err => {
          restartBtn.innerHTML = orig;
          appendLog('DAEMON', `Coordinator reconnect error: ${err.message}`);
        });
    });
  }

  // Cluster Auth Token Controls
  const inputToken = document.getElementById('inputCoordAuthToken');
  const btnToggleVis = document.getElementById('btnToggleAuthTokenVis');
  if (btnToggleVis && inputToken) {
    btnToggleVis.addEventListener('click', () => {
      const isPass = inputToken.type === 'password';
      inputToken.type = isPass ? 'text' : 'password';
      const icon = btnToggleVis.querySelector('.material-symbols-outlined');
      if (icon) {
        icon.textContent = isPass ? 'visibility_off' : 'visibility';
      }
    });
  }

  const btnSaveToken = document.getElementById('btnSaveAuthToken');
  if (btnSaveToken && inputToken) {
    btnSaveToken.addEventListener('click', () => {
      const tok = inputToken.value.trim();
      if (!tok) {
        appendLog('COORDINATOR', 'Enter a cluster auth token before saving (use Clear to remove)');
        inputToken.focus();
        return;
      }
      fetchWithAuth('/api/coordinator/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ auth_token: tok })
      })
      .then(async r => {
        const data = await r.json().catch(() => ({}));
        if (!r.ok) {
          throw new Error(data.error || `HTTP ${r.status}`);
        }
        return data;
      })
      .then(data => {
        inputToken.value = '';
        if (data && data.reconnect_error) {
          appendLog('COORDINATOR', `Cluster auth token saved, but signaling reconnect failed: ${data.reconnect_error}`);
        } else {
          appendLog('COORDINATOR', 'Cluster auth token updated & signaling reconnected');
        }
        loadCoordinatorStatus();
        loadWorkerSettings();
      })
      .catch(err => {
        appendLog('COORDINATOR', `Failed to update auth token: ${err.message}`);
      });
    });
  }

  const btnClearToken = document.getElementById('btnClearAuthToken');
  if (btnClearToken && inputToken) {
    btnClearToken.addEventListener('click', () => {
      inputToken.value = '';
      fetchWithAuth('/api/coordinator/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ clear_auth_token: true })
      })
      .then(async r => {
        const data = await r.json().catch(() => ({}));
        if (!r.ok) {
          throw new Error(data.error || `HTTP ${r.status}`);
        }
        return data;
      })
      .then(data => {
        if (data && data.reconnect_error) {
          appendLog('COORDINATOR', `Cleared cluster auth token, but signaling reconnect failed: ${data.reconnect_error}`);
        } else {
          appendLog('COORDINATOR', 'Cleared cluster auth token & reconnecting');
        }
        loadCoordinatorStatus();
        loadWorkerSettings();
      })
      .catch(err => {
        appendLog('COORDINATOR', `Failed to clear auth token: ${err.message}`);
      });
    });
  }

  const btnJump = document.getElementById('btnJumpToAuthToken');
  if (btnJump && inputToken) {
    btnJump.addEventListener('click', () => {
      inputToken.focus();
    });
  }

  // Worker Code Allowlist Controls
  const inputAllowPeer = document.getElementById('inputAddAllowPeer');
  const btnAddAllow = document.getElementById('btnAddAllowPeer');
  if (btnAddAllow && inputAllowPeer) {
    const handleAdd = () => {
      const peer = inputAllowPeer.value.trim();
      if (!peer) return;
      if (!currentWorkerAllowlist.includes(peer)) {
        saveWorkerAllowlist([...currentWorkerAllowlist, peer]);
      }
      inputAllowPeer.value = '';
    };
    btnAddAllow.addEventListener('click', handleAdd);
    inputAllowPeer.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        handleAdd();
      }
    });
  }

  const btnAllowAll = document.getElementById('btnAllowAllCode');
  if (btnAllowAll) {
    btnAllowAll.addEventListener('click', () => {
      saveWorkerAllowlist(['*']);
    });
  }

  const btnDenyAll = document.getElementById('btnDenyAllCode');
  if (btnDenyAll) {
    btnDenyAll.addEventListener('click', () => {
      saveWorkerAllowlist([]);
    });
  }
}

function loadCoordinatorStatus() {
  fetch('/api/coordinator/status')
    .then(r => r.json())
    .then(data => {
      if (!data) return;
      const uptimeElem = document.getElementById('coordUptime');
      if (uptimeElem) uptimeElem.textContent = data.uptime || '0m 00s';

      const socketElem = document.getElementById('coordListenSocket');
      if (socketElem) socketElem.textContent = data.transport || 'WebRTC DataChannel (ICE/DTLS/SCTP)';

      const mtlsElem = document.getElementById('coordMtls');
      if (mtlsElem) mtlsElem.textContent = 'DTLS 1.2 / SCTP (Pion WebRTC)';

      const daemonStat = document.getElementById('coordDaemonStatus');
      const authBadge = document.getElementById('coordAuthBadge');
      const bannerAuth = document.getElementById('bannerAuthRequired');
      const pillAuthToken = document.getElementById('pillAuthTokenStatus');

      if (data.status === 'AUTH_REQUIRED' || data.auth_required) {
        if (daemonStat) {
          daemonStat.textContent = 'AUTH_REQUIRED';
          daemonStat.className = 'text-amber-400 font-mono text-[11px] font-semibold';
        }
        if (authBadge) {
          authBadge.textContent = 'Auth Required (401)';
          authBadge.className = 'px-2 py-0.5 rounded text-[10px] font-mono font-semibold bg-amber-500/20 text-amber-300 border border-amber-500/40';
          authBadge.classList.remove('hidden');
        }
        if (bannerAuth) {
          bannerAuth.classList.remove('hidden');
        }
        if (pillAuthToken) {
          pillAuthToken.textContent = data.auth_token_set === true ? 'Invalid Token' : 'Not Set';
          pillAuthToken.className = 'px-2.5 py-0.5 rounded-full text-[10px] font-mono font-semibold bg-amber-500/15 border border-amber-500/30 text-amber-300';
        }
      } else {
        if (daemonStat) {
          daemonStat.textContent = data.status || 'RUNNING';
          daemonStat.className = 'text-secondary font-mono text-[11px] font-semibold';
        }
        if (bannerAuth) {
          bannerAuth.classList.add('hidden');
        }
        if (data.auth_token_set) {
          if (pillAuthToken) {
            pillAuthToken.textContent = 'Token Configured';
            pillAuthToken.className = 'px-2.5 py-0.5 rounded-full text-[10px] font-mono font-semibold bg-emerald-500/15 border border-secondary/30 text-secondary';
          }
          if (authBadge) {
            authBadge.textContent = 'Authenticated';
            authBadge.className = 'px-2 py-0.5 rounded text-[10px] font-mono font-semibold bg-emerald-500/15 text-secondary border border-secondary/30';
            authBadge.classList.remove('hidden');
          }
        } else {
          if (pillAuthToken) {
            pillAuthToken.textContent = 'Not Set';
            pillAuthToken.className = 'px-2.5 py-0.5 rounded-full text-[10px] font-mono font-semibold bg-surface-lowest border border-outline-variant/40 text-outline';
          }
          if (authBadge) {
            authBadge.classList.add('hidden');
          }
        }
      }

      if (topology) {
        if (topology.coordinator) {
          topology.coordinator.status = (data.status === 'RUNNING' || data.status === 'ONLINE') ? 'ONLINE' : 'OFFLINE';
        }
        if (data.coordinator_host) {
          topology.coordinatorHost = data.coordinator_host;
        } else if (data.coordinator_url) {
          try {
            const u = new URL(data.coordinator_url);
            topology.coordinatorHost = u.host;
          } catch (_) {
            topology.coordinatorHost = data.coordinator_url.replace(/^wss?:\/\//, '').replace(/\/.*$/, '');
          }
        }
      }

      const directCount = data.direct_peers_count ?? 0;
      const relayedCount = data.relayed_peers_count ?? 0;
      const avgRttVal = data.avg_rtt_ms ?? 0;
      const blobCount = data.blob_count ?? 0;

      const directPeers = document.getElementById('coordDirectPeers');
      if (directPeers) directPeers.textContent = `${directCount} Nodes`;

      const avgRtt = document.getElementById('coordAvgRtt');
      if (avgRtt) avgRtt.textContent = `Avg RTT: ${avgRttVal.toFixed(1)}ms`;

      const relayedPeers = document.getElementById('coordRelayedPeers');
      if (relayedPeers) relayedPeers.textContent = `${relayedCount} Nodes`;

      const relayFallback = document.getElementById('coordRelayFallback');
      if (relayFallback) relayFallback.textContent = 'TURN ICE Candidate';

      const throttled = document.getElementById('coordThrottledPeers');
      if (throttled) throttled.textContent = `${data.throttled_count ?? 0} Nodes`;

      const merkleVerif = document.getElementById('coordMerkleVerif');
      if (merkleVerif) {
        merkleVerif.textContent = blobCount > 0 ? '100.0%' : '0.0%';
      }

      const hashCount = document.getElementById('coordHashCount');
      if (hashCount) {
        hashCount.textContent = `${blobCount.toLocaleString()} Blobs`;
      }

      // Saturation bar
      const maxCap = data.mesh_capacity_max ?? 32;
      const totalNodes = directCount + relayedCount;
      const satLabel = document.getElementById('coordMeshSaturationLabel');
      if (satLabel) satLabel.textContent = `Mesh Link Saturation: ${totalNodes}/${maxCap} Cap`;

      const pct = maxCap > 0 ? Math.min(100, Math.round((totalNodes / maxCap) * 100)) : 0;
      const satPct = document.getElementById('coordMeshPercent');
      if (satPct) satPct.textContent = `${pct}% Capacity`;

      const directPct = maxCap > 0 ? (directCount / maxCap) * 100 : 0;
      const relayPct = maxCap > 0 ? (relayedCount / maxCap) * 100 : 0;
      const sparePct = Math.max(0, 100 - directPct - relayPct);

      const barDirect = document.getElementById('coordBarDirect');
      const barRelay = document.getElementById('coordBarRelay');
      const barSpare = document.getElementById('coordBarSpare');
      if (barDirect) barDirect.style.width = `${directPct}%`;
      if (barRelay) barRelay.style.width = `${relayPct}%`;
      if (barSpare) barSpare.style.width = `${sparePct}%`;
    }).catch(() => {});
}

function saveWorkerAllowlist(newList) {
  fetchWithAuth('/api/worker/settings', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code_allowlist: newList })
  })
  .then(r => r.json())
  .then(data => {
    appendLog('WORKER', `Updated code execution allowlist: [${(data.code_allowlist || []).join(', ')}]`);
    loadWorkerSettings();
  })
  .catch(err => {
    appendLog('WORKER', `Failed to update code allowlist: ${err.message}`);
  });
}

function loadWorkerSettings() {
  fetchWithAuth('/api/worker/settings')
    .then(r => r.json())
    .then(data => {
      if (!data) return;
      currentWorkerAllowlist = data.code_allowlist || [];

      // Runtime Status Badge
      const badge = document.getElementById('badgeDockerStatus');
      if (badge) {
        if (data.docker_active) {
          badge.textContent = 'Docker Sandbox Active';
          badge.className = 'px-2.5 py-0.5 rounded-full text-[10px] font-mono font-semibold bg-emerald-500/15 border border-secondary/30 text-secondary';
        } else {
          badge.textContent = 'Docker Unavailable (Forwarding Only)';
          badge.className = 'px-2.5 py-0.5 rounded-full text-[10px] font-mono font-semibold bg-amber-500/15 border border-amber-500/30 text-amber-300';
        }
      }

      // Render interactive chip list
      const container = document.getElementById('codeAllowlistChips');
      if (container) {
        container.textContent = '';
        if (currentWorkerAllowlist.length === 0) {
          const emptySpan = document.createElement('span');
          emptySpan.className = 'text-xs text-outline italic p-1';
          emptySpan.textContent = 'Default Deny — no remote peers can execute code on this machine.';
          container.appendChild(emptySpan);
        } else {
          currentWorkerAllowlist.forEach(p => {
            const span = document.createElement('span');
            if (p === '*') {
              span.className = 'px-2.5 py-1 rounded-md text-xs font-mono font-semibold bg-amber-500/20 text-amber-300 border border-amber-500/40 flex items-center gap-1.5';
              const warnIcon = document.createElement('span');
              warnIcon.className = 'material-symbols-outlined text-sm';
              warnIcon.textContent = 'warning';
              const label = document.createElement('span');
              label.textContent = 'Wildcard (*) active — any authenticated cluster peer can submit Docker code jobs.';
              const rmBtn = document.createElement('button');
              rmBtn.type = 'button';
              rmBtn.className = 'btn-remove-chip ml-1 text-amber-300 hover:text-white font-bold leading-none cursor-pointer';
              rmBtn.title = 'Remove wildcard';
              rmBtn.textContent = '\u00d7';
              rmBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                const nextList = currentWorkerAllowlist.filter(item => item !== '*');
                saveWorkerAllowlist(nextList);
              });
              span.append(warnIcon, label, rmBtn);
            } else {
              span.className = 'px-2.5 py-1 rounded-md text-xs font-mono bg-surface-high text-on-surface border border-outline-variant/50 flex items-center gap-1.5';
              const label = document.createElement('span');
              label.textContent = p;
              const rmBtn = document.createElement('button');
              rmBtn.type = 'button';
              rmBtn.className = 'btn-remove-chip ml-1 text-outline hover:text-error font-bold leading-none cursor-pointer';
              rmBtn.title = 'Remove peer';
              rmBtn.textContent = '\u00d7';
              rmBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                const nextList = currentWorkerAllowlist.filter(item => item !== p);
                saveWorkerAllowlist(nextList);
              });
              span.append(label, rmBtn);
            }
            container.appendChild(span);
          });
        }
      }

      // Update fleet peer suggestions
      updateFleetSuggestions();
    })
    .catch(() => {});
}

function updateFleetSuggestions() {
  const wrap = document.getElementById('fleetSuggestionsWrap');
  const list = document.getElementById('fleetSuggestionsList');
  if (!wrap || !list) return;

  const candidatePeers = (currentFleet || []).filter(p => {
    return p && p !== currentLocalId && !currentWorkerAllowlist.includes(p) && !currentWorkerAllowlist.includes('*');
  });

  if (candidatePeers.length === 0) {
    wrap.classList.add('hidden');
    list.innerHTML = '';
    return;
  }

  wrap.classList.remove('hidden');
  list.innerHTML = '';
  candidatePeers.forEach(p => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'px-2 py-0.5 rounded bg-surface-high hover:bg-surface-highest border border-outline-variant/30 text-[11px] text-primary font-mono cursor-pointer transition-colors';
    btn.textContent = `+ ${p}`;
    btn.title = `Authorize ${p}`;
    btn.addEventListener('click', () => {
      saveWorkerAllowlist([...currentWorkerAllowlist, p]);
    });
    list.appendChild(btn);
  });
}

function initShardMatrix() {
  const verifyBtn = document.getElementById('btnVerifyAllMerkle');
  if (verifyBtn) {
    verifyBtn.addEventListener('click', () => {
      const badgeCas = document.getElementById('merkleBadgeCas');

      const setVerifying = (el) => {
        if (!el) return;
        el.className = 'merkle-badge-verifying px-2.5 py-1 rounded font-mono text-xs flex items-center gap-1.5';
        el.innerHTML = '<span class="material-symbols-outlined text-sm">sync</span><span>Verifying SHA-256 Hashes...</span>';
      };
      setVerifying(badgeCas);

      fetch('/api/storage/scrub', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}'
      })
        .then(r => {
          if (!r.ok) throw new Error(`HTTP ${r.status}`);
          return r.json();
        })
        .then(res => {
          setTimeout(() => {
            if (!badgeCas) return;
            const corruptCount = res.corrupted ? res.corrupted.length : 0;
            if (corruptCount === 0) {
              badgeCas.className = 'merkle-badge-valid px-2.5 py-1 rounded bg-secondary/15 border border-secondary/40 text-secondary font-mono text-xs flex items-center gap-1.5';
              badgeCas.innerHTML = '<span class="material-symbols-outlined text-sm">check_circle</span><span>CAS Root: Valid / Matched</span>';
              appendLog('STORAGE', `Cryptographic audit complete: 100% stored blobs verified with zero corruption.`);
            } else {
              badgeCas.className = 'merkle-badge-error px-2.5 py-1 rounded bg-rose-500/15 border border-rose-500/40 text-rose-300 font-mono text-xs flex items-center gap-1.5';
              badgeCas.innerHTML = `<span class="material-symbols-outlined text-sm">warning</span><span>Detected ${corruptCount} Corrupted</span>`;
              appendLog('STORAGE', `Warning: detected ${corruptCount} corrupted blob(s)!`);
            }
          }, 800);
        })
        .catch(err => {
          if (badgeCas) {
            badgeCas.className = 'merkle-badge-error px-2.5 py-1 rounded bg-rose-500/15 border border-rose-500/40 text-rose-300 font-mono text-xs flex items-center gap-1.5';
            badgeCas.innerHTML = '<span class="material-symbols-outlined text-sm">error</span><span>Audit Failed</span>';
          }
          appendLog('STORAGE', `Storage scrub error: ${err.message}`);
        });
    });
  }

  const shardBtn = document.getElementById('btnShardNewDataset');
  if (shardBtn) {
    shardBtn.addEventListener('click', () => {
      const input = document.getElementById('storageContentInput');
      if (input) {
        input.value = `// Dataset partition payload\n// SHA-256 content-addressed chunk storage\nconst DATASET_SIGNATURE = "ds_partition_${Date.now()}";`;
        input.focus();
        input.scrollIntoView({ behavior: 'smooth' });
      }
    });
  }
}

function initTaskQueue() {
  const refreshBtn = document.getElementById('btnRefreshTasks');
  if (refreshBtn) {
    refreshBtn.addEventListener('click', loadTasks);
  }

  loadTasks();
}

function loadTasks() {
  fetch('/api/tasks')
    .then(r => r.json())
    .then(data => {
      if (!data || !data.tasks) return;
      const tbody = document.getElementById('taskQueueTableBody');
      const countBadge = document.getElementById('taskQueueCount');
      if (!tbody) return;

      if (countBadge) {
        countBadge.textContent = `Showing ${data.tasks.length} Active Tasks`;
      }

      tbody.textContent = '';
      if (data.tasks.length === 0) {
        const emptyTr = document.createElement('tr');
        const emptyTd = document.createElement('td');
        emptyTd.colSpan = 6;
        emptyTd.className = 'empty-cell';
        emptyTd.textContent = 'No active tasks in cluster queue.';
        emptyTr.appendChild(emptyTd);
        tbody.appendChild(emptyTr);
        return;
      }

      data.tasks.forEach(task => {
        const tr = document.createElement('tr');
        tr.className = 'hover:bg-surface-high/60 transition-colors';

        const priority = String(task.priority || '');
        let priClass = 'priority-p2';
        if (priority.includes('P0') || priority.includes('CRITICAL')) priClass = 'priority-p0';
        else if (priority.includes('P1') || priority.includes('HIGH')) priClass = 'priority-p1';
        else if (priority.includes('P3') || priority.includes('LOW')) priClass = 'priority-p3';

        const status = String(task.status || '');
        let statusClass = 'task-status-running';
        if (status.includes('COMPLETED')) statusClass = 'task-status-completed';
        else if (status.includes('DISPATCHED')) statusClass = 'task-status-dispatched';
        else if (status.includes('QUEUED')) statusClass = 'task-status-queued';
        else if (status.includes('FAILED')) statusClass = 'task-status-failed';

        const tdJob = document.createElement('td');
        tdJob.className = 'font-bold text-primary font-mono';
        tdJob.textContent = task.job_id || '';

        const tdClassAd = document.createElement('td');
        tdClassAd.className = 'text-on-surface font-mono';
        tdClassAd.textContent = task.class_ad || '';

        const tdMatch = document.createElement('td');
        tdMatch.className = 'text-secondary font-mono';
        tdMatch.textContent = task.target_match || '';

        const tdStage = document.createElement('td');
        tdStage.className = 'text-on-surface-variant font-mono';
        tdStage.textContent = task.stage_dag || '';

        const tdPri = document.createElement('td');
        const priSpan = document.createElement('span');
        priSpan.className = priClass;
        priSpan.textContent = priority;
        tdPri.appendChild(priSpan);

        const tdStatus = document.createElement('td');
        tdStatus.className = `${statusClass} font-mono`;
        tdStatus.textContent = `${status}${task.duration_ms ? ` (${task.duration_ms}ms)` : ''}`;

        tr.append(tdJob, tdClassAd, tdMatch, tdStage, tdPri, tdStatus);
        tbody.appendChild(tr);
      });
    })
    .catch(() => {});
}
