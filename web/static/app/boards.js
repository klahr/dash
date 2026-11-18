import { $, h, ib, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import * as store from "./store.js";
import { projectKey } from "./events.js";
import { renderBoard } from "./branches.js";

// ------------------------------------------------------------------ boards
// A project's tasks are on its Main board or on one of the boards added in
// Project settings → Board (project.boards). The board shows one board's
// tasks at a time (state.board: "" is Main); with more than one, a switcher
// sits above the lanes. Each browser remembers the board it last showed for
// each project.
export const MAIN_BOARD = "Main";

// boardsOf is project p's boards, Main first.
export const boardsOf = (p = state.project) => [{ id: "", name: MAIN_BOARD }, ...(p?.boards || [])];
export const hasBoards = (p = state.project) => !!p?.boards?.length;
export const boardName = (id, p = state.project) => boardsOf(p).find((b) => b.id === (id || ""))?.name || MAIN_BOARD;

const boardKey = (p) => `dash.board.${projectKey(p)}`;

// currentBoard is the board shown: Main when the one picked is gone.
export function currentBoard() {
  const id = state.board || "";
  return id && state.project?.boards?.some((b) => b.id === id) ? id : "";
}

// onBoard reports whether task t is on the board shown.
export const onBoard = (t) => (t.board || "") === currentBoard();

// restoreBoard picks the board last shown for project p.
export function restoreBoard(p) {
  state.board = p ? store.get(boardKey(p), "") : "";
}

// showBoard shows board id's tasks. Like the goal filter, a lane it leaves
// empty gives way to one with tasks.
export function showBoard(id) {
  id = id || "";
  if (id === currentBoard()) return;
  state.board = id;
  if (state.project) store.set(boardKey(state.project), id || null);
  state.lane = null;
  renderBoard();
}

// boardField is a new task's board, for its POST body: the board shown.
export const boardField = () => (currentBoard() ? { board: currentBoard() } : {});

// The switcher above the lanes, for a project with more than one board:
// each board with how many of its tasks aren't merged yet.
export function renderBoardSwitch() {
  const el = $("#board-switch");
  const p = state.project;
  el.hidden = !hasBoards(p);
  if (el.hidden) { el.replaceChildren(); return; }
  const open = new Map();
  for (const t of state.todos.values()) {
    if (t.status !== "merged") open.set(t.board || "", (open.get(t.board || "") || 0) + 1);
  }
  const cur = currentBoard();
  el.replaceChildren(...boardsOf(p).map((b) => {
    const n = open.get(b.id) || 0;
    return h("button", {
      type: "button", class: "board-tab", role: "tab", "aria-selected": String(b.id === cur),
      tabindex: b.id === cur ? "0" : "-1", "data-board": b.id,
      title: `${b.name}: ${plural(n, "open task")}`, onclick: () => showBoard(b.id),
    }, h("span", { class: "board-tab-name" }, b.name), n ? h("span", { class: "n" }, n) : null);
  }), ib({ type: "button", class: "icon-btn board-add", title: "New board", "aria-label": "New board", onclick: attempt(() => addBoard()) }, "plus"));
}

// Arrow keys move along the switcher's tabs, as a tablist's do.
$("#board-switch").addEventListener("keydown", (e) => {
  const tabs = [...e.currentTarget.querySelectorAll(".board-tab")];
  const i = tabs.indexOf(document.activeElement);
  if (i < 0) return;
  const to = { ArrowLeft: i - 1, ArrowRight: i + 1, Home: 0, End: tabs.length - 1 }[e.key];
  if (to == null) return;
  e.preventDefault();
  const tab = tabs[(to + tabs.length) % tabs.length];
  showBoard(tab.dataset.board);
  $(`#board-switch .board-tab[data-board="${CSS.escape(tab.dataset.board)}"]`)?.focus();
});

const boardsURL = (p) => `/api/projects/${p.id}/boards`;

// addBoard asks for a name and adds a board to the project. From the
// switcher it then shows the new board.
export async function addBoard({ show = true } = {}) {
  const p = state.project;
  const name = await ask({ title: "New board", body: `A board of its own for some of ${p.name}'s tasks.`, input: true, placeholder: "Name, e.g. Website", ok: "Add board" });
  if (!name) return;
  const res = await api("POST", boardsURL(p), { name }, p.prefix || "");
  boardsChanged(p, res);
  if (show) showBoard(res.board.id);
  toast(`Added board ${res.board.name}`);
}

async function renameBoard(b) {
  const p = state.project;
  const name = await ask({ title: `Rename ${b.name}`, input: true, value: b.name, ok: "Rename" });
  if (!name || name === b.name) return;
  boardsChanged(p, await api("PATCH", `${boardsURL(p)}/${b.id}`, { name }, p.prefix || ""));
}

async function moveBoard(b, position) {
  const p = state.project;
  boardsChanged(p, await api("PATCH", `${boardsURL(p)}/${b.id}`, { position }, p.prefix || ""));
}

async function deleteBoard(b) {
  const p = state.project;
  const n = [...state.todos.values()].filter((t) => t.board === b.id).length;
  const ok = await ask({
    title: `Delete board ${b.name}?`, danger: true, ok: "Delete board",
    body: n ? `Its ${plural(n, "task")} move to ${MAIN_BOARD}.` : "It has no tasks.",
  });
  if (!ok) return;
  boardsChanged(p, await api("DELETE", `${boardsURL(p)}/${b.id}`, null, p.prefix || ""));
  toast(`Deleted board ${b.name}`);
}

// boardsChanged takes project p's boards from an answer, ahead of the
// project.updated event that reloads the projects.
function boardsChanged(p, res) {
  const boards = res.boards || [];
  for (const x of [p, state.projects.find((q) => projectKey(q) === projectKey(p))]) if (x) x.boards = boards;
  if (projectKey(state.project) !== projectKey(p)) return;
  state.project.boards = boards;
  renderBoard();
  renderBoardSettings();
}

// The list in Project settings → Board.
export function renderBoardSettings() {
  const list = $("#boards-list");
  const p = state.project;
  if (!list || !p) return;
  const bs = p.boards || [];
  list.replaceChildren(
    h("li", { class: "board-row" }, h("span", { class: "board-row-name" }, MAIN_BOARD), h("span", { class: "hint" }, "where tasks go unless you pick another board")),
    ...bs.map((b, i) => h("li", { class: "board-row" },
      h("span", { class: "board-row-name" }, b.name),
      h("span", { class: "board-row-actions" },
        ib({ type: "button", class: "icon-btn", title: `Move ${b.name} up`, "aria-label": `Move ${b.name} up`, disabled: i === 0, onclick: attempt(() => moveBoard(b, i - 1)) }, "up"),
        ib({ type: "button", class: "icon-btn", title: `Move ${b.name} down`, "aria-label": `Move ${b.name} down`, disabled: i === bs.length - 1, onclick: attempt(() => moveBoard(b, i + 1)) }, "down"),
        ib({ type: "button", class: "icon-btn", title: `Rename ${b.name}`, "aria-label": `Rename ${b.name}`, onclick: attempt(() => renameBoard(b)) }, "edit"),
        ib({ type: "button", class: "icon-btn", title: `Delete ${b.name}`, "aria-label": `Delete ${b.name}`, onclick: attempt(() => deleteBoard(b)) }, "trash")))));
}
$("#boards-add")?.addEventListener("click", attempt(() => addBoard({ show: false })));

// moveToBoard moves task t to board id.
export async function moveToBoard(t, id) {
  const p = state.project;
  const done = await api("PATCH", `/api/todos/${t.id}`, { board: id || "" }, p.prefix || "");
  state.todos.set(done.id, done);
  renderBoard();
  toast(`Moved to ${boardName(id)}`);
}
