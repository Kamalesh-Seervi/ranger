// Shared client state, SSE transport, and derived helpers.

const KINDS = ['wifi-ap', 'wifi-client', 'ble', 'service'];

export const KIND_COLORS = {
  'wifi-ap': '#35e0a1',
  'wifi-client': '#ffb454',
  'ble': '#4ea8ff',
  'service': '#c792ea',
};

export const KIND_LABELS = {
  'wifi-ap': 'AP',
  'wifi-client': 'Client',
  'ble': 'BLE',
  'service': 'Service',
};

export const BANDS = [
  { id: '2.4GHz', min: 2400, max: 2500 },
  { id: '5GHz', min: 5150, max: 5900 },
  { id: '6GHz', min: 5925, max: 7125 },
];

export const CATEGORY_LABELS = {
  'router': 'Router',
  'phone': 'Phone',
  'computer': 'Computer',
  'tv': 'TV',
  'speaker': 'Speaker',
  'headphones': 'Headphones',
  'printer': 'Printer',
  'wearable': 'Wearable',
  'tracker': 'Tracker',
  'camera': 'Camera',
  'smart-home': 'Smart home',
  'console': 'Console',
  'beacon': 'Beacon',
  'peripheral': 'Peripheral',
  'vehicle': 'Vehicle',
  'unknown': '',
};

export const store = {
  devices: new Map(),
  scanners: [],
  insight: null,
  csi: null,
  selectedId: null,
  filter: '',
  kindFilter: 'all',
  categoryFilter: 'all',
  band: '2.4GHz',
  exponent: 3.0,
  connected: false,
  sort: { key: 'rssi', dir: -1 },
};

const listeners = new Set();

export function subscribe(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

let notifyQueued = false;
function notify() {
  if (notifyQueued) return;
  notifyQueued = true;
  queueMicrotask(() => {
    notifyQueued = false;
    for (const fn of listeners) fn();
  });
}

export function update(mutate) {
  mutate(store);
  notify();
}

/** Connects to the event stream, retrying with backoff if the server restarts. */
export function connect() {
  let retry = 1000;
  let source = null;

  const open = () => {
    source = new EventSource('/api/stream');

    source.addEventListener('open', () => {
      retry = 1000;
      update((s) => { s.connected = true; });
    });

    source.addEventListener('snapshot', (e) => {
      const payload = JSON.parse(e.data);
      update((s) => {
        s.devices = new Map((payload.devices || []).map((d) => [d.id, d]));
        s.scanners = payload.scanners || [];
        s.insight = payload.insight || null;
        s.csi = payload.csi || null;
        s.connected = true;
      });
    });

    source.addEventListener('insight', (e) => {
      const { data } = JSON.parse(e.data);
      if (data) update((s) => { s.insight = data; });
    });

    source.addEventListener('csi', (e) => {
      const { data } = JSON.parse(e.data);
      if (data) update((s) => { s.csi = data; });
    });

    source.addEventListener('upsert', (e) => {
      const { device } = JSON.parse(e.data);
      if (device) update((s) => { s.devices.set(device.id, device); });
    });

    source.addEventListener('expire', (e) => {
      const { id } = JSON.parse(e.data);
      if (!id) return;
      update((s) => {
        s.devices.delete(id);
        if (s.selectedId === id) s.selectedId = null;
      });
    });

    source.addEventListener('status', (e) => {
      const { data } = JSON.parse(e.data);
      if (!data) return;
      update((s) => {
        const i = s.scanners.findIndex((x) => x.name === data.name);
        if (i >= 0) s.scanners[i] = data;
        else s.scanners.push(data);
      });
    });

    source.addEventListener('error', () => {
      update((s) => { s.connected = false; });
      source.close();
      setTimeout(open, retry);
      retry = Math.min(retry * 2, 15000);
    });
  };

  open();
}

/** Reference signal in dBm at one metre, mirroring internal/geo. */
function refRSSI(kind) {
  if (kind === 'ble') return -59;
  if (kind === 'wifi-client') return -45;
  return -40;
}

/** Rough metres from RSSI via log-distance path loss. Uncalibrated. */
export function distance(device, exponent = store.exponent) {
  if (!device.hasRssi) return null;
  const n = exponent > 0 ? exponent : 3;
  const d = Math.pow(10, (refRSSI(device.kind) - device.rssi) / (10 * n));
  return Math.min(Math.max(d, 0.3), 250);
}

/** Maps RSSI to 0..1 for bars and opacity. */
export function quality(rssi) {
  return Math.min(Math.max((rssi + 95) / 65, 0), 1);
}

/** FNV-1a, used to give each device a fixed bearing on the radar so markers
 *  stay put between frames instead of jittering. */
export function hashId(id) {
  let h = 2166136261;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

export function bearing(id) {
  return ((hashId(id) % 36000) / 36000) * Math.PI * 2;
}

export function displayName(device) {
  if (device.name) return device.name;
  const meta = device.meta || {};

  // macOS withholds SSIDs without Location Services, so these entries are
  // per-channel aggregates rather than individual networks.
  if (meta.anonymous === 'true') {
    const n = Number(meta.count || 1);
    return n > 1 ? `(${n} hidden networks)` : '(hidden network)';
  }
  // Most BLE advertisements carry no name, but the vendor and Apple
  // continuity subtype together are usually enough to recognise a device.
  if (device.kind === 'ble') {
    const parts = [device.vendor, meta.apple].filter(Boolean);
    if (parts.length) return `(${parts.join(' · ')})`;
  }
  if (device.kind === 'wifi-ap') return '(hidden SSID)';
  return '(unnamed)';
}

/** Beacon payload fields worth surfacing for a selected device. */
export const READING_KEYS = [
  'temperature', 'humidity', 'pressure', 'battery',
  'url', 'uuid', 'major', 'minor', 'namespace', 'instance',
  'movements', 'uptime', 'advertisements', 'power',
];

/** Extracts decoded beacon readings from a device's metadata. */
export function readingsOf(device) {
  const meta = device.meta || {};
  if (!meta.payload) return null;
  const fields = [];
  for (const key of READING_KEYS) {
    if (meta[key]) fields.push([key, meta[key]]);
  }
  return fields.length ? { format: meta.payload, fields } : null;
}

/** Devices passing the current text, kind, and category filters. */
export function visibleDevices() {
  const q = store.filter.trim().toLowerCase();
  const out = [];
  for (const d of store.devices.values()) {
    if (store.kindFilter !== 'all' && d.kind !== store.kindFilter) continue;
    if (store.categoryFilter !== 'all' && (d.category || 'unknown') !== store.categoryFilter) continue;
    if (q) {
      const hay = `${d.name} ${d.addr} ${d.vendor} ${d.security} ${d.category || ''}`.toLowerCase();
      if (!hay.includes(q)) continue;
    }
    out.push(d);
  }
  return out;
}

/** Categories actually present, so the filter only offers useful options. */
export function presentCategories() {
  const seen = new Set();
  for (const d of store.devices.values()) {
    if (d.category && d.category !== 'unknown') seen.add(d.category);
  }
  return [...seen].sort();
}

/** The label shown on a device's type badge. */
export function categoryLabel(device) {
  const label = CATEGORY_LABELS[device.category];
  return label || KIND_LABELS[device.kind] || device.kind;
}

export function countsByKind() {
  const counts = Object.fromEntries(KINDS.map((k) => [k, 0]));
  for (const d of store.devices.values()) {
    if (counts[d.kind] !== undefined) counts[d.kind]++;
  }
  return counts;
}

// --- Plain-language helpers -------------------------------------------------
// These translate the technical numbers into words a non-technical person can
// read at a glance. The raw value stays available in a tooltip for anyone who
// wants it.

/** signalWords turns RSSI into Strong / Good / Fair / Weak. */
export function signalWords(device) {
  if (!device.hasRssi) return null;
  const q = quality(device.rssi);
  if (q >= 0.6) return { label: 'Strong', cls: 'sig-strong', q };
  if (q >= 0.4) return { label: 'Good', cls: 'sig-good', q };
  if (q >= 0.22) return { label: 'Fair', cls: 'sig-fair', q };
  return { label: 'Weak', cls: 'sig-weak', q };
}

/** distanceWords turns the rough metre estimate into a room-scale phrase. */
export function distanceWords(device) {
  const d = distance(device);
  if (d === null) return null;
  if (d < 2) return 'Right here';
  if (d < 5) return 'This room';
  if (d < 12) return 'Nearby';
  if (d < 30) return 'Same floor';
  return 'Far away';
}

/** securityWords describes a Wi-Fi network's safety in words, with a severity
 *  class so open networks can be flagged red. */
export function securityWords(device) {
  if (device.kind !== 'wifi-ap') return null;
  const s = (device.security || '').toUpperCase();
  if (!s || s === 'OPEN' || s === 'NONE') {
    return { label: 'Open — anyone can join', short: 'Open', cls: 'sec-open' };
  }
  if (s.includes('WEP') || s.startsWith('WPA-') || s === 'WPA') {
    return { label: 'Weak (outdated)', short: 'Weak', cls: 'sec-weak' };
  }
  if (s.includes('WPA3')) return { label: 'Secure (WPA3)', short: 'Secure', cls: 'sec-good' };
  if (s.includes('WPA2')) return { label: 'Secure (WPA2)', short: 'Secure', cls: 'sec-good' };
  if (s.includes('OWE')) return { label: 'Encrypted open', short: 'Encrypted', cls: 'sec-good' };
  return { label: device.security, short: 'Secure', cls: 'sec-good' };
}

/** A short article-friendly noun for a device, e.g. "a printer". */
function categoryNoun(device) {
  const label = CATEGORY_LABELS[device.category];
  if (label) return `a ${label.toLowerCase()}`;
  switch (device.kind) {
    case 'wifi-ap': return 'a Wi-Fi network';
    case 'wifi-client': return 'a Wi-Fi device';
    case 'ble': return 'a Bluetooth device';
    case 'service': return 'a network service';
    default: return 'a device';
  }
}

/** describeDevice builds a plain-English sentence explaining a device, so a
 *  non-technical person understands what they are looking at. */
export function describeDevice(device) {
  const parts = [];
  const noun = categoryNoun(device);
  const vendor = device.vendor && device.vendor !== '(randomized)' ? device.vendor : '';

  if (device.name) {
    parts.push(`${device.name} looks like ${noun}${vendor ? ` from ${vendor}` : ''}.`);
  } else if (vendor) {
    parts.push(`This looks like ${noun} from ${vendor}.`);
  } else {
    parts.push(`This looks like ${noun}.`);
  }

  const where = distanceWords(device);
  const sig = signalWords(device);
  if (where && sig) {
    parts.push(`Signal is ${sig.label.toLowerCase()} — it's ${where.toLowerCase()}.`);
  } else if (device.kind === 'service') {
    parts.push('It was found on your local network.');
  }

  const sec = securityWords(device);
  if (sec) {
    parts.push(sec.cls === 'sec-open'
      ? 'Its Wi-Fi is open — anyone can connect.'
      : `Its Wi-Fi is ${sec.label.toLowerCase()}.`);
  }

  const reading = readingsOf(device);
  if (reading) {
    const bits = reading.fields
      .filter(([k]) => ['temperature', 'humidity', 'battery'].includes(k))
      .map(([, v]) => v);
    if (bits.length) parts.push(`It is reporting ${bits.join(', ')}.`);
  }
  return parts.join(' ');
}

// Groups used by the overview strip: a handful of buckets a person recognises.
const OVERVIEW_GROUPS = [
  { id: 'wifi', label: 'Wi-Fi networks', match: (d) => d.kind === 'wifi-ap' },
  { id: 'bluetooth', label: 'Bluetooth', match: (d) => d.kind === 'ble' },
  { id: 'smart', label: 'Smart home', match: (d) => ['smart-home', 'tv', 'speaker', 'camera', 'printer'].includes(d.category) },
  { id: 'services', label: 'On the network', match: (d) => d.kind === 'service' },
];

/** overviewSummary condenses the whole scan into the few facts the header
 *  shows: where you are, what's around, and anything worth worrying about. */
export function overviewSummary() {
  const devices = [...store.devices.values()];
  const groups = OVERVIEW_GROUPS.map((g) => ({
    id: g.id,
    label: g.label,
    count: devices.filter(g.match).length,
  }));

  let closest = null;
  let closestQ = -1;
  for (const d of devices) {
    if (!d.hasRssi) continue;
    const q = quality(d.rssi);
    if (q > closestQ) { closestQ = q; closest = d; }
  }

  const openNetworks = devices.filter(
    (d) => d.kind === 'wifi-ap' && securityWords(d)?.cls === 'sec-open').length;

  const followers = (store.insight && store.insight.follows) || [];

  let sensing = null;
  if (store.csi && store.csi.active) {
    for (const s of store.csi.sensors || []) {
      const r = s.reading || {};
      if (r.breathing) { sensing = `breathing detected (${r.breathing.bpm} bpm)`; break; }
      if (r.presence) sensing = 'someone is present';
    }
  }
  if (!sensing && store.insight && store.insight.motion && store.insight.motion.detected) {
    sensing = 'movement nearby';
  }

  const place = store.insight && store.insight.active ? store.insight.name : null;

  return {
    total: devices.length,
    groups,
    closest,
    closestWhere: closest ? distanceWords(closest) : null,
    openNetworks,
    followers,
    sensing,
    place,
  };
}


export { KINDS };
