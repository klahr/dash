import { $ } from "./dom.js";
import { api } from "./api.js";
import { ask, toast } from "./dialogs.js";
import { state, tryState } from "./state.js";
import { hidePreview, paneIn, pv, renderLog, renderPreview, setCommitSource } from "./preview.js";
import { chooseRunner, loadTry, rememberRunner } from "./try.js";

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

// changed tells the commit pane (history.js) to redraw its Preview button.
const changed = () => document.dispatchEvent(new Event("dash:commitpreview"));

const cpv = { pid: null, sha: "", short: "", runner: "", checkingOut: false, timer: 0, seq: 0 };
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
  return "";
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
  return { preview: preview(), checkingOut: cpv.checkingOut, short: cpv.short, runner: cpv.runner || s?.runner || "", other, why: commitPreviewWhy() || "" };
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
  setCommitSource({ preview, state: paneState, start: () => startCommitPreview(), stop, close: closeCommitPreview });
  if (pv.todo) hidePreview();
  Object.assign(cpv, { pid: p.id, sha, short: short || shortSHA(sha) });
  pv.commit = { pid: p.id, sha };
  pv.log = "";
  paneIn($("#commit-detail"));
  $("#commit-detail").classList.add("previewing");
  renderPreview();
  changed();
}

// startCommitPreview starts the open commit's preview: after the first-time
// warning about shared state, and asking before it replaces another session.
export async function startCommitPreview(anchor = $("#pv-status")) {
  const p = state.project;
  if (!p || !pv.commit) return;
  if (!localStorage.getItem(warnKey(p.id))) {
    const ok = await ask({
      title: "Preview an old commit?",
      body: "The commit runs in a throwaway worktree on a runner, but not in a sandbox: it shares that runner's dev database, services and the files copied into worktrees (such as .env). " +
        "Old code may run old migrations or write data in an old format there. Turn commit previews off in Project settings → Commands if that would hurt.",
      ok: "Preview it",
    });
    if (!ok) return closeCommitPreview();
    try { localStorage.setItem(warnKey(p.id), "1"); } catch { /* private mode */ }
  }
  const s = tryState.session;
  if (s && s.commit !== cpv.sha && s.state === "live" &&
    !await ask({ title: `Preview ${cpv.short} instead?`, body: `${s.kind === "web" ? "A preview" : "A Try terminal"} runs at ${shortSHA(s.commit)} on ${s.runner}. One runs per project: it stops, and its worktree is removed.`, ok: "Replace it" })) {
    return renderPreview();
  }
  await chooseRunner(anchor, (runner) => start(p, runner));
}

async function start(p, runner) {
  const sha = cpv.sha;
  rememberRunner(p.id, runner);
  cpv.runner = tryState.runners.find((r) => r.id === runner)?.name || "";
  cpv.checkingOut = true;
  pv.log = "";
  renderLog();
  renderPreview();
  try {
    const s = await api("POST", `/api/projects/${p.id}/try`, { runner, ref: sha, kind: "web" });
    if (state.project?.id !== p.id) return;
    tryState.session = s;
    tryState.runner = s.runner_id;
  } finally {
    if (cpv.sha === sha) cpv.checkingOut = false;
    if (pv.commit) renderPreview();
    changed();
  }
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

async function stop() {
  const p = state.project;
  if (!p) return;
  await api("DELETE", `/api/projects/${p.id}/try`);
  tryState.session = null;
  clearTimeout(cpv.timer);
  renderPreview();
  changed();
  toast("Preview stopped", { icon: "stop" });
}

// closeCommitPreview hides the pane; the preview keeps running until it idles.
export function closeCommitPreview() {
  if (!pv.commit) return;
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
