'use strict';

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const GEO = { minLon: 112.85, maxLon: 113.95, minLat: 22.55, maxLat: 23.55, w: 1100, h: 820, pad: 34 };
const state = {
  overview: null,
  map: null,
  selectedMessage: null,
  activeMessageID: null,
  auth: JSON.parse(localStorage.getItem('amsgw-auth') || 'null'),
  clientsLoaded: false,
  viewBox: { x: 0, y: 0, w: 1100, h: 820 },
};

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function fmtTime(t) {
  if (!t) return '—';
  const d = new Date(t);
  return d.toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
}
function fmtClock(t) {
  if (!t) return '—';
  return new Date(t).toLocaleTimeString('zh-CN', { hour12: false });
}
async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw Object.assign(new Error(data.error || res.statusText), { data, status: res.status });
  return data;
}

function baseCall(call) {
  call = (call || '').toUpperCase().trim();
  const i = call.lastIndexOf('-');
  if (i >= 0 && /^\d{1,2}$/.test(call.slice(i + 1))) return call.slice(0, i);
  return call;
}
function passcode(call) {
  const root = baseCall(call);
  let h = 0x73e2;
  for (let i = 0; i < root.length; i++) {
    const code = root.charCodeAt(i);
    if (i % 2 === 0) h ^= (code << 8);
    else h ^= code;
  }
  return h & 0x7fff;
}

function project(lat, lon) {
  const x = GEO.pad + ((lon - GEO.minLon) / (GEO.maxLon - GEO.minLon)) * (GEO.w - GEO.pad * 2);
  const y = GEO.h - GEO.pad - ((lat - GEO.minLat) / (GEO.maxLat - GEO.minLat)) * (GEO.h - GEO.pad * 2);
  return [x, y];
}

function svgEl(tag, attrs = {}, parent) {
  const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'text') el.textContent = v;
    else el.setAttribute(k, v);
  }
  if (parent) parent.appendChild(el);
  return el;
}

function hopLabel(h) {
  if (!h) return '';
  if (h.kind === 'q') return `${h.marker}=${h.via}`;
  if (h.kind === 'relay') return h.alias ? `${h.call}[${h.alias}]` : h.call;
  if (h.kind === 'alias') return `${h.alias}#${h.remaining}`;
  return h.raw || '';
}

/* ---------- Tabs ---------- */
$$('.tab').forEach(btn => btn.addEventListener('click', () => {
  $$('.tab').forEach(b => b.classList.toggle('active', b === btn));
  const name = btn.dataset.tab;
  $$('.tab-panel').forEach(p => p.classList.toggle('active', p.dataset.panel === name));
  if (name === 'clients' && !state.clientsLoaded) loadClients();
  if (name === 'objects') renderObjects();
  if (name === 'messages') loadChats();
}));

/* ---------- Login ---------- */
function renderAuth() {
  const el = $('#loginStatus');
  if (!state.auth) {
    el.textContent = '未登录';
    el.className = 'login-status';
    return;
  }
  $('#callInput').value = state.auth.call;
  $('#passInput').value = state.auth.passcode;
  if (state.auth.passcode_ok) {
    el.textContent = `已登录 ${state.auth.call} · 可发`;
    el.className = 'login-status ok';
  } else {
    el.textContent = `${state.auth.call} · 口令错，仅可收`;
    el.className = 'login-status bad';
  }
}
$('#deriveBtn').addEventListener('click', () => {
  const call = $('#callInput').value.trim().toUpperCase();
  $('#passInput').value = String(passcode(call)).padStart(5, '0');
});
$('#loginBtn').addEventListener('click', async () => {
  const call = $('#callInput').value.trim().toUpperCase();
  const passcodeText = $('#passInput').value.trim() || String(passcode(call)).padStart(5, '0');
  try {
    const r = await api('/api/login', { method: 'POST', body: JSON.stringify({ call, passcode: passcodeText }) });
    state.auth = { call: r.call, passcode: passcodeText, passcode_ok: r.passcode_ok };
    localStorage.setItem('amsgw-auth', JSON.stringify(state.auth));
    renderAuth();
  } catch (e) {
    $('#loginStatus').textContent = '登录失败';
  }
});
renderAuth();

/* ---------- Overview ---------- */
async function loadOverview() {
  try {
    state.overview = await api('/api/overview');
    renderStats();
    renderVerify(state.overview.verify);
  } catch (e) { /* server may be starting */ }
}
function renderStats() {
  const c = state.overview.counts;
  const items = [
    ['台站', c.stations], ['报文', c.messages], ['位置', c.positions],
    ['中继转发', c.relay_forwards], ['网关发射', c.transmissions],
    ['短消息', c.chats], ['物体', c.objects_active], ['时间异常', c.time_bad],
    ['畸形报文', c.bad_packets], ['拒收', c.denials],
  ];
  $('#stats').innerHTML = items.map(([k, v]) => `<div class="stat"><b>${v}</b><span>${k}</span></div>`).join('');
}

/* ---------- Map ---------- */
async function loadMap() {
  state.map = await api('/api/map');
  renderMap();
  renderObjects();
}
function renderMap() {
  if (!state.map) return;
  drawTerrain();
  drawCoverage();
  drawTracks();
  drawObjects();
  drawMarkers();
}
function drawTerrain() {
  const g = $('#terrain');
  g.innerHTML = '';
  // Fictional coastline / river / hill masses, fixed in projected coordinates.
  const coast = 'M -20,610 C 150,540 210,690 360,610 S 610,520 760,600 980,650 1140,560 L1140,840 L-20,840 Z';
  svgEl('path', { d: coast, fill: '#cfe8f5', class: 'terrain' }, g);
  const river = 'M 180,-20 C 260,160 420,210 470,360 S 650,560 760,840';
  svgEl('path', { d: river, fill: 'none', stroke: '#8ecae6', 'stroke-width': 18, opacity: '.35', 'stroke-linecap': 'round' }, g);
  const hills = [
    'M120,180 C190,80 300,90 360,170 C300,150 210,160 120,180Z',
    'M650,120 C740,30 880,70 930,170 C840,130 740,130 650,120Z',
    'M480,260 C540,205 620,220 660,285 C590,260 535,265 480,260Z',
  ];
  hills.forEach(d => svgEl('path', { d, fill: '#b7d8b0', class: 'terrain' }, g));
  // Coordinate labels.
  for (let lon = 113.0; lon <= 113.8; lon += 0.2) {
    const [x] = project(22.55, lon);
    svgEl('text', { x, y: 806, 'font-size': 11, fill: '#789' , text: lon.toFixed(1) + '°E' }, g);
  }
  for (let lat = 22.7; lat <= 23.5; lat += 0.2) {
    const [, y] = project(lat, 112.85);
    svgEl('text', { x: 6, y, 'font-size': 11, fill: '#789', text: lat.toFixed(1) + '°N' }, g);
  }
}
function drawCoverage() {
  const g = $('#coverage');
  g.innerHTML = '';
  if (!$('#showCoverage').checked) return;
  for (const call of state.map.station_order || []) {
    const s = state.map.stations[call];
    if (!s || (s.kind !== 'relay' && s.kind !== 'gateway')) continue;
    const [x, y] = project(s.lat, s.lon);
    // Approx degrees for radius display at latitude 23.
    const rx = s.range_km / (111.32 * Math.cos(23 * Math.PI / 180)) * (GEO.w / (GEO.maxLon - GEO.minLon));
    const ry = s.range_km / 110.574 * (GEO.h / (GEO.maxLat - GEO.minLat));
    svgEl('ellipse', { cx: x, cy: y, rx, ry, class: 'coverage-circle' }, g);
  }
}
function drawTracks() {
  const tracks = $('#tracks');
  const pointsG = $('#reportPoints');
  tracks.innerHTML = '';
  pointsG.innerHTML = '';
  const showPoints = $('#showPoints').checked;
  for (const call of state.map.station_order || []) {
    const s = state.map.stations[call];
    if (!s || !s.track || s.track.length < 2) continue;
    const pts = s.track.map(p => project(p.lat, p.lon).join(',')).join(' ');
    svgEl('polyline', { points: pts, class: 'track-line' }, tracks);
    if (!showPoints) continue;
    for (const p of s.track) {
      const [x, y] = project(p.lat, p.lon);
      const reason = p.reason || 'interval';
      svgEl('circle', { cx: x, cy: y, r: reason === 'turn' ? 4.2 : 2.4, class: `rp ${reason}` }, pointsG);
    }
  }
}
function drawObjects() {
  const g = $('#objects');
  g.innerHTML = '';
  if (!$('#showObjects').checked) return;
  for (const name of state.map.object_order || []) {
    const o = state.map.objects[name];
    if (!o) continue;
    const [x, y] = project(o.lat, o.lon);
    const text = svgEl('text', {
      x, y: y + 6, 'font-size': 20, 'text-anchor': 'middle',
      opacity: o.active ? 1 : 0.45, class: 'marker object-marker',
      text: o.active ? '⭐' : '☁',
    }, g);
    text.addEventListener('click', ev => { ev.stopPropagation(); selectObject(name); });
    const title = svgEl('text', { x: x + 12, y: y - 8, class: 'marker-label', text: name }, g);
    title.style.pointerEvents = 'none';
  }
}
function drawMarkers() {
  const g = $('#stationMarkers');
  g.innerHTML = '';
  for (const call of state.map.station_order || []) {
    const s = state.map.stations[call];
    if (!s) continue;
    const [x, y] = project(s.lat, s.lon);
    const group = svgEl('g', { class: 'marker', transform: `translate(${x},${y})` }, g);
    group.addEventListener('click', ev => { ev.stopPropagation(); selectStation(call); });
    if (s.kind === 'relay') {
      svgEl('polygon', { points: '0,-9 9,8 -9,8', class: 'relay-tri', filter: 'url(#shadow)' }, group);
      svgEl('text', { x: 12, y: 4, class: 'marker-label', text: call }, group);
    } else if (s.kind === 'gateway') {
      svgEl('rect', { x: -7, y: -7, width: 14, height: 14, rx: 3, class: 'gateway-sq', filter: 'url(#shadow)' }, group);
      svgEl('text', { x: 11, y: 4, class: 'marker-label', text: call }, group);
    } else if (s.kind === 'weather') {
      svgEl('rect', { x: -6, y: -6, width: 12, height: 12, rx: 6, fill: '#20c997', stroke: 'white', 'stroke-width': 1.5 }, group);
      svgEl('text', { x: 10, y: 4, class: 'marker-label', text: call }, group);
    } else {
      svgEl('circle', { r: s.speed_kmh > 5 ? 4.2 : 3.4, class: `station-dot ${s.speed_kmh > 5 ? 'mobile' : 'fixed'}` }, group);
    }
    const title = svgEl('title', { text: `${call} · ${s.kind}` }, group);
  }
}

function mapCard(html) {
  const card = $('#mapCard');
  card.innerHTML = `<button class="close" id="mapCardClose">×</button>${html}`;
  card.classList.remove('hidden');
  $('#mapCardClose').onclick = () => card.classList.add('hidden');
}
function selectStation(call) {
  const s = state.map.stations[call];
  if (!s) return;
  const intervals = [];
  for (let i = 1; i < (s.track || []).length; i++) {
    intervals.push((new Date(s.track[i].at) - new Date(s.track[i - 1].at)) / 1000);
  }
  const minI = intervals.length ? Math.min(...intervals) : 0;
  const maxI = intervals.length ? Math.max(...intervals) : 0;
  mapCard(`
    <h3>${esc(call)}</h3>
    <div class="kv">
      <span>名称</span><b>${esc(s.name || '')}</b>
      <span>类型</span><b>${esc(s.kind)}</b>
      <span>位置</span><b>${s.lat.toFixed(5)}, ${s.lon.toFixed(5)}</b>
      <span>速度/方向</span><b>${(s.speed_kmh || 0).toFixed(1)} km/h · ${Math.round(s.heading || 0)}°</b>
      <span>最后报告</span><b>${fmtTime(s.last_seen)}</b>
      <span>报告点数</span><b>${(s.track || []).length}</b>
      <span>间隔范围</span><b>${Math.round(minI)}–${Math.round(maxI)} 秒</b>
      <span>别名/范围</span><b>${esc((s.aliases || []).join(','))} ${s.range_km ? '· ' + Math.round(s.range_km) + 'km' : ''}</b>
    </div>
    <p class="hint">红色大点是转弯立即补报；静止台站的点稀疏并呈灰色。</p>
    <button class="secondary small" id="stationPackets">查看该呼号报文</button>
  `);
  $('#stationPackets').onclick = () => {
    $$('.tab').find(b => b.dataset.tab === 'packets').click();
    $('#searchInput').value = call;
    loadMessages();
  };
}
function selectObject(name) {
  const o = state.map.objects[name];
  if (!o) return;
  const canDelete = state.auth && state.auth.passcode_ok && state.auth.call === o.owner;
  mapCard(`
    <h3>${esc(o.name)} ${o.active ? '⭐' : '（已撤销）'}</h3>
    <div class="kv">
      <span>原报告者</span><b>${esc(o.owner)}</b>
      <span>代报告者</span><b>${esc(o.last_reporter)}</b>
      <span>位置</span><b>${o.lat.toFixed(5)}, ${o.lon.toFixed(5)}</b>
      <span>创建</span><b>${fmtTime(o.created_at)}</b>
      <span>更新</span><b>${fmtTime(o.updated_at)}</b>
      <span>版本</span><b>${o.version}</b>
    </div>
    ${o.active ? `<button id="mapDeleteObject" ${canDelete ? '' : 'disabled title="只有 owner 且口令正确可撤"'}>撤销物体</button>
      ${!canDelete ? '<p class="hint">用 owner 呼号 ' + esc(o.owner) + ' 登录后可撤销。</p>' : ''}` : ''}
    <div id="objectAction"></div>
  `);
  const btn = $('#mapDeleteObject');
  if (btn) btn.onclick = () => deleteObject(o.name);
}

$('#showPoints').addEventListener('change', drawTracks);
$('#showCoverage').addEventListener('change', drawCoverage);
$('#showObjects').addEventListener('change', drawObjects);
$('#clearRoute').addEventListener('click', () => { $('#routeLayer').innerHTML = ''; });

/* Basic SVG zoom/pan */
const svg = $('#map');
svg.addEventListener('wheel', ev => {
  ev.preventDefault();
  const vb = state.viewBox;
  const scale = ev.deltaY > 0 ? 1.12 : 0.89;
  const rect = svg.getBoundingClientRect();
  const mx = ((ev.clientX - rect.left) / rect.width) * vb.w + vb.x;
  const my = ((ev.clientY - rect.top) / rect.height) * vb.h + vb.y;
  const nw = Math.max(220, Math.min(1100, vb.w * scale));
  const nh = nw * 820 / 1100;
  state.viewBox = { x: mx - (mx - vb.x) * (nw / vb.w), y: my - (my - vb.y) * (nh / vb.h), w: nw, h: nh };
  svg.setAttribute('viewBox', `${state.viewBox.x} ${state.viewBox.y} ${state.viewBox.w} ${state.viewBox.h}`);
}, { passive: false });

let mapDrag = null;
svg.addEventListener('pointerdown', ev => {
  if (ev.target.closest('.marker')) return;
  mapDrag = { x: ev.clientX, y: ev.clientY, vb: { ...state.viewBox } };
  svg.setPointerCapture(ev.pointerId);
});
svg.addEventListener('pointermove', ev => {
  if (!mapDrag) return;
  const rect = svg.getBoundingClientRect();
  const sx = state.viewBox.w / rect.width;
  const sy = state.viewBox.h / rect.height;
  state.viewBox.x = mapDrag.vb.x - (ev.clientX - mapDrag.x) * sx;
  state.viewBox.y = mapDrag.vb.y - (ev.clientY - mapDrag.y) * sy;
  svg.setAttribute('viewBox', `${state.viewBox.x} ${state.viewBox.y} ${state.viewBox.w} ${state.viewBox.h}`);
});
function endDrag() { mapDrag = null; }
svg.addEventListener('pointerup', endDrag);
svg.addEventListener('pointercancel', endDrag);

/* ---------- Messages ---------- */
let searchTimer = null;
$('#searchInput').addEventListener('input', () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(loadMessages, 250);
});
$('#kindFilter').addEventListener('change', loadMessages);
$('#refreshMessages').addEventListener('click', loadMessages);

async function loadMessages() {
  const kind = $('#kindFilter').value;
  const q = $('#searchInput').value.trim();
  const params = new URLSearchParams({ limit: '400' });
  if (kind) params.set('kind', kind);
  if (q) params.set('q', q);
  const data = await api('/api/messages?' + params);
  renderMessageList($('#messageList'), data.messages, true);
}
async function loadChats() {
  const data = await api('/api/messages?kind=chat&limit=120');
  renderMessageList($('#chatList'), data.messages, false);
}
function renderMessageList(container, messages, showDetailOnClick) {
  container.innerHTML = messages.map(m => {
    const route = (m.route_calls || []).join(' → ');
    const badges = [
      `<span class="badge ${m.kind}">${kindName(m.kind)}</span>`,
      m.source_class ? `<span class="badge ${m.source_class}">${sourceName(m.source_class)}</span>` : '',
      m.time_bad ? '<span class="badge timebad">时间异常</span>' : '',
      m.downlink_gateway ? `<span class="badge gw">网关 ${esc(m.downlink_gateway)}${m.delivered_rf ? ' ✓' : ''}</span>` : '',
    ].join('');
    const title = m.kind === 'chat' ? `${esc(m.source)} → ${esc(m.target)}` :
      m.kind.startsWith('object') ? `${esc(m.object)} · ${esc(m.source)}` :
      `${esc(m.source)}${m.target ? ' → ' + esc(m.target) : ''}`;
    const sub = m.text ? esc(m.text) : route ? `路径：${esc(route)}` :
      m.kind === 'position' ? `${m.report_reason || ''} ${(m.route_calls || []).length ? '· 中继 ' + esc(route) : '· 直收'}` :
      m.object ? `物体 ${esc(m.object)}` : m.id;
    return `<div class="message-row ${state.activeMessageID === m.id ? 'active' : ''}" data-id="${esc(m.id)}">
      <div class="row-top">${badges}<span class="row-title">${title}</span><span class="row-time">${fmtClock(m.received_at)}</span></div>
      <div class="row-sub">${sub}</div>
    </div>`;
  }).join('') || '<p class="muted">没有匹配报文。</p>';
  container.querySelectorAll('.message-row').forEach(row => row.addEventListener('click', async () => {
    state.activeMessageID = row.dataset.id;
    $$('.message-row').forEach(r => r.classList.toggle('active', r.dataset.id === state.activeMessageID));
    const m = await api('/api/message?id=' + encodeURIComponent(row.dataset.id));
    state.selectedMessage = m;
    if (showDetailOnClick) renderDetail(m);
    drawMessageRoute(m);
    if ($('.tab-panel.active')?.dataset.panel === 'messages' && !showDetailOnClick) {
      $$('.tab').find(b => b.dataset.tab === 'packets').click();
      renderDetail(m);
    }
  }));
}
function kindName(k) {
  return { position: '位置', weather: '气象', chat: '短消息', object: '物体', 'object-remove': '撤销', notice: '通知' }[k] || k;
}
function sourceName(s) {
  return { direct: '网关直收', relayed: '经中继', network: '其他服务器', client: '客户端', server: '服务器' }[s] || s;
}

function renderDetail(m) {
  const d = $('#packetDetail');
  d.classList.remove('hidden');
  const pathChips = [`<span class="chip src">${esc(m.source)}</span><span class="arrow">→</span>`];
  (m.route_calls || []).forEach((c, i) => {
    pathChips.push(`<span class="chip relay">${esc(c)}</span><span class="arrow">→</span>`);
  });
  if (m.q_marker || m.via) pathChips.push(`<span class="chip gw">${esc(m.q_marker || 'Q')}=${esc(m.via || '')}</span><span class="arrow">→</span>`);
  pathChips.push('<span class="chip server">SRV</span>');
  if ((m.downlinks || []).length) {
    m.downlinks.forEach(dl => {
      pathChips.push('<span class="arrow">⇢</span>');
      pathChips.push(`<span class="chip gw">${esc(dl.gateway || '无合格网关')}</span>`);
    });
  }
  const receptions = (m.receptions || []).map((r, i) => `
    <div class="reception ${m.time_bad ? 'bad-time' : ''}">
      <b>副本 ${i + 1}</b> · ${fmtTime(r.at)} · ${esc(r.ingress)} ${esc(r.via || '')} · ${sourceName(r.source_class)}
      <div class="muted">${(r.path || []).map(hopLabel).map(esc).join(' → ')}</div>
      <pre class="raw">${esc(r.raw)}</pre>
    </div>`).join('');
  const downlinks = (m.downlinks || []).map(dl => `
    <div class="reception">
      <b>下行网关 ${esc(dl.gateway || '无')}</b> · 听邻时间 ${fmtTime(dl.heard_at)} · ${Math.round(dl.heard_age_seconds || 0)}s 前听到 ${esc(dl.recipient)} · RF ${dl.delivered_rf ? '送达' : (dl.gateway ? '随机丢失' : '未发射')}
      ${dl.frame ? `<pre class="raw">${esc(dl.frame)}</pre>` : '<p class="muted">没有任何网关在 TTL 内直接听到收件人，因此未转到无线电。</p>'}
    </div>`).join('');
  d.innerHTML = `
    <h3>${esc(m.id)} · ${kindName(m.kind)}</h3>
    <div class="path-flow">${pathChips.join('')}</div>
    <div class="kv">
      <span>服务器收到</span><b>${fmtTime(m.received_at)}</b>
      <span>报文时间</span><b>${fmtTime(m.inferred_at)}（${esc(m.stamp_precision || '—')}）</b>
      <span>时间偏差</span><b class="${m.time_bad ? 'error-text' : 'good-text'}">${Math.round(m.skew_seconds || 0)} 秒${m.time_bad ? ' · 超过 20 分钟阈值' : ''}</b>
      <span>来源途径</span><b>${sourceName(m.source_class)} ${esc(m.q_marker || '')}</b>
      ${m.target ? '<span>收件人</span><b>' + esc(m.target) + '</b>' : ''}
      ${m.text ? '<span>文本</span><b>' + esc(m.text) + '</b>' : ''}
      ${m.object ? '<span>物体</span><b>' + esc(m.object) + ' / ' + esc(m.object_action) + '</b>' : ''}
    </div>
    ${downlinks ? '<h4>短消息下行 / 第三方封装</h4>' + downlinks : ''}
    <h4>所有重复副本与完整路径</h4>
    ${receptions || '<p class="muted">无副本记录。</p>'}
    <h4>汇聚后原始帧</h4>
    <pre class="raw">${esc(m.raw)}</pre>
  `;
}

function coordForCall(call, fallback) {
  if (fallback && Number.isFinite(fallback.lat)) return [fallback.lat, fallback.lon];
  const s = state.map?.stations?.[call];
  return s ? [s.lat, s.lon] : null;
}
function drawMessageRoute(m) {
  const g = $('#routeLayer');
  g.innerHTML = '';
  const coords = [];
  const src = coordForCall(m.source, m.has_location ? { lat: m.lat, lon: m.lon } : null);
  if (src) coords.push({ call: m.source, p: src, cls: 'src' });
  (m.route_calls || []).forEach(call => {
    const p = coordForCall(call);
    if (p) coords.push({ call, p, cls: 'relay' });
  });
  const gw = m.via && /^GW/.test(m.via) ? m.via : (m.downlinks?.[0]?.gateway);
  if (gw) {
    const p = coordForCall(gw);
    if (p) coords.push({ call: gw, p, cls: 'gw' });
  }
  if (m.kind === 'chat' && m.target) {
    const p = coordForCall(m.target);
    if (p) coords.push({ call: m.target, p, cls: 'target' });
  }
  if (coords.length >= 2) {
    svgEl('polyline', { points: coords.map(x => project(x.p[0], x.p[1]).join(',')).join(' '), class: 'route-line', 'stroke-dasharray': m.kind === 'chat' ? '9 6' : '' }, g);
  }
  coords.forEach((x, i) => {
    const [sx, sy] = project(x.p[0], x.p[1]);
    svgEl('circle', { cx: sx, cy: sy, r: 6, class: 'route-node', fill: x.cls === 'relay' ? '#ff9f1c' : x.cls === 'gw' ? '#7c5cff' : x.cls === 'target' ? '#b78cff' : '#38d39f' }, g);
    if (i === coords.length - 1 || x.cls === 'relay' || x.cls === 'gw') {
      svgEl('text', { x: sx + 9, y: sy - 9, class: 'route-label', text: x.call }, g);
    }
  });
}

/* ---------- Objects ---------- */
function renderObjects() {
  const box = $('#objectList');
  if (!box || !state.map) return;
  const names = [...(state.map.object_order || [])].sort((a, b) => a.localeCompare(b, 'zh'));
  box.innerHTML = names.map(name => {
    const o = state.map.objects[name];
    const canDelete = state.auth?.passcode_ok && state.auth.call === o.owner && o.active;
    return `<div class="object-card ${o.active ? '' : 'inactive'}">
      <div class="object-title">${esc(o.name)} <span class="pill ${o.active ? 'ok' : 'ro'}">${o.active ? '在管' : '已撤'}</span>
        ${o.active ? `<button class="small" data-delete="${esc(o.name)}" ${canDelete ? '' : 'disabled'}>撤销</button>` : ''}
      </div>
      <div class="object-meta">
        <span>owner / 原报告者：<b>${esc(o.owner)}</b></span>
        <span>last reporter / 代报告者：<b>${esc(o.last_reporter)}</b></span>
        <span>位置：${o.lat.toFixed(5)}, ${o.lon.toFixed(5)} · v${o.version}</span>
        <span>更新：${fmtTime(o.updated_at)}</span>
        ${!canDelete && o.active ? '<span class="muted">只有 owner 用正确口令登录后可撤。</span>' : ''}
      </div>
    </div>`;
  }).join('');
  box.querySelectorAll('[data-delete]').forEach(btn => btn.addEventListener('click', () => deleteObject(btn.dataset.delete)));
}
async function deleteObject(name) {
  if (!state.auth?.passcode_ok) {
    alert('请先用 owner 呼号和正确口令登录。');
    return;
  }
  try {
    await api('/api/object/remove', {
      method: 'POST',
      body: JSON.stringify({ call: state.auth.call, passcode: state.auth.passcode, name }),
    });
    await loadMap();
    renderObjects();
  } catch (e) {
    alert('撤销被拒：' + e.message);
    await loadMap();
  }
}

/* ---------- Send ---------- */
$('#sendBtn').addEventListener('click', async () => {
  const to = $('#toInput').value.trim().toUpperCase();
  const text = $('#textInput').value.trim();
  const out = $('#sendResult');
  if (!state.auth?.passcode_ok) {
    out.innerHTML = '<span class="send-bad">当前是只读/未登录状态；请先在顶部用正确口令登录。</span>';
    return;
  }
  try {
    const r = await api('/api/send', { method: 'POST', body: JSON.stringify({ call: state.auth.call, passcode: state.auth.passcode, to, text }) });
    const gw = r.downlink?.gateway;
    out.innerHTML = gw
      ? `<span class="send-ok">已进服务器，并由 ${esc(gw)} 按第三方格式转无线电；RF 送达：${r.downlink.delivered_rf ? '是' : '随机丢失（可重发观察）'}。</span><pre class="raw">${esc(r.downlink.frame)}</pre>`
      : '<span class="send-bad">服务器已收，但最近 45 分钟没有网关在附近听到 ' + esc(to) + '，未转无线电。</span>';
    loadMessages();
  } catch (e) {
    out.innerHTML = `<span class="send-bad">发送失败：${esc(e.message)}</span>`;
  }
});

async function loadHeard() {
  try {
    const data = await api('/api/heard');
    $('#heardList').innerHTML = data.heard.map(h => `<option value="${esc(h.call)}">${esc(h.gateway)} · ${Math.round(h.age_seconds)}s</option>`).join('');
    if (data.heard.length && !state.heardChosen) {
      state.heardChosen = true;
      // Pick a recently heard callsign so the gateway third-party path is visible.
      $('#toInput').placeholder = `最近听到：${data.heard[0].call} / ${data.heard[0].gateway}`;
    }
  } catch (e) { /* optional */ }
}

/* ---------- Clients ---------- */
async function loadClients() {
  state.clientsLoaded = true;
  const data = await api('/api/clients');
  $('#clientList').innerHTML = data.clients.map(c => `
    <div class="client-row" data-id="${esc(c.id)}">
      <span class="call">${esc(c.call)}</span>
      <span class="sub">${subscriptionText(c.subs)} · 收件箱 ${c.inbox_count}</span>
      <span class="pill ${c.passcode_ok ? 'ok' : 'ro'}">${c.passcode_ok ? '口令正确/可发' : '口令错/只收'}</span>
    </div>`).join('');
  $$('.client-row').forEach(row => row.addEventListener('click', async () => {
    const c = await api('/api/client?id=' + encodeURIComponent(row.dataset.id));
    const detail = $('#clientDetail');
    detail.classList.remove('hidden');
    detail.textContent = `${c.id} ${c.call}\ncan_send=${c.can_send}\nsubs=${JSON.stringify(c.subs)}\n\n收到的报文集合（${c.inbox.length}）:\n${c.inbox.join('\n')}`;
  }));
}
function subscriptionText(subs) {
  const kinds = Object.keys(subs?.kinds || {});
  const calls = Object.keys(subs?.calls || {});
  if (!kinds.length && !calls.length) return '订阅全部公开报文 + 与我相关私信';
  return [kinds.length ? '类型:' + kinds.join(',') : '', calls.length ? '呼号:' + calls.join(',') : ''].filter(Boolean).join(' / ');
}

/* ---------- Verify ---------- */
function renderVerify(v) {
  const box = $('#verifyBox');
  if (!v) return;
  if (v.running) {
    box.className = 'verify-box running';
    box.innerHTML = '⏳ 独立参照实现正在同一种子下重放全部链路与规则，约需 15–30 秒…';
    return;
  }
  box.className = 'verify-box ' + (v.ok ? 'ok' : 'bad');
  box.innerHTML = `
    <h2>${v.ok ? '✅ 集合完全一致' : '❌ 存在不一致'}</h2>
    <p>${esc(v.summary || '')}</p>
    <div class="kv">
      <span>客户端</span><b>${v.clients}</b>
      <span>原始发射</span><b>${v.originals}</b>
      <span>汇聚报文</span><b>${v.hub_messages}</b>
      <span>中继转发</span><b>${v.relay_forwards}</b>
      <span>完成时间</span><b>${fmtTime(v.finished_at)}</b>
    </div>
    ${(v.mismatches || []).length ? '<pre class="raw">' + esc(v.mismatches.join('\n')) + '</pre>' : ''}
    <p class="hint">命令行可重复运行：<code>go test ./internal/reference -run TestParityFixedSeedAcceptance -v</code></p>
  `;
}

/* ---------- Boot ---------- */
loadOverview();
loadMap().then(loadMessages).catch(console.error);
loadHeard();
setInterval(loadOverview, 3000);
setInterval(() => {
  loadMessages().catch(() => {});
  if ($$('.tab-panel').find(p => p.dataset.panel === 'messages')?.classList.contains('active')) loadChats().catch(() => {});
}, 6000);
