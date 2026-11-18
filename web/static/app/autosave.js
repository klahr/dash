import { h } from "./dom.js";
import { fail, toast } from "./dialogs.js";

// ------------------------------------------------------------------ saving a field
// The rail sheets (Settings, Account, Organization, Runners) have no Save
// button: each field saves on its own when it changes (text on blur or
// Enter, never on each keystroke). The sheet's header then says "Saved" for
// a moment; an error shows under the field itself, which keeps what was
// typed so it can be fixed. Only dialogs that create something keep a
// Save button.

// fieldBox is the part of the form an error goes under: the field's label,
// toggle, row or fieldset.
const fieldBox = (el) => el.closest?.(".field, .toggle, .color-row, .runner-setting, .org-invite, fieldset") || el;
const errors = new WeakMap(); // field box → its error line

// fieldError shows msg under el's field (or clears it, given no msg).
export function fieldError(el, msg) {
  const box = fieldBox(el);
  let line = errors.get(box);
  if (!msg) {
    line?.remove();
    errors.delete(box);
    if (el.removeAttribute) el.removeAttribute("aria-invalid");
    return;
  }
  if (!line?.isConnected) {
    line = h("p", { class: "hint err-text field-error", role: "alert" });
    errors.set(box, line);
    if (box.localName === "fieldset") box.append(line); else box.after(line);
  }
  line.textContent = msg;
  if (el.setAttribute) el.setAttribute("aria-invalid", "true");
}

// savedNote says "Saving…", then "Saved", in the header of the sheet el is in.
const notes = new WeakMap(); // dialog → { el, timer, pending }
function note(el) {
  const d = el.closest?.("dialog");
  const head = d?.querySelector(".sheet-head");
  if (!head) return null;
  let n = notes.get(d);
  if (!n?.el.isConnected) {
    n = { el: h("span", { class: "save-note", role: "status", "aria-live": "polite" }), timer: 0, pending: 0 };
    const close = head.querySelector(".icon-btn:last-child");
    if (close) close.before(n.el); else head.append(n.el);
    notes.set(d, n);
  }
  return n;
}
function showNote(n, text, cls) {
  clearTimeout(n.timer);
  n.el.textContent = text;
  n.el.className = text ? `save-note shown ${cls || ""}` : "save-note";
  if (cls === "done") n.timer = setTimeout(() => { n.el.className = "save-note"; }, 2000);
}

// saveField runs save (an async function) for the field el: its error under
// the field, or "Saved" in the sheet's header. With the sheet already
// closed, an error is a toast. save resolving to false means it saved
// nothing after all. Resolves to whether it saved.
export async function saveField(el, save) {
  const n = note(el);
  if (n) { n.pending++; showNote(n, "Saving…"); }
  try {
    if (await save() === false) { // nothing saved (the person changed their mind)
      if (n && !--n.pending) showNote(n, "");
      return false;
    }
    fieldError(el, "");
    if (n && !--n.pending) showNote(n, "Saved", "done");
    else if (!n) toast("Saved", { tone: "ok" });
    return true;
  } catch (e) {
    if (n && !--n.pending) showNote(n, "");
    const d = el.closest?.("dialog");
    if (d && !d.open) fail(e);
    else fieldError(el, e.message || String(e));
    return false;
  }
}

// savedNote says "Saved" in the header of el's sheet, for a field that
// saves on its own terms (after a password, say).
export function savedNote(el) {
  const n = note(el);
  if (n) showNote(n, "Saved", "done");
  else toast("Saved", { tone: "ok" });
}

// checked throws the browser's own message when el's value breaks its
// min, max, required… (not a custom validity, which its owner checks).
export function checked(el) {
  const v = el.validity;
  if (v && !v.valid && !v.customError) throw new Error(el.validationMessage);
}

// queue runs the saves of one sheet one after another, so they land in order.
export function saveQueue() {
  let last = Promise.resolve();
  return (fn) => (last = last.then(fn, fn));
}
