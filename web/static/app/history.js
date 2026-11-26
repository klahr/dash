import { graphLayout, graphTips, newestCommit, reachable } from "../graphlayout.js";
import { latestOnly } from "../commitnav.js";
import { $, $$, dateTimeText, debounce, fill, h, ib, ico, plural } from "./dom.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { copy, fail, toast } from "./dialogs.js";
import { detailPane } from "./detailpane.js";
import { cp, hist, state, tryState } from "./state.js";
import { baseBranch, projectKey } from "./events.js";
import { detailBackground } from "./detail.js";
import { diffImages, diffLines, isDiffImage, parseDiff } from "./drawmode.js";
import { openRelease } from "./releases.js";
import { DEPLOY_STATUS, deployCommit, dp, loadDeploys, openDeploy } from "./deploys.js";
import { showView } from "./nav.js";
import { changed } from "./routes.js";
import { agoText } from "../time.js";
import { loadRemote } from "./origin.js";
import { closeCommitPreview, commitPreviewState, commitPreviewWhy, previewingCommit, showCommitPreview } from "./commitpreview.js";
import { loadTry, tryAtCommit } from "./try.js";
import { PV_LABEL } from "./preview.js";

// ------------------------------------------------------------------ history
// History → Commits: the default branch's commit graph, a dot per commit on
// coloured lanes, as GUI git clients draw it, each commit with the
// environments it is live in and the other deploys that shipped it. Its
// Deploys tab is deploys.js's.
export async function loadGraph() {
  const p = state.project;
  if (!p) return renderGraph(null);
  // What each environment runs, for its marker, and the deploys, for theirs
  // (none when there are no environments, or it fails).
  const envs = !!p.environments?.length;
  const live = envs ? api("GET", `/api/projects/${p.id}/deploys/live`).catch(() => null) : null;
  const deploys = envs ? loadDeploys().catch(() => {}) : null;
  const res = await api("GET", `/api/projects/${p.id}/graph`);
  hist.live = liveByCommit(await live);
  await deploys;
  if (projectKey(state.project) === projectKey(p)) renderGraph(res);
}

// liveByCommit maps each commit an environment runs to those environments.
function liveByCommit(live) {
  const m = new Map();
  for (const e of live?.environments || []) {
    if (!e.commit) continue;
    if (!m.has(e.commit)) m.set(e.commit, []);
    m.get(e.commit).push(e);
  }
  return m;
}

// liveRefs are the markers of the environments commit hash is live in.
function liveRefs(hash) {
  return (hist.live?.get(hash) || []).map((e) => h("span", { class: "graph-ref env", title: `Live in ${e.environment} since ${dateTimeText(e.since)}` },
    h("span", { class: "sr-only" }, "Live in "), e.environment));
}

// deploysOf are the deploys of commit hash, newest first (the Deploys tab's
// list, when it is this project's).
function deploysOf(hash) {
  if (!hash || dp.project?.id !== state.project?.id) return [];
  return dp.list.filter((d) => d.commit === hash);
}

const DEPLOY_WORD = { superseded: "", live: "" }; // shipped and live once: the environment says it

// deployRefs are the markers of the deploys that shipped commit hash, the
// newest per environment, where it isn't live now (its live marker says so).
function deployRefs(hash) {
  const live = new Set((hist.live?.get(hash) || []).map((e) => e.environment));
  const seen = new Set();
  const out = [];
  for (const d of deploysOf(hash)) {
    if (seen.has(d.environment) || live.has(d.environment)) continue;
    seen.add(d.environment);
    const status = DEPLOY_STATUS[d.status] || d.status;
    const word = d.status in DEPLOY_WORD ? DEPLOY_WORD[d.status] : status.toLowerCase();
    out.push(h("span", { class: `graph-ref deploy d-${d.status}`, title: `${d.rollback ? "Rollback" : "Deploy"} to ${d.environment}: ${status}, ${agoText(d.created_at)}` },
      h("span", { class: "sr-only" }, "Deployed to "), d.environment, word ? ` · ${word}` : ""));
  }
  return out;
}

// showDeploy opens deploy id on History's Deploys tab.
async function showDeploy(id) {
  showView("deploys", { now: true });
  await openDeploy(id);
}

export const refreshGraph = debounce(() => state.view === "history" && loadGraph().catch(() => {}), 800);

// refreshHistory is History's Refresh (and r): with a remote it fetches
// origin now and waits for it, then redraws the graph and says how many
// commits came in; without one it only redraws.
let refreshing = false;
export async function refreshHistory() {
  const p = state.project, btn = $("#graph-refresh");
  if (!p || refreshing) return;
  refreshing = true;
  btn.disabled = true;
  btn.setAttribute("aria-busy", "true");
  const before = new Set(hist.commits.map((c) => c.hash));
  try {
    const st = p.remote_url ? await loadRemote(true, true, { wait: true }) : null;
    if (projectKey(state.project) !== projectKey(p)) return;
    await loadGraph();
    if (projectKey(state.project) !== projectKey(p)) return;
    if (st?.error) { toast(`Can't fetch origin: ${st.error}`, { tone: "error" }); return; }
    const fresh = hist.commits.filter((c) => !before.has(c.hash)).length;
    toast(!p.remote_url ? "History refreshed" : fresh ? `${plural(fresh, "new commit")} from origin` : "Up to date");
  } catch (e) {
    fail(e);
  } finally {
    refreshing = false;
    btn.disabled = false;
    btn.removeAttribute("aria-busy");
  }
}
$("#graph-refresh").addEventListener("click", () => refreshHistory());

const LANE_W = 14;

// A row's lines as an SVG. The upper part has a fixed height (the dot sits
// level with the first line of text); the lower one stretches to the row's
// height however much its subject wraps, keeping its strokes even.
function graphLines(lines, width, stretch) {
  const x = (k) => LANE_W / 2 + k * LANE_W, H = stretch ? 100 : 13;
  const d = lines.map(([a, b, c]) => {
    const path = a === b ? `M${x(a)} 0V${H}` : `M${x(a)} 0C${x(a)} ${H / 2} ${x(b)} ${H / 2} ${x(b)} ${H}`;
    return `<path d="${path}" style="stroke:var(--lane-${c})"/>`;
  }).join("");
  const svg = h("span", { class: stretch ? "graph-down" : "graph-up", "aria-hidden": "true" });
  svg.innerHTML = `<svg viewBox="0 0 ${width * LANE_W} ${H}" preserveAspectRatio="none">${d}</svg>`;
  return svg;
}

export function renderGraph(res) {
  const commits = res?.commits || [];
  $("#graph-empty").hidden = commits.length > 0;
  $("#graph-refresh").hidden = !state.project?.remote_url; // without a remote, r still redraws
  $("#graph-empty-text").textContent = state.project ? `${baseBranch(state.project) || "main"} has no commits yet.` : "Select a project to see its main branch.";
  // Commits origin's tip can't reach haven't been pushed yet: their dots are
  // hollow. Those the local branch's tip can't reach are only on origin
  // (fetched, not in the checkout yet): their dots are faded.
  const branch = res?.branch || baseBranch(state.project) || "main";
  const remote = `origin/${branch}`;
  const { local: localTip, origin: tip } = graphTips(commits, branch);
  const pushed = reachable(commits, tip), local = reachable(commits, localTip);
  const onOriginOnly = (c) => !!(tip && localTip) && !local.has(c.hash);
  const { rows, width } = graphLayout(commits);
  hist.commits = commits.map((c) => ({ ...c, pushed: !tip || pushed.has(c.hash), incoming: onOriginOnly(c) }));
  hist.byHash = new Map(hist.commits.map((c) => [c.hash, c]));
  hist.remote = remote;
  hist.branch = branch;
  // Another project's graph: its commit pane goes.
  if (cp.sha && cp.project !== state.project?.id) closeCommit();
  const graph = $("#graph");
  // A refresh keeps keyboard focus on the commit that had it.
  const focused = document.activeElement?.closest?.("#graph li")?.dataset.hash;
  graph.style.setProperty("--graph-w", `${width * LANE_W}px`);
  graph.replaceChildren(...commits.map((c, i) => {
    const { col, color, up, down } = rows[i];
    const isPushed = !tip || pushed.has(c.hash), incoming = onOriginOnly(c);
    const open = (e) => { if (e.type === "click" || e.key === "Enter" || e.key === " ") { e.preventDefault(); selectCommit(c.hash).catch(fail); } };
    const dot = "graph-dot" + (isPushed ? "" : " unpushed") + (incoming ? " incoming" : "") + (c === tip ? " remote" : "") + (c.parents?.length > 1 ? " merge" : "");
    const selected = c.hash === cp.sha;
    const title = incoming ? INCOMING : isPushed ? null : "Not pushed yet";
    return h("li", { title, "data-hash": c.hash, class: [selected ? "selected" : "", incoming ? "incoming" : ""].join(" ").trim() || null },
      h("span", { class: "graph-lane" },
        graphLines(up, width, false), graphLines(down, width, true),
        h("span", { class: dot, style: `--x:${col * LANE_W + LANE_W / 2}px;--c:var(--lane-${color})` })),
      h("span", { class: "graph-commit", role: "button", tabindex: "0", "aria-controls": "commit-detail", "aria-current": selected ? "true" : null, onclick: open, onkeydown: open },
        h("span", { class: "sr-only" }, "Commit "), h("code", { class: "graph-sha" }, c.short),
        // The hollow dot and the row's title are for the eye and the mouse: say it here too.
        title ? h("span", { class: "sr-only" }, ` (${title.toLowerCase()})`) : null,
        ...(c.refs || []).map((r) => graphRef(r, incoming)),
        ...liveRefs(c.hash),
        ...deployRefs(c.hash),
        h("span", { class: "graph-subject" }, c.subject),
        h("span", { class: "graph-meta" }, `${c.author} · ${c.when}`)));
  }));
  if (focused) $(`#graph li[data-hash="${focused}"] .graph-commit`)?.focus({ preventScroll: true });
  // The open commit may have been pushed (or tagged) since.
  if (cp.sha && !$("#commit-detail").hidden) renderCommitHead();
}

// GitHub's web address of a remote ("" if it isn't on GitHub).
export function githubURL(remote) {
  const m = /github\.com[:/]([^/]+)\/([^/]+?)(?:\.git)?\/?$/.exec(remote || "");
  return m ? `https://github.com/${m[1]}/${m[2]}` : "";
}

// ------------------------------------------------------------------ commit pane
// Selecting a History row shows that commit (full screen on a phone): its
// message, who made it, its parents and the files it changed. On a desktop
// the pane lies over most of the graph, a sliver of which stays visible and
// usable (j/k move the selection and the pane follows); the files are listed
// beside the selected one's diff, and n/p (or ↓/↑ in the list) step through
// them. On a phone each file is a fold whose diff loads when it is opened.
const commitLoad = latestOnly();

const commitAPI = (p, sha) => `/api/projects/${p.id}/commits/${sha}`;

// The modal parts apply on a phone only, where the pane covers everything.
const commitPane = detailPane({
  pane: () => $("#commit-detail"),
  closeBtn: () => $("#cd-close"),
  background: () => detailBackground(), // detail.js imports this module: read it when called
  isOpen: () => !!cp.sha,
  rowFor: (sha) => $(`#graph li[data-hash="${sha}"] .graph-commit`),
  onViewport: () => renderCommit(),
});

// What Ctrl-d/u scroll with a commit open: the diff beside the file list on
// a desktop, the whole pane on a phone.
export const commitScroller = () => desktop.matches ? $("#cd-diff") : $("#cd-body");

// selectCommit shows sha in the pane. wait debounces the request, for
// keys held down; the pane's head shows the graph's row at once meanwhile.
export async function selectCommit(sha, { wait = 0 } = {}) {
  const p = state.project;
  const pane = $("#commit-detail");
  if (!p || (cp.sha === sha && !pane.hidden)) return;
  if (previewingCommit() && !previewingCommit(sha)) closeCommitPreview(); // the pane is that commit's
  // A file list that had focus gets it back once the next commit's files are in.
  cp.listFocus = $("#cd-files").contains(document.activeElement);
  cp.sha = sha;
  cp.project = p.id;
  cp.detail = null;
  cp.error = "";
  cp.file = null;
  cp.texts = new Map();
  cp.msgOpen = false;
  delete $("#cd-diff").dataset.path;
  cp.diffs?.abort();
  cp.diffs = new AbortController();
  markSelectedCommit();
  commitPane.open();
  renderCommit();
  changed("push");
  $("#cd-body").scrollTop = 0;
  $("#cd-diff").scrollTop = 0;
  const res = await commitLoad.run((signal) => api("GET", commitAPI(p, sha), undefined, undefined, { signal }), { wait })
    .catch((e) => ({ error: e }));
  if (res.stale || cp.sha !== sha || projectKey(state.project) !== projectKey(p)) return;
  if (res.error) cp.error = res.error.message;
  else cp.detail = res.value;
  renderCommit();
}

// latestCommit is the hash of the graph's branch's newest commit, as a
// runner fetches it ("" while the graph has none).
export const latestCommit = () => newestCommit(hist.commits, hist.branch)?.hash || "";

// showCommitAt opens History on the commit a preview runs (commitpreview.js)
// and returns its hash: ref "" is the newest commit of the default branch as
// the graph has it, a hash its commit; any other ref, or a hash the graph
// lacks, the newest one too, until the runner says which it checked out.
export async function showCommitAt(ref) {
  const p = state.project;
  if (state.view !== "history") showView("history", { now: true });
  await loadGraph();
  if (projectKey(state.project) !== projectKey(p)) return "";
  const want = /^[0-9a-f]{7,40}$/i.test(ref || "") ? ref.toLowerCase() : "";
  const sha = (want && hist.commits.find((c) => c.hash.startsWith(want))?.hash) || (want.length === 40 ? want : "") ||
    latestCommit() || "";
  if (!sha) throw new Error(`${hist.branch || "main"} has no commits yet.`);
  selectCommit(sha).catch(fail);
  return sha;
}

export function closeCommit() {
  if (!cp.sha) return;
  closeCommitPreview();
  commitLoad.cancel();
  cp.diffs?.abort();
  const sha = cp.sha;
  cp.sha = cp.detail = null;
  changed("close");
  markSelectedCommit();
  commitPane.close(sha);
}
$("#cd-close").addEventListener("click", closeCommit);

function markSelectedCommit() {
  $$("#graph li.selected").forEach((li) => {
    li.classList.remove("selected");
    $(".graph-commit", li)?.removeAttribute("aria-current");
  });
  const li = cp.sha && $(`#graph li[data-hash="${cp.sha}"]`);
  if (!li) return;
  li.classList.add("selected");
  $(".graph-commit", li)?.setAttribute("aria-current", "true");
  li.scrollIntoView({ block: "nearest" });
}

// j/k: the next or previous commit; focus moves along if it was on a row.
export function moveCommit(to) {
  if (!to || to === cp.sha) return;
  const onRow = document.activeElement?.matches?.("#graph .graph-commit");
  selectCommit(to, { wait: 150 }).catch(fail);
  if (onRow) $(`#graph li[data-hash="${to}"] .graph-commit`)?.focus({ preventScroll: true });
}

// The commit as a release, Tag release… and Copy SHA know it.
function commitRef() {
  const c = hist.byHash.get(cp.sha), d = cp.detail;
  return { sha: d?.hash || cp.sha, short: d?.short || c?.short || cp.sha.slice(0, 7), subject: d?.subject ?? c?.subject ?? "" };
}

const INCOMING = "On origin, not in the local checkout yet";

// A ref's chip. origin's is "pushed" while the local branch has it too.
function graphRef(r, incoming) {
  if (r === hist.remote && incoming) return h("span", { class: "graph-ref remote", title: "Origin's newest commit, not in the local checkout yet" }, r);
  return r === hist.remote
    ? h("span", { class: "graph-ref remote", title: "Last pushed commit" }, `pushed · ${r}`)
    : h("span", { class: `graph-ref${r.startsWith("tag: ") ? " tag" : ""}` }, r.replace(/^HEAD -> /, ""));
}

// The pane's head: SHA (click copies), subject, refs and the actions.
function renderCommitHead() {
  const p = state.project;
  const c = hist.byHash.get(cp.sha), d = cp.detail;
  const ref = commitRef();
  $("#cd-title").textContent = ref.subject || ref.short;
  fill($("#cd-meta"),
    h("button", { type: "button", class: "commit-sha", title: `Copy ${ref.sha}`, "aria-label": `Copy the commit's SHA ${ref.sha}`, onclick: () => copy(ref.sha, "SHA copied") }, ref.short),
    ...(d?.refs || c?.refs || []).map((r) => graphRef(r, c?.incoming)),
    ...liveRefs(ref.sha));
  if (!p) return;
  const gh = githubURL(p.remote_url);
  // A commit beyond the loaded graph is older than ones in it: pushed if they are.
  const pushed = c ? c.pushed : hist.commits.at(-1)?.pushed ?? true;
  const why = !p.remote_url ? "Connect a remote first" : !pushed ? "Push first" : "";
  fill($("#cd-actions"),
    ib({ type: "button", class: "btn small", disabled: !!why, title: why || "Tag a release at this commit", onclick: () => openRelease(commitRef()).catch(fail) }, "tag", "Tag release…"),
    why ? h("em", { class: "menu-note" }, why) : null,
    p.environments?.length ? ib({ type: "button", class: "btn small", title: "Deploy this commit to one of the project's environments",
      onclick: () => deployCommit(commitRef()).catch(fail) }, "rocket", "Deploy to…") : null,
    ...tryButtons(p, c, pushed),
    ib({ type: "button", class: "btn small", onclick: () => copy(commitRef().sha, "SHA copied") }, "copy", "Copy SHA"),
    gh ? h("a", { class: "btn small", href: `${gh}/commit/${ref.sha}`, target: "_blank", rel: "noopener" }, ico("external"), "Open on GitHub") : null);
}

// tryButtons are the commit pane's Try at this commit (a CLI project: the
// Shell page's Try terminal at the commit) or Preview (a web project: the app
// at the commit, in the preview pane beside it). Both run on a runner, at a
// commit of the default branch that origin has; elsewhere they're greyed,
// saying why. Without a runner working on the project there are none.
let tryLooked = null; // the project whose Try state History asked for
function tryButtons(p, c, pushed) {
  if (p.prefix) return [];
  if (tryState.pid !== p.id || (!tryState.loaded && tryLooked !== p.id)) {
    tryLooked = p.id;
    loadTry({ any: true }).then(() => { if (cp.sha && state.project?.id === p.id) renderCommitHead(); }).catch(() => {});
    return [];
  }
  if (!tryState.loaded || !tryState.candidates.length) return []; // no runner works on it (or no hub)
  const branch = p.default_branch || "main";
  const commitWhy = hist.branch && hist.branch !== branch ? `Only commits of ${branch} can run: this graph is ${hist.branch}.`
    : !pushed && p.remote_url ? "Push first: the runner checks the commit out from origin." : "";
  const ref = commitRef();
  if (p.preview_kind === "terminal") {
    const why = commitWhy || (!tryState.runners.length ? tryState.disabled || "No runner can run this project." : "");
    return [ib({ type: "button", class: "btn small", disabled: !!why, title: why || `Open a terminal at ${ref.short} in a throwaway worktree on a runner (Shell)`,
      onclick: (e) => tryAtCommit(ref.sha, e.currentTarget).catch(fail) }, "terminal", "Try at this commit")];
  }
  const off = commitPreviewWhy(p);
  if (off === null) return [];
  const why = commitWhy || off;
  const st = commitPreviewState(ref.sha), open = previewingCommit(ref.sha);
  return [h("button", { type: "button", class: `btn small pv-btn${open ? " active" : ""}`, disabled: !!why, "aria-pressed": String(open),
    title: why || (st ? `Preview: ${PV_LABEL[st] || st}` : `Run the app at ${ref.short} in a throwaway worktree on a runner, and preview it here`),
    onclick: (e) => (open ? closeCommitPreview() : showCommitPreview(ref, e.currentTarget).catch(fail)) },
  st ? h("span", { class: `pv-dot ${st}`, "aria-hidden": "true" }) : ico("window"), open ? "Hide preview" : "Preview")];
}
// The commit preview started, stopped, or its pane opened or closed (commitpreview.js).
document.addEventListener("dash:commitpreview", () => { if (cp.sha && !$("#commit-detail").hidden) renderCommitHead(); });

function renderCommit() {
  if (!cp.sha) return;
  renderCommitHead();
  const d = cp.detail;
  $("#cd-error").hidden = !cp.error;
  $("#cd-error").textContent = cp.error;
  renderMessage(d?.body || "");
  const person = (x) => h("span", { title: x.email }, x.name, " ", h("span", { class: "commit-email" }, `<${x.email}>`));
  const when = (x) => h("time", { datetime: x.date, title: agoText(x.date) }, dateTimeText(x.date));
  const parent = (sha) => h("button", { type: "button", class: "link inline commit-parent", title: `Show ${sha}`, onclick: () => selectCommit(sha).catch(fail) },
    hist.byHash.get(sha)?.short || sha.slice(0, 7));
  const sameCommitter = d && d.committer.name === d.author.name && d.committer.email === d.author.email;
  fill($("#cd-facts"), ...(d ? [
    h("dt", {}, "Author"), h("dd", {}, person(d.author), " · ", when(d.author)),
    sameCommitter ? null : h("dt", {}, "Committer"),
    sameCommitter ? null : h("dd", {}, person(d.committer), " · ", when(d.committer)),
    h("dt", {}, d.parents.length > 1 ? "Parents" : "Parent"),
    h("dd", {}, d.parents.length ? d.parents.flatMap((sha, i) => [i ? " " : null, parent(sha)]) : "None (the first commit)"),
    ...deployFacts(d.hash || cp.sha),
  ] : []));
  const head = $("#cd-files-head");
  head.hidden = !d;
  const wide = desktop.matches;
  $("#cd-diff").hidden = !wide || !d?.files.length;
  if (!d) {
    fill($("#cd-files"), cp.error ? null : h("p", { class: "pane-empty" }, "Loading…"));
    return;
  }
  const add = d.files.reduce((n, f) => n + f.add, 0), del = d.files.reduce((n, f) => n + f.del, 0);
  fill(head, `${plural(d.file_count, "file")} changed`,
    d.files.length ? h("span", { class: "counts" }, " ", h("span", { class: "plus" }, `+${add}`), " ", h("span", { class: "minus" }, `−${del}`)) : null);
  fill($("#cd-files"),
    !d.files.length ? h("p", { class: "pane-empty" }, d.parents.length > 1 ? "The merge brought no changes into this branch." : "No files changed.") : null,
    d.files.length ? (wide ? fileList(d.files) : d.files.map((f) => commitFile(f))) : null,
    d.file_count > d.files.length ? h("p", { class: "hint" }, `And ${d.file_count - d.files.length} more files, not listed.`) : null);
  if (!wide || !d.files.length) return;
  // The first file is selected by default (or the one still selected, after a resize).
  if (!d.files.some((f) => f.path === cp.file)) cp.file = d.files[0].path;
  pickFile(cp.file, { focus: cp.listFocus && (!document.activeElement || document.activeElement === document.body) });
  cp.listFocus = false;
}

// deployFacts list the commit's deploys, each opening on History → Deploys
// (where Promote and Roll back are).
function deployFacts(hash) {
  const list = deploysOf(hash);
  if (!list.length) return [];
  return [h("dt", {}, list.length > 1 ? "Deploys" : "Deploy"), h("dd", { class: "commit-deploys" }, ...list.map((x) =>
    h("button", { type: "button", class: "link inline", title: `Open the ${x.rollback ? "rollback" : "deploy"} on History → Deploys`, onclick: () => showDeploy(x.id).catch(fail) },
      h("span", { class: `status d-${x.status}` }, DEPLOY_STATUS[x.status] || x.status), ` ${x.environment} · ${agoText(x.created_at)}`)))];
}

// A long message shows its first lines until "Show all" (desktop only: the
// phone's pane scrolls as a whole).
const LONG_MSG_LINES = 6;
function renderMessage(body) {
  const wrap = $("#cd-message-wrap"), more = $("#cd-message-more");
  wrap.hidden = !body;
  $("#cd-message").textContent = body;
  const long = body.split("\n").length > LONG_MSG_LINES || body.length > 480;
  more.hidden = !long;
  wrap.classList.toggle("clamped", long && !cp.msgOpen);
  more.textContent = cp.msgOpen ? "Show less" : "Show all";
  more.setAttribute("aria-expanded", String(!!cp.msgOpen));
}
$("#cd-message-more").addEventListener("click", () => {
  cp.msgOpen = !cp.msgOpen;
  renderMessage(cp.detail?.body || "");
});

const FILE_STATUS = { A: "Added", M: "Modified", D: "Deleted", R: "Renamed", C: "Copied", T: "Type changed" };

// A changed file: its diff loads the first time it is opened.
function commitFile(f) {
  const body = h("div", { class: "diff-lines" });
  let loaded = false;
  return h("details", {
    class: "diff-file",
    ontoggle: (e) => { if (e.target.open && !loaded) { loaded = true; loadCommitFile(f, body).catch(fail); } },
  },
  h("summary", {},
    h("span", { class: `file-status st-${f.status}`, title: FILE_STATUS[f.status] || f.status }, f.status),
    h("span", { class: "path" }, f.old_path ? `${f.old_path} → ${f.path}` : f.path),
    h("span", { class: "counts" }, f.binary ? "binary" : [h("span", { class: "plus" }, `+${f.add}`), " ", h("span", { class: "minus" }, `−${f.del}`)])),
  body);
}

// The rows of a file's diff, null if the commit changed meanwhile. A
// commit's diffs are kept while it stays open, for stepping back to a file.
async function diffNodes(f) {
  if (f.binary && (isDiffImage(f.path) || isDiffImage(f.old_path))) {
    const url = (side, path) => `${commitAPI(state.project, cp.sha)}/image?side=${side}&path=${encodeURIComponent(path)}`;
    return [diffImages(f.status === "A" ? null : f.old_path || f.path, f.status === "D" ? null : f.path, url)];
  }
  if (f.binary) return [h("div", { class: "meta" }, "Binary file: no text diff.")];
  const p = state.project, sha = cp.sha, signal = cp.diffs.signal;
  let text = cp.texts.get(f.path);
  if (text == null) {
    try {
      text = await api("GET", `${commitAPI(p, sha)}/diff?path=${encodeURIComponent(f.path)}`, undefined, undefined, { signal });
    } catch (e) {
      return !signal.aborted && cp.sha === sha ? [h("div", { class: "meta err-text" }, e.message)] : null;
    }
    if (cp.sha !== sha) return null;
    cp.texts.set(f.path, text);
  }
  const cut = text.endsWith("[diff truncated]");
  const [file] = parseDiff(cut ? text.slice(0, -"[diff truncated]".length) : text);
  return [
    ...(file?.lines.some((l) => l.startsWith("@@")) ? diffLines(file) : [h("div", { class: "meta" }, "No text changes (only the file's name or mode).")]),
    cut ? h("div", { class: "meta" }, "The diff is too large to show in full.") : null,
  ].filter(Boolean);
}

async function loadCommitFile(f, el) {
  if (!f.binary) fill(el, h("div", { class: "meta" }, "Loading…"));
  const nodes = await diffNodes(f);
  if (nodes) fill(el, ...nodes);
}

// ------------------------------------------------------------------ desktop: file list beside the diff
const fileId = (i) => `cd-file-${i}`;
const fileCounts = (f) => h("span", { class: "counts" }, f.binary ? "binary" : [h("span", { class: "plus" }, `+${f.add}`), " ", h("span", { class: "minus" }, `−${f.del}`)]);
const fileStatus = (f) => h("span", { class: `file-status st-${f.status}`, title: FILE_STATUS[f.status] || f.status }, f.status);

// A path as its directory (dimmed) and its name, which is what tells files apart.
function filePath(f) {
  const split = (path) => {
    const i = path.lastIndexOf("/");
    return [h("span", { class: "dir" }, path.slice(0, i + 1)), h("span", { class: "name" }, path.slice(i + 1))];
  };
  return h("span", { class: "path", title: f.old_path ? `${f.old_path} → ${f.path}` : f.path },
    ...(f.old_path ? [...split(f.old_path), h("span", { class: "dir" }, " → ")] : []), ...split(f.path));
}

function fileList(files) {
  return h("ul", { class: "commit-file-list", role: "listbox", "aria-label": "Changed files", onkeydown: fileListKey },
    ...files.map((f, i) => h("li", {
      id: fileId(i), role: "option", tabindex: "-1", "aria-selected": "false", "data-path": f.path,
      "aria-label": `${FILE_STATUS[f.status] || f.status} ${f.path}${f.binary ? ", binary" : `, ${f.add} added, ${f.del} removed`}`,
      onclick: () => pickFile(f.path),
    }, fileStatus(f), filePath(f), fileCounts(f))));
}

// ↓/↑ (Home/End) move through the list while it has focus; the rest of the
// keys (j/k for commits, n/p, Esc) are the History view's (app/palette.js).
function fileListKey(e) {
  if (e.altKey || e.ctrlKey || e.metaKey) return;
  const by = { ArrowDown: 1, ArrowUp: -1, Home: -Infinity, End: Infinity }[e.key];
  if (!by) return;
  e.preventDefault();
  e.stopPropagation();
  stepCommitFile(by, { focus: true });
}

// stepCommitFile moves the selection n files on (back, if negative) and
// tells whether the key was the commit pane's: it is while one is open on
// a desktop, even with nothing (yet) to step to.
export function stepCommitFile(n, { focus } = {}) {
  if (!cp.sha || !desktop.matches || $("#commit-detail").hidden) return false;
  const files = cp.detail?.files || [];
  if (!files.length) return true;
  const i = Math.max(0, files.findIndex((f) => f.path === cp.file));
  const j = Math.max(0, Math.min(files.length - 1, i + n));
  const inList = $("#cd-files").contains(document.activeElement);
  pickFile(files[j].path, { focus: focus || inList });
  return true;
}

// pickFile selects a file of the open commit and shows its diff.
function pickFile(path, { focus = false } = {}) {
  const files = cp.detail?.files || [];
  const i = files.findIndex((f) => f.path === path);
  if (i < 0) return;
  cp.file = path;
  const list = $("#cd-files .commit-file-list");
  if (list) {
    for (const li of list.children) {
      const on = li.dataset.path === path;
      li.setAttribute("aria-selected", String(on));
      li.tabIndex = on ? 0 : -1;
    }
    list.setAttribute("aria-activedescendant", fileId(i));
    const li = $(`#${fileId(i)}`);
    li?.scrollIntoView({ block: "nearest" });
    if (focus) li?.focus({ preventScroll: true });
  }
  if ($("#cd-diff").dataset.path !== path) showFileDiff(files[i]).catch(fail);
}

async function showFileDiff(f) {
  const panel = $("#cd-diff");
  const sha = cp.sha;
  const body = h("div", { class: "diff-lines" });
  panel.dataset.path = f.path;
  fill(panel,
    h("header", { class: "commit-diff-head" }, fileStatus(f), filePath(f), fileCounts(f)),
    body);
  panel.scrollTop = 0;
  panel.scrollLeft = 0;
  // A diff already loaded shows at once; a new one says so while it loads.
  if (!f.binary && !cp.texts.has(f.path)) fill(body, h("div", { class: "meta" }, "Loading…"));
  const nodes = await diffNodes(f);
  if (nodes && cp.sha === sha && cp.file === f.path && body.isConnected) fill(body, ...nodes);
}
