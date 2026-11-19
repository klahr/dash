import { $, $$, dateTimeText, fill, h, ib, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, busy, copy, fail, toast } from "./dialogs.js";
import { detailPane } from "./detailpane.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { agoText } from "./branches.js";
import { detailBackground, openDetail } from "./detail.js";
import { openSettings } from "./settings.js";
import { changed, linkTo } from "./routes.js";

// ------------------------------------------------------------------ deploys
// The Deploys page: one card per environment of the project (what is live
// there, since when, how far it is ahead of the others, what is on its way)
// and the deploys, newest first. A deploy opens in a pane beside the list
// (full screen with Back on a phone), like a History commit; Esc closes it.
// The project's own scripts do the deploying, on a runner; Dash records
// what is live where (docs/deploys.md).
export const DEPLOY_STATUS = {
  queued: "Queued", building: "Building", deploying: "Deploying", verifying: "Verifying",
  live: "Live", failed: "Failed", rolled_back: "Rolled back", superseded: "Replaced",
  awaiting_approval: "Awaiting approval", rejected: "Rejected",
};
const ACTIVE = ["building", "deploying", "verifying"];
const TRIGGER = { user: "", merge: "after a merge", release: "for a release", goal: "for a goal", auto_rollback: "automatically, its checks failed", agent: "by Claude" };
const ENV_TRIGGER = { merge: "Deploys each merge to the default branch", release: "Deploys each release tag" };

// startedBy says who or what started deploy d: "by ana", "by Claude for
// ana", "after a merge", "for release 1.4.0".
export function startedBy(d) {
  switch (d.trigger) {
    case "agent": return d.by_name ? `by Claude for ${d.by_name}` : "by Claude";
    case "release": return `for release ${d.trigger_ref || ""}`.trim() + (d.by_name ? `, tagged by ${d.by_name}` : "");
    case "merge": return "after a merge";
  }
  return [d.by_name ? `by ${d.by_name}` : "", TRIGGER[d.trigger] || ""].filter(Boolean).join(" ");
}
const CHECK = { health: "Health check", verify: "Verify command", soak: "Soak" };

export const dp = {
  project: null, // the project the page is of
  envs: [],      // its environments, as GET /environments has them
  list: [],      // its deploys, newest first, without their logs
  loaded: false,
  get open() { return state.deployOpen || null; }, // in state: nav.js may close it before this module has run
  set open(v) { state.deployOpen = v; },
  detail: null,  // the open deploy, with its log
  error: "",
};

const prefix = () => dp.project?.prefix || "";
const short = (sha) => (sha || "").slice(0, 7);

// loadDeploys reads the project's environments and deploys.
export async function loadDeploys() {
  const p = state.project;
  if (!p) return resetDeploys();
  const [envs, list] = await Promise.all([
    api("GET", `/api/projects/${p.id}/environments`, null, p.prefix || ""),
    api("GET", `/api/projects/${p.id}/deploys`, null, p.prefix || ""),
  ]);
  if (projectKey(state.project) !== projectKey(p)) return;
  dp.project = p;
  dp.envs = envs || [];
  dp.list = list || [];
  dp.loaded = true;
  renderDeploys();
  if (dp.open && !dp.list.some((d) => d.id === dp.open)) closeDeploy();
}

function resetDeploys() {
  closeDeploy();
  dp.project = state.project;
  dp.envs = [];
  dp.list = [];
  dp.loaded = false;
  renderDeploys();
}

// ------------------------------------------------------------------ the page
export function renderDeploys() {
  const p = state.project;
  const none = !p || (dp.loaded && !dp.envs.length);
  $("#deploy-empty").hidden = !none;
  $("#deploy-empty-title").textContent = p ? "No environments yet" : "No project";
  fill($("#deploy-empty-text"), p
    ? ["An environment is a place the project is deployed to, such as production or staging, with the script that deploys it. Add one in ",
      h("button", { type: "button", class: "link inline", onclick: () => openDeploySettings() }, "Project settings → Deploy"), "."]
    : "Select a project to see its deploys.");
  $("#deploy-settings").hidden = !p;
  renderEnvCards();
  renderList();
}

function openDeploySettings() {
  openSettings();
  $('#settings-sheet .rail-item[data-rail="deploy"]')?.click();
}

// An environment's card: what is live, what is on its way, Deploy and
// Promote (when the caller's role allows them and nothing blocks them), what
// a deploy needs, and the deploys waiting for an approval.
function envCard(e) {
  const live = e.live, active = e.active, pol = e.policy || {};
  const blocked = pol.blocked || [];
  const deployTitle = !pol.can_deploy ? `Only ${ROLE_WORD[e.deploy_role] || "some roles"} may deploy to ${e.name}`
    : blocked.length ? blocked.join("; ")
    : `Deploy the tip of ${state.project?.default_branch || "the default branch"} to ${e.name}${pol.needs?.some((n) => n.startsWith("approval")) ? " (once approved)" : ""}`;
  const promote = live && pol.promote_to ? ib({ type: "button", class: "btn small", disabled: pol.promote_blocked?.length ? true : null,
    title: pol.promote_blocked?.length ? pol.promote_blocked.join("; ") : `Deploy ${short(live.commit)}${live.artifact ? ` (artifact ${live.artifact})` : ""} to ${pol.promote_to}, with no build`,
    onclick: attempt((ev) => busy(ev.currentTarget, () => promoteTo(e.name, pol.promote_to, live))) }, "push", `Promote to ${pol.promote_to}`) : null;
  const ahead = Object.entries(e.ahead || {}).map(([other, n]) => `${plural(n, "commit")} ahead of ${other}`);
  const url = live?.url || e.url;
  return h("li", { class: `env-card${active ? " busy" : ""}`, "data-env": e.name },
    h("div", { class: "env-head" },
      h("h3", { class: "env-name" }, e.name),
      url ? h("a", { class: "env-url", href: url, target: "_blank", rel: "noopener" }, url.replace(/^https?:\/\//, "")) : null,
      ib({ type: "button", class: "btn small primary env-deploy", title: deployTitle, disabled: !pol.can_deploy || blocked.length ? true : null,
        onclick: attempt((ev) => busy(ev.currentTarget, () => deployTo(e.name))) }, "rocket", "Deploy")),
    live
      ? h("button", { type: "button", class: "env-live", onclick: () => openDeploy(live.id).catch(fail), title: "Open the deploy that is live" },
        h("span", { class: "status d-live" }, "Live"),
        h("code", {}, short(live.commit)),
        h("span", { class: "env-subject" }, live.subject || ""),
        h("span", { class: "env-since", title: dateTimeText(liveSince(live)) }, `since ${agoText(liveSince(live))}`))
      : h("p", { class: "hint" }, "Nothing deployed here yet."),
    live?.version ? h("p", { class: "env-line" }, "Version ", h("code", {}, live.version)) : null,
    ahead.length ? h("p", { class: "env-line" }, `${e.name} is ${ahead.join(", ")}.`) : null,
    active ? h("button", { type: "button", class: "env-line env-active", onclick: () => openDeploy(active.id).catch(fail) },
      h("span", { class: "act-spin", "aria-hidden": "true" }), `${DEPLOY_STATUS[active.status]} ${short(active.commit)}…`) : null,
    e.queued ? h("p", { class: "env-line hint" }, `${plural(e.queued, "deploy")} queued`) : null,
    ENV_TRIGGER[e.trigger] ? h("p", { class: "env-line hint" }, `${ENV_TRIGGER[e.trigger]}.`) : null,
    pol.awaiting?.length ? h("ul", { class: "env-line env-awaiting", "aria-label": `Deploys to ${e.name} awaiting approval` }, ...pol.awaiting.map((d) => h("li", {},
      h("button", { type: "button", class: "link", onclick: () => openDeploy(d.id).catch(fail) }, statusTag(d)),
      h("code", {}, short(d.ref || d.commit) || "tip"),
      h("span", { class: "env-subject hint" }, startedBy(d)),
      ...approveButtons(d, pol)))) : null,
    promote ? h("div", { class: "env-line env-actions" }, promote) : null,
    pol.frozen ? h("p", { class: "env-line warn" }, pol.frozen) : null,
    blocked.filter((b) => b !== pol.frozen).map((b) => h("p", { class: "env-line hint" }, b)),
    pol.needs?.length ? h("p", { class: "env-line hint" }, `Deploys need ${pol.needs.join(", ")}.`) : null,
    e.rollback ? h("div", { class: "env-line env-rollback" },
      rollbackButton(e.name, e.rollback.of, e.rollback.to)) : null,
    e.runners?.length ? h("p", { class: "env-line hint" }, "Runs on ", e.runners.map((r) => `${r.name}${r.online ? "" : " (offline)"}`).join(", ")) : null,
    e.warning ? h("p", { class: "env-line hint warn" }, e.warning) : null);
}

function renderEnvCards() {
  $("#env-cards").replaceChildren(...dp.envs.map(envCard));
}

const liveSince = (d) => (d.phases || []).find((ph) => ph.status === "live")?.at || d.finished_at || d.created_at;

// How long a deploy took (or has taken), e.g. "1m 20s".
function took(d) {
  if (!d.started_at) return "";
  const end = d.finished_at ? new Date(d.finished_at) : new Date();
  const s = Math.max(0, Math.round((end - new Date(d.started_at)) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

const statusTag = (d) => h("span", { class: `status d-${d.status}` },
  ACTIVE.includes(d.status) ? h("span", { class: "act-spin", "aria-hidden": "true" }) : null,
  DEPLOY_STATUS[d.status] || d.status);

function renderList() {
  const list = $("#deploy-list");
  const focused = document.activeElement?.closest?.("#deploy-list li")?.dataset.id;
  $("#deploy-list-head").hidden = !dp.list.length;
  list.replaceChildren(...dp.list.map((d) => h("li", { class: `deploy${d.id === dp.open ? " selected" : ""}`, "data-id": d.id },
    h("button", { type: "button", class: "deploy-open", "aria-controls": "deploy-detail", "aria-current": d.id === dp.open ? "true" : null, onclick: () => openDeploy(d.id).catch(fail) },
      h("span", { class: "deploy-row" }, statusTag(d), h("span", { class: "deploy-env" }, d.environment),
        d.commit ? h("code", {}, short(d.commit)) : h("span", { class: "hint" }, d.ref ? short(d.ref) : "tip"),
        h("span", { class: "deploy-subject" }, d.subject || "")),
      h("span", { class: "deploy-sub" },
        h("time", { datetime: d.created_at, title: dateTimeText(d.created_at) }, agoText(d.created_at)),
        startedBy(d) ? h("span", {}, startedBy(d)) : null,
        d.runner ? h("span", {}, `on ${d.runner}`) : null,
        took(d) ? h("span", {}, took(d)) : null,
        d.shipped?.length ? h("span", {}, plural(d.shipped.filter((x) => x.kind === "task").length, "task")) : null)))));
  if (focused) $(`#deploy-list li[data-id="${focused}"] .deploy-open`)?.focus({ preventScroll: true });
}

// rollbackButton offers "Roll back to <commit>": a rollback of deploy of
// (live, or deployed and failed) to the last good deploy before it.
function rollbackButton(env, of, to) {
  return ib({ type: "button", class: "btn small", title: `Deploy ${short(to.commit)}${to.subject ? ` (${to.subject})` : ""} to ${env} again, with DASH_ROLLBACK=1`,
    onclick: attempt((ev) => busy(ev.currentTarget, () => rollBack(env, of, to))) }, "undo", `Roll back to ${short(to.commit)}`);
}

async function rollBack(env, of, to) {
  if (!await ask({ title: `Roll ${env} back to ${short(to.commit)}?`,
    body: `${to.subject ? `“${to.subject}”, ` : ""}live ${agoText(liveSince(to))}, is deployed again${to.artifact ? ` (artifact ${to.artifact})` : ""}, ahead of any queued deploys. Its script gets DASH_ROLLBACK=1.`,
    ok: "Roll back" })) return;
  const d = await api("POST", `/api/deploys/${of}/rollback`, null, prefix());
  mergeDeploy(d);
  toast(`Rollback of ${env} to ${short(to.commit)} queued`, { action: { label: "Open", run: () => openDeploy(d.id).catch(fail) } });
}

const ROLE_WORD = { member: "members", admin: "admins", owner: "owners" };

// deployTo queues a deploy of the default branch's tip (or of commit); one
// whose environment needs an approval waits for it.
async function deployTo(env, commit = "") {
  const p = dp.project || state.project;
  const d = await api("POST", `/api/projects/${p.id}/deploys`, { environment: env, commit }, prefix());
  queued(d, `Deploy to ${env}`);
}

function queued(d, what) {
  mergeDeploy(d);
  const waits = d.status === "awaiting_approval";
  toast(waits ? `It needs an approval from ${ROLE_WORD[d.needs_approval] ? `one of the ${ROLE_WORD[d.needs_approval]}` : "someone"} before it runs.` : "",
    { head: `${what} ${waits ? "awaits approval" : "queued"}`, action: { label: "Open", run: () => openDeploy(d.id).catch(fail) } });
}

// promoteTo deploys what is live on from (its commit and artifact) to to,
// with no build.
async function promoteTo(from, to, live) {
  if (!await ask({ title: `Promote ${short(live.commit)} to ${to}?`,
    body: `What is live on ${from}${live.subject ? `, “${live.subject}”` : ""}, is deployed to ${to} as it is: ${live.artifact ? `its artifact ${live.artifact} goes in $DASH_ARTIFACT` : "no artifact was recorded, so the deploy command gets only the commit"}, and the build command doesn't run.`,
    ok: "Promote" })) return;
  const p = dp.project || state.project;
  const d = await api("POST", `/api/projects/${p.id}/deploys`, { environment: to, promote_from: from }, prefix());
  queued(d, `Promotion to ${to}`);
}

// approveButtons are Approve and Reject for deploy d awaiting approval,
// when the caller's role may approve deploys to its environment.
function approveButtons(d, pol) {
  if (d.status !== "awaiting_approval" || !pol?.can_approve) return [];
  return [
    ib({ type: "button", class: "btn small primary", title: "Queue it: it runs when its turn comes", onclick: attempt((ev) => busy(ev.currentTarget, () => decide(d, true))) }, "check", "Approve"),
    ib({ type: "button", class: "btn small danger", title: "Refuse it: it doesn't run", onclick: attempt((ev) => busy(ev.currentTarget, () => decide(d, false))) }, "x", "Reject"),
  ];
}

async function decide(d, approve) {
  let body = null;
  if (!approve) {
    const note = await ask({ title: `Reject the deploy to ${d.environment}?`, body: "It won't run. Say why, if you like:", input: true, placeholder: "e.g. wait for the release notes", ok: "Reject", danger: true });
    if (note == null) return;
    body = { note };
  }
  const x = await api("POST", `/api/deploys/${d.id}/${approve ? "approve" : "reject"}`, body, prefix());
  mergeDeploy(x);
  toast(approve ? `Deploy to ${d.environment} approved` : `Deploy to ${d.environment} rejected`);
  loadEnvs().catch(() => {});
}

// ------------------------------------------------------------------ live updates
// deploy.updated carries the deploy without its log: the list takes it, the
// environments are read again, and the open deploy's log with it.
let envTimer = 0, detailTimer = 0;
export function onDeployEvent(ev) {
  const d = ev.data;
  if (!d?.id || dp.project?.id !== d.project_id) return;
  const was = dp.list.find((x) => x.id === d.id)?.status;
  if (was && was !== "failed" && d.status === "failed" && !d.cancelled) {
    toast(d.error || "", { tone: "error", head: `${d.rollback ? "Rollback" : "Deploy"} to ${d.environment} failed`, action: { label: "Open", run: () => openDeploy(d.id).catch(fail) } });
  }
  mergeDeploy(d);
  clearTimeout(envTimer);
  envTimer = setTimeout(() => state.view === "deploys" && loadEnvs().catch(() => {}), 300);
  // Another deploy to the same environment may change what the open one offers (Roll back).
  if (dp.open && (dp.open === d.id || dp.detail?.environment === d.environment)) {
    clearTimeout(detailTimer);
    const id = dp.open;
    detailTimer = setTimeout(() => loadDeploy(id).catch(() => {}), 200);
  }
}

function mergeDeploy(d) {
  const i = dp.list.findIndex((x) => x.id === d.id);
  if (i >= 0) dp.list[i] = { ...dp.list[i], ...d };
  else dp.list.unshift(d);
  dp.list.sort((a, b) => b.created_at.localeCompare(a.created_at));
  renderList();
}

async function loadEnvs() {
  const p = dp.project;
  if (!p) return;
  const envs = await api("GET", `/api/projects/${p.id}/environments`, null, p.prefix || "");
  if (dp.project !== p) return;
  dp.envs = envs || [];
  renderDeploys();
}

// ------------------------------------------------------------------ the deploy pane
const deployPane = detailPane({
  pane: () => $("#deploy-detail"),
  closeBtn: () => $("#dd-close"),
  background: () => detailBackground(),
  isOpen: () => !!dp.open,
  rowFor: (id) => $(`#deploy-list li[data-id="${id}"] .deploy-open`),
  onViewport: () => renderDeploy(),
});

export async function openDeploy(id) {
  const pane = $("#deploy-detail");
  if (dp.open === id && !pane.hidden) return;
  dp.open = id;
  dp.detail = dp.list.find((d) => d.id === id) || null;
  dp.error = "";
  deployPane.open();
  markSelected();
  renderDeploy();
  $("#dd-body").scrollTop = 0;
  changed("push");
  await loadDeploy(id);
}

// While the open deploy runs, its log is read again every few seconds.
let pollTimer = 0;
async function loadDeploy(id) {
  clearTimeout(pollTimer);
  try {
    const d = await api("GET", `/api/deploys/${id}`, null, prefix());
    if (dp.open !== id) return;
    dp.detail = d;
    dp.error = "";
  } catch (e) {
    if (dp.open !== id) return;
    dp.error = e.message;
  }
  renderDeploy();
  const d = dp.detail;
  if (d && (ACTIVE.includes(d.status) || d.status === "queued")) pollTimer = setTimeout(() => loadDeploy(id).catch(() => {}), 2000);
}

export function closeDeploy() {
  if (!state.deployOpen) return;
  clearTimeout(pollTimer);
  clearTimeout(detailTimer);
  const id = dp.open;
  dp.open = dp.detail = null;
  changed("close");
  markSelected();
  deployPane.close(id);
}
$("#dd-close").addEventListener("click", closeDeploy);

function markSelected() {
  for (const li of $$("#deploy-list li")) {
    const on = li.dataset.id === dp.open;
    li.classList.toggle("selected", on);
    $(".deploy-open", li)?.toggleAttribute("aria-current", on);
    if (on) li.scrollIntoView({ block: "nearest" });
  }
}

// j/k on the Deploys page: the next or previous deploy, opened.
export function stepDeploy(n) {
  const ids = $$("#deploy-list li").map((li) => li.dataset.id);
  if (!ids.length) return;
  const i = ids.indexOf(dp.open);
  const next = ids[Math.max(0, Math.min(ids.length - 1, i < 0 ? (n > 0 ? 0 : ids.length - 1) : i + n))];
  if (next && next !== dp.open) openDeploy(next).catch(fail);
}

function renderDeploy() {
  const d = dp.detail;
  if (!d || $("#deploy-detail").hidden) return;
  $("#dd-title").textContent = `${d.rollback ? "Rollback" : "Deploy"} to ${d.environment}`;
  fill($("#dd-meta"), statusTag(d),
    d.commit ? h("code", { title: d.commit }, short(d.commit)) : null,
    h("span", { title: dateTimeText(d.created_at) }, `queued ${agoText(d.created_at)}`),
    startedBy(d) ? h("span", {}, startedBy(d)) : null);
  renderDeployActions(d);
  $("#dd-error").hidden = !(dp.error || d.error);
  $("#dd-error").textContent = dp.error || d.error || "";
  const row = (label, ...value) => [h("dt", {}, label), h("dd", {}, ...value)];
  const prev = d.prev_deploy ? h("button", { type: "button", class: "link", onclick: () => openDeploy(d.prev_deploy).catch(fail) }, short(d.prev_commit)) : null;
  fill($("#dd-facts"), ...[
    d.subject ? row("Commit", h("code", {}, short(d.commit)), " ", d.subject) : null,
    prev ? row("Replaces", prev, d.commits ? h("span", { class: "hint" }, ` · ${plural(d.commits, "commit")} since`) : null) : null,
    d.version ? row("Version", h("code", {}, d.version)) : null,
    d.artifact ? row("Artifact", h("code", {}, d.artifact)) : null,
    d.url ? row("URL", h("a", { href: d.url, target: "_blank", rel: "noopener" }, d.url)) : null,
    d.runner ? row("Ran on", d.runner) : null,
    d.exit_code != null ? row("Exit code", String(d.exit_code)) : null,
    d.promoted_from ? row("Promoted from", d.promoted_env || "", " ", openLink(d.promoted_from)) : null,
    d.needs_approval && !d.approval ? row("Needs approval", `from ${ROLE_WORD[d.needs_approval] ? `one of the ${ROLE_WORD[d.needs_approval]}` : d.needs_approval}`) : null,
    d.approval ? row(d.approval.approved ? "Approved by" : "Rejected by", d.approval.by_name || "someone", " ",
      h("span", { class: "hint", title: dateTimeText(d.approval.at) }, agoText(d.approval.at)), d.approval.note ? ` · ${d.approval.note}` : "") : null,
    d.tests_passed ? row("Tests", "passed on this commit") : null,
    d.rollback_of ? row("Rolls back", openLink(d.rollback_of)) : null,
    d.rolled_back_by ? row("Rolled back by", openLink(d.rolled_back_by)) : null,
    (d.trigger === "merge" || d.trigger === "agent") && d.trigger_ref && d.trigger_ref !== "chat"
      ? row(d.trigger === "merge" ? "Merged task" : "For task", h("button", { type: "button", class: "link", onclick: () => openDetail(d.trigger_ref).catch(fail) }, "Open the task")) : null,
    d.fix_task ? row("Fix", h("button", { type: "button", class: "link", onclick: () => openDetail(d.fix_task).catch(fail) }, `Fix failed deploy to ${d.environment}`)) : null,
  ].filter(Boolean).flat());
  renderPhases(d);
  renderChecks(d);
  renderShipped(d);
  const log = $("#dd-log");
  const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 8;
  log.textContent = d.log || (d.status === "queued" ? "Waiting for its turn…" : d.status === "awaiting_approval" ? "Waiting for an approval…"
    : ACTIVE.includes(d.status) ? "Starting…" : "No output.");
  if (atEnd) log.scrollTop = log.scrollHeight;
}

// The phases: when it entered each status, and how long each took.
function renderPhases(d) {
  const ph = d.phases || [];
  $("#dd-phases").replaceChildren(...ph.map((p, i) => {
    const next = ph[i + 1];
    const secs = next ? Math.round((new Date(next.at) - new Date(p.at)) / 1000) : null;
    return h("li", { class: `deploy-phase d-${p.status}` },
      h("span", { class: "deploy-phase-name" }, DEPLOY_STATUS[p.status] || p.status),
      h("time", { datetime: p.at, title: dateTimeText(p.at) }, agoText(p.at)),
      secs != null ? h("span", { class: "hint" }, secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${secs % 60}s`) : null);
  }));
}

// A link that opens deploy id, by its commit when the list has it.
function openLink(id) {
  const x = dp.list.find((d) => d.id === id);
  return h("button", { type: "button", class: "link", onclick: () => openDeploy(id).catch(fail) }, x?.commit ? short(x.commit) : "Open");
}

// How its checks went (health check, verify command, soak), once deployed.
function renderChecks(d) {
  const items = d.checks || [];
  $("#dd-checks-wrap").hidden = !items.length;
  $("#dd-checks").replaceChildren(...items.map((c) => h("li", { class: `deploy-check ${c.ok ? "ok" : "bad"}` },
    h("span", { class: `status ${c.ok ? "d-live" : "d-failed"}` }, c.ok ? "Passed" : "Failed"),
    h("span", { class: "deploy-check-name" }, CHECK[c.name] || c.name),
    c.detail ? h("span", { class: "hint" }, c.detail) : null)));
}

// What it shipped: the tasks and cards the commits since the last deploy name.
function renderShipped(d) {
  const items = d.shipped || [];
  $("#dd-shipped-wrap").hidden = !items.length;
  $("#dd-shipped").replaceChildren(...items.map((x) => h("li", {},
    x.kind === "task"
      ? h("button", { type: "button", class: "link", onclick: () => openDetail(x.id).catch(fail) }, x.title || x.id)
      : h("span", {}, h("span", { class: "scope-tag" }, "card"), " ", x.title || x.id))));
}

function renderDeployActions(d) {
  const btn = (icon, label, run, cls = "btn small", title = "") => ib({ type: "button", class: cls, title, onclick: attempt((e) => busy(e.currentTarget, run)) }, icon, label);
  const running = ACTIVE.includes(d.status);
  const pol = dp.envs.find((e) => e.name === d.environment)?.policy;
  fill($("#dd-actions"),
    ...approveButtons(d, pol),
    d.status === "queued" || d.status === "awaiting_approval" ? btn("x", "Cancel", () => stopDeploy(d), "btn small danger", "Take it off the queue") : null,
    running ? btn("stop", "Stop", () => stopDeploy(d), "btn small danger", "Stop its script on the runner") : null,
    d.rollback_target ? rollbackButton(d.environment, d.id, d.rollback_target) : null,
    !running && !["queued", "awaiting_approval"].includes(d.status) && d.commit && pol?.can_deploy ? btn("redo", "Deploy again", () => deployTo(d.environment, d.commit), "btn small", `Deploy ${short(d.commit)} to ${d.environment} again`) : null,
    btn("link", "Copy link", () => copy(linkTo({ view: "deploys", deploy: d.id }, state.project), "Link copied"), "btn small", "Copy a link that opens this deploy"));
}

async function stopDeploy(d) {
  const queued = d.status === "queued" || d.status === "awaiting_approval";
  if (!queued && !await ask({ title: `Stop the deploy to ${d.environment}?`, body: "Its script is stopped where it runs, part way through: the environment may be left half deployed.", ok: "Stop deploy", danger: true })) return;
  await api("DELETE", `/api/deploys/${d.id}`, null, prefix());
  toast(queued ? "Deploy cancelled" : "Stopping the deploy");
}

$("#deploy-settings").addEventListener("click", () => openDeploySettings());
