import { $, h, ib, ico, plural } from "./dom.js";
import { api, here } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import * as store from "./store.js";
import { baseBranch } from "./events.js";
import { renderBoard, title } from "./branches.js";
import { describePerm, duration, isQuestion, isSettingsChange, questionGroups, settingChanges } from "./chatparts.js";
import { openDetail, renderActions } from "./detail.js";
import { renderPane, reworkTodo } from "./drawmode.js";
import { myID } from "./people.js";

// ------------------------------------------------------------------ diff comments
// Comments on lines of a task's diff (Changes), stored with the task until
// Rework sends them all in one note (reworkTodo) or they are discarded.
export function commentsFor(id) { return state.todos.get(id)?.comments || []; }
// canComment: t takes comments on its diff, and files marked viewed.
export const canComment = (t) => !!t && ["review", "failed"].includes(t.status);

// applyTodo shows the task as the server answered a change to its comments
// at once; its todo.updated event follows.
function applyTodo(t) {
  if (!t?.id) return;
  state.todos.set(t.id, t);
  if (state.detail !== t.id) return;
  renderPane();
  renderActions(t);
}

// "line 12", "lines 3–5", "removed line 7": where comment c is.
export function linesLabel(c) {
  const lines = c.end_line > c.line ? `lines ${c.line}–${c.end_line}` : `line ${c.line}`;
  return c.side === "old" ? `removed ${lines}` : lines;
}

// Comments being typed, kept across re-renders of the diff (the task updates
// often while in review): JSON [todo, file, side, line] of the range's last
// row → { text, from: the range's first row's { side, ln } }.
const drafts = new Map();
const draftKey = (id, { file, side, ln }) => JSON.stringify([id, file, side, ln]);
const diffLine = (file, side, ln) => [...$("#d-diff").querySelectorAll(".diff-lines > div[data-ln]")]
  .find((d) => d.dataset.file === file && d.dataset.ln === String(ln) && d.dataset.side === side);
// The draft box new lines extend (tap or shift-click another line): the one opened last.
let lastDraft = null;

// rowsBetween is the diff's rows from a to b (either order), in one file.
function rowsBetween(a, b) {
  const rows = [...a.parentElement.querySelectorAll(":scope > div[data-ln]")];
  const [i, j] = [rows.indexOf(a), rows.indexOf(b)].sort((x, y) => x - y);
  return i < 0 ? [b] : rows.slice(i, j + 1);
}

// rangeOf is what a comment on rows says about where it is: numbered as the
// file is now when any row is in it, else as the removed lines were; code
// quotes every row as the diff shows it.
function rangeOf(rows) {
  const news = rows.filter((r) => r.dataset.side === "new");
  const use = news.length ? news : rows;
  return {
    file: rows[0].dataset.file, side: news.length ? "new" : "old",
    line: Number(use[0].dataset.ln), end_line: Number(use.at(-1).dataset.ln),
    code: rows.map((r) => r.querySelector(".code").textContent).join("\n"),
  };
}

// commentBox opens a new comment on the rows from start to end, under the
// last of them.
function commentBox(start, end, t, text = "") {
  const rows = rowsBetween(start, end);
  const last = rows.at(-1), first = rows[0];
  const key = draftKey(t.id, last.dataset);
  const draft = { text, from: { side: first.dataset.side, ln: first.dataset.ln } };
  drafts.set(key, draft);
  lastDraft = key;
  rows.forEach((r) => r.classList.add("sel"));
  const where = rangeOf(rows);
  // Closing the box puts focus back on the line's comment button.
  const close = () => {
    drafts.delete(key);
    if (lastDraft === key) lastDraft = null;
    rows.forEach((r) => r.classList.remove("sel"));
    box.remove();
    last.querySelector(".ln-comment")?.focus();
  };
  const ta = h("textarea", { rows: 3, placeholder: "What should Claude change here?", "aria-label": `Comment on ${where.file} ${linesLabel(where)}` });
  ta.value = text;
  ta.addEventListener("input", () => { draft.text = ta.value; });
  const add = ib({ class: "btn primary" }, "comment", "Add comment");
  const box = h("form", { class: "comment-box", "data-key": key },
    h("p", { class: "comment-where" }, `${where.file}, ${linesLabel(where)}`,
      h("span", { class: "hint" }, rows.length > 1 ? "" : " · shift-click or tap another line for a range")),
    ta,
    h("div", { class: "comment-actions" },
      ib({ type: "button", class: "btn", onclick: close }, "x", "Cancel"),
      add));
  box.addEventListener("submit", attempt(async (ev) => {
    ev.preventDefault();
    const body = { ...where, text: ta.value.trim() };
    if (!body.text) return close();
    add.disabled = true;
    let res;
    try {
      res = await api("POST", `/api/todos/${t.id}/comments`, body);
    } finally {
      add.disabled = false;
    }
    close();
    applyTodo(res);
    diffLine(where.file, last.dataset.side, last.dataset.ln)?.querySelector(".ln-comment")?.focus();
  }));
  ta.addEventListener("keydown", (ev) => {
    if ((ev.ctrlKey || ev.metaKey) && ev.key === "Enter") { ev.preventDefault(); box.requestSubmit(); }
    if (ev.key === "Escape") { ev.stopPropagation(); close(); }
  });
  last.after(box);
  return ta;
}

// Comments being edited: comment id → the text so far.
const editing = new Map();

// commentRow is a saved comment under its line in the diff (diffLines):
// its text with Edit and Delete, or the box editing it.
export function commentRow(c) {
  const t = state.todos.get(state.detail);
  if (editing.has(c.id)) return editRow(c, t);
  const edit = () => { editing.set(c.id, c.text); renderPane(); $(`#d-diff .comment-box[data-key="edit:${c.id}"] textarea`)?.focus(); };
  return h("div", { class: "comment", "data-id": c.id },
    h("div", { class: "comment-body" },
      c.end_line > c.line || c.side === "old" ? h("span", { class: "comment-where" }, linesLabel(c)) : null,
      h("span", { class: "comment-text" }, c.text)),
    h("span", { class: "comment-tools" },
      ib({ class: "link", type: "button", "aria-label": `Edit comment on ${c.file} ${linesLabel(c)}`, onclick: edit }, "edit", "Edit"),
      ib({ class: "link", type: "button", "aria-label": `Delete comment on ${c.file} ${linesLabel(c)}`, onclick: attempt(async () => {
        applyTodo(await api("DELETE", `/api/todos/${t.id}/comments/${c.id}`));
      }) }, "trash", "Delete")));
}

function editRow(c, t) {
  const key = `edit:${c.id}`;
  const ta = h("textarea", { rows: 3, "aria-label": `Edit comment on ${c.file} ${linesLabel(c)}` });
  ta.value = editing.get(c.id);
  ta.addEventListener("input", () => editing.set(c.id, ta.value));
  const close = () => { editing.delete(c.id); renderPane(); };
  const save = ib({ class: "btn primary" }, "check", "Save");
  const box = h("form", { class: "comment-box", "data-key": key },
    h("p", { class: "comment-where" }, `${c.file}, ${linesLabel(c)}`),
    ta,
    h("div", { class: "comment-actions" },
      ib({ type: "button", class: "btn", onclick: close }, "x", "Cancel"),
      save));
  box.addEventListener("submit", attempt(async (ev) => {
    ev.preventDefault();
    const text = ta.value.trim();
    if (!text) return close();
    save.disabled = true;
    let res;
    try {
      res = await api("PATCH", `/api/todos/${t.id}/comments/${c.id}`, { text });
    } finally {
      save.disabled = false;
    }
    editing.delete(c.id);
    applyTodo(res);
  }));
  ta.addEventListener("keydown", (ev) => {
    if ((ev.ctrlKey || ev.metaKey) && ev.key === "Enter") { ev.preventDefault(); box.requestSubmit(); }
    if (ev.key === "Escape") { ev.stopPropagation(); close(); }
  });
  return box;
}

// commentsBar sits above the diff while the task has comments to send: how
// many, Discard, and Send rework (with an optional note for all of them).
export function commentsBar(t) {
  const n = commentsFor(t?.id).length;
  if (!n || !canComment(t)) return null;
  return h("div", { class: "comments-bar", role: "group", "aria-label": "Comments to send" },
    h("span", { class: "comments-count" }, ico("comment"), `${plural(n, "comment")} to send`),
    h("span", { class: "pv-spacer" }),
    ib({ class: "btn small", type: "button", title: "Throw these comments away", onclick: attempt(async () => {
      if (!await ask({ title: `Discard ${plural(n, "comment")}?`, ok: "Discard", danger: true })) return;
      applyTodo(await api("DELETE", `/api/todos/${t.id}/comments`));
    }) }, "x", "Discard"),
    ib({ class: "btn small primary", type: "button", title: "Send every comment, and a note if you like, as the rework", onclick: attempt(() => reworkTodo(t)) }, "redo", "Send rework…"));
}

// Comments this browser kept before they were stored with the task: moved
// to it the first time the task's diff shows.
const legacy = store.getJSON("dash.comments", null);
function moveLegacy(t) {
  const list = legacy?.[t.id];
  if (!list) return;
  delete legacy[t.id];
  if (Object.keys(legacy).length) store.setJSON("dash.comments", legacy);
  else store.remove("dash.comments");
  (async () => {
    let res = null;
    for (const c of list) res = await api("POST", `/api/todos/${t.id}/comments`, { file: c.file, side: c.side, line: c.line, code: c.code, text: c.text });
    applyTodo(res);
  })().catch(() => {});
}

// keepCommentBoxes runs rebuild (which replaces the diff's rows) and puts the
// open comment boxes back under their lines, with their text, focus and caret.
export function keepCommentBoxes(rebuild) {
  const active = document.activeElement;
  const focused = active?.closest?.(".comment-box")?.dataset.key;
  const sel = focused && active.tagName === "TEXTAREA" ? [active.selectionStart, active.selectionEnd] : null;
  rebuild();
  const t = state.todos.get(state.detail);
  if (!canComment(t)) return;
  moveLegacy(t);
  const last = lastDraft;
  for (const [key, d] of [...drafts]) {
    const [id, file, side, ln] = JSON.parse(key);
    if (id !== t.id) continue;
    const end = diffLine(file, side, ln);
    if (!end) continue; // kept, in case the line comes back
    commentBox(diffLine(file, d.from.side, d.from.ln) || end, end, t, d.text);
  }
  lastDraft = last;
  const box = focused && [...$("#d-diff").querySelectorAll(".comment-box")].find((b) => b.dataset.key === focused);
  const ta = box?.querySelector("textarea");
  if (ta) {
    ta.focus();
    if (sel) ta.setSelectionRange(...sel);
  }
}

// Shift-click would select the code's text: the range is the comment's.
$("#d-diff").addEventListener("mousedown", (e) => {
  if (e.shiftKey && e.target.closest(".diff-lines > div[data-ln]") && lastDraft) e.preventDefault();
});

// A click (a tap) on a line opens a comment on it. With a comment just
// opened in the same file, and nothing typed in it yet (or with Shift), it
// covers the lines from there to here instead.
$("#d-diff").addEventListener("click", (e) => {
  const line = e.target.closest(".diff-lines > div[data-ln]");
  const t = state.todos.get(state.detail);
  if (!line || !canComment(t)) return;
  const d = lastDraft && drafts.get(lastDraft);
  const openBox = d && [...line.parentElement.querySelectorAll(":scope > .comment-box")].find((b) => b.dataset.key === lastDraft);
  if (openBox && (e.shiftKey || !d.text.trim())) {
    const from = diffLine(line.dataset.file, d.from.side, d.from.ln);
    if (from) {
      drafts.delete(lastDraft);
      rowsBetween(from, openBox.previousElementSibling).forEach((r) => r.classList.remove("sel"));
      openBox.remove();
      commentBox(from, line, t, d.text).focus();
      return;
    }
  }
  if (line.nextElementSibling?.classList.contains("comment-box")) return line.nextElementSibling.querySelector("textarea").focus();
  commentBox(line, line, t).focus();
});

// ------------------------------------------------------------------ viewed files
// Each file of a task's diff in review can be marked viewed, per user. The
// server keeps the marks with the hash of the file's diff, and the task's
// review_files (path, lines, hash) as its diff is now: a mark counts while
// its hash is its file's, so a rework that changes the file clears it.

// viewedFiles is the paths of task t's files the user has viewed.
export function viewedFiles(t) {
  const me = myID();
  const hash = new Map((t?.review_files || []).map((f) => [f.path, f.hash]));
  return new Set((t?.viewed || []).filter((v) => (v.user || 0) === me && hash.get(v.file) === v.hash).map((v) => v.file));
}

// viewedKey changes when what the pane shows of t's viewed files does.
export const viewedKey = (t) => JSON.stringify([t?.viewed || [], (t?.review_files || []).map((f) => f.hash)]);

// reviewProgress is how far a review of files ([{ path, lines }]) has got
// with the paths in viewed: how many files, and the share of the lines
// changed (a file with no lines, a binary one, counts as one line).
export function reviewProgress(files, viewed) {
  const weight = (f) => Math.max(1, f.lines || 0);
  let total = 0, done = 0, n = 0;
  for (const f of files) {
    total += weight(f);
    if (viewed.has(f.path)) { done += weight(f); n++; }
  }
  return { n, of: files.length, frac: total ? done / total : 0 };
}
export const progressText = (p) => `${p.n} of ${plural(p.of, "file")} viewed`;

// progressBar is the bar of review progress p: "pane" (above the diff,
// with its text) or "card" (along a card's bottom edge, the text its tooltip).
export function progressBar(p, where) {
  const text = progressText(p), pct = Math.round(p.frac * 100);
  const bar = h("span", { class: "review-bar", role: "progressbar", "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": String(pct), "aria-valuetext": text, "aria-label": "Review progress" },
    h("span", { class: "review-fill", style: `width:${p.frac * 100}%` }));
  if (where === "card") {
    bar.classList.add("card-review-bar");
    bar.title = text;
    return bar;
  }
  return h("div", { class: `review-progress${p.n === p.of ? " done" : ""}` }, h("span", { class: "review-text" }, text), bar);
}

// cardProgress is the bar on task t's card in Review: nothing until the
// user has viewed a file.
export function cardProgress(t) {
  if (t.status !== "review" || !t.review_files?.length) return null;
  const viewed = viewedFiles(t);
  if (!viewed.size) return null;
  return progressBar(reviewProgress(t.review_files, viewed), "card");
}

// setViewed marks file of task t viewed (or not): the file folds (or
// unfolds) at once, and the server's answer follows.
const setViewed = attempt(async (t, file, viewed) => {
  if (viewed) state.diffOpen.delete(file);
  else state.diffOpen.add(file);
  const f = (t.review_files || []).find((x) => x.path === file);
  if (f) {
    const me = myID();
    const others = (t.viewed || []).filter((v) => (v.user || 0) !== me || v.file !== file);
    applyTodo({ ...t, viewed: viewed ? [...others, { user: me, file, hash: f.hash }] : others });
  }
  try {
    applyTodo(await api("PUT", `/api/todos/${t.id}/viewed`, { file, viewed }));
  } catch (e) {
    applyTodo(t);
    throw e;
  }
});

// viewedBox is the Viewed checkbox in the header of file path's diff.
export function viewedBox(t, path, viewed) {
  if (!canComment(t)) return null;
  const box = h("input", { type: "checkbox", checked: viewed, "aria-label": `Viewed ${path}`, onchange: () => setViewed(t, path, box.checked) });
  // In the file's summary, a click on the label's text would also fold or
  // unfold the file: the label passes it on to the box itself instead.
  return h("label", { class: "diff-viewed", title: "Mark viewed (v)", onclick: (e) => {
    if (e.target === box) return;
    e.preventDefault();
    box.click();
  } }, box, h("span", {}, "Viewed"));
}

// toggleViewedKey is v in an open task's changes: the file with focus,
// else the one at the top of the pane.
export function toggleViewedKey() {
  const t = state.todos.get(state.detail);
  if (!canComment(t)) throw new Error("Files are marked viewed on a task in review.");
  const files = [...$("#d-diff").querySelectorAll(".diff-file")];
  let file = document.activeElement?.closest?.(".diff-file");
  if (!file) {
    const top = $("#detail .detail-body")?.getBoundingClientRect().top ?? 0;
    const sticky = $(".pane-tabs")?.getBoundingClientRect().bottom ?? top;
    file = files.find((f) => f.getBoundingClientRect().bottom > Math.max(top, sticky) + 8);
  }
  const box = file?.querySelector(".diff-viewed input");
  if (!box) return;
  setViewed(t, file.dataset.path, !box.checked);
}

// ------------------------------------------------------------------ since the last review
// Back in Review after a rework, Changes shows what changed since the user's
// last review (the server's sinceReview, from the task's reviews), with the
// line comments sent with that rework next to it. A switch shows the full
// diff instead; the choice is kept per task in this browser.
const FULL_KEY = "dash.diffFull";
const fullFor = (id) => !!store.getJSON(FULL_KEY, {})[id];
function setFull(id, on) {
  const all = store.getJSON(FULL_KEY, {});
  delete all[id];
  if (on) all[id] = 1;
  const ids = Object.keys(all); // oldest first: only the newest 200 are kept
  for (const k of ids.slice(0, Math.max(0, ids.length - 200))) delete all[k];
  store.setJSON(FULL_KEY, all);
}

// reworked: task t is back in Review after at least one rework.
export const reworked = (t) => t?.status === "review" && t.reviews?.length > 0;

// fetchChanges is task t's Changes: text, the patch, and since, what it
// shows back in Review after a rework ({ round, full, note }; null at a
// first review or in another column).
export async function fetchChanges(t) {
  const full = () => api("GET", `/api/todos/${t.id}/diff`);
  if (!reworked(t)) return { text: await full(), since: null };
  const round = t.reviews.length + 1;
  if (fullFor(t.id)) return { text: await full(), since: { round, full: true, chosen: true, note: "" } };
  try {
    const r = await api("GET", `/api/todos/${t.id}/diff/since`);
    return { text: r.diff || "", since: { round: r.round || round, full: !r.since, chosen: false, note: r.note || "" } };
  } catch (e) {
    return { text: await full(), since: { round, full: true, chosen: false, note: `The changes since your last review couldn't be loaded (${e.message}).` } };
  }
}

// sinceShown is what the open task's Changes shows (fetchChanges' since),
// when it is back in Review after a rework.
function sinceShown(t) {
  const s = state.diffSince;
  return reworked(t) && s?.todo === t.id ? s : null;
}

// sentComments are the line comments sent with task t's last rework, while
// it is back in Review.
export const sentComments = (t) => sinceShown(t) ? t.reviews.at(-1).comments || [] : [];

// sentRow is a comment sent with the last rework, under its line in the
// diff (diffLines): marked as sent, read only.
export function sentRow(c) {
  return h("div", { class: "comment sent", "data-id": c.id },
    h("div", { class: "comment-body" },
      h("span", { class: "comment-where" }, h("span", { class: "sent-tag" }, ico("check"), "Sent"), ` with your last rework · ${linesLabel(c)}`),
      h("span", { class: "comment-text" }, c.text)));
}

// sinceBar sits above task t's Changes back in Review after a rework: what
// the diff is (since the last review, or all of it, and why), the switch
// between the two, and the comments sent with the rework whose lines the
// diff doesn't show (rest).
export function sinceBar(t, rest = [], reload) {
  const s = sinceShown(t);
  if (!s) return null;
  const base = baseBranch(state.project) || "main";
  const sw = h("input", { type: "checkbox", role: "switch", checked: s.chosen, onchange: (e) => { setFull(t.id, e.target.checked); reload(); } });
  const sent = t.reviews.at(-1).comments?.length || 0;
  return h("section", { class: "since-bar", "aria-label": "What these changes are" },
    h("div", { class: "since-head" },
      h("span", { class: "since-label" }, ico(s.full ? "branch" : "redo"),
        s.full ? `All changes against ${base}` : `Changes since your last review (round ${s.round})`),
      h("span", { class: "pv-spacer" }),
      h("label", { class: "toggle since-switch", title: s.chosen ? "Show only what changed since your last review" : `Show every change against ${base}` },
        sw, h("span", {}, "Show all changes"))),
    !s.chosen && s.note ? h("p", { class: "since-note" }, s.note, " This is the full diff.") : null,
    !s.full && sent ? h("p", { class: "since-note" }, `${plural(sent, "comment")} sent with your last rework, marked Sent: check each was dealt with.`) : null,
    rest.length ? h("details", { class: "since-sent", open: true },
      h("summary", {}, `${plural(rest.length, "sent comment")} on lines not shown here`),
      ...rest.map((c) => h("div", { class: "comment sent", "data-id": c.id },
        h("div", { class: "comment-body" },
          h("span", { class: "comment-where" }, h("span", { class: "sent-tag" }, ico("check"), "Sent"), ` ${c.file}, ${linesLabel(c)}`),
          c.code ? h("pre", { class: "since-code" }, c.code) : null,
          h("span", { class: "comment-text" }, c.text))))) : null);
}

// ------------------------------------------------------------------ tests (checks)
// The checks and the token use sit in one Details fold, which shows when
// either has something, with a badge on it while the last test run failed.
export function renderChecks(t) {
  fillChecks(t);
  $("#d-fold").hidden = $("#d-checks").hidden && $("#d-runs").hidden;
  $("#d-fold-badge").hidden = $("#d-checks").hidden || !$("#d-checks-state").classList.contains("fail");
}

function fillChecks(t) {
  const box = $("#d-checks");
  const c = t.checks;
  const st = $("#d-checks-state");
  // Dash's tests running now replace the last result until they finish.
  const run = state.activity.get(t.id);
  if (run && !run.tool && run.label === "Running tests") {
    box.hidden = false;
    st.className = "checks-state running";
    st.replaceChildren(h("span", { class: "act-spin", "aria-hidden": "true" }), h("span", {}, "Running tests"));
    $("#d-checks-meta").replaceChildren(h("span", { "data-since": String(run.since) }, duration(Date.now() - run.since)),
      c ? ` · last run ${c.passed ? "passed" : "failed"}` : "");
    $("#d-checks-output").textContent = `$ ${run.cmd}\n\nThe output streams into the timeline.`;
    delete box.dataset.todo; // open it again if this run fails
    return;
  }
  box.hidden = !c;
  if (!c) return;
  st.className = `checks-state ${c.passed ? "pass" : "fail"}`;
  st.textContent = c.passed ? "Tests pass" : `Tests fail (exit ${c.exit_code})`;
  const when = { merge: `before merging into ${baseBranch(state.project) || "main"}`, manual: "run by hand", session: "run by Claude through Dash" }[c.stage] || "before review";
  $("#d-checks-meta").textContent = `${when} · ${Math.round(c.duration_s)}s`;
  $("#d-checks-output").textContent = `$ ${c.command}\n\n${c.output || "No output."}`;
  if (!c.passed && box.dataset.todo !== t.id) box.open = true;
  box.dataset.todo = t.id;
}

// ------------------------------------------------------------------ permission requests
// permCard is request p's card: a permission's Allow, Always allow and Deny,
// or a question's answers. where is where it shows: "board" (the banner over
// the board, naming its task, with Open task), "task" (the task's pane) or
// "needs" (Needs you's pane, needs.js; prefix is the computer of p's project).
export function permCard(p, where, prefix = here()) {
  const withTodo = where === "board";
  if (isQuestion(p)) return questionCard(p, where, prefix);
  const d = describePerm(p);
  const t = state.todos.get(p.todo_id);
  const decide = (decision) => attempt(async () => {
    await api("POST", `/api/permissions/${p.id}`, { decision }, prefix);
    toast(decision === "deny" ? "Denied" : decision === "always" ? `${p.tool} always allowed in this project` : "Allowed");
  });
  return h("div", { class: "perm", role: "group", "aria-label": `Permission request: ${d.title}` },
    h("p", { class: "perm-q" }, `Claude wants to ${d.title}`,
      withTodo && t ? h("span", { class: "todo-ref" }, ` · ${title(t)}`) : null),
    d.note ? h("p", { class: "hint", style: "margin:0" }, d.note) : null,
    isSettingsChange(p) ? settingChanges(p) : d.detail ? h("code", { class: "perm-cmd" }, d.detail) : null,
    h("div", { class: "perm-actions" },
      ib({ class: "btn allow", title: "Allow (y)", onclick: decide("allow") }, "check", "Allow"),
      isSettingsChange(p) ? null : ib({ class: "btn", title: "Always allow (A)", onclick: decide("always") }, "checks", `Always allow ${p.tool}`),
      ib({ class: "btn danger", title: "Deny (X)", onclick: decide("deny") }, "x", "Deny"),
      withTodo && t ? ib({ class: "link", onclick: () => openDetail(t.id) }, "right", "Open task") : null));
}

// Question cards are kept across renders, so that a choice made in one isn't
// lost when another request comes or goes: request id and place → card.
const questionCards = new Map();

// questionCard answers Claude's AskUserQuestion request p: each question
// with its header, its options (checkboxes when several may be chosen), an
// Other field for an answer in the user's own words, Submit, and Decline.
function questionCard(p, where, prefix) {
  const withTodo = where === "board";
  const key = `${p.id}:${where}`;
  if (questionCards.has(key)) return questionCards.get(key);
  const t = state.todos.get(p.todo_id);
  const { qs, groups, answers, focusMissing } = questionGroups(p, `ask-${p.id}-${where[0]}`);
  const send = (body, done) => attempt(async () => {
    buttons.forEach((b) => { b.disabled = true; });
    try {
      await api("POST", `/api/permissions/${p.id}`, body, prefix);
    } catch (e) {
      buttons.forEach((b) => { b.disabled = false; });
      throw e;
    }
    toast(done);
  })();
  const submit = ib({ class: "btn allow", type: "submit" }, "check", "Submit");
  const decline = ib({ class: "btn danger", type: "button", onclick: async () => {
    const why = await ask({ title: "Decline Claude's question?", body: "Claude carries on without your answer. You can tell it why.", input: true, placeholder: "Why (optional)", ok: "Decline" });
    if (why === null) return;
    send({ decision: "deny", message: why || "The user declined to answer." }, "Declined");
  } }, "x", "Decline");
  const buttons = [submit, decline];
  const card = h("form", { class: "perm ask", "aria-label": `Claude asks: ${qs.map((q) => q.question).join(" ")}`, onsubmit: (e) => {
    e.preventDefault();
    const a = answers();
    if (a.missing) {
      toast(`Answer “${a.missing.q.header || a.missing.q.question}” first`);
      focusMissing(a.missing);
      return;
    }
    send({ decision: "allow", answers: a.out }, "Answer sent");
  } },
    h("p", { class: "perm-q" }, qs.length > 1 ? "Claude has some questions" : "Claude has a question",
      withTodo && t ? h("span", { class: "todo-ref" }, ` · ${title(t)}`) : null),
    ...groups.map((g) => g.el),
    h("div", { class: "perm-actions" }, ...buttons,
      withTodo && t ? ib({ class: "link", type: "button", onclick: () => openDetail(t.id) }, "right", "Open task") : null));
  questionCards.set(key, card);
  return card;
}

// forgetQuestionCards drops the kept question cards of where whose requests
// aren't pending any more (ids: those that are).
export function forgetQuestionCards(where, ids) {
  for (const key of questionCards.keys()) {
    const i = key.lastIndexOf(":");
    if (key.slice(i + 1) === where && !ids.includes(key.slice(0, i))) questionCards.delete(key);
  }
}

export function renderPerms() {
  const ids = state.perms.map((p) => p.id);
  forgetQuestionCards("board", ids);
  forgetQuestionCards("task", ids);
  const banner = $("#perm-banner");
  banner.hidden = !state.perms.length;
  banner.replaceChildren(...state.perms.map((p) => permCard(p, "board")));
  const mine = state.perms.filter((p) => p.todo_id === state.detail);
  $("#d-perms").hidden = !mine.length;
  $("#d-perms").replaceChildren(...mine.map((p) => permCard(p, "task")));
  renderBoard();
}
