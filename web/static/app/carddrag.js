import { h } from "./dom.js";
import { EASE, reduceMotion } from "./dialogs.js";
import { edgeScroller, glide, restRect } from "./glide.js";

// ------------------------------------------------------------------ dragging a card
// The drag mechanics the task board (dnd.js) and Planning (planning.js)
// share. A mouse drags after a few pixels, a finger after a long press (a
// quick swipe still scrolls). A ghost of the card follows the pointer. The
// card leaves its place for a gap (the slot) where it would land, and the
// cards around it glide aside (glide.js). Esc, or a cancelled pointer, flies
// the ghost back home.
//
// makeCardDrag returns the drag; attach(root) lets presses on root's cards
// start one. What differs per board are the hooks:
//   pick(card)      what is dragged (null: the card can't be);
//   start(drag)     the drag began (marks drop targets);
//   move(x, y)      the pointer moved: find the target and place the gap
//                   with drag.place(parent, ref);
//   drop(x, y)      the card was let go: a function that acts on the drop,
//                   run once the drag is cleaned up, or null to put it back;
//   end()           the drag is over, dropped or not (clears the marks);
//   abort()         the card went back to its place.
// The classes: item (what a press starts on), li (the list item the gap
// stands in for), list (where a dropped card takes the gap's place), ghost
// and slot; slotHeight(r) is the gap's height for a card of box r.
const LONG_PRESS = 320; // ms a finger holds still before it drags
const TOUCH_SLOP = 10; // px a finger moves before the press is a scroll
const MOUSE_SLOP = 6; // px a mouse moves before the press is a drag

export function makeCardDrag(o) {
  const d = { active: false, pending: null, data: null, card: null, li: null, ghost: null, slot: null, slotOff: null,
    dx: 0, dy: 0, aborting: false, suppressClick: false, press, place, attach };

  // press: a press on card, which becomes a drag once the pointer moves
  // (or, on touch, holds still).
  function press(card, e) {
    if (!card || e.button > 0) return;
    const data = o.pick(card);
    if (data == null) return;
    const p = d.pending = { card, data, x: e.clientX, y: e.clientY, touch: e.pointerType !== "mouse" };
    if (p.touch) p.timer = setTimeout(() => d.pending === p && start(p.x, p.y), LONG_PRESS);
  }

  function cancelPending() {
    if (d.pending?.timer) clearTimeout(d.pending.timer);
    d.pending = null;
  }

  function start(x, y) {
    const { card, data } = d.pending;
    cancelPending();
    if (!card.isConnected) return;
    const r = card.getBoundingClientRect();
    Object.assign(d, { active: true, data, card, li: card.closest(o.li) });
    // Clamped: a drag from the task board's widened hover copy can start outside the card.
    d.dx = Math.max(0, Math.min(x - r.left, r.width - 16));
    d.dy = Math.max(0, Math.min(y - r.top, r.height - 8));
    d.ghost = card.cloneNode(true);
    d.ghost.classList.add(o.ghost);
    d.ghost.style.width = `${r.width}px`;
    document.body.append(d.ghost);
    // The gap starts where the card was, as tall. It fades in once; moved
    // later, it shows at once.
    d.slot = h("li", { class: o.slot, "aria-hidden": "true" });
    d.slot.style.height = `${o.slotHeight ? o.slotHeight(r) : r.height}px`;
    d.slot.addEventListener("animationend", () => d.slot?.classList.add("placed"), { once: true });
    card.classList.add("dragging");
    if (d.li) {
      d.li.before(d.slot);
      d.li.classList.add("drag-src");
      const sr = d.slot.getBoundingClientRect();
      d.slotOff = { x: r.left - sr.left, y: r.top - sr.top }; // where the card sits by its slot
    }
    document.body.classList.add("is-dragging");
    navigator.vibrate?.(12);
    o.start?.(d);
    move(x, y);
  }

  // Held near the top or bottom, the column (or the page, on a phone) scrolls.
  const edge = edgeScroller((x, y) => d.active && move(x, y));

  function move(x, y) {
    if (d.aborting) return;
    edge.move(x, y);
    d.ghost.style.transform = `translate(${x - d.dx}px, ${y - d.dy}px) rotate(1.2deg) scale(1.02)`;
    o.move(x, y);
  }

  // place moves the gap to before ref in parent (nowhere, without a
  // parent); what's around where it was and where it goes glides.
  // The gap and the dragged card (hidden) take no place of their own, so a
  // ref that is either of them means the node after it, and a gap already
  // there stays put: moving it would restart its fade-in.
  function place(parent, ref = null) {
    const s = d.slot;
    const skip = (n) => { while (n && (n === s || n === d.li)) n = n.nextSibling; return n; };
    ref = skip(ref);
    if (parent ? s.parentNode === parent && skip(s.nextSibling) === ref : !s.parentNode) return;
    const around = new Set();
    for (const p of [s.parentNode, parent]) {
      if (!p) continue;
      for (const el of p.children) around.add(el);
      // What follows the list in its column (Add all to queue…) moves too.
      for (const el of p.parentNode?.children || []) around.add(el);
    }
    around.delete(s);
    around.delete(d.li);
    glide([...around], () => parent ? parent.insertBefore(s, ref) : s.remove());
  }

  function cleanup() {
    edge.stop();
    d.ghost?.remove();
    // The card takes the gap's place, so the board drawn after a drop
    // glides it (and its neighbours) on from where they are.
    if (d.li && d.slot?.parentNode?.matches(o.list)) d.slot.replaceWith(d.li);
    else d.slot?.remove();
    d.li?.classList.remove("drag-src");
    d.card?.classList.remove("dragging");
    document.body.classList.remove("is-dragging");
    o.end?.();
    Object.assign(d, { active: false, aborting: false, data: null, card: null, li: null, ghost: null, slot: null });
    // The click that ends a mouse drag isn't a click on the card.
    d.suppressClick = true;
    setTimeout(() => { d.suppressClick = false; }, 0);
  }

  function abort() {
    if (d.aborting) return;
    d.aborting = true;
    edge.stop();
    // The gap closes where it is and opens again at home, and the ghost flies back into it.
    if (d.li) place(d.li.parentNode, d.li);
    const g = d.ghost;
    const done = () => { cleanup(); o.abort?.(); };
    if (!g || reduceMotion.matches) { done(); return; }
    let home = d.card.getBoundingClientRect();
    if (d.li) { const s = restRect(d.slot); home = { left: s.left + d.slotOff.x, top: s.top + d.slotOff.y }; }
    g.animate([{ transform: g.style.transform }, { transform: `translate(${home.left}px, ${home.top}px)` }], { duration: 200, easing: EASE, fill: "forwards" });
    setTimeout(done, 190);
  }

  function end(x, y) {
    if (d.aborting) return;
    const then = o.drop(x, y);
    if (!then) { abort(); return; }
    cleanup();
    then();
  }

  document.addEventListener("pointermove", (e) => {
    if (d.active) { move(e.clientX, e.clientY); return; }
    const p = d.pending;
    if (!p) return;
    const dist = Math.hypot(e.clientX - p.x, e.clientY - p.y);
    if (p.touch) { if (dist > TOUCH_SLOP) cancelPending(); return; } // it's a scroll
    if (dist > MOUSE_SLOP) start(e.clientX, e.clientY);
  });
  // While dragging on touch, stop the page from scrolling under the finger.
  document.addEventListener("touchmove", (e) => { if (d.active) e.preventDefault(); }, { passive: false });
  document.addEventListener("pointerup", (e) => { if (d.active) end(e.clientX, e.clientY); cancelPending(); });
  document.addEventListener("pointercancel", () => { if (d.active) abort(); cancelPending(); });
  document.addEventListener("keydown", (e) => { if (d.active && e.key === "Escape") { e.stopPropagation(); abort(); } }, true);
  // A drag ends with a click; swallow it so the card doesn't open.
  document.addEventListener("click", (e) => {
    if (!d.suppressClick) return;
    e.stopPropagation(); e.preventDefault();
    d.suppressClick = false;
  }, true);

  // attach lets a press on one of root's cards start a drag.
  function attach(root) {
    root.addEventListener("pointerdown", (e) => press(e.target.closest?.(o.item), e));
    // A long press opens no menu on the card it drags.
    root.addEventListener("contextmenu", (e) => { if (d.active || d.pending?.touch) e.preventDefault(); });
  }

  return d;
}
