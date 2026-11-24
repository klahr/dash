// Tests for the shared time wording (web/static/time.js). Run with `node --test web/`.
import test from "node:test";
import assert from "node:assert/strict";
import { DAY, HOUR, MIN, SECOND, ago, agoText, clockText, durationText, resetText, untilText } from "./static/time.js";

const now = Date.parse("2026-01-01T12:00:00Z");
const at = (ms) => new Date(now + ms).toISOString();

test("ago and agoText", () => {
  assert.equal(ago(at(-30 * SECOND), now), "now");
  assert.equal(ago(at(30 * SECOND), now), "now");
  assert.equal(ago(at(-(2 * MIN + 59 * SECOND)), now), "2m");
  assert.equal(ago(at(-(3 * HOUR + 59 * MIN)), now), "3h");
  assert.equal(ago(at(-4 * DAY), now), "4d");
  assert.equal(ago(at(-800 * DAY), now), "2y");
  assert.equal(agoText(at(-10 * SECOND), now), "just now");
  assert.equal(agoText(at(-5 * MIN), now), "5m ago");
});

test("untilText", () => {
  assert.equal(untilText(at(30 * SECOND), now), "soon");
  assert.equal(untilText(at(-HOUR), now), "soon");
  assert.equal(untilText(at(5 * MIN + 50 * SECOND), now), "in 5m");
  assert.equal(untilText(at(2 * HOUR + 40 * MIN), now), "in 2h");
  assert.equal(untilText(at(2 * DAY + 20 * HOUR), now), "in 2d");
});

test("clockText ticks like a stopwatch", () => {
  assert.equal(clockText(at(-(6 * MIN + 12 * SECOND)), now), "6:12");
  assert.equal(clockText(now - 21 * MIN, now), "21:00");
  assert.equal(clockText(at(-(HOUR + 2 * MIN + 9 * SECOND)), now), "1:02:09");
  assert.equal(clockText(at(5 * SECOND), now), "0:00");
});

test("durations use the largest two units", () => {
  assert.equal(durationText(0), "<1m");
  assert.equal(durationText(59 * SECOND), "<1m");
  assert.equal(durationText(-5 * MIN), "<1m");
  assert.equal(durationText(45 * MIN), "45m");
  assert.equal(durationText(2 * HOUR), "2h");
  assert.equal(durationText(2 * HOUR + 15 * MIN + 30 * SECOND), "2h 15m");
  assert.equal(durationText(3 * DAY + 6 * HOUR + 40 * MIN), "3d 6h");
  assert.equal(durationText(DAY + 5 * MIN), "1d");
  assert.equal(durationText(0, SECOND), "0s");
  assert.equal(durationText(8 * SECOND + 900, SECOND), "8s");
  assert.equal(durationText(65 * SECOND, SECOND), "1m 5s");
  assert.equal(durationText(2 * MIN, SECOND), "2m");
  assert.equal(durationText(HOUR + 2 * MIN + 30 * SECOND, SECOND), "1h 2m");
});

test("resetText counts down", () => {
  assert.equal(resetText(at(3 * DAY + 4 * HOUR + 5 * MIN), now), "resets in 3d 4h");
  assert.equal(resetText(at(3 * DAY), now), "resets in 3d");
  assert.equal(resetText(at(5 * HOUR + 12 * MIN), now), "resets in 5h 12m");
  assert.equal(resetText(at(2 * HOUR), now), "resets in 2h");
  assert.equal(resetText(at(12 * MIN + 30 * SECOND), now), "resets in 12m");
  assert.equal(resetText(at(30 * SECOND), now), "resets now");
  assert.equal(resetText(at(-HOUR), now), "resets now");
  assert.equal(resetText("", now), "");
  assert.equal(resetText("garbage", now), "");
  assert.equal(resetText("0001-01-01T00:00:00Z", now), "");
});
