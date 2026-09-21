// Spectrum view: access points drawn as channel-width humps over frequency,
// making co-channel overlap visible.

import { store, update, BANDS, KIND_COLORS, displayName, visibleDevices } from './store.js';

const RSSI_TOP = -20;
const RSSI_BOTTOM = -100;
const PAD = { left: 38, right: 12, top: 12, bottom: 24 };

export class Spectrum {
  constructor(canvas) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.shapes = [];
    this.dpr = 1;

    this.resize();
    new ResizeObserver(() => this.resize()).observe(canvas);
    canvas.addEventListener('click', (e) => this.onClick(e));
  }

  resize() {
    const rect = this.canvas.getBoundingClientRect();
    if (rect.width === 0) return;
    this.dpr = window.devicePixelRatio || 1;
    this.canvas.width = Math.round(rect.width * this.dpr);
    this.canvas.height = Math.round(rect.height * this.dpr);
    this.w = rect.width;
    this.h = rect.height;
  }

  onClick(e) {
    const rect = this.canvas.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    let hit = null;
    for (const s of this.shapes) {
      if (x >= s.x0 && x <= s.x1 && y >= s.peakY - 6) { hit = s; break; }
    }
    update((st) => { st.selectedId = hit ? hit.id : null; });
  }

  band() {
    return BANDS.find((b) => b.id === store.band) || BANDS[0];
  }

  draw() {
    const { ctx, w, h } = this;
    if (!w || !h) return;

    ctx.save();
    ctx.scale(this.dpr, this.dpr);
    ctx.clearRect(0, 0, w, h);

    const band = this.band();
    const plot = {
      x: PAD.left,
      y: PAD.top,
      w: w - PAD.left - PAD.right,
      h: h - PAD.top - PAD.bottom,
    };

    const freqToX = (f) => plot.x + ((f - band.min) / (band.max - band.min)) * plot.w;
    const rssiToY = (r) => plot.y + ((RSSI_TOP - r) / (RSSI_TOP - RSSI_BOTTOM)) * plot.h;

    this.drawAxes(ctx, plot, band, freqToX, rssiToY);
    this.drawNetworks(ctx, plot, band, freqToX, rssiToY);

    ctx.restore();
  }

  drawAxes(ctx, plot, band, freqToX, rssiToY) {
    ctx.strokeStyle = '#1a2431';
    ctx.fillStyle = '#5f7186';
    ctx.font = '10px ui-monospace, monospace';
    ctx.lineWidth = 1;

    ctx.textAlign = 'right';
    for (let r = RSSI_TOP; r >= RSSI_BOTTOM; r -= 20) {
      const y = rssiToY(r);
      ctx.beginPath();
      ctx.moveTo(plot.x, y);
      ctx.lineTo(plot.x + plot.w, y);
      ctx.stroke();
      ctx.fillText(`${r}`, plot.x - 6, y + 3);
    }

    ctx.textAlign = 'center';
    const step = band.max - band.min > 400 ? 200 : 20;
    for (let f = Math.ceil(band.min / step) * step; f <= band.max; f += step) {
      const x = freqToX(f);
      ctx.strokeStyle = '#141d28';
      ctx.beginPath();
      ctx.moveTo(x, plot.y);
      ctx.lineTo(x, plot.y + plot.h);
      ctx.stroke();
      ctx.fillText(`${f}`, x, plot.y + plot.h + 14);
    }
  }

  drawNetworks(ctx, plot, band, freqToX, rssiToY) {
    this.shapes = [];

    const networks = visibleDevices()
      .filter((d) => d.frequency >= band.min && d.frequency <= band.max && d.hasRssi)
      .sort((a, b) => a.rssi - b.rssi);

    for (const d of networks) {
      const width = d.channelWidth > 0 ? d.channelWidth : 20;
      const x0 = freqToX(d.frequency - width / 2);
      const x1 = freqToX(d.frequency + width / 2);
      const xc = freqToX(d.frequency);
      const peakY = rssiToY(d.rssi);
      const baseY = plot.y + plot.h;
      const color = KIND_COLORS[d.kind] || '#8aa';
      const selected = store.selectedId === d.id;

      ctx.beginPath();
      ctx.moveTo(x0, baseY);
      ctx.bezierCurveTo(x0 + (xc - x0) * 0.45, baseY, x0 + (xc - x0) * 0.55, peakY, xc, peakY);
      ctx.bezierCurveTo(x1 - (x1 - xc) * 0.55, peakY, x1 - (x1 - xc) * 0.45, baseY, x1, baseY);
      ctx.closePath();

      ctx.fillStyle = withAlpha(color, selected ? 0.32 : 0.12);
      ctx.fill();
      ctx.strokeStyle = withAlpha(color, selected ? 1 : 0.7);
      ctx.lineWidth = selected ? 2 : 1;
      ctx.stroke();

      this.shapes.push({ id: d.id, x0, x1, peakY });

      const label = `${displayName(d)} · ${d.channel || '?'}`;
      ctx.fillStyle = selected ? '#ffffff' : '#8aa0b6';
      ctx.font = selected ? '600 11px -apple-system, system-ui, sans-serif'
                          : '10px -apple-system, system-ui, sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText(clip(label, 22), xc, peakY - 5);
    }

    if (networks.length === 0) {
      ctx.fillStyle = '#4a5b70';
      ctx.font = '12px -apple-system, system-ui, sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('No networks in this band', plot.x + plot.w / 2, plot.y + plot.h / 2);
    }
  }
}

function clip(s, n) {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}

function withAlpha(hex, alpha) {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}
