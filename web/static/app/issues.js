import { renderMarkdown } from "../editor.js";
import { $, $$, h, ib, ico } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { STATUS_LABEL, state } from "./state.js";
import { showView } from "./nav.js";
import { agoText, title } from "./branches.js";
import { openDetail, openTodo } from "./detail.js";
import { SCAN_KINDS, renderScans } from "./scans.js";

// ------------------------------------------------------------------ issues
// A standing list of what is wrong with the project (bugs, vulnerabilities,
// other problems), added by hand, by scans and by running tasks. Checking
// issues and creating tasks for them is the triage step; a merged task
// resolves its issues.
const SEVERITIES = ["critical", "high", "medium", "low", "info"];
const ISSUE_TYPES = { bug: "Bug", vuln: "Vulnerability", issue: "Issue" };
const ISSUE_STATUSES = { open: "Open", task_created: "Task created", resolved: "Resolved", dismissed: "Dismissed" };
export const triageable = (i) => i.status === "open" || i.status === "task_created";
// Only open issues are picked for bulk actions: one with a task is already
// being dealt with (it can still get another task or be dismissed from its
// own row).
export const pickable = (i) => i.status === "open";
// The list is grouped by where an issue stands, in this order.
const ISSUE_GROUPS = [
  { key: "open", label: "Needs triage", hint: "Not dealt with yet: create a task or dismiss" },
  { key: "task", label: "In a task", hint: "A task is working on these; they are resolved when it is merged" },
  { key: "closed", label: "Closed", hint: "Resolved or dismissed" },
];
const issueGroup = (i) => i.status === "open" ? "open" : i.status === "task_created" ? "task" : "closed";
// The task linked to an issue that says most about where it stands: one
// still going beats one that failed beats a merged one.
function issueTask(i) {
  const ts = (i.todo_ids || []).map((id) => state.todos.get(id)).filter(Boolean);
  const rank = (t) => ({ in_progress: 0, merging: 1, review: 2, queued: 3, draft: 4, failed: 5, merged: 6 })[t.status] ?? 7;
  return ts.sort((a, b) => rank(a) - rank(b))[0] || null;
}
const taskLabel = (t) => !t ? "Task" : t.paused ? "Paused" : STATUS_LABEL[t.status] || t.status;
export const iss = {
  list: [],
  loaded: false,      // list holds the project's issues (scan reports read their status from it)
  checked: new Set(), // ids picked for a bulk action
  expanded: new Set(),
  editing: null,      // id of the issue whose fields are being edited
  filter: { type: "", severity: "", status: "active", source: "", kind: "", run: null }, // kind: the scan kind that filed it; run: { id, ids, label } from a scan report
};

function sortIssues() {
  iss.list.sort((a, b) => (SEVERITIES.indexOf(a.severity) - SEVERITIES.indexOf(b.severity)) || (new Date(b.created_at) - new Date(a.created_at)));
}

export async function loadIssues() {
  const p = state.project;
  if (!p) return;
  const list = await api("GET", `/api/projects/${p.id}/issues`);
  if (state.project !== p) return;
  iss.list = list;
  iss.loaded = true;
  sortIssues();
  renderIssues();
  if (state.view === "scans") renderScans();
}

export function resetIssues() {
  iss.filter.run = null;
  iss.list = [];
  iss.loaded = false;
  iss.checked.clear();
  iss.expanded.clear();
  iss.editing = null;
  renderIssues();
}

export function onIssueEvent(ev) {
  const id = ev.data?.id;
  if (!id) return;
  iss.list = iss.list.filter((i) => i.id !== id);
  if (ev.type === "issue.updated") { iss.list.push(ev.data); sortIssues(); }
  else { iss.checked.delete(id); iss.expanded.delete(id); }
  renderIssues();
  if (state.view === "scans") renderScans();
}

function issueVisible(i) {
  const f = iss.filter;
  if (f.run && !f.run.ids.has(i.id)) return false;
  if (f.type && i.type !== f.type) return false;
  if (f.severity && i.severity !== f.severity) return false;
  if (f.status === "active" ? !triageable(i) : f.status && f.status !== "all" && i.status !== f.status) return false;
  if (f.source && i.source !== f.source && !i.source.startsWith(f.source + ":")) return false;
  if (f.kind && i.scan_kind !== f.kind) return false;
  return true;
}

const issueLoc = (i) => i.file ? (i.line ? `${i.file}:${i.line}` : i.file) : "";
const sourceLabel = (s) => s === "manual" ? "Added by hand" : s.startsWith("scan:") ? `Scan: ${s.slice(5)}` : s === "agent:chat" ? "Reported from the chat" : s.startsWith("agent:") ? "Reported by a task" : s;

function renderIssueBadge() {
  const n = iss.list.filter((i) => i.status === "open").length;
  const b = $("#issue-badge");
  b.hidden = !n;
  b.textContent = n;
  b.title = `${n} open issue${n === 1 ? "" : "s"}`;
}

export function renderIssues() {
  renderIssueBadge();
  const list = $("#issue-list");
  const shown = iss.list.filter(issueVisible);
  for (const id of iss.checked) if (!iss.list.some((i) => i.id === id && pickable(i))) iss.checked.delete(id);
  // Grouped by where they stand; headings only when there is more than one group.
  const groups = ISSUE_GROUPS.map((g) => ({ ...g, items: shown.filter((i) => issueGroup(i) === g.key) })).filter((g) => g.items.length);
  list.replaceChildren(...groups.flatMap((g) => [
    groups.length > 1 ? h("li", { class: `issue-group g-${g.key}`, title: g.hint }, h("span", {}, g.label), h("span", { class: "issue-group-n" }, g.items.length)) : null,
    ...g.items.map(issueRow),
  ].filter(Boolean)));
  const empty = $("#issue-empty");
  empty.hidden = shown.length > 0;
  $("#issue-empty-title").textContent = !state.project ? "No project" : iss.list.length ? "No issues match" : "No issues";
  $("#issue-empty-text").textContent = !state.project ? "Select a project to see its issues."
    : iss.list.length ? "Change the filters to see the others."
    : "Bugs, vulnerabilities and other problems go here: add one above, or let scans and running tasks report what they find. Check issues to turn them into tasks.";
  $("#issue-add").hidden = !state.project;
  $("#issue-tools").hidden = !state.project || !iss.list.length;
  const runChip = $("#issue-f-run");
  runChip.hidden = !iss.filter.run;
  runChip.replaceChildren(...(iss.filter.run ? [h("span", {}, iss.filter.run.label), ico("x")] : []));
  // Sources seen so far, for the source filter.
  const sel = $("#issue-f-source");
  const kinds = [...new Set(iss.list.map((i) => i.source.split(":")[0]))];
  const want = ["", ...["manual", "scan", "agent"].filter((k) => kinds.includes(k))];
  if ([...sel.options].map((o) => o.value).join() !== want.join()) {
    sel.replaceChildren(...want.map((k) => h("option", { value: k }, { "": "Any source", manual: "Added by hand", scan: "Scans", agent: "Agents" }[k])));
    if (!want.includes(iss.filter.source)) iss.filter.source = "";
    sel.value = iss.filter.source;
  }
  // Kinds of scan that filed issues, for the kind filter (shown once there are any).
  const kindSel = $("#issue-f-kind");
  const seenKinds = new Set(iss.list.map((i) => i.scan_kind).filter(Boolean));
  const wantKinds = ["", ...Object.keys(SCAN_KINDS).filter((k) => seenKinds.has(k)), ...[...seenKinds].filter((k) => !(k in SCAN_KINDS))];
  if (iss.filter.kind && !wantKinds.includes(iss.filter.kind)) iss.filter.kind = "";
  if ([...kindSel.options].map((o) => o.value).join() !== wantKinds.join()) {
    kindSel.replaceChildren(...wantKinds.map((k) => h("option", { value: k }, k ? SCAN_KINDS[k] || k : "Any scan kind")));
  }
  kindSel.value = iss.filter.kind;
  kindSel.hidden = wantKinds.length < 2;
  renderIssueBulk();
}

// renderIssueBulk updates what depends on the checked issues only, so
// ticking a box doesn't rebuild the list (and lose focus).
function renderIssueBulk() {
  const shown = iss.list.filter(issueVisible);
  const picked = shown.filter((i) => iss.checked.has(i.id));
  const all = $("#issue-all");
  const can = shown.filter(pickable);
  all.checked = can.length > 0 && picked.length === can.length;
  all.indeterminate = picked.length > 0 && picked.length < can.length;
  all.disabled = !can.length;
  $("#issue-bulk").hidden = !iss.checked.size;
  $("#issue-count").textContent = `${iss.checked.size} selected`;
  $("#issue-combine").disabled = iss.checked.size < 2;
}

function issueRow(i) {
  const open = iss.expanded.has(i.id);
  const toggle = () => {
    open ? iss.expanded.delete(i.id) : iss.expanded.add(i.id);
    renderIssues();
    $(`#issue-list .issue[data-id="${i.id}"] .issue-main`)?.focus();
  };
  // Each linked task, with where it stands; the busiest first.
  const linked = (i.todo_ids || []).map((id) => ({ id, t: state.todos.get(id) }));
  const lead = issueTask(i);
  linked.sort((a, b) => (b.t === lead) - (a.t === lead));
  const tasks = linked.map(({ id, t }) => issueTaskChip(id, t));
  // An issue with a task shows what the task is doing where the checkbox
  // would be: it is being dealt with, not waiting to be picked.
  const running = lead && !lead.paused && (lead.status === "in_progress" || lead.status === "merging");
  const mark = i.status === "task_created"
    ? h("span", { class: `issue-mark${lead ? ` s-${lead.status}` : ""}`, title: lead ? `A task is on it: ${taskLabel(lead)}` : "A task is on it", role: "img", "aria-label": "In a task" },
      running ? h("span", { class: "act-spin", "aria-hidden": "true" }) : ico(lead?.status === "failed" ? "x" : "draft"))
    : h("input", { type: "checkbox", class: "issue-check", checked: iss.checked.has(i.id), disabled: !pickable(i),
      "aria-label": `Select ${i.title}`, title: pickable(i) ? "Select for a bulk action" : `${ISSUE_STATUSES[i.status]}; reopen it to triage`,
      onchange: (e) => { e.target.checked ? iss.checked.add(i.id) : iss.checked.delete(i.id); renderIssueBulk(); } });
  return h("li", { class: `issue status-${i.status}${lead?.status === "failed" && i.status === "task_created" ? " task-failed" : ""}${open ? " open" : ""}`, "data-id": i.id },
    h("div", { class: "issue-row" },
      mark,
      h("span", { class: `sev sev-${i.severity}`, title: `Severity: ${i.severity}` }, i.severity),
      h("button", { type: "button", class: "issue-main", "aria-expanded": String(open), onclick: toggle },
        h("span", { class: `issue-type type-${i.type}` }, i.type === "vuln" ? "vuln" : i.type),
        h("span", { class: "issue-title" }, i.title),
        issueLoc(i) ? h("span", { class: "issue-loc" }, issueLoc(i)) : null),
      ...tasks,
      i.status === "resolved" || i.status === "dismissed" ? h("span", { class: "issue-status" }, ISSUE_STATUSES[i.status]) : null),
    open ? (iss.editing === i.id ? issueEditor(i) : issueBody(i)) : null);
}

// issueTaskChip is a linked task as a pill that opens it and says where it
// stands (Draft, Queued, Running, Ready for review, Failed, Merged).
function issueTaskChip(id, t) {
  const busy = t && !t.paused && (t.status === "in_progress" || t.status === "merging");
  return h("button", { type: "button", class: `issue-task${t ? ` s-${t.status}` : ""}`, title: t ? `${title(t)}: ${taskLabel(t)}. Open the task` : "Open the task", onclick: () => openDetail(id) },
    busy ? h("span", { class: "act-spin", "aria-hidden": "true" }) : ico(t?.status === "merged" ? "merge" : t?.status === "failed" ? "x" : "draft"),
    taskLabel(t));
}

// issueTaskNote explains, in an issue with a task, what happens next.
function issueTaskNote(i) {
  if (i.status !== "task_created") return null;
  const t = issueTask(i);
  const n = (i.todo_ids || []).length;
  const next = !t ? "It is resolved when its task is merged."
    : t.paused ? "The task is paused; resume it to carry on."
    : { draft: "The task is a draft: start it from the board to have it fixed.",
        queued: "The task is queued and starts when a slot is free.",
        in_progress: "The task is running now.",
        merging: "The task is being merged; the issue is resolved when it lands.",
        review: "The task is done and waiting for your review; merging it resolves this issue.",
        failed: "The task failed: open it to retry, or create another task.",
      }[t.status] || "It is resolved when the task is merged.";
  return h("div", { class: `issue-tasknote${t ? ` s-${t.status}` : ""}` },
    h("p", {}, h("strong", {}, n > 1 ? `${n} tasks are on this issue. ` : "A task is on this issue. "), next),
    t ? ib({ type: "button", class: "btn small", onclick: () => openDetail(t.id) }, "right", "Open the task") : null);
}

function issueBody(i) {
  const act = (label, icon, run, cls = "btn small") => ib({ type: "button", class: cls, onclick: attempt(run) }, icon, label);
  const patch = (body) => api("PATCH", `/api/issues/${i.id}`, body);
  return h("div", { class: "issue-body" },
    h("p", { class: "issue-meta" },
      `${ISSUE_TYPES[i.type] || i.type} · ${sourceLabel(i.source)} · ${agoText(i.created_at)}`,
      i.updated_at !== i.created_at ? ` · updated ${agoText(i.updated_at)}` : "",
      i.rule ? ` · rule ${i.rule}` : ""),
    issueTaskNote(i),
    i.status === "dismissed" ? h("p", { class: "issue-reason" }, `Dismissed${i.dismiss_reason ? `: ${i.dismiss_reason}` : ""}`) : null,
    i.details ? h("div", { class: "issue-md", innerHTML: renderMarkdown(i.details) }) : h("p", { class: "issue-none" }, "No details yet."),
    i.fix ? h("div", { class: "issue-fix" }, h("h4", {}, "Suggested fix"), h("div", { class: "issue-md", innerHTML: renderMarkdown(i.fix) })) : null,
    h("div", { class: "issue-actions" },
      act("Edit", "edit", () => { iss.editing = i.id; renderIssues(); }),
      triageable(i) ? act(i.status === "task_created" ? "Create another task" : "Create task", "plus", () => issueTasks([i.id], false, false)) : null,
      triageable(i) ? act("Dismiss", "x", () => dismissIssues([i.id])) : null,
      i.status === "open" ? act("Mark resolved", "check", () => patch({ status: "resolved" })) : null,
      !triageable(i) ? act("Reopen", "undo", () => patch({ status: "open" })) : null,
      act("Delete", "trash", async () => {
        if (await ask({ title: "Delete this issue?", body: `“${i.title}” is forgotten. If it is reported again it comes back as new; dismiss it instead to keep it out.`, ok: "Delete", danger: true }))
          await api("DELETE", `/api/issues/${i.id}`);
      }, "btn small danger")));
}

function issueEditor(i) {
  const f = (name, el) => h("label", { class: `issue-field f-${name}` }, h("span", {}, name[0].toUpperCase() + name.slice(1)), el);
  const titleIn = h("input", { value: i.title, required: true, autocomplete: "off" });
  const typeIn = h("select", {}, ...Object.entries(ISSUE_TYPES).map(([v, l]) => h("option", { value: v, selected: v === i.type }, l)));
  const sevIn = h("select", {}, ...SEVERITIES.map((v) => h("option", { value: v, selected: v === i.severity }, v[0].toUpperCase() + v.slice(1))));
  const fileIn = h("input", { value: i.file || "", placeholder: "path/to/file", autocomplete: "off", spellcheck: false });
  const lineIn = h("input", { type: "number", min: 0, value: i.line || "", placeholder: "Line" });
  const detailsIn = h("textarea", { rows: 5, placeholder: "What happens, why it is wrong, how to reproduce (markdown)" });
  detailsIn.value = i.details || "";
  const fixIn = h("textarea", { rows: 3, placeholder: "How it could be fixed (markdown)" });
  fixIn.value = i.fix || "";
  const cancel = () => { iss.editing = null; renderIssues(); };
  const form = h("form", { class: "issue-body issue-edit", onsubmit: attempt(async (e) => {
    e.preventDefault();
    await api("PATCH", `/api/issues/${i.id}`, {
      title: titleIn.value, type: typeIn.value, severity: sevIn.value, file: fileIn.value,
      line: parseInt(lineIn.value, 10) || 0, details: detailsIn.value, fix: fixIn.value,
    });
    iss.editing = null;
    renderIssues();
  }), onkeydown: (e) => { if (e.key === "Escape") { e.stopPropagation(); cancel(); } } },
    f("title", titleIn),
    h("div", { class: "issue-edit-row" }, f("type", typeIn), f("severity", sevIn), f("file", fileIn), f("line", lineIn)),
    f("details", detailsIn),
    f("fix", fixIn),
    h("div", { class: "issue-actions" },
      ib({ type: "submit", class: "btn small primary" }, "check", "Save"),
      h("button", { type: "button", class: "btn small", onclick: cancel }, "Cancel")));
  requestAnimationFrame(() => titleIn.focus());
  return form;
}

async function issueTasks(ids, combine, enqueue) {
  const todos = await api("POST", `/api/projects/${state.project.id}/issues/tasks`, { ids, combine, enqueue });
  for (const id of ids) iss.checked.delete(id);
  for (const t of todos) state.todos.set(t.id, t);
  renderIssues();
  const n = todos.length;
  const what = `${n} ${enqueue ? "queued" : "draft"} task${n === 1 ? "" : "s"}`;
  toast(n === 1 ? title(todos[0]) : `For ${ids.length} issues`, { tone: "ok", head: `Created ${what}`, action: n === 1 ? openTodo(todos[0].id) : { label: "Board", run: () => showView("board") } });
}

async function dismissIssues(ids) {
  const reason = await ask({
    title: ids.length === 1 ? "Dismiss this issue?" : `Dismiss ${ids.length} issues?`,
    body: "Dismissed issues stay dismissed when they are reported again. You can reopen them later.",
    input: true, placeholder: "Why? e.g. false positive, won't fix, by design", ok: "Dismiss",
  });
  if (reason === null) return;
  await api("POST", `/api/projects/${state.project.id}/issues/dismiss`, { ids, reason });
  for (const id of ids) iss.checked.delete(id);
  renderIssues();
}

$("#issue-add").addEventListener("submit", attempt(async (e) => {
  e.preventDefault();
  const input = $("#issue-add-title");
  const t = input.value.trim();
  if (!t || !state.project) return;
  const { issue, result } = await api("POST", `/api/projects/${state.project.id}/issues`, { title: t, type: $("#issue-add-type").value });
  input.value = "";
  if (result === "known" || result === "updated") toast(issue.title, { head: "Already on the list" });
  if (result === "reopened") toast(issue.title, { head: "Reopened: it had been resolved" });
  if (result === "dismissed") toast(issue.title, { head: "Already dismissed", action: { label: "Show", run: () => { iss.filter.status = "dismissed"; $("#issue-f-status").value = "dismissed"; iss.expanded.add(issue.id); renderIssues(); } } });
  if (result === "new") {
    // The live event may have added it already; show it either way, opened.
    if (!iss.list.some((i) => i.id === issue.id)) { iss.list.push(issue); sortIssues(); }
    iss.expanded.add(issue.id);
    renderIssues();
  }
}));
for (const [key, id] of [["type", "#issue-f-type"], ["severity", "#issue-f-severity"], ["status", "#issue-f-status"], ["source", "#issue-f-source"], ["kind", "#issue-f-kind"]]) {
  $(id).addEventListener("change", (e) => { iss.filter[key] = e.target.value; renderIssues(); });
}
$("#issue-all").addEventListener("change", (e) => {
  for (const i of iss.list.filter(issueVisible).filter(pickable)) e.target.checked ? iss.checked.add(i.id) : iss.checked.delete(i.id);
  for (const box of $$("#issue-list .issue-check")) if (!box.disabled) box.checked = e.target.checked;
  renderIssueBulk();
});
$("#issue-tasks").addEventListener("click", attempt(() => issueTasks([...iss.checked], false, $("#issue-queue").checked)));
$("#issue-combine").addEventListener("click", attempt(() => issueTasks([...iss.checked], true, $("#issue-queue").checked)));
$("#issue-dismiss").addEventListener("click", attempt(() => dismissIssues([...iss.checked])));
$("#issue-clear").addEventListener("click", () => { iss.checked.clear(); renderIssues(); });
