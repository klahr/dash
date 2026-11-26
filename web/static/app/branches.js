import { $, $$, avatar, dateTimeText, h, ib, ico, plural } from "./dom.js";
import { ago, agoText, clockText } from "../time.js";
import * as store from "./store.js";
import { api, here, on } from "./api.js";
import { EASE, art, ask, attempt, busy, reduceMotion, toast } from "./dialogs.js";
import { clip } from "./attachments.js";
import { DAY, LANES, state } from "./state.js";
import { baseBranch, projectKey } from "./events.js";
import { openNewProject, renderProjectHeader, renderProjectList } from "./projects.js";
import { landsOnGitHub, loadUnpushed, pushBar } from "./board.js";
import { activityNodes } from "./activity.js";
import { canStartNow, closeDetail, goesAfter, openDetail, pacing, renderActions, renderDetail, startNow } from "./detail.js";
import { canPreview, previewOf, previewTag, previewTodo } from "./preview.js";
import { reviewTodo } from "./drawmode.js";
import { renderRunnerStatus, runnerName, welcomeScreen } from "./runners.js";
import { updateNeedsYou } from "./notifications.js";
import { loadRemote } from "./origin.js";
import { loadGraph } from "./history.js";
import { loadFiles } from "./files.js";
import { PRIORITY_LEVELS, drag, priorityLevel } from "./dnd.js";
import { hoverCard } from "./hovercard.js";
import { tokenTag } from "./runstats.js";
import { keys, laneHidden, todoKey } from "./vim.js";
import { desktop } from "./viewport.js";
import { chatPage } from "./nav.js";
import { goalChip, renderGoalFilter, showAllGoals, waitingGoals } from "./goals.js";
import { creator, mineOn, multiMember, myID, people, setMine } from "./people.js";
import { isQuestion } from "./chatparts.js";
import { boardFilter, onBoard, pickBox, picked, renderCombineBar, renderTagFilter, setSearch, startPicking, tagRow } from "./tags.js";
import { plainText } from "../markdown.js";
import { servesProject } from "../groups.js";

// ------------------------------------------------------------------ branches
// The open switcher's GET /branches answer, for the project it was opened on.
let branchSheet = null;

async function openBranches() {
  const p = state.project;
  if (!p) return;
  branchSheet = { p, res: null, fetching: !!p.remote_url };
  $("#branch-filter").value = "";
  renderBranches();
  $("#branch-sheet").showModal();
  $("#branch-filter").focus();
  await loadBranches(false); // the refs as they are, at once
  if (p.remote_url) await loadBranches(true); // then with origin's latest
}

async function loadBranches(fetch) {
  const s = branchSheet;
  if (!s) return;
  try {
    const res = await api("GET", `/api/projects/${s.p.id}/branches${fetch ? "?fetch=1" : ""}`, null, s.p.prefix || "");
    if (branchSheet !== s) return;
    s.res = res;
  } finally {
    if (branchSheet === s && fetch) s.fetching = false;
    if (branchSheet === s) renderBranches();
  }
}

function renderBranches() {
  const s = branchSheet;
  if (!s) return;
  const p = state.project?.id === s.p.id ? state.project : s.p, res = s.res;
  const cur = res?.current || baseBranch(p), def = res?.default || p.default_branch;
  const off = cur !== def;
  $("#branch-intro").replaceChildren("New tasks start from ", h("code", {}, cur), " and land on it",
    ...(off ? [" instead of the default branch ", h("code", {}, def), ". Tasks already started keep the branch they started from."] : [", the default branch."]));
  $("#branch-loading").hidden = !s.fetching;
  $("#branch-loading").textContent = res?.fetch_error ? `Couldn't fetch from origin: ${res.fetch_error}` : "Fetching branches from origin…";
  if (res?.fetch_error) $("#branch-loading").hidden = false;

  // The current branch's standing against the default one, and its pull request.
  const box = $("#branch-current");
  box.hidden = !off;
  if (off) {
    const parts = [];
    if (res) parts.push(res.ahead ? `${plural(res.ahead, "commit")} ahead of ${def}` : `nothing ${def} doesn't have yet`);
    if (res?.behind) parts.push(`${res.behind} behind`);
    let action;
    if (res?.pr) {
      action = h("a", { class: "btn primary", href: res.pr.url, target: "_blank", rel: "noopener" }, ico("external"), `Pull request #${res.pr.number}`);
    } else {
      const why = !p.remote_url ? "The project has no GitHub repository" : res && !res.ahead ? `${cur} has nothing to merge into ${def} yet` : "";
      action = ib({ type: "button", class: "btn primary", id: "branch-pr", disabled: !!why || !res, title: why || `Push ${cur} and open a pull request into ${def}`,
        onclick: attempt((e) => createBranchPR(e.currentTarget)) }, "pr", `Create pull request into ${def}`);
    }
    box.replaceChildren(
      h("div", { class: "branch-current-head" }, ico("branch"), h("code", {}, cur), h("span", { class: "hint" }, parts.join(", "))),
      h("div", { class: "branch-current-actions" }, action,
        ib({ type: "button", class: "btn", onclick: attempt(() => switchBranch(def)) }, "undo", `Back to ${def}`)));
  }

  // Every branch, filtered by what's typed; a name that isn't one offers to create it.
  const q = $("#branch-filter").value.trim();
  const list = res?.branches || [{ name: cur, local: true }];
  const shown = list.filter((b) => !q || b.name.toLowerCase().includes(q.toLowerCase()));
  const items = shown.map((b) => {
    const tags = [b.name === def && "default", b.name === cur && "current", !b.local && "on origin"].filter(Boolean);
    return h("li", {}, h("button", {
      type: "button", class: "branch-item", "aria-current": b.name === cur ? "true" : "false",
      onclick: attempt(() => b.name !== cur && switchBranch(b.name)),
    }, ico(b.name === cur ? "check" : "branch"), h("span", { class: "branch-item-name" }, b.name),
    ...tags.map((t) => h("span", { class: `branch-tag ${t.replace(/ /g, "-")}` }, t))));
  });
  if (q && !list.some((b) => b.name === q)) {
    items.push(h("li", {}, ib({ type: "button", class: "branch-item branch-create", onclick: attempt(() => switchBranch(q, true)) },
      "plus", h("span", {}, "Create branch ", h("code", {}, q), " from ", h("code", {}, cur)))));
  }
  if (!items.length) items.push(h("li", { class: "hint" }, "No branches match."));
  $("#branch-list").replaceChildren(...items);
}

// Point new tasks at another branch (creating it from the current one with create).
async function switchBranch(name, create = false) {
  const old = branchSheet?.p || state.project;
  const saved = await api("PUT", `/api/projects/${old.id}/branch`, { branch: name, create }, old.prefix || "");
  const p = { ...saved, machine: old.machine, prefix: old.prefix };
  if (projectKey(state.project) === projectKey(old)) state.project = p;
  state.projects = state.projects.map((x) => (projectKey(x) === projectKey(old) ? p : x));
  $("#branch-sheet").close();
  renderProjectHeader();
  renderProjectList();
  toast(create ? `Created ${name}` : "", { head: `New tasks land on ${baseBranch(p)}`, icon: "branch" });
  if (projectKey(state.project) !== projectKey(p)) return;
  state.unpushed = 0;
  state.unpushedTodos = null;
  await Promise.all([loadUnpushed(), loadRemote(true), state.view === "history" && loadGraph(), state.view === "files" && loadFiles()]);
}

// Push the working branch and open its pull request into the default branch.
async function createBranchPR(btn) {
  const s = branchSheet;
  if (!s) return;
  await busy(btn, async () => {
    const pr = await api("POST", `/api/projects/${s.p.id}/branch/pr`, {}, s.p.prefix || "");
    if (branchSheet === s && s.res) { s.res.pr = pr; renderBranches(); }
    toast(`Pull request #${pr.number} is open`, { head: "Pull request created", icon: "pr", action: { label: "Open", run: () => window.open(pr.url, "_blank", "noopener") } });
  });
}

$("#pm-branch").addEventListener("click", attempt(openBranches));
$("#branch-filter").addEventListener("input", renderBranches);
$("#branch-filter").addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || e.isComposing) return;
  e.preventDefault();
  const q = e.currentTarget.value.trim(), s = branchSheet;
  if (!q || !s) return;
  const names = (s.res?.branches || []).map((b) => b.name);
  const match = names.includes(q) ? q : names.filter((n) => n.toLowerCase().includes(q.toLowerCase()));
  if (typeof match === "string") { if (match !== (s.res?.current || baseBranch(s.p))) attempt(() => switchBranch(match))(); }
  else if (match.length === 1) attempt(() => switchBranch(match[0]))();
  else if (!match.length) attempt(() => switchBranch(q, true))();
});
$("#branch-sheet").addEventListener("close", () => { branchSheet = null; });

export const title = (t) => t.title || t.prompt.split("\n")[0];
// Queue order: prioritised tasks first, lower before higher, oldest first
// among equals; then those without a priority (null), oldest first.
export const hasPriority = (t) => t.priority != null;
export const byPriority = (a, b) => (hasPriority(b) - hasPriority(a)) || (a.priority ?? 0) - (b.priority ?? 0) || a.created_at.localeCompare(b.created_at);

const isOld = (t) => t.status === "merged" && Date.now() - new Date(t.finished_at || t.updated_at) > DAY;

// laneItems is the board's tasks by lane, as shown: only the goal filtered
// to, if any, only the user's own with Mine on, and only those the tag
// filter and From scans let through (all: every task, as the review count
// and priorities need).
export function laneItems(all = false) {
  const out = Object.fromEntries(LANES.map((l) => [l.key, []]));
  const mine = mineOn() && myID();
  for (const x of state.todos.values()) {
    const t = shownTodo(x);
    if (!state.showOld && isOld(t)) continue;
    if (!all && state.goalFilter && t.goal_id !== state.goalFilter) continue;
    if (!all && mine && t.created_by !== mine) continue;
    if (!all && !onBoard(t)) continue;
    const lane = LANES.find((l) => l.statuses.includes(t.status));
    if (lane) out[lane.key].push(t);
  }
  for (const k in out) {
    out[k].sort(k === "done" ? (a, b) => b.updated_at.localeCompare(a.updated_at) : byPriority);
  }
  return out;
}

export function renderBoard() {
  if (drag.active) return; // don't rebuild under the user's finger; we re-render on drop
  const before = flipFirst();
  renderBoardNow();
  syncBoardEdges();
  syncRunClocks();
  flipPlay(before);
  hoverCard.sync();
}

// On a desktop the columns scroll sideways when they don't fit: the board's
// edges are shaded where there is more of it (.more-left, .more-right).
function syncBoardEdges() {
  const b = $("#board");
  const room = b.scrollWidth - b.clientWidth;
  b.classList.toggle("more-left", desktop.matches && b.scrollLeft > 1);
  b.classList.toggle("more-right", desktop.matches && b.scrollLeft < room - 1);
}
$("#board").addEventListener("scroll", syncBoardEdges, { passive: true });
new ResizeObserver(syncBoardEdges).observe($("#board"));

// FLIP: cards glide from where they were to where they are now, so a live
// status change reads as movement instead of a jump. New cards fade in.
function flipFirst() {
  const m = new Map();
  for (const el of $$(".todo[data-id]")) m.set(el.dataset.id, el.getBoundingClientRect());
  return m;
}
let boardRendered = false;
function flipPlay(before) {
  const first = !boardRendered;
  boardRendered = true;
  if (reduceMotion.matches || first) return;
  for (const el of $$(".todo[data-id]")) {
    const was = before.get(el.dataset.id);
    const now = el.getBoundingClientRect();
    if (!now.width) continue; // in a hidden lane (mobile)
    if (!was || !was.width) {
      el.animate([{ opacity: 0, transform: "scale(.97)" }, { opacity: 1, transform: "none" }], { duration: 240, easing: EASE });
      continue;
    }
    const dx = was.left - now.left, dy = was.top - now.top;
    if (Math.abs(dx) + Math.abs(dy) < 1) continue;
    el.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }], { duration: 320, easing: EASE });
  }
}

function renderBoardNow() {
  const items = laneItems();
  const empty = $("#board-empty");
  // The hub doesn't run tasks itself: with no runners yet it can't do
  // anything with a board, and its home is the welcome screen, projects or not.
  const welcome = state.runners?.length === 0;
  if (!state.project || welcome || state.runners === undefined) {
    $("#board").replaceChildren();
    $("#lane-tabs").replaceChildren();
    $("#board-head").hidden = true;
    $("#board-filters").hidden = true;
    $("#combine-bar").hidden = true;
    // Until the projects and runners are in, don't flash a welcome, a board
    // or "create your first project" that one of the others then replaces.
    empty.hidden = !state.projectsLoaded || state.runners === undefined;
    if (empty.hidden) return;
    // A hub with no runners yet: welcome, and explain how to add one first.
    if (welcome) { empty.replaceChildren(...welcomeScreen()); return; }
    empty.replaceChildren(
      // Optional illustration: save as web/static/img/empty-board.webp, 960x720 (4:3). Hidden if missing.
      // Convert: magick in.png -resize 960x720^ -gravity center -extent 960x720 -quality 85 web/static/img/empty-board.webp
      // Files are embedded in the binary: rebuild and restart dash afterwards.
      art("/img/empty-board.webp", 480, 360),
      h("p", { class: "empty-title" }, "Create your first project"),
      h("p", {}, "A project is a git repository. Every task runs in its own worktree, so several can run at once."),
      h("div", { class: "empty-actions" },
        ib({ class: "btn primary", onclick: () => openNewProject() }, "plus", "Add project")));
    return;
  }
  empty.hidden = true;
  $("#board-head").hidden = false;
  renderRunnerStatus();
  renderGoalFilter();
  renderMineFilter();
  const tagged = renderTagFilter();
  // The filters show only while there is something to filter.
  $("#board-filters").hidden = $("#goal-filter").hidden && $("#mine-filter").hidden && !tagged;
  renderBoardSearch();
  renderCombineBar();

  // A task just added may already have moved on (a queued task picked up at
  // once is Running): follow it rather than show its old lane empty.
  const f = state.follow;
  if (f && (Date.now() > f.until || state.lane !== f.lane)) state.follow = null;
  else if (f && !items[state.lane]?.length) {
    const t = f.ids.map((id) => state.todos.get(id)).find(Boolean);
    const lane = t && LANES.find((l) => l.statuses.includes(t.status))?.key;
    if (lane && lane !== state.lane) state.lane = f.lane = lane;
  }

  // Mobile opens on whatever needs attention first.
  if (!state.lane || laneHidden(state.lane)) {
    state.lane = ["review", "in_progress", "queued", "draft"].find((k) => items[k].length) || "queued";
  }

  $("#lane-tabs").replaceChildren(...LANES.map((l) => h("button", {
    role: "tab", class: `lane-tab s-${l.key === "done" ? "merged" : l.key}`, "data-lane": l.key, hidden: laneHidden(l.key),
    "aria-selected": String(state.lane === l.key), "data-count": items[l.key].length,
    onclick: () => { state.lane = l.key; renderBoard(); },
  }, l.label, h("span", { class: "n" }, items[l.key].length))));

  // Desktop: a minimized column stays in its place as a thin strip with its
  // name and count (.col-strip), which expands it again.
  $("#board").replaceChildren(...LANES.map((l, li) => h("section", {
    "data-lane": l.key, hidden: laneHidden(l.key),
    class: `col s-${l.key === "done" ? "merged" : l.key}${state.lane === l.key ? " active" : ""}${keys.active && keys.cur.lane === li ? " cursor" : ""}${state.collapsed.has(l.key) ? " collapsed" : ""}`, "aria-label": l.label,
  },
    state.collapsed.has(l.key) ? h("button", {
      type: "button", class: "col-strip", "aria-expanded": "false", "aria-label": `${l.label}, ${items[l.key].length}: expand`,
      title: `Expand ${l.label}`, onclick: () => toggleCollapsed(l.key),
    }, h("span", { class: "n" }, items[l.key].length), h("span", { class: "col-label" }, l.label)) : null,
    h("header", { class: "col-head" },
      h("span", { class: "col-label" }, l.label), h("span", { class: "n" }, items[l.key].length),
      ib({ class: "icon-btn col-toggle", title: `Minimize ${l.label}`, "aria-label": `Minimize ${l.label}`, onclick: () => toggleCollapsed(l.key) }, "minimize")),
    h("div", { class: "col-body" },
      l.key === "done" ? pushBar() : null,
      items[l.key].length ? h("ol", { class: "cards" }, items[l.key].map((t, ri) => h("li", {}, todoRow(t, li, ri)))) : null,
      l.key === "draft" ? selectBtn(items.draft) : null,
      l.key === "done" ? oldToggle() : null,
      l.key === "done" ? clearDoneBtn() : null))));
  // What needs you counts every task, whatever the board is filtered to.
  // (Always a filter: drafts from scans below high show only under From scans.)
  const every = laneItems(true);
  updateNeedsYou(every);

  $(".lane-tab[aria-selected=true]")?.scrollIntoView({ block: "nearest", inline: "nearest" });

  const goals = waitingGoals().length;
  const review = every.review.length + state.perms.length + goals;
  const badge = $("#review-badge");
  if (String(review) !== badge.dataset.n && review && !reduceMotion.matches) {
    badge.animate([{ transform: "scale(1.35)" }, { transform: "scale(1)" }], { duration: 260, easing: EASE });
  }
  badge.hidden = !review;
  badge.dataset.n = review;
  // The count on a phone (and a folded sidebar); on a desktop the breakdown in words.
  const questions = state.perms.filter(isQuestion).length, approvals = state.perms.length - questions;
  const parts = [every.review.length && `${every.review.length} review`, approvals && plural(approvals, "approval"),
    questions && plural(questions, "question")].filter(Boolean);
  // "2 goals" leads to every project's goals (goals.js showAllGoals).
  const goalPart = goals ? h("span", { class: "rb-goals" }, plural(goals, "goal")) : null;
  badge.replaceChildren(h("span", { class: "rb-n", "aria-hidden": "true" }, String(review)),
    h("span", { class: "rb-text" }, parts.join(" · "), parts.length && goalPart ? " · " : "", goalPart));
  badge.title = [every.review.length && `${every.review.length} to review`, approvals && `${approvals} waiting for approval`,
    questions && `${plural(questions, "question")} for you`,
    goals && `${goals} goal${goals > 1 ? "s" : ""} waiting for you`].filter(Boolean).join(" · ");
  renderBoardDot(review);
}

// The badge's "2 goals" opens Goals with every project's goals, not the Board.
$("#review-badge").addEventListener("click", (e) => {
  if (!e.target.closest(".rb-goals")) return;
  e.preventDefault();
  e.stopPropagation();
  showAllGoals();
});

// The Board button's dot when nothing waits on you: yellow while tasks run or
// merge, green once every task that ran this session has landed in Done.
// Waiting work wins: the review-coloured count above replaces the dot.
// The green dot goes once the board has been looked at, until the next round.
// (Only in memory: after a reload the round is empty, so no dot comes back.)
const boardRun = { project: null, ids: new Set(), seen: false };
function renderBoardDot(waiting) {
  const pid = state.project?.id ?? null;
  if (boardRun.project !== pid) { boardRun.project = pid; boardRun.ids.clear(); boardRun.seen = false; }
  const active = [...state.todos.values()].filter((t) => t.status === "in_progress" || t.status === "merging");
  // A new run after everything settled starts a fresh round.
  if (active.length && !active.some((t) => boardRun.ids.has(t.id))) { boardRun.ids.clear(); boardRun.seen = false; }
  for (const t of active) boardRun.ids.add(t.id);
  const tracked = [...boardRun.ids].map((id) => state.todos.get(id)).filter(Boolean);
  const done = !active.length && tracked.length > 0 && tracked.every((t) => t.status === "merged");
  if (done && boardShown()) boardRun.seen = true;
  const dot = $("#board-dot");
  dot.hidden = !!waiting || (!active.length && (!done || boardRun.seen));
  dot.classList.toggle("done", !active.length && done);
  // A dot on a phone; on a desktop it says which, in a word.
  dot.textContent = active.length ? "running" : "all merged";
  dot.title = active.length ? `${plural(active.length, "task")} running or merging` : "Every task that ran has merged";
}
// Whether the board is in front of the user: its view, not under the phone's
// chat page, in a tab that isn't hidden.
function boardShown() { return state.view === "board" && !chatPage() && !document.hidden; }
// boardSeen hides a green dot once the board is shown (nav.js calls it on every
// view change). The DOM check first: at startup this module may not have run yet.
export function boardSeen() {
  const dot = $("#board-dot");
  if (!dot || !dot.classList.contains("done") || !boardShown()) return;
  boardRun.seen = true;
  dot.hidden = true;
}
document.addEventListener("visibilitychange", boardSeen);

// ------------------------------------------------------------------ swipe between lanes (phones)
// On a phone the board shows one lane at a time: a horizontal swipe on it
// moves to the next lane (left) or the previous one (right), as the tabs do.
// Passive listeners, so vertical scrolling in the lane is never held up. Not
// from the screen's edges (iOS's back gesture), nor in anything that scrolls
// sideways itself, nor once a long-press has started dragging a card.
const SWIPE = { min: 50, edge: 20 };
let swipe = null;

// Whether el, or an ancestor inside the board, scrolls sideways.
function inSideScroller(el) {
  for (; el && el.id !== "board"; el = el.parentElement) {
    const ox = getComputedStyle(el).overflowX;
    if ((ox === "auto" || ox === "scroll") && el.scrollWidth > el.clientWidth) return true;
  }
  return false;
}

// The next shown lane from the current one in direction d (+1/-1), or null
// at the first or last.
function swipeLane(d) {
  const shown = LANES.map((l) => l.key).filter((k) => !laneHidden(k));
  const i = shown.indexOf(state.lane);
  return i < 0 ? null : shown[i + d] ?? null;
}

// A touch's moves and end go to the element it started on, even once a
// re-render (renderBoard replaces every lane) has taken that element out of
// the board, and then they no longer bubble up to it: so they are listened
// for on that element.
let unlistenSwipe = null;

$("#board").addEventListener("touchstart", (e) => {
  swipe = null;
  unlistenSwipe?.();
  unlistenSwipe = null;
  if (desktop.matches || e.touches.length !== 1 || drag.active) return;
  const t = e.touches[0];
  if (t.clientX < SWIPE.edge || t.clientX > innerWidth - SWIPE.edge) return;
  if (inSideScroller(e.target)) return;
  swipe = { x: t.clientX, y: t.clientY, dx: 0, dy: 0 };
  const el = e.target;
  const opts = { passive: true };
  const cancel = () => { swipe = null; unlisten(); };
  const end = (ev) => { unlisten(); swipeEnd(ev); };
  const unlisten = () => {
    el.removeEventListener("touchmove", swipeMove, opts);
    el.removeEventListener("touchend", end, opts);
    el.removeEventListener("touchcancel", cancel, opts);
    if (unlistenSwipe === unlisten) unlistenSwipe = null;
  };
  el.addEventListener("touchmove", swipeMove, opts);
  el.addEventListener("touchend", end, opts);
  el.addEventListener("touchcancel", cancel, opts);
  unlistenSwipe = unlisten;
}, { passive: true });

function swipeMove(e) {
  if (!swipe) return;
  if (drag.active || e.touches.length !== 1) { swipe = null; return; }
  const t = e.touches[0];
  swipe.dx = t.clientX - swipe.x;
  swipe.dy = t.clientY - swipe.y;
}

function swipeEnd(e) {
  const s = swipe;
  swipe = null;
  if (!s || drag.active || desktop.matches) return;
  const t = e.changedTouches[0];
  if (t) { s.dx = t.clientX - s.x; s.dy = t.clientY - s.y; }
  // Clearly sideways: well past the threshold and mostly horizontal.
  if (Math.abs(s.dx) <= SWIPE.min || Math.abs(s.dx) < 2 * Math.abs(s.dy)) return;
  const d = s.dx < 0 ? 1 : -1;
  const lane = swipeLane(d);
  if (!lane) return;
  state.lane = lane;
  renderBoard();
  if (reduceMotion.matches) return;
  $("#board .col.active")?.animate(
    [{ transform: `translateX(${d * 48}px)`, opacity: 0.4 }, { transform: "none", opacity: 1 }],
    { duration: 220, easing: EASE });
}

// ------------------------------------------------------------------ Mine filter
// Only the tasks the signed-in user created; in an org of one it is hidden.
// Like the goal filter, a lane it leaves empty gives way to one with tasks.
export function toggleMine(on = !mineOn()) {
  if (!multiMember() || !myID()) return;
  setMine(on);
  state.lane = null;
  renderBoard();
}

// Shown while it can filter something: someone else made a task here (or it is on).
function renderMineFilter() {
  const btn = $("#mine-filter");
  const on = mineOn();
  const others = [...state.todos.values()].some((t) => t.created_by && t.created_by !== myID());
  btn.hidden = !multiMember() || !myID() || (!on && !others);
  if (btn.hidden) return;
  btn.setAttribute("aria-pressed", String(on));
  const me = people.byId.get(myID()) || { id: myID(), username: state.me?.username, avatar: state.me?.user?.avatar };
  btn.replaceChildren(avatar(me), "Mine");
}
$("#mine-filter").addEventListener("click", () => toggleMine());

// ------------------------------------------------------------------ search
// The magnifier in the board's header opens a field that filters the cards by
// their title and instructions as you type (onBoard in tags.js); Esc clears it.
// It stays open while it holds text.
const search = { open: false };

function renderBoardSearch() {
  const input = $("#board-search-input");
  const q = boardFilter.q;
  if (document.activeElement !== input && input.value !== q) input.value = q; // another project's was cleared
  const open = search.open || !!q;
  input.hidden = !open;
  $("#board-head").classList.toggle("searching", open);
  $("#board-search-open").setAttribute("aria-expanded", String(open));
}

function openBoardSearch() {
  search.open = true;
  renderBoardSearch();
  $("#board-search-input").focus();
}

function closeBoardSearch() {
  search.open = false;
  $("#board-search-input").value = "";
  setSearch("");
}

$("#board-search-open").addEventListener("click", () => {
  if (search.open || boardFilter.q) closeBoardSearch();
  else openBoardSearch();
});
$("#board-search-input").addEventListener("input", (e) => setSearch(e.target.value));
$("#board-search-input").addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || e.isComposing) return;
  e.preventDefault();
  e.stopPropagation();
  closeBoardSearch();
  $("#board-search-open").focus();
});
// Left empty, it folds back to the magnifier.
$("#board-search-input").addEventListener("blur", () => {
  if (boardFilter.q || !search.open) return;
  search.open = false;
  renderBoardSearch();
});
// The people arrive after the board's first drawing: draw it again with them.
people.onChange = () => { renderBoard(); if (state.detail) renderDetail(); };

// creatorEl is the face of who created task t, on its card: only in an org
// of several, and not for tasks Dash made or whose creator left.
function creatorEl(t) {
  if (!multiMember()) return null;
  const u = creator(t);
  if (!u) return null;
  return h("span", { class: "todo-creator", title: `Created by ${u.username}` },
    avatar(u), h("span", { class: "sr-only" }, `Created by ${u.username}`));
}

function toggleCollapsed(key) {
  if (!state.collapsed.delete(key)) state.collapsed.add(key);
  store.setJSON("dash.collapsed", [...state.collapsed]);
  renderBoard();
}

function oldToggle() {
  const hidden = [...state.todos.values()].filter(isOld).length;
  if (!hidden && !state.showOld) return null;
  return ib({ class: "link older", onclick: () => { state.showOld = !state.showOld; renderBoard(); } }, state.showOld ? "up" : "down",
    state.showOld ? "Hide merged tasks older than a day" : `Show ${plural(hidden, "older merged task")}`);
}

// Select, at the foot of Drafts: shows every draft's box and the bar, which
// queues or combines the drafts picked (a phone has no hover to show a box).
function selectBtn(drafts) {
  if (drafts.length < 2 || document.body.classList.contains("picking")) return null;
  return ib({ class: "link older select-drafts", onclick: startPicking }, "checks", "Select");
}

// Failed tasks wait in Review to be retried, reworked or deleted, so Delete merged leaves them.
const doneTodos = () => [...state.todos.values()].filter((t) => t.status === "merged");

function clearDoneBtn() {
  if (!doneTodos().length) return null;
  return ib({ class: "link older", onclick: attempt(clearDone) }, "trash", "Delete merged…");
}

async function clearDone() {
  const list = doneTodos();
  if (!list.length) return;
  const unpushed = list.filter((t) => t.status === "merged" && state.unpushedTodos?.has(t.id)).length;
  const n = list.length;
  if (!await ask({ title: unpushed ? `Not all merged tasks are pushed — delete ${n} anyway?` : `Delete ${plural(n, "merged task")}?`,
    body: "Deletes every merged task in this project. This can't be undone." +
      (unpushed ? ` ${unpushed} merged ${unpushed === 1 ? "task hasn't" : "tasks haven't"} been pushed yet. Their changes are not discarded: the commits stay on the local default branch and can still be pushed.` : ""),
    ok: "Delete", danger: true })) return;
  const res = await Promise.allSettled(list.map((t) => api("DELETE", `/api/todos/${t.id}`)));
  if (list.some((t) => t.id === state.detail)) closeDetail();
  const bad = res.filter((r) => r.status === "rejected").length;
  toast(bad ? `Deleted ${n - bad}, ${bad} failed` : `Deleted ${plural(n, "task")}`);
}

// runClock is a running task's stopwatch on its card. One timer ticks every
// clock on the board (syncRunClocks), and stops when none is shown.
function runClock(iso) {
  return h("span", { class: "run-clock", title: runTitle(iso), "data-started": iso },
    ico("stopwatch"), h("span", { class: "run-clock-t" }, clockText(iso)));
}
const runTitle = (iso) => { const a = ago(iso); return `Running for ${a === "now" ? "under a minute" : a}, since ${dateTimeText(iso)}`; };
let runTimer = 0;
function tickRunClocks() {
  const els = $$(".run-clock[data-started]");
  if (!els.length) { clearInterval(runTimer); runTimer = 0; return; }
  if (document.hidden) return;
  for (const el of els) {
    if (!el.offsetParent) continue; // on a hidden lane or view
    el.lastChild.textContent = clockText(el.dataset.started);
    el.title = runTitle(el.dataset.started);
  }
}
function syncRunClocks() {
  const any = !!$(".run-clock[data-started]");
  if (any && !runTimer) runTimer = setInterval(tickRunClocks, 1000);
  else if (!any && runTimer) { clearInterval(runTimer); runTimer = 0; }
}

// doneAt is when a merged or failed task finished: finished_at, or updated_at
// for one that has none (a task synced in from elsewhere).
export const doneAt = (t) => (t.status === "merged" || t.status === "failed") ? (t.finished_at || t.updated_at || "") : "";

// doneMeta is "Merged 3d ago" / "Failed 2h ago" for the task pane; older
// than a week it gives the date itself.
export function doneMeta(t) {
  const at = doneAt(t);
  if (!at || isNaN(new Date(at))) return null;
  const word = closedNoChanges(t) ? "Closed" : t.status === "merged" ? "Merged" : "Failed";
  const text = Date.now() - new Date(at) > 7 * DAY ? dateTimeText(at) : agoText(at);
  return h("time", { datetime: at, title: dateTimeText(at) }, `${word} ${text}`);
}

// A merged todo is pushed once origin has its commit (in "pr" mode, and on
// runners, merging is pushing).
function isPushed(t) {
  const p = state.project;
  if (t.status !== "merged" || closedNoChanges(t) || !p?.remote_url) return false;
  if (landsOnGitHub(p)) return true;
  return !!state.unpushedTodos && !state.unpushedTodos.has(t.id);
}

// depWait tells what a queued todo is waiting for: its dependencies that
// aren't merged yet and the task it builds on while that isn't finished
// (merged or failed), and those of them that failed or are drafts, which
// hold it back until someone acts. Null when nothing holds it back.
function depWait(t) {
  if (t.status !== "queued" || (!t.depends_on?.length && !t.builds_on)) return null;
  const deps = (t.depends_on || []).map((id) => state.todos.get(id)).filter((d) => d && d.status !== "merged");
  const base = t.builds_on && !t.depends_on?.includes(t.builds_on) ? state.todos.get(t.builds_on) : null;
  if (base && base.status !== "merged" && base.status !== "failed") deps.push(base);
  if (!deps.length) return null;
  return { deps, failed: deps.filter((d) => d.status === "failed"), drafts: deps.filter((d) => d.status === "draft") };
}

// A dependency's title (or label) that opens it.
export function depLink(d, label = title(d)) {
  return h("button", {
    type: "button", class: "dep-link", title: `Open “${title(d)}”`,
    onpointerdown: (e) => e.stopPropagation(),
    onclick: (e) => { e.stopPropagation(); openDetail(d.id); },
  }, label);
}

// taskLink is depLink inside running text: a link, which wraps with the
// words around it where a button would drop to a line of its own.
export function taskLink(d, label = title(d)) {
  return h("a", {
    href: "#", class: "dep-link", title: `Open “${title(d)}”`,
    onpointerdown: (e) => e.stopPropagation(),
    onclick: (e) => { e.preventDefault(); e.stopPropagation(); openDetail(d.id); },
  }, label);
}

// "waits for A, B +2", or "waits for A, which failed" when a dependency
// failed (the only case shown as a problem). A draft it waits for has
// Queue both on the card; drafts marks them in the task's pane, which hasn't.
function depLine({ deps, failed }, max = 2, drafts = false) {
  const list = (xs) => [
    ...xs.slice(0, max).flatMap((d, i) => [i ? ", " : "", depLink(d), drafts && d.status === "draft" ? " (draft)" : ""]),
    xs.length > max ? ` +${xs.length - max}` : "",
  ];
  if (failed.length) return ["waits for ", ...list(failed), ", which failed"];
  return ["waits for ", ...list(deps)];
}

// queueDepsChip queues the drafts a queued task waits for.
function queueDepsChip(drafts) {
  return cardChip("enqueue-chip", "", "queue", drafts.length > 1 ? `Queue all ${drafts.length + 1}` : "Queue both", async () => {
    for (const d of drafts) await api("POST", `/api/todos/${d.id}/enqueue`);
    toast(`Queued ${drafts.length > 1 ? plural(drafts.length, "task") : title(drafts[0])}`);
  });
}

// The detail view's line about the task t builds on: "Builds on #id title".
export function buildsOnMeta(t) {
  if (!t.builds_on) return null;
  const d = state.todos.get(t.builds_on);
  return h("span", { class: "deps", title: "Its first run continues that task's Claude session" },
    "Builds on ", d ? depLink(d, `#${d.id} ${title(d)}`) : `#${t.builds_on}`);
}

// Conflicts found ahead of the merge queue (t.conflicts) show while the
// task's branch is in play: in progress, in review or merging.
function conflictsOf(t) {
  return ["in_progress", "review", "merging"].includes(t.status) && t.conflicts?.length ? t.conflicts : null;
}

// The other task a link, a conflict or a run log entry names: by its id
// here, or (a runner's record, seen on its hub) by the hub's id.
export function linkedTask(id, hubId) {
  return (id && state.todos.get(id)) || (hubId && state.todos.get(hubId)) || null;
}

const fileList = (fs) => (fs?.length ? fs.join(", ") : "");

// What task t had to do with other tasks, newest first: the conflicts it
// has now, then t.links (conflicts found earlier, conflict notes given and
// got, active_work calls). Each item's parts(who) words it around who, the
// other task (a string, or a link to it).
function interactions(t) {
  const items = [], live = new Set();
  for (const c of conflictsOf(t) || []) {
    if (c.base) continue;
    live.add(c.todo_id || c.name);
    items.push({ o: linkedTask(c.todo_id, c.hub_todo_id), id: c.todo_id, name: c.name, conflict: true,
      parts: (who) => [who, ` also changes ${fileList(c.files)}`] });
  }
  for (const l of [...(t.links || [])].reverse()) {
    if (l.kind === "conflict" && live.has(l.todo_id || l.name)) continue; // a conflict it has now: said above
    const fs = fileList(l.files);
    const parts = {
      conflict: (who) => ["Conflicted with ", who, fs ? ` in ${fs}` : "", ", earlier"],
      note: l.out
        ? (who) => ["Noted ", who, `, behind in the merge queue, of conflicts in ${fs}`]
        : (who) => ["From ", who, `, ahead in the merge queue: conflicts in ${fs}; ${l.delivered ? "sent to Claude" : "noted for Claude's next run"}`],
      active_work: l.out
        ? (who) => ["Checked ", who, `'s work${fs ? ` (${fs})` : ""}`]
        : (who) => [who, ` checked this task's work${fs ? ` (${fs})` : ""}`],
    }[l.kind];
    if (parts) items.push({ o: linkedTask(l.todo_id, l.hub_todo_id), id: l.todo_id, name: l.name, parts });
  }
  return items;
}

// The other task of an interaction, as text and as a link that opens it.
const whoText = (it) => (it.o ? `#${it.o.id} ${title(it.o)}` : it.id ? `#${it.id}${it.name ? ` ${it.name}` : ""}` : it.name || "another task");
const whoNode = (it) => (it.o ? taskLink(it.o, whoText(it)) : whoText(it));
const baseLine = (c) => `Conflicts with ${c.name} in ${fileList(c.files)}`;

// A card's link marker, while the task is active: "↔ Fix login +1" for the
// tasks it conflicts with, got or gave a note, or looked up or was looked
// up by (active_work), which opens the first; all of it on hover. With a
// conflict with the default branch only: "Conflicts with main in foo.go".
function linkTag(t) {
  if (!["queued", "in_progress", "review", "merging"].includes(t.status)) return null;
  const items = interactions(t), base = (conflictsOf(t) || []).filter((c) => c.base);
  if (!items.length && !base.length) return null;
  const hover = [...base.map(baseLine), ...items.map((it) => it.parts(whoText(it)).join(""))].join("\n");
  const cls = base.length || items.some((it) => it.conflict) ? "link-tag conflict-tag" : "link-tag";
  if (!items.length) {
    const c = base[0], fs = c.files || [];
    return h("span", { class: cls, title: `${hover}\nFound ahead of the merge queue` },
      `Conflicts with ${c.name} in ${fs[0] || "files"}${fs.length > 1 ? ` +${fs.length - 1}` : ""}`);
  }
  const others = new Set(items.map((it) => it.o?.id || it.id || it.name));
  const it = items[0], more = others.size > 1 ? ` +${others.size - 1}` : "";
  const label = `↔ ${it.o ? title(it.o) : it.name || `#${it.id}`}${more}`;
  const el = it.o ? depLink(it.o, label) : h("span", {}, label);
  el.classList.add(...cls.split(" "));
  el.title = it.o ? `${hover}\nClick to open “${title(it.o)}”` : hover;
  el.setAttribute("aria-label", `Linked tasks: ${hover}`);
  return el;
}

// The link marker on a row of its own under the card's title and line, so
// it takes no width from them.
function linkRow(t) {
  const tag = linkTag(t);
  return tag ? h("span", { class: "todo-links" }, tag) : null;
}

// The detail view's lines about other tasks: its conflicts with the default
// branch, then every interaction, each with a link to the other task.
export function conflictMeta(t) {
  const items = interactions(t), base = (conflictsOf(t) || []).filter((c) => c.base);
  if (!items.length && !base.length) return null;
  return h("span", { class: "links" },
    ...base.map((c) => h("span", { class: "conflicts", title: "Found ahead of the merge queue by merging the branches without a checkout" },
      "Conflicts with ", h("span", { class: "branch" }, c.name), ` in ${fileList(c.files)}`)),
    ...items.map((it) => h("span", { class: it.conflict ? "conflicts" : null }, "↔ ", ...it.parts(whoNode(it)),
      it.conflict && it.o?.status === "merging" ? " (ahead in the merge queue)" : "")));
}

// The detail view's line about dependencies.
export function depMeta(t) {
  const wait = depWait(t);
  if (wait) return h("span", { class: `deps${wait.failed.length ? " failed" : ""}` }, depLine(wait, 5, true));
  const deps = (t.depends_on || []).map((id) => state.todos.get(id)).filter(Boolean);
  if (!deps.length) return null;
  return h("span", { class: "deps" }, "Depends on ", deps.flatMap((d, i) => [i ? ", " : "", depLink(d)]));
}

// needsOwnRunner reports whether queued task t waits only because none of
// the runners with its project's labels does its people's work: they need a
// runner of their own, or a shared one (dispatch's "no runner of …'s").
export const needsOwnRunner = (t) => t.status === "queued" && !t.paused && /\bno runner of .+ has the labels /.test(t.waiting || "");
// needsOwnRunnerTag is the card's short label for that.
function needsOwnRunnerTag(t) {
  if (!needsOwnRunner(t)) return null;
  const me = myID();
  const mine = me > 0 && (t.created_by === me || t.run_by === me);
  return h("span", { class: "needs-runner-tag", title: t.waiting }, mine ? "Needs your runner" : "Needs their runner");
}

export function todoRow(t, li = -1, ri = -1) {
  const atCursor = keys.active && keys.cur.lane === li && keys.cur.row === ri;
  let sub = plainText(t.prompt);
  let subClass = "todo-sub";
  // Once Claude has run: what it did (its answer, for a task with no changes,
  // so it can be closed unopened), not the prompt.
  if (t.summary && ["review", "merging", "merged"].includes(t.status)) sub = plainText(t.summary);
  const fixing = t.sent_back && ["queued", "in_progress"].includes(t.status) ? t.sent_back : "";
  if (fixing && t.status === "queued") sub = `${/^review /.test(fixing) ? "Sent back from GitHub" : "Sent back from merge"}: ${fixing}`;
  if (t.status === "in_progress") { sub = state.lastLine.get(t.id) || "Working…"; subClass += " progress"; }
  // Tests or setup running, in any state (a manual test run on a task in review, the merge queue's tests).
  const act = state.activity.get(t.id);
  if (act && t.status !== "draft") { sub = activityNodes(act); subClass = "todo-sub progress activity"; }
  if (t.paused) sub = t.status === "in_progress" ? "Pausing…" : "Paused";
  if (t.waiting && t.status === "queued" && !t.paused) { sub = t.waiting; subClass = "todo-sub waiting-runner"; }
  if (waitsOnGitHub(t)) { sub = prStatus(t.pr); subClass = `todo-sub pr-status ${prTone(sub)}`; }
  if (t.error) { sub = t.error; subClass += " err"; }
  if (t.reviewing) { sub = "Claude is reviewing…"; subClass = "todo-sub progress"; }
  const asks = state.perms.filter((p) => p.todo_id === t.id);
  if (asks.length) { sub = asks.every(isQuestion) ? "Waiting for your answer" : "Waiting for your approval"; subClass = "todo-sub waiting"; }
  const wait = !t.paused && !t.error && depWait(t);
  if (wait) { sub = depLine(wait); subClass = `todo-sub deps${wait.failed.length ? " failed" : ""}`; }
  // Held back only by the weekly pace: it says so, with Start now beside it.
  const paced = canStartNow(t) && !t.error && !wait && pacing(t) && (!t.waiting || /to stay on pace/.test(t.waiting))
    && !state.perms.some((p) => p.todo_id === t.id);
  if (paced) { sub = "Waiting for weekly pace"; subClass = "todo-sub waiting-runner"; }
  // Waits for a task naming the same files to go first: Start now runs it anyway.
  const after = canStartNow(t) && !t.error && !wait && goesAfter(t);
  const checkTag = t.checks && ["review", "merging", "failed"].includes(t.status)
    ? h("span", { class: `check-tag ${t.checks.passed ? "pass" : "fail"}` }, t.checks.passed ? "Tests pass" : "Tests fail")
    : null;
  // The card is a container, not a button: its title is the button that opens
  // the task, and its actions (chips, PR link, dependency links) are sibling
  // controls, so each is focusable and announced on its own. A press anywhere
  // else on the card opens the task too.
  return h("div", {
    class: `todo s-${t.status}${isPushed(t) ? " pushed" : ""}${t.reviewing ? " reviewing" : ""}${state.detail === t.id ? " selected" : ""}${atCursor ? " cursor" : ""}${picked.has(t.id) ? " picked" : ""}`,
    "data-id": t.id, "aria-current": state.detail === t.id ? "true" : null,
    onclick: (e) => { if (!e.target.closest("a, button:not(.todo-open), label")) openDetail(t.id); },
  },
    h("span", { class: "node", "aria-hidden": "true" }),
    h("span", { class: "todo-main" },
      h("button", { type: "button", class: "todo-open" }, h("span", { class: "todo-title" }, title(t))),
      h("span", { class: subClass }, sub),
      linkRow(t),
      tagRow(t, [prioChip(t), t.model ? h("span", { class: "meta-chip model-chip" }, t.model) : null, t.run_on ? runOnChip(t.run_on) : null]),
      goalChip(t)),
    // A draft's Queue and pick box: on hover or focus, the box on every draft while picking.
    t.status === "draft" ? h("span", { class: "card-actions draft-actions" }, enqueueChip(t), pickBox(t)) : null,
    h("span", { class: "todo-side" },
      checkTag,
      t.status === "failed" ? h("span", { class: "failed-tag" }, "Failed") : null,
      t.no_changes && t.status === "review" ? h("span", { class: "nochange-tag" }, "No changes") : null,
      closedNoChanges(t) ? h("span", { class: "nochange-tag" }, "Closed · no changes") : null,
      t.paused ? h("span", { class: "paused-tag" }, "Paused") : null,
      needsOwnRunnerTag(t),
      t.runner && ["in_progress", "review", "merging", "failed"].includes(t.status) && severalRunners() ? h("span", { class: "runner-tag" }, runnerName(t.runner)) : null,
      t.reviewing ? h("span", { class: "reviewing-tag" }, "Reviewing") : null,
      t.pr && t.status !== "merged" ? prTag(t.pr) : null,
      waitsOnGitHub(t) ? prReviewTag(t.pr) : null,
      t.revert ? h("span", { class: "revert-tag" }, "Reverted") : null,
      t.ci_run ? h("span", { class: "ci-tag" }, "Build fix") : null,
      fixing ? h("span", { class: "sentback-tag", title: `Sent back: ${fixing}` }, /^review /.test(fixing) ? "Review comments" : /conflict/.test(fixing) ? "Merge conflict" : "Failing checks") : null,
      t.attachments?.length ? h("span", { class: "attach-tag", title: `${t.attachments.length} attached` }, clip(), String(t.attachments.length)) : null,
      t.diff_files && ["review", "merging", "merged", "failed"].includes(t.status)
        ? h("span", { class: "diffsize", title: `${plural(t.diff_files, "file")} changed` },
          h("span", { class: "plus" }, `+${t.diff_add || 0}`), " ", h("span", { class: "minus" }, `−${t.diff_del || 0}`))
        : null,
      ["review", "merging", "merged", "failed"].includes(t.status) ? tokenTag(t) : null,
      t.status === "in_progress" && t.started_at ? runClock(t.started_at) : null,
      previewTag(t),
      t.status === "merged" && doneAt(t) ? h("span", { class: "done-ago", title: `${closedNoChanges(t) ? "Closed" : "Merged"} ${dateTimeText(doneAt(t))}` }, agoText(doneAt(t))) : null,
      unpushed(t) ? h("span", { class: "unpushed-tag" }, "Not pushed") : null,
      t.status === "review" && t.finished_at ? h("span", { class: "card-ago", title: dateTimeText(t.finished_at) }, agoText(t.finished_at)) : null,
      creatorEl(t),
      wait?.drafts.length && !wait.failed.length ? h("span", { class: "card-actions" }, queueDepsChip(wait.drafts)) : null,
      paced || after ? h("span", { class: "card-actions" }, startNowChip(t)) : null),
    t.status === "review" && !t.reviewing ? reviewActions(t) : null);
}

// The card's chips for its priority, and the runner it is set to run on.
function prioChip(t) {
  if (!hasPriority(t)) return null;
  const level = priorityLevel(t.priority);
  return h("span", { class: `meta-chip prio-chip prio-${level}` }, ico(level === "urgent" ? "alert" : "up"), PRIORITY_LEVELS[level]);
}

function runOnChip(id) {
  const name = state.runners?.find((r) => r.id === id)?.name || id;
  return h("span", { class: "meta-chip run-on-chip" }, ico("computer"), name);
}

// A card names the runner a task ran on only when the project has several.
function severalRunners() {
  const p = state.project;
  return (state.runners || []).filter((r) => !p || p.prefix || servesProject(r, p)).length > 1;
}

// A merged task whose commit origin doesn't have yet (Push, at the top of Merged).
function unpushed(t) {
  const p = state.project;
  return t.status === "merged" && !closedNoChanges(t) && !!p?.remote_url && !landsOnGitHub(p) && !!state.unpushedTodos?.has(t.id);
}

// A task whose pull request is open on GitHub: it is reviewed, and merged
// or closed, there (in pull request mode, from the moment Claude finishes it).
export function waitsOnGitHub(t) {
  return t.pr?.state === "OPEN" && ["review", "merging"].includes(t.status);
}

// Whether Approve applies: not to a task reviewed on GitHub, unless opening
// or pushing its pull request failed (its error says so) and approving retries.
export function approvable(t) {
  return t.status === "review" && (!waitsOnGitHub(t) || !!t.error);
}

// Whether Close applies instead of Approve: a task in review that changed
// no files has nothing to merge, only an answer to read.
export function closable(t) {
  return t.status === "review" && !!t.no_changes && !t.pr;
}

// A task closed without changes: Merged, but nothing was merged.
export function closedNoChanges(t) {
  return t.status === "merged" && !!t.no_changes && !t.merged_commit;
}

// closeTodo closes task t, in review without changes.
export async function closeTodo(t) {
  await whileBusy(t, "Closing…", () => api("POST", `/api/todos/${t.id}/close`));
  toast("Closed");
}

// The tasks being approved, merged or closed, each with what its main
// button says meanwhile ("Merging…"). Their buttons, on the card and in the
// task's pane, stay disabled until the request is done, also when the
// board is drawn again in between.
export const busyTodos = new Map();

// whileBusy runs run with task t's buttons disabled and label on its main one.
export async function whileBusy(t, label, run) {
  if (busyTodos.has(t.id)) return;
  busyTodos.set(t.id, label);
  redrawBusy(t.id);
  try {
    return await run();
  } finally {
    busyTodos.delete(t.id);
    redrawBusy(t.id);
  }
}

function redrawBusy(id) {
  renderBoard();
  const t = state.detail === id && state.todos.get(id);
  if (t) renderActions(t);
}

// reviewActions is a card's row of buttons in review: Review first (Close,
// for a task that changed nothing), then Approve or Merge pull request,
// then Ask Claude and Preview as icons. All disabled while one runs.
function reviewActions(t) {
  const el = h("span", { class: "card-actions review-actions" },
    closable(t) ? closeChip(t) : reviewChip(t),
    closable(t) ? null : approvable(t) ? approveChip(t) : canMergePR(t) ? mergeChip(t) : null,
    closable(t) ? null : askClaudeChip(t),
    canPreview(t) ? previewChip(t) : null);
  const label = busyTodos.get(t.id);
  if (label == null) return el;
  el.classList.add("busy");
  el.setAttribute("aria-busy", "true");
  for (const b of el.querySelectorAll("button")) b.disabled = true;
  for (const a of el.querySelectorAll("a")) { a.setAttribute("aria-disabled", "true"); a.tabIndex = -1; }
  const main = closable(t) ? el.querySelector(".review-chip") : el.querySelector(".approve-chip");
  if (main) main.lastChild.textContent = label;
  return el;
}

// Whether Merge pull request applies: its pull request is open and ready
// to merge (approved and green, or approved automatically), or green and
// only waiting for an approval, which GitHub may not require. GitHub
// decides; when it refuses, its message is shown and nothing changes.
export function canMergePR(t) {
  return t.status === "review" && waitsOnGitHub(t) && !t.error && !t.pr.draft
    && ["merge", "review"].includes(t.pr.waits) && (t.pr.checks || []).every((c) => c.state === "passed");
}

const MERGE_METHODS = { squash: "squash and merge", merge: "a merge commit", rebase: "rebase and merge" };

// mergePR asks, then merges task t's pull request on GitHub, with the gh
// account of the computer that follows it (t's runner, or this Dash's).
export async function mergePR(t) {
  const method = state.project?.merge_method;
  const how = MERGE_METHODS[method] ? `with ${MERGE_METHODS[method]}` : "with the repository's default merge method";
  const where = t.runner ? `the runner ${runnerName(t.runner)}, which follows it` : "the computer running Dash";
  const ok = await ask({
    title: `Merge pull request #${t.pr.number}?`,
    body: `Merges "${title(t)}" into ${baseBranch(state.project) || "the default branch"} on GitHub ${how}. `
      + `It is done as the GitHub account gh is signed in with on ${where}. Branch protection applies: if GitHub can't merge it yet, it says why and nothing changes.`,
    ok: "Merge pull request",
  });
  if (!ok) return;
  const res = await whileBusy(t, "Merging…", () => api("POST", `/api/todos/${t.id}/merge-pr`));
  const as = res?.as ? ` as ${res.as}` : "";
  toast(res?.queued ? `Queued to merge${as}` : `Pull request #${t.pr.number} merged${as}`, { tone: "ok", icon: "merge" });
}

// cardChip is an action button on a card: a click (or press) on it runs
// run, with its errors shown, and doesn't reach the card under it, which
// would open the task or start a drag. icon is an icon's name or a node.
// Without a label it is an icon button, named by its title.
function cardChip(cls, title, icon, label, run) {
  const stop = (e) => e.stopPropagation();
  return h("button", {
    type: "button", class: `${cls}${label ? "" : " icon-chip"}`, title: title || null, "aria-label": label ? null : title,
    onpointerdown: stop,
    onclick: (e) => { stop(e); attempt(run)(); },
  }, typeof icon === "string" ? ico(icon) : icon, label ? h("span", { class: "chip-label" }, label) : null);
}

function mergeChip(t) {
  return cardChip("approve-chip", `Merge pull request #${t.pr.number}`, "merge", "Merge PR", () => mergePR(t));
}

// What a task's pull request waits for, as Dash last saw it.
export function prStatus(pr) {
  return pr.status || `Waiting for GitHub on PR #${pr.number}…`;
}

export function prTone(line) {
  if (/^(Blocked|Checks? (failed|still)|Still conflicts)/.test(line)) return "err";
  if (/^(Waiting for (you|a review)|Approved|Changes requested)/.test(line)) return "waiting";
  return "progress";
}

// Where the review on GitHub stands, and whether it is still a draft.
export function prReviewTag(pr) {
  if (pr.draft) return h("span", { class: "pr-review draft", title: "A draft until its checks pass" }, "Draft");
  const label = { approved: "Approved", changes_requested: "Changes requested", pending: "Awaiting review" }[pr.review];
  return label ? h("span", { class: `pr-review ${pr.review}`, title: "Review on GitHub" }, label) : null;
}

// One line per check, for a tooltip.
const CHECK_MARK = { passed: "✓", failed: "✗", pending: "…" };
export function prChecksTitle(pr) {
  return (pr.checks || []).map((c) => `${CHECK_MARK[c.state] || "·"} ${c.name}`).join("\n");
}

// prChecksEl is the pull request's checks as visible text, each with its mark
// and result in words ("✗ lint failed"), not only a colour.
export function prChecksEl(pr) {
  const checks = pr.checks || [];
  if (!checks.length) return null;
  const word = { passed: "passed", failed: "failed", pending: "running" };
  return h("span", { class: "pr-checks", role: "list", "aria-label": "Checks" },
    ...checks.map((c) => h("span", { class: `pr-check ${c.state}`, role: "listitem" }, `${CHECK_MARK[c.state] || "·"} ${c.name} ${word[c.state] || c.state}`)));
}

// The card's "PR #N", opening the pull request.
function prTag(pr) {
  return h("a", {
    class: "pr-tag", href: pr.url, target: "_blank", rel: "noopener", title: `Open pull request #${pr.number} on GitHub`,
    onpointerdown: (e) => e.stopPropagation(), onclick: (e) => e.stopPropagation(),
  }, `PR #${pr.number}`);
}

// You review it: on GitHub when it has a pull request, else in the task's diff,
// where line comments, Rework and Approve are.
function reviewChip(t) {
  const stop = (e) => e.stopPropagation();
  if (t.pr) {
    return h("a", {
      class: "review-chip", href: t.pr.url, target: "_blank", rel: "noopener",
      title: `Pull request #${t.pr.number}`, onpointerdown: stop, onclick: stop,
    }, ico("eye"), h("span", { class: "chip-label" }, "Review"));
  }
  return cardChip("review-chip", "", "eye", "Review", () => openDetail(t.id, { pane: "diff" }));
}

// Claude reviews it (the task view's "Review with Claude").
function askClaudeChip(t) {
  return cardChip("ask-claude-chip", "Ask Claude", "spark", "", async () => { await openDetail(t.id); reviewTodo(); });
}

// Opens the task with its live preview, starting the server if needed.
function previewChip(t) {
  const p = previewOf(t.id);
  return cardChip("preview-chip", "Preview", p ? h("span", { class: `pv-dot ${p.state}`, "aria-hidden": "true" }) : "window", "", () => previewTodo(t));
}

function approveChip(t) {
  return cardChip("approve-chip", "Approve", "check", "Approve", () => approveWithUndo(t));
}

function closeChip(t) {
  return cardChip("review-chip", "", "check", "Close", () => closeTodo(t));
}

// approveWithUndo approves task t from the board: it shows in Merging at
// once, and the merge starts once its toast has gone without Undo.
export function approveWithUndo(t) {
  const retry = waitsOnGitHub(t);
  moveWithUndo(t, { verb: "approve", status: "merging", icon: retry ? "pr" : "merge", head: retry ? "Pushing the pull request again" : "Approved — merging" });
}

// ------------------------------------------------------------------ moves with Undo
// A move from the board that is hard to take back (approve: it merges;
// to drafts: it closes the pull request) waits while its toast shows Undo.
// Meanwhile the card shows where it goes; the request is sent once the toast
// has gone without Undo, or at once when the page is left.
export const UNDO_MS = 5000;
const pendingMoves = new Map(); // task id → { status, send }

// shownTodo is task t as the board shows it: with a move waiting, in the status it moves to.
export function shownTodo(t) {
  const m = pendingMoves.get(t.id);
  return m && m.status !== t.status ? { ...t, status: m.status } : t;
}

// moveWithUndo moves task t: POST /api/todos/<id>/<verb>, which puts it in
// status, once the Undo in its toast (head) has passed. Not if the task has
// moved on meanwhile (approved in its pane, say).
export function moveWithUndo(t, { verb, status, head, icon }) {
  if (pendingMoves.has(t.id) || busyTodos.has(t.id)) return;
  const prefix = here(), path = `/api/todos/${t.id}/${verb}`;
  const m = { status, send: () => fetch(on(prefix, path), { method: "POST", keepalive: true }) };
  pendingMoves.set(t.id, m);
  renderBoard();
  toast(title(t), {
    head, icon, ms: UNDO_MS, action: { label: "Undo", run: () => {} },
    onClose: attempt(async (undone) => {
      if (pendingMoves.get(t.id) !== m) return; // sent already: the page was left
      const now = state.todos.get(t.id);
      if (undone || (now && now.status !== t.status)) { pendingMoves.delete(t.id); renderBoard(); return; }
      try {
        await api("POST", path, null, prefix);
      } finally {
        pendingMoves.delete(t.id);
        renderBoard();
      }
    }),
  });
}

addEventListener("pagehide", () => {
  for (const m of pendingMoves.values()) m.send();
  pendingMoves.clear();
});

function enqueueChip(t) {
  return cardChip("enqueue-chip", "Queue", "queue", "", () => todoKey(t, "s"));
}

// startNowChip starts a queued card that waits for the weekly pace, or for
// another task to go first.
function startNowChip(t) {
  return cardChip("enqueue-chip start-now-chip", "", "play", "Start now", () => startNow(t));
}

export function updateLiveLine(id) {
  const el = $(`.todo[data-id="${id}"] .todo-sub.progress`);
  if (!el) return;
  const act = state.activity.get(id);
  if (act) {
    // Only the output line changes while the same activity runs; the timer keeps ticking.
    const line = el.querySelector(".act-line");
    if (el.dataset.act === act.key && line) line.textContent = act.line || act.cmd;
    else el.replaceChildren(...activityNodes(act));
    el.dataset.act = act.key;
    el.classList.add("activity"); // its spans laid out with gaps, as on a card rendered with the activity
    return;
  }
  delete el.dataset.act;
  el.classList.remove("activity");
  el.textContent = state.lastLine.get(id) || "Working…";
}
