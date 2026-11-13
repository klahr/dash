#!/usr/bin/env node
// Fills a demo Dash (see README.md here) with the acme-shop project's tasks
// in every lane, and a goal, for the landing page's screenshots.
//
//   node seed.mjs http://127.0.0.1:7791 <project id>
import { writeFileSync } from 'node:fs';

const [base, pid] = process.argv.slice(2);
const demo = process.env.DEMO_DIR || '/tmp/landing-demo';
let cookie = '';

async function api(method, path, body) {
  const res = await fetch(base + path, {
    method, headers: { 'Content-Type': 'application/json', Cookie: cookie },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const set = res.headers.get('set-cookie');
  if (set) cookie = set.split(';')[0];
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, fn, secs = 90) {
  for (let i = 0; i < secs * 2; i++) {
    const v = await fn();
    if (v) return v;
    await sleep(500);
  }
  throw new Error(`timed out waiting for ${what}`);
}
const todo = (id) => api('GET', `/api/todos/${id}`);
const status = async (id, want) => until(`${id} to be ${want}`, async () => (await todo(id)).status === want);
const add = (title, prompt, opts = {}) => api('POST', `/api/projects/${pid}/todos`, { title, prompt, ...opts });

// mergeTask queues a task, approves it in review and waits for the merge.
async function mergeTask(title, prompt) {
  const t = await add(title, prompt, { enqueue: true });
  await status(t.id, 'review');
  await api('POST', `/api/todos/${t.id}/approve`, {});
  await status(t.id, 'merged');
  return t;
}

await api('POST', '/api/login', { username: process.env.DASH_USERNAME || 'demo', password: process.env.DASH_PASSWORD || 'demo-password-for-shots' });
writeFileSync(`${demo}/hold`, '');

// Done. The first task also makes the runner's clone, which goals need.
await mergeTask('Show stock levels on product pages', 'Show the stock level on product pages: "In stock", "Only 3 left" below 5, or "Out of stock".');
await mergeTask('Fix rounding in cart totals', 'Cart totals are off by a cent with some quantities: fix the rounding in the cart.');

// A goal that is done: queued on its own, one task, merged.
const search = await api('POST', '/api/goals', {
  project: pid, title: 'Product search', description: 'Customers can search the catalogue by name and description.', mode: 'queue',
});
const searchTask = await until('the search task', async () => ((await api('GET', `/api/goals/${search.id}`)).tasks || [])[0]);
await status(searchTask.id, 'review');
await sleep(2000);
await api('POST', `/api/todos/${searchTask.id}/approve`, {});
await status(searchTask.id, 'merged');
await until('the search goal to be done', async () => (await api('GET', `/api/goals/${search.id}`)).status === 'done');

// A goal: its coordinator plans four tasks, queued at once.
const goal = await api('POST', '/api/goals', {
  project: pid, title: 'Checkout in under 2 seconds',
  description: 'The checkout page takes 3.4 s to render on a mid-range phone. Get it under 2 s without dropping features.',
  mode: 'ask', budget: { max_tasks: 8, weekly_share: 40 },
});
await until('the goal\'s plan', async () => ((await api('GET', `/api/goals/${goal.id}`)).plan || []).length > 0);
await api('POST', `/api/goals/${goal.id}/plan/accept`, { queue: true });
const goalTasks = async () => (await api('GET', `/api/goals/${goal.id}`)).tasks || [];
const byTitle = async (title) => until(title, async () => (await goalTasks()).find((t) => t.title === title));
const cache = await byTitle('Cache the product list');
await status(cache.id, 'review');
await sleep(3000); // the coordinator reviews it
await api('POST', `/api/todos/${cache.id}/approve`, {});
await status(cache.id, 'merged');
const lazy = await byTitle('Lazy-load product images');
await status(lazy.id, 'review');

// Review.
for (const [title, prompt] of [
  ['Send order confirmation emails', 'Send an order confirmation email once an order is paid, with the items, the total and the delivery address.'],
  ['Add discount codes at checkout', 'Let customers enter a discount code at checkout. Support percentage and fixed codes, with a minimum order and an expiry date. Add tests.'],
]) {
  const t = await add(title, prompt, { enqueue: true });
  await status(t.id, 'review');
}

// Running (held), then queued behind them.
for (const [title, prompt] of [
  ['Add a wishlist', 'Add a wishlist: signed-in customers can save products for later and move them to the cart.'],
  ['Paginate the order history', 'The order history page loads every order at once: paginate it, 20 orders a page.'],
]) {
  const t = await add(title, prompt, { enqueue: true });
  await status(t.id, 'in_progress');
}
for (const [title, prompt] of [
  ['Dark mode for the storefront', 'Add a dark theme to the storefront that follows the system setting.'],
  ['Add a sitemap.xml', 'Serve /sitemap.xml with every product page and category.'],
]) await add(title, prompt, { enqueue: true });

// Drafts.
for (const [title, prompt] of [
  ['Migrate product images to WebP', 'Convert product images to WebP at upload and serve them with a JPEG fallback.'],
  ['Rate-limit the sign-in form', 'Rate-limit sign-in attempts per address and per account.'],
]) await add(title, prompt);

// A goal whose plan waits for you.
const a11y = await api('POST', '/api/goals', {
  project: pid, title: 'Accessible checkout',
  description: 'Checkout meets WCAG 2.2 AA: labelled fields, readable errors, and it works with the keyboard alone.',
  mode: 'ask', budget: { max_tasks: 5 },
});
await until('its plan', async () => ((await api('GET', `/api/goals/${a11y.id}`)).plan || []).length > 0);

console.log(JSON.stringify({ goal: goal.id }));
