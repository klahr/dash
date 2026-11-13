#!/usr/bin/env node
// A stand-in for Claude Code, for the landing page's screenshots: it answers
// the way Dash calls `claude -p` and makes a canned change for each demo task
// (keyed by a word in the task's prompt), so the board fills with real
// diffs, test runs and timelines without a Claude subscription.
//
// A task whose prompt matches a "hold" entry keeps running while
// $DEMO_DIR/hold exists. Every run reports the usage windows Claude Code
// would (rate_limit_event), so the usage meters have something to show.
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { dirname } from 'node:path';

const args = process.argv.slice(2);
const has = (a) => args.includes(a);
const demo = process.env.DEMO_DIR || '/tmp';

if (has('--version')) {
  console.log('2.1.0 (Claude Code)');
  process.exit(0);
}
const stdin = readFileSync(0, 'utf8');
if (!stdin.trim()) {
  console.error('Error: Input must be provided either through stdin or as a prompt argument when using --print');
  process.exit(1);
}

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
  total_cost_usd: cost, session_id: 'demo-session',
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

function edit(file, find, replace) {
  const s = readFileSync(file, 'utf8');
  if (!s.includes(find)) throw new Error(`${file}: no ${JSON.stringify(find)}`);
  writeFileSync(file, s.replace(find, replace));
}
function write(file, text) {
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, text);
}

// The demo tasks: a word in the prompt, what Claude "does", and whether
// the run holds (stays Running) while $DEMO_DIR/hold exists.
const tasks = [
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
  { key: /wishlist/i, hold: true, steps: [['Read', { file_path: 'src/cart.js' }], ['Grep', { pattern: 'customerId', path: 'src' }], ['Write', { file_path: 'src/wishlist.js' }], ['Bash', { command: 'node --test test/wishlist.test.js' }]] },
  { key: /order history/i, hold: true, steps: [['Read', { file_path: 'src/orders.js' }], ['Edit', { file_path: 'src/orders.js' }], ['Bash', { command: 'node --test' }]] },
  { key: /inline the critical css/i, hold: true, steps: [['Read', { file_path: 'src/views/layout.html' }], ['Bash', { command: 'npx lighthouse http://localhost:4100 --only-categories=performance --quiet' }]] },
];

async function task() {
  out({ type: 'system', subtype: 'init', session_id: 'demo-session' });
  usage();
  const t = tasks.find((x) => x.key.test(stdin));
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
  say('I\'ll read the relevant code, make the change and run the tests.');
  tool('Read', { file_path: 'src/checkout.js' }, '…');
  tool('Grep', { pattern: 'total\\(', path: 'src' }, 'src/cart.js\nsrc/checkout.js');
  t.run();
  tool('Bash', { command: 'node --test' }, '# tests 9\n# pass 9\n# fail 0');
  say(t.summary);
  result(t.summary);
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
    const plan = plans.find((p) => last.includes(p.goal)) || plans[0];
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
  done(woke ? 'Done for now.' : 'Sure: what would you like to change?');
}

// A short call: a title, or the summary of a finished task's report.
const small = () => console.log(tasks.find((t) => t.summary && stdin.includes(t.summary.slice(0, 40)))?.short || 'Demo task');

try {
  if (has('text')) small();
  else if (has('--include-partial-messages')) await chat();
  else await task();
} catch (e) {
  console.error(String(e));
  process.exit(1);
}
