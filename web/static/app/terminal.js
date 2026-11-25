import { $, h } from "./dom.js";
import { wsURL } from "./viewport.js";
import { api, here, on } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { projectFirst, state } from "./state.js";
import { showView } from "./nav.js";
import { isHub } from "./projects.js";
import { servesProject } from "../groups.js";
import { clockText } from "../time.js";
import { closeDetail } from "./detail.js";
import { loadProcs, renderProcs } from "./try.js";
import { setMode } from "./vim.js";
import * as store from "./store.js";

// ------------------------------------------------------------------ run: processes & terminal
// The Shell view's and the terminal preview's look.
export const TERM_OPTS = {
  fontFamily: '"JetBrains Mono", ui-monospace, monospace', fontSize: 13, cursorBlink: true,
  theme: { background: "#0a0f17", foreground: "#e6ecf5", cursor: "#8aa4ff", selectionBackground: "#2a384d" },
};
// procTerminal makes an xterm.js terminal in el for a process's WebSocket
// (procWS's protocol: binary frames are output; text frames are JSON, such as
// {type:"exit", code}; it takes {type:"input"} and {type:"resize"}). The terminal
// refits when observe changes size, while visible() says it shows.
// connect(url, { onClose }) attaches it to a socket (closing the one before) and
// returns the socket; onClose({ got }) runs when the socket closes by itself
// (not by close() or another connect), got saying whether anything came.
export function procTerminal(el, { observe = el, visible = () => true } = {}) {
  const term = new window.Terminal(TERM_OPTS);
  const fitter = new window.FitAddon.FitAddon();
  term.loadAddon(fitter);
  term.open(el);
  let ws = null;
  const send = (msg) => { if (ws?.readyState === 1) ws.send(JSON.stringify(msg)); };
  const fit = () => {
    if (!visible()) return;
    try { fitter.fit(); send({ type: "resize", cols: term.cols, rows: term.rows }); } catch {}
  };
  new ResizeObserver(fit).observe(observe);
  term.onData((data) => send({ type: "input", data }));
  // Ctrl-] leaves the terminal and returns to normal mode, like detaching in the TUI.
  term.attachCustomKeyEventHandler((e) => {
    if (e.type === "keydown" && e.ctrlKey && e.key === "]") { term.blur(); setMode(); return false; }
    return true;
  });
  const close = () => { const old = ws; ws = null; old?.close(); };
  const connect = (url, { onClose } = {}) => {
    close();
    term.reset();
    const sock = new WebSocket(url);
    ws = sock;
    let got = false;
    sock.binaryType = "arraybuffer";
    sock.onopen = fit;
    sock.onmessage = (m) => {
      got = true;
      if (typeof m.data !== "string") return term.write(new Uint8Array(m.data));
      const msg = JSON.parse(m.data);
      if (msg.type === "exit") term.write(`\r\n\x1b[90mExited with code ${msg.code}\x1b[0m\r\n`);
    };
    sock.onclose = () => {
      if (ws !== sock) return; // closed on purpose
      ws = null;
      onClose?.({ got });
    };
    return sock;
  };
  return { term, fit, send, connect, close };
}

const shell = procTerminal($("#term"), { visible: () => state.view === "run" });
export const term = shell.term;
export const fitTerm = shell.fit;
export let termWS = null;
export let attached = null;
let attachedProc = null; // the process attached, as started or listed, and its project
let attachedIn = null;

export function attach(p) {
  renderShellNote();
  attached = p.id;
  attachedProc = p;
  attachedIn = state.project?.id;
  renderShellHead();
  termWS = shell.connect(wsURL(on(here(), `/ws/proc/${p.id}`)));
  // Piped (non-PTY) output has bare \n with no tty driver to add the \r.
  term.options.convertEol = p.kind !== "pty";
  requestAnimationFrame(fitTerm);
  renderProcs();
  if (p.kind === "pty") term.focus();
}

export async function startProc(req, show = true) {
  const p = await api("POST", `/api/projects/${state.project.id}/processes`, { ...req, cols: term.cols, rows: term.rows });
  if (req.preset && !req.todo_id) quickStarted.set(`${req.kind}:${req.preset}`, p.id);
  // A runner's checkout of a hub project with uncommitted changes runs them.
  if (p.warning) toast(p.warning, { tone: "paused", head: "Uncommitted changes", ms: 9000 });
  if (!show) return loadProcs();
  closeDetail();
  attach(p);
  showView("run");
  if (p.url && !p.warning) toast(`Server starting at ${p.url}`);
}

// Why no terminal opens for project p (on a task's runner, with runnerID) on the hub,
// from its runners: only on the caller's own runner, and only one that says it
// opens terminals (an older one may not). "" when one may (or the runner hasn't said: it answers itself).
export function shellBlocked(p = state.project, runnerID = "") {
  if (!p || p.prefix || !isHub()) return "";
  const online = state.runners.filter((r) => r.online && (runnerID ? r.id === runnerID : servesProject(r, p)));
  if (!online.length) return "";
  const mine = online.filter((r) => r.mine);
  if (!mine.length) {
    const names = online.map((r) => r.name).join(", ");
    return `Terminals open only on your own runner, and ${names} ${online.length > 1 ? "belong" : "belongs"} to another member. Its owner can open one, or give a runner of yours the labels ${p.name} runs on.`;
  }
  if (mine.every((r) => r.reported && !r.reported.terminals)) {
    return `${mine.map((r) => r.name).join(", ")} doesn't open terminals for the hub: it is older than this hub, update it.`;
  }
  return "";
}

// openShell opens a terminal (req: todo_id, command), or says why it can't.
export function openShell(req = {}, runnerID = "") {
  const why = shellBlocked(state.project, runnerID);
  if (why) throw new Error(why);
  return startProc({ kind: "pty", ...req });
}

// ---- the Shell view's header: where the shell runs, and on a hub the runner a new one opens on

// The runner a process runs on: the hub names it in the processes it lists.
const procRunner = (x) => x?.runner_id || state.runners?.find((r) => r.name === x?.runner)?.id || "";

// shellRunners are the runners of project p a shell may open on, on a hub (none
// elsewhere): the caller's own online ones that open terminals (eligible), and
// the project's others with why not (offline, not yours, update needed).
export function shellRunners(p = state.project) {
  if (!p || p.prefix || !isHub()) return { eligible: [], others: [] };
  const eligible = [], others = [];
  for (const r of state.runners.filter((r) => servesProject(r, p))) {
    const why = !r.mine ? "not yours" : !r.online ? "offline" : r.reported && !r.reported.terminals ? "update needed" : "";
    (why ? others : eligible).push(why ? { ...r, why } : r);
  }
  return { eligible, others };
}

// The runner picked for p's shell (remembered per project), while it is still
// eligible; "" lets the hub choose (its default: one of yours that opens terminals).
const pickKey = (pid) => `dash.shellRunner.${pid}`;
function shellPick(p = state.project) {
  const id = p && store.get(pickKey(p.id), "");
  return id && shellRunners(p).eligible.some((r) => r.id === id) ? id : "";
}

// shortDir shortens a folder for the header: the home folder as ~, and only
// the first and last parts of a long path (~/…/dash).
export function shortDir(d) {
  if (!d) return "";
  const sep = d.includes("/") ? "/" : "\\";
  const home = sep === "/" ? /^\/(home|Users)\/[^/]+(?=\/|$)/ : /^[A-Za-z]:\\Users\\[^\\]+(?=\\|$)/;
  const parts = d.replace(home, "~").split(sep);
  return parts.length > 3 ? [parts[0], "…", parts.at(-1)].join(sep) : parts.join(sep);
}

function renderShellHead() {
  const p = state.project;
  const want = shellPick(p);
  let cur = (attached && state.procs.find((x) => x.id === attached)) || (attachedIn === p?.id ? attachedProc : null);
  // A shell on another runner than the one picked isn't in view (see ensureShell):
  // the header names the picked runner, also when it refused to open one, so the
  // menu doesn't jump back and the shell before can be picked again.
  if (cur?.kind === "pty" && want && procRunner(cur) !== want) cur = null;
  const hub = !!p && !p.prefix && isHub();
  const { eligible, others } = shellRunners(p);
  const menu = hub && eligible.length > 1;
  const curID = procRunner(cur);
  const sel = $("#shell-runner");
  if (menu) {
    const opts = [...eligible];
    if (curID && !opts.some((r) => r.id === curID) && cur?.runner) opts.push({ id: curID, name: cur.runner });
    sel.replaceChildren(...opts.map((r) => h("option", { value: r.id }, r.name)),
      ...others.map((r) => h("option", { value: r.id, disabled: true }, `${r.name} (${r.why})`)));
    sel.value = want || curID || eligible[0].id;
  }
  $("#shell-runner-wrap").hidden = !menu;
  const name = menu ? "" : cur?.runner || (hub ? eligible[0]?.name || "" : p?.prefix ? "" : "This computer");
  $("#shell-runner-name").textContent = name;
  const dir = cur?.dir || "";
  $("#shell-dir").textContent = shortDir(dir);
  $("#shell-dir").title = dir;
  $("#shell-sep").hidden = !dir || !(menu || name);
  $("#shell-head").hidden = !p || !(menu || name || dir);
}

// Picking a runner opens a shell there (or shows the one already open there);
// the shell before keeps running.
$("#shell-runner").addEventListener("change", attempt(async (e) => {
  const p = state.project, id = e.target.value;
  if (!p || !id) return;
  store.set(pickKey(p.id), id);
  forgetShellRefusal();
  const pty = latestShell(id);
  if (pty) return attach(pty);
  renderShellHead();
  await openShellOn(id);
}));

// The latest running shell in the project's checkout, on runner id when given.
function latestShell(id = "") {
  return state.procs.filter((x) => x.kind === "pty" && x.running && inMain(x) && (!id || procRunner(x) === id))
    .reduce((a, b) => (!a || b.started_at > a.started_at ? b : a), null);
}

// openShellOn opens the Shell view's terminal, on runner id when given, or
// says why the runner refused (once, until the runners or the project change).
function openShellOn(id = "") {
  const pid = state.project.id;
  opening = true;
  return startProc({ kind: "pty", ...(id ? { runner_id: id } : {}) })
    .catch((e) => {
      shellRefused = { pid, id, msg: e.message || String(e) };
      if (state.project?.id === pid) renderShellNote(shellRefused.msg);
    })
    .finally(() => { opening = false; });
}

// The Shell view is only a terminal: show the project's running shell, or open one.
// When the runner refuses, say why once instead of asking again with every update
// (until the runners or the project change).
let opening = false;
let shellRefused = null; // { pid, id, msg }
export function forgetShellRefusal() { shellRefused = null; }
function renderShellNote(msg, title = "No terminal here") {
  $("#shell-note").hidden = !msg;
  $("#term-wrap").hidden = !!msg;
  $("#shell-note-title").textContent = title;
  $("#shell-note-text").textContent = msg || "";
}
export function ensureShell() {
  // No project (once the list is in): say so instead of a blank terminal.
  if (!state.project) return state.projectsLoaded && renderShellNote("Select a project to open a shell in it.", "No project");
  if (state.view !== "run" || opening) return;
  renderShellHead();
  // The runner picked for this project's shell: a shell elsewhere isn't kept
  // in view (it keeps running), though another process's output is.
  const want = shellPick();
  const cur = attached && state.procs.find((p) => p.id === attached);
  if (cur && (cur.kind !== "pty" || !want || procRunner(cur) === want)) return renderShellNote();
  const blocked = shellBlocked(); // another member's shell is theirs: the runner won't attach it either
  const pty = !blocked && latestShell(want);
  if (pty) { renderShellNote(); return attach(pty); }
  const pid = state.project.id;
  const why = blocked || (shellRefused?.pid === pid && shellRefused.id === want ? shellRefused.msg : "");
  renderShellNote(why);
  if (why) return;
  openShellOn(want);
}

// Run tests, in the top bar's Run ▾ menu (try.js): while a run goes, it shows its output
// instead of starting it again.
export async function runTests() {
  if (!state.project) throw new Error(projectFirst());
  const proc = quickProc("task:test");
  if (!proc?.running) return startProc({ kind: "task", preset: "test" });
  closeDetail();
  attach(proc);
  showView("run");
}

// The project's server (its run command, in its checkout), which the top bar's Run
// starts and stops (try.js).
export const serverRunning = () => !!state.project && !!quickProc("web:run")?.running;
export async function toggleServer() {
  const proc = quickProc("web:run");
  if (!proc?.running) return startProc({ kind: "web", preset: "run" });
  await api("DELETE", `/api/processes/${proc.id}`);
  toast("Stopping");
  return loadProcs();
}

// The latest server/test/deploy process started from the project's own checkout (not a worktree).
const quickCmd = (spec) => ({ "web:run": state.project?.run_command, "task:test": state.project?.test_command, "task:deploy": state.project?.environments?.[0]?.deploy_command })[spec];
// What a server or deploy command runs on, read from the command itself. First match wins; a rule without a label keeps the generic one.
const TECH_RULES = {
  "web:run": [
    [/\badb\s|gradlew.*install|installDebug|run[-:]android/, "android", "Run on Android"],
    [/xcodebuild|xcrun simctl|ios-deploy|run[-:]ios/, "apple", "Run on iOS"],
    [/\bflutter run\b/, "flutter", "Run Flutter app"],
    [/\bdocker[- ](compose )?(up|run)\b/, "docker"],
    [/\bnext\b/, "nextjs"],
    [/\bvite\b/, "vitejs"],
    [/\bbunx?\b/, "bun"],
    [/\b(npm|npx|pnpm|yarn)\b/, "nodejs"],
    [/\bgo run\b/, "go"],
    [/\bcargo run\b/, "rust"],
    [/\bdotnet run\b/, "dotnetcore"],
    [/manage\.py runserver/, "django"],
  ],
  "task:deploy": [
    [/\bvercel\b/, "vercel", "Deploy to Vercel"],
    [/\bnetlify\b/, "netlify", "Deploy to Netlify"],
    [/\bfirebase deploy\b/, "firebase", "Deploy to Firebase"],
    [/\bwrangler\b/, "cloudflare", "Deploy to Cloudflare"],
    [/\bheroku\b/, "heroku", "Deploy to Heroku"],
    [/\bkubectl\b/, "kubernetes", "Deploy to Kubernetes"],
    [/\bgcloud\b/, "googlecloud", "Deploy to GCP"],
    [/\b(aws|sam|cdk)\b/, "amazonwebservices", "Deploy to AWS"],
    [/\baz\b/, "azure", "Deploy to Azure"],
    [/\bdocker[- ](compose )?(up|run)\b/, "docker"],
  ],
};
export const techFor = (spec) => {
  const cmd = quickCmd(spec);
  const hit = cmd && TECH_RULES[spec]?.find(([re]) => re.test(cmd));
  return hit ? { icon: hit[1], label: hit[2] } : {};
};
// A process running in the project's own checkout, not a task's worktree. Dash marks it
// (main); on a hub its dir is the runner's clone, never the hub's path. Processes from
// before Dash marked them go by their dir.
// A runner older than those marks lists its processes with its clone's dir: there, one
// that isn't a task's (no todo_id, not in a worktree) is taken to be in the checkout.
const inMain = (x) => x.main || (!x.preset && (x.dir === state.project?.path
  || (x.runner && !x.todo_id && !/\/worktrees\//.test(x.dir || ""))));
// The process each quick action last started from this page, by spec: found even when
// its runner doesn't mark it (see inMain).
const quickStarted = new Map();
function quickProc(spec) {
  // Found even with the command cleared, so a run still going can be viewed or stopped.
  const cmd = quickCmd(spec);
  const [kind, preset] = spec.split(":");
  const mine = quickStarted.get(spec);
  return state.procs.filter((x) => x.id === mine || (x.kind === kind && inMain(x) && (x.preset ? x.preset === preset : !!cmd && x.command === cmd)))
    .reduce((a, b) => (!a || b.started_at > a.started_at ? b : a), null);
}

// testStatus says how the project's last test run went, for the Run ▾ menu (try.js):
// null with no test command and no run going. state is "running", "ok", "bad" or "".
export function testStatus() {
  const proc = state.project ? quickProc("task:test") : null;
  if (!state.project || !(quickCmd("task:test") || proc?.running)) return null;
  const running = !!proc?.running;
  const ok = proc && !running && proc.exit_code === 0;
  const bad = proc && !running && proc.exit_code != null && proc.exit_code !== 0;
  const cut = proc && !running && proc.exit_code == null;
  const elapsed = proc ? clockText(proc.started_at) : "0:00";
  return {
    state: running ? "running" : ok ? "ok" : bad ? "bad" : "",
    label: running ? `Testing ${elapsed}` : "Run tests",
    note: running ? "" : ok ? "passed" : bad ? "failed" : cut ? "interrupted" : "",
    title: running ? `Testing… ${elapsed}: click to view output`
      : ok ? "Last test run passed: click to run again"
      : bad ? `Last test run failed (exit ${proc.exit_code}): click to run again`
      : cut ? "Last test run was interrupted by a Dash restart: click to run again"
      : "Run the project's tests (T on Code → Shell)",
  };
}
