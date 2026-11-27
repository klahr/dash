// Pace wording for the Claude usage meters (web/static/app/usage.js): pace
// as time ahead or behind (the reset countdown is resetText in time.js).
// Pure, so usagetime_test.mjs can cover it.

import { HOUR, MIN, SECOND, durationText } from "./time.js";

// Time ahead (>0) or behind (<0) of an even pace, in ms: the gap between the
// share used and the share of the window elapsed, as window time.
export const paceMs = (utilization, elapsed, windowMs) => (utilization - elapsed) * windowMs;

// Ahead of pace by a minute or more (the meters turn red).
export const aheadOfPace = (ms) => ms >= MIN;

// "1h 20m ahead", "40m behind", or "on pace" within a minute.
export function paceText(ms) {
  if (Math.abs(ms) < MIN) return "on pace";
  return `${durationText(Math.abs(ms))} ${ms > 0 ? "ahead" : "behind"}`;
}

// A wait that counts down live: to the second under an hour ("12m 5s"),
// else to the minute ("1h 20m").
export const waitText = (ms) => durationText(ms, ms < HOUR ? SECOND : MIN);

// The pace gap in seconds sinceMs after the snapshot was taken: usage holds
// still between snapshots while the pace line moves on with the clock.
export const liveGapS = (p, sinceMs = 0) => (p?.gap_s || 0) - Math.max(0, sinceMs) / SECOND;

// The queue's place on the weekly pace (the snapshot's pace, sinceMs after
// it was taken), in words, or null when it isn't following it.
export function paceStateText(p, sinceMs = 0) {
  if (!p || !p.follow) return null;
  const n = (k) => `${k} task${k === 1 ? "" : "s"} at once`;
  switch (p.state) {
    case "waiting": return `Ahead of pace: waiting ${waitText(liveGapS(p, sinceMs) * SECOND)} for the line to catch up`;
    case "on_pace": return `On pace: ${n(p.slots)}`;
    case "catching_up": return `Behind pace: catching up with ${n(p.slots)}`;
  }
  return "Follows the pace once Claude reports weekly usage";
}
