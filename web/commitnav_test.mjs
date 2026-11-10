// Tests for the History view's commit pane (web/static/commitnav.js). Run
// with `node --test web/*_test.mjs`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { stepCommit, latestOnly } from "./static/commitnav.js";

const hashes = ["c3", "c2", "c1"];

test("j/k move the selection and stop at the ends", () => {
  assert.equal(stepCommit(hashes, "c3", 1), "c2");
  assert.equal(stepCommit(hashes, "c2", -1), "c3");
  assert.equal(stepCommit(hashes, "c3", -1), "c3");
  assert.equal(stepCommit(hashes, "c1", 1), "c1");
  assert.equal(stepCommit(hashes, "c3", 5), "c1");
  assert.equal(stepCommit(hashes, "c1", -Infinity), "c3");
  assert.equal(stepCommit(hashes, "c3", Infinity), "c1");
});

test("nothing selected (or the commit is gone): j/k start at the top", () => {
  assert.equal(stepCommit(hashes, null, 1), "c3");
  assert.equal(stepCommit(hashes, "gone", -1), "c3");
  assert.equal(stepCommit(hashes, null, -Infinity), "c3");
  assert.equal(stepCommit(hashes, null, Infinity), "c1");
  assert.equal(stepCommit([], null, 1), null);
});

// deferred is a promise settled from outside, like a request in flight.
function deferred() {
  let resolve, reject;
  const promise = new Promise((a, b) => { resolve = a; reject = b; });
  return { promise, resolve, reject };
}

test("a slow answer for a commit no longer selected is stale", async () => {
  const l = latestOnly();
  const first = deferred(), second = deferred();
  const signals = [];
  const a = l.run((s) => { signals.push(s); return first.promise; });
  const b = l.run((s) => { signals.push(s); return second.promise; });
  await Promise.resolve(); await Promise.resolve();
  assert.equal(signals[0].aborted, true, "the first request is aborted");
  assert.equal(signals[1].aborted, false);
  second.resolve("B");
  first.resolve("A"); // arrives last
  assert.deepEqual(await b, { value: "B" });
  assert.deepEqual(await a, { stale: true });
});

test("a superseded request's error doesn't surface; the latest one's does", async () => {
  const l = latestOnly();
  const first = deferred();
  const a = l.run(() => first.promise);
  const b = l.run(() => Promise.reject(new Error("boom")));
  first.reject(Object.assign(new Error("aborted"), { name: "AbortError" }));
  assert.deepEqual(await a, { stale: true });
  await assert.rejects(b, /boom/);
});

test("quick moves are debounced into one request", async () => {
  const timers = [];
  const l = latestOnly({
    setTimer: (fn) => { timers.push({ fn, live: true }); return timers.length - 1; },
    clearTimer: (i) => { timers[i].live = false; },
  });
  const calls = [];
  const runs = ["c3", "c2", "c1"].map((h) => l.run(() => { calls.push(h); return h; }, { wait: 150 }));
  assert.equal(calls.length, 0);
  timers.filter((t) => t.live).forEach((t) => t.fn());
  assert.deepEqual(await Promise.all(runs), [{ stale: true }, { stale: true }, { value: "c1" }]);
  assert.deepEqual(calls, ["c1"]);
});

test("cancel (the pane closed) makes the request in flight stale", async () => {
  const l = latestOnly();
  const d = deferred();
  let signal;
  const a = l.run((s) => { signal = s; return d.promise; });
  await Promise.resolve();
  l.cancel();
  assert.equal(signal.aborted, true);
  d.resolve("late");
  assert.deepEqual(await a, { stale: true });
});
