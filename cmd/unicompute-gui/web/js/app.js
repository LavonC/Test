/**
 * UniCompute Light Control Room Controller
 * Reconciled 1:1 with cmd/unicompute-gui/api.go, pkg/gpu/types.go, pkg/gpu/watchdog/watchdog.go.
 * Zero inline HTML event handlers; all dynamic strings escaped; all HTTP responses checked with resp.ok.
 */

// =========================================================================
// AUTH TOKEN & API INTERCEPTOR
// =========================================================================
(function () {
  const hash = window.location.hash || '';
  const match = hash.match(/token=([a-fA-F0-9]+)/);
  if (match && match[1]) {
    sessionStorage.setItem('unicompute_gui_token', match[1]);
    if (window.history && typeof window.history.replaceState === 'function') {
      window.history.replaceState(null, '', window.location.pathname + window.location.search);
    }
  }
})();

function getAuthToken() {
  return sessionStorage.getItem('unicompute_gui_token') || '';
}

const _originalFetch = window.fetch;
window.fetch = function (url, options = {}) {
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

async function apiFetchJson(url, options = {}) {
  const resp = await fetch(url, options);
  const raw = await resp.text();
  let data = null;
  try {
    data = raw ? JSON.parse(raw) : {};
  } catch (e) {
    data = { raw };
  }
  if (!resp.ok) {
    const msg = (data && data.error) ? data.error : (raw || `HTTP ${resp.status}`);
    throw new Error(msg);
  }
  return data;
}

// =========================================================================
// STATE & CONSTANTS
// =========================================================================
const defaultCudaSample = `#include <stdio.h>
#include <stdlib.h>
#include <cuda_runtime.h>

__global__ void vectorAdd(const float *A, const float *B, float *C, int N) {
    int i = blockDim.x * blockIdx.x + threadIdx.x;
    if (i < N) {
        C[i] = A[i] + B[i];
    }
}

int main() {
    int N = 1024;
    size_t size = N * sizeof(float);
    float *d_A = NULL, *d_B = NULL, *d_C = NULL;
    cudaMalloc((void **)&d_A, size);
    cudaMalloc((void **)&d_B, size);
    cudaMalloc((void **)&d_C, size);
    vectorAdd<<<4, 256>>>(d_A, d_B, d_C, N);
    cudaDeviceSynchronize();
    cudaFree(d_A);
    cudaFree(d_B);
    cudaFree(d_C);
    return 0;
}`;

const defaultPythonSample = `import math, sys
data = sys.stdin.read().strip()
print("UniCompute Tier 2 Python container ok")
print("2^16 =", 2 ** 16)
if data:
    print("stdin:", data)
`;

const state = {
  currentPage: 'overview',
  backendConnected: false,
  localNodeId: 'local-node',
  poolId: 'default-pool',
  hostMeta: {
    os: '-',
    arch: '-',
    cpus: 0,
    goVersion: '-',
    dockerAvailable: false,
    natType: 'Unknown',
    isBehindCGNAT: false,
    storageUsedBytes: 0,
    storageQuotaBytes: 0,
    blobCount: 0,
  },
  coordStatus: {
    status: 'DISCONNECTED',
    authRequired: false,
    authTokenSet: false,
    coordinatorURL: '',
    coordinatorHost: '',
    uptime: '0h 00m 00s',
    fleetCount: 0,
    directPeersCount: 0,
    relayedPeersCount: 0,
    avgRttMs: 0,
    transport: 'WebRTC DataChannel (ICE/DTLS/SCTP)',
  },
  localGpuProfile: null,
  localWatchdog: null,
  gpuEnabled: false,

  selectedTopoNodeId: 'local-node',
  topoViewMode: 'mesh',
  peerPingResults: {},

  gpuSelectedFile: 'vector_add.cu',
  cudaSamples: [],
  allSamples: [],
  cudaSource: defaultCudaSample,
  hipSource: '',
  hipifyResult: null,
  hipifyRunning: false,

  sandbox: {
    language: 'python',
    targetPeer: 'local',
    source: defaultPythonSample,
    stdin: '',
    timeout: 10,
    selectedSample: '',
    running: false,
    result: null,
    shellPeer: 'local',
    shellCommand: '',
    shellRunning: false,
    shellResult: null,
  },

  workerSettings: {
    codeAllowlist: [],
    dockerActive: false,
    wildcardAllowed: false,
    newPeerInput: '',
  },

  taskFilterTab: 'All',
  taskSearch: '',
  selectedTaskId: '',

  createForm: {
    title: '',
    tier: 'Tier 3 GPU',
    targetNode: 'Auto',
    language: 'python',
    source: defaultPythonSample,
    stdin: '',
    timeout: 10,
    gpuTaskType: 'inference',
    batches: 10,
    gpuSourceCode: '',
  },

  vaultDatasets: [],
  nodeSearch: '',
  logTab: 'All',
  logSeverity: 'All',
  logSearch: '',

  settingsDraft: {
    coordinatorURL: '',
    authToken: '',
  },

  nodes: [],
  connectedPeers: [],
  tasks: [],
  logs: [],
  telemetryHistory: [],
};

let wsInstance = null;
let wsReconnectAttempts = 0;
let wsReconnectTimer = null;

// =========================================================================
// OFFLINE SVG ICONS & CANVAS CHART
// =========================================================================
const ICON_PATHS = {
  'activity': '<polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/>',
  'alert-triangle': '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
  'arrow-left': '<path d="m12 19-7-7 7-7"/><path d="M19 12H5"/>',
  'boxes': '<path d="M2.97 12.92A2 2 0 0 0 2 14.63v3.24a2 2 0 0 0 .97 1.71l3 1.8a2 2 0 0 0 2.06 0L12 19v-5.5l-5-3z"/><path d="m7 16.5-4.74-2.85"/><path d="m7 16.5 5-3"/><path d="M7 16.5v5.17"/><path d="M12 13.5V19l3.97 2.38a2 2 0 0 0 2.06 0l3-1.8a2 2 0 0 0 .97-1.71v-3.24a2 2 0 0 0-.97-1.71L17 10.5z"/><path d="m17 16.5-5-3"/><path d="m17 16.5 4.74-2.85"/><path d="M17 16.5v5.17"/><path d="M7.97 4.42A2 2 0 0 0 7 6.13v4.37l5 3 5-3V6.13a2 2 0 0 0-.97-1.71l-3-1.8a2 2 0 0 0-2.06 0z"/><path d="M12 8 7.26 5.15"/><path d="m12 8 4.74-2.85"/><path d="M12 13.5V8"/>',
  'check-circle-2': '<circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/>',
  'code-2': '<path d="m18 16 4-4-4-4"/><path d="m6 8-4 4 4 4"/><path d="m14.5 4-5 16"/>',
  'copy': '<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
  'cpu': '<rect width="16" height="16" x="4" y="4" rx="2"/><rect width="6" height="6" x="9" y="9" rx="1"/><path d="M15 2v2"/><path d="M15 20v2"/><path d="M2 15h2"/><path d="M2 9h2"/><path d="M20 15h2"/><path d="M20 9h2"/><path d="M9 2v2"/><path d="M9 20v2"/>',
  'database': '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5V19A9 3 0 0 0 21 19V5"/><path d="M3 12A9 3 0 0 0 21 12"/>',
  'download': '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/>',
  'file-up': '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M12 12v6"/><path d="m15 15-3-3-3 3"/>',
  'info': '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>',
  'layers': '<path d="m12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z"/><path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65"/><path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65"/>',
  'layout-dashboard': '<rect width="7" height="9" x="3" y="3" rx="1"/><rect width="7" height="5" x="14" y="3" rx="1"/><rect width="7" height="9" x="14" y="12" rx="1"/><rect width="7" height="5" x="3" y="16" rx="1"/>',
  'list-checks': '<path d="m3 17 2 2 4-4"/><path d="m3 7 2 2 4-4"/><path d="M13 6h8"/><path d="M13 12h8"/><path d="M13 18h8"/>',
  'menu': '<line x1="4" x2="20" y1="12" y2="12"/><line x1="4" x2="20" y1="6" y2="6"/><line x1="4" x2="20" y1="18" y2="18"/>',
  'plus': '<path d="M5 12h14"/><path d="M12 5v14"/>',
  'search': '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>',
  'server': '<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/>',
  'share-2': '<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><line x1="8.59" x2="15.42" y1="13.51" y2="17.49"/><line x1="15.41" x2="8.59" y1="6.51" y2="10.49"/>',
  'shield-alert': '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="M12 8v4"/><path d="M12 16h.01"/>',
  'shield-check': '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="m9 12 2 2 4-4"/>',
  'sliders-horizontal': '<line x1="21" x2="14" y1="4" y2="4"/><line x1="10" x2="3" y1="4" y2="4"/><line x1="21" x2="12" y1="12" y2="12"/><line x1="8" x2="3" y1="12" y2="12"/><line x1="21" x2="16" y1="20" y2="20"/><line x1="12" x2="3" y1="20" y2="20"/><line x1="14" x2="14" y1="2" y2="6"/><line x1="8" x2="8" y1="10" y2="14"/><line x1="16" x2="16" y1="18" y2="22"/>',
  'terminal': '<polyline points="4 17 10 11 4 5"/><line x1="12" x2="20" y1="19" y2="19"/>',
  'x': '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
};

function renderIcons(root = document) {
  const nodes = root.querySelectorAll ? root.querySelectorAll('i[data-lucide]') : [];
  nodes.forEach(el => {
    const name = el.getAttribute('data-lucide');
    const paths = ICON_PATHS[name];
    if (!paths) return;
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '2');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    const cls = el.getAttribute('class');
    if (cls) svg.setAttribute('class', cls);
    svg.setAttribute('aria-hidden', 'true');
    svg.innerHTML = paths;
    el.replaceWith(svg);
  });
}

function drawOverviewChart(canvas, samples) {
  if (!canvas || !canvas.getContext) return;
  const parent = canvas.parentElement;
  const width = (parent && parent.clientWidth) ? parent.clientWidth : 600;
  const height = (parent && parent.clientHeight) ? parent.clientHeight : 256;
  const dpr = window.devicePixelRatio || 1;
  canvas.width = Math.floor(width * dpr);
  canvas.height = Math.floor(height * dpr);
  canvas.style.width = width + 'px';
  canvas.style.height = height + 'px';

  const ctx = canvas.getContext('2d');
  ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, width, height);

  const pad = { top: 12, right: 16, bottom: 26, left: 36 };
  const plotW = Math.max(40, width - pad.left - pad.right);
  const plotH = Math.max(40, height - pad.top - pad.bottom);

  let maxVal = 100;
  samples.forEach(s => {
    maxVal = Math.max(maxVal, Number(s.vramPct || 0), Number(s.storagePct || 0), Number(s.avgRttMs || 0));
  });
  maxVal = Math.ceil(maxVal / 25) * 25;
  if (maxVal <= 0) maxVal = 100;

  ctx.font = '11px system-ui, -apple-system, sans-serif';
  ctx.textBaseline = 'middle';
  const gridSteps = 4;
  for (let i = 0; i <= gridSteps; i++) {
    const val = Math.round((maxVal / gridSteps) * i);
    const y = pad.top + plotH - (i / gridSteps) * plotH;
    ctx.strokeStyle = '#F1F5F9';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(pad.left, y);
    ctx.lineTo(pad.left + plotW, y);
    ctx.stroke();

    ctx.fillStyle = '#64748B';
    ctx.textAlign = 'right';
    ctx.fillText(String(val), pad.left - 8, y);
  }

  const count = samples.length;
  const xFor = (idx) => count <= 1 ? pad.left + plotW / 2 : pad.left + (idx / (count - 1)) * plotW;
  const yFor = (v) => pad.top + plotH - (Math.max(0, Number(v || 0)) / maxVal) * plotH;

  // X-axis labels (up to 5 ticks)
  ctx.fillStyle = '#64748B';
  ctx.textAlign = 'center';
  ctx.textBaseline = 'top';
  const labelStep = Math.max(1, Math.ceil(count / 5));
  for (let i = 0; i < count; i += labelStep) {
    ctx.fillText(String(samples[i].label || ''), xFor(i), pad.top + plotH + 8);
  }

  const drawSeries = (vals, color, fillColor, dashed) => {
    if (!vals.length) return;
    ctx.save();
    if (dashed) ctx.setLineDash([4, 4]);
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.beginPath();
    vals.forEach((v, idx) => {
      const x = xFor(idx);
      const y = yFor(v);
      if (idx === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.stroke();

    if (fillColor && vals.length > 1) {
      ctx.setLineDash([]);
      ctx.lineTo(xFor(vals.length - 1), pad.top + plotH);
      ctx.lineTo(xFor(0), pad.top + plotH);
      ctx.closePath();
      ctx.fillStyle = fillColor;
      ctx.fill();
    }

    ctx.setLineDash([]);
    ctx.fillStyle = color;
    vals.forEach((v, idx) => {
      ctx.beginPath();
      ctx.arc(xFor(idx), yFor(v), 2.5, 0, Math.PI * 2);
      ctx.fill();
    });
    ctx.restore();
  };

  drawSeries(samples.map(s => s.vramPct), '#8B5CF6', 'rgba(139,92,246,0.08)', false);
  drawSeries(samples.map(s => s.storagePct), '#0EA5E9', null, false);
  drawSeries(samples.map(s => s.avgRttMs), '#10B981', null, true);
}

// =========================================================================
// UTILITIES
// =========================================================================
function escapeHtml(str) {
  return String(str ?? '').replace(/[&<>"']/g, s => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  }[s]));
}

function formatBytes(bytes) {
  const n = Number(bytes || 0);
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

function destroyCharts() {
  // No external chart library instances to destroy; canvas redraws on page render.
}

function showToast(message, tone = 'success') {
  const stack = document.getElementById('toastStack');
  const el = document.createElement('div');
  const iconName = tone === 'warning' || tone === 'error' ? 'alert-triangle' : tone === 'info' ? 'info' : 'check-circle-2';
  const iconColor = tone === 'error' ? 'text-rose-400' : tone === 'warning' ? 'text-amber-400' : tone === 'info' ? 'text-sky-400' : 'text-emerald-400';
  el.className = 'pointer-events-auto flex items-center gap-2.5 bg-slate-900 text-white text-xs font-medium px-4 py-3 rounded-xl shadow-lg border border-slate-700 max-w-sm';
  el.innerHTML = `<i data-lucide="${iconName}" class="w-4 h-4 shrink-0 ${iconColor}"></i><span>${escapeHtml(message)}</span>`;
  stack.appendChild(el);
  renderIcons(el);
  setTimeout(() => {
    el.style.opacity = '0';
    el.style.transition = 'opacity 0.25s ease';
    setTimeout(() => el.remove(), 260);
  }, 3200);
}

function copyText(text, label = 'Copied to clipboard') {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(() => showToast(label)).catch(() => fallbackCopy(text, label));
  } else {
    fallbackCopy(text, label);
  }
}

function fallbackCopy(text, label) {
  const ta = document.createElement('textarea');
  ta.value = text;
  document.body.appendChild(ta);
  ta.select();
  try { document.execCommand('copy'); showToast(label); } catch (e) {}
  ta.remove();
}

function toggleMobileNav(open) {
  const sb = document.getElementById('sidebar');
  const bd = document.getElementById('mobileBackdrop');
  if (open) {
    sb.classList.remove('-translate-x-full');
    bd.classList.remove('hidden');
  } else {
    sb.classList.add('-translate-x-full');
    bd.classList.add('hidden');
  }
}

function openModal(title, bodyHtml) {
  document.getElementById('modalTitle').textContent = title;
  document.getElementById('modalBody').innerHTML = bodyHtml;
  const bd = document.getElementById('modalBackdrop');
  bd.classList.remove('hidden');
  bd.classList.add('flex');
  renderIcons(bd);
}

function closeModal() {
  const bd = document.getElementById('modalBackdrop');
  bd.classList.add('hidden');
  bd.classList.remove('flex');
}

function mapTaskStatus(rawStatus) {
  const s = String(rawStatus || '').toUpperCase();
  if (s === 'QUEUED' || s === 'PENDING') return 'Pending';
  if (s === 'RUNNING' || s === 'DISPATCHED') return 'Running';
  if (s === 'COMPLETED') return 'Completed';
  if (s.startsWith('FAILED') || s === 'REJECTED' || s === 'PREEMPTED') return 'Failed';
  return 'Running';
}

function classifyEvent(evt) {
  const t = String(evt.type || '').toLowerCase();
  const m = String(evt.message || '').toLowerCase();

  let sev = 'Info';
  if (t.includes('fail') || t.includes('error') || t.includes('reject') || m.includes('failed')) {
    sev = 'Error';
  } else if (t.includes('warn') || t.includes('preempt') || t.includes('disconnect') || t.includes('left') || t.includes('timeout')) {
    sev = 'Warning';
  }

  let cat = 'Coordinator';
  if (t.includes('peer') || t.includes('p2p') || t.includes('connection')) cat = 'Network';
  else if (t.includes('storage') || t.includes('chunk')) cat = 'Storage';
  else if (t.includes('task')) cat = 'Tasks';
  else if (t.includes('worker')) cat = 'Worker';

  const tsRaw = evt.timestamp ? new Date(evt.timestamp) : new Date();
  const ts = isNaN(tsRaw.getTime()) ? new Date().toTimeString().slice(0, 8) : tsRaw.toTimeString().slice(0, 8);

  return {
    ts,
    sev,
    cat,
    src: evt.peer_id || state.localNodeId,
    type: evt.type || 'event',
    msg: evt.message || (evt.payload ? JSON.stringify(evt.payload) : ''),
  };
}

// =========================================================================
// BACKEND SYNC & WEBSOCKET
// =========================================================================
async function syncBackend() {
  try {
    const [statusRes, coordRes, gpuRes, fleetRes, peersRes, tasksRes, blobsRes, workerRes] = await Promise.allSettled([
      apiFetchJson('/api/status'),
      apiFetchJson('/api/coordinator/status'),
      apiFetchJson('/api/gpu'),
      apiFetchJson('/api/fleet'),
      apiFetchJson('/api/peers'),
      apiFetchJson('/api/tasks'),
      apiFetchJson('/api/storage/blobs'),
      apiFetchJson('/api/worker/settings'),
    ]);

    const status = statusRes.status === 'fulfilled' ? statusRes.value : null;
    const coord = coordRes.status === 'fulfilled' ? coordRes.value : null;
    const gpuData = gpuRes.status === 'fulfilled' ? gpuRes.value : null;
    const fleetData = fleetRes.status === 'fulfilled' ? fleetRes.value : null;
    const peersData = peersRes.status === 'fulfilled' ? peersRes.value : null;
    const tasksData = tasksRes.status === 'fulfilled' ? tasksRes.value : null;
    const blobsData = blobsRes.status === 'fulfilled' ? blobsRes.value : null;
    const workerData = workerRes.status === 'fulfilled' ? workerRes.value : null;

    if (status) {
      state.backendConnected = true;
      state.localNodeId = status.node_id || state.localNodeId;
      state.poolId = status.pool_id || state.poolId;
      state.gpuEnabled = Boolean(status.gpu_enabled);
      state.hostMeta = {
        os: status.os || '-',
        arch: status.arch || '-',
        cpus: status.cpus || 0,
        goVersion: status.go_version || '-',
        dockerAvailable: Boolean(status.docker_available),
        natType: status.nat?.nat_type || 'Unknown',
        isBehindCGNAT: Boolean(status.nat?.is_behind_cgnat),
        storageUsedBytes: status.storage?.used_bytes || 0,
        storageQuotaBytes: status.storage?.quota_bytes || 0,
        blobCount: status.storage?.blob_count || 0,
      };
      if (!state.settingsDraft.coordinatorURL && status.coordinator_url) {
        state.settingsDraft.coordinatorURL = status.coordinator_url;
      }
    }

    if (coord) {
      state.coordStatus = {
        status: coord.status || 'DISCONNECTED',
        authRequired: Boolean(coord.auth_required),
        authTokenSet: Boolean(coord.auth_token_set),
        coordinatorURL: coord.coordinator_url || '',
        coordinatorHost: coord.coordinator_host || '',
        uptime: coord.uptime || '0h 00m 00s',
        fleetCount: Number(coord.fleet_count || 0),
        directPeersCount: Number(coord.direct_peers_count || 0),
        relayedPeersCount: Number(coord.relayed_peers_count || 0),
        avgRttMs: Number(coord.avg_rtt_ms || 0),
        transport: coord.transport || 'WebRTC DataChannel (ICE/DTLS/SCTP)',
      };
      if (state.currentPage !== 'settings') {
        state.settingsDraft.coordinatorURL = state.coordStatus.coordinatorURL;
      }
    }

    if (gpuData) {
      state.gpuEnabled = Boolean(gpuData.enabled);
      state.localGpuProfile = gpuData.profile || null;
      state.localWatchdog = gpuData.watchdog || null;
    }

    if (workerData) {
      state.workerSettings.codeAllowlist = Array.isArray(workerData.code_allowlist) ? workerData.code_allowlist : [];
      state.workerSettings.dockerActive = Boolean(workerData.docker_active);
      state.workerSettings.wildcardAllowed = Boolean(workerData.wildcard_allowed);
    }

    // Merge local host + /api/peers + /api/fleet (note: /api/fleet returns { nodes: ["id1", "id2"] })
    const peerMap = new Map();
    const peersArr = (peersData && Array.isArray(peersData.peers)) ? peersData.peers : [];
    state.connectedPeers = peersArr;
    peersArr.forEach(p => {
      if (p && p.peer_id) {
        peerMap.set(p.peer_id, p);
      }
    });

    const fleetStrings = (fleetData && Array.isArray(fleetData.nodes)) ? fleetData.nodes : [];
    const uniquePeerIds = new Set();
    fleetStrings.forEach(id => {
      if (typeof id === 'string' && id && id !== state.localNodeId) {
        uniquePeerIds.add(id);
      }
    });
    peerMap.forEach((_, id) => {
      if (id !== state.localNodeId) {
        uniquePeerIds.add(id);
      }
    });

    const prof = state.localGpuProfile || (status && status.gpu_profile) || {};
    const totalVramGB = prof.total_vram_bytes ? Number((prof.total_vram_bytes / (1024 * 1024 * 1024)).toFixed(2)) : 0;
    const availVramGB = prof.available_vram_bytes ? Number((prof.available_vram_bytes / (1024 * 1024 * 1024)).toFixed(2)) : 0;
    const usedVramGB = totalVramGB > 0 ? Number(Math.max(0, totalVramGB - availVramGB).toFixed(2)) : 0;
    const gpuBackends = Array.isArray(prof.supported_backends) && prof.supported_backends.length
      ? prof.supported_backends
      : (() => {
          const r = [];
          if (prof.cuda_supported) r.push('CUDA');
          if (prof.directml_supported) r.push('DirectML');
          if (prof.vulkan_supported) r.push('Vulkan');
          if (!r.length) r.push('CPU');
          return r;
        })();

    const nodeList = [{
      id: state.localNodeId,
      isLocal: true,
      connectedP2P: true,
      lab: state.poolId || 'Local Pool',
      tier: totalVramGB > 0 ? 'Tier 3 GPU' : (state.hostMeta.dockerAvailable ? 'Tier 2 Container' : 'Tier 1/Host'),
      os: `${state.hostMeta.os}/${state.hostMeta.arch}`,
      cpus: state.hostMeta.cpus,
      gpuModel: prof.device_name || 'CPU / Integrated',
      gpuVendor: prof.vendor || 'Unknown',
      vramUsed: usedVramGB,
      vramTotal: totalVramGB,
      tempC: Number(prof.temperature_c || 0),
      powerW: Number(prof.power_usage_w || 0),
      rttMs: 0,
      pairType: 'local',
      transport: 'Local Host',
      status: 'Online',
      blobs: state.hostMeta.blobCount,
      runtimes: gpuBackends,
      bytesSent: 0,
      bytesReceived: 0,
      color: '#0284C7',
      pos: [50, 22],
    }];

    const remoteIds = Array.from(uniquePeerIds).sort();
    remoteIds.forEach((peerId, idx) => {
      const pInfo = peerMap.get(peerId);
      const isConnected = Boolean(pInfo);
      const metrics = pInfo?.metrics || {};
      const angle = ((idx + 1) / (remoteIds.length + 1)) * Math.PI * 2;
      const px = Math.round(50 + 32 * Math.cos(angle));
      const py = Math.round(50 + 34 * Math.sin(angle));
      const transportLabel = isConnected
        ? (metrics.is_direct || pInfo.is_direct ? `Direct WebRTC (${metrics.pair_type || 'p2p'})` : `Relayed (${metrics.pair_type || 'relay'})`)
        : 'Signaling Only';
      nodeList.push({
        id: peerId,
        isLocal: false,
        connectedP2P: isConnected,
        lab: state.poolId || 'Fleet Peer',
        tier: 'Fleet Peer',
        os: 'Remote Peer',
        cpus: 0,
        gpuModel: 'Remote Capability',
        gpuVendor: 'Unknown',
        vramUsed: 0,
        vramTotal: 0,
        tempC: 0,
        powerW: 0,
        rttMs: isConnected ? Number((metrics.rtt_ms || 0).toFixed(2)) : 0,
        pairType: metrics.pair_type || '',
        transport: transportLabel,
        status: isConnected ? 'Online' : 'Discovered',
        blobs: 0,
        runtimes: ['WebRTC DataChannel'],
        bytesSent: metrics.bytes_sent || 0,
        bytesReceived: metrics.bytes_received || 0,
        color: isConnected ? '#059669' : '#64748B',
        pos: [px, py],
      });
    });

    state.nodes = nodeList;
    if (!state.nodes.find(n => n.id === state.selectedTopoNodeId)) {
      state.selectedTopoNodeId = state.localNodeId;
    }

    if (tasksData && Array.isArray(tasksData.tasks)) {
      state.tasks = tasksData.tasks.map(t => {
        const mappedStatus = mapTaskStatus(t.status);
        return {
          id: t.job_id,
          title: t.class_ad || t.stage_dag || t.job_id,
          kind: t.stage_dag || 'Cluster Task',
          tier: (t.target_match || '').includes('Tier 3') ? 'Tier 3 GPU' : 'Tier 2 Container',
          specs: t.target_match || 'Matched',
          worker: t.target_match || state.localNodeId,
          priority: t.priority || 'P1',
          durationMs: t.duration_ms ?? null,
          timestamp: t.timestamp || '',
          rawStatus: t.status || '',
          status: mappedStatus,
          progress: mappedStatus === 'Completed' ? 100 : (mappedStatus === 'Failed' ? 0 : 50),
        };
      });
      if (!state.selectedTaskId && state.tasks.length) {
        state.selectedTaskId = state.tasks[0].id;
      }
    }

    if (blobsData && Array.isArray(blobsData.blobs)) {
      state.vaultDatasets = blobsData.blobs.map(b => ({
        id: b.id,
        sizeBytes: b.size_bytes ?? b.size ?? 0,
        sizeLabel: formatBytes(b.size_bytes ?? b.size ?? 0),
        createdAt: b.created_at ?? b.mod_time ?? '',
      }));
    }

    // Record real telemetry sample for Overview live chart
    const storagePct = state.hostMeta.storageQuotaBytes > 0
      ? Math.min(100, Math.round((state.hostMeta.storageUsedBytes / state.hostMeta.storageQuotaBytes) * 100))
      : 0;
    const vramPct = totalVramGB > 0 ? Math.min(100, Math.round((usedVramGB / totalVramGB) * 100)) : 0;
    state.telemetryHistory.push({
      label: new Date().toTimeString().slice(0, 8),
      vramPct,
      storagePct,
      avgRttMs: Number((state.coordStatus.avgRttMs || 0).toFixed(1)),
    });
    if (state.telemetryHistory.length > 15) {
      state.telemetryHistory.shift();
    }

    updateChrome();

    // Never re-render while typing in inputs/textareas/selects, and never wipe active search pages
    const activeTag = document.activeElement ? document.activeElement.tagName : '';
    if (activeTag !== 'TEXTAREA' && activeTag !== 'INPUT' && activeTag !== 'SELECT') {
      renderCurrentPageOnly();
    }
  } catch (err) {
    console.warn('Backend sync warning:', err);
  }
}

async function loadSamplesFromBackend() {
  try {
    const data = await apiFetchJson('/api/samples');
    const items = Array.isArray(data) ? data : (data.samples || []);
    state.allSamples = items;
    state.cudaSamples = items.filter(s => s.language === 'cuda' || (s.name && s.name.endsWith('.cu')));
    if (state.cudaSamples.length > 0 && state.cudaSource === defaultCudaSample) {
      state.gpuSelectedFile = state.cudaSamples[0].name;
      state.cudaSource = state.cudaSamples[0].content;
    }
    if (state.currentPage === 'gpu' || state.currentPage === 'sandbox') {
      renderCurrentPageOnly();
    }
  } catch (e) {
    console.warn('Samples fetch failed:', e);
  }
}

function initWebSocket() {
  if (wsInstance && (wsInstance.readyState === WebSocket.OPEN || wsInstance.readyState === WebSocket.CONNECTING)) {
    return;
  }
  try {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const token = getAuthToken();
    const wsUrl = `${proto}//${window.location.host}/api/events${token ? '?token=' + encodeURIComponent(token) : ''}`;
    const ws = new WebSocket(wsUrl);
    wsInstance = ws;

    ws.onopen = () => {
      wsReconnectAttempts = 0;
    };

    ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data);
        state.logs.unshift(classifyEvent(msg));
        if (state.logs.length > 150) state.logs.pop();
        if (state.currentPage === 'logs') {
          updateLogsTableOnly();
        } else if (state.currentPage === 'overview') {
          const activeTag = document.activeElement ? document.activeElement.tagName : '';
          if (activeTag !== 'INPUT' && activeTag !== 'TEXTAREA') {
            renderCurrentPageOnly();
          }
        }
      } catch (e) {}
    };

    ws.onclose = () => {
      wsInstance = null;
      if (wsReconnectTimer) clearTimeout(wsReconnectTimer);
      const delay = Math.min(16000, 1000 * Math.pow(2, wsReconnectAttempts));
      wsReconnectAttempts += 1;
      wsReconnectTimer = setTimeout(initWebSocket, delay);
    };
  } catch (e) {}
}

function updateChrome() {
  const connectedP2PCount = state.nodes.filter(n => !n.isLocal && n.connectedP2P).length;
  const totalPeers = Math.max(0, state.nodes.length - 1);
  document.getElementById('sidebarCoordinatorName').textContent = state.localNodeId;
  document.getElementById('sidebarNodeSummary').textContent = `${connectedP2PCount}/${totalPeers} P2P · ${state.coordStatus.status}`;
  document.getElementById('navTaskBadge').textContent = state.tasks.length;

  const dot = document.getElementById('sidebarStatusDot');
  const pill = document.getElementById('topbarPill');
  const coordSt = state.coordStatus.status;
  let pillCls = 'bg-emerald-50 text-emerald-700 border-emerald-200';
  let dotCls = 'bg-emerald-500';
  if (!state.backendConnected || coordSt === 'DISCONNECTED') {
    pillCls = 'bg-amber-50 text-amber-700 border-amber-200';
    dotCls = 'bg-amber-500';
  } else if (coordSt === 'AUTH_REQUIRED') {
    pillCls = 'bg-rose-50 text-rose-700 border-rose-200';
    dotCls = 'bg-rose-500';
  }
  dot.className = `w-2 h-2 rounded-full shrink-0 ${dotCls}`;
  pill.className = `hidden sm:inline-flex items-center gap-1.5 text-xs font-medium px-2.5 py-1 rounded-full border ${pillCls}`;
  pill.innerHTML = `<span class="w-1.5 h-1.5 rounded-full ${dotCls}"></span><span>${escapeHtml(coordSt)} · ${connectedP2PCount}/${totalPeers} P2P</span>`;

  const banner = document.getElementById('coordAuthBanner');
  if (banner) {
    if (state.coordStatus.authRequired) {
      banner.classList.remove('hidden');
    } else {
      banner.classList.add('hidden');
    }
  }
}

// =========================================================================
// NAVIGATION ROUTER
// =========================================================================
const pageTitles = {
  overview: 'Overview',
  topology: 'Network Topology',
  sandbox: 'CPU Sandbox & Remote Shell',
  gpu: 'GPU Compute & HIPIFY',
  worker: 'Worker Allowlist & Sandbox',
  tasks: 'Task Queue',
  'create-task': 'Create Task',
  'task-detail': 'Task Execution & Details',
  storage: 'Storage & CAS Blobs',
  nodes: 'Fleet Nodes',
  logs: 'Live Event Logs',
  settings: 'Coordinator & Node Settings',
  attribution: 'Architecture Notes',
};

function renderCurrentPageOnly() {
  const page = state.currentPage;
  const container = document.getElementById('pageContainer');
  destroyCharts();
  if (page === 'overview') renderOverview(container);
  else if (page === 'topology') renderTopology(container);
  else if (page === 'sandbox') renderSandbox(container);
  else if (page === 'gpu') renderGpuCompute(container);
  else if (page === 'worker') renderWorkerSandbox(container);
  else if (page === 'tasks') renderTaskQueue(container);
  else if (page === 'create-task') renderCreateTask(container);
  else if (page === 'task-detail') renderTaskDetail(container);
  else if (page === 'storage') renderStorage(container);
  else if (page === 'nodes') renderFleetNodes(container);
  else if (page === 'logs') renderLogs(container);
  else if (page === 'settings') renderSettings(container);
  else if (page === 'attribution') renderAttribution(container);
  renderIcons(container);
}

function navigate(page) {
  state.currentPage = page;
  toggleMobileNav(false);
  updateChrome();

  const activeNavKey = (page === 'create-task' || page === 'task-detail') ? 'tasks' : page;
  document.querySelectorAll('.nav-btn').forEach(btn => {
    if (btn.dataset.nav === activeNavKey) {
      btn.classList.add('nav-active');
      btn.classList.remove('nav-idle');
    } else {
      btn.classList.remove('nav-active');
      btn.classList.add('nav-idle');
    }
  });

  document.getElementById('topbarTitle').textContent = pageTitles[page] || 'Overview';
  renderCurrentPageOnly();
}

function openCreateTask(preselectedNode = 'Auto') {
  state.createForm.targetNode = preselectedNode;
  if (preselectedNode && preselectedNode !== 'Auto') {
    state.createForm.tier = 'Tier 2 Container';
  }
  navigate('create-task');
}

function openTaskDetail(taskId) {
  state.selectedTaskId = taskId;
  navigate('task-detail');
}

// =========================================================================
// PAGE 1: OVERVIEW
// =========================================================================
function renderOverview(container) {
  const local = state.nodes.find(n => n.isLocal) || {};
  const remoteNodes = state.nodes.filter(n => !n.isLocal);
  const connectedCount = remoteNodes.filter(n => n.connectedP2P).length;
  const runningTasks = state.tasks.filter(t => t.status === 'Running').length;
  const pendingTasks = state.tasks.filter(t => t.status === 'Pending').length;
  const failedTasks = state.tasks.filter(t => t.status === 'Failed').length;
  const completedTasks = state.tasks.filter(t => t.status === 'Completed').length;
  const vramTotal = Number(local.vramTotal || 0);
  const vramUsed = Number(local.vramUsed || 0);
  const vramPct = vramTotal > 0 ? Math.min(100, Math.round((vramUsed / vramTotal) * 100)) : 0;
  const storagePct = state.hostMeta.storageQuotaBytes > 0
    ? Math.min(100, Math.round((state.hostMeta.storageUsedBytes / state.hostMeta.storageQuotaBytes) * 100))
    : 0;

  container.innerHTML = `
    <div class="space-y-6">
      <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="flex items-center justify-between text-xs text-slate-500 mb-3">
            <span>Coordinator &amp; Fleet</span>
            <i data-lucide="server" class="w-4 h-4 text-slate-400"></i>
          </div>
          <div class="flex items-baseline gap-2">
            <span class="text-2xl font-bold text-slate-900">${connectedCount} / ${remoteNodes.length}</span>
            <span class="text-xs font-medium ${state.coordStatus.status === 'RUNNING' || state.coordStatus.status === 'CONNECTED' ? 'text-emerald-600' : 'text-amber-600'}">${escapeHtml(state.coordStatus.status)}</span>
          </div>
          <div class="mt-2 text-xs text-slate-500 truncate">Host: ${escapeHtml(state.localNodeId)} (${escapeHtml(state.hostMeta.os)}/${escapeHtml(state.hostMeta.arch)}, ${state.hostMeta.cpus} CPU)</div>
        </div>

        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="flex items-center justify-between text-xs text-slate-500 mb-3">
            <span>Cluster Tasks</span>
            <i data-lucide="activity" class="w-4 h-4 text-slate-400"></i>
          </div>
          <div class="flex items-baseline gap-2">
            <span class="text-2xl font-bold text-slate-900">${state.tasks.length}</span>
            <span class="text-xs text-slate-500">${runningTasks} running · ${pendingTasks} pending</span>
          </div>
          <div class="mt-2 text-xs text-slate-500">${completedTasks} completed · ${failedTasks} failed</div>
        </div>

        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="flex items-center justify-between text-xs text-slate-500 mb-3">
            <span>Local GPU VRAM</span>
            <i data-lucide="cpu" class="w-4 h-4 text-slate-400"></i>
          </div>
          <div class="flex items-baseline gap-2">
            <span class="text-2xl font-bold text-slate-900">${vramTotal.toFixed(1)} GB</span>
            <span class="text-xs text-slate-500">${Math.max(0, vramTotal - vramUsed).toFixed(1)} GB free</span>
          </div>
          <div class="mt-3 h-1.5 bg-slate-100 rounded-full overflow-hidden">
            <div class="h-full bg-slate-900 rounded-full" style="width:${vramPct}%"></div>
          </div>
        </div>

        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="flex items-center justify-between text-xs text-slate-500 mb-3">
            <span>CAS Blob Store</span>
            <i data-lucide="database" class="w-4 h-4 text-slate-400"></i>
          </div>
          <div class="flex items-baseline gap-2">
            <span class="text-2xl font-bold text-slate-900">${state.vaultDatasets.length} Blobs</span>
            <span class="text-xs text-slate-500">${formatBytes(state.hostMeta.storageUsedBytes)}</span>
          </div>
          <div class="mt-2 text-xs text-slate-500">${storagePct}% of ${formatBytes(state.hostMeta.storageQuotaBytes)} quota · NAT: ${escapeHtml(state.hostMeta.natType)}</div>
        </div>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-3 gap-5">
        <div class="lg:col-span-2 bg-white rounded-xl border border-slate-200 p-5">
          <div class="flex items-center justify-between mb-4">
            <h2 class="text-sm font-bold text-slate-900">Live Telemetry Samples (From Backend Polls)</h2>
            <div class="flex items-center gap-4 text-xs text-slate-500">
              <span class="flex items-center gap-1.5"><span class="w-2.5 h-2.5 rounded-full bg-violet-500"></span>VRAM %</span>
              <span class="flex items-center gap-1.5"><span class="w-2.5 h-2.5 rounded-full bg-sky-500"></span>Storage %</span>
              <span class="flex items-center gap-1.5"><span class="w-2.5 h-2.5 rounded-full bg-emerald-500"></span>Avg P2P RTT (ms)</span>
            </div>
          </div>
          <div class="relative h-64">
            <canvas id="overviewUtilChart"></canvas>
          </div>
        </div>

        <div class="bg-white rounded-xl border border-slate-200 p-5 flex flex-col justify-between space-y-4">
          <div class="flex items-center justify-between">
            <h2 class="text-sm font-bold text-slate-900">Node &amp; Transport Diagnostics</h2>
            <span class="text-xs font-medium ${state.hostMeta.dockerAvailable ? 'text-emerald-600' : 'text-amber-600'}">${state.hostMeta.dockerAvailable ? 'Docker Ready' : 'No Docker'}</span>
          </div>
          <div class="space-y-2.5 text-xs">
            <div class="p-2.5 rounded-lg bg-slate-50 flex items-center justify-between">
              <span class="text-slate-500">Coordinator</span>
              <strong class="text-slate-900 font-mono-code truncate max-w-[170px]">${escapeHtml(state.coordStatus.coordinatorHost || 'Not set')}</strong>
            </div>
            <div class="p-2.5 rounded-lg bg-slate-50 flex items-center justify-between">
              <span class="text-slate-500">NAT / CGNAT</span>
              <strong class="text-slate-900">${escapeHtml(state.hostMeta.natType)}${state.hostMeta.isBehindCGNAT ? ' (CGNAT)' : ''}</strong>
            </div>
            <div class="p-2.5 rounded-lg bg-slate-50 flex items-center justify-between">
              <span class="text-slate-500">P2P Direct / Relayed</span>
              <strong class="text-emerald-700">${state.coordStatus.directPeersCount} Direct · ${state.coordStatus.relayedPeersCount} Relayed</strong>
            </div>
            <div class="p-2.5 rounded-lg bg-slate-50 flex items-center justify-between">
              <span class="text-slate-500">Watchdog</span>
              <strong class="text-slate-900">${escapeHtml(state.localWatchdog?.state || 'stopped')}${state.localWatchdog?.on_battery ? ` (${state.localWatchdog.battery_percent}% bat)` : ''}</strong>
            </div>
          </div>
          <div class="text-[11px] text-slate-500 font-mono-code">Uptime: ${escapeHtml(state.coordStatus.uptime)} · ${escapeHtml(state.hostMeta.goVersion)}</div>
        </div>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-5 gap-5">
        <div class="lg:col-span-3 bg-white rounded-xl border border-slate-200 p-5">
          <div class="flex items-center justify-between mb-3">
            <h2 class="text-sm font-bold text-slate-900">Recent Workloads</h2>
            <button type="button" data-action="nav" data-page="tasks" class="text-xs font-semibold text-sky-600 hover:text-sky-700">View all</button>
          </div>
          <div class="divide-y divide-slate-100">
            ${state.tasks.length ? state.tasks.slice(0, 4).map(t => `
              <button type="button" data-action="open-task" data-id="${escapeHtml(t.id)}" class="w-full text-left py-3 flex items-center gap-3 hover:bg-slate-50/80 px-2 -mx-2 rounded-lg transition-colors">
                <div class="min-w-0 flex-1">
                  <div class="flex items-center justify-between gap-2">
                    <span class="text-xs font-semibold text-slate-900 truncate">${escapeHtml(t.title)}</span>
                    <span class="text-xs font-mono-code text-slate-600 shrink-0">${escapeHtml(t.status)}${t.durationMs != null ? ` · ${t.durationMs}ms` : ''}</span>
                  </div>
                  <div class="text-xs text-slate-500 mt-0.5">${escapeHtml(t.worker)} · ${escapeHtml(t.tier)}</div>
                </div>
              </button>
            `).join('') : '<div class="py-6 text-center text-xs text-slate-500">No tasks recorded yet. Click "+ New Task" or run a sandbox / GPU job.</div>'}
          </div>
        </div>

        <div class="lg:col-span-2 bg-white rounded-xl border border-slate-200 p-5">
          <div class="flex items-center justify-between mb-3">
            <h2 class="text-sm font-bold text-slate-900">Recent Events</h2>
            <button type="button" data-action="nav" data-page="logs" class="text-xs font-semibold text-sky-600 hover:text-sky-700">View logs</button>
          </div>
          <div class="space-y-3.5">
            ${state.logs.length ? state.logs.slice(0, 4).map(l => `
              <div class="flex items-start gap-2.5 text-xs">
                <span class="mt-1.5 w-2 h-2 rounded-full shrink-0 ${l.sev === 'Error' ? 'bg-rose-500' : l.sev === 'Warning' ? 'bg-amber-500' : 'bg-sky-500'}"></span>
                <div class="min-w-0 flex-1">
                  <div class="flex items-center justify-between gap-2">
                    <span class="font-semibold text-slate-800">${escapeHtml(l.type)}</span>
                    <span class="text-[11px] font-mono-code text-slate-400">${escapeHtml(l.ts)}</span>
                  </div>
                  <p class="text-xs text-slate-500 mt-0.5 leading-relaxed break-words">${escapeHtml(l.msg)}</p>
                </div>
              </div>
            `).join('') : '<div class="py-6 text-center text-xs text-slate-500">Listening for live node events via /api/events...</div>'}
          </div>
        </div>
      </div>
    </div>
  `;

  const ctx = document.getElementById('overviewUtilChart');
  if (ctx) {
    const samples = state.telemetryHistory.length
      ? state.telemetryHistory
      : [{ label: 'Now', vramPct, storagePct, avgRttMs: Number((state.coordStatus.avgRttMs || 0).toFixed(1)) }];
    drawOverviewChart(ctx, samples);
  }
}

// =========================================================================
// PAGE 2: NETWORK TOPOLOGY & LIVE P2P CONTROLS
// =========================================================================
function renderTopology(container) {
  const selected = state.nodes.find(n => n.id === state.selectedTopoNodeId) || state.nodes[0];
  if (!selected) {
    container.innerHTML = '<div class="bg-white rounded-xl border border-slate-200 p-8 text-center text-xs text-slate-500">No nodes discovered yet.</div>';
    return;
  }

  const localNode = state.nodes.find(n => n.isLocal) || state.nodes[0];
  const lx = localNode.pos[0] * 6;
  const ly = localNode.pos[1] * 4.2;
  const lastPing = state.peerPingResults[selected.id];

  container.innerHTML = `
    <div class="grid grid-cols-1 xl:grid-cols-3 gap-5">
      <div class="xl:col-span-2 bg-white rounded-xl border border-slate-200 p-5 flex flex-col justify-between min-h-[540px]" style="background-image: radial-gradient(#E2E8F0 1px, transparent 1px); background-size: 24px 24px;">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div class="inline-flex rounded-lg bg-slate-100 p-1 border border-slate-200 text-xs">
            <button type="button" data-action="set-topo-view" data-mode="mesh" class="px-3 py-1 rounded-md font-medium ${state.topoViewMode === 'mesh' ? 'bg-white text-slate-900 shadow-2xs' : 'text-slate-500 hover:text-slate-800'}">P2P Mesh</button>
            <button type="button" data-action="set-topo-view" data-mode="signaling" class="px-3 py-1 rounded-md font-medium ${state.topoViewMode === 'signaling' ? 'bg-white text-slate-900 shadow-2xs' : 'text-slate-500 hover:text-slate-800'}">Signaling Hub</button>
          </div>
          <div class="flex items-center gap-3 text-xs text-slate-600 bg-white/90 px-3 py-1.5 rounded-lg border border-slate-200">
            <span class="flex items-center gap-1.5"><span class="w-2.5 h-2.5 rounded-full bg-sky-500"></span>Local Node</span>
            <span class="flex items-center gap-1.5"><span class="w-2.5 h-2.5 rounded-full bg-emerald-500"></span>P2P Connected</span>
            <span class="flex items-center gap-1.5"><span class="w-2.5 h-2.5 rounded-full bg-slate-400"></span>Fleet Discovered</span>
          </div>
        </div>

        <div class="relative w-full h-[420px] my-auto">
          <svg viewBox="0 0 600 420" class="w-full h-full">
            <circle cx="300" cy="210" r="88" fill="none" stroke="#E2E8F0" stroke-dasharray="4 4" />
            <circle cx="300" cy="210" r="165" fill="none" stroke="#CBD5E1" stroke-dasharray="5 5" />

            <!-- Signaling hub lines -->
            ${state.nodes.map(n => {
              const x = n.pos[0] * 6;
              const y = n.pos[1] * 4.2;
              return `<line x1="300" y1="210" x2="${x}" y2="${y}" stroke="#E2E8F0" stroke-width="1.2" stroke-dasharray="3 4" />`;
            }).join('')}

            <!-- Direct/Relayed WebRTC P2P DataChannel links from local node to connected peers -->
            ${state.nodes.filter(n => !n.isLocal && n.connectedP2P).map(n => {
              const x = n.pos[0] * 6;
              const y = n.pos[1] * 4.2;
              const isRelayed = n.transport.toLowerCase().includes('relayed');
              const strokeColor = isRelayed ? '#D97706' : '#059669';
              return `<line x1="${lx}" y1="${ly}" x2="${x}" y2="${y}" stroke="${strokeColor}" stroke-width="2.5" class="flow-line" />`;
            }).join('')}

            <g>
              <circle cx="300" cy="210" r="38" fill="#E0F2FE" fill-opacity="0.6" />
              <circle cx="300" cy="210" r="28" fill="#FFFFFF" stroke="#0284C7" stroke-width="2.5" />
              <text x="300" y="213" text-anchor="middle" font-size="10" font-weight="700" fill="#0F172A">HUB</text>
            </g>

            ${state.nodes.map(n => {
              const x = n.pos[0] * 6;
              const y = n.pos[1] * 4.2;
              const isSel = n.id === selected.id;
              return `
                <g class="cursor-pointer" data-action="select-topo-node" data-id="${escapeHtml(n.id)}">
                  ${isSel ? `<circle cx="${x}" cy="${y}" r="26" fill="${n.color}" fill-opacity="0.14" stroke="${n.color}" stroke-width="1.5" stroke-dasharray="3 3" />` : ''}
                  <circle cx="${x}" cy="${y}" r="18" fill="#FFFFFF" stroke="${n.color}" stroke-width="${isSel ? 3 : 2}" />
                  <circle cx="${x}" cy="${y}" r="5" fill="${n.color}" />
                  <text x="${x}" y="${y - 24}" text-anchor="middle" font-size="11" font-weight="700" fill="#0F172A">${escapeHtml(n.id)}</text>
                  <text x="${x}" y="${y + 32}" text-anchor="middle" font-size="10" fill="#64748B">${escapeHtml(n.isLocal ? 'Local Host' : n.transport)}</text>
                </g>
              `;
            }).join('')}
          </svg>
        </div>

        <div class="text-xs text-slate-500 text-center">Solid/animated emerald links represent active WebRTC DataChannels (${state.coordStatus.directPeersCount} direct, ${state.coordStatus.relayedPeersCount} relayed).</div>
      </div>

      <div class="bg-white rounded-xl border border-slate-200 p-5 flex flex-col justify-between">
        <div>
          <div class="flex items-start justify-between gap-2 pb-4 border-b border-slate-100">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <span class="w-2.5 h-2.5 rounded-full ${selected.status === 'Online' ? 'bg-emerald-500' : 'bg-slate-400'}"></span>
                <h2 class="text-base font-bold text-slate-900 truncate">${escapeHtml(selected.id)}</h2>
              </div>
              <p class="text-xs text-slate-500 mt-1">${escapeHtml(selected.lab)} · ${escapeHtml(selected.os)}</p>
            </div>
            <span class="text-xs font-medium px-2.5 py-1 rounded-lg bg-slate-100 text-slate-700 shrink-0">${escapeHtml(selected.status)}</span>
          </div>

          <div class="py-4 border-b border-slate-100 space-y-2 text-xs">
            <div class="flex justify-between"><span class="text-slate-500">Transport Path</span><span class="font-semibold text-slate-900">${escapeHtml(selected.transport)}</span></div>
            <div class="flex justify-between"><span class="text-slate-500">Hardware / GPU</span><span class="font-semibold text-slate-900 truncate max-w-[180px]">${escapeHtml(selected.gpuModel)}</span></div>
            ${selected.vramTotal > 0 ? `<div class="flex justify-between"><span class="text-slate-500">VRAM</span><span class="font-mono-code text-slate-900">${selected.vramUsed} / ${selected.vramTotal} GB</span></div>` : ''}
            ${selected.isLocal ? `<div class="flex justify-between"><span class="text-slate-500">Host CPUs</span><span class="font-mono-code text-slate-900">${selected.cpus}</span></div>` : ''}
          </div>

          <div class="grid grid-cols-2 gap-3 my-4 text-xs">
            <div class="p-3 rounded-lg bg-slate-50">
              <div class="text-slate-500">RTT</div>
              <div class="font-semibold text-slate-900 mt-0.5">${selected.isLocal ? 'Local (0 ms)' : (selected.connectedP2P ? `${selected.rttMs} ms` : 'Disconnected')}</div>
            </div>
            <div class="p-3 rounded-lg bg-slate-50">
              <div class="text-slate-500">Data Transfer</div>
              <div class="font-semibold text-slate-900 mt-0.5">${selected.isLocal ? `${selected.blobs} blobs` : `${formatBytes(selected.bytesSent)} / ${formatBytes(selected.bytesReceived)}`}</div>
            </div>
          </div>

          ${lastPing ? `
            <div class="p-3 rounded-lg border text-xs mb-4 ${lastPing.ok ? 'bg-emerald-50 border-emerald-200 text-emerald-900' : 'bg-rose-50 border-rose-200 text-rose-900'}">
              <strong>Last Ping:</strong> ${escapeHtml(lastPing.message)}
            </div>
          ` : ''}
        </div>

        <div class="grid grid-cols-2 gap-2">
          ${selected.isLocal ? `
            <button type="button" data-action="view-node-logs" data-id="${escapeHtml(selected.id)}" class="col-span-2 py-2 rounded-lg border border-slate-200 hover:bg-slate-50 text-xs font-semibold text-slate-700">View Local Logs</button>
          ` : `
            ${selected.connectedP2P ? `
              <button type="button" data-action="peer-disconnect" data-id="${escapeHtml(selected.id)}" class="py-2 rounded-lg border border-rose-200 bg-rose-50 hover:bg-rose-100 text-xs font-semibold text-rose-700">Disconnect</button>
              <button type="button" data-action="peer-ping" data-id="${escapeHtml(selected.id)}" class="py-2 rounded-lg border border-slate-200 hover:bg-slate-50 text-xs font-semibold text-slate-700">Ping Peer</button>
            ` : `
              <button type="button" data-action="peer-connect" data-id="${escapeHtml(selected.id)}" class="col-span-2 py-2 rounded-lg bg-emerald-600 hover:bg-emerald-700 text-white text-xs font-semibold">Connect P2P</button>
            `}
            <button type="button" data-action="view-node-logs" data-id="${escapeHtml(selected.id)}" class="col-span-2 py-2 rounded-lg border border-slate-200 hover:bg-slate-50 text-xs font-semibold text-slate-700">View Node Logs</button>
          `}
          <button type="button" data-action="assign-workload" data-id="${escapeHtml(selected.isLocal ? 'Auto' : selected.id)}" class="col-span-2 py-2.5 rounded-lg bg-slate-900 hover:bg-slate-800 text-white text-xs font-semibold">Assign Workload</button>
        </div>
      </div>
    </div>
  `;
}

async function connectPeerAction(peerId) {
  try {
    showToast(`Connecting WebRTC P2P to ${peerId}...`, 'info');
    await apiFetchJson('/api/peer/connect', { method: 'POST', body: JSON.stringify({ peer_id: peerId }) });
    showToast(`Connected P2P link with ${peerId}`);
    await syncBackend();
  } catch (err) {
    showToast(`P2P connect failed: ${err.message}`, 'error');
  }
}

async function disconnectPeerAction(peerId) {
  try {
    await apiFetchJson('/api/peer/disconnect', { method: 'POST', body: JSON.stringify({ peer_id: peerId }) });
    delete state.peerPingResults[peerId];
    showToast(`Disconnected P2P link with ${peerId}`, 'info');
    await syncBackend();
  } catch (err) {
    showToast(`P2P disconnect failed: ${err.message}`, 'error');
  }
}

async function pingPeerAction(peerId) {
  try {
    const res = await apiFetchJson('/api/peer/ping', { method: 'POST', body: JSON.stringify({ peer_id: peerId }) });
    if (res.status === 'ping_timeout') {
      state.peerPingResults[peerId] = { ok: false, message: 'Ping timed out (> 5000 ms)' };
      showToast(`Ping to ${peerId} timed out`, 'warning');
    } else {
      const rtt = Number(res.rtt_ms ?? res.duration_ms ?? 0).toFixed(2);
      state.peerPingResults[peerId] = { ok: true, message: `Pong received in ${rtt} ms` };
      showToast(`Ping ${peerId}: ${rtt} ms`);
    }
    await syncBackend();
  } catch (err) {
    state.peerPingResults[peerId] = { ok: false, message: err.message };
    showToast(`Ping failed: ${err.message}`, 'error');
    if (state.currentPage === 'topology') renderCurrentPageOnly();
  }
}

// =========================================================================
// PAGE 3: CPU SANDBOX & REMOTE SHELL (OBJECTIVE 2)
// =========================================================================
function renderSandbox(container) {
  const sb = state.sandbox;
  const codeSamples = state.allSamples.filter(s => s.language === 'python' || s.language === 'c' || s.language === 'cpp');
  const peerOptions = ['local', ...state.nodes.filter(n => !n.isLocal).map(n => n.id)];

  container.innerHTML = `
    <div class="space-y-6">
      <!-- Objective 2 Docker Sandbox -->
      <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 pb-3">
          <div>
            <h2 class="text-sm font-bold text-slate-900">Objective 2 · Isolated Container Code Runner</h2>
            <p class="text-xs text-slate-500 mt-0.5">Execute Python, C, or C++ jobs locally or over P2P WebRTC DataChannels via <code class="font-mono-code">POST /api/code/run</code>.</p>
          </div>
          <div class="flex items-center gap-2 text-xs">
            <span class="px-2.5 py-1 rounded-full font-medium ${state.hostMeta.dockerAvailable ? 'bg-emerald-50 text-emerald-700' : 'bg-amber-50 text-amber-700'}">
              Local Docker: ${state.hostMeta.dockerAvailable ? 'Active' : 'Unavailable'}
            </span>
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-4 gap-3 text-xs">
          <div>
            <label class="block font-semibold text-slate-700 mb-1">Sample Template</label>
            <select id="sbSampleSelect" class="w-full px-3 py-2 rounded-lg border border-slate-200 bg-white font-mono-code">
              <option value="">Custom code...</option>
              ${codeSamples.map((s, idx) => `<option value="${idx}" ${sb.selectedSample === s.name ? 'selected' : ''}>${escapeHtml(s.name)} (${escapeHtml(s.language)})</option>`).join('')}
            </select>
          </div>
          <div>
            <label class="block font-semibold text-slate-700 mb-1">Language</label>
            <select id="sbLanguage" class="w-full px-3 py-2 rounded-lg border border-slate-200 bg-white">
              <option value="python" ${sb.language === 'python' ? 'selected' : ''}>Python</option>
              <option value="c" ${sb.language === 'c' ? 'selected' : ''}>C (gcc)</option>
              <option value="cpp" ${sb.language === 'cpp' ? 'selected' : ''}>C++ (g++)</option>
            </select>
          </div>
          <div>
            <label class="block font-semibold text-slate-700 mb-1">Target Peer</label>
            <select id="sbTargetPeer" class="w-full px-3 py-2 rounded-lg border border-slate-200 bg-white font-mono-code">
              ${peerOptions.map(id => `<option value="${escapeHtml(id)}" ${sb.targetPeer === id ? 'selected' : ''}>${escapeHtml(id === 'local' ? 'local (This Node)' : id)}</option>`).join('')}
            </select>
          </div>
          <div>
            <label class="block font-semibold text-slate-700 mb-1">Timeout: <span id="sbTimeoutLabel" class="font-mono-code">${sb.timeout}s</span></label>
            <input id="sbTimeoutSlider" type="range" min="1" max="30" value="${sb.timeout}" class="w-full mt-2" />
          </div>
        </div>

        <div class="grid grid-cols-1 lg:grid-cols-3 gap-4 text-xs">
          <div class="lg:col-span-2">
            <label class="block font-semibold text-slate-700 mb-1.5">Source Code</label>
            <textarea id="sbSourceEditor" rows="11" spellcheck="false" class="w-full p-3 rounded-xl bg-slate-50 border border-slate-200 font-mono-code text-xs text-slate-800 focus:outline-none focus:border-slate-400">${escapeHtml(sb.source)}</textarea>
          </div>
          <div class="space-y-3">
            <div>
              <label class="block font-semibold text-slate-700 mb-1.5">Standard Input (stdin)</label>
              <textarea id="sbStdinEditor" rows="6" spellcheck="false" placeholder="Optional stdin lines..." class="w-full p-3 rounded-xl bg-slate-50 border border-slate-200 font-mono-code text-xs text-slate-800 focus:outline-none focus:border-slate-400">${escapeHtml(sb.stdin)}</textarea>
            </div>
            <button type="button" data-action="run-sandbox-code" ${sb.running ? 'disabled' : ''} class="w-full py-2.5 rounded-lg bg-slate-900 hover:bg-slate-800 disabled:opacity-50 text-white font-semibold">
              ${sb.running ? 'Executing Container...' : 'Run Container Job'}
            </button>
          </div>
        </div>

        ${sb.result ? `
          <div class="rounded-xl border ${sb.result.exit_code === 0 && !sb.result.error ? 'border-emerald-200 bg-emerald-50/40' : 'border-rose-200 bg-rose-50/40'} p-4 space-y-2 text-xs">
            <div class="flex flex-wrap items-center justify-between gap-2 font-mono-code">
              <span class="font-semibold text-slate-900">Exit Code: ${escapeHtml(sb.result.exit_code)} · Duration: ${escapeHtml(sb.result.duration_ms || 0)} ms</span>
              ${sb.result.job_id ? `<span class="text-slate-500">Job: ${escapeHtml(sb.result.job_id)}</span>` : ''}
            </div>
            ${sb.result.error ? `<div class="text-rose-700 font-mono-code">${escapeHtml(sb.result.error)}</div>` : ''}
            ${sb.result.stdout ? `<div><div class="text-[11px] font-semibold text-slate-500 mb-1">STDOUT</div><pre class="p-3 rounded-lg bg-white border border-slate-200 font-mono-code overflow-x-auto text-slate-800">${escapeHtml(sb.result.stdout)}</pre></div>` : ''}
            ${sb.result.stderr ? `<div><div class="text-[11px] font-semibold text-rose-600 mb-1">STDERR</div><pre class="p-3 rounded-lg bg-white border border-rose-200 font-mono-code overflow-x-auto text-rose-800">${escapeHtml(sb.result.stderr)}</pre></div>` : ''}
          </div>
        ` : ''}
      </div>

      <!-- Remote Shell Execution -->
      <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
        <div class="border-b border-slate-100 pb-3">
          <h2 class="text-sm font-bold text-slate-900">Remote Shell Execution</h2>
          <p class="text-xs text-slate-500 mt-0.5">Opt-in authenticated command execution via <code class="font-mono-code">POST /api/shell/exec</code> (requires <code class="font-mono-code">-enable-remote-shell</code> on target).</p>
        </div>
        <form id="shellExecForm" class="flex flex-wrap items-center gap-2.5 text-xs">
          <select id="shellTargetPeer" class="px-3 py-2 rounded-lg border border-slate-200 bg-white font-mono-code">
            <option value="local" ${sb.shellPeer === 'local' ? 'selected' : ''}>local</option>
            <option value="all" ${sb.shellPeer === 'all' ? 'selected' : ''}>all (Connected Peers)</option>
            ${state.nodes.filter(n => !n.isLocal).map(n => `<option value="${escapeHtml(n.id)}" ${sb.shellPeer === n.id ? 'selected' : ''}>${escapeHtml(n.id)}</option>`).join('')}
          </select>
          <input id="shellCmdInput" value="${escapeHtml(sb.shellCommand)}" placeholder="e.g., uname -a or hostname" class="flex-1 min-w-[220px] px-3 py-2 rounded-lg border border-slate-200 font-mono-code" />
          <button type="submit" ${sb.shellRunning ? 'disabled' : ''} class="px-4 py-2 rounded-lg bg-slate-900 hover:bg-slate-800 disabled:opacity-50 text-white font-semibold">
            ${sb.shellRunning ? 'Running...' : 'Execute Shell'}
          </button>
        </form>
        ${sb.shellResult ? `
          <div class="rounded-xl border border-slate-200 bg-slate-50 p-4 space-y-2 text-xs font-mono-code">
            ${sb.shellResult.results ? `<pre class="overflow-x-auto text-slate-800">${escapeHtml(JSON.stringify(sb.shellResult.results, null, 2))}</pre>` : `
              <div class="font-semibold text-slate-900">Exit Code: ${escapeHtml(sb.shellResult.exit_code)} · ${escapeHtml(sb.shellResult.duration_ms || 0)} ms</div>
              ${sb.shellResult.error ? `<div class="text-rose-700">${escapeHtml(sb.shellResult.error)}</div>` : ''}
              ${sb.shellResult.stdout ? `<pre class="p-2.5 rounded bg-white border border-slate-200 overflow-x-auto text-slate-800">${escapeHtml(sb.shellResult.stdout)}</pre>` : ''}
              ${sb.shellResult.stderr ? `<pre class="p-2.5 rounded bg-white border border-rose-200 overflow-x-auto text-rose-800">${escapeHtml(sb.shellResult.stderr)}</pre>` : ''}
            `}
          </div>
        ` : ''}
      </div>
    </div>
  `;
}

async function runSandboxCode() {
  const sb = state.sandbox;
  if (!sb.source.trim()) {
    showToast('Enter source code to run', 'warning');
    return;
  }
  sb.running = true;
  renderCurrentPageOnly();
  try {
    const res = await apiFetchJson('/api/code/run', {
      method: 'POST',
      body: JSON.stringify({
        peer_id: sb.targetPeer,
        language: sb.language,
        source: sb.source,
        stdin: sb.stdin,
        timeout: Number(sb.timeout) || 10,
      }),
    });
    sb.result = res;
    if (res.error) {
      showToast(res.error, 'warning');
    } else if (res.exit_code !== 0) {
      showToast(`Container exited with code ${res.exit_code}`, 'warning');
    } else {
      showToast(`Container job ${res.job_id || ''} completed in ${res.duration_ms}ms`);
    }
    await syncBackend();
  } catch (err) {
    sb.result = { exit_code: -1, error: err.message };
    showToast(`Container execution failed: ${err.message}`, 'error');
  } finally {
    sb.running = false;
    if (state.currentPage === 'sandbox') renderCurrentPageOnly();
  }
}

async function runShellExec() {
  const sb = state.sandbox;
  if (!sb.shellCommand.trim()) {
    showToast('Enter a shell command', 'warning');
    return;
  }
  sb.shellRunning = true;
  renderCurrentPageOnly();
  try {
    const res = await apiFetchJson('/api/shell/exec', {
      method: 'POST',
      body: JSON.stringify({
        peer_id: sb.shellPeer,
        command: sb.shellCommand,
      }),
    });
    sb.shellResult = res;
    if (res.error) {
      showToast(res.error, 'warning');
    } else {
      showToast('Shell command completed');
    }
  } catch (err) {
    sb.shellResult = { exit_code: -1, error: err.message };
    showToast(`Shell exec failed: ${err.message}`, 'error');
  } finally {
    sb.shellRunning = false;
    if (state.currentPage === 'sandbox') renderCurrentPageOnly();
  }
}

// =========================================================================
// PAGE 4: GPU COMPUTE & HIPIFY TRANSLATOR
// =========================================================================
function renderGpuCompute(container) {
  const prof = state.localGpuProfile || {};
  const wd = state.localWatchdog || {};
  const vramTotalGB = prof.total_vram_bytes ? Number((prof.total_vram_bytes / (1024 * 1024 * 1024)).toFixed(2)) : 0;
  const vramAvailGB = prof.available_vram_bytes ? Number((prof.available_vram_bytes / (1024 * 1024 * 1024)).toFixed(2)) : 0;
  const vramUsedGB = vramTotalGB > 0 ? Number(Math.max(0, vramTotalGB - vramAvailGB).toFixed(2)) : 0;
  const vramPct = vramTotalGB > 0 ? Math.min(100, Math.round((vramUsedGB / vramTotalGB) * 100)) : 0;
  const backends = Array.isArray(prof.supported_backends) ? prof.supported_backends : [];
  const res = state.hipifyResult;

  container.innerHTML = `
    <div class="space-y-6">
      <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="text-xs text-slate-500 mb-2">GPU Subsystem</div>
          <div class="flex items-baseline gap-2">
            <span class="text-xl font-bold text-slate-900 truncate">${escapeHtml(prof.device_name || 'Probing...')}</span>
          </div>
          <div class="mt-2.5 flex flex-wrap items-center gap-1.5 text-[11px] font-medium">
            <span class="px-2 py-0.5 rounded bg-slate-100 text-slate-700">${escapeHtml(prof.vendor || 'Unknown')}</span>
            ${prof.cuda_supported ? '<span class="px-2 py-0.5 rounded bg-emerald-50 text-emerald-700">CUDA</span>' : ''}
            ${prof.directml_supported ? '<span class="px-2 py-0.5 rounded bg-sky-50 text-sky-700">DirectML</span>' : ''}
            ${backends.map(b => `<span class="px-2 py-0.5 rounded bg-indigo-50 text-indigo-700">${escapeHtml(b)}</span>`).join('')}
          </div>
        </div>

        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="text-xs text-slate-500 mb-2">VRAM Capacity</div>
          <div class="flex items-baseline gap-2">
            <span class="text-2xl font-bold text-slate-900">${vramTotalGB.toFixed(1)} GB</span>
            <span class="text-xs text-slate-500">${vramAvailGB.toFixed(1)} GB Free</span>
          </div>
          <div class="mt-3 h-1.5 bg-slate-100 rounded-full overflow-hidden">
            <div class="h-full bg-slate-900 rounded-full" style="width:${vramPct}%"></div>
          </div>
        </div>

        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="text-xs text-slate-500 mb-2">Thermal &amp; Power</div>
          <div class="text-2xl font-bold text-slate-900">${prof.temperature_c ? `${prof.temperature_c.toFixed(0)}°C` : 'N/A'}</div>
          <div class="mt-2 text-xs text-slate-500">${prof.power_usage_w ? `${prof.power_usage_w.toFixed(1)} W draw` : 'Power telemetry N/A'}</div>
        </div>

        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="text-xs text-slate-500 mb-2">Activity Watchdog</div>
          <div class="text-lg font-bold ${wd.state === 'running' ? 'text-emerald-700' : 'text-amber-600'}">${escapeHtml(wd.state || 'stopped')}</div>
          <div class="mt-2 text-xs text-slate-500">
            ${wd.on_battery ? `Battery (${wd.battery_percent}%)` : 'AC Power'} · ${wd.user_active ? 'User Active' : 'Idle'}
            ${wd.last_pause_reason ? ` · ${escapeHtml(wd.last_pause_reason)}` : ''}
          </div>
        </div>
      </div>

      <div class="grid grid-cols-1 xl:grid-cols-3 gap-5">
        <div class="xl:col-span-2 bg-white rounded-xl border border-slate-200 p-5 space-y-4">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 pb-3">
            <div>
              <div class="flex items-center gap-2">
                <h2 class="text-sm font-bold text-slate-900">CUDA &rarr; AMD HIPIFY Translator</h2>
                <span class="text-[11px] font-mono-code px-2 py-0.5 rounded bg-slate-100 text-slate-700">POST /api/gpu/hipify</span>
              </div>
              <div class="text-xs font-mono-code text-slate-500 mt-0.5">Selected file: ${escapeHtml(state.gpuSelectedFile)}</div>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <input type="file" id="cudaFileInput" accept=".cu,.cuh,.cpp,.h,.c" class="hidden" />
              <button type="button" data-action="pick-cuda-file" class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-slate-200 bg-white hover:bg-slate-50 text-slate-700 text-xs font-semibold">
                <i data-lucide="file-up" class="w-3.5 h-3.5"></i>
                <span>Select CUDA File</span>
              </button>
              <select id="cudaSampleSelect" class="px-2.5 py-1.5 rounded-lg border border-slate-200 bg-white text-xs font-mono-code text-slate-700">
                <option value="">Samples (${state.cudaSamples.length})...</option>
                ${state.cudaSamples.map((s, idx) => `<option value="${idx}" ${state.gpuSelectedFile === s.name ? 'selected' : ''}>${escapeHtml(s.name)}</option>`).join('')}
              </select>
              <button type="button" data-action="run-hipify" ${state.hipifyRunning ? 'disabled' : ''} class="px-3.5 py-1.5 rounded-lg bg-slate-900 hover:bg-slate-800 disabled:opacity-50 text-white text-xs font-semibold">
                ${state.hipifyRunning ? 'Converting...' : 'Convert to HIP'}
              </button>
              ${state.hipSource ? `
                <button type="button" data-action="download-hip" class="inline-flex items-center gap-1 px-3 py-1.5 rounded-lg border border-emerald-200 bg-emerald-50 hover:bg-emerald-100 text-emerald-800 text-xs font-semibold">
                  <i data-lucide="download" class="w-3.5 h-3.5"></i>
                  <span>.hip</span>
                </button>
              ` : ''}
            </div>
          </div>

          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div>
              <label class="block text-[11px] font-mono-code font-semibold text-slate-500 mb-1.5">Original CUDA C++ Source</label>
              <textarea id="cudaEditor" rows="12" spellcheck="false" class="w-full p-3 rounded-xl bg-slate-50 border border-slate-200 font-mono-code text-xs text-slate-800 focus:outline-none focus:border-slate-400">${escapeHtml(state.cudaSource)}</textarea>
            </div>
            <div>
              <label class="block text-[11px] font-mono-code font-semibold text-sky-700 mb-1.5">Translated AMD HIP C++ Output</label>
              <textarea id="hipEditor" readonly rows="12" spellcheck="false" class="w-full p-3 rounded-xl bg-slate-50 border border-slate-200 font-mono-code text-xs text-slate-800 focus:outline-none" placeholder="// Click 'Convert to HIP'...">${escapeHtml(state.hipSource)}</textarea>
            </div>
          </div>

          ${res ? (res.success ? `
            <div class="px-3.5 py-2.5 rounded-lg ${res.warning ? 'bg-amber-50 border-amber-200 text-amber-900' : 'bg-emerald-50 border-emerald-200 text-emerald-900'} border space-y-1 text-xs">
              <div class="flex items-center gap-2">
                <i data-lucide="${res.warning ? 'alert-triangle' : 'check-circle-2'}" class="w-4 h-4 shrink-0"></i>
                <span><strong>Converted</strong> (${res.replacements_made || 0} refs via <code class="font-mono-code">${escapeHtml(res.engine || 'builtin')}</code>).</span>
              </div>
              ${res.warning ? `<div class="font-mono-code text-[11px]">Warning: ${escapeHtml(res.warning)}</div>` : ''}
            </div>
          ` : `
            <div class="p-3.5 rounded-lg bg-rose-50 border border-rose-200 space-y-1.5 text-xs text-rose-900">
              <div class="flex items-center gap-2 font-semibold">
                <i data-lucide="alert-triangle" class="w-4 h-4 text-rose-600 shrink-0"></i>
                <span>${escapeHtml(res.error || 'Compatibility issue detected')}</span>
              </div>
              ${res.report && Array.isArray(res.report.issues) ? res.report.issues.map(iss => `
                <div class="font-mono-code text-[11px] text-rose-800 pl-6">Line ${iss.line_number}: [${escapeHtml(iss.construct)}] ${escapeHtml(iss.reason)}</div>
              `).join('') : ''}
            </div>
          `) : `
            <div class="rounded-lg bg-slate-50 border border-slate-200 p-3 font-mono-code text-xs text-slate-500">
              Select a CUDA sample or paste code above, then click "Convert to HIP".
            </div>
          `}
        </div>

        <div class="bg-white rounded-xl border border-slate-200 p-5 flex flex-col justify-between space-y-4">
          <div class="space-y-3 text-xs">
            <h2 class="text-sm font-bold text-slate-900">GPU Task Runner (POST /api/gpu/run)</h2>
            <p class="text-slate-500">Runs an ONNX/CUDA pre-flight &amp; training/inference capsule on the local node GPU manager.</p>
            <div class="p-3 rounded-lg bg-slate-50 space-y-1.5">
              <div class="flex justify-between"><span class="text-slate-500">Driver</span><span class="font-mono-code text-slate-900">${escapeHtml(prof.driver_version || 'N/A')}</span></div>
              <div class="flex justify-between"><span class="text-slate-500">GPU Enabled</span><span class="font-semibold text-slate-900">${state.gpuEnabled ? 'Yes' : 'No'}</span></div>
              <div class="flex justify-between"><span class="text-slate-500">User Idle</span><span class="font-mono-code text-slate-900">${wd.user_active ? 'Active' : 'Idle'}</span></div>
            </div>
          </div>
          <div class="space-y-2">
            <button type="button" data-action="dispatch-gpu-task" data-task-type="training" class="w-full py-2.5 rounded-lg bg-slate-900 hover:bg-slate-800 text-white text-xs font-semibold">Run Training Capsule (20 Batches)</button>
            <button type="button" data-action="dispatch-gpu-task" data-task-type="inference" class="w-full py-2.5 rounded-lg border border-slate-200 hover:bg-slate-50 text-slate-800 text-xs font-semibold">Run Inference Capsule</button>
          </div>
        </div>
      </div>
    </div>
  `;
}

async function runHipify() {
  if (!state.cudaSource.trim()) {
    showToast('Please select or paste CUDA C++ source code', 'warning');
    return;
  }
  state.hipifyRunning = true;
  renderCurrentPageOnly();
  try {
    const res = await apiFetchJson('/api/gpu/hipify', {
      method: 'POST',
      body: JSON.stringify({ source: state.cudaSource }),
    });
    state.hipifyResult = res;
    state.hipSource = res.translated_source || '// Translation blocked due to compatibility issues.';
    if (res.success) {
      showToast(`Translated ${res.replacements_made || 0} references via ${res.engine || 'hipify'}`, res.warning ? 'warning' : 'success');
    } else {
      showToast(res.error || 'HIPIFY reported compatibility issues', 'warning');
    }
  } catch (err) {
    showToast('HIPIFY API request failed: ' + err.message, 'error');
  } finally {
    state.hipifyRunning = false;
    if (state.currentPage === 'gpu') renderCurrentPageOnly();
  }
}

function downloadHipFile() {
  if (!state.hipSource) return;
  const outName = state.gpuSelectedFile.replace(/\.(cu|cuh|cpp|h|c)$/i, '') + '.hip';
  const blob = new Blob([state.hipSource], { type: 'text/plain' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = outName;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 500);
  showToast(`Downloaded ${outName}`);
}

async function dispatchGpuBatch(taskType = 'training') {
  try {
    const res = await apiFetchJson('/api/gpu/run', {
      method: 'POST',
      body: JSON.stringify({
        task_type: taskType,
        source_code: state.cudaSource,
        batches: 20,
      }),
    });
    if (res.status === 'rejected') {
      showToast(res.diagnostic_message || res.error || 'GPU task rejected', 'warning');
    } else {
      showToast(`GPU task ${res.job_id || ''} (${res.status || 'completed'})`);
    }
    await syncBackend();
    if (res.job_id) openTaskDetail(res.job_id);
  } catch (err) {
    showToast('GPU run failed: ' + err.message, 'error');
  }
}

// =========================================================================
// PAGE 5: WORKER SANDBOX ALLOWLIST (/api/worker/settings)
// =========================================================================
function renderWorkerSandbox(container) {
  const ws = state.workerSettings;
  const prof = state.localGpuProfile || {};
  const wd = state.localWatchdog || {};
  const allowlist = ws.codeAllowlist || [];

  container.innerHTML = `
    <div class="space-y-6 max-w-4xl">
      <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-4 border-b border-slate-100 pb-4">
          <div>
            <div class="flex items-center gap-2.5">
              <h2 class="text-sm font-bold text-slate-900">Tier 2 Docker Code Execution Allowlist</h2>
              <span class="text-xs font-medium px-2.5 py-0.5 rounded-full ${ws.dockerActive ? 'bg-emerald-50 text-emerald-700' : 'bg-amber-50 text-amber-700'}">
                docker_active: ${ws.dockerActive ? 'true' : 'false'}
              </span>
            </div>
            <p class="text-xs text-slate-500 mt-1">Controls which peers may submit remote container jobs via <code class="font-mono-code">GET/POST /api/worker/settings</code> (<code class="font-mono-code">code_allowlist</code>).</p>
          </div>
          <div class="flex items-center gap-2">
            <button type="button" data-action="allowlist-wildcard" class="px-3 py-1.5 rounded-lg border ${ws.wildcardAllowed ? 'bg-amber-50 border-amber-300 text-amber-800' : 'border-slate-200 bg-white text-slate-700 hover:bg-slate-50'} text-xs font-semibold">
              Allow All (*)
            </button>
            <button type="button" data-action="allowlist-deny-all" class="px-3 py-1.5 rounded-lg border border-rose-200 bg-rose-50 hover:bg-rose-100 text-rose-700 text-xs font-semibold">
              Deny All ([])
            </button>
          </div>
        </div>

        <form id="allowlistAddForm" class="flex flex-wrap items-center gap-2.5 text-xs">
          <input id="allowlistPeerInput" value="${escapeHtml(ws.newPeerInput)}" placeholder="Enter peer ID (or * for wildcard)..." class="flex-1 min-w-[220px] px-3 py-2 rounded-lg border border-slate-200 font-mono-code" />
          <button type="submit" class="px-4 py-2 rounded-lg bg-slate-900 hover:bg-slate-800 text-white font-semibold">Add Peer ID</button>
        </form>

        <div class="space-y-2 text-xs">
          <div class="font-semibold text-slate-700">Authorized Peers (${allowlist.length})</div>
          ${allowlist.length ? `
            <div class="flex flex-wrap gap-2">
              ${allowlist.map(peerId => `
                <span class="inline-flex items-center gap-2 px-3 py-1.5 rounded-lg bg-slate-100 border border-slate-200 font-mono-code text-slate-800">
                  <span>${escapeHtml(peerId)}</span>
                  <button type="button" data-action="allowlist-remove" data-id="${escapeHtml(peerId)}" class="text-slate-400 hover:text-rose-600" aria-label="Remove ${escapeHtml(peerId)}">&times;</button>
                </span>
              `).join('')}
            </div>
          ` : '<div class="p-4 rounded-lg bg-slate-50 border border-slate-200 text-slate-500">Allowlist is empty (`[]`) — all remote container submissions are denied.</div>'}
        </div>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-5 text-xs">
        <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-2.5">
          <h3 class="text-sm font-bold text-slate-900">Tier 3 GPU Profile</h3>
          <div class="flex justify-between"><span class="text-slate-500">Device</span><span class="font-semibold text-slate-900">${escapeHtml(prof.device_name || 'Unavailable')}</span></div>
          <div class="flex justify-between"><span class="text-slate-500">Vendor</span><span class="font-semibold text-slate-900">${escapeHtml(prof.vendor || 'Unknown')}</span></div>
          <div class="flex justify-between"><span class="text-slate-500">CUDA / DirectML</span><span class="font-mono-code text-slate-900">${Boolean(prof.cuda_supported)} / ${Boolean(prof.directml_supported)}</span></div>
        </div>
        <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-2.5">
          <h3 class="text-sm font-bold text-slate-900">Watchdog Governance</h3>
          <div class="flex justify-between"><span class="text-slate-500">State</span><span class="font-semibold text-slate-900">${escapeHtml(wd.state || 'stopped')}</span></div>
          <div class="flex justify-between"><span class="text-slate-500">On Battery</span><span class="font-mono-code text-slate-900">${wd.on_battery ? `true (${wd.battery_percent}%)` : 'false'}</span></div>
          <div class="flex justify-between"><span class="text-slate-500">User Active</span><span class="font-mono-code text-slate-900">${Boolean(wd.user_active)}</span></div>
        </div>
      </div>
    </div>
  `;
}

async function updateWorkerAllowlist(nextList) {
  try {
    const res = await apiFetchJson('/api/worker/settings', {
      method: 'POST',
      body: JSON.stringify({ code_allowlist: nextList }),
    });
    state.workerSettings.codeAllowlist = Array.isArray(res.code_allowlist) ? res.code_allowlist : [];
    state.workerSettings.dockerActive = Boolean(res.docker_active);
    state.workerSettings.wildcardAllowed = Boolean(res.wildcard_allowed);
    state.workerSettings.newPeerInput = '';
    showToast(`Worker allowlist updated (${state.workerSettings.codeAllowlist.length} entries)`);
    renderCurrentPageOnly();
  } catch (err) {
    showToast(`Failed to update allowlist: ${err.message}`, 'error');
  }
}

// =========================================================================
// PAGE 6: TASK QUEUE, CREATE TASK, TASK DETAIL
// =========================================================================
function getFilteredTasksHtml() {
  const filtered = state.tasks.filter(t => {
    const matchTab = state.taskFilterTab === 'All' || t.status === state.taskFilterTab;
    const matchQ = (t.id + ' ' + t.title + ' ' + t.worker).toLowerCase().includes(state.taskSearch.toLowerCase());
    return matchTab && matchQ;
  });
  if (!filtered.length) {
    return '<div class="py-10 text-center text-xs text-slate-500">No tasks found.</div>';
  }
  return filtered.map(t => `
    <div class="p-4 rounded-xl border border-slate-200 hover:border-slate-300 transition-colors grid grid-cols-1 lg:grid-cols-12 gap-3 items-center">
      <div class="lg:col-span-5 cursor-pointer" data-action="open-task" data-id="${escapeHtml(t.id)}">
        <div class="flex items-center gap-2">
          <span class="font-mono-code text-xs text-slate-500">${escapeHtml(t.id)}</span>
          <span class="text-[11px] font-medium px-2 py-0.5 rounded-full ${t.status === 'Running' ? 'bg-sky-50 text-sky-700' : t.status === 'Completed' ? 'bg-emerald-50 text-emerald-700' : t.status === 'Failed' ? 'bg-rose-50 text-rose-700' : 'bg-amber-50 text-amber-700'}">${escapeHtml(t.status)}</span>
        </div>
        <div class="text-sm font-semibold text-slate-900 mt-1 hover:text-sky-700">${escapeHtml(t.title)}</div>
      </div>
      <div class="lg:col-span-2 text-xs text-slate-600">${escapeHtml(t.tier)}</div>
      <div class="lg:col-span-2 text-xs font-medium text-slate-800 truncate">${escapeHtml(t.worker)}</div>
      <div class="lg:col-span-2 text-xs font-mono-code text-slate-500">${escapeHtml(t.timestamp || '-')}${t.durationMs != null ? ` · ${t.durationMs}ms` : ''}</div>
      <div class="lg:col-span-1 flex items-center justify-end">
        <button type="button" data-action="open-task" data-id="${escapeHtml(t.id)}" class="px-2.5 py-1.5 rounded-lg border border-slate-200 hover:bg-slate-50 text-xs font-semibold text-slate-700">Details</button>
      </div>
    </div>
  `).join('');
}

function renderTaskQueue(container) {
  const tabs = ['All', 'Pending', 'Running', 'Completed', 'Failed'];
  container.innerHTML = `
    <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex flex-wrap gap-1.5">
          ${tabs.map(tab => {
            const cnt = tab === 'All' ? state.tasks.length : state.tasks.filter(t => t.status === tab).length;
            return `<button type="button" data-action="set-task-tab" data-tab="${tab}" class="px-3 py-1.5 rounded-lg text-xs font-semibold ${state.taskFilterTab === tab ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'}">${tab} (${cnt})</button>`;
          }).join('')}
        </div>
        <div class="flex items-center gap-2.5 w-full sm:w-auto">
          <div class="relative flex-1 sm:w-56">
            <i data-lucide="search" class="w-3.5 h-3.5 text-slate-400 absolute left-3 top-1/2 -translate-y-1/2"></i>
            <input id="taskSearchInput" type="text" value="${escapeHtml(state.taskSearch)}" placeholder="Search tasks..." class="w-full pl-8 pr-3 py-1.5 rounded-lg border border-slate-200 text-xs focus:outline-none focus:border-slate-400" />
          </div>
          <button type="button" data-action="open-create-task" class="px-3.5 py-1.5 rounded-lg bg-slate-900 hover:bg-slate-800 text-white text-xs font-semibold shrink-0">+ New Task</button>
        </div>
      </div>

      <div id="taskListContainer" class="space-y-2.5">
        ${getFilteredTasksHtml()}
      </div>
    </div>
  `;
}

function renderCreateTask(container) {
  const f = state.createForm;
  const isContainer = f.tier === 'Tier 2 Container';

  container.innerHTML = `
    <div class="space-y-5 max-w-3xl">
      <button type="button" data-action="nav" data-page="tasks" class="inline-flex items-center gap-1.5 text-xs font-semibold text-slate-600 hover:text-slate-900">
        <i data-lucide="arrow-left" class="w-4 h-4"></i>Back to Task Queue
      </button>

      <form id="createTaskForm" class="bg-white rounded-xl border border-slate-200 p-5 space-y-4 text-xs">
        <h2 class="text-sm font-bold text-slate-900">Dispatch New Compute Task</h2>
        <div>
          <label class="block font-semibold text-slate-700 mb-1.5">Task Label</label>
          <input id="ctTitle" value="${escapeHtml(f.title)}" placeholder="e.g., Matrix Multiply / Data Transform" class="w-full px-3 py-2 rounded-lg border border-slate-200" />
        </div>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div>
            <label class="block font-semibold text-slate-700 mb-1.5">Execution Tier</label>
            <select id="ctTier" class="w-full px-3 py-2 rounded-lg border border-slate-200 bg-white">
              <option ${f.tier === 'Tier 3 GPU' ? 'selected' : ''}>Tier 3 GPU</option>
              <option ${f.tier === 'Tier 2 Container' ? 'selected' : ''}>Tier 2 Container</option>
            </select>
          </div>
          <div>
            <label class="block font-semibold text-slate-700 mb-1.5">Target Node</label>
            <select id="ctTargetNode" ${isContainer ? '' : 'disabled'} class="w-full px-3 py-2 rounded-lg border border-slate-200 bg-white font-mono-code">
              ${isContainer ? `
                <option value="Auto" ${f.targetNode === 'Auto' ? 'selected' : ''}>Auto (local)</option>
                ${state.nodes.map(n => `<option value="${escapeHtml(n.id)}" ${f.targetNode === n.id ? 'selected' : ''}>${escapeHtml(n.id)}</option>`).join('')}
              ` : `
                <option value="Auto" selected>Local Node Only (${escapeHtml(state.localNodeId)})</option>
              `}
            </select>
            ${!isContainer ? '<p class="text-[11px] text-slate-500 mt-1">Tier 3 GPU capsules execute locally on this node.</p>' : ''}
          </div>
        </div>

        ${isContainer ? `
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label class="block font-semibold text-slate-700 mb-1.5">Language</label>
              <select id="ctLang" class="w-full px-3 py-2 rounded-lg border border-slate-200 bg-white">
                <option value="python" ${f.language === 'python' ? 'selected' : ''}>python</option>
                <option value="c" ${f.language === 'c' ? 'selected' : ''}>c</option>
                <option value="cpp" ${f.language === 'cpp' ? 'selected' : ''}>cpp</option>
              </select>
            </div>
            <div>
              <label class="block font-semibold text-slate-700 mb-1.5">Timeout (seconds)</label>
              <input id="ctTimeout" type="number" min="1" max="30" value="${f.timeout}" class="w-full px-3 py-2 rounded-lg border border-slate-200 font-mono-code" />
            </div>
          </div>
          <div>
            <label class="block font-semibold text-slate-700 mb-1.5">Container Source Code</label>
            <textarea id="ctSource" rows="7" required spellcheck="false" class="w-full p-3 rounded-lg border border-slate-200 bg-slate-50 font-mono-code">${escapeHtml(f.source)}</textarea>
          </div>
          <div>
            <label class="block font-semibold text-slate-700 mb-1.5">Stdin (optional)</label>
            <textarea id="ctStdin" rows="2" spellcheck="false" class="w-full p-3 rounded-lg border border-slate-200 bg-slate-50 font-mono-code">${escapeHtml(f.stdin)}</textarea>
          </div>
        ` : `
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label class="block font-semibold text-slate-700 mb-1.5">GPU Task Type</label>
              <select id="ctGpuType" class="w-full px-3 py-2 rounded-lg border border-slate-200 bg-white">
                <option value="inference" ${f.gpuTaskType === 'inference' ? 'selected' : ''}>inference</option>
                <option value="training" ${f.gpuTaskType === 'training' ? 'selected' : ''}>training</option>
              </select>
            </div>
            <div>
              <label class="block font-semibold text-slate-700 mb-1.5">Batches</label>
              <input id="ctBatches" type="number" min="1" max="500" value="${f.batches}" class="w-full px-3 py-2 rounded-lg border border-slate-200 font-mono-code" />
            </div>
          </div>
          <div>
            <label class="block font-semibold text-slate-700 mb-1.5">Optional CUDA/HIP Source for Pre-flight Check</label>
            <textarea id="ctGpuSource" rows="5" spellcheck="false" placeholder="Optional .cu source..." class="w-full p-3 rounded-lg border border-slate-200 bg-slate-50 font-mono-code">${escapeHtml(f.gpuSourceCode)}</textarea>
          </div>
        `}

        <div class="flex justify-end gap-2 pt-2">
          <button type="button" data-action="nav" data-page="tasks" class="px-4 py-2 rounded-lg border border-slate-200 font-semibold text-slate-700">Cancel</button>
          <button type="submit" class="px-5 py-2 rounded-lg bg-slate-900 hover:bg-slate-800 text-white font-semibold">Dispatch Task</button>
        </div>
      </form>
    </div>
  `;
}

async function submitCreatedTask() {
  const f = state.createForm;
  try {
    if (f.tier === 'Tier 2 Container') {
      const peerId = (!f.targetNode || f.targetNode === 'Auto') ? 'local' : f.targetNode;
      const res = await apiFetchJson('/api/code/run', {
        method: 'POST',
        body: JSON.stringify({
          peer_id: peerId,
          language: f.language,
          source: f.source,
          stdin: f.stdin,
          timeout: Number(f.timeout) || 10,
        }),
      });
      if (res.error) {
        showToast(res.error, 'warning');
      } else {
        showToast(`Container job ${res.job_id || ''} exited ${res.exit_code}`);
      }
      await syncBackend();
      if (res.job_id) openTaskDetail(res.job_id);
      else navigate('tasks');
    } else {
      const res = await apiFetchJson('/api/gpu/run', {
        method: 'POST',
        body: JSON.stringify({
          task_type: f.gpuTaskType || 'inference',
          source_code: f.gpuSourceCode || '',
          batches: Number(f.batches) || 10,
        }),
      });
      if (res.status === 'rejected') {
        showToast(res.diagnostic_message || res.error || 'GPU task rejected', 'warning');
      } else {
        showToast(`Dispatched GPU task ${res.job_id || ''} (${res.status || 'completed'})`);
      }
      await syncBackend();
      if (res.job_id) openTaskDetail(res.job_id);
      else navigate('tasks');
    }
  } catch (err) {
    showToast('Task dispatch failed: ' + err.message, 'error');
  }
}

function renderTaskDetail(container) {
  const t = state.tasks.find(x => x.id === state.selectedTaskId) || state.tasks[0];
  if (!t) { navigate('tasks'); return; }
  container.innerHTML = `
    <div class="space-y-5">
      <div class="flex items-center gap-3">
        <button type="button" data-action="nav" data-page="tasks" class="p-2 rounded-lg border border-slate-200 bg-white hover:bg-slate-50 text-slate-600">
          <i data-lucide="arrow-left" class="w-4 h-4"></i>
        </button>
        <div>
          <div class="font-mono-code text-xs text-slate-500">${escapeHtml(t.id)} · ${escapeHtml(t.rawStatus || t.status)}</div>
          <h2 class="text-base font-bold text-slate-900">${escapeHtml(t.title)}</h2>
        </div>
      </div>
      <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-3 text-xs">
        <h3 class="text-sm font-bold text-slate-900">Task Metadata</h3>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div class="p-3 rounded-lg bg-slate-50"><span class="text-slate-500">Target Match:</span> <strong class="text-slate-900">${escapeHtml(t.worker)}</strong></div>
          <div class="p-3 rounded-lg bg-slate-50"><span class="text-slate-500">Stage / DAG:</span> <strong class="text-slate-900 font-mono-code">${escapeHtml(t.kind)}</strong></div>
          <div class="p-3 rounded-lg bg-slate-50"><span class="text-slate-500">Recorded At:</span> <strong class="text-slate-900 font-mono-code">${escapeHtml(t.timestamp || '-')}</strong></div>
          <div class="p-3 rounded-lg bg-slate-50"><span class="text-slate-500">Duration:</span> <strong class="text-slate-900 font-mono-code">${t.durationMs != null ? `${t.durationMs} ms` : '-'}</strong></div>
        </div>
      </div>
    </div>
  `;
}

// =========================================================================
// PAGE 7: STORAGE & CAS BLOBS
// =========================================================================
function renderStorage(container) {
  container.innerHTML = `
    <div class="space-y-6">
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="text-xs text-slate-500">CAS Blobs</div>
          <div class="text-2xl font-bold text-slate-900 mt-1">${state.vaultDatasets.length}</div>
          <div class="text-xs text-slate-500 mt-1">${formatBytes(state.hostMeta.storageUsedBytes)} / ${formatBytes(state.hostMeta.storageQuotaBytes)}</div>
        </div>
        <div class="bg-white rounded-xl border border-slate-200 p-5">
          <div class="text-xs text-slate-500">Content Addressing</div>
          <div class="text-lg font-bold text-slate-900 mt-1">SHA-256 Sharded CAS</div>
          <div class="text-xs text-slate-500 mt-1">data/&lt;id[:2]&gt;/&lt;id[2:4]&gt;/&lt;id&gt;</div>
        </div>
        <div class="bg-white rounded-xl border border-slate-200 p-5 flex items-center justify-between">
          <div>
            <div class="text-xs text-slate-500">Integrity Scrub</div>
            <div class="text-sm font-semibold text-emerald-700 mt-1">Verify All SHA-256</div>
          </div>
          <button type="button" data-action="scrub-storage" class="px-3.5 py-2 rounded-lg bg-slate-900 hover:bg-slate-800 text-white text-xs font-semibold">Run Scrub</button>
        </div>
      </div>

      <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-3">
        <div class="flex items-center justify-between">
          <h2 class="text-sm font-bold text-slate-900">Stored Blobs</h2>
          <button type="button" data-action="open-put-blob" class="px-3 py-1.5 rounded-lg border border-slate-200 hover:bg-slate-50 text-xs font-semibold text-slate-700">+ Store Blob</button>
        </div>
        ${state.vaultDatasets.length ? `
          <div class="divide-y divide-slate-100 text-xs">
            ${state.vaultDatasets.map(ds => `
              <div class="py-3 flex flex-wrap items-center justify-between gap-3">
                <div class="min-w-0 flex-1">
                  <div class="font-mono-code font-semibold text-slate-900 truncate">${escapeHtml(ds.id)}</div>
                  <div class="text-slate-500 mt-0.5">${escapeHtml(ds.sizeLabel)}${ds.createdAt ? ` · ${escapeHtml(new Date(ds.createdAt).toLocaleString())}` : ''}</div>
                </div>
                <div class="flex items-center gap-2 shrink-0">
                  <button type="button" data-action="view-blob" data-id="${escapeHtml(ds.id)}" class="px-2.5 py-1 rounded-lg border border-slate-200 hover:bg-slate-50 font-semibold text-slate-700">View</button>
                  <button type="button" data-action="delete-blob" data-id="${escapeHtml(ds.id)}" class="px-2.5 py-1 rounded-lg border border-rose-200 bg-rose-50 hover:bg-rose-100 font-semibold text-rose-700">Delete</button>
                </div>
              </div>
            `).join('')}
          </div>
        ` : '<div class="py-8 text-center text-xs text-slate-500">No blobs stored locally yet. Click "+ Store Blob" to compute a SHA-256 blob.</div>'}
      </div>
    </div>
  `;
}

function openAddDatasetModal() {
  openModal('Store Content-Addressed Blob', `
    <form id="putBlobForm" class="space-y-3 text-xs">
      <p class="text-slate-500">The backend computes the SHA-256 content hash as the immutable <code class="font-mono-code">blob_id</code>.</p>
      <div>
        <label class="block font-semibold text-slate-700 mb-1">Payload Content</label>
        <textarea id="newDsData" rows="4" required class="w-full px-3 py-2 rounded-lg border border-slate-200 font-mono-code">hello unicompute</textarea>
      </div>
      <button type="submit" class="w-full py-2.5 rounded-lg bg-slate-900 hover:bg-slate-800 text-white font-semibold">Store in CAS Vault</button>
    </form>
  `);
}

async function submitNewDataset() {
  const val = document.getElementById('newDsData').value;
  try {
    const res = await apiFetchJson('/api/storage/put', {
      method: 'POST',
      body: JSON.stringify({ content: val }),
    });
    closeModal();
    showToast(`Stored blob ${res.blob_id} (${res.size_bytes ?? res.size ?? 0} bytes)`);
    await syncBackend();
  } catch (err) {
    showToast('Failed to store blob: ' + err.message, 'error');
  }
}

async function viewBlobAction(blobId) {
  try {
    const res = await apiFetchJson(`/api/storage/get?id=${encodeURIComponent(blobId)}`);
    const isValid = Boolean(res.valid ?? res.verified);
    openModal('CAS Blob Inspector', `
      <div class="space-y-3 text-xs">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="font-mono-code text-slate-700 break-all">${escapeHtml(res.blob_id || blobId)}</span>
          <span class="px-2 py-0.5 rounded-full font-semibold ${isValid ? 'bg-emerald-50 text-emerald-700' : 'bg-rose-50 text-rose-700'}">
            ${isValid ? 'SHA-256 Valid' : 'Checksum Invalid'}
          </span>
        </div>
        <div class="text-slate-500">Size: ${escapeHtml(res.size_bytes ?? res.size ?? 0)} bytes${res.truncated ? ' (preview truncated at 512 KiB)' : ''}</div>
        <pre class="p-3 rounded-xl bg-slate-50 border border-slate-200 font-mono-code text-slate-800 max-h-72 overflow-auto whitespace-pre-wrap break-words">${escapeHtml(res.content || '')}</pre>
      </div>
    `);
  } catch (err) {
    showToast('Failed to read blob: ' + err.message, 'error');
  }
}

async function deleteBlobAction(blobId) {
  try {
    await apiFetchJson('/api/storage/del', {
      method: 'POST',
      body: JSON.stringify({ blob_id: blobId }),
    });
    showToast(`Deleted blob ${blobId}`, 'info');
    await syncBackend();
  } catch (err) {
    showToast('Failed to delete blob: ' + err.message, 'error');
  }
}

async function repairAllShards() {
  try {
    const res = await apiFetchJson('/api/storage/scrub', { method: 'POST', body: JSON.stringify({}) });
    const corruptCount = Array.isArray(res.corrupted) ? res.corrupted.length : 0;
    showToast(`Storage scrub complete (corrupted: ${corruptCount})`, corruptCount > 0 ? 'warning' : 'success');
  } catch (err) {
    showToast('Scrub failed: ' + err.message, 'error');
  }
}

// =========================================================================
// PAGE 8: NODES
// =========================================================================
function getFilteredNodesRowsHtml() {
  const filtered = state.nodes.filter(n => {
    const q = state.nodeSearch.toLowerCase();
    return (n.id + ' ' + n.lab + ' ' + n.gpuModel + ' ' + n.os + ' ' + n.transport).toLowerCase().includes(q);
  });
  if (!filtered.length) {
    return '<tr><td colspan="6" class="py-8 text-center text-slate-500">No matching nodes.</td></tr>';
  }
  return filtered.map(n => `
    <tr class="hover:bg-slate-50/70">
      <td class="py-3 px-3 font-semibold text-slate-900 font-mono-code">${escapeHtml(n.id)}</td>
      <td class="py-3 px-3 text-slate-600">${escapeHtml(n.transport)}</td>
      <td class="py-3 px-3 text-slate-700">${escapeHtml(n.isLocal ? `${n.gpuModel} (${n.vramTotal} GB)` : (n.connectedP2P ? `${n.rttMs} ms` : '-'))}</td>
      <td class="py-3 px-3">
        <span class="px-2 py-0.5 rounded-full text-[11px] font-medium ${n.status === 'Online' ? 'bg-emerald-50 text-emerald-700' : 'bg-slate-100 text-slate-600'}">${escapeHtml(n.status)}</span>
      </td>
      <td class="py-3 px-3 text-right space-x-1.5">
        ${!n.isLocal && !n.connectedP2P ? `<button type="button" data-action="peer-connect" data-id="${escapeHtml(n.id)}" class="px-2.5 py-1 rounded-lg bg-emerald-600 hover:bg-emerald-700 text-white text-xs font-semibold">Connect</button>` : ''}
        ${!n.isLocal && n.connectedP2P ? `<button type="button" data-action="peer-ping" data-id="${escapeHtml(n.id)}" class="px-2.5 py-1 rounded-lg border border-slate-200 hover:bg-slate-50 text-xs font-semibold text-slate-700">Ping</button>` : ''}
        ${!n.isLocal && n.connectedP2P ? `<button type="button" data-action="peer-disconnect" data-id="${escapeHtml(n.id)}" class="px-2.5 py-1 rounded-lg border border-rose-200 bg-rose-50 hover:bg-rose-100 text-xs font-semibold text-rose-700">Disconnect</button>` : ''}
        <button type="button" data-action="inspect-node" data-id="${escapeHtml(n.id)}" class="px-2.5 py-1 rounded-lg border border-slate-200 hover:bg-slate-50 text-xs font-semibold text-slate-700">Inspect</button>
      </td>
    </tr>
  `).join('');
}

function renderFleetNodes(container) {
  container.innerHTML = `
    <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="relative flex-1 min-w-[180px] max-w-xs">
          <i data-lucide="search" class="w-3.5 h-3.5 text-slate-400 absolute left-3 top-1/2 -translate-y-1/2"></i>
          <input id="nodeSearchInput" type="text" value="${escapeHtml(state.nodeSearch)}" placeholder="Search nodes..." class="w-full pl-8 pr-3 py-2 rounded-lg border border-slate-200 text-xs focus:outline-none focus:border-slate-400" />
        </div>
        <button type="button" data-action="refresh-fleet" class="px-3.5 py-2 rounded-lg bg-slate-900 hover:bg-slate-800 text-white text-xs font-semibold">Refresh Fleet</button>
      </div>

      <div class="overflow-x-auto">
        <table class="w-full text-left border-collapse min-w-[700px]">
          <thead>
            <tr class="border-b border-slate-200 text-xs font-semibold text-slate-500">
              <th class="py-2.5 px-3">Node ID</th>
              <th class="py-2.5 px-3">Transport</th>
              <th class="py-2.5 px-3">Hardware / RTT</th>
              <th class="py-2.5 px-3">Status</th>
              <th class="py-2.5 px-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody id="nodeTableBody" class="divide-y divide-slate-100 text-xs">
            ${getFilteredNodesRowsHtml()}
          </tbody>
        </table>
      </div>
    </div>
  `;
}

// =========================================================================
// PAGE 9: LOGS
// =========================================================================
function getFilteredLogsRowsHtml() {
  const shown = state.logs.filter(l => {
    const matchCat = state.logTab === 'All' || l.cat === state.logTab;
    const matchSev = state.logSeverity === 'All' || l.sev === state.logSeverity;
    const matchQ = (l.src + ' ' + l.type + ' ' + l.msg).toLowerCase().includes(state.logSearch.toLowerCase());
    return matchCat && matchSev && matchQ;
  });
  if (!shown.length) {
    return '<tr><td colspan="5" class="py-8 text-center text-slate-500">No matching logs.</td></tr>';
  }
  return shown.map(l => `
    <tr class="hover:bg-slate-50/70">
      <td class="py-2.5 px-3 font-mono-code text-slate-500">${escapeHtml(l.ts)}</td>
      <td class="py-2.5 px-3"><span class="px-2 py-0.5 rounded-full text-[11px] font-medium ${l.sev === 'Error' ? 'bg-rose-50 text-rose-700' : l.sev === 'Warning' ? 'bg-amber-50 text-amber-700' : 'bg-sky-50 text-sky-700'}">${escapeHtml(l.sev)}</span></td>
      <td class="py-2.5 px-3 font-semibold text-slate-800">${escapeHtml(l.src)}</td>
      <td class="py-2.5 px-3 text-slate-700">${escapeHtml(l.type)}</td>
      <td class="py-2.5 px-3 text-slate-600 break-words">${escapeHtml(l.msg)}</td>
    </tr>
  `).join('');
}

function updateLogsTableOnly() {
  const tbody = document.getElementById('logTableBody');
  if (tbody) tbody.innerHTML = getFilteredLogsRowsHtml();
}

function renderLogs(container) {
  const cats = ['All', 'Coordinator', 'Network', 'Storage', 'Tasks', 'Worker'];
  container.innerHTML = `
    <div class="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex flex-wrap gap-1.5">
          ${cats.map(c => `<button type="button" data-action="set-log-tab" data-tab="${c}" class="px-3 py-1.5 rounded-lg text-xs font-semibold ${state.logTab === c ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'}">${c}</button>`).join('')}
        </div>
        <div class="flex items-center gap-2">
          <div class="relative w-52">
            <i data-lucide="search" class="w-3.5 h-3.5 text-slate-400 absolute left-3 top-1/2 -translate-y-1/2"></i>
            <input id="logSearchInput" type="text" value="${escapeHtml(state.logSearch)}" placeholder="Filter logs..." class="w-full pl-8 pr-3 py-1.5 rounded-lg border border-slate-200 text-xs focus:outline-none focus:border-slate-400" />
          </div>
          <button type="button" data-action="clear-logs" class="px-3 py-1.5 rounded-lg border border-slate-200 bg-white hover:bg-slate-50 text-xs font-semibold text-slate-700">Clear</button>
        </div>
      </div>

      <div class="rounded-xl border border-slate-200 overflow-x-auto">
        <table class="w-full text-left border-collapse min-w-[680px]">
          <thead>
            <tr class="bg-slate-50 border-b border-slate-200 text-xs font-semibold text-slate-500">
              <th class="py-2.5 px-3">Time</th>
              <th class="py-2.5 px-3">Level</th>
              <th class="py-2.5 px-3">Source</th>
              <th class="py-2.5 px-3">Event</th>
              <th class="py-2.5 px-3">Message</th>
            </tr>
          </thead>
          <tbody id="logTableBody" class="divide-y divide-slate-100 text-xs">
            ${getFilteredLogsRowsHtml()}
          </tbody>
        </table>
      </div>
    </div>
  `;
}

// =========================================================================
// PAGE 10: SETTINGS (/api/coordinator/connect & /api/coordinator/settings)
// =========================================================================
function renderSettings(container) {
  const d = state.settingsDraft;
  const cs = state.coordStatus;

  container.innerHTML = `
    <div class="space-y-5 max-w-3xl">
      <div class="bg-white rounded-xl border border-slate-200 p-6 space-y-4 text-xs">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 pb-4">
          <div>
            <h2 class="text-base font-bold text-slate-900">Coordinator Signaling &amp; Cluster Auth</h2>
            <p class="text-slate-500 mt-0.5">Status: <strong class="font-mono-code">${escapeHtml(cs.status)}</strong> · Token Set: <strong class="font-mono-code">${cs.authTokenSet ? 'true' : 'false'}</strong></p>
          </div>
          <button type="button" data-action="connect-coordinator" class="px-4 py-2 rounded-lg bg-slate-900 hover:bg-slate-800 text-white font-semibold">Connect / Switch Hub</button>
        </div>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div class="sm:col-span-2">
            <label class="block font-semibold text-slate-700 mb-1.5">Coordinator WebSocket URL (ws:// or wss://)</label>
            <input id="setCoordUrl" value="${escapeHtml(d.coordinatorURL)}" placeholder="ws://127.0.0.1:8080/ws" class="w-full px-3 py-2 rounded-lg border border-slate-200 font-mono-code" />
          </div>
          <div class="sm:col-span-2">
            <label class="block font-semibold text-slate-700 mb-1.5">Cluster Auth Token (write-only; never echoed)</label>
            <div class="flex gap-2">
              <input id="setAuthToken" type="password" value="${escapeHtml(d.authToken)}" placeholder="${cs.authTokenSet ? 'Token configured (enter new token to replace)' : 'Enter UNICOMPUTE_AUTH_TOKEN...'}" class="flex-1 px-3 py-2 rounded-lg border border-slate-200 font-mono-code" />
              <button type="button" data-action="save-settings" class="px-3 py-2 rounded-lg bg-slate-900 hover:bg-slate-800 text-white font-semibold">Save Token</button>
              <button type="button" data-action="clear-auth-token" class="px-3 py-2 rounded-lg border border-rose-200 bg-rose-50 hover:bg-rose-100 text-rose-700 font-semibold">Clear Token</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  `;
}

async function connectCoordinatorAction() {
  const url = (state.settingsDraft.coordinatorURL || '').trim();
  if (!url) {
    showToast('Coordinator URL is required', 'warning');
    return;
  }
  const payload = { url };
  if (state.settingsDraft.authToken && state.settingsDraft.authToken.trim() !== '') {
    payload.auth_token = state.settingsDraft.authToken.trim();
  }
  try {
    const res = await apiFetchJson('/api/coordinator/connect', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    state.settingsDraft.authToken = '';
    if (res.reconnect_error) {
      showToast(`Switch error: ${res.reconnect_error}`, 'warning');
    } else {
      showToast(`Connected coordinator: ${res.url}`);
    }
    await syncBackend();
  } catch (err) {
    showToast(`Coordinator connect failed: ${err.message}`, 'error');
  }
}

async function saveSettings(extra = {}) {
  const payload = { ...extra };
  if (state.settingsDraft.authToken && state.settingsDraft.authToken.trim() !== '' && !extra.clear_auth_token) {
    payload.auth_token = state.settingsDraft.authToken.trim();
  }
  try {
    const res = await apiFetchJson('/api/coordinator/settings', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    state.settingsDraft.authToken = '';
    if (res.reconnect_error) {
      showToast(`Settings saved; reconnect note: ${res.reconnect_error}`, 'warning');
    } else {
      showToast(`Cluster auth token ${res.auth_token_set ? 'saved' : 'cleared'}`);
    }
    await syncBackend();
  } catch (err) {
    showToast(`Failed to save settings: ${err.message}`, 'error');
  }
}

// =========================================================================
// PAGE 11: ARCHITECTURE NOTES
// =========================================================================
function renderAttribution(container) {
  container.innerHTML = `
    <div class="bg-white rounded-xl border border-slate-200 p-6 space-y-3 max-w-2xl text-xs text-slate-600">
      <h2 class="text-sm font-bold text-slate-900">UniCompute Control Room Architecture</h2>
      <p>Objective 1 provides Ed25519 challenge-response signaling, DTLS fingerprint binding, WebRTC DataChannel transport with disk-backed chunk streaming, and SHA-256 sharded CAS storage.</p>
      <p>Objective 2 provides rootless Docker code execution (<code class="font-mono-code">/api/code/run</code>) and opt-in authenticated remote shell (<code class="font-mono-code">/api/shell/exec</code>) with peer allowlists (<code class="font-mono-code">/api/worker/settings</code>).</p>
      <p>Objective 3 provides hardware GPU probing, activity watchdog governance, HIPIFY CUDA-to-HIP analysis/translation (<code class="font-mono-code">/api/gpu/hipify</code>), and sandboxed task execution (<code class="font-mono-code">/api/gpu/run</code>).</p>
    </div>
  `;
}

// =========================================================================
// DELEGATED EVENT LISTENERS (ZERO INLINE HANDLERS)
// =========================================================================
document.addEventListener('click', (e) => {
  const navBtn = e.target.closest('.nav-btn');
  if (navBtn && navBtn.dataset.nav) {
    navigate(navBtn.dataset.nav);
    return;
  }

  const target = e.target.closest('[data-action]');
  if (!target) return;

  const action = target.dataset.action;
  const id = target.dataset.id || '';

  if (action === 'close-mobile-nav') toggleMobileNav(false);
  else if (action === 'open-mobile-nav') toggleMobileNav(true);
  else if (action === 'open-create-task') openCreateTask('Auto');
  else if (action === 'goto-settings') navigate('settings');
  else if (action === 'close-modal') {
    if (e.target.id === 'modalBackdrop' || target.closest('button[data-action="close-modal"]')) closeModal();
  } else if (action === 'nav' && target.dataset.page) navigate(target.dataset.page);
  else if (action === 'open-task' && id) openTaskDetail(id);
  else if (action === 'set-topo-view') { state.topoViewMode = target.dataset.mode || 'mesh'; renderCurrentPageOnly(); }
  else if (action === 'select-topo-node' && id) { state.selectedTopoNodeId = id; renderCurrentPageOnly(); }
  else if (action === 'view-node-logs' && id) { state.logSearch = id; state.logTab = 'All'; navigate('logs'); }
  else if (action === 'assign-workload') openCreateTask(id || 'Auto');
  else if (action === 'peer-connect' && id) connectPeerAction(id);
  else if (action === 'peer-disconnect' && id) disconnectPeerAction(id);
  else if (action === 'peer-ping' && id) pingPeerAction(id);
  else if (action === 'run-sandbox-code') runSandboxCode();
  else if (action === 'pick-cuda-file') document.getElementById('cudaFileInput')?.click();
  else if (action === 'run-hipify') runHipify();
  else if (action === 'download-hip') downloadHipFile();
  else if (action === 'dispatch-gpu-task') dispatchGpuBatch(target.dataset.taskType || 'training');
  else if (action === 'allowlist-wildcard') updateWorkerAllowlist(['*']);
  else if (action === 'allowlist-deny-all') updateWorkerAllowlist([]);
  else if (action === 'allowlist-remove' && id) {
    updateWorkerAllowlist(state.workerSettings.codeAllowlist.filter(x => x !== id));
  } else if (action === 'set-task-tab') {
    state.taskFilterTab = target.dataset.tab || 'All';
    renderCurrentPageOnly();
  } else if (action === 'scrub-storage') repairAllShards();
  else if (action === 'open-put-blob') openAddDatasetModal();
  else if (action === 'view-blob' && id) viewBlobAction(id);
  else if (action === 'delete-blob' && id) deleteBlobAction(id);
  else if (action === 'refresh-fleet') { syncBackend().then(() => showToast('Fleet refreshed')); }
  else if (action === 'inspect-node' && id) { state.selectedTopoNodeId = id; navigate('topology'); }
  else if (action === 'set-log-tab') { state.logTab = target.dataset.tab || 'All'; renderCurrentPageOnly(); }
  else if (action === 'clear-logs') { state.logs = []; updateLogsTableOnly(); }
  else if (action === 'connect-coordinator') connectCoordinatorAction();
  else if (action === 'save-settings') saveSettings();
  else if (action === 'clear-auth-token') saveSettings({ clear_auth_token: true });
});

document.addEventListener('input', (e) => {
  const el = e.target;
  if (!el || !el.id) return;

  if (el.id === 'cudaEditor') state.cudaSource = el.value;
  else if (el.id === 'sbSourceEditor') state.sandbox.source = el.value;
  else if (el.id === 'sbStdinEditor') state.sandbox.stdin = el.value;
  else if (el.id === 'sbTimeoutSlider') {
    state.sandbox.timeout = Number(el.value);
    const lbl = document.getElementById('sbTimeoutLabel');
    if (lbl) lbl.textContent = `${state.sandbox.timeout}s`;
  } else if (el.id === 'shellCmdInput') state.sandbox.shellCommand = el.value;
  else if (el.id === 'allowlistPeerInput') state.workerSettings.newPeerInput = el.value;
  else if (el.id === 'taskSearchInput') {
    state.taskSearch = el.value;
    const box = document.getElementById('taskListContainer');
    if (box) box.innerHTML = getFilteredTasksHtml();
  } else if (el.id === 'nodeSearchInput') {
    state.nodeSearch = el.value;
    const tbody = document.getElementById('nodeTableBody');
    if (tbody) tbody.innerHTML = getFilteredNodesRowsHtml();
  } else if (el.id === 'logSearchInput') {
    state.logSearch = el.value;
    updateLogsTableOnly();
  } else if (el.id === 'ctTitle') state.createForm.title = el.value;
  else if (el.id === 'ctTimeout') state.createForm.timeout = Number(el.value);
  else if (el.id === 'ctSource') state.createForm.source = el.value;
  else if (el.id === 'ctStdin') state.createForm.stdin = el.value;
  else if (el.id === 'ctBatches') state.createForm.batches = Number(el.value);
  else if (el.id === 'ctGpuSource') state.createForm.gpuSourceCode = el.value;
  else if (el.id === 'setCoordUrl') state.settingsDraft.coordinatorURL = el.value;
  else if (el.id === 'setAuthToken') state.settingsDraft.authToken = el.value;
});

document.addEventListener('change', (e) => {
  const el = e.target;
  if (!el || !el.id) return;

  if (el.id === 'cudaFileInput') {
    const file = el.files && el.files[0];
    if (!file) return;
    state.gpuSelectedFile = file.name;
    const reader = new FileReader();
    reader.onload = (ev) => {
      state.cudaSource = ev.target.result;
      runHipify();
    };
    reader.readAsText(file);
  } else if (el.id === 'cudaSampleSelect') {
    if (el.value === '') return;
    const sample = state.cudaSamples[Number(el.value)];
    if (!sample) return;
    state.gpuSelectedFile = sample.name;
    state.cudaSource = sample.content;
    state.hipSource = '';
    state.hipifyResult = null;
    renderCurrentPageOnly();
  } else if (el.id === 'sbSampleSelect') {
    const codeSamples = state.allSamples.filter(s => s.language === 'python' || s.language === 'c' || s.language === 'cpp');
    if (el.value === '') return;
    const sample = codeSamples[Number(el.value)];
    if (!sample) return;
    state.sandbox.selectedSample = sample.name;
    state.sandbox.language = sample.language;
    state.sandbox.source = sample.content;
    renderCurrentPageOnly();
  } else if (el.id === 'sbLanguage') state.sandbox.language = el.value;
  else if (el.id === 'sbTargetPeer') state.sandbox.targetPeer = el.value;
  else if (el.id === 'shellTargetPeer') state.sandbox.shellPeer = el.value;
  else if (el.id === 'ctTier') {
    state.createForm.tier = el.value;
    if (el.value !== 'Tier 2 Container') {
      state.createForm.targetNode = 'Auto';
    }
    renderCurrentPageOnly();
  }
  else if (el.id === 'ctTargetNode') state.createForm.targetNode = el.value;
  else if (el.id === 'ctLang') state.createForm.language = el.value;
  else if (el.id === 'ctGpuType') state.createForm.gpuTaskType = el.value;
});

document.addEventListener('submit', (e) => {
  const form = e.target;
  if (!form || !form.id) return;
  e.preventDefault();

  if (form.id === 'shellExecForm') runShellExec();
  else if (form.id === 'allowlistAddForm') {
    const entry = state.workerSettings.newPeerInput.trim();
    if (!entry) return;
    const next = Array.from(new Set([...state.workerSettings.codeAllowlist, entry]));
    updateWorkerAllowlist(next);
  } else if (form.id === 'createTaskForm') submitCreatedTask();
  else if (form.id === 'putBlobForm') submitNewDataset();
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') closeModal();
});

window.addEventListener('DOMContentLoaded', () => {
  renderIcons(document);
  navigate('overview');
  syncBackend();
  loadSamplesFromBackend();
  initWebSocket();
  setInterval(syncBackend, 5000);
});
