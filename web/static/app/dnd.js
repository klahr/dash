import { $, $$, h } from "./dom.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { EASE, ask, fail, leave, reduceMotion, toast } from "./dialogs.js";
import { LANES, state } from "./state.js";
import { approvable, hasPriority, laneItems, renderBoard } from "./branches.js";
import { hoverCard } from "./hovercard.js";

// ------------------------------------------------------------------ drag & drop
// Drag a card to another lane to act on it, or within a lane to reorder.
// Mouse: drag after a few pixels. Touch: long-press, then drag (a quick
// swipe still scrolls). Lane tabs are drop targets on phones.
export const drag = { active: false, pending: null, t: null, card: null, ghost: null, slot: null, target: null, dx: 0, dy: 0, suppressClick: false };

// What dropping a todo of a status into a lane does (null = not allowed).
function dropAction(t, lane) {
  const from = t.status;
  const to = LANES.find((l) => l.key === lane);
  if (!to) return null;
  if (to.statuses.includes(from)) return ["draft", "queued", "review"].includes(lane) && from !== "failed" ? { kind: "reorder" } : null;
  if (t.reviewing) return null; // locked until the review finishes
  const go = (verb, label) => ({ kind: "action", verb, label });
  if (lane === "queued" && ["draft", "failed"].includes(from)) return go("enqueue", "Added to queue");
  if (lane === "queued" && from === "review") return { kind: "rework" };
  if (lane === "draft" && ["queued", "review", "failed"].includes(from)) {
    return go("cancel", t.pr?.state === "OPEN" ? `Moved to drafts; pull request #${t.pr.number} closed` : "Moved to drafts");
  }
  // A task whose pull request is open is approved (and merged) on GitHub.
  if (lane === "merging" && from === "review" && approvable(t)) return go("approve", "Approved — merging");
  return null;
}

$("#board").addEventListener("pointerdown", (e) => pendDrag(e.target.closest(".todo[data-id]"), e));

// A press on a card (or on its widened hover copy, which passes the board's
// card) may become a drag once the pointer moves.
export function pendDrag(card, e) {
  if (!card || e.button > 0) return;
  const t = state.todos.get(card.dataset.id);
  if (!t) return;
  drag.pending = { card, t, x: e.clientX, y: e.clientY, touch: e.pointerType !== "mouse" };
  if (drag.pending.touch) {
    drag.pending.timer = setTimeout(() => drag.pending && startDrag(drag.pending.x, drag.pending.y), 320);
  }
}

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
  hoverCard.close();
  const r = card.getBoundingClientRect();
  drag.active = true;
  drag.t = t;
  drag.card = card;
  // Clamped: a drag from the widened hover copy can start outside the card.
  drag.dx = Math.max(0, Math.min(x - r.left, r.width - 16));
  drag.dy = Math.max(0, Math.min(y - r.top, r.height - 8));
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
      await reorder(t, target.lane, target.index, true);
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
  if (inLane && all.findIndex((x) => x.id === t.id) === index) return;
  const prio = all.filter((x) => x.id !== t.id && hasPriority(x));
  const k = prio.length;
  let changes; // [task, new priority (null: none)]
  if (index > k || (k === 0 && index > 0)) {
    changes = [[t, null]];
  } else if (index === k) {
    changes = [[t, k ? prio[k - 1].priority + 1 : 0]];
  } else {
    // Between prio[index - 1] and prio[index]; at the top, keep to 0 and up.
    const hi = prio[index].priority;
    const lo = index > 0 ? prio[index - 1].priority : Math.min(hi, 0) - 1;
    if (hi - lo >= 2) {
      changes = [[t, lo + Math.floor((hi - lo) / 2)]];
    } else {
      const order = [...prio];
      order.splice(index, 0, t);
      changes = order.map((x, i) => [x, i]);
    }
  }
  changes = changes.filter(([x, p]) => (x.priority ?? null) !== p);
  if (!changes.length) return;
  for (const [x, p] of changes) state.todos.set(x.id, { ...state.todos.get(x.id), priority: p });
  renderBoard();
  await Promise.all(changes.map(([x, p]) => api("PATCH", `/api/todos/${x.id}`, { priority: p })));
}

// The task form's priority field: empty is no priority.
export function formPriority(v) {
  v = String(v).trim();
  if (!v) return null;
  const n = Number(v);
  if (!Number.isInteger(n)) throw new Error("Priority must be a whole number, or empty for none.");
  return n;
}

// One more than the highest priority among t's lane (0 if none): the slot
// right after the prioritised cards, where a task given a priority goes.
export function nextPriority(t) {
  const lane = LANES.find((l) => l.statuses.includes(t.status));
  const ps = (lane ? laneItems()[lane.key] : []).filter((x) => x.id !== t.id && hasPriority(x)).map((x) => x.priority);
  return ps.length ? Math.max(...ps) + 1 : 0;
}

export async function clearPriority(t) {
  await api("PATCH", `/api/todos/${t.id}`, { priority: null });
  toast("Priority cleared");
}
