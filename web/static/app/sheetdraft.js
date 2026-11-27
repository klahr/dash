import * as store from "./store.js";
import { agoText } from "../time.js";
import { h, ib } from "./dom.js";

// ------------------------------------------------------------------ drafts
// drafts keeps a form in this browser (localStorage) while it is being
// edited: the task form and the quick sheet use it directly. sheetDraft
// wires it to any sheet: what is typed into its text fields (inputs and
// textareas, never a password) autosaves while it is open. Closing the sheet by accident (Esc, ×,
// a click outside, a reload) keeps the text; opening the same sheet on the
// same thing brings it back, with a line saying so and a Discard button.
// Submitting forgets it, and so does Discard in guardDiscard's question.
//
// sheetDraft(dialog) wires a sheet; call open(key) once its fields are
// filled for what it edits (key: "new", an id…). By default a close with a
// value other than "cancel" is a submit and forgets the draft. With
// { keep: true } the owner calls done() once the save worked, so a failed
// save that opens the sheet again still has its draft.

const PREFIX = "dash.draft.";
const draftStore = {
  get: (k) => store.getJSON(PREFIX + k),
  set: (k, v) => store.setJSON(PREFIX + k, v),
  del: (k) => store.remove(PREFIX + k),
};

// drafts keeps the open form saved under a key while it is being edited.
// read() returns the form's fields, write(d) fills them.
export function drafts({ read, write, onStatus }) {
  let key = null, timer = null, base = "";
  const snapshot = () => JSON.stringify(read());
  const save = () => {
    clearTimeout(timer);
    timer = null;
    if (!key) return;
    const now = snapshot();
    if (now === base) { draftStore.del(key); return; } // back to what's saved on the server
    if (draftStore.set(key, { ...JSON.parse(now), at: Date.now() })) onStatus("saved");
  };
  addEventListener("pagehide", save);
  document.addEventListener("visibilitychange", () => { if (document.hidden) save(); });
  return {
    // open starts editing under key; returns the restored draft, if any.
    open(k) {
      key = k;
      base = snapshot();
      const d = draftStore.get(k);
      if (!d) { onStatus(""); return null; }
      write(d);
      if (snapshot() === base) { draftStore.del(k); onStatus(""); return null; }
      onStatus("restored", d.at);
      return d;
    },
    changed() { clearTimeout(timer); timer = setTimeout(save, 300); onStatus("typing"); },
    flush: save,
    // done forgets the draft (after a successful save); close keeps it.
    done() { clearTimeout(timer); if (key) draftStore.del(key); key = null; },
    close() { save(); key = null; },
    discard() { if (key) draftStore.del(key); },
    has: (k) => !!draftStore.get(k),
  };
}

const SKIP = new Set(["password", "hidden", "file", "checkbox", "radio", "range", "color", "button", "submit", "reset", "image", "search"]);
const textFields = (root) => [...root.querySelectorAll("input, textarea")]
  .filter((el) => (el.localName === "textarea" || !SKIP.has(el.type)) && (el.name || el.id) && !el.readOnly);

// fieldValues is { name: value } for root's text fields; a name used more
// than once gets "#n" after it.
function fieldValues(root) {
  const out = {}, seen = {};
  for (const el of textFields(root)) {
    const k = el.name || el.id, n = seen[k] = (seen[k] || 0) + 1;
    out[n > 1 ? `${k}#${n}` : k] = el.value;
  }
  return out;
}
function setFieldValues(root, values = {}) {
  const seen = {};
  for (const el of textFields(root)) {
    const k = el.name || el.id, n = seen[k] = (seen[k] || 0) + 1;
    const v = values[n > 1 ? `${k}#${n}` : k];
    if (typeof v !== "string" || v === el.value) continue;
    el.value = v;
    el.dispatchEvent(new Event("input", { bubbles: true })); // the sheet's own checks and hints follow
  }
}

// draftHash makes a short, stable key part from text (the text a question
// starts from, say), so a draft only comes back to the same question.
export function draftHash(text = "") {
  let x = 5381;
  for (let i = 0; i < text.length; i++) x = ((x << 5) + x + text.charCodeAt(i)) | 0;
  return (x >>> 0).toString(36);
}

export function sheetDraft(dialog, { keep = false } = {}) {
  let base = {}, key = null, writing = false;
  const note = h("p", { class: "draft-status sheet-draft", "aria-live": "polite", hidden: true });
  const placeNote = () => {
    if (note.isConnected) return;
    const head = dialog.querySelector(".sheet-head");
    if (head) head.after(note); else dialog.querySelector("form")?.prepend(note);
  };
  const d = drafts({
    read: () => ({ fields: fieldValues(dialog) }),
    write: (v) => { writing = true; try { setFieldValues(dialog, v.fields); } finally { writing = false; } },
    onStatus(st, at) {
      if (st === "typing" || st === "saved") return;
      note.dataset.state = st;
      note.hidden = st !== "restored";
      if (st !== "restored") { note.replaceChildren(); return; }
      placeNote();
      note.replaceChildren(`Restored your unsaved text from ${agoText(at)}. `,
        ib({ type: "button", class: "link inline", onclick: discard }, "trash", "Discard it"));
    },
  });
  // Discard puts back what the sheet opened with and forgets the draft.
  function discard() {
    const k = key;
    d.discard();
    writing = true;
    try { setFieldValues(dialog, base); } finally { writing = false; }
    d.open(k);
    textFields(dialog)[0]?.focus();
  }
  dialog.addEventListener("input", () => { if (key && !writing) d.changed(); });
  dialog.addEventListener("discard", () => api.done()); // guardDiscard: the person chose to drop it
  dialog.addEventListener("close", () => {
    if (dialog.open || !key) return; // opened again before the event came
    note.hidden = true;
    const v = dialog.returnValue;
    if (v && v !== "cancel" && !keep) api.done();
    else if (v && v !== "cancel") d.flush(); // the owner says done() once it saved
    else { d.close(); key = null; }
  });
  const api = {
    // open starts keeping the sheet's text under key, bringing back a draft
    // kept there. Call it with the fields filled, after showModal.
    open(k) {
      key = `sheet/${dialog.id || "sheet"}/${k}`;
      dialog.returnValue = "";
      base = fieldValues(dialog);
      d.open(key);
    },
    done() { d.done(); key = null; note.hidden = true; },
  };
  return api;
}
