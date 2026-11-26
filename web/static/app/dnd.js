import { $, $$, h } from "./dom.js";
import { makeCardDrag } from "./carddrag.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { ask, fail, leave, toast } from "./dialogs.js";
import { LANES, state } from "./state.js";
import { approvable, closable, hasPriority, laneItems, moveWithUndo, renderBoard } from "./branches.js";
import { laneHidden } from "./vim.js";
import { hoverCard } from "./hovercard.js";
import { dropBefore } from "./glide.js";

// ------------------------------------------------------------------ drag & drop
// Drag a card to another lane to act on it, or within a lane to reorder
// (the mechanics are carddrag.js's). Lane tabs are drop targets on phones.

// What dropping a todo of a status into a lane does (null = not allowed).
function dropAction(t, lane) {
  const from = t.status;
  const to = LANES.find((l) => l.key === lane);
  if (!to) return null;
  if (to.statuses.includes(from)) return ["draft", "queued", "review"].includes(lane) && from !== "failed" ? { kind: "reorder" } : null;
  if (t.reviewing) return null; // locked until the review finishes
  // drop: the column's label while a card is dragged over it; undo: the move
  // waits while its toast offers Undo (it closes a pull request, merges or closes).
  const go = (verb, drop, label, undo = false) => ({ kind: "action", verb, drop, label, undo });
  if (lane === "queued" && ["draft", "failed"].includes(from)) return go("enqueue", "Queue", "Added to queue");
  if (lane === "queued" && from === "review") return { kind: "rework", drop: closable(t) ? "Ask for changes" : "Send back", danger: true };
  if (lane === "draft" && ["queued", "review", "failed"].includes(from)) {
    const pr = t.pr?.state === "OPEN";
    return { ...go("cancel", pr ? `Cancel task · close PR #${t.pr.number}` : from === "queued" ? "Move to drafts" : "Cancel task",
      pr ? `Moved to drafts; pull request #${t.pr.number} closed` : "Moved to drafts", from !== "queued"), danger: from !== "queued" };
  }
  // A task that changed no files is closed: nothing to merge. Merging is
  // folded away while empty, so Merged takes these drops too.
  if (["merging", "done"].includes(lane) && closable(t)) return { ...go("close", "Close", "Closed", true), status: "merged" };
  // A task whose pull request is open is approved (and merged) on GitHub.
  if (["merging", "done"].includes(lane) && from === "review" && approvable(t)) return { ...go("approve", "Approve", "Approved — merging", true), status: "merging" };
  return null;
}

// What is dragged is the task; target is where it would drop: { lane, action, index }.
let target = null;
export const drag = makeCardDrag({
  item: ".todo[data-id]", li: ".cards > li", list: ".cards", ghost: "drag-ghost", slot: "drop-slot",
  slotHeight: (r) => Math.max(r.height - 8, 24), // its margins make up the rest
  pick: (card) => state.todos.get(card.dataset.id) || null,
  start() { hoverCard.close(); markTargets(); showDropBar(); },
  move: moveDrag,
  drop: () => {
    const t = drag.data, to = target;
    return to ? () => endDrag(t, to) : null;
  },
  end() {
    target = null;
    const bar = $("#drop-bar");
    if (bar) leave(bar, "leaving", () => bar.remove(), 160);
    $$(".drop-ok, .drop-no, .drop-over").forEach((el) => el.classList.remove("drop-ok", "drop-no", "drop-over"));
    $$(".drop-label").forEach((el) => el.remove());
  },
  abort: () => renderBoard(),
});
drag.attach($("#board"));
$("#board").addEventListener("contextmenu", (e) => { if (e.target.closest(".todo")) e.preventDefault(); });

// The lanes shown, by key (Merging folds away while empty; a desktop's minimized ones aren't columns).
const shownLanes = () => LANES.filter((l) => !laneHidden(l.key));

// On phones the other lanes are off-screen, so a drag shows a bar of large
// drop targets for exactly the moves this card allows, each saying what it does.
function showDropBar() {
  if (desktop.matches) return;
  const seen = new Set();
  const zones = shownLanes().map((l) => [l, dropAction(drag.data, l.key)])
    .filter(([, a]) => a && a.kind !== "reorder" && !seen.has(a.drop) && seen.add(a.drop))
    .map(([l, a]) => h("div", { class: `drop-zone s-${l.key === "done" ? "merged" : l.key}${a.danger ? " danger" : ""}`, "data-lane": l.key }, a.drop));
  if (!zones.length) return;
  document.body.append(h("div", { class: "drop-bar", id: "drop-bar" }, zones));
}

// Highlight lanes (columns and mobile tabs) where this card may be dropped;
// a column it would move to says, at its top, what the drop does.
function markTargets() {
  for (const el of $$(".col[data-lane], .lane-tab[data-lane]")) {
    const a = dropAction(drag.data, el.dataset.lane);
    el.classList.toggle("drop-ok", !!a);
    el.classList.toggle("drop-no", !a);
    if (a?.drop && el.matches(".col")) {
      el.querySelector(".col-head").after(h("div", { class: `drop-label${a.danger ? " danger" : ""}`, "aria-hidden": "true" }, a.drop));
    }
  }
}

function moveDrag(x, y) {
  const under = document.elementFromPoint(x, y);
  const laneEl = under?.closest(".drop-zone[data-lane], .col[data-lane], .lane-tab[data-lane]");
  const lane = laneEl?.dataset.lane || null;
  const action = lane ? dropAction(drag.data, lane) : null;
  $$(".drop-over").forEach((el) => el !== laneEl && el.classList.remove("drop-over"));
  target = action ? { lane, action, index: Infinity } : null;
  if (!action) { drag.place(null); return; }
  laneEl.classList.add("drop-over");

  // Inside a visible column, show where the card would land.
  const col = laneEl.classList.contains("col") ? laneEl : null;
  const list = col?.querySelector(".cards") || null;
  if (!col || laneEl.matches(".col:not(.active)") && !desktop.matches) { drag.place(null); return; }
  // Measured where the cards rest, not mid-glide, so the gap doesn't flicker.
  const items = list ? [...list.children].filter((li) => li !== drag.slot && li !== drag.li) : [];
  const before = dropBefore(items, y);
  target.index = before ? items.indexOf(before) : items.length;
  const body = col.querySelector(".col-body");
  if (list) drag.place(list, before);
  else if (body) drag.place(body, body.firstChild);
}

async function endDrag(t, target) {
  let optimistic = false; // t is shown in its new lane before the server has it
  try {
    if (target.action.kind === "reorder") {
      await reorder(t, target.lane, target.index, true);
    } else if (target.action.kind === "rework") {
      renderBoard();
      const feedback = closable(t)
        ? await ask({ title: "Ask for changes", body: "Claude continues in the same worktree with your message and runs again.", input: true, placeholder: "What should Claude do?", ok: "Send" })
        : await ask({ title: "Rework", body: "Claude continues in the same worktree with your rework notes.", input: true, placeholder: "What should change?", ok: "Rework" });
      if (feedback === null) return;
      await api("POST", `/api/todos/${t.id}/rework`, { feedback });
      toast(closable(t) ? "Sent to Claude" : "Sent for rework");
    } else if (target.action.undo) {
      // Shown in its new lane at once; sent once the toast's Undo has passed.
      const lane = LANES.find((l) => l.key === target.lane);
      moveWithUndo(t, { verb: target.action.verb, status: target.action.status || lane.statuses[0], head: target.action.label,
        icon: { cancel: "draft", approve: "merge" }[target.action.verb] || "check" });
    } else {
      // Optimistic: show it in the new lane right away; the server event confirms.
      const lane = LANES.find((l) => l.key === target.lane);
      state.todos.set(t.id, { ...t, status: target.action.status || lane.statuses[0] });
      optimistic = true;
      renderBoard();
      await api("POST", `/api/todos/${t.id}/${target.action.verb}`);
      optimistic = false;
      toast(target.action.label);
      if (target.lane === "queued" && Number.isFinite(target.index)) await reorder(state.todos.get(t.id), "queued", target.index);
    }
  } catch (err) {
    // Undo the optimistic move: only the status, so events that came in
    // meanwhile stay. A failed reorder puts its own cards right (reorder).
    const cur = state.todos.get(t.id);
    if (optimistic && cur) state.todos.set(t.id, { ...cur, status: t.status });
    renderBoard();
    fail(err);
  }
}

// Reprioritise t for a drop at index among the lane's other cards. Only t
// changes: dropped among the prioritised cards it gets a priority between its
// neighbours, right after the last one that one plus 1, and among the cards
// without a priority (always ordered by age) it loses its own. Only when there
// is no free integer between the neighbours are the lane's prioritised cards
// renumbered 0..n; cards without a priority are never touched. inLane: t
// was already in this lane, so a drop where it was changes nothing.
async function reorder(t, laneKey, index, inLane = false) {
  if (!Number.isFinite(index)) return; // dropped on a lane tab: no position
  const all = laneItems()[laneKey];
  // The card sits where it was dropped (carddrag.js): a drop that changes
  // nothing still renders, to glide it back to its place in the order.
  if (inLane && all.findIndex((x) => x.id === t.id) === index) { renderBoard(); return; }
  const prio = all.filter((x) => x.id !== t.id && hasPriority(x));
  const k = prio.length;
  let changes; // [task, new priority (null: none)]
  if (index > k || (k === 0 && index > 0)) {
    changes = [[t, null]];
  } else if (index === k) {
    changes = [[t, k ? prio[k - 1].priority + 1 : 0]];
  } else {
    // Between prio[index - 1] and prio[index]; at the top, keep to 0 and up
    // (or above the urgent ones, below 0).
    const hi = prio[index].priority;
    const lo = index > 0 ? prio[index - 1].priority : hi < 0 ? hi - 2 : Math.min(hi, 0) - 1;
    if (hi - lo >= 2) {
      changes = [[t, lo + Math.floor((hi - lo) / 2)]];
    } else {
      // Renumbered, the urgent ones (and t among them) stay below 0.
      const order = [...prio];
      order.splice(index, 0, t);
      const urgent = order.findLastIndex((x) => x !== t && x.priority < 0) + 1;
      changes = order.map((x, i) => [x, i - urgent]);
    }
  }
  changes = changes.filter(([x, p]) => (x.priority ?? null) !== p);
  if (!changes.length) { renderBoard(); return; }
  for (const [x, p] of changes) state.todos.set(x.id, { ...state.todos.get(x.id), priority: p });
  renderBoard();
  const failed = (await Promise.allSettled(changes.map(([x, p]) => api("PATCH", `/api/todos/${x.id}`, { priority: p }))))
    .find((r) => r.status === "rejected");
  if (!failed) return;
  // Some PATCHes may have landed and others not: show every changed card as
  // the server has it now, or failing that its old priority.
  await Promise.all(changes.map(async ([x]) => {
    const fresh = await api("GET", `/api/todos/${x.id}`).catch(() => null);
    const cur = state.todos.get(x.id);
    if (cur) state.todos.set(x.id, fresh?.id === x.id ? fresh : { ...cur, priority: x.priority });
  }));
  renderBoard();
  throw failed.reason;
}

// Priority as the forms offer it: None, High or Urgent, over the numeric
// priority the queue sorts by (lower first, then those without one). Urgent
// is below 0 and High 0 and up, where dragging cards on the board keeps them.
export const PRIORITY_LEVELS = { "": "None", high: "High", urgent: "Urgent" };
export const priorityLevel = (n) => n == null || n === "" ? "" : Number(n) < 0 ? "urgent" : "high";
// levelPriority is the numeric priority for a level picked in a form; cur
// (the task's own) is kept while its level stays the same, so the task keeps
// its place among the others of that level.
export function levelPriority(level, cur = null) {
  if (cur != null && priorityLevel(cur) === level) return cur;
  return level === "urgent" ? -1 : level === "high" ? 0 : null;
}

// One more than the highest priority among t's lane (0 if none): the slot
// right after the prioritised cards, where a task given a priority goes.
export function nextPriority(t) {
  const lane = LANES.find((l) => l.statuses.includes(t.status));
  const ps = (lane ? laneItems(true)[lane.key] : []).filter((x) => x.id !== t.id && hasPriority(x)).map((x) => x.priority);
  return ps.length ? Math.max(...ps) + 1 : 0;
}

export async function clearPriority(t) {
  await api("PATCH", `/api/todos/${t.id}`, { priority: null });
  toast("Priority cleared");
}
