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

// ------------------------------------------------------------------ scrolling at the edges
// A card held near the top or bottom of what scrolls (its column on a
// desktop, the page on a phone) scrolls it, faster the nearer the edge, for
// as long as it is held there: a finger that stops still keeps scrolling.
// On the page, the bars stuck over it (top bar, lane tabs, nav, drop bar)
// are the edges. Over a lane tab or the drop bar it doesn't scroll: those
// are drop targets in themselves.
const EDGE = 72; // px from the edge where scrolling starts
const MAX_SPEED = 20; // px per frame, at the edge and past it
const BARS = ".topbar, .lanes, .nav, .drop-bar";
const HOLD = ".lane-tab, .drop-bar";

// edgeScroller returns { move(x, y), stop() }: call move on every pointer
// move of a drag and stop when it ends. onScroll(x, y) runs after each
// scroll, to place the drop where the content under the pointer now is.
export function edgeScroller(onScroll) {
  let at = null, raf = 0;
  const tick = () => {
    raf = 0;
    if (!at) return;
    const { x, y } = at;
    const under = document.elementFromPoint(x, y);
    if (under?.closest(HOLD)) return;
    const el = scrollerOf(under?.closest(".col")?.querySelector(".col-body") || under);
    const { top, bottom } = scrollArea(el);
    const v = y < top + EDGE ? -speed(top + EDGE - y) : y > bottom - EDGE ? speed(y - bottom + EDGE) : 0;
    if (!v) return;
    const was = el.scrollTop;
    el.scrollTop = was + v;
    if (el.scrollTop === was) return; // at the end already
    onScroll(x, y);
    if (!raf) raf = requestAnimationFrame(tick);
  };
  return {
    move(x, y) { at = { x, y }; if (!raf) raf = requestAnimationFrame(tick); },
    stop() { at = null; cancelAnimationFrame(raf); raf = 0; },
  };
}

function speed(d) {
  return Math.round(Math.min(MAX_SPEED, 2 + (d / EDGE) * MAX_SPEED));
}

// scrollerOf is the nearest element around el that scrolls up and down (the page if none).
function scrollerOf(el) {
  const page = document.scrollingElement || document.documentElement;
  for (; el && el !== document.body && el !== page; el = el.parentElement) {
    if (/(auto|scroll)/.test(getComputedStyle(el).overflowY) && el.scrollHeight > el.clientHeight + 1) return el;
  }
  return page;
}

// scrollArea is the part of el's box that shows on screen, top and bottom.
function scrollArea(el) {
  if (el !== (document.scrollingElement || document.documentElement)) {
    const r = el.getBoundingClientRect();
    return { top: Math.max(r.top, 0), bottom: Math.min(r.bottom, innerHeight) };
  }
  let top = 0, bottom = innerHeight;
  for (const bar of document.querySelectorAll(BARS)) {
    if (!/^(sticky|fixed)$/.test(getComputedStyle(bar).position)) continue;
    const r = bar.getBoundingClientRect();
    if (!r.height || r.bottom <= 0 || r.top >= innerHeight) continue;
    if (r.top + r.height / 2 < innerHeight / 2) top = Math.max(top, r.bottom);
    else bottom = Math.min(bottom, r.top);
  }
  return { top, bottom };
}
