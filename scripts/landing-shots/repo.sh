#!/bin/sh
# Makes the demo project for the landing page's screenshots: a small web shop,
# acme-shop, as a git repository in $1.
set -eu
dir=$1
mkdir -p "$dir/src/views" "$dir/test"
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

The Acme storefront: products, cart and checkout.

    npm start    # http://localhost:4100
    npm test
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
