// Time wording for the Claude usage meters (web/static/app/usage.js):
// pace as time ahead/behind and resets as a countdown. Pure, so
// usagetime_test.mjs can cover it.

export const MIN = 60000, HOUR = 60 * MIN;

// A duration in its largest two units: "45m", "2h 15m", "3d 6h", "<1m".
export function fmtDuration(ms) {
  const m = Math.floor(Math.max(0, ms) / MIN);
  if (m < 1) return "<1m";
  const d = Math.floor(m / 1440), hr = Math.floor((m % 1440) / 60), min = m % 60;
  if (d) return hr ? `${d}d ${hr}h` : `${d}d`;
  if (hr) return min ? `${hr}h ${min}m` : `${hr}h`;
  return `${min}m`;
}

// Time ahead (>0) or behind (<0) of an even pace, in ms: the gap between the
// share used and the share of the window elapsed, as window time.
export const paceMs = (utilization, elapsed, windowMs) => (utilization - elapsed) * windowMs;

// Ahead of pace by a minute or more (the meters turn red).
export const aheadOfPace = (ms) => ms >= MIN;

// "1h 20m ahead", "40m behind", or "on pace" within a minute.
export function paceText(ms) {
  if (Math.abs(ms) < MIN) return "on pace";
  return `${fmtDuration(Math.abs(ms))} ${ms > 0 ? "ahead" : "behind"}`;
}

// "resets in 2h 15m".
export const resetText = (resetsAt, now = Date.now()) => `resets in ${fmtDuration(new Date(resetsAt) - now)}`;
