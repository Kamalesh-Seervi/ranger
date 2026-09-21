// Radar view: devices plotted by estimated distance, with a sweeping beam.

import { store, update, distance, bearing, quality, KIND_COLORS, displayName, visibleDevices } from './store.js';

const MAX_RANGE_M = 60;
const SWEEP_PERIOD_MS = 4000;

export class Radar {
  constructor(canvas) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.points = [];
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
    let best = null;
    let bestDist = 18;
    for (const p of this.points) {
      const d = Math.hypot(p.x - x, p.y - y);
      if (d < bestDist) { bestDist = d; best = p; }
    }
    update((s) => { s.selectedId = best ? best.id : null; });
  }

  /** Log scale keeps nearby devices readable without pushing distant ones off. */
  radiusFor(metres, maxRadius) {
    const t = Math.log1p(Math.min(metres, MAX_RANGE_M)) / Math.log1p(MAX_RANGE_M);
    return t * maxRadius;
  }

  draw(now) {
    const { ctx, w, h } = this;
    if (!w || !h) return;

    ctx.save();
    ctx.scale(this.dpr, this.dpr);
    ctx.clearRect(0, 0, w, h);

    const cx = w / 2;
    const cy = h / 2;
    const maxRadius = Math.min(w, h) / 2 - 24;

    this.drawGrid(ctx, cx, cy, maxRadius);
    this.drawSweep(ctx, cx, cy, maxRadius, now);
    this.drawDevices(ctx, cx, cy, maxRadius, now);

    ctx.restore();
  }

  drawGrid(ctx, cx, cy, maxRadius) {
    ctx.strokeStyle = '#1e2a3a';
    ctx.lineWidth = 1;
    ctx.fillStyle = '#4a5b70';
    ctx.font = '10px ui-monospace, monospace';
    ctx.textAlign = 'left';

    for (const metres of [1, 5, 15, 60]) {
      const r = this.radiusFor(metres, maxRadius);
      ctx.beginPath();
      ctx.arc(cx, cy, r, 0, Math.PI * 2);
      ctx.stroke();
      ctx.fillText(`${metres}m`, cx + 4, cy - r - 3);
    }

    ctx.beginPath();
    for (let i = 0; i < 8; i++) {
      const a = (i / 8) * Math.PI * 2;
      ctx.moveTo(cx, cy);
      ctx.lineTo(cx + Math.cos(a) * maxRadius, cy + Math.sin(a) * maxRadius);
    }
    ctx.stroke();

    ctx.fillStyle = '#35e0a1';
    ctx.beginPath();
    ctx.arc(cx, cy, 3, 0, Math.PI * 2);
    ctx.fill();
  }

  drawSweep(ctx, cx, cy, maxRadius, now) {
    const angle = ((now % SWEEP_PERIOD_MS) / SWEEP_PERIOD_MS) * Math.PI * 2;
    this.sweepAngle = angle;

    const gradient = ctx.createConicGradient
      ? ctx.createConicGradient(angle - 0.9, cx, cy)
      : null;

    if (gradient) {
      gradient.addColorStop(0, 'rgba(53, 224, 161, 0)');
      gradient.addColorStop(0.14, 'rgba(53, 224, 161, 0.20)');
      gradient.addColorStop(0.145, 'rgba(53, 224, 161, 0)');
      ctx.fillStyle = gradient;
      ctx.beginPath();
      ctx.arc(cx, cy, maxRadius, 0, Math.PI * 2);
      ctx.fill();
    }

    ctx.strokeStyle = 'rgba(53, 224, 161, 0.55)';
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.moveTo(cx, cy);
    ctx.lineTo(cx + Math.cos(angle) * maxRadius, cy + Math.sin(angle) * maxRadius);
    ctx.stroke();
  }

  drawDevices(ctx, cx, cy, maxRadius, now) {
    this.points = [];
    const devices = visibleDevices();

    for (const d of devices) {
      const metres = distance(d);
      const angle = bearing(d.id);
      // Devices with no signal reading (mDNS) sit on the outer ring.
      const r = metres === null ? maxRadius * 0.94 : this.radiusFor(metres, maxRadius);
      const x = cx + Math.cos(angle) * r;
      const y = cy + Math.sin(angle) * r;
      this.points.push({ id: d.id, x, y });

      const color = KIND_COLORS[d.kind] || '#8aa';
      const selected = store.selectedId === d.id;

      // Brighten briefly as the beam passes, so the eye catches new contacts.
      const delta = Math.abs(normalizeAngle(angle - this.sweepAngle));
      const ping = Math.max(0, 1 - delta / 0.5);

      const strength = d.hasRssi ? quality(d.rssi) : 0.35;
      const size = 3 + strength * 3.5 + ping * 2.5;

      if (ping > 0.02) {
        ctx.fillStyle = withAlpha(color, 0.18 * ping);
        ctx.beginPath();
        ctx.arc(x, y, size + 10 * ping, 0, Math.PI * 2);
        ctx.fill();
      }

      ctx.fillStyle = withAlpha(color, 0.45 + strength * 0.55);
      ctx.beginPath();
      if (d.kind === 'wifi-ap') {
        ctx.arc(x, y, size, 0, Math.PI * 2);
      } else if (d.kind === 'ble') {
        drawDiamond(ctx, x, y, size + 0.5);
      } else if (d.kind === 'service') {
        ctx.rect(x - size, y - size, size * 2, size * 2);
      } else {
        drawTriangle(ctx, x, y, size + 1);
      }
      ctx.fill();

      if (selected) {
        ctx.strokeStyle = '#ffffff';
        ctx.lineWidth = 1.5;
        ctx.beginPath();
        ctx.arc(x, y, size + 6, 0, Math.PI * 2);
        ctx.stroke();

        ctx.fillStyle = '#d6e0ea';
        ctx.font = '11px -apple-system, system-ui, sans-serif';
        ctx.textAlign = 'center';
        ctx.fillText(displayName(d), x, y - size - 11);
      }
    }
  }
}

function normalizeAngle(a) {
  while (a > Math.PI) a -= Math.PI * 2;
  while (a < -Math.PI) a += Math.PI * 2;
  return a;
}

function withAlpha(hex, alpha) {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

function drawDiamond(ctx, x, y, s) {
  ctx.moveTo(x, y - s);
  ctx.lineTo(x + s, y);
  ctx.lineTo(x, y + s);
  ctx.lineTo(x - s, y);
  ctx.closePath();
}

function drawTriangle(ctx, x, y, s) {
  ctx.moveTo(x, y - s);
  ctx.lineTo(x + s * 0.87, y + s * 0.5);
  ctx.lineTo(x - s * 0.87, y + s * 0.5);
  ctx.closePath();
}
