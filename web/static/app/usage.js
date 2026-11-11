import { $, h, setLabel, timeLocale } from "./dom.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { HOUR, aheadOfPace, paceMs, paceText, resetText } from "../usagetime.js";

// ------------------------------------------------------------------ usage & queue
export async function loadUsage() {
  state.usage = await api("GET", "/api/usage");
  renderUsage();
}

const windowLabel = (n) => ({ five_hour: "5-hour window", seven_day: "Weekly", seven_day_opus: "Weekly (Opus)" })[n] || n;
export const shortLabel = (n) => ({ five_hour: "5h", seven_day: "7d", seven_day_opus: "7d Opus" })[n] || n;
const fmtTime = (iso) => {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay ? d.toLocaleTimeString(timeLocale, { hour: "2-digit", minute: "2-digit" }) : d.toLocaleString(timeLocale, { weekday: "short", hour: "2-digit", minute: "2-digit" });
};
const windowHours = { five_hour: 5, seven_day: 168, seven_day_opus: 168 };
// Share of the window elapsed since its last reset (0..1), or null for an unknown window length.
const windowElapsed = (w) => {
  const hours = windowHours[w.name];
  if (!hours) return null;
  const span = hours * 3600000;
  return Math.min(1, Math.max(0, (span - (new Date(w.resets_at) - Date.now())) / span));
};

export function renderUsage() {
  const u = state.usage;
  const pill = $("#usage-pill");
  if (!u || (!u.known && !u.manual_pause)) { pill.hidden = true; return; }
  pill.hidden = false;
  const live = (u.windows || []).filter((w) => new Date(w.resets_at) > new Date());
  const top = live.reduce((a, w) => (!a || w.utilization > a.utilization ? w : a), null);
  const overPace = (w) => { const e = windowElapsed(w); return e !== null && w.utilization > e; };
  pill.className = `usage-pill${u.paused ? " paused" : top && (top.utilization >= 0.7 || overPace(top)) ? " warn" : ""}`;
  pill.title = u.paused ? u.reason : top && overPace(top) ? "Claude subscription usage, ahead of pace" : "Claude subscription usage";

  const reason = u.paused
    ? `${u.reason}. No new tasks start${u.until && !u.manual_pause ? ` until ${fmtTime(u.until)}` : ""}; running ones finish.`
    : "Tasks start normally.";
  // What a window's figures say, in words and in exact numbers for tooltips.
  const describe = (w) => {
    const pct = Math.round(w.utilization * 100);
    const elapsed = windowElapsed(w);
    const pace = elapsed === null ? null : Math.round(elapsed * 100);
    const gap = elapsed === null ? null : paceMs(w.utilization, elapsed, windowHours[w.name] * HOUR);
    const points = pace === null ? null : pct - pace;
    return {
      pct, pace, gap,
      over: gap !== null && aheadOfPace(gap),
      reset: resetText(w.resets_at),
      resetClock: `Resets ${fmtTime(w.resets_at)}`,
      exact: `${pct}% used${pace === null ? "" : `, ${pace}% of the window elapsed (${points >= 0 ? "+" : "−"}${Math.abs(points)} points vs pace)`}`,
    };
  };
  const meter = (w, compact) => {
    const d = describe(w);
    const { pct, pace } = d;
    const cls = w.utilization >= (u.pause_at || 1) ? "full" : w.utilization >= 0.7 ? "warn" : "";
    const fill = Math.min(100, pct);
    const pacing = d.gap === null ? null : paceText(d.gap);
    return h("div", {},
      h("div", { class: "meter-head" },
        h("span", {}, compact ? shortLabel(w.name) : windowLabel(w.name)),
        compact
          ? h("span", { title: d.exact }, `${pct}%`)
          : h("span", { title: `${d.exact}. ${d.resetClock}.` }, `${pct}% · ${d.reset}`)),
      h("div", { class: `meter ${cls}`, role: "meter", "aria-valuenow": pct, "aria-valuemin": 0, "aria-valuemax": 100, "aria-label": windowLabel(w.name) },
        h("span", { style: `width:${fill}%` }),
        pace !== null && fill > pace ? h("b", { class: "over", style: `left:${pace}%;width:${fill - pace}%` }) : null,
        pace !== null ? h("i", { class: "mark pace", style: `left:${pace}%`, title: `Pace: ${pace}% of the window elapsed, ${pct}% used (${pacing})` }) : null,
        u.pause_at > 0 && u.pause_at < 1 ? h("i", { class: "mark", style: `left:${u.pause_at * 100}%`, title: "Queue pauses here" }) : null),
      compact
        ? h("div", { class: `meter-pace${d.over ? " over" : ""}`, title: `${d.exact}. ${d.resetClock}.` },
          d.reset, pacing ? ` · ${pacing}` : null)
        : pacing ? h("div", { class: `meter-pace${d.over ? " over" : ""}`, title: d.exact }, pacing) : null);
  };
  const pie = (w) => {
    const d = describe(w);
    const { pct, pace } = d;
    const cls = w.utilization >= (u.pause_at || 1) ? " full" : w.utilization >= 0.7 ? " warn" : "";
    const pacing = d.gap === null ? "" : ` · ${paceText(d.gap)}`;
    return h("span", { class: "pie-wrap", title: `${windowLabel(w.name)}: ${d.exact}${pacing} · ${d.reset} (${fmtTime(w.resets_at)})` },
      h("span", { class: `pie${cls}${pace !== null ? " paced" : ""}`, style: `--p:${Math.min(100, pct)};--e:${pace ?? 0}`, role: "img", "aria-label": `${windowLabel(w.name)} ${pct}%` },
        h("b", {}, pct)),
      h("small", {}, shortLabel(w.name)));
  };
  pill.replaceChildren(
    h("span", { class: "usage-pies" }, live.map(pie)),
    h("span", { class: "usage-sum" }, u.paused ? "Queue paused" : top ? `${shortLabel(top.name)} ${Math.round(top.utilization * 100)}%${overPace(top) ? " ▲" : ""}` : "Usage"),
    h("span", { class: "usage-detail" },
      h("span", { class: "usage-head" }, h("span", {}, "Claude usage"), h("span", {}, u.paused ? "Paused" : "Queue running")),
      u.paused ? h("span", { class: "usage-reason" }, reason) : null,
      live.map((w) => meter(w, true))));

  $("#usage-reason").textContent = reason;
  $("#usage-meters").replaceChildren(...live.map((w) => meter(w, false)));
  $("#usage-note").textContent = u.pause_at > 0 && u.pause_at < 1
    ? `The queue pauses automatically at ${Math.round(u.pause_at * 100)}% of any window.`
    : "The queue pauses automatically when a window's limit is reached.";
  setLabel($("#queue-toggle"), u.manual_pause ? "play" : "pause", u.manual_pause ? "Resume queue" : "Pause queue");
  $("#queue-toggle").className = `btn ${u.manual_pause ? "primary" : ""}`;
}

$("#usage-pill").addEventListener("click", () => { renderUsage(); $("#usage-sheet").showModal(); });
$("#queue-toggle").addEventListener("click", attempt(async () => {
  state.usage = await api("POST", state.usage?.manual_pause ? "/api/queue/resume" : "/api/queue/pause");
  renderUsage();
  toast(state.usage.manual_pause ? "Queue paused" : "Queue resumed");
}));
