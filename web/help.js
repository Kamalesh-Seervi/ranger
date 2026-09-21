// Guide overlay: plain-language help so a first-time, non-technical visitor
// understands what every panel means. Opens automatically on the first visit.

const SEEN_KEY = 'ranger.guide.seen';

const SECTIONS = [
  {
    h: 'What is this?',
    p: 'Ranger listens to the radio signals around you — Wi-Fi, Bluetooth, and smart-home gadgets — and shows what is nearby. It only listens. It never joins a network, changes anything, or transmits.',
  },
  {
    h: 'The summary at the top',
    p: 'A plain-English snapshot: where you are, how many devices are around, the closest one, and anything worth noticing such as an open Wi-Fi network or a tracker that keeps appearing next to you.',
  },
  {
    h: 'Radar',
    p: 'Each dot is a device. The closer a dot is to the centre, the closer the device is to you. This is a rough estimate from signal strength, not a precise position. Click a dot to focus on it.',
  },
  {
    h: 'Finder',
    p: 'Pick any device to hunt it down. Turn on Audio and it beeps faster and higher as you get closer — useful for finding a lost tag, a noisy speaker, or where a signal is coming from.',
  },
  {
    h: 'Devices list',
    p: 'Everything found, with our best guess at what each one is. Signal and Distance are shown in plain words; the exact numbers are in the tooltips. Click a row to focus it.',
  },
  {
    h: 'Place',
    p: 'Ranger learns your rooms from the unique mix of Wi-Fi each one hears — no GPS, no map. Click a place to give it a name. It remembers between runs.',
  },
  {
    h: 'What the words mean',
    list: [
      ['Signal', 'How strong the device comes through — Strong, Good, Fair or Weak.'],
      ['Distance', 'A rough room-scale guess from the signal. Walls and bodies shift it, so treat it as a hint.'],
      ['Secure / Open', 'Whether a Wi-Fi network is protected. “Open” means anyone can join it.'],
      ['Type', 'Our best guess at what a device is — a phone, TV, printer and so on — with the confidence shown when you hover it.'],
    ],
  },
  {
    h: 'Your privacy',
    p: 'The dashboard runs only on this computer and is locked to it. Everything shown is already broadcast openly over the air; Ranger just collects and explains it.',
  },
];

export class Guide {
  constructor(openButton) {
    this.overlay = buildOverlay(() => this.close());
    document.body.append(this.overlay);

    openButton.addEventListener('click', () => this.open());
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') this.close();
    });

    // First-time visitors get the guide automatically.
    let seen = false;
    try { seen = localStorage.getItem(SEEN_KEY) === '1'; } catch { /* private mode */ }
    if (!seen) this.open();
  }

  open() {
    this.overlay.classList.add('open');
    try { localStorage.setItem(SEEN_KEY, '1'); } catch { /* ignore */ }
  }

  close() {
    this.overlay.classList.remove('open');
  }
}

function buildOverlay(onClose) {
  const overlay = document.createElement('div');
  overlay.className = 'guide-overlay';
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) onClose();
  });

  const modal = document.createElement('div');
  modal.className = 'guide-modal';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-label', 'How to use Ranger');

  const head = document.createElement('div');
  head.className = 'guide-head';
  const title = document.createElement('h2');
  title.textContent = 'How to read this';
  const close = document.createElement('button');
  close.type = 'button';
  close.className = 'guide-close';
  close.setAttribute('aria-label', 'Close');
  close.textContent = '×';
  close.addEventListener('click', onClose);
  head.append(title, close);
  modal.append(head);

  const body = document.createElement('div');
  body.className = 'guide-body';
  for (const s of SECTIONS) {
    const h = document.createElement('h3');
    h.textContent = s.h;
    body.append(h);
    if (s.p) {
      const p = document.createElement('p');
      p.textContent = s.p;
      body.append(p);
    }
    if (s.list) {
      const dl = document.createElement('dl');
      for (const [term, def] of s.list) {
        const dt = document.createElement('dt');
        dt.textContent = term;
        const dd = document.createElement('dd');
        dd.textContent = def;
        dl.append(dt, dd);
      }
      body.append(dl);
    }
  }
  modal.append(body);

  const foot = document.createElement('div');
  foot.className = 'guide-foot';
  const got = document.createElement('button');
  got.type = 'button';
  got.className = 'guide-got';
  got.textContent = 'Got it';
  got.addEventListener('click', onClose);
  foot.append(got);
  modal.append(foot);

  overlay.append(modal);
  return overlay;
}
