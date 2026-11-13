import { mdEditor, vimNormal, vimOn } from "../editor.js";
import { $, $$, h, plural } from "./dom.js";
import { desktop } from "./viewport.js";
import { attempt, busy, toast } from "./dialogs.js";
import { attachBox } from "./attachments.js";
import { state } from "./state.js";
import { title } from "./branches.js";
import { editable, openTodoForm, updateTodo } from "./todos.js";
import { depOptions, draft, promptEditor, setModelField, todoAttach } from "./todoform.js";
import { formPriority } from "./dnd.js";
import { openDetail, renderDetail } from "./detail.js";

// ------------------------------------------------------------------ inline editing (desktop)
// On a desktop the open task is edited in its detail pane: click the title to
// rename it, the instructions to edit them in the Markdown editor the task form
// uses, and priority, model and dependencies are controls under the title that
// save at once. Phones keep the task form (#todo-sheet); so does a new task,
// and "Edit in the full form" in ⋯ (clean up, split).
export const inlineEdits = (t) => desktop.matches && editable(t);

const cur = () => state.todos.get(state.detail);
const refresh = () => { if (state.detail) renderDetail(); };
const sameIds = (a = [], b = []) => a.length === b.length && a.every((x, i) => x === b[i]);

// ---- title: Enter (or leaving the field) saves, Esc cancels.
const titleEl = $("#d-title"), titleIn = $("#d-title-input");
let titleFor = null; // the task whose title is being edited

export function editTitle() {
  const t = cur();
  if (!inlineEdits(t)) return;
  titleFor = t.id;
  titleIn.value = t.title;
  titleIn.placeholder = t.title ? "Claude names it if empty" : title(t);
  titleEl.hidden = true;
  titleIn.hidden = false;
  titleIn.focus();
  titleIn.select();
}
function endTitle(refocus = false) {
  const had = document.activeElement === titleIn;
  titleFor = null;
  titleIn.hidden = true;
  titleEl.hidden = false;
  if (refocus && had) titleEl.focus();
}
async function saveTitle(refocus) {
  const t = state.todos.get(titleFor);
  if (!t) return endTitle(refocus);
  const v = titleIn.value.trim();
  if (v !== t.title) await updateTodo(t, { title: v });
  if (titleFor !== t.id) return; // cancelled or another task meanwhile
  endTitle(refocus);
  refresh();
}
titleEl.addEventListener("click", editTitle);
titleEl.addEventListener("keydown", (e) => {
  if ((e.key !== "Enter" && e.key !== " ") || !inlineEdits(cur())) return;
  e.preventDefault();
  e.stopPropagation();
  editTitle();
});
titleIn.addEventListener("keydown", (e) => {
  if (e.isComposing) return;
  const save = e.key === "Enter" || ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s");
  if (!save && e.key !== "Escape") return;
  e.preventDefault();
  e.stopPropagation(); // not the pane's Esc (close) or the app's keys
  if (save) attempt(() => saveTitle(true))();
  else endTitle(true);
});
titleIn.addEventListener("blur", () => { if (titleFor) attempt(() => saveTitle(false))(); });

// ---- instructions: the task form's Markdown editor, with its attachments.
const promptBox = $("#d-prompt-edit"), promptIn = $("#d-prompt-input");
const ed = mdEditor(promptIn);
const attach = attachBox($("#d-prompt-attach"), ed.el, () => {}, { zone: promptBox });
let promptFor = null; // the task whose instructions are being edited

export function editPrompt(text, atts) {
  const t = cur();
  if (!inlineEdits(t)) return;
  promptFor = t.id;
  promptIn.value = text ?? t.prompt;
  attach.reset(atts ?? t.attachments ?? []);
  refresh();
  ed.view.focus();
  ed.view.dispatch({ selection: { anchor: ed.view.state.doc.length }, scrollIntoView: true });
}

const promptChanged = (t) => !!t && (promptIn.value.trim() !== t.prompt.trim() || !sameIds(attach.ids(), (t.attachments || []).map((a) => a.id)));

function endPrompt() {
  const had = promptBox.contains(document.activeElement);
  promptFor = null;
  promptBox.hidden = true;
  $("#d-prompt").hidden = false;
  return had;
}

// Cancel keeps what was typed for a few seconds: Undo brings it back.
function cancelPrompt() {
  const id = promptFor, t = state.todos.get(id);
  const text = promptIn.value, atts = attach.items();
  const dirty = promptChanged(t);
  const had = endPrompt();
  if (dirty) {
    toast("Your edit of the instructions was discarded", { action: { label: "Undo", run: async () => {
      if (state.detail !== id) await openDetail(id);
      editPrompt(text, atts);
    } } });
  }
  return had;
}

async function savePrompt() {
  const t = state.todos.get(promptFor);
  if (!t) return endPrompt();
  if (!editable(t)) throw new Error("This task has started, so its instructions can't be changed now. Copy your text before cancelling.");
  await attach.settled();
  const changes = {};
  const prompt = promptIn.value.trim();
  if (prompt !== t.prompt) changes.prompt = prompt;
  const ids = attach.ids();
  if (!sameIds(ids, (t.attachments || []).map((a) => a.id))) changes.attachments = ids;
  if (Object.keys(changes).length) {
    await busy($("#d-prompt-save"), () => updateTodo(t, changes), { label: "Saving…" });
    toast("Instructions saved", { tone: "ok" });
  }
  if (promptFor !== t.id) return;
  const had = endPrompt();
  refresh();
  if (had) $("#d-prompt .edit-prompt")?.focus();
}

function cancelAndRefocus() {
  const had = cancelPrompt();
  refresh();
  if (had) $("#d-prompt .edit-prompt")?.focus();
}

// "Full editor…": the task form, with what was typed here, for clean up and split.
function openFull() {
  const t = state.todos.get(promptFor);
  if (!t) return;
  const text = promptIn.value, atts = attach.items();
  endPrompt();
  refresh();
  openTodoForm(t);
  $("#todo-form").prompt.value = text;
  todoAttach.reset(atts);
  promptEditor.render();
  draft.changed();
}

$("#d-prompt-save").addEventListener("click", attempt(savePrompt));
$("#d-prompt-cancel").addEventListener("click", cancelAndRefocus);
$("#d-prompt-full").addEventListener("click", openFull);
// Before the editor sees them: Ctrl+Enter / Ctrl+S save, Esc cancels (in vim,
// Esc first leaves insert mode). None of these reach the pane (Esc closes it)
// or the app's vim keys.
promptBox.addEventListener("keydown", (e) => {
  if (e.isComposing) return;
  const mod = (e.ctrlKey || e.metaKey) && !e.altKey;
  if (mod && (e.key === "Enter" || e.key.toLowerCase() === "s")) {
    e.preventDefault();
    e.stopPropagation();
    attempt(savePrompt)();
  } else if (e.key === "Escape") {
    if (vimOn() && e.target === ed.view.contentDOM && !vimNormal(ed.view)) return;
    e.preventDefault();
    e.stopPropagation();
    cancelAndRefocus();
  }
}, true);
promptBox.addEventListener("keydown", (e) => { if (e.key === "Escape") e.stopPropagation(); });

// Clicking the rendered instructions edits them (not a link, nor text being selected).
$("#d-prompt").addEventListener("click", (e) => {
  if (!inlineEdits(cur()) || e.target.closest("a, button, .feedback-list")) return;
  if (!document.getSelection()?.isCollapsed) return;
  editPrompt();
});

// ---- priority, model and dependencies: each saves as soon as it changes.
const metaBox = $("#d-meta-edit");
let metaFor = null, deps = null; // deps: the open dependency list, { wrap, pop, btn }

function closeDeps(refocus = false) {
  if (!deps) return;
  const { pop, btn } = deps;
  deps = null;
  pop.hidden = true;
  btn.setAttribute("aria-expanded", "false");
  if (refocus) btn.focus();
  renderMeta(cur());
}
document.addEventListener("pointerdown", (e) => { if (deps && !deps.wrap.contains(e.target)) closeDeps(); });

// save runs a change of the open task, putting the control back as it was if it fails.
const save = (fn, undo) => attempt(async () => {
  const t = cur();
  if (!t) return;
  try { await fn(t); } catch (e) { undo(t); throw e; }
  refresh();
});

function renderMeta(t) {
  const on = inlineEdits(t);
  metaBox.hidden = !on;
  if (!on) { if (deps) closeDeps(); metaBox.replaceChildren(); metaFor = null; return; }
  // Leave the controls alone while one is in use (the board re-renders on every event).
  if (metaFor === t.id && (deps || metaBox.contains(document.activeElement))) return;
  if (deps) closeDeps();
  metaFor = t.id;

  const prio = h("input", {
    type: "number", step: 1, inputmode: "numeric", placeholder: "None", class: "meta-input", value: t.priority ?? "",
    "aria-label": "Priority", title: "Prioritised tasks run first, lower numbers before higher; empty for none",
    onkeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); prio.blur(); } },
  });
  prio.addEventListener("change", save(async (t) => {
    const p = formPriority(prio.value);
    if (p !== (t.priority ?? null)) await updateTodo(t, { priority: p });
  }, (t) => { prio.value = t.priority ?? ""; }));

  const model = h("select", { class: "meta-select", "aria-label": "Model", title: "The Claude model the task runs on, from its next run" },
    [...$("#todo-form").model.options].filter((o) => !o.hasAttribute("data-custom")).map((o) => o.cloneNode(true)));
  setModelField({ model }, t.model || "");
  model.addEventListener("change", save((t) => updateTodo(t, { model: model.value }), (t) => setModelField({ model }, t.model || "")));

  const kids = [
    h("label", { class: "meta-ctl" }, h("span", {}, "Priority"), prio),
    h("label", { class: "meta-ctl" }, h("span", {}, "Model"), model),
  ];

  const opts = depOptions(t, t.depends_on || []);
  if (opts.length) {
    const n = (t.depends_on || []).length;
    const btn = h("button", { type: "button", class: "meta-btn", "aria-haspopup": "true", "aria-expanded": "false", title: "Tasks that must be merged before this one starts" },
      n ? `Depends on ${plural(n, "task")}` : "No dependencies");
    const pop = h("div", { class: "dep-pop", hidden: true, role: "group", "aria-label": "Depends on" },
      h("p", { class: "hint" }, "Starts once these are merged"),
      h("div", { class: "dep-picker" }, opts));
    const wrap = h("span", { class: "meta-deps" }, btn, pop);
    btn.addEventListener("click", () => {
      if (deps) return closeDeps();
      deps = { wrap, pop, btn };
      pop.hidden = false;
      btn.setAttribute("aria-expanded", "true");
      pop.querySelector("input")?.focus();
    });
    pop.addEventListener("change", save(async (t) => {
      const ids = $$("input[name=dep]:checked", pop).map((i) => i.value);
      await updateTodo(t, { depends_on: ids });
      btn.textContent = ids.length ? `Depends on ${plural(ids.length, "task")}` : "No dependencies";
    }, (t) => { const sel = new Set(t.depends_on || []); $$("input[name=dep]", pop).forEach((i) => { i.checked = sel.has(i.value); }); }));
    kids.push(wrap);
  }
  metaBox.replaceChildren(...kids);
}
// Esc in a control puts it back and leaves it, or closes the dependency list; the pane stays open.
metaBox.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  e.preventDefault();
  e.stopPropagation();
  if (deps) return closeDeps(true);
  metaFor = null; // rebuilt from the task: drops a half-typed priority
  e.target.blur?.();
  renderMeta(cur());
});

// ---- the detail pane's part
// renderInline brings the pane's edit state up to date; renderDetail calls it last.
export function renderInline(t) {
  const on = inlineEdits(t);
  if (titleFor && (titleFor !== t.id || !on)) endTitle();
  if (promptFor && promptFor !== t.id) cancelPrompt();
  titleEl.classList.toggle("editable", on);
  if (on) { titleEl.tabIndex = 0; titleEl.title = "Click to rename (Enter saves, Esc cancels)"; } else { titleEl.removeAttribute("tabindex"); titleEl.removeAttribute("title"); }
  renderMeta(t);
  const editing = promptFor === t.id;
  $("#d-prompt").hidden = editing;
  $("#d-prompt").classList.toggle("editable", on);
  promptBox.hidden = !editing;
  if (editing) {
    $("#d-prompt-wrap").open = true;
    $("#d-attach").hidden = true; // they are in the editor
  }
}

// endInline stops editing when the pane closes; an unsaved edit can be brought back from the toast.
export function endInline() {
  if (titleFor) endTitle();
  if (promptFor) cancelPrompt();
  if (deps) closeDeps();
}

// editTodo is "e" (vim keys): in place for the open task on a desktop, else the task form.
export function editTodo(t) {
  if (state.detail === t.id && inlineEdits(t)) return editPrompt();
  return openTodoForm(t);
}
