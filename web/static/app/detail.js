import { renderMarkdown } from "../editor.js";
import { $, $$, dateText, dateTimeText, h, ib, ico, plural } from "./dom.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { ask, attempt, busy, copy, fail, leave, toast } from "./dialogs.js";
import { attachThumb } from "./attachments.js";
import { statusLabel, state } from "./state.js";
import { approvable, buildsOnMeta, busyTodos, closable, closeTodo, closedNoChanges, canMergePR, whileBusy, doneMeta, conflictMeta, mergePR, depMeta, hasPriority, prChecksEl, prChecksTitle, prReviewTag, prStatus, prTone, waitsOnGitHub } from "./branches.js";
import { goalChip } from "./goals.js";
import { editable, openTodoForm } from "./todos.js";
import { editPrompt, endInline, inlineEdits, renderInline } from "./inlineedit.js";
import { canPreview, hidePreview, previewButton, previewOf, pv, renderPreview, stopPreview } from "./preview.js";
import { canRework, popMenu, renderPane, renderReworkBox, revertTodo, reviewTodo, reworkTodo } from "./drawmode.js";
import { runnerName } from "./runners.js";
import { commentsFor, renderChecks, renderPerms, sendComments } from "./review.js";
import { renderTodoChatMode, todoChat } from "./chat.js";
import { renderRunStats } from "./runstats.js";
import { loadTodoChat } from "./chatpane.js";
import { shellBlocked, startProc } from "./terminal.js";
import { closeCommit } from "./history.js";
import { PRIORITY_LEVELS, clearPriority, priorityLevel } from "./dnd.js";
import { linkToCard } from "./planning.js";
import { agentCan, cantSay } from "./agents.js";
import { creator } from "./people.js";
import { avatar } from "./avatar.js";
import { changed, linkTo } from "./routes.js";
import { deleteBody, renderDetailTags, renderFinding } from "./tags.js";

// ------------------------------------------------------------------ detail
// On a desktop's Board the panel lies beside the board, as a list's detail
// pane does (.detail-beside): the board narrows to keep the card opened in
// sight, marked as selected, and another card swaps the pane. Elsewhere (over
// another page) it is modal, with a scrim, and on a phone full screen: the
// rest of the app is inert while it is open. Closing it returns focus to what
// opened it.
let detailOpener = null;
// A toast's action that opens a task.
export const openTodo = (id) => ({ label: "Open", run: () => openDetail(id) });
export const detailBackground = () => [".topbar", ".sidebar", ".nav", "main", "#chat-pane"].map((s) => $(s)).filter(Boolean);
const setDetailModal = (on) => detailBackground().forEach((el) => { el.inert = on; });

// syncDetailMode lays the open panel out for where it is: beside the board or
// modal. nav.js calls it on every view change, and it runs on crossing the
// desktop breakpoint.
export function syncDetailMode() {
  const open = !!state.detail;
  const beside = open && desktop.matches && state.view === "board";
  $(".app").classList.toggle("detail-beside", beside);
  setDetailModal(open && !beside);
  $("#scrim").hidden = !open || !desktop.matches || beside;
  // Full screen on a phone it goes back; on a desktop it closes a pane.
  $("#d-close").setAttribute("aria-label", desktop.matches ? "Close" : "Back");
  if (beside) requestAnimationFrame(showSelectedCard);
}

// showSelectedCard scrolls the board to the opened card, once it has narrowed.
function showSelectedCard() {
  $(".todo.selected")?.scrollIntoView({ block: "nearest", inline: "nearest" });
}

// pane: "diff" opens it on the changes (the card's Review chip) instead of the timeline.
export async function openDetail(id, { pane = "timeline" } = {}) {
  closeCommit();
  // Opening another task from inside the panel keeps the original opener.
  const a = document.activeElement;
  if (a && a !== document.body && !$("#detail").contains(a)) detailOpener = a;
  if (state.detail !== id) hidePreview();
  state.detail = id;
  // On a desktop the task's running preview opens beside it.
  if (desktop.matches && previewOf(id)) pv.todo = id;
  state.pane = pane;
  state.diff = "";
  state.diffOpen = new Set();
  $("#detail").hidden = false;
  syncDetailMode();
  renderDetail();
  // On a phone, focus in the composer would raise the keyboard and hide the
  // action bar (Approve, Close…): focus Back there instead.
  if (state.pane === "timeline") (desktop.matches ? todoChat.input : $("#d-close")).focus();
  else $("#d-diff")?.focus({ preventScroll: true });
  $$(".todo.selected").forEach((e) => e.classList.remove("selected"));
  $(`.todo[data-id="${id}"]`)?.classList.add("selected");
  $$(".todo[aria-current]").forEach((e) => e.removeAttribute("aria-current"));
  $(`.todo[data-id="${id}"]`)?.setAttribute("aria-current", "true");
  changed("push");
  await Promise.all([loadDiff(), loadTodoChat()]).catch(fail);
}

export function closeDetail() {
  endInline();
  hidePreview();
  const id = state.detail;
  const wasOpen = id && !$("#detail").hidden;
  state.detail = null;
  if (id) changed("close");
  const hide = () => { if (!state.detail) { $("#detail").hidden = true; $("#scrim").hidden = true; } };
  if (wasOpen) leave($("#detail"), "closing", hide); else hide();
  $$(".todo.selected").forEach((e) => e.classList.remove("selected"));
  $$(".todo[aria-current]").forEach((e) => e.removeAttribute("aria-current"));
  syncDetailMode();
  const opener = detailOpener;
  detailOpener = null;
  // Only take focus back if it was in the panel (or lost); a close caused by
  // something else (switching project, a dialog) leaves focus where it is.
  if (!wasOpen) return;
  const a = document.activeElement;
  if (a && a !== document.body && !$("#detail").contains(a)) return;
  const back = opener?.isConnected && !opener.closest("[hidden]") ? opener : $(`.todo[data-id="${id}"]`);
  back?.focus({ preventScroll: false });
}
$("#d-close").addEventListener("click", closeDetail);
// Copy link: the task's URL (routes.js), which opens it for anyone who can see the project.
export const taskLink = (id) => linkTo({ view: "board", task: id }, state.project);
$("#d-link").addEventListener("click", () => { if (state.detail) copy(taskLink(state.detail), "Link copied"); });
$("#scrim").addEventListener("click", closeDetail);
// Inline editing (desktop) and the phone's task form differ: redraw on crossing the breakpoint.
desktop.addEventListener("change", () => { if (state.detail) { syncDetailMode(); renderDetail(); } });
// Esc closes the panel in palette.js: not in a field (but the empty composer),
// where it only leaves the field, nor while it cancels an IME composition.

export async function loadDiff() {
  const id = state.detail;
  const t = state.todos.get(id);
  if (!t?.worktree) { state.diff = ""; renderPane(); return; }
  const text = await api("GET", `/api/todos/${id}/diff`).catch((e) => `error: ${e.message}`);
  if (state.detail !== id) return;
  state.diff = text;
  renderPane();
}

// createdBy names who created task t in its pane: "Created by Dash" for one
// Dash made, nothing when their account was deleted or they left the org
// (nor without an org, where there are no people to name).
function createdBy(t) {
  if (!state.me?.org) return null;
  if (!t.created_by) return h("span", { class: "created-by" }, "Created by Dash");
  const u = creator(t);
  if (!u) return null;
  return h("span", { class: "created-by" }, avatar(u), `Created by ${u.username}`);
}

// ------------------------------------------------------------------ live in
// A merged task of a project with environments says where it is live:
// "Live in production since 4 Nov 2026", from the project's deploy records
// (GET /deploys/live, read at most every 30s per project).
const liveCache = new Map(); // project id → { at, tasks, loading }
const LIVE_FOR = 30000;

function liveMeta(t) {
  if (t.status !== "merged") return null;
  const p = state.projects.find((x) => x.id === t.project_id);
  if (!p?.environments?.length) return null;
  const c = liveCache.get(p.id);
  if (!c || (Date.now() - c.at > LIVE_FOR && !c.loading)) {
    const next = { at: Date.now(), tasks: c?.tasks || {}, loading: true };
    liveCache.set(p.id, next);
    api("GET", `/api/projects/${p.id}/deploys/live`, null, p.prefix || "")
      .then((res) => { next.tasks = res?.tasks || {}; })
      .catch(() => {})
      .finally(() => { next.loading = false; if (state.detail === t.id) renderDetail(); });
  }
  const lives = liveCache.get(p.id).tasks[t.id] || [];
  if (!lives.length) return null;
  // Production if it is live there, else the environment furthest along.
  const l = lives.find((x) => x.environment === "production" || x.environment === "prod") || lives.at(-1);
  const all = lives.map((x) => `${x.environment} since ${dateTimeText(x.since)}`).join("\n");
  return h("span", { class: "live-tag", title: `Live in ${all}` }, `Live in ${l.environment} since ${dateText(l.since)}`);
}

// A deploy going live changes where tasks are live: read it again.
export function forgetLive(projectID) { liveCache.delete(projectID); }

let promptFold = ""; // the task the prompt was last folded for

// firstLine is the prompt's first line with text, as plain words: without a
// heading's #, a list's bullet, a quote's > or emphasis marks.
function firstLine(md) {
  const line = (md || "").split("\n").map((l) => l.trim()).find((l) => l && !/^(```|~~~|---+$|\*\*\*+$)/.test(l)) || "";
  return line.replace(/^(#{1,6}\s+|>\s*|[-*+]\s+(\[[ xX]\]\s+)?|\d+[.)]\s+)/, "")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/[*_`~]+/g, "").trim();
}
export function renderDetail() {
  const t = state.todos.get(state.detail);
  if (!t) return;
  $("#d-meta").replaceChildren(...[
    h("span", { class: `status s-${t.status}` }, statusLabel(t)),
    t.worktree ? h("span", { class: "branch" }, t.worktree.branch) : null,
    doneMeta(t),
    goalChip(t),
    createdBy(t),
    hasPriority(t) && !inlineEdits(t) ? h("span", {}, `${PRIORITY_LEVELS[priorityLevel(t.priority)]} priority`) : null, // else an editable field below
    depMeta(t),
    buildsOnMeta(t),
    conflictMeta(t),
    t.runs ? h("span", {}, `${plural(t.runs, "run")}${t.cost_usd ? ` · ≈$${t.cost_usd.toFixed(2)}` : ""}`) : null,
    t.auto_fixes ? h("span", {}, `Fixed automatically ${t.auto_fixes}×`) : null,
    t.status === "queued" && t.start_now ? h("span", { class: "start-now-tag", title: "It starts as soon as a lane is free, while the queue waits for the weekly pace. A usage limit or a paused queue still holds it back." }, "Starts now, despite the pace") : null,
    t.status === "in_progress" && t.started_now ? h("span", { class: "start-now-tag", title: "This run counts toward usage, so the queue may wait a little longer for the pace afterwards." }, "Started now, despite the pace") : null,
    t.pr ? h("a", { class: "pr-link", href: t.pr.url, target: "_blank", rel: "noopener" }, `Pull request #${t.pr.number}${t.pr.state === "OPEN" ? "" : ` · ${t.pr.state.toLowerCase()}`}`) : null,
    waitsOnGitHub(t) ? prReviewTag(t.pr) : null,
    waitsOnGitHub(t) ? h("span", { class: `pr-status ${prTone(prStatus(t.pr))}`, title: prChecksTitle(t.pr) }, prStatus(t.pr)) : null,
    waitsOnGitHub(t) ? prChecksEl(t.pr) : null,
    t.merged_commit ? h("span", { class: "branch", title: t.merged_commit }, t.merged_commit.slice(0, 8)) : null,
    liveMeta(t),
    t.revert ? (t.revert.pr_url
      ? h("a", { class: "revert-tag", href: t.revert.pr_url, target: "_blank", rel: "noopener" }, "Revert pull request")
      : h("span", { class: "revert-tag", title: t.revert.commit }, "Reverted")) : null,
  ].filter(Boolean));
  renderDetailTags(t);
  renderFinding(t);
  const gallery = $("#d-attach");
  gallery.hidden = !t.attachments?.length;
  gallery.replaceChildren(...(t.attachments || []).map((a) => attachThumb(a)));
  renderRunStats(t);
  renderChecks(t); // after the run stats: it shows or hides the fold they share
  renderPerms();
  $("#d-prompt").replaceChildren(
    ib({ type: "button", class: "btn small copy-prompt", title: "Copy the raw markdown", onclick: attempt(() => copy(t.prompt, "Prompt copied")) }, "copy", "Copy"),
    ...(inlineEdits(t) ? [ib({ type: "button", class: "btn small copy-prompt edit-prompt", title: "Edit the instructions here (or click them; e)", onclick: () => editPrompt() }, "edit", "Edit")] : []),
    h("div", { class: "md", innerHTML: renderMarkdown(t.prompt) }),
    ...(t.feedback?.length ? [h("ol", { class: "feedback-list" }, t.feedback.map((f) => h("li", { class: "md", innerHTML: renderMarkdown(f) })))] : []));
  $("#d-prompt-first").textContent = firstLine(t.prompt);
  // Folded to its first line each time a task opens; Show unfolds it.
  if (promptFold !== t.id) { promptFold = t.id; $("#d-prompt-wrap").open = false; }
  const why = t.error ? t.error + (t.resumable && t.runner && agentCan(state.project, "resume") ? ` — retry to continue on ${runnerName(t.runner)}, where its session is kept.` : "")
    : t.status === "queued" && t.waiting ? t.waiting : "";
  $("#d-error").hidden = !why;
  $("#d-error").textContent = why;
  renderActions(t);
  renderTodoChatMode(t);
  renderPane();
  renderPreview();
  renderReworkBox();
  renderInline(t);
}

export function renderActions(t) {
  // Disabled while its request is in flight: a double click sends it once.
  const post = (verb, body, msg) => attempt((e) => busy(e.currentTarget, async () => { await api("POST", `/api/todos/${t.id}/${verb}`, body); toast(msg); }));
  const b = (icon, label, onclick, cls = "") => ib({ class: `btn ${cls}`, onclick }, icon, label);
  const rework = attempt(() => reworkTodo(t));
  // On a desktop the task is edited in the pane itself (⋯ has the full form).
  const edit = () => desktop.matches ? null : b("edit", "Edit", () => openTodoForm(t));
  const btns = [];
  switch (t.status) {
    case "draft":
      btns.push(edit(), b("queue", "Queue", post("enqueue", null, "Queued"), "primary grow"));
      break;
    case "queued":
      btns.push(edit(), b("inbox", "Move to drafts", post("cancel", null, "Moved to drafts"), "grow"));
      if (t.paused) btns.push(b("play", "Resume", post("resume", null, "Resumed"), "primary grow"));
      else if (canStartNow(t) && (pacing(t) || goesAfter(t))) btns.push(startNowButton(t, post));
      break;
    case "in_progress":
      if (!t.paused) {
        // Pausing keeps the session to go on with: an agent that can't resume one would start over.
        const pause = b("pause", "Pause", post("pause", null, "Pausing"));
        if (!agentCan(state.project, "resume")) {
          pause.disabled = true;
          pause.title = `${cantSay(state.project, "resume")}, so a paused task would start over. Stop it instead.`;
        }
        btns.push(pause);
      }
      btns.push(b("stop", "Stop", post("cancel", null, "Stopping"), "danger grow"));
      break;
    case "review":
      if (t.reviewing) {
        btns.push(h("span", { class: "btn grow reviewing" }, ico("eye"), "Claude is reviewing…"));
        break;
      }
      // Changed no files: nothing to merge. Ask for changes · Close.
      if (closable(t)) {
        btns.push(b("redo", "Ask for changes…", rework));
        btns.push(b("check", "Close", attempt(() => closeTodo(t)), "review grow"));
        if (busyTodos.has(t.id)) disableWhileBusy(btns, busyTodos.get(t.id));
        break;
      }
      // Preview · Rework · Approve (Claude's review is on the card and in ⋯)
      if (canPreview(t)) btns.push(previewButton(t));
      if (commentsFor(t.id).length) {
        btns.push(b("redo", `Rework with ${plural(commentsFor(t.id).length, "note")}…`, attempt(() => sendComments(t)), "primary"));
      } else {
        btns.push(b("redo", "Rework…", rework));
      }
      if (approvable(t)) {
        btns.push(b("check", "Approve", attempt(async () => {
          await whileBusy(t, waitsOnGitHub(t) ? "Pushing…" : "Merging…", () => api("POST", `/api/todos/${t.id}/approve`));
          toast(waitsOnGitHub(t) ? "Pushing the pull request again" : "Approved — merging");
        }), "review grow"));
      } else if (canMergePR(t)) {
        btns.push(h("a", { class: "btn", href: t.pr.url, target: "_blank", rel: "noopener", title: `Open pull request #${t.pr.number} on GitHub` }, ico("external"), "GitHub"));
        btns.push(b("merge", "Merge pull request", attempt(() => mergePR(t)), "review grow"));
      } else {
        // Reviewed on GitHub: a review asking for changes comes back to Claude.
        btns.push(h("a", { class: "btn primary grow", href: t.pr.url, target: "_blank", rel: "noopener", title: "Review, approve or close it on GitHub" },
          ico("external"), `Review PR #${t.pr.number} on GitHub`));
      }
      if (busyTodos.has(t.id)) disableWhileBusy(btns, busyTodos.get(t.id));
      break;
    case "merging":
      if (t.pr) btns.push(h("a", { class: "btn primary grow", href: t.pr.url, target: "_blank", rel: "noopener" }, ico("external"), `Open pull request #${t.pr.number}`));
      break;
    case "failed":
      btns.push(t.worktree ? b("redo", "Rework…", rework) : edit());
      btns.push(t.worktree && t.error?.startsWith("merge:")
        ? b("merge", "Retry merge", post("approve", null, "Merging again"), "review grow")
        : retryButton(t, b, post));
      break;
    case "merged":
      if (!t.revert && !closedNoChanges(t)) btns.push(b("undo", "Revert…", attempt(() => revertTodo(t)), "grow"));
      break;
  }
  if (!busyTodos.has(t.id)) $("#d-actions").removeAttribute("aria-busy");
  $("#d-actions").replaceChildren(...btns.filter(Boolean));
}

// disableWhileBusy disables the pane's buttons while the task is approved,
// merged or closed, with label on the main one (the last).
function disableWhileBusy(btns, label) {
  const els = btns.filter(Boolean);
  for (const el of els) {
    if (el.tagName === "A") { el.setAttribute("aria-disabled", "true"); el.tabIndex = -1; el.classList.add("is-disabled"); }
    else el.disabled = true;
  }
  const main = els.at(-1);
  if (main?.tagName === "BUTTON") main.lastChild.textContent = label;
  $("#d-actions").setAttribute("aria-busy", "true");
}

// retryButton queues failed task t again. One its runner can resume
// continues there, in the session that runner kept.
function retryButton(t, b, post) {
  const btn = b("redo", "Retry", post("enqueue", null, "Queued again"), "primary grow");
  if (!agentCan(state.project, "resume")) btn.title = `Starts over with the full task: ${cantSay(state.project, "resume")}`;
  else if (t.resumable && t.runner) btn.title = `Continues on ${runnerName(t.runner)}, where its session is kept`;
  return btn;
}

// pacing reports whether queued task t waits for the weekly pace: the
// queue's (or, on a hub, your account's), or its runners' as its waiting
// reason says.
export const pacing = (t) => !!state.usage?.pacing || /to stay on pace/.test(t.waiting || "");
// goesAfter reports whether queued task t waits for another task that names
// the same files to go first (the dispatcher's waiting reason says so).
export const goesAfter = (t) => t.status === "queued" && !t.paused && /\bto go first\b/.test(t.waiting || "");
export const startNowHelp = "Start this task as soon as a lane is free, despite the weekly pace; the other tasks keep following it. "
  + "Its run counts toward usage, so the queue may wait a little longer afterwards.";
// canStartNow: Start now is offered for a queued task that isn't paused or already started now.
export const canStartNow = (t) => t.status === "queued" && !t.paused && !t.start_now;
// startNow starts queued task t as soon as a lane is free, despite the pace.
export const startNow = (t) => api("POST", `/api/todos/${t.id}/start-now`)
  .then(() => toast(goesAfter(t) ? "Starting now, beside the task it waited for" : "Starting now, despite the pace"));

// startNowButton starts a queued task now, while the queue waits for the
// weekly pace or the task waits for another to go first (it is shown only
// then; ⋯ always has it).
function startNowButton(t, post) {
  if (goesAfter(t)) {
    const why = `${t.waiting}. Start now runs it beside that task anyway; they may then conflict when they merge, which Dash sends back to Claude to fix.`;
    return ib({ class: "btn primary grow", title: why, onclick: attempt((e) => busy(e.currentTarget, () => startNow(t))) }, "play", "Start now");
  }
  const why = `${state.usage?.pacing && state.usage.reason ? state.usage.reason : t.waiting || "The queue waits for the weekly pace"}. ${startNowHelp}`;
  return ib({ class: "btn primary grow", title: why, onclick: attempt((e) => busy(e.currentTarget, () => startNow(t))) }, "play", "Start now");
}

$("#d-more").addEventListener("click", attempt(async (e) => {
  const t = state.todos.get(state.detail);
  if (!t) return;
  const btn = e.currentTarget;
  // A task of a runner's own project has its worktree on that runner, reached
  // through it like the project; a hub task's is on the runner that ran it.
  const here = !t.runner && !!state.project?.prefix;
  const settled = !["in_progress", "merging"].includes(t.status);
  const edit = [], worktree = [], organise = [], danger = [];
  if (desktop.matches && editable(t)) edit.push(["edit", "Edit in the full form", () => openTodoForm(t)]);
  if (canStartNow(t)) edit.push(["play", "Start now", () => startNow(t)]);
  if (t.status === "review" && !t.reviewing && here) edit.push(["eye", "Review with Claude", reviewTodo]);
  if (settled && t.worktree && here && state.project?.test_command) edit.push(["check", "Run the tests again", () => api("POST", `/api/todos/${t.id}/test`).then(() => toast("Running tests…"))]);
  if (settled && hasPriority(t)) edit.push(["x", "Clear priority", () => clearPriority(t)]);

  if (t.worktree && (here || (t.remote_id && !shellBlocked(state.project, t.runner))) && ["review", "failed"].includes(t.status)) {
    worktree.push(["terminal", "Open shell in this worktree", () => startProc({ kind: "pty", todo_id: t.id })]);
  }
  if (settled && t.worktree) {
    if (here) worktree.push(["code", "Open worktree in editor", () => api("POST", `/api/todos/${t.id}/open`).then(() => toast("Opening in your editor"))]);
    worktree.push(["copy", `Copy branch name (${t.worktree.branch})`, () => copy(t.worktree.branch, "Branch name copied")]);
    if (here) worktree.push(["copy", "Copy worktree path", () => copy(t.worktree.path, "Path copied")]);
  }
  if (previewOf(t.id)) worktree.push(["stop", "Stop the live preview", () => stopPreview(t.id)]);
  if (t.pr) worktree.push(["external", `Open pull request #${t.pr.number}`, () => window.open(t.pr.url, "_blank", "noopener")]);

  if (settled) organise.push(["copy", "Duplicate as a new draft", () => api("POST", `/api/todos/${t.id}/duplicate`).then(() => toast("Duplicated as a draft"))]);
  // Planning is the hub's: its cards link to the tasks it has.
  if (!state.project?.prefix) organise.push(["plan", "Link to a planning card…", () => linkToCard("task", t.id)]);
  if (todoChat.history.length && state.detail === t.id) danger.push(["x", "Clear this task's chat…", () => todoChat.clear(), { danger: true }]);

  if (t.status === "merged" && !t.revert && !closedNoChanges(t) && here) danger.push(["undo", "Revert…", () => revertTodo(t), { danger: true }]);
  if (settled) {
    danger.push(["trash", "Delete task…", async () => {
      if (await ask({ title: "Delete this task?", body: deleteBody(t), ok: "Delete", danger: true })) {
        await api("DELETE", `/api/todos/${t.id}`);
        closeDetail();
        toast("Task deleted");
      }
    }, { danger: true }]);
  }
  await popMenu(btn, [edit, worktree, organise, danger]);
}));

