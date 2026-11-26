import { $, dateTimeText, h, ib, ico, plural, setModelField } from "./dom.js";
import { api, here, on } from "./api.js";
import { ask, attempt, copy, toast } from "./dialogs.js";
import { state } from "./state.js";
import { projectKey, projectName } from "./events.js";
import { showView } from "./nav.js";
import { changed } from "./routes.js";
import { selectProject } from "./projects.js";
import { agoText, untilText } from "../time.js";
import { showRunnerNeeded } from "./runners.js";
import { notifyOn } from "./notifications.js";
import { openDetail } from "./detail.js";
import { pickDrafts, worstSeverity } from "./tags.js";

// ------------------------------------------------------------------ scans
// Preconfigured, read-only reviews of the project (security, code review,
// dependency audit, dead code, duplication and complexity, or the team's own templates in .dash/scans/) that run on
// demand or on a schedule and add what they find to the Board as draft
// tasks (a finding a task already has bumps that task instead). The Board's
// From scans filter shows what they added; Scans… in the project's ⋮ menu
// (also gn, and Scans' old page's address) opens the Scans sheet, where they
// are run and set up.
export const SCAN_KINDS = {
  security: "Security", code_review: "Code review", dependencies: "Dependencies", tests: "Tests", docs: "Docs",
  performance: "Performance", dead_code: "Dead code", flaky_tests: "Flaky tests", accessibility: "Accessibility",
  breaking_changes: "Breaking changes", maintainability: "Duplication & complexity", supply_chain: "CI/CD & supply chain", custom: "Custom",
};
const SCAN_SCOPES = { repo: "Whole repository", since_last_scan: "Changes since the last scan", since_main: "Branch vs. main", path: "One path" };
export const scans = { list: [], problems: [], builtin: false, dir: ".dash/scans", loaded: false, history: new Map(), open: new Set(), dismissed: [] };

export async function loadScans() {
  const p = state.project;
  if (!p) return;
  const r = await api("GET", `/api/projects/${p.id}/scans`);
  if (projectKey(state.project) !== projectKey(p)) return;
  Object.assign(scans, { list: r.scans, problems: r.problems, builtin: r.builtin, dir: r.dir, loaded: true });
  for (const name of scans.history.keys()) loadScanHistory(name).catch(() => {});
  renderScans();
  loadDismissed().catch(() => {});
}

// loadDismissed reads the project's dismissed issues: drafts with issues
// that were deleted. Reports of them add no draft until undone.
export async function loadDismissed() {
  const p = state.project;
  if (!p) return;
  const list = await api("GET", `/api/projects/${p.id}/dismissed`);
  if (projectKey(state.project) !== projectKey(p)) return;
  scans.dismissed = list || [];
  renderDismissed();
}

const sourceLabel = (src) => !src || src === "manual" ? "by hand" : src === "agent:chat" ? "the chat" : src.startsWith("agent:") ? `task ${src.slice(6)}` : src;

// renderDismissed lists the dismissed issues, each with Undo.
function renderDismissed() {
  const el = $("#scan-dismissed");
  const list = state.project ? scans.dismissed || [] : [];
  el.hidden = !list.length;
  if (!list.length) { el.replaceChildren(); return; }
  const undo = (d) => attempt(async () => {
    await api("DELETE", `/api/projects/${state.project.id}/dismissed/${encodeURIComponent(d.fingerprint)}`);
    scans.dismissed = scans.dismissed.filter((x) => x.fingerprint !== d.fingerprint);
    renderDismissed();
    toast(`The next report of “${d.title}” adds a draft again`, { tone: "ok" });
  });
  el.replaceChildren(
    h("h3", {}, "Dismissed", h("span", { class: "n" }, String(list.length))),
    h("p", { class: "scan-hint" }, "Issues whose drafts were deleted. Scans and Claude don't add them again until you undo."),
    h("ul", { class: "dismissed-list" }, ...list.map((d) => h("li", {},
      d.severity ? h("span", { class: `tag-chip sev sev-${d.severity}` }, d.severity) : null,
      h("div", { class: "dismissed-main" },
        h("span", { class: "dismissed-title" }, d.title),
        h("span", { class: "dismissed-sub" }, [d.file ? h("code", {}, d.file) : null, sourceLabel(d.source), d.at ? agoText(d.at) : null,
          d.todo_title ? `deleted with “${d.todo_title}”` : null, d.reason || null].filter(Boolean).flatMap((x, i) => i ? [" · ", x] : [x]))),
      h("button", { type: "button", class: "btn small", onclick: undo(d), title: "Let reports of it add a draft again" }, "Undo")))));
}

export function resetScans() {
  Object.assign(scans, { list: [], problems: [], builtin: false, loaded: false, dismissed: [] });
  scans.history.clear();
  scans.open.clear();
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
export function scanFinished(run, p, where = projectName(p)) {
  if (run.status === "running" || !run.finished_at) return;
  const go = p ? { label: "Show", run: async () => { if (projectKey(state.project) !== projectKey(p)) await selectProject(p); showRunTasks(run); } } : null;
  const head = scanLabel(run.scan);
  if (run.trigger !== "manual") {
    if (run.status !== "succeeded" || !run.urgent) return;
    const msg = `${plural(run.urgent, "new critical or high draft")} · ${scanSummary(run)}`;
    toast(msg, { tone: "error", icon: "bug", where, head, action: go, ms: 12000 });
    if (notifyOn() && "Notification" in window && Notification.permission === "granted" && !(document.visibilityState === "visible" && document.hasFocus())) {
      const n = new Notification(head, { body: [where, msg].filter(Boolean).join("\n"), tag: `dash-scan-${run.id}` });
      n.onclick = () => { window.focus(); go?.run(); n.close(); };
    }
    return;
  }
  if (run.status === "failed") toast(run.error, { tone: "error", where, head: `${head} failed` });
  else if (run.status === "succeeded" || run.status === "skipped") toast(scanSummary(run), { tone: "ok", where, head, action: run.status === "succeeded" && runTasks(run).length ? go : null });
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
  const c = counts(r);
  return r.findings ? `${c.added} added, ${c.there} already there, ${c.dismissed} dismissed` : "No issues found";
}

// counts sums up a run's findings: added (new drafts, including issues a
// merged task had fixed that came back), already there (a task had them:
// bumped) and dismissed (left out).
function counts(r) {
  const c = r.counts || {};
  return { added: (c.new || 0) + (c.reopened || 0), there: (c.known || 0) + (c.updated || 0), dismissed: c.dismissed || 0, reopened: c.reopened || 0 };
}

// runTasks are the drafts a run added: for new issues and for ones that came back.
const runTasks = (r) => [...(r.new_tasks || []), ...(r.reopened_tasks || [])];

// scanTasksChanged re-renders the Scans sheet when tasks it counts changed:
// what a run's drafts became (still drafts, in a task, fixed, deleted) is
// read from state.todos. With no id (all tasks were loaded), always.
export function scanTasksChanged(id) {
  if (!scans.loaded) return;
  if (id) {
    const runs = [...scans.list.flatMap((s) => [s.last_run, s.last_report]), ...[...scans.history.values()].flat()];
    if (!runs.some((r) => r && runTasks(r).includes(id))) return;
  }
  renderScans();
}

// showRunTasks shows what a run added: the one draft, or the Board with
// its drafts that are still drafts picked (and shown, From scans or not).
function showRunTasks(run) {
  const ids = runTasks(run).filter((id) => state.todos.has(id));
  const sheet = $("#scans-sheet");
  if (sheet.open) sheet.close();
  showView("board");
  if (ids.length === 1) openDetail(ids[0]);
  else if (ids.length) pickDrafts(ids);
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

// openScans opens the Scans sheet over the page, and reads the scans again.
export async function openScans() {
  const d = $("#scans-sheet");
  if (!state.project) return;
  if (!d.open) {
    renderScans();
    d.showModal();
    changed("push");
  }
  await loadScans();
}
$("#pm-scans").addEventListener("click", attempt(() => openScans()));

export function renderScans() {
  const p = state.project;
  const intro = $("#scan-intro");
  intro.hidden = !p || !scans.loaded;
  // One line on what scans are; where they come from is one click away, and
  // overrides, approvals and sharing are explained where they come up.
  const more = h("details", {}, h("summary", {}, "Learn more"),
    h("p", {}, "Besides Dash's built-ins, scans can live in ", h("code", {}, `${scans.dir}/`),
      " in the repository, shared with your team, or be created here with Create custom scan. With the same name, a repository scan replaces the built-in and one written here replaces either."));
  more.open = !!intro.querySelector("details[open]"); // stays open across re-renders
  intro.replaceChildren(h("p", {}, "Scans read the code and add what they find to the Board as draft tasks, tagged with the scan's name. Run them now or on a schedule."), more);
  $("#scan-create").hidden = !p || !scans.loaded;
  showRunnerNeeded($("#scan-runner-note"), scans.loaded ? p : null);
  const probs = $("#scan-problems");
  probs.hidden = !scans.problems.length;
  probs.replaceChildren(...scans.problems.map((x) => h("li", {}, h("code", {}, x.file), `: ${x.error}`)));
  $("#scan-list").replaceChildren(...(p ? scans.list.map(scanCard) : []));
  $("#scan-empty").hidden = !!p;
  renderDismissed();
}

// scanSource says where a scan comes from, for its card.
const scanSource = (s) => s.source === "dash" ? "written in Dash" : s.builtin ? "built-in" : h("code", {}, s.file);

// scanKind is the kind badge, left out where the title already says it
// (e.g. "Security scan", "Dependency audit") and for custom scans.
function scanKind(s) {
  const label = SCAN_KINDS[s.kind] || s.kind;
  if (s.kind === "custom" || (s.title || "").toLowerCase().includes(label.slice(0, 4).toLowerCase())) return null;
  return h("span", { class: `scan-kind kind-${s.kind}` }, label);
}

// scanToggle shows or hides a card's details.
function scanToggle(s) {
  const open = scans.open.has(s.name);
  return ib({ type: "button", class: "btn small scan-more", "aria-expanded": String(open), "aria-controls": `scan-details-${s.name}`, title: open ? "Hide details" : "Details: settings, schedule, run reports and actions",
    onclick: () => { if (open) scans.open.delete(s.name); else scans.open.add(s.name); renderScans(); } }, "more", h("span", { class: "scan-more-label" }, "Details"));
}

// scanLine is a card's one-line status: what the last run came to, or that
// one is running or none has run.
function scanLine(s) {
  const r = s.last_run;
  const text = (t) => h("span", { class: "scan-line-text" }, t);
  if (!r) return h("div", { class: "scan-line" }, text("Never run"));
  if (r.status === "running") return h("div", { class: "scan-line st-running" }, h("span", { class: "spinner", "aria-hidden": "true" }), text(r.activity ? `Running… ${r.activity}` : "Running…"));
  const ago = agoText(r.started_at);
  if (r.status === "failed") {
    const err = (r.error || "").split("\n")[0];
    return h("div", { class: "scan-line st-failed", title: r.error || null }, text(`Failed ${ago}${err ? ` · ${err}` : ""}`));
  }
  if (r.status !== "succeeded") {
    const label = { skipped: "Skipped", canceled: "Canceled", interrupted: "Interrupted" }[r.status] || r.status;
    return h("div", { class: "scan-line" }, text(r.status === "canceled" ? `${label} ${ago}` : `${label} ${ago} · ${scanSummary(r)}`));
  }
  if (!r.findings) return h("div", { class: "scan-line" }, text(`Last run ${ago} · No issues found`));
  const { st, badge } = runResult(r);
  const added = counts(r).added;
  return h("div", { class: "scan-line" }, text(`Last run ${ago} ·${st.open ? "" : ` ${added ? `${added} added, none still drafts` : "Nothing new"}`}`),
    st.open ? h("button", { type: "button", class: "link scan-count new", title: "Show them on the Board", onclick: () => showRunTasks(r) }, plural(st.open, "new draft")) : null,
    badge);
}

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
  const meta = [SCAN_SCOPES[s.scope] || s.scope, s.full_scan_days && `full scan every ${plural(s.full_scan_days, "day")}`, s.path && s.scope !== "path" ? `in ${s.path}` : s.path, s.model, s.effort && `${s.effort} effort`].filter(Boolean).join(" · ");
  const hist = scans.history.get(s.name);
  const earlier = hist?.filter((r) => r.id !== s.last_run?.id && r.id !== s.last_report?.id);
  const live = sch.enabled && sch.cron;
  return h("li", { class: `scan-card${running ? " running" : ""}`, "data-scan": s.name },
    h("div", { class: "scan-head" },
      h("div", { class: "scan-name" }, h("strong", {}, s.title), scanKind(s)),
      scanLine(s),
      h("span", { class: `scan-when-sched${live ? " on" : ""}`, title: live ? `${sch.cron}${s.next_run ? `; next run ${untilText(s.next_run)}, ${dateTimeText(s.next_run)}` : ""}` : null },
        ico("clock"), live ? describeCron(sch.cron) : "Not scheduled"),
      h("div", { class: "scan-btns" },
        running
          ? ib({ type: "button", class: "btn small", onclick: attempt(() => api("POST", `/api/scan-runs/${running.id}/cancel`)) }, "stop", "Stop")
          : ib({ type: "button", class: "btn small primary", onclick: attempt(async () => {
              const run = await api("POST", `/api/projects/${state.project.id}/scans/${encodeURIComponent(s.name)}/run`);
              s.running = run.id;
              s.last_run = run;
              renderScans();
            }) }, "play", "Run now"),
        scanToggle(s))),
    scanApprovals(s, patch),
    !scans.open.has(s.name) ? null : h("div", { class: "scan-details", id: `scan-details-${s.name}` },
      h("div", { class: "scan-meta" }, meta, " · ", scanSource(s)),
      scanActions(s),
      h("div", { class: "scan-sched" },
        h("label", { class: "scan-switch", title: sch.cron ? (sch.enabled ? "Turn the schedule off" : "Turn the schedule on") : "Set a schedule first" },
          h("input", { type: "checkbox", role: "switch", checked: sch.enabled, disabled: !sch.cron, onchange: attempt((e) => patch(
            // Turning on a repository schedule never approved approves the one shown.
            e.target.checked && sch.requested && sch.cron === sch.requested ? { approve_schedule: sch.requested } : { enabled: e.target.checked })) }),
          h("span", {}, "Scheduled")),
        h("button", { type: "button", class: "link scan-cron", title: sch.cron ? `${sch.cron} — change` : "Set a schedule", onclick: editCron },
          sch.cron ? describeCron(sch.cron) : "No schedule"),
        sch.enabled && s.next_run ? h("span", { class: "scan-next", title: dateTimeText(s.next_run) }, `next ${untilText(s.next_run)}`) : null,
        sch.custom ? h("button", { type: "button", class: "link scan-reset", title: `Back to the template's schedule${sch.default ? ` (${sch.default})` : ""}, off` , onclick: attempt(() => patch({ reset: true })) }, "Reset") : null,
        !sch.enabled && sch.cron && s.builtin && !sch.custom ? h("span", { class: "scan-hint" }, "suggested; off until turned on") : null),
      scanEarly(s, patch),
      s.last_run ? scanReport(s.last_run, false, true) : null,
      s.last_report && s.last_report.id !== s.last_run?.id ? scanReport(s.last_report, true) : null,
      !s.last_run ? null : h("div", { class: "scan-foot" },
        h("button", { type: "button", class: "link", "aria-expanded": String(!!hist), onclick: attempt(() => {
          if (hist) { scans.history.delete(s.name); renderScans(); } else return loadScanHistory(s.name);
        }) }, hist ? "Hide earlier runs" : "Earlier runs")),
      hist ? h("ol", { class: "scan-history" }, ...earlier.map((r) => h("li", {}, scanReport(r, true))),
        earlier.length ? null : h("li", { class: "scan-never" }, "No earlier runs.")) : null));
}

// scanEarly is the schedule's "Run early if there's spare usage": once per
// cron period, early when the 5-hour usage window is about to reset with
// usage to spare, otherwise at the cron time. It needs a cron schedule.
function scanEarly(s, patch) {
  const sch = s.schedule;
  const e = sch.early || {};
  const on = !!(e.on && sch.cron);
  return h("div", { class: "scan-early" },
    h("div", { class: "scan-sched" },
      h("label", { class: "scan-switch", title: sch.cron ? null : "Set a schedule first" },
        h("input", { type: "checkbox", role: "switch", checked: on, disabled: !sch.cron,
          onchange: attempt((ev) => patch({ early: { on: ev.target.checked } })) }),
        h("span", {}, "Run early if there's spare usage")),
      !sch.cron ? h("span", { class: "scan-hint" }, "needs a schedule") : null,
      on ? h("label", { class: "scan-early-when" }, h("span", {}, "When"),
        h("select", { onchange: attempt((ev) => patch({ early: { when: ev.target.value } })) },
          h("option", { value: "any", selected: e.when !== "night" }, "Any time"),
          h("option", { value: "night", selected: e.when === "night" }, "At night only"))) : null),
    h("p", { class: "scan-early-hint" }, "Runs the scan up to an hour before your Claude usage resets if less than 70% is used. Otherwise it runs at the scheduled time."));
}

// shadowedScanCard is a repository scan that doesn't run because a scan
// written in Dash has its name.
function shadowedScanCard(s) {
  return h("li", { class: "scan-card shadowed", "data-scan": s.name },
    h("div", { class: "scan-head" },
      h("div", { class: "scan-name" }, h("strong", {}, s.title), scanKind(s), h("span", { class: "scan-shadow-tag" }, "Replaced")),
      h("div", { class: "scan-line" }, `Not used: the scan written in Dash called “${s.name}” runs instead`),
      h("div", { class: "scan-btns" }, scanToggle(s))),
    !scans.open.has(s.name) ? null : h("div", { class: "scan-details", id: `scan-details-${s.name}` },
      h("div", { class: "scan-meta" }, scanSource(s)),
      scanActions(s),
      h("p", { class: "scan-note" }, `Delete the scan written in Dash called “${s.name}” to use this file again.`)));
}

// scanActions edit, duplicate, export or delete a scan. Only scans written
// in Dash can be edited or deleted; any can be duplicated into one.
function scanActions(s) {
  const dash = s.source === "dash";
  const url = `/api/projects/${state.project.id}/scans/${encodeURIComponent(s.name)}`;
  const del = attempt(async () => {
    if (!await ask({
      title: `Delete ${s.title}?`, danger: true, ok: "Delete scan",
      body: `The scan is removed from Dash. Its earlier runs and the drafts it added (tagged scan:${s.name}) stay. If a built-in or repository scan is also called “${s.name}”, it is used again, with its schedule off.`,
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
  // A scan of the changes since its last run can be asked to look at everything once.
  const full = attempt(async () => {
    const run = await api("POST", `${url}/run`, { full: true });
    s.running = run.id;
    s.last_run = run;
    renderScans();
  });
  const busy = s.last_run?.status === "running" && s.running;
  return h("div", { class: "scan-actions" },
    s.scope === "since_last_scan" && !s.shadowed_by ? h("button", { type: "button", class: "link", disabled: !!busy,
      title: "Scan the whole repository once, not only what changed since the last run", onclick: full }, "Full scan") : null,
    dash ? h("button", { type: "button", class: "link", onclick: () => openScanEditor(s, true) }, "Edit") : null,
    h("button", { type: "button", class: "link", onclick: () => openScanEditor(s, false) }, "Duplicate"),
    dash && s.markdown ? h("button", { type: "button", class: "link", onclick: exp }, "Export to repo") : null,
    dash ? h("button", { type: "button", class: "link danger", onclick: del }, "Delete…") : null);
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
  setModelField(f, s?.model || ""); // the task form's choices, and a model id a template names
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
    path: f.path.value.trim(), model: f.model.value, effort: f.effort.value, schedule: f.schedule.value.trim(),
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
// shell or runs on a timer. A built-in that runs the repository's code with
// a shell (flaky-tests) asks too, saying why (approval_note).
function scanApprovals(s, patch) {
  const sch = s.schedule;
  const askTools = s.tools?.length && !s.tools_approved;
  const askSched = !!sch.requested;
  if (!askTools && !askSched) return null;
  // A built-in scan that runs the repository's code says why it asks.
  const note = s.approval_note;
  const approveTools = attempt(async () => {
    if (!await ask({
      title: `Let ${s.title} use these tools?`,
      body: note
        ? `${note} It asks for: ${s.tools.join(", ")}. Approve only if you trust everyone who can merge to this repository.`
        : `${s.file} asks for: ${s.tools.join(", ")}. Scans run on this computer; tools such as Bash run commands here. Approve only if you trust this template. If it asks for other tools later, it goes back to read-only until you approve again.`,
      ok: "Approve tools",
    })) return;
    await patch({ approve_tools: s.tools });
  });
  const changed = sch.enabled && sch.cron !== sch.requested;
  return h("div", { class: "scan-approve", role: "note" },
    askTools ? h("p", {},
      note ? h("span", {}, h("strong", {}, "Needs approval: "), note, " Tools: ", h("code", {}, s.tools.join(", ")), ". ")
        : h("span", {}, s.approved_tools?.length ? "The template now asks for other tools: " : "The template asks for tools beyond read-only: ",
          h("code", {}, s.tools.join(", ")), ". It runs read-only until you approve them."),
      h("button", { type: "button", class: "btn small", onclick: approveTools }, "Approve tools")) : null,
    askSched ? h("p", {},
      h("span", {}, changed
        ? `The template's schedule changed to ${describeCron(sch.requested)} (${sch.requested}); it keeps running ${describeCron(sch.cron)} until you approve the new one.`
        : `The template asks to run ${describeCron(sch.requested)} (${sch.requested}); it stays off until you turn it on.`),
      h("button", { type: "button", class: "btn small", onclick: attempt(() => patch({ approve_schedule: sch.requested })) }, changed ? "Approve new schedule" : "Approve and turn on")) : null);
}

// runStatus sorts the drafts a run added by what became of them since:
// still drafts (open), in the queue, running or in review (task), merged
// (fixed), or gone (deleted, which dismissed their issues, or merged into
// another draft). Critical and high ones still drafts are counted.
function runStatus(r) {
  const out = { open: 0, task: 0, fixed: 0, gone: 0, critical: 0, high: 0 };
  for (const id of runTasks(r)) {
    const t = state.todos.get(id);
    if (!t) { out.gone++; continue; }
    const k = t.status === "draft" ? "open" : t.status === "merged" ? "fixed" : "task";
    out[k]++;
    const sev = worstSeverity(t);
    if (k === "open" && (sev === "critical" || sev === "high")) out[sev]++;
  }
  // Runs from before the ids were kept: trust the counts.
  if (!runTasks(r).length) out.open = counts(r).added;
  return out;
}

// runResult reads what became of a run's drafts, with the critical and high
// ones still drafts as a badge (or null).
function runResult(r) {
  const st = runStatus(r);
  const tracked = runTasks(r).length > 0;
  const sev = tracked ? { critical: st.critical, high: st.high } : { critical: r.critical || 0, high: r.high || 0 };
  return { st, badge: severityBadge(sev, tracked ? 0 : r.urgent || 0), pickable: tracked ? st.open : 0 };
}

// severityBadge names a run's new or reopened critical and high issues by
// their severity: "1 critical · 2 high". Runs from before the split only
// kept their sum (urgent).
function severityBadge({ critical, high }, urgent) {
  const parts = [critical ? `${critical} critical` : null, high ? `${high} high` : null].filter(Boolean);
  const text = parts.length ? parts.join(" · ") : urgent ? `${urgent} critical or high` : null;
  if (!text) return null;
  const title = parts.length ? `Drafts this run added for ${critical && high ? "critical and high" : critical ? "critical" : "high"} issues that are still drafts` : "Drafts this run added for critical or high issues that are still drafts";
  return h("span", { class: "sev sev-high scan-urgent", title }, text);
}

// RUN_TRIGGER says why a run started.
const RUN_TRIGGER = {
  schedule: "scheduled",
  early: "started early (spare usage)",
  resume: "↻ resumed after restart",
  manual: "by hand",
};

// scanReport is a run's full report. For the run the card's status line
// already sums up (lead), what that line says is left out.
function scanReport(r, compact = false, lead = false) {
  const c = counts(r);
  const icon = { running: null, succeeded: "check", failed: "x", skipped: "redo", canceled: "stop", interrupted: "reload" }[r.status];
  const label = { running: "Running", succeeded: "Done", failed: "Failed", skipped: "Skipped", canceled: "Canceled", interrupted: "Interrupted" }[r.status] || r.status;
  const bits = [
    agoText(r.started_at),
    RUN_TRIGGER[r.trigger] || "by hand",
    r.commit ? (r.incremental && r.since ? `${r.since.slice(0, 7)}…${r.commit.slice(0, 7)}` : r.commit.slice(0, 7)) : null,
    r.finished_at ? `${Math.max(1, Math.round((new Date(r.finished_at) - new Date(r.started_at)) / 1000))}s` : null,
    r.cost_usd ? `≈$${r.cost_usd.toFixed(2)}` : null,
    r.invalid ? `${r.invalid} dropped` : null,
  ].filter(Boolean);
  let body;
  if (r.status === "succeeded" && r.findings) {
    // The counts are what the run found; what became of its drafts since is
    // read from the Board as it is now.
    const { st, badge, pickable } = runResult(r);
    const since = (n, text, title) => n ? h("span", { class: "scan-count muted", title }, `${n} ${text}`) : null;
    body = h("p", { class: "scan-counts" },
      lead ? null : st.open ? h("button", { type: "button", class: "scan-count link new", title: "Show them on the Board", onclick: () => showRunTasks(r) }, `${c.added} added`) : h("span", { class: "scan-count" }, `${c.added} added`),
      c.reopened ? h("span", { class: "scan-count", title: "Issues a merged task had fixed that came back: new drafts" }, `${c.reopened} came back`) : null,
      c.there ? h("span", { class: "scan-count", title: "A task already had them: it counts the report (×N)" }, `${c.there} already there`) : null,
      c.dismissed ? h("span", { class: "scan-count muted", title: "Found again, but dismissed before: no draft" }, `${c.dismissed} dismissed`) : null,
      since(st.task, "in a task", "Drafts from this run that were queued since: running, in review or failed"),
      since(st.fixed, "fixed", "Drafts from this run that were merged since"),
      since(st.gone, "deleted", "Drafts from this run deleted since (which dismissed their issues) or merged into another draft"),
      lead ? null : badge,
      pickable > 1 ? ib({ type: "button", class: "btn small scan-select", onclick: () => showRunTasks(r) }, "checks", `Select ${pickable} on the Board`) : null);
    if (lead && !body.children.length) body = null;
  } else if (!lead || r.status === "failed") {
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

