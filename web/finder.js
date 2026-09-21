// Hot-cold finder: focus one device and show whether you are closing in.

import { store, distance, quality, displayName, readingsOf, describeDevice, distanceWords } from './store.js';

// Number of trailing samples compared to decide the trend.
const TREND_WINDOW = 6;
const TREND_THRESHOLD_DB = 2;

export class Finder {
  constructor(root, toggle) {
    this.root = root;
    this.audioEnabled = false;
    this.audioCtx = null;
    this.osc = null;
    this.gain = null;

    toggle.addEventListener('click', () => {
      this.audioEnabled = !this.audioEnabled;
      toggle.setAttribute('aria-pressed', String(this.audioEnabled));
      toggle.textContent = this.audioEnabled ? 'Audio on' : 'Audio off';
      if (!this.audioEnabled) this.stopTone();
    });
  }

  render() {
    const device = store.selectedId ? store.devices.get(store.selectedId) : null;

    if (!device) {
      this.root.replaceChildren(message('Select a device on the radar, spectrum, or table to track it.'));
      this.stopTone();
      return;
    }
    if (!device.hasRssi) {
      this.root.replaceChildren(
        title(displayName(device), device.addr),
        message('This source reports no signal strength, so proximity cannot be estimated.'),
        ...readingNodes(device),
      );
      this.stopTone();
      return;
    }

    const q = quality(device.rssi);
    const metres = distance(device);
    const trend = trendOf(device.history);

    const name = title(displayName(device), `${device.addr || ''} ${device.vendor ? `· ${device.vendor}` : ''}`.trim());

    const row = document.createElement('div');
    row.className = 'finder-row';

    const rssi = document.createElement('span');
    rssi.className = 'finder-rssi';
    rssi.textContent = String(device.rssi);
    rssi.style.color = heatColor(q);

    const unit = document.createElement('span');
    unit.className = 'finder-unit';
    unit.textContent = 'dBm';

    const arrow = document.createElement('span');
    arrow.className = `finder-trend trend-${trend.kind}`;
    arrow.textContent = trend.symbol;
    arrow.title = trend.label;

    row.append(rssi, unit, arrow);

    const bar = document.createElement('div');
    bar.className = 'finder-bar';
    const fill = document.createElement('div');
    fill.className = 'finder-fill';
    fill.style.width = `${(q * 100).toFixed(1)}%`;
    fill.style.background = heatColor(q);
    bar.append(fill);

    const dist = document.createElement('div');
    dist.className = 'finder-dist';
    const where = distanceWords(device);
    dist.textContent = `≈ ${metres.toFixed(1)} m${where ? ` · ${where.toLowerCase()}` : ''} · ${trend.label}`;

    const summary = document.createElement('p');
    summary.className = 'finder-summary';
    summary.textContent = describeDevice(device);

    this.root.replaceChildren(name, summary, row, bar, dist, ...readingNodes(device));
    this.updateTone(q);
  }

  /** Pitch rises as the signal strengthens, so you can search without looking. */
  updateTone(q) {
    if (!this.audioEnabled) return;
    if (!this.audioCtx) {
      const Ctor = window.AudioContext || window.webkitAudioContext;
      if (!Ctor) return;
      this.audioCtx = new Ctor();
    }
    if (this.audioCtx.state === 'suspended') this.audioCtx.resume();

    if (!this.osc) {
      this.osc = this.audioCtx.createOscillator();
      this.gain = this.audioCtx.createGain();
      this.osc.type = 'sine';
      this.gain.gain.value = 0.04;
      this.osc.connect(this.gain).connect(this.audioCtx.destination);
      this.osc.start();
    }
    const freq = 220 + q * 880;
    this.osc.frequency.setTargetAtTime(freq, this.audioCtx.currentTime, 0.08);
  }

  stopTone() {
    if (!this.osc) return;
    this.osc.stop();
    this.osc.disconnect();
    this.gain.disconnect();
    this.osc = null;
    this.gain = null;
  }
}

// readingNodes renders sensor values decoded from the device's advertisement,
// turning an anonymous blip into an actual measurement.
function readingNodes(device) {
  const reading = readingsOf(device);
  if (!reading) return [];

  const wrap = document.createElement('div');
  wrap.className = 'readings';

  const head = document.createElement('div');
  head.className = 'readings-head';
  head.textContent = reading.format;
  wrap.append(head);

  const list = document.createElement('dl');
  for (const [key, value] of reading.fields) {
    const term = document.createElement('dt');
    term.textContent = key;
    const def = document.createElement('dd');
    def.textContent = value;
    list.append(term, def);
  }
  wrap.append(list);

  return [wrap];
}

function title(nameText, subText) {
  const wrap = document.createDocumentFragment();
  const h = document.createElement('div');
  h.className = 'finder-name';
  h.textContent = nameText;
  const sub = document.createElement('div');
  sub.className = 'finder-sub';
  sub.textContent = subText || '';
  wrap.append(h, sub);
  return wrap;
}

function message(textValue) {
  const p = document.createElement('p');
  p.className = 'empty';
  p.textContent = textValue;
  return p;
}

function trendOf(history) {
  const samples = history || [];
  if (samples.length < 3) {
    return { kind: 'flat', symbol: '•', label: 'gathering samples' };
  }
  const recent = samples.slice(-TREND_WINDOW);
  const half = Math.max(1, Math.floor(recent.length / 2));
  const older = mean(recent.slice(0, half).map((s) => s.r));
  const newer = mean(recent.slice(-half).map((s) => s.r));
  const delta = newer - older;

  if (delta > TREND_THRESHOLD_DB) return { kind: 'closer', symbol: '▲', label: 'getting closer' };
  if (delta < -TREND_THRESHOLD_DB) return { kind: 'away', symbol: '▼', label: 'moving away' };
  return { kind: 'flat', symbol: '■', label: 'holding steady' };
}

function mean(values) {
  return values.reduce((a, b) => a + b, 0) / values.length;
}

function heatColor(q) {
  if (q > 0.66) return '#35e0a1';
  if (q > 0.33) return '#ffb454';
  return '#ff6b6b';
}
