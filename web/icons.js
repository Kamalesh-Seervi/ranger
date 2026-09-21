// Small inline SVG icons, drawn from primitives so they render identically
// everywhere and need no font or image. Every icon inherits currentColor.

const SVG_NS = 'http://www.w3.org/2000/svg';

function svg(children, { size = 18, fill = false } = {}) {
  const el = document.createElementNS(SVG_NS, 'svg');
  el.setAttribute('viewBox', '0 0 20 20');
  el.setAttribute('width', String(size));
  el.setAttribute('height', String(size));
  el.setAttribute('fill', 'none');
  el.setAttribute('stroke', 'currentColor');
  el.setAttribute('stroke-width', '1.6');
  el.setAttribute('stroke-linecap', 'round');
  el.setAttribute('stroke-linejoin', 'round');
  el.classList.add('icon');
  for (const c of children) el.append(c);
  return el;
}

function path(d, { fill = false } = {}) {
  const p = document.createElementNS(SVG_NS, 'path');
  p.setAttribute('d', d);
  if (fill) { p.setAttribute('fill', 'currentColor'); p.setAttribute('stroke', 'none'); }
  return p;
}

function circle(cx, cy, r, filled = false) {
  const c = document.createElementNS(SVG_NS, 'circle');
  c.setAttribute('cx', cx); c.setAttribute('cy', cy); c.setAttribute('r', r);
  if (filled) { c.setAttribute('fill', 'currentColor'); c.setAttribute('stroke', 'none'); }
  return c;
}

const BUILDERS = {
  wifi: () => svg([
    path('M3 8a11 11 0 0 1 14 0'),
    path('M6 11a7 7 0 0 1 8 0'),
    circle(10, 14.5, 1.2, true),
  ]),
  bluetooth: () => svg([
    path('M7 6.5 L13 13.5 L10 16 V4 L13 6.5 L7 13.5'),
  ]),
  house: () => svg([
    path('M3.5 9.5 L10 4 L16.5 9.5 V16.5 H3.5 Z'),
    path('M8.5 16.5 V12 H11.5 V16.5'),
  ]),
  globe: () => svg([
    circle(10, 10, 7),
    path('M3 10 H17'),
    path('M10 3 C6.5 6 6.5 14 10 17 C13.5 14 13.5 6 10 3'),
  ]),
  pin: () => svg([
    path('M10 2.5 C6.7 2.5 4.5 5 4.5 8 C4.5 11.7 10 17.5 10 17.5 C10 17.5 15.5 11.7 15.5 8 C15.5 5 13.3 2.5 10 2.5 Z'),
    circle(10, 8, 2),
  ]),
  alert: () => svg([
    path('M10 3 L18 16.5 H2 Z'),
    path('M10 8 V12'),
    circle(10, 14.4, 0.9, true),
  ]),
  person: () => svg([
    circle(10, 7, 2.7),
    path('M4.5 16.5 C4.5 12.8 15.5 12.8 15.5 16.5'),
  ]),
  shield: () => svg([
    path('M10 2.5 L16 4.5 V9.5 C16 13.5 13 16 10 17.5 C7 16 4 13.5 4 9.5 V4.5 Z'),
    path('M7.5 10 L9.3 12 L12.8 8'),
  ]),
  devices: () => svg([
    path('M3 5.5 H12 V13 H3 Z'),
    path('M13.5 8 H17 V15 H13.5 Z'),
  ]),
  waves: () => svg([
    path('M3 12 C4.5 9 6 9 7.5 12 S10.5 15 12 12 S15 9 17 12'),
    path('M3 8 C4.5 5.5 6 5.5 7.5 8'),
  ]),
};

/** icon returns a fresh SVG element for the named glyph, or a dot fallback. */
export function icon(name, opts) {
  const build = BUILDERS[name];
  const el = build ? build() : svg([circle(10, 10, 3, true)]);
  if (opts && opts.size) {
    el.setAttribute('width', String(opts.size));
    el.setAttribute('height', String(opts.size));
  }
  return el;
}

// Which glyph represents each overview group and device category.
export const CATEGORY_ICON = {
  router: 'wifi', 'wifi-ap': 'wifi', 'wifi-client': 'devices',
  phone: 'devices', computer: 'devices', tv: 'house', speaker: 'house',
  headphones: 'waves', printer: 'house', wearable: 'waves', tracker: 'pin',
  camera: 'house', 'smart-home': 'house', console: 'devices', beacon: 'waves',
  peripheral: 'devices', vehicle: 'pin', ble: 'bluetooth', service: 'globe',
};

/** categoryIcon picks the best glyph for a device. */
export function deviceIcon(device) {
  const name = CATEGORY_ICON[device.category] || CATEGORY_ICON[device.kind] || 'devices';
  return icon(name);
}
