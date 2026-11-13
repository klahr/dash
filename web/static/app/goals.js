import { renderMarkdown } from "../editor.js";
import { $, $$, dateTimeText, fill, h, ib, ico, plural } from "./dom.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { ask, attempt, busy, fail, leave, toast } from "./dialogs.js";
import { STATUS_LABEL, state } from "./state.js";
import { eventFor, latest, projectKey } from "./events.js";
import { agoText, renderBoard, title } from "./branches.js";
import { detailBackground, openDetail, renderDetail } from "./detail.js";
import { openGoalPlan } from "./todoform.js";
import { openGoalChat } from "./chatpane.js";
import { fmtTokens } from "./runstats.js";
import { notifyGoal } from "./notifications.js";
import { showView } from "./nav.js";

// ------------------------------------------------------------------ goals
// A goal is something bigger than one task: a coordinator (Claude) plans its
// tasks, dispatches them and reviews what each one did; approving and
// merging stay the user's (docs/goals.md). The Goals page lists the
// project's goals with their progress; one opens in a pane beside the list
// (full screen with Back on a phone), like a History commit.
//
// On the board, a goal's tasks stay ordinary cards in the usual lanes. Each
// carries a chip with its goal's name, in a colour of its own; pressing it
// shows only that goal's tasks (state.goalFilter) until the chip at the top
// of the board clears it. The goals are state.goals, by id.
export const GOAL_STATUS = {
  planning: "Planning", working: "Working", waiting_for_you: "Waiting for you",
  paused: "Paused", done: "Done", out_of_budget: "Out of budget",
};
// The list's order: what needs you first, finished goals last.
const STATUS_ORDER = ["waiting_for_you", "planning", "working", "paused", "out_of_budget", "done"];

export const gp = {
  // The goals as a list; they are kept in state.goals (by id), which the
  // board's chips and badge read from startup.
  get list() { return [...state.goals.values()]; },
  set list(v) { state.goals = new Map(v.map((g) => [g.id, g])); },
  loaded: false,
  project: null,   // the project the list is of
  get open() { return state.goalOpen; }, // in state: nav.js may close it before this module has run
  set open(v) { state.goalOpen = v; },
  detail: null,    // that goal in full (with its log)
  error: "",
  opener: null,
  editing: null,   // the goal the sheet edits (null: a new one)
  planFor: new Set(), // goals made here: their first plan opens by itself once it is in
};

const goalAPI = (id) => `/api/goals/${id}`;
const prefix = () => gp.project?.prefix || "";

// loadGoals reads the project's goals. Events seen while the list was on
// its way are replayed over it. A Dash from before goals has none.
export async function loadGoals() {
  const p = state.project;
  if (!p) return resetGoals();
  let list = [], seen = [];
  try {
    ({ data: list, seen } = await latest(() => api("GET", `/api/projects/${p.id}/goals`, null, p.prefix || "")));
  } catch { /* a Dash from before goals: its tasks have none */ }
  if (projectKey(state.project) !== projectKey(p)) return; // another project's by now
  const goals = new Map((list || []).map((g) => [g.id, g]));
  for (const { ev, m } of seen) {
    if (ev.type === "goal.updated" && eventFor(p, ev, m)) goals.set(ev.data.id, { ...goals.get(ev.data.id), ...ev.data });
  }
  gp.project = p;
  state.goals = goals;
  gp.loaded = true;
  renderGoals();
  renderBoard();
  if (state.detail) renderDetail();
  if (gp.open && !state.goals.has(gp.open)) closeGoal();
}

export function resetGoals() {
  closeGoal();
  gp.list = [];
  gp.loaded = false;
  gp.project = state.project;
  renderGoals();
}

// waitingGoals are the project's goals that wait for you: a plan to accept,
// a question to answer, drafts to queue.
// (Hoisted: renderBoard calls it, and may run before this module has.)
export function waitingGoals() { return [...state.goals.values()].filter((g) => g.status === "waiting_for_you"); }

// goalTasks are g's tasks, each as the board has it (with its dependencies)
// where it is loaded, in dependency order: a task after those it needs.
export function goalTasks(g) {
  const known = new Map((g.tasks || []).map((t) => [t.id, t]));
  for (const t of state.todos.values()) if (t.goal_id === g.id && !known.has(t.id)) known.set(t.id, t);
  const tasks = [...known.values()].map((t) => state.todos.get(t.id) || t);
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const out = [], seen = new Set();
  const visit = (t, path) => {
    if (seen.has(t.id) || path.has(t.id)) return;
    path.add(t.id);
    for (const d of t.depends_on || []) if (byId.has(d)) visit(byId.get(d), path);
    seen.add(t.id);
    out.push(t);
  };
  tasks.forEach((t) => visit(t, new Set()));
  return out;
}

const merged = (ts) => ts.filter((t) => t.status === "merged").length;

// progress is a .cl-progress bar: "4 of 7 merged".
function progress(g) {
  const ts = goalTasks(g), n = ts.length, m = merged(ts);
  const bar = h("span", { style: `width:${n ? Math.round(100 * m / n) : 0}%` });
  return h("div", { class: `cl-progress goal-progress${n && m === n ? " complete" : ""}` },
    h("div", { class: "cl-bar", role: "progressbar", "aria-valuemin": "0", "aria-valuemax": String(n), "aria-valuenow": String(m), "aria-label": "Tasks merged" }, bar),
    h("span", { class: "cl-count" }, n ? `${m} of ${n} merged` : g.status === "planning" ? "Planning…" : "No tasks yet"));
}

const statusTag = (g) => h("span", { class: `status goal-status g-${g.status}` },
  g.running ? h("span", { class: "act-spin", "aria-hidden": "true" }) : null,
  GOAL_STATUS[g.status] || g.status);

// What a waiting goal waits for, in a few words.
function waitText(g) {
  if (g.status !== "waiting_for_you") return "";
  if (g.waiting_for === "plan") return `A plan of ${plural(g.plan?.length || 0, "task")} waits for you`;
  if (g.waiting_for === "question") return "The coordinator asks you something";
  if (g.waiting_for === "drafts") return "Its drafts wait for you to queue them";
  return g.note || "";
}

export function renderGoals() {
  const list = $("#goal-list");
  if (!list) return;
  const goals = [...gp.list].sort((a, b) => (STATUS_ORDER.indexOf(a.status) - STATUS_ORDER.indexOf(b.status)) || b.updated_at.localeCompare(a.updated_at));
  $("#goal-empty").hidden = goals.length > 0;
  $("#goal-empty-title").textContent = state.project ? "No goals yet" : "No project";
  $("#goal-empty-text").textContent = state.project
    ? "A goal is something bigger than one task. Say what you want done: a coordinator reads the code, plans the tasks and reviews what each one did. Approving and merging stay yours."
    : "Select a project to see its goals.";
  $("#goal-new").hidden = !state.project;
  const focused = document.activeElement?.closest?.("#goal-list li")?.dataset.id;
  list.replaceChildren(...goals.map((g) => h("li", { class: `goal${g.id === gp.open ? " selected" : ""}`, "data-id": g.id },
    h("button", { type: "button", class: "goal-open", "aria-controls": "goal-detail", "aria-current": g.id === gp.open ? "true" : null, onclick: () => openGoal(g.id).catch(fail) },
      h("span", { class: "goal-row" }, h("span", { class: "goal-title" }, g.title), statusTag(g)),
      progress(g),
      waitText(g) ? h("span", { class: "goal-wait" }, waitText(g)) : null))));
  if (focused) $(`#goal-list li[data-id="${focused}"] .goal-open`)?.focus({ preventScroll: true });
  renderGoalBadges();
}

// The Goals buttons' badges count the goals that wait for you; the Board's
// badge and the tab title count them too (renderBoard).
function renderGoalBadges() {
  const n = waitingGoals().length;
  for (const b of [$("#goal-badge"), $("#menu-goal-badge")]) {
    if (!b) continue;
    b.hidden = !n;
    b.textContent = n || "";
    b.title = n ? `${plural(n, "goal")} waiting for you` : "";
  }
}

// ------------------------------------------------------------------ live updates
// goal.updated carries the goal without its log or tasks: those are kept
// from what was loaded, and the open goal is read again.
let detailTimer = 0;
export function onGoalEvent(ev) {
  const g = ev.data;
  if (!g?.id) return;
  const prev = state.goals.get(g.id) || null;
  const next = { ...prev, ...g, tasks: prev?.tasks || [], weekly_used: prev?.weekly_used };
  state.goals.set(g.id, next);
  announce(prev, next);
  renderGoals();
  renderBoard(); // its badge, and its tasks' chips
  if (state.detail && prev?.title !== g.title) renderDetail(); // the open task's chip
  if (gp.open === g.id) {
    clearTimeout(detailTimer);
    detailTimer = setTimeout(() => loadGoal(g.id).catch(() => {}), 300);
  }
  // Its first plan, for a goal made here: shown as soon as it is in.
  if (gp.planFor.has(g.id) && g.waiting_for === "plan" && g.plan?.length && !document.querySelector("dialog[open]")) {
    gp.planFor.delete(g.id);
    reviewPlan(next).catch(fail);
  }
  if (g.status === "done" || g.status === "out_of_budget") gp.planFor.delete(g.id);
}

// announce tells you about a goal that now waits for you, is done or out
// of budget.
function announce(prev, g) {
  if (!prev || (prev.status === g.status && prev.waiting_for === g.waiting_for)) return;
  const go = { label: "Open", run: () => showGoal(g.id) };
  if (g.status === "waiting_for_you") {
    toast(g.title, { tone: "review", head: "Goal waits for you", icon: "target", action: go });
    notifyGoal("Goal waits for you", g, waitText(g));
  } else if (g.status === "done") {
    toast(g.title, { tone: "ok", head: "Goal done", icon: "target", action: go });
    notifyGoal("Goal done", g, g.note);
  } else if (g.status === "out_of_budget") {
    toast(g.title, { tone: "error", head: "Goal out of budget", icon: "target", action: go });
    notifyGoal("Goal out of budget", g, g.note);
  }
}

// A task of a goal changed: its progress (and the open plan) follow.
export function goalTaskChanged(t) {
  if (!t?.goal_id || !gp.list.length) return;
  const g = state.goals.get(t.goal_id);
  if (!g) return;
  const tasks = g.tasks || [];
  const at = tasks.findIndex((x) => x.id === t.id);
  const item = { id: t.id, title: t.title, status: t.status };
  g.tasks = at >= 0 ? tasks.map((x, j) => (j === at ? item : x)) : [...tasks, item];
  if (state.view === "goals") renderGoals();
  if (gp.open === g.id) renderGoal();
}

// ------------------------------------------------------------------ the goal pane
// Open: beside the list on a desktop (not modal), full screen on a phone.
function setGoalModal() {
  const on = !$("#goal-detail").hidden && !desktop.matches;
  detailBackground().forEach((el) => { el.inert = on; });
  $("#gd-close").setAttribute("aria-label", desktop.matches ? "Close" : "Back");
}

// showGoal goes to the Goals page with goal id open (a toast's Open, a notification).
export async function showGoal(id) {
  showView("goals");
  await openGoal(id);
}

export async function openGoal(id) {
  const pane = $("#goal-detail");
  if (gp.open === id && !pane.hidden) return;
  if (pane.hidden) {
    const a = document.activeElement;
    gp.opener = a && a !== document.body ? a : null;
  }
  gp.open = id;
  gp.detail = state.goals.get(id) || null;
  gp.error = "";
  pane.hidden = false;
  setGoalModal();
  markSelected();
  renderGoal();
  $("#gd-body").scrollTop = 0;
  await loadGoal(id);
}

async function loadGoal(id) {
  try {
    const g = await api("GET", goalAPI(id), null, prefix());
    if (gp.open !== id) return;
    gp.detail = g;
    gp.error = "";
    state.goals.set(id, { ...g, log: undefined });
    renderGoals();
  } catch (e) {
    if (gp.open !== id) return;
    gp.error = e.message;
  }
  renderGoal();
}

export function closeGoal() {
  if (!state.goalOpen) return;
  clearTimeout(detailTimer);
  const pane = $("#goal-detail");
  const wasOpen = !pane.hidden;
  const id = gp.open;
  gp.open = gp.detail = null;
  markSelected();
  const hide = () => { if (!gp.open) { pane.hidden = true; setGoalModal(); } };
  if (wasOpen) leave(pane, "closing", hide); else hide();
  detailBackground().forEach((el) => { el.inert = false; });
  const opener = gp.opener;
  gp.opener = null;
  const a = document.activeElement;
  if (!wasOpen || (a && a !== document.body && !pane.contains(a))) return;
  const row = $(`#goal-list li[data-id="${id}"] .goal-open`);
  (row || (opener?.isConnected ? opener : null))?.focus();
}
$("#gd-close").addEventListener("click", closeGoal);
desktop.addEventListener("change", () => { if (gp.open) { setGoalModal(); renderGoal(); } });

function markSelected() {
  for (const li of $$("#goal-list li")) {
    const on = li.dataset.id === gp.open;
    li.classList.toggle("selected", on);
    $(".goal-open", li)?.toggleAttribute("aria-current", on);
    if (on) li.scrollIntoView({ block: "nearest" });
  }
}

// j/k on the Goals page: the next or previous goal, opened.
export function stepGoal(n) {
  const ids = $$("#goal-list li").map((li) => li.dataset.id);
  if (!ids.length) return;
  const i = ids.indexOf(gp.open);
  const next = ids[Math.max(0, Math.min(ids.length - 1, i < 0 ? (n > 0 ? 0 : ids.length - 1) : i + n))];
  if (next && next !== gp.open) {
    const onRow = document.activeElement?.matches?.("#goal-list .goal-open");
    openGoal(next).catch(fail);
    if (onRow) $(`#goal-list li[data-id="${next}"] .goal-open`)?.focus({ preventScroll: true });
  }
}

function renderGoal() {
  const g = gp.detail;
  if (!g || $("#goal-detail").hidden) return;
  $("#gd-title").textContent = g.title;
  const mode = g.mode === "queue" ? "Queues on its own" : "Asks before queuing";
  fill($("#gd-meta"), statusTag(g), h("span", {}, mode), g.created_at ? h("span", { title: dateTimeText(g.created_at) }, `started ${agoText(g.created_at)}`) : null);
  renderGoalActions(g);
  $("#gd-error").hidden = !gp.error;
  $("#gd-error").textContent = gp.error;
  renderNote(g);
  $("#gd-desc-wrap").hidden = !g.description;
  $("#gd-desc").innerHTML = renderMarkdown(g.description || "");
  renderPlan(g);
  renderBudget(g);
  renderLog(g);
}

// The buttons: what fits where the goal stands. Approve and Merge are never
// here: a goal's tasks are approved and merged on the board, by you.
function renderGoalActions(g) {
  const btn = (icon, label, run, cls = "btn small", title = "") => ib({ type: "button", class: cls, title, onclick: attempt((e) => busy(e.currentTarget, run)) }, icon, label);
  const active = ["planning", "working", "waiting_for_you"].includes(g.status);
  const resumable = g.status === "paused" || g.status === "out_of_budget";
  fill($("#gd-actions"),
    g.waiting_for === "plan" && g.plan?.length ? btn("checks", "Review plan", () => reviewPlan(g), "btn small primary", "Edit the coordinator's plan, then save it as drafts or queue it") : null,
    g.waiting_for === "drafts" ? btn("queue", "Queue drafts", () => queueDrafts(g), "btn small primary", "Queue the goal's draft tasks") : null,
    btn("comment", "Talk to coordinator", () => talk(g), g.waiting_for === "question" ? "btn small primary" : "btn small", "Open the coordinator's conversation in the chat"),
    active ? btn("pause", "Pause", () => goalAction(g, "pause"), "btn small", "Hold the coordinator; the tasks carry on") : null,
    resumable ? btn("play", "Resume", () => goalAction(g, "resume"), "btn small", g.status === "out_of_budget" ? "Raise the budget first (Edit goal)" : "Go on where it was") : null,
    g.status !== "done" ? btn("edit", "Edit goal", () => openGoalSheet({ goal: g })) : null,
    g.status !== "done" ? btn("stop", "Stop", () => stopGoal(g), "btn small danger", "End the goal for good; its tasks stay on the board") : null);
}

// The note says why the goal waits, ended or can't go on: the
// coordinator's question, its summary, the budget used up. Styled like a
// chat reply: it is the coordinator speaking.
function renderNote(g) {
  const el = $("#gd-note");
  const text = g.note || waitText(g);
  el.hidden = !text;
  if (!text) return;
  el.className = `goal-note msg assistant g-${g.status}`;
  fill(el, h("div", { class: "msg-from" }, g.status === "waiting_for_you" ? "Waiting for you" : GOAL_STATUS[g.status] || "Coordinator"),
    h("div", { class: "reply", innerHTML: renderMarkdown(text) }));
}

// Plan: the goal's tasks in dependency order, each linked to its task. A
// plan waiting to be accepted shows below them.
function renderPlan(g) {
  const ts = goalTasks(g);
  const num = new Map(ts.map((t, i) => [t.id, i + 1]));
  const rows = ts.map((t, i) => {
    const after = (t.depends_on || []).map((d) => num.get(d)).filter(Boolean).sort((a, b) => a - b);
    return h("li", { class: `goal-task s-${t.status}` },
      h("span", { class: "split-num", "aria-hidden": "true" }, String(i + 1)),
      h("button", { type: "button", class: "goal-task-open", title: "Open the task", onclick: () => openDetail(t.id).catch(fail) },
        h("span", { class: "goal-task-title" }, title({ title: t.title, prompt: t.prompt || t.title || t.id })),
        h("span", { class: "goal-task-sub" },
          h("span", { class: "status" }, t.paused ? "Paused" : STATUS_LABEL[t.status] || t.status),
          after.length ? h("span", {}, `after ${after.join(", ")}`) : null)));
  });
  const waiting = g.waiting_for === "plan" ? (g.plan || []) : [];
  const proposed = waiting.map((it, i) => h("li", { class: "goal-task proposed s-draft" },
    h("span", { class: "split-num", "aria-hidden": "true" }, String(ts.length + i + 1)),
    h("span", { class: "goal-task-open" },
      h("span", { class: "goal-task-title" }, it.title || it.prompt.split("\n")[0]),
      h("span", { class: "goal-task-sub" }, h("span", { class: "status" }, "Proposed"),
        it.after?.length ? h("span", {}, `after ${it.after.map((a) => ts.length + a + 1).join(", ")}`) : null))));
  const empty = !rows.length && !proposed.length
    ? [h("li", { class: "pane-empty" }, g.status === "planning" ? "The coordinator reads the code and plans the tasks…" : "No tasks yet.")]
    : [];
  $("#gd-plan").replaceChildren(...rows, ...proposed, ...empty);
}

// Budget: tasks made of the most it may make, the weekly share used, and
// the tokens its tasks took.
function renderBudget(g) {
  const ts = goalTasks(g);
  const b = g.budget || {};
  const runs = ts.flatMap((t) => t.run_stats || []);
  const tokens = runs.reduce((n, r) => n + (r.input_tokens || 0) + (r.output_tokens || 0) + (r.cache_read_tokens || 0) + (r.cache_creation_tokens || 0), 0);
  const cost = runs.reduce((n, r) => n + (r.cost_usd || 0), 0);
  const meter = (label, used, max, text) => h("div", { class: "goal-meter" },
    h("span", { class: "goal-meter-label" }, label),
    max ? h("div", { class: `cl-progress${used >= max ? " over" : ""}` },
      h("div", { class: "cl-bar", role: "progressbar", "aria-label": label, "aria-valuemin": "0", "aria-valuemax": String(max), "aria-valuenow": String(Math.min(used, max)) },
        h("span", { style: `width:${Math.min(100, Math.round(100 * used / max))}%` })),
      h("span", { class: "cl-count" }, text)) : h("span", { class: "cl-count" }, text));
  fill($("#gd-budget"),
    meter("Tasks", ts.length, b.max_tasks || 0, b.max_tasks ? `${ts.length} of ${b.max_tasks}` : `${ts.length} · no limit`),
    meter("Weekly share", g.weekly_used ?? 0, b.weekly_share || 0,
      b.weekly_share ? (g.weekly_used != null ? `${g.weekly_used.toFixed(1)}% of ${b.weekly_share}%` : `${b.weekly_share}%; use not known yet`) : "no limit"),
    meter("Tokens", 0, 0, runs.length ? `${fmtTokens(tokens)} · $${cost.toFixed(2)}` : "none yet"));
}

const LOG_KIND = { plan: "Plan", wake: "Woken", action: "Did", note: "Note", user: "You", status: "Dash" };
function renderLog(g) {
  const log = [...(g.log || [])].reverse(); // newest first
  $("#gd-log").replaceChildren(...(log.length ? log.map((e) => h("li", { class: `goal-log-entry k-${e.kind}` },
    h("span", { class: "goal-log-head" },
      h("span", { class: "goal-log-kind" }, LOG_KIND[e.kind] || e.kind),
      h("time", { datetime: e.at, title: dateTimeText(e.at) }, agoText(e.at)),
      e.todo ? h("button", { type: "button", class: "link", onclick: () => openDetail(e.todo).catch(fail) }, `#${e.todo}`) : null),
    h("span", { class: "goal-log-text" }, e.text),
    e.why ? h("span", { class: "goal-log-why" }, e.why) : null))
    : [h("li", { class: "pane-empty" }, gp.detail?.log ? "Nothing yet." : "Loading…")]));
}

// ------------------------------------------------------------------ what you do with a goal
async function goalAction(g, action) {
  const saved = await api("POST", `${goalAPI(g.id)}/${action}`, {}, prefix());
  if (gp.open === g.id) { gp.detail = saved; renderGoal(); }
  toast({ pause: "Goal paused", resume: "Goal resumed" }[action] || "Done");
}

async function stopGoal(g) {
  if (!await ask({ title: "Stop the goal?", body: "The coordinator stops for good and the goal is done. Its tasks stay on the board as they are.", ok: "Stop goal", danger: true })) return;
  const saved = await api("POST", `${goalAPI(g.id)}/stop`, {}, prefix());
  if (gp.open === g.id) { gp.detail = saved; renderGoal(); }
  toast("Goal stopped");
}

async function queueDrafts(g) {
  const drafts = goalTasks(g).filter((t) => t.status === "draft");
  if (!drafts.length) throw new Error("The goal has no drafts to queue.");
  for (const t of drafts) await api("POST", `/api/todos/${t.id}/enqueue`, null, prefix());
  toast(`Queued ${plural(drafts.length, "task")}`);
}

// reviewPlan opens the goal's waiting plan in the Split into tasks sheet,
// to edit and save as drafts or queue.
async function reviewPlan(g) {
  const full = g.plan ? g : await api("GET", goalAPI(g.id), null, prefix());
  if (full.waiting_for !== "plan" || !full.plan?.length) throw new Error("The goal has no plan waiting for you.");
  openGoalPlan(full, gp.project || state.project);
}

function talk(g) {
  if (!desktop.matches) closeGoal(); // a phone's chat page lies under the pane
  openGoalChat(g, gp.project || state.project);
}

// ------------------------------------------------------------------ New goal / Edit goal
// The sheet asks what to get done, a budget and whether its tasks wait for
// you. A new goal opens in the pane, planning; its first plan opens in the
// Split into tasks sheet once the coordinator has one.
export function openGoalSheet({ goal = null, title: t = "", description = "" } = {}) {
  if (!state.project) throw new Error("Select a project first.");
  gp.editing = goal;
  const f = $("#goal-form");
  f.reset();
  $("#goal-sheet-title").textContent = goal ? "Edit goal" : "New goal";
  $("#goal-sheet-hint").hidden = !!goal;
  $("#goal-save").lastChild.textContent = goal ? "Save" : "Start goal";
  f.title.value = goal?.title ?? t;
  f.description.value = goal?.description ?? description;
  f.max_tasks.value = goal?.budget?.max_tasks || "";
  f.weekly_share.value = goal?.budget?.weekly_share || "";
  f.mode.value = goal?.mode || "ask";
  $("#goal-sheet").returnValue = "";
  $("#goal-sheet").showModal();
  (f.description.value ? f.title : f.description).focus();
}

const whole = (v, max, what) => {
  if (v === "") return 0;
  const n = Number(v);
  if (!Number.isInteger(n) || n < 0 || n > max) throw new Error(`${what} must be a whole number from 0 to ${max}.`);
  return n;
};

$("#goal-form").noValidate = true;
$("#goal-sheet").addEventListener("close", attempt(async () => {
  const sheet = $("#goal-sheet");
  if (sheet.returnValue !== "save") return;
  const f = $("#goal-form");
  const goal = gp.editing;
  const reopen = () => sheet.showModal();
  let body;
  try {
    body = {
      title: f.title.value.trim(),
      description: f.description.value.trim(),
      budget: { max_tasks: whole(f.max_tasks.value, 1000, "Max tasks"), weekly_share: whole(f.weekly_share.value, 100, "The weekly share") },
      mode: f.mode.value || "ask",
    };
    if (!body.title && !body.description) throw new Error("Say what to get done.");
  } catch (e) { reopen(); throw e; }
  const p = goal ? (gp.project || state.project) : state.project;
  try {
    if (goal) {
      const saved = await api("PATCH", goalAPI(goal.id), body, p.prefix || "");
      if (gp.open === goal.id) { gp.detail = saved; renderGoal(); }
      toast("Goal saved");
      return;
    }
    const g = await api("POST", "/api/goals", { ...body, project: p.id }, p.prefix || "");
    if (projectKey(state.project) !== projectKey(p)) return;
    gp.project = p;
    if (!state.goals.has(g.id)) state.goals.set(g.id, { ...g, log: undefined });
    if (body.mode === "ask") gp.planFor.add(g.id);
    toast(body.mode === "ask" ? "The coordinator reads the code; its plan opens here for you to check" : "The coordinator reads the code and queues its plan", { tone: "ok", head: "Goal started", icon: "target" });
    await showGoal(g.id);
  } catch (e) { reopen(); throw e; }
}));
$("#goal-new").addEventListener("click", attempt(() => openGoalSheet()));

// ------------------------------------------------------------------ goals on the board
export const goalName = (id) => state.goals.get(id)?.title || "Goal";

// goalHue is goal id's colour, as a hue: the same every time, on every
// computer, and different goals mostly far apart.
export function goalHue(id) {
  let x = 2166136261; // FNV-1a
  for (let i = 0; i < id.length; i++) x = Math.imul(x ^ id.charCodeAt(i), 16777619);
  return (x >>> 0) % 360;
}

const hue = (id) => `--goal-h: ${goalHue(id)}`;

// Show only goal id's tasks (null: every task). A lane the filter leaves
// empty gives way to one with the goal's tasks (on a phone, the one shown).
export function filterGoal(id) {
  state.goalFilter = id || null;
  state.lane = null;
  renderBoard();
  if (state.detail) renderDetail();
}

// goalChip is task t's goal chip (null when it has none): it filters the
// board to that goal, or back to every task when it already is.
export function goalChip(t, cls = "") {
  if (!t.goal_id) return null;
  const name = goalName(t.goal_id), on = state.goalFilter === t.goal_id;
  return h("button", {
    type: "button", class: `goal-chip ${cls}`.trim(), style: hue(t.goal_id), "aria-pressed": String(on),
    title: on ? "Show every task" : `Show only the tasks of goal “${name}”`,
    onpointerdown: (e) => e.stopPropagation(), // not the start of a drag
    onclick: (e) => { e.stopPropagation(); filterGoal(on ? null : t.goal_id); },
  }, h("span", { class: "goal-dot", "aria-hidden": "true" }), h("span", { class: "goal-name" }, name));
}

// The chip at the top of the board while it shows one goal's tasks: the
// goal, how many of its tasks the board has, and a press clears it.
export function renderGoalFilter() {
  const el = $("#goal-filter"), id = state.goalFilter;
  el.hidden = !id;
  if (!id) { el.replaceChildren(); return; }
  const n = [...state.todos.values()].filter((t) => t.goal_id === id).length;
  el.replaceChildren(
    h("span", { class: "goal-filter-label" }, "Showing goal"),
    h("button", { type: "button", class: "goal-chip on", style: hue(id), title: "Show every task", "aria-label": `Goal ${goalName(id)}: show every task`, onclick: () => filterGoal(null) },
      h("span", { class: "goal-dot", "aria-hidden": "true" }), h("span", { class: "goal-name" }, goalName(id)), ico("x")),
    h("span", { class: "goal-filter-n" }, plural(n, "task")));
}