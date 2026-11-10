import { codeEditor, invisiblesOn, setInvisibles, setVim, vimOn } from "../editor.js";
import { $ } from "./dom.js";
import { api } from "./api.js";
import { ask, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { loadDiff } from "./detail.js";
import { syncPrefs } from "./prefs.js";

// ------------------------------------------------------------------ editing a task's changes in review
// The reviewer fixes a file of the task before approving it: the file as the
// task's branch has it, and saving commits the new content on that branch
// (on its runner for a task that ran on one), so approving lands it too.
export function canEditDiff(t) { return !!t?.worktree && ["review", "failed"].includes(t.status) && !t.reviewing; }
const diffEditText = $("#diff-edit-text");
const diffEditor = codeEditor(diffEditText);
const diffEdit = { id: null, path: null, blob: "", original: "", saving: false };
export const diffEditDirty = () => !!diffEdit.path && diffEditText.value !== diffEdit.original;

export async function openDiffEdit(id, path) {
  const res = await api("GET", `/api/todos/${id}/file?path=${encodeURIComponent(path)}`);
  if (res.binary) return toast(`${path} isn't text, so it can't be edited here.`);
  if (res.too_large) return toast(`${path} is ${Math.round(res.size / 1024)} KiB; the editor opens files up to 512 KiB.`);
  Object.assign(diffEdit, { id, path, blob: res.blob, original: res.content ?? "", saving: false });
  $("#diff-edit-title").textContent = path;
  $("#diff-edit-hint").textContent = `Saving commits the change on ${res.branch || "the task's branch"}, so approving merges it with the rest.`;
  diffEditor.load(diffEdit.original, path);
  renderDiffEdit();
  $("#diff-edit-sheet").showModal();
  diffEditor.view.focus();
}

function renderDiffEdit() {
  const dirty = diffEditDirty();
  $("#diff-edit-save").disabled = !dirty || diffEdit.saving;
  $("#diff-edit-revert").disabled = !dirty || diffEdit.saving;
  $("#diff-edit-state").textContent = diffEdit.saving ? "Committing…" : dirty ? "Modified" : "";
}

async function saveDiffEdit() {
  if (!diffEditDirty() || diffEdit.saving) return;
  const { id, path, blob } = diffEdit, content = diffEditText.value;
  diffEdit.saving = true;
  renderDiffEdit();
  try {
    await api("PUT", `/api/todos/${id}/file`, { path, blob, content });
    diffEdit.original = content;
  } finally {
    diffEdit.saving = false;
    renderDiffEdit();
  }
  $("#diff-edit-sheet").close();
  toast(`Committed your edit to ${path}`);
  if (state.detail === id) loadDiff();
}

// Closing with unsaved changes asks first (the × and Cancel, or Escape).
async function closeDiffEdit() {
  if (diffEditDirty() && !(await ask({ title: `Discard your changes to ${diffEdit.path}?`, ok: "Discard", danger: true }))) return;
  diffEdit.original = diffEditText.value; // nothing left to lose
  $("#diff-edit-sheet").close();
}
$("#diff-edit-form").addEventListener("submit", (e) => {
  if (e.submitter?.value !== "cancel" || !diffEditDirty()) return;
  e.preventDefault();
  closeDiffEdit().catch(fail);
});
$("#diff-edit-sheet").addEventListener("cancel", (e) => {
  if (!diffEditDirty()) return;
  e.preventDefault();
  closeDiffEdit().catch(fail);
});
$("#diff-edit-sheet").addEventListener("close", () => { diffEdit.path = null; });
diffEditText.addEventListener("input", renderDiffEdit);
diffEditor.el.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "s") { e.preventDefault(); e.stopPropagation(); saveDiffEdit().catch(fail); }
}, true);
$("#diff-edit-save").addEventListener("click", () => saveDiffEdit().catch(fail));
$("#diff-edit-revert").addEventListener("click", () => diffEditor.replace(diffEdit.original));

export function toggleVim() {
  setVim(!vimOn());
  syncPrefs();
  toast(vimOn() ? "Vim keys on in the editors" : "Vim keys off");
}

export function toggleInvisibles() {
  setInvisibles(!invisiblesOn());
  syncPrefs();
  toast(invisiblesOn() ? "Invisible characters shown in the editors" : "Invisible characters hidden");
}
