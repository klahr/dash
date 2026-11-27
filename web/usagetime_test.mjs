// Tests for the usage meters' pace wording (web/static/usagetime.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { HOUR, MIN } from "./static/time.js";
import { aheadOfPace, liveGapS, paceMs, paceStateText, paceText, waitText } from "./static/usagetime.js";

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

test("paceStateText says where the queue is on the weekly pace", () => {
  assert.equal(paceStateText(null), null);
  assert.equal(paceStateText({ follow: false, state: "" }), null);
  assert.equal(paceStateText({ follow: true, state: "waiting", gap_s: 80 * 60 }), "Ahead of pace: waiting 1h 20m for the line to catch up");
  assert.equal(paceStateText({ follow: true, state: "on_pace", slots: 1 }), "On pace: 1 task at once");
  assert.equal(paceStateText({ follow: true, state: "catching_up", slots: 4 }), "Behind pace: catching up with 4 tasks at once");
  assert.equal(paceStateText({ follow: true, state: "unknown" }), "Follows the pace once Claude reports weekly usage");
});

test("the wait for the pace counts down live", () => {
  const p = { follow: true, state: "waiting", gap_s: 80 * 60 };
  assert.equal(liveGapS(p, 30 * 1000), 80 * 60 - 30);
  // Under an hour it counts to the second.
  assert.equal(paceStateText(p, 25 * MIN), "Ahead of pace: waiting 55m for the line to catch up");
  assert.equal(paceStateText(p, 79 * MIN + 55 * 1000), "Ahead of pace: waiting 5s for the line to catch up");
  assert.equal(paceStateText(p, 2 * HOUR), "Ahead of pace: waiting 0s for the line to catch up");
  assert.equal(waitText(HOUR + 90 * 1000), "1h 1m");
  assert.equal(waitText(65 * 1000), "1m 5s");
});
