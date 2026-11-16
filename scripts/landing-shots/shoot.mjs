#!/usr/bin/env node
// Takes the landing page's screenshots of the demo Dash (demo.sh) with
// headless Chromium over the DevTools protocol, as PNGs in an output folder.
// make.sh turns them into the WebP files in web/static/img/landing/.
//
//   CHROME=/path/to/chrome-headless-shell node shoot.mjs <out dir> [shot …]
import { writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { launch, signIn as signInAt, sleep } from './cdp.mjs';

const base = process.env.DASH_URL || 'http://127.0.0.1:7791';
const user = process.env.DASH_USERNAME || 'demo';
const pass = process.env.DASH_PASSWORD || 'demo-password-for-shots';
const [outDir, ...only] = process.argv.slice(2);
if (!outDir || !process.env.CHROME) {
  console.error('usage: CHROME=<chrome> node shoot.mjs <out dir> [shot …]');
  process.exit(2);
}
mkdirSync(outDir, { recursive: true });

const page = await launch();
const { js, wait, viewport } = page;
const go = (path) => page.go(base + path);
async function shot(name) {
  await sleep(700); // let animations settle
  const { data } = await page.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
  writeFileSync(join(outDir, `${name}.png`), Buffer.from(data, 'base64'));
  console.log(`${name}.png`);
}
// click clicks the first element matching sel whose text includes text.
const click = (sel, text = '') => js(`(() => {
  const el = [...document.querySelectorAll(${JSON.stringify(sel)})].find((e) => e.textContent.includes(${JSON.stringify(text)}));
  if (!el) throw new Error('no ' + ${JSON.stringify(sel + ' ' + text)});
  el.click();
})()`);
const cardOf = (title) => `[...document.querySelectorAll('#board .todo')].find((c) => c.textContent.includes(${JSON.stringify(title)}))`;

async function signIn() {
  await viewport(1680, 1000, 2);
  await signInAt(page, base, user, pass);
}

const shots = {
  // The board, wide.
  async board() {
    await viewport(1680, 1000, 2);
    await go('/');
    await wait('the board', `document.querySelectorAll('#board .todo').length > 8`);
    await shot('board');
  },
  // A task in review, with a file's diff open in the detail pane.
  async review() {
    await viewport(1680, 1000, 2);
    await go('/');
    await wait('the card', cardOf('Add discount codes'));
    await js(`${cardOf('Add discount codes')}.click()`);
    await wait('the detail pane', `document.querySelector('.pane-tabs button[data-pane=diff]')`);
    await js(`document.querySelector('.pane-tabs button[data-pane=diff]').click()`);
    await wait('the diff', `document.querySelector('#d-diff details.diff-file')`);
    await js(`(() => { const d = [...document.querySelectorAll('#d-diff details.diff-file')].find((d) => d.textContent.includes('checkout.js')); if (d) d.open = true; })()`);
    await sleep(500);
    await js(`document.querySelectorAll('.detail, .detail *').forEach((e) => { if (e.scrollTop) e.scrollTop = 0; })`);
    await shot('review');
  },
  // The Goals page with the goal open.
  async goals() {
    await viewport(1680, 1000, 2);
    await go('/');
    await wait('the board', `document.querySelector('#board .todo')`);
    await js(`document.querySelector('button[data-view=goals]').click()`);
    await wait('the goal', `document.querySelector('#goal-list li')`);
    await wait('the goal in progress', `[...document.querySelectorAll('#goal-list li')].find((li) => li.textContent.includes('Checkout in under 2 seconds'))`);
    await js(`[...document.querySelectorAll('#goal-list li')].find((li) => li.textContent.includes('Checkout in under 2 seconds')).querySelector('.goal-open').click()`);
    await wait('the goal open', `!document.querySelector('#goal-detail').hidden && document.querySelector('#gd-plan li')`);
    await shot('goals');
  },
  // The wall view, as a TV shows it.
  async wall() {
    await viewport(1920, 1080, 1);
    await go('/wall');
    await wait('the wall', `document.body.textContent.includes('Inline the critical CSS')`);
    await sleep(8000); // its panels fill in and counters roll to their values
    await shot('wall');
  },
  // The board's Review lane on a phone.
  async phone() {
    await viewport(390, 640, 3, true);
    await go('/');
    await js(`[...document.querySelectorAll('[data-view=board]')].find((b) => b.offsetParent)?.click()`);
    await wait('the board', `document.querySelector('#board .todo')`);
    await js(`document.querySelector('#lane-tabs .lane-tab[data-lane=review]').click()`);
    await wait('the Review lane', `document.querySelector('#board .col.active[data-lane=review] .todo')`);
    await js(`(() => {
      const strip = document.querySelector('#lane-tabs'), tab = strip.querySelector('[data-lane=review]');
      strip.scrollLeft = tab.offsetLeft - (strip.clientWidth - tab.offsetWidth) / 2;
      window.scrollTo(0, 0);
    })()`);
    await shot('phone');
  },
};

try {
  await signIn();
  for (const [name, fn] of Object.entries(shots)) {
    if (only.length && !only.includes(name)) continue;
    await fn();
  }
} finally {
  page.close();
}
