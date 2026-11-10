import { $, $$, h } from "./dom.js";
import { api, here } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { depPicker, draft, draftKey, promptEditor, setModelField, split, todoAttach } from "./todoform.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { grouped, projectWhere } from "./projects.js";
import { renderBoard } from "./branches.js";
import { showRunnerNeeded } from "./runners.js";
import { formPriority } from "./dnd.js";

// ------------------------------------------------------------------ new / edit todo
export let editing = null;
export let pickProject = false;
// pick lets you choose the project, for todos made outside a project (the global chat).
export function openTodoForm(t = null, { pick = false } = {}) {
  editing = t;
  pickProject = pick && !t;
  const f = $("#todo-form");
  f.reset();
  $("#todo-project-field").hidden = !pickProject;
  if (pickProject) {
    f.project.replaceChildren(...state.projects.map((p) =>
      h("option", { value: projectKey(p) }, grouped() ? `${p.name} · ${projectWhere(p)}` : p.name)));
    f.project.value = projectKey(state.project);
  }
  $("#todo-sheet-title").textContent = t ? "Edit task" : "New task";
  showRunnerNeeded($("#todo-runner-note"), state.project);
  $("#todo-clear").hidden = !!t;
  $("#todo-split").hidden = !split.splittable(t);
  if (t) {
    f.title.value = t.title;
    f.prompt.value = t.prompt;
    f.priority.value = t.priority ?? "";
  }
  setModelField(f, t?.model || "");
  todoAttach.reset(t?.attachments || []);
  depPicker.reset(t, t?.depends_on || []);
  promptEditor.render();
  draft.open(draftKey(t));
  $("#todo-sheet").returnValue = ""; // Esc keeps the previous close's value
  $("#todo-sheet").showModal();
  (t ? f.prompt : f.title).focus();
}
$("#new-todo").addEventListener("click", () => openQuickAdd());
// Toasts sit to the left of the FAB; track its width (0 while hidden) so they never overlap it.
new ResizeObserver(([e]) => {
  const w = e.target.offsetWidth;
  document.documentElement.style.setProperty("--fab-space", w ? `${w + 12}px` : "0px");
}).observe($("#new-todo"));

// ------------------------------------------------------------------ quick add
// The quick sheet (FAB, n): one field per task, the text is its prompt and
// Claude names it. A multi-line paste stays one task; the field just grows.
// "More options…" carries the text over to the full form (Shift+N).
const quick = {
  sheet: $("#quick-sheet"),
  rows: $("#quick-rows"),
  queue: $("#quick-queue"),
};
try { quick.queue.checked = localStorage.getItem("dash.quickQueue") === "1"; } catch {}

function quickRow(text = "") {
  const ta = h("textarea", {
    class: "quick-row", rows: 1, enterkeyhint: "done", autocomplete: "off",
    "aria-label": "Instructions for the task", placeholder: "What should Claude do?",
  });
  ta.value = text;
  return ta;
}
const quickFields = () => $$(".quick-row", quick.rows);
const quickTexts = () => quickFields().map((r) => r.value.trim()).filter(Boolean);
function fitQuickRow(ta) {
  ta.style.height = "auto";
  ta.style.height = `${ta.scrollHeight + (ta.offsetHeight - ta.clientHeight)}px`;
}
function quickAddRow(after = null, text = "") {
  const ta = quickRow(text);
  after ? after.after(ta) : quick.rows.append(ta);
  fitQuickRow(ta);
  ta.focus();
  return ta;
}
function updateQuickLabel() {
  const n = quickTexts().length;
  const what = n > 1 ? `${n} tasks` : "task";
  $("#quick-submit-label").textContent = quick.queue.checked ? `Queue ${what}` : `Save ${what} as draft${n > 1 ? "s" : ""}`;
}

export function openQuickAdd(texts = [""]) {
  if (!state.project) return toast("Select a project first", true);
  quick.rows.replaceChildren();
  showRunnerNeeded($("#quick-runner-note"), state.project);
  quick.sheet.returnValue = "";
  quick.sheet.showModal();
  texts.forEach((t) => quickAddRow(null, t));
  updateQuickLabel();
}

async function submitQuick() {
  const texts = quickTexts();
  if (!texts.length) { quickFields()[0]?.focus(); return; }
  const p = state.project;
  const enqueue = quick.queue.checked;
  quick.sheet.close("add");
  let done = 0;
  const ids = [];
  try {
    for (const prompt of texts) {
      const t = await api("POST", `/api/projects/${p.id}/todos`, { title: "", prompt, enqueue }, p.prefix || "");
      if (t?.id) ids.push(t.id);
      done++;
    }
  } catch (e) {
    // Give back what wasn't saved so nothing typed is lost.
    if (projectKey(state.project) === projectKey(p)) openQuickAdd(texts.slice(done));
    throw e;
  }
  const n = texts.length;
  toast(enqueue ? (n > 1 ? `Added ${n} tasks to the queue` : "Added to queue") : (n > 1 ? `Saved ${n} drafts` : "Saved as draft"));
  if (projectKey(state.project) === projectKey(p)) showNewTasks(enqueue ? "queued" : "draft", ids);
}

// Show the lane new tasks were added to. The board follows them for a few
// seconds, so a queued task the runner starts at once is shown under Running
// instead of leaving an empty Queued lane on screen.
export function showNewTasks(lane, ids) {
  state.lane = lane;
  state.follow = ids.length ? { ids, lane, until: Date.now() + 15000 } : null;
  if (ids.length) renderBoard();
}

function quickMore() {
  const text = quickTexts().join("\n\n");
  quick.sheet.close("more");
  openTodoForm();
  if (!text) return;
  const f = $("#todo-form");
  const kept = f.prompt.value.trim(); // a restored draft of the full form
  f.prompt.value = kept ? `${kept}\n\n${text}` : text;
  promptEditor.render();
  draft.changed();
}

quick.rows.addEventListener("input", (e) => {
  if (e.target.matches(".quick-row")) fitQuickRow(e.target);
  updateQuickLabel();
});
quick.rows.addEventListener("keydown", (e) => {
  const ta = e.target;
  if (!ta.matches(".quick-row") || e.isComposing) return;
  if (e.key === "Enter" && e.shiftKey && !e.ctrlKey && !e.metaKey) {
    e.preventDefault();
    quickAddRow(ta);
  } else if (e.key === "Enter" && !e.altKey) {
    e.preventDefault();
    attempt(submitQuick)();
  } else if (e.key === "Backspace" && !ta.value && quickFields().length > 1) {
    e.preventDefault();
    const prev = ta.previousElementSibling || ta.nextElementSibling;
    ta.remove();
    prev.focus();
    prev.setSelectionRange(prev.value.length, prev.value.length);
    updateQuickLabel();
  }
});
quick.queue.addEventListener("change", () => {
  try { localStorage.setItem("dash.quickQueue", quick.queue.checked ? "1" : "0"); } catch {}
  updateQuickLabel();
  (quick.last?.isConnected ? quick.last : quickFields().at(-1))?.focus(); // keep typing
});
quick.rows.addEventListener("focusin", (e) => { if (e.target.matches(".quick-row")) quick.last = e.target; });
$("#quick-form").addEventListener("submit", (e) => { e.preventDefault(); if (e.submitter?.value === "cancel") return quick.sheet.close(); attempt(submitQuick)(); });
$("#quick-another").addEventListener("click", () => quickAddRow(quickFields().at(-1)));
$("#quick-more").addEventListener("click", quickMore);

$("#todo-sheet").addEventListener("close", attempt(async () => {
  const action = $("#todo-sheet").returnValue;
  if (action === "split") return draft.done(); // saved as the split's tasks
  if (action !== "draft" && action !== "queue") {
    const kept = draft.has(draftKey(editing));
    draft.close();
    if (draft.has(draftKey(editing)) && !kept) toast("Your draft is kept; open the form again to continue");
    return;
  }
  draft.flush(); // if saving fails below, the draft is still there
  const f = $("#todo-form");
  await todoAttach.settled();
  const body = { title: f.title.value.trim(), prompt: f.prompt.value.trim(), priority: formPriority(f.priority.value), model: f.model.value, attachments: todoAttach.ids(), depends_on: depPicker.ids() };
  if (!body.title && !body.prompt) throw new Error("Add a title or instructions before saving.");
  if (editing) {
    await api("PATCH", `/api/todos/${editing.id}`, body);
    if (action === "queue" && editing.status !== "queued") await api("POST", `/api/todos/${editing.id}/enqueue`);
    toast(action === "queue" ? "Saved and queued" : "Saved");
  } else {
    const p = (pickProject && state.projects.find((x) => projectKey(x) === f.project.value)) || state.project;
    // Attachments are uploaded to the current project's computer.
    if (body.attachments.length && p.prefix !== here()) throw new Error(`Attachments can't be sent to ${p.name} on ${p.machine}; remove them or pick a project on this computer.`);
    const t = await api("POST", `/api/projects/${p.id}/todos`, { ...body, enqueue: action === "queue" }, p.prefix || "");
    const other = projectKey(p) !== projectKey(state.project);
    toast(`${action === "queue" ? "Added to queue" : "Saved as draft"}${other ? ` in ${p.name}` : ""}`);
    if (!other) showNewTasks(action === "queue" ? "queued" : "draft", t?.id ? [t.id] : []);
  }
  draft.done();
}));
// Native form validation would block "Save as draft" too; we validate on close instead.
$("#todo-form").noValidate = true;
