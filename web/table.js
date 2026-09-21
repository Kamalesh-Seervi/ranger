// Device table with per-row RSSI sparklines.
//
// SSIDs and BLE local names are attacker-controlled strings straight off the
// air, so every device-derived value is written with textContent. Never switch
// this file to innerHTML.

import {
  store, update, distance, quality, displayName,
  visibleDevices, categoryLabel, signalWords, distanceWords, securityWords,
} from './store.js';
import { deviceIcon } from './icons.js';

const SVG_NS = 'http://www.w3.org/2000/svg';
const STALE_AFTER_MS = 20000;

export class DeviceTable {
  constructor(table) {
    this.tbody = table.querySelector('tbody');
    this.head = table.querySelector('thead');

    this.head.addEventListener('click', (e) => {
      const th = e.target.closest('th[data-sort]');
      if (!th) return;
      const key = th.dataset.sort;
      update((s) => {
        if (s.sort.key === key) s.sort.dir *= -1;
        else s.sort = { key, dir: key === 'name' || key === 'vendor' ? 1 : -1 };
      });
    });

    this.tbody.addEventListener('click', (e) => {
      const tr = e.target.closest('tr[data-id]');
      if (!tr) return;
      update((s) => {
        s.selectedId = s.selectedId === tr.dataset.id ? null : tr.dataset.id;
      });
    });
  }

  render() {
    const rows = visibleDevices().sort(comparator(store.sort));
    const now = Date.now();

    this.tbody.replaceChildren(...rows.map((d) => this.row(d, now)));

    for (const th of this.head.querySelectorAll('th[data-sort]')) {
      const active = th.dataset.sort === store.sort.key;
      th.classList.toggle('sorted', active);
      th.setAttribute('aria-sort', active ? (store.sort.dir > 0 ? 'ascending' : 'descending') : 'none');
    }
  }

  row(d, now) {
    const tr = document.createElement('tr');
    tr.dataset.id = d.id;
    if (store.selectedId === d.id) tr.classList.add('selected');

    const age = now - new Date(d.lastSeen).getTime();
    if (age > STALE_AFTER_MS) tr.classList.add('stale');
    if (now - new Date(d.firstSeen).getTime() < 15000) tr.classList.add('fresh');

    const badge = document.createElement('span');
    badge.className = `badge ${d.kind}`;
    const ic = deviceIcon(d);
    ic.classList.add('badge-icon');
    badge.append(ic, document.createTextNode(categoryLabel(d)));
    badge.title = describeClassification(d);
    if (d.category && d.category !== 'unknown' && d.categoryConfidence < 0.5) {
      badge.classList.add('tentative');
    }
    tr.append(cell(badge));

    const name = document.createElement('span');
    name.textContent = displayName(d);
    if (!d.name) name.classList.add('anon');
    tr.append(cell(name, 'name-cell'));

    tr.append(text(d.addr || '—', 'mono'));
    tr.append(text(d.vendor || '—'));
    tr.append(text(d.channel ? String(d.channel) : '—', 'mono'));
    tr.append(cell(securityChip(d)));
    tr.append(cell(signalChip(d)));
    tr.append(cell(sparkline(d.history)));
    tr.append(cell(distanceChip(d)));
    tr.append(text(formatAge(age), 'mono'));

    return tr;
  }
}

// signalChip, distanceChip and securityChip render plain words with the exact
// value in a tooltip, so the table reads easily without losing precision.
function signalChip(d) {
  const sig = signalWords(d);
  if (!sig) return dash();
  const span = document.createElement('span');
  span.className = `chip ${sig.cls}`;
  span.textContent = sig.label;
  span.title = `${d.rssi} dBm`;
  return span;
}

function distanceChip(d) {
  const words = distanceWords(d);
  if (!words) return dash();
  const span = document.createElement('span');
  span.className = 'chip-plain';
  span.textContent = words;
  const m = distance(d);
  if (m !== null) span.title = `about ${m.toFixed(1)} m (rough)`;
  return span;
}

function securityChip(d) {
  const sec = securityWords(d);
  if (!sec) return dash();
  const span = document.createElement('span');
  span.className = `chip ${sec.cls}`;
  span.textContent = sec.short;
  span.title = d.security || sec.label;
  return span;
}

function dash() {
  const span = document.createElement('span');
  span.className = 'mono';
  span.textContent = '—';
  return span;
}

function cell(node, className) {
  const td = document.createElement('td');
  if (className) td.className = className;
  td.append(node);
  return td;
}

function text(value, className) {
  const td = document.createElement('td');
  if (className) td.className = className;
  td.textContent = value;
  return td;
}

/** Builds a fixed-size RSSI trace. Only numbers reach the DOM here. */
function sparkline(history) {
  const w = 74;
  const h = 18;
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('width', String(w));
  svg.setAttribute('height', String(h));
  svg.setAttribute('viewBox', `0 0 ${w} ${h}`);
  svg.classList.add('spark');

  const samples = history || [];
  if (samples.length < 2) return svg;

  const recent = samples.slice(-40);
  const points = recent.map((s, i) => {
    const x = (i / (recent.length - 1)) * w;
    const y = h - quality(s.r) * (h - 2) - 1;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(' ');

  const line = document.createElementNS(SVG_NS, 'polyline');
  line.setAttribute('points', points);
  line.setAttribute('fill', 'none');
  line.setAttribute('stroke', 'currentColor');
  line.setAttribute('stroke-width', '1.2');
  line.setAttribute('stroke-linejoin', 'round');

  const last = recent[recent.length - 1];
  svg.style.color = signalColor(last.r);
  svg.append(line);
  return svg;
}

// Spells out why a device was classified the way it was, so a wrong guess is
// debuggable rather than mysterious.
function describeClassification(d) {
  const lines = [];
  if (d.category && d.category !== 'unknown') {
    lines.push(`${d.category} · ${Math.round((d.categoryConfidence || 0) * 100)}% confident`);
  } else {
    lines.push('type unknown');
  }
  if (d.mobility && d.mobility !== 'unknown') lines.push(`movement: ${d.mobility}`);
  if (d.evidence && d.evidence.length) lines.push('', ...d.evidence);
  return lines.join('\n');
}

function signalColor(rssi) {  const q = quality(rssi);
  if (q > 0.6) return '#35e0a1';
  if (q > 0.35) return '#ffb454';
  return '#ff6b6b';
}

function formatAge(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  return `${Math.floor(m / 60)}h`;
}

function comparator({ key, dir }) {
  return (a, b) => {
    const va = sortValue(a, key);
    const vb = sortValue(b, key);
    if (va === vb) return displayName(a).localeCompare(displayName(b));
    if (typeof va === 'string' || typeof vb === 'string') {
      return String(va).localeCompare(String(vb)) * dir;
    }
    return (va - vb) * dir;
  };
}

function sortValue(d, key) {
  switch (key) {
    case 'name': return displayName(d).toLowerCase();
    case 'addr': return (d.addr || '').toLowerCase();
    case 'vendor': return (d.vendor || '').toLowerCase();
    case 'security': return (d.security || '').toLowerCase();
    case 'kind': return d.kind;
    case 'channel': return d.channel || 0;
    // Devices with no reading sort last rather than pretending to be at 0 dBm.
    case 'rssi': return d.hasRssi ? d.rssi : -999;
    case 'distance': return distance(d) ?? 9999;
    case 'lastSeen': return new Date(d.lastSeen).getTime();
    default: return 0;
  }
}
