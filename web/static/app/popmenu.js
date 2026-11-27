import { $$, h, ico } from "./dom.js";
import { attempt } from "./dialogs.js";

// ------------------------------------------------------------------ pop-up menu
// Its own module, with no imports from the app, so planning.html (app/planning.js)
// can use it too.

// popMenu drops a menu down from anchor (a button), looking like the project
// menu (.foot-menu-list). groups are lists of choices, separated by a line
// (empty ones are skipped); a group with a heading property shows it above
// its choices. A choice is [icon, label, fn] or
// [icon, label, fn, { disabled, note, danger, title, keys, tone, checked, keep }]: icon is an
// icon's name or an element; checked makes it a menuitemcheckbox, ticked or not; a disabled one shows why
// (note, or title as its tooltip); keys is its keyboard shortcut, shown at the end;
// tone ("running", "ok" or "bad") colors its icon and note with how it last went;
// keep runs fn with the menu left open, to pick more than one (filters): groups
// is then a function, called again after fn to show the choices as they are now.
// Arrow keys move, Enter picks, Esc or a click outside closes it and
// focus returns to anchor. Resolves once the picked choice has run.
let popOpen = null; // the open menu's close function
export function popMenu(anchor, groups, { heading = "" } = {}) {
  const wasOpen = popOpen?.anchor === anchor;
  popOpen?.(false);
  if (wasOpen) return Promise.resolve(); // a second click on the button closes it
  const groupsNow = () => (typeof groups === "function" ? groups() : groups).filter((g) => g?.length);
  if (!groupsNow().length) return Promise.resolve();
  let picked = null, done, quiet = false;
  const rows = () => groupsNow().flatMap((g, i) => [
    i ? h("hr", { class: "menu-sep", role: "separator" }) : null,
    g.heading ? h("div", { class: "pop-menu-head", role: "presentation" }, g.heading) : null,
    ...g.map(([icon, label, fn, { disabled = false, note = "", danger = false, title = "", keys = "", tone = "", checked, keep = false } = {}]) => h("button", {
      type: "button", role: checked === undefined ? "menuitem" : "menuitemcheckbox", "aria-checked": checked === undefined ? null : String(!!checked),
      class: [danger && "danger", tone && `tone-${tone}`].filter(Boolean).join(" ") || null,
      disabled, title: title || note || null, tabindex: -1,
      onclick: keep ? (e) => pickKept(fn, e.currentTarget) : () => { picked = fn; close(true); },
    }, typeof icon === "string" ? ico(icon) : icon, h("span", { class: "menu-label" }, label), note ? h("em", { class: "menu-note" }, note) : null,
    checked ? ico("check") : null,
    keys ? h("kbd", { class: "menu-key", "aria-hidden": "true" }, keys) : null)),
  ]);
  const el = h("div", { class: "foot-menu-list pop-menu", role: "menu", popover: "manual", "aria-label": heading || anchor.getAttribute("aria-label") || "Actions" },
    heading ? h("div", { class: "pop-menu-head", "aria-hidden": "true" }, heading) : null, ...rows());
  // pickKept runs a keep choice and shows the choices again, the focus on the
  // same row. What fn redraws behind the menu may scroll the page: that doesn't close it.
  async function pickKept(fn, row) {
    const at = items().indexOf(row);
    quiet = true;
    try {
      await attempt(fn)();
      if (popOpen !== close) return;
      const head = heading ? el.firstElementChild : null;
      el.replaceChildren(...[head, ...rows()].filter(Boolean));
      const list = items();
      list[Math.min(Math.max(at, 0), list.length - 1)]?.focus();
    } finally {
      requestAnimationFrame(() => requestAnimationFrame(() => { quiet = false; }));
    }
  }
  const items = () => $$("button:not(:disabled)", el);
  const onKey = (e) => {
    e.stopPropagation(); // no app shortcuts while it's open (Enter and Space still pick the focused row)
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(true); return; }
    if (e.key === "Tab") { e.preventDefault(); close(true); return; }
    const list = items(), at = list.indexOf(document.activeElement);
    const to = { ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: list.length - 1 }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    list[(to + list.length) % list.length]?.focus();
  };
  const onDown = (e) => { if (!el.contains(e.target) && !anchor.contains(e.target)) close(true); };
  const onScroll = (e) => { if (!quiet && !el.contains(e.target)) close(false); };
  const onResize = () => close(false);
  function close(refocus) {
    if (popOpen !== close) return;
    popOpen = null;
    document.removeEventListener("keydown", onKey, true);
    document.removeEventListener("pointerdown", onDown, true);
    document.removeEventListener("scroll", onScroll, true);
    window.removeEventListener("resize", onResize);
    anchor.setAttribute("aria-expanded", "false");
    el.remove();
    if (refocus && anchor.isConnected) anchor.focus();
    done();
  }
  close.anchor = anchor;
  popOpen = close;
  // Inside the anchor's modal dialog, if it is in one: everything outside an
  // open modal is inert, so a menu in <body> would let clicks through to the sheet.
  const modal = anchor.closest("dialog");
  (modal?.matches(":modal") ? modal : document.body).append(el);
  el.showPopover?.(); // the top layer: above the task sheet, the preview and open dialogs
  // Under the button, right-aligned to it; above it when there's more room there.
  const r = anchor.getBoundingClientRect(), vh = window.innerHeight, vw = document.documentElement.clientWidth, gap = 4, edge = 8;
  const below = vh - r.bottom - gap - edge, above = r.top - gap - edge;
  const up = el.offsetHeight > below && above > below;
  el.style.maxHeight = `${Math.max(120, up ? above : below)}px`;
  el.style.right = `${Math.max(edge, vw - r.right)}px`;
  if (up) el.style.bottom = `${vh - r.top + gap}px`; else el.style.top = `${r.bottom + gap}px`;
  anchor.setAttribute("aria-expanded", "true");
  document.addEventListener("keydown", onKey, true);
  document.addEventListener("pointerdown", onDown, true);
  document.addEventListener("scroll", onScroll, true);
  window.addEventListener("resize", onResize);
  items()[0]?.focus();
  // Run the choice only after the menu closed: it may open another (a submenu).
  return new Promise((r) => { done = r; }).then(() => picked && attempt(picked)());
}
