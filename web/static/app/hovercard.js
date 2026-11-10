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
// on it starts a drag of the card itself.
const HOVER_DELAY = 300, HOVER_W = 460, HOVER_MAX_H = 360, HOVER_MARGIN = 12;
const canHover = matchMedia("(hover: hover) and (pointer: fine)");
// A card's controls: on them the copy doesn't open, so a button doesn't move
// away under the pointer reaching for it. (The title is a button too, but a
// press anywhere on the card opens the task.)
const CARD_CTRL = ".card-actions, a, button:not(.todo-open)";
const CARD_CTRL_HOVER = ".card-actions:hover, a:hover, button:not(.todo-open):hover";
export const hoverCard = {
  el: null, card: null, row: null, rect: null, waiting: null, timer: 0, watch: null,
  want(card, target) {
    if (card && target?.closest(CARD_CTRL)) { if (card === this.waiting) this.cancel(); return; }
    if (card === this.card || card === this.waiting) return;
    this.cancel();
    if (!card || !desktop.matches || !canHover.matches || drag.active || drag.pending) return;
    this.waiting = card;
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
    // pointer, as far as the copy still covers the card (it closes only when
    // the pointer leaves it) and stays on screen.
    const mine = this.card.querySelector(".card-actions"), theirs = this.row.querySelector(".card-actions");
    if (mine && theirs) {
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
  close() {
    this.cancel();
    this.watch?.disconnect();
    this.el?.remove();
    this.el = this.card = this.row = this.watch = null;
  },
};
$("#board").addEventListener("pointerover", (e) => {
  if (e.pointerType === "touch" || e.buttons) return;
  hoverCard.want(e.target.closest(".todo[data-id]"), e.target);
});
$("#board").addEventListener("pointerout", (e) => {
  const card = e.target.closest(".todo[data-id]");
  if (card && card === hoverCard.waiting && !card.contains(e.relatedTarget)) hoverCard.cancel();
});
addEventListener("scroll", (e) => { if (hoverCard.el && !hoverCard.el.contains(e.target)) hoverCard.close(); }, true);
addEventListener("resize", () => hoverCard.close());
addEventListener("blur", () => hoverCard.close());
desktop.addEventListener("change", () => hoverCard.close());
