import { $, h, ib, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { saveComments, state } from "./state.js";
import { baseBranch } from "./events.js";
import { renderBoard, title } from "./branches.js";
import { duration } from "./activity.js";
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
  const extra = await ask({ title: `Send ${plural(list.length, "comment")} to Claude`, body: "Claude continues in the same worktree and addresses each comment.", input: true, placeholder: "Anything else? (optional)", ok: "Send for rework" });
  if (extra === null) return;
  await api("POST", `/api/todos/${t.id}/rework`, { feedback: commentFeedback(list, extra) });
  delete state.comments[t.id];
  saveComments();
  toast("Comments sent for rework");
}

// ------------------------------------------------------------------ tests (checks)
export function renderChecks(t) {
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
  const when = { merge: `before merging into ${baseBranch(state.project) || "main"}`, manual: "run by hand" }[c.stage] || "before review";
  $("#d-checks-meta").textContent = `${when} · ${Math.round(c.duration_s)}s`;
  $("#d-checks-output").textContent = `$ ${c.command}\n\n${c.output || "No output."}`;
  if (!c.passed && box.dataset.todo !== t.id) box.open = true;
  box.dataset.todo = t.id;
}

// ------------------------------------------------------------------ permission requests
export function describePerm(p) {
  const i = p.input || {};
  switch (p.tool) {
    case "Bash": return { title: "run a command", detail: i.command, note: i.description };
    case "Write": case "Edit": case "MultiEdit": case "NotebookEdit": return { title: `edit ${i.file_path || i.notebook_path || "a file"}`, detail: i.file_path };
    case "WebFetch": return { title: "fetch a web page", detail: i.url };
    case "WebSearch": return { title: "search the web", detail: i.query };
    default: return { title: `use ${p.tool}`, detail: JSON.stringify(i, null, 2) };
  }
}

function permCard(p, withTodo) {
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
      ib({ class: "btn allow", onclick: decide("allow") }, "check", "Allow"),
      ib({ class: "btn", onclick: decide("always") }, "checks", `Always allow ${p.tool}`),
      ib({ class: "btn danger", onclick: decide("deny") }, "x", "Deny"),
      withTodo && t ? ib({ class: "link", onclick: () => openDetail(t.id) }, "right", "Open task") : null));
}

export function renderPerms() {
  const banner = $("#perm-banner");
  banner.hidden = !state.perms.length;
  banner.replaceChildren(...state.perms.map((p) => permCard(p, true)));
  const mine = state.perms.filter((p) => p.todo_id === state.detail);
  $("#d-perms").hidden = !mine.length;
  $("#d-perms").replaceChildren(...mine.map((p) => permCard(p, false)));
  renderBoard();
}
