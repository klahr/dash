import { $, $$, fill, h, ib, ico } from "./dom.js";
import * as store from "./store.js";
import { desktop, wsURL } from "./viewport.js";
import { api, on } from "./api.js";
import { attempt, copy, openSheet, toast } from "./dialogs.js";
import { state } from "./state.js";
import { renderBoard } from "./branches.js";
import { openDetail, renderActions } from "./detail.js";
import { canRework, draw, endDraw, popMenu, previewsSheet, renderPreviewsStatus, reworkTodo, startDraw } from "./drawmode.js";
import { procTerminal } from "./terminal.js";

// ------------------------------------------------------------------ live preview
// A task in review can run the project's server (run_command) in its worktree.
// Dash proxies it at https://<task>.<preview domain>, so it opens on a phone
// too; the pane shows it beside the task (desktop) or full screen (phone).
export const pv = {
  todo: null,   // task whose preview pane is open
  commit: null, // or History's commit whose preview pane is open (commitpreview.js)
  proc: null,   // process whose log is streamed
  ws: null,
  log: "",
  shown: null,  // process the iframe was loaded for
  crashed: null, // process whose crash already opened the log
  width: store.get("dash.pvWidth", "full"),
};
const pvKey = (machine, id) => `${machine || ""}/${id}`;
export const previewOf = (id) => state.previews.get(pvKey(state.project?.machine, id)) || null;
// A CLI project (preview_kind "terminal") previews in a terminal and needs no run_command.
const termProject = () => state.project?.preview_kind === "terminal";
export const isTermPreview = (p) => (p ? p.kind === "terminal" : termProject());
// A hub's task previews on its runner, where its worktree is; the hub serves the page.
export const canPreview = (t) => t?.status === "review" && !!t.worktree && (termProject() || !!state.project?.run_command);
export const PV_LABEL = { starting: "Starting…", live: "Live", crashed: "Crashed" };
// eslint-disable-next-line no-control-regex
const stripAnsi = (s) => s.replace(/\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-_]|\r(?!\n)/g, "");
const lastLines = (n) => pv.log.trimEnd().split("\n").slice(-n).join("\n");

export async function loadPreviews(m) {
  const list = await api("GET", "/api/previews", null, m.prefix || "");
  for (const [k, p] of state.previews) if (p.machine === m.name) state.previews.delete(k);
  for (const p of list) state.previews.set(pvKey(m.name, p.todo_id), { ...p, machine: m.name, prefix: m.prefix || "" });
  previewsChanged();
}

export function previewEvent(p, m) {
  const key = pvKey(m.name, p.todo_id);
  const prev = state.previews.get(key);
  const ours = state.project && m.name === state.project.machine;
  if (p.state === "stopped") {
    state.previews.delete(key);
    if (ours && prev && p.reason && p.reason !== "stopped") toast(p.title || "", { head: `Preview stopped: ${p.reason}`, icon: "stop" });
  } else {
    state.previews.set(key, { ...p, machine: m.name, prefix: m.prefix || "" });
  }
  if (p.state === "crashed" && prev?.state !== "crashed") {
    if (!(ours && pv.todo === p.todo_id)) toast(p.title || "", { tone: "error", head: "Preview crashed", action: ours ? { label: "Open", run: () => previewTodo(state.todos.get(p.todo_id)) } : null });
  }
  previewsChanged();
}

function previewsChanged() {
  renderPreviewsStatus();
  renderBoard();
  const t = state.todos.get(state.detail);
  if (t) { renderActions(t); renderPreview(); }
  previewsSheet?.render();
}

// The board card's dot: green while the preview is live.
export function previewTag(t) {
  const p = t.status === "review" && previewOf(t.id);
  if (!p) return null;
  return h("span", { class: `pv-tag ${p.state}`, title: `Preview: ${PV_LABEL[p.state] || p.state}` }, PV_LABEL[p.state] || p.state);
}

export function previewButton(t) {
  const p = previewOf(t.id);
  const open = pv.todo === t.id;
  return h("button", { class: `btn pv-btn${open ? " active" : ""}`, "aria-pressed": String(open), onclick: attempt(() => (open ? hidePreview() : previewTodo(t))) },
    p ? h("span", { class: `pv-dot ${p.state}`, "aria-hidden": "true" }) : ico("window"),
    open ? "Hide preview" : "Preview");
}

// previewTodo opens t's preview pane, starting its server if it isn't running.
export async function previewTodo(t) {
  if (!t) return;
  if (pv.commit) hidePreview();
  paneIn($("#detail"));
  if (!termProject() && !state.project?.run_command) throw new Error("Set a start server command in the project's settings to preview tasks.");
  if (!canPreview(t)) throw new Error("Only tasks ready for review can be previewed.");
  if (state.detail !== t.id) await openDetail(t.id);
  pv.todo = t.id;
  renderActions(t);
  renderPreview();
  const cur = previewOf(t.id);
  if (cur && cur.state !== "crashed") return;
  const p = await api("POST", `/api/todos/${t.id}/preview`);
  const m = state.project;
  state.previews.set(pvKey(m.machine, t.id), { ...p, machine: m.machine, prefix: m.prefix || "" });
  previewsChanged();
}

// The pane lives in the task's panel; History's commit pane borrows it.
let commitSrc = null; // commitpreview.js: { preview(), state(), start(), update(), stop(), close() }
export function setCommitSource(src) { commitSrc = src; }
// shownPreview is the preview the pane shows: the task's or the commit's.
export const shownPreview = () => (pv.commit ? commitSrc?.preview() : previewOf(pv.todo)) || null;
// commitShown is the commit pane's state (commitpreview.js's paneState), while it shows one.
export const commitShown = () => (pv.commit ? commitSrc?.state() || null : null);

// paneIn moves the preview pane into host (the task's panel or the commit's).
export function paneIn(host) {
  const el = $("#d-preview");
  if (el.parentElement !== host) host.append(el);
}

export function hidePreview() {
  if (pv.commit) { endDraw(); return commitSrc?.close(); }
  if (!pv.todo) return;
  endDraw();
  pv.todo = null;
  disconnectLog();
  disconnectTerm();
  pv.shown = null;
  $("#pv-iframe").src = "about:blank"; // stops the app (and counts as no traffic)
  $("#pv-iframe").hidden = true;
  $("#d-preview").hidden = true;
  $("#detail").classList.remove("previewing");
  const t = state.todos.get(state.detail);
  if (t) renderActions(t);
}

export async function stopPreview(id = pv.todo) {
  const p = previewOf(id);
  if (!p) return hidePreview();
  await api("DELETE", `/api/todos/${id}/preview`, null, p.prefix);
  if (pv.todo === id) hidePreview();
  toast("Preview stopped");
}

// Where the iframe can load the preview from, if anywhere: through the proxy
// (open_url signs the browser in to the preview's host first), or without a
// preview domain straight from localhost when Dash is open on its own computer.
function previewTarget(p) {
  if (p.url) return { src: on(p.prefix, p.open_url), url: p.url };
  const local = !p.prefix && /^(localhost|127\.0\.0\.1|\[::1\])$/.test(location.hostname);
  return { src: local ? p.local_url : null, url: p.local_url };
}

function disconnectLog() {
  pv.ws?.close();
  pv.ws = null;
  pv.proc = null;
}

export function connectLog(p) {
  disconnectLog();
  pv.proc = p.process_id;
  pv.log = "";
  const dec = new TextDecoder();
  const ws = new WebSocket(wsURL(on(p.prefix, `/ws/proc/${p.process_id}`)));
  ws.binaryType = "arraybuffer";
  ws.onmessage = (m) => {
    if (typeof m.data === "string") return; // exit notice: the event says so too
    pv.log = (pv.log + stripAnsi(dec.decode(m.data, { stream: true }))).slice(-64000);
    renderLog();
  };
  pv.ws = ws;
}

export function renderLog() {
  const pre = $("#pv-log-text");
  const end = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
  pre.textContent = pv.log || "No output yet.";
  if (end) pre.scrollTop = pre.scrollHeight;
  const tail = $("#pv-tail");
  if (tail) tail.textContent = lastLines(8) || "No output yet.";
}

// A terminal preview (CLI projects) is the preview's PTY in xterm.js, over
// the WebSocket in its terminal_url (procWS's protocol). Made on first use.
let pvT = null; // procTerminal's { term, fit, send, connect, close }
let pvTerm = null;
const pvSend = (msg) => pvT?.send(msg);
function pvTermOnce() {
  pvT ||= procTerminal($("#pv-term"), { visible: () => !$("#pv-term").hidden });
  pvTerm = pvT.term;
  return pvT;
}

function disconnectTerm() {
  pvT?.close();
  pv.term = null;
  pv.termProc = null;
}

export function connectTerm(p) {
  disconnectTerm();
  const t = pvTermOnce();
  pv.termProc = p.process_id;
  pv.term = t.connect(wsURL(on(p.prefix, p.terminal_url || `/ws/todos/${encodeURIComponent(p.todo_id)}/preview`)));
  requestAnimationFrame(() => { t.fit(); if (desktop.matches) t.term.focus(); });
}

// The last n lines of the terminal on screen and in its scrollback.
function termBufferTail(n) {
  const buf = pvTerm?.buffer.active;
  if (!buf) return "";
  const lines = [];
  for (let i = Math.max(0, buf.length - n); i < buf.length; i++) lines.push(buf.getLine(i)?.translateToString(true) ?? "");
  return lines.join("\n");
}

// Lines of the terminal preview sent back with "Rework with transcript…".
const TRANSCRIPT_LINES = 200;

// A crashed terminal preview has no log stream: fetch its last lines once.
async function loadTermLog(t, p) {
  if (pv.logFor === p.process_id) return;
  pv.logFor = p.process_id;
  pv.log = "";
  const raw = await api("GET", `/api/todos/${t.id}/preview/log?lines=${TRANSCRIPT_LINES}`, null, p.prefix).catch(() => termBufferTail(TRANSCRIPT_LINES));
  if (pv.logFor !== p.process_id) return;
  pv.log = stripAnsi(String(raw));
  renderLog();
}

const fence = (text) => { const ticks = "`".repeat(Math.max(3, ...(text.match(/`{3,}/g) || []).map((m) => m.length + 1))); return `${ticks}\n${text}\n${ticks}`; };

// Sends the task back with what the terminal preview shows: the part
// selected in the terminal, or else its last TRANSCRIPT_LINES lines.
async function sendBackWithTranscript(t) {
  const p = previewOf(t.id);
  if (!p || !isTermPreview(p)) return;
  const sel = pvTerm?.hasSelection() ? pvTerm.getSelection().trimEnd() : "";
  let text = sel;
  if (!text) {
    const raw = await api("GET", `/api/todos/${t.id}/preview/log?lines=${TRANSCRIPT_LINES}`, null, p.prefix).catch(() => termBufferTail(TRANSCRIPT_LINES));
    text = stripAnsi(String(raw)).trimEnd();
  }
  const head = sel ? "From the terminal preview (the part I selected):" : "The end of the terminal preview's transcript:";
  if (await reworkTodo(t, `${head}\n\n${fence(text)}`)) hidePreview();
}

export function renderPreview() {
  if (pv.commit) return renderCommitPreview();
  const t = state.todos.get(state.detail);
  const open = !!t && pv.todo === t.id;
  $("#d-preview").hidden = !open;
  $("#detail").classList.toggle("previewing", open);
  if (!open) return;
  const p = previewOf(t.id);
  const termKind = isTermPreview(p);
  delete $("#d-preview").dataset.of;
  if (p && !termKind && p.process_id !== pv.proc) connectLog(p);
  const st = p?.state || "stopped";
  $("#d-preview").dataset.state = st;
  $("#d-preview").dataset.kind = termKind ? "terminal" : "web";
  $("#pv-state").replaceChildren(h("span", { class: `pv-dot ${st}`, "aria-hidden": "true" }),
    p ? `${PV_LABEL[st]}${st === "live" && !termKind ? ` · :${p.port}` : ""}` : "Not running");
  $("#pv-log summary").textContent = termKind ? "Terminal log" : "Server log";
  if (termKind) return renderTermPreview(t, p, st);
  const target = renderWebBar(p, st);
  drawButton(st === "live" && !!target?.src && !!p?.url, p, st, "add it to the review");
  $("#pv-stop").disabled = !p;

  const status = $("#pv-status");
  if (showFrame(p, st, target)) return;
  const tail = () => h("pre", { class: "log pv-tail", id: "pv-tail" }, lastLines(8) || "No output yet.");
  const restart = () => ib({ class: "btn", type: "button", onclick: attempt(() => previewTodo(t)) }, "play", "Start again");
  switch (st) {
    case "starting":
      status.replaceChildren(h("div", { class: "spinner big", "aria-hidden": "true" }),
        h("strong", {}, "Starting the server"),
        h("p", { class: "hint" }, h("code", {}, state.project?.run_command || ""), ` · waiting for port ${p.port}…`), tail());
      break;
    case "crashed":
      if (pv.crashed !== p.process_id) { pv.crashed = p.process_id; $("#pv-log").open = true; } // opens on its own, once
      status.replaceChildren(h("strong", { class: "pv-err" }, `The server exited${p.exit_code != null ? ` with code ${p.exit_code}` : ""}`),
        tail(),
        h("div", { class: "pv-status-actions" },
          canRework(t) ? ib({ class: "btn primary", type: "button", onclick: attempt(() => sendBackWithLog(t)) }, "redo", "Rework with this log…") : null,
          restart()));
      break;
    case "live": // no preview domain, and not on Dash's own computer
      status.replaceChildren(h("strong", {}, "This device can't open the preview"),
        p.local_url
          ? h("p", { class: "hint" }, "Dash has no preview domain set up (-preview-domain, see docs/previews.md), so the server is only reachable on the computer running Dash: ",
            h("a", { href: p.local_url, target: "_blank", rel: "noopener" }, p.local_url))
          : h("p", { class: "hint" }, "Dash has no preview domain set up (-preview-domain, see docs/previews.md), so it can't serve the preview from the task's runner."));
      break;
    default:
      fill(status, h("strong", {}, "The preview isn't running"),
        h("p", { class: "hint" }, "It stops after 30 minutes without traffic, and when the task is approved or sent back."),
        canPreview(t) ? restart() : null);
  }
}

// drawButton sets up the bar's Draw: it needs the live page, through the proxy.
function drawButton(drawable, p, st, then) {
  const b = $("#pv-draw-btn");
  b.disabled = !drawable && !draw.on;
  b.title = drawable || !p || st !== "live" ? `Draw on a snapshot of the page, crop it and ${then} (P)` : "Drawing needs a preview domain (-preview-domain): the snapshot is taken through Dash's preview proxy";
}

// renderWebBar sets up a web preview's bar (widths, links, Reload) and
// returns where its page loads from.
function renderWebBar(p, st) {
  updateButton("");
  $("#pv-term").hidden = $("#pv-keys").hidden = $("#pv-transcript").hidden = true;
  if (pv.term) disconnectTerm();
  for (const b of $$(".pv-widths button")) b.setAttribute("aria-pressed", String(b.dataset.width === pv.width));
  $("#pv-frame").style.setProperty("--pv-w", pv.width === "full" ? "100%" : `${pv.width}px`);
  $("#pv-frame").classList.toggle("full", pv.width === "full");
  const target = p ? previewTarget(p) : null;
  $("#pv-open").disabled = $("#pv-copy").disabled = !(p?.url || p?.local_url);
  $("#pv-qr").hidden = !p?.url;
  $("#pv-reload").disabled = !(st === "live" && target?.src);
  return target;
}

// updateButton shows the bar's New commit abc1234 · Update while a preview
// Run started is behind the default branch's newest commit, sha ("": none).
// On a phone the bar is hidden: the actions button gets a dot, and the item.
function updateButton(sha) {
  const b = $("#pv-update"), short = sha.slice(0, 7);
  b.hidden = !sha;
  $("#pv-fab").classList.toggle("has-news", !!sha);
  if (!sha) return;
  b.replaceChildren(ico("merge"), `New commit ${short} · Update`);
  b.title = `Preview ${short}, the newest ${state.project?.default_branch || "main"}, on the same runner (it checks out and sets up again)`;
}
$("#pv-update").addEventListener("click", attempt(() => commitSrc?.update()));

// showFrame shows a live page in the iframe (and says so), else empties it
// and shows the status in its place.
function showFrame(p, st, target) {
  const frame = $("#pv-iframe"), status = $("#pv-status");
  if (st === "live" && target?.src) {
    if (pv.shown !== p.process_id) { pv.shown = p.process_id; frame.src = target.src; }
    frame.hidden = false;
    status.hidden = true;
    return true;
  }
  if (pv.shown) { pv.shown = null; frame.src = "about:blank"; }
  frame.hidden = true;
  status.hidden = false;
  return false;
}

// A commit's preview (History, commitpreview.js): the app at that commit, in
// a throwaway worktree on a runner. Like a task's web preview, without
// Rework: there is no task to send back, so Draw's picture goes to a chat
// instead (drawmode.js). A fresh worktree installs and builds from scratch,
// so while it starts the log shows how far it got.
function renderCommitPreview() {
  const c = commitSrc?.state();
  if (!c) return;
  const p = c.preview;
  const st = c.checkingOut ? "starting" : p?.state || "stopped";
  const el = $("#d-preview");
  el.hidden = false;
  el.dataset.state = st;
  el.dataset.kind = "web";
  el.dataset.of = "commit";
  $("#pv-state").replaceChildren(h("span", { class: `pv-dot ${st}`, "aria-hidden": "true" }),
    c.checkingOut ? "Checking out…" : p ? `${PV_LABEL[st]}${st === "live" ? ` · ${c.short}` : ""}` : "Not running");
  $("#pv-log summary").textContent = "Server log";
  const target = renderWebBar(p, st);
  updateButton(c.newer);
  drawButton(st === "live" && !!target?.src && !!p?.url, p, st, "attach it to a chat");
  $("#pv-stop").disabled = !p;
  if (showFrame(p, st, target)) return;
  const status = $("#pv-status");
  const tail = () => h("pre", { class: "log pv-tail", id: "pv-tail" }, lastLines(12) || "No output yet.");
  const restart = (label = "Start again") => ib({ class: "btn", type: "button", onclick: attempt(() => commitSrc.start()) }, "play", label);
  if (c.checkingOut) {
    return fill(status, h("div", { class: "spinner big", "aria-hidden": "true" }), h("strong", {}, `Checking out ${c.short}`),
      h("p", { class: "hint" }, `On ${c.runner || "a runner"}, in a throwaway worktree. Fetching first can take a moment.`));
  }
  switch (st) {
    case "starting":
      return fill(status, h("div", { class: "spinner big", "aria-hidden": "true" }),
        h("strong", {}, "Setting up and starting the server"),
        h("p", { class: "hint" }, "A fresh worktree installs its dependencies and builds from scratch, so the first start is slow. ",
          h("code", {}, state.project?.run_command || ""), p.port ? ` · waiting for port ${p.port}…` : ""),
        tail());
    case "crashed":
      if (pv.crashed !== p.process_id) { pv.crashed = p.process_id; $("#pv-log").open = true; }
      return fill(status, h("strong", { class: "pv-err" }, `The server exited${p.exit_code != null ? ` with code ${p.exit_code}` : ""}`),
        h("p", { class: "hint" }, "At this commit the setup or start server command failed. The log says why."),
        tail(), h("div", { class: "pv-status-actions" }, restart()));
    case "live": // no preview domain: the hub would have refused it
      return fill(status, h("strong", {}, "This device can't open the preview"),
        h("p", { class: "hint" }, "Dash has no preview domain set up (-preview-domain, see docs/previews.md)."));
    default:
      return fill(status, h("strong", {}, c.other ? `The preview runs at ${c.other}` : "The preview isn't running"),
        h("p", { class: "hint" }, c.other ? "One commit preview runs per project: starting this one stops that one."
          : "It stops after 30 minutes without traffic, and its worktree is removed."),
        c.why ? h("p", { class: "hint" }, c.why) : restart(c.other ? `Preview ${c.short} instead` : "Start"));
  }
}

// A terminal preview's pane: the live terminal, or what became of it. The
// web preview's widths, Draw and link buttons don't apply (CSS hides them).
function renderTermPreview(t, p, st) {
  if (pv.ws) disconnectLog();
  const frame = $("#pv-iframe");
  if (pv.shown) { pv.shown = null; frame.src = "about:blank"; }
  frame.hidden = true;
  const live = st === "live";
  $("#pv-term").hidden = $("#pv-keys").hidden = !live;
  updateButton("");
  $("#pv-transcript").hidden = false;
  $("#pv-transcript").disabled = !live || !canRework(t);
  $("#pv-stop").disabled = !p;
  const status = $("#pv-status");
  if (live) {
    status.hidden = true;
    if (pv.termProc !== p.process_id) connectTerm(p);
    return;
  }
  if (pv.term) disconnectTerm();
  status.hidden = false;
  const cmd = state.project?.preview_command;
  const tail = () => h("pre", { class: "log pv-tail", id: "pv-tail" }, lastLines(8) || "No output yet.");
  const restart = () => ib({ class: "btn", type: "button", onclick: attempt(() => previewTodo(t)) }, "play", "Start again");
  switch (st) {
    case "starting":
      fill(status, h("div", { class: "spinner big", "aria-hidden": "true" }), h("strong", {}, "Opening the terminal"),
        cmd ? h("p", { class: "hint" }, h("code", {}, cmd)) : null);
      break;
    case "crashed":
      loadTermLog(t, p);
      if (pv.crashed !== p.process_id) { pv.crashed = p.process_id; $("#pv-log").open = true; }
      status.replaceChildren(h("strong", { class: "pv-err" }, `The terminal exited${p.exit_code != null ? ` with code ${p.exit_code}` : ""}`),
        tail(),
        h("div", { class: "pv-status-actions" },
          canRework(t) ? ib({ class: "btn primary", type: "button", onclick: attempt(() => sendBackWithLog(t)) }, "redo", "Rework with this log…") : null,
          restart()));
      break;
    default:
      fill(status, h("strong", {}, "The preview isn't running"),
        h("p", { class: "hint" }, "It stops after 30 minutes without typing, and when the task is approved or sent back."),
        canPreview(t) ? restart() : null);
  }
}

// Crashed: send the task back to Claude with the end of the server's log.
async function sendBackWithLog(t) {
  const p = previewOf(t.id);
  const term = isTermPreview(p);
  const raw = await api("GET", `/api/todos/${t.id}/preview/log${term ? `?lines=${TRANSCRIPT_LINES}` : ""}`, null, p?.prefix).catch(() => pv.log);
  const log = stripAnsi(String(raw)).trimEnd().slice(-6000);
  const how = p?.exit_code != null ? `exited with code ${p.exit_code}` : "crashed";
  const cmd = state.project?.preview_command;
  const note = term
    ? `The terminal preview (${cmd ? `\`${cmd}\`` : "a shell"} in the task's worktree, with ./.dash-bin on PATH as $DASH_BIN) ${how}. Make it work. The end of its output:\n\n${fence(log)}`
    : `The preview server (\`${state.project?.run_command || "run_command"}\`, with $PORT set) ${how}. Make it start and serve the app. The end of its log:\n\n\`\`\`\n${log}\n\`\`\``;
  if (await reworkTodo(t, note)) hidePreview();
}

function reloadPreview() {
  const p = shownPreview();
  if (!p || p.state !== "live") return;
  const target = previewTarget(p);
  if (!target.src) return;
  $("#pv-iframe").src = "about:blank";
  requestAnimationFrame(() => { $("#pv-iframe").src = p.url || target.src; });
}

function openQR(url, prefix = "") {
  openSheet({
    title: "Open on another device", cls: "qr-sheet",
    body: [
      h("img", { class: "qr", src: on(prefix, `/api/qr?text=${encodeURIComponent(url)}`), width: 240, height: 240, alt: `QR code for ${url}` }),
      h("p", { class: "hint qr-url" }, url),
      h("p", { class: "hint" }, "That device signs in to Dash first if it isn't already."),
    ],
    footer: ib({ class: "btn primary", value: "ok" }, "check", "Done"),
  });
}

$$(".pv-widths button").forEach((b) => b.addEventListener("click", () => {
  pv.width = b.dataset.width;
  store.set("dash.pvWidth", pv.width);
  renderPreview();
}));
$("#pv-reload").addEventListener("click", reloadPreview);
$("#pv-open").addEventListener("click", () => { const p = shownPreview(); if (p?.url || p?.local_url) window.open(p.url || p.local_url, "_blank", "noopener"); });
$("#pv-copy").addEventListener("click", attempt(async () => { const p = shownPreview(); if (p?.url || p?.local_url) await copy(p.url || p.local_url, "Preview link copied"); }));
$("#pv-qr").addEventListener("click", () => { const p = shownPreview(); if (p?.url) openQR(p.url, p.prefix); });
$("#pv-stop").addEventListener("click", attempt(() => (pv.commit ? commitSrc?.stop() : stopPreview())));
$("#pv-close").addEventListener("click", hidePreview);
$("#pv-transcript").addEventListener("click", attempt(() => { const t = state.todos.get(pv.todo); if (t) return sendBackWithTranscript(t); }));
// Phone: keys a touch keyboard lacks. Arrows follow the app's cursor key mode.
export const PV_KEYS = { esc: "\x1b", tab: "\t", "ctrl-c": "\x03", "ctrl-d": "\x04" };
export const PV_ARROWS = { up: "A", down: "B", right: "C", left: "D" };
$$("#pv-keys [data-key]").forEach((b) => {
  b.addEventListener("mousedown", (e) => e.preventDefault()); // the terminal keeps focus (and the keyboard stays up)
  b.addEventListener("click", () => {
    const k = b.dataset.key;
    pvSend({ type: "input", data: PV_KEYS[k] ?? `\x1b${pvTerm?.modes?.applicationCursorKeysMode ? "O" : "["}${PV_ARROWS[k]}` });
  });
});
// Phone: the preview is full screen; this sheet leads back out.
$("#pv-fab").addEventListener("click", attempt(async (e) => {
  if (pv.commit) {
    const p = shownPreview(), page = [["minimize", "Back to the commit", hidePreview]], link = [], stop = [];
    if (p?.state === "live" && p.url) page.push(["edit", "Draw", startDraw]);
    if (p?.state === "live") page.push(["reload", "Reload", reloadPreview]);
    const newer = commitShown()?.newer;
    if (newer) page.push(["merge", `New commit ${newer.slice(0, 7)} · Update`, () => commitSrc?.update()]);
    if (p?.url) link.push(["external", "Open in a new tab", () => window.open(p.url, "_blank", "noopener")], ["link", "Copy link", () => copy(p.url, "Preview link copied")]);
    if (p) stop.push(["stop", "Stop the preview", () => commitSrc?.stop()]);
    return popMenu(e.currentTarget, [page, link, stop]);
  }
  const t = state.todos.get(pv.todo);
  if (!t) return hidePreview();
  const p = previewOf(t.id);
  const term = isTermPreview(p); // Draw, Reload and links are for web pages
  const page = [["minimize", "Back to task", hidePreview]], rework = [], link = [], stop = [];
  if (!term && p?.state === "live" && p.url) page.push(["edit", "Draw", startDraw]);
  if (!term && p?.state === "live") page.push(["reload", "Reload", reloadPreview]);
  if (canRework(t) && term && p?.state === "live") rework.push(["redo", pvTerm?.hasSelection() ? "Rework with the selection…" : "Rework with transcript…", () => sendBackWithTranscript(t)]);
  if (canRework(t)) rework.push(["redo", "Rework…", () => (p?.state === "crashed" ? sendBackWithLog(t) : reworkTodo(t).then((sent) => sent && hidePreview()))]);
  if (p) {
    if (!term && (p.url || p.local_url)) link.push(["external", "Open in a new tab", () => window.open(p.url || p.local_url, "_blank", "noopener")]);
    if (!term && p.url) link.push(["link", "Copy link", () => copy(p.url, "Preview link copied")]);
    stop.push(["stop", "Stop the preview", () => stopPreview(t.id)]);
  }
  await popMenu(e.currentTarget, [page, rework, link, stop]);
}));
