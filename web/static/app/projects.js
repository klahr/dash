import { projectGroups as groupProjects, isShared, projectRunnerNames, projectSubtitle, runnerBadge as badgeFor, uniqueProjects } from "../groups.js";
import { $, $$, h, ib, ico, plural, setLabel } from "./dom.js";
import * as store from "./store.js";
import { desktop } from "./viewport.js";
import { api, deepLink, on } from "./api.js";
import { attempt, busy, fail, railOf, reduceMotion, toast } from "./dialogs.js";
import { state } from "./state.js";
import { showView } from "./nav.js";
import { baseBranch, eventFor, latest, multi, projectKey, projectStatus, refreshLive } from "./events.js";
import { loadTodos, pushDone, renderPushMain } from "./board.js";
import { renderBoard } from "./branches.js";
import { ghMark, ghRepoHints, ghRepoLabel, ghRepoRows } from "./github.js";
import { closeDetail, openDetail } from "./detail.js";
import { syncPrefs } from "./prefs.js";
import { loadRunners, openRunners, renderSideRunners, startRunner } from "./runners.js";
import { loadMe, openOrg, renameOrg, sendInvite, setRequire2FA } from "./account.js";
import { loadBilling, newOrg } from "./billing.js";
import { createToken } from "./tokens.js";
import { openAccount } from "./accountsheet.js";
import { notifyOn, renderTitle } from "./notifications.js";
import { renderPerms } from "./review.js";
import { loadUsage } from "./usage.js";
import { loadCI, renderCI } from "./actions.js";
import { loadCheckouts, loadRemote, renderCheckouts, renderRemote } from "./origin.js";
import { openSettings } from "./settings.js";
import { renderChatPane } from "./chatpane.js";
import { renderQuick } from "./terminal.js";
import { loadProcs, loadTry, renderProcs } from "./try.js";
import { closeCommit, loadGraph, renderGraph } from "./history.js";
import { loadFiles, renderFile, renderFileTree } from "./files.js";
import { loadIssues, resetIssues } from "./issues.js";
import { loadScans, resetScans } from "./scans.js";
import { loadChecklist } from "./checklist.js";

// ------------------------------------------------------------------ projects
export async function loadProjects() {
  if (!state.machines) state.machines = await api("GET", "/api/machines");
  const lists = await Promise.all(state.machines.map(async (m) => {
    if (!m.ok) return [];
    try {
      return (await api("GET", "/api/projects", null, m.prefix)).map((p) => ({ ...p, machine: m.name, prefix: m.prefix }));
    } catch (e) {
      m.ok = false;
      m.error = e.message;
      return [];
    }
  }));
  state.projects = lists.flat();
  const first = !state.projectsLoaded;
  state.projectsLoaded = true;
  const saved = store.get("dash.project");
  const link = first ? deepLink : null;
  const want = (link?.project && state.projects.some((p) => p.id === link.project) ? link.project : null) || projectKey(state.project) || saved;
  const next = state.projects.find((p) => projectKey(p) === want || p.id === want) || state.projects[0] || null;
  if (projectKey(next) !== projectKey(state.project)) await selectProject(next);
  else { state.project = next; renderProjectHeader(); if (first && !next) renderBoard(); }
  renderProjectList();
  if (link?.task && next?.id === link.project) { showView("board"); openDetail(link.task).catch(() => {}); }
}


export async function selectProject(p) {
  state.project = p;
  state.todos = new Map();
  state.unpushed = 0;
  state.unpushedTodos = null;
  state.lane = null;
  state.follow = null;
  closeDetail();
  closeCommit();
  if (p) store.set("dash.project", projectKey(p));
  renderProjectHeader();
  refreshLive();
  loadUsage().catch(() => {});
  renderProjectList();
  state.ci = null;
  renderCI();
  state.remote = null;
  renderRemote();
  loadRemote(true).catch(() => {});
  state.checkouts = [];
  renderCheckouts();
  loadCheckouts().catch(() => {});
  renderChatPane();
  resetIssues();
  resetScans();
  loadTry().catch(() => {});
  if (!p) { state.perms = []; renderBoard(); renderProcs(); renderPerms(); renderGraph(null); renderFileTree(); renderFile(); loadChecklist().catch(fail); return; }
  await Promise.all([loadIssues(), loadTodos(), loadProcs(), loadPerms(), loadCI(), state.view === "history" && loadGraph(), state.view === "files" && loadFiles(), state.view === "checklist" && loadChecklist(), state.view === "scans" && loadScans()]);
}

async function loadPerms() {
  const p = state.project;
  if (!p) return;
  const { data, seen } = await latest(() => api("GET", `/api/permissions?project=${p.id}`));
  if (projectKey(state.project) !== projectKey(p)) return; // another project's by now
  let perms = data;
  for (const { ev, m } of seen) {
    if (ev.type !== "permission.updated" || !eventFor(p, ev, m)) continue;
    perms = perms.filter((x) => x.id !== ev.data.id);
    if (!ev.data.decision) perms.push(ev.data);
  }
  state.perms = perms;
  renderPerms();
}

// With several computers, the project's computer only fits the header on wide screens; on
// phones it would crowd the project name, and the menu lists it anyway.
function renderProjectBranch() {
  const p = state.project, el = $("#project-branch");
  el.textContent = p && desktop.matches && grouped() ? projectWhere(p) : "";
  el.hidden = !el.textContent;
}

export function renderProjectHeader() {
  const p = state.project;
  $("#project-name").replaceChildren(...(p ? [projectDot(p), p.name].filter(Boolean) : ["No project"]));
  renderBrand();
  renderTitle();
  // The project's own color marks the top bar (and the phone's browser chrome).
  const app = $(".app");
  if (p?.color) app.style.setProperty("--project", p.color);
  else app.style.removeProperty("--project");
  app.classList.toggle("has-project-color", !!p?.color);
  $('meta[name="theme-color"]').content = p?.color || "#101722";
  renderProjectBranch();
  $("#project-menu").hidden = !p;
  renderPushMain();
  renderQuick();
  const warn = $("#sync-warn");
  warn.hidden = !p?.sync_error;
  if (p?.sync_error) {
    warn.replaceChildren(h("strong", {}, `Can't sync ${baseBranch(p)} with GitHub`), h("span", {}, p.sync_error),
      h("span", { class: "hint" }, `Fix it in ${p.path}; Dash retries on the next merge.`));
  }
}

export function renderProjectList() {
  renderSideProjects();
  renderChatPane();
  syncPrefs();
}

export const notifyLabel = () => notifyOn() ? "Turn off notifications" : "Turn on notifications";

// openNewProject opens the Add project sheet. Every project belongs to a
// runner: the sheet picks one (the hub keeps no projects itself).
export function openNewProject() {
  const f = $("#new-project"), d = $("#new-project-sheet");
  if (!isHub()) {
    fail(new Error("Projects are added from the hub's dashboard: Add project there, on this runner."));
    return;
  }
  // Closed while it was adding one: show how that is going.
  if (np.run && !np.run.done) { d.showModal(); return; }
  const runners = projectRunners();
  if (!runners.length) {
    fail(new Error(!state.runners.length ? `A runner is required: this dashboard keeps no projects itself. To add one, ${startRunner}.`
      : state.runners.some((r) => r.online) ? "None of the online runners is yours, and only a runner's owner or an admin of the organization can link projects to it. Start your own with `dash runner`."
        : noRunnerOnline));
    return;
  }
  const keep = f.machine.value, pick = runners.some((r) => r.name === keep) ? keep : defaultRunner(runners);
  f.machine.replaceChildren(...runners.map((r) => h("option", { value: r.name, selected: r.name === pick },
    `${r.name}${r.mine ? " (yours)" : ""}`)));
  f.machine.value = pick;
  $("#new-project-runner").hidden = runners.length < 2;
  showProgress(false);
  d.showModal();
  setHubMode(hubMode());
  focusSource();
}

// ---- Add project: on a runner, never on the hub. A shared project is a git
// remote its runners clone: the repository comes from the user's GitHub
// repositories (listed with the picked runner's gh), a new one (created with
// that gh), or a pasted URL, and the runner it is linked to clones it into
// its projects folder, or adopts a clone of the same remote already there.
// A local-only project is the runner's own: a new repository in its projects
// folder (git init and an initial commit), with no remote, for throwaway
// work; deleting it deletes that folder too.
//
// The sheet's rail picks the source (railLayout); beside it only that
// source's fields, then the name and a collapsed Advanced section. Once
// submitted, the pane shows the steps ticking off (startAdd).
const SHEET = "new-project-sheet";
const hubMode = () => railOf(SHEET)?.current || "gh";
const ghState = { cache: new Map(), pick: null, loading: "" };
// np is the sheet's own state: the name it filled in itself (replaced as the
// repository changes, until the user types their own), the GitHub repository
// it created (so trying again doesn't create it twice), and the run under way.
const np = { nameAuto: "", created: null, run: null };
// The runner picked in the sheet, from state.runners.
function sheetRunner() {
  const name = $("#new-project").machine.value;
  return state.runners?.find((r) => r.name === name) || null;
}
const fromURL = (u) => (u || "").trim().replace(/\/+$/, "").replace(/\.git$/, "").split(/[/:]/).pop() || "";

// focusSource puts the focus where the chosen source starts: the filter, or its first field.
function focusSource() {
  const mode = hubMode(), f = $("#new-project");
  ({ gh: $("#gh-filter"), create: f.github, url: f.remote_url, local: f.name }[mode])?.focus();
}

function setHubMode(mode) {
  const d = $(`#${SHEET}`);
  d.dataset.mode = mode;
  $("#new-project-clone-row").hidden = mode === "local";
  $("#new-project-clone-hint").hidden = mode === "local";
  syncHubMode();
  if (mode === "gh") loadGitHubRepos().catch(fail);
}
$(`#${SHEET}`).addEventListener("railchange", (e) => { if (e.currentTarget.open) setHubMode(e.detail); });

// newProjectReady says whether the chosen source has what it needs, and if
// not, what blocks it (shown under the fields; a missing field just keeps
// the button disabled).
function newProjectReady() {
  const f = $("#new-project"), mode = hubMode(), r = sheetRunner();
  if (!r) return { ready: false, why: noRunnerOnline };
  if (!r.mine && (mode === "create" || mode === "local")) {
    return { ready: false, why: mode === "create" ? `Only ${r.name}'s owner can create repositories with its GitHub login. Pick one of your runners, or add it from a URL.`
      : `Only ${r.name}'s owner can make local-only projects on it. Pick one of your runners.` };
  }
  const has = { gh: !!ghState.pick, create: !!f.github.value.trim(), url: !!f.remote_url.value.trim(), local: !!f.name.value.trim() }[mode];
  return { ready: has, why: "" };
}

// syncHubMode updates what depends on the fields: the name (shown once
// there is a repository, filled in from it), the hints, and the button,
// which says what it will do and stays disabled until it can.
function syncHubMode() {
  const f = $("#new-project");
  const mode = hubMode(), r = sheetRunner(), where = r?.name || "The runner";
  const repo = { gh: ghState.pick?.nameWithOwner, create: f.github.value.trim(), url: f.remote_url.value.trim() }[mode] || "";
  const suggested = mode === "gh" ? ghState.pick?.name || "" : mode === "local" ? "" : fromURL(repo);
  if (f.name.value === np.nameAuto) f.name.value = np.nameAuto = suggested;
  $("#new-project-name-row").hidden = mode !== "local" && !repo;
  $("#new-project-name-label").replaceChildren(...(mode === "local" ? ["Name"] : ["Name ", h("em", {}, "optional, how Dash shows it")]));
  f.name.placeholder = mode === "local" ? "e.g. sandbox" : suggested;
  const picked = $("#new-project-picked");
  picked.hidden = mode !== "gh" || !ghState.pick;
  if (ghState.pick) {
    picked.replaceChildren(ghMark(), h("span", {}, h("span", { class: "gh-repo-owner" }, `${ghState.pick.owner}/`), h("strong", {}, ghState.pick.name)), h("em", {}, "selected"));
  }
  const { ready, why } = newProjectReady();
  const hint = $("#new-project-where");
  hint.classList.toggle("warn", !!why);
  hint.textContent = why || (mode === "local" ? "" : `${where} clones it into its projects folder.`);
  hint.hidden = !hint.textContent || (mode === "gh" && !r?.mine);
  $("#new-project-github-hint").textContent = `An empty ${f.github_public.checked ? "public" : "private"} repository, created with ${where}'s GitHub login.`;
  $("#new-project-github-hint").hidden = !!why;
  $("#new-project-local-where").textContent = r?.name || "the runner";
  setLabel($("#new-project-go"), "plus", {
    gh: ghState.pick ? `Add ${ghState.pick.nameWithOwner}` : "Pick a repository",
    create: "Create repository and add",
    url: "Add project",
    local: "Create local project",
  }[mode]);
  $("#new-project-go").disabled = !ready || !!np.run;
}
for (const el of ["remote_url", "github", "name"]) {
  const input = $("#new-project")[el];
  input.addEventListener("input", syncHubMode);
}
$("#new-project").github_public.addEventListener("change", syncHubMode);
// Enter in a field adds the project when it can (the sheet's first button,
// which a plain Enter would press, is Close). Enter in the filter picks a repository.
$("#new-project").addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || e.isComposing || e.target.localName !== "input" || e.target.type === "checkbox" || e.target.id === "gh-filter") return;
  e.preventDefault();
  if (!$("#new-project-go").disabled) $("#new-project-go").click();
});

// loadGitHubRepos lists the user's repositories with the picked runner's
// gh (once per runner while the page is open).
async function loadGitHubRepos(refresh = false) {
  const r = sheetRunner(), hint = $("#gh-hint");
  if (ghState.pick && (refresh || ghState.pick.runner !== r?.id)) ghState.pick = null;
  syncHubMode();
  // A message in the list's place, with a way on: from a URL, or trying again.
  const note = (msg, { retry = false, loading = false } = {}) => {
    hint.textContent = "";
    $("#gh-list").replaceChildren(h("div", { class: `gh-empty gh-note${loading ? " loading" : ""}`, role: loading ? "status" : retry ? "alert" : null },
      loading ? h("span", { class: "spinner", "aria-hidden": "true" }) : null,
      h("span", { class: "gh-note-text" }, msg),
      loading ? null : h("span", { class: "gh-note-actions" },
        retry ? ib({ type: "button", class: "btn small", onclick: () => loadGitHubRepos(true).catch(fail) }, "reload", "Try again") : null,
        h("button", { type: "button", class: "link", onclick: () => railOf(SHEET).show("url", { focus: true }) }, "Add it from a URL"))));
  };
  if (!r) return note(noRunnerOnline);
  if (!r.mine) return note(`Only ${r.name}'s owner can list repositories with its GitHub login. Pick one of your runners above.`);
  if (!refresh && ghState.cache.has(r.id)) { renderGitHubRepos(); return; }
  if (ghState.loading === r.id) return;
  ghState.loading = r.id;
  note(`Asking ${r.name}'s gh for your repositories…`, { loading: true });
  try {
    ghState.cache.set(r.id, await api("GET", `/api/runners/${encodeURIComponent(r.id)}/github-repos`, null, ""));
  } catch (e) {
    if (sheetRunner()?.id === r.id && hubMode() === "gh") note(`Couldn't list your repositories: ${e.message || e}`, { retry: true });
    return;
  } finally {
    if (ghState.loading === r.id) ghState.loading = "";
  }
  if (sheetRunner()?.id === r.id && hubMode() === "gh") renderGitHubRepos();
}

// renderGitHubRepos shows the cached list, filtered, grouped by owner. A
// repository that already has a project is marked and can't be picked.
function renderGitHubRepos() {
  const r = sheetRunner(), res = r && ghState.cache.get(r.id);
  if (!res) return;
  const all = res.repos || [];
  const { rows, more } = ghRepoRows(all, { q: $("#gh-filter").value.trim().toLowerCase(), user: res.user, row: (x) => {
    const picked = ghState.pick?.nameWithOwner === x.nameWithOwner;
    return h("button", {
      type: "button", class: `gh-repo${x.linked ? " linked" : ""}`, role: "option", "aria-selected": String(picked), "aria-disabled": x.linked ? "true" : null,
      title: x.linked ? `Already the project ${x.linked}` : x.url || x.nameWithOwner,
      onclick: (e) => {
        if (x.linked) return;
        const focused = document.activeElement === e.currentTarget;
        ghState.pick = { ...x, runner: r.id };
        renderGitHubRepos();
        syncHubMode();
        if (focused) $(".gh-repo[aria-selected=true]", $("#gh-list"))?.focus(); // the row is redrawn
      },
    },
    ghMark(),
    h("span", { class: "gh-repo-text" },
      ghRepoLabel(x),
      x.description ? h("span", { class: "gh-repo-desc" }, x.description) : null,
      h("span", { class: "gh-repo-meta" }, ...ghRepoHints(x).map((t) => h("span", {}, t)), x.linked ? h("span", { class: "gh-linked" }, `project: ${x.linked}`) : null)));
  } });
  $("#gh-list").replaceChildren(...(rows.length ? rows : [h("div", { class: "gh-empty" }, all.length ? "No repository matches." : "No repositories on GitHub yet: create one instead.")]));
  $("#gh-hint").replaceChildren(more > 0 ? `${more} more; type to narrow the list. `
    : all.length ? `${plural(all.length, "repository", "repositories")} from ${r.name}'s gh, signed in as ${res.user}. ` : "",
  h("button", { type: "button", class: "link inline", onclick: () => loadGitHubRepos(true).catch(fail) }, "Refresh"));
}
$("#gh-filter").addEventListener("input", renderGitHubRepos);
$("#gh-filter").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); $(".gh-repo:not(.linked)", $("#gh-list"))?.click(); }
});

// ---- Adding it: the pane shows the steps, each ticking off as it is done:
// creating the GitHub repository (for a new one), adding the project, and
// the runner's clone, which the hub has it make at once (POST
// /api/runners/{id}/links, as the Runners page does) so a remote it can't
// read says so here. A step that fails shows why, with Try again; what was
// entered stays. Done, the sheet closes on the new project.
const CLONE_WAIT_MS = 45000; // then the clone carries on out of sight
const STEP_ICONS = { ok: "check", fail: "x", note: "clock" };

// startAdd reads the sheet and starts adding the project it describes.
function startAdd() {
  const f = $("#new-project"), r = sheetRunner(), mode = hubMode();
  if (np.run || !r || !newProjectReady().ready) return;
  const body = Object.fromEntries(new FormData(f));
  const commands = { run_command: body.run_command, test_command: body.test_command, deploy_command: body.deploy_command };
  const run = { mode, r, name: (body.name || "").trim(), remote: "", project: null, made: null, done: false, steps: [] };
  const id = encodeURIComponent(r.id);
  const step = (label, fn) => run.steps.push({ label, fn, state: "wait", detail: "", actions: null });
  if (mode === "local") {
    run.title = `Creating ${run.name}`;
    step(`Creating the repository on ${r.name}`, async (s) => {
      run.project = await api("POST", `/api/runners/${id}/projects`, { name: run.name, ...commands }, "");
      s.label = `Created ${run.project.name} on ${r.name}, local only`;
    });
  } else {
    if (mode === "gh") {
      run.remote = `https://github.com/${ghState.pick.nameWithOwner}.git`;
      run.name ||= ghState.pick.name;
      run.title = `Adding ${ghState.pick.nameWithOwner}`;
    } else if (mode === "url") {
      run.remote = body.remote_url.trim();
      run.name ||= fromURL(run.remote);
      run.title = `Adding ${fromURL(run.remote)}`;
    } else {
      const want = { runner: r.id, name: body.github.trim(), public: !!body.github_public };
      run.title = `Creating ${want.name}`;
      step("Creating GitHub repository", async (s) => {
        // Created already, by a try that failed later on: not again.
        const same = np.created && np.created.runner === want.runner && np.created.name === want.name;
        const repo = same ? np.created.repo : await api("POST", `/api/runners/${id}/github-repos`, { name: want.name, public: want.public }, "");
        np.created = { ...want, repo };
        ghState.cache.delete(r.id);
        run.remote = `https://github.com/${repo.name}.git`;
        run.name ||= repo.name.split("/").pop();
        s.label = `Created ${repo.name} on GitHub`;
      });
    }
    step("Adding project", async (s) => {
      run.project = await api("POST", "/api/projects", { name: run.name, remote_url: run.remote, clone_url: (body.clone_url || "").trim(), runners: [r.id], ...commands }, "");
      np.created = null;
      s.label = `Added ${run.project.name}`;
    });
    step(`${r.name} cloning`, async (s) => {
      const call = api("POST", `/api/runners/${id}/links`, { project_id: run.project.id }, "");
      call.catch(() => {});
      let res;
      try {
        res = await Promise.race([call, new Promise((ok) => setTimeout(() => ok(null), CLONE_WAIT_MS))]);
      } catch (e) {
        if (![403, 404, 405].includes(e.status)) throw e;
        res = null; // can't ask: it clones when it first gets work
      }
      if (res?.clone_error) throw new Error(res.clone_error);
      if (res?.cloned) { s.label = `Cloned on ${r.name}`; return; }
      s.state = "note";
      s.label = res ? `${r.name} is offline: it clones the project when it is back` : `Clone started on ${r.name}`;
    });
  }
  np.run = run;
  showProgress(true);
  $("#new-project-progress-title").focus(); // the button that had it is gone
  runAdd(run);
}

// runAdd does run's steps from the first not done yet: from the start, or
// from the one that failed, on Try again.
async function runAdd(run) {
  for (const s of run.steps) {
    if (s.state === "ok" || s.state === "note") continue;
    s.state = "run";
    s.detail = "";
    s.actions = null;
    renderRun(run);
    try {
      await s.fn(s);
      if (s.state === "run") s.state = "ok";
    } catch (e) {
      s.state = "fail";
      s.detail = e.message || String(e);
      s.actions = failActions(run);
      renderRun(run);
      $(".np-step.is-fail .btn.primary", $("#new-project-steps"))?.focus();
      if (!$(`#${SHEET}`).open) toast(`${run.title}: ${s.detail}`, { tone: "error", action: { label: "Show", run: openNewProject } });
      return;
    }
    renderRun(run);
  }
  await finishAdd(run);
}

// failActions: Try again, from the step that failed; and, before anything
// was made, Change details, back to the form as it was. Once the project is
// there (only its clone failed), Open project instead.
function failActions(run) {
  const again = ib({ type: "button", class: "btn small primary", onclick: () => runAdd(run) }, "reload", "Try again");
  const back = run.project
    ? ib({ type: "button", class: "btn small", onclick: () => finishAdd(run).catch(fail) }, "right", "Open project")
    : ib({ type: "button", class: "btn small", onclick: () => { np.run = null; showProgress(false); syncHubMode(); focusSource(); } }, "edit", "Change details");
  return [again, back];
}

// finishAdd opens the new project and closes the sheet, cleared for the next.
async function finishAdd(run) {
  if (run.done) return;
  run.done = true;
  const { r, mode } = run;
  const d = $(`#${SHEET}`), f = $("#new-project");
  renderRun(run);
  await loadProjects();
  const made = run.mode === "local" ? state.projects.find((x) => x.id === run.project.id && x.machine === r.name)
    : state.projects.find((x) => x.id === run.project.id && !x.prefix);
  if (made) await selectProject(made);
  if (!reduceMotion.matches && d.open) await new Promise((ok) => setTimeout(ok, 500)); // a moment to see it all ticked
  np.run = null;
  f.reset();
  np.nameAuto = "";
  ghState.pick = null;
  if (d.open) d.close();
  showProgress(false);
  const clone = run.steps.at(-1);
  const name = run.project.name;
  // Only platform admins see runners' own projects in this dashboard.
  toast(mode === "local" ? (made ? `Created ${name} on ${r.name}, local only` : `Created ${name} on ${r.name}, local only: open it from ${r.name}'s own dashboard`)
    : `${mode === "create" ? "Created" : "Added"} ${name}; ${clone.state === "ok" ? `cloned on ${r.name}` : clone.state === "fail" ? `${r.name} couldn't clone it yet` : `${r.name} clones it into its projects folder`}`,
  { tone: clone.state === "fail" ? "info" : "ok" });
}

// showProgress swaps the fields for the steps (or back), with the source,
// the runner and the button out of reach meanwhile.
function showProgress(on) {
  const f = $("#new-project");
  $("#new-project-fields").hidden = on;
  $("#new-project-progress").hidden = !on;
  $("#new-project-nav").inert = on;
  $(`#${SHEET}`).classList.toggle("np-busy", on);
  f.machine.disabled = on;
  $("#new-project-go").hidden = on;
  if (!on) syncHubMode();
}

function renderRun(run) {
  if (np.run !== run) return;
  const failed = run.steps.some((s) => s.state === "fail");
  $("#new-project-progress-title").textContent = failed ? `${run.title}: something went wrong` : run.done ? `${run.title}: done` : `${run.title}…`;
  $("#new-project-steps").replaceChildren(...run.steps.map((s) => h("li", { class: `np-step is-${s.state}` },
    h("span", { class: "np-step-icon", "aria-hidden": "true" }, s.state === "run" ? h("span", { class: "spinner" }) : STEP_ICONS[s.state] ? ico(STEP_ICONS[s.state]) : null),
    h("span", { class: "np-step-text" },
      h("span", { class: "np-step-label" }, s.label, h("span", { class: "sr-only" }, { run: " (in progress)", ok: " (done)", fail: " (failed)", wait: "", note: "" }[s.state])),
      s.detail ? h("span", { class: "np-step-error", role: "alert" }, s.detail) : null,
      s.actions ? h("span", { class: "np-step-actions" }, ...s.actions) : null))));
}

// isHub reports whether this dashboard is a hub: it has runners (or could),
// and keeps no projects on its own computer. Only a runner started with
// -addr serves a dashboard that isn't one; it makes no projects.
export const isHub = () => Array.isArray(state.runners);
const noRunnerOnline = "No runner is online. Start one with `dash runner`.";

// projectRunners are the runners a project can be made on: the org's online
// runners the user may link projects to (their own, or any as an admin).
// From state.runners, not state.machines: only platform admins see the
// runners among the machines.
const projectRunners = () => (state.runners || []).filter((r) => r.online && (r.mine || r.can_manage));

// defaultRunner picks where a new project goes: an online runner of the
// user's own, else the open project's computer, else the first.
function defaultRunner(runners) {
  const mine = runners.find((r) => r.mine);
  return (mine || runners.find((r) => r.name === state.project?.machine) || runners[0])?.name;
}
$("#new-project").machine.addEventListener("change", () => { if (hubMode() === "gh") loadGitHubRepos().catch(fail); else syncHubMode(); });

// A project's image icon, or its initial on its color.
export function projectIcon(p, el = h("span", { class: "side-project-icon", "aria-hidden": "true" })) {
  el.style.setProperty("--project", p.color || "");
  el.replaceChildren(p.icon ? h("img", { src: on(p.prefix || "", `/api/uploads/${p.icon}`), alt: "" }) : [...p.name][0]?.toUpperCase() || "?");
  return el;
}
// The top bar's brand: the active project's icon and name, or Dash's own without one.
// Phones show only the icon (CSS hides the name).
const dashBrand = { logo: $("#brand-logo"), name: $("#brand-name").textContent };
function renderBrand() {
  const p = state.project, brand = $(".brand");
  brand.querySelector(".brand-icon, #brand-logo").replaceWith(p ? projectIcon(p, h("span", { class: "side-project-icon brand-icon", "aria-hidden": "true" })) : dashBrand.logo);
  $("#brand-name").textContent = p ? p.name : dashBrand.name;
  brand.classList.toggle("is-project", !!p);
}
// A project list's two-line label: the name line, and under it, smaller and
// muted, the project's owner/repo (projectSubtitle) when it has one.
const projectLabel = (nameLine, sub, cls) =>
  h("span", { class: `project-label ${cls}` }, nameLine, sub ? h("span", { class: "project-sub" }, sub) : null);
export const projectDot = (p) => (p.color ?h("span", { class: "project-dot", style: `--project: ${p.color}`, "aria-hidden": "true" }) : null);

// Desktop sidebar / phone menu: every project, once, one click away (see
// groups.js for its order). The current one shows its branch; its settings
// and actions live in the top bar's project menu. Runners show as a badge
// on a project whose runners need attention, and in the Runners block below
// the project buttons (runners.js).
export function renderSideProjects() {
  const item = (p) => {
    const cur = projectKey(state.project) === projectKey(p);
    const st = projectStatus(p);
    const stLabel = st === "running" ? "Claude is working" : st === "review" ? "Ready for review" : "";
    const icon = projectIcon(p);
    if (st) icon.append(h("span", { class: `side-project-status ${st}` }));
    const sub = projectSubtitle(p);
    const badge = runnerBadge(p);
    return h("li", { class: cur ? "current" : "" },
      h("button", {
        type: "button", class: "side-project", title: [p.name, sub, p.path, stLabel].filter(Boolean).join("\n"), "aria-current": cur ? "true" : "false",
        "aria-label": [p.name, sub, stLabel.toLowerCase(), badge?.label].filter(Boolean).join(", "),
        onclick: attempt(async () => { await selectProject(p); setMenu(false); }),
      },
      icon,
      projectLabel(h("span", { class: "side-project-name" }, p.name), sub, "side-project-text"),
      sharedMark(p),
      badge ? badgeEl(badge, "side-project-branch") : cur && baseBranch(p) ? h("span", { class: "side-project-branch" }, baseBranch(p)) : null));
  };
  const items = withHeadings(projectGroups(), (g) => h("li", { class: "side-machine", "data-group": g.key }, groupHead(g)), item);
  $("#side-project-list").replaceChildren(...(items.length ? items : [h("li", { class: "machine-empty" }, "No projects yet")]));
  renderSideRunners();
}

// projectGroups are the PROJECTS list's groups (see groups.js): on a hub,
// Shared and one per runner with local-only projects; elsewhere one per
// computer. Each project is in one group.
function projectGroups() { return groupProjects({ machines: state.machines || [], runners: state.runners ?? null, projects: state.projects || [] }); }
// withHeadings lists the groups' items, under their headings only when
// there is more than one group.
const withHeadings = (groups, head, item) => groups.length > 1
  ? groups.flatMap((g) => [head(g), ...g.projects.map(item)]) : groups.flatMap((g) => g.projects.map(item));
// The list is grouped on a hub (by runner) or with several computers.
export function grouped() { return isHub() || multi(); }
const groupHead = (g) => [h("span", { class: "side-machine-name" }, g.name)];
// runnerBadge is p's runner badge (see groups.js), or null when its runners are fine.
const runnerBadge = (p) => badgeFor(p, { machines: state.machines || [], runners: state.runners ?? null });
// badgeEl shows a runner badge; hovering it lists the runners and their status.
const badgeEl = (b, cls) => h("span", { class: `${cls} warn runner-badge ${b.kind}`, title: b.title }, b.label);
// A quiet mark on the hub's shared projects, to tell them from a runner's own.
export function sharedMark(p) {
  if (!isHub() || !isShared(p)) return null;
  const names = projectRunnerNames(p, state.runners);
  const title = names.length ? `Shared project, cloned on ${names.join(", ")}` : "Shared project";
  return h("span", { class: "shared-mark", title, "aria-label": "shared" }, ico("users"));
}
// Where p is worked on, for labels: its runner, or a shared project's runners.
export function projectWhere(p) {
  if (!isHub() || !isShared(p)) return p.machine;
  const names = projectRunnerNames(p, state.runners);
  return names.length ? names.join(", ") : "needs a runner";
}

// needsRunner reports whether p is a hub project no runner is linked to
// (made on the hub before it stopped running anything itself).
export const needsRunner = (p) => isHub() && !!p && !p.prefix && !p.runners?.length;

export async function cleanup() {
  const p = state.project;
  if (!p) throw new Error("Select a project first (p).");
  toast("Cleaning up…");
  const res = await api("POST", `/api/projects/${p.id}/cleanup`);
  toast(res.summary.charAt(0).toUpperCase() + res.summary.slice(1));
}

// Delete project: from Dash only, with its folder, and/or its GitHub
// repository, after the password is typed again.
export function openDeleteProject() {
  const p = state.project;
  if (!p) throw new Error("Select a project first (p).");
  const f = $("#delete-project");
  f.reset();
  $("#delete-project-title").textContent = `Delete ${p.name}`;
  $("#delete-project-path").textContent = p.path;
  // A hub project is only forgotten: its clones stay on the runners (and
  // any files from before the hub stopped keeping projects stay on disk).
  // A local-only project's folder always goes with it.
  const onHub = isHub() && !p.prefix;
  $("#delete-project .choice").hidden = onHub || !!p.local_only;
  $("#delete-local-only").hidden = !p.local_only;
  $("#delete-local-path").textContent = p.path;
  if (p.local_only) f.files.value = "delete";
  $("#delete-github-row").hidden = $("#delete-github-hint").hidden = !p.remote_url || onHub;
  $("#delete-github-hint").textContent = `Deletes ${p.remote_url} for good, with its issues and pull requests. gh needs the delete_repo scope.`;
  syncDeleteProject();
  $("#delete-project-sheet").showModal();
  f.password.focus();
}
function syncDeleteProject() {
  const f = $("#delete-project");
  const what = [f.files.value === "delete" && "folder", f.github.checked && "GitHub repo"].filter(Boolean);
  setLabel($("#delete-project-ok"), "trash", what.length ? `Delete project and ${what.join(" and ")}` : "Remove from Dash");
}
$("#delete-project").addEventListener("change", syncDeleteProject);
$("#delete-project").addEventListener("submit", attempt(async (e) => {
  if (e.submitter?.value !== "ok") return;
  e.preventDefault();
  const f = e.target;
  const p = state.project;
  await busy($("#delete-project-ok"), async () => {
    try {
      await api("DELETE", `/api/projects/${p.id}`, { password: f.password.value, delete_files: f.files.value === "delete", delete_github: !$("#delete-github-row").hidden && f.github.checked }, p.prefix);
    } catch (err) {
      f.password.select();
      throw err;
    }
  });
  $("#delete-project-sheet").close();
  await loadProjects(); // moves on to another project
  toast(`${p.name} deleted`, { tone: "ok" });
}));

// Phones open the sidebar's projects, actions and status as a drawer from the
// top bar; on desktop the same button collapses the sidebar to its icons.
export const menuOpen = () => $(".app").classList.contains("menu-open");
export const sideCollapsed = () => $(".app").classList.contains("side-collapsed");
function syncMenuBtn() {
  const btn = $("#menu-open");
  const label = !desktop.matches ? "Menu" : sideCollapsed() ? "Expand sidebar" : "Collapse sidebar";
  btn.setAttribute("aria-expanded", String(desktop.matches ? !sideCollapsed() : menuOpen()));
  btn.setAttribute("aria-label", label);
  btn.title = desktop.matches ? `${label} ([)` : "";
  // The project name opens the dropdown on desktop and the drawer on phones.
  $("#project-switch").setAttribute("aria-controls", desktop.matches ? "project-picker" : "menu");
}
// While the phone drawer is open it is modal: everything behind the scrim
// (the top bar, main, the chat pane and the bottom nav's own buttons, which
// share #menu's parent) is inert. An open task panel keeps its own inert state.
const menuBackground = () => [...[".topbar", "main", "#chat-pane"].map((s) => $(s)).filter(Boolean),
  ...$$(".nav > *").filter((el) => el.id !== "menu" && el.id !== "menu-scrim")];
export function setSideCollapsed(on) {
  $(".app").classList.toggle("side-collapsed", on);
  store.setFlag("dash.sideCollapsed", on);
  syncMenuBtn();
}
export function setMenu(open) {
  open = open && !desktop.matches;
  const wasOpen = menuOpen();
  const menu = $("#menu");
  $(".app").classList.toggle("menu-open", open);
  $("#menu-scrim").hidden = !open;
  $("#project-switch").setAttribute("aria-expanded", String(open));
  if (open !== wasOpen) {
    // The task panel makes the top bar, main and chat pane inert itself (and the
    // whole .nav), so closing the drawer over it leaves those inert.
    for (const el of menuBackground()) el.inert = open || (!!state.detail && !el.closest(".nav"));
    if (open) { menu.setAttribute("role", "dialog"); menu.setAttribute("aria-modal", "true"); menu.setAttribute("aria-label", "Menu"); }
    else { menu.removeAttribute("role"); menu.removeAttribute("aria-modal"); menu.removeAttribute("aria-label"); }
    // Closing returns focus to the menu button, unless the caller already moved it
    // somewhere outside the drawer (a section button, the palette).
    const a = document.activeElement;
    if (!open && (!a || a === document.body || menu.contains(a))) $("#menu-open").focus();
  }
  syncMenuBtn();
}
$(".app").classList.toggle("side-collapsed", store.flag("dash.sideCollapsed"));

// Desktop: drag the sidebar's right edge to resize it, like the chat pane. The
// width survives collapsing; CSS also caps it at 40vw so main stays usable.
const SIDE_MIN_W = 180, SIDE_MAX_W = 480;
function setSideWidth(w, save = true) {
  w = w == null ? null : Math.min(SIDE_MAX_W, Math.max(SIDE_MIN_W, Math.round(w)));
  if (w) $(".app").style.setProperty("--side-w-set", `${w}px`);
  else $(".app").style.removeProperty("--side-w-set");
  if (save) store.set("dash.sideW", w || null);
}
setSideWidth(Number(store.get("dash.sideW")) || null, false);
{
  const handle = $("#side-resize"), panel = $(".sidebar");
  handle.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    handle.setPointerCapture(e.pointerId);
    handle.classList.add("dragging");
    const left = panel.getBoundingClientRect().left;
    const move = (ev) => setSideWidth(ev.clientX - left, false);
    const up = () => {
      handle.removeEventListener("pointermove", move);
      handle.classList.remove("dragging");
      setSideWidth(panel.getBoundingClientRect().width);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up, { once: true });
    handle.addEventListener("pointercancel", up, { once: true });
  });
  handle.addEventListener("dblclick", () => setSideWidth(null));
  handle.addEventListener("keydown", (e) => {
    const step = e.shiftKey ? 64 : 16;
    const d = e.key === "ArrowRight" ? step : e.key === "ArrowLeft" ? -step : 0;
    if (!d) return;
    e.preventDefault();
    e.stopPropagation();
    setSideWidth(panel.getBoundingClientRect().width + d);
  });
}
syncMenuBtn();
export function openProjects() {
  if (desktop.matches && sideCollapsed()) return setPicker(true);
  renderProjectList();
  setMenu(true);
  ($(".side-project[aria-current=true]") || $(".side-project"))?.focus();
}
// J/K: the next/previous project in sidebar order, wrapping around.
export function stepProject(d) {
  const list = uniqueProjects(projectGroups());
  if (!list.length) return;
  const i = list.findIndex((p) => projectKey(p) === projectKey(state.project));
  return selectProject(list[(((i + d) % list.length) + list.length) % list.length]);
}
// Desktop: the top bar's project name opens a dropdown of every project.
// Phones use the drawer instead.
export function setPicker(open) {
  const list = $("#project-picker");
  list.hidden = !open;
  $("#project-switch").setAttribute("aria-expanded", String(open));
  if (!open) return;
  const item = (p) => {
    const cur = projectKey(state.project) === projectKey(p);
    const sub = projectSubtitle(p);
    return h("button", {
      type: "button", role: "menuitem", title: [p.name, sub, p.path].filter(Boolean).join("\n"), "aria-current": cur ? "true" : "false",
      "aria-label": [p.name, sub, baseBranch(p)].filter(Boolean).join(", "),
      onclick: attempt(async () => { setPicker(false); if (!cur) await selectProject(p); }),
    }, projectLabel(h("span", { class: "project-name" }, projectDot(p), p.name, sharedMark(p)), sub, "picker-text"),
    runnerBadge(p) ? badgeEl(runnerBadge(p), "project-branch") : h("span", { class: "project-branch" }, baseBranch(p)));
  };
  const items = withHeadings(projectGroups(), (g) => h("div", { class: "side-machine", "data-group": g.key }, groupHead(g)), item);
  list.replaceChildren(...(state.projects.length ? items : [h("div", { class: "machine-empty" }, "No projects yet")]),
    h("button", { type: "button", role: "menuitem", class: "picker-new", onclick: () => { setPicker(false); openNewProject(); } }, "+ Add project"));
  ($("button[aria-current=true]", list) || $("button", list))?.focus();
}
$("#project-switch").addEventListener("click", (e) => {
  if (!desktop.matches) return openProjects();
  e.stopPropagation();
  setPicker($("#project-picker").hidden);
});
document.addEventListener("click", (e) => { if (!$("#project-picker").hidden && !e.target.closest(".project-picker")) setPicker(false); });
$("#project-picker").addEventListener("keydown", (e) => {
  const btns = $$("#project-picker button");
  const i = btns.indexOf(document.activeElement);
  const step = { ArrowDown: 1, j: 1, ArrowUp: -1, k: -1 }[e.key];
  if (step) btns[(i + step + btns.length) % btns.length].focus();
  else if (e.key === "Escape" || e.key === "q") { setPicker(false); $("#project-switch").focus(); }
  else return;
  e.preventDefault(); e.stopPropagation();
});
$("#menu-open").addEventListener("click", () => desktop.matches ? setSideCollapsed(!sideCollapsed()) : openProjects());
$("#menu-close").addEventListener("click", () => { setMenu(false); $("#menu-open").focus(); });
$("#menu-scrim").addEventListener("click", () => setMenu(false));
desktop.addEventListener("change", () => { setMenu(false); $("#project-picker").hidden = true; renderProjectBranch(); });
$("#side-new-project").addEventListener("click", () => openNewProject());
$("#pm-settings").addEventListener("click", openSettings);
$("#push-main").addEventListener("click", attempt(pushDone));
$("#pm-cleanup").addEventListener("click", attempt(cleanup));
$("#pm-delete").addEventListener("click", attempt(openDeleteProject));
// The account menu shows who is logged in, the org they work in and the
// others they can switch to.
loadMe().then(() => loadBilling()).catch(() => {});
loadRunners().catch(() => {});
$("#account-open").addEventListener("click", attempt(() => openAccount()));
$("#org-open").addEventListener("click", attempt(openOrg));
$("#runners-open").addEventListener("click", attempt(openRunners));
$("#settings-runners-manage").addEventListener("click", attempt(openRunners));
$("#token-create").addEventListener("click", attempt(createToken));
$("#org-invite-send").addEventListener("click", attempt(sendInvite));
$("#org-new").addEventListener("click", attempt(newOrg));
$("#org-require-2fa").addEventListener("change", attempt(setRequire2FA));
$("#org-slug-save").addEventListener("click", attempt(renameOrg));
$("#org-slug").addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); attempt(renameOrg)(); } });
for (const menu of $$(".foot-menu")) {
  menu.querySelector(".foot-menu-list").addEventListener("click", () => { menu.open = false; });
  document.addEventListener("click", (e) => { if (!menu.contains(e.target)) menu.open = false; });
  menu.addEventListener("keydown", (e) => {
    if (e.key !== "Escape" || !menu.open) return;
    e.stopPropagation(); menu.open = false; menu.querySelector("summary").focus();
  });
}
$("#new-project").addEventListener("submit", (e) => {
  if (e.submitter?.value === "cancel") return;
  e.preventDefault();
  startAdd();
});
