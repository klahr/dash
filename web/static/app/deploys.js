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
};
const ACTIVE = ["building", "deploying", "verifying"];
const TRIGGER = { user: "", merge: "after a merge", release: "for a release", goal: "for a goal" };

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

// An environment's card: what is live, what is on its way, and Deploy.
function envCard(e) {
  const live = e.live, active = e.active;
  const ahead = Object.entries(e.ahead || {}).map(([other, n]) => `${plural(n, "commit")} ahead of ${other}`);
  const url = live?.url || e.url;
  return h("li", { class: `env-card${active ? " busy" : ""}`, "data-env": e.name },
    h("div", { class: "env-head" },
      h("h3", { class: "env-name" }, e.name),
      url ? h("a", { class: "env-url", href: url, target: "_blank", rel: "noopener" }, url.replace(/^https?:\/\//, "")) : null,
      ib({ type: "button", class: "btn small primary env-deploy", title: `Deploy the tip of ${state.project?.default_branch || "the default branch"} to ${e.name}`,
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
        d.by_name ? h("span", {}, `by ${d.by_name}`) : null,
        TRIGGER[d.trigger] ? h("span", {}, TRIGGER[d.trigger]) : null,
        d.runner ? h("span", {}, `on ${d.runner}`) : null,
        took(d) ? h("span", {}, took(d)) : null,
        d.shipped?.length ? h("span", {}, plural(d.shipped.filter((x) => x.kind === "task").length, "task")) : null)))));
  if (focused) $(`#deploy-list li[data-id="${focused}"] .deploy-open`)?.focus({ preventScroll: true });
}

// deployTo queues a deploy of the default branch's tip (or of commit).
async function deployTo(env, commit = "") {
  const p = dp.project || state.project;
  const d = await api("POST", `/api/projects/${p.id}/deploys`, { environment: env, commit }, prefix());
  mergeDeploy(d);
  toast(`Deploy to ${env} queued`, { action: { label: "Open", run: () => openDeploy(d.id).catch(fail) } });
}

// ------------------------------------------------------------------ live updates
// deploy.updated carries the deploy without its log: the list takes it, the
// environments are read again, and the open deploy's log with it.
let envTimer = 0, detailTimer = 0;
export function onDeployEvent(ev) {
  const d = ev.data;
  if (!d?.id || dp.project?.id !== d.project_id) return;
  mergeDeploy(d);
  clearTimeout(envTimer);
  envTimer = setTimeout(() => state.view === "deploys" && loadEnvs().catch(() => {}), 300);
  if (dp.open === d.id) {
    clearTimeout(detailTimer);
    detailTimer = setTimeout(() => loadDeploy(d.id).catch(() => {}), 200);
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
    d.by_name ? h("span", {}, `by ${d.by_name}${TRIGGER[d.trigger] ? ` ${TRIGGER[d.trigger]}` : ""}`) : null);
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
  ].filter(Boolean).flat());
  renderPhases(d);
  renderShipped(d);
  const log = $("#dd-log");
  const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 8;
  log.textContent = d.log || (d.status === "queued" ? "Waiting for its turn…" : ACTIVE.includes(d.status) ? "Starting…" : "No output.");
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
  fill($("#dd-actions"),
    d.status === "queued" ? btn("x", "Cancel", () => stopDeploy(d), "btn small danger", "Take it off the queue") : null,
    running ? btn("stop", "Stop", () => stopDeploy(d), "btn small danger", "Stop its script on the runner") : null,
    !running && d.status !== "queued" && d.commit ? btn("redo", "Deploy again", () => deployTo(d.environment, d.commit), "btn small", `Deploy ${short(d.commit)} to ${d.environment} again`) : null,
    btn("link", "Copy link", () => copy(linkTo({ view: "deploys", deploy: d.id }, state.project), "Link copied"), "btn small", "Copy a link that opens this deploy"));
}

async function stopDeploy(d) {
  const queued = d.status === "queued";
  if (!queued && !await ask({ title: `Stop the deploy to ${d.environment}?`, body: "Its script is stopped where it runs, part way through: the environment may be left half deployed.", ok: "Stop deploy", danger: true })) return;
  await api("DELETE", `/api/deploys/${d.id}`, null, prefix());
  toast(queued ? "Deploy cancelled" : "Stopping the deploy");
}

$("#deploy-settings").addEventListener("click", () => openDeploySettings());
