// Tests for the brand colours made from a project's colour (web/static/brandcolor.js).
// Run with `node --test web/`.
import test from "node:test";
import assert from "node:assert/strict";
import { BG, ON_DARK, ON_LIGHT, RAISED, SURFACE, TEXT_CONTRAST, brandColors, contrast, luminance, parseHex } from "./static/brandcolor.js";

// The swatches of Project settings → General (app/settings.js: SWATCHES), and
// custom colours that are hard to make readable.
const SWATCHES = ["#8aa4ff", "#5ec4a8", "#7fcf6b", "#f2c14e", "#f39a4e", "#ef6b6b", "#e57ad2", "#a98bf0"];
const HARD = ["#e5484d", "#0b1a3a", "#f5f5f5", "#ff0000", "#ffff00", "#000000", "#ffffff", "#101722"];

function meetsTargets(color, opts = {}) {
  const c = brandColors(color, opts);
  const backs = { bg: opts.bg || BG, surface: opts.surface || SURFACE, raised: opts.raised || RAISED };
  for (const [name, fill] of [["brand", c.brand], ["hover", c.hover]]) {
    // --brand is the text colour of links and labels on each background.
    for (const [back, b] of Object.entries(backs)) {
      assert.ok(contrast(fill, b) >= TEXT_CONTRAST, `${color}: ${name} ${fill} as text on ${back} ${contrast(fill, b).toFixed(2)}`);
    }
    assert.ok(contrast(fill, c.on) >= TEXT_CONTRAST, `${color}: text ${c.on} on ${name} ${fill} ${contrast(fill, c.on).toFixed(2)}`);
  }
  return c;
}

test("each swatch and hard colour meets the contrast targets", () => {
  for (const color of [...SWATCHES, ...HARD]) meetsTargets(color);
});

test("a colour that already reads is kept as it is", () => {
  for (const color of SWATCHES) assert.equal(brandColors(color).brand, color);
  // Blue is the default brand: the same colours as style.css's.
  const blue = brandColors("#8aa4ff");
  assert.equal(blue.on, ON_DARK);
});

test("a dark colour is lightened, keeping its hue", () => {
  const c = brandColors("#0b1a3a");
  assert.ok(luminance(c.brand) > luminance("#0b1a3a"));
  const [r, g, b] = parseHex(c.brand);
  assert.ok(b > g && g > r, `${c.brand} is still blue`);
  assert.equal(c.on, ON_DARK);
});

test("hover is lighter than the brand on a dark theme", () => {
  for (const color of [...SWATCHES, ...HARD]) {
    const c = brandColors(color);
    assert.ok(luminance(c.hover) >= luminance(c.brand), color);
  }
});

test("a light theme darkens instead", () => {
  const light = { bg: "#ffffff", surface: "#f4f6fa", raised: "#e9edf3", dark: false };
  for (const color of [...SWATCHES, ...HARD]) {
    const c = meetsTargets(color, light);
    assert.equal(c.on, ON_LIGHT, color);
    assert.ok(luminance(c.hover) <= luminance(c.brand), color);
  }
});

test("no colour, or a bad one, gives null", () => {
  for (const bad of ["", null, undefined, "blue", "#12345", "#ggg"]) assert.equal(brandColors(bad), null);
  assert.deepEqual(parseHex("#abc"), [0xaa, 0xbb, 0xcc]);
});
