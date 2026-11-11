import { $, $$, h, ib, ico } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { openTodo } from "./detail.js";
import { keys } from "./vim.js";

// ------------------------------------------------------------------ checklist
// The project's TODO.md as a task list. Changes show at once and are sent
// together a moment after the last one, as one commit to the default branch
// (or one write of a local TODO.md git doesn't track). Refs are the server's
// (line + hash of the item's text): a stale one gets a 409, after which the
// list is read again.
const CL_DELAY = 2000;
export const cl = {
  key: "",          // projectKey of the project shown
  doc: null,        // the checklist as last read or saved
  pending: [],      // ops not sent yet: {op, ref|tmp, …}
  timer: null,
  saving: null,     // the save in flight (a promise)
  tmp: 0,           // ids of items added but not saved yet
  cursor: null,     // ref (or "tmp:n") of the item the keys act on
  editing: null,    // ref being edited
  menu: null,       // ref whose move menu is open
  drag: null,       // the item being dragged by its grip: {ref, drop}
  stale: null,      // TODO.md changed elsewhere while there were unsaved changes: its new version (true: not known)
};

const clItemsOf = (doc) => (doc?.sections || []).flatMap((s) => s.entries.filter((e) => e.item).map((e) => e.item));

function resetChecklist() {
  clearTimeout(cl.timer);
  Object.assign(cl, { key: projectKey(state.project), doc: null, pending: [], timer: null, cursor: null, editing: null, menu: null, stale: null });
  renderChecklist();
}

export async function loadChecklist() {
  const p = state.project;
  if (projectKey(p) !== cl.key) {
    await flushChecklist().catch(() => {});
    resetChecklist();
  }
  if (!p) return renderChecklist();
  await flushChecklist();
  cl.stale = null; // what is read now is as new as it gets
  const doc = await api("GET", `/api/projects/${p.id}/checklist`);
  if (projectKey(state.project) !== projectKey(p)) return;
  cl.doc = doc;
  renderChecklist();
}

// clChanged is told that the open project's TODO.md changed (checklist.updated:
// a commit from another tab or device, the board's MCP tools, a merge, pull
// or sync, or a save of an untracked TODO.md). The checklist on screen reads
// it again, unless that is the version it shows already (its own save); with
// changes not saved yet, or an item being edited, it waits for those: saving
// answers with the file as committed, and a clash is a 409 that reads it
// again. A checklist not on screen is read when it is opened.
export function clChanged(version) {
  if (state.view !== "checklist" || !cl.doc) return;
  if (version && version === cl.doc.version) return;
  cl.stale = version || true;
  clCatchUp();
}

// clCatchUp reads TODO.md again if it changed elsewhere and nothing of the
// user's is in the way any more.
function clCatchUp() {
  if (!cl.stale || cl.pending.length || cl.editing || cl.saving || state.view !== "checklist") return;
  if (cl.stale === cl.doc?.version) { cl.stale = null; return; }
  loadChecklist().catch(fail);
}

// queueOp records a change, shows it, and saves it after CL_DELAY.
function queueOp(op) {
  const same = (o) => o.ref && o.ref === op.ref && o.op === op.op;
  if (op.op === "add" || op.op === "reorder") {
    cl.pending.push(op);
  } else if (op.tmp) {
    // A change to an item added but not saved yet changes its add.
    const add = cl.pending.find((o) => o.op === "add" && o.tmp === op.tmp);
    if (!add) return;
    if (op.op === "check") add.checked = op.checked;
    if (op.op === "edit") { add.text = op.text; add.more = op.more; }
    if (op.op === "delete") cl.pending = cl.pending.filter((o) => o !== add);
  } else {
    cl.pending = cl.pending.filter((o) => !same(o));
    cl.pending.push(op);
  }
  renderChecklist();
  clearTimeout(cl.timer);
  cl.timer = setTimeout(() => flushChecklist().catch(fail), CL_DELAY);
}

// flushChecklist sends the pending changes now (after a save in flight).
export async function flushChecklist() {
  clearTimeout(cl.timer);
  cl.timer = null;
  while (cl.saving) await cl.saving.catch(() => {});
  if (!cl.pending.length || !cl.doc) return;
  const p = state.projects?.find((x) => projectKey(x) === cl.key) || state.project;
  const ops = cl.pending.map(({ tmp, ...o }) => o);
  const sent = cl.pending;
  cl.pending = [];
  cl.stale = null; // the save answers with TODO.md as it is after it
  renderChecklistState("Saving…");
  cl.saving = api("POST", `/api/projects/${p.id}/checklist`, { version: cl.doc.version, ops }, p.prefix || "");
  try {
    const res = await cl.saving;
    if (projectKey(state.project) !== cl.key) return;
    cl.doc = res.checklist;
    // Changes made while this one was saving refer to the old refs.
    for (const o of cl.pending) for (const k of ["ref", "before", "after"]) if (o[k]) o[k] = res.refs?.[o[k]] || o[k];
    if (cl.cursor && res.refs?.[cl.cursor]) cl.cursor = res.refs[cl.cursor];
    if (cl.cursor?.startsWith("tmp:")) cl.cursor = null;
    if (res.task) toast(res.note || "", { head: `Task ${res.task.id} will commit TODO.md`, action: openTodo(res.task.id) });
    renderChecklist();
  } catch (e) {
    if (projectKey(state.project) !== cl.key) return;
    if (e.status === 409) {
      toast("TODO.md changed elsewhere since it was read; it was reloaded. Make your change again.", { tone: "error", head: "Not saved" });
    } else {
      cl.pending = [...sent, ...cl.pending]; // keep them to retry
      fail(e);
      renderChecklist();
      return;
    }
    cl.pending = [];
    cl.saving = null; // or loadChecklist's flush waits for this save forever
    await loadChecklist().catch(fail);
  } finally {
    cl.saving = null;
    clCatchUp(); // changed elsewhere while this was saving
  }
}

// The checklist as shown: what was read, with the pending changes on top.
function clView() {
  const checks = new Map(), edits = new Map(), gone = new Set(), adds = [], moves = [];
  for (const o of cl.pending) {
    if (o.op === "check") checks.set(o.ref, o.checked);
    if (o.op === "edit") edits.set(o.ref, o);
    if (o.op === "delete") gone.add(o.ref);
    if (o.op === "add") adds.push(o);
    if (o.op === "reorder") moves.push(o);
  }
  const sections = (cl.doc?.sections || []).map((s) => ({
    ...s,
    entries: s.entries.filter((e) => !e.item || !gone.has(e.item.ref)).map((e) => {
      if (!e.item) return e;
      const it = { ...e.item };
      if (checks.has(it.ref)) { it.checked = checks.get(it.ref); it.pending = true; }
      if (edits.has(it.ref)) { it.text = edits.get(it.ref).text; it.more = edits.get(it.ref).more ?? it.more; it.pending = true; }
      return { item: it };
    }),
  }));
  for (const m of moves) clReorder(sections, m);
  for (const a of adds) {
    const title = (a.section || "").trim();
    let s = title ? sections.find((x) => x.title.toLowerCase() === title.toLowerCase()) : sections.at(-1);
    if (!s) { s = { title, level: 2, entries: [] }; sections.push(s); }
    s.entries.push({ item: { ref: `tmp:${a.tmp}`, tmp: a.tmp, text: a.text.split("\n")[0], more: a.text.split("\n").slice(1).join("\n"), checked: !!a.checked, indent: 0, pending: true } });
  }
  return sections;
}

// clBlock finds an item in sections and the entries it spans: it and the
// items nested under it.
function clBlock(sections, ref) {
  for (const s of sections) {
    const i = s.entries.findIndex((e) => e.item?.ref === ref);
    if (i < 0) continue;
    let j = i + 1;
    while (j < s.entries.length && s.entries[j].item && s.entries[j].item.indent > s.entries[i].item.indent) j++;
    return { s, i, n: j - i };
  }
  return null;
}

// clReorder makes a reorder op to the sections on screen, as the server makes
// it to TODO.md: the item and its nested items, indented as their new neighbour.
function clReorder(sections, o) {
  const from = clBlock(sections, o.ref);
  if (!from) return;
  const block = from.s.entries.slice(from.i, from.i + from.n);
  if (block.some((e) => [o.before, o.after].includes(e.item.ref))) return;
  from.s.entries.splice(from.i, from.n);
  const to = clBlock(sections, o.before || o.after);
  let s, at, indent = 0;
  if (to) {
    s = to.s;
    at = o.before ? to.i : to.i + to.n;
    indent = to.s.entries[to.i].item.indent || 0;
  } else {
    s = sections.find((x) => x.title && x.title.toLowerCase() === (o.section || "").toLowerCase());
    if (!s) { from.s.entries.splice(from.i, 0, ...block); return; }
    at = s.entries.length;
    indent = s.entries.find((e) => e.item)?.item.indent || 0;
  }
  const d = indent - (block[0].item.indent || 0);
  s.entries.splice(at, 0, ...block.map((e) => ({ item: { ...e.item, indent: Math.max(0, (e.item.indent || 0) + d), pending: true } })));
}

// clStep is the reorder op that moves an item one place up (-1) or down (1):
// past its neighbour at its level, or else to the end of the section above or
// the top of the one below.
function clStep(it, d) {
  const sections = clView();
  const at = clBlock(sections, it.ref);
  if (!at) return null;
  const { s, i, n } = at, ind = it.indent || 0;
  const items = s.entries.map((e, k) => ({ it: e.item, k })).filter((x) => x.it && !x.it.tmp);
  if (d < 0) {
    const prev = items.filter((x) => x.k < i && (x.it.indent || 0) <= ind).at(-1);
    if (prev) return { op: "reorder", ref: it.ref, before: prev.it.ref };
  } else {
    const next = items.find((x) => x.k >= i + n && (x.it.indent || 0) <= ind);
    if (next) return (next.it.indent || 0) < ind ? { op: "reorder", ref: it.ref, before: next.it.ref } : { op: "reorder", ref: it.ref, after: next.it.ref };
  }
  const shown = sections.filter((x) => x.title || x.entries.length);
  const other = shown[shown.indexOf(s) + d];
  if (!other) return null;
  const its = other.entries.map((e) => e.item).filter((x) => x && !x.tmp);
  if (d > 0 && its.length) return { op: "reorder", ref: it.ref, before: its[0].ref };
  const top = its.filter((x) => (x.indent || 0) === Math.min(...its.map((y) => y.indent || 0))).at(-1);
  if (top) return { op: "reorder", ref: it.ref, after: top.ref };
  return other.title ? { op: "reorder", ref: it.ref, section: other.title } : null;
}

function clStepItem(it, d) {
  const op = clStep(it, d);
  if (!op) return;
  cl.cursor = it.ref;
  queueOp(op);
  $(`#cl-body .cl-item[data-ref="${CSS.escape(it.ref)}"]`)?.scrollIntoView({ block: "nearest" });
}

// Dragging an item by its grip moves it, with its nested items, to the gap
// it is dropped in (indented as the item below it, or above it at the end of
// a list), or onto a section's heading: to the top of that section.
function clDragStart(e, it) {
  if (e.button > 0) return;
  e.preventDefault();
  e.currentTarget.setPointerCapture?.(e.pointerId);
  cl.drag = { ref: it.ref, drop: null };
  cl.cursor = it.ref;
  $(`#cl-body .cl-item[data-ref="${CSS.escape(it.ref)}"]`)?.classList.add("dragging");
  document.body.classList.add("is-dragging");
}

function clDropAt(x, y) {
  const el = document.elementFromPoint(x, y);
  const sections = clView();
  const block = clBlock(sections, cl.drag.ref);
  const mine = new Set(block ? block.s.entries.slice(block.i, block.i + block.n).map((e) => e.item.ref) : [cl.drag.ref]);
  const head = el?.closest("#cl-body .cl-section > h3");
  if (head) {
    const s = sections.find((x) => x.title === head.dataset.title);
    const first = s?.entries.find((e) => e.item && !e.item.tmp)?.item;
    if (first) return mine.has(first.ref) ? null : { el: $(`#cl-body .cl-item[data-ref="${CSS.escape(first.ref)}"]`), pos: "before", op: { before: first.ref } };
    return s?.title ? { el: head, pos: "into", op: { section: s.title } } : null;
  }
  const row = el?.closest("#cl-body .cl-item");
  if (!row || row.dataset.ref.startsWith("tmp:") || mine.has(row.dataset.ref)) return null;
  const r = row.getBoundingClientRect();
  if (y < r.top + r.height / 2) return { el: row, pos: "before", op: { before: row.dataset.ref } };
  let next = row.nextElementSibling;
  while (next && mine.has(next.dataset.ref)) next = next.nextElementSibling;
  if (next && !next.dataset.ref.startsWith("tmp:")) return { el: next, pos: "before", op: { before: next.dataset.ref } };
  return { el: row, pos: "after", op: { after: row.dataset.ref } };
}

function clDragShow(drop) {
  $$("#cl-body .drop-before, #cl-body .drop-after, #cl-body .drop-into").forEach((el) => el.classList.remove("drop-before", "drop-after", "drop-into"));
  drop?.el?.classList.add(`drop-${drop.pos}`);
}

document.addEventListener("pointermove", (e) => {
  if (!cl.drag) return;
  cl.drag.drop = clDropAt(e.clientX, e.clientY);
  clDragShow(cl.drag.drop);
});
document.addEventListener("pointerup", () => {
  if (!cl.drag) return;
  const { ref, drop } = cl.drag;
  cl.drag = null;
  document.body.classList.remove("is-dragging");
  if (drop) queueOp({ op: "reorder", ref, ...drop.op });
  else renderChecklist();
});
document.addEventListener("pointercancel", () => {
  if (!cl.drag) return;
  cl.drag = null;
  document.body.classList.remove("is-dragging");
  renderChecklist();
});

function renderChecklistState(msg) {
  const d = cl.doc;
  let text = msg;
  if (!text && d) {
    const where = { commit: `committed to ${d.branch}`, local: "saved in the checkout (git doesn't track it)", task: "a task commits changes (pull request mode)" }[d.via] || "";
    text = cl.pending.length ? "Unsaved changes…" : d.exists ? `On ${d.branch}; changes are ${where}` : "";
  }
  $("#cl-state").textContent = text || "";
}

function renderChecklist() {
  const body = $("#cl-body"), p = state.project, d = cl.doc;
  const sections = d ? clView() : [];
  const exists = !!d && (d.exists || cl.pending.length > 0);
  $("#cl-empty").hidden = !p || !d || exists;
  if (!p) { $("#cl-empty").hidden = false; $("#cl-empty-title").textContent = "No project"; $("#cl-empty-text").textContent = "Select a project to see its TODO.md."; $("#cl-create").hidden = true; }
  else { $("#cl-empty-title").textContent = "No TODO.md"; $("#cl-empty-text").textContent = "This project has no TODO.md at the root of its repository. Add a first item to create one."; $("#cl-create").hidden = false; }
  $("#cl-add").hidden = !exists;
  renderChecklistState();
  const all = sections.flatMap((s) => s.entries.filter((e) => e.item).map((e) => e.item));
  const done = all.filter((it) => it.checked).length;
  $("#cl-progress").hidden = !all.length;
  $("#cl-progress").classList.toggle("complete", !!all.length && done === all.length);
  $("#cl-bar-fill").style.width = `${all.length ? (done / all.length) * 100 : 0}%`;
  $("#cl-count").textContent = done === all.length ? `All ${all.length} done` : `${done} of ${all.length} done`;
  const sel = $("#cl-add-section"), keep = sel.value;
  sel.replaceChildren(...sections.filter((s) => s.title).map((s) => h("option", { value: s.title }, s.title)));
  sel.hidden = !sel.options.length;
  if ([...sel.options].some((o) => o.value === keep)) sel.value = keep;
  else if (sel.options.length) sel.value = sel.options[Math.min(1, sel.options.length - 1)].value; // the first section under the title
  body.replaceChildren(...sections.filter((s) => s.title || s.entries.length).map((s) => {
    const kids = [];
    let list = null;
    for (const e of s.entries) {
      if (!e.item) { list = null; kids.push(h("p", { class: "cl-note" }, e.text)); continue; }
      if (!list) { list = h("ul", { class: "cl-list" }); kids.push(list); }
      list.append(clItem(e.item));
    }
    const items = s.entries.filter((e) => e.item), n = items.filter((e) => e.item.checked).length;
    const count = items.length ? h("span", { class: `cl-section-count${n === items.length ? " complete" : ""}` }, `${n}/${items.length}`) : null;
    return h("section", { class: `cl-section lvl-${s.level}` }, s.title ? h("h3", { "data-title": s.title }, h("span", {}, s.title), count) : null, ...kids);
  }));
  if (cl.editing) body.querySelector(".cl-edit textarea")?.focus();
}

function clOp(it, op) { return it.tmp ? { ...op, tmp: it.tmp } : { ...op, ref: it.ref }; }

function clItem(it) {
  const label = it.text || "(empty)";
  const row = h("li", {
    class: `cl-item${it.checked ? " done" : ""}${it.pending ? " pending" : ""}${cl.cursor === it.ref && keys.active ? " cursor" : ""}`,
    "data-ref": it.ref, tabindex: "-1", style: `--indent: ${Math.min(6, Math.round((it.indent || 0) / 2))}`,
  });
  if (!it.tmp) {
    row.append(h("button", { type: "button", class: "cl-grip", title: "Drag to reorder (+ moves it up, - down)", "aria-label": `Reorder ${label}`,
      onpointerdown: (e) => clDragStart(e, it) }, ico("grip")));
  } else row.append(h("span", { class: "cl-grip" }));
  const box = h("input", { type: "checkbox", checked: it.checked, "aria-label": `${it.checked ? "Uncheck" : "Check off"} ${label}`,
    onchange: (e) => { cl.cursor = it.ref; queueOp(clOp(it, { op: "check", checked: e.target.checked })); } });
  row.append(h("label", { class: "cl-check" }, box));
  if (cl.editing === it.ref) {
    const ta = h("textarea", { "aria-label": "Item text" });
    ta.value = it.more ? `${it.text}\n${it.more}` : it.text;
    const done = (save) => {
      cl.editing = null;
      const [first, ...rest] = ta.value.trim().split("\n");
      if (save && first.trim() && ta.value.trim() !== (it.more ? `${it.text}\n${it.more}` : it.text)) {
        queueOp(clOp(it, { op: "edit", text: first.trim(), more: rest.join("\n").trim() }));
      } else { renderChecklist(); clCatchUp(); }
    };
    ta.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); done(true); }
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); done(false); }
    });
    ta.addEventListener("blur", () => { if (cl.editing === it.ref) done(true); });
    row.append(h("div", { class: "cl-edit" }, ta));
    return row;
  }
  row.append(h("span", { class: "cl-text", title: "Click to edit", onclick: () => { cl.editing = it.ref; renderChecklist(); } },
    it.text, it.more ? h("span", { class: "cl-more" }, it.more) : null));
  const acts = h("span", { class: "cl-actions" });
  if (!it.checked && !it.tmp) {
    const split = h("span", { class: "cl-move" },
      h("button", { type: "button", title: "Move to a draft task (m)", onclick: () => attempt(moveChecklistItem)(it, false) }, ico("draft"), h("span", { class: "cl-move-label" }, "Move to draft")),
      h("button", { type: "button", title: "More ways to move it", "aria-label": `Move ${label}: more`, "aria-haspopup": "menu", "aria-expanded": String(cl.menu === it.ref),
        onclick: (e) => { e.stopPropagation(); cl.menu = cl.menu === it.ref ? null : it.ref; renderChecklist(); $(".cl-menu button")?.focus(); } }, ico("down")));
    acts.append(split);
    if (cl.menu === it.ref) {
      row.append(h("div", { class: "cl-menu", role: "menu" },
        h("button", { type: "button", role: "menuitem", onclick: () => { cl.menu = null; attempt(moveChecklistItem)(it, false); } }, ico("draft"), "Move to draft"),
        h("button", { type: "button", role: "menuitem", onclick: () => { cl.menu = null; attempt(moveChecklistItem)(it, true); } }, ico("queue"), "Move & queue")));
    }
  }
  acts.append(ib({ type: "button", class: "cl-icon cl-del", title: "Delete", "aria-label": `Delete ${label}`, onclick: () => attempt(deleteChecklistItem)(it) }, "trash"));
  row.append(acts);
  return row;
}
document.addEventListener("click", (e) => { if (cl.menu && !e.target.closest(".cl-menu, .cl-move")) { cl.menu = null; renderChecklist(); } });

async function deleteChecklistItem(it) {
  if (!await ask({ title: `Delete "${it.text}"?`, body: "It is removed from TODO.md, with its continuation lines and any items nested under it.", ok: "Delete", danger: true })) return;
  queueOp(clOp(it, { op: "delete" }));
}

// moveChecklistItem makes a task of an item (a draft, or queued) and takes
// the item out of TODO.md, right away.
async function moveChecklistItem(it, queue) {
  const p = state.project;
  await flushChecklist();
  const ref = clItemsOf(cl.doc).find((x) => x.ref === it.ref || x.text === it.text)?.ref || it.ref;
  const res = await api("POST", `/api/projects/${p.id}/checklist/move`, { ref, version: cl.doc.version, queue }).catch(async (e) => {
    if (e.status === 409) await loadChecklist().catch(() => {});
    throw e;
  });
  const t = res.task;
  if (res.removed) {
    toast(t.title, { tone: "ok", head: queue ? `Queued as task ${t.id}` : `Moved to draft ${t.id}`, action: openTodo(t.id) });
    if (res.saved?.checklist) cl.doc = res.saved.checklist;
    if (res.saved?.task) toast(res.saved.note || "", { head: `Task ${res.saved.task.id} removes it from TODO.md` });
    renderChecklist();
  } else {
    toast(`${res.error}. The item is still in TODO.md.`, { tone: "error", head: `Created task ${t.id}`, action: openTodo(t.id) });
    await loadChecklist();
  }
}

function addChecklistItem(text, section) {
  text = text.trim();
  if (!text) return;
  queueOp({ op: "add", text, section: section || "", tmp: ++cl.tmp });
}

$("#cl-add").addEventListener("submit", (e) => {
  e.preventDefault();
  addChecklistItem($("#cl-add-text").value, $("#cl-add-section").value);
  $("#cl-add-text").value = "";
});
$("#cl-create").addEventListener("click", attempt(async () => {
  const text = await ask({ title: "Create TODO.md", body: "Its first item:", input: true, placeholder: "Something to do", ok: "Create" });
  if (text) { addChecklistItem(text, ""); await flushChecklist(); }
}));
addEventListener("pagehide", () => { if (cl.pending.length) flushChecklist().catch(() => {}); });
document.addEventListener("visibilitychange", () => { if (document.hidden && cl.pending.length) flushChecklist().catch(() => {}); });

// Keys on the checklist view: j/k move, x toggles, m/M move, +/- move the
// item up or down, e edits, dd deletes, a adds, r reads TODO.md again (it updates by itself; r is for
// edits made by hand to a TODO.md git doesn't track).
function clMoveCursor(d, abs) {
  const refs = $$("#cl-body .cl-item").map((el) => el.dataset.ref);
  if (!refs.length) return;
  const i = refs.indexOf(cl.cursor);
  const j = abs != null ? abs : i < 0 ? 0 : i + d;
  cl.cursor = refs[Math.max(0, Math.min(refs.length - 1, j))];
  keys.active = true;
  renderChecklist();
  const el = $(`#cl-body .cl-item[data-ref="${CSS.escape(cl.cursor)}"]`);
  el?.focus({ preventScroll: true });
  el?.scrollIntoView({ block: "nearest" });
}

export async function checklistKey(k, n, hadCount) {
  const it = clView().flatMap((s) => s.entries).map((e) => e.item).find((x) => x && x.ref === cl.cursor);
  switch (k) {
    case "j": case "ArrowDown": return clMoveCursor(n);
    case "k": case "ArrowUp": return clMoveCursor(-n);
    case "gg": return clMoveCursor(0, hadCount ? n - 1 : 0);
    case "G": return clMoveCursor(0, hadCount ? n - 1 : Infinity);
    case "a": case "i": return $("#cl-add").hidden ? $("#cl-create").click() : $("#cl-add-text").focus();
    case "r": return loadChecklist();
  }
  if (!it) return;
  switch (k) {
    case "x": case " ": return queueOp(clOp(it, { op: "check", checked: !it.checked }));
    case "m": case "M":
      if (it.tmp) await flushChecklist();
      if (it.checked) throw new Error("Checked items stay in TODO.md; uncheck it to move it.");
      return moveChecklistItem(it, k === "M");
    case "e": case "Enter": cl.editing = it.ref; return renderChecklist();
    case "dd": return deleteChecklistItem(it);
    case "+": case "-":
      if (it.tmp) return flushChecklist();
      for (let i = 0; i < n; i++) clStepItem(clView().flatMap((s) => s.entries).map((e) => e.item).find((x) => x?.ref === it.ref), k === "+" ? -1 : 1);
      return;
  }
}
