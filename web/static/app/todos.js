import { $, $$, h } from "./dom.js";
import { api, here } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { buildOnPicker, depPicker, runOnPicker, tagPicker, draft, draftKey, promptEditor, setFormModel, split, todoAttach } from "./todoform.js";
import { projectFirst, state } from "./state.js";
import { projectKey } from "./events.js";
import { grouped, projectWhere } from "./projects.js";
import { renderBoard } from "./branches.js";
import { showRunnerNeeded } from "./runners.js";
import { levelPriority, priorityLevel } from "./dnd.js";
import { goalsOn, openGoalSheet } from "./goals.js";
import { confirmRunAs } from "./people.js";
import { openFeatureBrief } from "./planner.js";
import { tagChips } from "./tags.js";
import { popMenu } from "./popmenu.js";
import { addTag, quickTags } from "../tagtext.js";
import { drafts } from "../editor.js";

// ------------------------------------------------------------------ new / edit todo
// A task's title, instructions and options can be edited while it is a draft,
// queued, or failed before it got a worktree (one with a worktree is reworked instead).
export const editable = (t) => !!t && (t.status === "draft" || t.status === "queued" || (t.status === "failed" && !t.worktree));

// updateTodo saves changes to a task: only the fields given are changed. The
// board shows the result at once instead of waiting for the server's event.
// A change that makes another member's queued task yours too is asked first
// (confirmRunAs; asked: the caller did): it resolves to null when cancelled.
export async function updateTodo(t, changes, { asked = false } = {}) {
  if ("title" in changes || "prompt" in changes) {
    const after = { title: t.title, prompt: t.prompt, ...changes };
    if (!after.title?.trim() && !after.prompt?.trim()) throw new Error("Add a title or instructions before saving.");
  }
  if (!asked && !await confirmRunAs(t, changes)) return null;
  const saved = await api("PATCH", `/api/todos/${t.id}`, changes);
  if (saved?.id && state.todos.has(saved.id)) {
    state.todos.set(saved.id, saved);
    renderBoard();
  }
  return saved;
}

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
  // A form that picks the project shows it in its Project field instead.
  sheetTitle($("#todo-sheet-title"), t ? "Edit task" : "New task", pickProject ? null : state.project);
  showRunnerNeeded($("#todo-runner-note"), state.project);
  $("#todo-clear").hidden = !!t;
  $("#todo-split").hidden = !split.splittable(t);
  $("#todo-bigger").hidden = !!t || !biggerChoices(state.project).length; // a goal is new work, not an edit of a task
  if (t) {
    f.title.value = t.title;
    f.prompt.value = t.prompt;
    f.priority.value = priorityLevel(t.priority);
  }
  setFormModel(t?.model || "");
  todoAttach.reset(t?.attachments || []);
  depPicker.reset(t, t?.depends_on || []);
  buildOnPicker.reset(t, t?.builds_on || "");
  runOnPicker.reset(t, t?.run_on || "");
  tagPicker.reset(t?.tags || []);
  promptEditor.render();
  draft.open(draftKey(t));
  $("#todo-sheet").returnValue = ""; // Esc keeps the previous close's value
  $("#todo-sheet").showModal();
  f.prompt.focus(); // the instructions are the form's main field, and its first
}

// sheetTitle names a task dialog: "New task in <project>", so it's plain
// which project the task is filed into (p null: no project named).
function sheetTitle(el, text, p) {
  el.replaceChildren(text, ...(p ? [" in ", h("strong", { class: "sheet-project" }, p.name)] : []));
}
$("#new-todo").addEventListener("click", () => openQuickAdd());
// New task floats on a phone (on a desktop it is in the board's header): toasts sit to
// the left of it there; track its width (0 while hidden or not floating) so they never overlap it.
new ResizeObserver(([e]) => {
  const w = getComputedStyle(e.target).position === "fixed" ? e.target.offsetWidth : 0;
  document.documentElement.style.setProperty("--fab-space", w ? `${w + 12}px` : "0px");
}).observe($("#new-todo"));

// ------------------------------------------------------------------ quick add
// The quick sheet (FAB, n): one field per task, the text is its prompt and
// Claude names it. Multi-line text (Shift-Enter, a paste) stays one task; the field just grows.
// "More options…" carries the text over to the full form (Shift+N). It
// ends like the full form: Save draft (Enter, so it is the primary button) or Queue (Ctrl/⌘+Enter).
// A #tag in the text tags the task (quickTags in tagtext.js has the rule):
// it is taken out of the text, and the field with focus shows its tags below.
const quick = {
  sheet: $("#quick-sheet"),
  rows: $("#quick-rows"),
};

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
// showQuickTags shows the tags of the field ta, while it has any.
function showQuickTags(ta) {
  const el = $("#quick-tags");
  const { tags } = quickTags(ta?.value || "");
  el.hidden = !tags.length;
  el.replaceChildren(...(tags.length ? [h("span", { class: "hint" }, "Tags"), ...tagChips(tags)] : []));
}
function quickAddRow(after = null, text = "") {
  const ta = quickRow(text);
  after ? after.after(ta) : quick.rows.append(ta);
  fitQuickRow(ta);
  ta.focus();
  return ta;
}
// What is typed in the quick sheet autosaves to this browser, per project, as
// the full form's draft does: closing it (Esc, ✕, a click outside) keeps the
// text, and the sheet brings it back the next time it opens. Saving the
// tasks, or carrying the text on to the full form, forgets it.
const quickDraftKey = () => `quick/${projectKey(state.project)}`;
const quickDraft = drafts({
  read: () => ({ texts: quickFields().map((r) => r.value).filter((v) => v.trim()) }),
  write(d) {
    quick.rows.replaceChildren();
    (d.texts || []).forEach((t) => quickAddRow(null, t));
  },
  onStatus() {},
});

// openQuickAdd opens the quick sheet with its saved draft, if any, and texts
// (what a failed save gives back) after it.
export function openQuickAdd(texts = []) {
  if (!state.project) return toast(projectFirst(), true);
  if (!quick.sheet.open) quickClosed(true); // a close whose event hasn't come yet
  quickPending = true;
  quick.rows.replaceChildren();
  showRunnerNeeded($("#quick-runner-note"), state.project);
  sheetTitle($("#quick-title"), "New task", state.project);
  $("#quick-bigger").hidden = !biggerChoices(state.project).length;
  quick.sheet.returnValue = "";
  quick.sheet.showModal();
  quickDraft.open(quickDraftKey()); // with no rows yet, the base is "nothing typed"
  const given = texts.filter((t) => t.trim());
  given.forEach((t) => quickAddRow(null, t));
  if (!quickFields().length) quickAddRow();
  if (given.length) quickDraft.flush();
  showQuickTags(document.activeElement);
}

// A close other than a save keeps the text as a draft (the full form, the
// goal and the brief take it with them; goal and brief keep it, in case
// they are not made, as the full form does).
// The browser fires close later, as a task of its own, and input goes first:
// New task clicked right after Esc opens the sheet before it comes, so
// openQuickAdd handles the close first (it would otherwise clear the fields
// and bring back an older autosave). quickPending: a close not handled yet.
let quickPending = false;
quick.sheet.addEventListener("close", () => { if (!quick.sheet.open) quickClosed(); });
function quickClosed(reopening = false) {
  if (!quickPending) return;
  quickPending = false;
  const action = quick.sheet.returnValue;
  if (action === "add") return; // submitQuick has it
  if (action === "more") return quickDraft.done();
  const key = quickDraftKey(), kept = quickDraft.has(key);
  quickDraft.close();
  if (!reopening && quickDraft.has(key) && !kept && action !== "goal" && action !== "brief") toast("Your text is kept; open New task again to continue");
}

async function submitQuick(enqueue) {
  const texts = quickTexts();
  if (!texts.length) { quickFields()[0]?.focus(); return; }
  const tasks = texts.map(quickTags);
  const bare = tasks.findIndex((x) => !x.text);
  if (bare >= 0) {
    quickFields().filter((r) => r.value.trim())[bare]?.focus();
    throw new Error("Say what Claude should do, not only tags.");
  }
  const p = state.project;
  // The text is in hand: forget the draft now (so opening the sheet while
  // this saves doesn't bring it back); a failed save opens the sheet again
  // with what wasn't saved, which drafts it again.
  quickDraft.done();
  quick.sheet.close("add");
  let done = 0;
  const ids = [];
  try {
    for (const { text: prompt, tags } of tasks) {
      const t = await api("POST", `/api/projects/${p.id}/todos`, { title: "", prompt, tags, enqueue }, p.prefix || "");
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

// The #tags go to the form's Tags.
function quickMore() {
  const tasks = quickTexts().map(quickTags);
  const text = tasks.map((x) => x.text).filter(Boolean).join("\n\n");
  quick.sheet.close("more");
  openTodoForm();
  const tags = tasks.flatMap((x) => x.tags).reduce((list, x) => addTag(list, x).tags, tagPicker.list());
  if (tags.length !== tagPicker.list().length) { tagPicker.reset(tags); draft.changed(); }
  if (!text) return;
  const f = $("#todo-form");
  const kept = f.prompt.value.trim(); // a restored draft of the full form
  f.prompt.value = kept ? `${kept}\n\n${text}` : text;
  promptEditor.render();
  draft.changed();
}

quick.rows.addEventListener("input", (e) => {
  if (e.target.matches(".quick-row")) { fitQuickRow(e.target); showQuickTags(e.target); quickDraft.changed(); }
});
quick.rows.addEventListener("focusin", (e) => { if (e.target.matches(".quick-row")) showQuickTags(e.target); });
// Keys in a field: Enter saves a draft, Ctrl/⌘-Enter queues, Shift-Enter is a
// new line (as in a chat), Alt/⌥-Enter starts another task's field.
quick.rows.addEventListener("keydown", (e) => {
  const ta = e.target;
  if (!ta.matches(".quick-row") || e.isComposing || e.key !== "Enter" && e.key !== "Backspace") return;
  const mod = e.ctrlKey || e.metaKey;
  if (e.key === "Enter" && e.shiftKey && !mod && !e.altKey) {
    // The browser types the new line; the input event grows the field.
  } else if (e.key === "Enter" && e.altKey && !mod && !e.shiftKey) {
    e.preventDefault();
    quickAddRow(ta);
  } else if (e.key === "Enter" && !e.altKey) {
    e.preventDefault();
    attempt(submitQuick)(e.ctrlKey || e.metaKey);
  } else if (e.key === "Backspace" && !ta.value && quickFields().length > 1) {
    e.preventDefault();
    const prev = ta.previousElementSibling || ta.nextElementSibling;
    ta.remove();
    prev.focus();
    prev.setSelectionRange(prev.value.length, prev.value.length);
  }
});
$("#quick-form").addEventListener("submit", (e) => {
  e.preventDefault();
  if (e.submitter?.value === "cancel") return quick.sheet.close();
  attempt(submitQuick)(e.submitter?.value === "queue");
});
$("#quick-another").addEventListener("click", () => quickAddRow(quickFields().at(-1)));
$("#quick-more").addEventListener("click", quickMore);
// Something bigger? A menu in both task dialogs, of what isn't a task: New
// goal… (what was typed becomes what the goal is to get done) and Write a
// feature brief (what was typed is the idea; the brief goes onto a new
// Planning card for the project). Each closes the dialog and opens its own.
// Briefs are of the hub's own projects (a runner's own project isn't on Planning).
const briefsOn = (p) => !!p && !p.prefix;
function featureBrief(idea) {
  openFeatureBrief({ project: state.project.id, idea }).catch((e) => toast(e.message || String(e), true));
}
// biggerChoices lists the menu's choices for project p; typed() gives what
// was typed ({ title, text }) and close(why) closes the dialog.
function biggerChoices(p, typed = () => ({}), close = () => {}) {
  return [
    goalsOn(p) && ["target", "New goal…", () => {
      const { title = "", text = "" } = typed();
      close("goal");
      openGoalSheet({ title, description: text });
    }, { note: "Claude plans tasks toward it" }],
    briefsOn(p) && ["spark", "Write a feature brief", () => {
      const { title = "", text = "" } = typed();
      close("brief");
      featureBrief([title, text].filter(Boolean).join("\n\n"));
    }, { note: "Claude interviews you", title: "Claude interviews you about it and writes a brief onto a Planning card" }],
  ].filter(Boolean);
}
$("#quick-bigger").addEventListener("click", (e) => popMenu(e.currentTarget,
  [biggerChoices(state.project, () => ({ text: quickTexts().join("\n\n") }), (why) => quick.sheet.close(why))],
  { heading: "Something bigger?" }));
$("#todo-bigger").addEventListener("click", (e) => {
  const f = $("#todo-form");
  popMenu(e.currentTarget,
    [biggerChoices(state.project, () => ({ title: f.title.value.trim(), text: f.prompt.value.trim() }), (why) => $("#todo-sheet").close(why))],
    { heading: "Something bigger?" });
});

// Enter in Title doesn't submit the form (its first button is Close): it goes
// on to Priority, the next field (Ctrl/⌘-Enter queues, palette.js).
$("#todo-form").title.addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || e.isComposing || e.ctrlKey || e.metaKey) return;
  e.preventDefault();
  $("#todo-form").priority.focus();
});

$("#todo-sheet").addEventListener("close", attempt(async () => {
  // A close event comes as a task of its own: when the form was opened again
  // meanwhile, that one is still being edited.
  if ($("#todo-sheet").open) return;
  const action = $("#todo-sheet").returnValue;
  if (action === "split") return draft.done(); // saved as the split's tasks
  if (action === "goal" || action === "brief") return draft.close(); // kept, in case the goal or brief isn't made
  if (action !== "draft" && action !== "queue") {
    const kept = draft.has(draftKey(editing));
    draft.close();
    if (draft.has(draftKey(editing)) && !kept) toast("Your draft is kept; open the form again to continue");
    return;
  }
  draft.flush(); // if saving fails below, the draft is still there
  const f = $("#todo-form");
  await todoAttach.settled();
  const body = { title: f.title.value.trim(), prompt: f.prompt.value.trim(), priority: levelPriority(f.priority.value, editing?.priority), model: f.model.value, attachments: todoAttach.ids(), depends_on: depPicker.ids(), builds_on: buildOnPicker.value(), run_on: runOnPicker.value(), tags: tagPicker.value() };
  if (editing) {
    if (!await confirmRunAs(editing, body, action === "queue" && editing.status !== "queued")) {
      draft.close();
      toast("Not saved; your draft is kept");
      return;
    }
    await updateTodo(editing, body, { asked: true });
    if (action === "queue" && editing.status !== "queued") await api("POST", `/api/todos/${editing.id}/enqueue`);
    toast(action === "queue" ? "Saved and queued" : "Saved");
  } else {
    if (!body.title && !body.prompt) throw new Error("Add a title or instructions before saving.");
    const p = (pickProject && state.projects.find((x) => projectKey(x) === f.project.value)) || state.project;
    // Attachments are uploaded to the current project's computer.
    if (body.attachments.length && p.prefix !== here()) throw new Error(`Attachments can't be sent to ${p.name} on ${p.machine}; remove them or pick a project on this computer.`);
    const other = projectKey(p) !== projectKey(state.project);
    const t = await api("POST", `/api/projects/${p.id}/todos`, { ...body, enqueue: action === "queue" }, p.prefix || "");
    toast(`${action === "queue" ? "Added to queue" : "Saved as draft"}${other ? ` in ${p.name}` : ""}`);
    if (!other) showNewTasks(action === "queue" ? "queued" : "draft", t?.id ? [t.id] : []);
  }
  draft.done();
}));
// Native form validation would block "Save draft" too; we validate on close instead.
$("#todo-form").noValidate = true;
