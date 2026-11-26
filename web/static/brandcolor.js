// The brand colours (style.css: --brand, --brand-hover, --on-brand) made from a
// project's colour, so buttons, links and focus rings take it and still read.
// No DOM: app/projects.js applies them; web/brandcolor_test.mjs tests them.

// The app's defaults (style.css :root): the page, card and raised
// backgrounds, and the dark and light text a brand fill can carry.
export const BG = "#101722";
export const SURFACE = "#172131";
export const RAISED = "#1f2b3e";
export const ON_DARK = "#0d1424";
export const ON_LIGHT = "#ffffff";
// DEFAULT is style.css's --brand (and the Blue swatch): a project in it keeps
// the stylesheet's brand colours as they are.
export const DEFAULT = "#8aa4ff";

// The contrast the brand colour needs against --bg, --surface and --raised,
// and the text on a brand fill against it: style.css uses --brand as the
// text colour of links and labels on those backgrounds (WCAG 1.4.3), which
// covers its use for fills and focus rings too (1.4.11, 3:1).
export const TEXT_CONTRAST = 4.5;

// parseHex is "#rgb" or "#rrggbb" as [r, g, b] (0–255), or null.
export function parseHex(hex) {
  const m = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(String(hex || "").trim());
  if (!m) return null;
  const s = m[1].length === 3 ? [...m[1]].map((c) => c + c).join("") : m[1];
  return [0, 2, 4].map((i) => parseInt(s.slice(i, i + 2), 16));
}

export const toHex = (rgb) => "#" + rgb.map((v) => Math.round(Math.min(255, Math.max(0, v))).toString(16).padStart(2, "0")).join("");

// luminance is the WCAG relative luminance of a colour, 0 (black) to 1 (white).
export function luminance(c) {
  const rgb = typeof c === "string" ? parseHex(c) : c;
  const [r, g, b] = rgb.map((v) => {
    const s = v / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

// contrast is the WCAG contrast ratio of two colours, 1 to 21.
export function contrast(a, b) {
  const la = luminance(a), lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

// mix is a + (b - a) * t, channel by channel.
const mix = (a, b, t) => a.map((v, i) => v + (b[i] - v) * t);

// HSL, to change a colour's lightness but keep its hue and saturation.
function toHsl([r, g, b]) {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b), l = (max + min) / 2, d = max - min;
  if (!d) return [0, 0, l];
  const s = d / (1 - Math.abs(2 * l - 1));
  const h = max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
  return [(h * 60 + 360) % 360, s, l]; // 0–360: (g - b) / d is negative for a pink
}
function fromHsl([h, s, l]) {
  const c = (1 - Math.abs(2 * l - 1)) * s, x = c * (1 - Math.abs(((h / 60) % 2) - 1)), m = l - c / 2;
  const [r, g, b] = h < 60 ? [c, x, 0] : h < 120 ? [x, c, 0] : h < 180 ? [0, c, x] : h < 240 ? [0, x, c] : h < 300 ? [x, 0, c] : [c, 0, x];
  return [r, g, b].map((v) => (v + m) * 255);
}

// onColor is the text colour, dark or light, that reads better on fill.
export function onColor(fill, dark = ON_DARK, light = ON_LIGHT) {
  return contrast(fill, dark) >= contrast(fill, light) ? dark : light;
}

// brandColors makes the brand colours from a project's colour:
//   brand  the colour, lightened (on a dark theme; darkened on a light one)
//          until it has TEXT_CONTRAST against bg, surface and raised, as
//          text, and the text on it has TEXT_CONTRAST too
//   hover  a step further the same way, for :hover
//   on     the text on a brand fill: ON_DARK or ON_LIGHT, whichever reads
//          better (ON_DARK on a dark theme, as the brand is light there)
// null for a colour that isn't one ("" or bad input): the defaults apply.
export function brandColors(color, { bg = BG, surface = SURFACE, raised = RAISED, dark = true } = {}) {
  const rgb = parseHex(color);
  if (!rgb) return null;
  const [h, s, l0] = toHsl(rgb);
  // The text aimed for is the one lightening (darkening) helps, so hover,
  // a step further, keeps every contrast too.
  const text = dark ? ON_DARK : ON_LIGHT;
  const ok = (c) => [bg, surface, raised, text].every((b) => contrast(c, b) >= TEXT_CONTRAST);
  let c = rgb;
  // Lightness in small steps toward white (or black), keeping the hue; at
  // the end of the range the colour is white (black), which always passes.
  for (let i = 1; !ok(c) && i <= 100; i++) {
    const l = dark ? l0 + (1 - l0) * i / 100 : l0 * (1 - i / 100);
    c = fromHsl([h, s, l]).map(Math.round); // checked as it is stored
  }
  const hover = mix(c, dark ? [255, 255, 255] : [0, 0, 0], 0.2);
  const brand = toHex(c);
  return { brand, hover: toHex(hover), on: onColor(brand) };
}
