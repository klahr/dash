// The recording: headless Chrome at 1920×1080 with Page.startScreencast.
// Each step's actions run, then the screen holds until the step's clip has
// been said. Frames are saved with Chrome's timestamps, so the video can be
// rebuilt at an even frame rate (see media.mjs's framesToVideo).
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launch, signIn, sleep } from '../../landing-shots/cdp.mjs';
import { WIDTH, HEIGHT } from './media.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const cursorJS = readFileSync(join(here, 'cursor.js'), 'utf8');
const now = () => Date.now() / 1000;

// findExpr is a page expression for the first visible element matching
// sel whose text includes text. With inSel, text picks the container
// (matching inSel) instead, and sel is looked up inside it.
export const findExpr = (sel, text, inSel) => `(() => {
  const shown = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
  const has = (e) => ${text ? `e.textContent.includes(${JSON.stringify(text)})` : 'true'};
  ${inSel
    ? `for (const c of document.querySelectorAll(${JSON.stringify(inSel)})) {
    if (!shown(c) || !has(c)) continue;
    const e = [...c.querySelectorAll(${JSON.stringify(sel)})].find(shown);
    if (e) return e;
  }
  return null;`
    : `return [...document.querySelectorAll(${JSON.stringify(sel)})].find((e) => shown(e) && has(e)) || null;`}
})()`;

// record runs script against the Dash at base and returns the frames and
// each step's start, in seconds from the recording's start.
export async function record(script, clips, { base, user, pass, workDir, log = console.error }) {
  const framesDir = join(workDir, 'frames');
  mkdirSync(framesDir, { recursive: true });
  const page = await launch({
    chrome: process.env.CHROME,
    flags: ['--headless=new', `--window-size=${WIDTH},${HEIGHT}`, '--mute-audio', '--disable-features=Translate'],
  });
  const { js, send } = page;
  const frames = [];
  let writing = Promise.resolve();
  try {
    await page.viewport(WIDTH, HEIGHT, 1);
    await send('Emulation.setFocusEmulationEnabled', { enabled: true });
    await signIn(page, base, user, pass);
    await send('Page.addScriptToEvaluateOnNewDocument', { source: cursorJS });
    await page.go(base + script.start);
    await page.wait('the page', `document.querySelector('#board, main, body')`);
    await sleep(1500); // fonts, the board's first render

    // The screencast: every frame Chrome draws, with its timestamp.
    page.on('Page.screencastFrame', ({ data, metadata, sessionId }) => {
      send('Page.screencastFrameAck', { sessionId }).catch(() => {});
      const file = join(framesDir, `${String(frames.length).padStart(6, '0')}.jpg`);
      frames.push({ t: metadata.timestamp ?? now(), file });
      const buf = Buffer.from(data, 'base64');
      writing = writing.then(() => writeFileSync(file, buf));
    });
    await send('Page.startScreencast', { format: 'jpeg', quality: 92, maxWidth: WIDTH, maxHeight: HEIGHT, everyNthFrame: 1 });
    // Nudge the page so the first frame arrives straight away.
    await js('window.__tutCursor && window.__tutCursor.move(window.__tutCursor.pos.x + 1, window.__tutCursor.pos.y, 0)');
    const start = now();
    await sleep(script.lead * 1000);

    const act = actions(page);
    const steps = [];
    for (const [i, step] of script.steps.entries()) {
      const at = now() - start;
      log(`step ${i + 1}/${script.steps.length} at ${at.toFixed(2)}s: ${step.say.slice(0, 60) || '(no narration)'}`);
      for (const a of step.do) await act(a);
      const clip = clips[i];
      const until = at + (clip ? clip.seconds + script.gap : 0) + step.hold;
      const spent = now() - start;
      if (clip && spent > at + clip.seconds) log(`  step ${i + 1}'s actions took ${(spent - at).toFixed(1)}s, longer than its ${clip.seconds.toFixed(1)}s line`);
      if (until > spent) await sleep((until - spent) * 1000);
      steps.push({ at, end: now() - start });
    }
    await sleep(script.tail * 1000);
    const end = now();
    await send('Page.stopScreencast');
    await writing;
    return { frames, start, end, steps };
  } finally {
    page.close();
  }
}

// actions returns the function that runs one action of a step.
function actions(page) {
  const { js, send, wait } = page;
  const cursor = async (x, y) => {
    const from = await js('window.__tutCursor ? window.__tutCursor.pos : null');
    if (!from) return;
    const d = Math.hypot(x - from.x, y - from.y);
    const ms = d < 4 ? 0 : Math.round(Math.min(1100, 380 + d * 0.38));
    await js(`window.__tutCursor.move(${x}, ${y}, ${ms})`);
  };
  // target waits for the element and returns a point inside it, scrolled
  // into view.
  const target = async (sel, text, inSel, timeout = 30) => {
    const el = findExpr(sel, text, inSel);
    await wait(`${sel}${text ? ` with "${text}"` : ''}`, el, timeout);
    return js(`(() => {
      const e = ${el};
      e.scrollIntoView({ block: 'nearest', inline: 'nearest' });
      const r = e.getBoundingClientRect();
      return { x: Math.round(r.left + Math.min(r.width / 2, 60)), y: Math.round(r.top + r.height / 2) };
    })()`);
  };
  const mouse = (type, x, y) => send('Input.dispatchMouseEvent', { type, x, y, button: 'left', buttons: type === 'mousePressed' ? 1 : 0, clickCount: 1 });
  const click = async (sel, text, inSel, timeout) => {
    const p = await target(sel, text, inSel, timeout);
    await cursor(p.x, p.y);
    await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: p.x, y: p.y });
    await sleep(120);
    await js('window.__tutCursor && window.__tutCursor.down()');
    await mouse('mousePressed', p.x, p.y);
    await sleep(70);
    await mouse('mouseReleased', p.x, p.y);
    await js('window.__tutCursor && window.__tutCursor.up()');
    await sleep(150);
  };
  const keys = {
    Enter: { key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' },
    Escape: { key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 },
    Tab: { key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 },
  };
  return async (a) => {
    switch (a.kind) {
      case 'click': return click(a.click, a.text, a.in, a.timeout);
      case 'hover': {
        const p = await target(a.hover, a.text, a.in, a.timeout);
        await cursor(p.x, p.y);
        return send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: p.x, y: p.y });
      }
      case 'type': {
        if (a.into) await click(a.into, a.text, a.in);
        const cps = a.cps || 18;
        let i = 0;
        for (const ch of a.type) {
          await send('Input.insertText', { text: ch });
          // A touch of rhythm: longer after spaces and punctuation.
          const extra = /[ ,.]/.test(ch) ? 40 : ((i++ * 37) % 23);
          await sleep(1000 / cps + extra);
        }
        return;
      }
      case 'key': {
        const k = keys[a.key] || { key: a.key, text: a.key.length === 1 ? a.key : undefined };
        await send('Input.dispatchKeyEvent', { type: 'keyDown', ...k });
        await send('Input.dispatchKeyEvent', { type: 'keyUp', ...k, text: undefined });
        return;
      }
      case 'wait':
        if (a.gone) return wait(`${a.wait} to go`, `!(${findExpr(a.wait, a.text, a.in)})`, a.timeout || 60);
        return wait(`${a.wait}${a.text ? ` with "${a.text}"` : ''}`, findExpr(a.wait, a.text, a.in), a.timeout || 60);
      case 'scroll': {
        // Scrolls the element smoothly into view, or scrolls it by "by" pixels.
        const el = findExpr(a.scroll, a.text, a.in);
        await wait(a.scroll, el, a.timeout || 30);
        // "by" scrolls the element, or the nearest scrolling box around it.
        await js(a.by
          ? `(() => {
            let e = ${el};
            while (e && !(e.scrollHeight > e.clientHeight && /auto|scroll/.test(getComputedStyle(e).overflowY))) e = e.parentElement;
            (e || document.scrollingElement).scrollBy({ top: ${Number(a.by)}, behavior: 'smooth' });
          })()`
          : `${el}.scrollIntoView({ block: 'center', behavior: 'smooth' })`);
        return sleep(700);
      }
      case 'pause': return sleep(a.pause * 1000);
      case 'open': {
        const base = await js('location.origin');
        await page.go(base + a.open);
        return sleep(800);
      }
      default: throw new Error(`unknown action ${a.kind}`);
    }
  };
}
