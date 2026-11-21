import { $, docsLink, fill, h, ib, ico, plural } from "./dom.js";
import { api } from "./api.js";
import { art, ask, attempt, fail, reduceMotion, toast } from "./dialogs.js";
import { checked, saveField } from "./autosave.js";
import { changed } from "./routes.js";
import { state } from "./state.js";
import * as store from "./store.js";
import { refreshMachines } from "./events.js";
import { isHub, loadProjects, needsRunner, openNewProject, renderSideProjects, selectProject, setMenu } from "./projects.js";
import { agoText, renderBoard } from "./branches.js";
import { ghRepoHints, ghRepoLabel, ghRepoRows } from "./github.js";
import { copyBlock, installCmd as install, installCmdWin as installWin, psq, shq } from "./install.js";
import { shortLabel } from "./usage.js";
import { openSettings, syncSettingsRunners } from "./settings.js";
import { ensureShell, forgetShellRefusal } from "./terminal.js";
import { loadTry } from "./try.js";
import { loadRunnerTokens } from "./tokens.js";
import { isRelease, runnerOutdated as outdatedFor } from "../runnerversion.js";
import { LABEL_ANY, projectLabels, runnerLabels, servesProject } from "../groups.js";
import { FEATURES, agentTitle, loadAgents } from "./agents.js";
import { docsLabels, labelPicker, refreshLabelPickers } from "./labelpicker.js";

// ------------------------------------------------------------------ runners
// The one-line installer and the version runners should run (from /api/runners).
// canUpdate: runners can update themselves to the hub's release (not from a
// hub built from source).
const runnerInfo = { hub: location.origin, version: "", canUpdate: false };
// The "Add a runner" in progress: the runners there were before its code,
// so the one that registers with it can be told apart.
let runnerAdd = null;

// installCmd and installCmdWin: the installers (app/install.js) from this hub.
const installCmd = (args) => install(runnerInfo.hub, args);
const installCmdWin = (args) => installWin(runnerInfo.hub, args);
// updateCmds shows how to update a runner by hand, on any system.
const updateCmds = () => [
  copyBlock(installCmd(), "Command copied", { pre: true }),
  h("p", { class: "hint" }, "On Windows, in PowerShell:"),
  copyBlock(installCmdWin(), "Command copied", { pre: true }),
];

// Runners run the org's tasks (`dash runner`), each with its owner's
// Claude subscription. They connect out to the hub: no inbound
// access, VPN or shared secret needed on their side. Projects run on runner
// labels; the hub sends their tasks only to runners with those labels.
export async function loadRunners() {
  loadAgents(); // their names and features, once
  try {
    const res = await api("GET", "/api/runners");
    // By name, so every list of them (a project's settings too) keeps one
    // order across refreshes.
    state.runners = [...(res.runners || [])].sort(byName);
    Object.assign(runnerInfo, { hub: res.hub || location.origin, version: res.version || "", canUpdate: !!res.can_update });
  } catch {
    state.runners = null; // this Dash has no runners (it is one, say)
  }
  syncSettingsRunners(); // the open settings show which runners have the project's labels
  refreshLabelPickers(); // the labels they offer, too
  forgetShellRefusal(); // a runner came or went, or now opens terminals: try again
  if (state.view === "run") ensureShell();
  if (state.project) loadTry().catch(() => {}); // which runners Try may use changed
  $("#runners-open").hidden = state.runners === null;
  renderDiskBanner();
  // The board waits for the runners, and shows the welcome screen until there's one.
  // The projects list groups by runner, so it changes with them too.
  if (state.projectsLoaded) { renderBoard(); renderSideProjects(); }
  return state.runners;
}
export const runnerName = (id) => state.runners?.find((r) => r.id === id)?.name || "its runner";

// runnerOS is the operating system r last connected from: its name, logo
// (TECH_ICONS; null for one without) and label with the architecture, or
// null when r hasn't said (it runs a Dash from before it did).
const osNames = { darwin: ["macOS", "apple"], linux: ["Linux", "linux"], windows: ["Windows", "windows11"] };
export function runnerOS(r) {
  if (!r.os) return null;
  const [name, icon] = osNames[r.os] || [r.os, null];
  return { name, icon, label: r.arch ? `${name} (${r.arch})` : name };
}
// osLogo is r's OS logo with its label as tooltip and for screen readers.
const osLogo = (r, cls) => {
  const os = runnerOS(r);
  return os?.icon ? h("span", { class: cls, role: "img", title: os.label, "aria-label": os.label }, ico(os.icon)) : null;
};
// osChip is r's OS logo and name, for its lines of details.
const osChip = (r) => {
  const os = runnerOS(r);
  return os ? h("span", { class: "runner-os", title: `Operating system: ${os.label}` }, os.icon ? ico(os.icon) : null, os.label) : null;
};

// The hub never runs Claude: tasks and scans of p run only on a runner with
// the labels it runs on that does the viewer's work (their own, or a shared
// one: tunnel.Runner.WorksFor). runnerNeeded says why their work would wait
// now, or "" (also on a Dash without runners of its own, a runner, and for
// another runner's project).
export const startRunner = "use Runners → Add a runner";
export const noRunnerOfMine = "You have no runner for this project.";
function runnerNeeded(p) {
  if (!p || p.prefix || !Array.isArray(state.runners)) return "";
  if (!state.runners.length) return `Its work needs a runner. To add one, ${startRunner}.`;
  const linked = state.runners.filter((r) => servesProject(r, p));
  if (!linked.length) return `${p.name} needs a runner: ${p.runs_on?.length ? `none has ${projectLabels(p).join(", ")}` : "it has no labels"}. Its work waits.`;
  const usable = linked.filter(worksForMe);
  if (!usable.length) return `${noRunnerOfMine} Your tasks wait. To add one, ${startRunner}, or ask an admin for a shared runner.`;
  if (!usable.some((r) => r.online)) return "No runner of this project is online. Its work waits.";
  return "";
}
// worksForMe reports whether runner r takes the viewer's tasks: it is
// theirs, or it takes other members' tasks.
const worksForMe = (r) => !!(r.mine || r.takes_others);
// renderRunnerBanner says above the board when the open project's git,
// files and commands can't be reached (no runner with its labels, none of
// the viewer's, or none online), with a way to change that.
export function renderRunnerBanner() {
  const el = $("#runner-banner");
  const p = state.project;
  const why = isHub() && p && !p.prefix && state.runners?.length ? runnerNeeded(p) : "";
  el.hidden = !why;
  if (!why) return el.replaceChildren();
  if (why.startsWith(noRunnerOfMine)) {
    return el.replaceChildren(`${noRunnerOfMine} `,
      h("button", { type: "button", class: "link inline", onclick: attempt(() => openAddRunner()) }, "Add a runner"),
      ", or ask an admin for a shared runner. Your tasks wait until then.");
  }
  el.replaceChildren(why, " ", needsRunner(p)
    ? h("button", { type: "button", class: "link inline", onclick: () => { openSettings(); $("#settings-runners-field")?.scrollIntoView({ block: "center" }); } }, "Change its labels")
    : h("button", { type: "button", class: "link inline", onclick: attempt(() => openRunners()) }, "Runners"));
}

// showRunnerNeeded fills a .runner-note element for p, hidden when nothing is missing.
export function showRunnerNeeded(el, p) {
  const why = runnerNeeded(p);
  el.hidden = !why;
  el.textContent = why;
}
const runnerLimit = (r) => r.parallel || r.reported?.parallel || 1;
// runnerBoost is how many more tasks than its limit runner r may run at once
// now, while the hub's pacing catches up with the weekly pace (0: none).
const runnerBoost = (r) => {
  const p = r.reported?.usage?.pace;
  return p?.state === "catching_up" ? Math.max((p.slots || 0) - runnerLimit(r), 0) : 0;
};
// runnerSlots is how many tasks r may run at once now: its limit and any
// boost, and never fewer than it runs (a task started under a boost that
// has since ended still runs).
const runnerSlots = (r) => Math.max(runnerLimit(r) + runnerBoost(r), r.running || 0);
const boostNote = (r) => runnerBoost(r) ? ` (+${runnerBoost(r)} catching up with the weekly pace)` : "";
const runnerOutdated = (r) => outdatedFor(runnerInfo.version, r.version);
// runnerAccount is the Claude account runner r's claude is logged in to
// (null: logged out, or an older runner that doesn't say). The hub paces
// the runners on one account together.
const runnerAccount = (r) => r.reported?.claude?.account || null;
const planLabel = (s) => ({ max: "Max", pro: "Pro", team: "Team", enterprise: "Enterprise", free: "Free" })[s] || (s ? s[0].toUpperCase() + s.slice(1) : "");
function accountChip(r) {
  const a = runnerAccount(r);
  if (!a) return null;
  const plan = planLabel(a.subscription);
  return h("span", { class: "runner-account", title: `Claude account${a.org_name ? ` · ${a.org_name}` : ""}. Runners on one account share its usage and are paced together.` },
    ico("user"), h("span", { class: "runner-account-email" }, a.email), plan ? h("span", { class: "runner-plan" }, plan) : null);
}
// signInChip says how Claude signs in on runner r (an API key, or which
// subscription), and whether it takes other members' tasks.
function signInChip(r) {
  if (!r.sign_in || r.sign_in === "unknown") return null;
  const key = r.sign_in === "an API key";
  return h("span", { class: `runner-signin${r.takes_others ? " shared" : ""}`,
    title: r.takes_others ? "Takes other members' tasks: it runs them with its API key, never a subscription"
      : `Takes only ${r.owner?.username || "its owner"}'s tasks` },
    ico(key ? "key" : "user"), key ? "API key" : r.sign_in.replace(/^an? /, ""), r.takes_others ? " · shared" : "");
}

// whoseTasks says whose tasks runner r takes, and why not others' when its
// setting is on but has no effect.
function whoseTasks(r) {
  const owner = r.owner?.username || "its owner";
  if (r.claude_only) {
    return h("p", { class: "hint" }, `No tasks: ${owner} is a planner, and their runner runs only their chat and feature briefs.`);
  }
  if (r.takes_others) {
    return h("p", { class: "hint" }, `${owner}'s and other members' tasks, on its API key.`);
  }
  return h("p", { class: `hint${r.shared ? " warn" : ""}` },
    `Only ${owner}'s tasks, on ${r.mine ? "your" : `${owner}'s`} login (${r.sign_in || "unknown"}).`,
    r.shared && r.cant_share ? ` Sharing has no effect: ${r.cant_share}.` : "");
}

// pacedWith are the other runners on r's Claude account.
const pacedWith = (r) => {
  const e = runnerAccount(r)?.email?.toLowerCase();
  return e ? (state.runners || []).filter((x) => x.id !== r.id && runnerAccount(x)?.email?.toLowerCase() === e) : [];
};
// runnerClaudeWarnings is what runner r found changed about its Claude Code CLI.
const runnerClaudeWarnings = (r) => r.reported?.claude?.warnings || [];

// Disk space. The runner reports its free space with its heartbeat; the hub
// works out under the runner's thresholds (r.disk) whether it is low
// (r.disk_low, the warning) and too full to start a task (r.disk_full).
const GiB = 2 ** 30;
const sizeText = (n) => n >= 100 * GiB ? `${Math.round(n / GiB)} GB` : n >= GiB ? `${(n / GiB).toFixed(1)} GB`
  : n >= 2 ** 20 ? `${Math.round(n / 2 ** 20)} MB` : `${Math.round(n / 1024)} KB`;
const diskDefaults = { warn_percent: 10, warn_gb: 10, min_free_gb: 2 };
const minFreeText = (r) => `${r.disk?.min_free_gb || diskDefaults.min_free_gb} GB`;
const diskWarning = (r) => r.disk_low ? `${r.disk_low}.${r.disk_full ? ` It starts no tasks until it has ${minFreeText(r)} free.` : ""}` : "";
const volumeLabels = { projects: "Projects", worktrees: "Task worktrees", cache: "Cache folder" };

// saveRunner saves one of runner r's settings as it changes: "Saved" in the
// sheet's header, or the error under the field (which keeps what was typed).
// The runner's page is drawn again once it is saved.
function saveRunner(el, r, body, then) {
  attempt(async () => {
    if (!(await saveField(el, async () => { checked(el); await patchRunner(r, body); }))) return;
    if (then) await then();
    await renderRunners();
  })();
}

// diskSettings are runner r's disk thresholds, for its owner and the org's
// admins: empty is the default (for the Go build cache trim: off).
function diskSettings(r) {
  const input = (key, label, { max = 100000, placeholder = String(diskDefaults[key] ?? "") } = {}) =>
    h("input", { type: "number", min: "0", max: String(max), step: "any", inputmode: "decimal", value: r.disk?.[key] ? String(r.disk[key]) : "",
      placeholder, "aria-label": `${label} (${r.name})`,
      onchange: (e) => saveRunner(e.target, r, { disk: { ...r.disk, [key]: Number(e.target.value) || 0 } }) });
  const unit = (el, text) => h("span", { class: "runner-setting-unit" }, el, h("span", { "aria-hidden": "true" }, text));
  const setting = (title, hint, control) => h("div", { class: "runner-setting" },
    h("div", { class: "runner-setting-text" }, h("strong", {}, title), h("p", { class: "hint" }, hint)), control);
  return h("div", { class: "runner-disk-settings" },
    setting("Warn below", "Free space that warns on the board. Empty: 10% or 10 GB.",
      h("span", { class: "runner-setting-pair" }, unit(input("warn_percent", "Warn below percent free", { max: 99 }), "%"), "or",
        unit(input("warn_gb", "Warn below GB free"), "GB"))),
    setting("Start no task below", "Tasks wait instead of failing. Empty: 2 GB.",
      unit(input("min_free_gb", "Start no task below GB free"), "GB")),
    setting("Trim the Go build cache above", "Runs go clean -cache, at most daily. Empty: off.",
      unit(input("trim_go_cache_gb", "Trim the Go build cache above GB", { placeholder: "Off" }), "GB")));
}

// renderDiskBanner says on the board, for everyone in the org, which
// runners are nearly out of disk space, until they have enough again.
export function renderDiskBanner() {
  const el = $("#disk-banner");
  if (!el) return;
  const low = (state.runners || []).filter((r) => r.disk_low);
  el.hidden = !low.length;
  el.classList.toggle("full", low.some((r) => r.disk_full));
  if (!low.length) return el.replaceChildren();
  el.replaceChildren(...low.map((r) => h("span", {},
    h("strong", {}, r.disk_full ? "Runner disk full: " : "Runner disk nearly full: "), diskWarning(r), " ",
    h("button", { type: "button", class: "link inline", onclick: attempt(() => openRunners(r.id)) }, "See what uses it"))));
}

// runnerDisk is runner r's Disk section: free space per disk, the warning
// with the largest folders in its cache folder, and its Go build cache.
function runnerDisk(r) {
  const d = r.reported?.disk;
  if (!d?.volumes?.length) return null; // an older runner, or one that can't measure
  const meter = (v) => {
    const used = v.total ? Math.round((1 - v.free / v.total) * 100) : 0;
    const bar = h("span", { class: "runner-meter-fill" });
    bar.style.width = `${Math.min(100, Math.max(0, used))}%`;
    const low = r.disk_low && v.free === Math.min(...d.volumes.map((x) => x.free));
    return h("div", { class: `runner-disk-vol runner-meter${low ? " high" : used >= 80 ? " mid" : ""}`, title: v.path },
      h("span", { class: "runner-disk-label" }, volumeLabels[v.label] || v.label),
      h("span", { class: "runner-meter-bar", role: "meter", "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": String(used),
        "aria-label": `${volumeLabels[v.label] || v.label} disk used` }, bar),
      h("span", { class: "runner-disk-free" }, `${sizeText(v.free)} free of ${sizeText(v.total)}`));
  };
  const gc = d.go_cache;
  return h("div", { class: "runner-disk" },
    ...d.volumes.map(meter),
    r.disk_low ? h("div", { class: "runner-update runner-disk-warn", role: "status" },
      h("p", { class: "hint warn" }, diskWarning(r)),
      d.largest?.length ? h("p", { class: "hint" }, "Largest folders in its cache folder:") : null,
      d.largest?.length ? h("ul", { class: "runner-disk-largest" }, ...d.largest.map((f) =>
        h("li", {}, h("code", {}, f.path), h("span", {}, sizeText(f.size))))) : null,
      h("p", { class: "hint" }, "To empty the Go build cache, run on ", r.name, ":"),
      copyBlock("go clean -cache", "Command copied", { pre: true })) : null,
    gc?.path ? h("p", { class: "hint runner-disk-go" }, "Go build cache: ", h("strong", {}, sizeText(gc.size || 0)), " in ", h("code", {}, gc.path),
      gc.trimmed ? `. Trimmed ${agoText(gc.trimmed)}${gc.freed ? `, freeing ${sizeText(gc.freed)}` : ""}.` : ".") : null,
    gc?.error ? h("p", { class: "hint warn" }, gc.error) : null);
}

// runnerStatus is what runner r is doing, in a few words, and its tone
// (on, busy, paused, off) for the dot and the card's accent.
function runnerStatus(r) {
  const n = (k) => plural(k, "task");
  // Updating (maintenance mode): it takes no new tasks, apart from its pause.
  const u = r.update;
  if (u?.phase === "updating") return { tone: "paused", text: `Updating to ${u.target}…` };
  if (u && !r.online) return { tone: "off", text: `Offline · updates to ${u.target} when back` };
  if (u) return { tone: "paused", text: r.running ? `Waiting for ${n(r.running)} to finish…` : "Updating soon…" };
  if (!r.online) return { tone: "off", text: r.last_seen ? `Offline · seen ${agoText(r.last_seen)}` : "Never connected" };
  if (r.paused) return { tone: "paused", text: r.running ? `Paused · finishing ${n(r.running)}` : "Paused" };
  const usage = r.reported?.usage;
  if (usage?.known && usage.paused) return { tone: "paused", text: `Waiting on Claude limits${usage.reason ? ` (${usage.reason})` : ""}` };
  if (r.running) return { tone: "busy", text: `Running ${r.running} of ${runnerSlots(r)}${boostNote(r)}` };
  return { tone: "on", text: `Idle · up to ${runnerSlots(r)} at once${boostNote(r)}` };
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

// The sidebar's Runners block (only on a hub): a row per runner with its
// dot, name and tasks running of its slots, online ones first; past
// sideRunnerRows the offline rest fold into one line. ⬆ marks a runner that
// runs another Dash version than the hub; its tooltip has the update command.
const sideRunnerRows = 6;
export function renderSideRunners() {
  const box = $("#side-runners"), list = state.runners;
  box.hidden = !Array.isArray(list);
  if (box.hidden) return;
  // The dot takes the runner's tone from runnerStatus, as in the Runners
  // sheet: yellow while it runs tasks or is paused, green idle, red offline.
  const dot = { off: "down", busy: "some", paused: "some", on: "ok" };
  const row = (r) => {
    const st = runnerStatus(r), outdated = runnerOutdated(r);
    // In words, not only the dot's colour: offline, updating, paused, idle or busy, with its slots.
    const count = !r.online ? "offline" : r.update ? "updating" : r.paused ? "paused" : `${r.running ? "busy" : "idle"} ${r.running || 0}/${runnerSlots(r)}${runnerBoost(r) ? "⇡" : ""}`;
    const update = outdated ? `Runs ${r.version}, the hub runs ${runnerInfo.version}. To update it, run on ${r.name}:\n${installCmd()}` : "";
    const diskWarn = diskWarning(r);
    return h("li", {}, h("button", {
      type: "button", class: "side-runner", title: [`${r.name}: ${st.text}`, runnerOS(r)?.label, diskWarn, update].filter(Boolean).join("\n"),
      onclick: attempt(async () => { setMenu(false); await openRunners(r.id); }),
    },
    h("span", { class: "side-runner-icon", "aria-hidden": "true" }, h("span", { class: `machine-dot ${dot[st.tone]}` }), outdated ? h("span", { class: "side-runner-badge" }, "⬆") : null),
    h("span", { class: "side-runner-name" }, r.name),
    osLogo(r, "side-runner-os"),
    diskWarn ? h("span", { class: "side-runner-disk", "aria-hidden": "true" }, ico("alert")) : null,
    outdated ? h("span", { class: "side-runner-update", "aria-hidden": "true" }, "⬆") : null,
    h("span", { class: "side-runner-count" }, count),
    diskWarn || outdated ? h("span", { class: "sr-only" }, (diskWarn ? ", disk nearly full" : "") + (outdated ? ", update available" : "")) : null));
  };
  // Folded, the head says what the rows would: how many are online and
  // how many tasks run on them.
  const online = list.filter((r) => r.online);
  const running = online.reduce((n, r) => n + (r.running || 0), 0);
  $("#side-runners-sum").textContent = !list.length ? "" : `${online.length}/${list.length} online${running ? ` · ${running} running` : ""}`;
  const sorted = [...list].sort(byStatus);
  // Past the limit, only offline runners fold: online ones always show.
  const cut = sorted.length > sideRunnerRows ? Math.max(sideRunnerRows - 1, sorted.filter((r) => r.online).length) : sorted.length;
  const top = sorted.slice(0, cut), rest = sorted.slice(cut);
  const manage = (label, title) => h("li", {}, h("button", { type: "button", class: "side-runner side-runner-more", title,
    onclick: attempt(async () => { setMenu(false); await openRunners(); }) }, label));
  $("#side-runner-list").replaceChildren(...[
    ...(list.length ? top.map(row) : [h("li", { class: "machine-empty" }, "No runners yet")]),
    rest.length ? manage(`+${rest.length} offline`, rest.map((r) => r.name).join("\n")) : null,
    manage("Manage runners", "Open the Runners page"),
  ].filter(Boolean));
}

// Folding the sidebar's Runners block leaves its head, with the summary, so
// the projects list above it gets the room. Kept per browser.
function foldSideRunners(folded) {
  $("#side-runners").classList.toggle("folded", folded);
  const btn = $("#side-runners-fold");
  btn.setAttribute("aria-expanded", String(!folded));
  btn.title = folded ? "Show the runners" : "Fold the runners";
}
foldSideRunners(store.flag("dash.runnersFolded"));
$("#side-runners-fold").addEventListener("click", () => {
  const folded = !$("#side-runners").classList.contains("folded");
  store.setFlag("dash.runnersFolded", folded);
  foldSideRunners(folded);
});

// The projects runner r builds (it has the labels they run on): their tasks run there.
const runnerProjects = (r) => (state.projects || []).filter((p) => !p.prefix && servesProject(r, p));

// What the sheet shows beside its rail: "overview", "add" (a new runner's
// code) or a runner's id. Back to the overview each time it opens.
let shown = "overview";
const patchRunner = (r, body) => api("PATCH", `/api/runners/${encodeURIComponent(r.id)}`, body);
// By name, regardless of case; runners with the same name by id, so they
// don't swap places on each refresh.
function byName(a, b) {
  return a.name.localeCompare(b.name, undefined, { sensitivity: "base", numeric: true }) || a.name.localeCompare(b.name) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
}
// Online before offline; by name. (Callers split yours from the others'.)
const byStatus = (a, b) => (!!b.online - !!a.online) || byName(a, b);
const runnerWarned = (r) => !!r.disk_low || runnerOutdated(r) || runnerClaudeWarnings(r).length > 0;
const runnerWarnText = (r) => r.disk_low ? (r.disk_full ? "Disk full" : "Disk nearly full") : runnerOutdated(r) ? "Out of date" : "Claude Code warning";

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
      osLogo(r, "runner-rail-os"),
      runnerWarned(r) ? h("span", { class: "runner-rail-warn", title: runnerWarnText(r) }, ico("alert")) : null);
    b.classList.add("runner-rail", `tone-${st.tone}`);
    b.title = [`${r.name}: ${st.text}`, runnerOS(r)?.label].filter(Boolean).join("\n");
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
  const updating = list.filter((r) => r.update).length;
  const outdated = list.filter((r) => runnerOutdated(r) && !r.update).length;
  const claudeWarn = list.filter((r) => runnerClaudeWarnings(r).length).length;
  const diskLow = list.filter((r) => r.disk_low).length;
  return [
    stat(online, `of ${list.length} online`, online ? "on" : "off"),
    stat(running, running === 1 ? "task running" : "tasks running", running ? "busy" : ""),
    paused ? stat(paused, "paused", "paused") : null,
    updating ? stat(updating, "updating", "paused") : null,
    outdated ? stat(outdated, "out of date", "paused") : null,
    claudeWarn ? stat(claudeWarn, "with Claude Code warnings", "paused") : null,
    diskLow ? stat(diskLow, "low on disk space", "off") : null,
  ].filter(Boolean);
}

// The overview: how the runners are doing, one line each to open it.
function paintOverview(list) {
  const box = $("#runners-list");
  $("#runners-add").hidden = list === null;
  if (list === null) {
    $("#runners-stats").replaceChildren();
    box.replaceChildren(h("p", { class: "hint" }, "This Dash takes no runners."));
    return;
  }
  $("#runners-stats").replaceChildren(...(list.length ? runnerStats(list) : []));
  if (!list.length) {
    box.replaceChildren(h("div", { class: "runners-empty" },
      ico("computer"),
      h("p", { class: "empty-title" }, "No runners yet"),
      h("p", { class: "hint" }, "A runner is a computer that runs this organization's tasks and chats: its owner's, with their Claude subscription, or every member's, when it is shared with an API key. Projects pick runners by label. It needs git and Claude Code logged in, on Linux, macOS or Windows. ",
        docsLink("several-computers.md"))));
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
          osChip(r),
          showOwner && r.owner ? h("span", { class: "runner-owner" }, ico("users"), r.owner.username) : null,
          accountChip(r),
          h("span", {}, n ? plural(n, "project") : "No projects"),
          runnerWarned(r) ? h("span", { class: "runner-row-warn" }, ico("alert"), runnerWarnText(r)) : null)),
      runnerUsageMeters(r),
      ico("chevron-right")));
  };
  const group = (title, rs, showOwner) => rs.length ? [
    title ? h("h3", { class: "section-head" }, title) : null,
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
      osChip(r),
      r.version ? h("span", { class: `runner-version${outdated ? " warn" : ""}`, title: outdated ? `Out of date: the hub runs ${runnerInfo.version}` : "Dash version" }, `Dash ${r.version}`) : null,
      claude ? h("span", { class: `runner-version${claudeWarns.length ? " warn" : ""}`,
        title: claudeWarns.length ? "Claude Code version: its output or flags look different from what Dash expects" : "Claude Code version" },
        `Claude ${claude.version || "?"}`) : null,
      accountChip(r),
      signInChip(r)),
    pacedWith(r).length ? h("p", { class: "hint runner-paced-with" },
      `Shares its Claude account, and its usage, with ${pacedWith(r).map((x) => x.name).join(", ")}.`) : null,
    runnerUpdate(r),
    claudeWarns.length ? h("div", { class: "runner-update runner-claude-warn", role: "status" },
      h("p", { class: "hint warn" }, `Claude Code${claude.version ? ` ${claude.version}` : ""} looks different from what Dash expects. Updating Dash may help:`),
      h("ul", {}, ...claudeWarns.map((w) => h("li", {}, w)))) : null,
    h("h4", { class: "section-head" }, "Agents"),
    runnerAgents(r),
    usage ? h("h4", { class: "section-head" }, "Claude usage") : null,
    usage,
    r.reported?.disk?.volumes?.length || r.can_manage ? h("h4", { class: "section-head" }, "Disk") : null,
    runnerDisk(r),
    r.can_manage ? diskSettings(r) : null,
    h("h4", { class: "section-head" }, "Whose tasks"),
    whoseTasks(r),
    h("h4", { class: "section-head" }, "Labels"),
    runnerLabelsSection(r),
    h("h4", { class: "section-head" }, "Projects"),
    h("div", { class: "runner-projects" },
      projects.length
        ? projects.map((p) => h("button", { class: "runner-project", type: "button", title: `Open ${p.name}`,
          onclick: () => { $("#runners-sheet").close(); selectProject(p); } }, ico("repo"), p.name))
        : h("span", { class: "hint" }, "No project runs on its labels yet.")),
    h("div", { class: "runner-actions" },
      ib({ class: "btn small", type: "button",
        title: `The shared projects ${r.name} builds, and the labels they run on`,
        onclick: attempt(() => showRunnerLinks(r)) }, "repo", "Shared projects…"),
      r.mine ? ib({ class: "btn small", type: "button", disabled: !r.online,
        title: r.online ? `Pick git repositories on ${r.name} to add as its projects` : `${r.name} is offline`,
        onclick: attempt(() => showRunnerRepos(r)) }, "plus", "Add from this runner…") : null));

  if (!r.can_manage) {
    $("#runner-manage").replaceChildren(h("p", { class: "hint runner-manage-note" }, `Only ${r.owner?.username || "its owner"} or an admin can change ${r.name}.`));
    return;
  }
  const parallel = h("input", { type: "number", min: "0", max: "64", inputmode: "numeric", value: r.parallel ? String(r.parallel) : "",
    placeholder: String(r.reported?.parallel || 1), "aria-label": `Tasks ${r.name} runs at once`,
    onchange: (e) => saveRunner(e.target, r, { parallel: Number(e.target.value) || 0 }) });
  const setting = (title, hint, control, cls = "") => h("div", { class: `runner-setting ${cls}` },
    h("div", { class: "runner-setting-text" }, h("strong", {}, title), h("p", { class: "hint" }, hint)), control);
  fill($("#runner-manage"),
    h("h4", { class: "section-head" }, "Settings"),
    setting("Tasks at once", `Empty: its own setting (${r.reported?.parallel || 1}).`, parallel),
    setting(r.paused ? "Paused" : "Taking tasks",
      r.paused ? "Takes no new tasks until resumed." : "Pausing finishes what it runs, then takes no new tasks.",
      ib({ class: "btn small", type: "button", onclick: attempt(async () => {
        await patchRunner(r, { paused: !r.paused });
        toast(r.paused ? `${r.name} takes tasks again` : `${r.name} paused: it finishes what it runs and takes no new tasks`);
        await renderRunners();
      }) }, r.paused ? "play" : "pause", r.paused ? "Resume" : "Pause")),
    setting("Take other members' tasks",
      r.shared
        ? (r.takes_others ? "On, with its API key."
          : `On, but has no effect: ${r.cant_share || "it needs an API key alone"}.`)
        : r.cant_share
          ? `Needs an API key and no subscription login: ${r.cant_share}.`
          : "Off: only its owner's tasks.",
      ib({ class: "btn small", type: "button", "aria-pressed": String(!!r.shared), disabled: !r.shared && !!r.cant_share,
        title: !r.shared && r.cant_share ? r.cant_share : "", onclick: attempt(async () => {
          await patchRunner(r, { shared: !r.shared });
          toast(r.shared ? `${r.name} takes only ${r.owner?.username || "its owner"}'s tasks now` : `${r.name} takes other members' tasks too, on its API key`);
          await renderRunners();
        }) }, "users", r.shared ? "Turn off" : "Turn on")),
    runnerInfo.canUpdate ? setting(r.auto_update ? "Updates itself" : "Updated by hand",
      r.auto_update ? "Updates to the hub's version once its tasks finish."
        : "Off: update it by hand.",
      ib({ class: "btn small", type: "button", "aria-pressed": String(!!r.auto_update), onclick: attempt(async () => {
        await patchRunner(r, { auto_update: !r.auto_update });
        toast(r.auto_update ? `${r.name} is updated by hand now` : `${r.name} updates itself to the hub's version`);
        await renderRunners();
      }) }, "reload", r.auto_update ? "Turn off" : "Turn on")) : null,
    setting("Revoke", "Disconnects it and revokes its token.",
      ib({ class: "btn small danger", type: "button", title: `Revoke ${r.name}'s token`, onclick: attempt(async () => {
        const ok = await ask({ title: `Revoke ${r.name}?`, body: "Its running tasks fail and can be queued again. To bring it back, install it with a new code.", ok: "Revoke", danger: true });
        if (!ok) return;
        await api("DELETE", `/api/runners/${encodeURIComponent(r.id)}`);
        toast(`${r.name} revoked`);
        await refreshMachines();
      }) }, "trash", "Revoke…"), "danger"));
}

// agentState is how agent a stands on its runner: a short text and a tone.
function agentState(a) {
  if (!a.checked_at) return { text: "Not checked yet", tone: "muted" };
  if (!a.installed) return { text: "Not installed", tone: "off" };
  if (!a.logged_in) return { text: "Not logged in", tone: "warn" };
  return { text: "Logged in", tone: "ok" };
}

// runnerAgents lists the agent CLIs runner r reported: each one's version,
// login, the features it has and its warnings. Projects on an agent run
// only on runners where it is installed and logged in (its label).
function runnerAgents(r) {
  const list = r.agents || [];
  if (!list.length) {
    return h("p", { class: "hint" }, r.online ? "It hasn't reported its agents yet." : "It reports its agents when it's online.");
  }
  // Claude Code's warnings are shown above, as before agents were reported.
  const claudeShown = runnerClaudeWarnings(r).length > 0;
  return h("ul", { class: "runner-agents" }, ...list.map((a) => {
    const st = agentState(a);
    const has = new Set(a.features || []);
    const warns = a.name === "claude" && claudeShown ? [] : a.warnings || [];
    return h("li", { class: "runner-agent" },
      h("div", { class: "runner-agent-head" },
        h("strong", {}, agentTitle(a.name)),
        a.version ? h("span", { class: `runner-version${a.warnings?.length ? " warn" : ""}`, title: `${agentTitle(a.name)} version` }, a.version) : null,
        h("span", { class: `runner-agent-state ${st.tone}` }, st.text),
        h("span", { class: "runner-label default", title: st.tone === "ok" || st.tone === "muted"
          ? `Its label: projects on ${agentTitle(a.name)} run here` : `Without its label until ${agentTitle(a.name)} is installed and logged in` }, a.name)),
      h("div", { class: "runner-agent-features", "aria-label": "Features" }, ...Object.entries(FEATURES).map(([f, x]) =>
        h("span", { class: `runner-agent-feature${has.has(f) ? "" : " off"}`, title: `${has.has(f) ? "Can" : "Can't"} ${x.does}` },
          ico(has.has(f) ? "check" : "x"), x.label))),
      warns.length ? h("ul", { class: "hint warn runner-agent-warn" }, ...warns.map((w) => h("li", {}, w))) : null);
  }));
}

// runnerLabelsSection shows runner r's labels: its system's (dashed) and,
// as chips its owner or an admin picks, its own; each change is saved.
function runnerLabelsSection(r) {
  const defaults = r.default_labels || [];
  if (!r.can_manage) {
    return runnerLabels(r).length ? h("div", { class: "runner-labels" }, ...runnerLabels(r).map((l, i) =>
      h("span", { class: `runner-label${i < defaults.length ? " default" : ""}`, title: i < defaults.length ? "Its system, architecture or agents" : "" }, l)))
      : h("p", { class: "hint" }, `None: it builds only projects on ${LABEL_ANY}.`);
  }
  const picker = labelPicker({ value: (r.labels || []).join(","), exclude: defaults, label: `${r.name}'s own labels`, placeholder: "e.g. gpu",
    // Saved as each chip comes or goes, as the sheet's other fields are
    // (autosave.js): "Saved" in its header, or the error under the picker.
    onChange: (labels) => attempt(async () => {
      if (!(await saveField(picker.el, () => patchRunner(r, { labels })))) return;
      await loadProjects();
      await renderRunners();
      $("#runner-detail .runner-own-labels .label-picker-input")?.focus();
    })() });
  return h("div", { class: "runner-own-labels" },
    defaults.length ? h("div", { class: "runner-labels" }, ...defaults.map((l) =>
      h("span", { class: "runner-label default", title: "Its system, architecture or agents" }, l))) : null,
    picker.el);
}

// runnerUpdate is runner r's update: its progress, why the last one failed,
// or, out of date, an Update button for its owner and the org's admins (a
// hub built from source has no release to update to: its owner gets the
// install command instead).
function runnerUpdate(r) {
  const u = r.update;
  const path = `/api/runners/${encodeURIComponent(r.id)}/update`;
  if (u) {
    const waiting = u.phase === "waiting";
    const text = !waiting ? `Updating to ${u.target}…`
      : !r.online ? `Updates to ${u.target} once back online.`
      : r.running ? `Updates to ${u.target} after ${plural(r.running, "task")}; takes no new ones meanwhile.`
      : `Updating to ${u.target} in a moment.`;
    return h("div", { class: "runner-update", role: "status" },
      h("p", { class: "hint" }, `${u.auto ? "Auto-update: " : ""}${text}`),
      waiting && r.can_manage ? ib({ class: "btn small", type: "button", onclick: attempt(async () => {
        await api("DELETE", path);
        toast(`${r.name}'s update canceled: it takes tasks again`);
        await renderRunners();
      }) }, "x", "Cancel update") : null);
  }
  const failed = r.update_error ? h("p", { class: "hint warn" }, `The last update failed: ${r.update_error}`) : null;
  if (!runnerOutdated(r)) return failed ? h("div", { class: "runner-update", role: "status" }, failed) : null;
  if (runnerInfo.canUpdate && r.can_manage) {
    return h("div", { class: "runner-update" },
      failed,
      h("p", { class: "hint warn" }, `Runs ${r.version}; the hub runs ${runnerInfo.version}.`),
      ib({ class: "btn small", type: "button", onclick: attempt(async () => {
        await api("POST", path);
        toast(`${r.name} updates to ${runnerInfo.version} once its tasks are done`);
        await renderRunners();
      }) }, "up", `Update to ${runnerInfo.version}`),
      r.mine ? h("details", { class: "runner-update-manual" }, h("summary", {}, "Or update it by hand"),
        h("p", { class: "hint" }, `Run this on ${r.name} (it keeps its registration):`),
        ...updateCmds()) : null);
  }
  if (!r.mine) return failed;
  return h("div", { class: "runner-update" },
    failed,
    h("p", { class: "hint warn" }, `Runs ${r.version}, the hub runs ${runnerInfo.version}. To update it, run this on ${r.name} (it keeps its registration):`),
    ...updateCmds());
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
      h("p", { class: "hint" }, "No new git repositories in ", where, "."),
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
    if (out.added.length) toast(`Added ${plural(out.added.length, "project")} on ${r.name}`);
    for (const f of out.failed) toast(`${f.path}: ${f.error}`, true);
    await loadProjects();
    await showRunnerRepos(r);
  }));
  box.replaceChildren(
    h("h4", {}, `Projects on ${r.name}`),
    h("p", { class: "hint" }, "Git repositories in ", where, ". Tick the ones to add as its own projects."),
    h("label", { class: "runner-pick runner-repos-all" }, all, "Select all"),
    h("ul", { class: "runner-repos-list" }, ...res.repos.map((x, i) => h("li", {},
      h("label", { class: "runner-pick" }, checks[i],
        h("span", { class: "runner-repo" },
          h("span", { class: "runner-name" }, x.name),
          h("span", { class: "hint" }, x.path, x.branch ? ` · ${x.branch}` : " · detached", x.remote ? ` · ${x.remote}` : " · no remote")))))),
    h("div", { class: "runner-repos-actions" }, addBtn, close));
}

// ---- A runner's Projects: which of the org's shared projects it builds.
// Projects never name runners: a runner builds the projects that run on
// labels it has. This panel shows each shared project with the labels it
// runs on and whether the runner has them; its owner or an admin gives it
// a project's missing labels in one click. A repository from its owner's
// GitHub that isn't a project yet becomes a shared project, on the runner's
// own labels (or any, any runner, when it has none).
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

// refreshRunnerLinks reloads the open Projects panel: labels changed here,
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

// withBusy marks key busy while fn runs, then reloads the projects and the panel.
async function withBusy(key, fn) {
  links.busy.add(key);
  renderRunnerLinks();
  try {
    await fn();
  } finally {
    links.busy.delete(key);
    await loadProjects();
    await loadRunners();
    await refreshRunnerLinks();
  }
}

// giveLabels gives runner r the labels project l runs on that it lacks, so
// it builds l (and any other project on those labels).
async function giveLabels(r, l) {
  const owner = r.owner?.username || "its owner";
  if (!await ask({ title: `Give ${r.name} ${plural(l.missing.length, "label")}?`,
    body: `${r.name} gets ${l.missing.join(", ")}, so it builds ${l.name} and every other project on those labels, with ${owner}'s Claude login.`,
    ok: "Add labels" })) return;
  await withBusy(l.id, async () => {
    await patchRunner(r, { labels: [...(r.labels || []), ...l.missing] });
    toast(`${r.name} now builds ${l.name}`, { tone: "ok" });
  });
}

// createFromRepo makes a shared project of a GitHub repository, from runner r.
async function createFromRepo(r, repo, name) {
  await withBusy(repo, async () => {
    toast(`Creating the shared project ${name}…`);
    const res = await api("POST", `/api/runners/${encodeURIComponent(r.id)}/links`, { github: repo, name });
    const on = `runs on ${res.project.runs_on.join(", ")}`;
    if (res.clone_error) toast(`${res.project.name} ${on}, but ${res.clone_error}`, true);
    else toast(`${res.created ? "Created the shared project" : "Found"} ${res.project.name}, which ${on}${res.cloned ? `; ${r.name} has its clone` : ""}`, { tone: "ok" });
  });
}

// renderRunnerLinks draws the projects runner links.runner builds or could:
// its owner's GitHub repositories (as in Add project) and the org's other
// shared projects. Typing in the filter redraws only the list, so the field
// keeps its focus and caret.
function renderRunnerLinks() {
  const r = links.runner, res = links.data;
  if (!r || !res) return;
  const org = state.me?.org?.name || "the organization";
  const owner = r.owner?.username || "its owner";
  const filter = h("input", { type: "search", placeholder: "Filter by name or description", value: links.q, autocomplete: "off", spellcheck: false, "aria-label": "Filter projects" });
  const list = h("div", { class: "gh-list", role: "list", "aria-label": `Projects ${r.name} builds` }, ...runnerLinkRows(r, res, org));
  filter.addEventListener("input", () => {
    links.q = filter.value;
    list.replaceChildren(...runnerLinkRows(r, res, org));
  });
  $("#runners-links").replaceChildren(
    h("h4", {}, `Projects ${r.name} builds`),
    h("p", { class: "hint" }, `It builds the projects on its labels${runnerLabels(res.runner).length ? ` (${runnerLabels(res.runner).join(", ")})` : ""} and on ${LABEL_ANY}. `, docsLabels()),
    r.mine ? h("p", { class: "hint" }, `Create project makes a shared project on ${r.labels?.length ? r.labels.join(", ") : LABEL_ANY}. For one only ${r.name} works on, use Add from this runner….`) : null,
    h("p", { class: "hint" }, ghNoteText(r, res, org, owner)),
    h("div", { class: "gh-filter-row" }, filter),
    list,
    linksClose());
}

// runnerLinkRows is the list's rows for the filter in links.q.
function runnerLinkRows(r, res, org) {
  const can = res.runner.can_manage;
  const q = links.q.trim().toLowerCase();
  const byId = new Map(res.projects.map((p) => [p.id, p]));
  const projectOf = (x) => x.project && byId.get(x.project);
  const row = ({ key, label, desc, meta, action }) => h("div", { class: "gh-repo", role: "listitem", "data-key": key, "aria-busy": links.busy.has(key) ? "true" : null },
    h("span", { class: "gh-repo-text" }, label, desc ? h("span", { class: "gh-repo-desc" }, desc) : null,
      h("span", { class: "gh-repo-meta" }, ...meta.filter(Boolean).map((t) => h("span", {}, t)))),
    action || null);
  const linkMeta = (l) => [l.runs_on.length ? `runs on ${l.runs_on.join(", ")}` : "runs on no labels",
    l.linked ? `${r.name} builds it` : l.runs_on.length ? `${r.name} lacks ${l.missing.join(", ")}` : null,
    l.running ? `${l.running} running there` : null, l.worktrees ? `${l.worktrees} with a worktree there` : null];
  const projectRow = (l, extra = {}) => row({ key: l.id,
    label: extra.label || h("span", { class: "gh-repo-title" }, h("strong", {}, l.name)),
    desc: extra.desc ?? l.remote_url, meta: [...(extra.meta || []), `shared project: ${l.name}`, ...linkMeta(l)],
    action: can && !l.linked && l.missing?.length ? h("button", { class: "btn small", type: "button", disabled: links.busy.has(l.id),
      title: `Give ${r.name} ${l.missing.join(", ")}`, onclick: attempt(() => giveLabels(r, l)) }, "Add labels") : null });

  const rows = [];
  // The owner's GitHub repositories, grouped by owner, as in Add project.
  const repos = links.gh?.repos || [];
  if (links.gh) {
    const gh = ghRepoRows(repos, { q, user: links.gh.user, also: (x) => [projectOf(x)?.name], row: (x) => {
      const l = projectOf(x), label = ghRepoLabel(x), hints = ghRepoHints(x);
      return l ? projectRow(l, { label, desc: x.description || "", meta: hints })
        : row({ key: x.nameWithOwner, label, desc: x.description || "", meta: [...hints, "not a project yet"],
          action: can ? h("button", { class: "btn small", type: "button", disabled: links.busy.has(x.nameWithOwner),
            onclick: attempt(() => createFromRepo(r, x.nameWithOwner, x.name)) }, "Create project") : null });
    } });
    rows.push(...gh.rows);
    if (gh.more) rows.push(h("div", { class: "gh-empty" }, `${gh.more} more; type to narrow the list.`));
  }
  // The org's other shared projects: not from this GitHub login (a remote
  // URL, another account's repository), or all of them without the list.
  // Those it builds first.
  const seen = new Set(repos.map((x) => projectOf(x)?.id).filter(Boolean));
  const rest = res.projects.filter((l) => !seen.has(l.id) && (!q || [l.name, l.remote_url].some((s) => (s || "").toLowerCase().includes(q))))
    .sort((a, b) => b.linked - a.linked);
  if (rest.length) {
    rows.push(h("div", { class: "gh-owner", role: "presentation" }, h("span", {}, links.gh ? `Other shared projects in ${org}` : `Shared projects in ${org}`)));
    rows.push(...rest.map((l) => projectRow(l)));
  }
  return rows.length ? rows : [h("div", { class: "gh-empty" }, q ? "No project matches." : `${org} has no shared projects yet.`)];
}

// ghNoteText says where the list's repositories come from, or why there are none.
function ghNoteText(r, res, org, owner) {
  if (!r.mine) {
    return `Only ${owner} can list ${r.name}'s GitHub repositories; ${org}'s shared projects are below.`;
  }
  if (!r.online) return `${r.name} is offline, so it can't list your repositories; the shared projects are below.`;
  if (links.ghError) return `${links.ghError} The org's shared projects are below.`;
  if (!links.gh) return `Asking ${r.name}'s gh for your repositories…`;
  return `Your repositories from ${r.name}'s gh (signed in as ${links.gh.user}), and the org's other shared projects.`;
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
    let psArgs = `-Code ${code}`;
    if (n) psArgs += ` -Name ${psq(n)}`;
    if (d) psArgs += ` -Projects ${psq(d)}`;
    oneLine.replaceChildren(
      h("p", {}, "On Linux or macOS, in a terminal:"),
      copyBlock(installCmd(args), "Command copied", { pre: true }),
      h("p", {}, "On Windows, in PowerShell (needs ", h("code", {}, "winget install Git.Git"), "):"),
      copyBlock(installCmdWin(psArgs), "Command copied", { pre: true }),
      h("p", { class: "hint" }, "It installs Dash", isRelease(runnerInfo.version) ? ` ${runnerInfo.version}` : "", " and starts the runner as a service. No sudo or admin rights. ",
        h("a", { href: "https://github.com/klahr/dash/blob/main/docs/several-computers.md#add-a-runner", target: "_blank", rel: "noopener" }, "What it does")));
    let flags = "";
    if (n) flags += ` --name ${shq(n)}`;
    if (d) flags += ` --projects ${shq(d)}`;
    const rel = isRelease(runnerInfo.version) ? `https://github.com/klahr/dash/releases/tag/${runnerInfo.version}` : "https://github.com/klahr/dash/releases/latest";
    manual.replaceChildren(h("ol", { class: "steps" },
      h("li", {}, h("strong", {}, "Get Dash. "), "Download and unpack ", h("a", { href: rel, target: "_blank", rel: "noopener" }, "the release"), " for its system. It needs git, and claude logged in."),
      h("li", {}, h("strong", {}, "Register and run it:"), copyBlock(`dash runner --server ${runnerInfo.hub} --code ${code}${flags}`, "Command copied", { pre: true }),
        h("p", { class: "hint" }, "Later, just ", h("code", {}, "dash runner"), ".")),
      h("li", {}, h("strong", {}, "Optionally, keep it running in the background:"), copyBlock("dash runner install", "Command copied", { pre: true }),
        h("p", { class: "hint" }, h("code", {}, "dash runner status"), " shows how it's doing."))));
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
      h("summary", {}, "Name and projects directory (optional)"),
      h("div", { class: "runner-add-fields" },
        h("label", { class: "field" }, h("span", {}, "Name"), name),
        h("label", { class: "field" }, h("span", {}, "Projects directory"), dir))),
    h("div", { class: "runner-add-foot" },
      h("p", { class: "runner-add-status", id: "runner-add-status", role: "status" }, "Waiting for the runner to connect…"),
      h("button", { class: "btn small", type: "button", onclick: closeRunnerAdd }, "Cancel")),
    h("p", { class: "hint" }, `The code works once, for ${mins} minutes. The runner is yours.`));
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
// id, when given, opens that runner's page instead of the overview.
export async function openRunners(id) {
  runnerAdd = null;
  $("#runners-code").replaceChildren();
  closeRunnerPanels();
  shown = typeof id === "string" ? id : "overview";
  $("#runners-main").scrollTop = 0;
  await renderRunners();
  loadRunnerTokens().catch(() => { $("#runners-tokens").hidden = true; });
  const d = $("#runners-sheet");
  if (!d.open) d.showModal();
  changed("push");
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
    art("/img/empty-board.webp", 480, 360),
    h("p", { class: "empty-title" }, "Welcome to Dash"),
    h("p", {}, "Claude does your tasks on ", h("strong", {}, "runners"), ", with git and Claude Code logged in. Add one to start."),
    h("ol", { class: "steps welcome-steps" },
      h("li", {}, h("strong", {}, "Add a runner. "), "Paste its install command on Linux, macOS or Windows. No open ports needed."),
      h("li", {}, h("strong", {}, "Create a project. "), "A git repository; its labels pick its runners."),
      h("li", {}, h("strong", {}, "Add tasks. "), "Each runs in its own worktree and comes back for review.")),
    h("div", { class: "empty-actions" },
      ib({ class: "btn primary", onclick: attempt(openAddRunner) }, "plus", "Add a runner"),
      ib({ class: "btn", onclick: openNewProject }, "plus", "Add project")),
  ];
}
