import { graphLayout } from "../graphlayout.js";
import { latestOnly } from "../commitnav.js";
import { $, $$, fill, h, ib, ico, timeLocale } from "./dom.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { copy, fail, leave } from "./dialogs.js";
import { cp, hist, state } from "./state.js";
import { baseBranch, projectKey } from "./events.js";
import { detailBackground } from "./detail.js";
import { diffLines, parseDiff } from "./drawmode.js";
import { openRelease } from "./releases.js";

// ------------------------------------------------------------------ history
// The default branch's commit graph: a dot per commit on coloured lanes, as
// GUI git clients draw it.
export async function loadGraph() {
  const p = state.project;
  if (!p) return renderGraph(null);
  const res = await api("GET", `/api/projects/${p.id}/graph`);
  if (projectKey(state.project) === projectKey(p)) renderGraph(res);
}

let graphTimer = 0;
export const refreshGraph = () => { clearTimeout(graphTimer); graphTimer = setTimeout(() => state.view === "history" && loadGraph().catch(() => {}), 800); };

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
  $("#graph-empty-text").textContent = state.project ? `${baseBranch(state.project) || "main"} has no commits yet.` : "Select a project to see its main branch.";
  // Commits origin's tip can't reach haven't been pushed yet: their dots are hollow.
  const remote = `origin/${res?.branch || baseBranch(state.project) || "main"}`;
  const tip = commits.find((c) => c.refs?.includes(remote));
  const pushed = new Set(tip ? [tip.hash] : []);
  for (const c of commits) if (pushed.has(c.hash)) c.parents?.forEach((p) => pushed.add(p));
  const { rows, width } = graphLayout(commits);
  hist.commits = commits.map((c) => ({ ...c, pushed: !tip || pushed.has(c.hash) }));
  hist.byHash = new Map(hist.commits.map((c) => [c.hash, c]));
  hist.remote = remote;
  // Another project's graph: its commit pane goes.
  if (cp.sha && cp.project !== state.project?.id) closeCommit();
  const graph = $("#graph");
  // A refresh keeps keyboard focus on the commit that had it.
  const focused = document.activeElement?.closest?.("#graph li")?.dataset.hash;
  graph.style.setProperty("--graph-w", `${width * LANE_W}px`);
  graph.replaceChildren(...commits.map((c, i) => {
    const { col, color, up, down } = rows[i];
    const isPushed = !tip || pushed.has(c.hash);
    const open = (e) => { if (e.type === "click" || e.key === "Enter" || e.key === " ") { e.preventDefault(); selectCommit(c.hash).catch(fail); } };
    const dot = "graph-dot" + (isPushed ? "" : " unpushed") + (c === tip ? " remote" : "") + (c.parents?.length > 1 ? " merge" : "");
    const selected = c.hash === cp.sha;
    return h("li", { title: isPushed ? null : "Not pushed yet", "data-hash": c.hash, class: selected ? "selected" : null },
      h("span", { class: "graph-lane" },
        graphLines(up, width, false), graphLines(down, width, true),
        h("span", { class: dot, style: `--x:${col * LANE_W + LANE_W / 2}px;--c:var(--lane-${color})` })),
      h("span", { class: "graph-commit", role: "button", tabindex: "0", "aria-controls": "commit-detail", "aria-current": selected ? "true" : null, "aria-label": `Commit ${c.short}: ${c.subject}`, onclick: open, onkeydown: open },
        h("code", { class: "graph-sha" }, c.short),
        ...(c.refs || []).map(graphRef),
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
// Selecting a History row shows that commit on the right (full screen on a
// phone): its message, who made it, its parents and the files it changed,
// each file's diff loaded when it is opened. The graph stays usable beside
// it on a desktop; j/k move the selection and the pane follows.
const commitLoad = latestOnly();

const commitAPI = (p, sha) => `/api/projects/${p.id}/commits/${sha}`;

// The modal parts apply on a phone only, where the pane covers everything.
function setCommitModal() {
  const on = !$("#commit-detail").hidden && !desktop.matches;
  detailBackground().forEach((el) => { el.inert = on; });
}

// selectCommit shows sha in the pane. wait debounces the request, for
// keys held down; the pane's head shows the graph's row at once meanwhile.
async function selectCommit(sha, { wait = 0 } = {}) {
  const p = state.project;
  const pane = $("#commit-detail");
  if (!p || (cp.sha === sha && !pane.hidden)) return;
  if (pane.hidden) {
    const a = document.activeElement;
    cp.opener = a && a !== document.body ? a : null;
  }
  cp.sha = sha;
  cp.project = p.id;
  cp.detail = null;
  cp.error = "";
  cp.diffs?.abort();
  cp.diffs = new AbortController();
  markSelectedCommit();
  pane.hidden = false;
  setCommitModal();
  renderCommit();
  $("#cd-body").scrollTop = 0;
  const res = await commitLoad.run((signal) => api("GET", commitAPI(p, sha), undefined, undefined, { signal }), { wait })
    .catch((e) => ({ error: e }));
  if (res.stale || cp.sha !== sha || projectKey(state.project) !== projectKey(p)) return;
  if (res.error) cp.error = res.error.message;
  else cp.detail = res.value;
  renderCommit();
}

export function closeCommit() {
  if (!cp.sha) return;
  commitLoad.cancel();
  cp.diffs?.abort();
  const pane = $("#commit-detail");
  const wasOpen = !pane.hidden;
  const sha = cp.sha;
  cp.sha = cp.detail = null;
  markSelectedCommit();
  const hide = () => { if (!cp.sha) { pane.hidden = true; setCommitModal(); } };
  if (wasOpen) leave(pane, "closing", hide); else hide();
  detailBackground().forEach((el) => { el.inert = false; });
  // Focus goes back to the row only if it was in the pane (or lost).
  const opener = cp.opener;
  cp.opener = null;
  const a = document.activeElement;
  if (!wasOpen || (a && a !== document.body && !pane.contains(a))) return;
  const row = $(`#graph li[data-hash="${sha}"] .graph-commit`);
  (row || (opener?.isConnected ? opener : null))?.focus();
}
$("#cd-close").addEventListener("click", closeCommit);
desktop.addEventListener("change", () => { if (cp.sha) setCommitModal(); });

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

// "3 days ago", for an absolute date's tooltip.
const relFmt = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
function relTime(iso) {
  const s = (new Date(iso) - Date.now()) / 1000;
  for (const [unit, n] of [["year", 31536000], ["month", 2592000], ["week", 604800], ["day", 86400], ["hour", 3600], ["minute", 60]]) {
    if (Math.abs(s) >= n) return relFmt.format(Math.round(s / n), unit);
  }
  return relFmt.format(Math.round(s), "second");
}
const absTime = (iso) => new Date(iso).toLocaleString(timeLocale, { dateStyle: "medium", timeStyle: "short" });

// The commit as a release, Tag release… and Copy SHA know it.
function commitRef() {
  const c = hist.byHash.get(cp.sha), d = cp.detail;
  return { sha: d?.hash || cp.sha, short: d?.short || c?.short || cp.sha.slice(0, 7), subject: d?.subject ?? c?.subject ?? "" };
}

function graphRef(r) {
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
    ...(d?.refs || c?.refs || []).map(graphRef));
  if (!p) return;
  const gh = githubURL(p.remote_url);
  // A commit beyond the loaded graph is older than ones in it: pushed if they are.
  const pushed = c ? c.pushed : hist.commits.at(-1)?.pushed ?? true;
  const why = !p.remote_url ? "Connect a remote first" : !pushed ? "Push first" : "";
  fill($("#cd-actions"),
    ib({ type: "button", class: "btn small", disabled: !!why, title: why || "Tag a release at this commit", onclick: () => openRelease(commitRef()).catch(fail) }, "tag", "Tag release…"),
    why ? h("em", { class: "menu-note" }, why) : null,
    ib({ type: "button", class: "btn small", onclick: () => copy(commitRef().sha, "SHA copied") }, "copy", "Copy SHA"),
    gh ? h("a", { class: "btn small", href: `${gh}/commit/${ref.sha}`, target: "_blank", rel: "noopener" }, ico("external"), "Open on GitHub") : null);
}

function renderCommit() {
  if (!cp.sha) return;
  renderCommitHead();
  const d = cp.detail;
  $("#cd-error").hidden = !cp.error;
  $("#cd-error").textContent = cp.error;
  $("#cd-message").hidden = !d?.body;
  $("#cd-message").textContent = d?.body || "";
  const person = (x) => h("span", { title: x.email }, x.name, " ", h("span", { class: "commit-email" }, `<${x.email}>`));
  const when = (x) => h("time", { datetime: x.date, title: relTime(x.date) }, absTime(x.date));
  const parent = (sha) => h("button", { type: "button", class: "link inline commit-parent", title: `Show ${sha}`, onclick: () => selectCommit(sha).catch(fail) },
    hist.byHash.get(sha)?.short || sha.slice(0, 7));
  const sameCommitter = d && d.committer.name === d.author.name && d.committer.email === d.author.email;
  fill($("#cd-facts"), ...(d ? [
    h("dt", {}, "Author"), h("dd", {}, person(d.author), " · ", when(d.author)),
    sameCommitter ? null : h("dt", {}, "Committer"),
    sameCommitter ? null : h("dd", {}, person(d.committer), " · ", when(d.committer)),
    h("dt", {}, d.parents.length > 1 ? "Parents" : "Parent"),
    h("dd", {}, d.parents.length ? d.parents.flatMap((sha, i) => [i ? " " : null, parent(sha)]) : "None (the first commit)"),
  ] : []));
  const head = $("#cd-files-head");
  head.hidden = !d;
  if (!d) {
    fill($("#cd-files"), cp.error ? null : h("p", { class: "pane-empty" }, "Loading…"));
    return;
  }
  const add = d.files.reduce((n, f) => n + f.add, 0), del = d.files.reduce((n, f) => n + f.del, 0);
  fill(head, `${d.file_count} ${d.file_count === 1 ? "file" : "files"} changed`,
    d.files.length ? h("span", { class: "counts" }, " ", h("span", { class: "plus" }, `+${add}`), " ", h("span", { class: "minus" }, `−${del}`)) : null);
  fill($("#cd-files"),
    !d.files.length ? h("p", { class: "pane-empty" }, d.parents.length > 1 ? "The merge brought no changes into this branch." : "No files changed.") : null,
    ...d.files.map((f) => commitFile(f)),
    d.file_count > d.files.length ? h("p", { class: "hint" }, `And ${d.file_count - d.files.length} more files, not listed.`) : null);
}

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

async function loadCommitFile(f, el) {
  const p = state.project, sha = cp.sha, signal = cp.diffs.signal;
  if (f.binary) return fill(el, h("div", { class: "meta" }, "Binary file: no text diff."));
  fill(el, h("div", { class: "meta" }, "Loading…"));
  let text;
  try {
    text = await api("GET", `${commitAPI(p, sha)}/diff?path=${encodeURIComponent(f.path)}`, undefined, undefined, { signal });
  } catch (e) {
    if (!signal.aborted && cp.sha === sha) fill(el, h("div", { class: "meta err-text" }, e.message));
    return;
  }
  if (cp.sha !== sha) return;
  const cut = text.endsWith("[diff truncated]");
  const [file] = parseDiff(cut ? text.slice(0, -"[diff truncated]".length) : text);
  fill(el, ...(file?.lines.some((l) => l.startsWith("@@")) ? diffLines(file) : [h("div", { class: "meta" }, "No text changes (only the file's name or mode).")]),
    cut ? h("div", { class: "meta" }, "The diff is too large to show in full.") : null);
}
