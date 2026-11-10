import { $ } from "./dom.js";
import { wsURL } from "./viewport.js";
import { api, here, on } from "./api.js";
import { fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { grouped, loadProjects, projectWhere, renderSideProjects, selectProject } from "./projects.js";
import { loadUnpushed } from "./board.js";
import { renderBoard, title, updateLiveLine } from "./branches.js";
import { trackActivity } from "./activity.js";
import { closeDetail, loadDiff, openDetail, openTodo, renderDetail } from "./detail.js";
import { connectLog, connectTerm, loadPreviews, previewEvent, previewOf, pv } from "./preview.js";
import { loadRunners, renderRunners } from "./runners.js";
import { notify } from "./notifications.js";
import { describePerm, renderPerms } from "./review.js";
import { loadUsage, renderUsage } from "./usage.js";
import { releaseNews, renderCI } from "./actions.js";
import { loadCheckouts, renderCheckouts, renderRemote } from "./origin.js";
import { chatRunner, loadChat, todoChat } from "./chat.js";
import { loadTodoChat } from "./chatpane.js";
import { addWorkerEvent } from "./timeline.js";
import { attach, attached, termWS } from "./terminal.js";
import { loadProcs } from "./try.js";
import { refreshGraph } from "./history.js";
import { iss, onIssueEvent, renderIssues } from "./issues.js";
import { loadScans, onScanRun, scanFinished, scans } from "./scans.js";
import { clChanged } from "./checklist.js";
import { checkForUpdate } from "./updates.js";

// ------------------------------------------------------------------ live events
// One live connection per computer, so notifications arrive from all of them.
const sockets = new Map(); // machine name → { ws, live }
export async function connectAll() {
  state.machines = await api("GET", "/api/machines");
  for (const m of state.machines) if (m.ok && !sockets.has(m.name)) connectEvents(m);
  await loadProjects();
}

function connectEvents(m) {
  const ws = new WebSocket(wsURL(on(m.prefix, "/ws/events")));
  const sock = { ws, live: false, timer: 0 };
  sockets.set(m.name, sock);
  ws.onopen = () => {
    if (sockets.get(m.name) !== sock) return ws.close(); // replaced by reviveSockets meanwhile
    sock.live = true;
    refreshLive();
    if (m.local) {
      checkForUpdate(); // a reconnect often means Dash restarted, maybe updated
      loadChat().catch(() => {});
    }
    // Dash restarted (a deploy of Dash itself does): its runners kept their processes.
    loadProjects().then(() => loadProcs()).catch(fail);
    loadPreviews(m).catch(() => {});
  };
  ws.onclose = async () => {
    if (sockets.get(m.name) !== sock) return; // replaced: its successor is already connecting
    sock.live = false;
    refreshLive();
    // Logged out: go to the login page. Server down or restarting: keep trying.
    const me = await fetch("/api/me").catch(() => null);
    if (me?.status === 401) { location.replace("/login.html"); return; }
    // A runner that went offline or was revoked is reconnected by
    // refreshMachines when the hub says it's back.
    if (m.runner) {
      const now = (state.machines || []).find((x) => x.name === m.name);
      if (!now || !now.ok) { sockets.delete(m.name); return; }
    }
    if (sockets.get(m.name) !== sock) return; // revived while we asked
    sock.timer = setTimeout(() => { if (sockets.get(m.name) === sock) connectEvents(m); }, m.local ? 1500 : 5000);
  };
  ws.onmessage = (msg) => onEvent(JSON.parse(msg.data), m);
}

// A phone suspends a page sent to the home screen (or a locked screen) and
// drops its sockets, often without telling it: on return a socket may be
// closed with its retry timer throttled, or still claim to be open with
// nothing behind it. So on return, connect again at once: sockets that are
// down right away, and after a longer absence open-looking ones too, since
// those can't be trusted (the server stops pinging after a minute).
let hiddenAt = document.hidden ? Date.now() : 0;
function reviveSockets() {
  const away = hiddenAt ? Date.now() - hiddenAt : 0;
  hiddenAt = 0;
  const stale = (ws) => ws.readyState > 1 || (away > 15000 && ws.readyState === 1);
  for (const m of state.machines || []) {
    const sock = sockets.get(m.name);
    if (!m.ok || (sock && !stale(sock.ws))) continue;
    if (sock) { clearTimeout(sock.timer); sockets.delete(m.name); sock.ws.close(); }
    connectEvents(m);
  }
  refreshLive();
  // The open terminal and preview log reconnect too; both replay their scrollback.
  if (termWS && stale(termWS) && state.view === "run") {
    const p = state.procs.find((x) => x.id === attached && x.running);
    if (p) attach(p);
  }
  if (pv.ws && stale(pv.ws)) {
    const p = state.todos.get(pv.todo) && previewOf(pv.todo);
    if (p?.process_id === pv.proc) connectLog(p);
  }
  if (pv.term && stale(pv.term)) {
    const p = state.todos.get(pv.todo) && previewOf(pv.todo);
    if (p?.process_id === pv.termProc && p.state === "live") connectTerm(p);
  }
}
document.addEventListener("visibilitychange", () => {
  if (document.hidden) hiddenAt = Date.now();
  else reviveSockets();
});
window.addEventListener("pageshow", (e) => { if (e.persisted) reviveSockets(); });
window.addEventListener("online", reviveSockets);

// A runner came online, went offline, registered or was revoked: list the
// computers again, connect to the ones that are up, reload their projects.
export async function refreshMachines() {
  state.machines = await api("GET", "/api/machines");
  for (const m of state.machines) if (m.ok && !sockets.has(m.name)) connectEvents(m);
  await loadProjects();
  loadProcs().catch(() => {}); // a runner back online lists its processes again
  if ($("#runners-sheet").open) await renderRunners();
  else await loadRunners();
}

export function multi() { return (state.machines || []).length > 1; }
// The branch a project's tasks start from and land on: the one picked in the branch switcher,
// else its default branch.
export function baseBranch(p) { return p?.work_branch || p?.default_branch || ""; }
export function refreshLive() { setLive(!!sockets.get(state.project?.machine || state.machines?.[0]?.name)?.live); }
function setLive(on) {
  $("#live").classList.toggle("on", on);
  $("#live").title = on ? "Live updates connected" : "Live updates disconnected — reconnecting";
  $("#live-label").textContent = on ? "Live" : "Offline";
}

// A project's tasks and permission requests are fetched over REST and then
// kept current by live events. On a busy computer or a slow network the
// fetch's answer can arrive after newer events (a task created, a request
// made just after it was read), and would undo them: the events seen while
// a fetch is on its way are kept, to replay over its answer (latest).
const inflight = new Set();
export async function latest(fetchIt) {
  const seen = [];
  inflight.add(seen);
  try {
    return { data: await fetchIt(), seen };
  } finally {
    inflight.delete(seen);
  }
}
export const eventFor = (p, ev, m) => !!p && ev.project_id === p.id && m.name === p.machine;

function onEvent(ev, m = { name: "" }) {
  for (const seen of inflight) seen.push({ ev, m });
  const sameMachine = !state.project || m.name === state.project.machine;
  const mine = eventFor(state.project, ev, m);
  if (!mine) elsewhere(ev, m);
  if (ev.type === "todo.updated" || ev.type === "todo.deleted") trackActive(ev, m);
  switch (ev.type) {
    case "runners":
      if (m.local) { refreshMachines().catch(fail); chatRunner.load().catch(() => {}); }
      break;
    case "project.updated":
    case "project.deleted":
      loadProjects().catch(fail);
      break;
    case "todo.updated":
      if (!mine) break;
      {
        const prev = state.todos.get(ev.todo_id);
        state.todos.set(ev.todo_id, ev.data);
        // Nothing runs for a task back in drafts or the queue, or merged: drop an activity whose end we missed.
        if (["draft", "queued", "merged"].includes(ev.data.status)) state.activity.delete(ev.todo_id);
        renderBoard();
        if (state.view === "issues" && (prev?.status !== ev.data.status || prev?.paused !== ev.data.paused) && iss.list.some((i) => i.todo_ids?.includes(ev.todo_id))) renderIssues();
        if (state.detail === ev.todo_id) {
          renderDetail();
          if (prev?.status !== ev.data.status && ev.data.worktree) loadDiff();
          // Dash explains the failure in the chat.
          if (prev && prev.status !== ev.data.status && ev.data.status === "failed" && !todoChat.busy) loadTodoChat().catch(fail);
        } else if (prev?.status !== ev.data.status && state.todos.get(state.detail)?.depends_on?.includes(ev.todo_id)) {
          renderDetail(); // the open task's "waiting for" line
        }
        if (prev && prev.status !== ev.data.status) {
          if (ev.data.status === "review") { toast(title(ev.data), { tone: "review", head: "Ready for review", action: openTodo(ev.todo_id) }); notify("Ready for review", ev.data, "", !here()); }
          if (ev.data.status === "failed") { toast(title(ev.data), { tone: "error", head: "Failed", action: openTodo(ev.todo_id) }); notify("Failed", ev.data, ev.data.error, !here()); }
          if (ev.data.status === "merged") { toast(title(ev.data), { tone: "ok", head: "Merged", icon: "merge" }); notify("Merged", ev.data); loadUnpushed().catch(fail); }
        }
        if (prev && prev.revert?.commit !== ev.data.revert?.commit) loadUnpushed().catch(fail);
        if (prev && !prev.revert && ev.data.revert) {
          const url = ev.data.revert.pr_url;
          toast(title(ev.data), { tone: "pr", icon: "undo", head: url ? "Revert pull request opened" : "Reverted", action: url ? { label: "View", run: () => window.open(url, "_blank", "noopener") } : openTodo(ev.todo_id) });
        }
        if (prev && !prev.pr && ev.data.pr) toast(title(ev.data), { tone: "pr", head: `Pull request #${ev.data.pr.number} opened`, action: { label: "View", run: () => window.open(ev.data.pr.url, "_blank", "noopener") } });
      }
      break;
    case "issue.updated":
    case "issue.deleted":
      if (mine) onIssueEvent(ev);
      break;
    case "scan.run":
      if (mine) onScanRun(ev);
      break;
    case "scan.settings":
    case "scan.template":
      if (mine && scans.loaded) loadScans().catch(() => {});
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
      break;
    case "todo.event":
      if (mine) trackActivity(ev.todo_id, ev.data);
      if (state.detail === ev.todo_id) addWorkerEvent(ev.todo_id, ev.data);
      break;
    case "git.op":
      if (mine) refreshGraph();
      break;
    case "merge.queue":
      if (sameMachine) $("#merge-status").textContent = ev.data.pending ? `${ev.data.pending} in merge queue` : "";
      break;
    case "process.updated":
      if (mine) loadProcs().catch(fail);
      break;
    case "checklist.updated":
      if (mine) clChanged(ev.data?.version);
      break;
    case "preview.updated":
      previewEvent(ev.data, m);
      break;
    case "permission.updated":
      if (!mine) break;
      state.perms = state.perms.filter((p) => p.id !== ev.data.id);
      if (!ev.data.decision) {
        state.perms.push(ev.data);
        const t = state.todos.get(ev.data.todo_id);
        toast(t ? `${describePerm(ev.data).title} · ${title(t)}` : describePerm(ev.data).title, { tone: "approval", head: "Claude needs your approval", action: t ? { label: "Review", run: () => openDetail(t.id) } : null });
        if (t) notify(`Claude wants to ${describePerm(ev.data).title}`, t, describePerm(ev.data).detail, !here());
      }
      renderPerms();
      break;
    case "usage.updated":
      if (!sameMachine) break;
      // On a hub that doesn't run Claude the usage shown is your runner's,
      // which only GET /api/usage gives.
      if (chatRunner.info?.local === false) loadUsage().catch(() => {});
      else { state.usage = ev.data; renderUsage(); }
      break;
    case "ci.updated":
      if (!mine) break;
      if (state.ci?.state && state.ci.state !== "failing" && ev.data.state === "failing") {
        toast(ev.data.todo_id ? "Claude is on it" : "", { tone: "error", head: `Build failing on ${ev.data.branch}`, action: ev.data.todo_id ? openTodo(ev.data.todo_id) : null });
        notify(`Build failing on ${ev.data.branch}`, { title: state.project.name, prompt: "" }, "", !here());
      }
      if (state.ci?.state === "failing" && ev.data.state === "passing") toast("", { tone: "ok", head: `Build passing again on ${ev.data.branch}` });
      releaseNews(state.ci?.release, ev.data.release);
      state.ci = ev.data;
      renderCI();
      break;
    case "remote.updated":
      if (!mine) break;
      if (state.remote?.ahead !== ev.data.ahead || state.remote?.behind !== ev.data.behind) loadUnpushed().catch(() => {});
      state.remote = ev.data;
      renderRemote();
      loadCheckouts().catch(() => {});
      break;
    case "checkouts.updated":
      if (!mine) break;
      state.checkouts = ev.data || [];
      renderCheckouts();
      break;
  }
}

// Keeps each project's running / ready-for-review todos current for the sidebar dots.
function trackActive(ev, m) {
  const p = (state.projects || []).find((x) => x.id === ev.project_id && x.machine === m.name);
  if (!p) return;
  const active = { ...p.active };
  const st = ev.type === "todo.updated" ? ev.data.status : null;
  if (st === "in_progress" || st === "review") active[ev.todo_id] = st;
  else delete active[ev.todo_id];
  if (projectStatus({ active }) === projectStatus(p)) { p.active = active; return; }
  p.active = active;
  renderSideProjects();
}

// Yellow while Claude works on something, green when work waits for review.
export const projectStatus = (p) => {
  const s = Object.values(p.active || {});
  return s.includes("in_progress") ? "running" : s.includes("review") ? "review" : "";
};

// Things that happen in projects you're not looking at, possibly on another
// computer: tell the user about the ones that need them.
const seen = new Map(); // "machine/todo" → last status
function elsewhere(ev, m) {
  const p = (state.projects || []).find((x) => x.id === ev.project_id && x.machine === m.name);
  const where = p ? (grouped() ? `${p.name} on ${projectWhere(p)}` : p.name) : m.name;
  if (ev.type === "todo.updated") {
    const key = `${m.name}/${ev.todo_id}`;
    const prev = seen.get(key);
    seen.set(key, ev.data.status);
    if (!prev || prev === ev.data.status) return;
    const go = p ? { label: "Go", run: async () => { await selectProject(p); await openDetail(ev.todo_id); } } : null;
    if (ev.data.status === "review") { toast(`${title(ev.data)} · ${where}`, { tone: "review", head: "Ready for review", action: go }); notify("Ready for review", ev.data, where); }
    if (ev.data.status === "failed") notify("Failed", ev.data, `${where}: ${ev.data.error || ""}`);
  }
  if (ev.type === "scan.run" && ev.data.trigger !== "manual") scanFinished(ev.data, p, where);
  if (ev.type === "permission.updated" && !ev.data.decision) {
    toast(`${describePerm(ev.data).title} · ${where}`, { tone: "approval", head: "Claude needs your approval", action: p ? { label: "Go", run: async () => { await selectProject(p); await openDetail(ev.data.todo_id); } } : null });
    notify(`Claude wants to ${describePerm(ev.data).title}`, { title: where, prompt: "" }, describePerm(ev.data).detail);
  }
}

export function projectKey(p) { return p ? `${p.machine}/${p.id}` : ""; }
