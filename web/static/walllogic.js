// The wall view's decisions, apart from its drawing (wall.js), so Node tests
// can check them: when its data is stale, how a running task compares with
// its typical time, when to switch the running list to compact rows, the
// burn-in drift, off hours, and how figures and the overall status are
// worded. It imports only eta.js and time.js, which a TV with a wall token
// and no session may load too (auth.go's public files).
import { typicalOf } from "./eta.js";
import { MIN, HOUR, durationText } from "./time.js";

const pad = (n) => String(n).padStart(2, "0");

// age: "2 h 14", "48 min", "now"
export function age(ms) {
  const m = Math.max(0, Math.floor(ms / MIN));
  if (m < 1) return "now";
  if (m < 60) return m + " min";
  const hrs = Math.floor(m / 60);
  if (hrs < 48) return hrs + " h " + pad(m % 60);
  return Math.floor(hrs / 24) + " d";
}

// ---- stale data ----

// isStale says whether the wall's data can no longer be taken as current:
// the last poll failed, or the last good one is older than about two poll
// intervals (a hung request, a sleeping TV).
export function isStale({ failures, lastOk, now, pollMs }) {
  if (!lastOk) return false; // nothing shown yet: "Loading…" says so
  return failures > 0 || now - lastOk > 2 * pollMs + 2000;
}

// staleText is the notice over a stale wall: "Last updated 4 min ago · retrying".
export function staleText(lastOk, now) {
  const s = Math.max(0, Math.floor((now - lastOk) / 1000));
  const ago = s < 60 ? s + " s" : age(now - lastOk);
  return "Last updated " + ago + " ago · retrying";
}

// ---- running tasks ----

// progress compares a running task's elapsed time with the typical time of
// comparable runs (took: their seconds, newest first, from /api/wall), the
// median eta.js takes. null without an estimate. level is "" while within
// the typical time, "over" past it, "late" at twice it or more; text says
// so in words too: "8m / ~12m", "14m / ~12m · over usual", "25m / ~12m · 2× usual".
export function progress(elapsedMs, took) {
  const typ = typicalOf(took || [], (s) => s * 1000).ms;
  if (!typ) return null;
  const ratio = elapsedMs / typ;
  const level = ratio >= 2 ? "late" : ratio > 1 ? "over" : "";
  let text = durationText(elapsedMs) + " / ~" + durationText(typ);
  if (level === "late") text += " · " + Math.floor(ratio * 10) / 10 + "× usual";
  else if (level === "over") text += " · over usual";
  return { fraction: Math.min(1, ratio), level, text };
}

// How long a running task's activity line may stay the same before the
// wall says it's stuck.
export const STUCK_MS = 3 * MIN;

// stuckText is "stuck on go test for 6 min" when the activity hasn't changed
// for STUCK_MS, else "".
export function stuckText(activity, at, now) {
  if (!activity || !at) return "";
  const ms = now - new Date(at);
  if (ms < STUCK_MS) return "";
  return "stuck on " + activity + " for " + durationText(ms);
}

// nextCompact says whether the running list should be compact rows: when
// more tasks run than full cards fit (fits), and back to cards only once
// one fewer than that runs, so a count going up and down by one doesn't
// switch the layout on every poll.
export function nextCompact(compact, count, fits) {
  return compact ? count >= fits : count > fits;
}

// ---- burn-in ----

export const DRIFT_MS = 3 * MIN;
const DRIFT = [[0, 0], [3, 1], [5, 4], [2, 6], [-2, 5], [-5, 3], [-4, -1], [-1, -4], [3, -3]];

// drift is where the whole board sits at now: a few pixels off, moving to
// the next spot every DRIFT_MS, so an OLED screen's pixels don't hold the
// same picture for days.
export function drift(now) {
  const [x, y] = DRIFT[Math.floor(now / DRIFT_MS) % DRIFT.length];
  return { x, y };
}

// ---- off hours ----

// workHours reads ?hours=8-18 (default; "all" for always) and ?days=1-5
// (0 is Sunday; default Monday to Friday) from params.
export function workHours(params) {
  const cfg = { from: 8, to: 18, days: [1, 2, 3, 4, 5], always: false };
  const hours = params.get("hours");
  if (hours === "all") cfg.always = true;
  const m = /^(\d{1,2})-(\d{1,2})$/.exec(hours || "");
  if (m && +m[1] <= 24 && +m[2] <= 24 && +m[1] !== +m[2]) [cfg.from, cfg.to] = [+m[1], +m[2]];
  const d = /^([0-6])-([0-6])$/.exec(params.get("days") || "");
  if (d) {
    cfg.days = [];
    for (let i = +d[1]; ; i = (i + 1) % 7) {
      cfg.days.push(i);
      if (i === +d[2]) break;
    }
  }
  return cfg;
}

// offHours says whether date is outside cfg's work hours. Hours may wrap
// past midnight (22-6).
export function offHours(date, cfg) {
  if (cfg.always) return false;
  if (!cfg.days.includes(date.getDay())) return true;
  const h = date.getHours();
  const working = cfg.from < cfg.to ? h >= cfg.from && h < cfg.to : h >= cfg.from || h < cfg.to;
  return !working;
}

// quiet says whether the board has nothing to show: nothing runs and nothing
// waits on a person.
export const quiet = (d) => !d.running.length && !d.needs.length && !(d.status && d.status.waiting);

// weekText is the off-hours summary: "This week: 84 merged · 91% first try".
export function weekText(w) {
  let s = "This week: " + w.merged + " merged";
  if (w.first_try != null) s += " · " + Math.round(w.first_try * 100) + "% first try";
  return s;
}

// ---- figures ----

// deltaText compares today's figure with the average day's at the same
// time, with an arrow and a word: "▲ above avg 9", "▼ below avg 9",
// "= avg 9". fmt words a value. "" without an average.
export function deltaText(now, avg, fmt = String) {
  if (avg == null || now == null) return "";
  const close = Math.abs(now - avg) <= Math.max(Math.abs(avg) * 0.05, 1e-9);
  if (close || fmt(now) === fmt(avg)) return "= avg " + fmt(avg);
  return (now > avg ? "▲ above" : "▼ below") + " avg " + fmt(avg);
}

// oneDecimal is 9, 2.4, 0.3.
export const oneDecimal = (v) => String(Math.round(v * 10) / 10);

// ---- the overall status ----

export const STATUS_SYMBOL = { ok: "●", warn: "▲", bad: "✕" };

// headline words the overall status for the header: its symbol, a line
// ("All good · 5 running", "Needs action · 3 running · 2 waiting on
// people") and, below it, why it isn't all good ("queue paused · 1 open
// issue").
export function headline(st) {
  if (!st) return { symbol: "", text: "Loading…", why: "" };
  const parts = [st.label, st.running + " running"];
  if (st.waiting) parts.push(st.waiting + " waiting on people");
  return { symbol: STATUS_SYMBOL[st.level] || "", text: parts.join(" · "), why: (st.reasons || []).join(" · ") };
}

// fullText words a usage window heading to 100% before it resets
// (full_at, from /api/wall), "" otherwise: "100% in ~2h at this rate".
export function fullText(w, now, short = false) {
  if (!w || !w.runs_out || !w.full_at) return "";
  const left = new Date(w.full_at) - now;
  if (left < MIN) return "at the limit";
  return "100% in ~" + durationText(left < HOUR ? left : Math.round(left / HOUR) * HOUR) + (short ? "" : " at this rate");
}
