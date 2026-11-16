// The intro's and outro's shared runtime: the logo, the moving background
// and the frame stepping the renderer (lib/brand.mjs) uses.
//
// The page reads its text from the query string (title, next, link,
// series, thanks). With capture=1 nothing moves by itself: the renderer
// calls brandSeek(t) for each frame. Without it the page plays live, for
// a quick look in a browser.
const params = new URLSearchParams(location.search);
export const text = (k, fallback = '') => params.get(k) ?? fallback;
const capture = params.has('capture');

// The background: slowly drifting colour, a faint grid and a few motes,
// all a pure function of time so every frame can be drawn on its own.
// stillAt, when set, eases the motion to a stop at that time.
function background(canvas, stillAt) {
  const g = canvas.getContext('2d');
  const W = canvas.width = 1920, H = canvas.height = 1080;
  const css = getComputedStyle(document.documentElement);
  const v = (n) => css.getPropertyValue(n).trim();
  const [bg, accent, accent2, review] = [v('--bg'), v('--accent'), v('--accent2'), v('--review')];
  let seed = 7;
  const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647);
  const motes = Array.from({ length: 70 }, () => ({ x: rnd() * W, y: rnd() * H, r: 0.8 + rnd() * 1.8, s: 6 + rnd() * 16, a: 0.12 + rnd() * 0.28, p: rnd() * 6.28 }));
  const blobs = [
    { c: accent, x: 0.28, y: 0.32, r: 760, ax: 140, ay: 90, w: 0.11, p: 0 },
    { c: accent2, x: 0.76, y: 0.70, r: 680, ax: 160, ay: 80, w: 0.09, p: 2.1 },
    { c: review, x: 0.62, y: 0.18, r: 520, ax: 120, ay: 70, w: 0.13, p: 4.2 },
  ];
  const alpha = (hex, a) => {
    const n = parseInt(hex.slice(1), 16);
    return `rgba(${n >> 16}, ${(n >> 8) & 255}, ${n & 255}, ${a})`;
  };
  return (t) => {
    // Motion time: with stillAt, the speed falls to zero at stillAt.
    const m = stillAt ? (t < stillAt ? t - (t * t) / (2 * stillAt) : stillAt / 2) : t;
    g.fillStyle = bg;
    g.fillRect(0, 0, W, H);
    for (const b of blobs) {
      const x = b.x * W + Math.sin(m * b.w * 6.28 + b.p) * b.ax, y = b.y * H + Math.cos(m * b.w * 5 + b.p) * b.ay;
      const gr = g.createRadialGradient(x, y, 0, x, y, b.r);
      gr.addColorStop(0, alpha(b.c, 0.17));
      gr.addColorStop(1, alpha(b.c, 0));
      g.fillStyle = gr;
      g.fillRect(0, 0, W, H);
    }
    // The grid drifts up and to the left, fading out towards the edges.
    const step = 80, ox = (m * 9) % step, oy = (m * 14) % step;
    g.strokeStyle = alpha(accent, 0.055);
    g.lineWidth = 1;
    g.beginPath();
    for (let x = -ox; x < W; x += step) { g.moveTo(x + 0.5, 0); g.lineTo(x + 0.5, H); }
    for (let y = -oy; y < H; y += step) { g.moveTo(0, y + 0.5); g.lineTo(W, y + 0.5); }
    g.stroke();
    for (const p of motes) {
      const y = ((p.y - m * p.s) % H + H) % H, x = p.x + Math.sin(m * 0.4 + p.p) * 14;
      g.fillStyle = alpha(accent, p.a * (0.75 + 0.25 * Math.sin(m * 0.8 + p.p)));
      g.beginPath();
      g.arc(x, y, p.r, 0, 6.283);
      g.fill();
    }
    const vg = g.createRadialGradient(W / 2, H / 2, H * 0.35, W / 2, H / 2, W * 0.62);
    vg.addColorStop(0, alpha(bg, 0));
    vg.addColorStop(1, alpha(bg, 0.85));
    g.fillStyle = vg;
    g.fillRect(0, 0, W, H);
  };
}

// logo puts Dash's logo (web/static/favicon.svg) into el and tags its
// pieces for the animations.
async function logo(el) {
  el.insertAdjacentHTML('afterbegin', await fetch('/web/static/favicon.svg').then((r) => r.text()));
  const svg = el.querySelector('svg');
  svg.querySelector('rect').classList.add('tile');
  const path = svg.querySelector('path');
  path.classList.add('stem');
  path.setAttribute('pathLength', '1');
  const [glow, head, top, ring] = svg.querySelectorAll('circle');
  glow.classList.add('glow');
  head.classList.add('head');
  top.classList.add('top');
  ring.classList.add('ring');
  el.insertAdjacentHTML('beforeend', '<div class="sweep"></div>');
}

// words fills el with s, each letter in a span numbered by --i.
export function words(el, s, from = 0) {
  let i = from;
  el.classList.add('words');
  el.replaceChildren(...s.split(/\s+/).filter(Boolean).flatMap((w, k) => {
    const word = document.createElement('span');
    word.className = 'w';
    for (const ch of w) {
      const l = document.createElement('span');
      l.className = 'l';
      l.textContent = ch;
      l.style.setProperty('--i', i++);
      word.append(l);
    }
    i++;
    return k ? [document.createTextNode(' '), word] : [word];
  }));
  return i;
}

// start sets the page up: setup(…) runs once fonts and the logo are in,
// then the animations start (body.go). It resolves window.brandReady.
export function start({ duration, stillAt, setup }) {
  document.body.dataset.duration = duration;
  window.brandReady = (async () => {
    await Promise.all([...document.querySelectorAll('.logo')].map(logo));
    await document.fonts.ready;
    setup?.();
    const draw = background(document.getElementById('bg'), stillAt);
    document.body.classList.add('go');
    if (capture) {
      window.brandSeek = async (t) => {
        for (const a of document.getAnimations()) {
          a.pause();
          a.currentTime = t * 1000;
        }
        draw(t);
        await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
      };
    } else {
      const t0 = performance.now();
      const loop = () => {
        const t = (performance.now() - t0) / 1000;
        draw(t);
        if (t < duration) requestAnimationFrame(loop);
      };
      loop();
    }
    return duration;
  })();
}
