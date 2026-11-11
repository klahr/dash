// Tests for the Wall's reset countdown (resetLabel in web/static/wall.js). Run with `node --test web/`.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const src = readFileSync(new URL("./static/wall.js", import.meta.url), "utf8");
const fn = src.match(/function resetLabel[\s\S]*?\n}\n/)[0];
const resetLabel = new Function(fn + "; return resetLabel;")();

const now = new Date("2026-01-01T12:00:00Z");
const at = (ms) => new Date(now.getTime() + ms).toISOString();
const M = 60000, H = 60 * M, D = 24 * H;

test("resetLabel", () => {
  assert.equal(resetLabel(at(3 * D + 4 * H + 5 * M), now), "resets in 3d 4h");
  assert.equal(resetLabel(at(3 * D), now), "resets in 3d");
  assert.equal(resetLabel(at(5 * H + 12 * M), now), "resets in 5h 12m");
  assert.equal(resetLabel(at(2 * H), now), "resets in 2h");
  assert.equal(resetLabel(at(12 * M + 30000), now), "resets in 12m");
  assert.equal(resetLabel(at(59 * M), now), "resets in 59m");
  assert.equal(resetLabel(at(30000), now), "resets now");
  assert.equal(resetLabel(at(-H), now), "resets now");
  assert.equal(resetLabel("", now), "");
  assert.equal(resetLabel("garbage", now), "");
  assert.equal(resetLabel("0001-01-01T00:00:00Z", now), "");
});
