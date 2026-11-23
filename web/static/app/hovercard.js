import { $, h } from "./dom.js";
import { desktop } from "./viewport.js";
import { state } from "./state.js";
import { todoRow } from "./branches.js";
import { drag, pendDrag } from "./dnd.js";

// ------------------------------------------------------------------ hover card
// On the desktop board with a mouse, resting on a card shows it widened to a
// readable width, floating over the neighbouring columns. The columns don't
// change: the widened card is a copy, fixed over the card, outside the
// scrolling column so nothing clips it. It is rendered from the task like the
// card, with its own handlers, and follows the card's live updates. A press
// on it starts a drag of the card itself; a click on it is passed to the card
// (modifier keys too), so it does what a click on the card does. Its own
// controls (chips, links, the pick box) work as the card's do.
//
// It stays out of the way of selecting cards:
//   - It opens only when the pointer rests: every move restarts the wait, so
//     crossing cards opens nothing.
//   - It doesn't open on a card's controls (its pick box included), nor at
//     all while drafts are picked, so you can go down a column ticking boxes.
//   - It stays open while the pointer is inside the card's own area (the
//     card's rectangle), except over the card's pick box, where it closes so
//     the real box is under the pointer. Once the pointer is outside that
//     area, over another card, the copy closes and the wait starts for that
//     card, even where the copy still covers it. Over parts of the copy with
//     no card below, or on the copy's own pick box, it stays. Once the copy
//     has been scrolled (reading a long text), it stays open until the
//     pointer leaves the copy.
//   - A draft's copy isn't shifted up to line up its buttons with the card's,
//     so its pick box stays over the card's.
const HOVER_DELAY = 500, HOVER_W = 460, HOVER_MAX_H = 360, HOVER_MARGIN = 12;
const canHover = matchMedia("(hover: hover) and (pointer: fine)");
// A card's controls: on them the copy doesn't open, so a button doesn't move
// away under the pointer reaching for it. (The title is a button too, but a
// press anywhere on the card opens the task.)
const CARD_CTRL = ".card-actions, a, button:not(.todo-open), .todo-pick";
const CARD_CTRL_HOVER = ".card-actions:hover, a:hover, button:not(.todo-open):hover, .todo-pick:hover";
// A click on these in the copy runs the copy's own handler; anywhere else it
// is passed to the card.
const COPY_CTRL = "a, button:not(.todo-open), label, input";
const inside = (r, x, y) => x >= r.left && x < r.right && y >= r.top && y < r.bottom;
export const hoverCard = {
  el: null, card: null, row: null, rect: null, waiting: null, timer: 0, watch: null, reading: false,
  want(card, target) {
    if (card && target?.closest(CARD_CTRL)) { if (card === this.waiting) this.cancel(); return; }
    if (card === this.card || card === this.waiting) return;
    this.cancel();
    if (!card || !desktop.matches || !canHover.matches || drag.active || drag.pending) return;
    if (document.body.classList.contains("picking")) return;
    this.waiting = card;
    this.wait();
  },
  // (Re)starts the wait for the card waited on: the copy opens once the
  // pointer has rested this long.
  wait() {
    const card = this.waiting;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      this.waiting = null;
      // Live updates may have re-rendered the board meanwhile: take today's card.
      const now = card.isConnected ? card : $(`#board .todo[data-id="${CSS.escape(card.dataset.id)}"]`);
      if (now) this.open(now);
    }, HOVER_DELAY);
  },
  cancel() { clearTimeout(this.timer); this.waiting = null; },
  open(card) {
    if (drag.active || drag.pending || !card.matches(":hover") || card.querySelector(CARD_CTRL_HOVER)) return;
    if (document.body.classList.contains("picking")) return;
    this.close();
    const r = card.getBoundingClientRect();
    const w = Math.max(r.width, Math.min(HOVER_W, innerWidth - 2 * HOVER_MARGIN));
    const sub = card.querySelector(".todo-sub");
    // Already wide and nothing cut off: there is nothing more to show.
    if (w <= r.width + 1 && !(sub && sub.scrollHeight > sub.clientHeight + 1)) return;
    this.card = card;
    this.el = h("div", { class: "todo-float", "aria-hidden": "true",
      onpointerleave: (e) => { if (e.pointerType !== "touch") this.close(); },
      onpointerdown: (e) => { if (this.card) pendDrag(this.card, e); },
      onscroll: () => { if (this.el?.scrollTop > 0) this.reading = true; },
      // A tall copy scrolls itself; at its end (or if it fits) the wheel
      // scrolls the column underneath, as it would without the copy.
      onwheel: (e) => {
        const el = this.el;
        if (el && !e.deltaX && (e.deltaY < 0 ? el.scrollTop > 0 : el.scrollTop + el.clientHeight < el.scrollHeight - 1)) return;
        e.preventDefault();
        const body = this.card?.closest(".col-body"); this.close(); body?.scrollBy({ top: e.deltaY, left: e.deltaX });
      },
      oncontextmenu: (e) => e.preventDefault(),
    });
    // A click on the copy is a click on the card, with the same keys held.
    this.el.addEventListener("click", (e) => {
      if (!this.card || e.target.closest(COPY_CTRL)) return;
      e.stopPropagation();
      e.preventDefault();
      const { clientX, clientY, screenX, screenY, button, buttons, detail, ctrlKey, metaKey, shiftKey, altKey } = e;
      this.card.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, composed: true, view: window,
        clientX, clientY, screenX, screenY, button, buttons, detail, ctrlKey, metaKey, shiftKey, altKey }));
    }, true);
    document.body.append(this.el);
    this.render();
    this.place(r, w);
    this.watch = new MutationObserver(() => this.render());
    this.observe();
  },
  observe() {
    this.watch.disconnect();
    this.watch.observe(this.card, { attributes: true, attributeFilter: ["class"], childList: true, subtree: true, characterData: true });
  },
  // Copies the card: fresh content from the task, the card's classes (status,
  // selection, keyboard cursor). The row element stays, so a press that
  // started on it still ends in a click.
  render() {
    const t = this.card && state.todos.get(this.card.dataset.id);
    if (!t) return this.close();
    const fresh = todoRow(t);
    if (!this.row) {
      this.row = fresh;
      this.row.removeAttribute("data-id"); // board code looks cards up by data-id; this isn't one
      this.el.append(this.row);
    } else {
      this.row.replaceChildren(...fresh.childNodes);
    }
    // The copy is aria-hidden: keep its controls out of the tab order.
    for (const el of this.row.querySelectorAll("button, a")) el.tabIndex = -1;
    this.row.className = this.card.className;
    this.row.style.minHeight = `${this.card.offsetHeight}px`; // always covers the card
  },
  place(r, w) {
    const el = this.el;
    this.rect = r;
    let left = r.left;
    if (left + w > innerWidth - HOVER_MARGIN) left = r.right - w; // rightmost column: grow to the left
    left = Math.max(HOVER_MARGIN, Math.min(left, innerWidth - HOVER_MARGIN - w));
    el.style.width = `${w}px`;
    // Long texts don't cover the whole board: past a cap the copy scrolls.
    el.style.maxHeight = `${Math.max(r.height, Math.min(HOVER_MAX_H, innerHeight - 2 * HOVER_MARGIN))}px`;
    el.style.left = `${left}px`;
    el.style.top = `${r.top}px`;
    const hgt = el.offsetHeight;
    let top = r.top;
    // The copy's buttons where the card's are, so they don't jump under the
    // pointer, as far as the copy still covers the card and stays on screen.
    // A draft's copy isn't moved: its pick box stays over the card's.
    const mine = this.card.querySelector(".card-actions"), theirs = this.row.querySelector(".card-actions");
    if (mine && theirs && !this.card.querySelector(".todo-pick")) {
      top += mine.getBoundingClientRect().top - theirs.getBoundingClientRect().top;
      top = Math.min(r.top, Math.max(top, r.bottom - hgt));
    }
    top = Math.max(HOVER_MARGIN, Math.min(top, innerHeight - HOVER_MARGIN - hgt));
    el.style.top = `${top}px`;
  },
  // After the board re-renders: follow the new card, or close if it moved.
  sync() {
    if (!this.card || this.card.isConnected) return; // same card: the observer follows it
    const card = $(`#board .todo[data-id="${CSS.escape(this.card.dataset.id)}"]`);
    // Gone, or gliding to a new place (FLIP): the pointer is no longer on it.
    if (!card || drag.active || card.getAnimations().length) return this.close();
    const r = card.getBoundingClientRect();
    if (Math.abs(r.left - this.rect.left) > 1 || Math.abs(r.top - this.rect.top) > 1 || Math.abs(r.width - this.rect.width) > 1) return this.close();
    this.card = card;
    this.observe();
    this.render();
    this.place(r, parseFloat(this.el.style.width));
  },
  // follow applies the rule at the top to a pointer move while the copy is open.
  follow(e) {
    if (!this.el || e.pointerType === "touch" || e.buttons) return;
    const x = e.clientX, y = e.clientY;
    if (inside(this.rect, x, y)) {
      const pick = this.card.querySelector(".todo-pick");
      if (pick && inside(pick.getBoundingClientRect(), x, y)) this.close();
      return;
    }
    // Reading a scrolled copy, or reaching for its own pick box (off the
    // card's when the copy had to move to stay on screen).
    if ((this.reading || e.target.closest?.(".todo-pick")) && this.el.contains(e.target)) return;
    if (!this.el.contains(e.target)) return; // off the copy: its pointerleave closes it
    const under = document.elementsFromPoint(x, y).find((el) => !this.el.contains(el));
    const card = under?.closest("#board .todo[data-id]");
    if (!card || card === this.card) return;
    this.close();
    this.want(card, under);
  },
  close() {
    this.cancel();
    this.watch?.disconnect();
    this.el?.remove();
    this.el = this.card = this.row = this.watch = null;
    this.reading = false;
  },
};
$("#board").addEventListener("pointerover", (e) => {
  if (e.pointerType === "touch" || e.buttons) return;
  hoverCard.want(e.target.closest(".todo[data-id]"), e.target);
});
// Moving on: the copy opens only once the pointer rests.
$("#board").addEventListener("pointermove", (e) => {
  if (e.pointerType === "touch" || !hoverCard.waiting) return;
  if (e.buttons) hoverCard.cancel(); else hoverCard.wait();
});
document.addEventListener("pointermove", (e) => hoverCard.follow(e));
$("#board").addEventListener("pointerout", (e) => {
  const card = e.target.closest(".todo[data-id]");
  if (card && card === hoverCard.waiting && !card.contains(e.relatedTarget)) hoverCard.cancel();
});
addEventListener("scroll", (e) => { if (hoverCard.el && !hoverCard.el.contains(e.target)) hoverCard.close(); }, true);
addEventListener("resize", () => hoverCard.close());
addEventListener("blur", () => hoverCard.close());
desktop.addEventListener("change", () => hoverCard.close());
