import { $, ib, plural } from "./dom.js";
import { api } from "./api.js";
import { attempt, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { baseBranch, eventFor, latest, projectKey } from "./events.js";
import { renderBoard } from "./branches.js";
import { renderDetail } from "./detail.js";
import { scanTasksChanged } from "./scans.js";

// ------------------------------------------------------------------ board
export async function loadTodos() {
  const p = state.project;
  if (!p) return;
  const { data: list, seen } = await latest(() => api("GET", `/api/projects/${p.id}/todos`));
  if (projectKey(state.project) !== projectKey(p)) return; // another project's by now
  const todos = new Map(list.map((t) => [t.id, t]));
  for (const { ev, m } of seen) {
    if (!eventFor(p, ev, m)) continue;
    if (ev.type === "todo.updated") todos.set(ev.todo_id, ev.data);
    else if (ev.type === "todo.deleted") todos.delete(ev.todo_id);
  }
  state.todos = todos;
  renderBoard();
  scanTasksChanged(); // Scans counts what became of its drafts from these
  if (state.detail) renderDetail();
  loadUnpushed().catch(fail);
}

// landsOnGitHub: approved work reaches GitHub as it merges, with nothing
// left to push from here: through pull requests, or pushed by the runners.
export const landsOnGitHub = (p) => p.merge_mode === "pr" || !!p.runs_on?.length;

export async function loadUnpushed() {
  const p = state.project;
  if (!p?.remote_url || landsOnGitHub(p)) { state.unpushed = 0; renderPushMain(); return; }
  const { count, todos = [] } = await api("GET", `/api/projects/${p.id}/unpushed`);
  const same = (a, b) => a && a.size === b.length && b.every((id) => a.has(id));
  if (projectKey(state.project) !== projectKey(p) || (count === state.unpushed && same(state.unpushedTodos, todos))) return;
  state.unpushed = count;
  state.unpushedTodos = new Set(todos);
  renderBoard();
  renderPushMain();
}

export async function pushDone() {
  const p = state.project;
  const btn = $("#push-main");
  btn.disabled = true;
  try {
    await api("POST", `/api/projects/${p.id}/push`);
    toast(`Pushed ${baseBranch(p)}`);
    await loadUnpushed();
  } finally { renderPushMain(); }
}

// The sidebar's Push action: shown for projects that push to GitHub directly,
// saying how many commits wait (or that there are none).
export function renderPushMain() {
  const p = state.project, n = state.unpushed;
  const btn = $("#push-main");
  btn.hidden = !p?.remote_url || landsOnGitHub(p);
  if (btn.hidden) return;
  const label = n ? `Push ${plural(n, "commit")} to ${baseBranch(p)}` : `${baseBranch(p)} is up to date with GitHub`;
  btn.disabled = !n;
  btn.title = label;
  btn.setAttribute("aria-label", label);
  btn.querySelector("span").textContent = n ? `Push ${n > 99 ? "99+" : n}` : "Pushed";
}

export function pushBar() {
  const n = state.unpushed;
  if (!n) return null;
  return ib({ type: "button", class: "btn primary push-done", onclick: attempt(pushDone) }, "push",
    `Push ${plural(n, "commit")} to ${baseBranch(state.project)}`);
}
