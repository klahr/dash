// A small Chrome DevTools protocol client, shared by shoot.mjs (the landing
// page's screenshots) and scripts/tutorials (the tutorial videos).
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// launch starts Chrome (the binary in $CHROME, or chrome) with a throwaway
// profile and connects to its first page. flags are added to Chrome's own.
export async function launch({ chrome = process.env.CHROME, flags = [] } = {}) {
  if (!chrome) throw new Error('set CHROME to a Chrome or chrome-headless-shell binary');
  const profile = mkdtempSync(join(tmpdir(), 'cdp-'));
  const port = 9300 + Math.floor(Math.random() * 500);
  const proc = spawn(chrome, [
    `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--hide-scrollbars', '--force-color-profile=srgb', '--font-render-hinting=none', '--no-first-run',
    ...flags, 'about:blank',
  ], { stdio: 'ignore' });
  let target;
  for (let i = 0; i < 75 && !target; i++) {
    await sleep(200);
    const list = await fetch(`http://127.0.0.1:${port}/json/list`).then((r) => r.json()).catch(() => null);
    target = list?.find((t) => t.type === 'page');
  }
  if (!target) {
    proc.kill();
    throw new Error(`Chrome (${chrome}) did not start`);
  }
  const page = await connect(target.webSocketDebuggerUrl);
  const close = page.close;
  page.close = () => {
    close();
    proc.kill();
    setTimeout(() => rmSync(profile, { recursive: true, force: true }), 500).unref();
  };
  await page.send('Page.enable');
  await page.send('Runtime.enable');
  return page;
}

// connect opens a page's DevTools socket and returns helpers for it.
async function connect(url) {
  const ws = new WebSocket(url);
  await new Promise((r, j) => {
    ws.addEventListener('open', r, { once: true });
    ws.addEventListener('error', j, { once: true });
  });
  let nextId = 0;
  const pending = new Map();
  const handlers = new Map();
  ws.addEventListener('message', (e) => {
    const m = JSON.parse(e.data);
    if (m.method === 'Runtime.exceptionThrown') {
      const d = m.params.exceptionDetails;
      console.error('page error:', d.exception?.description || d.text);
    }
    if (m.method) for (const fn of handlers.get(m.method) || []) fn(m.params);
    if (m.id && pending.has(m.id)) {
      const { resolve, reject } = pending.get(m.id);
      pending.delete(m.id);
      if (m.error) reject(new Error(JSON.stringify(m.error)));
      else resolve(m.result);
    }
  });
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = ++nextId;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });
  // on calls fn with the params of every event named method.
  const on = (method, fn) => handlers.set(method, [...(handlers.get(method) || []), fn]);
  // js evaluates expr in the page (awaiting a promise) and returns its value.
  async function js(expr) {
    const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(`${expr}: ${r.exceptionDetails.exception?.description || r.exceptionDetails.text}`);
    return r.result.value;
  }
  // wait waits until expr is truthy in the page.
  async function wait(what, expr, secs = 20) {
    for (let i = 0; i < secs * 10; i++) {
      if (await js(`!!(${expr})`).catch(() => false)) return;
      await sleep(100);
    }
    throw new Error(`timed out waiting for ${what}`);
  }
  async function viewport(width, height, scale, mobile = false) {
    await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: scale, mobile });
    await send('Emulation.setTouchEmulationEnabled', { enabled: mobile });
  }
  // go opens url and waits for it to load.
  async function go(url) {
    await send('Page.navigate', { url });
    await sleep(500);
    await wait('the page', 'document.readyState === "complete"');
  }
  return { send, on, js, wait, viewport, go, close: () => ws.close() };
}

// signIn signs in to the Dash at base and waits for the board.
export async function signIn(page, base, user, pass) {
  await page.go(base + '/login.html');
  await page.js(`(() => {
    const f = document.querySelector('#login');
    f.username.value = ${JSON.stringify(user)}; f.password.value = ${JSON.stringify(pass)};
    f.querySelector('button.btn.primary').click();
  })()`);
  // The board's columns, or its welcome when there is no project yet.
  await page.wait('the board', `document.querySelector('#board .col, #board-empty .empty-title')`);
}
