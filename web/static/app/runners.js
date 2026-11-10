import { $, h, ib, ico } from "./dom.js";
import { api } from "./api.js";
import { art, ask, attempt, fail, reduceMotion, toast } from "./dialogs.js";
import { state } from "./state.js";
import { refreshMachines } from "./events.js";
import { isHub, loadProjects, needsRunner, openNewProject, renderSideProjects, selectProject } from "./projects.js";
import { ago, renderBoard } from "./branches.js";
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
export const agoPhrase = (iso) => { const a = ago(iso); return a === "now" ? "just now" : `${a} ago`; };

// runnerStatus is what runner r is doing, in a few words, and its tone
// (on, busy, paused, off) for the dot and the card's accent.
function runnerStatus(r) {
  const n = (k) => `${k} task${k === 1 ? "" : "s"}`;
  if (!r.online) return { tone: "off", text: r.last_seen ? `Offline · seen ${agoPhrase(r.last_seen)}` : "Never connected" };
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

function runnerCard(r, { showOwner }) {
  const st = runnerStatus(r);
  const outdated = runnerOutdated(r);
  const claude = r.reported?.claude;
  const claudeWarns = runnerClaudeWarnings(r);
  const patch = (body) => api("PATCH", `/api/runners/${encodeURIComponent(r.id)}`, body);
  const projects = runnerProjects(r);
  const controls = [];
  if (r.can_manage) {
    controls.push(h("label", { class: "runner-parallel", title: "How many tasks the hub sends it at once. Empty: the runner's own -parallel setting." }, "Tasks at once",
      h("input", { type: "number", min: "0", max: "64", inputmode: "numeric", value: r.parallel ? String(r.parallel) : "",
        placeholder: String(r.reported?.parallel || 1), "aria-label": `Tasks ${r.name} runs at once`,
        onchange: attempt(async (e) => { await patch({ parallel: Number(e.target.value) || 0 }); await renderRunners(); }) })));
  }
  controls.push(h("span", { class: "runner-controls-gap" }));
  if (r.mine) {
    controls.push(ib({ class: "btn small", type: "button", disabled: !r.online,
      title: r.online ? `Pick git repositories on ${r.name} to add as its projects` : `${r.name} is offline`,
      onclick: attempt(() => showRunnerRepos(r)) }, "repo", "Add projects…"));
  }
  if (r.can_manage) {
    controls.push(
      ib({ class: "btn small", type: "button", onclick: attempt(async () => {
        await patch({ paused: !r.paused });
        toast(r.paused ? `${r.name} takes tasks again` : `${r.name} paused: it finishes what it runs and takes no new tasks`);
        await renderRunners();
      }) }, r.paused ? "play" : "pause", r.paused ? "Resume" : "Pause"),
      ib({ class: "btn small danger", type: "button", title: `Disconnect ${r.name} and revoke its token`, onclick: attempt(async () => {
        const ok = await ask({ title: `Revoke ${r.name}?`, body: "It's disconnected now, its token stops working and it's unlinked from every project. Tasks it is running fail and can be queued again on another runner. Run the install command with a new code to bring it back.", ok: "Revoke", danger: true });
        if (!ok) return;
        await api("DELETE", `/api/runners/${encodeURIComponent(r.id)}`);
        toast(`${r.name} revoked`);
        await refreshMachines();
      }) }, "trash", "Revoke"));
  }
  return h("li", { class: `runner-card tone-${st.tone}` },
    h("div", { class: "runner-card-head" },
      h("span", { class: "runner-dot", "aria-hidden": "true" }),
      h("span", { class: "runner-name" }, r.name),
      r.version ? h("span", { class: `runner-version${outdated ? " warn" : ""}`, title: outdated ? `Out of date: the hub runs ${runnerInfo.version}` : "Dash version" }, r.version) : null,
      claude ? h("span", { class: `runner-version${claudeWarns.length ? " warn" : ""}`,
        title: claudeWarns.length ? "Claude Code version: its output or flags look different from what Dash expects" : "Claude Code version" },
        `Claude ${claude.version || "?"}`) : null,
      h("span", { class: "runner-state" }, st.text)),
    h("div", { class: "runner-meta" },
      showOwner && r.owner ? h("span", { class: "runner-owner" }, ico("users"), r.owner.username) : null,
      runnerUsageMeters(r)),
    h("div", { class: "runner-projects" },
      projects.length
        ? projects.map((p) => h("button", { class: "runner-project", type: "button", title: `Open ${p.name}`,
          onclick: () => { $("#runners-sheet").close(); selectProject(p); } }, ico("repo"), p.name))
        : h("span", { class: "hint" }, "No projects linked yet. Link one in its project settings", r.mine ? ", or add projects from this computer." : ".")),
    outdated && r.mine ? h("div", { class: "runner-update" },
      h("p", { class: "hint warn" }, `Runs ${r.version}, the hub runs ${runnerInfo.version}. To update it, run this on ${r.name} (it keeps its registration):`),
      copyBlock(installCmd(), "Command copied", { pre: true })) : null,
    claudeWarns.length ? h("div", { class: "runner-update runner-claude-warn", role: "status" },
      h("p", { class: "hint warn" }, `Claude Code${claude.version ? ` ${claude.version}` : ""} on ${r.name} doesn't look the way Dash expects. Tasks may fail or miss usage limits; updating Dash may help:`),
      h("ul", {}, ...claudeWarns.map((w) => h("li", {}, w)))) : null,
    controls.length > 1 ? h("div", { class: "runner-controls" }, ...controls) : null);
}

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

export async function renderRunners() {
  const list = await loadRunners();
  const box = $("#runners-list");
  if (list === null) {
    $("#runners-stats").replaceChildren();
    $("#runners-add").hidden = true;
    box.replaceChildren(h("p", { class: "hint" }, "This Dash doesn't take runners."));
    return;
  }
  // Yours first, then the others'; online before offline; by name.
  const order = (a, b) => (b.online - a.online) || a.name.localeCompare(b.name);
  const mine = list.filter((r) => r.mine).sort(order), others = list.filter((r) => !r.mine).sort(order);
  const group = (title, rs, showOwner) => rs.length ? [
    title ? h("h3", { class: "org-section" }, title) : null,
    h("ul", { class: "runner-cards" }, ...rs.map((r) => runnerCard(r, { showOwner }))),
  ] : [];
  $("#runners-stats").replaceChildren(...(list.length ? runnerStats(list) : []));
  if (!list.length) $(".runners-about").open = true;
  box.replaceChildren(...(list.length ? [
    ...group(others.length ? "Yours" : "", mine, false),
    ...group(mine.length ? `Others in ${state.me?.org?.name || "the organization"}` : "", others, true),
  ].filter(Boolean) : [h("div", { class: "runners-empty" },
    ico("computer"),
    h("p", { class: "empty-title" }, "No runners yet"),
    h("p", { class: "hint" }, "Add a computer with git and Claude Code logged in: you get a one-line install command for its terminal (Linux or macOS)."))]));
  if (runnerAdd) {
    const added = list.find((r) => !runnerAdd.known.has(r.id));
    const st = $("#runner-add-status");
    if (st && added) {
      st.classList.toggle("on", added.online);
      st.replaceChildren(added.online ? `Connected ✓ ${added.name} is online.` : `${added.name} registered, connecting…`);
      if (added.online) {
        runnerAdd = null;
        const close = $("#runner-add-close");
        if (close) { close.textContent = "Done"; close.classList.add("primary"); }
        $("#runners-add").hidden = false;
        if (added.mine) showRunnerRepos(added).catch(fail);
      }
    }
  }
}

// showRunnerRepos lists the git repositories runner r (the caller's own)
// finds in its projects directory, to tick and add as its projects. The
// runner looks; it reports only names, paths, remotes and branches.
async function showRunnerRepos(r) {
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

async function addRunner() {
  $("#runners-repos").hidden = true;
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
      h("h4", {}, "Add a runner"),
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
      h("button", { class: "btn small", id: "runner-add-close", type: "button", onclick: closeRunnerAdd }, "Cancel")),
    h("p", { class: "hint" }, `The code works once, for ${mins} minutes, and makes the runner yours, in ${state.me?.org?.name || "this organization"}.`));
  $("#runners-code").hidden = false;
  $("#runners-add").hidden = true;
}
// closeRunnerAdd puts the "Add a runner" panel away (its unused code just expires).
function closeRunnerAdd() {
  runnerAdd = null;
  $("#runners-code").hidden = true;
  $("#runners-code").replaceChildren();
  $("#runners-add").hidden = false;
}
$("#runners-add").addEventListener("click", attempt(addRunner));

// openRunners shows the organization's runners: what each is doing, and
// adding, pausing, limiting and revoking them.
export async function openRunners() {
  closeRunnerAdd();
  $("#runners-repos").hidden = true;
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
