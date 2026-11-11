import { projectGroups as groupProjects, isShared, projectRunnerNames, projectSubtitle, uniqueProjects } from "../groups.js";
import { $, $$, h, ico, setLabel } from "./dom.js";
import { desktop } from "./viewport.js";
import { api, deepLink, on } from "./api.js";
import { attempt, busy, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { showView } from "./nav.js";
import { baseBranch, eventFor, latest, multi, projectKey, projectStatus, refreshLive } from "./events.js";
import { loadTodos, pushDone, renderPushMain } from "./board.js";
import { agoText, renderBoard } from "./branches.js";
import { closeDetail, openDetail } from "./detail.js";
import { openPrefs, syncPrefs } from "./prefs.js";
import { loadRunners, openRunners, startRunner } from "./runners.js";
import { loadMe, openGoogle, openOrg, openTwoFactor, renameOrg, sendInvite, setRequire2FA } from "./account.js";
import { loadBilling, newOrg } from "./billing.js";
import { createToken, openTokens } from "./tokens.js";
import { openConnector } from "./wallview.js";
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
  const saved = (() => { try { return localStorage.getItem("dash.project"); } catch { return null; } })();
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
  try { if (p) localStorage.setItem("dash.project", projectKey(p)); } catch {}
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

// openNewProject opens the New project sheet. Every project belongs to a
// runner: the sheet picks one first (the hub keeps no projects itself).
export function openNewProject() {
  const f = $("#new-project");
  if (!isHub()) {
    fail(new Error("Projects are made from the hub's dashboard: New project there, on this runner."));
    return;
  }
  const runners = projectRunners();
  if (!runners.length) {
    fail(new Error(!state.runners.length ? `A runner is required: this dashboard keeps no projects itself. To add one, ${startRunner}.`
      : state.runners.some((r) => r.online) ? "None of the online runners is yours, and only a runner's owner or an admin of the organization can link projects to it. Start your own with `dash runner`."
        : noRunnerOnline));
    return;
  }
  const pick = defaultRunner(runners);
  f.machine.replaceChildren(...runners.map((r) => h("option", { value: r.name, selected: r.name === pick },
    `${r.name}${r.mine ? " (yours)" : ""}`)));
  setHubMode("gh");
  $("#new-project-sheet").showModal();
}

// ---- New project: on a runner, never on the hub. A shared project is a git
// remote its runners clone: the repository comes from the user's GitHub
// repositories (listed with the picked runner's gh), a new one (created with
// that gh), or a pasted URL, and the runner it is linked to clones it into
// <projects>/<repo>, or adopts a clone of the same remote already there.
// A local-only project is the runner's own: a new repository in its projects
// folder (git init and an initial commit), with no remote, for throwaway
// work; deleting it deletes that folder too.
const hubMode = () => $("#new-project-hub-mode [aria-selected=true]")?.dataset.mode || "gh";
const ghState = { cache: new Map(), pick: null, loading: "" };
// The runner picked in the sheet, from state.runners.
function sheetRunner() {
  const name = $("#new-project").machine.value;
  return state.runners?.find((r) => r.name === name) || null;
}

function setHubMode(mode) {
  const f = $("#new-project");
  for (const b of $$("#new-project-hub-mode button")) b.setAttribute("aria-selected", String(b.dataset.mode === mode));
  $("#new-project-title").textContent = "New project";
  // gh and url add a repository that already exists; create and local make one.
  $("#new-project-submit").textContent = { gh: "Add project", url: "Add project", create: "Create repository and project" }[mode] || "Create project";
  $("#new-project-gh").hidden = mode !== "gh";
  $("#new-project-url-row").hidden = mode !== "url";
  f.remote_url.required = mode === "url";
  $("#new-project-github-row").hidden = mode !== "create";
  f.github.required = mode === "create";
  f.github.placeholder = "name, or owner/name for an organization";
  $("#new-project-public-row").hidden = mode !== "create";
  $("#new-project-hub-hint").hidden = mode === "local";
  $("#new-project-clone-row").hidden = mode === "local";
  $("#new-project-local-hint").hidden = mode !== "local";
  f.name.required = mode === "local";
  $("#new-project-name-label").replaceChildren(...(mode === "local" ? ["Name"] : ["Name ", h("em", {}, "optional")]));
  syncHubMode();
  if (mode === "gh") loadGitHubRepos().catch(fail);
}
for (const b of $$("#new-project-hub-mode button")) b.addEventListener("click", () => setHubMode(b.dataset.mode));

// syncHubMode updates what depends on the fields: the name's default and
// the GitHub hint.
function syncHubMode() {
  const f = $("#new-project");
  const mode = hubMode(), r = sheetRunner();
  const fromURL = (u) => (u || "").trim().replace(/\/+$/, "").replace(/\.git$/, "").split(/[/:]/).pop() || "";
  f.name.placeholder = mode === "local" ? "e.g. sandbox"
    : mode === "gh" ? ghState.pick?.name || "the repository's name"
      : mode === "create" ? fromURL(f.github.value) || "the repository's name"
        : fromURL(f.remote_url.value) || "the repository's name";
  const hint = $("#new-project-github-hint");
  hint.hidden = mode !== "create";
  hint.textContent = r && !r.mine ? `Only ${r.name}'s owner can create repositories with its GitHub login. Pick one of your runners, or paste a remote URL.`
    : `A new empty ${f.github_public.checked ? "public" : "private"} repository is created on GitHub with ${r?.name || "the runner"}'s gh, then cloned there.`;
  $("#new-project-local-where").textContent = r?.name || "the runner";
}
$("#new-project").remote_url.addEventListener("input", syncHubMode);
$("#new-project").github_public.addEventListener("change", syncHubMode);
$("#new-project").github.addEventListener("input", syncHubMode);

// The GitHub mark (github.com/logos), inline: no request for an icon.
const GH_MARK = "M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.1.79-.25.79-.56v-2c-3.2.7-3.87-1.37-3.87-1.37-.52-1.33-1.28-1.69-1.28-1.69-1.04-.71.08-.7.08-.7 1.16.08 1.77 1.19 1.77 1.19 1.03 1.76 2.7 1.25 3.36.96.1-.75.4-1.25.73-1.54-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.84 1.19 3.1 0 4.42-2.69 5.4-5.25 5.68.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.56A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5z";
export const ghMark = (cls = "gh-mark") => {
  const t = document.createElement("template");
  t.innerHTML = `<svg class="${cls}" viewBox="0 0 24 24" aria-hidden="true"><path d="${GH_MARK}" fill="currentColor"/></svg>`;
  return t.content.firstChild;
};
// An owner's avatar from GitHub, or the mark when it doesn't load.
export const ghAvatar = (owner) => {
  const img = h("img", { class: "gh-avatar", alt: "", width: 20, height: 20, loading: "lazy", referrerPolicy: "no-referrer",
    src: `https://github.com/${encodeURIComponent(owner)}.png?size=40` });
  img.addEventListener("error", () => img.replaceWith(ghMark("gh-avatar gh-mark")), { once: true });
  return img;
};

// loadGitHubRepos lists the user's repositories with the picked runner's
// gh (once per runner while the page is open).
async function loadGitHubRepos(refresh = false) {
  const r = sheetRunner(), hint = $("#gh-hint");
  ghState.pick = null;
  syncHubMode();
  if (!r) { $("#gh-list").replaceChildren(); hint.textContent = noRunnerOnline; return; }
  if (!r.mine) {
    $("#gh-list").replaceChildren();
    hint.textContent = `Only ${r.name}'s owner can list repositories with its GitHub login. Pick one of your runners, or paste a remote URL.`;
    return;
  }
  if (!refresh && ghState.cache.has(r.id)) { renderGitHubRepos(); return; }
  ghState.loading = r.id;
  $("#gh-list").replaceChildren(h("div", { class: "gh-empty" }, `Asking ${r.name}'s gh for your repositories…`));
  hint.textContent = "";
  try {
    ghState.cache.set(r.id, await api("GET", `/api/runners/${encodeURIComponent(r.id)}/github-repos`, null, ""));
  } catch (e) {
    if (sheetRunner()?.id !== r.id) return;
    $("#gh-list").replaceChildren();
    hint.textContent = `${e.message || e} You can paste a remote URL instead.`;
    return;
  } finally {
    if (ghState.loading === r.id) ghState.loading = "";
  }
  if (sheetRunner()?.id === r.id && hubMode() === "gh") renderGitHubRepos();
}

// renderGitHubRepos shows the cached list, filtered, grouped by owner. A
// repository that already has a project is marked and can't be picked.
const GH_SHOWN = 200;
function renderGitHubRepos() {
  const r = sheetRunner(), res = r && ghState.cache.get(r.id);
  if (!res) return;
  const q = $("#gh-filter").value.trim().toLowerCase();
  const all = res.repos || [];
  const hits = all.filter((x) => !q || x.nameWithOwner.toLowerCase().includes(q) || (x.description || "").toLowerCase().includes(q));
  const rows = [];
  let owner = null;
  for (const x of hits.slice(0, GH_SHOWN)) {
    if (x.owner !== owner) {
      owner = x.owner;
      rows.push(h("div", { class: "gh-owner", role: "presentation" }, ghAvatar(owner), h("span", {}, owner), owner === res.user ? h("em", {}, "you") : null));
    }
    const picked = ghState.pick?.nameWithOwner === x.nameWithOwner;
    const hints = [x.isPrivate && "private", x.isFork && "fork", x.isArchived && "archived", x.updatedAt && `updated ${agoText(x.updatedAt)}`].filter(Boolean);
    rows.push(h("button", {
      type: "button", class: `gh-repo${x.linked ? " linked" : ""}`, role: "option", "aria-selected": String(picked), "aria-disabled": x.linked ? "true" : null,
      title: x.linked ? `Already the project ${x.linked}` : x.url || x.nameWithOwner,
      onclick: () => { if (x.linked) return; ghState.pick = x; renderGitHubRepos(); syncHubMode(); },
    },
    ghMark(),
    h("span", { class: "gh-repo-text" },
      h("span", { class: "gh-repo-title" }, h("span", { class: "gh-repo-owner" }, `${x.owner}/`), h("strong", {}, x.name)),
      x.description ? h("span", { class: "gh-repo-desc" }, x.description) : null,
      h("span", { class: "gh-repo-meta" }, ...hints.map((t) => h("span", {}, t)), x.linked ? h("span", { class: "gh-linked" }, `project: ${x.linked}`) : null))));
  }
  $("#gh-list").replaceChildren(...(rows.length ? rows : [h("div", { class: "gh-empty" }, all.length ? "No repository matches." : "No repositories on GitHub yet: create one instead.")]));
  const more = hits.length - GH_SHOWN;
  $("#gh-hint").textContent = more > 0 ? `${more} more; type to narrow the list.`
    : all.length ? `${all.length} ${all.length === 1 ? "repository" : "repositories"} from ${r.name}'s gh (signed in as ${res.user}).` : "";
}
$("#gh-filter").addEventListener("input", renderGitHubRepos);
$("#gh-filter").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); $(".gh-repo:not(.linked)", $("#gh-list"))?.click(); }
});

// submitHubProject makes the hub's project from the sheet and links it to
// the picked runner, which clones (or adopts) it.
async function submitHubProject(form, body) {
  const r = sheetRunner();
  if (!r) throw new Error(noRunnerOnline);
  const mode = hubMode();
  let remote = "", name = (body.name || "").trim();
  if (mode === "local") return submitLocalProject(form, r, name, body);
  if (mode === "gh") {
    const x = ghState.pick;
    if (!x) throw new Error("Pick a repository from the list.");
    remote = `https://github.com/${x.nameWithOwner}.git`;
    name ||= x.name;
  } else if (mode === "create") {
    if (!r.mine) throw new Error(`Only ${r.name}'s owner can create repositories with its GitHub login.`);
    toast("Creating GitHub repository…");
    const repo = await api("POST", `/api/runners/${encodeURIComponent(r.id)}/github-repos`, { name: body.github.trim(), public: !!body.github_public }, "");
    remote = `https://github.com/${repo.name}.git`;
    name ||= repo.name.split("/").pop();
    ghState.cache.delete(r.id);
  } else {
    remote = (body.remote_url || "").trim();
    name ||= remote.replace(/\/+$/, "").replace(/\.git$/, "").split(/[/:]/).pop();
  }
  toast(`Linking to ${r.name}…`);
  const p = await api("POST", "/api/projects", { name, remote_url: remote, clone_url: (body.clone_url || "").trim(), runners: [r.id],
    run_command: body.run_command, test_command: body.test_command, deploy_command: body.deploy_command }, "");
  form.reset();
  ghState.pick = null;
  await loadProjects();
  await selectProject(state.projects.find((x) => x.id === p.id && !x.prefix));
  $("#new-project-sheet").close();
  toast(`${mode === "create" ? "Created" : "Added"} ${p.name}; ${r.name} clones it into its projects folder`, { tone: "ok" });
}

// submitLocalProject makes a local-only project: the runner r's own, a new
// repository in its projects folder with no remote.
async function submitLocalProject(form, r, name, body) {
  if (!name) throw new Error("Name the project.");
  if (!r.mine) throw new Error(`Only ${r.name}'s owner can make local-only projects on it. Pick one of your runners.`);
  toast(`Creating ${name} on ${r.name}…`);
  const p = await api("POST", `/api/runners/${encodeURIComponent(r.id)}/projects`, { name,
    run_command: body.run_command, test_command: body.test_command, deploy_command: body.deploy_command }, "");
  form.reset();
  await loadProjects();
  const made = state.projects.find((x) => x.id === p.id && x.machine === r.name);
  if (made) await selectProject(made);
  $("#new-project-sheet").close();
  // Only platform admins see runners' own projects in this dashboard.
  toast(made ? `Created ${p.name} on ${r.name}, local only`
    : `Created ${p.name} on ${r.name}, local only: open it from ${r.name}'s own dashboard`, { tone: "ok" });
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
$("#new-project").machine.addEventListener("change", () => { syncHubMode(); if (hubMode() === "gh") loadGitHubRepos().catch(fail); });

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

// Desktop sidebar / phone menu: every project, one click away. The current one
// shows its branch; its settings and actions live in the top bar's project menu.
export function renderSideProjects() {
  const item = (p, inGroup) => {
    const cur = projectKey(state.project) === projectKey(p);
    const st = projectStatus(p);
    const stLabel = st === "running" ? "Claude is working" : st === "review" ? "Ready for review" : "";
    const icon = projectIcon(p);
    if (st) icon.append(h("span", { class: `side-project-status ${st}` }));
    const sub = projectSubtitle(p);
    return h("li", { class: [cur && "current", inGroup && "in-group"].filter(Boolean).join(" ") },
      h("button", {
        type: "button", class: "side-project", title: [p.name, sub, p.path, stLabel].filter(Boolean).join("\n"), "aria-current": cur ? "true" : "false",
        "aria-label": [p.name, sub, stLabel.toLowerCase()].filter(Boolean).join(", "),
        onclick: attempt(async () => { await selectProject(p); setMenu(false); }),
      },
      icon,
      projectLabel(h("span", { class: "side-project-name" }, p.name), sub, "side-project-text"),
      sharedMark(p),
      needsRunner(p) ? h("span", { class: "side-project-branch warn", title: "No runner is linked to it: link one in Project settings → Runners" }, "needs a runner")
        : cur && baseBranch(p) ? h("span", { class: "side-project-branch" }, baseBranch(p)) : null));
  };
  // Grouped, each runner heads its projects, which hang from it on a guide line.
  const items = !grouped() ? state.projects.map((p) => item(p)) : projectGroups().flatMap((g) => [
    h("li", { class: `side-machine${g.needsRunner ? " needs-runner" : ""}`, title: groupTitle(g), "data-group": g.key }, groupHead(g)),
    ...g.projects.map((p) => item(p, true)),
  ]);
  $("#side-project-list").replaceChildren(...(items.length ? items : [h("li", { class: "machine-empty" }, "No projects yet")]));
}

// projectGroups are the PROJECTS list's groups (see groups.js): on a hub,
// one per runner with every project it has a clone of, then the shared
// projects that need a runner. A shared project can be in several groups.
function projectGroups() { return groupProjects({ machines: state.machines || [], runners: state.runners ?? null, projects: state.projects || [] }); }
// The list is grouped on a hub (by runner) or with several computers.
export function grouped() { return isHub() || multi(); }
// A group's heading: the runner's online dot, its name, and how many
// projects it holds, so it reads as the owner of the projects under it.
const groupHead = (g) => [
  g.machine ? h("span", { class: `machine-dot ${g.machine.ok ? "ok" : "down"}`, "aria-hidden": "true" }) : null,
  h("span", { class: "side-machine-name" }, g.name),
  h("span", { class: "side-machine-count", "aria-label": `${g.projects.length} project${g.projects.length === 1 ? "" : "s"}` }, String(g.projects.length))];
// A group heading's tooltip: why it is offline or what it holds.
const groupTitle = (g) => [g.machine && `Projects on ${g.name}`, g.title].filter(Boolean).join("\n");
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
  try { localStorage.setItem("dash.sideCollapsed", on ? "1" : "0"); } catch {}
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
try { $(".app").classList.toggle("side-collapsed", localStorage.getItem("dash.sideCollapsed") === "1"); } catch {}

// Desktop: drag the sidebar's right edge to resize it, like the chat pane. The
// width survives collapsing; CSS also caps it at 40vw so main stays usable.
const SIDE_MIN_W = 180, SIDE_MAX_W = 480;
function setSideWidth(w, save = true) {
  w = w == null ? null : Math.min(SIDE_MAX_W, Math.max(SIDE_MIN_W, Math.round(w)));
  if (w) $(".app").style.setProperty("--side-w-set", `${w}px`);
  else $(".app").style.removeProperty("--side-w-set");
  if (save) try { w ? localStorage.setItem("dash.sideW", String(w)) : localStorage.removeItem("dash.sideW"); } catch {}
}
try { setSideWidth(Number(localStorage.getItem("dash.sideW")) || null, false); } catch {}
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
  // A shared project listed under several runners is one stop.
  const list = !grouped() ? state.projects : uniqueProjects(projectGroups());
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
    h("span", { class: "project-branch" }, baseBranch(p)));
  };
  const items = !grouped() ? state.projects.map(item) : projectGroups().flatMap((g) => [
    h("div", { class: `side-machine${g.needsRunner ? " needs-runner" : ""}`, title: groupTitle(g), "data-group": g.key }, groupHead(g)),
    ...g.projects.map(item)]);
  list.replaceChildren(...(state.projects.length ? items : [h("div", { class: "machine-empty" }, "No projects yet")]),
    h("button", { type: "button", role: "menuitem", class: "picker-new", onclick: () => { setPicker(false); openNewProject(); } }, "+ New project"));
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
$("#prefs-open").addEventListener("click", openPrefs);
$("#connector-open").addEventListener("click", attempt(openConnector));
$("#pm-settings").addEventListener("click", openSettings);
$("#push-main").addEventListener("click", attempt(pushDone));
$("#pm-cleanup").addEventListener("click", attempt(cleanup));
$("#pm-delete").addEventListener("click", attempt(openDeleteProject));
// The account menu shows who is logged in, the org they work in and the
// others they can switch to.
loadMe().then(() => loadBilling()).catch(() => {});
loadRunners().catch(() => {});
$("#org-open").addEventListener("click", attempt(openOrg));
$("#runners-open").addEventListener("click", attempt(openRunners));
$("#settings-runners-manage").addEventListener("click", attempt(openRunners));
$("#tokens-open").addEventListener("click", attempt(openTokens));
$("#token-create").addEventListener("click", attempt(createToken));
$("#org-invite-send").addEventListener("click", attempt(sendInvite));
$("#org-new").addEventListener("click", attempt(newOrg));
$("#twofactor-open").addEventListener("click", attempt(openTwoFactor));
$("#google-open").addEventListener("click", attempt(openGoogle));
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
$("#new-project").addEventListener("submit", attempt(async (e) => {
  if (e.submitter?.value === "cancel") return;
  e.preventDefault();
  const { machine, ...body } = Object.fromEntries(new FormData(e.target));
  await submitHubProject(e.target, body);
}));
