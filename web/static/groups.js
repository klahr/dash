// The PROJECTS list's groups, kept free of the DOM so they can be tested
// with Node (web/groups_test.mjs).
//
// On a hub there is one group per runner, holding every project that runner
// has a clone of: its own projects and the hub's projects linked to it. A hub
// project linked to several runners is in each of their groups (the same
// object, so selecting it highlights every entry). Hub projects linked to no
// runner go last, under "Needs a runner". A runner with no projects shows
// only while it is online, so new projects can still be added to it.
//
// Elsewhere (a Dash that isn't a hub) there is one group per computer.

const byName = (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.name.localeCompare(b.name);

// isShared reports whether p is the hub's project rather than a runner's own:
// hub projects are the ones not reached through a runner's /peer/ prefix.
export const isShared = (p) => !!p && !p.prefix;

// projectGroups returns [{ key, name, machine?, needsRunner?, title, projects }].
// machines is GET /api/machines (with ok as loaded); runners is GET
// /api/runners' list on a hub, or null; projects carry machine and prefix.
export function projectGroups({ machines = [], runners = null, projects = [] }) {
  if (!Array.isArray(runners)) {
    return machines.map((m) => ({
      key: `machine:${m.name}`, name: m.name, machine: m, title: m.ok ? "" : m.error || "unreachable",
      projects: projects.filter((p) => p.machine === m.name).sort(byName),
    }));
  }
  const shared = projects.filter(isShared);
  // Platform admins see every runner in machines; everyone sees their
  // org's in runners. Either may know a runner the other doesn't.
  const names = [...new Set([...machines.filter((m) => m.runner).map((m) => m.name), ...runners.map((r) => r.name)])]
    .sort((a, b) => byName({ name: a }, { name: b }));
  const groups = [];
  for (const name of names) {
    const m = machines.find((x) => x.runner && x.name === name);
    const r = runners.find((x) => x.name === name);
    const ok = m ? !!m.ok : !!r?.online;
    const own = projects.filter((p) => !isShared(p) && p.machine === name);
    const linked = r ? shared.filter((p) => p.runners?.includes(r.id)) : [];
    if (!ok && !own.length && !linked.length) continue;
    groups.push({
      key: `runner:${name}`, name, machine: { ...m, name, ok }, title: ok ? "" : m?.error || "offline",
      projects: [...own, ...linked].sort(byName),
    });
  }
  const placed = new Set(groups.flatMap((g) => g.projects));
  const waiting = shared.filter((p) => !placed.has(p)).sort(byName);
  if (waiting.length) {
    groups.push({
      key: "needs-runner", name: "Needs a runner", needsRunner: true,
      title: "Shared projects no runner is linked to: link one in Project settings → Runners", projects: waiting,
    });
  }
  return groups;
}

// uniqueProjects lists the groups' projects once each, in list order (a
// shared project shown under several runners counts once).
export const uniqueProjects = (groups) => [...new Set(groups.flatMap((g) => g.projects))];

// projectRunnerNames names the runners p has a clone on: its own runner, or
// the runners a hub project is linked to (by name, in name order).
export function projectRunnerNames(p, runners) {
  if (!p) return [];
  if (!isShared(p) || !Array.isArray(runners)) return [p.machine].filter(Boolean);
  return runners.filter((r) => p.runners?.includes(r.id)).map((r) => r.name).sort((a, b) => byName({ name: a }, { name: b }));
}
