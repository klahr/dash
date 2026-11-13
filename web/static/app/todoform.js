import { drafts, mdEditor } from "../editor.js";
import { $, $$, h, ib, plural } from "./dom.js";
import { api, here } from "./api.js";
import { ask, attempt, busy, toast } from "./dialogs.js";
import { attachBox } from "./attachments.js";
import { STATUS_LABEL, state } from "./state.js";
import { projectKey } from "./events.js";
import { agoText, byPriority, title } from "./branches.js";
import { editing, pickProject, showNewTasks } from "./todos.js";
import { formPriority } from "./dnd.js";

// ------------------------------------------------------------------ the task form
export const promptEditor = mdEditor($("#todo-form").prompt);
export const todoAttach = attachBox($("#todo-attach"), promptEditor.el, () => draft.changed());

// The task form's "Depends on" picker: the project's unmerged tasks other
// than the one being edited. Ticked tasks must be merged before it starts.
// depOptions lists the tasks t can depend on as checkboxes (name "dep"),
// those in ids ticked. Tasks already chosen stay listed (and kept) even once merged.
export function depOptions(t, ids = []) {
  const sel = new Set(ids);
  return [...state.todos.values()]
    .filter((x) => x.id !== t?.id && (x.status !== "merged" || sel.has(x.id)))
    .sort(byPriority)
    .map((x) => h("label", { class: `dep-option s-${x.status}` },
      h("input", { type: "checkbox", name: "dep", value: x.id, checked: sel.has(x.id) }),
      h("span", { class: "dep-title" }, title(x)),
      h("span", { class: "dep-status" }, STATUS_LABEL[x.status] || x.status)));
}

export const depPicker = (() => {
  const field = $("#todo-deps-field"), list = $("#todo-deps");
  let self = null;
  const reset = (t, ids = []) => {
    self = t;
    const opts = depOptions(t, ids);
    // The list is of the open project; a form picking another one can't use it.
    const usable = !pickProject || projectKey(state.project) === $("#todo-form").project.value;
    field.hidden = !usable || !opts.length;
    list.replaceChildren(...opts);
  };
  $("#todo-form").project.addEventListener("change", () => reset(self, []));
  return {
    reset,
    ids: () => field.hidden ? [] : $$("input[name=dep]:checked", list).map((i) => i.value),
  };
})();

// The task form's "Build on" choice: a finished task (merged or failed) of
// the project whose Claude session the task's first run continues. The most
// recently finished come first; the one chosen stays listed whatever it is.
const FINISHED = new Set(["merged", "failed"]);
const finishedAt = (x) => new Date(x.finished_at || x.updated_at || 0).getTime();
// buildOnOptions lists the tasks t can build on as <option>s, id (the one
// chosen) included; null when there is none to pick from.
export function buildOnOptions(t, id = "") {
  const cur = id ? state.todos.get(id) : null;
  const recent = [...state.todos.values()]
    .filter((x) => x.id !== t?.id && x.id !== id && FINISHED.has(x.status))
    .sort((a, b) => finishedAt(b) - finishedAt(a))
    .slice(0, 20);
  if (!recent.length && !id) return null;
  const opt = (x) => h("option", { value: x.id }, `#${x.id} ${title(x)} · ${STATUS_LABEL[x.status] || x.status}`);
  return [
    h("option", { value: "" }, "Nothing: start fresh"),
    ...(cur ? [opt(cur)] : id ? [h("option", { value: id }, `#${id}`)] : []),
    ...recent.map(opt)];
}

export const buildOnPicker = (() => {
  const field = $("#todo-buildon-field"), sel = $("#todo-form").builds_on;
  let self = null;
  const reset = (t, id = "") => {
    self = t;
    const opts = buildOnOptions(t, id);
    sel.replaceChildren(...(opts || []));
    sel.value = id || "";
    const usable = !pickProject || projectKey(state.project) === $("#todo-form").project.value;
    field.hidden = !usable || !opts;
  };
  $("#todo-form").project.addEventListener("change", () => reset(self, ""));
  return { reset, value: () => field.hidden ? "" : sel.value };
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
    return { title: f.title.value, prompt: f.prompt.value, priority: f.priority.value, model: f.model.value, attachments: todoAttach.items(), depends_on: depPicker.ids(), builds_on: buildOnPicker.value() };
  },
  write(d) {
    const f = $("#todo-form");
    f.title.value = d.title ?? "";
    f.prompt.value = d.prompt ?? "";
    f.priority.value = d.priority ?? "";
    setModelField(f, d.model ?? editing?.model ?? "");
    todoAttach.reset(d.attachments || []);
    depPicker.reset(editing, d.depends_on ?? editing?.depends_on ?? []); // drafts from before dependencies have none
    buildOnPicker.reset(editing, d.builds_on ?? editing?.builds_on ?? "");
    promptEditor.render();
  },
  onStatus(st, at) {
    const el = $("#todo-draft");
    if (st === "typing") return;
    el.dataset.state = st;
    if (st === "saved") { el.replaceChildren("Draft saved on this device"); return; }
    if (st === "restored") {
      el.replaceChildren(`Restored your unsaved draft from ${agoText(at)}. `,
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
  buildOnPicker.reset(t, t?.builds_on || "");
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
  buildOnPicker.reset(null, "");
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
// one task each, with the form's priority, model, files, dependencies and
// the task they build on;
// a part Claude says builds on earlier ones starts after they are merged.
// The form's task itself isn't saved (an edited one is replaced).
export const split = {
  sheet: $("#split-sheet"),
  list: $("#split-parts"),
  parts: [],          // { title, prompt, after: Set of parts, id once created }
  splittable: (t) => !t || t.status === "draft" || t.status === "queued",
  goal: null,         // the goal whose coordinator's plan the sheet shows (openGoalPlan)
  project: null,      // that goal's project
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

// planParts are a proposed plan's items as the sheet's parts: an item's
// after (indexes of earlier items) become references to those parts.
function planParts(items) {
  const made = (items || []).map((p) => ({ title: p.title || "", prompt: p.prompt || "", model: p.model || "", after: new Set() }));
  (items || []).forEach((p, i) => (p.after || []).forEach((j) => { if (j >= 0 && j < i) made[i].after.add(made[j]); }));
  return made;
}

// openGoalPlan shows the coordinator's plan for goal g (of project p) in
// the sheet, to edit (dependencies too) and save as drafts or queue. It is
// accepted as edited: the coordinator learns what changed.
export function openGoalPlan(g, p) {
  split.goal = g;
  split.project = p;
  openSplit(planParts(g.plan));
}

function openSplit(parts) {
  split.parts = parts;
  const one = parts.length === 1;
  const g = split.goal;
  $("#split-hint").textContent = g
    ? `The coordinator proposes ${plural(parts.length, "task")} for “${g.title}”. Edit, remove or add tasks and set which ones each waits for; nothing is made until you save.`
    : (one
      ? "Claude thinks this is small enough for one task. Add more below if you still want to split it."
      : `Claude proposes ${parts.length} tasks. Edit, remove or add tasks; nothing is saved until you do.`)
      + (editing ? ` They replace “${title(editing)}”.` : "");
  $("#split-replan").hidden = !g;
  renderSplit();
  split.sheet.returnValue = "";
  split.sheet.showModal();
  split.list.querySelector("input")?.focus();
}

function renderSplit() {
  const parts = split.parts;
  const focus = document.activeElement?.closest?.(".split-dep") && [$$(".split-part", split.list).indexOf(document.activeElement.closest(".split-part")), document.activeElement.textContent];
  const num = (p) => parts.indexOf(p) + 1;
  split.list.replaceChildren(...parts.map((p, i) => {
    const titleIn = h("input", { class: "split-title", value: p.title, readOnly: !!p.id, placeholder: "Claude names it if empty", autocomplete: "off", "aria-label": `Title of task ${i + 1}`, oninput: () => { p.title = titleIn.value; } });
    const promptIn = h("textarea", { class: "split-prompt", rows: 4, readOnly: !!p.id, placeholder: "Instructions for Claude", "aria-label": `Instructions for task ${i + 1}`, oninput: () => { p.prompt = promptIn.value; } });
    promptIn.value = p.prompt;
    // The earlier tasks this one waits for: a toggle each (it starts once they are merged).
    const earlier = parts.slice(0, i);
    const deps = !p.id && earlier.length ? h("div", { class: "split-deps", role: "group", "aria-label": `Task ${i + 1} starts after` },
      h("span", { class: "hint" }, "After"),
      ...earlier.map((x) => h("button", {
        type: "button", class: "split-dep", "aria-pressed": String(p.after.has(x)),
        title: `${p.after.has(x) ? "Starts after" : "Doesn't wait for"} task ${num(x)}${x.title ? `: ${x.title}` : ""}`,
        onclick: () => { p.after.has(x) ? p.after.delete(x) : p.after.add(x); renderSplit(); },
      }, String(num(x))))) : null;
    const after = [...p.after].map(num).sort((a, b) => a - b);
    return h("li", { class: `split-part${p.id ? " saved" : ""}` },
      h("div", { class: "split-head" },
        h("span", { class: "split-num", "aria-hidden": "true" }, String(i + 1)),
        titleIn,
        p.id ? h("span", { class: "split-saved" }, "Saved") :
          ib({ type: "button", class: "icon-btn", "aria-label": `Remove task ${i + 1}`, title: "Remove this task", onclick: () => removeSplitPart(p) }, "x")),
      promptIn,
      deps || (after.length ? h("p", { class: "hint split-after" }, `Builds on ${after.length > 1 ? "tasks" : "task"} ${after.join(", ")}`) : null));
  }));
  if (focus) $$(".split-dep", $$(".split-part", split.list)[focus[0]]).find((b) => b.textContent === focus[1])?.focus();
  const n = parts.filter((p) => !p.id).length;
  const what = n === 1 ? "task" : `${n} tasks`;
  $("#split-draft-label").textContent = n === 1 ? "Save as draft" : `Save ${n} drafts`;
  $("#split-queue-label").textContent = `Queue ${what}`;
  $("#split-order-row").hidden = !!split.goal || !parts.some((p) => p.after.size);
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
  const common = { priority: formPriority(f.priority.value), model: f.model.value, attachments, builds_on: buildOnPicker.value() };
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
  if (split.goal) return acceptGoalPlan(action);
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
// acceptGoalPlan saves the goal's plan as the sheet has it: its tasks are
// made as drafts, or queued. If that fails the sheet opens again.
async function acceptGoalPlan(action) {
  const g = split.goal, p = split.project || state.project;
  if (action !== "draft" && action !== "queue") { split.goal = split.project = null; return; }
  const parts = split.parts;
  for (const x of parts) { x.title = x.title.trim(); x.prompt = x.prompt.trim(); }
  const reopen = () => { renderSplit(); split.sheet.showModal(); };
  if (!parts.length) { reopen(); throw new Error("Add a task to save, or ask the coordinator to plan again."); }
  if (parts.some((x) => !x.prompt && !x.title)) { reopen(); throw new Error("Each task needs instructions; remove the empty ones."); }
  const plan = parts.map((x) => ({ title: x.title, prompt: x.prompt || x.title, model: x.model || "", after: [...x.after].map((y) => parts.indexOf(y)).filter((j) => j >= 0).sort((a, b) => a - b) }));
  const queue = action === "queue";
  let res;
  try {
    res = await api("POST", `/api/goals/${g.id}/plan/accept`, { plan, queue }, p?.prefix || "");
  } catch (e) { reopen(); throw e; }
  split.goal = split.project = null;
  const ids = (res.created || []).map((t) => t.id);
  toast(`${queue ? `Queued ${plural(plan.length, "task")}` : `Saved ${plural(plan.length, "draft")}`} for “${g.title}”`, { tone: "ok", icon: "target" });
  if (projectKey(p) === projectKey(state.project) && ids.length) showNewTasks(queue ? "queued" : "draft", ids);
}

// Plan again: the coordinator is told what to change and proposes another plan.
$("#split-replan").addEventListener("click", attempt(async () => {
  const g = split.goal, p = split.project || state.project;
  if (!g) return;
  const feedback = await ask({ title: "Plan again", body: "Tell the coordinator what to change. It plans again; the goal is planning until its new plan is in.", input: true, placeholder: "Fewer, larger tasks; leave the API alone…", ok: "Plan again" });
  if (feedback === null) return;
  await api("POST", `/api/goals/${g.id}/plan/reject`, { feedback }, p?.prefix || "");
  split.sheet.close("replan");
  toast("The coordinator plans again", { icon: "target" });
}));
$("#todo-form").addEventListener("input", () => draft.changed());
$("#todo-form").addEventListener("change", () => draft.changed());
