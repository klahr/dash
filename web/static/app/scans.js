import { $, $$, h, ib, ico, timeLocale } from "./dom.js";
import { api, here, on } from "./api.js";
import { ask, attempt, copy, toast } from "./dialogs.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { showView } from "./nav.js";
import { selectProject } from "./projects.js";
import { agoText } from "./branches.js";
import { showRunnerNeeded } from "./runners.js";
import { notifyOn } from "./notifications.js";
import { iss, pickable, renderIssues, triageable } from "./issues.js";

// ------------------------------------------------------------------ scans
// Preconfigured, read-only reviews of the project (security, code review,
// dependency audit, dead code, duplication and complexity, or the team's own templates in .dash/scans/) that run on
// demand or on a schedule and file what they find in Issues.
export const SCAN_KINDS = {
  security: "Security", code_review: "Code review", dependencies: "Dependencies", tests: "Tests", docs: "Docs",
  performance: "Performance", dead_code: "Dead code", flaky_tests: "Flaky tests", accessibility: "Accessibility",
  breaking_changes: "Breaking changes", maintainability: "Duplication & complexity", custom: "Custom",
};
const SCAN_SCOPES = { repo: "Whole repository", since_last_scan: "Changes since the last scan", since_main: "Branch vs. main", path: "One path" };
export const scans = { list: [], problems: [], builtin: false, dir: ".dash/scans", loaded: false, history: new Map() };
const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

export async function loadScans() {
  const p = state.project;
  if (!p) return;
  const r = await api("GET", `/api/projects/${p.id}/scans`);
  if (projectKey(state.project) !== projectKey(p)) return;
  Object.assign(scans, { list: r.scans, problems: r.problems, builtin: r.builtin, dir: r.dir, loaded: true });
  for (const name of scans.history.keys()) loadScanHistory(name).catch(() => {});
  renderScans();
}

export function resetScans() {
  Object.assign(scans, { list: [], problems: [], builtin: false, loaded: false });
  scans.history.clear();
  renderScans();
}

async function loadScanHistory(name) {
  const p = state.project;
  const runs = await api("GET", `/api/projects/${p.id}/scan-runs?scan=${encodeURIComponent(name)}`);
  if (projectKey(state.project) !== projectKey(p)) return;
  scans.history.set(name, runs);
  renderScans();
}

export function onScanRun(ev) {
  const run = ev.data;
  const s = scans.list.find((x) => x.name === run.scan);
  if (s) {
    if (!s.last_run || s.last_run.id === run.id || new Date(run.started_at) >= new Date(s.last_run.started_at)) {
      if (s.last_run?.status === "succeeded" && s.last_run.id !== run.id) s.last_report = s.last_run;
      s.last_run = run;
      if (run.status === "succeeded") s.last_report = null;
    }
    if (run.status === "running") s.running = run.id;
    else if (s.running === run.id) s.running = "";
  }
  const hist = scans.history.get(run.scan);
  if (hist) {
    const i = hist.findIndex((x) => x.id === run.id);
    if (i >= 0) hist[i] = run; else hist.unshift(run);
  }
  if (run.status !== "running" && s && !s.running) loadScans().catch(() => {}); // the next run time moved
  renderScans();
  scanFinished(run, state.project);
}

// scanFinished tells about a run that just ended: always for one started by
// hand, and for a scheduled or resumed one when it found new critical or
// high issues.
export function scanFinished(run, p, where = "") {
  if (run.status === "running" || !run.finished_at) return;
  const go = p ? { label: "Show", run: async () => { if (projectKey(state.project) !== projectKey(p)) await selectProject(p); showRunIssues(run, "new"); } } : null;
  const head = `${scanLabel(run.scan)}${where ? ` · ${where}` : ""}`;
  if (run.trigger !== "manual") {
    if (run.status !== "succeeded" || !run.urgent) return;
    const msg = `${plural(run.urgent, "new critical or high issue")} · ${scanSummary(run)}`;
    toast(msg, { tone: "error", icon: "bug", head, action: go, ms: 12000 });
    if (notifyOn() && "Notification" in window && Notification.permission === "granted" && !(document.visibilityState === "visible" && document.hasFocus())) {
      const n = new Notification(head, { body: msg, tag: `dash-scan-${run.id}` });
      n.onclick = () => { window.focus(); go?.run(); n.close(); };
    }
    return;
  }
  if (run.status === "failed") toast(run.error, { tone: "error", head: `${head} failed` });
  else if (run.status === "succeeded" || run.status === "skipped") toast(scanSummary(run), { tone: "ok", head, action: run.status === "succeeded" && run.new_issues?.length ? go : null });
}

const scanLabel = (name) => scans.list.find((s) => s.name === name)?.title || name;

function scanSummary(r) {
  switch (r.status) {
    case "running": return r.activity || "Running…";
    case "skipped": return r.note || "Nothing to scan";
    case "failed": return r.error || "Failed";
    case "canceled": return "Canceled";
    case "interrupted": return r.note || "Dash stopped during the scan";
  }
  const c = r.counts || {};
  const known = (c.known || 0) + (c.updated || 0);
  const parts = [`${c.new || 0} new`];
  if (known) parts.push(`${known} already known`);
  if (c.reopened) parts.push(`${c.reopened} reopened`);
  if (c.dismissed) parts.push(`${c.dismissed} dismissed`);
  return r.findings ? parts.join(", ") : "No issues found";
}

// showRunIssues opens Issues filtered to what a run added ("new"), brought
// back ("reopened") or both ("all"). With select, the ones still open (no
// task yet) are checked, ready for a bulk action.
function showRunIssues(run, which, select = false) {
  const ids = which === "reopened" ? run.reopened_issues || []
    : which === "all" ? [...(run.new_issues || []), ...(run.reopened_issues || [])]
    : run.new_issues || [];
  const what = { reopened: "Reopened", all: "Found" }[which] || "New";
  iss.filter.run = { id: run.id, ids: new Set(ids), label: `${what} · ${scanLabel(run.scan)}, ${agoText(run.started_at)}` };
  iss.filter.status = select ? "active" : "all";
  $("#issue-f-status").value = iss.filter.status;
  if (select) {
    iss.checked.clear();
    for (const i of iss.list) if (iss.filter.run.ids.has(i.id) && pickable(i)) iss.checked.add(i.id);
  }
  showView("issues");
  renderIssues();
}


// "in 5m", "in 3h", "in 2d"
function until(iso) {
  const s = (new Date(iso) - Date.now()) / 1000;
  if (s < 60) return "soon";
  if (s < 3600) return `in ${Math.round(s / 60)}m`;
  if (s < 86400) return `in ${Math.round(s / 3600)}h`;
  return `in ${Math.round(s / 86400)}d`;
}

// describeCron reads common cron expressions out; others stay as they are.
function describeCron(expr) {
  const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];
  const f = (expr || "").trim().split(/\s+/);
  const macro = { "@hourly": "Every hour", "@daily": "Daily at 00:00", "@midnight": "Daily at 00:00", "@weekly": "Sundays at 00:00", "@monthly": "Monthly on the 1st" }[f[0]];
  if (macro) return macro;
  if (f.length !== 5) return expr;
  const [mi, hr, dom, mon, dow] = f;
  const num = (x) => /^\d+$/.test(x);
  if (/^\*\/\d+$/.test(mi) && hr === "*" && dom === "*" && mon === "*" && dow === "*") return `Every ${mi.slice(2)} minutes`;
  if (num(mi) && hr === "*" && dom === "*" && mon === "*" && dow === "*") return `Hourly at :${mi.padStart(2, "0")}`;
  if (!num(mi) || !num(hr) || mon !== "*") return expr;
  const time = `${hr.padStart(2, "0")}:${mi.padStart(2, "0")}`;
  if (dom === "*" && dow === "*") return `Daily at ${time}`;
  if (dom === "*" && (dow === "1-5" || dow === "mon-fri")) return `Weekdays at ${time}`;
  if (dom === "*" && num(dow) && +dow <= 7) return `${days[+dow]}s at ${time}`;
  if (num(dom) && dow === "*") return `Monthly on day ${dom} at ${time}`;
  return expr;
}

function renderScanBadge() {
  const n = scans.list.filter((s) => s.running).length;
  // The side rail's, and on mobile the menu drawer's.
  for (const b of $$("#scan-badge, #menu-scan-badge")) {
    b.hidden = !n;
    b.textContent = n;
    b.title = `${plural(n, "scan")} running`;
  }
}

export function renderScans() {
  renderScanBadge();
  const p = state.project;
  const intro = $("#scan-intro");
  intro.hidden = !p || !scans.loaded;
  // One line on what scans are; where they come from is one click away, and
  // overrides, approvals and sharing are explained where they come up.
  const more = h("details", {}, h("summary", {}, "Learn more"),
    h("p", {}, "Besides Dash's built-ins, scans can live in ", h("code", {}, `${scans.dir}/`),
      " in the repository, shared with your team, or be created here with Create custom scan. With the same name, a repository scan replaces the built-in and one written here replaces either."));
  more.open = !!intro.querySelector("details[open]"); // stays open across re-renders
  intro.replaceChildren(h("p", {}, "Scans read the code and add what they find to Issues. Run them now or on a schedule."), more);
  $("#scan-create").hidden = !p || !scans.loaded;
  showRunnerNeeded($("#scan-runner-note"), scans.loaded ? p : null);
  const probs = $("#scan-problems");
  probs.hidden = !scans.problems.length;
  probs.replaceChildren(...scans.problems.map((x) => h("li", {}, h("code", {}, x.file), `: ${x.error}`)));
  $("#scan-list").replaceChildren(...(p ? scans.list.map(scanCard) : []));
  $("#scan-empty").hidden = !!p;
}

// scanSource says where a scan comes from, for its card.
const scanSource = (s) => s.source === "dash" ? "written in Dash" : s.builtin ? "built-in" : h("code", {}, s.file);

function scanCard(s) {
  if (s.shadowed_by) return shadowedScanCard(s);
  const sch = s.schedule;
  const running = s.last_run?.status === "running" && s.running ? s.last_run : null;
  const patch = (body) => api("PATCH", `/api/projects/${state.project.id}/scans/${encodeURIComponent(s.name)}`, body).then(() => loadScans());
  const editCron = attempt(async () => {
    const v = await ask({
      title: `Schedule of ${s.title}`, input: true, value: sch.cron, placeholder: "0 6 * * *", ok: "Save",
      body: `A cron expression: minute hour day month weekday, in this computer's time. E.g. "0 6 * * *" daily at 06:00, "0 7 * * 1-5" weekdays at 07:00, "0 6 1 * *" monthly on the 1st at 06:00, "@hourly".${sch.default ? ` Empty: the template's (${sch.default}).` : ""}`,
    });
    if (v === null) return;
    await patch({ schedule: v.trim(), ...(v.trim() && !sch.enabled ? { enabled: true } : {}) });
  });
  const meta = [SCAN_SCOPES[s.scope] || s.scope, s.path && s.scope !== "path" ? `in ${s.path}` : s.path, s.model, s.effort && `${s.effort} effort`].filter(Boolean).join(" · ");
  const hist = scans.history.get(s.name);
  return h("li", { class: `scan-card${running ? " running" : ""}`, "data-scan": s.name },
    h("div", { class: "scan-head" },
      h("div", { class: "scan-id" },
        h("div", { class: "scan-name" }, h("strong", {}, s.title), h("span", { class: `scan-kind kind-${s.kind}` }, SCAN_KINDS[s.kind] || s.kind)),
        h("div", { class: "scan-meta" }, meta, " · ", scanSource(s)),
        scanActions(s)),
      running
        ? ib({ type: "button", class: "btn small", onclick: attempt(() => api("POST", `/api/scan-runs/${running.id}/cancel`)) }, "stop", "Stop")
        : ib({ type: "button", class: "btn small primary", onclick: attempt(async () => {
            const run = await api("POST", `/api/projects/${state.project.id}/scans/${encodeURIComponent(s.name)}/run`);
            s.running = run.id;
            s.last_run = run;
            renderScans();
          }) }, "play", "Run now")),
    h("div", { class: "scan-sched" },
      h("label", { class: "scan-switch", title: sch.cron ? (sch.enabled ? "Turn the schedule off" : "Turn the schedule on") : "Set a schedule first" },
        h("input", { type: "checkbox", role: "switch", checked: sch.enabled, disabled: !sch.cron, onchange: attempt((e) => patch(
          // Turning on a repository schedule never approved approves the one shown.
          e.target.checked && sch.requested && sch.cron === sch.requested ? { approve_schedule: sch.requested } : { enabled: e.target.checked })) }),
        h("span", {}, "Scheduled")),
      h("button", { type: "button", class: "link scan-cron", title: sch.cron ? `${sch.cron} — change` : "Set a schedule", onclick: editCron },
        sch.cron ? describeCron(sch.cron) : "No schedule"),
      sch.enabled && s.next_run ? h("span", { class: "scan-next", title: new Date(s.next_run).toLocaleString(timeLocale) }, `next ${until(s.next_run)}`) : null,
      sch.custom ? h("button", { type: "button", class: "link scan-reset", title: `Back to the template's schedule${sch.default ? ` (${sch.default})` : ""}, off` , onclick: attempt(() => patch({ reset: true })) }, "Reset") : null,
      !sch.enabled && sch.cron && s.builtin && !sch.custom ? h("span", { class: "scan-hint" }, "suggested; off until turned on") : null),
    scanApprovals(s, patch),
    s.last_run ? scanReport(s.last_run) : h("p", { class: "scan-never" }, "Never run."),
    s.last_report && s.last_report.id !== s.last_run?.id ? scanReport(s.last_report, true) : null,
    !s.last_run ? null : h("div", { class: "scan-foot" },
      h("button", { type: "button", class: "link", "aria-expanded": String(!!hist), onclick: attempt(() => {
        if (hist) { scans.history.delete(s.name); renderScans(); } else return loadScanHistory(s.name);
      }) }, hist ? "Hide earlier runs" : "Earlier runs")),
    hist ? h("ol", { class: "scan-history" }, ...hist.filter((r) => r.id !== s.last_run?.id && r.id !== s.last_report?.id).map((r) => h("li", {}, scanReport(r, true))),
      hist.filter((r) => r.id !== s.last_run?.id && r.id !== s.last_report?.id).length ? null : h("li", { class: "scan-never" }, "No earlier runs.")) : null);
}

// shadowedScanCard is a repository scan that doesn't run because a scan
// written in Dash has its name.
function shadowedScanCard(s) {
  return h("li", { class: "scan-card shadowed", "data-scan": s.name },
    h("div", { class: "scan-head" },
      h("div", { class: "scan-id" },
        h("div", { class: "scan-name" }, h("strong", {}, s.title), h("span", { class: `scan-kind kind-${s.kind}` }, SCAN_KINDS[s.kind] || s.kind), h("span", { class: "scan-shadow-tag" }, "Replaced")),
        h("div", { class: "scan-meta" }, scanSource(s)),
        scanActions(s))),
    h("p", { class: "scan-note" }, `Not used: the scan written in Dash called “${s.name}” runs instead. Delete that one to use this file again.`));
}

// scanActions edit, duplicate, export or delete a scan. Only scans written
// in Dash can be edited or deleted; any can be duplicated into one.
function scanActions(s) {
  const dash = s.source === "dash";
  const url = `/api/projects/${state.project.id}/scans/${encodeURIComponent(s.name)}`;
  const del = attempt(async () => {
    if (!await ask({
      title: `Delete ${s.title}?`, danger: true, ok: "Delete scan",
      body: `The scan is removed from Dash. Its earlier runs and the issues it found (source scan:${s.name}) stay. If a built-in or repository scan is also called “${s.name}”, it is used again, with its schedule off.`,
    })) return;
    await api("DELETE", url);
    scans.history.delete(s.name);
    await loadScans();
    toast(`Deleted ${s.title}`);
  });
  const exp = attempt(async () => {
    const file = `${scans.dir}/${s.name}.md`;
    const v = await ask({
      title: "Export to repo", input: true, value: s.markdown, ok: "Copy",
      body: `Save this as ${file} and commit it to share the scan with your team. Once it's merged, delete this one here so the repository's file is used: a scan written in Dash replaces a repository file of the same name.`,
    });
    if (v !== null) await copy(s.markdown, `Copied ${file}`);
  });
  return h("div", { class: "scan-actions" },
    dash ? h("button", { type: "button", class: "link", onclick: () => openScanEditor(s, true) }, "Edit") : null,
    h("button", { type: "button", class: "link", onclick: () => openScanEditor(s, false) }, "Duplicate"),
    dash && s.markdown ? h("button", { type: "button", class: "link", onclick: exp }, "Export to repo") : null,
    dash ? h("button", { type: "button", class: "link danger", onclick: del }, "Delete") : null);
}

// scanEditing is the scan the editor changes; null: it makes a new one.
let scanEditing = null;

// openScanEditor opens the scan editor: empty for a new scan, on a Dash
// scan to edit it (edit=true), or on any scan to start a copy of it.
function openScanEditor(s = null, edit = false) {
  const f = $("#scan-form");
  f.reset();
  scanEditing = edit ? s : null;
  const taken = new Set(scans.list.map((x) => x.name));
  let name = "";
  if (s && !edit) {
    for (let i = 1; !name || taken.has(name); i++) name = `${s.name}-${i === 1 ? "copy" : `copy${i}`}`;
  }
  const sch = s?.schedule;
  f.name.value = edit ? s.name : name;
  f.name.readOnly = edit;
  f.title.value = s ? (edit ? s.title : `${s.title} (copy)`) : "";
  f.kind.value = s?.kind || "custom";
  f.scope.value = s?.scope || "repo";
  f.path.value = s?.path || "";
  f.model.value = s?.model || "";
  f.effort.value = s?.effort || "";
  // Editing: the schedule that runs now (a change in the list included);
  // a copy starts from the template's.
  f.schedule.value = (edit ? sch?.cron : sch?.default) || "";
  f.tools.value = (s?.tools || []).join("\n");
  f.prompt.value = s?.prompt || "";
  $("#scan-sheet-title").textContent = edit ? `Edit ${s.title}` : s ? `Duplicate ${s.title}` : "Create custom scan";
  // Advanced starts closed unless the scan sets something there.
  $("#scan-advanced").open = !!s && !!(s.kind !== "custom" || s.scope !== "repo" || s.path || s.model || s.effort || f.schedule.value || s.tools?.length);
  const from = $("#scan-form-from");
  from.hidden = !(s && !edit);
  if (s && !edit) from.textContent = `A new scan, kept in Dash, starting from ${s.source === "dash" ? "the Dash scan" : s.builtin ? "the built-in scan" : s.file} “${s.name}”.`;
  $("#scan-sheet").showModal();
  (edit ? f.prompt : f.title).focus();
}

// scanSlug makes a scan name from its title: "API error handling" →
// "api-error-handling", unique among the scans listed.
function scanSlug(title) {
  const base = title.normalize("NFKD").replace(/[\u0300-\u036f]/g, "").toLowerCase()
    .replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 56).replace(/-+$/, "") || "scan";
  const taken = new Set(scans.list.map((x) => x.name));
  let name = base;
  for (let i = 2; taken.has(name); i++) name = `${base}-${i}`;
  return name;
}

$("#scan-new").addEventListener("click", () => openScanEditor());
$("#scan-form").addEventListener("submit", attempt(async (e) => {
  if (e.submitter?.value !== "save") return;
  e.preventDefault(); // stays open if saving fails
  const f = e.target;
  const body = {
    name: f.name.value.trim() || scanSlug(f.title.value.trim()), title: f.title.value.trim(), kind: f.kind.value, scope: f.scope.value,
    path: f.path.value.trim(), model: f.model.value.trim(), effort: f.effort.value, schedule: f.schedule.value.trim(),
    tools: f.tools.value.split("\n").map((x) => x.trim()).filter(Boolean), prompt: f.prompt.value,
  };
  const pid = state.project.id;
  const replaces = !scanEditing && scans.list.find((x) => x.name === body.name && x.source !== "dash" && !x.shadowed_by);
  if (replaces && !await ask({
    title: `Replace ${replaces.title}?`, ok: "Replace",
    body: `The ${replaces.builtin ? "built-in scan" : `repository scan ${replaces.file}`} is called “${body.name}” too. The new scan runs instead of it until you delete the new one.`,
  })) return;
  if (scanEditing) await api("PUT", `/api/projects/${pid}/scans/${encodeURIComponent(scanEditing.name)}`, body);
  else await api("POST", `/api/projects/${pid}/scans`, body);
  const what = scanEditing ? "Saved" : "Created";
  $("#scan-sheet").close();
  await loadScans();
  toast(`${what} ${body.title || body.name}`, { tone: "ok" });
}));

// scanApprovals shows what a repository template asks for that the user
// hasn't approved: tools beyond read-only (it runs read-only until then) and
// its schedule (off, or the approved one, until then). Anyone who can merge
// to the repository can change a template, so Dash asks before it runs a
// shell or runs on a timer.
function scanApprovals(s, patch) {
  const sch = s.schedule;
  const askTools = s.tools?.length && !s.tools_approved;
  const askSched = !!sch.requested;
  if (!askTools && !askSched) return null;
  const approveTools = attempt(async () => {
    if (!await ask({
      title: `Let ${s.title} use these tools?`,
      body: `${s.file} asks for: ${s.tools.join(", ")}. Scans run on this computer; tools such as Bash run commands here. Approve only if you trust this template. If it asks for other tools later, it goes back to read-only until you approve again.`,
      ok: "Approve tools",
    })) return;
    await patch({ approve_tools: s.tools });
  });
  const changed = sch.enabled && sch.cron !== sch.requested;
  return h("div", { class: "scan-approve", role: "note" },
    askTools ? h("p", {},
      h("span", {}, s.approved_tools?.length ? "The template now asks for other tools: " : "The template asks for tools beyond read-only: ",
        h("code", {}, s.tools.join(", ")), ". It runs read-only until you approve them."),
      h("button", { type: "button", class: "btn small", onclick: approveTools }, "Approve tools")) : null,
    askSched ? h("p", {},
      h("span", {}, changed
        ? `The template's schedule changed to ${describeCron(sch.requested)} (${sch.requested}); it keeps running ${describeCron(sch.cron)} until you approve the new one.`
        : `The template asks to run ${describeCron(sch.requested)} (${sch.requested}); it stays off until you turn it on.`),
      h("button", { type: "button", class: "btn small", onclick: attempt(() => patch({ approve_schedule: sch.requested })) }, changed ? "Approve new schedule" : "Approve and turn on")) : null);
}

// runIssueStatus sorts the issues a run added or reopened by what became of
// them since: still open, fixed (resolved), taken up in a task, dismissed or
// deleted. Without the project's issues loaded, everything counts as open.
function runIssueStatus(r, which) {
  const ids = (which === "reopened" ? r.reopened_issues : r.new_issues) || [];
  const out = { open: 0, task: 0, fixed: 0, dismissed: 0, gone: 0, urgent: 0 };
  for (const id of ids) {
    if (!iss.loaded) { out.open++; continue; }
    const i = iss.list.find((x) => x.id === id);
    if (!i) { out.gone++; continue; }
    const k = { open: "open", task_created: "task", resolved: "fixed", dismissed: "dismissed" }[i.status] || "open";
    out[k]++;
    if (triageable(i) && (i.severity === "critical" || i.severity === "high")) out.urgent++;
  }
  // Runs from before the ids were kept: trust the counts.
  if (!ids.length) out.open = which === "reopened" ? r.counts?.reopened || 0 : r.counts?.new || 0;
  return out;
}

function scanReport(r, compact = false) {
  const c = r.counts || {};
  const known = (c.known || 0) + (c.updated || 0);
  const icon = { running: null, succeeded: "check", failed: "x", skipped: "redo", canceled: "stop", interrupted: "reload" }[r.status];
  const label = { running: "Running", succeeded: "Done", failed: "Failed", skipped: "Skipped", canceled: "Canceled", interrupted: "Interrupted" }[r.status] || r.status;
  const link = (n, text, which) => n ? h("button", { type: "button", class: `scan-count link${which === "new" ? " new" : ""}`, title: "Show them in Issues", onclick: () => showRunIssues(r, which) }, `${n} ${text}`) : null;
  const bits = [
    agoText(r.started_at),
    r.trigger === "schedule" ? "scheduled" : r.trigger === "resume" ? "↻ resumed after restart" : "by hand",
    r.commit ? (r.incremental && r.since ? `${r.since.slice(0, 7)}…${r.commit.slice(0, 7)}` : r.commit.slice(0, 7)) : null,
    r.finished_at ? `${Math.max(1, Math.round((new Date(r.finished_at) - new Date(r.started_at)) / 1000))}s` : null,
    r.cost_usd ? `≈$${r.cost_usd.toFixed(2)}` : null,
    r.invalid ? `${r.invalid} dropped` : null,
  ].filter(Boolean);
  let body;
  if (r.status === "succeeded" && r.findings) {
    // The counts are what the run found; what has been done about its new
    // and reopened issues since is read from the issues as they are now.
    const nw = runIssueStatus(r, "new"), ro = runIssueStatus(r, "reopened");
    const since = (k, text, title) => {
      const n = nw[k] + ro[k];
      return n ? h("button", { type: "button", class: "scan-count link muted", title, onclick: () => showRunIssues(r, nw[k] ? "new" : "reopened") }, `${n} ${text}`) : null;
    };
    const urgent = iss.loaded && (r.new_issues?.length || r.reopened_issues?.length) ? nw.urgent + ro.urgent : r.urgent;
    // What "Select all" would check: this run's issues that are still open (not in a task).
    const selectable = iss.loaded && (r.new_issues?.length || r.reopened_issues?.length) ? nw.open + ro.open : 0;
    body = h("p", { class: "scan-counts" },
      link(nw.open, "new", "new") || h("span", { class: "scan-count" }, "0 new"),
      known ? h("span", { class: "scan-count" }, `${known} already known`) : null,
      link(ro.open, "reopened", "reopened"),
      c.dismissed ? h("span", { class: "scan-count muted", title: "Found again, but you dismissed them; they stay dismissed" }, `${c.dismissed} dismissed`) : null,
      since("task", "in a task", "New or reopened issues from this run that a task is working on"),
      since("fixed", "fixed", "New or reopened issues from this run that have been fixed since"),
      since("dismissed", "dismissed since", "New or reopened issues from this run that were dismissed since"),
      nw.gone + ro.gone ? h("span", { class: "scan-count muted", title: "New or reopened issues from this run that were deleted since" }, `${nw.gone + ro.gone} deleted`) : null,
      urgent ? h("span", { class: "sev sev-high scan-urgent", title: "New or reopened issues of critical or high severity that are still open" }, `${urgent} urgent`) : null,
      selectable ? ib({ type: "button", class: "btn small scan-select", title: "Open the new and reopened issues from this run that are still open, all selected, to create tasks for them or dismiss them", onclick: () => showRunIssues(r, "all", true) }, "checks", `Select all ${selectable}`) : null);
  } else {
    body = h("p", { class: `scan-msg${r.status === "failed" ? " error" : ""}` }, r.status === "running" ? h("span", { class: "spinner", "aria-hidden": "true" }) : null, scanSummary(r));
  }
  return h("div", { class: `scan-report st-${r.status}${compact ? " compact" : ""}` },
    h("div", { class: "scan-report-head" },
      h("span", { class: "scan-status" }, icon ? ico(icon) : null, label),
      h("span", { class: "scan-when", title: r.invalid ? "Dropped: findings the scan returned that were not valid; see the log" : null }, bits.join(" · ")),
      h("a", { class: "link scan-log", href: on(here(), `/api/scan-runs/${r.id}/log`), target: "_blank", rel: "noopener" }, "Log")),
    body,
    r.answer ? h("details", { class: "scan-answer" }, h("summary", {}, "The agent's answer"), h("pre", {}, r.answer)) : null,
    r.note && r.status === "succeeded" ? h("p", { class: "scan-note" }, r.note) : null,
    r.resumed_as ? h("p", { class: "scan-note" }, `↻ Resumed as ${r.resumed_as}`) : null,
    r.resumed_from ? h("p", { class: "scan-note" }, `Restarts run ${r.resumed_from}, which Dash's restart interrupted`) : null);
}

$("#issue-f-run").addEventListener("click", () => { iss.filter.run = null; iss.filter.status = "active"; $("#issue-f-status").value = "active"; renderIssues(); });
