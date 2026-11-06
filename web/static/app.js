// Dash web client. REST for state, /ws/events for live updates,
// /ws/chat for streaming answers, /ws/proc/:id for terminals.

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const h = (tag, props = {}, ...kids) => {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k in el && k !== "list") el[k] = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  el.append(...kids.flat().filter((k) => k != null && k !== false));
  return el;
};
const wsURL = (path) => `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}${path}`;
const desktop = matchMedia("(min-width: 1024px)");

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) {
    location.replace("/login.html");
    throw new Error("Sign in again to continue.");
  }
  const ct = res.headers.get("content-type") || "";
  const data = ct.includes("json") ? await res.json() : await res.text();
  if (!res.ok) throw new Error(data.error || data || res.statusText);
  return data;
}

// ------------------------------------------------------------------ toasts & dialogs
const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)");
const EASE = "cubic-bezier(.2, .8, .2, 1)";

// leave() plays an exit animation (CSS class) before running done.
function leave(el, cls, done, ms = 180) {
  if (reduceMotion.matches) return done();
  el.classList.add(cls);
  setTimeout(() => { el.classList.remove(cls); done(); }, ms);
}

function toast(msg, isError = false) {
  const t = h("div", { class: `toast${isError ? " error" : ""}`, role: isError ? "alert" : "status" }, msg);
  $("#toasts").append(t);
  setTimeout(() => leave(t, "leaving", () => t.remove()), isError ? 6000 : 3000);
}
const fail = (e) => toast(e.message || String(e), true);
const attempt = (fn) => async (...a) => { try { await fn(...a); } catch (e) { fail(e); } };

// Optional illustration; disappears if the image file isn't there.
function art(src, w, hgt) {
  const img = h("img", { class: "empty-art", src, alt: "", width: w, height: hgt, loading: "lazy", decoding: "async" });
  img.addEventListener("error", () => img.remove());
  return img;
}

async function copy(text, msg) {
  try {
    await navigator.clipboard.writeText(text);
    toast(msg);
  } catch {
    await ask({ title: "Copy this", body: text, ok: "Done" }); // clipboard needs https or localhost
  }
}

// ask() shows a confirmation or a text question as a sheet. Resolves to the
// entered text (or true), or null when cancelled.
function ask({ title, body = "", input = false, placeholder = "", ok = "OK", danger = false }) {
  const d = $("#ask-sheet");
  $("#ask-title").textContent = title;
  $("#ask-body").textContent = body;
  $("#ask-body").hidden = !body;
  const field = $("#ask-input");
  field.hidden = !input;
  field.value = "";
  field.placeholder = placeholder;
  const okBtn = $("#ask-ok");
  okBtn.textContent = ok;
  okBtn.className = `btn ${danger ? "danger" : "primary"}`;
  d.showModal();
  if (input) field.focus();
  return new Promise((resolve) => {
    d.addEventListener("close", () => {
      if (d.returnValue !== "ok") return resolve(null);
      resolve(input ? field.value.trim() : true);
    }, { once: true });
  });
}

// ------------------------------------------------------------------ state
const LANES = [
  { key: "draft", label: "Drafts", statuses: ["draft"], empty: "Drafts wait here until you queue them." },
  { key: "queued", label: "Queued", statuses: ["queued"], empty: "Nothing waiting. New todos start here." },
  { key: "in_progress", label: "Running", statuses: ["in_progress"], empty: "No agents working right now." },
  { key: "review", label: "Review", statuses: ["review"], empty: "Nothing to review. Finished work lands here." },
  { key: "merging", label: "Merging", statuses: ["merging"], empty: "The merge queue is clear." },
  { key: "done", label: "Done", statuses: ["merged", "failed"], empty: "Merged and failed todos show up here." },
];
const STATUS_LABEL = { draft: "Draft", queued: "Queued", in_progress: "Running", review: "Ready for review", merging: "Merging", merged: "Merged", failed: "Failed" };

const state = {
  projects: [],
  project: null,
  todos: new Map(),
  lastLine: new Map(),
  lane: null,          // mobile: selected lane key
  detail: null,        // open todo id
  pane: "diff",
  diff: "",
  procs: [],
  perms: [],           // pending permission requests for this project
  usage: null,
  view: "board",
  query: "",           // board search (/)
  showOld: false,      // show merged todos older than a day
  comments: loadComments(), // todo id → [{file, line, side, code, text}] (diff review notes)
};
const DAY = 24 * 3600 * 1000;

function loadComments() {
  try { return JSON.parse(localStorage.getItem("dash.comments") || "{}"); } catch { return {}; }
}
function saveComments() {
  try { localStorage.setItem("dash.comments", JSON.stringify(state.comments)); } catch {}
}

// ------------------------------------------------------------------ navigation
function showView(name) {
  if (name !== state.view && document.startViewTransition && !reduceMotion.matches) {
    document.startViewTransition(() => applyView(name));
    return;
  }
  applyView(name);
}

function applyView(name) {
  state.view = name;
  $$(".nav > button").forEach((b) => {
    const on = b.dataset.view === name;
    b.classList.toggle("active", on);
    b.toggleAttribute("aria-current", on);
    if (on) b.setAttribute("aria-current", "page");
  });
  $$(".view").forEach((v) => v.classList.toggle("active", v.id === `view-${name}`));
  if (name === "run") requestAnimationFrame(fitTerm);
  if (name === "chat") $("#chat-log").scrollTop = $("#chat-log").scrollHeight;
}
$$(".nav > button[data-view]").forEach((b) => b.addEventListener("click", () => showView(b.dataset.view)));

// ------------------------------------------------------------------ live events
function connectEvents() {
  const ws = new WebSocket(wsURL("/ws/events"));
  ws.onopen = () => {
    setLive(true);
    loadProjects().catch(fail);
  };
  ws.onclose = async () => {
    setLive(false);
    try { await api("GET", "/api/me"); } catch { return; }
    setTimeout(connectEvents, 1500);
  };
  ws.onmessage = (m) => onEvent(JSON.parse(m.data));
}
function setLive(on) {
  $("#live").classList.toggle("on", on);
  $("#live").title = on ? "Live updates connected" : "Live updates disconnected — reconnecting";
  $("#live-label").textContent = on ? "Live" : "Offline";
}

function onEvent(ev) {
  const mine = state.project && ev.project_id === state.project.id;
  switch (ev.type) {
    case "project.updated":
      loadProjects().catch(fail);
      break;
    case "todo.updated":
      if (!mine) break;
      {
        const prev = state.todos.get(ev.todo_id);
        state.todos.set(ev.todo_id, ev.data);
        renderBoard();
        if (state.detail === ev.todo_id) {
          renderDetail();
          if (prev?.status !== ev.data.status && ev.data.worktree) loadDiff();
        }
        if (prev && prev.status !== ev.data.status) {
          if (ev.data.status === "review") { toast(`Ready for review: ${title(ev.data)}`); notify("Ready for review", ev.data); }
          if (ev.data.status === "failed") notify("Failed", ev.data, ev.data.error);
          if (ev.data.status === "merged") notify("Merged", ev.data);
        }
      }
      break;
    case "todo.deleted":
      if (!mine) break;
      state.todos.delete(ev.todo_id);
      if (state.detail === ev.todo_id) closeDetail();
      renderBoard();
      break;
    case "todo.output":
      state.lastLine.set(ev.todo_id, String(ev.data).split("\n").pop());
      if (mine) updateLiveLine(ev.todo_id);
      if (state.detail === ev.todo_id) {
        const log = $("#d-log");
        const stick = log.parentElement.scrollTop + log.parentElement.clientHeight >= log.parentElement.scrollHeight - 40;
        log.textContent += `${new Date(ev.time).toLocaleTimeString()} ${ev.data}\n`;
        if (stick && state.pane === "log") log.parentElement.scrollTop = log.parentElement.scrollHeight;
      }
      break;
    case "git.op":
      if (mine || !ev.project_id) addActivity(ev);
      break;
    case "merge.queue":
      $("#merge-status").textContent = ev.data.pending ? `${ev.data.pending} in merge queue` : "";
      break;
    case "process.updated":
      if (mine) loadProcs().catch(fail);
      break;
    case "permission.updated":
      if (!mine) break;
      state.perms = state.perms.filter((p) => p.id !== ev.data.id);
      if (!ev.data.decision) {
        state.perms.push(ev.data);
        const t = state.todos.get(ev.data.todo_id);
        toast(`Claude needs your approval: ${describePerm(ev.data).title}${t ? ` (${title(t)})` : ""}`);
        if (t) notify(`Claude wants to ${describePerm(ev.data).title}`, t, describePerm(ev.data).detail);
      }
      renderPerms();
      break;
    case "usage.updated":
      state.usage = ev.data;
      renderUsage();
      break;
  }
}

// ------------------------------------------------------------------ projects
async function loadProjects() {
  state.projects = await api("GET", "/api/projects");
  const saved = (() => { try { return localStorage.getItem("dash.project"); } catch { return null; } })();
  const want = state.project?.id || saved;
  const next = state.projects.find((p) => p.id === want) || state.projects[0] || null;
  if (next?.id !== state.project?.id) await selectProject(next);
  else { state.project = next; renderProjectHeader(); }
  renderProjectList();
}

async function selectProject(p) {
  state.project = p;
  state.todos = new Map();
  state.lane = null;
  closeDetail();
  try { if (p) localStorage.setItem("dash.project", p.id); } catch {}
  renderProjectHeader();
  renderProjectList();
  if (!p) { state.perms = []; renderBoard(); renderProcs(); renderPerms(); return; }
  await Promise.all([loadTodos(), loadProcs(), loadPerms()]);
}

async function loadPerms() {
  if (!state.project) return;
  state.perms = await api("GET", `/api/permissions?project=${state.project.id}`);
  renderPerms();
}

function renderProjectHeader() {
  const p = state.project;
  $("#project-name").textContent = p ? p.name : "No project";
  $("#project-branch").textContent = p ? p.default_branch : "";
}

function renderProjectList() {
  $("#project-list").replaceChildren(...state.projects.map((p) => h("li", {},
    h("button", {
      type: "button", "aria-current": state.project?.id === p.id ? "true" : "false",
      onclick: attempt(async () => { await selectProject(p); $("#project-sheet").close(); }),
    }, h("strong", {}, p.name), h("span", { class: "path" }, p.path)))));

  const p = state.project;
  $("#project-tools").replaceChildren(...(p ? [
    h("span", { class: "remote" }, p.remote_url ? `Remote: ${p.remote_url}` : "No GitHub remote yet."),
    p.remote_url
      ? h("button", { type: "button", class: "btn", onclick: attempt(async () => { await api("POST", `/api/projects/${p.id}/push`); toast(`Pushed ${p.default_branch}`); }) }, `Push ${p.default_branch}`)
      : h("button", { type: "button", class: "btn", onclick: attempt(createGitHubRepo) }, "Create GitHub repository"),
    h("button", { type: "button", class: "btn", onclick: openSettings }, "Settings"),
  ] : []),
  h("button", { type: "button", class: "btn", onclick: attempt(toggleNotifications) }, notifyOn() ? "Turn off notifications" : "Turn on notifications"),
  h("button", { type: "button", class: "link logout", onclick: logout }, "Log out"));
}

async function createGitHubRepo() {
  const p = state.project;
  const name = await ask({ title: "Create GitHub repository", body: "Creates a private repository with gh and pushes the default branch.", input: true, placeholder: p.name, ok: "Create" });
  if (name === null) return;
  await api("POST", `/api/projects/${p.id}/github`, { name: name || p.name, private: true });
  toast("GitHub repository created");
}

$("#project-switch").addEventListener("click", () => { renderProjectList(); $("#project-sheet").showModal(); });
$("#new-project").addEventListener("submit", attempt(async (e) => {
  e.preventDefault();
  const body = Object.fromEntries(new FormData(e.target));
  const p = await api("POST", "/api/projects", body);
  e.target.reset();
  e.target.closest("details").open = false;
  state.projects = await api("GET", "/api/projects");
  await selectProject(state.projects.find((x) => x.id === p.id));
  $("#project-sheet").close();
  toast(`Created ${p.name}`);
}));

// ------------------------------------------------------------------ board
async function loadTodos() {
  if (!state.project) return;
  const list = await api("GET", `/api/projects/${state.project.id}/todos`);
  state.todos = new Map(list.map((t) => [t.id, t]));
  renderBoard();
  if (state.detail) renderDetail();
}

const title = (t) => t.title || t.prompt.split("\n")[0];
const byPriority = (a, b) => a.priority - b.priority || a.created_at.localeCompare(b.created_at);

const matches = (t, q) => !q || `${t.title} ${t.prompt}`.toLowerCase().includes(q.toLowerCase());
const isOld = (t) => t.status === "merged" && Date.now() - new Date(t.finished_at || t.updated_at) > DAY;

function laneItems() {
  const out = Object.fromEntries(LANES.map((l) => [l.key, []]));
  for (const t of state.todos.values()) {
    if (!matches(t, state.query)) continue;
    if (!state.showOld && isOld(t)) continue;
    const lane = LANES.find((l) => l.statuses.includes(t.status));
    if (lane) out[lane.key].push(t);
  }
  for (const k in out) {
    out[k].sort(k === "done" ? (a, b) => b.updated_at.localeCompare(a.updated_at) : byPriority);
  }
  return out;
}

function renderBoard() {
  if (drag.active) return; // don't rebuild under the user's finger; we re-render on drop
  const before = flipFirst();
  renderBoardNow();
  flipPlay(before);
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
  if (!state.project) {
    $("#board").replaceChildren();
    $("#lane-tabs").replaceChildren();
    empty.hidden = false;
    empty.replaceChildren(
      // Optional illustration: save as web/static/img/empty-board.webp, 960x720 (4:3). Hidden if missing.
      // Convert: magick in.png -resize 960x720^ -gravity center -extent 960x720 -quality 85 web/static/img/empty-board.webp
      // Files are embedded in the binary: rebuild and restart dash afterwards.
      art("img/empty-board.webp", 480, 360),
      h("p", { class: "empty-title" }, "Create your first project"),
      h("p", {}, "A project is a git repository. Every todo runs in its own worktree, so several can run at once."),
      h("button", { class: "btn primary", onclick: () => { $("#project-sheet").showModal(); $(".new-project").open = true; } }, "New project"));
    $("#new-todo").hidden = true;
    return;
  }
  empty.hidden = true;
  $("#new-todo").hidden = false;

  // Mobile opens on whatever needs attention first.
  if (!state.lane) {
    state.lane = ["review", "in_progress", "queued", "draft"].find((k) => items[k].length) || "queued";
  }

  $("#lane-tabs").replaceChildren(...LANES.map((l) => h("button", {
    role: "tab", class: `lane-tab s-${l.key === "done" ? "merged" : l.key}`, "data-lane": l.key,
    "aria-selected": String(state.lane === l.key), "data-count": items[l.key].length,
    onclick: () => { state.lane = l.key; renderBoard(); },
  }, l.label, h("span", { class: "n" }, items[l.key].length))));

  $("#board").replaceChildren(...LANES.map((l, li) => h("section", {
    "data-lane": l.key,
    class: `col s-${l.key === "done" ? "merged" : l.key}${state.lane === l.key ? " active" : ""}`, "aria-label": l.label,
  },
    h("header", { class: "col-head" }, l.label, h("span", { class: "n" }, items[l.key].length)),
    h("div", { class: "col-body" },
      items[l.key].length
        ? h("ol", { class: "rail" }, items[l.key].map((t, ri) => h("li", {}, todoRow(t, li, ri))))
        : h("p", { class: "col-empty" }, state.query ? "No matches." : l.empty),
      l.key === "done" ? oldToggle() : null))));
  syncSearchBar();
  updateNeedsYou(items);

  $(".lane-tab[aria-selected=true]")?.scrollIntoView({ block: "nearest", inline: "nearest" });

  const review = items.review.length + state.perms.length;
  const badge = $("#review-badge");
  if (String(review) !== badge.textContent && review && !reduceMotion.matches) {
    badge.animate([{ transform: "scale(1.35)" }, { transform: "scale(1)" }], { duration: 260, easing: EASE });
  }
  badge.hidden = !review;
  badge.textContent = review;
}

function oldToggle() {
  const hidden = [...state.todos.values()].filter((t) => isOld(t) && matches(t, state.query)).length;
  if (!hidden && !state.showOld) return null;
  return h("button", { class: "link older", onclick: () => { state.showOld = !state.showOld; renderBoard(); } },
    state.showOld ? "Hide merged todos older than a day" : `Show ${hidden} older merged ${hidden === 1 ? "todo" : "todos"}`);
}

// "2m", "3h", "4d"
function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso)) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < DAY / 1000) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

function todoRow(t, li = -1, ri = -1) {
  const atCursor = keys.active && keys.cur.lane === li && keys.cur.row === ri;
  let sub = t.prompt.replace(/\s+/g, " ");
  let subClass = "todo-sub";
  if (t.status === "in_progress") { sub = state.lastLine.get(t.id) || "Working…"; subClass += " progress"; }
  if (t.error) { sub = t.error; subClass += " err"; }
  if (state.perms.some((p) => p.todo_id === t.id)) { sub = "Waiting for your approval"; subClass = "todo-sub waiting"; }
  const checkTag = t.checks && ["review", "merging", "failed"].includes(t.status)
    ? h("span", { class: `check-tag ${t.checks.passed ? "pass" : "fail"}` }, t.checks.passed ? "Tests pass" : "Tests fail")
    : null;
  return h("button", {
    class: `todo s-${t.status}${t.goal ? " goal" : ""}${state.detail === t.id ? " selected" : ""}${atCursor ? " cursor" : ""}`,
    "data-id": t.id, onclick: () => openDetail(t.id),
  },
    h("span", { class: "node", "aria-hidden": "true" }),
    h("span", { class: "todo-main" }, h("span", { class: "todo-title" }, title(t)), h("span", { class: subClass }, sub)),
    h("span", { class: "todo-side" },
      checkTag,
      t.goal ? h("span", { class: "goal-tag" }, "Milestone") : null,
      t.diff_files && ["review", "merging", "merged", "failed"].includes(t.status)
        ? h("span", { class: "diffsize", title: `${t.diff_files} ${t.diff_files === 1 ? "file" : "files"} changed` },
          h("span", { class: "plus" }, `+${t.diff_add || 0}`), " ", h("span", { class: "minus" }, `−${t.diff_del || 0}`))
        : null,
      t.status === "in_progress" && t.started_at ? h("span", { title: "Running for" }, ago(t.started_at)) : null,
      t.status === "review" && t.finished_at ? h("span", { title: "Waiting for review" }, ago(t.finished_at)) : null,
      t.priority ? h("span", {}, `P${t.priority}`) : null));
}

function updateLiveLine(id) {
  const el = $(`.todo[data-id="${id}"] .todo-sub.progress`);
  if (el) el.textContent = state.lastLine.get(id);
}

// ------------------------------------------------------------------ new / edit todo
let editing = null;
function openTodoForm(t = null) {
  editing = t;
  const f = $("#todo-form");
  f.reset();
  $("#todo-sheet-title").textContent = t ? "Edit todo" : "New todo";
  if (t) {
    f.title.value = t.title;
    f.prompt.value = t.prompt;
    f.goal.checked = t.goal;
    f.priority.value = t.priority;
  }
  $("#todo-sheet").showModal();
  (t ? f.prompt : f.title).focus();
}
$("#new-todo").addEventListener("click", () => openTodoForm());
$("#todo-sheet").addEventListener("close", attempt(async () => {
  const action = $("#todo-sheet").returnValue;
  if (action !== "draft" && action !== "queue") return;
  const f = $("#todo-form");
  const body = { title: f.title.value.trim(), prompt: f.prompt.value.trim(), goal: f.goal.checked, priority: Number(f.priority.value) || 0 };
  if (!body.prompt) throw new Error("Add instructions for Claude before saving.");
  if (editing) {
    await api("PATCH", `/api/todos/${editing.id}`, body);
    if (action === "queue" && editing.status !== "queued") await api("POST", `/api/todos/${editing.id}/enqueue`);
    toast(action === "queue" ? "Saved and queued" : "Saved");
  } else {
    await api("POST", `/api/projects/${state.project.id}/todos`, { ...body, enqueue: action === "queue" });
    toast(action === "queue" ? "Added to queue" : "Saved as draft");
    state.lane = action === "queue" ? "queued" : "draft";
  }
}));
// Native form validation would block "Save as draft" too; we validate on close instead.
$("#todo-form").noValidate = true;

// ------------------------------------------------------------------ detail
async function openDetail(id) {
  state.detail = id;
  const t = state.todos.get(id);
  state.pane = t?.worktree ? "diff" : "log";
  state.diff = "";
  $("#detail").hidden = false;
  $("#scrim").hidden = !desktop.matches;
  renderDetail();
  $$(".todo.selected").forEach((e) => e.classList.remove("selected"));
  $(`.todo[data-id="${id}"]`)?.classList.add("selected");
  $("#d-log").textContent = "";
  await Promise.all([loadLog(), loadDiff()]).catch(fail);
}

function closeDetail() {
  const wasOpen = state.detail && !$("#detail").hidden;
  state.detail = null;
  const hide = () => { if (!state.detail) { $("#detail").hidden = true; $("#scrim").hidden = true; } };
  if (wasOpen) leave($("#detail"), "closing", hide); else hide();
  $$(".todo.selected").forEach((e) => e.classList.remove("selected"));
}
$("#d-close").addEventListener("click", closeDetail);
$("#scrim").addEventListener("click", closeDetail);
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && state.detail && !document.querySelector("dialog[open]")) closeDetail();
});

async function loadLog() {
  const id = state.detail;
  const text = await api("GET", `/api/todos/${id}/log`);
  if (state.detail === id) $("#d-log").textContent = text || "No output yet.";
}
async function loadDiff() {
  const id = state.detail;
  const t = state.todos.get(id);
  if (!t?.worktree) { state.diff = ""; renderPane(); return; }
  const text = await api("GET", `/api/todos/${id}/diff`).catch((e) => `error: ${e.message}`);
  if (state.detail !== id) return;
  state.diff = text;
  renderPane();
}

function renderDetail() {
  const t = state.todos.get(state.detail);
  if (!t) return;
  $("#d-title").textContent = title(t);
  $("#d-meta").replaceChildren(...[
    h("span", { class: `status s-${t.status}` }, STATUS_LABEL[t.status] || t.status),
    t.goal ? h("span", { class: "goal-tag" }, "Milestone") : null,
    t.worktree ? h("span", { class: "branch" }, t.worktree.branch) : null,
    t.priority ? h("span", {}, `Priority ${t.priority}`) : null,
    t.runs ? h("span", {}, `${t.runs} ${t.runs === 1 ? "run" : "runs"}${t.cost_usd ? ` · ≈$${t.cost_usd.toFixed(2)}` : ""}`) : null,
    t.auto_fixes ? h("span", {}, `Fixed automatically ${t.auto_fixes}×`) : null,
  ].filter(Boolean));
  renderChecks(t);
  renderPerms();
  $("#d-prompt").replaceChildren(t.prompt,
    ...(t.feedback?.length ? [h("ol", { class: "feedback-list" }, t.feedback.map((f) => h("li", {}, f)))] : []));
  $("#d-prompt-wrap").open = !t.worktree;
  $("#d-error").hidden = !t.error;
  $("#d-error").textContent = t.error || "";
  renderActions(t);
  renderPane();
}

function renderActions(t) {
  const post = (verb, body, msg) => attempt(async () => { await api("POST", `/api/todos/${t.id}/${verb}`, body); toast(msg); });
  const b = (label, onclick, cls = "") => h("button", { class: `btn ${cls}`, onclick }, label);
  const rework = attempt(async () => {
    const feedback = await ask({ title: "Send back for rework", body: "Claude continues in the same worktree with your notes.", input: true, placeholder: "What should change?", ok: "Send back" });
    if (feedback === null) return;
    await api("POST", `/api/todos/${t.id}/rework`, { feedback });
    toast("Sent back for rework");
  });
  const btns = [];
  switch (t.status) {
    case "draft":
      btns.push(b("Edit", () => openTodoForm(t)), b("Add to queue", post("enqueue", null, "Added to queue"), "primary grow"));
      break;
    case "queued":
      btns.push(b("Edit", () => openTodoForm(t)), b("Move to drafts", post("cancel", null, "Moved to drafts"), "grow"));
      break;
    case "in_progress":
      btns.push(b("Stop", post("cancel", null, "Stopping"), "danger grow"));
      break;
    case "review":
      if (commentsFor(t.id).length) {
        btns.push(b(`Send ${commentsFor(t.id).length} ${commentsFor(t.id).length === 1 ? "comment" : "comments"}`, attempt(() => sendComments(t)), "primary"));
      } else {
        btns.push(b("Rework", rework));
      }
      btns.push(b("Approve and merge", post("approve", null, "Approved — merging"), "review grow"));
      break;
    case "failed":
      btns.push(t.worktree ? b("Rework", rework) : b("Edit", () => openTodoForm(t)), b("Retry", post("enqueue", null, "Queued again"), "primary grow"));
      break;
  }
  $("#d-actions").replaceChildren(...btns);
}

$("#d-more").addEventListener("click", attempt(async () => {
  const t = state.todos.get(state.detail);
  if (!t) return;
  const choices = [];
  if (t.worktree && ["review", "failed"].includes(t.status)) {
    choices.push(["Try it: start server in this worktree", () => startProc({ kind: "web", preset: "run", todo_id: t.id })]);
    choices.push(["Open shell in this worktree", () => startProc({ kind: "pty", todo_id: t.id })]);
  }
  if (!["in_progress", "merging"].includes(t.status)) {
    choices.push([t.goal ? "Unmark milestone" : "Mark as milestone", () => api("PATCH", `/api/todos/${t.id}`, { goal: !t.goal })]);
    if (t.worktree && state.project?.test_command) choices.push(["Run the tests again", () => api("POST", `/api/todos/${t.id}/test`).then(() => toast("Running tests…"))]);
    choices.push(["Duplicate as a new draft", () => api("POST", `/api/todos/${t.id}/duplicate`).then(() => toast("Duplicated as a draft"))]);
    if (t.worktree) {
      choices.push(["Open worktree in editor", () => api("POST", `/api/todos/${t.id}/open`).then(() => toast("Opening in your editor"))]);
      choices.push([`Copy branch name (${t.worktree.branch})`, () => copy(t.worktree.branch, "Branch name copied")]);
      choices.push(["Copy worktree path", () => copy(t.worktree.path, "Path copied")]);
    }
    choices.push(["Delete todo", async () => {
      if (await ask({ title: "Delete this todo?", body: "Its worktree and branch are removed too. This can't be undone.", ok: "Delete", danger: true })) {
        await api("DELETE", `/api/todos/${t.id}`);
        closeDetail();
        toast("Todo deleted");
      }
    }]);
  }
  if (!choices.length) return;
  await menu(choices);
}));

// A small action sheet built on the ask dialog.
function menu(choices) {
  const d = $("#ask-sheet");
  $("#ask-title").textContent = "Actions";
  $("#ask-body").hidden = true;
  $("#ask-input").hidden = true;
  $("#ask-ok").hidden = true;
  const list = h("div", { class: "project-list" }, choices.map(([label, fn]) => h("button", {
    type: "button", class: "btn", style: "width:100%;margin-bottom:8px",
    onclick: attempt(async () => { d.close(); await fn(); }),
  }, label)));
  $("#ask-body").after(list);
  d.showModal();
  return new Promise((r) => d.addEventListener("close", () => { list.remove(); $("#ask-ok").hidden = false; r(); }, { once: true }));
}

$$(".segmented button").forEach((b) => b.addEventListener("click", () => { state.pane = b.dataset.pane; renderPane(); }));

function renderPane() {
  $$(".segmented button").forEach((b) => b.setAttribute("aria-selected", String(b.dataset.pane === state.pane)));
  $("#d-diff").hidden = state.pane !== "diff";
  $("#d-log").hidden = state.pane !== "log";
  renderDiff(state.diff);
}

function renderDiff(text) {
  const files = [];
  let cur = null;
  for (const line of text.split("\n")) {
    if (line.startsWith("diff --git")) {
      cur = { path: line.replace(/^diff --git a\/(.*) b\/.*$/, "$1"), lines: [], add: 0, del: 0 };
      files.push(cur);
    } else if (cur) {
      if (/^(index |new file|deleted file|similarity|rename |old mode|new mode|--- |\+\+\+ )/.test(line)) continue;
      if (line.startsWith("+")) cur.add++;
      else if (line.startsWith("-")) cur.del++;
      cur.lines.push(line);
    }
  }
  const add = files.reduce((n, f) => n + f.add, 0);
  const del = files.reduce((n, f) => n + f.del, 0);
  $("#d-diffstat").replaceChildren(...(files.length ? [h("span", { class: "plus" }, `+${add}`), " ", h("span", { class: "minus" }, `−${del}`)] : []));

  if (!files.length) {
    const t = state.todos.get(state.detail);
    const msg = text.startsWith("error:") ? text : t?.worktree ? "No changes compared to the default branch." : "Changes appear here once Claude has worked on this todo.";
    $("#d-diff").replaceChildren(h("p", { class: "pane-empty" }, msg));
    return;
  }
  $("#d-diff").replaceChildren(...files.map((f) => h("details", { class: "diff-file", open: files.length <= 8 },
    h("summary", {}, h("span", { class: "path" }, f.path), h("span", { class: "counts" }, h("span", { class: "plus" }, `+${f.add}`), " ", h("span", { class: "minus" }, `−${f.del}`))),
    h("div", { class: "diff-lines" }, diffLines(f)))));
}

// Rows for one file, numbered like the new file (removed lines use old numbers),
// with saved review comments under their lines.
function diffLines(f) {
  const comments = commentsFor(state.detail).filter((c) => c.file === f.path);
  const rows = [];
  let oldLn = 0, newLn = 0;
  for (const l of f.lines) {
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)/.exec(l);
    if (hunk) {
      oldLn = Number(hunk[1]); newLn = Number(hunk[2]);
      rows.push(h("div", { class: "hunk" }, l));
      continue;
    }
    if (l.startsWith("\\")) { rows.push(h("div", { class: "meta" }, l)); continue; }
    const side = l.startsWith("-") ? "old" : "new";
    const ln = side === "old" ? oldLn++ : newLn++;
    if (l.startsWith(" ") || l === "") oldLn++;
    rows.push(h("div", { class: l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : "", "data-ln": ln, "data-side": side, "data-file": f.path, title: "Click to comment" }, l || " "));
    for (const c of comments.filter((c) => c.line === ln && c.side === side)) {
      rows.push(h("div", { class: "comment" },
        h("span", {}, c.text),
        h("button", { class: "link", type: "button", "aria-label": "Delete comment", onclick: () => {
          state.comments[state.detail] = commentsFor(state.detail).filter((x) => x !== c);
          saveComments(); renderPane(); renderActions(state.todos.get(state.detail));
        } }, "Delete")));
    }
  }
  return rows;
}

// ------------------------------------------------------------------ notifications & tab title
const notifyOn = () => { try { return localStorage.getItem("dash.notify") === "1"; } catch { return false; } };
function notify(what, t, detail = "") {
  if (!notifyOn() || !("Notification" in window) || Notification.permission !== "granted") return;
  if (document.visibilityState === "visible" && document.hasFocus()) return; // the page already shows it
  const n = new Notification(`${what}: ${title(t)}`, { body: detail || "", tag: `dash-${t.id}`, silent: false });
  n.onclick = () => { window.focus(); showView("board"); openDetail(t.id); n.close(); };
}
async function toggleNotifications() {
  if (!("Notification" in window)) throw new Error("This browser doesn't support notifications.");
  if (notifyOn()) {
    try { localStorage.setItem("dash.notify", "0"); } catch {}
    toast("Notifications off");
  } else {
    const perm = await Notification.requestPermission();
    if (perm !== "granted") throw new Error("Notifications are blocked for this site in your browser settings.");
    try { localStorage.setItem("dash.notify", "1"); } catch {}
    toast("Notifications on: you'll hear about reviews, failures and approvals while Dash is in the background");
  }
  renderProjectList();
}
// Tab title shows how many things wait for you: reviews + permission requests.
function updateNeedsYou(items) {
  const n = (items?.review?.length || 0) + state.perms.length;
  document.title = n ? `(${n}) Dash` : "Dash";
}

// ------------------------------------------------------------------ search (/)
function openSearch() {
  showView("board");
  $("#search-bar").hidden = false;
  const input = $("#search-input");
  input.focus();
  input.select();
}
function syncSearchBar() {
  const bar = $("#search-bar");
  if (state.query && bar.hidden) bar.hidden = false;
  if (document.activeElement !== $("#search-input")) $("#search-input").value = state.query;
  const n = Object.values(laneItems()).reduce((a, l) => a + l.length, 0);
  $("#search-count").textContent = state.query ? `${n} ${n === 1 ? "match" : "matches"}` : "";
}
function clearSearch() {
  state.query = "";
  $("#search-input").value = "";
  $("#search-bar").hidden = true;
  renderBoard();
}
$("#search-input").addEventListener("input", (e) => { state.query = e.target.value; keys.cur.row = 0; renderBoard(); });
$("#search-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); e.target.blur(); jumpMatch(1, true); }
  if (e.key === "Escape") { e.preventDefault(); clearSearch(); e.target.blur(); }
});
$("#search-clear").addEventListener("click", clearSearch);
$("#search-open").addEventListener("click", openSearch);

// n / N: next/previous match in lane order
function jumpMatch(dir, fromStart = false) {
  const flat = [];
  LANES.forEach((l, li) => laneTodos(li).forEach((t, ri) => flat.push([li, ri])));
  if (!flat.length) return toast("No matches");
  let i = fromStart ? 0 : flat.findIndex(([li, ri]) => li === keys.cur.lane && ri === keys.cur.row);
  if (!fromStart) i = (i + dir + flat.length) % flat.length;
  moveCursor(...flat[Math.max(0, i)]);
}

// ------------------------------------------------------------------ diff comments
function commentsFor(id) { return state.comments[id] || []; }

$("#d-diff").addEventListener("click", (e) => {
  const line = e.target.closest(".diff-lines > div[data-ln]");
  const t = state.todos.get(state.detail);
  if (!line || !t || !["review", "failed"].includes(t.status)) return;
  if (line.nextElementSibling?.classList.contains("comment-box")) return;
  const box = h("form", { class: "comment-box" },
    h("textarea", { rows: 3, placeholder: "What should Claude change here?", "aria-label": "Comment on this line" }),
    h("div", { class: "comment-actions" },
      h("button", { type: "button", class: "btn", onclick: () => box.remove() }, "Cancel"),
      h("button", { class: "btn primary" }, "Add comment")));
  box.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const text = box.querySelector("textarea").value.trim();
    if (!text) return box.remove();
    state.comments[t.id] = [...commentsFor(t.id), { file: line.dataset.file, line: Number(line.dataset.ln), side: line.dataset.side, code: line.textContent, text }];
    saveComments();
    renderPane();
    renderActions(t);
  });
  box.querySelector("textarea").addEventListener("keydown", (ev) => {
    if (ev.ctrlKey && ev.key === "Enter") box.requestSubmit();
    if (ev.key === "Escape") { ev.stopPropagation(); box.remove(); }
  });
  line.after(box);
  box.querySelector("textarea").focus();
});

function commentFeedback(list, extra) {
  const parts = list.map((c) => `### ${c.file}, ${c.side === "old" ? "removed line" : "line"} ${c.line}\n> ${c.code}\n\n${c.text}`);
  return `Review comments on your diff:\n\n${parts.join("\n\n")}${extra ? `\n\nAlso: ${extra}` : ""}`;
}

async function sendComments(t) {
  const list = commentsFor(t.id);
  const extra = await ask({ title: `Send ${list.length} ${list.length === 1 ? "comment" : "comments"} to Claude`, body: "Claude continues in the same worktree and addresses each comment.", input: true, placeholder: "Anything else? (optional)", ok: "Send for rework" });
  if (extra === null) return;
  await api("POST", `/api/todos/${t.id}/rework`, { feedback: commentFeedback(list, extra) });
  delete state.comments[t.id];
  saveComments();
  toast("Comments sent for rework");
}

// ------------------------------------------------------------------ tests (checks)
function renderChecks(t) {
  const box = $("#d-checks");
  const c = t.checks;
  box.hidden = !c;
  if (!c) return;
  const st = $("#d-checks-state");
  st.className = `checks-state ${c.passed ? "pass" : "fail"}`;
  st.textContent = c.passed ? "Tests pass" : `Tests fail (exit ${c.exit_code})`;
  const when = c.stage === "merge" ? `after merging ${state.project?.default_branch || "main"}` : "before review";
  $("#d-checks-meta").textContent = `${when} · ${Math.round(c.duration_s)}s`;
  $("#d-checks-output").textContent = `$ ${c.command}\n\n${c.output || "No output."}`;
  if (!c.passed && box.dataset.todo !== t.id) box.open = true;
  box.dataset.todo = t.id;
}

// ------------------------------------------------------------------ permission requests
function describePerm(p) {
  const i = p.input || {};
  switch (p.tool) {
    case "Bash": return { title: "run a command", detail: i.command, note: i.description };
    case "Write": case "Edit": case "MultiEdit": case "NotebookEdit": return { title: `edit ${i.file_path || i.notebook_path || "a file"}`, detail: i.file_path };
    case "WebFetch": return { title: "fetch a web page", detail: i.url };
    case "WebSearch": return { title: "search the web", detail: i.query };
    default: return { title: `use ${p.tool}`, detail: JSON.stringify(i, null, 2) };
  }
}

function permCard(p, withTodo) {
  const d = describePerm(p);
  const t = state.todos.get(p.todo_id);
  const decide = (decision) => attempt(async () => {
    await api("POST", `/api/permissions/${p.id}`, { decision });
    toast(decision === "deny" ? "Denied" : decision === "always" ? `${p.tool} allowed for this todo` : "Allowed");
  });
  return h("div", { class: "perm", role: "group", "aria-label": `Permission request: ${d.title}` },
    h("p", { class: "perm-q" }, `Claude wants to ${d.title}`,
      withTodo && t ? h("span", { class: "todo-ref" }, ` · ${title(t)}`) : null),
    d.note ? h("p", { class: "hint", style: "margin:0" }, d.note) : null,
    d.detail ? h("code", { class: "perm-cmd" }, d.detail) : null,
    h("div", { class: "perm-actions" },
      h("button", { class: "btn allow", onclick: decide("allow") }, "Allow"),
      h("button", { class: "btn", onclick: decide("always") }, `Always allow ${p.tool}`),
      h("button", { class: "btn danger", onclick: decide("deny") }, "Deny"),
      withTodo && t ? h("button", { class: "link", onclick: () => openDetail(t.id) }, "Open todo") : null));
}

function renderPerms() {
  const banner = $("#perm-banner");
  banner.hidden = !state.perms.length;
  banner.replaceChildren(...state.perms.map((p) => permCard(p, true)));
  const mine = state.perms.filter((p) => p.todo_id === state.detail);
  $("#d-perms").hidden = !mine.length;
  $("#d-perms").replaceChildren(...mine.map((p) => permCard(p, false)));
  renderBoard();
}

// ------------------------------------------------------------------ usage & queue
async function loadUsage() {
  state.usage = await api("GET", "/api/usage");
  renderUsage();
}

const windowLabel = (n) => ({ five_hour: "5-hour window", seven_day: "Weekly", seven_day_opus: "Weekly (Opus)" })[n] || n;
const shortLabel = (n) => ({ five_hour: "5h", seven_day: "7d", seven_day_opus: "7d Opus" })[n] || n;
const fmtTime = (iso) => {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : d.toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
};

function renderUsage() {
  const u = state.usage;
  const pill = $("#usage-pill");
  if (!u || (!u.known && !u.manual_pause)) { pill.hidden = true; return; }
  pill.hidden = false;
  const live = (u.windows || []).filter((w) => new Date(w.resets_at) > new Date());
  const top = live.reduce((a, w) => (!a || w.utilization > a.utilization ? w : a), null);
  pill.className = `usage-pill${u.paused ? " paused" : top && top.utilization >= 0.7 ? " warn" : ""}`;
  pill.textContent = u.paused ? "Queue paused" : top ? `${shortLabel(top.name)} ${Math.round(top.utilization * 100)}%` : "Usage";
  pill.title = u.paused ? u.reason : "Claude subscription usage";

  $("#usage-reason").textContent = u.paused
    ? `${u.reason}. No new todos start${u.until && !u.manual_pause ? ` until ${fmtTime(u.until)}` : ""}; running ones finish.`
    : "Todos start normally.";
  $("#usage-meters").replaceChildren(...live.map((w) => {
    const pct = Math.round(w.utilization * 100);
    const cls = w.utilization >= (u.pause_at || 1) ? "full" : w.utilization >= 0.7 ? "warn" : "";
    return h("div", {},
      h("div", { class: "meter-head" }, h("span", {}, windowLabel(w.name)), h("span", {}, `${pct}% · resets ${fmtTime(w.resets_at)}`)),
      h("div", { class: `meter ${cls}`, role: "meter", "aria-valuenow": pct, "aria-valuemin": 0, "aria-valuemax": 100, "aria-label": windowLabel(w.name) },
        h("span", { style: `width:${Math.min(100, pct)}%` }),
        u.pause_at > 0 && u.pause_at < 1 ? h("i", { class: "mark", style: `left:${u.pause_at * 100}%`, title: "Queue pauses here" }) : null));
  }));
  $("#usage-note").textContent = u.pause_at > 0 && u.pause_at < 1
    ? `The queue pauses automatically at ${Math.round(u.pause_at * 100)}% of any window.`
    : "The queue pauses automatically when a window's limit is reached.";
  $("#queue-toggle").textContent = u.manual_pause ? "Resume queue" : "Pause queue";
  $("#queue-toggle").className = `btn ${u.manual_pause ? "primary" : ""}`;
}

$("#usage-pill").addEventListener("click", () => { renderUsage(); $("#usage-sheet").showModal(); });
$("#queue-toggle").addEventListener("click", attempt(async () => {
  state.usage = await api("POST", state.usage?.manual_pause ? "/api/queue/resume" : "/api/queue/pause");
  renderUsage();
  toast(state.usage.manual_pause ? "Queue paused" : "Queue resumed");
}));

// ------------------------------------------------------------------ project settings
function openSettings() {
  const p = state.project;
  const f = $("#settings-form");
  f.test_command.value = p.test_command || "";
  f.max_auto_fixes.value = p.max_auto_fixes ?? 2;
  f.setup_command.value = p.setup_command || "";
  f.copy_files.value = (p.copy_files || []).join("\n");
  f.run_command.value = p.run_command || "";
  f.deploy_command.value = p.deploy_command || "";
  f.auto_push.checked = !!p.auto_push;
  $("#project-sheet").close();
  $("#settings-sheet").showModal();
}
$("#settings-sheet").addEventListener("close", attempt(async () => {
  if ($("#settings-sheet").returnValue !== "save") return;
  const f = $("#settings-form");
  state.project = await api("PATCH", `/api/projects/${state.project.id}`, {
    test_command: f.test_command.value.trim(),
    max_auto_fixes: Math.max(0, Number(f.max_auto_fixes.value) || 0),
    setup_command: f.setup_command.value.trim(),
    copy_files: f.copy_files.value.split("\n").map((x) => x.trim()).filter(Boolean),
    run_command: f.run_command.value.trim(),
    deploy_command: f.deploy_command.value.trim(),
    auto_push: f.auto_push.checked,
  });
  toast("Settings saved");
}));

// ------------------------------------------------------------------ chat
const chat = { history: [], current: null, ws: null };

function chatSocket() {
  if (chat.ws && chat.ws.readyState <= 1) return chat.ws;
  chat.ws = new WebSocket(wsURL("/ws/chat"));
  chat.ws.onmessage = (m) => {
    const d = JSON.parse(m.data);
    const c = chat.current;
    if (!c) return;
    if (d.kind === "thinking") c.thinking.textContent += d.text;
    else if (d.kind === "text") { c.reply.textContent += d.text; c.content += d.text; }
    else if (d.kind === "error") { c.el.classList.add("error"); c.reply.textContent += (c.content ? "\n\n" : "") + d.text; }
    if (d.kind === "done" || d.kind === "error") {
      c.el.classList.remove("pending");
      if (c.content) {
        const answer = c.content;
        c.el.append(h("div", { class: "msg-actions" },
          h("button", { class: "link", onclick: () => { if (!state.project) return toast("Select a project first", true); openTodoForm(); $("#todo-form").prompt.value = answer; } }, "Make this a todo"),
          h("button", { class: "link", onclick: () => copy(answer, "Copied") }, "Copy")));
      }
      if (c.content) chat.history.push({ role: "assistant", content: c.content });
      else chat.history.pop(); // keep history alternating
      chat.current = null;
      $("#chat-send").disabled = false;
    }
    const log = $("#chat-log");
    if (log.scrollHeight - log.scrollTop - log.clientHeight < 120) log.scrollTop = log.scrollHeight;
  };
  chat.ws.onclose = () => {
    if (chat.current) {
      chat.current.el.classList.remove("pending");
      chat.current.reply.textContent ||= "Connection lost. Send your message again.";
      chat.current = null;
      chat.history.pop();
      $("#chat-send").disabled = false;
    }
  };
  return chat.ws;
}

function sendChat(q) {
  $("#chat-empty").hidden = true;
  chat.history.push({ role: "user", content: q });
  const thinking = h("div", { class: "thinking" });
  const reply = h("div", { class: "reply" });
  const el = h("div", { class: "msg assistant pending" }, thinking, reply);
  $("#chat-log").append(h("div", { class: "msg user" }, q), el);
  $("#chat-log").scrollTop = $("#chat-log").scrollHeight;
  chat.current = { el, thinking, reply, content: "" };
  $("#chat-send").disabled = true;
  const ws = chatSocket();
  const go = () => ws.send(JSON.stringify({ type: "send", messages: chat.history }));
  ws.readyState === 1 ? go() : ws.addEventListener("open", go, { once: true });
}

const chatInput = $("#chat-form").q;
const autoGrow = () => { chatInput.style.height = "auto"; chatInput.style.height = `${chatInput.scrollHeight}px`; };
chatInput.addEventListener("input", autoGrow);
$("#chat-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const q = chatInput.value.trim();
  if (!q || chat.current) return;
  chatInput.value = "";
  autoGrow();
  sendChat(q);
});
chatInput.addEventListener("keydown", (e) => {
  // Enter sends on devices with a hardware keyboard; on touch keyboards Enter adds a line.
  if (e.key === "Enter" && !e.shiftKey && !matchMedia("(pointer: coarse)").matches) {
    e.preventDefault();
    $("#chat-form").requestSubmit();
  }
});

// ------------------------------------------------------------------ run: processes & terminal
const term = new window.Terminal({
  fontFamily: '"JetBrains Mono", ui-monospace, monospace', fontSize: 13, cursorBlink: true,
  theme: { background: "#0a0f17", foreground: "#e6ecf5", cursor: "#8aa4ff", selectionBackground: "#2a384d" },
});
const fit = new window.FitAddon.FitAddon();
term.loadAddon(fit);
term.open($("#term"));
let termWS = null;
let attached = null;

const sendSize = () => termWS?.readyState === 1 && termWS.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
function fitTerm() { if (!$("#term-wrap").hidden) { try { fit.fit(); sendSize(); } catch {} } }
new ResizeObserver(fitTerm).observe($("#term"));
term.onData((data) => termWS?.readyState === 1 && termWS.send(JSON.stringify({ type: "input", data })));
// Ctrl-] leaves the terminal and returns to normal mode, like detaching in the TUI.
term.attachCustomKeyEventHandler((e) => {
  if (e.type === "keydown" && e.ctrlKey && e.key === "]") { term.blur(); setMode(); return false; }
  return true;
});

function attach(p) {
  termWS?.close();
  attached = p.id;
  $("#term-wrap").hidden = false;
  $("#term-title").textContent = p.command;
  term.reset();
  requestAnimationFrame(fitTerm);
  termWS = new WebSocket(wsURL(`/ws/proc/${p.id}`));
  termWS.binaryType = "arraybuffer";
  termWS.onopen = sendSize;
  termWS.onmessage = (m) => {
    if (typeof m.data === "string") {
      const msg = JSON.parse(m.data);
      if (msg.type === "exit") term.write(`\r\n\x1b[90mProcess exited with code ${msg.code}\x1b[0m\r\n`);
    } else {
      term.write(new Uint8Array(m.data));
    }
  };
  renderProcs();
  if (p.kind === "pty") term.focus();
}
$("#term-close").addEventListener("click", () => { termWS?.close(); attached = null; $("#term-wrap").hidden = true; renderProcs(); });

async function startProc(req) {
  const p = await api("POST", `/api/projects/${state.project.id}/processes`, { ...req, cols: term.cols, rows: term.rows });
  closeDetail();
  showView("run");
  attach(p);
  if (p.url) toast(`Server starting at ${p.url}`);
}

$(".run-actions").addEventListener("click", attempt(async (e) => {
  const spec = e.target.closest("[data-run]")?.dataset.run;
  if (!spec || !state.project) return;
  const [kind, preset] = spec.split(":");
  await startProc({ kind, preset });
}));
$("#run-custom").addEventListener("submit", attempt(async (e) => {
  e.preventDefault();
  const command = e.target.cmd.value.trim();
  if (!command || !state.project) return;
  await startProc({ kind: "pty", command });
  e.target.reset();
}));

async function loadProcs() {
  if (!state.project) return;
  state.procs = await api("GET", `/api/projects/${state.project.id}/processes`);
  renderProcs();
}

function renderProcs() {
  const running = state.procs.filter((p) => p.running).length;
  $("#proc-badge").hidden = !running;
  $("#proc-badge").textContent = running;
  const kindLabel = { web: "Server", task: "Task", pty: "Terminal" };
  keys.proc = Math.min(keys.proc, Math.max(0, state.procs.length - 1));
  $("#procs").replaceChildren(...state.procs.map((p, i) => h("li", { class: `proc${p.id === attached ? " attached" : ""}${keys.active && state.view === "run" && i === keys.proc ? " cursor" : ""}` },
    h("span", { class: "proc-cmd" }, p.command),
    h("span", { class: "proc-actions" },
      h("button", { class: "btn", onclick: () => attach(p) }, p.id === attached ? "Viewing" : "View"),
      p.running ? h("button", { class: "btn danger", onclick: attempt(async () => { await api("DELETE", `/api/processes/${p.id}`); toast("Stopping"); }) }, "Stop") : null),
    h("span", { class: "proc-meta" },
      h("span", { class: `status proc-state${p.running ? " running" : ""}` }, p.running ? "Running" : `Exited ${p.exit_code ?? ""}`),
      h("span", {}, kindLabel[p.kind] || p.kind),
      p.url ? h("a", { href: p.url, target: "_blank", rel: "noopener" }, p.url) : null))));
}

// ------------------------------------------------------------------ activity
function addActivity(ev) {
  const d = ev.data;
  $("#activity-empty").hidden = true;
  $("#activity").prepend(h("li", {},
    h("span", { class: d.ok ? "ok" : "bad", "aria-label": d.ok ? "succeeded" : "failed" }, d.ok ? "✓" : "✗"),
    h("code", {}, d.cmd),
    h("time", {}, `${new Date(ev.time).toLocaleTimeString()} · ${d.ms} ms`)));
  while ($("#activity").children.length > 300) $("#activity").lastChild.remove();
}

// ------------------------------------------------------------------ drag & drop
// Drag a card to another lane to act on it, or within a lane to reorder.
// Mouse: drag after a few pixels. Touch: long-press, then drag (a quick
// swipe still scrolls). Lane tabs are drop targets on phones.
const drag = { active: false, pending: null, t: null, card: null, ghost: null, slot: null, target: null, dx: 0, dy: 0, suppressClick: false };

// What dropping a todo of a status into a lane does (null = not allowed).
function dropAction(t, lane) {
  const from = t.status;
  const to = LANES.find((l) => l.key === lane);
  if (!to) return null;
  if (to.statuses.includes(from)) return ["draft", "queued", "review"].includes(lane) ? { kind: "reorder" } : null;
  const go = (verb, label) => ({ kind: "action", verb, label });
  if (lane === "queued" && ["draft", "failed"].includes(from)) return go("enqueue", "Added to queue");
  if (lane === "queued" && from === "review") return { kind: "rework" };
  if (lane === "draft" && ["queued", "review", "failed"].includes(from)) return go("cancel", "Moved to drafts");
  if (lane === "merging" && from === "review") return go("approve", "Approved — merging");
  return null;
}

$("#board").addEventListener("pointerdown", (e) => {
  const card = e.target.closest(".todo[data-id]");
  if (!card || e.button > 0) return;
  const t = state.todos.get(card.dataset.id);
  if (!t) return;
  drag.pending = { card, t, x: e.clientX, y: e.clientY, touch: e.pointerType !== "mouse" };
  if (drag.pending.touch) {
    drag.pending.timer = setTimeout(() => drag.pending && startDrag(drag.pending.x, drag.pending.y), 320);
  }
});

document.addEventListener("pointermove", (e) => {
  if (drag.active) { moveDrag(e.clientX, e.clientY); return; }
  const p = drag.pending;
  if (!p) return;
  const dist = Math.hypot(e.clientX - p.x, e.clientY - p.y);
  if (p.touch) { if (dist > 10) cancelPending(); return; } // it's a scroll
  if (dist > 6) startDrag(e.clientX, e.clientY);
});
// While dragging on touch, stop the page from scrolling under the finger.
document.addEventListener("touchmove", (e) => { if (drag.active) e.preventDefault(); }, { passive: false });
document.addEventListener("pointerup", (e) => { if (drag.active) endDrag(e.clientX, e.clientY); cancelPending(); });
document.addEventListener("pointercancel", () => { if (drag.active) abortDrag(); cancelPending(); });
document.addEventListener("keydown", (e) => { if (drag.active && e.key === "Escape") { e.stopPropagation(); abortDrag(); } }, true);
// A drag ends with a click on the card; swallow it so the detail doesn't open.
document.addEventListener("click", (e) => { if (drag.suppressClick) { e.stopPropagation(); e.preventDefault(); drag.suppressClick = false; } }, true);
$("#board").addEventListener("contextmenu", (e) => { if (e.target.closest(".todo")) e.preventDefault(); });

function cancelPending() {
  if (drag.pending?.timer) clearTimeout(drag.pending.timer);
  drag.pending = null;
}

function startDrag(x, y) {
  const { card, t } = drag.pending;
  cancelPending();
  const r = card.getBoundingClientRect();
  drag.active = true;
  drag.t = t;
  drag.card = card;
  drag.dx = x - r.left;
  drag.dy = y - r.top;
  drag.ghost = card.cloneNode(true);
  drag.ghost.classList.add("drag-ghost");
  drag.ghost.style.width = `${r.width}px`;
  document.body.append(drag.ghost);
  drag.slot = h("li", { class: "drop-slot", "aria-hidden": "true" });
  drag.slot.style.height = `${r.height}px`;
  card.classList.add("dragging");
  document.body.classList.add("is-dragging");
  navigator.vibrate?.(12);
  markTargets();
  showDropBar();
  moveDrag(x, y);
}

// On phones the other lanes are off-screen, so a drag shows a bar of large
// drop targets for exactly the moves this card allows.
const DROP_LABEL = { enqueue: "Add to queue", cancel: "Back to drafts", approve: "Approve and merge" };
function showDropBar() {
  if (desktop.matches) return;
  const zones = LANES.map((l) => [l, dropAction(drag.t, l.key)])
    .filter(([l, a]) => a && a.kind !== "reorder")
    .map(([l, a]) => h("div", { class: `drop-zone s-${l.key === "done" ? "merged" : l.key}`, "data-lane": l.key },
      a.kind === "rework" ? "Send back for rework" : DROP_LABEL[a.verb] || l.label));
  const bar = h("div", { class: "drop-bar", id: "drop-bar" },
    zones.length ? zones : h("div", { class: "drop-zone none" }, "Drag up or down to reorder"));
  document.body.append(bar);
}

// Highlight lanes (columns and mobile tabs) where this card may be dropped.
function markTargets() {
  for (const el of $$(".col[data-lane], .lane-tab[data-lane]")) {
    const ok = !!dropAction(drag.t, el.dataset.lane);
    el.classList.toggle("drop-ok", ok);
    el.classList.toggle("drop-no", !ok);
  }
}

function moveDrag(x, y) {
  drag.ghost.style.transform = `translate(${x - drag.dx}px, ${y - drag.dy}px) rotate(1.2deg) scale(1.02)`;
  const under = document.elementFromPoint(x, y);
  const laneEl = under?.closest(".drop-zone[data-lane], .col[data-lane], .lane-tab[data-lane]");
  const lane = laneEl?.dataset.lane || null;
  const action = lane ? dropAction(drag.t, lane) : null;
  $$(".drop-over").forEach((el) => el !== laneEl && el.classList.remove("drop-over"));
  drag.target = action ? { lane, action, index: Infinity } : null;
  if (!action) { drag.slot.remove(); return; }
  laneEl.classList.add("drop-over");

  // Inside a visible column, show where the card would land.
  const col = laneEl.classList.contains("col") ? laneEl : null;
  const rail = col?.querySelector(".rail") || null;
  if (!col || laneEl.matches(".col:not(.active)") && !desktop.matches) { drag.slot.remove(); return; }
  const cards = [...col.querySelectorAll(".todo[data-id]")].filter((c) => c !== drag.card);
  let index = cards.findIndex((c) => { const r = c.getBoundingClientRect(); return y < r.top + r.height / 2; });
  if (index < 0) index = cards.length;
  drag.target.index = index;
  if (rail) {
    const ref = cards[index]?.closest("li") || null;
    if (drag.slot.nextSibling !== ref || drag.slot.parentNode !== rail) rail.insertBefore(drag.slot, ref);
  } else {
    col.querySelector(".col-body")?.prepend(drag.slot);
  }
  autoScroll(col.querySelector(".col-body") || document.scrollingElement, y);
}

function autoScroll(el, y) {
  const r = el === document.scrollingElement ? { top: 0, bottom: innerHeight } : el.getBoundingClientRect();
  if (y < r.top + 60) el.scrollBy({ top: -14 });
  else if (y > r.bottom - 60) el.scrollBy({ top: 14 });
}

function cleanupDrag() {
  drag.ghost?.remove();
  drag.slot?.remove();
  const bar = $("#drop-bar");
  if (bar) leave(bar, "leaving", () => bar.remove(), 160);
  drag.card?.classList.remove("dragging");
  document.body.classList.remove("is-dragging");
  $$(".drop-ok, .drop-no, .drop-over").forEach((el) => el.classList.remove("drop-ok", "drop-no", "drop-over"));
  drag.active = false;
  drag.suppressClick = true;
  setTimeout(() => { drag.suppressClick = false; }, 0);
}

function abortDrag() {
  // Fly the ghost back to where the card was.
  const g = drag.ghost, home = drag.card.getBoundingClientRect();
  if (g && !reduceMotion.matches) {
    g.animate([{ transform: g.style.transform }, { transform: `translate(${home.left}px, ${home.top}px)` }], { duration: 200, easing: EASE });
    setTimeout(() => { cleanupDrag(); renderBoard(); }, 190);
  } else { cleanupDrag(); renderBoard(); }
}

async function endDrag() {
  const target = drag.target, t = drag.t;
  if (!target) return abortDrag();
  cleanupDrag();
  try {
    if (target.action.kind === "reorder") {
      await reorder(t, target.lane, target.index);
    } else if (target.action.kind === "rework") {
      renderBoard();
      const feedback = await ask({ title: "Send back for rework", body: "Claude continues in the same worktree with your notes.", input: true, placeholder: "What should change?", ok: "Send back" });
      if (feedback === null) return;
      await api("POST", `/api/todos/${t.id}/rework`, { feedback });
      toast("Sent back for rework");
    } else {
      // Optimistic: show it in the new lane right away; the server event confirms.
      const lane = LANES.find((l) => l.key === target.lane);
      state.todos.set(t.id, { ...t, status: lane.statuses[0] });
      renderBoard();
      await api("POST", `/api/todos/${t.id}/${target.action.verb}`);
      toast(target.action.label);
      if (target.lane === "queued" && Number.isFinite(target.index)) await reorder(state.todos.get(t.id), "queued", target.index);
    }
  } catch (err) {
    state.todos.set(t.id, t); // undo the optimistic move
    renderBoard();
    fail(err);
  }
}

// Reorder a lane by rewriting priorities 0..n-1 (lower runs first).
async function reorder(t, laneKey, index) {
  const lane = laneItems()[laneKey].filter((x) => x.id !== t.id);
  lane.splice(Math.min(index, lane.length), 0, t);
  const changes = lane.map((x, i) => [x, i]).filter(([x, i]) => x.priority !== i);
  for (const [x, i] of changes) state.todos.set(x.id, { ...state.todos.get(x.id), priority: i });
  renderBoard();
  await Promise.all(changes.map(([x, i]) => api("PATCH", `/api/todos/${x.id}`, { priority: i })));
}

// ------------------------------------------------------------------ vim keys
// Normal mode is the default; typing in a field or the terminal is insert
// mode. Esc (Ctrl-] in the terminal) returns to normal mode.
const VIEWS = ["board", "chat", "run", "activity"];
const keys = { active: false, count: 0, pending: "", cur: { lane: 0, row: 0 }, proc: 0 };

const BOUND = /^(:|\?|gt|gT|gg|G|p|q|Escape|y|Y|A|n|N|j|k|h|l|w|b|o|O|s|x|a|r|e|E|\+|-|K|J|\*|t|T|!|dd|i|I|R|D|\/|Enter| |Arrow(Up|Down|Left|Right)|ctrl\+[dufb])$/;
const isTyping = (el) => el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.closest?.(".xterm"));
const openDialog = () => document.querySelector("dialog[open]");

function setMode() {
  const el = $("#kbd-status");
  const typing = isTyping(document.activeElement);
  el.dataset.mode = typing ? "insert" : "normal";
  el.textContent = (typing ? "INSERT" : "NORMAL") + (keys.count || keys.pending ? `  ${keys.count || ""}${keys.pending}` : "");
}
document.addEventListener("focusin", setMode);
document.addEventListener("focusout", () => setTimeout(setMode));
// Mouse or touch hides the keyboard cursor; a key brings it back.
document.addEventListener("pointerdown", () => { if (keys.active) { keys.active = false; renderBoard(); } });

function laneCount() { return LANES.length; }
function laneTodos(li) { return laneItems()[LANES[li].key] || []; }
function cursorTodo() { return laneTodos(keys.cur.lane)[keys.cur.row] || null; }

function moveCursor(lane, row) {
  lane = Math.max(0, Math.min(laneCount() - 1, lane));
  const n = laneTodos(lane).length;
  keys.cur = { lane, row: Math.max(0, Math.min(n - 1, row)) };
  keys.active = true;
  if (!desktop.matches) state.lane = LANES[lane].key; // mobile shows one lane
  renderBoard();
  $(".todo.cursor")?.scrollIntoView({ block: "nearest", inline: "nearest" });
}

// The element j/k and Ctrl-d/u scroll in the current context.
function scroller() {
  if (state.detail) return $(".detail-body");
  if (state.view === "chat") return $("#chat-log");
  return desktop.matches ? $(".view.active") : document.scrollingElement;
}

function scrollBy(el, how, n) {
  const page = el.clientHeight || innerHeight;
  const delta = { j: 48, k: -48, "ctrl+d": page / 2, "ctrl+u": -page / 2, "ctrl+f": page * 0.9, "ctrl+b": -page * 0.9 }[how];
  if (how === "gg") el.scrollTo({ top: 0 });
  else if (how === "G") el.scrollTo({ top: el.scrollHeight });
  else el.scrollBy({ top: delta * n });
}

// Actions on a todo; shared by the board cursor and the open detail view.
async function todoKey(t, k) {
  const post = (verb, body, msg) => api("POST", `/api/todos/${t.id}/${verb}`, body).then(() => toast(msg));
  switch (k) {
    case "s": return post("enqueue", null, "Added to queue");
    case "x": return post("cancel", null, t.status === "in_progress" ? "Stopping" : "Moved to drafts");
    case "a": return post("approve", null, "Approved — merging");
    case "r": {
      const feedback = await ask({ title: "Send back for rework", body: "Claude continues in the same worktree with your notes.", input: true, placeholder: "What should change?", ok: "Send back" });
      return feedback === null ? null : post("rework", { feedback }, "Sent back for rework");
    }
    case "e": return openTodoForm(t);
    case "+": case "K": return api("PATCH", `/api/todos/${t.id}`, { priority: t.priority - 1 });
    case "-": case "J": return api("PATCH", `/api/todos/${t.id}`, { priority: t.priority + 1 });
    case "*": return api("PATCH", `/api/todos/${t.id}`, { goal: !t.goal });
    case "t": return startProc({ kind: "web", preset: "run", todo_id: t.id });
    case "!": return startProc({ kind: "pty", todo_id: t.id });
    case "dd":
      if (await ask({ title: `Delete "${title(t)}"?`, body: "Its worktree and branch are removed too. This can't be undone.", ok: "Delete", danger: true })) {
        await api("DELETE", `/api/todos/${t.id}`);
        if (state.detail === t.id) closeDetail();
        toast("Todo deleted");
      }
      return;
  }
  if (k === "T") return api("POST", `/api/todos/${t.id}/test`).then(() => toast("Running tests…"));
  if (k === "Y") return api("POST", `/api/todos/${t.id}/duplicate`).then(() => toast("Duplicated as a draft"));
  if (k === "E") return api("POST", `/api/todos/${t.id}/open`).then(() => toast("Opening in your editor"));
  return false; // not a todo key
}

// y / A / n answer the permission request in view: the open todo's, else the oldest.
async function permKey(k) {
  const p = state.perms.find((x) => x.todo_id === state.detail) || (!state.detail && state.perms[0]);
  if (!p) return false;
  const decision = { y: "allow", A: "always", n: "deny" }[k];
  await api("POST", `/api/permissions/${p.id}`, { decision });
  toast(decision === "deny" ? "Denied" : decision === "always" ? `${p.tool} allowed for this todo` : "Allowed");
}

async function runCommand(line) {
  line = line.trim().replace(/^:/, "");
  if (!line) return;
  if (/^\d+$/.test(line)) { moveCursor(keys.cur.lane, Number(line) - 1); return; }
  const [name, ...rest] = line.split(/\s+/);
  const arg = rest.join(" ");
  const need = () => { if (!state.project) throw new Error("Select a project first (p)."); return state.project; };
  switch (name) {
    case "q": case "q!": case "close": closeDetail(); openDialog()?.close(); return;
    case "b": case "board": return showView("board");
    case "c": case "chat": return showView("chat");
    case "run": case "!":
      if (!arg) return showView("run");
      need(); return startProc({ kind: "pty", command: arg });
    case "ps": case "procs": return showView("run");
    case "activity": case "log": return showView("activity");
    case "p": case "project": {
      if (!arg) { renderProjectList(); return $("#project-sheet").showModal(); }
      const p = state.projects.find((x) => x.name.toLowerCase().startsWith(arg.toLowerCase()));
      if (!p) throw new Error(`No project matching "${arg}".`);
      return selectProject(p);
    }
    case "np": case "newproject": {
      const [pname, path = ""] = arg.split(/\s+/, 2);
      if (!pname) throw new Error("Usage: :newproject <name> [path]");
      const p = await api("POST", "/api/projects", { name: pname, path });
      state.projects = await api("GET", "/api/projects");
      await selectProject(state.projects.find((x) => x.id === p.id));
      return toast(`Created ${p.name}`);
    }
    case "new": case "o":
      need(); openTodoForm(); if (arg) $("#todo-form").prompt.value = arg; return;
    case "todo":
      if (!arg) throw new Error("Usage: :todo <instructions>");
      await api("POST", `/api/projects/${need().id}/todos`, { prompt: arg, enqueue: true });
      return toast("Added to queue");
    case "push":
      await api("POST", `/api/projects/${need().id}/push`);
      return toast(`Pushed ${state.project.default_branch}`);
    case "pause": case "resume":
      state.usage = await api("POST", `/api/queue/${name}`);
      renderUsage();
      return toast(name === "pause" ? "Queue paused" : "Queue resumed");
    case "usage": renderUsage(); return $("#usage-sheet").showModal();
    case "settings": need(); return openSettings();
    case "perms": case "permissions":
      if (!state.perms.length) return toast("No permission requests waiting");
      showView("board"); closeDetail();
      return $("#perm-banner").scrollIntoView({ block: "nearest" });
    case "noh": case "nohlsearch": return clearSearch();
    case "notify": case "notifications": return toggleNotifications();
    case "clear": $("#chat-clear")?.click(); chat.history = []; $("#chat-log").replaceChildren($("#chat-empty")); $("#chat-empty").hidden = false; return;
    case "help": case "h": return $("#keys-sheet").showModal();
    case "logout": return logout();
    case "e": case "e!": case "refresh": return loadProjects();
  }
  throw new Error(`Not a command: ${name}. Press ? for help.`);
}

function openCommandLine() {
  const d = $("#cmd-sheet");
  const input = $("#cmd-input");
  input.value = "";
  d.showModal();
  input.focus();
}
$("#cmd-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const line = $("#cmd-input").value;
  $("#cmd-sheet").close();
  attempt(runCommand)(line);
});

document.addEventListener("keydown", (e) => {
  if (e.metaKey || e.altKey || e.isComposing) return;
  const target = e.target;

  // Insert mode: only Esc (leave) is ours; Ctrl-] is handled by the terminal.
  if (isTyping(target)) {
    if (e.key === "Escape" && !target.closest("dialog")) { target.blur(); setMode(); e.preventDefault(); }
    return;
  }
  if (openDialog()) return; // dialogs handle their own keys (Esc closes)

  let k = e.key;
  if (e.ctrlKey) {
    if (!["d", "u", "f", "b"].includes(k)) return;
    k = `ctrl+${k}`;
  }
  if (k === "Shift" || k === "Control") return;
  // Keyboard users activating a focused control keep the browser's behaviour.
  if ((k === "Enter" || k === " ") && target.closest?.("button, a, summary, input, [role=tab]")) return;

  // Counts: 5j, 10G …
  if (/^[0-9]$/.test(k) && !(k === "0" && !keys.count) && !keys.pending) {
    keys.count = keys.count * 10 + Number(k);
    setMode();
    return;
  }
  const n = Math.max(1, keys.count);
  const hadCount = keys.count > 0;
  keys.count = 0;
  if (keys.pending) {
    k = keys.pending + k;
    keys.pending = "";
  } else if (["g", "d"].includes(k)) {
    keys.pending = k;
    setMode();
    return;
  }
  setMode();
  if (!BOUND.test(k)) return; // leave Tab, browser shortcuts etc. alone
  e.preventDefault();
  attempt(handleNormal)(k, n, hadCount);
});

async function handleNormal(k, n, hadCount) {
  // Global keys.
  switch (k) {
    case ":": return openCommandLine();
    case "?": return $("#keys-sheet").showModal();
    case "gt": return showView(VIEWS[(VIEWS.indexOf(state.view) + n) % VIEWS.length]);
    case "gT": return showView(VIEWS[(VIEWS.indexOf(state.view) - n + VIEWS.length * n) % VIEWS.length]);
    case "p": renderProjectList(); return $("#project-sheet").showModal();
    case "Escape": case "q":
      if (state.detail) return closeDetail();
      if (k === "Escape" && state.query) return clearSearch();
      return;
    case "y": case "A":
      if (await permKey(k) === false) toast("No permission request waiting");
      return;
    case "n": case "N":
      // n answers "deny" only when a permission request is in view; otherwise next/previous search match.
      if (k === "n" && state.perms.length && (state.detail ? state.perms.some((p) => p.todo_id === state.detail) : !state.query)) return permKey("n");
      if (state.view === "board" && !state.detail) return jumpMatch(k === "n" ? 1 : -1);
      return;
    case "/":
      if (state.view === "board" && !state.detail) return openSearch();
      break;
  }

  // Detail view.
  if (state.detail) {
    const t = state.todos.get(state.detail);
    if (["j", "k", "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "gg", "G"].includes(k)) return scrollBy(scroller(), k, n);
    if (["h", "l"].includes(k)) { state.pane = state.pane === "diff" ? "log" : "diff"; return renderPane(); }
    if (t) await todoKey(t, k);
    return;
  }

  switch (state.view) {
    case "board": {
      if (!state.project) return;
      const { lane, row } = keys.cur;
      switch (k) {
        case "j": case "ArrowDown": return moveCursor(lane, keys.active ? row + n : row);
        case "k": case "ArrowUp": return moveCursor(lane, row - n);
        case "h": case "ArrowLeft": return moveCursor(lane - n, row);
        case "l": case "ArrowRight": return moveCursor(lane + n, row);
        case "gg": return moveCursor(lane, hadCount ? n - 1 : 0);
        case "G": return moveCursor(lane, hadCount ? n - 1 : Infinity);
        case "w": case "b": {
          const step = k === "w" ? 1 : -1;
          for (let li = lane + step; li >= 0 && li < laneCount(); li += step) {
            if (laneTodos(li).length) return moveCursor(li, 0);
          }
          return;
        }
        case "Enter": case "Space": case " ": {
          const t = cursorTodo();
          if (t) return openDetail(t.id);
          return;
        }
        case "o": case "O": return openTodoForm();
      }
      const t = cursorTodo();
      if (t) await todoKey(t, k);
      return;
    }
    case "chat":
      if (["i", "a", "o", "I", "A"].includes(k)) return chatInput.focus();
      if (["j", "k", "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "gg", "G"].includes(k)) return scrollBy($("#chat-log"), k, n);
      return;
    case "run": {
      const procs = state.procs;
      const cur = procs[keys.proc];
      switch (k) {
        case "j": keys.active = true; keys.proc = Math.min(procs.length - 1, keys.proc + n); return renderProcs();
        case "k": keys.active = true; keys.proc = Math.max(0, keys.proc - n); return renderProcs();
        case "Enter": if (cur) attach(cur); return;
        case "i": case "a": if (!$("#term-wrap").hidden) term.focus(); return;
        case "x": if (cur?.running) { await api("DELETE", `/api/processes/${cur.id}`); toast("Stopping"); } return;
        case "R": return startProc({ kind: "web", preset: "run" });
        case "T": return startProc({ kind: "task", preset: "test" });
        case "D": return startProc({ kind: "task", preset: "deploy" });
        case "!": return startProc({ kind: "pty" });
        case "/": return $("#run-custom").cmd.focus();
      }
      return;
    }
    case "activity":
      if (["j", "k", "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "gg", "G"].includes(k)) return scrollBy(scroller(), k, n);
  }
}

// Forms: Ctrl-Enter adds to the queue, Ctrl-S saves a draft.
$("#todo-form").addEventListener("keydown", (e) => {
  if (e.ctrlKey && e.key === "Enter") { e.preventDefault(); $("#todo-sheet").close("queue"); }
  if (e.ctrlKey && e.key.toLowerCase() === "s") { e.preventDefault(); $("#todo-sheet").close("draft"); }
});

// ------------------------------------------------------------------ boot
async function logout() {
  await fetch("/api/logout", { method: "POST" });
  location.replace("/login.html");
}
$("#logout").addEventListener("click", logout);
desktop.addEventListener("change", () => { if (state.detail) $("#scrim").hidden = !desktop.matches; });

renderBoard();
setMode();
setInterval(() => { if (!document.hidden) renderBoard(); }, 30000); // keep "4m"/"2h" current
connectEvents();
loadUsage().catch(() => {});
