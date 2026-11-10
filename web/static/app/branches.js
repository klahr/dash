import { $, $$, h, ib, ico } from "./dom.js";
import { api } from "./api.js";
import { EASE, art, ask, attempt, busy, reduceMotion, toast } from "./dialogs.js";
import { clip } from "./attachments.js";
import { DAY, LANES, state } from "./state.js";
import { baseBranch, projectKey } from "./events.js";
import { openNewProject, renderProjectHeader, renderProjectList } from "./projects.js";
import { landsOnGitHub, loadUnpushed, pushBar } from "./board.js";
import { activityNodes } from "./activity.js";
import { closeDetail, openDetail } from "./detail.js";
import { canPreview, previewOf, previewTag, previewTodo } from "./preview.js";
import { reviewTodo } from "./drawmode.js";
import { renderRunnerBanner, runnerName, welcomeScreen } from "./runners.js";
import { updateNeedsYou } from "./notifications.js";
import { loadRemote } from "./origin.js";
import { loadGraph } from "./history.js";
import { loadFiles } from "./files.js";
import { drag } from "./dnd.js";
import { hoverCard } from "./hovercard.js";
import { loadChecklist } from "./checklist.js";
import { keys, laneHidden, todoKey } from "./vim.js";
import { desktop } from "./viewport.js";

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
    if (res) parts.push(res.ahead ? `${res.ahead} ${res.ahead === 1 ? "commit" : "commits"} ahead of ${def}` : `nothing ${def} doesn't have yet`);
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
  if (state.project !== p) return;
  state.unpushed = 0;
  state.unpushedTodos = null;
  await Promise.all([loadUnpushed(), loadRemote(true), state.view === "history" && loadGraph(), state.view === "files" && loadFiles(), state.view === "checklist" && loadChecklist()]);
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

export function laneItems() {
  const out = Object.fromEntries(LANES.map((l) => [l.key, []]));
  for (const t of state.todos.values()) {
    if (!state.showOld && isOld(t)) continue;
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
  flipPlay(before);
  hoverCard.sync();
}

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
    $("#lane-chips").replaceChildren();
    $("#new-todo").hidden = true;
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
      art("img/empty-board.webp", 480, 360),
      h("p", { class: "empty-title" }, "Create your first project"),
      h("p", {}, "A project is a git repository. Every task runs in its own worktree, so several can run at once."),
      h("div", { class: "empty-actions" },
        ib({ class: "btn primary", onclick: () => openNewProject() }, "plus", "New project")));
    return;
  }
  empty.hidden = true;
  $("#new-todo").hidden = false;
  renderRunnerBanner();

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

  // Desktop: minimized columns leave the board and show as chips in the top bar.
  $("#lane-chips").replaceChildren(...LANES.filter((l) => state.collapsed.has(l.key) && !laneHidden(l.key)).map((l) => h("button", {
    type: "button", class: `lane-tab lane-chip s-${l.key === "done" ? "merged" : l.key}`, "data-lane": l.key,
    "data-count": items[l.key].length, title: `Expand ${l.label}`, onclick: () => toggleCollapsed(l.key),
  }, l.label, h("span", { class: "n" }, items[l.key].length))));

  $("#board").replaceChildren(...LANES.map((l, li) => h("section", {
    "data-lane": l.key, hidden: laneHidden(l.key),
    class: `col s-${l.key === "done" ? "merged" : l.key}${state.lane === l.key ? " active" : ""}${keys.active && keys.cur.lane === li ? " cursor" : ""}${state.collapsed.has(l.key) ? " collapsed" : ""}`, "aria-label": l.label,
  },
    h("header", { class: "col-head" },
      h("span", { class: "col-label" }, l.label), h("span", { class: "n" }, items[l.key].length),
      ib({ class: "icon-btn col-toggle", title: `Minimize ${l.label}`, onclick: () => toggleCollapsed(l.key) }, "minimize")),
    h("div", { class: "col-body" },
      l.key === "done" ? pushBar() : null,
      items[l.key].length
        ? h("ol", { class: "rail" }, items[l.key].map((t, ri) => h("li", {}, todoRow(t, li, ri))))
        : h("p", { class: "col-empty" }, l.empty),
      l.key === "draft" ? enqueueAllBtn(items.draft) : null,
      l.key === "done" ? oldToggle() : null,
      l.key === "done" ? clearDoneBtn() : null))));
  updateNeedsYou(items);

  $(".lane-tab[aria-selected=true]")?.scrollIntoView({ block: "nearest", inline: "nearest" });

  const review = items.review.length + state.perms.length;
  const badge = $("#review-badge");
  if (String(review) !== badge.textContent && review && !reduceMotion.matches) {
    badge.animate([{ transform: "scale(1.35)" }, { transform: "scale(1)" }], { duration: 260, easing: EASE });
  }
  badge.hidden = !review;
  badge.textContent = review;
  renderBoardDot(review);
}

// The Board button's dot when nothing waits on you: yellow while tasks run or
// merge, green once every task that ran this session has landed in Done.
// Waiting work wins: the review-coloured count above replaces the dot.
const boardRun = { project: null, ids: new Set() };
function renderBoardDot(waiting) {
  const pid = state.project?.id ?? null;
  if (boardRun.project !== pid) { boardRun.project = pid; boardRun.ids.clear(); }
  const active = [...state.todos.values()].filter((t) => t.status === "in_progress" || t.status === "merging");
  // A new run after everything settled starts a fresh round.
  if (active.length && !active.some((t) => boardRun.ids.has(t.id))) boardRun.ids.clear();
  for (const t of active) boardRun.ids.add(t.id);
  const tracked = [...boardRun.ids].map((id) => state.todos.get(id)).filter(Boolean);
  const done = !active.length && tracked.length > 0 && tracked.every((t) => t.status === "merged");
  const dot = $("#board-dot");
  dot.hidden = !!waiting || (!active.length && !done);
  dot.classList.toggle("done", !active.length && done);
}

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

$("#board").addEventListener("touchstart", (e) => {
  swipe = null;
  if (desktop.matches || e.touches.length !== 1 || drag.active) return;
  const t = e.touches[0];
  if (t.clientX < SWIPE.edge || t.clientX > innerWidth - SWIPE.edge) return;
  if (inSideScroller(e.target)) return;
  swipe = { x: t.clientX, y: t.clientY, dx: 0, dy: 0 };
}, { passive: true });

$("#board").addEventListener("touchmove", (e) => {
  if (!swipe) return;
  if (drag.active || e.touches.length !== 1) { swipe = null; return; }
  const t = e.touches[0];
  swipe.dx = t.clientX - swipe.x;
  swipe.dy = t.clientY - swipe.y;
}, { passive: true });

$("#board").addEventListener("touchcancel", () => { swipe = null; }, { passive: true });

$("#board").addEventListener("touchend", (e) => {
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
}, { passive: true });

function toggleCollapsed(key) {
  if (!state.collapsed.delete(key)) state.collapsed.add(key);
  try { localStorage.setItem("dash.collapsed", JSON.stringify([...state.collapsed])); } catch {}
  renderBoard();
}

function oldToggle() {
  const hidden = [...state.todos.values()].filter(isOld).length;
  if (!hidden && !state.showOld) return null;
  return ib({ class: "link older", onclick: () => { state.showOld = !state.showOld; renderBoard(); } }, state.showOld ? "up" : "down",
    state.showOld ? "Hide merged tasks older than a day" : `Show ${hidden} older merged ${hidden === 1 ? "task" : "tasks"}`);
}

// Queues every visible draft, in board order, so they keep their relative priority.
function enqueueAllBtn(drafts) {
  if (drafts.length < 2) return null;
  return ib({ class: "link older", title: "Move every draft in this column to the queue", onclick: attempt(() => enqueueAll(drafts)) },
    "queue", `Enqueue all ${drafts.length}`);
}

async function enqueueAll(drafts) {
  let bad = 0;
  for (const t of drafts) {
    try { await api("POST", `/api/todos/${t.id}/enqueue`); } catch { bad++; }
  }
  const n = drafts.length - bad;
  toast(bad ? `Queued ${n}, ${bad} failed` : `Queued ${n} ${n === 1 ? "task" : "tasks"}`);
}

// Failed tasks wait in Review to be retried, reworked or deleted, so Clear Done leaves them.
const doneTodos = () => [...state.todos.values()].filter((t) => t.status === "merged");

function clearDoneBtn() {
  if (!doneTodos().length) return null;
  return ib({ class: "link older", onclick: attempt(clearDone) }, "trash", "Clear Done");
}

async function clearDone() {
  const list = doneTodos();
  if (!list.length) return;
  const unpushed = list.filter((t) => t.status === "merged" && state.unpushedTodos?.has(t.id)).length;
  const n = list.length;
  if (!await ask({ title: unpushed ? `Not all done tasks are pushed — clear ${n} anyway?` : `Clear ${n} done ${n === 1 ? "task" : "tasks"}?`,
    body: "Deletes every merged task in this project. This can't be undone." +
      (unpushed ? ` ${unpushed} merged ${unpushed === 1 ? "task hasn't" : "tasks haven't"} been pushed yet. Their changes are not discarded: the commits stay on the local default branch and can still be pushed.` : ""),
    ok: "Clear", danger: true })) return;
  const res = await Promise.allSettled(list.map((t) => api("DELETE", `/api/todos/${t.id}`)));
  if (list.some((t) => t.id === state.detail)) closeDetail();
  const bad = res.filter((r) => r.status === "rejected").length;
  toast(bad ? `Cleared ${n - bad}, ${bad} failed` : `Cleared ${n} ${n === 1 ? "task" : "tasks"}`);
}

// "2m", "3h", "4d"
export function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso)) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < DAY / 1000) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

// "just now", "2m ago"
export const agoText = (iso) => { const a = ago(iso); return a === "now" ? "just now" : `${a} ago`; };

// A merged todo is pushed once origin has its commit (in "pr" mode, and on
// runners, merging is pushing).
function isPushed(t) {
  const p = state.project;
  if (t.status !== "merged" || !p?.remote_url) return false;
  if (landsOnGitHub(p)) return true;
  return !!state.unpushedTodos && !state.unpushedTodos.has(t.id);
}

// depWait tells what a queued todo is waiting for: its dependencies that
// aren't merged yet, and those of them that failed or are drafts, which
// hold it back until someone acts. Null when nothing holds it back.
function depWait(t) {
  if (t.status !== "queued" || !t.depends_on?.length) return null;
  const deps = t.depends_on.map((id) => state.todos.get(id)).filter((d) => d && d.status !== "merged");
  if (!deps.length) return null;
  return { deps, stuck: deps.filter((d) => d.status === "failed" || d.status === "draft") };
}

// A dependency's title that opens it.
function depLink(d) {
  return h("button", {
    type: "button", class: "dep-link", title: `Open “${title(d)}”`,
    onpointerdown: (e) => e.stopPropagation(),
    onclick: (e) => { e.stopPropagation(); openDetail(d.id); },
  }, title(d));
}

// "⏳ waiting for A, B +2", or "blocked: A failed" when a dependency is stuck.
function depLine({ deps, stuck }, max = 2) {
  const list = (xs, what) => [
    ...xs.slice(0, max).flatMap((d, i) => [i ? ", " : "", depLink(d), what ? ` ${what(d)}` : ""]),
    xs.length > max ? ` +${xs.length - max}` : "",
  ];
  if (stuck.length) return ["blocked: ", ...list(stuck, (d) => d.status === "draft" ? "is a draft" : "failed")];
  return ["⏳ waiting for ", ...list(deps)];
}

// The detail view's line about dependencies.
export function depMeta(t) {
  const wait = depWait(t);
  if (wait) return h("span", { class: `deps${wait.stuck.length ? " stuck" : ""}` }, depLine(wait, 5));
  const deps = (t.depends_on || []).map((id) => state.todos.get(id)).filter(Boolean);
  if (!deps.length) return null;
  return h("span", { class: "deps" }, "Depends on ", deps.flatMap((d, i) => [i ? ", " : "", depLink(d)]));
}

export function todoRow(t, li = -1, ri = -1) {
  const atCursor = keys.active && keys.cur.lane === li && keys.cur.row === ri;
  let sub = t.prompt.replace(/\s+/g, " ");
  let subClass = "todo-sub";
  if (t.summary && t.status === "merged") sub = t.summary;
  const fixing = t.sent_back && ["queued", "in_progress"].includes(t.status) ? t.sent_back : "";
  if (fixing && t.status === "queued") sub = `Sent back from merge: ${fixing}`;
  if (t.status === "in_progress") { sub = state.lastLine.get(t.id) || "Working…"; subClass += " progress"; }
  // Tests or setup running, in any state (a manual test run on a task in review, the merge queue's tests).
  const act = state.activity.get(t.id);
  if (act && t.status !== "draft") { sub = activityNodes(act); subClass = "todo-sub progress activity"; }
  if (t.paused) sub = t.status === "in_progress" ? "Pausing…" : "Paused";
  if (t.waiting && t.status === "queued" && !t.paused) { sub = t.waiting; subClass = "todo-sub waiting-runner"; }
  if (t.status === "merging" && t.pr?.state === "OPEN") { sub = prStatus(t.pr); subClass = `todo-sub pr-status ${prTone(sub)}`; }
  if (t.error) { sub = t.error; subClass += " err"; }
  if (t.reviewing) { sub = "Claude is reviewing…"; subClass = "todo-sub progress"; }
  if (state.perms.some((p) => p.todo_id === t.id)) { sub = "Waiting for your approval"; subClass = "todo-sub waiting"; }
  const wait = !t.paused && !t.error && depWait(t);
  if (wait) { sub = depLine(wait); subClass = `todo-sub deps${wait.stuck.length ? " stuck" : ""}`; }
  const checkTag = t.checks && ["review", "merging", "failed"].includes(t.status)
    ? h("span", { class: `check-tag ${t.checks.passed ? "pass" : "fail"}` }, t.checks.passed ? "Tests pass" : "Tests fail")
    : null;
  // The card is a container, not a button: its title is the button that opens
  // the task, and its actions (chips, PR link, dependency links) are sibling
  // controls, so each is focusable and announced on its own. A press anywhere
  // else on the card opens the task too.
  return h("div", {
    class: `todo s-${t.status}${isPushed(t) ? " pushed" : ""}${t.reviewing ? " reviewing" : ""}${state.detail === t.id ? " selected" : ""}${atCursor ? " cursor" : ""}`,
    "data-id": t.id,
    onclick: (e) => { if (!e.target.closest("a, button:not(.todo-open)")) openDetail(t.id); },
  },
    h("span", { class: "node", "aria-hidden": "true" }),
    h("span", { class: "todo-main" },
      h("button", { type: "button", class: "todo-open" }, h("span", { class: "todo-title" }, title(t))),
      h("span", { class: subClass }, sub)),
    h("span", { class: "todo-side" },
      checkTag,
      t.paused ? h("span", { class: "paused-tag" }, "Paused") : null,
      t.runner && ["in_progress", "review", "merging", "failed"].includes(t.status) ? h("span", { class: "runner-tag", title: "Runs on this runner" }, runnerName(t.runner)) : null,
      t.reviewing ? h("span", { class: "reviewing-tag" }, "Reviewing") : null,
      t.pr && t.status !== "merged" ? prTag(t.pr) : null,
      t.revert ? h("span", { class: "revert-tag" }, "Reverted") : null,
      t.ci_run ? h("span", { class: "ci-tag" }, "Build fix") : null,
      fixing ? h("span", { class: "sentback-tag", title: `Sent back from merge: ${fixing}` }, /conflict/.test(fixing) ? "Merge conflict" : "Failing checks") : null,
      t.attachments?.length ? h("span", { class: "attach-tag", title: `${t.attachments.length} attached` }, clip(), String(t.attachments.length)) : null,
      t.diff_files && ["review", "merging", "merged", "failed"].includes(t.status)
        ? h("span", { class: "diffsize", title: `${t.diff_files} ${t.diff_files === 1 ? "file" : "files"} changed` },
          h("span", { class: "plus" }, `+${t.diff_add || 0}`), " ", h("span", { class: "minus" }, `−${t.diff_del || 0}`))
        : null,
      t.status === "in_progress" && t.started_at ? h("span", { title: "Running for" }, ago(t.started_at)) : null,
      previewTag(t),
      t.status === "review" && t.finished_at ? h("span", { title: "Waiting for review" }, ago(t.finished_at)) : null,
      t.model ? h("span", { class: "model-tag", title: `Runs on ${t.model}` }, t.model) : null,
      hasPriority(t) ? h("span", { class: "prio-tag", title: `Priority ${t.priority} (lower runs first)` }, `P${t.priority}`) : null,
      t.status === "review" && !t.reviewing ? h("span", { class: "card-actions" }, canPreview(t) ? previewChip(t) : null, reviewChip(t), approveChip(t)) : null,
      t.status === "draft" ? h("span", { class: "card-actions" }, enqueueChip(t)) : null));
}

// What a merging task's pull request waits for, as the hub last saw it.
export function prStatus(pr) {
  return pr.status || `Waiting for GitHub on PR #${pr.number}…`;
}

export function prTone(line) {
  if (/^(Blocked|Checks? (failed|still)|Still conflicts)/.test(line)) return "err";
  if (/^Waiting for you/.test(line)) return "waiting";
  return "progress";
}

// One line per check, for a tooltip.
export function prChecksTitle(pr) {
  const mark = { passed: "✓", failed: "✗", pending: "…" };
  return (pr.checks || []).map((c) => `${mark[c.state] || "·"} ${c.name}`).join("\n");
}

// The card's "PR #N", opening the pull request.
function prTag(pr) {
  return h("a", {
    class: "pr-tag", href: pr.url, target: "_blank", rel: "noopener", title: `Open pull request #${pr.number} on GitHub`,
    onpointerdown: (e) => e.stopPropagation(), onclick: (e) => e.stopPropagation(),
  }, `PR #${pr.number}`);
}

function reviewChip(t) {
  return h("button", {
    type: "button", class: "review-chip", title: "Have Claude review the changes",
    onpointerdown: (e) => e.stopPropagation(),
    onclick: (e) => {
      e.stopPropagation();
      attempt(async () => { await openDetail(t.id); reviewTodo(); })();
    },
  }, ico("eye"), "Review");
}

// Opens the task with its live preview, starting the server if needed.
function previewChip(t) {
  const p = previewOf(t.id);
  return h("button", {
    type: "button", class: "preview-chip", title: "Open the live preview",
    onpointerdown: (e) => e.stopPropagation(),
    onclick: (e) => {
      e.stopPropagation();
      attempt(() => previewTodo(t))();
    },
  }, p ? h("span", { class: `pv-dot ${p.state}`, "aria-hidden": "true" }) : ico("window"), "Preview");
}

function approveChip(t) {
  return h("button", {
    type: "button", class: "approve-chip", title: "Approve and merge without opening the diff",
    onpointerdown: (e) => e.stopPropagation(),
    onclick: (e) => {
      e.stopPropagation();
      attempt(async () => { await api("POST", `/api/todos/${t.id}/approve`); toast("Approved — merging"); })();
    },
  }, ico("check"), "Approve");
}

function enqueueChip(t) {
  return h("button", {
    type: "button", class: "enqueue-chip", title: "Move to the queue",
    onpointerdown: (e) => e.stopPropagation(),
    onclick: (e) => {
      e.stopPropagation();
      attempt(todoKey)(t, "s");
    },
  }, ico("queue"), "Enqueue");
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
