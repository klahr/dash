import { $ } from "./dom.js";
import { api } from "./api.js";
import { ask, fail, toast } from "./dialogs.js";
import { hist, state, tryState } from "./state.js";
import { hidePreview, paneIn, pv, renderLog, renderPreview, setCommitSource } from "./preview.js";
import { chooseRunner, loadTry, rememberRunner, renderTryButton } from "./try.js";
import { latestCommit, selectCommit, showCommitAt } from "./history.js";
import { endDraw } from "./drawmode.js";

// ------------------------------------------------------------------ commit preview
// History's Preview: a web project's app as it was at a commit of the default
// branch. It is the project's Try session with kind "web" (api/try.go): the
// commit checked out in a throwaway worktree on a runner, its setup command
// and start server command run there, served through the preview proxy at
// <project>--<runner>.<preview domain>. One per project (it replaces a Try
// terminal or a preview at another commit, after asking), stopped after 30
// minutes without traffic, its worktree removed when it stops.
//
// The pane is the task preview's (preview.js), moved into the commit pane while
// it shows a commit. Try sessions send no events: while it starts it is looked
// at every 1.5 seconds, with its log, so a slow first install shows progress.
//
// A web project's header Run is the same preview, of the latest commit of the
// default branch (previewOnRunner, from try.js): it opens History on that
// commit with the pane beside it, and its Terminate is this Stop. When a newer
// commit lands on that branch (History's graph redraws on merges and fetches),
// its bar offers New commit abc1234 · Update: the same Run, on the same runner.
// Only when pressed: a checkout and setup take a while, and would wipe the
// page or a drawing in progress.

// changed tells the commit pane (history.js) to redraw its Preview button,
// and the header's Run (try.js) to follow the session.
const changed = () => { document.dispatchEvent(new Event("dash:commitpreview")); renderTryButton(); };

// follows is the commit a Run started (the latest, then): "" for a picked one.
const cpv = { pid: null, sha: "", short: "", runner: "", checkingOut: false, timer: 0, seq: 0, follows: "" };
const shortSHA = (sha) => (sha || "").slice(0, 7);
const warnKey = (pid) => `dash.commitPreview.warned.${pid}`;

// The web session, if the project's Try session is one.
// (tryState is the open project's: it starts afresh on another project.)
const webSession = () => (tryState.pid === state.project?.id && tryState.session?.kind === "web" ? tryState.session : null);

// commitPreviewWhy says why History can't preview commits of p ("" when it
// can, null when it shouldn't offer it at all: no runner works on the project).
export function commitPreviewWhy(p = state.project) {
  if (!p || p.prefix) return null;
  if (p.preview_kind === "terminal") return null; // a CLI project: Try at this commit
  if (tryState.pid === p.id && tryState.loaded && !tryState.candidates.length) return null;
  if (!p.run_command) return "Set a start server command in Project settings → Commands to preview commits.";
  if (p.commit_previews === false) return "Commit previews are off in Project settings → Commands.";
  if (tryState.pid === p.id && tryState.loaded && !tryState.runners.length) return tryState.disabled || "No runner can run this project.";
  if (tryState.pid === p.id && tryState.loaded && tryState.webDisabled) return sentence(tryState.webDisabled);
  return "";
}

// The server's reasons start in lower case, for joining to its error.
const sentence = (s) => `${s.charAt(0).toUpperCase()}${s.slice(1)}${/[.)]$/.test(s) ? "" : "."}`;

// runPreviewWhy says why a web project's Run can't preview its latest commit
// ("" when it can): History's reasons, and no runner working on it at all.
export function runPreviewWhy(p = state.project) {
  const why = commitPreviewWhy(p);
  if (why !== null) return why;
  return tryState.pid === p?.id && tryState.loaded ? tryState.disabled || "No runner works on this project." : "";
}

// The preview as the pane shows it (preview.js's shape), when the session is
// at the commit the pane is for.
function preview() {
  const s = webSession();
  if (!s || s.commit !== cpv.sha) return null;
  return { ...s, todo_id: "", prefix: "", local_url: "" };
}

function paneState() {
  const s = webSession();
  const other = s && s.commit !== cpv.sha && s.state !== "crashed" ? shortSHA(s.commit) : "";
  return { preview: preview(), checkingOut: cpv.checkingOut, short: cpv.short, runner: cpv.runner || s?.runner || "", other, why: commitPreviewWhy() || "", newer: newerCommit() };
}

// newerCommit is the newest commit of the default branch when the pane shows
// a preview Run started and the branch has moved on since ("" otherwise).
// The graph must know the running commit: one the runner fetched past the
// graph would make the graph's newest an older one.
export function newerCommit() {
  const p = state.project, s = webSession();
  if (!p || !pv.commit || cpv.pid !== p.id || cpv.checkingOut || !cpv.follows) return "";
  if (pv.commit.sha !== cpv.follows || s?.commit !== cpv.follows || !hist.byHash?.has(cpv.follows)) return "";
  const newest = latestCommit();
  return newest && newest !== cpv.follows && isLatest(p, newest) ? newest : "";
}

// graphChanged is History's graph redrawn (a merge, a fetch, Refresh): the
// bar's New commit · Update may come, or name a newer commit.
export function graphChanged() {
  if (pv.commit) renderPreview();
}

// updateCommitPreview is that button: Run again, on the runner the preview
// runs on, at the width the pane has (no warning: it is today's code).
export async function updateCommitPreview() {
  const p = state.project, sha = newerCommit(), runner = webSession()?.runner_id;
  if (!p || !sha || !runner) return;
  open(p, sha, shortSHA(sha));
  selectCommit(sha).catch(fail);
  await start(p, runner, "", true);
}

// showCommitPreview opens the pane on the commit (sha, short), starting its
// preview unless it runs at that commit already.
export async function showCommitPreview({ sha, short }, anchor) {
  const p = state.project;
  if (!p) return;
  const why = commitPreviewWhy(p);
  if (why) throw new Error(why);
  await loadTry({ any: true });
  if (state.project?.id !== p.id) return;
  if (commitPreviewWhy(p)) throw new Error(commitPreviewWhy(p));
  open(p, sha, short);
  const s = webSession();
  if (s && s.commit === sha && s.state !== "crashed") { poll(); return; }
  await startCommitPreview(anchor);
}

function open(p, sha, short) {
  // Registered here, not when this module loads: preview.js may not have run
  // yet then (the modules import each other).
  setCommitSource({ preview, state: paneState, start: () => startCommitPreview(), update: updateCommitPreview, stop: stopCommitPreview, close: closeCommitPreview });
  if (pv.todo) hidePreview();
  endDraw(); // a picture of another commit
  Object.assign(cpv, { pid: p.id, sha, short: short || shortSHA(sha) });
  pv.commit = { pid: p.id, sha };
  pv.log = "";
  paneIn($("#commit-detail"));
  $("#commit-detail").classList.add("previewing");
  renderPreview();
  changed();
}

// warned says whether the user knows an old commit shares the runner's state:
// it asks the first time, per project and browser.
async function warned(p) {
  if (localStorage.getItem(warnKey(p.id))) return true;
  const ok = await ask({
    title: "Preview an old commit?",
    body: "The commit runs in a throwaway worktree on a runner, but not in a sandbox: it shares that runner's dev database, services and the files copied into worktrees (such as .env). " +
      "Old code may run old migrations or write data in an old format there. Turn commit previews off in Project settings → Commands if that would hurt.",
    ok: "Preview it",
  });
  if (ok) try { localStorage.setItem(warnKey(p.id), "1"); } catch { /* private mode */ }
  return ok;
}

// isLatest says whether sha is the newest commit of p's default branch, as
// History's graph has it (today's code: no warning about old commits).
const isLatest = (p, sha) => !!sha && hist.branch === (p.default_branch || "main") && sha === latestCommit();

// previewOnRunner is a web project's Run (ref "": the latest commit of the
// default branch, as the runner fetches it) and its ▾ → Pick a commit… (any
// ref): it opens History on that commit and starts its preview on runner, in
// the pane beside it. Until the runner says which commit it checked out, the
// pane is beside the graph's newest one (or the one ref names); then it moves.
export async function previewOnRunner(ref, runner) {
  const p = state.project;
  if (!p) return;
  const why = runPreviewWhy(p);
  if (why) throw new Error(why);
  const sha = await showCommitAt(ref);
  if (!sha || state.project?.id !== p.id) return;
  const branch = p.default_branch || "main";
  const exact = !!ref && sha.startsWith(ref.toLowerCase());
  // The latest commit is today's code: no warning. Another ref (a tag, say)
  // may name an old one: the graph's newest stands in for it until the runner
  // says which it checked out, so it warns.
  const latest = !ref || ref === branch || ref === `origin/${branch}` || (exact && isLatest(p, sha));
  if (!latest && !await warned(p)) return;
  open(p, sha, !ref ? `the latest ${branch}` : exact ? shortSHA(sha) : ref);
  await start(p, runner, ref, !ref || ref === branch || ref === `origin/${branch}`);
}

// startCommitPreview starts the open commit's preview: after the first-time
// warning about shared state, and asking before it replaces another session.
export async function startCommitPreview(anchor = $("#pv-status")) {
  const p = state.project;
  if (!p || !pv.commit) return;
  if (!isLatest(p, cpv.sha) && !await warned(p)) return closeCommitPreview();
  const s = tryState.session;
  if (s && s.commit !== cpv.sha && s.state === "live" &&
    !await ask({ title: `Preview ${cpv.short} instead?`, body: `${s.kind === "web" ? "A preview" : "A Try terminal"} runs at ${shortSHA(s.commit)} on ${s.runner}. One runs per project: it stops, and its worktree is removed.`, ok: "Replace it" })) {
    return renderPreview();
  }
  // Start again on a preview Run started (it crashed, say) keeps it Run's.
  const run = !!cpv.follows && cpv.follows === cpv.sha;
  await chooseRunner(anchor, (runner) => start(p, runner, cpv.sha, run));
}

// start starts the preview at ref (the open commit's, by default) on runner;
// run says it is the default branch's latest commit, to follow (cpv.follows).
// While it checks out, the header's Run is Terminate (tryState.starting); one
// pressed then stops it once it has started.
async function start(p, runner, ref = cpv.sha, run = false) {
  const sha = cpv.sha;
  cpv.follows = "";
  rememberRunner(p.id, runner);
  cpv.runner = tryState.runners.find((r) => r.id === runner)?.name || "";
  cpv.checkingOut = true;
  tryState.starting = `${cpv.short} on ${cpv.runner || "a runner"}`;
  pv.log = "";
  renderLog();
  renderPreview();
  changed();
  let ok = false;
  try {
    const s = await api("POST", `/api/projects/${p.id}/try`, { runner, ref, kind: "web" });
    if (state.project?.id !== p.id) return;
    tryState.session = s;
    tryState.runner = s.runner_id;
    ok = true;
    if (run) cpv.follows = s.commit;
    // Run and a picked ref: the pane moves to the commit the runner checked out.
    if (pv.commit && cpv.sha === sha && s.commit !== sha) {
      Object.assign(cpv, { sha: s.commit, short: shortSHA(s.commit) });
      pv.commit.sha = s.commit;
      selectCommit(s.commit).catch(fail);
    }
  } finally {
    tryState.starting = "";
    if (!ok) tryState.stopAsked = false;
    if (cpv.sha === sha || ok) cpv.checkingOut = false;
    if (pv.commit) renderPreview();
    changed();
  }
  if (tryState.stopAsked && state.project?.id === p.id) return stopCommitPreview();
  poll();
}

// poll looks at the session and its log: often while it starts, now and
// then while it runs (it may crash, or stop as idle), not once it's gone.
function poll() {
  clearTimeout(cpv.timer);
  if (!pv.commit) return;
  const n = ++cpv.seq, pid = cpv.pid;
  const again = () => {
    if (n !== cpv.seq || !pv.commit) return;
    const st = webSession()?.state;
    if (st === "starting") cpv.timer = setTimeout(poll, 1500);
    else if (st === "live") cpv.timer = setTimeout(poll, 10000);
  };
  api("GET", `/api/projects/${pid}/try/log?lines=200`).then((out) => {
    if (n !== cpv.seq || tryState.pid !== pid) return;
    const was = tryState.session;
    tryState.session = out.session;
    if (out.session?.commit === cpv.sha) { pv.log = out.text || ""; renderLog(); }
    if (was?.state === "live" && out.session?.state === "crashed" && pv.commit) $("#pv-log").open = true;
  }, () => {
    if (n !== cpv.seq || tryState.pid !== pid) return;
    tryState.session = null; // not running (any more)
  }).finally(() => {
    if (n !== cpv.seq) return;
    if (pv.commit) renderPreview();
    changed();
    again();
  });
}

// stopCommitPreview stops the preview: the pane's Stop, and the header's Terminate.
export async function stopCommitPreview() {
  const p = state.project;
  if (!p) return;
  tryState.stopAsked = false;
  await api("DELETE", `/api/projects/${p.id}/try`);
  tryState.session = null;
  cpv.follows = "";
  clearTimeout(cpv.timer);
  if (pv.commit) renderPreview();
  changed();
  toast("Preview stopped", { icon: "stop" });
}

// showRunningPreview opens the running preview in History, beside its
// commit (the header's "abc1234 on laptop" beside Terminate).
export async function showRunningPreview() {
  const p = state.project, s = webSession();
  if (!p) return;
  const sha = await showCommitAt(s?.commit || "");
  if (!s || !sha || state.project?.id !== p.id || previewingCommit(s.commit)) return;
  open(p, s.commit, shortSHA(s.commit));
  poll();
}

// closeCommitPreview hides the pane; the preview keeps running until it idles.
export function closeCommitPreview() {
  if (!pv.commit) return;
  endDraw();
  pv.commit = null;
  cpv.seq++;
  clearTimeout(cpv.timer);
  pv.shown = null;
  $("#pv-iframe").src = "about:blank";
  $("#pv-iframe").hidden = true;
  const el = $("#d-preview");
  el.hidden = true;
  delete el.dataset.of;
  $("#commit-detail").classList.remove("previewing");
  paneIn($("#detail"));
  changed();
}

// previewingCommit says whether the pane shows sha's preview.
export const previewingCommit = (sha) => !!pv.commit && (!sha || pv.commit.sha === sha);

// The session's state for the commit pane's button: "live", "starting",
// "crashed" when it runs at sha, else "".
export function commitPreviewState(sha) {
  const s = webSession();
  return s && s.commit === sha ? s.state : "";
}
