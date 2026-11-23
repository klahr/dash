// The PROJECTS list's groups, kept free of the DOM so they can be tested
// with Node (web/groups_test.mjs).
//
// The list is a project picker: each project is in it once, by name, in a
// place that doesn't move when runners come online, go offline, or gain or
// lose the labels a project runs on. On a hub the org's shared projects go under "Shared",
// and a runner's own (local-only) projects under "On <runner>". Elsewhere (a
// Dash that isn't a hub) there is one group per computer. Headings show only
// when there is more than one group. Runners themselves live on the Runners
// page; a project row only gets a badge (runnerBadge) when its runners need
// attention.

const byName = (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.name.localeCompare(b.name);
const byStr = (a, b) => byName({ name: a }, { name: b });

// isShared reports whether p is the hub's project rather than a runner's own:
// hub projects are the ones not reached through a runner's /peer/ prefix.
export const isShared = (p) => !!p && !p.prefix;

// projectGroups returns [{ key, name, projects }], each project in exactly
// one group. machines is GET /api/machines; runners is GET /api/runners'
// list on a hub, or null; projects carry machine and prefix.
export function projectGroups({ machines = [], runners = null, projects = [] }) {
  const groups = [];
  const on = (name, ps) => { if (ps.length) groups.push({ key: `on:${name}`, name: `On ${name}`, projects: [...ps].sort(byName) }); };
  if (!Array.isArray(runners)) {
    for (const m of machines) on(m.name, projects.filter((p) => p.machine === m.name));
    return groups;
  }
  const shared = projects.filter(isShared);
  if (shared.length) groups.push({ key: "shared", name: "Shared", projects: shared.sort(byName) });
  const own = projects.filter((p) => !isShared(p));
  for (const name of [...new Set(own.map((p) => p.machine))].sort(byStr)) on(name, own.filter((p) => p.machine === name));
  return groups;
}

// starKey names p for its star (the server's starred projects): a project
// of the Dash serving the page by its id, a runner's own by "<runner>/<id>".
// Not projectKey's "<machine>/<id>": this computer's name is its host name,
// which changes when it moves, and a star should stay.
export const starKey = (p) => (!p ? "" : p.prefix ? `${p.machine}/${p.id}` : p.id);

// starSections splits the PROJECTS list's groups (projectGroups) by the
// keys in starred (a Set of starKeys): { starred, others, count }, starred
// sorted by name, others the groups without them (empty ones dropped) and
// count the projects in others. It returns null when no project is starred
// (stars of projects that are gone count for nothing): the list is just
// the groups then.
export function starSections(groups, starred) {
  const all = uniqueProjects(groups);
  const on = all.filter((p) => starred?.has(starKey(p)));
  if (!on.length) return null;
  const others = groups.map((g) => ({ ...g, projects: g.projects.filter((p) => !on.includes(p)) })).filter((g) => g.projects.length);
  return { starred: [...on].sort(byName), others, count: all.length - on.length };
}

// LABEL_ANY is the label every runner matches without having it: a project
// that runs on it alone runs on any runner of its org (model.LabelAny).
export const LABEL_ANY = "any";
const isAny = (l) => String(l).toLowerCase() === LABEL_ANY;

// DEFAULT_AGENT is the agent a project without one runs on: Claude Code
// (model.DefaultAgent). Each agent's label is its name (model.AgentLabel).
export const DEFAULT_AGENT = "claude";
export const projectAgent = (p) => p?.agent || DEFAULT_AGENT;

// runnerLabels are every label runner r has: its default ones (its system
// and architecture, none for a runner too old to say; then one per agent it
// can run), then those it was given. Never "any", which every runner
// matches anyway. A runner the hub says nothing of agents about runs
// Claude Code, as every runner did.
export const runnerLabels = (r) => [...(r?.default_labels || []), ...(r && !r.agents ? [DEFAULT_AGENT] : []), ...(r?.labels || [])];

// projectLabels are the labels a runner needs to work on hub project p:
// those it runs on (p.runs_on), and its agent's (model.Project.RunnerLabels).
// None when it runs on no labels: then it runs on no runner.
export function projectLabels(p) {
  const want = [...(p?.runs_on || [])];
  if (want.length && !want.some((l) => String(l).toLowerCase() === projectAgent(p))) want.push(projectAgent(p));
  return want;
}

// servesProject reports whether runner r works on hub project p: it has every
// label p runs on, and its agent's (projectLabels), whatever their case, as on
// GitHub; every runner matches "any". A project that runs on no labels runs
// on no runner.
export function servesProject(r, p) {
  const want = projectLabels(p);
  if (!r || !want.length) return false;
  const have = new Set(runnerLabels(r).map((l) => l.toLowerCase()));
  return want.every((l) => isAny(l) || have.has(String(l).toLowerCase()));
}

// missingLabels are the labels p needs that runner r lacks (never "any").
export function missingLabels(r, p) {
  const have = new Set(runnerLabels(r).map((l) => l.toLowerCase()));
  return projectLabels(p).filter((l) => !isAny(l) && !have.has(String(l).toLowerCase()));
}

// agentRunners says which of runners can run agent name now: those online
// with its label (ready), and why each other online one can't (why, one
// line per runner). The project settings warn when ready is empty.
export function agentRunners(runners, name) {
  const want = String(name || DEFAULT_AGENT).toLowerCase();
  const ready = [], why = [];
  for (const r of runners || []) {
    if (!r.online) continue;
    if (runnerLabels(r).some((l) => String(l).toLowerCase() === want)) { ready.push(r); continue; }
    const a = runnerAgent(r, want);
    why.push(`${r.name}: ${!a ? "doesn't have it" : !a.installed ? "not installed" : !a.logged_in ? "not logged in" : "not ready"}`);
  }
  return { ready, why };
}

// runnerAgent is what runner r reported of agent name (null: nothing; a
// runner the hub says nothing of agents about may still run Claude Code).
export const runnerAgent = (r, name) => (r?.agents || []).find((a) => a.name === name) || null;

// parseLabels reads labels typed in one box, split by commas or spaces.
export const parseLabels = (s) => [...new Set(String(s || "").split(/[\s,]+/).map((l) => l.trim()).filter(Boolean))];

// uniqueProjects lists the groups' projects in list order (J/K's order).
export const uniqueProjects = (groups) => [...new Set(groups.flatMap((g) => g.projects))];

// runnerOnline reports whether the runner called name is online: from its
// computer's status when the user sees it (platform admins), else from the
// org's runners list.
function runnerOnline(name, machines, runners) {
  const m = machines.find((x) => x.runner && x.name === name);
  return m ? !!m.ok : !!runners?.find((x) => x.name === name)?.online;
}

// projectRunners lists the runners p has a clone on, by name, each with
// whether it is online: a shared project's runners (those with the labels
// it runs on), else its own runner (or computer).
export function projectRunners(p, { machines = [], runners = null } = {}) {
  if (!p) return [];
  if (isShared(p) && Array.isArray(runners)) {
    return runners.filter((r) => servesProject(r, p)).map((r) => r.name).sort(byStr)
      .map((name) => ({ name, online: runnerOnline(name, machines, runners) }));
  }
  if (!p.machine) return [];
  const m = machines.find((x) => x.name === p.machine);
  return [{ name: p.machine, online: m ? !!m.ok : runnerOnline(p.machine, machines, runners) }];
}

// runnerBadge is a project row's runner badge, or null when every runner
// it has is online: { kind, label, title }, kind one of "none" (no runner
// has its labels), "offline" (none online) or "partial" (some online). title lists
// the runners and their status, one per line.
export function runnerBadge(p, ctx = {}) {
  const rs = projectRunners(p, ctx);
  if (!rs.length) {
    if (!isShared(p) || !Array.isArray(ctx.runners)) return null;
    return { kind: "none", label: "needs a runner", title: `No runner has the labels it runs on${p.runs_on?.length ? ` (${projectLabels(p).join(", ")})` : ""}: give one those labels in Runners, or change them in Project settings → Runners` };
  }
  const up = rs.filter((r) => r.online).length;
  if (up === rs.length) return null;
  const title = rs.map((r) => `${r.name}: ${r.online ? "online" : "offline"}`).join("\n");
  if (!up) return { kind: "offline", label: rs.length === 1 ? "runner offline" : "all runners offline", title };
  return { kind: "partial", label: `${up}/${rs.length} online`, title };
}

// projectRunnerNames names the runners p has a clone on: its own runner, or
// the runners with the labels a hub project runs on (by name, in name order).
export function projectRunnerNames(p, runners) {
  if (!p) return [];
  if (!isShared(p) || !Array.isArray(runners)) return [p.machine].filter(Boolean);
  return runners.filter((r) => servesProject(r, p)).map((r) => r.name).sort((a, b) => byName({ name: a }, { name: b }));
}

// remoteRepo is the repository a remote names, as owner/repo: https, ssh://
// and scp-like git@host:owner/repo.git, on GitHub or any other host (a
// GitLab subgroup keeps its full path), without .git. It is "" for a local
// path or no remote.
export function remoteRepo(remote) {
  const r = String(remote || "").trim().replace(/\/+$/, "").replace(/\.git$/i, "");
  if (/^file:/i.test(r)) return "";
  // scheme://[user[:pass]@]host[:port]/path, or scp-like [user@]host:path
  // (a host of two or more letters, so C:\ isn't one).
  const m = /^[a-z][a-z\d+.-]*:\/\/[^/]+\/(.+)$/i.exec(r) || /^(?:[^@/\s]+@)?[^@/:\\\s]{2,}:(?!\/)(.+)$/.exec(r);
  return m ? m[1].split("/").filter(Boolean).join("/") : "";
}

// projectSubtitle is the line under a project's name in project lists: its
// repository as owner/repo, even when the repo is called like the project
// (the owner tells something). Without a hosted remote it is the project's
// folder, unless that is just its name again ("").
export function projectSubtitle(p) {
  if (!p) return "";
  const repo = remoteRepo(p.remote_url);
  if (repo) return repo;
  const dir = String(p.remote_url || p.path || "").trim().replace(/[\\/]+$/, "").split(/[\\/]/).pop().replace(/\.git$/i, "");
  return dir && dir.toLowerCase() !== String(p.name || "").trim().toLowerCase() ? dir : "";
}
