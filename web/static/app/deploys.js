import { $, $$, dateTimeText, fill, h, ib, plural, setLabel } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, busy, copy, fail, openSheet, toast } from "./dialogs.js";
import { detailPane } from "./detailpane.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { agoText } from "../time.js";
import { duration } from "./activity.js";
import { detailBackground, openDetail } from "./detail.js";
import { openSettings } from "./settings.js";
import { changed, linkTo } from "./routes.js";
import { openRelease } from "./releases.js";
import { releaseFields, releaseWarnings } from "./releaseform.js";
import { refreshGraph } from "./history.js";
import { estimate, etaText } from "../deployeta.js";

// ------------------------------------------------------------------ deploys
// History → Deploys: one card per environment of the project (what is live
// there, since when, how far it is ahead of the others, what is on its way)
// and the deploys, newest first. A deploy opens in a pane beside the list
// (full screen with Back on a phone), like a History commit; Esc closes it.
// The project's own scripts do the deploying, on a runner; Dash records
// what is live where (docs/deploys.md).
export const DEPLOY_STATUS = {
  queued: "Queued", building: "Building", deploying: "Deploying", verifying: "Verifying",
  live: "Live", failed: "Failed", rolled_back: "Rolled back", superseded: "Replaced",
  awaiting_approval: "Awaiting approval", rejected: "Rejected", waiting_build: "Waiting for build",
};
// Not started yet: may be cancelled (taken off the queue).
const PENDING = ["queued", "awaiting_approval", "waiting_build"];
const ACTIVE = ["building", "deploying", "verifying"];
const TRIGGER = { user: "", merge: "after a merge", release: "for a release", auto_rollback: "automatically, its checks failed", agent: "by Claude" };
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
  build: null,   // { id, build }: the release build the open deploy waits for
  error: "",
};

const prefix = () => dp.project?.prefix || "";
const short = (sha) => (sha || "").slice(0, 7);

// commitLabel is how a deploy's commit reads: "v1.4.0 (a1b2c3d)" when it
// has a release tag, else just the short commit.
export const commitLabel = (d) => {
  const c = short(d.commit || d.ref);
  return d.tag ? (c && d.tag !== c ? `${d.tag} (${c})` : d.tag) : c;
};

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

export function resetDeploys() {
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
  $("#view-deploys .deploys-title").hidden = !dp.envs.length;
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
    : pol.frozen ? pol.frozen
    : `Pick a release tag to deploy to ${e.name}${pol.needs?.some((n) => n.startsWith("approval")) ? " (once approved)" : ""}`;
  const promote = live && pol.promote_to ? ib({ type: "button", class: "btn small", disabled: pol.promote_blocked?.length ? true : null,
    title: pol.promote_blocked?.length ? pol.promote_blocked.join("; ") : `Deploy ${short(live.commit)}${live.artifact ? ` (artifact ${live.artifact})` : ""} to ${pol.promote_to}, with no build`,
    onclick: attempt((ev) => busy(ev.currentTarget, () => promoteTo(e.name, pol.promote_to, live))) }, "push", `Promote to ${pol.promote_to}`) : null;
  const ahead = Object.entries(e.ahead || {}).map(([other, n]) => `${plural(n, "commit")} ahead of ${other}`);
  const url = live?.url || e.url;
  return h("li", { class: `env-card${active ? " busy" : ""}`, "data-env": e.name },
    h("div", { class: "env-head" },
      h("h3", { class: "env-name" }, e.name),
      url ? h("a", { class: "env-url", href: url, target: "_blank", rel: "noopener" }, url.replace(/^https?:\/\//, "")) : null,
      ib({ type: "button", class: "btn small primary env-deploy", title: deployTitle, disabled: !pol.can_deploy || pol.frozen ? true : null,
        onclick: attempt(() => pickTag(e)) }, "rocket", "Deploy…")),
    live
      ? h("button", { type: "button", class: "env-live", onclick: () => openDeploy(live.id).catch(fail), title: "Open the deploy that is live" },
        h("span", { class: "status d-live" }, "Live"),
        h("code", {}, commitLabel(live)),
        h("span", { class: "env-subject" }, live.subject || ""),
        h("span", { class: "env-since", title: dateTimeText(liveSince(live)) }, `since ${agoText(liveSince(live))}`))
      : h("p", { class: "hint" }, "Nothing deployed here yet."),
    live?.version ? h("p", { class: "env-line" }, "Version ", h("code", {}, live.version)) : null,
    ahead.length ? h("p", { class: "env-line" }, `${e.name} is ${ahead.join(", ")}.`) : null,
    active ? h("button", { type: "button", class: "env-line env-active", onclick: () => openDeploy(active.id).catch(fail) },
      h("span", { class: "act-spin", "aria-hidden": "true" }), `${DEPLOY_STATUS[active.status]} ${short(active.commit)}…`) : null,
    e.queued ? h("p", { class: "env-line hint" }, `${plural(e.queued, "deploy")} queued`) : null,
    ...dp.list.filter((d) => d.environment === e.name && d.status === "waiting_build").map((d) => h("p", { class: "env-line" },
      h("button", { type: "button", class: "link", onclick: () => openDeploy(d.id).catch(fail) }, statusTag(d)),
      " ", h("span", { class: "hint" }, `${d.tag || short(d.ref)} deploys once its release build passes`))),
    e.trigger === "merge" && e.release_tags_only ? h("p", { class: "env-line hint warn" }, "Set to deploy each merge, but it only deploys release tags: merges deploy nothing here.")
      : ENV_TRIGGER[e.trigger] ? h("p", { class: "env-line hint" }, `${ENV_TRIGGER[e.trigger]}.`) : null,
    e.release_tags_only ? h("p", { class: "env-line hint" }, "Deploys release tags only.") : null,
    e.trigger === "release" && e.wait_for_build ? h("p", { class: "env-line hint" }, "Waits for the release build to pass before deploying a release tag.") : null,
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

// The cards are rebuilt only where their environment changed: a card that
// keeps its node keeps its focus, and a button busy() disabled stays so.
// A rebuilt card gets back the focus it had, by the focused control's place.
const cardKey = (e) => JSON.stringify([e, dp.list.filter((d) => d.environment === e.name && d.status === "waiting_build")]);
function renderEnvCards() {
  const list = $("#env-cards");
  const had = document.activeElement?.closest?.("#env-cards li.env-card");
  const at = had ? $$("button, a", had).indexOf(document.activeElement) : -1;
  const old = new Map($$(":scope > li.env-card", list).map((li) => [li.dataset.env, li]));
  dp.envs.forEach((e, i) => {
    const key = cardKey(e);
    let li = old.get(e.name);
    if (!li || li.cardKey !== key) {
      const fresh = envCard(e);
      fresh.cardKey = key;
      if (li) li.replaceWith(fresh);
      li = fresh;
      if (had && had.dataset.env === e.name && at >= 0) $$("button, a", li)[at]?.focus({ preventScroll: true });
    }
    old.delete(e.name);
    if (list.children[i] !== li) list.insertBefore(li, list.children[i] || null);
  });
  for (const li of old.values()) li.remove();
}

const liveSince = (d) => (d.phases || []).find((ph) => ph.status === "live")?.at || d.finished_at || d.created_at;

// How long a deploy took (or has taken), e.g. "1m 20s" or "2h 03m".
function took(d) {
  if (!d.started_at) return "";
  const end = d.finished_at ? new Date(d.finished_at) : new Date();
  return duration(end - new Date(d.started_at));
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
        d.commit ? h("code", {}, commitLabel(d)) : h("span", { class: "hint" }, d.ref ? commitLabel(d) : "tip"),
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
  return ib({ type: "button", class: "btn small", "data-act": "rollback", title: `Deploy ${short(to.commit)}${to.subject ? ` (${to.subject})` : ""} to ${env} again, with DASH_ROLLBACK=1`,
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

// deployTo queues a deploy of commit (named tag, if it has one); one whose
// environment needs an approval waits for it.
export async function deployTo(env, commit, { project = dp.project || state.project, tag = "" } = {}) {
  const d = await api("POST", `/api/projects/${project.id}/deploys`, { environment: env, commit }, project.prefix || "");
  if (dp.project?.id === project.id) queued(d, `Deploy of ${tag ? `${tag} (${short(commit)})` : short(commit)} to ${env}`);
  else toast("", { tone: "ok", head: `Deploy of ${tag || short(commit)} to ${env} ${d.status === "awaiting_approval" ? "awaits approval" : "queued"}` });
  return d;
}

// tagDistance says how far release tag t is from what env runs (live: its
// commit): "4 commits ahead of production", "2 commits behind", "same".
export function tagDistance(t, env, live) {
  if (!live) return `nothing is live on ${env} yet`;
  if (!t.compared) return "";
  if (t.commit === live) return `live on ${env} now`;
  if (!t.ahead && !t.behind) return `same as ${env}`;
  if (!t.behind) return `${plural(t.ahead, "commit")} ahead of ${env}`;
  if (!t.ahead) return `${plural(t.behind, "commit")} behind ${env}`;
  return `${plural(t.ahead, "commit")} ahead of and ${t.behind} behind ${env}`;
}

// A release tag's build, as Deploy's picker shows it (GET …/deploys/tags
// gives each tag one; GET …/ci/releases the raw builds, read again while
// one builds).
const BUILD_LABEL = { building: "Building", passed: "Build passed", failed: "Build failed", unknown: "No build found" };
const BUILD_TITLE = {
  building: "Its release workflows are still running: open the runs",
  passed: "Its release workflows passed: open the runs",
  failed: "Its release workflows failed: open the runs",
  unknown: "Dash found no GitHub Actions runs of this tag",
};
const RAW_BUILD = { building: "building", published: "passed", failed: "failed" };

// buildFromRelease is tag build b (GET …/ci/releases) of a tag on commit,
// as …/deploys/tags would say it; was is what was shown before.
export function buildFromRelease(b, commit, was = null) {
  if (!b || (commit && b.sha && b.sha !== commit)) return { state: "unknown" };
  const state = RAW_BUILD[b.state] || "unknown";
  if (state === "unknown") return { state };
  const runs = b.runs || [];
  return { state, runs, url: runs.length === 1 ? runs[0].url : was?.url || runs[0]?.url || "" };
}

// buildBadge shows build (state, url), linked to its runs when it has them.
export function buildBadge(build) {
  const state = build?.state || "unknown";
  const props = { class: `status tag-build b-${state}`, title: BUILD_TITLE[state] || "", "data-build": state };
  return build?.url
    ? h("a", { ...props, href: build.url, target: "_blank", rel: "noopener" }, BUILD_LABEL[state] || state)
    : h("span", props, BUILD_LABEL[state] || state);
}

// sureOfBuild asks before deploying tag, whose build failed or still runs,
// to env; it is true when there is nothing to ask, or they said go.
async function sureOfBuild(tag, build, env) {
  if (build?.state === "failed") {
    return ask({ title: `The release build of ${tag} failed; deploy it to ${env} anyway?`,
      body: "Its release workflows failed on GitHub Actions, so what it publishes (an image, a package) may be missing or broken.", ok: "Deploy anyway", danger: true });
  }
  if (build?.state === "building") {
    return ask({ title: `The release build of ${tag} is still running; deploy it to ${env} now?`,
      body: "Its release workflows haven't finished on GitHub Actions, so what it publishes (an image, a package) may not be there yet.", ok: "Deploy now" });
  }
  return true;
}

// deployCommit is History's "Deploy to…" on commit c ({ sha, short,
// subject }): a picker of the project's environments (those the caller may
// not deploy to, and those that deploy release tags only when c has none,
// are there but unavailable, with why), then a confirmation that names c's
// release tag, or says it has none. The one place to deploy any commit.
export async function deployCommit(c) {
  const p = state.project;
  if (!p) return;
  const body = h("div", { class: "tag-pick" }, h("p", { class: "hint" }, "Loading environments…"));
  const ok = ib({ class: "btn primary", value: "ok", disabled: true }, "rocket", "Next");
  const { form, closed } = openSheet({ title: `Deploy ${c.short} to…`, body: [body], footer: ok });
  let envs, tags;
  try {
    [envs, tags] = await Promise.all([
      api("GET", `/api/projects/${p.id}/environments`, null, p.prefix || ""),
      api("GET", `/api/projects/${p.id}/deploys/tags?commit=${encodeURIComponent(c.sha)}`, null, p.prefix || ""),
    ]);
  } catch (err) {
    fill(body, h("p", { class: "err-text" }, err.message));
    return;
  }
  const tag = tags.commit_tags?.[0] || "";
  const sha = tags.commit || c.sha;
  const why = (e) => {
    const pol = e.policy || {};
    if (!pol.can_deploy) return `only ${ROLE_WORD[e.deploy_role] || "some roles"} may deploy to ${e.name}`;
    if (e.release_tags_only && !tag) return `${e.name} only deploys release tags`;
    return pol.frozen || "";
  };
  const live = (e) => e.live?.commit === sha ? "live there now" : e.live ? `runs ${commitLabel(e.live)}` : "nothing deployed yet";
  const first = (envs || []).findIndex((e) => !why(e));
  fill(body,
    h("p", {}, tag ? ["Release ", h("strong", {}, tag), " ", h("code", {}, short(sha)), c.subject ? ` ${c.subject}` : "", tags.commit_build ? buildBadge(tags.commit_build) : ""]
      : [h("code", {}, short(sha)), ` isn't tagged${c.subject ? ` (${c.subject})` : ""}. Deploy a release tag from History → Deploys when you can.`]),
    envs?.length ? h("fieldset", { class: "field choice" }, h("legend", {}, "Environment"),
      ...envs.map((e, i) => h("label", { class: "radio" },
        h("input", { type: "radio", name: "env", value: e.name, disabled: why(e) ? true : null, checked: i === first ? true : null }),
        h("span", {}, e.name, h("em", {}, why(e) ? `Unavailable: ${why(e)}.` : `${live(e)}${e.policy?.needs?.some((n) => n.startsWith("approval")) ? " · needs an approval" : ""}`)))))
      : h("p", { class: "hint" }, "The project has no environments (Project settings → Deploy)."));
  ok.disabled = first < 0;
  body.querySelector("input:checked")?.focus();
  if ((await closed) !== "ok") return;
  const env = new FormData(form).get("env");
  if (!env) return;
  const risky = ["failed", "building"].includes(tags.commit_build?.state);
  const sure = tag && risky
    ? await sureOfBuild(tag, tags.commit_build, env)
    : tag
    ? await ask({ title: `Deploy ${tag} (${short(sha)}) to ${env}?`, body: c.subject || "", ok: "Deploy" })
    : await ask({ title: `${short(sha)} isn't tagged; deploy it to ${env} anyway?`, body: `${c.subject ? `“${c.subject}”. ` : ""}It has no release tag, so it isn't a release.`, ok: "Deploy anyway" });
  if (!sure) return;
  await deployTo(env, sha, { project: p, tag });
}

// pickTag is Deploy on an environment's card: a picker of what to deploy
// there. The environments are a chain (staging → production: where Promote
// sends, policy.promote_from): on the chain's first one, a new release from
// the tip of origin's branch comes first, when the tip is past the latest
// release tag; on a later one, promoting what is live on the one before
// does, and the new release is still there, lower down, as a direct
// release. Then the release tags, newest first, each with how far it is
// from what the environment runs. A new release is tagged, pushed and
// deployed in one go (Tag 1.4.1 and deploy); where the environment deploys
// each release tag on its own, pushing the tag is the deploy (Tag 1.4.1).
// Only those who may tag (owners, admins) can make one; the others are
// told so, and pick a tag or promote.
async function pickTag(e) {
  const p = dp.project || state.project;
  const body = h("div", { class: "tag-pick" }, h("p", { class: "hint" }, "Fetching release tags…"));
  const ok = ib({ class: "btn primary", value: "ok", disabled: true }, "rocket", "Deploy");
  const { dialog, form, closed } = openSheet({ title: `Deploy to ${e.name}`, body: [body], footer: ok });
  let res, info = null;
  try {
    [res, info] = await Promise.all([
      api("GET", `/api/projects/${p.id}/deploys/tags?environment=${encodeURIComponent(e.name)}`, null, prefix()),
      api("GET", `/api/projects/${p.id}/release?sha=`, null, prefix()).catch(() => null),
    ]);
  } catch (err) {
    fill(body, h("p", { class: "err-text" }, err.message));
    return;
  }
  const pol = e.policy || {};
  const branch = p.default_branch || "main";
  const later = !!pol.promote_from;
  const offer = pol.promote;
  // The tip is past the latest release tag, untagged (a runner on an older
  // Dash doesn't say: then only a first release is offered).
  const fresh = !!info && (Array.isArray(info.sha_tags)
    ? !info.sha_tags.length && (!info.previous || info.since == null || info.since > 0)
    : !info.previous);
  if (!res.tags?.length && !fresh && !offer) {
    fill(body,
      h("p", {}, `${p.name} has no release tags yet${res.format ? ` (tag format ${res.format})` : ""}. Deploys ship release tags: tag one first.`),
      h("p", {}, ib({ type: "button", class: "btn small", onclick: () => {
        dialog.close();
        openRelease({ sha: "", short: branch, subject: `the tip of origin/${branch}` }).catch(fail);
      } }, "tag", "Tag release…")),
      h("p", { class: "hint" }, "To deploy a commit that isn't tagged, open it in History and choose Deploy to…."));
    return;
  }
  const approval = pol.needs?.some((n) => n.startsWith("approval"));
  const triggered = e.trigger === "release";
  const others = dp.envs.filter((x) => x.name !== e.name && x.trigger === "release").map((x) => x.name);
  const pickRadio = (value, { disabled = false } = {}) =>
    h("input", { type: "radio", name: "tag", value, disabled: disabled || null });

  // A new release from the tip of origin's branch.
  let fields = null, release = null;
  if (fresh) {
    const since = !info.previous ? "no release tag yet" : info.since == null ? `new commits since ${info.previous}` : `${plural(info.since, "commit")} since ${info.previous}`;
    const title = later ? `Direct release from origin/${branch}` : `New release from origin/${branch}`;
    const sub = [later ? `hotfix: straight to ${e.name}, skipping ${pol.promote_from}` : "", since, short(info.sha)].filter(Boolean).join(" · ");
    fields = info.can_tag ? releaseFields(info, { rows: 5, onChange: () => sync() }) : null;
    const warnings = releaseWarnings(info, { cantTag: false });
    release = h("div", { class: "tag-pick-release" },
      h("label", { class: "radio tag-pick-row" }, pickRadio("@new", { disabled: !info.can_tag }),
        h("span", {}, h("strong", {}, title), h("em", {}, sub))),
      info.can_tag
        ? h("div", { class: "tag-pick-details", hidden: true },
          warnings.length ? h("ul", { class: "release-warnings" }, ...warnings) : null,
          fields.version, fields.notes,
          h("p", { class: "hint tag-pick-trigger" }),
          others.length ? h("p", { class: "hint" }, `${others.join(", ")} also ${others.length === 1 ? "deploys" : "deploy"} each release tag.`) : null)
        : h("p", { class: "hint tag-pick-cant" }, `${info.cant_tag || "You can't tag releases"}. Deploy a release tag${offer ? " or promote" : ""} instead.`));
  }

  // Promote what is live on the environment before this one.
  const promote = offer ? h("label", { class: "radio tag-pick-row tag-pick-promote" },
    pickRadio("@promote", { disabled: !!offer.blocked?.length }),
    h("span", {}, h("strong", {}, `Promote ${offer.deploy.tag || short(offer.deploy.commit)} from ${offer.from}`),
      h("em", {}, offer.blocked?.length ? `Unavailable: ${offer.blocked.join("; ")}.`
        : [offer.deploy.tag ? short(offer.deploy.commit) : "", `live on ${offer.from} ${agoText(liveSince(offer.deploy))}`, "no build"].filter(Boolean).join(" · ")))) : null;

  const row = (t, i) => h("label", { class: "radio tag-pick-row", "data-tag": t.tag },
    pickRadio(t.tag),
    h("span", {}, t.tag, " ", h("code", { class: "hint" }, short(t.commit)), buildBadge(t.build),
      h("em", {}, [i === 0 ? "newest" : "", tagDistance(t, e.name, res.live)].filter(Boolean).join(" · "))));
  const tags = res.tags?.length
    ? h("fieldset", { class: "field choice tag-pick-list" }, h("legend", { class: later || fresh ? "" : "sr-only" }, "Release tags"), ...res.tags.map(row))
    : null;
  const note = h("p", { class: "hint", hidden: true });
  fill(body,
    later ? [promote, tags, release] : [release, promote, tags],
    approval ? h("p", { class: "hint" }, "It waits for an approval before it runs.") : null,
    note);
  // The first choice that can go: the top of the picker, else a tag.
  const radios = $$('input[name="tag"]', body);
  (radios.find((r) => !r.disabled) || {}).checked = true;

  const picked = () => $('input[name="tag"]:checked', body)?.value || "";
  function sync() {
    const v = picked();
    const details = $(".tag-pick-details", body);
    if (details) details.hidden = v !== "@new";
    note.hidden = true;
    if (v === "@new" && fields) {
      const r = fields.read();
      setLabel(ok, "tag", r.tag ? (triggered ? `Tag ${r.tag}` : `Tag ${r.tag} and deploy`) : "Tag and deploy");
      ok.disabled = !!r.error;
      const t = $(".tag-pick-trigger", body);
      t.hidden = !triggered;
      t.textContent = triggered ? `${e.name} deploys each release tag on its own: pushing ${r.tag || "the tag"} deploys it there, once.` : "";
    } else if (v === "@promote") {
      setLabel(ok, "push", `Promote to ${e.name}`);
      ok.disabled = false;
      note.hidden = false;
      note.textContent = `What is live on ${offer.from} is deployed as it is: ${offer.deploy.artifact ? `its artifact ${offer.deploy.artifact}` : "the same commit"}, and the build command doesn't run.`;
    } else {
      setLabel(ok, "rocket", "Deploy");
      ok.disabled = !v;
    }
  }
  body.addEventListener("change", (ev) => { if (ev.target.name === "tag") sync(); });
  sync();
  $('input[name="tag"]:checked', body)?.focus();
  followBuilds(p, res.tags || [], body, dialog);

  // A new release stays open while the tag is pushed; the rest close first.
  form.addEventListener("submit", attempt(async (ev) => {
    if (ev.submitter?.value !== "ok" || picked() !== "@new") return;
    ev.preventDefault();
    const r = fields.read();
    if (r.error) return;
    await busy(ok, async () => {
      const out = await api("POST", `/api/projects/${p.id}/tags`, { sha: info.sha, version: r.tag, notes: r.notes, deploy: e.name }, prefix());
      dialog.close("done");
      released(e.name, out);
    }, { label: `Pushing ${r.tag}…` });
  }));
  if ((await closed) !== "ok") return;
  const v = picked();
  if (v === "@promote") {
    const d = await api("POST", `/api/projects/${p.id}/deploys`, { environment: e.name, promote_from: offer.from }, prefix());
    queued(d, `Promotion of ${offer.deploy.tag || short(offer.deploy.commit)} to ${e.name}`);
    return;
  }
  const t = res.tags?.find((x) => x.tag === v);
  if (t && await sureOfBuild(t.tag, t.build, e.name)) await deployTo(e.name, t.commit, { project: p, tag: t.tag });
}

// followBuilds reads the release builds again every few seconds while the
// picker is open and one of its tags builds, and updates their badges (and
// tags, so the confirmation sees the news).
export const BUILD_POLL = { ms: 10000 };
function followBuilds(p, tags, body, dialog) {
  let timer = 0;
  const building = () => tags.some((t) => t.build?.state === "building");
  const next = () => { if (dialog.open && building()) timer = setTimeout(poll, BUILD_POLL.ms); };
  async function poll() {
    let list;
    try {
      list = await api("GET", `/api/projects/${p.id}/ci/releases?limit=100`, null, p.prefix || "");
    } catch {
      return next();
    }
    if (!dialog.open) return;
    const byTag = new Map((list || []).map((b) => [b.tag, b]));
    for (const t of tags) {
      if (t.build?.state !== "building") continue;
      t.build = buildFromRelease(byTag.get(t.tag), t.commit, t.build);
      $(`.tag-pick-row[data-tag="${CSS.escape(t.tag)}"] .tag-build`, body)?.replaceWith(buildBadge(t.build));
    }
    next();
  }
  dialog.addEventListener("close", () => clearTimeout(timer), { once: true });
  next();
}

// released tells how a release made from Deploy's picker went: tagged, and
// deployed to env (by the request, or by env's own release trigger), or
// tagged but not deployed, and why.
function released(env, out) {
  refreshGraph();
  if (out.deploy) mergeDeploy(out.deploy);
  loadEnvs().catch(() => {});
  const open = out.deploy ? { label: "Open", run: () => openDeploy(out.deploy.id).catch(fail) } : null;
  if (out.deploy_error) {
    toast(out.deploy_error, { tone: "error", head: `Tagged ${out.tag}; not deployed to ${env}`, action: open });
    return;
  }
  const waits = out.deploy?.status === "awaiting_approval";
  toast(out.triggered ? `${env} deploys each release tag, so its trigger deploys ${out.tag}.` : "",
    { tone: "ok", icon: "tag", head: `Tagged ${out.tag}; deploy to ${env} ${waits ? "awaits approval" : "queued"}`, action: open });
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
    ib({ type: "button", class: "btn small primary", "data-act": "approve", title: "Queue it: it runs when its turn comes", onclick: attempt((ev) => busy(ev.currentTarget, () => decide(d, true))) }, "check", "Approve"),
    ib({ type: "button", class: "btn small danger", "data-act": "reject", title: "Refuse it: it doesn't run", onclick: attempt((ev) => busy(ev.currentTarget, () => decide(d, false))) }, "x", "Reject"),
  ];
}

async function decide(d, approve) {
  let body = null;
  if (approve) {
    const what = commitLabel(d) || "the tip";
    if (!await ask({ title: `Approve the deploy of ${what} to ${d.environment}?`, body: `${d.subject ? `“${d.subject}”, ` : ""}${startedBy(d) ? `started ${startedBy(d)}, ` : ""}is queued and deploys when its turn comes.`, ok: "Approve" })) return;
  } else {
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
  refreshGraph(); // History → Commits marks the commits deploys shipped (when it shows)
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
    if (d.status === "waiting_build" && d.tag) {
      const b = await api("GET", `/api/projects/${d.project_id}/ci/releases?tag=${encodeURIComponent(d.tag)}`, null, prefix()).catch(() => null);
      if (dp.open !== id) return;
      dp.build = { id, build: buildFromRelease(b, d.ref) };
    }
    dp.error = "";
  } catch (e) {
    if (dp.open !== id) return;
    dp.error = e.message;
  }
  renderDeploy();
  const d = dp.detail;
  if (d && (ACTIVE.includes(d.status) || d.status === "queued")) pollTimer = setTimeout(() => loadDeploy(id).catch(() => {}), 2000);
  else if (d?.status === "waiting_build") pollTimer = setTimeout(() => loadDeploy(id).catch(() => {}), BUILD_POLL.ms);
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

// j/k on History → Deploys: the next or previous deploy, opened.
export function stepDeploy(n) {
  const ids = $$("#deploy-list li").map((li) => li.dataset.id);
  if (!ids.length) return;
  const i = ids.indexOf(dp.open);
  const next = ids[Math.max(0, Math.min(ids.length - 1, i < 0 ? (n > 0 ? 0 : ids.length - 1) : i + n))];
  if (next && next !== dp.open) openDeploy(next).catch(fail);
}

let logShown = null; // the deploy whose log (not a placeholder) #dd-log shows
function renderDeploy() {
  const d = dp.detail;
  if (!d || $("#deploy-detail").hidden) return;
  $("#dd-title").textContent = `${d.rollback ? "Rollback" : "Deploy"} to ${d.environment}`;
  fill($("#dd-meta"), statusTag(d),
    d.commit ? h("code", { title: d.commit }, commitLabel(d)) : null,
    h("span", { title: dateTimeText(d.created_at) }, `queued ${agoText(d.created_at)}`),
    startedBy(d) ? h("span", {}, startedBy(d)) : null);
  renderDeployActions(d);
  $("#dd-error").hidden = !(dp.error || d.error);
  $("#dd-error").textContent = dp.error || d.error || "";
  const row = (label, ...value) => [h("dt", {}, label), h("dd", {}, ...value)];
  const prev = d.prev_deploy ? h("button", { type: "button", class: "link", onclick: () => openDeploy(d.prev_deploy).catch(fail) }, short(d.prev_commit)) : null;
  fill($("#dd-facts"), ...[
    d.commit || d.subject ? row("Commit", h("code", { title: d.commit || "" }, commitLabel(d)), " ", d.subject || "") : null,
    prev ? row("Replaces", prev, d.commits ? h("span", { class: "hint" }, ` · ${plural(d.commits, "commit")} since`) : null) : null,
    d.version ? row("Version", h("code", {}, d.version)) : null,
    d.artifact ? row("Artifact", h("code", {}, d.artifact)) : null,
    d.url ? row("URL", h("a", { href: d.url, target: "_blank", rel: "noopener" }, d.url)) : null,
    d.runner ? row("Ran on", d.runner) : null,
    d.exit_code != null ? row("Exit code", String(d.exit_code)) : null,
    d.promoted_from ? row("Promoted from", d.promoted_env || "", " ", openLink(d.promoted_from)) : null,
    d.status === "waiting_build" ? row("Release build", dp.build?.id === d.id ? buildBadge(dp.build.build) : h("span", { class: "hint" }, "…"),
      h("span", { class: "hint" }, " · it deploys once this passes")) : null,
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
  const text = d.log || (d.status === "queued" ? "Waiting for its turn…" : d.status === "awaiting_approval" ? "Waiting for an approval…"
    : d.status === "waiting_build" ? `Waiting for the release build of ${d.tag || "its tag"} to pass…`
    : ACTIVE.includes(d.status) ? "Starting…" : "No output.");
  // A growing log gets only its new tail, so a selection in it stays.
  const node = log.firstChild;
  if (d.log && logShown === d.id && node?.nodeType === Node.TEXT_NODE && !log.childNodes[1] && d.log.startsWith(node.data)) {
    if (d.log.length > node.data.length) node.appendData(d.log.slice(node.data.length));
  } else if (log.textContent !== text) log.textContent = text;
  logShown = d.log ? d.id : null;
  if (atEnd) log.scrollTop = log.scrollHeight;
  renderEta(d);
}

// While it runs, the Log heading says how long it has left, from the median
// time the environment's earlier successful deploys took to go live.
function renderEta(d) {
  const el = $("#dd-eta");
  const e = estimate(d, dp.list, Date.now());
  el.hidden = !e;
  if (!e) return;
  el.textContent = `· ${etaText(e)}`;
  el.classList.toggle("over", e.over);
  el.title = `Elapsed ${took(d)}; deploys to ${d.environment} usually take ${duration(e.typical)} (median of the last ${plural(e.n, "successful deploy")})`;
}

// The phases: when it entered each status, and how long each took.
function renderPhases(d) {
  const ph = d.phases || [];
  $("#dd-phases").replaceChildren(...ph.map((p, i) => {
    const next = ph[i + 1];
    return h("li", { class: `deploy-phase d-${p.status}` },
      h("span", { class: "deploy-phase-name" }, DEPLOY_STATUS[p.status] || p.status),
      h("time", { datetime: p.at, title: dateTimeText(p.at) }, agoText(p.at)),
      next ? h("span", { class: "hint" }, duration(new Date(next.at) - new Date(p.at))) : null);
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

// The actions are rebuilt only when what they offer changes (not on each
// poll of a running deploy): a focused button keeps its focus, and one
// busy() disabled stays disabled. A rebuild focuses the same action again.
function renderDeployActions(d) {
  const btn = (act, icon, label, run, cls = "btn small", title = "") => ib({ type: "button", class: cls, title, "data-act": act, onclick: attempt((e) => busy(e.currentTarget, run)) }, icon, label);
  const running = ACTIVE.includes(d.status);
  const pol = dp.envs.find((e) => e.name === d.environment)?.policy;
  const el = $("#dd-actions");
  const key = JSON.stringify([d.id, d.status, d.environment, d.commit, d.tag, d.rollback_target, pol?.can_approve, pol?.can_deploy, state.project?.id]);
  if (el.actionsKey === key) return;
  el.actionsKey = key;
  const focused = el.contains(document.activeElement) ? document.activeElement.dataset.act : "";
  fill(el,
    ...approveButtons(d, pol),
    PENDING.includes(d.status) ? btn("cancel", "x", "Cancel", () => stopDeploy(d), "btn small danger", "Take it off the queue") : null,
    running ? btn("stop", "stop", "Stop", () => stopDeploy(d), "btn small danger", "Stop its script on the runner") : null,
    d.rollback_target ? rollbackButton(d.environment, d.id, d.rollback_target) : null,
    !running && !PENDING.includes(d.status) && d.commit && pol?.can_deploy ? btn("again", "redo", "Deploy again", () => deployAgain(d), "btn small", `Deploy ${commitLabel(d)} to ${d.environment} again`) : null,
    btn("link", "link", "Copy link", () => copy(linkTo({ view: "deploys", deploy: d.id }, state.project), "Link copied"), "btn small", "Copy a link that opens this deploy"));
  if (focused) $(`[data-act="${focused}"]`, el)?.focus({ preventScroll: true });
}

// deployAgain deploys d's commit to its environment again, once asked;
// a commit that failed or was rolled back there is asked about plainly.
async function deployAgain(d) {
  const env = d.environment, what = commitLabel(d);
  const live = dp.envs.find((e) => e.name === env)?.live;
  const now = !live ? `Nothing is live on ${env} now.` : live.commit === d.commit ? `It is live on ${env} now.` : `It replaces ${commitLabel(live)}, live on ${env} now.`;
  const subject = d.subject ? `“${d.subject}”. ` : "";
  const bad = { failed: `${what} failed to deploy to ${env}`, rolled_back: `${what} was rolled back on ${env}` }[d.status];
  const sure = bad
    ? await ask({ title: `${bad}; deploy it again anyway?`, body: `${subject}${now}`, ok: "Deploy anyway", danger: true })
    : await ask({ title: `Deploy ${what} to ${env} again?`, body: `${subject}${now}`, ok: "Deploy again", danger: true });
  if (sure) await deployTo(env, d.commit, { tag: d.tag });
}

async function stopDeploy(d) {
  const queued = PENDING.includes(d.status);
  const waiting = { awaiting_approval: "awaits an approval", waiting_build: "waits for its release build" }[d.status];
  const what = commitLabel(d) || "the tip";
  const sure = !queued
    ? await ask({ title: `Stop the deploy to ${d.environment}?`, body: "Its script is stopped where it runs, part way through: the environment may be left half deployed.", ok: "Stop deploy", danger: true })
    : waiting ? await ask({ title: `Cancel the deploy of ${what} to ${d.environment}?`, body: `It ${waiting}; cancelled, it doesn't run.`, ok: "Cancel deploy", danger: true })
    : true;
  if (!sure) return;
  await api("DELETE", `/api/deploys/${d.id}`, null, prefix());
  toast(queued ? "Deploy cancelled" : "Stopping the deploy");
  if (dp.open === d.id) {
    await loadDeploy(d.id);
    if (dp.open === d.id && dp.detail?.id === d.id) mergeDeploy({ ...dp.detail, log: undefined });
  }
  loadEnvs().catch(() => {});
}

$("#deploy-settings").addEventListener("click", () => openDeploySettings());
