import { $, h, setLabel, whenText } from "./dom.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { saveField } from "./autosave.js";
import { state } from "./state.js";
import { setStatus } from "./status.js";
import { aheadOfPace, paceMs, paceStateText, paceText, waitText } from "../usagetime.js";
import { HOUR, resetText } from "../time.js";

// ------------------------------------------------------------------ usage & queue
export async function loadUsage() {
  state.usage = await api("GET", "/api/usage");
  renderUsage();
}

const windowLabel = (n) => ({ five_hour: "5-hour window", seven_day: "Weekly", seven_day_opus: "Weekly (Opus)" })[n] || n;
export const shortLabel = (n) => ({ five_hour: "5h", seven_day: "7d", seven_day_opus: "7d Opus" })[n] || n;
const windowHours = { five_hour: 5, seven_day: 168, seven_day_opus: 168 };
// Share of the window elapsed since its last reset (0..1), or null for an unknown window length.
const windowElapsed = (w) => {
  const hours = windowHours[w.name];
  if (!hours) return null;
  const span = hours * 3600000;
  return Math.min(1, Math.max(0, (span - (new Date(w.resets_at) - Date.now())) / span));
};

// The Claude accounts to show one by one: on a hub whose runners (yours, in
// this org) are on more than one (GET /api/usage's accounts), else null.
const shownAccounts = (u) => (u?.accounts?.length > 1 ? u.accounts : null);
const liveWindows = (u) => (u.windows || []).filter((w) => new Date(w.resets_at) > new Date());
// The account whose pace settings the sheet shows, with several: the one
// picked, else the one the hub shows first (your newest runner's).
let paceAccount = "";
function shownPace(u) {
  const accts = shownAccounts(u)?.filter((a) => a.account);
  if (!accts?.length) return u?.pace;
  if (!accts.some((a) => a.account === paceAccount)) {
    paceAccount = accts.some((a) => a.account === u.pace?.account) ? u.pace.account : accts[0].account;
  }
  return accts.find((a) => a.account === paceAccount).pace;
}

// When the shown snapshot came in: its pace gap counts down from then.
let snap = null, snapAt = 0;

export function renderUsage() {
  const u = state.usage;
  if (u !== snap) { snap = u; snapAt = Date.now(); }
  if (!u || (!u.known && !u.manual_pause)) { setStatus("usage", null); return; }
  const accts = shownAccounts(u);
  const live = accts ? accts.flatMap(liveWindows) : liveWindows(u);
  const top = live.reduce((a, w) => (!a || w.utilization > a.utilization ? w : a), null);
  const overPace = (w) => { const e = windowElapsed(w); return e !== null && w.utilization > e; };
  // Waiting only to stay on the weekly pace is the queue working as asked, not a stop.
  const stopped = u.paused && !u.pacing;
  const tone = stopped ? "bad" : u.pacing || (top && (top.utilization >= 0.7 || overPace(top))) ? "warn" : "ok";

  const until = (x) => (x.until && !x.manual_pause ? ` until ${whenText(x.until)}` : "");
  // A wait for the pace counts down to its until, live.
  const why = (x) => (x.pacing && x.until && !x.manual_pause
    ? `Waiting ${waitText(new Date(x.until) - Date.now())} to stay on pace` : x.reason);
  const reason = u.paused
    ? `${why(u)}. No new tasks start${until(u)}; running ones finish.`
    : accts ? "Tasks start normally. A new task goes to a runner on the account with the most usage left." : "Tasks start normally.";
  // One account's wait, under its name, while another still runs tasks.
  const accountReason = (a) => (a.paused && !u.paused
    ? h("p", { class: `usage-reason${a.pacing ? " pacing" : ""}` }, `${why(a)}: no new tasks on this account${until(a)}.`) : null);
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
      resetClock: `Resets ${whenText(w.resets_at)}`,
      exact: `${pct}% used${pace === null ? "" : `, ${pace}% of the window elapsed (${points >= 0 ? "+" : "−"}${Math.abs(points)} points vs pace)`}`,
    };
  };
  const meter = (w, compact, label = "") => {
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
      h("div", { class: `meter ${cls}`, role: "meter", "aria-valuenow": pct, "aria-valuemin": 0, "aria-valuemax": 100, "aria-label": `${windowLabel(w.name)}${label ? `, ${label}` : ""}` },
        h("span", { style: `width:${fill}%` }),
        pace !== null && fill > pace ? h("b", { class: "over", style: `left:${pace}%;width:${fill - pace}%` }) : null,
        pace !== null ? h("i", { class: "mark pace", style: `left:${pace}%`, title: `Pace: ${pace}% of the window elapsed, ${pct}% used (${pacing})` }) : null),
      compact
        ? h("div", { class: `meter-pace${d.over ? " over" : ""}`, title: `${d.exact}. ${d.resetClock}.` },
          d.reset, pacing ? ` · ${pacing}` : null)
        : pacing ? h("div", { class: `meter-pace${d.over ? " over" : ""}`, title: d.exact }, pacing) : null);
  };
  // With several Claude accounts: each one's meters under its name (and,
  // in the sheet, the runners on it).
  const perAccount = (compact) => accts.map((a) => h(compact ? "div" : "section", { class: "usage-account", "aria-label": `Claude account ${a.label}` },
    h(compact ? "div" : "h3", { class: "usage-account-name" }, a.label),
    !compact && a.account && a.runners?.length ? h("p", { class: "usage-account-runners" }, `on ${a.runners.join(", ")}`) : null,
    accountReason(a),
    liveWindows(a).map((w) => meter(w, compact, a.label))));
  // In the status popover: a summary, why the queue waits, and a compact meter per window.
  setStatus("usage", {
    tone, label: "Claude usage",
    text: u.pacing ? "Waiting for pace" : u.paused ? "Queue paused"
      : top ? `${shortLabel(top.name)} ${Math.round(top.utilization * 100)}%${overPace(top) ? ", ahead of pace" : ""}${accts ? `, ${accts.length} accounts` : ""}` : "Queue running",
    detail: h("div", { class: "status-usage" },
      u.paused ? h("p", { class: `usage-reason${u.pacing ? " pacing" : ""}` }, reason) : null,
      accts ? perAccount(true) : live.map((w) => meter(w, true))),
    action: { label: "Details", run: openUsage },
  });

  $("#usage-reason").textContent = reason;
  $("#usage-meters").replaceChildren(...(accts ? perAccount(false) : live.map((w) => meter(w, false))));
  renderPace(shownPace(u), accts);
  // In the project menu, beside Pause this project: the queue is every project's.
  const q = $("#pm-queue");
  setLabel(q, u.manual_pause ? "play" : "pause", u.manual_pause ? "Resume queue (all projects)" : "Pause queue (all projects)");
  q.title = u.manual_pause ? "Let queued tasks start again, in every project of this Dash" : "No new task starts in any project of this Dash; running tasks finish";
}

// The "Follow weekly pace" settings and where the queue stands with them.
// On a hub (p.hub) they are a Claude account's: the hub paces every runner
// logged in to it as one budget, and caps catching up at a multiple of
// their usual parallelism rather than a number of tasks. With runners on
// several accounts (accts), the sheet picks which account's it shows.
function renderPace(p, accts) {
  const follow = !!p?.follow;
  const hub = !!p?.hub;
  const box = $("#pace-follow");
  box.checked = follow;
  box.disabled = hub && !p.account;
  const named = accts?.filter((a) => a.account) || [];
  $("#pace-help").textContent = !hub
    ? "Keeps weekly usage on the pace line: no new tasks while ahead of it, more at once while behind."
    : named.length > 1
      ? "Each Claude account has its own setting. Paces every runner on the account picked here as one budget: no new tasks while ahead of the pace line, more at once while behind."
      : "Paces every runner on your Claude account as one budget: no new tasks while ahead of the pace line, more at once while behind.";
  const acct = $("#pace-account");
  acct.hidden = !hub;
  // Not under the user's hand (an open account pick) on a live tick.
  if (hub && !acct.contains(document.activeElement)) {
    const rs = p.runners || [];
    const on = rs.length ? h("span", {}, `on ${rs.join(", ")}`) : null;
    const pick = () => h("label", { class: "pace-pick" }, h("span", {}, "Settings for"),
      h("select", { id: "pace-account-pick", onchange: (e) => { paceAccount = e.target.value; renderPace(shownPace(state.usage), shownAccounts(state.usage)); } },
        named.map((a) => h("option", { value: a.account, selected: a.account === p.account }, a.account))));
    acct.replaceChildren(...(named.length > 1
      ? [pick(), on]
      : p.account
        ? [h("span", {}, "Claude account ", h("strong", {}, p.account)), on]
        : [h("span", {}, "Your runners haven't said which Claude account they're logged in to. Update Dash on them to pace them here.")]));
  }
  const text = paceStateText(p, Date.now() - snapAt);
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
  const p = shownPace(state.usage);
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

// Live: what the meters and waits say moves with the clock between
// snapshots (the share of each window elapsed, the pace gap, the countdowns),
// so they redraw every second while the Status popover or the usage sheet is
// open, and every half minute otherwise for the status dot. A wait for the
// pace that has run out asks for a fresh snapshot (at most every 30s).
let ticks = 0, refetched = 0;
setInterval(() => {
  ticks++;
  if (document.hidden || !state.usage) return;
  const open = $("#status-pop")?.open || $("#usage-sheet")?.open;
  if (!open && ticks % 30) return;
  renderUsage();
  const u = state.usage;
  const due = [u, ...(u.accounts || [])].some((x) => x.pacing && x.until && new Date(x.until) <= Date.now());
  if (due && Date.now() - refetched > 30000) { refetched = Date.now(); loadUsage().catch(() => {}); }
}, 1000);

export function openUsage() { renderUsage(); $("#usage-sheet").showModal(); }
$("#pm-queue").addEventListener("click", attempt(async () => {
  state.usage = await api("POST", state.usage?.manual_pause ? "/api/queue/resume" : "/api/queue/pause");
  renderUsage();
  toast(state.usage.manual_pause ? "Queue paused" : "Queue resumed");
}));
