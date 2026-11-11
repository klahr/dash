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

// The queue's place on the weekly pace (the snapshot's pace), in words, or
// null when it isn't following it.
export function paceStateText(p) {
  if (!p || !p.follow) return null;
  const n = (k) => `${k} task${k === 1 ? "" : "s"} at once`;
  switch (p.state) {
    case "waiting": return `Ahead of pace: waiting ${fmtDuration(p.gap_s * 1000)} for the line to catch up`;
    case "on_pace": return `On pace: ${n(p.slots)}`;
    case "catching_up": return `Behind pace: catching up with ${n(p.slots)}`;
  }
  return "Follows the pace once Claude reports weekly usage";
}

// "resets in 2h 15m".
export const resetText = (resetsAt, now = Date.now()) => `resets in ${fmtDuration(new Date(resetsAt) - now)}`;
