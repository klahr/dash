// How long ago, how long until and how long: the time wording every page
// shares (the app's modules, wall.js, usagetime.js, eta.js). Pure, with no
// imports, so Node tests can load it and the wall view (no session) can too.

export const SECOND = 1000, MIN = 60 * SECOND, HOUR = 60 * MIN, DAY = 24 * HOUR;

// "now", "2m", "3h", "4d", "2y": how long since iso, in its largest unit.
export function ago(iso, now = Date.now()) {
  const s = Math.max(0, (now - new Date(iso)) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  if (s < 365 * 86400) return `${Math.floor(s / 86400)}d`;
  return `${Math.floor(s / (365 * 86400))}y`;
}

// "just now", "2m ago".
export const agoText = (iso, now = Date.now()) => { const a = ago(iso, now); return a === "now" ? "just now" : `${a} ago`; };

// "soon", "in 5m", "in 3h", "in 2d": how long until iso, in its largest unit.
export function untilText(iso, now = Date.now()) {
  const s = (new Date(iso) - now) / 1000;
  if (s < 60) return "soon";
  if (s < 3600) return `in ${Math.floor(s / 60)}m`;
  if (s < 86400) return `in ${Math.floor(s / 3600)}h`;
  return `in ${Math.floor(s / 86400)}d`;
}

// "6:12", "1:02:09": how long since since (an ISO time or ms), as a ticking clock.
export function clockText(since, now = Date.now()) {
  const s = Math.max(0, Math.floor((now - new Date(since)) / 1000));
  const mm = Math.floor(s / 60) % 60, ss = String(s % 60).padStart(2, "0");
  return s < 3600 ? `${Math.floor(s / 60)}:${ss}` : `${Math.floor(s / 3600)}:${String(mm).padStart(2, "0")}:${ss}`;
}

// A duration in its largest two units, down to unit (MIN or SECOND):
// "45m", "2h 15m", "3d 6h", "<1m"; with SECOND, "8s", "1m 5s".
export function durationText(ms, unit = MIN) {
  const n = Math.floor(Math.max(0, ms) / unit);
  if (unit === MIN && n < 1) return "<1m";
  const units = [["d", DAY], ["h", HOUR], ["m", MIN], ["s", SECOND]].filter(([, u]) => u >= unit);
  const parts = [];
  let left = n * unit;
  for (const [name, u] of units) {
    const k = Math.floor(left / u);
    left -= k * u;
    if (k || parts.length) parts.push(k ? `${k}${name}` : "");
    if (parts.length === 2) break;
  }
  return parts.filter(Boolean).join(" ") || `0${units.at(-1)[0]}`;
}

// "resets in 2h 15m", "resets now" under a minute, "" for no time (a Claude usage window's reset).
export function resetText(resetsAt, now = Date.now()) {
  const d = new Date(resetsAt);
  if (isNaN(d) || d.getFullYear() < 2000) return "";
  return d - now < MIN ? "resets now" : `resets in ${durationText(d - now)}`;
}
