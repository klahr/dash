import { $, h, ib, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { saveComments, state } from "./state.js";
import { baseBranch } from "./events.js";
import { renderBoard, title } from "./branches.js";
import { describePerm, duration, isQuestion, questionGroups } from "./chatparts.js";
import { openDetail, renderActions } from "./detail.js";
import { renderPane } from "./drawmode.js";

// ------------------------------------------------------------------ diff comments
export function commentsFor(id) { return state.comments[id] || []; }

$("#d-diff").addEventListener("click", (e) => {
  const line = e.target.closest(".diff-lines > div[data-ln]");
  const t = state.todos.get(state.detail);
  if (!line || !t || !["review", "failed"].includes(t.status)) return;
  if (line.nextElementSibling?.classList.contains("comment-box")) return line.nextElementSibling.querySelector("textarea").focus();
  const trigger = line.querySelector(".ln-comment");
  // Closing the box puts focus back on the line's comment button.
  const close = () => { box.remove(); trigger?.focus(); };
  const box = h("form", { class: "comment-box" },
    h("textarea", { rows: 3, placeholder: "What should Claude change here?", "aria-label": `Comment on ${line.dataset.file} line ${line.dataset.ln}` }),
    h("div", { class: "comment-actions" },
      ib({ type: "button", class: "btn", onclick: close }, "x", "Cancel"),
      ib({ class: "btn primary" }, "comment", "Add comment")));
  box.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const text = box.querySelector("textarea").value.trim();
    if (!text) return close();
    const { file, ln, side } = line.dataset;
    state.comments[t.id] = [...commentsFor(t.id), { file, line: Number(ln), side, code: line.querySelector(".code").textContent, text }];
    saveComments();
    renderPane();
    renderActions(t);
    [...$("#d-diff").querySelectorAll(".diff-lines > div[data-ln]")]
      .find((d) => d.dataset.file === file && d.dataset.ln === ln && d.dataset.side === side)
      ?.querySelector(".ln-comment")?.focus();
  });
  box.querySelector("textarea").addEventListener("keydown", (ev) => {
    if (ev.ctrlKey && ev.key === "Enter") box.requestSubmit();
    if (ev.key === "Escape") { ev.stopPropagation(); close(); }
  });
  line.after(box);
  box.querySelector("textarea").focus();
});

function commentFeedback(list, extra) {
  const parts = list.map((c) => `### ${c.file}, ${c.side === "old" ? "removed line" : "line"} ${c.line}\n> ${c.code}\n\n${c.text}`);
  return `Review comments on your diff:\n\n${parts.join("\n\n")}${extra ? `\n\nAlso: ${extra}` : ""}`;
}

export async function sendComments(t) {
  const list = commentsFor(t.id);
  const extra = await ask({ title: `Rework with ${plural(list.length, "note")}`, body: "Claude continues in the same worktree and addresses each of your rework notes.", input: true, placeholder: "Anything else? (optional)", ok: "Rework" });
  if (extra === null) return;
  await api("POST", `/api/todos/${t.id}/rework`, { feedback: commentFeedback(list, extra) });
  delete state.comments[t.id];
  saveComments();
  toast("Sent for rework");
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
function permCard(p, withTodo) {
  if (isQuestion(p)) return questionCard(p, withTodo);
  const d = describePerm(p);
  const t = state.todos.get(p.todo_id);
  const decide = (decision) => attempt(async () => {
    await api("POST", `/api/permissions/${p.id}`, { decision });
    toast(decision === "deny" ? "Denied" : decision === "always" ? `${p.tool} always allowed in this project` : "Allowed");
  });
  return h("div", { class: "perm", role: "group", "aria-label": `Permission request: ${d.title}` },
    h("p", { class: "perm-q" }, `Claude wants to ${d.title}`,
      withTodo && t ? h("span", { class: "todo-ref" }, ` · ${title(t)}`) : null),
    d.note ? h("p", { class: "hint", style: "margin:0" }, d.note) : null,
    d.detail ? h("code", { class: "perm-cmd" }, d.detail) : null,
    h("div", { class: "perm-actions" },
      ib({ class: "btn allow", title: "Allow (y)", onclick: decide("allow") }, "check", "Allow"),
      ib({ class: "btn", title: "Always allow (A)", onclick: decide("always") }, "checks", `Always allow ${p.tool}`),
      ib({ class: "btn danger", title: "Deny (X)", onclick: decide("deny") }, "x", "Deny"),
      withTodo && t ? ib({ class: "link", onclick: () => openDetail(t.id) }, "right", "Open task") : null));
}

// Question cards are kept across renders, so that a choice made in one isn't
// lost when another request comes or goes: request id and place → card.
const questionCards = new Map();

// questionCard answers Claude's AskUserQuestion request p: each question
// with its header, its options (checkboxes when several may be chosen), an
// Other field for an answer in the user's own words, Submit, and Decline.
function questionCard(p, withTodo) {
  const key = `${p.id}:${withTodo ? "board" : "task"}`;
  if (questionCards.has(key)) return questionCards.get(key);
  const t = state.todos.get(p.todo_id);
  const { qs, groups, answers, focusMissing } = questionGroups(p, `ask-${p.id}-${withTodo ? "b" : "t"}`);
  const send = (body, done) => attempt(async () => {
    buttons.forEach((b) => { b.disabled = true; });
    try {
      await api("POST", `/api/permissions/${p.id}`, body);
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

export function renderPerms() {
  for (const key of questionCards.keys()) {
    if (!state.perms.some((p) => key.startsWith(`${p.id}:`))) questionCards.delete(key);
  }
  const banner = $("#perm-banner");
  banner.hidden = !state.perms.length;
  banner.replaceChildren(...state.perms.map((p) => permCard(p, true)));
  const mine = state.perms.filter((p) => p.todo_id === state.detail);
  $("#d-perms").hidden = !mine.length;
  $("#d-perms").replaceChildren(...mine.map((p) => permCard(p, false)));
  renderBoard();
}
