// Through-wall sensing panel: presence, motion, and breathing derived from an
// ESP32's Channel State Information.

import { store } from './store.js';

export class SensingPanel {
  constructor(panel, root, hint) {
    this.panel = panel;
    this.root = root;
    this.hint = hint;
  }

  render() {
    const csi = store.csi;

    // The panel only appears once CSI ingest is enabled, so it never clutters
    // the dashboard for the common laptop-only setup.
    if (!csi) {
      this.panel.hidden = true;
      return;
    }
    this.panel.hidden = false;

    if (!csi.active) {
      this.hint.textContent = '';
      this.root.replaceChildren(note(csi.reason || 'CSI ingest is not running.'));
      return;
    }

    this.hint.textContent = csi.listen ? `listening on ${csi.listen}` : '';

    const sensors = csi.sensors || [];
    if (sensors.length === 0) {
      this.root.replaceChildren(note(csi.reason || 'No CSI sensors reporting yet.'));
      return;
    }

    this.root.replaceChildren(...sensors.map((s) => sensorCard(s)));
  }
}

function sensorCard(sensor) {
  const card = document.createElement('div');
  card.className = 'sensor';

  const head = document.createElement('div');
  head.className = 'sensor-head';
  const id = document.createElement('span');
  id.className = 'sensor-id';
  id.textContent = sensor.id;
  const rate = document.createElement('span');
  rate.className = 'sensor-rate';
  rate.textContent = `${sensor.fps ?? 0}/s · ${sensor.rssi} dBm · ch ${sensor.channel}`;
  head.append(id, rate);
  card.append(head);

  const reading = sensor.reading || {};

  const status = document.createElement('div');
  status.className = 'sensor-status';
  if (reading.calibrating) {
    status.append(pill('calibrating', 'calm'));
  } else if (reading.presence) {
    status.append(pill('presence', 'alert'));
  } else {
    status.append(pill('empty', 'calm'));
  }
  if (reading.breathing) {
    const bpm = pill(`breathing ${reading.breathing.bpm} bpm`, 'breath');
    bpm.title = `confidence ${Math.round((reading.breathing.confidence || 0) * 100)}%`;
    status.append(bpm);
  }
  card.append(status);

  // Motion bar.
  const bar = document.createElement('div');
  bar.className = 'sensor-bar';
  const fill = document.createElement('div');
  fill.className = 'sensor-fill';
  fill.style.width = `${Math.min(100, (reading.motionLevel || 0) * 100).toFixed(0)}%`;
  bar.append(fill);
  card.append(bar);

  card.append(subcarrierBars(reading.amplitude || []));

  if (sensor.loss > 0.02) {
    const loss = document.createElement('div');
    loss.className = 'sensor-loss';
    loss.textContent = `${Math.round(sensor.loss * 100)}% packet loss`;
    card.append(loss);
  }
  return card;
}

// subcarrierBars draws the live channel response as a compact bar strip, the
// closest thing to "seeing" the radio channel the ESP32 measures.
function subcarrierBars(amplitude) {
  const wrap = document.createElement('div');
  wrap.className = 'subcarriers';
  if (!amplitude.length) return wrap;

  const max = Math.max(1, ...amplitude);
  for (const a of amplitude) {
    const bar = document.createElement('span');
    bar.className = 'subcarrier';
    bar.style.height = `${Math.max(2, (a / max) * 100).toFixed(0)}%`;
    wrap.append(bar);
  }
  return wrap;
}

function pill(text, kind) {
  const span = document.createElement('span');
  span.className = `sensor-pill ${kind}`;
  span.textContent = text;
  return span;
}

function note(text) {
  const p = document.createElement('p');
  p.className = 'empty';
  p.textContent = text;
  return p;
}
