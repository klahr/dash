import { $, fill, h, ib, ico } from "./dom.js";
import { api } from "./api.js";
import { art, ask, attempt, fail, reduceMotion, toast } from "./dialogs.js";
import { state } from "./state.js";
import { refreshMachines } from "./events.js";
import { ghAvatar, isHub, loadProjects, needsRunner, openNewProject, renderSideProjects, selectProject } from "./projects.js";
import { agoText, renderBoard } from "./branches.js";
import { copyBlock } from "./prefs.js";
import { shortLabel } from "./usage.js";
import { openSettings } from "./settings.js";
import { ensureShell, forgetShellRefusal } from "./terminal.js";
import { loadTry } from "./try.js";

// ------------------------------------------------------------------ runners
// The one-line installer and the version runners should run (from /api/runners).
const runnerInfo = { hub: location.origin, version: "" };
// The "Add a runner" in progress: the runners there were before its code,
// so the one that registers with it can be told apart.
let runnerAdd = null;

// shq quotes s for a POSIX shell, leaving a leading ~/ for it to expand.
const shq = (s) => {
  if (s.startsWith("~/")) return `~/${s.length > 2 ? shq(s.slice(2)) : ""}`;
  return /^[\w@%+=:,./-]+$/.test(s) ? s : `'${s.replaceAll("'", `'\\''`)}'`;
};
const installCmd = (args = "") => `curl -fsSL ${runnerInfo.hub}/runner/install.sh | sh${args ? ` -s -- ${args}` : ""}`;
const isRelease = (v) => /^v\d+\.\d+\.\d+/.test(v || "");

// Computers that run the org's tasks (`dash runner`), each with its owner's
// Claude subscription. They connect out to this dashboard: no inbound
// access, VPN or shared secret needed on their side. Projects are linked to
// them in their settings; the hub sends those tasks only to them.
export async function loadRunners() {
  try {
    const res = await api("GET", "/api/runners");
    state.runners = res.runners;
    Object.assign(runnerInfo, { hub: res.hub || location.origin, version: res.version || "" });
  } catch {
    state.runners = null; // this Dash has no runners (it is one, say)
  }
  forgetShellRefusal(); // a runner came or went, or now opens terminals: try again
  if (state.view === "run") ensureShell();
  if (state.project) loadTry().catch(() => {}); // which runners Try may use changed
  $("#runners-open").hidden = state.runners === null;
  // The board waits for the runners, and shows the welcome screen until there's one.
  // The projects list groups by runner, so it changes with them too.
  if (state.projectsLoaded) { renderBoard(); renderSideProjects(); }
  return state.runners;
}
export const runnerName = (id) => state.runners?.find((r) => r.id === id)?.name || "its runner";

// The hub never runs Claude: tasks and scans of p run only on a runner
// linked to it. runnerNeeded says why they would wait now, or "" (also on a
// Dash without runners of its own, a runner, and for another computer's project).
export const startRunner = "start `dash runner` on a computer with git and Claude Code (account menu → Runners → Add a runner)";
function runnerNeeded(p) {
  if (!p || p.prefix || !Array.isArray(state.runners)) return "";
  if (!state.runners.length) return `A runner is required: this dashboard doesn't run Claude itself. To add one, ${startRunner}.`;
  const linked = state.runners.filter((r) => p.runners?.includes(r.id));
  if (!linked.length) return `${p.name} needs a runner: no runner is linked to it, so its tasks, scans, git history, files and commands wait until you link one in Project settings → Runners. This dashboard runs nothing itself.`;
  if (!linked.some((r) => r.online)) return `No runner is online for this project. Start one with \`dash runner\`. Until then its tasks and scans wait, and its git history, files and commands can't be shown.`;
  return "";
}
// renderRunnerBanner says above the board when the open project's git,
// files and commands can't be reached (no runner linked, or none online),
// with a way to link one.
export function renderRunnerBanner() {
  const el = $("#runner-banner");
  const p = state.project;
  const why = isHub() && p && !p.prefix && state.runners?.length ? runnerNeeded(p) : "";
  el.hidden = !why;
  if (!why) return el.replaceChildren();
  el.replaceChildren(why, " ", needsRunner(p)
    ? h("button", { type: "button", class: "link inline", onclick: () => { openSettings(); $("#settings-runners-field")?.scrollIntoView({ block: "center" }); } }, "Link a runner")
    : h("button", { type: "button", class: "link inline", onclick: attempt(() => openRunners()) }, "Runners"));
}

// showRunnerNeeded fills a .runner-note element for p, hidden when nothing is missing.
export function showRunnerNeeded(el, p) {
  const why = runnerNeeded(p);
  el.hidden = !why;
  el.textContent = why;
}
const runnerLimit = (r) => r.parallel || r.reported?.parallel || 1;
const runnerOutdated = (r) => isRelease(runnerInfo.version) && !!r.version && r.version !== runnerInfo.version;
// runnerClaudeWarnings is what runner r found changed about its Claude Code CLI.
const runnerClaudeWarnings = (r) => r.reported?.claude?.warnings || [];

// runnerStatus is what runner r is doing, in a few words, and its tone
// (on, busy, paused, off) for the dot and the card's accent.
function runnerStatus(r) {
  const n = (k) => `${k} task${k === 1 ? "" : "s"}`;
  if (!r.online) return { tone: "off", text: r.last_seen ? `Offline · seen ${agoText(r.last_seen)}` : "Never connected" };
  if (r.paused) return { tone: "paused", text: r.running ? `Paused · finishing ${n(r.running)}` : "Paused" };
  const u = r.reported?.usage;
  if (u?.known && u.paused) return { tone: "paused", text: `Waiting on Claude limits${u.reason ? ` (${u.reason})` : ""}` };
  if (r.running) return { tone: "busy", text: `Running ${r.running} of ${runnerLimit(r)}` };
  return { tone: "on", text: `Idle · up to ${runnerLimit(r)} at once` };
}

// runnerUsageMeters shows how much of its owner's Claude subscription
// windows runner r has used, fullest first.
function runnerUsageMeters(r) {
  const u = r.reported?.usage;
  if (!u?.known || !u.windows?.length) return null;
  const ws = [...u.windows].sort((a, b) => b.utilization - a.utilization).slice(0, 2);
  return h("div", { class: "runner-usage", title: "Claude usage of the runner owner's subscription" },
    ...ws.map((w) => {
      const pct = Math.round(w.utilization * 100);
      const bar = h("span", { class: "runner-meter-fill" });
      bar.style.width = `${Math.min(100, Math.max(0, pct))}%`;
      return h("span", { class: `runner-meter${pct >= 90 ? " high" : pct >= 70 ? " mid" : ""}` },
        h("span", { class: "runner-meter-label" }, shortLabel(w.name)),
        h("span", { class: "runner-meter-bar", role: "meter", "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": String(pct), "aria-label": `${shortLabel(w.name)} usage` }, bar),
        h("span", { class: "runner-meter-pct" }, `${pct}%`));
    }));
}

// The projects linked to runner r: their tasks run there.
const runnerProjects = (r) => (state.projects || []).filter((p) => !p.prefix && p.runners?.includes(r.id));

// What the sheet shows beside its rail: "overview", "add" (a new runner's
// code) or a runner's id. Back to the overview each time it opens.
let shown = "overview";
const patchRunner = (r, body) => api("PATCH", `/api/runners/${encodeURIComponent(r.id)}`, body);
// Yours first, then the others'; online before offline; by name.
const byStatus = (a, b) => (b.online - a.online) || a.name.localeCompare(b.name);
const runnerWarned = (r) => runnerOutdated(r) || runnerClaudeWarnings(r).length > 0;

// selectRunner shows key ("overview", "add" or a runner's id) beside the rail.
function selectRunner(key) {
  if (key === shown) return;
  closeRunnerPanels();
  shown = key;
  paintRunners();
  $("#runners-main").scrollTop = 0;
  $("#runners-nav .rail-item[aria-current]")?.scrollIntoView({ block: "nearest", inline: "nearest" }); // the strip on a narrow screen
}
// closeRunnerPanels puts away a runner's Projects and Add projects panels.
function closeRunnerPanels() {
  links.runner = null;
  for (const id of ["#runners-links", "#runners-repos"]) { $(id).hidden = true; $(id).replaceChildren(); }
}

// paintRunners draws the sheet from state.runners: the rail, the overview
// and the chosen runner. The rail keeps its focus across a redraw.
function paintRunners() {
  const list = state.runners;
  if (shown !== "overview" && shown !== "add" && !list?.some((r) => r.id === shown)) { closeRunnerPanels(); shown = "overview"; }
  if (shown === "add" && !$("#runners-code").hasChildNodes()) shown = "overview";
  const nav = $("#runners-nav");
  const navFocused = nav.contains(document.activeElement);
  paintRunnerNav(list);
  paintOverview(list);
  const r = list?.find((x) => x.id === shown);
  if (r) paintRunner(r);
  const section = r ? "runner" : shown;
  for (const s of $("#runners-main").children) s.classList.toggle("current", s.dataset.section === section);
  if (navFocused) $("#runners-nav .rail-item[aria-current]")?.focus();
}

function paintRunnerNav(list) {
  const item = (key, ...kids) => h("button", { type: "button", class: "rail-item", "data-rail": key,
    "aria-current": key === shown ? "true" : null, tabIndex: key === shown ? 0 : -1 }, ...kids);
  const runnerItem = (r) => {
    const st = runnerStatus(r);
    const b = item(r.id,
      h("span", { class: "runner-dot", "aria-hidden": "true" }),
      h("span", { class: "runner-rail-text" },
        h("span", { class: "runner-rail-name" }, r.name),
        h("span", { class: "runner-rail-state" }, st.text)),
      runnerWarned(r) ? h("span", { class: "runner-rail-warn", title: "Needs attention" }, ico("alert")) : null);
    b.classList.add("runner-rail", `tone-${st.tone}`);
    b.title = `${r.name}: ${st.text}`;
    return b;
  };
  const group = (head, ...items) => h("div", { class: "rail-group" }, head ? h("span", { class: "rail-head" }, head) : null, ...items);
  if (!Array.isArray(list)) return $("#runners-nav").replaceChildren();
  const mine = list.filter((r) => r.mine).sort(byStatus), others = list.filter((r) => !r.mine).sort(byStatus);
  fill($("#runners-nav"),
    group("", item("overview", "Overview")),
    mine.length ? group("Yours", ...mine.map(runnerItem)) : null,
    others.length ? group(mine.length ? "Others" : "Runners", ...others.map(runnerItem)) : null,
    group("", item("add", ico("plus"), runnerAdd ? "Adding a runner…" : "Add a runner")));
}
$("#runners-nav").addEventListener("click", (e) => {
  const b = e.target.closest(".rail-item");
  if (!b) return;
  if (b.dataset.rail === "add") attempt(openRunnerAdd)();
  else selectRunner(b.dataset.rail);
});
// Arrow keys, Home and End move along the rail, as in Settings; only the
// chosen entry is a tab stop. "Add a runner" isn't picked by passing it.
$("#runners-nav").addEventListener("keydown", (e) => {
  const items = [...$("#runners-nav").querySelectorAll(".rail-item")];
  const i = items.indexOf(e.target);
  if (i < 0) return;
  const next = { ArrowDown: i + 1, ArrowRight: i + 1, ArrowUp: i - 1, ArrowLeft: i - 1, Home: 0, End: items.length - 1 }[e.key];
  if (next === undefined) return;
  e.preventDefault();
  const b = items[(next + items.length) % items.length];
  if (b.dataset.rail === "add" && !runnerAdd) { for (const x of items) x.tabIndex = x === b ? 0 : -1; b.focus(); return; }
  selectRunner(b.dataset.rail);
  $("#runners-nav .rail-item[aria-current]")?.focus();
});

function runnerStats(list) {
  const stat = (n, label, cls = "") => h("span", { class: `runner-stat ${cls}` }, h("strong", {}, String(n)), ` ${label}`);
  const online = list.filter((r) => r.online).length;
  const running = list.reduce((s, r) => s + (r.running || 0), 0);
  const paused = list.filter((r) => r.paused).length;
  const outdated = list.filter(runnerOutdated).length;
  const claudeWarn = list.filter((r) => runnerClaudeWarnings(r).length).length;
  return [
    stat(online, `of ${list.length} online`, online ? "on" : "off"),
    stat(running, running === 1 ? "task running" : "tasks running", running ? "busy" : ""),
    paused ? stat(paused, "paused", "paused") : null,
    outdated ? stat(outdated, "out of date", "paused") : null,
    claudeWarn ? stat(claudeWarn, "with Claude Code warnings", "paused") : null,
  ].filter(Boolean);
}

// The overview: how the runners are doing, one line each to open it.
function paintOverview(list) {
  const box = $("#runners-list");
  $("#runners-add").hidden = list === null;
  if (list === null) {
    $("#runners-stats").replaceChildren();
    box.replaceChildren(h("p", { class: "hint" }, "This Dash doesn't take runners."));
    return;
  }
  $("#runners-stats").replaceChildren(...(list.length ? runnerStats(list) : []));
  if (!list.length) {
    $(".runners-about").open = true;
    box.replaceChildren(h("div", { class: "runners-empty" },
      ico("computer"),
      h("p", { class: "empty-title" }, "No runners yet"),
      h("p", { class: "hint" }, "Add a computer with git and Claude Code logged in: you get a one-line install command for its terminal (Linux or macOS).")));
    return;
  }
  const mine = list.filter((r) => r.mine).sort(byStatus), others = list.filter((r) => !r.mine).sort(byStatus);
  const row = (r, showOwner) => {
    const st = runnerStatus(r);
    const n = runnerProjects(r).length;
    return h("li", {}, h("button", { type: "button", class: `runner-row-btn tone-${st.tone}`, onclick: () => selectRunner(r.id) },
      h("span", { class: "runner-dot", "aria-hidden": "true" }),
      h("span", { class: "runner-row-main" },
        h("span", { class: "runner-name" }, r.name),
        h("span", { class: "runner-row-sub" },
          h("span", { class: "runner-state" }, st.text),
          showOwner && r.owner ? h("span", { class: "runner-owner" }, ico("users"), r.owner.username) : null,
          h("span", {}, n ? `${n} project${n === 1 ? "" : "s"}` : "No projects"),
          runnerWarned(r) ? h("span", { class: "runner-row-warn" }, ico("alert"), runnerOutdated(r) ? "Out of date" : "Claude Code warning") : null)),
      runnerUsageMeters(r),
      ico("chevron-right")));
  };
  const group = (title, rs, showOwner) => rs.length ? [
    title ? h("h3", { class: "org-section" }, title) : null,
    h("ul", { class: "runner-rows" }, ...rs.map((r) => row(r, showOwner))),
  ] : [];
  box.replaceChildren(...[
    ...group(others.length ? "Yours" : "", mine, false),
    ...group(mine.length ? `Others in ${state.me?.org?.name || "the organization"}` : "", others, true),
  ].filter(Boolean));
}

// paintRunner fills the chosen runner's page: its status, warnings, usage
// and projects above its Projects panels, then what its owner can change.
function paintRunner(r) {
  const st = runnerStatus(r);
  const outdated = runnerOutdated(r);
  const claude = r.reported?.claude;
  const claudeWarns = runnerClaudeWarnings(r);
  const projects = runnerProjects(r);
  const usage = runnerUsageMeters(r);
  fill($("#runner-detail"),
    h("div", { class: `runner-head tone-${st.tone}` },
      h("span", { class: "runner-dot", "aria-hidden": "true" }),
      h("h3", { class: "runner-title" }, r.name),
      h("span", { class: "runner-state" }, st.text)),
    h("div", { class: "runner-meta" },
      r.owner && !r.mine ? h("span", { class: "runner-owner" }, ico("users"), r.owner.username) : null,
      r.mine ? h("span", { class: "runner-owner" }, ico("users"), "Yours") : null,
      r.version ? h("span", { class: `runner-version${outdated ? " warn" : ""}`, title: outdated ? `Out of date: the hub runs ${runnerInfo.version}` : "Dash version" }, `Dash ${r.version}`) : null,
      claude ? h("span", { class: `runner-version${claudeWarns.length ? " warn" : ""}`,
        title: claudeWarns.length ? "Claude Code version: its output or flags look different from what Dash expects" : "Claude Code version" },
        `Claude ${claude.version || "?"}`) : null),
    outdated && r.mine ? h("div", { class: "runner-update" },
      h("p", { class: "hint warn" }, `Runs ${r.version}, the hub runs ${runnerInfo.version}. To update it, run this on ${r.name} (it keeps its registration):`),
      copyBlock(installCmd(), "Command copied", { pre: true })) : null,
    claudeWarns.length ? h("div", { class: "runner-update runner-claude-warn", role: "status" },
      h("p", { class: "hint warn" }, `Claude Code${claude.version ? ` ${claude.version}` : ""} on ${r.name} doesn't look the way Dash expects. Tasks may fail or miss usage limits; updating Dash may help:`),
      h("ul", {}, ...claudeWarns.map((w) => h("li", {}, w)))) : null,
    usage ? h("h4", { class: "runner-sub" }, "Claude usage") : null,
    usage,
    h("h4", { class: "runner-sub" }, "Projects"),
    h("div", { class: "runner-projects" },
      projects.length
        ? projects.map((p) => h("button", { class: "runner-project", type: "button", title: `Open ${p.name}`,
          onclick: () => { $("#runners-sheet").close(); selectProject(p); } }, ico("repo"), p.name))
        : h("span", { class: "hint" }, r.can_manage
          ? ["No projects linked yet. Choose some below, or link one in its project settings", r.mine ? ", or add projects from this computer." : "."]
          : "No projects linked yet.")),
    h("div", { class: "runner-actions" },
      ib({ class: "btn small", type: "button",
        title: r.can_manage ? `Pick the shared projects ${r.name} builds` : `The shared projects ${r.name} builds`,
        onclick: attempt(() => showRunnerLinks(r)) }, "repo", r.can_manage ? "Choose projects…" : "Shared projects…"),
      r.mine ? ib({ class: "btn small", type: "button", disabled: !r.online,
        title: r.online ? `Pick git repositories on ${r.name} to add as its projects` : `${r.name} is offline`,
        onclick: attempt(() => showRunnerRepos(r)) }, "plus", "Add from this computer…") : null));

  if (!r.can_manage) {
    $("#runner-manage").replaceChildren(h("p", { class: "hint runner-manage-note" }, `Only ${r.owner?.username || "its owner"} or an admin can change ${r.name}.`));
    return;
  }
  const parallel = h("input", { type: "number", min: "0", max: "64", inputmode: "numeric", value: r.parallel ? String(r.parallel) : "",
    placeholder: String(r.reported?.parallel || 1), "aria-label": `Tasks ${r.name} runs at once`,
    onchange: attempt(async (e) => { await patchRunner(r, { parallel: Number(e.target.value) || 0 }); await renderRunners(); }) });
  const setting = (title, hint, control, cls = "") => h("div", { class: `runner-setting ${cls}` },
    h("div", { class: "runner-setting-text" }, h("strong", {}, title), h("p", { class: "hint" }, hint)), control);
  fill($("#runner-manage"),
    h("h4", { class: "runner-sub" }, "Settings"),
    setting("Tasks at once", `How many tasks the hub sends it at once. Empty: the runner's own setting (${r.reported?.parallel || 1}).`, parallel),
    setting(r.paused ? "Paused" : "Taking tasks",
      r.paused ? "It takes no new tasks until you resume it." : "Pausing lets it finish what it runs, then it takes no new tasks.",
      ib({ class: "btn small", type: "button", onclick: attempt(async () => {
        await patchRunner(r, { paused: !r.paused });
        toast(r.paused ? `${r.name} takes tasks again` : `${r.name} paused: it finishes what it runs and takes no new tasks`);
        await renderRunners();
      }) }, r.paused ? "play" : "pause", r.paused ? "Resume" : "Pause")),
    setting("Revoke", "Disconnects it, stops its token and unlinks it from every project.",
      ib({ class: "btn small danger", type: "button", title: `Disconnect ${r.name} and revoke its token`, onclick: attempt(async () => {
        const ok = await ask({ title: `Revoke ${r.name}?`, body: "It's disconnected now, its token stops working and it's unlinked from every project. Tasks it is running fail and can be queued again on another runner. Run the install command with a new code to bring it back.", ok: "Revoke", danger: true });
        if (!ok) return;
        await api("DELETE", `/api/runners/${encodeURIComponent(r.id)}`);
        toast(`${r.name} revoked`);
        await refreshMachines();
      }) }, "trash", "Revoke…"), "danger"));
}

export async function renderRunners() {
  const list = await loadRunners();
  let added = null;
  if (runnerAdd && list) {
    added = list.find((r) => !runnerAdd.known.has(r.id));
    const st = $("#runner-add-status");
    if (st && added && !added.online) st.replaceChildren(`${added.name} registered, connecting…`);
    if (added?.online) {
      // Connected: on to its page, where its projects are picked.
      runnerAdd = null;
      $("#runners-code").replaceChildren();
      closeRunnerPanels();
      shown = added.id;
      toast(`Connected: ${added.name} is online`, { tone: "ok" });
    } else added = null;
  }
  paintRunners();
  if (added?.mine) showRunnerRepos(added).catch(fail);
}

// showRunnerRepos lists the git repositories runner r (the caller's own)
// finds in its projects directory, to tick and add as its projects. The
// runner looks; it reports only names, paths, remotes and branches.
async function showRunnerRepos(r) {
  links.runner = null;
  $("#runners-links").hidden = true;
  const box = $("#runners-repos");
  const url = `/api/runners/${encodeURIComponent(r.id)}/repos`;
  box.hidden = false;
  box.replaceChildren(h("h4", {}, `Projects on ${r.name}`), h("p", { class: "hint" }, "Looking for git repositories…"));
  box.scrollIntoView({ block: "nearest", behavior: reduceMotion.matches ? "auto" : "smooth" });
  let res;
  try {
    res = await api("GET", url);
  } catch (e) {
    box.replaceChildren(h("h4", {}, `Projects on ${r.name}`), h("p", { class: "hint warn" }, e.message || String(e)));
    return;
  }
  const close = h("button", { class: "btn small", type: "button", onclick: () => { box.hidden = true; box.replaceChildren(); } }, "Close");
  const where = res.root ? h("code", {}, res.root) : "its projects directory";
  if (!res.repos.length) {
    box.replaceChildren(h("h4", {}, `Projects on ${r.name}`),
      h("p", { class: "hint" }, "No git repositories to add in ", where, " (folders there and one level down; those already projects, the hub's among them, and old clones in ", h("code", {}, "hub/"), " are left out)."),
      h("div", { class: "runner-repos-actions" }, close));
    return;
  }
  const checks = res.repos.map((x) => h("input", { type: "checkbox", value: x.path, "aria-label": x.path }));
  const all = h("input", { type: "checkbox", "aria-label": "Select all" });
  const addBtn = h("button", { class: "btn primary", type: "button", disabled: true }, "Add selected");
  const sync = () => {
    const n = checks.filter((c) => c.checked).length;
    all.checked = n === checks.length;
    all.indeterminate = n > 0 && n < checks.length;
    addBtn.disabled = n === 0;
    addBtn.textContent = n ? `Add ${n} selected` : "Add selected";
  };
  all.addEventListener("change", () => { for (const c of checks) c.checked = all.checked; sync(); });
  for (const c of checks) c.addEventListener("change", sync);
  addBtn.addEventListener("click", attempt(async () => {
    const paths = checks.filter((c) => c.checked).map((c) => c.value);
    addBtn.disabled = true;
    addBtn.textContent = "Adding…";
    let out;
    try {
      out = await api("POST", url, { paths });
    } finally {
      sync();
    }
    if (out.added.length) toast(`Added ${out.added.length} project${out.added.length === 1 ? "" : "s"} on ${r.name}`);
    for (const f of out.failed) toast(`${f.path}: ${f.error}`, true);
    await loadProjects();
    await showRunnerRepos(r);
  }));
  box.replaceChildren(
    h("h4", {}, `Projects on ${r.name}`),
    h("p", { class: "hint" }, "Git repositories in ", where, " on ", r.name, ". Tick the ones to add as its own projects; their tasks run there."),
    h("label", { class: "runner-pick runner-repos-all" }, all, "Select all"),
    h("ul", { class: "runner-repos-list" }, ...res.repos.map((x, i) => h("li", {},
      h("label", { class: "runner-pick" }, checks[i],
        h("span", { class: "runner-repo" },
          h("span", { class: "runner-name" }, x.name),
          h("span", { class: "hint" }, x.path, x.branch ? ` · ${x.branch}` : " · detached", x.remote ? ` · ${x.remote}` : " · no remote")))))),
    h("div", { class: "runner-repos-actions" }, addBtn, close));
}

// ---- A runner's Projects: which of the org's shared projects it builds,
// picked from the runner's side. The same link as a project's Settings →
// Runners: ticking links the runner (it clones the project now when it is
// online), unticking unlinks it. A repository from its owner's GitHub that
// isn't a project yet becomes a shared project, linked in the same step.
const links = { runner: null, data: null, gh: null, ghError: "", busy: new Set(), q: "" };

async function showRunnerLinks(r) {
  $("#runners-repos").hidden = true;
  const box = $("#runners-links");
  Object.assign(links, { runner: r, data: null, gh: null, ghError: "", q: "" });
  box.hidden = false;
  box.replaceChildren(h("h4", {}, `Projects ${r.name} builds`), h("p", { class: "hint" }, "Loading the organization's projects…"));
  box.scrollIntoView({ block: "nearest", behavior: reduceMotion.matches ? "auto" : "smooth" });
  await Promise.all([refreshRunnerLinks(), r.mine && r.online ? loadLinkRepos(r) : null]);
}

// refreshRunnerLinks reloads the open Projects panel: a link changed here,
// in a project's settings or in another browser (project.updated).
export async function refreshRunnerLinks() {
  const r = links.runner;
  if (!r || $("#runners-links").hidden) return;
  try {
    const res = await api("GET", `/api/runners/${encodeURIComponent(r.id)}/links`);
    if (links.runner !== r) return;
    links.data = res;
  } catch (e) {
    if (links.runner !== r) return;
    $("#runners-links").replaceChildren(h("h4", {}, `Projects ${r.name} builds`), h("p", { class: "hint warn" }, e.message || String(e)), linksClose());
    return;
  }
  renderRunnerLinks();
}

async function loadLinkRepos(r) {
  try {
    links.gh = await api("GET", `/api/runners/${encodeURIComponent(r.id)}/github-repos`, null, "");
  } catch (e) {
    links.ghError = e.message || String(e);
  }
  if (links.runner === r) renderRunnerLinks();
}

const linksClose = () => h("div", { class: "runner-repos-actions" },
  h("button", { class: "btn small", type: "button", onclick: () => { links.runner = null; $("#runners-links").hidden = true; $("#runners-links").replaceChildren(); } }, "Close"));

// setLink links (on) or unlinks runner r and a project: an existing one
// (pid), or a GitHub repository to make one from (repo).
async function setLink(r, { pid, repo, name }, on) {
  const key = pid || repo;
  if (!on) {
    const l = links.data?.projects.find((x) => x.id === pid);
    if (l?.running) throw new Error(`${l.running} ${l.running === 1 ? "task" : "tasks"} of ${l.name} ${l.running === 1 ? "is" : "are"} running on ${r.name}: wait for ${l.running === 1 ? "it" : "them"} to finish (or cancel ${l.running === 1 ? "it" : "them"}) before unlinking it.`);
    if (l?.worktrees && !await ask({ title: `Unlink ${l.name} from ${r.name}?`,
      body: `${l.worktrees} ${l.worktrees === 1 ? "task has its worktree" : "tasks have their worktrees"} on ${r.name}. They stay there, but ${r.name} gets no more work for ${l.name}: queued again, they start afresh on another runner linked to it. The project and its tasks stay for everyone else.`,
      ok: "Unlink", danger: true })) return;
  }
  links.busy.add(key);
  renderRunnerLinks();
  try {
    if (!on) {
      await api("DELETE", `/api/runners/${encodeURIComponent(r.id)}/links/${encodeURIComponent(pid)}`);
      toast(`${r.name} no longer builds ${name}`);
      return;
    }
    if (repo) toast(`Creating the shared project ${name} and linking ${r.name}…`);
    else if (r.online) toast(`Linking ${name}; ${r.name} clones it…`);
    const res = await api("POST", `/api/runners/${encodeURIComponent(r.id)}/links`, pid ? { project_id: pid } : { github: repo, name });
    if (res.clone_error) toast(`${res.project.name} is linked, but ${res.clone_error}`, true);
    else toast(`${res.created ? "Created the shared project" : "Linked"} ${res.project.name}; ${res.cloned ? `${r.name} has its clone` : `${r.name} clones it when it next gets work for it`}`, { tone: "ok" });
  } finally {
    links.busy.delete(key);
    await loadProjects();
    await refreshRunnerLinks();
  }
}

function renderRunnerLinks() {
  const r = links.runner, res = links.data;
  if (!r || !res) return;
  const box = $("#runners-links");
  const can = res.runner.can_manage;
  const org = state.me?.org?.name || "the organization";
  const owner = r.owner?.username || "its owner";
  const q = links.q.trim().toLowerCase();
  const hit = (...xs) => !q || xs.some((x) => (x || "").toLowerCase().includes(q));
  const byId = new Map(res.projects.map((p) => [p.id, p]));
  const row = ({ key, checked, label, desc, meta, busy, disabled, onchange }) => {
    const box = h("input", { type: "checkbox", checked, disabled: disabled || busy, "aria-label": label.textContent || label });
    box.addEventListener("change", attempt(async () => {
      try { await onchange(box.checked); } finally { box.checked = checked; }
    }));
    return h("label", { class: "gh-repo", "data-key": key, "aria-busy": busy ? "true" : null }, box,
      h("span", { class: "gh-repo-text" }, label, desc ? h("span", { class: "gh-repo-desc" }, desc) : null,
        h("span", { class: "gh-repo-meta" }, ...meta.filter(Boolean).map((t) => h("span", {}, t)))));
  };
  const linkMeta = (l) => [l.linked ? `${r.name} builds it` : null, l.running ? `${l.running} running there` : null,
    l.worktrees ? `${l.worktrees} with a worktree there` : null];
  const projectRow = (l, extra = {}) => row({ key: l.id, checked: l.linked, busy: links.busy.has(l.id), disabled: !can,
    label: extra.label || h("span", { class: "gh-repo-title" }, h("strong", {}, l.name)),
    desc: extra.desc ?? l.remote_url, meta: [...(extra.meta || []), `shared project: ${l.name}`, ...linkMeta(l)],
    onchange: (on) => setLink(r, { pid: l.id, name: l.name }, on) });

  const rows = [];
  const seen = new Set();
  // The owner's GitHub repositories, grouped by owner, as in New project.
  if (links.gh) {
    let ghOwner = null;
    for (const x of links.gh.repos || []) {
      const l = x.project && byId.get(x.project);
      if (l) seen.add(l.id);
      if (!hit(x.nameWithOwner, x.description, l?.name)) continue;
      if (x.owner !== ghOwner) {
        ghOwner = x.owner;
        rows.push(h("div", { class: "gh-owner", role: "presentation" }, ghAvatar(ghOwner), h("span", {}, ghOwner), ghOwner === links.gh.user ? h("em", {}, "you") : null));
      }
      const label = h("span", { class: "gh-repo-title" }, h("span", { class: "gh-repo-owner" }, `${x.owner}/`), h("strong", {}, x.name));
      const hints = [x.isPrivate && "private", x.isFork && "fork", x.isArchived && "archived", x.updatedAt && `updated ${agoText(x.updatedAt)}`];
      rows.push(l ? projectRow(l, { label, desc: x.description || "", meta: hints })
        : row({ key: x.nameWithOwner, checked: false, busy: links.busy.has(x.nameWithOwner), disabled: !can, label, desc: x.description || "",
          meta: [...hints, "not a project yet: ticking it creates a shared project"],
          onchange: (on) => on && setLink(r, { repo: x.nameWithOwner, name: x.name }, true) }));
    }
  }
  // The org's other shared projects: not from this GitHub login (a remote
  // URL, another account's repository), or all of them without the list.
  const rest = res.projects.filter((l) => !seen.has(l.id) && hit(l.name, l.remote_url));
  if (rest.length) {
    rows.push(h("div", { class: "gh-owner", role: "presentation" }, h("span", {}, links.gh ? `Other shared projects in ${org}` : `Shared projects in ${org}`)));
    rows.push(...rest.map((l) => projectRow(l)));
  }

  const unlinked = res.projects.filter((l) => !l.linked);
  const all = can && unlinked.length ? h("button", { class: "btn small", type: "button", disabled: links.busy.size > 0,
    title: `Link ${r.name} to every shared project of ${org}`,
    onclick: attempt(async () => {
      if (!await ask({ title: `Build all of ${org}'s projects on ${r.name}?`,
        body: `${r.name} is linked to ${unlinked.length} more shared ${unlinked.length === 1 ? "project" : "projects"} (${unlinked.map((l) => l.name).join(", ")}) and clones them${r.online ? " now, one after another" : " when it next gets work for them"}. Their tasks may then run on it, with ${owner}'s Claude subscription.`,
        ok: "Link all" })) return;
      const out = await api("POST", `/api/runners/${encodeURIComponent(r.id)}/links/all`);
      if (out.linked.length) toast(`${r.name} now builds ${out.linked.length} more ${out.linked.length === 1 ? "project" : "projects"}`, { tone: "ok" });
      for (const f of out.failed) toast(`${f.project}: ${f.error}`, true);
      await loadProjects();
      await refreshRunnerLinks();
    }) }, `Build all ${org}'s projects (${unlinked.length})`) : null;

  const filter = h("input", { type: "search", placeholder: "Filter by name or description", value: links.q, autocomplete: "off", spellcheck: false, "aria-label": "Filter projects" });
  filter.addEventListener("input", () => {
    links.q = filter.value;
    renderRunnerLinks();
    const f = $("#runners-links input[type=search]");
    f?.focus();
    f?.setSelectionRange(f.value.length, f.value.length);
  });

  const ghNote = r.mine
    ? (!r.online ? `${r.name} is offline, so its GitHub login can't list your repositories now; the org's shared projects are below.`
      : links.ghError ? `${links.ghError} The org's shared projects are below.`
        : !links.gh ? `Asking ${r.name}'s gh for your repositories…`
          : `Your repositories from ${r.name}'s gh (signed in as ${links.gh.user}), and the org's other shared projects.`)
    : `Only ${owner} can list repositories with ${r.name}'s GitHub login, and this dashboard has no GitHub login of its own, so new repositories aren't listed here: only ${org}'s existing shared projects.${can ? "" : ` Only ${owner} or an admin can change what ${r.name} builds.`}`;
  box.replaceChildren(
    h("h4", {}, `Projects ${r.name} builds`),
    h("p", { class: "hint" }, `Tick a project to link ${r.name} to it: it clones the repository into its projects folder and takes its tasks. Untick to unlink: the project and its tasks stay for the other runners.`),
    r.mine ? h("p", { class: "hint warn" }, `Ticking a repository that isn't a project yet creates a shared project that everyone in ${org} sees and can link their runners to. For a repository only this computer works on, use Add from this computer… instead.`) : null,
    h("p", { class: "hint" }, ghNote),
    h("div", { class: "gh-filter-row" }, filter, all),
    h("div", { class: "gh-list", role: "group", "aria-label": `Projects ${r.name} builds` },
      ...(rows.length ? rows : [h("div", { class: "gh-empty" }, q ? "No project matches." : `${org} has no shared projects yet.`)])),
    linksClose());
}

// addRunner gets a one-time code and shows how to install a runner with it,
// beside the rail; renderRunners notices the runner it registers.
async function addRunner() {
  const before = await api("GET", "/api/runners");
  const { code, expires, hub } = await api("POST", "/api/runners/codes");
  if (hub) runnerInfo.hub = hub;
  runnerAdd = { known: new Set(before.runners.map((r) => r.id)) };
  const mins = Math.round((new Date(expires) - Date.now()) / 60000);
  const name = h("input", { type: "text", placeholder: "its host name", autocomplete: "off", spellcheck: false, pattern: "[a-z0-9][a-z0-9-]{0,31}" });
  const dir = h("input", { type: "text", placeholder: "~/dash-projects", autocomplete: "off", spellcheck: false });
  const oneLine = h("div", {});
  const manual = h("div", { hidden: true });
  const render = () => {
    const n = name.value.trim().toLowerCase(), d = dir.value.trim();
    let args = `--code ${code}`;
    if (n) args += ` --name ${shq(n)}`;
    if (d) args += ` --projects ${shq(d)}`;
    oneLine.replaceChildren(
      h("p", {}, "On the other computer (Linux or macOS), paste this in a terminal:"),
      copyBlock(installCmd(args), "Command copied", { pre: true }),
      h("p", { class: "hint" }, "It downloads Dash", isRelease(runnerInfo.version) ? ` ${runnerInfo.version}` : "", " to ", h("code", {}, "~/.local/bin"), ", checks git, claude and the connection to this hub, registers, and starts it as a background service. No sudo, no Go."));
    let flags = "";
    if (n) flags += ` --name ${shq(n)}`;
    if (d) flags += ` --projects ${shq(d)}`;
    const rel = isRelease(runnerInfo.version) ? `https://github.com/klahr/dash/releases/tag/${runnerInfo.version}` : "https://github.com/klahr/dash/releases/latest";
    manual.replaceChildren(h("ol", { class: "steps" },
      h("li", {}, h("strong", {}, "Get Dash. "), "Download the archive for the computer from ", h("a", { href: rel, target: "_blank", rel: "noopener" }, "the release"), " and unpack it, or build it from a checkout (", h("code", {}, "go build -o bin/dash ./cmd/dash"), "). It needs git, and claude logged in."),
      h("li", {}, h("strong", {}, "Register and run it:"), copyBlock(`dash runner --server ${runnerInfo.hub} --code ${code}${flags}`, "Command copied", { pre: true }),
        h("p", { class: "hint" }, "It keeps its token and reconnects by itself; later just run ", h("code", {}, "dash runner"), ".")),
      h("li", {}, h("strong", {}, "Optionally, keep it running in the background:"), copyBlock("dash runner install", "Command copied", { pre: true }),
        h("p", { class: "hint" }, "A systemd user service on Linux, a LaunchAgent on macOS. ", h("code", {}, "dash runner status"), " shows how it's doing."))));
  };
  name.addEventListener("input", render);
  dir.addEventListener("input", render);
  const tab = (label, pane, other) => h("button", { type: "button", role: "tab", "aria-selected": String(pane === oneLine), onclick: (e) => {
    pane.hidden = false; other.hidden = true;
    for (const b of e.currentTarget.parentElement.children) b.setAttribute("aria-selected", String(b === e.currentTarget));
  } }, label);
  render();
  $("#runners-code").replaceChildren(
    h("div", { class: "runner-add-head" },
      h("h3", { class: "runner-title" }, "Add a runner"),
      h("div", { class: "segmented", role: "tablist", "aria-label": "How to install" }, tab("One line", oneLine, manual), tab("Manual", manual, oneLine))),
    oneLine, manual,
    h("details", { class: "runner-add-opts" },
      h("summary", {}, "Name and projects directory"),
      h("p", { class: "hint" }, "Both optional: the name defaults to the computer's host name, and the projects directory is where it looks for and clones repositories. The command above updates as you type."),
      h("div", { class: "runner-add-fields" },
        h("label", { class: "field" }, h("span", {}, "Name"), name),
        h("label", { class: "field" }, h("span", {}, "Projects directory"), dir))),
    h("div", { class: "runner-add-foot" },
      h("p", { class: "runner-add-status", id: "runner-add-status", role: "status" }, "Waiting for the runner to connect…"),
      h("button", { class: "btn small", type: "button", onclick: closeRunnerAdd }, "Cancel")),
    h("p", { class: "hint" }, `The code works once, for ${mins} minutes, and makes the runner yours, in ${state.me?.org?.name || "this organization"}. Once it connects, its page opens here.`));
  closeRunnerPanels();
  shown = "add";
  paintRunners();
  $("#runners-main").scrollTop = 0;
}
// openRunnerAdd shows the runner being added, or starts adding one.
const openRunnerAdd = () => runnerAdd ? selectRunner("add") : addRunner();
// closeRunnerAdd puts the "Add a runner" pane away (its unused code just expires).
function closeRunnerAdd() {
  runnerAdd = null;
  $("#runners-code").replaceChildren();
  if (shown === "add") selectRunner("overview");
  else paintRunners();
}
$("#runners-add").addEventListener("click", attempt(openRunnerAdd));

// openRunners shows the organization's runners: an overview, and a page per
// runner for its projects, pausing, limiting and revoking it.
export async function openRunners() {
  runnerAdd = null;
  $("#runners-code").replaceChildren();
  closeRunnerPanels();
  shown = "overview";
  $("#runners-main").scrollTop = 0;
  await renderRunners();
  const d = $("#runners-sheet");
  if (!d.open) d.showModal();
}
// Enter in a field (tasks at once, the new runner's name) commits it rather
// than submitting the sheet's form, which would close it.
$("#runners-form").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && e.target.matches("input")) { e.preventDefault(); e.target.blur(); }
});

// openAddRunner opens the runners sheet straight on a new runner code.
async function openAddRunner() {
  await openRunners();
  await addRunner();
}

// The board of a hub that has no runners yet: what a runner is, how to add one, and what comes after.
export function welcomeScreen() {
  return [
    art("img/empty-board.webp", 480, 360),
    h("p", { class: "empty-title" }, "Welcome to Dash"),
    h("p", {}, "Claude does your tasks on ", h("strong", {}, "runners"), ": computers of yours with git and Claude Code logged in, each using its owner's Claude subscription. ",
"This dashboard doesn't run Claude itself, so start by adding one: ", h("code", {}, "dash runner"), " on a computer of yours."),
    h("ol", { class: "steps welcome-steps" },
      h("li", {}, h("strong", {}, "Add a runner. "), "Get a one-time code here, then paste the install command in a terminal on the other computer (Linux or macOS). It connects out to this dashboard: no open ports, VPN or sudo needed."),
      h("li", {}, h("strong", {}, "Create a project. "), "A project is a git repository. Link it to the runner in its settings."),
      h("li", {}, h("strong", {}, "Add tasks. "), "Each runs in its own worktree and comes back for review.")),
    h("div", { class: "welcome-actions" },
      ib({ class: "btn primary", onclick: attempt(openAddRunner) }, "plus", "Add a runner"),
      ib({ class: "btn", onclick: openNewProject }, "plus", "New project")),
  ];
}
