import { $, h, ico, plural } from "./dom.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { isTyping } from "./vim.js";
import { renderBoard, title } from "./branches.js";
import { openDetail } from "./detail.js";
import { goalHue } from "./goals.js";
import { MAX_TAG_LEN, addTag, cleanTag } from "../tagtext.js";
import { openScans, scansRunning } from "./scans.js";

// ------------------------------------------------------------------ tags
// Tasks carry short tags. A draft made from a reported issue (a scan's
// finding, Claude's report_issue) has its type, severity and source as tags,
// and its issues in t.finding. Here: the tags on cards and in a task's pane,
// the Board's filters (From scans (N): only what scans added, with Scans…
// beside it; a tag filter), and picking drafts to Merge.

export const SEVERITIES = ["critical", "high", "medium", "low", "info"];
const sevRank = (s) => { const i = SEVERITIES.indexOf(s); return i < 0 ? SEVERITIES.length : i; };

// worstSeverity is the most severe of t's issues ("" for a task without any).
export function worstSeverity(t) {
  let w = "";
  for (const i of t.finding?.issues || []) if (!w || sevRank(i.severity) < sevRank(w)) w = i.severity;
  return w;
}

// reportCount is how many times t's issues were reported, all together.
export const reportCount = (t) => (t.finding?.issues || []).reduce((n, i) => n + Math.max(i.reports || 0, 1), 0);

// tagLook is a tag chip's colour: a severity's own colour, or else a hue
// from the tag's name, the same for the same name (in any case) everywhere.
function tagLook(x) {
  const k = x.toLowerCase();
  return SEVERITIES.includes(k) ? { cls: ` sev sev-${k}`, style: null } : { cls: " hued", style: `--tag-h: ${goalHue(k)}` };
}

const hasTag = (t, tag) => (t.tags || []).some((x) => x.toLowerCase() === tag.toLowerCase());
const fromScan = (t) => (t.tags || []).some((x) => x.toLowerCase().startsWith("scan:"));

// quietDraft: a draft a scan added for an issue below high. The Board shows
// those only under From scans, so a scan's volume doesn't bury the rest.
export const quietDraft = (t) => t.status === "draft" && !!t.finding && fromScan(t) && sevRank(worstSeverity(t)) > 1;

// boardFilter is the Board's tag filter and whether it shows only the tasks
// scans added (From scans, quiet drafts included), for the project open
// (both reset when another project opens).
export const boardFilter = { project: null, tag: "", showScans: false };

function sameProject() {
  const id = state.project?.id ?? null;
  if (boardFilter.project === id) return;
  boardFilter.project = id;
  boardFilter.tag = "";
  boardFilter.showScans = false;
  picked.clear();
}

// onBoard reports whether the Board shows t under its filters.
export function onBoard(t) {
  sameProject();
  if (boardFilter.tag && !hasTag(t, boardFilter.tag)) return false;
  return boardFilter.showScans ? fromScan(t) : !quietDraft(t);
}

// setScanFilter turns From scans on or off (on: true, off: false, else the other way).
export function setScanFilter(on = !boardFilter.showScans) {
  sameProject();
  boardFilter.showScans = on;
  state.lane = null;
  renderBoard();
}

export function setTagFilter(tag) {
  sameProject();
  boardFilter.tag = boardFilter.tag.toLowerCase() === (tag || "").toLowerCase() ? "" : tag || "";
  state.lane = null; // an empty lane gives way to one with tasks, as with the other filters
  renderBoard();
}

// renderTagFilter fills the filter bar's tag part: From scans (N), the
// drafts scans added, with Scans… (the Scans sheet: run them, schedules,
// settings) beside it; the tag filtered to (removable) and a tag picker.
// It reports whether there is anything to show.
export function renderTagFilter() {
  sameProject();
  const el = $("#tag-filter");
  const all = [...state.todos.values()];
  const tagged = (t) => !boardFilter.tag || hasTag(t, boardFilter.tag);
  const drafts = all.filter((t) => t.status === "draft" && fromScan(t) && tagged(t)).length;
  const quiet = all.filter((t) => quietDraft(t) && tagged(t)).length;
  const counts = new Map();
  for (const t of all) for (const x of t.tags || []) counts.set(x.toLowerCase(), { name: counts.get(x.toLowerCase())?.name || x, n: (counts.get(x.toLowerCase())?.n || 0) + 1 });
  const kids = [];
  if (state.project) {
    const running = scansRunning();
    kids.push(h("span", { class: "scan-filter-group", role: "group", "aria-label": "Scans" },
      h("button", {
        type: "button", class: "filter-chip scan-filter", "aria-pressed": String(boardFilter.showScans),
        title: boardFilter.showScans ? "Show every task again"
          : `Show only the tasks scans added: ${plural(drafts, "draft")}${quiet ? `, ${quiet} of them for medium, low and info issues, which show only here` : ""}`,
        onclick: () => setScanFilter(),
      }, ico("bug"), `From scans (${drafts})`),
      h("button", { type: "button", class: "filter-chip scans-open", "aria-haspopup": "dialog", title: "Scans: run them, their schedules and settings (gn)", onclick: attempt(() => openScans()) },
        ico("scan"), h("span", {}, "Scans…"),
        h("span", { class: "badge quiet", hidden: !running, title: running ? `${plural(running, "scan")} running` : null }, running ? String(running) : ""))));
  }
  if (boardFilter.tag) {
    const look = tagLook(boardFilter.tag);
    kids.push(h("button", { type: "button", class: `filter-chip tag-active${look.style ? " hued" : ""}`, style: look.style, "aria-pressed": "true", title: `Show every task again, not only those tagged ${boardFilter.tag}`, onclick: () => setTagFilter("") },
      `Tag: ${boardFilter.tag}`, h("span", { class: "x", "aria-hidden": "true" }, "×")));
  } else if (counts.size) {
    const list = [...counts.values()].sort((a, b) => b.n - a.n || a.name.localeCompare(b.name));
    kids.push(h("select", { class: "tag-filter-pick", "aria-label": "Filter by tag", onchange: (e) => setTagFilter(e.target.value) },
      h("option", { value: "" }, "Filter by tag"),
      ...list.map((x) => h("option", { value: x.name }, `${x.name} (${x.n})`))));
  }
  el.replaceChildren(...kids);
  el.hidden = !kids.length;
  return !el.hidden;
}

// tagRow is a card's tags: the severity of its issues and how many times
// they were reported (×N), then its tags, each a button that filters the
// Board by it.
export function tagRow(t) {
  const tags = t.tags || [];
  const sev = worstSeverity(t);
  const n = reportCount(t);
  if (!tags.length && !sev) return null;
  const shown = tags.slice(0, 5);
  const chip = (x) => {
    const look = tagLook(x);
    return h("button", {
      type: "button", class: `tag-chip${look.cls}${boardFilter.tag.toLowerCase() === x.toLowerCase() ? " on" : ""}`,
      style: look.style, title: boardFilter.tag.toLowerCase() === x.toLowerCase() ? "Show every task again" : `Show only tasks tagged ${x}`,
      onpointerdown: (e) => e.stopPropagation(),
      onclick: (e) => { e.stopPropagation(); setTagFilter(x); },
    }, x);
  };
  return h("span", { class: "todo-tags" },
    sev && !tags.some((x) => x.toLowerCase() === sev) ? h("span", { class: `tag-chip sev sev-${sev}`, title: `Severity: ${sev}` }, sev) : null,
    n > 1 ? h("span", { class: "reports-tag", title: `Reported ${n} times` }, `×${n}`) : null,
    ...shown.map(chip),
    tags.length > shown.length ? h("span", { class: "reports-tag", title: tags.slice(shown.length).join(", ") }, `+${tags.length - shown.length}`) : null);
}

// ------------------------------------------------------------------ picking drafts to merge
// picked are the drafts chosen on the Board, in the order they were chosen:
// Merge combines them into the first.
export const picked = new Set();

export function pickBox(t) {
  if (t.status !== "draft") return null;
  const on = picked.has(t.id);
  return h("label", { class: "todo-pick", title: on ? "Unselect" : "Select, to merge with other drafts", onclick: (e) => e.stopPropagation(), onpointerdown: (e) => e.stopPropagation() },
    h("input", { type: "checkbox", checked: on, "aria-label": `Select ${title(t)}`, onchange: (e) => { if (e.target.checked) picked.add(t.id); else picked.delete(t.id); renderBoard(); } }));
}

export function clearPicked() {
  if (!picked.size) return;
  picked.clear();
  renderBoard();
}

// renderMergeBar shows "N selected · Merge · Clear" while drafts are picked
// (only drafts that are still drafts count).
export function renderMergeBar() {
  sameProject();
  for (const id of [...picked]) if (state.todos.get(id)?.status !== "draft") picked.delete(id);
  const bar = $("#merge-bar");
  const n = picked.size;
  document.body.classList.toggle("picking", n > 0);
  bar.hidden = !n;
  if (!n) { bar.replaceChildren(); return; }
  const merge = h("button", { type: "button", class: "btn small primary", disabled: n < 2, title: n < 2 ? "Select at least two drafts" : "Combine them into the first one you selected: its issues, tags and report counts together", onclick: attempt(mergePicked) }, ico("merge"), "Merge");
  bar.replaceChildren(h("span", { class: "merge-count" }, `${n} selected`), merge,
    h("button", { type: "button", class: "link", onclick: clearPicked }, "Clear"));
}

async function mergePicked() {
  const ids = [...picked];
  if (ids.length < 2) return;
  const t = await api("POST", `/api/projects/${state.project.id}/todos/merge`, { ids });
  picked.clear();
  renderBoard();
  toast(title(t), { tone: "ok", icon: "merge", head: `Merged ${plural(ids.length, "draft")}`, action: { label: "Open", run: () => openDetail(t.id) } });
}

// pickDrafts picks the drafts among ids (a scan run's new ones), showing
// the quiet ones too, so they can be merged or looked through on the Board.
export function pickDrafts(ids) {
  sameProject();
  picked.clear();
  for (const id of ids) {
    const t = state.todos.get(id);
    if (t?.status !== "draft") continue;
    picked.add(id);
    if (quietDraft(t)) boardFilter.showScans = true;
  }
  boardFilter.tag = "";
  state.lane = "draft";
  renderBoard();
}

document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !picked.size || e.defaultPrevented || state.detail || state.view !== "board") return;
  if (document.querySelector("dialog[open]") || isTyping(document.activeElement)) return;
  clearPicked();
});

// ------------------------------------------------------------------ the tag input
// tagInput makes el a task's tags, editable: a chip each with × to take it
// off, and a box where Enter or a comma adds what is typed and Backspace in
// the empty box takes the last one off. The project's other tags are offered
// as you type. tags() gives the tags shown; save(next) sets them (it may be
// async; render again once they change). The task pane and the task form use it.
export function tagInput(el, { tags, save }) {
  const input = h("input", { class: "tag-input", placeholder: "Add tag", "aria-label": "Add a tag", autocomplete: "off", maxlength: MAX_TAG_LEN, list: "task-tag-options" });
  const add = attempt(async () => {
    const v = input.value;
    if (!cleanTag(v)) { input.value = ""; return; }
    const cur = tags();
    const r = addTag(cur, v);
    if (r.error) throw new Error(r.error);
    input.value = "";
    if (r.tags.length !== cur.length) await save(r.tags);
  });
  input.addEventListener("keydown", (e) => {
    if ((e.key === "Enter" || e.key === ",") && !e.isComposing) { e.preventDefault(); add(); }
    else if (e.key === "Backspace" && !input.value) {
      const cur = tags();
      if (cur.length) attempt(() => save(cur.slice(0, -1)))();
    }
  });
  // Nor does Enter submit a form the box is in (the task form).
  input.addEventListener("keypress", (e) => { if (e.key === "Enter") e.preventDefault(); });
  input.addEventListener("focus", tagOptions);
  // A suggestion picked from the list adds it at once.
  input.addEventListener("input", (e) => { if (e.inputType === "insertReplacementText" || !e.inputType) add(); });
  input.addEventListener("blur", () => { if (input.value.trim()) add(); });
  // The chips change around the box, which stays put so it keeps focus.
  const render = () => {
    const cur = tags();
    if (input.parentNode !== el) el.replaceChildren(input);
    for (const c of [...el.children]) if (c !== input) c.remove();
    input.before(...cur.map((x) => {
      const look = tagLook(x);
      return h("span", { class: `tag-chip editable${look.cls}`, style: look.style }, x,
        h("button", { type: "button", class: "tag-x", "aria-label": `Remove tag ${x}`, title: `Remove ${x}`,
          onclick: attempt(() => save(tags().filter((y) => y !== x))) }, "×"));
    }));
  };
  return { render, input };
}

// tagOptions fills the suggestions with the project's tags, the most used first.
function tagOptions() {
  const counts = new Map();
  for (const t of state.todos.values()) for (const x of t.tags || []) {
    const k = x.toLowerCase();
    counts.set(k, { name: counts.get(k)?.name || x, n: (counts.get(k)?.n || 0) + 1 });
  }
  const list = [...counts.values()].sort((a, b) => b.n - a.n || a.name.localeCompare(b.name));
  $("#task-tag-options").replaceChildren(...list.map((x) => h("option", { value: x.name })));
}

// tagChips is a read-only chip per tag, as the tag input shows them.
export const tagChips = (tags) => tags.map((x) => {
  const look = tagLook(x);
  return h("span", { class: `tag-chip${look.cls}`, style: look.style }, x);
});

// ------------------------------------------------------------------ a task's pane
// The tags, editable; a change is saved at once and shows when the task updates.
let detailTask = null, detailTags = null;

export function renderDetailTags(t) {
  detailTask = t;
  detailTags ||= tagInput($("#d-tags"), {
    tags: () => detailTask?.tags || [],
    save: async (tags) => { if (detailTask) await api("PATCH", `/api/todos/${detailTask.id}`, { tags }); },
  });
  detailTags.render();
}

// dismissNote tells, in a delete dialog, that deleting t dismisses its
// issues: reports of them add no draft until undone in Scans → Dismissed.
export const dismissNote = (t) => t.finding?.issues?.length && t.status !== "merged"
  ? ` Its ${t.finding.issues.length > 1 ? "issues are" : "issue is"} dismissed: scans and Claude won't add ${t.finding.issues.length > 1 ? "them" : "it"} again until you undo it in Scans → Dismissed.` : "";

// renderFinding shows what a task made from reported issues knows about
// them: each one's severity, type, place, and who reported it how often.
export function renderFinding(t) {
  const el = $("#d-finding");
  const issues = t.finding?.issues || [];
  el.hidden = !issues.length;
  if (!issues.length) { el.replaceChildren(); return; }
  const who = (src) => src === "manual" ? "by hand" : src === "agent:chat" ? "the chat" : src?.startsWith("agent:") ? `task ${src.slice(6)}` : src;
  const earlier = t.finding.earlier && state.todos.get(t.finding.earlier);
  el.replaceChildren(
    h("p", { class: "finding-head" }, issues.length > 1 ? `${issues.length} reported issues` : "Reported issue",
      reportCount(t) > 1 ? ` · reported ${reportCount(t)} times` : ""),
    t.finding.earlier ? h("p", { class: "finding-back" }, "Came back after ",
      earlier ? h("button", { type: "button", class: "link", onclick: () => openDetail(earlier.id) }, title(earlier)) : `task ${t.finding.earlier}`,
      " fixed it and was merged.") : null,
    h("ul", { class: "finding-list" }, ...issues.map((i) => h("li", {},
      h("span", { class: `tag-chip sev sev-${i.severity}` }, i.severity),
      h("span", { class: "finding-type" }, { vuln: "security", bug: "bug" }[i.type] || "issue"),
      i.file ? h("code", { class: "finding-loc" }, i.line ? `${i.file}:${i.line}` : i.file) : null,
      issues.length > 1 ? h("span", { class: "finding-title" }, i.title) : null,
      h("span", { class: "finding-src", title: (i.sources || [i.source]).filter(Boolean).map(who).join(", ") },
        `${Math.max(i.reports || 0, 1) > 1 ? `${Math.max(i.reports, 1)}× · ` : ""}${(i.sources?.length ? i.sources : [i.source]).filter(Boolean).map(who).join(", ")}`)))));
}
