// Tests for the usage meters' time wording (web/static/usagetime.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { HOUR, MIN, aheadOfPace, fmtDuration, paceMs, paceText, resetText } from "./static/usagetime.js";

test("durations use the largest two units", () => {
  assert.equal(fmtDuration(0), "<1m");
  assert.equal(fmtDuration(59 * 1000), "<1m");
  assert.equal(fmtDuration(-5 * MIN), "<1m");
  assert.equal(fmtDuration(45 * MIN), "45m");
  assert.equal(fmtDuration(2 * HOUR), "2h");
  assert.equal(fmtDuration(2 * HOUR + 15 * MIN + 30 * 1000), "2h 15m");
  assert.equal(fmtDuration(3 * 24 * HOUR + 6 * HOUR + 40 * MIN), "3d 6h");
  assert.equal(fmtDuration(24 * HOUR + 5 * MIN), "1d");
});

test("pace points become window time", () => {
  // 5-hour window: 12 points ahead is 36 minutes; a week: 12 points is ~20h.
  assert.equal(paceText(paceMs(0.42, 0.30, 5 * HOUR)), "36m ahead");
  assert.equal(paceText(paceMs(0.30, 0.42, 168 * HOUR)), "20h 9m behind");
  assert.equal(paceText(paceMs(0.5, 0.5, 5 * HOUR)), "on pace");
  assert.equal(paceText(30 * 1000), "on pace");
  assert.equal(paceText(-30 * 1000), "on pace");
});

test("ahead of pace only from a minute up", () => {
  assert.equal(aheadOfPace(MIN), true);
  assert.equal(aheadOfPace(59 * 1000), false);
  assert.equal(aheadOfPace(-HOUR), false);
});

test("resets count down", () => {
  const now = Date.parse("2026-11-11T12:00:00Z");
  assert.equal(resetText("2026-11-11T14:15:00Z", now), "resets in 2h 15m");
  assert.equal(resetText("2026-11-14T18:00:00Z", now), "resets in 3d 6h");
  assert.equal(resetText("2026-11-11T12:00:20Z", now), "resets in <1m");
});
