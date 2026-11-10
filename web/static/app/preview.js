import { $, $$, fill, h, ib, ico } from "./dom.js";
import { desktop, wsURL } from "./viewport.js";
import { api, on } from "./api.js";
import { attempt, copy, toast } from "./dialogs.js";
import { state } from "./state.js";
import { renderBoard } from "./branches.js";
import { openDetail, renderActions } from "./detail.js";
import { canRework, draw, endDraw, menu, previewsSheet, renderPreviewsBtn, reworkTodo, startDraw } from "./drawmode.js";
import { TERM_OPTS } from "./terminal.js";
import { setMode } from "./vim.js";

// ------------------------------------------------------------------ live preview
// A task in review can run the project's server (run_command) in its worktree.
// Dash proxies it at https://<task>.<preview domain>, so it opens on a phone
// too; the pane shows it beside the task (desktop) or full screen (phone).
export const pv = {
  todo: null,   // task whose preview pane is open
  proc: null,   // process whose log is streamed
  ws: null,
  log: "",
  shown: null,  // process the iframe was loaded for
  crashed: null, // process whose crash already opened the log
  width: (() => { try { return localStorage.getItem("dash.pvWidth") || "full"; } catch { return "full"; } })(),
};
const pvKey = (machine, id) => `${machine || ""}/${id}`;
export const previewOf = (id) => state.previews.get(pvKey(state.project?.machine, id)) || null;
// A CLI project (preview_kind "terminal") previews in a terminal and needs no run_command.
const termProject = () => state.project?.preview_kind === "terminal";
export const isTermPreview = (p) => (p ? p.kind === "terminal" : termProject());
export const canPreview = (t) => t?.status === "review" && !!t.worktree && !t.runner && (termProject() || !!state.project?.run_command);
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
  renderPreviewsBtn();
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

export function hidePreview() {
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

function renderLog() {
  const pre = $("#pv-log-text");
  const end = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
  pre.textContent = pv.log || "No output yet.";
  if (end) pre.scrollTop = pre.scrollHeight;
  const tail = $("#pv-tail");
  if (tail) tail.textContent = lastLines(8) || "No output yet.";
}

// A terminal preview (CLI projects) is the preview's PTY in xterm.js, over
// the WebSocket in its terminal_url (procWS's protocol). Made on first use.
let pvTerm = null;
let pvFit = null;
const pvSend = (msg) => pv.term?.readyState === 1 && pv.term.send(JSON.stringify(msg));
function fitPvTerm() {
  if (!pvTerm || $("#pv-term").hidden) return;
  try { pvFit.fit(); pvSend({ type: "resize", cols: pvTerm.cols, rows: pvTerm.rows }); } catch {}
}
function pvTermOnce() {
  if (pvTerm) return pvTerm;
  pvTerm = new window.Terminal(TERM_OPTS);
  pvFit = new window.FitAddon.FitAddon();
  pvTerm.loadAddon(pvFit);
  pvTerm.open($("#pv-term"));
  new ResizeObserver(fitPvTerm).observe($("#pv-term"));
  pvTerm.onData((data) => pvSend({ type: "input", data }));
  // Ctrl-] leaves the terminal, as in the Shell view.
  pvTerm.attachCustomKeyEventHandler((e) => {
    if (e.type === "keydown" && e.ctrlKey && e.key === "]") { pvTerm.blur(); setMode(); return false; }
    return true;
  });
  return pvTerm;
}

function disconnectTerm() {
  pv.term?.close();
  pv.term = null;
  pv.termProc = null;
}

export function connectTerm(p) {
  disconnectTerm();
  const term = pvTermOnce();
  pv.termProc = p.process_id;
  term.reset();
  const ws = new WebSocket(wsURL(on(p.prefix, p.terminal_url || `/ws/todos/${encodeURIComponent(p.todo_id)}/preview`)));
  ws.binaryType = "arraybuffer";
  ws.onopen = fitPvTerm;
  ws.onmessage = (m) => {
    if (typeof m.data === "string") {
      const msg = JSON.parse(m.data);
      if (msg.type === "exit") term.write(`\r\n\x1b[90mExited with code ${msg.code}\x1b[0m\r\n`);
    } else {
      term.write(new Uint8Array(m.data));
    }
  };
  pv.term = ws;
  requestAnimationFrame(() => { fitPvTerm(); if (desktop.matches) term.focus(); });
}

// The last n lines of the terminal on screen and in its scrollback.
function termBufferTail(n) {
  const buf = pvTerm?.buffer.active;
  if (!buf) return "";
  const lines = [];
  for (let i = Math.max(0, buf.length - n); i < buf.length; i++) lines.push(buf.getLine(i)?.translateToString(true) ?? "");
  return lines.join("\n");
}

// Lines of the terminal preview sent back with "Send back with transcript".
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
  const t = state.todos.get(state.detail);
  const open = !!t && pv.todo === t.id;
  $("#d-preview").hidden = !open;
  $("#detail").classList.toggle("previewing", open);
  if (!open) return;
  const p = previewOf(t.id);
  const termKind = isTermPreview(p);
  if (p && !termKind && p.process_id !== pv.proc) connectLog(p);
  const st = p?.state || "stopped";
  $("#d-preview").dataset.state = st;
  $("#d-preview").dataset.kind = termKind ? "terminal" : "web";
  $("#pv-state").replaceChildren(h("span", { class: `pv-dot ${st}`, "aria-hidden": "true" }),
    p ? `${PV_LABEL[st]}${st === "live" && !termKind ? ` · :${p.port}` : ""}` : "Not running");
  $("#pv-log summary").textContent = termKind ? "Terminal log" : "Server log";
  if (termKind) return renderTermPreview(t, p, st);
  $("#pv-term").hidden = $("#pv-keys").hidden = $("#pv-transcript").hidden = true;
  if (pv.term) disconnectTerm();
  for (const b of $$(".pv-widths button")) b.setAttribute("aria-pressed", String(b.dataset.width === pv.width));
  $("#pv-frame").style.setProperty("--pv-w", pv.width === "full" ? "100%" : `${pv.width}px`);
  $("#pv-frame").classList.toggle("full", pv.width === "full");
  const target = p ? previewTarget(p) : null;
  $("#pv-open").disabled = $("#pv-copy").disabled = !p;
  $("#pv-qr").hidden = !p?.url;
  $("#pv-reload").disabled = !(st === "live" && target?.src);
  const drawable = st === "live" && !!target?.src && !!p.url;
  $("#pv-draw-btn").disabled = !drawable && !draw.on;
  $("#pv-draw-btn").title = drawable || !p || st !== "live" ? "Draw on a snapshot of the page, crop it and add it to the review (D)" : "Drawing needs a preview domain (-preview-domain): the snapshot is taken through Dash's preview proxy";
  $("#pv-stop").disabled = !p;

  const frame = $("#pv-iframe");
  const status = $("#pv-status");
  if (st === "live" && target.src) {
    if (pv.shown !== p.process_id) { pv.shown = p.process_id; frame.src = target.src; }
    frame.hidden = false;
    status.hidden = true;
    return;
  }
  if (pv.shown) { pv.shown = null; frame.src = "about:blank"; }
  frame.hidden = true;
  status.hidden = false;
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
          canRework(t) ? ib({ class: "btn primary", type: "button", onclick: attempt(() => sendBackWithLog(t)) }, "redo", "Send back with this log") : null,
          restart()));
      break;
    case "live": // no preview domain, and not on Dash's own computer
      status.replaceChildren(h("strong", {}, "This device can't open the preview"),
        h("p", { class: "hint" }, "Dash has no preview domain set up (-preview-domain, see docs/previews.md), so the server is only reachable on the computer running Dash: ",
          h("a", { href: p.local_url, target: "_blank", rel: "noopener" }, p.local_url)));
      break;
    default:
      fill(status, h("strong", {}, "The preview isn't running"),
        h("p", { class: "hint" }, "It stops after 30 minutes without traffic, and when the task is approved or sent back."),
        canPreview(t) ? restart() : null);
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
          canRework(t) ? ib({ class: "btn primary", type: "button", onclick: attempt(() => sendBackWithLog(t)) }, "redo", "Send back with this log") : null,
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
  const p = previewOf(pv.todo);
  if (!p || p.state !== "live") return;
  const target = previewTarget(p);
  if (!target.src) return;
  $("#pv-iframe").src = "about:blank";
  requestAnimationFrame(() => { $("#pv-iframe").src = p.url || target.src; });
}

function openQR(url, prefix = "") {
  const d = h("dialog", { class: "sheet small qr-sheet", "aria-label": "QR code" },
    h("form", { method: "dialog" },
      h("h2", {}, "Open on another device"),
      h("img", { class: "qr", src: on(prefix, `/api/qr?text=${encodeURIComponent(url)}`), width: 240, height: 240, alt: `QR code for ${url}` }),
      h("p", { class: "hint qr-url" }, url),
      h("p", { class: "hint" }, "That device signs in to Dash first if it isn't already."),
      h("footer", { class: "sheet-actions" }, ib({ class: "btn primary", value: "ok" }, "check", "Done"))));
  d.addEventListener("close", () => d.remove());
  document.body.append(d);
  d.showModal();
}

$$(".pv-widths button").forEach((b) => b.addEventListener("click", () => {
  pv.width = b.dataset.width;
  try { localStorage.setItem("dash.pvWidth", pv.width); } catch {}
  renderPreview();
}));
$("#pv-reload").addEventListener("click", reloadPreview);
$("#pv-open").addEventListener("click", () => { const p = previewOf(pv.todo); if (p) window.open(p.url || p.local_url, "_blank", "noopener"); });
$("#pv-copy").addEventListener("click", attempt(async () => { const p = previewOf(pv.todo); if (p) await copy(p.url || p.local_url, "Preview link copied"); }));
$("#pv-qr").addEventListener("click", () => { const p = previewOf(pv.todo); if (p?.url) openQR(p.url, p.prefix); });
$("#pv-stop").addEventListener("click", attempt(() => stopPreview()));
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
$("#pv-fab").addEventListener("click", attempt(async () => {
  const t = state.todos.get(pv.todo);
  if (!t) return hidePreview();
  const p = previewOf(t.id);
  const term = isTermPreview(p); // Draw, Reload and links are for web pages
  const choices = [["minimize", "Back to task", hidePreview]];
  if (!term && p?.state === "live" && p.url) choices.push(["edit", "Draw", startDraw]);
  if (!term && p?.state === "live") choices.push(["reload", "Reload", reloadPreview]);
  if (canRework(t) && term && p?.state === "live") choices.push(["redo", pvTerm?.hasSelection() ? "Send back with the selection" : "Send back with transcript", () => sendBackWithTranscript(t)]);
  if (canRework(t)) choices.push(["redo", "Send back", () => (p?.state === "crashed" ? sendBackWithLog(t) : reworkTodo(t).then((sent) => sent && hidePreview()))]);
  if (p) {
    if (!term) choices.push(["external", "Open in a new tab", () => window.open(p.url || p.local_url, "_blank", "noopener")]);
    if (!term && p.url) choices.push(["link", "Copy link", () => copy(p.url, "Preview link copied")]);
    choices.push(["stop", "Stop the preview", () => stopPreview(t.id)]);
  }
  await menu(choices);
}));
