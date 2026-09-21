// Entry point: wires the store to the views and drives the render loops.

import {
  store, update, subscribe, connect,
  BANDS, KINDS, KIND_LABELS, CATEGORY_LABELS,
  countsByKind, presentCategories,
} from './store.js';
import { Radar } from './radar.js';
import { Spectrum } from './spectrum.js';
import { DeviceTable } from './table.js';
import { Finder } from './finder.js';
import { PlacePanel } from './place.js';
import { SensingPanel } from './sensing.js';
import { Overview } from './overview.js';
import { Guide } from './help.js';

// Canvases animate every frame; DOM views repaint on a slower cadence so a
// busy scan cannot thrash layout.
const DOM_REFRESH_MS = 500;

const radar = new Radar(document.getElementById('radar'));
const spectrum = new Spectrum(document.getElementById('spectrum'));
const table = new DeviceTable(document.getElementById('devices'));
const finder = new Finder(
  document.getElementById('finder'),
  document.getElementById('finder-audio'),
);
const placePanel = new PlacePanel(
  document.getElementById('place'),
  document.getElementById('place-anchors'),
);
const sensingPanel = new SensingPanel(
  document.getElementById('sensing-panel'),
  document.getElementById('sensing'),
  document.getElementById('sensing-hint'),
);
const overview = new Overview(document.getElementById('overview'));
new Guide(document.getElementById('guide-open'));

const linkDot = document.getElementById('link-dot');
const countsEl = document.getElementById('counts');
const alertsEl = document.getElementById('alerts');
const scannersEl = document.getElementById('scanners');

buildBandTabs();
buildKindTabs();
bindControls();

let domDirty = true;
subscribe(() => { domDirty = true; });

function frame(now) {
  radar.draw(now);
  spectrum.draw();
  requestAnimationFrame(frame);
}
requestAnimationFrame(frame);

setInterval(() => {
  // Repaint on a timer even when idle so "last seen" ages visibly.
  renderDom();
  domDirty = false;
}, DOM_REFRESH_MS);

function renderDom() {
  overview.render();
  table.render();
  finder.render();
  placePanel.render();
  sensingPanel.render();
  renderCounts();
  renderCategoryOptions();
  renderScanners();
  linkDot.classList.toggle('live', store.connected);
  linkDot.title = store.connected ? 'Connected' : 'Disconnected — retrying';
}

// The type filter only lists categories actually detected, so it stays short.
let categoryOptionsKey = '';
function renderCategoryOptions() {
  const categories = presentCategories();
  const key = categories.join(',');
  if (key === categoryOptionsKey) return;
  categoryOptionsKey = key;

  const select = document.getElementById('category-filter');
  const previous = store.categoryFilter;

  const options = [['all', 'All types']];
  for (const c of categories) options.push([c, CATEGORY_LABELS[c] || c]);

  select.replaceChildren(...options.map(([value, label]) => {
    const option = document.createElement('option');
    option.value = value;
    option.textContent = label;
    return option;
  }));

  if (options.some(([value]) => value === previous)) select.value = previous;
  else update((s) => { s.categoryFilter = 'all'; });
}

function renderCounts() {
  const counts = countsByKind();
  countsEl.replaceChildren(...KINDS.map((kind) => {
    const span = document.createElement('span');
    const strong = document.createElement('b');
    strong.textContent = String(counts[kind]);
    span.append(strong, ` ${KIND_LABELS[kind]}`);
    return span;
  }));
}

function renderScanners() {
  scannersEl.replaceChildren(...store.scanners.map((s) => {
    const li = document.createElement('li');

    const name = document.createElement('span');
    name.className = 'scanner-name';
    name.textContent = s.name;

    const state = document.createElement('span');
    state.className = `scanner-state state-${s.state}`;
    state.textContent = s.state;

    const detail = document.createElement('span');
    detail.className = 'scanner-detail';
    detail.textContent = s.error || s.reason || '';

    const count = document.createElement('span');
    count.className = 'scanner-count';
    count.textContent = `${s.observations ?? 0}`;

    li.append(name, state, detail, count);
    return li;
  }));

  renderAlerts();
}

// A scanner may be running yet degraded (for example Wi-Fi without the
// permission that reveals network names), so surface any hint, not just
// failures. Devices that follow you across places are shown here too, since
// that is the most important thing the dashboard can tell you.
function renderAlerts() {
  const cards = [];

  for (const alert of (store.insight && store.insight.follows) || []) {
    cards.push(followCard(alert));
  }
  for (const s of store.scanners.filter((x) => x.hint)) {
    const div = document.createElement('div');
    div.className = 'alert';
    const head = document.createElement('b');
    head.textContent = `${s.name}: ${s.reason || s.error || 'unavailable'}`;
    div.append(head, document.createTextNode(s.hint));
    cards.push(div);
  }

  alertsEl.replaceChildren(...cards);
}

function followCard(alert) {
  const div = document.createElement('div');
  div.className = 'alert danger';

  const who = alert.name || [alert.vendor, alert.category].filter(Boolean).join(' ') || 'Unknown device';
  const head = document.createElement('b');
  head.textContent = `${who} has been with you in ${alert.places.length} places`;

  const detail = document.createElement('div');
  detail.textContent = `${alert.places.join(', ')} · currently ${alert.rssi} dBm`;

  const caveat = document.createElement('div');
  caveat.className = 'alert-caveat';
  caveat.textContent =
    'Most Bluetooth devices rotate their address, so a shared address across ' +
    'places usually means a tracker separated from its owner. It can still be ' +
    'a coincidence — check before acting.';

  const select = document.createElement('button');
  select.type = 'button';
  select.className = 'tab';
  select.textContent = 'Track it';
  select.addEventListener('click', () => {
    update((s) => { s.selectedId = alert.deviceId; });
  });

  div.append(head, detail, caveat, select);
  return div;
}

function buildBandTabs() {
  const host = document.getElementById('band-tabs');
  host.replaceChildren(...BANDS.map((band) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'tab';
    btn.textContent = band.id;
    btn.setAttribute('aria-pressed', String(store.band === band.id));
    btn.addEventListener('click', () => {
      update((s) => { s.band = band.id; });
      for (const el of host.children) {
        el.setAttribute('aria-pressed', String(el.textContent === band.id));
      }
    });
    return btn;
  }));
}

function buildKindTabs() {
  const host = document.getElementById('kind-tabs');
  const options = [['all', 'All'], ...KINDS.map((k) => [k, KIND_LABELS[k]])];

  host.replaceChildren(...options.map(([value, label]) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'tab';
    btn.textContent = label;
    btn.dataset.value = value;
    btn.setAttribute('aria-pressed', String(store.kindFilter === value));
    btn.addEventListener('click', () => {
      update((s) => { s.kindFilter = value; });
      for (const el of host.children) {
        el.setAttribute('aria-pressed', String(el.dataset.value === value));
      }
    });
    return btn;
  }));
}

function bindControls() {
  const filter = document.getElementById('filter');
  filter.addEventListener('input', () => {
    update((s) => { s.filter = filter.value; });
  });

  const categoryFilter = document.getElementById('category-filter');
  categoryFilter.addEventListener('change', () => {
    update((s) => { s.categoryFilter = categoryFilter.value; });
  });

  const exponent = document.getElementById('exponent');
  const exponentOut = document.getElementById('exponent-out');
  exponent.addEventListener('input', () => {
    const value = Number(exponent.value);
    exponentOut.textContent = value.toFixed(1);
    update((s) => { s.exponent = value; });
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') update((s) => { s.selectedId = null; });
    if (e.key === '/' && document.activeElement !== filter) {
      e.preventDefault();
      filter.focus();
    }
  });
}

connect();
renderDom();
