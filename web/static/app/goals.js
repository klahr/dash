import { $, h, ico, plural } from "./dom.js";
import { api } from "./api.js";
import { state } from "./state.js";
import { eventFor, latest, projectKey } from "./events.js";
import { renderBoard } from "./branches.js";
import { renderDetail } from "./detail.js";

// ------------------------------------------------------------------ goals on the board
// A goal's tasks stay ordinary cards in the usual lanes. Each carries a chip
// with its goal's name, in a colour of its own; pressing it shows only that
// goal's tasks (state.goalFilter) until the chip at the top of the board
// clears it. The goals themselves (their titles) are state.goals.

export async function loadGoals() {
  const p = state.project;
  if (!p) return;
  let list = [], seen = [];
  try {
    ({ data: list, seen } = await latest(() => api("GET", `/api/projects/${p.id}/goals`)));
  } catch { /* a Dash from before goals: its tasks have none */ }
  if (projectKey(state.project) !== projectKey(p)) return; // another project's by now
  const goals = new Map(list.map((g) => [g.id, g]));
  for (const { ev, m } of seen) if (ev.type === "goal.updated" && eventFor(p, ev, m)) goals.set(ev.data.id, ev.data);
  state.goals = goals;
  renderBoard();
  if (state.detail) renderDetail();
}

// A goal was made or changed (its title, say): its chips follow.
export function onGoalEvent(ev) {
  if (!ev.data?.id) return;
  const was = state.goals.get(ev.data.id)?.title;
  state.goals.set(ev.data.id, ev.data);
  if (was === ev.data.title) return;
  renderBoard();
  if (state.detail) renderDetail();
}

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
