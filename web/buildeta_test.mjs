// Tests for a running build's progress (web/static/buildeta.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { BUILD_SAMPLES, buildEtaText, buildProgress, tookMs, typicalRun } from "./static/buildeta.js";

const MIN = 60000;
const T0 = Date.parse("2026-11-01T10:00:00Z");
const at = (ms) => new Date(T0 + ms).toISOString();

// A finished run of workflow wf that took mins minutes.
const done = (id, mins, { wf = "CI", conclusion = "success", ...rest } = {}) => ({
  id, workflow: wf, status: "completed", conclusion, created_at: at(0), started_at: at(0), updated_at: at(mins * MIN), ...rest,
});
const running = (start, extra = {}) => ({ id: 999, workflow: "CI", status: "in_progress", conclusion: "", created_at: at(start), started_at: at(start), ...extra });

test("a run took from its start until it finished, when it passed", () => {
  assert.equal(tookMs(done(1, 4)), 4 * MIN);
  assert.equal(tookMs(done(2, 4, { conclusion: "failure" })), 0);
  assert.equal(tookMs(done(3, 4, { conclusion: "cancelled" })), 0);
  assert.equal(tookMs(done(4, 4, { started_at: undefined })), 0);
  assert.equal(tookMs(running(0)), 0);
});

test("typical is the median of the same workflow's successful runs", () => {
  const list = [done(1, 2), done(2, 10), done(3, 4), done(4, 30, { wf: "Release" }), done(5, 1, { conclusion: "failure" })];
  assert.deepEqual(typicalRun(running(0), list), { ms: 4 * MIN, n: 3 });
  assert.deepEqual(typicalRun(running(0, { workflow: "Release" }), list), { ms: 30 * MIN, n: 1 });
  assert.deepEqual(typicalRun(running(0, { workflow: "Lint" }), list), { ms: 0, n: 0 });
  assert.deepEqual(typicalRun(running(0), [done(1, 2), done(2, 6)]), { ms: 4 * MIN, n: 2 });
  assert.deepEqual(typicalRun(running(0), [done(1, 2), done(1, 2), done(2, 6)]), { ms: 4 * MIN, n: 2 }, "a run listed twice counts once");
});

test("typical goes by the newest runs only", () => {
  const list = [...Array(BUILD_SAMPLES).fill(0).map((_, i) => done(i, 3)), ...Array(20).fill(0).map((_, i) => done(100 + i, 30))];
  assert.deepEqual(typicalRun(running(0), list), { ms: 3 * MIN, n: BUILD_SAMPLES });
});

test("progress grows from the start to the typical time, then is over", () => {
  const list = [done(1, 10)];
  assert.deepEqual(buildProgress(running(0), list, T0 + 4 * MIN), { frac: 0.4, left: 6 * MIN, over: false, typical: 10 * MIN, n: 1 });
  const late = buildProgress(running(0), list, T0 + 12 * MIN);
  assert.equal(late.frac, 1);
  assert.equal(late.left, 0);
  assert.equal(late.over, true);
  assert.equal(buildProgress(running(0), list, T0 - MIN).frac, 0, "a clock behind the start shows no progress");
});

test("no progress for a finished, queued or unknown run", () => {
  const list = [done(1, 10)];
  assert.equal(buildProgress(done(2, 3), list, T0), null);
  assert.equal(buildProgress(running(0, { status: "queued" }), list, T0), null);
  assert.equal(buildProgress(running(0, { workflow: "Other" }), list, T0), null);
  assert.equal(buildProgress(running(0), [], T0), null);
  assert.equal(buildProgress(null, list, T0), null);
});

test("progress falls back to when the run was created", () => {
  const p = buildProgress(running(0, { started_at: undefined }), [done(1, 10)], T0 + 5 * MIN);
  assert.equal(p.frac, 0.5);
});

test("the time left is worded", () => {
  assert.equal(buildEtaText(null), "");
  assert.equal(buildEtaText({ left: 20000, over: false }), "under a minute left");
  assert.equal(buildEtaText({ left: 3 * MIN, over: false }), "about 3m left");
  assert.equal(buildEtaText({ left: 75 * MIN, over: false }), "about 1h 15m left");
  assert.equal(buildEtaText({ left: 0, over: true }), "taking longer than usual");
});
