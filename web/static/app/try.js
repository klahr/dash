import { $, $$, debounce, h, ib, setLabel } from "./dom.js";
import { chatBeside, desktop, wsURL } from "./viewport.js";
import { api } from "./api.js";
import { ask, attempt, fail, openSheet, toast } from "./dialogs.js";
import { popMenu } from "./drawmode.js";
import { href } from "./routes.js";
import { projectFirst, state, tryState } from "./state.js";
import { showView } from "./nav.js";
import { setMenu } from "./projects.js";
import { closeDetail } from "./detail.js";
import { PV_ARROWS, PV_KEYS } from "./preview.js";
import { agoText } from "../time.js";
import { pane, renderTryBack, toggleChatPane } from "./chatpane.js";
import { ensureShell, procTerminal, runTests, serverRunning, testStatus, toggleServer } from "./terminal.js";
import { pushDone, pushStatus } from "./board.js";
import { buildStatus, openCI } from "./actions.js";
import { previewOnRunner, runPreviewWhy, showRunningPreview, stopCommitPreview } from "./commitpreview.js";

// ------------------------------------------------------------------ try: the project's CLI on a runner
// (tryState is declared with state, near the top: views render before this runs.)
// The Shell page's "commit…" place (view "try"; "main" is terminal.js's shell
// in the checkout). It opens a terminal on the project itself (not a task): its preview_command,
// or a shell, in a throwaway worktree on a runner the user picks, at the tip of
// the default branch (fetched first) or a commit picked from its history. The
// project chat sits beside it (a tab of its own on phones), reads the terminal
// with read_try_terminal, and files what the user finds as issues or draft tasks.
function tryProject(p = state.project) { return !!p && p.preview_kind === "terminal" && !p.prefix; }
// webProject: a web project of this Dash with a start server command. Its Run
// previews the latest commit on a runner, as History's Preview does
// (commitpreview.js); ▾ → Run in checkout runs the command in its checkout.
const webProject = (p = state.project) => !!p && !p.prefix && p.preview_kind !== "terminal" && !!p.run_command;
// onRunner: the project's Run runs on a runner, as its Try session.
const onRunner = (p = state.project) => tryProject(p) || webProject(p);
function shortSHA(sha) { return (sha || "").slice(0, 7); }
function trySend(msg) { tryT?.send(msg); }

// Only the latest look at the session counts: one asked before Run or Terminate
// changed it, answered after, would undo the change on the button.
let trySeq = 0;
// any: load it for a web project too (History's commit preview is its web try session).
export async function loadTry({ any = false } = {}) {
  const p = state.project;
  const n = ++trySeq;
  if (tryState.pid !== (p?.id || null)) {
    disconnectTry();
    Object.assign(tryState, { pid: p?.id || null, loaded: false, runners: [], candidates: [], default: "", disabled: "", webDisabled: "", session: null, runner: "", refused: null, pending: null, stopAsked: false });
  }
  if (!onRunner(p) && !(any && p && !p.prefix)) { renderTryButton(); if (state.view === "try") renderTry(); return; }
  const st = await api("GET", `/api/projects/${p.id}/try`);
  if (state.project?.id !== p.id || n !== trySeq) return;
  Object.assign(tryState, { loaded: true, runners: st.runners || [], candidates: st.candidates || [], default: st.default || "", disabled: st.disabled || "", webDisabled: st.web_disabled || "", session: st.session || null });
  const ids = tryState.runners.map((r) => r.id);
  if (tryState.session) tryState.runner = tryState.session.runner_id;
  else if (!ids.includes(tryState.runner)) tryState.runner = st.default || "";
  renderTryButton();
  if (state.view === "try") renderTry();
}

// The Shell page's picker: "main" (the default branch's name) and "commit…",
// which only a CLI project with a runner for it can use.
const NOT_CLI = "Trying a commit is for CLI projects: set the preview to Terminal in the project's settings.";
export function renderTryButton() {
  renderRun();
  const p = state.project, s = tryState.session;
  $(".shell-at").hidden = !p; // no project: the page is its "No project" note (ensureShell)
  $("#shell-at-branch").textContent = p?.default_branch || "main";
  const btn = $("#shell-at-commit");
  const can = tryProject();
  const off = can && tryState.loaded && !tryState.runners.length && !s;
  btn.disabled = !can || off;
  btn.title = !p ? "Pick a project first" : !can ? NOT_CLI : off ? tryState.disabled
    : s ? `Trying ${shortSHA(s.commit)} on ${s.runner}. Pick another commit`
      : "Try the project at a commit, in a throwaway worktree on a runner";
}

// ---- Run: the header's split button
// Shown whenever a project is open. A CLI project's ▶ Run starts the latest
// commit (an empty ref: the default branch's tip) on the runner used last, or
// the only one; ▾ lists the project's runners to pick from. While a run starts
// or goes it is ■ Terminate, with the commit and runner beside it, linking to
// its terminal. A web project with a run command does the same with a commit
// preview (commitpreview.js): ▶ Run previews the latest commit on a runner, in
// History beside it, and ▾ also has Run in checkout (terminal.js: toggleServer,
// the command in the project's checkout). One on another computer's Dash runs it in its
// checkout, and while it runs the button is ■ Stop. Any other project has nothing to run: ▶ Run is greyed
// (aria-disabled, so it still takes clicks) and opens Configure….
// ▾ also holds the project's other actions: Run tests, Push and Builds…, and a
// dot on it says one needs a look (a test run or build going, or failed).
const runKey = (pid) => `dash.run.runner.${pid}`;
function lastRunner(pid) { try { return localStorage.getItem(runKey(pid)) || ""; } catch { return ""; } }
export function rememberRunner(pid, id) { try { localStorage.setItem(runKey(pid), id); } catch { /* private mode */ } }

// running: a run is starting or going (an exited one shows its last screen,
// but the button is Run again).
// (A commit preview is "starting" until its server answers.)
const running = () => !!tryState.starting || !!tryState.pending || tryState.stopAsked || ["live", "starting"].includes(tryState.session?.state);

// webRun: the project's Run runs its run_command in its checkout (a web
// project on another computer's Dash).
const webRun = (p = state.project) => !!p && !onRunner(p) && p.preview_kind !== "terminal" && !!p.run_command;

// Run in checkout: a web project's run_command in its checkout, uncommitted
// changes included, its output on Code → Shell (R there). Stop while it runs.
function checkoutRow() {
  const stop = serverRunning();
  return [stop ? "stop" : "play", stop ? "Stop the checkout server" : "Run in checkout", toggleServer,
    { title: stop ? "Stop the server running in the project's checkout" : `Run ${state.project?.run_command} in the project's checkout, uncommitted changes included (Code → Shell)` }];
}

// noRunWhy says why the project has nothing to run ("" when it has).
function noRunWhy(p = state.project) {
  if (!p || onRunner(p) || webRun(p)) return "";
  if (p.preview_kind === "terminal") return "Run isn't available for a project on another computer's Dash. Configure… opens Project settings → Commands";
  return "No run command set. Configure it in Project settings → Commands";
}

async function configureRun() {
  const p = state.project;
  if (!p) throw new Error(projectFirst());
  const { goTo } = await import("./teamsetup.js");
  await goTo("settings", "commands", p);
}

// runRunner is the runner ▶ Run uses without asking: the one used last, the
// default, or the only one that can run it ("" when it must ask).
function runRunner() {
  const ok = tryState.runners.map((r) => r.id);
  const last = state.project ? lastRunner(state.project.id) : "";
  if (ok.includes(last)) return last;
  if (ok.includes(tryState.default)) return tryState.default;
  return ok.length === 1 ? ok[0] : "";
}

function renderRun() {
  const p = state.project, s = tryState.session;
  const wrap = $("#run-split"), main = $("#run-main"), at = $("#run-at");
  wrap.hidden = !p;
  if (wrap.hidden) return;
  renderPickState();
  const why = noRunWhy(p);
  main.classList.toggle("off", !!why);
  if (why) main.setAttribute("aria-disabled", "true"); else main.removeAttribute("aria-disabled");
  if (!onRunner(p)) {
    // Nothing to run (greyed, opens Configure…), or the run command in the checkout.
    const stop = !why && serverRunning();
    main.classList.toggle("stop", stop);
    setLabel(main, stop ? "stop" : "play", h("span", { class: "run-main-label" }, stop ? "Stop" : "Run"));
    main.disabled = false;
    main.title = why || (stop ? "Stop the server" : `Run ${p.run_command}`);
    at.hidden = true;
    return;
  }
  const stop = running(), web = webProject(p);
  // A web project's preview can't run (no runner, no preview domain, turned off): greyed, saying why.
  const off = !stop && (web ? runPreviewWhy(p) : tryState.loaded && !tryState.runners.length ? tryState.disabled || "No runner can run this project." : "");
  main.classList.toggle("stop", stop);
  setLabel(main, stop ? "stop" : "play", h("span", { class: "run-main-label" }, stop ? "Terminate" : "Run"));
  main.disabled = !!off;
  const on = tryState.runners.find((r) => r.id === runRunner())?.name;
  const latest = `${web ? "Preview" : "Run"} the latest ${p.default_branch || "main"}`;
  const label = stop ? `Terminate the ${web ? "preview" : "run"}` : `${latest} on ${on || "a runner"}`;
  main.title = off ? (web ? `${off} ▾ → Run in checkout runs it in the project's checkout.` : off) : label;
  // What runs, and where: a link to its terminal.
  const what = s && !tryState.starting ? shortSHA(s.commit) : "";
  const where = s && !tryState.starting ? s.runner : tryState.runners.find((r) => r.id === (tryState.pending?.runner || tryState.runner))?.name || "";
  at.hidden = !stop;
  if (stop) {
    at.href = href(web ? { view: "history" } : { view: "try" }, p);
    at.title = web ? "Open its preview" : "Open its terminal";
    at.replaceChildren(what ? h("code", {}, what) : "starting", where ? h("span", { class: "run-at-on" }, ` on ${where}`) : "");
  }
}

// renderPickState marks ▾ with the most pressing of its actions' states: a failed
// test run or build (red), one going (pulsing), or commits waiting to be pushed.
function renderPickState() {
  const pick = $("#run-pick"), t = testStatus(), b = buildStatus(), push = pushStatus();
  const bad = [t?.state === "bad" && "Tests failed", b?.state === "bad" && b.note].filter(Boolean);
  const going = [t?.state === "running" && t.label, b?.state === "running" && b.note].filter(Boolean);
  const mark = bad.length ? "bad" : going.length ? "running" : push?.n ? "push" : "";
  if (mark) pick.dataset.state = mark; else delete pick.dataset.state;
  const say = bad.length ? bad : going.length ? going : push?.n ? [push.title] : [];
  const label = ["Run, tests, push and builds", ...say].join(": ");
  pick.title = label;
  pick.setAttribute("aria-label", label);
}
// The menu's items are only read when it opens: a test run going counts its time there.
setInterval(() => { if ($("#run-pick").dataset.state === "running" && !document.hidden) renderPickState(); }, 5000);

// actionRows are Run ▾'s Run tests, Push and Builds…, each with its key (palette.js, vim.js).
function actionRows() {
  const t = testStatus(), push = pushStatus(), b = buildStatus();
  return [
    t && ["check", t.label, runTests, { note: t.note, tone: t.state, keys: "T", title: t.title }],
    push && ["push", push.label, pushDone, { disabled: !push.n, keys: ":push", title: push.title }],
    b && ["checks", "Builds…", openCI, { note: b.note, tone: b.state, keys: ":ci", title: "GitHub Actions on the default branch" }],
  ].filter(Boolean);
}

// chooseRunner calls then with the runner a commit runs on: the session's,
// the one ▶ Run uses, or one picked from a menu at anchor when several can.
export async function chooseRunner(anchor, then) {
  if (!tryState.runners.length) throw new Error(tryState.disabled || "No runner can run this project.");
  const ok = tryState.runners.map((r) => r.id);
  const id = ok.includes(tryState.session?.runner_id) ? tryState.session.runner_id : runRunner();
  if (id) return then(id);
  const last = state.project ? lastRunner(state.project.id) : "";
  await popMenu(anchor, [tryState.runners.map((r) => ["computer", r.id === last ? `${r.name} (last used)` : r.name, () => then(r.id)])], { heading: "Run on" });
}

// tryAtCommit opens the Shell page's Try terminal at commit sha (History's
// "Try at this commit"), asking first when it would replace a session at
// another commit.
export async function tryAtCommit(sha, anchor) {
  const p = state.project;
  if (!p) throw new Error(projectFirst());
  if (!tryProject()) throw new Error(NOT_CLI);
  if (!tryState.loaded) await loadTry();
  const s = tryState.session;
  if (s && s.state === "live" && s.commit !== sha &&
    !await ask({ title: `Try ${shortSHA(sha)} instead?`, body: `${s.kind === "web" ? "A commit preview" : "A Try terminal"} runs at ${shortSHA(s.commit)} on ${s.runner}. One runs per project: it stops, and its worktree is removed.`, ok: "Replace it" })) return;
  await chooseRunner(anchor, (runner) => {
    rememberRunner(p.id, runner);
    if (s?.state === "live" && s.commit === sha && s.kind !== "web") return openTry(); // already there
    tryState.pending = { ref: sha, runner };
    renderRun();
    openTry();
  });
}

// runOn starts the latest commit on runner and opens its terminal (a web
// project: its preview, in History).
async function runOn(runner) {
  const p = state.project;
  if (!p) return;
  rememberRunner(p.id, runner);
  if (pane.open && !chatBeside.matches) toggleChatPane(false); // a phone shows the terminal, not the chat
  if (webProject(p)) return previewOnRunner("", runner);
  if (state.view === "try") return startTry({ ref: "", runner });
  tryState.pending = { ref: "", runner }; // enterTry starts it
  renderRun();
  openTry();
}

// runMenu drops down the project's runners (those that can't run it greyed,
// saying why), then "Pick a commit…"; while a run goes, Terminate instead. A web
// project's has Run in checkout too, and one on another computer's Dash only Run
// (or Stop). Then the project's actions (actionRows) and
// "Configure…" (Project settings → Commands, where the preview kind and command are).
function runMenu() {
  const conf = [["settings", "Configure…", configureRun]];
  if (!onRunner()) {
    const why = noRunWhy(), stop = !why && serverRunning();
    const run = [stop ? "stop" : "play", stop ? "Stop" : "Run", clickRun, { disabled: !!why, title: why || (stop ? "Stop the server" : `Run ${state.project?.run_command}`) }];
    return popMenu($("#run-pick"), [[run], actionRows(), conf], { heading: why ? "Nothing to run" : "Run" });
  }
  const web = webProject(), checkout = web ? [checkoutRow()] : [];
  if (running()) {
    return popMenu($("#run-pick"), [[["stop", "Terminate", clickRun, { title: `Terminate the ${web ? "preview" : "run"}` }]], checkout, actionRows(), conf], { heading: "Run" });
  }
  // A web project's preview can't run at all (no preview domain, turned off): its rows say why.
  const off = web && tryState.runners.length ? runPreviewWhy() : "";
  const cands = tryState.candidates.length ? tryState.candidates : tryState.runners.map((r) => ({ ...r, online: true }));
  const ok = new Set(tryState.runners.map((r) => r.id));
  const last = state.project ? lastRunner(state.project.id) : "";
  const rows = cands.map((r) => {
    const can = ok.has(r.id) && !off;
    const why = ok.has(r.id) ? off : r.why || tryState.disabled || "Can't run this project.";
    return ["computer", r.id === last ? `${r.name} (last used)` : r.name, () => runOn(r.id),
      { disabled: !can, note: !r.online ? "offline" : ok.has(r.id) ? "online" : "can't run it", title: why }];
  });
  if (!rows.length) rows.push(["computer", "No runner", () => {}, { disabled: true, title: tryState.disabled }]);
  const pick = ["log", "Pick a commit…", () => { tryState.runner ||= runRunner(); return pickCommit(); }, { disabled: !!off, title: off }];
  return popMenu($("#run-pick"), [rows, [pick], checkout, actionRows(), conf], { heading: web ? "Preview on" : "Run on" });
}

async function clickRun() {
  const p = state.project;
  if (!p) throw new Error(projectFirst());
  if (noRunWhy(p)) return configureRun(); // greyed: one click from setting it up
  if (webRun(p)) return toggleServer(); // runs it, or stops it while it runs
  if (running()) {
    // Terminate: stop it, or ask startTry (commitpreview.js's start) to once it has started.
    if (tryState.starting || tryState.pending) { tryState.stopAsked = true; tryState.pending = null; return renderRun(); }
    return webProject(p) ? stopCommitPreview() : stopTry();
  }
  if (!tryState.loaded) await loadTry();
  if (webProject(p) && runPreviewWhy(p)) throw new Error(runPreviewWhy(p));
  if (!tryState.runners.length) throw new Error(tryState.disabled || "No runner can run this project.");
  const id = runRunner();
  if (!id) return runMenu();
  await runOn(id);
}

export function openTry() {
  if (!state.project) throw new Error(projectFirst());
  if (!tryProject()) throw new Error(NOT_CLI);
  closeDetail();
  if (!desktop.matches) setMenu(false);
  showView("try");
}

// "commit…" on the Shell page: the commit picker (a running session shows
// first, and the picker opens on it). Picking a commit opens the page there.
// A web project's (Run ▾ → Pick a commit…) previews the commit picked.
async function pickCommit() {
  if (!state.project) throw new Error(projectFirst());
  if (webProject()) {
    if (!tryState.loaded) await loadTry();
    if (runPreviewWhy()) throw new Error(runPreviewWhy());
    return chooseRunner($("#run-pick"), (runner) => { tryState.runner = runner; return openTryCommits(); });
  }
  if (!tryProject()) throw new Error(NOT_CLI);
  if (state.view !== "try" && tryState.session) return showView("try");
  if (!tryState.loaded) await loadTry();
  if (!tryState.runner) return openTry(); // the page asks for a runner, or says why none can
  await openTryCommits();
}

// focusTry types into the commit's terminal (i on the Shell page).
export function focusTry() { tryTerm?.focus(); }

// Entering the view reattaches to the running session, or starts one at the
// tip of the default branch when the runner is clear. Wide screens get the chat beside it.
export async function enterTry() {
  renderTry();
  await loadTry();
  if (state.view !== "try") return;
  const want = tryState.pending; // a commit picked before the page showed
  tryState.pending = null;
  if (tryState.stopAsked) { tryState.stopAsked = false; renderRun(); } // Terminate, before it started
  else if (want && !tryState.starting) await startTry(want);
  else if (!tryState.session && tryState.runner && !tryState.starting) await startTry({ ref: "" });
  if (desktop.matches && !pane.open && state.project) {
    const keep = document.activeElement?.id === "run-main" ? document.activeElement : null; // Run started it
    toggleChatPane(true, "project");
    keep?.focus();
  }
}

export function leaveTry() {
  disconnectTry();
  // Left before enterTry started a Run (Back undid the switch): it won't start, so the button is Run again.
  if (tryState.pending) { tryState.pending = null; tryState.stopAsked = false; renderRun(); }
}

async function startTry({ ref = tryState.session?.ref || "", runner = tryState.runner } = {}) {
  const p = state.project;
  if (!p) return;
  if (!runner) throw new Error(tryState.runners.length ? "Pick the runner to try it on." : tryState.disabled || "No runner can try a commit of this project.");
  const name = tryState.runners.find((r) => r.id === runner)?.name || "the runner";
  const what = !ref ? `the latest ${p.default_branch || "main"}` : /^[0-9a-f]{7,40}$/i.test(ref) ? shortSHA(ref) : ref;
  tryState.starting = `${what} on ${name}`;
  tryState.runner = runner;
  disconnectTry();
  renderTry();
  renderRun();
  let ok = false;
  try {
    const s = await api("POST", `/api/projects/${p.id}/try`, { runner, ref });
    if (state.project?.id !== p.id) return;
    trySeq++; // a look asked while it started is older than this
    Object.assign(tryState, { session: s, runner: s.runner_id, refused: null });
    ok = true;
  } finally {
    tryState.starting = "";
    if (!ok) tryState.stopAsked = false;
    if (state.project?.id === p.id) { renderTry(); renderTryButton(); }
  }
  // Terminate was pressed while it was starting: stop what started.
  if (tryState.stopAsked && state.project?.id === p.id) await stopTry();
}

async function stopTry() {
  const p = state.project;
  if (!p) return;
  tryState.stopAsked = false;
  tryState.pending = null;
  disconnectTry();
  await api("DELETE", `/api/projects/${p.id}/try`);
  trySeq++; // a look asked while it stopped is older than this
  tryState.session = null;
  renderTry();
  renderTryButton();
  toast("Stopped trying the commit", { icon: "stop" });
}

export function fitTryTerm() { tryT?.fit(); }
// The Try terminal (procTerminal's { term, fit, send, connect, close }), once made.
let tryT = null;
let tryTerm = null;
function tryTermOnce() {
  tryT ||= procTerminal($("#try-term"), { observe: $("#try-term-wrap"), visible: () => state.view === "try" && !$("#try-term-wrap").hidden });
  tryTerm = tryT.term;
  return tryT;
}

function disconnectTry() {
  tryState.ws = null;
  tryState.proc = null;
  tryT?.close();
}

function connectTry(s) {
  disconnectTry();
  const t = tryTermOnce();
  tryState.proc = s.process_id;
  tryState.ws = t.connect(wsURL(s.terminal_url), {
    onClose: ({ got }) => {
      tryState.ws = null;
      tryState.proc = null;
      // Never attached (the runner refused): don't keep trying the same one.
      if (!got) tryState.refused = s.process_id;
      loadTry().catch(() => renderTry()); // it exited, went idle, or the runner left
    },
  });
  // The header's Run button keeps focus when it started the run (it is Terminate now).
  requestAnimationFrame(() => { t.fit(); if (desktop.matches && document.activeElement?.id !== "run-main") t.term.focus(); });
}

function renderTry() {
  const p = state.project, s = tryState.session;
  const runs = [...tryState.runners];
  if (s && !runs.some((r) => r.id === s.runner_id)) runs.push({ id: s.runner_id, name: s.runner });
  const sel = $("#try-runner");
  sel.replaceChildren(...(tryState.runner ? [] : [h("option", { value: "" }, runs.length ? "Pick a runner…" : "No runner")]),
    ...runs.map((r) => h("option", { value: r.id }, r.name)));
  sel.value = tryState.runner;
  sel.disabled = runs.length <= 1 || !!tryState.starting;
  $("#try-runner-wrap").hidden = !runs.length;
  $("#try-sha").textContent = s ? shortSHA(s.commit) : p?.default_branch || "main";
  $("#try-subject").textContent = s ? s.subject : "latest";
  $("#try-commit").title = s ? `${s.commit.slice(0, 12)} · ${s.subject}${s.ref ? "" : ` (tip of ${s.branch})`} on ${s.runner}. Pick another commit` : "Pick the commit to try";
  $("#try-commit").disabled = !tryState.runner || !!tryState.starting;
  $("#try-restart").disabled = !tryState.runner || !!tryState.starting;
  $("#try-stop").disabled = !s || !!tryState.starting;
  renderTryBack();

  const attachable = !!s && s.state === "live" && !tryState.starting && tryState.refused !== s.process_id;
  // A session that exited keeps its last screen, with what to do next below it.
  const shown = attachable || (!!s && !tryState.starting && !!tryTerm && tryState.refused !== s.process_id && s.state === "crashed");
  $("#try-term-wrap").hidden = !shown;
  $("#try-keys").hidden = !attachable;
  if (attachable && tryState.proc !== s.process_id) connectTry(s);
  if (!attachable && tryState.ws) disconnectTry();

  const status = $("#try-status");
  const start = (label = "Start") => ib({ class: "btn primary", type: "button", onclick: attempt(() => startTry({ ref: s?.ref || "" })) }, "play", label);
  const say = (...kids) => { status.hidden = false; status.classList.toggle("strip", shown); status.replaceChildren(...kids); };
  status.hidden = true;
  if (!p) return say(h("strong", {}, "Pick a project"));
  if (!tryProject(p)) return say(h("strong", {}, "Trying a commit is for CLI projects"), h("p", { class: "hint" }, "Set the preview to Terminal in the project's settings: the commit runs its preview command in a terminal on a runner."));
  if (tryState.starting) return say(h("div", { class: "spinner big", "aria-hidden": "true" }), h("strong", {}, "Checking out"), h("p", { class: "hint" }, tryState.starting));
  if (!tryState.loaded) return say(h("div", { class: "spinner big", "aria-hidden": "true" }));
  if (!runs.length) return say(h("strong", {}, "No runner can try a commit"), h("p", { class: "hint" }, tryState.disabled));
  if (!tryState.runner) return say(h("strong", {}, "Pick a runner"), h("p", { class: "hint" }, "The commit runs in a throwaway worktree on the runner you pick, never in its own checkout."));
  if (!s) return say(h("strong", {}, "Not running"), h("p", { class: "hint" }, "It stops after 30 minutes without typing, and removes its worktree."), start());
  if (s.state === "crashed") return say(h("span", { class: "pv-err" }, `Exited${s.exit_code != null ? ` with code ${s.exit_code}` : ""}`), start("Start again"));
  if (tryState.refused === s.process_id) return say(h("strong", {}, "Couldn't attach to the terminal"), h("p", { class: "hint" }, `${s.runner} refused or went away.`),
    ib({ class: "btn", type: "button", onclick: () => { tryState.refused = null; renderTry(); } }, "reload", "Try again"));
}

// The commit picker: recent commits of the default branch from the runner's
// clone, searchable, or any hash, tag or branch typed in.
async function openTryCommits() {
  const p = state.project, runner = tryState.runner;
  if (!p || !runner) throw new Error("Pick the runner first.");
  const cur = tryState.session;
  const branch = p.default_branch || "main";
  const list = h("ul", { class: "try-commit-list", role: "listbox", "aria-label": "Commits" });
  const search = h("input", { type: "search", class: "try-commit-search", placeholder: "Search, or type a hash, tag or branch", "aria-label": "Search commits", autocomplete: "off", spellcheck: "false", autocapitalize: "off" });
  const note = h("p", { class: "hint try-commit-note" });
  const web = webProject(p);
  const { dialog: d } = openSheet({
    title: web ? "Preview a commit" : "Try a commit", cls: "try-commits-sheet", small: false,
    body: [h("p", { class: "hint" }, `${branch} on ${tryState.runners.find((r) => r.id === runner)?.name || cur?.runner || "the runner"}`), search, list, note],
  });
  let shown = [];
  const pick = (ref) => {
    d.close();
    if (web) return previewOnRunner(ref, runner).catch(fail);
    if (state.view === "try") return startTry({ ref, runner }).catch(fail);
    tryState.pending = { ref, runner };
    openTry();
  };
  const row = (label, meta, ref, active, extra = null) => h("li", { role: "none" },
    h("button", { type: "button", class: `try-commit-row${active ? " active" : ""}`, role: "option", "aria-selected": String(!!active), onclick: () => pick(ref) },
      extra, h("span", { class: "try-commit-text" }, h("span", { class: "try-commit-subject" }, label), meta ? h("span", { class: "try-commit-meta" }, meta) : null)));
  const render = (commits, q) => {
    shown = commits;
    const typed = q && !commits.some((c) => c.sha.startsWith(q.toLowerCase()) && commits.length === 1) && /^[\w./~^-]+$/.test(q) && !q.startsWith("-");
    list.replaceChildren(...[
      q ? null : row(`Latest ${branch}`, "fetched from origin when it starts", "", cur && !cur.ref, h("code", {}, branch)),
      typed ? row(`Check out “${q}”`, "a hash, tag or branch", q, false, h("code", {}, "ref")) : null,
      ...commits.map((c) => row(c.subject, `${c.author} · ${agoText(c.date)}`, c.sha, cur?.ref && cur.commit === c.sha, h("code", {}, c.short))),
    ].filter(Boolean));
    note.textContent = commits.length >= 100 ? "Showing the latest 100 matches; search to narrow them." : !commits.length && q ? "No commit on the branch matches; you can still check out what you typed." : "";
  };
  let seq = 0;
  const load = async (q, fetch = false) => {
    const n = ++seq;
    const qs = new URLSearchParams({ runner });
    if (q) qs.set("q", q);
    if (fetch) qs.set("fetch", "1");
    const res = await api("GET", `/api/projects/${p.id}/try/commits?${qs}`);
    if (n !== seq) return;
    if (res.warning) note.textContent = res.warning;
    render(res.commits || [], q);
  };
  search.addEventListener("input", debounce(() => load(search.value.trim()).catch(fail), 250));
  search.addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    e.preventDefault();
    const q = search.value.trim();
    if (shown.length === 1 && q) pick(shown[0].sha);
    else if (q) pick(q);
  });
  list.replaceChildren(h("li", { class: "try-commit-loading", role: "none" }, h("div", { class: "spinner", "aria-hidden": "true" })));
  if (desktop.matches) search.focus();
  await load("", true);
}

$("#shell-at-main").addEventListener("click", () => showView("run"));
$("#run-main").addEventListener("click", attempt(clickRun));
$("#run-pick").addEventListener("click", attempt(async () => {
  if (onRunner() && !tryState.loaded) await loadTry();
  await runMenu();
}));
$("#run-at").addEventListener("click", (e) => {
  if (e.button || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return; // a new tab or window
  e.preventDefault();
  attempt(webProject() ? showRunningPreview : openTry)();
});
$("#shell-at-commit").addEventListener("click", attempt(pickCommit));
$("#try-commit").addEventListener("click", attempt(openTryCommits));
$("#try-restart").addEventListener("click", attempt(() => startTry({ ref: tryState.session?.ref || "" })));
$("#try-stop").addEventListener("click", attempt(stopTry));
$("#try-runner").addEventListener("change", attempt(async (e) => {
  const id = e.target.value;
  if (!id || id === tryState.session?.runner_id) { tryState.runner = id; return renderTry(); }
  await startTry({ ref: tryState.session?.ref || "", runner: id }); // switching runner replaces the session
}));
$("#try-chat").addEventListener("click", () => toggleChatPane(true, "project"));
$("#try-tab-chat").addEventListener("click", () => toggleChatPane(true, "project"));
$("#try-tab-term").addEventListener("click", () => { if (pane.open && !desktop.matches) toggleChatPane(false); });
// Tablist keys: arrows/Home/End move to the other tab and select it.
$(".try-tabs").addEventListener("keydown", (e) => {
  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
  e.preventDefault();
  const tabs = [$("#try-tab-term"), $("#try-tab-chat")];
  const i = tabs.indexOf(document.activeElement);
  const next = e.key === "Home" ? tabs[0] : e.key === "End" ? tabs[1] : tabs[(i + 1) % 2];
  next.focus();
  next.click();
});
$("#chat-try-back").addEventListener("click", () => toggleChatPane(false));
$$("#try-keys [data-key]").forEach((b) => {
  b.addEventListener("mousedown", (e) => e.preventDefault()); // the terminal keeps focus (and the keyboard stays up)
  b.addEventListener("click", () => {
    const k = b.dataset.key;
    trySend({ type: "input", data: PV_KEYS[k] ?? `\x1b${tryTerm?.modes?.applicationCursorKeysMode ? "O" : "["}${PV_ARROWS[k]}` });
  });
});
// The session may stop on the runner (idle, exited): look again now and then while it's shown.
// Elsewhere, a running session is looked at too, so the header's Terminate turns back into Run
// (process.updated, procsChanged below, says so at once; this is for when an event is missed).
setInterval(() => {
  if (document.hidden || tryState.starting || !onRunner()) return;
  if (state.view === "try" || ["live", "starting"].includes(tryState.session?.state)) loadTry().catch(() => {});
}, 20000);

// Only the latest listing counts: an earlier one answered late (a server
// listed while it still stopped) or another project's would put Run out of step.
let procsSeq = 0;
export async function loadProcs() {
  const p = state.project;
  if (!p) return;
  const n = ++procsSeq;
  const procs = await api("GET", `/api/projects/${p.id}/processes`);
  if (n !== procsSeq || state.project?.id !== p.id) return;
  state.procs = procs;
  renderProcs();
}

// procsChanged: a process of the project started or ended (events.js). A
// CLI project's run is one: look at it again, so Run follows it at once
// (its exit, an idle stop, a run started on another device).
export function procsChanged() {
  loadProcs().catch(fail);
  if (onRunner() && !tryState.starting) loadTry().catch(() => {});
}

export function renderProcs() {
  renderRun();
  ensureShell();
}
