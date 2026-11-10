import { drafts, mdEditor } from "../editor.js";
import { $, $$, h, ib } from "./dom.js";
import { api, here } from "./api.js";
import { attempt, busy, toast } from "./dialogs.js";
import { attachBox } from "./attachments.js";
import { STATUS_LABEL, state } from "./state.js";
import { projectKey } from "./events.js";
import { ago, byPriority, title } from "./branches.js";
import { editing, pickProject, showNewTasks } from "./todos.js";
import { formPriority } from "./dnd.js";

// ------------------------------------------------------------------ the task form
export const promptEditor = mdEditor($("#todo-form").prompt);
export const todoAttach = attachBox($("#todo-attach"), promptEditor.el, () => draft.changed());

// The task form's "Depends on" picker: the project's unmerged tasks other
// than the one being edited. Ticked tasks must be merged before it starts.
export const depPicker = (() => {
  const field = $("#todo-deps-field"), list = $("#todo-deps");
  let self = null;
  const reset = (t, ids = []) => {
    self = t;
    const sel = new Set(ids);
    // Tasks already chosen stay listed (and kept) even once merged.
    const cands = [...state.todos.values()]
      .filter((x) => x.id !== t?.id && (x.status !== "merged" || sel.has(x.id)))
      .sort(byPriority);
    // The list is of the open project; a form picking another one can't use it.
    const usable = !pickProject || projectKey(state.project) === $("#todo-form").project.value;
    field.hidden = !usable || !cands.length;
    list.replaceChildren(...cands.map((x) => h("label", { class: `dep-option s-${x.status}` },
      h("input", { type: "checkbox", name: "dep", value: x.id, checked: sel.has(x.id) }),
      h("span", { class: "dep-title" }, title(x)),
      h("span", { class: "dep-status" }, STATUS_LABEL[x.status] || x.status))));
  };
  $("#todo-form").project.addEventListener("change", () => reset(self, []));
  return {
    reset,
    ids: () => field.hidden ? [] : $$("input[name=dep]:checked", list).map((i) => i.value),
  };
})();

// setModelField selects a task's model in the form, adding an option for a
// model id that isn't one of the aliases (set through the API or MCP).
export function setModelField(f, m) {
  const sel = f.model;
  $$("option[data-custom]", sel).forEach((o) => o.remove());
  if (m && ![...sel.options].some((o) => o.value === m)) sel.append(h("option", { value: m, "data-custom": "" }, m));
  sel.value = m;
}

// The todo form autosaves to this browser while you type; reopening the
// form (even after a reload or a crash) brings the draft back.
export const draftKey = (t) => `${projectKey(state.project)}/${t?.id || "new"}`;
export const draft = drafts({
  read() {
    const f = $("#todo-form");
    return { title: f.title.value, prompt: f.prompt.value, priority: f.priority.value, model: f.model.value, attachments: todoAttach.items(), depends_on: depPicker.ids() };
  },
  write(d) {
    const f = $("#todo-form");
    f.title.value = d.title ?? "";
    f.prompt.value = d.prompt ?? "";
    f.priority.value = d.priority ?? "";
    setModelField(f, d.model ?? editing?.model ?? "");
    todoAttach.reset(d.attachments || []);
    depPicker.reset(editing, d.depends_on ?? editing?.depends_on ?? []); // drafts from before dependencies have none
    promptEditor.render();
  },
  onStatus(st, at) {
    const el = $("#todo-draft");
    if (st === "typing") return;
    el.dataset.state = st;
    if (st === "saved") { el.replaceChildren("Draft saved on this device"); return; }
    if (st === "restored") {
      el.replaceChildren(`Restored your unsaved draft from ${ago(new Date(at).toISOString()) === "now" ? "just now" : `${ago(new Date(at).toISOString())} ago`}. `,
        ib({ type: "button", class: "link inline", onclick: discardDraft }, "trash", "Discard it"));
      return;
    }
    el.replaceChildren();
  },
});
function discardDraft() {
  draft.discard();
  const t = editing;
  const f = $("#todo-form");
  f.reset();
  if (t) { f.title.value = t.title; f.prompt.value = t.prompt; f.priority.value = t.priority ?? ""; }
  setModelField(f, t?.model || "");
  todoAttach.reset(t?.attachments || []);
  depPicker.reset(t, t?.depends_on || []);
  promptEditor.render();
  draft.open(draftKey(t));
  toast("Draft discarded");
}
$("#todo-clear").addEventListener("click", () => {
  const f = $("#todo-form");
  const project = f.project.value;
  draft.discard();
  f.reset();
  f.project.value = project;
  todoAttach.reset([]);
  depPicker.reset(null, []);
  promptEditor.render();
  draft.open(draftKey(null));
  f.title.focus();
  toast("Cleared");
});
// Clean up rewrites the instructions without the chatter around them (a pasted
// chat answer, say); Undo in the toast brings the original back.
$("#todo-refine").addEventListener("click", attempt(async () => {
  const f = $("#todo-form");
  const btn = $("#todo-refine");
  const before = f.prompt.value;
  if (!before.trim()) throw new Error("Write or paste instructions to clean up first.");
  const set = (v) => { f.prompt.value = v; promptEditor.render(); draft.changed(); };
  f.prompt.readOnly = true;
  try {
    await busy(btn, async () => {
      const { prompt } = await api("POST", "/api/refine", { prompt: before });
      if (f.prompt.value !== before) return; // the form changed meanwhile
      set(prompt);
      toast("Instructions cleaned up", { tone: "ok", action: { label: "Undo", run: () => set(before) } });
    }, { label: "Cleaning up…" });
  } finally {
    f.prompt.readOnly = false;
  }
}));
// Split asks Claude how to break a large task into smaller ones and shows
// them in a sheet over the form to edit, drop or add to. Saving them makes
// one task each, with the form's priority, model, files and dependencies;
// a part Claude says builds on earlier ones starts after they are merged.
// The form's task itself isn't saved (an edited one is replaced).
export const split = {
  sheet: $("#split-sheet"),
  list: $("#split-parts"),
  parts: [],          // { title, prompt, after: Set of parts, id once created }
  splittable: (t) => !t || t.status === "draft" || t.status === "queued",
};
$("#todo-split").addEventListener("click", attempt(async () => {
  const f = $("#todo-form");
  const btn = $("#todo-split");
  const before = f.prompt.value;
  if (!before.trim()) throw new Error("Write the instructions to split first.");
  f.prompt.readOnly = true;
  try {
    await busy(btn, async () => {
      const { parts } = await api("POST", "/api/split", { prompt: before });
      if (f.prompt.value !== before || !$("#todo-sheet").open) return; // the form changed meanwhile
      const made = (parts || []).map((p) => ({ title: p.title || "", prompt: p.prompt || "", after: new Set() }));
      (parts || []).forEach((p, i) => (p.after || []).forEach((j) => { if (j < i) made[i].after.add(made[j]); }));
      openSplit(made);
    }, { label: "Splitting…" });
  } finally {
    f.prompt.readOnly = false;
  }
}));

function openSplit(parts) {
  split.parts = parts;
  const one = parts.length === 1;
  $("#split-hint").textContent = (one
    ? "Claude thinks this is small enough for one task. Add more below if you still want to split it."
    : `Claude proposes ${parts.length} tasks. Edit, remove or add tasks; nothing is saved until you do.`)
    + (editing ? ` They replace “${title(editing)}”.` : "");
  renderSplit();
  split.sheet.returnValue = "";
  split.sheet.showModal();
  split.list.querySelector("input")?.focus();
}

function renderSplit() {
  const parts = split.parts;
  const num = (p) => parts.indexOf(p) + 1;
  split.list.replaceChildren(...parts.map((p, i) => {
    const titleIn = h("input", { class: "split-title", value: p.title, readOnly: !!p.id, placeholder: "Claude names it if empty", autocomplete: "off", "aria-label": `Title of task ${i + 1}`, oninput: () => { p.title = titleIn.value; } });
    const promptIn = h("textarea", { class: "split-prompt", rows: 4, readOnly: !!p.id, placeholder: "Instructions for Claude", "aria-label": `Instructions for task ${i + 1}`, oninput: () => { p.prompt = promptIn.value; } });
    promptIn.value = p.prompt;
    const after = [...p.after].map(num).sort((a, b) => a - b);
    return h("li", { class: `split-part${p.id ? " saved" : ""}` },
      h("div", { class: "split-head" },
        h("span", { class: "split-num", "aria-hidden": "true" }, String(i + 1)),
        titleIn,
        p.id ? h("span", { class: "split-saved" }, "Saved") :
          ib({ type: "button", class: "icon-btn", "aria-label": `Remove task ${i + 1}`, title: "Remove this task", onclick: () => removeSplitPart(p) }, "x")),
      promptIn,
      after.length ? h("p", { class: "hint split-after" }, `Builds on ${after.length > 1 ? "tasks" : "task"} ${after.join(", ")}`) : null);
  }));
  const n = parts.filter((p) => !p.id).length;
  const what = n === 1 ? "task" : `${n} tasks`;
  $("#split-draft-label").textContent = n === 1 ? "Save as draft" : `Save ${n} drafts`;
  $("#split-queue-label").textContent = `Queue ${what}`;
  $("#split-order-row").hidden = !parts.some((p) => p.after.size);
}

function removeSplitPart(p) {
  split.parts = split.parts.filter((x) => x !== p);
  for (const x of split.parts) x.after.delete(p);
  renderSplit();
}
$("#split-add").addEventListener("click", () => {
  split.parts.push({ title: "", prompt: "", after: new Set() });
  renderSplit();
  split.list.lastElementChild?.querySelector("input")?.focus();
});
// Enter in a title goes on to its instructions rather than submitting the
// sheet (whose first button is Close).
split.list.addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || !e.target.matches(".split-title") || e.isComposing) return;
  e.preventDefault();
  e.target.closest(".split-part")?.querySelector(".split-prompt")?.focus();
});
$("#split-form").noValidate = true;

// saveSplit makes the parts not saved yet as tasks of p, with the form's
// priority, model, files and dependencies.
async function saveSplit(parts, p, enqueue) {
  const left = parts.filter((x) => !x.id);
  if (!left.length) throw new Error("Add a task to save.");
  if (left.some((x) => !x.prompt)) throw new Error("Each task needs instructions; remove the empty ones.");
  const f = $("#todo-form");
  await todoAttach.settled();
  const attachments = todoAttach.ids();
  if (attachments.length && p.prefix !== here()) throw new Error(`Attachments can't be sent to ${p.name} on ${p.machine}; remove them or pick a project on this computer.`);
  const common = { priority: formPriority(f.priority.value), model: f.model.value, attachments };
  const deps = depPicker.ids();
  const order = $("#split-order").checked;
  for (const part of left) {
    const depends_on = [...new Set([...deps, ...(order ? [...part.after].map((x) => x.id) : [])])].filter(Boolean);
    const t = await api("POST", `/api/projects/${p.id}/todos`, { ...common, title: part.title, prompt: part.prompt, depends_on, enqueue }, p.prefix || "");
    part.id = t.id;
  }
}

// Saving the parts: they are made one by one, in order, so a part's
// dependencies already have ids. If one fails, the sheet stays with the
// saved ones marked, and saving again makes the rest.
split.sheet.addEventListener("close", attempt(async () => {
  const action = split.sheet.returnValue;
  if (action !== "draft" && action !== "queue") return;
  const parts = split.parts;
  for (const p of parts) { p.title = p.title.trim(); p.prompt = p.prompt.trim(); }
  const project = (pickProject && state.projects.find((x) => projectKey(x) === $("#todo-form").project.value)) || state.project;
  try {
    await saveSplit(parts, project, action === "queue");
  } catch (e) {
    if ($("#todo-sheet").open) { renderSplit(); split.sheet.showModal(); }
    throw e;
  }
  const enqueue = action === "queue";
  const replaced = editing;
  $("#todo-sheet").close("split");
  if (replaced) await api("DELETE", `/api/todos/${replaced.id}`);
  const n = parts.length;
  const other = projectKey(project) !== projectKey(state.project);
  toast(`${enqueue ? `Queued ${n} task${n > 1 ? "s" : ""}` : `Saved ${n} draft${n > 1 ? "s" : ""}`}${replaced ? ` in place of “${title(replaced)}”` : ""}${other ? ` in ${project.name}` : ""}`);
  if (!other) showNewTasks(enqueue ? "queued" : "draft", parts.map((x) => x.id));
}));
$("#todo-form").addEventListener("input", () => draft.changed());
$("#todo-form").addEventListener("change", () => draft.changed());
