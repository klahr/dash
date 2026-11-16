// Renders the intro and outro (brand/intro.html, brand/outro.html) frame
// by frame: every animation is paused and stepped to each frame's time,
// then captured with Page.captureScreenshot, so the clip is smooth however
// slow the machine. Clips are cached by a hash of everything that goes
// into them.
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, readdirSync, renameSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { dirname, extname, join, normalize, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { launch } from '../../landing-shots/cdp.mjs';
import { FPS, H264, WIDTH, HEIGHT } from './media.mjs';

const here = dirname(fileURLToPath(import.meta.url));
export const brandDir = join(here, '..', 'brand');
const root = join(here, '..', '..', '..');
const styleCSS = join(root, 'web', 'static', 'style.css');

// brandConfig is brand/brand.json: the link, the series' name and so on.
export const brandConfig = () => JSON.parse(readFileSync(join(brandDir, 'brand.json'), 'utf8'));

// rootVars returns the :root block of Dash's style.css, so the intro and
// outro use the app's colours and font without the rest of its styles.
export function rootVars(css = readFileSync(styleCSS, 'utf8')) {
  const m = css.match(/:root\s*\{[^}]*\}/);
  if (!m) throw new Error('no :root block in web/static/style.css');
  return m[0] + '\n';
}

// brandHash is the cache key of one rendered clip.
export function brandHash(kind, params) {
  const h = createHash('sha256');
  h.update(JSON.stringify({ v: 1, kind, params, fps: FPS, w: WIDTH, h: HEIGHT }));
  for (const f of readdirSync(brandDir).sort()) h.update(f).update(readFileSync(join(brandDir, f)));
  for (const f of [fileURLToPath(import.meta.url), styleCSS, join(root, 'web/static/favicon.svg'), join(root, 'web/static/vendor/fonts/fonts.css')]) h.update(readFileSync(f));
  return h.digest('hex').slice(0, 24);
}

// serve serves the brand pages, the app's fonts and logo, and vars.css on
// 127.0.0.1 and returns the server and its base URL.
async function serve() {
  const types = { '.html': 'text/html', '.css': 'text/css', '.js': 'text/javascript', '.svg': 'image/svg+xml', '.woff2': 'font/woff2', '.json': 'application/json' };
  const allowed = [join(root, 'scripts', 'tutorials', 'brand') + sep, join(root, 'web', 'static') + sep];
  const server = createServer((req, res) => {
    const path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
    if (path === '/brand/vars.css') {
      res.writeHead(200, { 'Content-Type': 'text/css' });
      return res.end(rootVars());
    }
    const file = normalize(join(root, path));
    if (!allowed.some((a) => file.startsWith(a)) || !existsSync(file)) {
      res.writeHead(404);
      return res.end();
    }
    res.writeHead(200, { 'Content-Type': types[extname(file)] || 'application/octet-stream' });
    res.end(readFileSync(file));
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  return { server, base: `http://127.0.0.1:${server.address().port}` };
}

// renderClip renders brand/<kind>.html with params (title, next, link, …)
// to out, an H.264 video without sound. hold adds that many seconds of the
// last frame. stills, when given, are times to also save as PNGs:
// {seconds: file}.
async function renderClip(page, base, kind, params, out, { hold = 0, stills = {} } = {}) {
  const q = new URLSearchParams({ capture: '1', ...params });
  await page.go(`${base}/scripts/tutorials/brand/${kind}.html?${q}`);
  const duration = await page.js('window.brandReady');
  const enc = spawn('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', '-f', 'image2pipe', '-c:v', 'mjpeg', '-framerate', String(FPS), '-i', '-',
    '-vf', 'format=yuv420p', ...H264, '-an', out + '.tmp.mp4'], { stdio: ['pipe', 'ignore', 'pipe'] });
  let err = '';
  enc.stderr.on('data', (d) => { err += d; });
  const done = new Promise((resolve, reject) => enc.on('close', (c) => (c === 0 ? resolve() : reject(new Error(`ffmpeg: ${err}`)))));
  const write = (buf) => new Promise((r) => (enc.stdin.write(buf) ? r() : enc.stdin.once('drain', r)));
  const n = Math.round(duration * FPS);
  const stillAt = Object.fromEntries(Object.entries(stills).map(([t, f]) => [Math.min(n - 1, Math.round(Number(t) * FPS)), f]));
  let last;
  for (let i = 0; i < n; i++) {
    await page.js(`brandSeek(${i / FPS})`);
    const { data } = await page.send('Page.captureScreenshot', { format: 'jpeg', quality: 95 });
    last = Buffer.from(data, 'base64');
    await write(last);
    if (stillAt[i]) {
      const png = await page.send('Page.captureScreenshot', { format: 'png' });
      writeFileSync(stillAt[i], Buffer.from(png.data, 'base64'));
    }
  }
  for (let i = 0; i < Math.round(hold * FPS); i++) await write(last);
  enc.stdin.end();
  await done;
  renameSync(out + '.tmp.mp4', out);
}

// brandClips returns the intro and outro for a video titled title (whose
// next video is next), rendering whichever isn't cached yet. With
// previewDir, both are rendered again into it with a few stills, and the
// cache is left alone.
export async function brandClips({ title, next = '' }, { cacheDir, previewDir, log = console.error }) {
  const cfg = brandConfig();
  const jobs = [
    { kind: 'intro', params: { title, series: cfg.series || 'Dash tutorials' }, hold: 0, stills: [0.5, 1.2, 2.0, 3.0, 4.2] },
    { kind: 'outro', params: { next, link: cfg.link, thanks: cfg.thanks || 'Thanks for watching' }, hold: cfg.outro_hold_seconds ?? 2, stills: [0.5, 1.0, 1.6, 2.5, 5.4] },
  ];
  const out = {};
  const todo = [];
  for (const j of jobs) {
    if (previewDir) {
      mkdirSync(previewDir, { recursive: true });
      out[j.kind] = join(previewDir, `brand-${j.kind}.mp4`);
      todo.push(j);
      continue;
    }
    const dir = join(cacheDir, 'brand');
    mkdirSync(dir, { recursive: true });
    out[j.kind] = join(dir, `${j.kind}-${brandHash(j.kind, { ...j.params, hold: j.hold })}.mp4`);
    if (!existsSync(out[j.kind])) todo.push(j);
  }
  if (!todo.length) return out;
  const { server, base } = await serve();
  const page = await launch({ flags: ['--headless=new', `--window-size=${WIDTH},${HEIGHT}`] });
  try {
    await page.viewport(WIDTH, HEIGHT, 1);
    for (const j of todo) {
      log(`rendering the ${j.kind}`);
      const stills = previewDir ? Object.fromEntries(j.stills.map((t) => [t, join(previewDir, `brand-${j.kind}-${t.toFixed(1)}s.png`)])) : {};
      await renderClip(page, base, j.kind, j.params, out[j.kind], { hold: j.hold, stills });
    }
  } finally {
    page.close();
    server.close();
  }
  return out;
}
