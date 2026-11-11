// Tests for the PROJECTS list's groups (web/static/groups.js). Run with
// `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { projectGroups, uniqueProjects, projectRunnerNames, projectRunners, runnerBadge, runnersSummary, remoteRepo, projectSubtitle } from "./static/groups.js";

const hub = { name: "hub", local: true, ok: true, prefix: "" };
const runner = (name, ok) => ({ name, runner: true, ok, prefix: `/peer/${name}`, error: ok ? "" : "offline" });
const shared = (id, name, runners = []) => ({ id, name, machine: "hub", prefix: "", runners });
const own = (id, name, machine) => ({ id, name, machine, prefix: `/peer/${machine}` });

const machines = [hub, runner("beta", true), runner("alpha", true), runner("gone", false), runner("idle", true)];
const runners = [
  { id: "r-alpha", name: "alpha", online: true },
  { id: "r-beta", name: "beta", online: true },
  { id: "r-gone", name: "gone", online: false },
  { id: "r-idle", name: "idle", online: true },
];
const site = shared("p1", "site", ["r-alpha", "r-beta"]);
const orphan = shared("p2", "orphan");
const tools = own("p3", "tools", "alpha");
const api = own("p4", "Api", "alpha");
const notes = own("p5", "notes", "beta");
const projects = [site, orphan, tools, api, notes];
const groups = projectGroups({ machines, runners, projects });
const names = (gs) => gs.map((g) => [g.name, g.projects.map((p) => p.name)]);

test("Shared first, then one On <runner> group per runner with local-only projects", () => {
  assert.deepEqual(names(groups), [["Shared", ["orphan", "site"]], ["On alpha", ["Api", "tools"]], ["On beta", ["notes"]]]);
  assert.deepEqual(groups.map((g) => g.key), ["shared", "on:alpha", "on:beta"]);
});

test("a shared project linked to two runners is listed once", () => {
  const all = groups.flatMap((g) => g.projects);
  assert.equal(all.filter((p) => p === site).length, 1);
  assert.deepEqual(uniqueProjects(groups).map((p) => p.name), ["orphan", "site", "Api", "tools", "notes"]);
  assert.deepEqual(projectRunnerNames(site, runners), ["alpha", "beta"]);
});

test("only shared projects: a single group (the list shows no headings)", () => {
  const gs = projectGroups({ machines, runners, projects: [site, orphan] });
  assert.equal(gs.length, 1);
  assert.equal(gs[0].key, "shared");
  assert.deepEqual(projectGroups({ machines, runners, projects: [] }), []);
});

test("members who don't see the runners' computers still get the shared group", () => {
  assert.deepEqual(names(projectGroups({ machines: [hub], runners, projects: [site] })), [["Shared", ["site"]]]);
});

test("without runners (not a hub), one group per computer with projects", () => {
  const gs = projectGroups({ machines: [hub, runner("alpha", true)], runners: null, projects: [shared("p6", "b"), shared("p7", "a")] });
  assert.deepEqual(names(gs), [["On hub", ["a", "b"]]]);
});

test("order doesn't change when runners go offline, are linked or unlinked", () => {
  const order = (ms, rs, ps) => uniqueProjects(projectGroups({ machines: ms, runners: rs, projects: ps })).map((p) => p.id);
  const before = order(machines, runners, projects);
  const down = machines.map((m) => m.runner ? { ...m, ok: false } : m);
  const off = runners.map((r) => ({ ...r, online: false }));
  assert.deepEqual(order(down, off, projects), before);
  const relinked = projects.map((p) => p === site ? { ...p, runners: ["r-gone"] } : p === orphan ? { ...p, runners: ["r-idle"] } : p);
  assert.deepEqual(order(machines, runners, relinked), before);
});

test("runner badge: none when every linked runner is online", () => {
  assert.equal(runnerBadge(site, { machines, runners }), null);
  assert.equal(runnerBadge(tools, { machines, runners }), null);
  assert.equal(runnerBadge(shared("p8", "solo", ["r-idle"]), { machines, runners }), null);
});

test("runner badge: needs a runner when none is linked", () => {
  const b = runnerBadge(orphan, { machines, runners });
  assert.equal(b.kind, "none");
  assert.equal(b.label, "needs a runner");
});

test("runner badge: offline when no linked runner is online", () => {
  assert.deepEqual(runnerBadge(shared("p9", "docs", ["r-gone"]), { machines, runners }),
    { kind: "offline", label: "runner offline", title: "gone: offline" });
  const off = machines.map((m) => m.name === "alpha" ? { ...m, ok: false } : m.name === "beta" ? { ...m, ok: false } : m);
  const b = runnerBadge(site, { machines: off, runners });
  assert.equal(b.kind, "offline");
  assert.equal(b.label, "all runners offline");
  assert.equal(b.title, "alpha: offline\nbeta: offline");
});

test("runner badge: partial when some linked runners are online; the tooltip lists them", () => {
  const b = runnerBadge(shared("p10", "web", ["r-gone", "r-alpha"]), { machines, runners });
  assert.deepEqual(b, { kind: "partial", label: "1/2 online", title: "alpha: online\ngone: offline" });
});

test("runner status comes from the org's runners when the computers aren't visible", () => {
  assert.deepEqual(projectRunners(site, { machines: [hub], runners }), [{ name: "alpha", online: true }, { name: "beta", online: true }]);
  assert.equal(runnerBadge(shared("p11", "x", ["r-gone"]), { machines: [hub], runners }).kind, "offline");
});

test("not a hub: no badges", () => {
  assert.equal(runnerBadge(shared("p12", "a"), { machines: [hub], runners: null }), null);
});

test("runners summary: overall dot", () => {
  assert.equal(runnersSummary({ machines, runners: null }), null);
  assert.equal(runnersSummary({ machines, runners: [] }).kind, "empty");
  assert.deepEqual(runnersSummary({ machines, runners }), { kind: "some", online: 3, total: 4, title: "3 of 4 runners online" });
  assert.equal(runnersSummary({ machines: [hub], runners: runners.filter((r) => r.online) }).kind, "ok");
  assert.equal(runnersSummary({ machines: [hub], runners: [runners[2]] }).kind, "down");
});

test("remoteRepo reads owner/repo from https, ssh and other hosts", () => {
  const cases = {
    "https://github.com/klahr/dash": "klahr/dash",
    "https://github.com/klahr/dash.git": "klahr/dash",
    "https://github.com/klahr/dash/": "klahr/dash",
    "https://x-access-token:abc@github.com/klahr/dash.git": "klahr/dash",
    "git@github.com:klahr/dash.git": "klahr/dash",
    "ssh://git@github.com:22/klahr/dash.git": "klahr/dash",
    "git@gitlab.com:group/sub/tool.git": "group/sub/tool",
    "https://codeberg.org/someone/thing": "someone/thing",
    "/home/me/src/dash": "",
    "file:///home/me/src/dash.git": "",
    "C:\\src\\dash": "",
    "../dash": "",
    "": "",
  };
  for (const [remote, want] of Object.entries(cases)) assert.equal(remoteRepo(remote), want, remote);
  assert.equal(remoteRepo(undefined), "");
});

test("projectSubtitle: owner/repo, else the folder unless it is the name", () => {
  assert.equal(projectSubtitle({ name: "Dash", remote_url: "git@github.com:klahr/dash.git", path: "/x/dash" }), "klahr/dash");
  // The owner is worth showing even when the repo is called like the project.
  assert.equal(projectSubtitle({ name: "zonventure", remote_url: "https://github.com/klahr/zonventure" }), "klahr/zonventure");
  assert.equal(projectSubtitle({ name: "Dash", path: "/home/me/src/dash-web" }), "dash-web");
  assert.equal(projectSubtitle({ name: "Dash", path: "/home/me/src/DASH/" }), "");
  assert.equal(projectSubtitle({ name: "Dash", remote_url: "/srv/git/tools.git", path: "/x/dash" }), "tools");
  assert.equal(projectSubtitle({ name: "Dash" }), "");
  assert.equal(projectSubtitle(null), "");
});
