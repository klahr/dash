import { $, clockTime, h, setLabel, timeLocale } from "./dom.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { saveField } from "./autosave.js";
import { state } from "./state.js";
import * as store from "./store.js";
import { HOUR, aheadOfPace, paceMs, paceStateText, paceText, resetText } from "../usagetime.js";

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
  return sameDay ? clockTime(d) : d.toLocaleString(timeLocale, { weekday: "short", hour: "2-digit", minute: "2-digit" });
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
  if (!u || (!u.known && !u.manual_pause)) { pill.hidden = $("#usage-fold").hidden = true; return; }
  pill.hidden = $("#usage-fold").hidden = false;
  const live = (u.windows || []).filter((w) => new Date(w.resets_at) > new Date());
  const top = live.reduce((a, w) => (!a || w.utilization > a.utilization ? w : a), null);
  const overPace = (w) => { const e = windowElapsed(w); return e !== null && w.utilization > e; };
  // Waiting only to stay on the weekly pace is the queue working as asked, not a stop.
  const stopped = u.paused && !u.pacing;
  pill.className = `usage-pill${stopped ? " paused" : u.pacing || (top && (top.utilization >= 0.7 || overPace(top))) ? " warn" : ""}`;
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
    const cls = w.utilization >= 1 ? "full" : w.utilization >= 0.7 ? "warn" : "";
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
        pace !== null ? h("i", { class: "mark pace", style: `left:${pace}%`, title: `Pace: ${pace}% of the window elapsed, ${pct}% used (${pacing})` }) : null),
      compact
        ? h("div", { class: `meter-pace${d.over ? " over" : ""}`, title: `${d.exact}. ${d.resetClock}.` },
          d.reset, pacing ? ` · ${pacing}` : null)
        : pacing ? h("div", { class: `meter-pace${d.over ? " over" : ""}`, title: d.exact }, pacing) : null);
  };
  const pie = (w) => {
    const d = describe(w);
    const { pct, pace } = d;
    const cls = w.utilization >= 1 ? " full" : w.utilization >= 0.7 ? " warn" : "";
    const pacing = d.gap === null ? "" : ` · ${paceText(d.gap)}`;
    return h("span", { class: "pie-wrap", title: `${windowLabel(w.name)}: ${d.exact}${pacing} · ${d.reset} (${fmtTime(w.resets_at)})` },
      h("span", { class: `pie${cls}${pace !== null ? " paced" : ""}`, style: `--p:${Math.min(100, pct)};--e:${pace ?? 0}`, role: "img", "aria-label": `${windowLabel(w.name)} ${pct}%` },
        h("b", {}, pct)),
      h("small", {}, shortLabel(w.name)));
  };
  pill.replaceChildren(
    h("span", { class: "usage-pies" }, live.map(pie)),
    h("span", { class: "usage-sum" }, u.pacing ? "Pacing" : u.paused ? "Queue paused" : top ? `${shortLabel(top.name)} ${Math.round(top.utilization * 100)}%${overPace(top) ? " ▲" : ""}` : "Usage"),
    h("span", { class: "usage-detail" },
      h("span", { class: "usage-head" }, h("span", {}, "Claude usage"), h("span", {}, u.pacing ? "Waiting for pace" : u.paused ? "Paused" : "Queue running")),
      u.paused ? h("span", { class: `usage-reason${u.pacing ? " pacing" : ""}` }, reason) : null,
      live.map((w) => meter(w, true))));

  $("#usage-reason").textContent = reason;
  $("#usage-meters").replaceChildren(...live.map((w) => meter(w, false)));
  renderPace(u.pace);
  // In the project menu, beside Pause this project: the queue is every project's.
  const q = $("#pm-queue");
  setLabel(q, u.manual_pause ? "play" : "pause", u.manual_pause ? "Resume queue (all projects)" : "Pause queue (all projects)");
  q.title = u.manual_pause ? "Let queued tasks start again, in every project of this Dash" : "No new task starts in any project of this Dash; running tasks finish";
}

// The "Follow weekly pace" settings and where the queue stands with them.
// On a hub (p.hub) they are your Claude account's: the hub paces every
// runner logged in to it as one budget, and caps catching up at a multiple
// of their usual parallelism rather than a number of tasks.
function renderPace(p) {
  const follow = !!p?.follow;
  const hub = !!p?.hub;
  const box = $("#pace-follow");
  box.checked = follow;
  box.disabled = hub && !p.account;
  $("#pace-help").textContent = hub
    ? "Paces every runner on your Claude account as one budget: no new tasks while ahead of the pace line, more at once while behind."
    : "Keeps weekly usage on the pace line: no new tasks while ahead of it, more at once while behind.";
  const acct = $("#pace-account");
  acct.hidden = !hub;
  if (hub) {
    const rs = p.runners || [];
    acct.replaceChildren(...(p.account
      ? [h("span", {}, "Claude account ", h("strong", {}, p.account)),
        rs.length ? h("span", {}, `on ${rs.join(", ")}`) : null]
      : [h("span", {}, "Your runners haven't said which Claude account they're logged in to. Update Dash on them to pace them here.")]));
  }
  const text = paceStateText(p);
  const st = $("#pace-state");
  st.hidden = !text;
  st.textContent = text || "";
  st.className = `pace-state ${p?.state || ""}`;
  $("#pace-max-row").hidden = !follow || hub;
  $("#pace-boost-row").hidden = !follow || !hub;
  if (hub) {
    const sel = $("#pace-boost");
    const v = String(p.max_boost || 2);
    if (![...sel.options].some((o) => o.value === v)) sel.append(h("option", { value: v }, `${v}× usual`));
    sel.value = v;
    return;
  }
  const max = $("#pace-max");
  const base = Math.max(1, p?.parallel || 1);
  max.min = String(base);
  max.placeholder = String(2 * base);
  if (document.activeElement !== max) max.value = p?.max_parallel ? String(p.max_parallel) : "";
  max.title = `Usually ${base} at once; empty = ${2 * base}`;
}

// The pace saves as it changes: "Saved" in the sheet's header, or the error
// under the field (the number as typed stays, to fix it).
const savePace = (e) => saveField(e.target, async () => {
  const p = state.usage?.pace;
  if (p?.hub) {
    state.usage = await api("PUT", "/api/queue/pace", { follow: $("#pace-follow").checked, max_boost: Number($("#pace-boost").value) || 0, account: p.account || "" });
  } else {
    const v = $("#pace-max").value.trim();
    const max = v === "" ? 0 : Math.round(Number(v));
    if (!Number.isFinite(max) || max < 0 || max > 64) throw new Error("Enter a number from 1 to 64.");
    state.usage = await api("PUT", "/api/queue/pace", { follow: $("#pace-follow").checked, max_parallel: max });
  }
  renderUsage(); // shows what was saved
});
$("#pace-follow").addEventListener("change", savePace);
$("#pace-max").addEventListener("change", savePace);
$("#pace-boost").addEventListener("change", savePace);

// Folding the sidebar's usage card leaves one line, its pies and a summary,
// so the projects list above it gets the room. Kept per browser.
function foldUsage(folded) {
  $("#usage-box").classList.toggle("folded", folded);
  const btn = $("#usage-fold");
  const text = folded ? "Show Claude usage in full" : "Fold Claude usage";
  btn.setAttribute("aria-expanded", String(!folded));
  btn.setAttribute("aria-label", text);
  btn.title = text;
}
foldUsage(store.flag("dash.usageFolded"));
$("#usage-fold").addEventListener("click", () => {
  const folded = !$("#usage-box").classList.contains("folded");
  store.setFlag("dash.usageFolded", folded);
  foldUsage(folded);
});

$("#usage-pill").addEventListener("click", () => { renderUsage(); $("#usage-sheet").showModal(); });
$("#pm-queue").addEventListener("click", attempt(async () => {
  state.usage = await api("POST", state.usage?.manual_pause ? "/api/queue/resume" : "/api/queue/pause");
  renderUsage();
  toast(state.usage.manual_pause ? "Queue paused" : "Queue resumed");
}));
