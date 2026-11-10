import { codeEditor } from "../editor.js";
import { $, fill, h } from "./dom.js";
import { api } from "./api.js";
import { ask, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { diffEditDirty } from "./diffedit.js";

// ------------------------------------------------------------------ files
// The default branch's files as a tree, and a code editor. Saving
// doesn't write to the branch: it queues a task that commits the edit in its
// own worktree and goes to review. Files git doesn't track (untracked or
// ignored, like .env) are listed too and saved straight to the checkout.
// Each project keeps its open file, so switching projects loses nothing.
const fileSessions = new Map(); // project key → { list, local, tree, open, path, isLocal, blob, original, text, note, size, saved }
const fileText = $("#file-text");
const fileEditor = codeEditor(fileText);
let fileShown = {}; // the session and path the editor holds; another one starts a fresh undo history
function fileSession() {
  if (!state.project) return null;
  const k = projectKey(state.project);
  if (!fileSessions.has(k)) fileSessions.set(k, { list: null, tree: null, open: new Set(), path: null });
  return fileSessions.get(k);
}
const fileDirty = (s) => !!s?.path && !s.note && s.text !== s.original;

export async function loadFiles() {
  const s = fileSession(), p = state.project;
  renderFileTree();
  renderFile();
  if (!s) return;
  const res = await api("GET", `/api/projects/${p.id}/files`);
  s.local = new Set(res.local || []);
  s.list = [...res.files, ...s.local].sort();
  s.tree = null;
  if (fileSession() === s) renderFileTree();
}

function fileTreeOf(s) {
  if (s.tree) return s.tree;
  const node = () => ({ dirs: new Map(), files: [] });
  s.tree = node();
  for (const f of s.list) {
    const parts = f.split("/");
    let n = s.tree;
    for (const d of parts.slice(0, -1)) {
      if (!n.dirs.has(d)) n.dirs.set(d, node());
      n = n.dirs.get(d);
    }
    n.files.push(parts.at(-1));
  }
  return s.tree;
}

const fileItem = (s, path, label) => h("button", {
  type: "button", class: `file-item${s.local?.has(path) ? " local" : ""}`,
  title: s.local?.has(path) ? `${path}\nNot in git: saving writes it straight to disk` : path, "aria-current": s.path === path ? "true" : null,
  onclick: () => openFile(path).catch(fail),
}, label);

// Folders render their contents when first opened, so big repositories stay quick.
function dirNodes(s, node, prefix) {
  const dirs = [...node.dirs.keys()].sort().map((name) => {
    const path = prefix + name, sub = node.dirs.get(name);
    const d = h("details", { class: "file-dir", open: s.open.has(path) }, h("summary", { title: path }, name));
    const fill = () => { if (d.childElementCount === 1) d.append(h("div", { class: "file-kids" }, ...dirNodes(s, sub, path + "/"))); };
    if (d.open) fill();
    d.addEventListener("toggle", () => { if (d.open) { s.open.add(path); fill(); } else s.open.delete(path); });
    return d;
  });
  return [...dirs, ...[...node.files].sort().map((name) => fileItem(s, prefix + name, name))];
}

export function renderFileTree() {
  const s = fileSession(), tree = $("#file-tree");
  const note = (text) => h("p", { class: "file-note" }, text);
  if (!s) return tree.replaceChildren(note("Select a project to browse its files."));
  if (!s.list) return tree.replaceChildren(note("Loading…"));
  if (!s.list.length) return tree.replaceChildren(note("No files on the default branch yet."));
  const q = $("#file-filter").value.trim().toLowerCase();
  if (!q) return tree.replaceChildren(...dirNodes(s, fileTreeOf(s), ""));
  const hits = s.list.filter((f) => f.toLowerCase().includes(q)), max = 300;
  fill(tree, ...hits.slice(0, max).map((f) => fileItem(s, f, f)),
    hits.length > max ? note(`${hits.length - max} more; type more to narrow it down`) : null,
    hits.length ? null : note("No matching files"));
}

export function renderFile() {
  const s = fileSession(), editable = !!s?.path && !s.note, dirty = fileDirty(s);
  $("#file-head").hidden = !s?.path;
  fileEditor.el.hidden = !editable;
  $("#file-empty").hidden = editable;
  if (s?.path) $("#file-path").textContent = s.path;
  if (editable && (fileShown.s !== s || fileShown.path !== s.path || fileText.value !== s.text)) {
    fileEditor.load(s.text, s.path);
    fileShown = { s, path: s.path };
  }
  $("#file-save").disabled = $("#file-revert").disabled = !dirty;
  $("#file-state").textContent = s?.isNew && !s.saved ? "New file, not on the branch yet" : dirty ? "Modified" : editable && s.saved ? (s.isLocal ? "Saved" : "Saved as a task") : s?.isLocal ? "Local file, not in git" : "";
  const save = $("#file-save");
  save.lastChild.textContent = s?.isLocal ? "Save" : "Save as task";
  save.title = s?.isLocal ? "Save to disk (Ctrl+S)" : "Save as a task (Ctrl+S)";
  $("#file-empty-title").textContent = s?.note === "binary" ? "Binary file" : s?.note === "large" ? "File too large" : "No file open";
  $("#file-empty-text").textContent = s?.note === "binary" ? `${s.path} isn't text, so it can't be edited here.`
    : s?.note === "large" ? `${s.path} is ${Math.round(s.size / 1024)} KiB; the editor opens files up to 512 KiB.`
    : s ? "Pick a file on the left to view and edit it. Saving creates a task that goes to review like any other; local files git doesn't track, like .env, are saved straight to disk."
    : "Select a project to browse its files.";
}

export async function openFile(path) {
  const s = fileSession(), p = state.project;
  if (!s || s.path === path) return;
  if (fileDirty(s) && !(await ask({ title: `Discard your changes to ${s.path}?`, ok: "Discard", danger: true }))) return;
  const isLocal = !!s.local?.has(path);
  const res = await api("GET", `/api/projects/${p.id}/file?path=${encodeURIComponent(path)}${isLocal ? "&local=1" : ""}`, null, p.prefix || "");
  const text = res.content ?? "";
  Object.assign(s, { path, isLocal, isNew: false, blob: res.blob, original: text, text, size: res.size, saved: false,
    note: res.binary ? "binary" : res.too_large ? "large" : "" });
  if (fileSession() !== s) return;
  renderFile();
  renderFileTree();
}

// newFile opens a file that isn't on the branch yet, starting from
// template; saving it opens a task that creates it.
export async function newFile(path, template) {
  const s = fileSession();
  if (!s) return;
  if (s.list?.includes(path)) return openFile(path);
  if (fileDirty(s) && !(await ask({ title: `Discard your changes to ${s.path}?`, ok: "Discard", danger: true }))) return;
  Object.assign(s, { path, isLocal: false, isNew: true, blob: "", original: "", text: template, size: 0, saved: false, note: "" });
  renderFile();
  renderFileTree();
}

async function saveFile() {
  const s = fileSession(), p = state.project;
  if (!fileDirty(s)) return;
  const { path, blob, text } = s;
  if (s.isLocal) {
    const res = await api("PUT", `/api/projects/${p.id}/file`, { path, blob, content: text }, p.prefix || "");
    if (s.path === path) { s.original = text; s.blob = res.blob; s.saved = true; }
    if (fileSession() === s) renderFile();
    toast(`Saved ${path}`);
    return;
  }
  const title = await ask({
    title: "Save as a task", input: true, placeholder: `${s.isNew ? "Create" : "Edit"} ${path}`, ok: "Save as task",
    body: s.isNew ? `${path} is created and committed in its own worktree and goes to review like any other task. Name it, or leave it blank.`
      : `The edit to ${path} is committed in its own worktree and goes to review like any other task. Name it, or leave it blank.`,
  });
  if (title == null) return;
  const t = await api("POST", `/api/projects/${p.id}/edits`, { path, blob, content: text, title }, p.prefix || "");
  if (s.path === path) { s.original = text; s.saved = true; }
  if (fileSession() === s) renderFile();
  toast(`Saved as task “${t.title}”`);
}

fileText.addEventListener("input", () => {
  const s = fileSession();
  if (!s) return;
  const was = fileDirty(s);
  s.text = fileText.value;
  if (was !== fileDirty(s)) renderFile();
});
// Before CodeMirror and vim see it. Tab indents; Escape first to move focus on with Tab.
fileEditor.el.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "s") { e.preventDefault(); e.stopPropagation(); saveFile().catch(fail); }
}, true);
$("#file-save").addEventListener("click", () => saveFile().catch(fail));
$("#file-revert").addEventListener("click", () => {
  const s = fileSession();
  if (!s) return;
  fileEditor.replace(s.original); // undoable; its input event updates s.text
});
$("#file-filter").addEventListener("input", renderFileTree);
addEventListener("beforeunload", (e) => { if ([...fileSessions.values()].some(fileDirty) || diffEditDirty()) e.preventDefault(); });
