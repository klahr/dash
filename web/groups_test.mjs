// Tests for the PROJECTS list's groups (web/static/groups.js). Run with
// `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { projectGroups, uniqueProjects, projectRunnerNames, remoteRepo, projectSubtitle } from "./static/groups.js";

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
const projects = [site, orphan, tools, api];
const groups = projectGroups({ machines, runners, projects });
const byName = Object.fromEntries(groups.map((g) => [g.name, g]));

test("one heading per runner, alphabetically, then Needs a runner", () => {
  assert.deepEqual(groups.map((g) => g.name), ["alpha", "beta", "idle", "Needs a runner"]);
  assert.deepEqual(groups.map((g) => g.key), ["runner:alpha", "runner:beta", "runner:idle", "needs-runner"]);
});

test("a shared project linked to two runners is under both, as the same project", () => {
  assert.deepEqual(byName.alpha.projects.map((p) => p.name), ["Api", "site", "tools"]);
  assert.deepEqual(byName.beta.projects.map((p) => p.name), ["site"]);
  assert.equal(byName.alpha.projects[1], byName.beta.projects[0]);
  // Stepping through the list (J/K) visits it once.
  assert.deepEqual(uniqueProjects(groups).map((p) => p.name), ["Api", "site", "tools", "orphan"]);
  assert.deepEqual(projectRunnerNames(site, runners), ["alpha", "beta"]);
});

test("a shared project linked to no runner is under Needs a runner", () => {
  assert.ok(byName["Needs a runner"].needsRunner);
  assert.deepEqual(byName["Needs a runner"].projects, [orphan]);
  assert.ok(!groups.slice(0, -1).some((g) => g.projects.includes(orphan)));
});

test("an offline runner without projects is hidden, an online one shows", () => {
  assert.equal(byName.gone, undefined);
  assert.deepEqual(byName.idle.projects, []);
  assert.equal(byName.idle.machine.ok, true);
});

test("an offline runner with projects shows, marked offline", () => {
  const gs = projectGroups({ machines, runners, projects: [shared("p5", "docs", ["r-gone"])] });
  const g = gs.find((x) => x.name === "gone");
  assert.equal(g.machine.ok, false);
  assert.equal(g.title, "offline");
  assert.equal(gs.find((x) => x.needsRunner), undefined); // no empty Needs a runner group
});

test("members who don't see the runners' computers still get their org's runners", () => {
  const gs = projectGroups({ machines: [hub], runners, projects: [site] });
  assert.deepEqual(gs.map((g) => [g.name, g.machine.ok, g.projects.length]), [["alpha", true, 1], ["beta", true, 1], ["idle", true, 0]]);
});

test("without runners (not a hub), one group per computer", () => {
  const gs = projectGroups({ machines: [hub], runners: null, projects: [shared("p6", "b"), shared("p7", "a")] });
  assert.deepEqual(gs.map((g) => [g.name, g.projects.map((p) => p.name)]), [["hub", ["a", "b"]]]);
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
