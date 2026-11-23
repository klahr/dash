#!/bin/sh
# Makes the demo project for the landing page's screenshots and the demo
# video: a small web shop, acme-shop, as a git repository in $1.
#
# The second commit adds what the demo video's five tasks need (see
# README.md here): a webhook settings page whose URL field has no
# validation and whose Save button sits too low, an orders export that
# writes every date a month early, a billing page with no tests, a
# stylesheet with hard-coded colours, and a README that is out of date.
# `npm start` serves it all on $PORT, and `node --test` passes.
set -eu
dir=$1
mkdir -p "$dir/src/views" "$dir/test" "$dir/public"
cd "$dir"
git init -q -b main
git config user.name "Demo"
git config user.email "demo@example.com"
git config commit.gpgsign false
git config tag.gpgsign false

cat > package.json <<'EOF'
{
  "name": "acme-shop",
  "private": true,
  "type": "module",
  "scripts": { "start": "node src/server.js", "test": "node --test" }
}
EOF

cat > README.md <<'EOF'
# acme-shop

The Acme storefront.

## Features

- Products and a cart
- PayPal checkout
- Light theme only: dark mode is planned
- Order export as PDF
- Gift cards

## Run it

Needs Node 14 and Yarn.

    yarn install
    yarn dev     # http://localhost:3000
    yarn test    # Jest
EOF

cat > TODO.md <<'EOF'
# TODO

## Next

- [ ] Gift cards at checkout
- [ ] Show delivery estimates on the product page
- [ ] Rate-limit the sign-in form
- [x] Product search

## Later

- [ ] Reviews and ratings
- [ ] Export orders as CSV
EOF

cat > src/products.js <<'EOF'
// The catalogue. Prices are in cents.
export const PRODUCTS = [
  { sku: 'MUG-01', name: 'Enamel mug', description: 'A camping mug that survives the dishwasher', price: 1490, stock: 42 },
  { sku: 'TEE-02', name: 'Organic tee', description: 'A heavy cotton t-shirt', price: 2900, stock: 3 },
  { sku: 'BAG-03', name: 'Canvas tote', description: 'A tote bag with an inside pocket', price: 1900, stock: 0 },
  { sku: 'CAP-04', name: 'Wool cap', description: 'A knitted cap for cold mornings', price: 2400, stock: 17 },
];

// findProduct returns the product with this SKU, or undefined.
export function findProduct(sku) {
  return PRODUCTS.find((p) => p.sku === sku);
}
EOF

cat > src/cart.js <<'EOF'
// The shopping cart: line items, quantities and totals.
import { findProduct } from './products.js';

export function createCart() {
  return { items: [] };
}

export function addItem(cart, sku, qty = 1) {
  const product = findProduct(sku);
  if (!product) throw new Error(`unknown product ${sku}`);
  const line = cart.items.find((i) => i.sku === sku);
  if (line) line.qty += qty;
  else cart.items.push({ sku, name: product.name, price: product.price, qty });
  return cart;
}

export function removeItem(cart, sku) {
  cart.items = cart.items.filter((i) => i.sku !== sku);
  return cart;
}

export function subtotal(cart) {
  return cart.items.reduce((sum, i) => sum + i.price * i.qty, 0);
}

export function total(cart, { shipping = 0 } = {}) {
  return subtotal(cart) + shipping;
}
EOF

cat > src/checkout.js <<'EOF'
// Checkout: shipping, the total and the payment.
import { subtotal, total } from './cart.js';
import { chargeCard } from './payments.js';

const FREE_SHIPPING_FROM = 5000;

export function shippingFor(cart) {
  return subtotal(cart) >= FREE_SHIPPING_FROM ? 0 : 495;
}

export async function checkout(cart, payment) {
  if (cart.items.length === 0) throw new Error('the cart is empty');
  const amount = total(cart, { shipping: shippingFor(cart) });
  const receipt = await chargeCard(payment, amount);
  return { amount, receipt, items: cart.items.map((i) => ({ ...i })) };
}
EOF

cat > src/payments.js <<'EOF'
// The payment provider. In development every charge succeeds.
export async function chargeCard(payment, amount) {
  if (!payment || !payment.token) throw new Error('no payment method');
  return { id: `ch_${Date.now().toString(36)}`, amount };
}
EOF

cat > src/mail.js <<'EOF'
// Outgoing email. In development it is printed instead of sent.
export async function sendMail(msg) {
  console.log(`mail to ${msg.to}: ${msg.subject}`);
}
EOF

cat > src/format.js <<'EOF'
export function formatPrice(cents) {
  return `€${(cents / 100).toFixed(2)}`;
}
EOF

cat > src/views/product.html <<'EOF'
<article class="product">
  <img src="{{image}}" alt="{{name}}">
  <h1>{{name}}</h1>
  <p class="price">{{price}}</p>
  <button class="add">Add to cart</button>
</article>
EOF

cat > src/server.js <<'EOF'
import { createServer } from 'node:http';
import { PRODUCTS } from './products.js';

const port = process.env.PORT || 4100;
createServer((req, res) => {
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify(PRODUCTS));
}).listen(port, () => console.log(`acme-shop on http://localhost:${port}`));
EOF

cat > test/cart.test.js <<'EOF'
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createCart, addItem, subtotal } from '../src/cart.js';
import { shippingFor, checkout } from '../src/checkout.js';

test('adding the same product twice adds to its quantity', () => {
  const cart = addItem(addItem(createCart(), 'MUG-01'), 'MUG-01');
  assert.equal(cart.items.length, 1);
  assert.equal(subtotal(cart), 2980);
});

test('free shipping from €50', () => {
  assert.equal(shippingFor(addItem(createCart(), 'MUG-01')), 495);
  assert.equal(shippingFor(addItem(createCart(), 'TEE-02', 2)), 0);
});

test('checkout charges the total', async () => {
  const r = await checkout(addItem(createCart(), 'CAP-04'), { token: 'tok_test' });
  assert.equal(r.amount, 2895);
});
EOF

git add -A
git commit -q -m "acme-shop: products, cart and checkout"

# The second commit: webhooks, billing and the orders export, served as
# pages. The README isn't updated (the demo's "Update the README" task).

cat > src/server.js <<'EOF'
// The shop's web server: the storefront, the settings pages and the orders
// export. It listens on $PORT (4100 when it isn't set).
import { createServer } from 'node:http';
import { readFileSync } from 'node:fs';
import { PRODUCTS } from './products.js';
import { formatPrice } from './format.js';
import { webhookUrl, setWebhookUrl } from './webhooks.js';
import { invoice } from './billing.js';
import { ORDERS } from './orders.js';
import { ordersCsv } from './export.js';

const file = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const fill = (tpl, vars) => tpl.replace(/{{(\w+)}}/g, (_, k) => vars[k] ?? '');
const escape = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]);

function page(res, title, body, css = []) {
  res.setHeader('Content-Type', 'text/html; charset=utf-8');
  const links = css.map((c) => `<link rel="stylesheet" href="/${c}">`).join('\n  ');
  res.end(fill(file('./views/layout.html'), { title, body, css: links }));
}

function form(req) {
  return new Promise((resolve) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => resolve(new URLSearchParams(body)));
  });
}

const routes = {
  'GET /': (req, res) => page(res, 'Shop', `<h1>Shop</h1>\n<div class="products">${PRODUCTS.map((p) => `
  <article class="card"><h2>${escape(p.name)}</h2><p>${escape(p.description)}</p><p class="price">${formatPrice(p.price)}</p></article>`).join('')}
</div>`),
  'GET /settings/webhooks': (req, res) => page(res, 'Webhooks',
    fill(file('./views/webhooks.html'), { url: escape(webhookUrl()), saved: '' }), ['webhooks.css']),
  'POST /settings/webhooks': async (req, res) => {
    setWebhookUrl((await form(req)).get('url') || '');
    page(res, 'Webhooks', fill(file('./views/webhooks.html'), { url: escape(webhookUrl()), saved: '<p class="notice">Saved.</p>' }), ['webhooks.css']);
  },
  'GET /billing': (req, res) => {
    const inv = invoice({ plan: 'growth', seats: 3, startedDay: 11, daysInMonth: 30 });
    const rows = inv.lines.map((l) => `<tr><td>${escape(l.label)}</td><td class="num">${formatPrice(l.amount)}</td></tr>`).join('\n      ');
    page(res, 'Billing', fill(file('./views/billing.html'), {
      rows, net: formatPrice(inv.net), vat: formatPrice(inv.vat), total: formatPrice(inv.total),
    }));
  },
  'GET /export/orders.csv': (req, res) => {
    res.setHeader('Content-Type', 'text/csv; charset=utf-8');
    res.end(ordersCsv(ORDERS));
  },
  'GET /products.json': (req, res) => {
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify(PRODUCTS));
  },
  'GET /style.css': (req, res) => { res.setHeader('Content-Type', 'text/css'); res.end(file('../public/style.css')); },
  'GET /webhooks.css': (req, res) => { res.setHeader('Content-Type', 'text/css'); res.end(file('../public/webhooks.css')); },
};

const port = process.env.PORT || 4100;
createServer(async (req, res) => {
  const route = routes[`${req.method} ${new URL(req.url, 'http://x').pathname}`];
  if (!route) {
    res.statusCode = 404;
    return page(res, 'Not found', '<h1>Not found</h1>');
  }
  await route(req, res);
}).listen(port, () => console.log(`acme-shop on http://localhost:${port}`));
EOF

cat > src/views/layout.html <<'EOF'
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{title}} · Acme</title>
  <link rel="stylesheet" href="/style.css">
  {{css}}
</head>
<body>
  <header class="top">
    <a class="logo" href="/">Acme</a>
    <nav>
      <a href="/">Shop</a>
      <a href="/billing">Billing</a>
      <a href="/settings/webhooks">Webhooks</a>
      <a href="/export/orders.csv">Export orders</a>
    </nav>
  </header>
  <main>
{{body}}
  </main>
</body>
</html>
EOF

cat > src/views/webhooks.html <<'EOF'
<h1>Webhooks</h1>
<p class="lead">Acme calls this address whenever an order is paid.</p>
<form class="webhook-form card" method="post" action="/settings/webhooks">
  <label for="webhook-url">Endpoint URL</label>
  <div class="row">
    <input id="webhook-url" name="url" type="text" placeholder="https://example.com/hooks/orders" value="{{url}}">
    <button class="save" type="submit">Save</button>
  </div>
  <p class="hint">We send a JSON body with the order for every payment.</p>
  {{saved}}
</form>
EOF

cat > src/views/billing.html <<'EOF'
<h1>Billing</h1>
<p class="lead">Growth plan, 3 seats, started on the 11th: this month is prorated.</p>
<table class="invoice card">
  <tbody>
      {{rows}}
  </tbody>
  <tfoot>
    <tr><td>Net</td><td class="num">{{net}}</td></tr>
    <tr><td>VAT 25%</td><td class="num">{{vat}}</td></tr>
    <tr class="total"><td>Total</td><td class="num">{{total}}</td></tr>
  </tfoot>
</table>
EOF

cat > public/style.css <<'EOF'
/* The shop's shared styles. */
* { box-sizing: border-box; }

body {
  margin: 0;
  font: 16px/1.5 system-ui, sans-serif;
  background: #f6f7f9;
  color: #1d2330;
}

a { color: #2453d6; }

.top {
  display: flex;
  align-items: center;
  gap: 24px;
  padding: 14px 32px;
  background: #ffffff;
  border-bottom: 1px solid #dde1e8;
}
.top .logo { font-weight: 700; font-size: 20px; color: #1d2330; text-decoration: none; }
.top nav { display: flex; gap: 18px; }
.top nav a { color: #4a5263; text-decoration: none; }

main { max-width: 760px; margin: 0 auto; padding: 32px; }
h1 { margin: 0 0 8px; }
.lead { color: #4a5263; margin: 0 0 24px; }

.card {
  background: #ffffff;
  border: 1px solid #dde1e8;
  border-radius: 10px;
  padding: 20px;
}
.products { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 16px; }
.products h2 { margin: 0; font-size: 18px; }
.price { font-weight: 600; }

label { display: block; font-weight: 600; margin-bottom: 6px; }
input {
  font: inherit;
  padding: 8px 12px;
  border: 1px solid #c3c9d4;
  border-radius: 6px;
  background: #ffffff;
  color: #1d2330;
}
button {
  font: inherit;
  padding: 8px 18px;
  border: 0;
  border-radius: 6px;
  background: #2453d6;
  color: #ffffff;
  cursor: pointer;
}
.hint { color: #6b7385; font-size: 14px; }
.notice { color: #1f7a3d; }
.error { color: #c42b2b; font-size: 14px; }

table { width: 100%; border-collapse: collapse; }
td { padding: 8px 0; border-bottom: 1px solid #eceff3; }
.num { text-align: right; }
.total td { font-weight: 700; border-bottom: 0; }
EOF

cat > public/webhooks.css <<'EOF'
/* The webhook settings page. */
.webhook-form .row { display: flex; gap: 8px; align-items: flex-start; }
.webhook-form input { flex: 1; }
.webhook-form .save { margin-top: 22px; margin-left: 4px; }
EOF

cat > src/webhooks.js <<'EOF'
// The webhook Acme calls when an order is paid. Kept in memory for now.
let url = '';

export function webhookUrl() {
  return url;
}

export function setWebhookUrl(value) {
  url = value.trim();
}
EOF

cat > src/orders.js <<'EOF'
// Sample orders, until the shop has a database. Totals are in cents.
export const ORDERS = [
  { id: 1041, date: new Date(2026, 0, 14), email: 'sam@example.com', total: 2895 },
  { id: 1042, date: new Date(2026, 1, 3), email: 'kim@example.com', total: 5800 },
  { id: 1043, date: new Date(2026, 2, 28), email: 'jo@example.com', total: 1490 },
];
EOF

cat > src/export.js <<'EOF'
// The orders export: one CSV line per order, for the accounts.
import { formatPrice } from './format.js';

// formatDate writes d as YYYY-MM-DD.
export function formatDate(d) {
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth())}-${pad(d.getDate())}`;
}

export function ordersCsv(orders) {
  const lines = orders.map((o) => [o.id, formatDate(o.date), o.email, formatPrice(o.total)].join(','));
  return ['id,date,email,total', ...lines].join('\n') + '\n';
}
EOF

cat > src/billing.js <<'EOF'
// Billing: the plans, prorating a month that started late, and VAT.
// Amounts are in cents.
export const PLANS = {
  starter: { name: 'Starter', price: 900 },
  growth: { name: 'Growth', price: 2900 },
  scale: { name: 'Scale', price: 9900 },
};
export const SEAT_PRICE = 500;
export const VAT_RATE = 0.25;

// prorate is the part of a monthly price for daysUsed of daysInMonth,
// rounded to the cent.
export function prorate(price, daysUsed, daysInMonth) {
  return Math.round((price * daysUsed) / daysInMonth);
}

export function vat(cents, rate = VAT_RATE) {
  return Math.round(cents * rate);
}

// invoice is a month's bill: the plan and the extra seats (prorated from
// startedDay), the net amount, VAT and the total.
export function invoice({ plan, seats = 1, startedDay = 1, daysInMonth = 30 }) {
  const p = PLANS[plan];
  if (!p) throw new Error(`unknown plan ${plan}`);
  const days = daysInMonth - startedDay + 1;
  const lines = [{ label: `${p.name} plan`, amount: prorate(p.price, days, daysInMonth) }];
  if (seats > 1) {
    lines.push({ label: `${seats - 1} extra seats`, amount: prorate((seats - 1) * SEAT_PRICE, days, daysInMonth) });
  }
  const net = lines.reduce((sum, l) => sum + l.amount, 0);
  return { lines, net, vat: vat(net), total: net + vat(net) };
}
EOF

cat > test/export.test.js <<'EOF'
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ordersCsv } from '../src/export.js';

test('the export has a header and a line per order', () => {
  const csv = ordersCsv([{ id: 7, date: new Date(2026, 5, 9), email: 'sam@example.com', total: 1490 }]);
  const lines = csv.trim().split('\n');
  assert.equal(lines[0], 'id,date,email,total');
  assert.equal(lines.length, 2);
  assert.match(lines[1], /^7,.*,sam@example\.com,€14\.90$/);
});
EOF

git add -A
git commit -q -m "Webhook settings, billing and the orders export"
