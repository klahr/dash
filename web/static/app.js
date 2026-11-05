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
function toast(msg, isError = false) {
  const t = h("div", { class: `toast${isError ? " error" : ""}`, role: isError ? "alert" : "status" }, msg);
  $("#toasts").append(t);
  setTimeout(() => t.remove(), isError ? 6000 : 3000);
}
const fail = (e) => toast(e.message || String(e), true);
const attempt = (fn) => async (...a) => { try { await fn(...a); } catch (e) { fail(e); } };

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
  view: "board",
};

// ------------------------------------------------------------------ navigation
function showView(name) {
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
        if (prev && prev.status !== ev.data.status && ev.data.status === "review") toast(`Ready for review: ${title(ev.data)}`);
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
  if (!p) { renderBoard(); renderProcs(); return; }
  await Promise.all([loadTodos(), loadProcs()]);
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
  ] : []), h("button", { type: "button", class: "link logout", onclick: logout }, "Log out"));
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

function laneItems() {
  const out = Object.fromEntries(LANES.map((l) => [l.key, []]));
  for (const t of state.todos.values()) {
    const lane = LANES.find((l) => l.statuses.includes(t.status));
    if (lane) out[lane.key].push(t);
  }
  for (const k in out) {
    out[k].sort(k === "done" ? (a, b) => b.updated_at.localeCompare(a.updated_at) : byPriority);
  }
  return out;
}

function renderBoard() {
  const items = laneItems();
  const empty = $("#board-empty");
  if (!state.project) {
    $("#board").replaceChildren();
    $("#lane-tabs").replaceChildren();
    empty.hidden = false;
    empty.replaceChildren(
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
    role: "tab", class: `lane-tab s-${l.key === "done" ? "merged" : l.key}`,
    "aria-selected": String(state.lane === l.key), "data-count": items[l.key].length,
    onclick: () => { state.lane = l.key; renderBoard(); },
  }, l.label, h("span", { class: "n" }, items[l.key].length))));

  $("#board").replaceChildren(...LANES.map((l) => h("section", {
    class: `col s-${l.key === "done" ? "merged" : l.key}${state.lane === l.key ? " active" : ""}`, "aria-label": l.label,
  },
    h("header", { class: "col-head" }, l.label, h("span", { class: "n" }, items[l.key].length)),
    h("div", { class: "col-body" },
      items[l.key].length
        ? h("ol", { class: "rail" }, items[l.key].map((t) => h("li", {}, todoRow(t))))
        : h("p", { class: "col-empty" }, l.empty)))));

  $(".lane-tab[aria-selected=true]")?.scrollIntoView({ block: "nearest", inline: "nearest" });

  const review = items.review.length;
  $("#review-badge").hidden = !review;
  $("#review-badge").textContent = review;
}

function todoRow(t) {
  let sub = t.prompt.replace(/\s+/g, " ");
  let subClass = "todo-sub";
  if (t.status === "in_progress") { sub = state.lastLine.get(t.id) || "Working…"; subClass += " progress"; }
  if (t.error) { sub = t.error; subClass += " err"; }
  return h("button", {
    class: `todo s-${t.status}${t.goal ? " goal" : ""}${state.detail === t.id ? " selected" : ""}`,
    "data-id": t.id, onclick: () => openDetail(t.id),
  },
    h("span", { class: "node", "aria-hidden": "true" }),
    h("span", { class: "todo-main" }, h("span", { class: "todo-title" }, title(t)), h("span", { class: subClass }, sub)),
    h("span", { class: "todo-side" },
      t.goal ? h("span", { class: "goal-tag" }, "Milestone") : null,
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
  state.detail = null;
  $("#detail").hidden = true;
  $("#scrim").hidden = true;
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
  ].filter(Boolean));
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
      btns.push(b("Rework", rework), b("Approve and merge", post("approve", null, "Approved — merging"), "review grow"));
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
    h("div", { class: "diff-lines" }, f.lines.map((l) => h("div", {
      class: l.startsWith("@@") ? "hunk" : l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : "",
    }, l || " "))))));
}

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
  $("#procs").replaceChildren(...state.procs.map((p) => h("li", { class: `proc${p.id === attached ? " attached" : ""}` },
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

// ------------------------------------------------------------------ boot
async function logout() {
  await fetch("/api/logout", { method: "POST" });
  location.replace("/login.html");
}
$("#logout").addEventListener("click", logout);
desktop.addEventListener("change", () => { if (state.detail) $("#scrim").hidden = !desktop.matches; });

renderBoard();
connectEvents();
