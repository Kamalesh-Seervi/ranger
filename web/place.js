// Place panel: which learned location we appear to be in, and the set of
// places discovered so far.

import { store } from './store.js';

export class PlacePanel {
  constructor(root, anchorsLabel) {
    this.root = root;
    this.anchorsLabel = anchorsLabel;
    this.editingId = null;
    this.busy = false;
  }

  render() {
    // Re-rendering mid-edit would discard what the user is typing.
    if (this.editingId) return;

    const place = store.insight;
    if (!place) {
      this.anchorsLabel.textContent = '';
      this.root.replaceChildren(note('Waiting for the first scan.'));
      return;
    }

    this.anchorsLabel.textContent = place.anchors
      ? `${place.anchors} stable anchors`
      : '';

    const parts = [];
    parts.push(place.active ? this.current(place) : note(place.reason || 'Not enough stable anchors yet.'));

    if (place.places && place.places.length) {
      parts.push(this.known(place.places));
    }
    parts.push(motionSection(place.motion));
    this.root.replaceChildren(...parts);
  }

  current(place) {
    const wrap = document.createElement('div');
    wrap.className = 'place-current';

    const name = document.createElement('button');
    name.type = 'button';
    name.className = 'place-name';
    name.textContent = place.name || 'Unnamed place';
    name.title = 'Click to rename';
    name.addEventListener('click', () => this.beginEdit(place.placeId, place.name));
    wrap.append(name);

    const meta = document.createElement('div');
    meta.className = 'place-meta';
    meta.textContent = `match ${Math.round(place.score * 100)}% · here ${formatDwell(place.dwellNs)}`;
    wrap.append(meta);

    const bar = document.createElement('div');
    bar.className = 'place-bar';
    const fill = document.createElement('div');
    fill.className = 'place-fill';
    fill.style.width = `${Math.min(100, place.score * 100).toFixed(0)}%`;
    bar.append(fill);
    wrap.append(bar);

    return wrap;
  }

  known(places) {
    const list = document.createElement('ul');
    list.className = 'place-list';

    for (const p of places) {
      const li = document.createElement('li');
      if (p.current) li.classList.add('current');

      const name = document.createElement('button');
      name.type = 'button';
      name.className = 'place-entry-name';
      name.textContent = p.name;
      name.title = (p.top || []).join('\n') || 'No anchors recorded';
      name.addEventListener('click', () => this.beginEdit(p.id, p.name));

      const stats = document.createElement('span');
      stats.className = 'place-entry-stats';
      stats.textContent = `${p.anchors} anchors · ${formatDwell(p.dwellNs)}`;

      const forget = document.createElement('button');
      forget.type = 'button';
      forget.className = 'place-forget';
      forget.textContent = '✕';
      forget.title = 'Forget this place';
      forget.addEventListener('click', () => this.send('/api/places/forget', { id: p.id }));

      li.append(name, stats, forget);
      list.append(li);
    }
    return list;
  }

  beginEdit(id, currentName) {
    if (!id) return;
    this.editingId = id;

    const form = document.createElement('form');
    form.className = 'place-edit';

    const input = document.createElement('input');
    input.type = 'text';
    input.maxLength = 64;
    input.value = currentName || '';
    input.placeholder = 'Kitchen, Office, Car…';

    const save = document.createElement('button');
    save.type = 'submit';
    save.className = 'tab';
    save.textContent = 'Save';

    form.append(input, save);
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      const name = input.value.trim();
      this.editingId = null;
      if (name) this.send('/api/places/rename', { id, name });
      else this.render();
    });
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        this.editingId = null;
        this.render();
      }
    });

    this.root.replaceChildren(form);
    input.focus();
    input.select();
  }

  async send(path, body) {
    if (this.busy) return;
    this.busy = true;
    try {
      const resp = await fetch(path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (resp.ok) {
        // The server echoes fresh state; the stream will also deliver it.
        store.insight = await resp.json();
      }
    } catch {
      // The stream reconnect path already surfaces connectivity problems.
    } finally {
      this.busy = false;
      this.render();
    }
  }
}

// motionSection reports disturbance in the radio environment. It can only say
// that something moved, never what, so the wording stays deliberately vague.
function motionSection(motion) {
  const wrap = document.createElement('div');
  wrap.className = 'motion';

  const head = document.createElement('div');
  head.className = 'motion-head';

  const title = document.createElement('span');
  title.className = 'motion-title';
  title.textContent = 'Movement';
  head.append(title);

  if (!motion || !motion.active) {
    const why = document.createElement('span');
    why.className = 'motion-note';
    why.textContent = (motion && motion.reason) || 'idle';
    head.append(why);
    wrap.append(head);
    return wrap;
  }

  const status = document.createElement('span');
  if (motion.calibrating) {
    status.className = 'motion-status calm';
    status.textContent = 'calibrating';
  } else if (motion.detected) {
    status.className = 'motion-status alert';
    status.textContent = 'something moved';
  } else {
    status.className = 'motion-status calm';
    status.textContent = 'still';
  }
  head.append(status);
  wrap.append(head);

  const bar = document.createElement('div');
  bar.className = 'motion-bar';
  const fill = document.createElement('div');
  fill.className = 'motion-fill';
  fill.style.width = `${Math.min(100, (motion.level || 0) * 100).toFixed(0)}%`;
  bar.append(fill);
  wrap.append(bar);

  const detail = document.createElement('div');
  detail.className = 'motion-note';
  detail.textContent =
    `${motion.disturbance} dB wobble vs ${motion.baseline} quiet · ` +
    `${motion.anchors} anchors at ${motion.sampleRate}/s`;
  detail.title =
    'Detects that the radio environment was disturbed, which usually means ' +
    'someone moved nearby. It cannot identify or locate what moved. ' +
    'Sensitivity depends on how often the anchors report.';
  wrap.append(detail);

  return wrap;
}

function note(text) {
  const p = document.createElement('p');
  p.className = 'empty';
  p.textContent = text;
  return p;
}

function formatDwell(nanoseconds) {
  const seconds = Math.round((nanoseconds || 0) / 1e9);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}
