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

// toast(msg) for info, toast(msg, true) for errors, or toast(msg, { tone, head, where, action: { label, run } }).
// where names the project the toast is about, on a small line above its head.
// Hovering or focusing a toast pauses its countdown.
const TOAST_ICONS = { info: "bell", ok: "check", error: "x", review: "eye", approval: "flag", pr: "merge" };
export function toast(msg, opts = {}) {
  if (typeof opts === "boolean") opts = { tone: opts ? "error" : "info" };
  const { tone = "info", head, where, action, icon = TOAST_ICONS[tone] || "bell", onClose } = opts;
  const ms = opts.ms ?? (tone === "error" || action ? 7000 : 3500);
  let timer, start, left = ms, held = 0, gone = false;
  // onClose(acted) runs once the toast goes, however it goes; acted: its action was clicked.
  const close = (acted = false) => {
    if (gone) return;
    gone = true;
    clearTimeout(timer);
    leave(t, "leaving", () => t.remove());
    onClose?.(acted);
  };
  const t = h("div", { class: `toast ${tone}`, role: tone === "error" ? "alert" : "status", style: `--ms: ${ms}ms` },
    h("span", { class: "toast-icon" }, ico(icon)),
    h("div", { class: "toast-body" }, where ? h("small", { class: "toast-where" }, where) : null, head ? h("strong", {}, head) : null, msg ? h("span", {}, msg) : null),
    action ? h("button", { class: "toast-action", type: "button", onclick: () => { close(true); attempt(action.run)(); } }, action.label) : null,
    ib({ class: "toast-close", type: "button", "aria-label": "Dismiss", title: "Dismiss", onclick: () => close() }, "x"),
    h("span", { class: "toast-bar" }));
  const run = () => { start = Date.now(); timer = setTimeout(close, left); };
  const hold = () => { if (held++) return; clearTimeout(timer); left -= Date.now() - start; t.classList.add("held"); };
  const release = () => { if (--held) return; t.classList.remove("held"); run(); };
  t.dismiss = () => close();
  t.addEventListener("pointerenter", hold);
  t.addEventListener("pointerleave", release);
  t.addEventListener("focusin", hold);
  t.addEventListener("focusout", release);
  const box = $("#toasts");
  box.append(t);
  // Voice commands' listening bar (voice.js) lives here too, and stays.
  for (let ts = box.querySelectorAll(":scope > .toast:not(.voice-bar)"), i = 0; i < ts.length - 4; i++) { ts[i].dismiss?.(); ts[i].remove(); }
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
export function raiseToasts() {
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

// Code under an svg block's picture (see renderMarkdown) shows its source.
document.addEventListener("click", (e) => {
  const btn = e.target.closest?.(".md-svg-code");
  const code = btn?.closest(".md-svg")?.querySelector(".code-block");
  if (!code) return;
  code.hidden = !code.hidden;
  btn.setAttribute("aria-expanded", String(!code.hidden));
  btn.textContent = code.hidden ? "Code" : "Hide code";
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

// A sheet's form is method="dialog" with its header Close button first, so a
// plain Enter in a field would press Close and drop what was typed. Instead it
// presses the sheet's main button (or, with none, commits the field). Fields
// with their own Enter (which preventDefault) are left alone.
const SUBMITS = "button:not([type]), button[type=submit], input[type=submit], input[type=image]";
const NO_ENTER = new Set(["checkbox", "radio", "range", "color", "file", "button", "submit", "reset", "image", "hidden"]);
document.addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || e.isComposing || e.defaultPrevented) return;
  const el = e.target;
  if (el.localName !== "input" || NO_ENTER.has(el.type)) return;
  const form = el.form;
  if (form?.method !== "dialog") return;
  const buttons = [...form.elements].filter((b) => b.matches(SUBMITS));
  const closer = (b) => b.value === "cancel" || !!b.closest(".sheet-head");
  if (!buttons[0] || !closer(buttons[0])) return;
  e.preventDefault();
  if (e.ctrlKey || e.metaKey || e.altKey) return; // a sheet's own shortcut, if any, ran already
  const usable = buttons.filter((b) => !closer(b) && !b.disabled && b.checkVisibility());
  const main = usable.find((b) => b.matches(".primary, .danger")) || usable[0];
  if (main) form.requestSubmit(main); else el.blur();
});

// openSheet builds a sheet on the fly (a header with the title and a close
// button, then body, then footer in .sheet-actions), shows it, and removes it
// when it closes, after onClose(returnValue). cls adds classes; small: false
// makes a wider sheet. closed resolves to the dialog's returnValue.
let sheetSeq = 0;
export function openSheet({ title, cls = "", small = true, body = [], footer = null, onClose = null }) {
  const id = `sheet-title-${++sheetSeq}`;
  const form = h("form", { method: "dialog" },
    h("header", { class: "sheet-head" }, h("h2", { id }, title),
      ib({ class: "icon-btn", value: "cancel", formnovalidate: true, "aria-label": "Close" }, "x")),
    body,
    footer ? h("footer", { class: "sheet-actions" }, footer) : null);
  const dialog = h("dialog", { class: ["sheet", small ? "small" : "", cls].filter(Boolean).join(" "), "aria-labelledby": id }, form);
  const closed = new Promise((resolve) => {
    dialog.addEventListener("close", () => {
      onClose?.(dialog.returnValue);
      dialog.remove();
      resolve(dialog.returnValue);
    }, { once: true });
  });
  document.body.append(dialog);
  dialog.showModal();
  return { dialog, form, closed };
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
// A section merged into another keeps its old key working: the entry lists it
// in data-rail-also (space-separated), and show(old) shows the merged one;
// railOf(id).key(old) names it.
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
  const also = new Map(items.flatMap((b) => (b.dataset.railAlso || "").split(/\s+/).filter(Boolean).map((k) => [k, b.dataset.rail])));
  const key = (k) => also.get(k) || k;
  const show = (k, opts) => { wanted = key(k); apply(opts); };
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
  // The close event comes a moment after close(): if the sheet was opened
  // again in between (at another section), it is that one's, not this.
  dialog.addEventListener("close", () => {
    if (dialog.open) return;
    wanted = first; apply(); nav.scrollLeft = 0;
  });
  apply();
  const rail = { show, key, get current() { return shown.dataset.rail; } };
  railLayouts.set(dialog.id, rail);
  return rail;
}
// A sheet whose rail is built at run time (Runners: one entry per runner)
// has none yet and wires its own.
for (const d of document.querySelectorAll("dialog.rail-sheet")) if (d.querySelector(".rail-item")) railLayout(d);
