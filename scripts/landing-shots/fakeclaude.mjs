#!/usr/bin/env node
// A stand-in for Claude Code, for the landing page's screenshots: it answers
// the way Dash calls `claude -p` and makes a canned change for each demo task
// (keyed by a word in the task's prompt), so the board fills with real
// diffs, test runs and timelines without a Claude subscription.
//
// A task whose prompt matches a "hold" entry keeps running while
// $DEMO_DIR/hold exists. Every run reports the usage windows Claude Code
// would (rate_limit_event), so the usage meters have something to show.
//
// Each task has a session of its own (demo-<id>), so a resumed run (a
// rework, or a merge conflict sent back) knows its task even though Dash
// then sends only the new notes. A conflict is resolved by keeping both
// sides' changes (it needs merge.conflictStyle diff3, which demo.sh sets);
// a task with rework() makes that change on any other rework.
import { readFileSync, writeFileSync, mkdirSync, existsSync, chmodSync } from 'node:fs';
import * as game from './starcatcher.mjs';
import { dirname } from 'node:path';
import { execFileSync } from 'node:child_process';

const args = process.argv.slice(2);
const has = (a) => args.includes(a);
const demo = process.env.DEMO_DIR || '/tmp';

if (has('--version')) {
  console.log('2.1.0 (Claude Code)');
  process.exit(0);
}
// `claude auth status`: signed in, so the runner shows whose login it uses.
if (args[0] === 'auth' && args[1] === 'status') {
  console.log(JSON.stringify({ loggedIn: true, authMethod: 'claude.ai', email: 'alex@example.com', orgName: "Alex's Organization", subscriptionType: 'max' }));
  process.exit(0);
}
const stdin = readFileSync(0, 'utf8');
// Every call's arguments and input, for working out what Dash sent.
// Next to this script: Dash runs Claude with an environment of its own.
try { writeFileSync(`${import.meta.dirname}/.claude-calls.log`, `\n===== ${new Date().toISOString()} ${process.argv.slice(2).filter((a) => a.length < 200).join(' ')}\n${stdin}\n`, { flag: 'a' }); } catch { /* best effort */ }
if (!stdin.trim()) {
  console.error('Error: Input must be provided either through stdin or as a prompt argument when using --print');
  process.exit(1);
}

// The session this run reports: --resume's, or its task's (see task()).
let session = (() => {
  const i = args.indexOf('--resume');
  return i >= 0 ? args[i + 1] : 'demo-session';
})();
const out = (v) => process.stdout.write(JSON.stringify(v) + '\n');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const hour = 3600;
const now = Math.floor(Date.now() / 1000);
const usage = () => out({
  type: 'rate_limit_event',
  rate_limit_info: {
    status: 'allowed',
    unifiedWindows: {
      five_hour: { utilization: 0.34, resetsAt: now + 2 * hour + 40 * 60, status: 'allowed' },
      seven_day: { utilization: 0.52, resetsAt: now + 3 * 24 * hour + 5 * hour, status: 'allowed' },
    },
  },
});
const result = (text, cost = 0.42, turns = 14) => out({
  type: 'result', subtype: 'success', is_error: false, result: text, num_turns: turns,
  total_cost_usd: cost, session_id: session,
  usage: { input_tokens: 18400, output_tokens: 6200, cache_read_input_tokens: 412000, cache_creation_input_tokens: 21000 },
});
let toolN = 0;
const tool = (name, input, output) => {
  const id = `toolu_demo_${++toolN}`;
  out({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name, input }] } });
  out({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, content: output || 'ok' }] } });
};
const say = (text) => out({ type: 'assistant', message: { content: [{ type: 'text', text }] } });

// callTool calls a tool on one of the MCP servers in --mcp-config.
async function callTool(server, name, args_) {
  const i = args.indexOf('--mcp-config');
  if (i < 0) throw new Error('no --mcp-config');
  const cfg = JSON.parse(args[i + 1]);
  const s = cfg.mcpServers?.[server];
  if (!s) throw new Error(`no ${server} server`);
  const res = await fetch(s.url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', ...(s.headers || {}) },
    body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: args_ } }),
  });
  const body = await res.text();
  if (!res.ok || body.includes('"isError":true')) throw new Error(`${name}: ${res.status} ${body}`);
  return body;
}

// mcpText is the text of an MCP tools/call answer (JSON, or one SSE event).
function mcpText(body) {
  const json = body.trim().startsWith('{') ? body : (body.match(/^data: (.*)$/m) || [])[1] || '{}';
  const r = JSON.parse(json).result || {};
  return (r.content || []).map((c) => c.text || '').join('') || JSON.stringify(r);
}

function edit(file, find, replace) {
  const s = readFileSync(file, 'utf8');
  if (!s.includes(find)) throw new Error(`${file}: no ${JSON.stringify(find)}`);
  writeFileSync(file, s.replace(find, replace));
}
function write(file, text, mode) {
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, text);
  if (mode) chmodSync(file, mode);
}

// The demo video's five tasks (seed.mjs's demo-video scene), by their
// titles. Each works for a while (pace ms between steps), so the board
// shows them running side by side. "Add dark mode" and "Update the README"
// both change the README's line about dark mode, so the second of them to
// merge conflicts; "Validate the webhook URL" leaves the Save button as low
// as it was, and fixes it when the task is sent back with any note.
const videoTasks = [
  {
    id: 'dark-mode', key: /^# Task: Add dark mode$/m, pace: 2500,
    short: 'Dark theme that follows the system',
    summary: 'Added a dark theme: the colours in `public/style.css` are CSS custom properties now, with dark values under `prefers-color-scheme: dark`, so every page follows the system setting. The README says so.',
    steps: [['Read', { file_path: 'public/style.css' }], ['Grep', { pattern: '#[0-9a-f]{6}', path: 'public' }], ['Edit', { file_path: 'public/style.css' }], ['Edit', { file_path: 'README.md' }], ['Bash', { command: 'node --test' }]],
    run() {
      const colours = [
        ['color: #ffffff', 'color: var(--on-accent)'],
        ['#f6f7f9', 'var(--bg)'], ['#ffffff', 'var(--surface)'], ['#1d2330', 'var(--text)'], ['#2453d6', 'var(--accent)'],
        ['#dde1e8', 'var(--border)'], ['#4a5263', 'var(--muted)'], ['#6b7385', 'var(--faint)'], ['#c3c9d4', 'var(--input-border)'],
        ['#1f7a3d', 'var(--ok)'], ['#c42b2b', 'var(--danger)'], ['#eceff3', 'var(--rule)'],
      ];
      let css = readFileSync('public/style.css', 'utf8');
      for (const [from, to] of colours) css = css.replaceAll(from, to);
      css = css.replace('/* The shop\'s shared styles. */\n', `/* The shop's shared styles. Colours are custom properties, with a dark
   theme that follows the system setting. */
:root {
  color-scheme: light dark;
  --bg: #f6f7f9;
  --surface: #ffffff;
  --text: #1d2330;
  --muted: #4a5263;
  --faint: #6b7385;
  --accent: #2453d6;
  --on-accent: #ffffff;
  --border: #dde1e8;
  --input-border: #c3c9d4;
  --rule: #eceff3;
  --ok: #1f7a3d;
  --danger: #c42b2b;
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg: #12151c;
    --surface: #1b1f29;
    --text: #e6e9ef;
    --muted: #a3abbb;
    --faint: #8189a0;
    --accent: #7a9bff;
    --on-accent: #0b0e14;
    --border: #2c3240;
    --input-border: #3a4152;
    --rule: #252a36;
    --ok: #5fd08a;
    --danger: #ff7a7a;
  }
}
`);
      write('public/style.css', css);
      edit('README.md', '- Light theme only: dark mode is planned\n', '- A dark theme that follows the system setting\n');
    },
  },
  {
    id: 'webhook-url', key: /^# Task: Validate the webhook URL$/m, pace: 2500,
    short: 'Webhook URL must be https',
    summary: 'The webhook URL must now be an absolute `https://` address that isn\'t localhost or a private network. The field is a required `type="url"` input, the server checks it again in `src/webhooks.js` and shows "Enter an https:// URL" under the field, and `test/webhooks.test.js` covers it.',
    steps: [['Read', { file_path: 'src/views/webhooks.html' }], ['Read', { file_path: 'src/webhooks.js' }], ['Edit', { file_path: 'src/webhooks.js' }], ['Edit', { file_path: 'src/views/webhooks.html' }], ['Edit', { file_path: 'src/server.js' }], ['Write', { file_path: 'test/webhooks.test.js' }], ['Bash', { command: 'node --test' }]],
    run() {
      write('src/webhooks.js', `// The webhook Acme calls when an order is paid. Kept in memory for now.
let url = '';

export function webhookUrl() {
  return url;
}

// validWebhookUrl reports whether value is an absolute https:// URL that
// isn't localhost or a private network address.
export function validWebhookUrl(value) {
  let u;
  try {
    u = new URL(value);
  } catch {
    return false;
  }
  if (u.protocol !== 'https:') return false;
  const host = u.hostname;
  return !(host === 'localhost' || host.endsWith('.localhost') || /^(127|10|192\\.168)\\./.test(host) || /^172\\.(1[6-9]|2\\d|3[01])\\./.test(host));
}

// setWebhookUrl saves value, or returns an error message and keeps the old one.
export function setWebhookUrl(value) {
  value = value.trim();
  if (!validWebhookUrl(value)) return 'Enter an https:// URL';
  url = value;
  return '';
}
`);
      edit('src/views/webhooks.html',
        '    <input id="webhook-url" name="url" type="text" placeholder="https://example.com/hooks/orders" value="{{url}}">\n',
        '    <input id="webhook-url" name="url" type="url" required pattern="https://.*" placeholder="https://example.com/hooks/orders" value="{{url}}" aria-describedby="webhook-error">\n');
      edit('src/views/webhooks.html', '  {{saved}}\n', '  <p class="error" id="webhook-error">{{error}}</p>\n  {{saved}}\n');
      edit('src/server.js', `    setWebhookUrl((await form(req)).get('url') || '');
    page(res, 'Webhooks', fill(file('./views/webhooks.html'), { url: escape(webhookUrl()), saved: '<p class="notice">Saved.</p>' }), ['webhooks.css']);`,
      `    const value = (await form(req)).get('url') || '';
    const error = setWebhookUrl(value);
    if (error) res.statusCode = 400;
    page(res, 'Webhooks', fill(file('./views/webhooks.html'), {
      url: escape(error ? value : webhookUrl()), error, saved: error ? '' : '<p class="notice">Saved.</p>',
    }), ['webhooks.css']);`);
      write('test/webhooks.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { validWebhookUrl, setWebhookUrl, webhookUrl } from '../src/webhooks.js';

test('an https URL is valid', () => {
  assert.ok(validWebhookUrl('https://example.com/hooks/orders'));
});

test('anything else is not', () => {
  for (const u of ['', 'not a url', 'http://example.com/hooks', 'ftp://example.com', 'https://localhost/x', 'https://192.168.1.4/x']) {
    assert.equal(validWebhookUrl(u), false, u);
  }
});

test('a bad URL keeps the saved one', () => {
  assert.equal(setWebhookUrl('https://example.com/a'), '');
  assert.equal(setWebhookUrl('nope'), 'Enter an https:// URL');
  assert.equal(webhookUrl(), 'https://example.com/a');
});
`);
    },
    rework: {
      summary: 'Lined the Save button up with the URL field: the row centres its items and the button lost its stray margins.',
      steps: [['Read', { file_path: 'public/webhooks.css' }], ['Edit', { file_path: 'public/webhooks.css' }]],
      run() {
        edit('public/webhooks.css', 'align-items: flex-start;', 'align-items: center;');
        edit('public/webhooks.css', '.webhook-form .save { margin-top: 22px; margin-left: 4px; }', '.webhook-form .save { margin: 0; }');
      },
    },
  },
  {
    id: 'export-dates', key: /^# Task: Fix the date bug in exports$/m, pace: 2500,
    short: 'Export dates no longer a month early',
    summary: 'The orders export wrote every date a month early (January as month 00): `formatDate` used `getMonth()`, which counts from 0. It adds 1 now, and a test pins January and December.',
    steps: [['Read', { file_path: 'src/export.js' }], ['Write', { file_path: 'test/export.test.js' }], ['Bash', { command: 'node --test test/export.test.js' }, '✖ dates keep their month\n  expected: 2026-01-31\n  actual:   2026-00-31'], ['Edit', { file_path: 'src/export.js' }], ['Bash', { command: 'node --test' }]],
    run() {
      edit('src/export.js', 'pad(d.getMonth())', 'pad(d.getMonth() + 1)');
      edit('test/export.test.js', "import { ordersCsv } from '../src/export.js';", "import { ordersCsv, formatDate } from '../src/export.js';");
      write('test/export.test.js', readFileSync('test/export.test.js', 'utf8') + `
test('dates keep their month', () => {
  assert.equal(formatDate(new Date(2026, 0, 31)), '2026-01-31');
  assert.equal(formatDate(new Date(2026, 11, 1)), '2026-12-01');
});
`);
    },
  },
  {
    id: 'billing-tests', key: /^# Task: Add tests for the billing page$/m, pace: 2500,
    short: 'Tests for plans, proration and VAT',
    summary: 'Added `test/billing.test.js`: plan prices, prorating a month that started on the 11th, VAT rounding, extra seats and an unknown plan. They all pass; no bug in `src/billing.js`.',
    steps: [['Read', { file_path: 'src/billing.js' }], ['Read', { file_path: 'src/views/billing.html' }], ['Write', { file_path: 'test/billing.test.js' }], ['Bash', { command: 'node --test test/billing.test.js' }], ['Bash', { command: 'node --test' }]],
    run() {
      write('test/billing.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { PLANS, prorate, vat, invoice } from '../src/billing.js';

test('a full month costs the plan price', () => {
  const inv = invoice({ plan: 'starter' });
  assert.equal(inv.net, PLANS.starter.price);
  assert.equal(inv.vat, 225);
  assert.equal(inv.total, 1125);
});

test('a month started late is prorated to the cent', () => {
  assert.equal(prorate(2900, 20, 30), 1933);
  assert.equal(prorate(2900, 30, 30), 2900);
});

test('VAT rounds to the nearest cent', () => {
  assert.equal(vat(2599), 650);
  assert.equal(vat(2598), 650);
  assert.equal(vat(2597), 649);
});

test('extra seats are billed and prorated too', () => {
  const inv = invoice({ plan: 'growth', seats: 3, startedDay: 11, daysInMonth: 30 });
  assert.deepEqual(inv.lines.map((l) => l.amount), [1933, 667]);
  assert.equal(inv.total, 3250);
});

test('an unknown plan is an error', () => {
  assert.throws(() => invoice({ plan: 'gold' }), /unknown plan gold/);
});
`);
    },
  },
  {
    id: 'readme', key: /^# Task: Update the README$/m, pace: 2500,
    short: 'README matches the shop today',
    summary: 'Rewrote the README: the Features list now names what the shop has (card checkout, webhooks, billing, the CSV export) instead of PayPal, PDF export and gift cards, and it runs with `npm start` on `$PORT` and tests with `npm test` (node\'s test runner), not Yarn and Jest.',
    steps: [['Read', { file_path: 'README.md' }], ['Read', { file_path: 'package.json' }], ['Grep', { pattern: 'PORT', path: 'src' }], ['Write', { file_path: 'README.md' }]],
    run() {
      write('README.md', `# acme-shop

The Acme storefront, with the settings pages and exports behind it.

## Features

- Products and a cart
- Card checkout, with free shipping from €50
- Webhooks: Acme calls your URL when an order is paid
- Billing: plans, extra seats and a prorated first month
- Orders export as CSV

## Run it

Needs Node 22. There is nothing to install.

    npm start    # http://localhost:4100, or $PORT
    npm test     # node --test
`);
    },
  },
];

// conflictFiles are the files with conflict markers in the working tree.
function conflictFiles() {
  try {
    return execFileSync('git', ['grep', '-l', '-e', '^<<<<<<< '], { encoding: 'utf8' }).split('\n').filter(Boolean);
  } catch {
    return []; // git grep exits 1 when nothing matches
  }
}

// merge3 resolves one conflict by keeping both sides' changes: ours, less
// the base lines theirs dropped, with the lines theirs added put after the
// line before them.
function merge3(ours, base, theirs) {
  if (!base) return [...ours, ...theirs.filter((l) => !ours.includes(l))];
  const dropped = new Set(base.filter((l) => !theirs.includes(l)));
  const res = ours.filter((l) => !dropped.has(l));
  let at = 0;
  for (const l of theirs) {
    const i = res.indexOf(l, at);
    if (i >= 0) at = i + 1;
    else if (!base.includes(l)) res.splice(at++, 0, l);
  }
  return res;
}

// resolveConflicts rewrites every conflict in file (diff3 markers) with merge3.
function resolveConflicts(file) {
  const out_ = [];
  let part = null; // 'ours', 'base' or 'theirs' inside a conflict
  let ours, base, theirs;
  for (const line of readFileSync(file, 'utf8').split('\n')) {
    if (line.startsWith('<<<<<<< ')) { part = 'ours'; ours = []; base = null; theirs = []; continue; }
    if (part && line.startsWith('||||||| ')) { part = 'base'; base = []; continue; }
    if (part && line === '=======') { part = 'theirs'; continue; }
    if (part && line.startsWith('>>>>>>> ')) { out_.push(...merge3(ours, base, theirs)); part = null; continue; }
    if (part === 'ours') ours.push(line);
    else if (part === 'base') base.push(line);
    else if (part === 'theirs') theirs.push(line);
    else out_.push(line);
  }
  writeFileSync(file, out_.join('\n'));
}

// The demo tasks: a word in the prompt, what Claude "does", and whether
// the run holds (stays Running) while $DEMO_DIR/hold exists.
const tasks = [
  ...videoTasks,
  {
    key: /discount code/i, short: 'Discount codes at checkout, with tests', summary: 'Added discount codes: `src/discounts.js` validates a code and works out the reduction, checkout applies it before shipping, and the tests cover percentage, fixed and expired codes.',
    run() {
      write('src/discounts.js', `// Discount codes customers enter at checkout.

const CODES = {
  WELCOME10: { kind: 'percent', value: 10 },
  SPRING5: { kind: 'fixed', value: 500, minSubtotal: 3000 },
  SUMMER20: { kind: 'percent', value: 20, expires: '2026-09-01' },
};

// findCode returns the discount for code, or null when there is none or it
// has expired.
export function findCode(code, today = new Date()) {
  const d = CODES[String(code || '').trim().toUpperCase()];
  if (!d) return null;
  if (d.expires && today >= new Date(d.expires)) return null;
  return d;
}

// reduction is how many cents discount d takes off a subtotal.
export function reduction(d, subtotal) {
  if (!d || subtotal < (d.minSubtotal || 0)) return 0;
  const off = d.kind === 'percent' ? Math.round((subtotal * d.value) / 100) : d.value;
  return Math.min(off, subtotal);
}
`);
      edit('src/checkout.js', "import { chargeCard } from './payments.js';\n",
        "import { chargeCard } from './payments.js';\nimport { findCode, reduction } from './discounts.js';\n");
      edit('src/checkout.js', `export async function checkout(cart, payment) {
  if (cart.items.length === 0) throw new Error('the cart is empty');
  const amount = total(cart, { shipping: shippingFor(cart) });
  const receipt = await chargeCard(payment, amount);
  return { amount, receipt, items: cart.items.map((i) => ({ ...i })) };
}`, `export async function checkout(cart, payment, { code } = {}) {
  if (cart.items.length === 0) throw new Error('the cart is empty');
  const discount = code ? findCode(code) : null;
  if (code && !discount) throw new Error(\`unknown or expired code \${code}\`);
  const off = reduction(discount, subtotal(cart));
  const amount = total(cart, { shipping: shippingFor(cart) }) - off;
  const receipt = await chargeCard(payment, amount);
  return { amount, discount: off, receipt, items: cart.items.map((i) => ({ ...i })) };
}`);
      write('test/discounts.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { findCode, reduction } from '../src/discounts.js';

test('a percentage code', () => {
  assert.equal(reduction(findCode('welcome10'), 4990), 499);
});

test('a fixed code needs its minimum', () => {
  const d = findCode('SPRING5');
  assert.equal(reduction(d, 2000), 0);
  assert.equal(reduction(d, 3000), 500);
});

test('an expired code', () => {
  assert.equal(findCode('SUMMER20', new Date('2026-10-01')), null);
});
`);
    },
  },
  {
    key: /confirmation email/i, short: 'Confirmation email once an order is paid', summary: 'Orders now send a confirmation email with the items, the total and the delivery address.',
    run() {
      write('src/emails/order-confirmation.js', `// The email a customer gets once their order is paid.
import { formatPrice } from '../format.js';

export function orderConfirmation(order) {
  const lines = order.items.map((i) => \`\${i.qty} × \${i.name}  \${formatPrice(i.price * i.qty)}\`);
  return {
    to: order.email,
    subject: \`Your Acme order #\${order.id}\`,
    text: ['Thanks for your order!', '', ...lines, '', \`Total: \${formatPrice(order.amount)}\`].join('\\n'),
  };
}
`);
      edit('src/checkout.js', '// Checkout: shipping, the total and the payment.', '// Checkout: shipping, the total, the payment and the confirmation email.');
      edit('src/checkout.js', "import { chargeCard } from './payments.js';\n",
        "import { chargeCard } from './payments.js';\nimport { sendMail } from './mail.js';\nimport { orderConfirmation } from './emails/order-confirmation.js';\n");
      edit('src/checkout.js', '  const receipt = await chargeCard(payment, amount);\n',
        '  const receipt = await chargeCard(payment, amount);\n  await sendMail(orderConfirmation({ id: receipt.id, email: payment.email, amount, items: cart.items }));\n');
    },
  },
  {
    key: /search/i, short: 'Search on name and description', summary: 'Added product search on name and description, ranked by matches in the name first.',
    run() {
      edit('src/products.js', '// findProduct', `// search returns the products whose name or description has every word
// of query, those matching in the name first.
export function search(query) {
  const words = query.toLowerCase().split(/\\s+/).filter(Boolean);
  const hits = PRODUCTS.filter((p) => words.every((w) => (p.name + ' ' + p.description).toLowerCase().includes(w)));
  return hits.sort((a, b) => score(b, words) - score(a, words));
}

function score(p, words) {
  return words.filter((w) => p.name.toLowerCase().includes(w)).length;
}

// findProduct`);
    },
  },
  {
    key: /stock level/i, short: 'Stock level on every product page', summary: 'Product pages show "In stock", "Only 3 left" or "Out of stock".',
    run() {
      edit('src/views/product.html', '  <p class="price">{{price}}</p>\n', '  <p class="price">{{price}}</p>\n  <p class="stock {{stockClass}}">{{stockText}}</p>\n');
    },
  },
  {
    key: /rounding/i, short: 'Line totals rounded once per line', summary: 'Line totals are rounded once per line, so a cart of 3 × €3.33 is €9.99.',
    run() {
      edit('src/cart.js', 'sum + i.price * i.qty', 'sum + Math.round(i.price * i.qty)');
    },
  },
  {
    key: /cache the product list/i, short: 'Product list cached for 60 s', summary: 'The product list is cached in memory for 60 seconds.',
    run() {
      write('src/cache.js', `// A small time-limited cache.
export function cached(fn, ttlMs) {
  let value, at = 0;
  return () => {
    if (Date.now() - at > ttlMs) { value = fn(); at = Date.now(); }
    return value;
  };
}
`);
    },
  },
  {
    key: /lazy-load/i, short: 'Images below the fold load lazily', summary: 'Product images below the fold load lazily.',
    run() {
      edit('src/views/product.html', '<img src="{{image}}"', '<img loading="lazy" decoding="async" src="{{image}}"');
    },
  },
  {
    // The task scripts/tutorials/first-task.yaml adds: it works slowly, so
    // the video can show it running.
    key: /free shipping/i, slow: true, title: 'Free-shipping hint in the cart', short: 'Cart says how far free shipping is',
    summary: 'The cart now says how much more is needed for free shipping, e.g. "Add €21.00 more for free shipping", and the tests cover it.',
    run() {
      edit('src/checkout.js', 'const FREE_SHIPPING_FROM = 5000;\n', `const FREE_SHIPPING_FROM = 5000;

// freeShippingHint tells the customer how far the cart is from free
// shipping, or returns '' once it ships free.
export function freeShippingHint(cart) {
  const missing = FREE_SHIPPING_FROM - subtotal(cart);
  return missing > 0 ? \`Add \${formatPrice(missing)} more for free shipping\` : '';
}
`);
      edit('src/checkout.js', "import { chargeCard } from './payments.js';\n",
        "import { chargeCard } from './payments.js';\nimport { formatPrice } from './format.js';\n");
      write('test/shipping.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createCart, addItem } from '../src/cart.js';
import { freeShippingHint } from '../src/checkout.js';

test('the hint says how much is missing', () => {
  assert.equal(freeShippingHint(addItem(createCart(), 'TEE-02')), 'Add €21.00 more for free shipping');
});

test('no hint once shipping is free', () => {
  assert.equal(freeShippingHint(addItem(createCart(), 'TEE-02', 2)), '');
});
`);
    },
  },
  { key: /wishlist/i, hold: true, steps: [['Read', { file_path: 'src/cart.js' }], ['Grep', { pattern: 'customerId', path: 'src' }], ['Write', { file_path: 'src/wishlist.js' }], ['Bash', { command: 'node --test test/wishlist.test.js' }]] },
  { key: /order history/i, hold: true, steps: [['Read', { file_path: 'src/orders.js' }], ['Edit', { file_path: 'src/orders.js' }], ['Bash', { command: 'node --test' }]] },
  { key: /inline the critical css/i, hold: true, steps: [['Read', { file_path: 'src/views/layout.html' }], ['Bash', { command: 'npx lighthouse http://localhost:4100 --only-categories=performance --quiet' }]] },
];

const sessionOf = (t) => `demo-${t.id || tasks.indexOf(t)}`;
const gameSessionOf = (t) => `game-${game.tasks.indexOf(t)}`;

// steps shows tool calls pace ms apart: [name, input, output?]. FAKECLAUDE_PACE_MS
// overrides the pace (0 for tests, more to slow a rehearsal down).
async function steps(list, pace) {
  if (process.env.FAKECLAUDE_PACE_MS) pace = Number(process.env.FAKECLAUDE_PACE_MS);
  for (const [name, input, output] of list) {
    await sleep(pace);
    tool(name, input, output || 'ok');
  }
  await sleep(pace);
}

async function task() {
  // A resumed run gets only the new notes: its task is its session's.
  const resumed = has('--resume');
  const own = resumed && tasks.find((x) => sessionOf(x) === session);
  // A Star Catcher task (starcatcher.mjs): its own instructions come first;
  // what Dash adds after them (other tasks, history) can name other tasks'
  // words, so the earliest match wins.
  const at = (x) => { const i = stdin.search(x.key); return i < 0 ? Infinity : i; };
  const g = !own && ((resumed && game.tasks.find((x) => gameSessionOf(x) === session))
    || game.tasks.filter((x) => at(x) < Infinity).sort((a, b) => at(a) - at(b))[0]);
  const t = !g && (own || tasks.find((x) => x.key.test(stdin)));
  if (g) session = gameSessionOf(g);
  else if (t) session = sessionOf(t);
  out({ type: 'system', subtype: 'init', session_id: session });
  usage();
  // The merge queue sent it back: main conflicts with it.
  if (/merging it into your work conflicts in|conflict markers are still in the code/.test(stdin)) {
    const files = conflictFiles();
    if (g) {
      // Star Catcher's tasks add lines side by side: keep both, ours first.
      say('Main has moved on. I\'ll resolve the conflicts so both changes keep working.');
      for (const f of files) {
        tool('Read', { file_path: f }, '…');
        await sleep(1500);
        writeFileSync(f, game.keepBoth(readFileSync(f, 'utf8')));
        tool('Edit', { file_path: f }, 'ok');
      }
      tool('Bash', { command: 'node --test' }, '# pass');
      const text = 'Resolved the conflict: both changes are kept, and the tests pass.';
      say(text);
      return result(text, 0.12, 4);
    }
    say(`\`main\` moved on under this task. I'll resolve the conflicts in ${files.join(', ')}, keeping both changes.`);
    await steps(files.map((f) => ['Read', { file_path: f }]), t?.pace || 500);
    files.forEach(resolveConflicts);
    await steps([...files.map((f) => ['Edit', { file_path: f }]), ['Bash', { command: 'node --test' }]], t?.pace || 500);
    const text = `Resolved the conflicts in ${files.join(', ')}: both this task's change and the one now on main are kept.`;
    say(text);
    return result(text, 0.12, 5);
  }
  if (g) return gameTask(g);
  // Sent back with notes (Rework…): its rework, if it has one.
  const rework = t?.rework && (resumed ? !stdin.includes('You were interrupted') : stdin.includes('## Follow-up notes'));
  if (rework) {
    say('Thanks for the picture: I\'ll fix that.');
    await steps(t.rework.steps, t.pace || 500);
    t.rework.run();
    await steps([['Bash', { command: 'node --test' }, '# pass\n# fail 0']], t.pace || 500);
    say(t.rework.summary);
    return result(t.rework.summary, 0.09, 4);
  }
  if (t?.steps && !t.hold) {
    say('I\'ll read the relevant code, make the change and run the tests.');
    await steps(t.steps.slice(0, -1), t.pace);
    t.run();
    await steps(t.steps.slice(-1), t.pace);
    say(t.summary);
    return result(t.summary);
  }
  if (!t) {
    say('Nothing to do for this one.');
    result('Nothing to change.', 0.05, 2);
    return;
  }
  if (t.hold) {
    say('Let me look at how the code is laid out first.');
    for (const [name, input] of t.steps) {
      tool(name, input, 'ok');
      await sleep(300);
    }
    while (existsSync(`${demo}/hold`)) await sleep(500);
    result('Done.', 0.3, 9);
    return;
  }
  // A slow task takes a few seconds per step, so its run can be watched.
  const pause = () => sleep(t.slow ? 2000 : 0);
  say('I\'ll read the relevant code, make the change and run the tests.');
  await pause();
  tool('Read', { file_path: 'src/checkout.js' }, '…');
  await pause();
  tool('Grep', { pattern: 'total\\(', path: 'src' }, 'src/cart.js\nsrc/checkout.js');
  await pause();
  t.run();
  if (t.slow) tool('Edit', { file_path: 'src/checkout.js' }, 'ok');
  await pause();
  tool('Bash', { command: 'node --test' }, '# tests 9\n# pass 9\n# fail 0');
  await pause();
  say(t.summary);
  result(t.summary);
}

// gameTask runs one of Star Catcher's tasks (starcatcher.mjs), or its rework.
async function gameTask(t) {
  const re = t.rework && existsSync(t.rework.applied) ? t.rework : null;
  const pause = () => sleep(t.slow ? 1800 : 300);
  const touched = [];
  const w = (f, text, mode) => { touched.push([f, existsSync(f)]); write(f, text, mode); };
  const e = (f, a, b) => { edit(f, a, b); touched.push([f, true]); };
  say(re ? 'I\'ll go through your notes and change what they ask for.' : 'I\'ll read the relevant code, make the change and run the tests.');
  await pause();
  for (const f of ['src/game.js', 'src/main.js'].filter(existsSync)) {
    tool('Read', { file_path: f }, '…');
    await pause();
  }
  (re || t).run(w, e);
  for (const [f, had] of new Map(touched.map(([f, had]) => [f, had]))) {
    tool(had ? 'Edit' : 'Write', { file_path: f }, 'ok');
    await pause();
  }
  tool('Bash', { command: 'node --test' }, '# pass');
  await pause();
  const text = (re || t).summary;
  say(text);
  result(text);
}

// The coordinator's plans, by a word in the goal's title.
const plans = [
  {
    goal: 'Checkout in under 2 seconds',
    why: 'Checkout spends most of its time fetching the product list and rendering blocking CSS. Cache the list first, then trim what the page loads, then measure.',
    tasks: [
      { title: 'Cache the product list', prompt: 'Cache the product list in memory for 60 seconds, so checkout doesn\'t load it on every request.' },
      { title: 'Lazy-load product images', prompt: 'Lazy-load product images below the fold on the product and checkout pages.' },
      { title: 'Inline the critical CSS', prompt: 'Inline the critical CSS of the checkout page and load the rest without blocking rendering.', after: [0] },
      { title: 'Add a checkout timing test', prompt: 'Add a test that fails when the checkout page takes more than 2 seconds to render with 200 products.', after: [1, 2] },
    ],
  },
  {
    goal: 'Accessible checkout',
    why: 'The checkout form has unlabelled fields, low-contrast errors and a card field that traps the keyboard. Fix the form first, then the errors, then check the whole flow.',
    tasks: [
      { title: 'Label every checkout field', prompt: 'Give every field of the checkout form a visible label tied to it, and group the address fields.' },
      { title: 'Announce form errors', prompt: 'Show checkout errors next to their field, with 4.5:1 contrast, and announce them to screen readers.' },
      { title: 'Keyboard-only checkout test', prompt: 'Add a test that completes checkout with the keyboard alone.', after: [0, 1] },
    ],
  },
  {
    goal: 'Product search',
    why: 'One task: search the name and description, then rank name matches first.',
    tasks: [{ title: 'Add product search', prompt: 'Add product search: match every word of the query against the name and description, name matches first.' }],
  },
];

// A chat turn: a goal's coordinator, or a plain chat.
async function chat() {
  const done = (text) => {
    out({ type: 'stream_event', event: { type: 'message_start' } });
    out({ type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text } } });
    result(text, 0.08, 3);
  };
  out({ type: 'system', subtype: 'init', session_id: 'demo-coordinator' });
  // Only the newest message counts: the goal's state, then what woke it.
  const last = stdin.slice(Math.max(0, stdin.lastIndexOf('<goal id=')));
  if (last.includes('propose a plan with propose_plan')) {
    const plan = game.plans.find((p) => p.goal.test(last.slice(0, last.indexOf('</goal>') + 1 || undefined)))
      || plans.find((p) => last.includes(p.goal)) || plans[0];
    await callTool('dash_board', 'propose_plan', { why: plan.why, tasks: plan.tasks });
    return done(`I've proposed a plan of ${plan.tasks.length} tasks.`);
  }
  // What woke it comes after the goal's state (whose log repeats old wake-ups).
  const wake = last.slice(last.indexOf('</goal>'));
  let woke = false;
  for (const [, id, title] of wake.matchAll(/Task (\w+) \("([^"]*)"\) entered review\./g)) {
    await callTool('dash_board', 'post_review_note', { todo: id, note: 'Checked the diff and the test run: the change is small and does what the plan asked. Fine to merge.' });
    await callTool('dash_board', 'add_log', { todo: id, text: `Reviewed "${title}": looks right.`, why: 'Tests pass and the diff stays within the task.' });
    woke = true;
  }
  for (const [, id, title] of wake.matchAll(/Task (\w+) \("([^"]*)"\) was merged\./g)) {
    await callTool('dash_board', 'add_log', { todo: id, text: `"${title}" is merged; the tasks after it can start.` });
    woke = true;
  }
  // Every task merged: the goal is done.
  const statuses = [...last.matchAll(/<task id="\w+" status="(\w+)"/g)].map((m) => m[1]);
  if (woke && statuses.length && statuses.every((s) => s === 'merged')) {
    await callTool('dash_board', 'finish_goal', { summary: 'Everything in the plan is merged.' });
  }
  // The project chat (no goal): the game's question about phones drafts a task.
  if (!woke && !stdin.includes('<goal id=') && game.chat.key.test(stdin.slice(-2000))) {
    const servers = JSON.parse(args[args.indexOf('--mcp-config') + 1] || '{}').mcpServers || {};
    const server = servers.dash_board ? 'dash_board' : Object.keys(servers).find((n) => /dash/.test(n)) || Object.keys(servers)[0];
    tool('Read', { file_path: 'src/input.js' }, '…');
    // The call and its result go in the stream too: Dash shows the task it
    // made as a card under the call (agent.CreatedTask reads the result).
    const input = { ...game.chat.task, draft: true };
    const id = `toolu_demo_${++toolN}`;
    out({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name: `mcp__${server}__create_todo`, input }] } });
    let text, isError = false;
    try {
      text = mcpText(await callTool(server, 'create_todo', input));
    } catch (e) {
      text = String(e.message || e);
      isError = true;
    }
    out({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, content: text, is_error: isError }] } });
    return done(isError ? `I couldn't draft the task: ${text}` : game.chat.answer);
  }
  done(woke ? 'Done for now.' : 'Sure: what would you like to change?');
}

// A short call: a title, or the summary of a finished task's report.
// A task with a title of its own is named by its prompt.
const all = [...game.tasks, ...tasks];
const small = () => console.log(all.find((t) => t.summary && stdin.includes(t.summary.slice(0, 40)))?.short
  || all.find((t) => t.rework && stdin.includes(t.rework.summary.slice(0, 40)))?.short
  || (stdin.includes('Lined the Save button up') && 'Save button lined up with the field')
  || (stdin.includes('Resolved the conflicts in') && 'Conflicts with main resolved')
  || (stdin.includes('Resolved the conflict') && game.tasks.map((t) => [stdin.search(t.key), t]).filter(([i]) => i >= 0).sort((a, b) => a[0] - b[0])[0]?.[1].short)
  || all.find((t) => t.title && t.key.test(stdin))?.title || 'Demo task');

// planTurn is one round of a brief's interview (agent.PlanInput in, JSON
// out): the game's project brief, or its levels feature.
function planTurn() {
  const plan = JSON.parse(stdin.slice(stdin.indexOf('{')));
  const iv = plan.feature_name || (plan.topics || []).some((t) => t.id === 'problem') ? game.interviews.feature : game.interviews.project;
  const n = Math.min(plan.rounds.length, iv.rounds.length - 1);
  const given = (id) => plan.rounds.flatMap((r) => r.questions || []).find((q) => q.id === id)?.answer;
  const answer = (id) => {
    const a = given(id);
    if (a?.text) return a.text;
    return iv.rounds.flatMap((r) => r.questions).find((q) => q.id === id)?.default || '';
  };
  const settled = new Set(iv.rounds.slice(0, n + 1).flatMap((r) => r.settled));
  const r = iv.rounds[n];
  console.log(JSON.stringify({
    note: r.note,
    questions: r.questions,
    topics: iv.topics.map((id) => ({ id, status: settled.has(id) ? 'done' : 'open' })),
    brief: iv.brief(answer),
    open_questions: r.ready ? [] : [iv.open],
    verdict: r.ready
      ? { ready: true, summary: 'The brief settles what to build and how to tell it works.', can_wait: [iv.open] }
      : { ready: false, summary: 'A few decisions are still open.', can_wait: [] },
  }));
}

try {
  if (has('text') && stdin.includes('The plan so far:')) planTurn();
  else if (has('text')) small();
  else if (has('--include-partial-messages')) await chat();
  else await task();
} catch (e) {
  // Say it where the task's timeline shows it, too.
  say(`fakeclaude: ${e.stack || e}`);
  console.error(String(e));
  process.exit(1);
}
