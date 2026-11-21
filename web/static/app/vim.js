import { $ } from "./dom.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { ask, attempt, railOf, toast } from "./dialogs.js";
import { LANES, projectFirst, state } from "./state.js";
import { showView } from "./nav.js";
import { cleanup, isHub, loadProjects, openNewProject, openProjects, selectProject } from "./projects.js";
import { pushDone } from "./board.js";
import { closable, closeTodo, hasPriority, laneItems, renderBoard, title, toggleMine } from "./branches.js";
import { multiMember } from "./people.js";
import { boardField } from "./boards.js";
import { openTodoForm } from "./todos.js";
import { editTodo } from "./inlineedit.js";
import { closeDetail } from "./detail.js";
import { previewTodo } from "./preview.js";
import { revertTodo, reworkTodo } from "./drawmode.js";
import { toggleInvisibles, toggleVim } from "./diffedit.js";
import { openPrefs } from "./prefs.js";
import { logout, openOrg } from "./account.js";
import { openAccount } from "./accountsheet.js";
import { openRunners } from "./runners.js";
import { openConnector } from "./wallview.js";
import { toggleNotifications } from "./notifications.js";
import { renderUsage } from "./usage.js";
import { openCI } from "./actions.js";
import { openSettings } from "./settings.js";
import { activeChat, toggleChatPane, togglePin } from "./chatpane.js";
import { openShell } from "./terminal.js";
import { openTry } from "./try.js";
import { clearPriority, nextPriority } from "./dnd.js";
import { isQuestion } from "./chatparts.js";

// ------------------------------------------------------------------ vim keys
// Normal mode is the default; typing in a field or the terminal is insert
// mode. Esc (Ctrl-] in the terminal) returns to normal mode.
// In the nav's order: the project's sections, then Planning (the organization's).
export const VIEWS = ["board", "issues", "scans", "run", "history", "goals", "files", "checklist", "planning"];
export const keys = { active: false, count: 0, pending: "", cur: { lane: 0, row: 0 } };

export const BOUND = /^(:|\?|gt|gT|gg|gx|gb|gi|gn|gc|gs|gh|go|gf|gl|gp|m|M|G|F|u|p|P|c|\[|q|Escape|y|Y|A|X|n|N|j|k|h|l|w|b|o|O|s|x|a|r|e|E|\+|-|K|J|\*|t|T|!|dd|i|I|R|D|\/|Enter| |Arrow(Up|Down|Left|Right)|ctrl\+[dufb])$/;
export const isTyping = (el) => el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.closest?.(".xterm"));
export const openDialog = () => document.querySelector("dialog[open]");

export function setMode() {
  const el = $("#kbd-status");
  const typing = isTyping(document.activeElement);
  const mode = document.activeElement?.id === "cmd-input" ? "command" : typing ? "insert" : "normal";
  el.dataset.mode = mode;
  el.textContent = mode.toUpperCase() +(keys.count || keys.pending ? `  ${keys.count || ""}${keys.pending}` : "");
}
document.addEventListener("focusin", setMode);
document.addEventListener("focusout", () => setTimeout(setMode));
// Mouse or touch hides the keyboard cursor; a key brings it back.
document.addEventListener("pointerdown", () => { if (keys.active) { keys.active = false; renderBoard(); } });

export function laneCount() { return LANES.length; }
export function laneTodos(li) { return laneItems()[LANES[li].key] || []; }
export function cursorTodo() { return laneTodos(keys.cur.lane)[keys.cur.row] || null; }
// Minimized lanes (desktop only) and hidden ones are skipped by lane navigation.
export function laneSkipped(li) { return (desktop.matches && state.collapsed.has(LANES[li].key)) || laneHidden(LANES[li].key); }
// With auto approve, finished work skips Review; the lane only shows while
// something still waits there (failing tests, a merge that came back).
export function laneHidden(key) {
  return key === "review" && !!state.project?.auto_approve && ![...state.todos.values()].some((t) => ["review", "failed"].includes(t.status));
}
export function stepLane(lane, d) {
  const step = Math.sign(d);
  for (let li = lane + step, left = Math.abs(d); li >= 0 && li < laneCount(); li += step) {
    if (!laneSkipped(li)) { lane = li; if (!--left) break; }
  }
  return lane;
}

export function moveCursor(lane, row) {
  lane = Math.max(0, Math.min(laneCount() - 1, lane));
  const n = laneTodos(lane).length;
  keys.cur = { lane, row: Math.max(0, Math.min(n - 1, row)) };
  keys.active = true;
  if (!desktop.matches) state.lane = LANES[lane].key; // mobile shows one lane
  renderBoard();
  ($(".todo.cursor") || $(".col.cursor"))?.scrollIntoView({ block: "nearest", inline: "nearest" });
}

// The element j/k and Ctrl-d/u scroll in the current context.
export function scroller() {
  if (state.detail) return $("#detail .detail-body");
  return desktop.matches ? $(".view.active") : document.scrollingElement;
}

export function scrollBy(el, how, n) {
  const page = el.clientHeight || innerHeight;
  const delta = { j: 48, k: -48, "ctrl+d": page / 2, "ctrl+u": -page / 2, "ctrl+f": page * 0.9, "ctrl+b": -page * 0.9 }[how];
  if (how === "gg") el.scrollTo({ top: 0 });
  else if (how === "G") el.scrollTo({ top: el.scrollHeight });
  else el.scrollBy({ top: delta * n });
}

// What Enter does to a task: approve one in review (or open its pull
// request, when it is reviewed on GitHub), enqueue a draft.
export function enterKey(t) {
  if (t.status === "review" && t.pr?.state === "OPEN" && !t.error) return "gx";
  return { review: "a", draft: "s" }[t.status];
}

// Actions on a todo; shared by the board cursor and the open detail view.
export async function todoKey(t, k) {
  const post = (verb, body, msg) => api("POST", `/api/todos/${t.id}/${verb}`, body).then(() => toast(msg));
  switch (k) {
    case "s": return post("enqueue", null, "Added to queue");
    case "x": return post("cancel", null, t.status === "in_progress" ? "Stopping" : "Moved to drafts");
    case "z": return t.paused ? post("resume", null, "Resumed") : post("pause", null, t.status === "in_progress" ? "Pausing" : "Paused");
    case "a": return closable(t) ? closeTodo(t) : post("approve", null, "Approved — merging");
    case "r": return reworkTodo(t);
    case "u": return revertTodo(t);
    case "gx":
      if (!t.pr) throw new Error("This task has no pull request.");
      return window.open(t.pr.url, "_blank", "noopener");
    case "e": return editTodo(t);
    case "+": return api("PATCH", `/api/todos/${t.id}`, { priority: hasPriority(t) ? t.priority - 1 : nextPriority(t) });
    case "-":
      if (!hasPriority(t)) throw new Error("This task has no priority to lower.");
      return api("PATCH", `/api/todos/${t.id}`, { priority: t.priority + 1 });
    case "_": return hasPriority(t) ? clearPriority(t) : undefined;
    case "t": return previewTodo(t);
    case "!": return openShell({ todo_id: t.id }, t.runner);
    case "dd":
      if (await ask({ title: `Delete "${title(t)}"?`, body: "Its worktree and branch are removed too. This can't be undone.", ok: "Delete", danger: true })) {
        await api("DELETE", `/api/todos/${t.id}`);
        if (state.detail === t.id) closeDetail();
        toast("Task deleted");
      }
      return;
  }
  if (k === "T") return api("POST", `/api/todos/${t.id}/test`).then(() => toast("Running tests…"));
  if (k === "Y") return api("POST", `/api/todos/${t.id}/duplicate`).then(() => toast("Duplicated as a draft"));
  if (k === "E") return api("POST", `/api/todos/${t.id}/open`).then(() => toast("Opening in your editor"));
  return false; // not a todo key
}

// y / A / X answer the permission request in view: the open todo's, else the oldest.
export async function permKey(k) {
  const p = state.perms.find((x) => x.todo_id === state.detail) || (!state.detail && state.perms[0]);
  if (!p) return false;
  const decision = { y: "allow", A: "always", X: "deny" }[k];
  if (isQuestion(p) && decision !== "deny") return toast("Answer Claude's question in its card");
  await api("POST", `/api/permissions/${p.id}`, { decision });
  toast(decision === "deny" ? "Denied" : decision === "always" ? `${p.tool} always allowed in this project` : "Allowed");
}

export async function runCommand(line) {
  line = line.trim().replace(/^:/, "");
  if (!line) return;
  if (/^\d+$/.test(line)) { moveCursor(keys.cur.lane, Number(line) - 1); return; }
  const [name, ...rest] = line.split(/\s+/);
  const arg = rest.join(" ");
  const need = () => { if (!state.project) throw new Error(projectFirst(" (p)")); return state.project; };
  switch (name) {
    case "q": case "q!": case "close": closeDetail(); openDialog()?.close(); return;
    case "b": case "board": return showView("board");
    case "mine":
      if (!multiMember()) throw new Error("Mine filters the board to your own tasks, in an organization of several people.");
      showView("board"); return toggleMine();
    case "c": case "chat": return toggleChatPane();
    case "pin": case "unpin": return togglePin();
    case "run": case "!": case "sh": case "shell":
      if (!arg) return showView("run");
      need(); return openShell({ command: arg });
    case "ps": case "procs": return showView("run");
    case "try": need(); return openTry();
    case "history": case "graph": case "tree": return showView("history");
    case "p": case "project": {
      if (!arg) return openProjects();
      const [pn, mn = ""] = arg.toLowerCase().split("@");
      const p = state.projects.find((x) => x.name.toLowerCase().startsWith(pn) && x.machine.toLowerCase().startsWith(mn));
      if (!p) throw new Error(`No project matching "${arg}".`);
      return selectProject(p);
    }
    // Projects are added on a runner, from the hub's Add project sheet (the
    // hub keeps none on its own disk): open it, with the name typed.
    case "np": case "newproject": {
      openNewProject();
      const f = $("#new-project");
      if (arg && $("#new-project-sheet").open && !f.name.value.trim()) f.name.value = arg;
      return;
    }
    // A clone already on a runner is added from that runner's page in Runners:
    // open yours, when you have one.
    case "ac": case "addclone": {
      if (!isHub()) throw new Error("Projects are added from the hub's dashboard.");
      const mine = state.runners.filter((r) => r.mine);
      await openRunners(mine.length === 1 ? mine[0].id : undefined);
      return toast(mine.length === 1 ? "Add from this runner… adds a repository already on it" : "Pick your runner, then Add from this runner…");
    }
    case "runners":
      if (!isHub()) throw new Error("Runners are on the hub's dashboard.");
      return openRunners();
    case "org": case "organization": return openOrg();
    case "account": return openAccount();
    case "new": case "o":
      need(); openTodoForm(); if (arg) $("#todo-form").prompt.value = arg; return;
    case "todo": case "task":
      if (!arg) throw new Error("Usage: :task <instructions>");
      await api("POST", `/api/projects/${need().id}/todos`, { prompt: arg, enqueue: true, ...boardField() });
      return toast("Added to queue");
    case "cleanup": case "clean": need(); return cleanup();
    case "ci": case "builds": return openCI();
    case "push": need(); return pushDone();
    case "pause": case "resume":
      state.usage = await api("POST", `/api/queue/${name}`);
      renderUsage();
      return toast(name === "pause" ? "Queue paused" : "Queue resumed");
    case "usage": renderUsage(); return $("#usage-sheet").showModal();
    case "settings": need(); return openSettings();
    case "prefs": case "preferences": return openPrefs();
    case "connector": case "connect": return openConnector();
    case "perms": case "permissions":
      if (!state.perms.length) return toast("No permission requests waiting");
      showView("board"); closeDetail();
      return $("#perm-banner").scrollIntoView({ block: "nearest" });
    case "notify": case "notifications": return toggleNotifications();
    case "vim": return toggleVim();
    case "list": case "invisibles": return toggleInvisibles();
    case "clear": {
      const c = activeChat();
      if (!c) throw new Error("Pick a project first.");
      return c.clear();
    }
    case "help": case "h": return openKeys();
    case "logout": return logout();
    case "e": case "e!": case "refresh": return loadProjects();
  }
  throw new Error(`Not a command: ${name}. Press ? for help.`);
}

// openKeys opens the Keyboard sheet at the keys of what is shown: the open
// task, else the page (the Shell at a commit is the Shell; Files and Planning
// have no keys of their own, so they start at Everywhere).
const KEY_AREA = { board: "board", run: "shell", try: "shell", history: "history", goals: "goals", checklist: "checklist" };
export function openKeys() {
  const d = $("#keys-sheet");
  d.showModal();
  railOf(d.id)?.show(state.detail ? "open" : KEY_AREA[state.view] || "everywhere");
}

export function openCommandLine() {
  const input = $("#cmd-input");
  input.value = "";
  input.focus();
}
$("#cmd-input").addEventListener("blur", (e) => { e.target.value = ""; });
$("#cmd-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const line = $("#cmd-input").value;
  $("#cmd-input").blur();
  attempt(runCommand)(line);
});
