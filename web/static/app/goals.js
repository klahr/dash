import { renderMarkdown } from "../editor.js";
import { $, $$, dateTimeText, fill, h, ib, ico, nameHue, plural } from "./dom.js";
import { chatBeside } from "./viewport.js";
import { api, on } from "./api.js";
import { attempt, busy, copy, fail, toast } from "./dialogs.js";
import { popMenu } from "./popmenu.js";
import { detailPane } from "./detailpane.js";
import { statusLabel, projectFirst, state } from "./state.js";
import { eventFor, latest, projectKey, projectName } from "./events.js";
import { renderBoard, title } from "./branches.js";
import { agoText } from "../time.js";
import { detailBackground, openDetail, renderDetail } from "./detail.js";
import { openGoalPlan } from "./todoform.js";
import { followGoal, openGoalChat, pane as chatPane } from "./chatpane.js";
import { fmtTokens } from "./runstats.js";
import { notifyGoal } from "./notifications.js";
import { showView } from "./nav.js";
import { linkToCard } from "./planning.js";
import { changed, linkTo } from "./routes.js";
import { projectDot, selectProject } from "./projects.js";
import * as store from "./store.js";

// ------------------------------------------------------------------ goals
// A goal is something bigger than one task: a coordinator (Claude) plans its
// tasks, dispatches them and reviews what each one did; approving and
// merging stay the user's (docs/goals.md). The Goals page lists the
// project's goals with their progress, or every project's (gp.scope), the
// active ones or the done ones (gp.show); one opens in a pane beside the
// list (full screen with Back on a phone), like a History commit.
//
// On the board, a goal's tasks stay ordinary cards in the usual lanes. Each
// carries a chip with its goal's name, in a colour of its own; pressing it
// shows only that goal's tasks (state.goalFilter) until the chip at the top
// of the board clears it. The goals are state.goals, by id.
export const GOAL_STATUS = {
  planning: "Planning", working: "Working", waiting_for_you: "Waiting for you",
  paused: "Paused", done: "Done", out_of_budget: "Out of budget", blocked: "Can't run",
};
// The list's order: what needs you first, finished goals last.
const STATUS_ORDER = ["waiting_for_you", "blocked", "planning", "working", "paused", "out_of_budget", "done"];

// blocked reports whether g's coordinator should run but can't (g.blocked
// says why: no runner, a failed clone, …). Its status stays what it was.
const ACTIVE = ["planning", "working", "waiting_for_you"];
export const goalBlocked = (g) => !!g?.blocked && ACTIVE.includes(g.status);
// shownStatus is g's status as the page shows it: "blocked" for a goal
// that can't run.
export const shownStatus = (g) => goalBlocked(g) ? "blocked" : g.status;

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
  editing: null,   // the goal the sheet edits (null: a new one)
  resuming: null,  // the goal out of budget whose Resume asks for its new limits
  descOpen: false, // the open goal's long description shown in full
  scope: store.get("dash.goals.scope", "project"), // "project": the project's goals; "all": every project's
  show: "active",  // "active" or "done"
  all: null,       // with scope "all": every project's goals, by id (GET /api/goals)
};

// Stop and Delete wait while their toast shows Undo (endWithUndo): the
// goals they end, by id → { verb, send }.
const ending = new Map();

// shown is goal g as the page shows it: stopped already while its Stop waits.
const shown = (g) => (g && ending.get(g.id)?.verb === "stop"
  ? { ...g, status: "done", stopped: true, waiting_for: "", plan: null, blocked: "", running: false } : g);

const goalAPI = (id) => `/api/goals/${id}`;
const prefix = () => gp.project?.prefix || "";

// goalsOn reports whether project p can have goals here. A runner's own
// project (one with a /peer/… prefix) can't: its runner doesn't offer goals
// to the hub, so they're used in the runner's own dashboard. New goal… is
// hidden there.
export const goalsOn = (p) => !!p && !p.prefix;

// homeOf is the project goal g is in (one that can have goals).
const homeOf = (g) => (state.projects || []).find((p) => goalsOn(p) && p.id === g.project_id) || null;
// allOffered reports whether All projects has more to show than the project.
const allOffered = () => (state.projects || []).some((p) => goalsOn(p) && projectKey(p) !== projectKey(state.project));
const allShown = () => gp.scope === "all" && allOffered();

// loadGoals reads the project's goals. Events seen while the list was on
// its way are replayed over it. A Dash from before goals has none.
export async function loadGoals() {
  const p = state.project;
  if (allShown()) loadAllGoals().catch(fail);
  if (!p) return resetGoals();
  if (!goalsOn(p)) return resetGoals(); // its runner would refuse: renderGoals says why
  let list = [], seen = [];
  try {
    ({ data: list, seen } = await latest(() => api("GET", `/api/projects/${p.id}/goals`, null, p.prefix || "")));
  } catch { /* a Dash from before goals: its tasks have none */ }
  if (projectKey(state.project) !== projectKey(p)) return; // another project's by now
  const goals = new Map((list || []).map((g) => [g.id, g]));
  for (const { ev, m } of seen) {
    if (ev.type === "goal.updated" && eventFor(p, ev, m)) goals.set(ev.data.id, { ...goals.get(ev.data.id), ...ev.data });
    if (ev.type === "goal.deleted" && eventFor(p, ev, m)) goals.delete(ev.data?.id);
  }
  gp.project = p;
  state.goals = goals;
  gp.loaded = true;
  renderGoals();
  renderBoard();
  if (state.detail) renderDetail();
  if (gp.open && !state.goals.has(gp.open)) closeGoal();
}

// loadAllGoals reads every project's goals, for All projects. Events seen
// while the list was on its way are replayed over it.
async function loadAllGoals() {
  let list = [], seen = [];
  try {
    ({ data: list, seen } = await latest(() => api("GET", "/api/goals")));
  } catch { /* a Dash from before goals */ }
  const all = new Map((list || []).map((g) => [g.id, g]));
  for (const { ev, m } of seen) onAllEvent(ev, m, all);
  gp.all = all;
  renderGoals();
}

// onAllEvent keeps every project's goals current while All projects has
// them: those of this Dash's own projects (m.local), where goals are.
export function onAllEvent(ev, m, all = gp.all) {
  if (!all || !m?.local || !ev.data?.id) return;
  if (ev.type === "goal.updated") all.set(ev.data.id, { ...all.get(ev.data.id), ...ev.data, blocked: ev.data.blocked || "" });
  else if (ev.type === "goal.deleted") all.delete(ev.data.id);
  else return;
  if (all === gp.all && state.view === "goals") renderGoals();
}

export function resetGoals() {
  closeGoal();
  gp.list = [];
  gp.loaded = false;
  gp.project = state.project;
  renderGoals();
}

// waitingGoals are the project's goals that wait for you: a plan to accept,
// a question to answer, drafts to queue, or a coordinator that can't run.
// (Hoisted: renderBoard calls it, and may run before this module has.)
export function waitingGoals() {
  return [...state.goals.values()].filter((g) => g.status === "waiting_for_you" || (g.blocked && ["planning", "working"].includes(g.status)));
}

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

// planWaits is the plan g's coordinator proposed, waiting for you to accept it.
const planWaits = (g) => (g.waiting_for === "plan" ? g.plan || [] : []);

// progress is a .cl-progress bar: "4 of 7 merged", a waiting plan's tasks
// counted too. Nothing while the goal has no tasks: its status says why.
function progress(g) {
  const ts = goalTasks(g), n = ts.length + planWaits(g).length, m = merged(ts);
  if (!n) return null;
  const bar = h("span", { style: `width:${Math.round(100 * m / n)}%` });
  return h("div", { class: `cl-progress goal-progress${m === n ? " complete" : ""}` },
    h("div", { class: "cl-bar", role: "progressbar", "aria-valuemin": "0", "aria-valuemax": String(n), "aria-valuenow": String(m), "aria-label": "Tasks merged" }, bar),
    h("span", { class: "cl-count" }, `${m} of ${n} merged`));
}

// stopped reports whether g is done because you stopped it.
const stopped = (g) => g.status === "done" && (g.stopped || g.note === "Stopped by you.");

// What a waiting goal waits for, as its status.
const WAIT_STATUS = { plan: "Plan to accept", question: "Question for you", drafts: "Drafts to queue" };
export function statusName(g) {
  const s = shownStatus(g);
  if (s === "waiting_for_you") return WAIT_STATUS[g.waiting_for] || GOAL_STATUS[s];
  if (stopped(g)) return "Stopped";
  return GOAL_STATUS[s] || g.status;
}

const statusTag = (g) => h("span", { class: `status goal-status g-${stopped(g) ? "paused" : shownStatus(g)}` },
  g.running ? h("span", { class: "act-spin", "aria-hidden": "true" }) : null,
  statusName(g));

// What a waiting goal waits for, or why it can't run, in a few words.
function waitText(g) {
  if (goalBlocked(g)) return `Can't run: ${g.blocked}`;
  if (g.status !== "waiting_for_you") return "";
  if (g.waiting_for === "plan") return `A plan of ${plural(g.plan?.length || 0, "task")} waits for you to accept it`;
  if (g.waiting_for === "question") return "The coordinator asks you something";
  if (g.waiting_for === "drafts") return "Its drafts wait for you to queue them";
  return g.note || "";
}

// The list: the project's goals or every project's, the active ones or the
// done ones, what needs you first. The goal open stays listed while it is
// open, even once it is done (or active again). With no goals, the page is
// a title and New goal….
const isDone = (g) => g.status === "done";
const byNeed = (a, b) => (STATUS_ORDER.indexOf(shownStatus(a)) - STATUS_ORDER.indexOf(shownStatus(b))) || (b.updated_at || "").localeCompare(a.updated_at || "");

function listed(all) {
  const goals = new Map(all ? gp.all || [] : []);
  for (const g of state.goals.values()) goals.set(g.id, { ...goals.get(g.id), ...g });
  return [...goals.values()].filter((g) => ending.get(g.id)?.verb !== "delete").map(shown);
}

export function renderGoals() {
  const list = $("#goal-list");
  if (!list) return;
  const on = goalsOn(state.project), all = allShown();
  const goals = listed(all);
  const done = goals.filter(isDone), active = goals.filter((g) => !isDone(g));
  if (!done.length) gp.show = "active";
  const rows = gp.show === "done" ? done : active;
  const open = gp.open && goals.find((g) => g.id === gp.open);
  if (open && !rows.includes(open)) rows.push(open);
  rows.sort(byNeed);

  const scope = $("#goal-scope");
  scope.hidden = !allOffered();
  $("#goal-scope-project").textContent = state.project?.name || "Project";
  const show = $("#goal-show");
  show.hidden = !done.length;
  $("#goal-n-active").textContent = active.length || "";
  $("#goal-n-done").textContent = done.length || "";
  const current = (b, on) => (on ? b.setAttribute("aria-current", "page") : b.removeAttribute("aria-current"));
  for (const b of $$("button", scope)) current(b, b.dataset.scope === (all ? "all" : "project"));
  for (const b of $$("button", show)) current(b, b.dataset.show === gp.show);

  const empty = !rows.length;
  $("#goal-empty").hidden = !empty;
  $("#goal-empty-title").textContent = !all && !state.project ? "No project"
    : !all && !on ? "No goals on a runner's own project"
      : goals.length ? "No active goals" : "No goals yet";
  $("#goal-empty-new").hidden = !on;
  $("#goal-new").hidden = !on || empty;

  const focused = document.activeElement?.closest?.("#goal-list li")?.dataset.id;
  list.replaceChildren(...rows.map((g) => {
    const home = all ? homeOf(g) : null;
    return h("li", { class: `goal${g.id === gp.open ? " selected" : ""}`, "data-id": g.id },
      h("button", { type: "button", class: "goal-open", "aria-controls": "goal-detail", "aria-current": g.id === gp.open ? "true" : null, onclick: () => openListed(g.id).catch(fail) },
        h("span", { class: "goal-row" }, h("span", { class: "goal-title" }, g.title), statusTag(g)),
        home ? h("span", { class: "goal-where" }, projectDot(home), home.name) : null,
        progress(g),
        goalBlocked(g) ? h("span", { class: "goal-wait" }, g.blocked) : null));
  }));
  if (focused) $(`#goal-list li[data-id="${focused}"] .goal-open`)?.focus({ preventScroll: true });
  renderGoalBadges();
}

// openListed opens a goal of the list: one of another project (All
// projects) opens with its project selected, as its toast's Go does.
async function openListed(id) {
  const g = (gp.all && gp.all.get(id)) || state.goals.get(id);
  const home = g && homeOf(g);
  if (home && projectKey(home) !== projectKey(state.project)) await selectProject(home, { restore: false });
  await openGoal(id);
}

// The page's tabs: this project or all projects, active goals or done ones.
$("#goal-scope").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-scope]");
  if (!b) return;
  showScope(b.dataset.scope);
});
$("#goal-show").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-show]");
  if (!b || b.dataset.show === gp.show) return;
  gp.show = b.dataset.show;
  if (gp.open && (isDone(shown(state.goals.get(gp.open) || {})) !== (gp.show === "done"))) closeGoal();
  renderGoals();
});

// showScope lists the project's goals ("project") or every project's ("all").
export function showScope(scope) {
  gp.scope = scope === "all" ? "all" : "project";
  store.set("dash.goals.scope", gp.scope);
  renderGoals();
  if (gp.scope === "all") loadAllGoals().catch(fail);
}

// showAllGoals goes to the Goals page with every project's goals (the
// Board badge's "goals").
export function showAllGoals() {
  gp.show = "active";
  gp.scope = "all";
  store.set("dash.goals.scope", "all");
  showView("goals"); // loads them (nav.js)
}

// The Goals buttons' badges count the goals that wait for you; the Board's
// badge and the tab title count them too (renderBoard).
function renderGoalBadges() {
  const n = waitingGoals().length;
  for (const b of [$("#goal-badge"), $("#menu-goal-badge")]) {
    if (!b) continue;
    b.hidden = !n;
    b.textContent = n || "";
    b.title = n ? `${plural(n, "goal")} need${n === 1 ? "s" : ""} you` : "";
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
  // blocked is left out once cleared: it mustn't stay from prev.
  const next = { ...prev, ...g, blocked: g.blocked || "", tasks: prev?.tasks || [], weekly_used: prev?.weekly_used };
  state.goals.set(g.id, next);
  announce(prev, next);
  renderGoals();
  renderBoard(); // its badge, and its tasks' chips
  if (state.detail && prev?.title !== g.title) renderDetail(); // the open task's chip
  if (gp.open === g.id) {
    clearTimeout(detailTimer);
    detailTimer = setTimeout(() => loadGoal(g.id).catch(() => {}), 300);
  }
}

// onGoalDeleted drops goal id, deleted here or in another window: its pane
// closes, and its tasks (still on the board) lose their chip by their own
// todo.updated.
export function onGoalDeleted(id) {
  if (!id || !state.goals.has(id)) return;
  state.goals.delete(id);
  if (gp.open === id) closeGoal();
  if (state.goalFilter === id) filterGoal(null);
  renderGoals();
  renderBoard();
  if (state.detail) renderDetail();
}

// announce tells you about a goal that now waits for you, can't run, is
// done or out of budget.
function announce(prev, g) {
  if (!prev || (shownStatus(prev) === shownStatus(g) && prev.waiting_for === g.waiting_for)) return;
  const go = { label: "Open", run: () => showGoal(g.id) };
  if (goalBlocked(g)) {
    toast(g.title, { tone: "error", head: "Goal can't run", where: projectName(state.project), icon: "target", action: go });
    notifyGoal("Goal can't run", g, g.blocked);
  } else if (g.status === "waiting_for_you") {
    toast(g.title, { tone: "review", head: "Goal waits for you", where: projectName(state.project), icon: "target", action: go });
    notifyGoal("Goal waits for you", g, waitText(g));
  } else if (g.status === "done") {
    toast(g.title, { tone: "ok", head: "Goal done", where: projectName(state.project), icon: "target", action: go });
    notifyGoal("Goal done", g, g.note);
  } else if (g.status === "out_of_budget") {
    toast(g.title, { tone: "error", head: "Goal out of budget", where: projectName(state.project), icon: "target", action: go });
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
// The chat lies beside it, never over it (style.css): on a desktop the list
// narrows for both; on a tablet the pane keeps left of the chat; on a phone
// the chat is a page that takes the pane's place until its Goal button
// leads back (chatpane.js). The chat is never made inert under the pane.
const chatPage = () => !chatBeside.matches && chatPane.open;
const goalPane = detailPane({
  pane: () => $("#goal-detail"),
  closeBtn: () => $("#gd-close"),
  // detail.js imports this module: read it when called.
  background: () => detailBackground().filter((el) => el.id !== "chat-pane"),
  modal: () => !chatPage(),
  isOpen: () => !!gp.open,
  rowFor: (id) => $(`#goal-list li[data-id="${id}"] .goal-open`),
  onHidden: () => $(".app").classList.remove("goal-open"),
  onViewport: () => renderGoal(),
});

// showGoal goes to the Goals page with goal id open (a toast's Open, a notification).
export async function showGoal(id) {
  showView("goals");
  await openGoal(id);
}

export async function openGoal(id) {
  const pane = $("#goal-detail");
  if (gp.open === id && !pane.hidden) return;
  gp.open = id;
  gp.detail = state.goals.get(id) || null;
  gp.error = "";
  gp.resuming = null;
  gp.descOpen = false;
  goalPane.open();
  $(".app").classList.add("goal-open"); // the list narrows (style.css)
  markSelected();
  renderGoal();
  $("#gd-body").scrollTop = 0;
  followGoal(gp.detail || { id }, gp.project || state.project);
  changed("push");
  await loadGoal(id);
}
// The chat opened or closed, or changed tabs: on a phone the pane is modal
// only while the chat page isn't over it.
document.addEventListener("chatpane", () => { if (gp.open) goalPane.setModal(); });

async function loadGoal(id) {
  try {
    const g = await api("GET", goalAPI(id), null, prefix());
    if (gp.open !== id) return;
    gp.detail = g;
    gp.error = "";
    state.goals.set(id, { ...g, log: undefined });
    renderGoals();
    followGoal(g, gp.project || state.project); // its title
  } catch (e) {
    if (gp.open !== id) return;
    gp.error = e.message;
  }
  renderGoal();
}

export function closeGoal() {
  if (!state.goalOpen) return;
  clearTimeout(detailTimer);
  const id = gp.open;
  gp.open = gp.detail = null;
  changed("close");
  markSelected();
  goalPane.close(id);
  followGoal(null);
}
$("#gd-close").addEventListener("click", closeGoal);

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
    openListed(next).catch(fail);
    if (onRow) $(`#goal-list li[data-id="${next}"] .goal-open`)?.focus({ preventScroll: true });
  }
}

// renderGoal draws the open goal: its head (status once, what it has spent,
// the action that fits where it stands and the rest in a menu), then what
// it is for, the coordinator's word if it has one, its tasks, its budget
// (only with a limit) and its log.
function renderGoal() {
  const g = shown(gp.detail);
  if (!g || $("#goal-detail").hidden) return;
  $("#gd-title").textContent = g.title;
  const spent = spend(g);
  fill($("#gd-meta"), statusTag(g),
    h("span", {}, g.mode === "queue" ? "Starts tasks on its own" : "Asks before starting tasks"),
    g.created_at ? h("span", { title: dateTimeText(g.created_at) }, `started ${agoText(g.created_at)}`) : null,
    spent && !hasLimit(g) ? h("span", { class: "goal-spend" }, spent) : null);
  renderGoalActions(g);
  renderResume(g);
  $("#gd-error").hidden = !gp.error;
  $("#gd-error").textContent = gp.error;
  renderDesc(g);
  renderNote(g);
  renderPlan(g);
  renderBudget(g);
  renderLog(g);
}

// The actions: the one that fits where the goal stands is the main button
// (Accept plan, Answer, Queue drafts, Resume, Try again), with at most one
// beside it; the rest are in ⋯. Pause holds the goal, Stop ends it (both in
// ⋯ while it runs), and only an ended goal can be deleted. Approve and
// Merge are never here: a goal's tasks are approved and merged on the
// board, by you.
function renderGoalActions(g) {
  const btn = ([icon, label, run], primary) => ib({ type: "button", class: `btn small${primary ? " primary" : ""}`, onclick: attempt((e) => busy(e.currentTarget, run)) }, icon, label);
  const active = ACTIVE.includes(g.status), waits = g.status === "waiting_for_you";
  const talkTo = ["comment", "Talk to coordinator", () => talk(g)];
  const pause = ["pause", "Pause", () => goalAction(g, "pause")];
  let main = null, beside = null, primary = true;
  if (goalBlocked(g)) main = ["redo", "Try again", () => goalAction(g, "resume")];
  else if (waits && g.waiting_for === "plan" && g.plan?.length) {
    main = ["check", "Accept plan", () => acceptPlan(g)];
    beside = ["edit", "Edit plan…", () => reviewPlan(g)];
  } else if (waits && g.waiting_for === "question") main = ["comment", "Answer", () => talk(g)];
  else if (waits && g.waiting_for === "drafts") main = ["queue", "Queue drafts", () => queueDrafts(g)];
  else if (g.status === "paused") main = ["play", "Resume", () => goalAction(g, "resume")];
  else if (g.status === "out_of_budget") main = ["play", "Resume…", () => openResume(g)];
  else {
    main = talkTo;
    primary = false;
    beside = active ? pause : null;
  }
  const used = [main, beside].map((x) => x?.[1]);
  const more = [
    main === talkTo || main[1] === "Answer" ? null : talkTo,
    active && !used.includes("Pause") ? pause : null,
    g.status !== "done" ? ["edit", "Edit goal…", () => openGoalSheet({ goal: g })] : null,
    prefix() ? null : ["plan", "Link to card…", () => linkToCard("goal", g.id)],
  ].filter(Boolean);
  const end = [
    g.status !== "done" ? ["stop", "Stop goal", () => endWithUndo(g, "stop"), { danger: true }] : null,
    g.status === "done" ? ["trash", "Delete goal", () => endWithUndo(g, "delete"), { danger: true }] : null,
  ].filter(Boolean);
  fill($("#gd-actions"), btn(main, primary), beside ? btn(beside) : null,
    ib({ type: "button", class: "icon-btn goal-more-btn", id: "gd-more", "aria-label": "More actions", title: "More", "aria-haspopup": "menu", "aria-expanded": "false",
      onclick: (e) => popMenu(e.currentTarget, [more, end]) }, "more"));
}

$("#gd-copy").addEventListener("click", () => gp.open && copy(linkTo({ view: "goals", goal: gp.open }, state.project), "Link copied"));

// ---- Resume a goal out of budget: its limits, to raise, and Resume, in one step.
const hasLimit = (g) => !!(g.budget?.max_tasks || g.budget?.weekly_share);

function openResume(g) {
  if (!hasLimit(g)) return goalAction(g, "resume");
  gp.resuming = g.id;
  renderGoal();
  const input = $("#gd-resume input");
  input?.focus();
  input?.select();
}

function closeResume() {
  gp.resuming = null;
  renderGoal();
  $("#gd-actions .btn")?.focus();
}

function renderResume(g) {
  const f = $("#gd-resume");
  const on = gp.resuming === g.id && g.status === "out_of_budget" && hasLimit(g);
  f.hidden = !on;
  if (!on) {
    f.replaceChildren();
    delete f.dataset.goal;
    return;
  }
  if (f.dataset.goal === g.id) return; // keep what is typed
  f.dataset.goal = g.id;
  const b = g.budget;
  const field = (name, label, value, max) => h("label", { class: "field inline" }, h("span", {}, label),
    h("input", { type: "number", name, min: "1", max, step: "1", inputmode: "numeric", value: String(value), required: true }));
  const go = ib({ type: "submit", class: "btn small primary" }, "play", "Resume");
  fill(f,
    b.max_tasks ? field("max_tasks", "Max tasks", b.max_tasks, "1000") : null,
    b.weekly_share ? field("weekly_share", "Weekly share %", b.weekly_share, "100") : null,
    go,
    h("button", { type: "button", class: "btn small", onclick: closeResume }, "Cancel"));
  f.onsubmit = (e) => {
    e.preventDefault();
    attempt(() => busy(go, async () => {
      const budget = {
        max_tasks: f.max_tasks ? whole(f.max_tasks.value, 1000, "Max tasks") : b.max_tasks || 0,
        weekly_share: f.weekly_share ? whole(f.weekly_share.value, 100, "The weekly share") : b.weekly_share || 0,
      };
      await api("PATCH", goalAPI(g.id), { budget }, prefix());
      await goalAction(g, "resume");
      gp.resuming = null;
      renderGoal();
    }))();
  };
  f.onkeydown = (e) => {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopPropagation();
    closeResume();
  };
}

// ---- What the goal is for: first, rendered; a long one shows its start until Show all.
const LONG_DESC = 700;
function renderDesc(g) {
  const wrap = $("#gd-desc-wrap"), more = $("#gd-desc-more"), d = (g.description || "").trim();
  wrap.hidden = !d || d === g.title;
  $("#gd-desc").innerHTML = renderMarkdown(d);
  const long = d.length > LONG_DESC || d.split("\n").length > 12;
  more.hidden = !long;
  wrap.classList.toggle("clamped", long && !gp.descOpen);
  more.textContent = gp.descOpen ? "Show less" : "Show all";
  more.setAttribute("aria-expanded", String(!!gp.descOpen));
}
$("#gd-desc-more").addEventListener("click", () => {
  gp.descOpen = !gp.descOpen;
  if (gp.detail) renderDesc(shown(gp.detail));
});

// The note is what the status alone doesn't say: the coordinator's question
// or its summary, or why the coordinator can't run. Styled like a chat
// reply, from whoever speaks.
function noteOf(g) {
  if (goalBlocked(g)) return { from: "Dash", text: g.blocked };
  if (g.status === "waiting_for_you" && g.waiting_for === "question" && g.note) return { from: "Coordinator", text: g.note };
  if (g.status === "done" && !stopped(g) && g.note) return { from: "Coordinator", text: g.note };
  return null;
}
function renderNote(g) {
  const el = $("#gd-note"), n = noteOf(g);
  el.hidden = !n;
  if (!n) return;
  el.className = `goal-note msg assistant g-${shownStatus(g)}`;
  fill(el, h("div", { class: "msg-from" }, n.from), h("div", { class: "reply", innerHTML: renderMarkdown(n.text) }));
}

// Tasks: the plan's tasks in dependency order, each with its state and
// linked to its task; a plan waiting to be accepted follows as Proposed,
// each opening to its instructions.
function renderPlan(g) {
  const ts = goalTasks(g);
  const num = new Map(ts.map((t, i) => [t.id, i + 1]));
  const rows = ts.map((t, i) => {
    const after = (t.depends_on || []).map((d) => num.get(d)).filter(Boolean).sort((a, b) => a - b);
    return h("li", { class: `goal-task s-${t.status}` },
      h("span", { class: "split-num", "aria-hidden": "true" }, String(i + 1)),
      h("button", { type: "button", class: "goal-task-open", onclick: () => openDetail(t.id).catch(fail) },
        h("span", { class: "goal-task-title" }, title({ title: t.title, prompt: t.prompt || t.title || t.id })),
        h("span", { class: "goal-task-sub" },
          h("span", { class: "status" }, t.paused ? "Paused" : statusLabel(t)),
          after.length ? h("span", {}, `after ${after.join(", ")}`) : null)));
  });
  const proposed = planWaits(g).map((it, i) => h("li", { class: "goal-task proposed s-draft" },
    h("span", { class: "split-num", "aria-hidden": "true" }, String(ts.length + i + 1)),
    h("details", { class: "goal-task-open goal-proposal" },
      h("summary", {},
        h("span", { class: "goal-task-title" }, it.title || it.prompt.split("\n")[0]),
        h("span", { class: "goal-task-sub" }, h("span", { class: "status" }, "Proposed"),
          it.after?.length ? h("span", {}, `after ${it.after.map((a) => ts.length + a + 1).join(", ")}`) : null)),
      h("div", { class: "goal-proposal-prompt md", innerHTML: renderMarkdown(it.prompt || "") }))));
  $("#gd-plan-wrap").hidden = !rows.length && !proposed.length;
  $("#gd-plan").replaceChildren(...rows, ...proposed);
}

// spend is what the goal's tasks took, "48k tokens · $0.42", once they took any.
function spend(g) {
  const runs = goalTasks(g).flatMap((t) => t.run_stats || []);
  if (!runs.length) return "";
  const tokens = runs.reduce((n, r) => n + (r.input_tokens || 0) + (r.output_tokens || 0) + (r.cache_read_tokens || 0) + (r.cache_creation_tokens || 0), 0);
  const cost = runs.reduce((n, r) => n + (r.cost_usd || 0), 0);
  return `${fmtTokens(tokens)} tokens · $${cost.toFixed(2)}`;
}

// Budget, only with a limit: each limit as a meter of what is used of it,
// and what the tasks spent. Without a limit, the spend is in the head.
function renderBudget(g) {
  const wrap = $("#gd-budget-wrap");
  wrap.hidden = !hasLimit(g);
  if (wrap.hidden) return;
  const b = g.budget, made = goalTasks(g).length, spent = spend(g);
  const meter = (label, used, max, text) => h("div", { class: "goal-meter" },
    h("span", { class: "goal-meter-label" }, label),
    h("div", { class: `cl-progress${used >= max ? " over" : ""}` },
      h("div", { class: "cl-bar", role: "progressbar", "aria-label": label, "aria-valuemin": "0", "aria-valuemax": String(max), "aria-valuenow": String(Math.min(used, max)) },
        h("span", { style: `width:${Math.min(100, Math.round(100 * used / max))}%` })),
      h("span", { class: "cl-count" }, text)));
  const row = (label, text) => h("div", { class: "goal-meter" }, h("span", { class: "goal-meter-label" }, label), h("span", { class: "cl-count" }, text));
  const weekly = !b.weekly_share ? null
    : g.weekly_used != null ? meter("Weekly share", g.weekly_used, b.weekly_share, `${g.weekly_used.toFixed(1)}% of ${b.weekly_share}%`)
      : row("Weekly share", `${b.weekly_share}%`);
  fill($("#gd-budget"),
    b.max_tasks ? meter("Tasks", made, b.max_tasks, `${made} of ${b.max_tasks}`) : null,
    weekly,
    spent ? row("Spent", spent) : null);
}

// ---- The log, newest first, read as a story: who speaks (the coordinator,
// you, or Dash), markdown rendered, a task by its title (a link to it), never
// its id. Task events that woke the coordinator are plain lines, and a run of
// them, with the routine steps that follow them, folds into one.
const finishedPrefix = "Finished the goal: ";
const isCreated = (e) => e.kind === "user" && /^Goal created\b/.test(e.text || "");
function speaker(e) {
  if (e.kind === "wake") return "";
  if (e.kind === "user") return isCreated(e) ? "Dash" : "You";
  if (e.kind === "status") return "Dash";
  return "Coordinator";
}
// tone: a goal finished is good news; a failure, a goal that can't run or
// out of budget is bad; the rest is neither.
function tone(e) {
  const text = e.text || "";
  if (e.kind === "status") return text.startsWith(finishedPrefix) ? "ok" : /^Goal created\b/.test(text) ? "" : "bad";
  if (e.kind === "wake" && /\bfailed\b/.test(text)) return "bad";
  return "";
}
const routine = (e) => e.kind === "wake" || (e.kind === "action" && !e.why && /^(Created|Queued|Edited|Re-queued) task\b/.test(e.text || ""));

const TASK_MARK = "";
const escRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
// logHTML is entry e's text as HTML: markdown, with the task it names as a
// link that shows its title.
function logHTML(e, g) {
  let text = e.text || "";
  if (!e.todo) return renderMarkdown(text);
  let quoted = "";
  const re = new RegExp(`\\b[Tt]ask #?${escRe(e.todo)}(?: \\("((?:[^"\\\\]|\\\\.)*)"\\))?`);
  if (!re.test(text)) return renderMarkdown(text);
  text = text.replace(re, (_, q) => { quoted = (q || "").replace(/\\(.)/g, "$1"); return TASK_MARK; });
  const t = goalTasks(g).find((x) => x.id === e.todo) || state.todos.get(e.todo);
  const name = t ? title({ title: t.title, prompt: t.prompt || t.title || "" }) : quoted;
  // "Queued task X: <its title>": the title is the link already.
  const tail = text.match(/: ([^\n]*)$/);
  if (tail && name && tail[1].replace(/…$/, "") && name.startsWith(tail[1].replace(/…$/, ""))) text = text.slice(0, tail.index);
  const link = h("button", { type: "button", class: "link inline goal-log-task", "data-todo": e.todo }, name || "a task").outerHTML;
  return renderMarkdown(text).replace(TASK_MARK, link);
}

function logEntry(e, g, tag = "li") {
  const who = speaker(e), t = tone(e);
  return h(tag, { class: `goal-log-entry k-${e.kind}${who ? "" : " event"}${t ? ` tone-${t}` : ""}` },
    h("span", { class: "goal-log-head" },
      who ? h("span", { class: "goal-log-kind" }, who) : null,
      h("time", { datetime: e.at, title: dateTimeText(e.at) }, agoText(e.at))),
    h("div", { class: "goal-log-text md", innerHTML: logHTML(e, g) }),
    e.why ? h("div", { class: "goal-log-why md", innerHTML: renderMarkdown(e.why) }) : null);
}

function renderLog(g) {
  const log = [...(g.log || [])].reverse(); // newest first
  const items = [];
  for (let i = 0; i < log.length;) {
    let j = i;
    while (j < log.length && routine(log[j])) j++;
    if (j - i >= 3) {
      const run = log.slice(i, j);
      items.push(h("li", { class: "goal-log-run" },
        h("details", {},
          h("summary", {}, logEntry(run[0], g, "div"), h("span", { class: "goal-log-more" }, `${run.length - 1} more`)),
          h("ol", { class: "goal-log" }, ...run.slice(1).map((e) => logEntry(e, g))))));
      i = j;
    } else {
      items.push(logEntry(log[i], g));
      i++;
    }
  }
  $("#gd-log").replaceChildren(...(items.length ? items : [h("li", { class: "pane-empty" }, gp.detail?.log ? "Nothing yet." : "Loading…")]));
}
$("#gd-log").addEventListener("click", (e) => {
  const b = e.target.closest(".goal-log-task");
  if (!b) return;
  e.preventDefault(); // not the fold's toggle
  openDetail(b.dataset.todo).catch(fail);
});

// ------------------------------------------------------------------ what you do with a goal
async function goalAction(g, action) {
  const retry = action === "resume" && goalBlocked(g);
  const saved = await api("POST", `${goalAPI(g.id)}/${action}`, {}, prefix());
  if (gp.open === g.id) { gp.detail = saved; renderGoal(); }
  toast(retry ? "Trying again" : { pause: "Goal paused", resume: "Goal resumed" }[action] || "Done");
}

// endWithUndo stops or deletes goal g once its toast has gone without Undo
// (or at once when the page is left). Meanwhile the goal shows stopped, or
// is gone from the list.
export const GOAL_UNDO_MS = 6000;
function endWithUndo(g, verb) {
  if (ending.has(g.id)) return;
  const p = prefix(), del = verb === "delete";
  const path = del ? goalAPI(g.id) : `${goalAPI(g.id)}/stop`, method = del ? "DELETE" : "POST";
  const m = { verb, send: () => fetch(on(p, path), { method, keepalive: true }) };
  ending.set(g.id, m);
  if (del && gp.open === g.id) closeGoal();
  renderGoals();
  renderGoal();
  toast(g.title, {
    head: del ? "Goal deleted" : "Goal stopped", icon: del ? "trash" : "stop", ms: GOAL_UNDO_MS, action: { label: "Undo", run: () => {} },
    onClose: attempt(async (undone) => {
      if (ending.get(g.id) !== m) return; // sent already: the page was left
      try {
        if (undone) return;
        if (del) {
          await api(method, path, null, p);
          onGoalDeleted(g.id);
        } else {
          const saved = await api(method, path, {}, p);
          state.goals.set(g.id, { ...state.goals.get(g.id), ...saved, log: undefined });
          if (gp.open === g.id) gp.detail = saved;
        }
      } finally {
        ending.delete(g.id);
        renderGoals();
        renderGoal();
      }
    }),
  });
}
addEventListener("pagehide", () => {
  for (const m of ending.values()) m.send();
  ending.clear();
});

async function queueDrafts(g) {
  const drafts = goalTasks(g).filter((t) => t.status === "draft");
  if (!drafts.length) throw new Error("The goal has no drafts to queue.");
  for (const t of drafts) await api("POST", `/api/todos/${t.id}/enqueue`, null, prefix());
  toast(`Queued ${plural(drafts.length, "task")}`);
}

// acceptPlan accepts the goal's waiting plan as it stands: its tasks are made and queued.
async function acceptPlan(g) {
  const res = await api("POST", `${goalAPI(g.id)}/plan/accept`, { queue: true }, prefix());
  if (gp.open === g.id) { gp.detail = res; renderGoal(); }
  toast(`Queued ${plural(res.created?.length || g.plan.length, "task")} for “${g.title}”`, { tone: "ok", icon: "target" });
}

// reviewPlan opens the goal's waiting plan in the plan sheet, to edit
// before accepting it.
async function reviewPlan(g) {
  const full = g.plan ? g : await api("GET", goalAPI(g.id), null, prefix());
  if (full.waiting_for !== "plan" || !full.plan?.length) throw new Error("The goal has no plan waiting for you.");
  openGoalPlan(full, gp.project || state.project);
}

function talk(g) {
  openGoalChat(g, gp.project || state.project);
}

// ------------------------------------------------------------------ New goal / Edit goal
// New goal asks what to get done and whether its tasks wait for you: the
// goal is named from what to get done, with no limit. Edit goal adds its
// name and the most tasks it may make (a weekly share set from the API is
// kept). A new goal opens in the pane, planning; its plan shows there to
// accept once the coordinator has one.
export function openGoalSheet({ goal = null, title: t = "", description = "" } = {}) {
  if (!state.project) throw new Error(projectFirst());
  if (!goal && !goalsOn(state.project)) throw new Error("No goals on a runner's own project.");
  gp.editing = goal;
  const f = $("#goal-form");
  f.reset();
  $("#goal-sheet-title").textContent = goal ? "Edit goal" : "New goal";
  $("#goal-save").lastChild.textContent = goal ? "Save" : "Start goal";
  $("#goal-title-field").hidden = !goal;
  $("#goal-max-field").hidden = !goal;
  f.title.value = goal?.title ?? t;
  f.description.value = goal?.description ?? (description || t);
  f.max_tasks.value = goal?.budget?.max_tasks || "";
  f.mode.value = goal?.mode || "ask";
  $("#goal-sheet").returnValue = "";
  $("#goal-sheet").showModal();
  f.description.focus();
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
      title: f.title.value.trim(), // a new goal's: carried over from the task form, if any
      description: f.description.value.trim(),
      mode: f.mode.value || "ask",
    };
    if (goal) body.budget = { max_tasks: whole(f.max_tasks.value, 1000, "Max tasks"), weekly_share: goal.budget?.weekly_share || 0 };
    if (!body.description && !(goal && body.title)) throw new Error("Say what to get done.");
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
    await showGoal(g.id);
  } catch (e) { reopen(); throw e; }
}));
$("#goal-new").addEventListener("click", attempt(() => openGoalSheet()));
$("#goal-empty-new").addEventListener("click", attempt(() => openGoalSheet()));

// ------------------------------------------------------------------ goals on the board
export const goalName = (id) => state.goals.get(id)?.title || "Goal";

// goalHue is goal id's colour, as a hue: the same every time, on every
// computer, and different goals mostly far apart.
export const goalHue = nameHue;

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
    onpointerdown: (e) => e.stopPropagation(), // not the start of a drag
    onclick: (e) => { e.stopPropagation(); filterGoal(on ? null : t.goal_id); },
  }, h("span", { class: "goal-dot", "aria-hidden": "true" }), h("span", { class: "goal-name" }, name));
}

// The chip in the board's filters while it shows one goal's tasks: the
// goal, and a press clears it.
export function renderGoalFilter() {
  const el = $("#goal-filter"), id = state.goalFilter;
  el.hidden = !id;
  if (!id) { el.replaceChildren(); return; }
  el.replaceChildren(
    h("button", { type: "button", class: "goal-chip on", style: hue(id), "aria-label": `Goal ${goalName(id)}: show every task`, onclick: () => filterGoal(null) },
      h("span", { class: "goal-dot", "aria-hidden": "true" }), h("span", { class: "goal-name" }, goalName(id)), ico("x")));
}