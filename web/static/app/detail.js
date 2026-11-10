import { renderMarkdown } from "../editor.js";
import { $, $$, h, ib, ico } from "./dom.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { ask, attempt, copy, fail, leave, toast } from "./dialogs.js";
import { attachThumb } from "./attachments.js";
import { STATUS_LABEL, state } from "./state.js";
import { depMeta, hasPriority, prChecksTitle, prStatus, prTone, title } from "./branches.js";
import { openTodoForm } from "./todos.js";
import { canPreview, hidePreview, previewButton, previewOf, pv, renderPreview, stopPreview } from "./preview.js";
import { canRework, menu, renderPane, renderReworkBox, revertTodo, reviewTodo, reworkTodo } from "./drawmode.js";
import { runnerName } from "./runners.js";
import { commentsFor, renderChecks, renderPerms, sendComments } from "./review.js";
import { todoChat } from "./chat.js";
import { loadTodoChat } from "./chatpane.js";
import { shellBlocked, startProc } from "./terminal.js";
import { closeCommit } from "./history.js";
import { clearPriority } from "./dnd.js";

// ------------------------------------------------------------------ detail
// The panel is modal (a scrim on desktop, full screen on phones): the rest of the
// app is inert while it is open, and closing it returns focus to what opened it.
let detailOpener = null;
// A toast's action that opens a task.
export const openTodo = (id) => ({ label: "Open", run: () => openDetail(id) });
export const detailBackground = () => [".topbar", ".sidebar", ".nav", "main", "#chat-pane"].map((s) => $(s)).filter(Boolean);
const setDetailModal = (on) => detailBackground().forEach((el) => { el.inert = on; });

export async function openDetail(id) {
  closeCommit();
  // Opening another task from inside the panel keeps the original opener.
  const a = document.activeElement;
  if (a && a !== document.body && !$("#detail").contains(a)) detailOpener = a;
  setDetailModal(true);
  if (state.detail !== id) hidePreview();
  state.detail = id;
  // On a desktop the task's running preview opens beside it.
  if (desktop.matches && previewOf(id)) pv.todo = id;
  state.pane = "timeline";
  state.diff = "";
  state.diffOpen = new Set();
  $("#detail").hidden = false;
  $("#scrim").hidden = !desktop.matches;
  renderDetail();
  if (state.pane === "timeline") todoChat.input.focus();
  $$(".todo.selected").forEach((e) => e.classList.remove("selected"));
  $(`.todo[data-id="${id}"]`)?.classList.add("selected");
  await Promise.all([loadDiff(), loadTodoChat()]).catch(fail);
}

export function closeDetail() {
  hidePreview();
  const id = state.detail;
  const wasOpen = id && !$("#detail").hidden;
  state.detail = null;
  const hide = () => { if (!state.detail) { $("#detail").hidden = true; $("#scrim").hidden = true; } };
  if (wasOpen) leave($("#detail"), "closing", hide); else hide();
  $$(".todo.selected").forEach((e) => e.classList.remove("selected"));
  setDetailModal(false);
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
$("#scrim").addEventListener("click", closeDetail);
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !state.detail || document.querySelector("dialog[open]")) return;
  if (pv.todo && !desktop.matches) hidePreview(); // a phone's full-screen preview first
  else closeDetail();
});

export async function loadDiff() {
  const id = state.detail;
  const t = state.todos.get(id);
  if (!t?.worktree) { state.diff = ""; renderPane(); return; }
  const text = await api("GET", `/api/todos/${id}/diff`).catch((e) => `error: ${e.message}`);
  if (state.detail !== id) return;
  state.diff = text;
  renderPane();
}

export function renderDetail() {
  const t = state.todos.get(state.detail);
  if (!t) return;
  $("#d-title").textContent = title(t);
  $("#d-meta").replaceChildren(...[
    h("span", { class: `status s-${t.status}` }, STATUS_LABEL[t.status] || t.status),
    t.worktree ? h("span", { class: "branch" }, t.worktree.branch) : null,
    hasPriority(t) ? h("span", {}, `Priority ${t.priority}`) : null,
    depMeta(t),
    t.runs ? h("span", {}, `${t.runs} ${t.runs === 1 ? "run" : "runs"}${t.cost_usd ? ` · ≈$${t.cost_usd.toFixed(2)}` : ""}`) : null,
    t.auto_fixes ? h("span", {}, `Fixed automatically ${t.auto_fixes}×`) : null,
    t.pr ? h("a", { class: "pr-link", href: t.pr.url, target: "_blank", rel: "noopener" }, `Pull request #${t.pr.number}${t.pr.state === "OPEN" ? "" : ` · ${t.pr.state.toLowerCase()}`}`) : null,
    t.pr && t.status === "merging" && t.pr.state === "OPEN" ? h("span", { class: `pr-status ${prTone(prStatus(t.pr))}`, title: prChecksTitle(t.pr) }, prStatus(t.pr)) : null,
    t.merged_commit ? h("span", { class: "branch", title: t.merged_commit }, t.merged_commit.slice(0, 8)) : null,
    t.revert ? (t.revert.pr_url
      ? h("a", { class: "revert-tag", href: t.revert.pr_url, target: "_blank", rel: "noopener" }, "Revert pull request")
      : h("span", { class: "revert-tag", title: t.revert.commit }, "Reverted")) : null,
  ].filter(Boolean));
  const gallery = $("#d-attach");
  gallery.hidden = !t.attachments?.length;
  gallery.replaceChildren(...(t.attachments || []).map((a) => attachThumb(a)));
  renderChecks(t);
  renderPerms();
  $("#d-prompt").replaceChildren(
    ib({ type: "button", class: "btn small copy-prompt", title: "Copy the raw markdown", onclick: attempt(() => copy(t.prompt, "Prompt copied")) }, "copy", "Copy"),
    h("div", { class: "md", innerHTML: renderMarkdown(t.prompt) }),
    ...(t.feedback?.length ? [h("ol", { class: "feedback-list" }, t.feedback.map((f) => h("li", { class: "md", innerHTML: renderMarkdown(f) })))] : []));
  $("#d-prompt-wrap").open = !t.worktree;
  const why = t.error ? t.error + (t.resumable && t.runner ? ` — retry to continue on ${runnerName(t.runner)}, where its session is kept.` : "")
    : t.status === "queued" && t.waiting ? t.waiting : "";
  $("#d-error").hidden = !why;
  $("#d-error").textContent = why;
  renderActions(t);
  $("#d-to-agent").hidden = !canRework(t);
  renderPane();
  renderPreview();
  renderReworkBox();
}

export function renderActions(t) {
  const post = (verb, body, msg) => attempt(async () => { await api("POST", `/api/todos/${t.id}/${verb}`, body); toast(msg); });
  const b = (icon, label, onclick, cls = "") => ib({ class: `btn ${cls}`, onclick }, icon, label);
  const rework = attempt(() => reworkTodo(t));
  const btns = [];
  switch (t.status) {
    case "draft":
      btns.push(b("edit", "Edit", () => openTodoForm(t)), b("queue", "Add to queue", post("enqueue", null, "Added to queue"), "primary grow"));
      break;
    case "queued":
      btns.push(b("edit", "Edit", () => openTodoForm(t)), b("inbox", "Move to drafts", post("cancel", null, "Moved to drafts"), "grow"));
      if (t.paused) btns.push(b("play", "Resume", post("resume", null, "Resumed"), "primary grow"));
      break;
    case "in_progress":
      if (!t.paused && !t.runner) btns.push(b("pause", "Pause", post("pause", null, "Pausing")));
      btns.push(b("stop", "Stop", post("cancel", null, "Stopping"), "danger grow"));
      break;
    case "review":
      if (t.reviewing) {
        btns.push(h("span", { class: "btn grow reviewing" }, ico("eye"), "Claude is reviewing…"));
        break;
      }
      // Preview · Rework · Approve (Claude's review is on the card and in ⋯)
      if (canPreview(t)) btns.push(previewButton(t));
      if (commentsFor(t.id).length) {
        btns.push(b("send", `Send ${commentsFor(t.id).length} ${commentsFor(t.id).length === 1 ? "comment" : "comments"}`, attempt(() => sendComments(t)), "primary"));
      } else {
        btns.push(b("redo", "Rework", rework));
      }
      btns.push(b("merge", "Approve", post("approve", null, "Approved — merging"), "review grow"));
      break;
    case "merging":
      if (t.pr) btns.push(h("a", { class: "btn primary grow", href: t.pr.url, target: "_blank", rel: "noopener" }, ico("external"), `Open pull request #${t.pr.number}`));
      break;
    case "failed":
      btns.push(t.worktree ? b("redo", "Rework", rework) : b("edit", "Edit", () => openTodoForm(t)));
      btns.push(t.worktree && t.error?.startsWith("merge:")
        ? b("merge", "Retry merge", post("approve", null, "Merging again"), "review grow")
        : t.resumable && t.runner ? b("redo", `Continue on ${runnerName(t.runner)}`, post("enqueue", null, "Queued again"), "primary grow")
          : b("redo", "Retry", post("enqueue", null, "Queued again"), "primary grow"));
      break;
    case "merged":
      if (!t.revert) btns.push(b("undo", "Revert", attempt(() => revertTodo(t)), "grow"));
      break;
  }
  $("#d-actions").replaceChildren(...btns);
}

$("#d-more").addEventListener("click", attempt(async () => {
  const t = state.todos.get(state.detail);
  if (!t) return;
  const choices = [];
  // A task of a runner's own project has its worktree on that runner, reached
  // through it like the project; a hub task's is on the runner that ran it.
  const here = !t.runner && !!state.project?.prefix;
  if (t.status === "review" && !t.reviewing && here) choices.push(["eye", "Review with Claude", reviewTodo]);
  if (previewOf(t.id)) choices.push(["stop", "Stop the live preview", () => stopPreview(t.id)]);
  if (t.worktree && (here || (t.remote_id && !shellBlocked(state.project, t.runner))) && ["review", "failed"].includes(t.status)) {
    choices.push(["terminal", "Open shell in this worktree", () => startProc({ kind: "pty", todo_id: t.id })]);
  }
  if (t.pr) choices.push(["external", `Open pull request #${t.pr.number}`, () => window.open(t.pr.url, "_blank", "noopener")]);
  if (t.status === "merged" && !t.revert && here) choices.push(["undo", "Revert this change", () => revertTodo(t)]);
  if (!["in_progress", "merging"].includes(t.status)) {
    if (t.worktree && here && state.project?.test_command) choices.push(["check", "Run the tests again", () => api("POST", `/api/todos/${t.id}/test`).then(() => toast("Running tests…"))]);
    if (hasPriority(t)) choices.push(["x", "Clear priority", () => clearPriority(t)]);
    choices.push(["copy", "Duplicate as a new draft", () => api("POST", `/api/todos/${t.id}/duplicate`).then(() => toast("Duplicated as a draft"))]);
    if (t.worktree && !here) choices.push(["copy", `Copy branch name (${t.worktree.branch})`, () => copy(t.worktree.branch, "Branch name copied")]);
    if (t.worktree && here) {
      choices.push(["code", "Open worktree in editor", () => api("POST", `/api/todos/${t.id}/open`).then(() => toast("Opening in your editor"))]);
      choices.push(["copy", `Copy branch name (${t.worktree.branch})`, () => copy(t.worktree.branch, "Branch name copied")]);
      choices.push(["copy", "Copy worktree path", () => copy(t.worktree.path, "Path copied")]);
    }
    choices.push(["trash", "Delete task", async () => {
      if (await ask({ title: "Delete this task?", body: "Its worktree and branch are removed too. This can't be undone.", ok: "Delete", danger: true })) {
        await api("DELETE", `/api/todos/${t.id}`);
        closeDetail();
        toast("Task deleted");
      }
    }]);
  }
  if (todoChat.history.length) choices.push(["x", "Clear this task's chat", () => api("DELETE", `/api/todos/${t.id}/chat`).then(() => { if (state.detail === t.id) todoChat.reset(); })]);
  if (!choices.length) return;
  await menu(choices);
}));
