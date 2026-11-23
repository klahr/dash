// Tests for the shared estimate helpers (web/static/eta.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { SAMPLES, etaText, typicalOf } from "./static/eta.js";

const MIN = 60000;
const self = (x) => x;

test("typical is the median of the times pick gives", () => {
  assert.deepEqual(typicalOf([2, 10, 4].map((m) => m * MIN), self), { ms: 4 * MIN, n: 3 });
  assert.deepEqual(typicalOf([2, 6].map((m) => m * MIN), self), { ms: 4 * MIN, n: 2 }, "an even count takes the middle two's mean");
  assert.deepEqual(typicalOf([], self), { ms: 0, n: 0 });
});

test("entries pick skips don't count", () => {
  const list = [{ ms: 5 * MIN, ok: true }, { ms: 1 * MIN, ok: false }, { ms: 0, ok: true }, { ms: 7 * MIN, ok: true }];
  assert.deepEqual(typicalOf(list, (x) => (x.ok ? x.ms : 0)), { ms: 6 * MIN, n: 2 });
  assert.deepEqual(typicalOf(list, () => 0), { ms: 0, n: 0 });
});

test("typical goes by the newest max entries only", () => {
  const list = [...Array(SAMPLES).fill(3 * MIN), ...Array(20).fill(30 * MIN)];
  assert.deepEqual(typicalOf(list, self), { ms: 3 * MIN, n: SAMPLES });
  assert.deepEqual(typicalOf([1, 2, 3, 100, 100].map((m) => m * MIN), self, 3), { ms: 2 * MIN, n: 3 });
  const seen = [];
  typicalOf([1, 2, 3, 4], (x) => (seen.push(x), x), 2);
  assert.deepEqual(seen, [1, 2], "pick isn't called past max");
});

test("the time left is worded", () => {
  assert.equal(etaText(null), "");
  assert.equal(etaText({ left: 20000, over: false }), "under a minute left");
  assert.equal(etaText({ left: 3 * MIN + 10000, over: false }), "about 3m left");
  assert.equal(etaText({ left: 75 * MIN, over: false }), "about 1h 15m left");
  assert.equal(etaText({ left: 120 * MIN, over: false }), "about 2h left");
  assert.equal(etaText({ left: 0, over: true }), "taking longer than usual");
});
