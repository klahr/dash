import { $, h, ib, ico, setLabel } from "./dom.js";
import { attachBox } from "./attachments.js";

// ------------------------------------------------------------------ toasts & dialogs
export const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)");
export const EASE = "cubic-bezier(.2, .8, .2, 1)";

// leave() plays an exit animation (CSS class) before running done.
export function leave(el, cls, done, ms = 180) {
  if (reduceMotion.matches) return done();
  el.classList.add(cls);
  setTimeout(() => { el.classList.remove(cls); done(); }, ms);
}

// toast(msg) for info, toast(msg, true) for errors, or toast(msg, { tone, head, action: { label, run } }).
// Hovering or focusing a toast pauses its countdown.
const TOAST_ICONS = { info: "bell", ok: "check", error: "x", review: "eye", approval: "flag", pr: "merge" };
export function toast(msg, opts = {}) {
  if (typeof opts === "boolean") opts = { tone: opts ? "error" : "info" };
  const { tone = "info", head, action, icon = TOAST_ICONS[tone] || "bell" } = opts;
  const ms = opts.ms ?? (tone === "error" || action ? 7000 : 3500);
  let timer, start, left = ms, held = 0, gone = false;
  const close = () => { if (gone) return; gone = true; clearTimeout(timer); leave(t, "leaving", () => t.remove()); };
  const t = h("div", { class: `toast ${tone}`, role: tone === "error" ? "alert" : "status", style: `--ms: ${ms}ms` },
    h("span", { class: "toast-icon" }, ico(icon)),
    h("div", { class: "toast-body" }, head ? h("strong", {}, head) : null, msg ? h("span", {}, msg) : null),
    action ? h("button", { class: "toast-action", type: "button", onclick: () => { close(); attempt(action.run)(); } }, action.label) : null,
    ib({ class: "toast-close", type: "button", "aria-label": "Dismiss", title: "Dismiss", onclick: close }, "x"),
    h("span", { class: "toast-bar" }));
  const run = () => { start = Date.now(); timer = setTimeout(close, left); };
  const hold = () => { if (held++) return; clearTimeout(timer); left -= Date.now() - start; t.classList.add("held"); };
  const release = () => { if (--held) return; t.classList.remove("held"); run(); };
  t.addEventListener("pointerenter", hold);
  t.addEventListener("pointerleave", release);
  t.addEventListener("focusin", hold);
  t.addEventListener("focusout", release);
  const box = $("#toasts");
  box.append(t);
  while (box.children.length > 4) box.firstChild.remove();
  run();
  raiseToasts();
}

// Sheets are modal <dialog>s in the top layer, above any z-index, and they make
// everything outside them inert. So while a modal is open, #toasts moves into
// the topmost one, and as a popover="manual" it's (re)shown after it so it is
// drawn above its blurred ::backdrop. Without popover support, toasts stay as before.
const toastBox = $("#toasts");
const toastHome = document.createComment("toasts");
toastBox.before(toastHome);
const modals = []; // open modal dialogs, oldest first
function raiseToasts() {
  if (!toastBox.showPopover) return;
  for (let i = modals.length - 1; i >= 0; i--) if (!modals[i].isConnected || !modals[i].matches(":modal")) modals.splice(i, 1);
  const host = modals.at(-1);
  if (host ? toastBox.parentNode !== host : toastBox.previousSibling !== toastHome) {
    if (host) host.append(toastBox); else toastHome.after(toastBox); // moving hides the popover
  }
  // Re-adding puts it last in the top layer. With no modal open it stays out of
  // the top layer (a hidden popover still renders, as .toasts sets display).
  if (toastBox.matches(":popover-open")) toastBox.hidePopover();
  if (host && toastBox.children.length) toastBox.showPopover();
}
if (toastBox.showPopover) {
  toastBox.popover = "manual";
  new MutationObserver((records) => {
    for (const { target: d } of records) {
      if (d.localName !== "dialog") continue;
      const i = modals.indexOf(d);
      if (i >= 0) modals.splice(i, 1);
      if (d.open && d.matches(":modal")) modals.push(d);
    }
    raiseToasts();
  }).observe(document.body, { subtree: true, attributes: true, attributeFilter: ["open"] });
  // Hide the empty popover once its last toast is gone.
  new MutationObserver(() => { if (!toastBox.children.length) raiseToasts(); }).observe(toastBox, { childList: true });
}
export const fail = (e) => toast(e.message || String(e), true);
export const attempt = (fn) => async (...a) => { try { await fn(...a); } catch (e) { fail(e); } };
// Runs fn with btn disabled (and, given a label, showing it in place of the
// button's text, its last child) so a double click doesn't run it twice.
// Pass e.currentTarget, not e.target: a click on the button's icon targets the SVG.
export async function busy(btn, fn, { label } = {}) {
  const text = btn.lastChild;
  const before = text?.textContent;
  btn.disabled = true;
  if (label != null && text) text.textContent = label;
  try {
    return await fn();
  } finally {
    if (btn.isConnected) {
      btn.disabled = false;
      if (label != null && text) text.textContent = before;
    }
  }
}

// Optional illustration; disappears if the image file isn't there.
export function art(src, w, hgt) {
  const img = h("img", { class: "empty-art", src, alt: "", width: w, height: hgt, loading: "lazy", decoding: "async" });
  img.addEventListener("error", () => img.remove());
  return img;
}

export async function copy(text, msg) {
  try {
    await navigator.clipboard.writeText(text);
    toast(msg);
  } catch {
    await ask({ title: "Copy this", body: text, ok: "Done" }); // clipboard needs https or localhost
  }
}

// Copy button on rendered markdown code blocks (see renderMarkdown).
document.addEventListener("click", (e) => {
  const btn = e.target.closest?.(".code-copy");
  const code = btn?.closest(".code-block")?.querySelector("pre code");
  if (!code) return;
  e.preventDefault();
  e.stopPropagation();
  copy(code.textContent, "Copied");
});

const askAttach = attachBox($("#ask-attach"), $("#ask-input"));
// ask() shows a confirmation or a text question as a sheet. Resolves to the
// entered text (or true), or null when cancelled.
export function ask({ title, body = "", input = false, value = "", placeholder = "", ok = "OK", danger = false, attach = false, attachments = [] }) {
  const d = $("#ask-sheet");
  $("#ask-attach").hidden = !attach;
  askAttach.reset(attachments);
  $("#ask-title").textContent = title;
  $("#ask-body").textContent = body;
  $("#ask-body").hidden = !body;
  const field = $("#ask-input");
  field.hidden = !input;
  field.value = value;
  field.placeholder = placeholder;
  const okBtn = $("#ask-ok");
  setLabel(okBtn, danger ? "trash" : "check", ok);
  okBtn.className = `btn ${danger ? "danger" : "primary"}`;
  d.returnValue = "";
  d.showModal();
  if (input) field.focus();
  return new Promise((resolve) => {
    d.addEventListener("close", () => {
      if (d.returnValue !== "ok") return resolve(null);
      if (attach) return askAttach.settled().then(() => resolve({ text: field.value.trim(), attachments: askAttach.ids() }));
      resolve(input ? field.value.trim() : true);
    }, { once: true });
  });
}

// railLayout wires a sheet's .rail-layout: its sections down a rail and the
// chosen one beside them; on a narrow screen the rail is a strip across the
// top. The chosen section stays while the sheet is open (a re-render or a
// sheet opened over it doesn't move it) and is the first again next time
// (or the one marked data-rail-first).
// A section whose every child is hidden drops out of the rail. Arrow keys,
// Home and End move along the rail; only the chosen entry is a tab stop, so
// Tab goes on into its section. railOf(id).show(key) picks one; the dialog
// gets a "railchange" event (detail: the key) when the shown section changes.
const railLayouts = new Map();
export const railOf = (id) => railLayouts.get(id);
function railLayout(dialog) {
  const nav = dialog.querySelector(".rail-nav");
  const main = dialog.querySelector(".rail-main");
  const items = [...nav.querySelectorAll(".rail-item")];
  const sectionOf = new Map(items.map((b) => [b, main.querySelector(`.rail-section[data-section="${b.dataset.rail}"]`)]));
  for (const [b, s] of sectionOf) {
    b.id ||= `${dialog.id}-rail-${b.dataset.rail}`;
    s.id ||= `${dialog.id}-section-${b.dataset.rail}`;
    b.setAttribute("aria-controls", s.id);
    s.setAttribute("aria-labelledby", b.id);
  }
  // The section shown first: the rail's first, or the one marked data-rail-first.
  const first = (items.find((b) => b.hasAttribute("data-rail-first")) || items[0]).dataset.rail;
  let wanted = first, shown = null;
  const refresh = () => {
    for (const [b, s] of sectionOf) {
      const empty = [...s.children].every((c) => c.hidden);
      if (b.hidden !== empty) b.hidden = empty;
    }
  };
  // apply shows the wanted section, or the first one left if it is empty.
  const apply = ({ focus = false } = {}) => {
    refresh();
    const b = items.find((x) => x.dataset.rail === wanted && !x.hidden) || items.find((x) => !x.hidden) || items[0];
    const changed = b !== shown;
    shown = b;
    for (const [x, s] of sectionOf) {
      const on = x === b;
      x.tabIndex = on ? 0 : -1;
      if (on) x.setAttribute("aria-current", "true"); else x.removeAttribute("aria-current");
      s.classList.toggle("current", on);
    }
    if (changed) main.scrollTop = 0;
    if (focus) b.focus();
    if (changed && dialog.open) b.scrollIntoView({ block: "nearest", inline: "nearest" }); // the strip on a narrow screen
    if (changed) dialog.dispatchEvent(new CustomEvent("railchange", { detail: b.dataset.rail }));
  };
  const show = (key, opts) => { wanted = key; apply(opts); };
  nav.addEventListener("click", (e) => {
    const b = e.target.closest(".rail-item");
    if (b) show(b.dataset.rail);
  });
  nav.addEventListener("keydown", (e) => {
    const on = items.filter((x) => !x.hidden);
    const i = on.indexOf(e.target);
    if (i < 0) return;
    const next = { ArrowDown: i + 1, ArrowRight: i + 1, ArrowUp: i - 1, ArrowLeft: i - 1, Home: 0, End: on.length - 1 }[e.key];
    if (next === undefined) return;
    e.preventDefault();
    show(on[(next + on.length) % on.length].dataset.rail, { focus: true });
  });
  // Saving with a bad field in a section that isn't shown: show that section
  // first, so the browser can point at the field. Only the first bad field.
  let pointed = false;
  dialog.addEventListener("invalid", (e) => {
    if (pointed) return;
    const s = e.target.closest?.(".rail-section");
    if (!s) return;
    pointed = true;
    setTimeout(() => { pointed = false; });
    if (!s.classList.contains("current")) show(s.dataset.section);
  }, true);
  // Content showing or hiding (runners, webhooks, billing…) adds or drops sections.
  new MutationObserver(() => apply()).observe(main, { subtree: true, attributes: true, attributeFilter: ["hidden"] });
  dialog.addEventListener("close", () => { wanted = first; apply(); nav.scrollLeft = 0; });
  apply();
  const rail = { show, get current() { return shown.dataset.rail; } };
  railLayouts.set(dialog.id, rail);
  return rail;
}
// A sheet whose rail is built at run time (Runners: one entry per runner)
// has none yet and wires its own.
for (const d of document.querySelectorAll("dialog.rail-sheet")) if (d.querySelector(".rail-item")) railLayout(d);
