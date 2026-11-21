// Tests for a running deploy's time-left estimate (web/static/deployeta.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { ETA_SAMPLES, estimate, etaText, tookToLive, typical } from "./static/deployeta.js";

const MIN = 60000;
const T0 = Date.parse("2026-11-01T10:00:00Z");
const at = (ms) => new Date(T0 + ms).toISOString();

// A deploy to env that started at start and went live mins minutes later.
const done = (id, mins, { env = "production", start = 0, status = "live", ...rest } = {}) => ({
  id, environment: env, status, started_at: at(start),
  phases: [{ status: "building", at: at(start) }, { status: "live", at: at(start + mins * MIN) }], ...rest,
});
const running = (start, extra = {}) => ({ id: "now", environment: "production", status: "building", started_at: at(start), ...extra });

test("time to live is from the start until it went live", () => {
  assert.equal(tookToLive(done("a", 4)), 4 * MIN);
  assert.equal(tookToLive(done("b", 4, { status: "superseded" })), 4 * MIN);
  assert.equal(tookToLive({ id: "c", status: "failed", started_at: at(0), phases: [] }), 0);
  assert.equal(tookToLive(done("d", 4, { status: "failed" })), 0);
  assert.equal(tookToLive(done("e", 4, { cancelled: true })), 0);
  assert.equal(tookToLive({ id: "f", status: "live", phases: [{ status: "live", at: at(0) }] }), 0);
});

test("typical is the median of the same environment's and kind's successful deploys", () => {
  const list = [done("a", 2), done("b", 10), done("c", 4), done("s", 30, { env: "staging" }), done("p", 1, { promote: true })];
  assert.deepEqual(typical(running(0), list), { ms: 4 * MIN, n: 3 });
  assert.deepEqual(typical(running(0, { promote: true }), list), { ms: 1 * MIN, n: 1 });
  assert.deepEqual(typical(running(0, { environment: "qa" }), list), { ms: 0, n: 0 });
  assert.deepEqual(typical(running(0), [done("a", 2), done("b", 6)]), { ms: 4 * MIN, n: 2 });
});

test("typical goes by the newest deploys only", () => {
  const list = [...Array(ETA_SAMPLES).fill(0).map((_, i) => done(`n${i}`, 3)), ...Array(20).fill(0).map((_, i) => done(`o${i}`, 30))];
  assert.deepEqual(typical(running(0), list), { ms: 3 * MIN, n: ETA_SAMPLES });
});

test("the estimate counts down from the typical time", () => {
  const list = [done("a", 5), done("b", 5)];
  assert.deepEqual(estimate(running(0), list, T0 + 2 * MIN), { left: 3 * MIN, over: false, typical: 5 * MIN, n: 2 });
  assert.equal(estimate(running(0), list, T0 + 6 * MIN).over, true);
  assert.equal(estimate({ ...running(0), status: "queued" }, list, T0), null);
  assert.equal(estimate({ ...running(0), status: "live" }, list, T0), null);
  assert.equal(estimate(running(0), [], T0), null);
});

test("estimates read plainly", () => {
  assert.equal(etaText(null), "");
  assert.equal(etaText({ left: 20000, over: false }), "under a minute left");
  assert.equal(etaText({ left: 3 * MIN + 10000, over: false }), "about 3m left");
  assert.equal(etaText({ left: 125 * MIN, over: false }), "about 2h 5m left");
  assert.equal(etaText({ left: 120 * MIN, over: false }), "about 2h left");
  assert.equal(etaText({ left: 0, over: true }), "taking longer than usual");
});
