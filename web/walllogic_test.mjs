// Tests for the wall view's decisions (web/static/walllogic.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import {
  age, isStale, staleText, progress, stuckText, nextCompact, drift, DRIFT_MS, workHours, offHours, quiet, weekText,
  deltaText, oneDecimal, headline, fullText,
} from "./static/walllogic.js";

const MIN = 60000, HOUR = 60 * MIN;

test("the data is stale after a failed poll or about two intervals without one", () => {
  const base = { failures: 0, lastOk: 1_000_000, pollMs: 5000 };
  assert.equal(isStale({ ...base, now: base.lastOk + 6000 }), false);
  assert.equal(isStale({ ...base, now: base.lastOk + 13000 }), true, "two intervals and more");
  assert.equal(isStale({ ...base, failures: 1, now: base.lastOk + 1000 }), true, "a failed poll");
  assert.equal(isStale({ ...base, lastOk: 0, failures: 3, now: 99 }), false, "nothing shown yet");
});

test("the stale notice says how old the data is", () => {
  assert.equal(staleText(0, 25000), "Last updated 25 s ago · retrying");
  assert.equal(staleText(0, 4 * MIN + 5000), "Last updated 4 min ago · retrying");
  assert.equal(staleText(0, 2 * HOUR + 14 * MIN), "Last updated 2 h 14 ago · retrying");
});

test("age", () => {
  assert.equal(age(30000), "now");
  assert.equal(age(48 * MIN), "48 min");
  assert.equal(age(50 * HOUR), "2 d");
});

test("progress compares elapsed time with the median of comparable runs", () => {
  assert.equal(progress(5 * MIN, []), null, "no estimate");
  assert.equal(progress(5 * MIN, undefined), null);
  const took = [10 * 60, 12 * 60, 20 * 60]; // median 12m
  assert.deepEqual(progress(8 * MIN, took), { fraction: 8 / 12, level: "", text: "8m / ~12m" });
  const over = progress(14 * MIN, took);
  assert.equal(over.level, "over");
  assert.equal(over.fraction, 1);
  assert.equal(over.text, "14m / ~12m · over usual");
  const late = progress(25 * MIN, took);
  assert.equal(late.level, "late");
  assert.equal(late.text, "25m / ~12m · 2× usual");
});

test("an activity that doesn't change for a while reads as stuck", () => {
  const at = new Date(0).toISOString();
  assert.equal(stuckText("go test ./...", at, 2 * MIN), "");
  assert.equal(stuckText("go test ./...", at, 6 * MIN), "stuck on go test ./... for 6m");
  assert.equal(stuckText("", at, 6 * MIN), "");
  assert.equal(stuckText("Edit a.go", undefined, 6 * MIN), "");
});

test("the running list switches to compact rows with hysteresis", () => {
  assert.equal(nextCompact(false, 5, 5), false);
  assert.equal(nextCompact(false, 6, 5), true, "more than fit");
  assert.equal(nextCompact(true, 5, 5), true, "one fewer doesn't switch back yet");
  assert.equal(nextCompact(true, 4, 5), false);
});

test("the board drifts a few pixels now and then", () => {
  assert.deepEqual(drift(0), { x: 0, y: 0 });
  assert.deepEqual(drift(DRIFT_MS - 1), { x: 0, y: 0 }, "still until the next step");
  const spots = new Set();
  for (let i = 0; i < 20; i++) {
    const { x, y } = drift(i * DRIFT_MS);
    assert.ok(Math.abs(x) <= 6 && Math.abs(y) <= 6);
    spots.add(x + "," + y);
  }
  assert.ok(spots.size > 4);
});

test("work hours from the URL, and off hours", () => {
  const cfg = workHours(new URLSearchParams(""));
  assert.deepEqual([cfg.from, cfg.to, cfg.days], [8, 18, [1, 2, 3, 4, 5]]);
  const fri = (h) => new Date(2026, 10, 27, h, 30); // a Friday
  assert.equal(offHours(fri(9), cfg), false);
  assert.equal(offHours(fri(19), cfg), true);
  assert.equal(offHours(fri(7), cfg), true);
  assert.equal(offHours(new Date(2026, 10, 28, 12), cfg), true, "Saturday");
  const night = workHours(new URLSearchParams("hours=22-6&days=5-1"));
  assert.deepEqual(night.days, [5, 6, 0, 1]);
  assert.equal(offHours(fri(23), night), false, "hours wrap past midnight");
  assert.equal(offHours(fri(12), night), true);
  assert.equal(offHours(new Date(2026, 10, 28, 12), workHours(new URLSearchParams("hours=all"))), false);
  assert.deepEqual(workHours(new URLSearchParams("hours=nonsense&days=9-1")).days, [1, 2, 3, 4, 5]);
});

test("the board is quiet with nothing running or waiting", () => {
  assert.equal(quiet({ running: [], needs: [], status: { waiting: 0 } }), true);
  assert.equal(quiet({ running: [{}], needs: [], status: { waiting: 0 } }), false);
  assert.equal(quiet({ running: [], needs: [{}], status: { waiting: 1 } }), false);
  assert.equal(weekText({ merged: 84, first_try: 0.91 }), "This week: 84 merged · 91% first try");
  assert.equal(weekText({ merged: 0 }), "This week: 0 merged");
});

test("today's figures against the average day", () => {
  assert.equal(deltaText(12, 9, oneDecimal), "▲ above avg 9");
  assert.equal(deltaText(3, 9.43, oneDecimal), "▼ below avg 9.4");
  assert.equal(deltaText(9, 9.2, oneDecimal), "= avg 9.2");
  assert.equal(deltaText(0, 0, oneDecimal), "= avg 0");
  assert.equal(deltaText(5, null), "");
});

test("the headline pairs the status with a symbol and words", () => {
  assert.deepEqual(headline({ level: "ok", label: "All good", reasons: [], running: 5, waiting: 0 }),
    { symbol: "●", text: "All good · 5 running", why: "" });
  assert.deepEqual(headline({ level: "bad", label: "Needs action", reasons: ["queue paused", "1 open issue"], running: 0, waiting: 2 }),
    { symbol: "✕", text: "Needs action · 0 running · 2 waiting on people", why: "queue paused · 1 open issue" });
  assert.equal(headline({ level: "warn", label: "Waiting on people", reasons: [], running: 1, waiting: 1 }).symbol, "▲");
  assert.equal(headline(null).text, "Loading…");
});

test("a usage window heading for 100% before it resets", () => {
  const now = 0;
  assert.equal(fullText({ runs_out: true, full_at: new Date(2 * HOUR + 10 * MIN).toISOString() }, now), "100% in ~2h at this rate");
  assert.equal(fullText({ runs_out: true, full_at: new Date(40 * MIN).toISOString() }, now, true), "100% in ~40m");
  assert.equal(fullText({ runs_out: false, full_at: new Date(HOUR).toISOString() }, now), "");
  assert.equal(fullText({ runs_out: true, full_at: new Date(0).toISOString() }, now), "at the limit");
});
