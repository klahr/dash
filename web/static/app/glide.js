import { EASE, reduceMotion } from "./dialogs.js";

// ------------------------------------------------------------------ gliding into place
// While a card is dragged (the task board, dnd.js; Planning, planning.js) a
// gap the size of the card opens where it would land, and the cards around
// it glide aside to make room (FLIP: measure, change the layout, then play
// each element from where it was to where it is now).

// glide runs change() and slides els from where they were to where change
// left them. An element still gliding starts again from where it is now, so
// quick moves chain without jumps.
export function glide(els, change, duration = 180) {
  if (reduceMotion.matches) { change(); return; }
  const before = els.map((el) => [el, el.getBoundingClientRect()]);
  change();
  for (const [el, was] of before) {
    if (!el.isConnected) continue;
    for (const a of el.getAnimations()) if (a.id === "glide") a.cancel();
    const now = el.getBoundingClientRect();
    if (!now.height || !was.height) continue;
    const dx = was.left - now.left, dy = was.top - now.top;
    if (Math.abs(dx) + Math.abs(dy) < 1) continue;
    const a = el.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }], { duration, easing: EASE });
    a.id = "glide";
  }
}

// restRect is el's box where the layout puts it, without the move a glide
// gives it now: where a drop lands is decided by where the cards will be,
// not by where they are on their way there (which would flicker).
export function restRect(el) {
  const r = el.getBoundingClientRect();
  const t = getComputedStyle(el).transform;
  if (!t || t === "none") return r;
  const m = new DOMMatrixReadOnly(t);
  return new DOMRect(r.left - m.m41, r.top - m.m42, r.width, r.height);
}

// dropBefore is the first of items whose middle is below y (null: after the last).
export function dropBefore(items, y) {
  return items.find((el) => { const r = restRect(el); return y < r.top + r.height / 2; }) || null;
}
