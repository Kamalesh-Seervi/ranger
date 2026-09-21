// Overview strip: a plain-English summary of the scan so anyone can understand
// what's around them without reading the technical panels below.

import { store, update, overviewSummary, displayName } from './store.js';
import { icon } from './icons.js';

const GROUP_ICON = { wifi: 'wifi', bluetooth: 'bluetooth', smart: 'house', services: 'globe' };

export class Overview {
  constructor(root) {
    this.root = root;
  }

  render() {
    if (!store.connected && store.devices.size === 0) {
      this.root.replaceChildren(headline(icon('waves'), 'Starting the scan…'));
      return;
    }

    const s = overviewSummary();
    const children = [];

    // Headline: where you are.
    if (s.place) {
      children.push(headline(icon('pin'), spanBold('You’re in ', s.place)));
    } else {
      const h = headline(icon('pin'), 'Learning your location…');
      h.title = 'Ranger recognises rooms from the mix of Wi-Fi it hears. Stay put for a minute.';
      children.push(h);
    }

    // Stat cards.
    const stats = document.createElement('div');
    stats.className = 'ov-stats';
    stats.append(statCard(icon('devices'), s.total, 'Devices around you'));
    for (const g of s.groups) {
      if (g.count > 0) stats.append(statCard(icon(GROUP_ICON[g.id] || 'devices'), g.count, g.label));
    }
    if (s.closest) {
      stats.append(closestCard(s));
    }
    children.push(stats);

    // Status pills — the things worth noticing.
    const status = document.createElement('div');
    status.className = 'ov-status';

    for (const f of s.followers) {
      const who = f.name || [f.vendor, f.category].filter(Boolean).join(' ') || 'A device';
      status.append(pill(icon('alert'), `${who} is following you`, 'danger'));
    }
    if (s.sensing) {
      status.append(pill(icon('person'), capitalize(s.sensing), 'info'));
    }
    if (s.openNetworks > 0) {
      status.append(pill(icon('shield'),
        `${s.openNetworks} open network${s.openNetworks > 1 ? 's' : ''} nearby`, 'warn'));
    } else if (s.groups.find((g) => g.id === 'wifi')?.count > 0) {
      status.append(pill(icon('shield'), 'All Wi-Fi nearby is secured', 'good'));
    }
    if (status.childElementCount === 0) {
      status.append(pill(icon('shield'), 'Nothing unusual', 'good'));
    }
    children.push(status);

    this.root.replaceChildren(...children);
  }
}

function headline(iconEl, text) {
  const div = document.createElement('div');
  div.className = 'ov-headline';
  iconEl.classList.add('ov-headline-icon');
  div.append(iconEl, typeof text === 'string' ? textNode(text) : text);
  return div;
}

function statCard(iconEl, value, label) {
  const card = document.createElement('div');
  card.className = 'ov-card';
  const ic = document.createElement('span');
  ic.className = 'ov-card-icon';
  ic.append(iconEl);
  const body = document.createElement('div');
  const num = document.createElement('div');
  num.className = 'ov-card-value';
  num.textContent = String(value);
  const lab = document.createElement('div');
  lab.className = 'ov-card-label';
  lab.textContent = label;
  body.append(num, lab);
  card.append(ic, body);
  return card;
}

// closestCard is clickable: it selects the nearest device so the finder and
// radar focus on it — a natural "what's right next to me?" action.
function closestCard(summary) {
  const card = document.createElement('button');
  card.type = 'button';
  card.className = 'ov-card ov-card-action';
  const ic = document.createElement('span');
  ic.className = 'ov-card-icon';
  ic.append(icon('pin'));
  const body = document.createElement('div');
  const num = document.createElement('div');
  num.className = 'ov-card-value ov-card-name';
  num.textContent = displayName(summary.closest);
  const lab = document.createElement('div');
  lab.className = 'ov-card-label';
  lab.textContent = `Closest · ${summary.closestWhere || 'nearby'}`;
  body.append(num, lab);
  card.append(ic, body);
  card.addEventListener('click', () => {
    update((st) => { st.selectedId = summary.closest.id; });
  });
  return card;
}

function pill(iconEl, text, kind) {
  const span = document.createElement('span');
  span.className = `ov-pill ov-${kind}`;
  iconEl.classList.add('ov-pill-icon');
  span.append(iconEl, textNode(text));
  return span;
}

function spanBold(prefix, bold) {
  const frag = document.createDocumentFragment();
  frag.append(textNode(prefix));
  const b = document.createElement('b');
  b.textContent = bold;
  frag.append(b);
  return frag;
}

function textNode(t) { return document.createTextNode(t); }
function capitalize(s) { return s.charAt(0).toUpperCase() + s.slice(1); }
