import { $, $$, h, ib } from "./dom.js";
import { desktop, wsURL } from "./viewport.js";
import { api } from "./api.js";
import { attempt, fail, toast } from "./dialogs.js";
import { state, tryState } from "./state.js";
import { showView } from "./nav.js";
import { setMenu } from "./projects.js";
import { closeDetail } from "./detail.js";
import { PV_ARROWS, PV_KEYS } from "./preview.js";
import { agoText } from "./branches.js";
import { pane, renderTryBack, toggleChatPane } from "./chatpane.js";
import { TERM_OPTS, ensureShell, renderQuick } from "./terminal.js";
import { setMode } from "./vim.js";

// ------------------------------------------------------------------ try: the project's CLI on a runner
// (tryState is declared with state, near the top: views render before this runs.)
// Try opens a terminal on the project itself (not a task): its preview_command,
// or a shell, in a throwaway worktree on a runner the user picks, at the tip of
// the default branch (fetched first) or a commit picked from its history. The
// project chat sits beside it (a tab of its own on phones), reads the terminal
// with read_try_terminal, and files what the user finds as issues or draft tasks.
function tryProject(p = state.project) { return !!p && p.preview_kind === "terminal" && !p.prefix; }
function shortSHA(sha) { return (sha || "").slice(0, 7); }
function trySend(msg) { if (tryState.ws?.readyState === 1) tryState.ws.send(JSON.stringify(msg)); }

export async function loadTry() {
  const p = state.project;
  if (tryState.pid !== (p?.id || null)) {
    disconnectTry();
    Object.assign(tryState, { pid: p?.id || null, loaded: false, runners: [], disabled: "", session: null, runner: "", refused: null });
  }
  if (!tryProject(p)) { renderTryButton(); if (state.view === "try") renderTry(); return; }
  const st = await api("GET", `/api/projects/${p.id}/try`);
  if (state.project?.id !== p.id) return;
  Object.assign(tryState, { loaded: true, runners: st.runners || [], disabled: st.disabled || "", session: st.session || null });
  const ids = tryState.runners.map((r) => r.id);
  if (tryState.session) tryState.runner = tryState.session.runner_id;
  else if (!ids.includes(tryState.runner)) tryState.runner = st.default || "";
  renderTryButton();
  if (state.view === "try") renderTry();
}

export function renderTryButton() {
  const btn = $("#quick-try"), why = $("#quick-try-why");
  const show = tryProject();
  const off = show && tryState.loaded && !tryState.runners.length && !tryState.session;
  btn.hidden = !show;
  btn.disabled = off;
  btn.classList.toggle("ok", !!tryState.session && tryState.session.state === "live");
  why.hidden = !off;
  why.textContent = off ? tryState.disabled : "";
  const s = tryState.session;
  btn.title = off ? tryState.disabled
    : s ? `Trying ${shortSHA(s.commit)} on ${s.runner}: open it`
      : "Try the project in a terminal on a runner, at main or an older commit";
}

export function openTry() {
  if (!state.project) throw new Error("Pick a project first.");
  if (!tryProject()) throw new Error("Try is for CLI projects: set the preview to Terminal in the project's settings.");
  closeDetail();
  if (!desktop.matches) setMenu(false); // on phones Try is in the menu drawer
  showView("try");
}

// Entering the view reattaches to the running session, or starts one at the
// tip of the default branch when the runner is clear. Wide screens get the chat beside it.
export async function enterTry() {
  renderTry();
  await loadTry();
  if (state.view !== "try") return;
  if (!tryState.session && tryState.runner && !tryState.starting) await startTry({ ref: "" });
  if (desktop.matches && !pane.open && state.project) toggleChatPane(true, "project");
}

export function leaveTry() {
  disconnectTry();
}

async function startTry({ ref = tryState.session?.ref || "", runner = tryState.runner } = {}) {
  const p = state.project;
  if (!p) return;
  if (!runner) throw new Error(tryState.runners.length ? "Pick the runner to try it on." : tryState.disabled || "No runner can run Try for this project.");
  const name = tryState.runners.find((r) => r.id === runner)?.name || "the runner";
  const what = !ref ? `the latest ${p.default_branch || "main"}` : /^[0-9a-f]{7,40}$/i.test(ref) ? shortSHA(ref) : ref;
  tryState.starting = `${what} on ${name}`;
  tryState.runner = runner;
  disconnectTry();
  renderTry();
  try {
    const s = await api("POST", `/api/projects/${p.id}/try`, { runner, ref });
    if (state.project?.id !== p.id) return;
    Object.assign(tryState, { session: s, runner: s.runner_id, refused: null });
  } finally {
    tryState.starting = "";
    if (state.project?.id === p.id) { renderTry(); renderTryButton(); }
  }
}

async function stopTry() {
  const p = state.project;
  if (!p) return;
  disconnectTry();
  await api("DELETE", `/api/projects/${p.id}/try`);
  tryState.session = null;
  renderTry();
  renderTryButton();
  toast("Try stopped", { icon: "stop" });
}

export function fitTryTerm() {
  if (!tryTerm || state.view !== "try" || $("#try-term-wrap").hidden) return;
  try { tryFit.fit(); trySend({ type: "resize", cols: tryTerm.cols, rows: tryTerm.rows }); } catch {}
}
// The Try terminal, once made.
let tryTerm = null;
let tryFit = null;
function tryTermOnce() {
  if (tryTerm) return tryTerm;
  tryTerm = new window.Terminal(TERM_OPTS);
  tryFit = new window.FitAddon.FitAddon();
  tryTerm.loadAddon(tryFit);
  tryTerm.open($("#try-term"));
  new ResizeObserver(fitTryTerm).observe($("#try-term-wrap"));
  tryTerm.onData((data) => trySend({ type: "input", data }));
  tryTerm.attachCustomKeyEventHandler((e) => {
    if (e.type === "keydown" && e.ctrlKey && e.key === "]") { tryTerm.blur(); setMode(); return false; }
    return true;
  });
  return tryTerm;
}

function disconnectTry() {
  const ws = tryState.ws;
  tryState.ws = null;
  tryState.proc = null;
  ws?.close();
}

function connectTry(s) {
  disconnectTry();
  const term = tryTermOnce();
  term.reset();
  tryState.proc = s.process_id;
  tryState.got = false;
  const ws = new WebSocket(wsURL(s.terminal_url));
  ws.binaryType = "arraybuffer";
  ws.onopen = fitTryTerm;
  ws.onmessage = (m) => {
    tryState.got = true;
    if (typeof m.data === "string") {
      const msg = JSON.parse(m.data);
      if (msg.type === "exit") term.write(`\r\n\x1b[90mExited with code ${msg.code}\x1b[0m\r\n`);
    } else {
      term.write(new Uint8Array(m.data));
    }
  };
  ws.onclose = () => {
    if (tryState.ws !== ws) return; // closed on purpose
    tryState.ws = null;
    tryState.proc = null;
    // Never attached (the runner refused): don't keep trying the same one.
    if (!tryState.got) tryState.refused = s.process_id;
    loadTry().catch(() => renderTry()); // it exited, went idle, or the runner left
  };
  tryState.ws = ws;
  requestAnimationFrame(() => { fitTryTerm(); if (desktop.matches) term.focus(); });
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
  if (!tryProject(p)) return say(h("strong", {}, "Try is for CLI projects"), h("p", { class: "hint" }, "Set the preview to Terminal in the project's settings: Try runs its preview command in a terminal on a runner."));
  if (tryState.starting) return say(h("div", { class: "spinner big", "aria-hidden": "true" }), h("strong", {}, "Checking out"), h("p", { class: "hint" }, tryState.starting));
  if (!tryState.loaded) return say(h("div", { class: "spinner big", "aria-hidden": "true" }));
  if (!runs.length) return say(h("strong", {}, "Try can't run"), h("p", { class: "hint" }, tryState.disabled));
  if (!tryState.runner) return say(h("strong", {}, "Pick a runner"), h("p", { class: "hint" }, "Try runs in a throwaway worktree on the runner you pick, never in its own checkout."));
  if (!s) return say(h("strong", {}, "Not running"), h("p", { class: "hint" }, "Try stops after 30 minutes without typing, and removes its worktree."), start());
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
  const d = h("dialog", { class: "sheet try-commits-sheet", "aria-label": "Pick a commit" },
    h("form", { method: "dialog" },
      h("div", { class: "sheet-head" }, h("h2", {}, "Try a commit"), ib({ class: "icon-btn", value: "cancel", "aria-label": "Close" }, "x")),
      h("p", { class: "hint" }, `${branch} on ${tryState.runners.find((r) => r.id === runner)?.name || cur?.runner || "the runner"}`),
      search, list, note));
  d.addEventListener("close", () => d.remove());
  let shown = [];
  const pick = (ref) => { d.close(); startTry({ ref, runner }).catch(fail); };
  const row = (label, meta, ref, active, extra = null) => h("li", {},
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
  let timer;
  search.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(() => load(search.value.trim()).catch(fail), 250); });
  search.addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    e.preventDefault();
    const q = search.value.trim();
    if (shown.length === 1 && q) pick(shown[0].sha);
    else if (q) pick(q);
  });
  document.body.append(d);
  d.showModal();
  list.replaceChildren(h("li", { class: "try-commit-loading" }, h("div", { class: "spinner", "aria-hidden": "true" })));
  if (desktop.matches) search.focus();
  await load("", true);
}

$("#quick-try").addEventListener("click", attempt(openTry));
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
setInterval(() => { if (state.view === "try" && !document.hidden && !tryState.starting) loadTry().catch(() => {}); }, 20000);

export async function loadProcs() {
  if (!state.project) return;
  state.procs = await api("GET", `/api/projects/${state.project.id}/processes`);
  renderProcs();
}

export function renderProcs() {
  renderQuick();
  const running = state.procs.filter((p) => p.running).length;
  $("#proc-badge").hidden = !running;
  $("#proc-badge").textContent = running;
  ensureShell();
}
