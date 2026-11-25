import { $, docsURL, h, ico } from "./dom.js";
import { raiseToasts, toast } from "./dialogs.js";
import * as store from "./store.js";
import { state } from "./state.js";
import { showView } from "./nav.js";
import { dictationSupported, fieldControl, speech, stopDictation } from "./dictation.js";
import { cursorTodo, keys, laneCount, laneTodos, moveCursor, permKey, todoKey } from "./vim.js";
import { title } from "./branches.js";
import { openDetail } from "./detail.js";
import { toggleChatPane } from "./chatpane.js";

// ------------------------------------------------------------------ voice commands (docs/dictation.md)
// Spoken commands that run what Dash's buttons and keys run, with the
// browser's Web Speech API, like dictation (dictation.js). A preference of this
// browser (Account → Voice), off by default, hidden where the API is missing.
//
// Command mode starts from the mic in the top bar or Ctrl/⌘-Shift-, and listens
// until Esc, "stop listening", an error or the page is hidden. A bar in the
// toasts' corner shows it listens and what it heard. Speech that isn't a
// command is ignored. Commands that change something say what was heard and
// what ran in a toast. Nothing destructive or irreversible (delete, merge,
// deploy, rollback) has a voice command.
//
// While dictating into a field, only a command said on its own, as the whole
// of an utterance (after a pause), runs instead of being typed, and only the
// commands marked field: "send" where the field has one way to send (a chat, a
// comment), and "stop listening". "… and then send" in one breath stays text.

const KEY = "dash.voice";
export const voiceOn = () => dictationSupported && store.flag(KEY);
const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;

// ---- the commands
// say: the phrases (normalize() drops "the", "this", "please" and such).
// field: the phrases that also work said on their own while dictating.
// does: what it did, for the toast of a command that changes something
// (changes); run may return a better text, or null when it said so itself.
// The pages and their tabs, by what is said (scans: the Scans sheet over the Board).
const SECTIONS = { board: "board", scans: "scans", code: "code", shell: "run", files: "files", history: "history", commits: "history", deploys: "deploys", goals: "goals", planning: "planning" };
const COMMANDS = [
  { id: "send", say: ["send", "send it", "send message", "submit"], field: ["send", "send it", "send message"], changes: true, does: "Sent the message", run: send },
  { id: "queue", say: ["queue task", "queue it", "queue draft", "run task", "start task"], changes: true, does: "Queued the task", run: queue },
  { id: "approve", say: ["approve", "approve it", "approve request", "allow", "allow it"], changes: true, does: "Allowed the permission request", run: () => answer("y") },
  { id: "deny", say: ["deny", "deny it", "deny request", "reject"], changes: true, does: "Denied the permission request", run: () => answer("X") },
  { id: "next", say: ["next card", "next task", "next"], run: () => stepCard(1) },
  { id: "previous", say: ["previous card", "previous task", "previous"], run: () => stepCard(-1) },
  { id: "chat", say: ["open chat", "show chat", "go to chat"], run: () => toggleChatPane(true, "project") },
  ...Object.entries(SECTIONS).map(([name, view]) => ({
    id: `open-${name}`, say: [`open ${name}`, `show ${name}`, `go to ${name}`], run: () => showView(view),
  })),
  { id: "stop", say: ["stop listening", "stop", "stop voice commands", "stop dictating", "thats all"], field: ["stop listening", "stop dictating"], run: () => stopCommands() },
];

// Words recognizers hear for the command words, and words around a command.
const SOUNDS_LIKE = { cue: "queue", q: "queue", kew: "queue", approved: "approve", denied: "deny", sent: "send" };
const FILLER = new Set(["the", "this", "that", "a", "an", "please", "now", "ok", "okay", "hey", "dash"]);
export function normalize(text) {
  return text.toLowerCase().replace(/['’]/g, "").replace(/[^\p{L}\p{N}]+/gu, " ").trim().split(" ")
    .map((w) => SOUNDS_LIKE[w] || w).filter((w) => w && !FILLER.has(w)).join(" ");
}
const PHRASES = new Map(COMMANDS.flatMap((c) => c.say.map((p) => [normalize(p), c])));

// match returns the command a result says, trying each alternative the
// recognizer offers, and the words heard: { cmd, said } or null.
function match(result) {
  for (let i = 0; i < result.length; i++) {
    const said = result[i].transcript.trim();
    const cmd = PHRASES.get(normalize(said));
    if (cmd) return { cmd, said };
  }
  return null;
}

// ---- what they run (the functions the buttons and keys call)
// send: the focused chat or comment (Enter), or the New task sheet (Enter: a draft).
function send() {
  const ctl = fieldControl(document.activeElement);
  if (ctl?.send) {
    if (!ctl.send()) throw new Error("The message is empty.");
    return "Sent the message";
  }
  if ($("#quick-sheet").open) { $("#quick-form").requestSubmit(); return "Saving the new task as a draft"; }
  throw new Error("Nothing to send here. Put the cursor in a chat or a comment.");
}

// queue: the task form (Ctrl-Enter), the New task sheet's Queue, else the
// open draft or the draft under the board's cursor (s).
async function queue() {
  // The sheets save as they close, and say how that went.
  if ($("#todo-sheet").open) { $("#todo-sheet").close("queue"); return "Queuing the task in the form"; }
  if ($("#quick-sheet").open) { $("#quick-form").requestSubmit($("#quick-form").querySelector("button[value=queue]")); return "Queuing the new task"; }
  const t = state.detail ? state.todos.get(state.detail) : keys.active && state.view === "board" ? cursorTodo() : null;
  if (!t) throw new Error("Open a draft first.");
  if (t.status !== "draft") throw new Error(`“${title(t)}” isn't a draft.`);
  await todoKey(t, "s");
  return `Queued “${title(t)}”`;
}

// answer: the permission request in view, as y / X do.
async function answer(k) {
  const r = await permKey(k);
  if (r === false) throw new Error("No permission request waiting.");
  return r === true ? undefined : null; // not true: it said why it didn't
}

// stepCard: in an open task, the next or previous task in its column; else the
// board's cursor, as j / k move it.
function stepCard(d) {
  if (!state.project) throw new Error("Pick a project first.");
  if (state.detail) {
    for (let li = 0; li < laneCount(); li++) {
      const list = laneTodos(li), i = list.findIndex((t) => t.id === state.detail);
      if (i < 0) continue;
      const t = list[i + d];
      if (!t) throw new Error(d > 0 ? "This is the last task in its column." : "This is the first task in its column.");
      return openDetail(t.id);
    }
    return;
  }
  if (state.view !== "board") showView("board");
  const { lane, row } = keys.cur;
  moveCursor(lane, keys.active ? row + d : row);
}

// runCmd runs a command heard, with a toast of what was heard and what ran
// for one that changes something, or of why it couldn't.
async function runCmd({ cmd, said }) {
  status(`Heard “${said}”`);
  const head = `Heard “${said}”`;
  try {
    const done = await cmd.run();
    if (cmd.changes && done !== null) toast(done || cmd.does, { tone: "ok", head, icon: "mic" });
  } catch (e) {
    toast(e.message || String(e), { tone: "error", head });
  }
}

// In a field being dictated into: only a field command said on its own.
speech.fieldCommand = (result, ctl) => {
  if (!voiceOn()) return null;
  const m = match(result);
  if (!m?.cmd.field?.some((p) => normalize(p) === normalize(m.said))) return null;
  if (m.cmd.id === "send" && !ctl.send) return null; // nothing to send there: it's a word
  if (m.cmd.id === "stop") return () => {}; // dictation ends; nothing else
  return () => runCmd(m);
};

// ---- command mode
const btn = $("#voice-open");
const MESSAGES = {
  "not-allowed": "Dash can't use the microphone. Allow it in the browser's site settings.",
  "service-not-allowed": "This browser doesn't allow speech recognition here.",
  "audio-capture": "No microphone found.",
  "no-speech": "No speech heard, so voice commands stopped.",
  "network": "Voice commands need the network, and it failed.",
  "language-not-supported": `Voice commands don't support your browser's language (${navigator.language}).`,
};

let session = null;
const statusEl = h("span", { class: "voice-status", "aria-live": "polite" });
const bar = h("div", { class: "toast voice-bar", role: "group", "aria-label": "Voice commands" },
  h("span", { class: "toast-icon" }, ico("mic")),
  h("div", { class: "toast-body" }, h("strong", {}, "Listening for commands"), statusEl),
  h("a", { class: "toast-action", href: docsURL("dictation.md#voice-commands"), target: "_blank", rel: "noopener" }, "Commands"),
  h("button", { class: "toast-action", type: "button", onclick: () => stopCommands() }, "Stop"));
const status = (text) => { statusEl.textContent = text; };

const showButton = () => document.documentElement.classList.toggle("voice-on", voiceOn());
showButton();
addEventListener("storage", (e) => { if (e.key === KEY) { showButton(); if (!voiceOn()) stopCommands(); } });

export function setVoice(on) {
  store.setFlag(KEY, on);
  showButton();
  if (!on) stopCommands();
}

export function toggleCommands() {
  if (session) stopCommands(); else startCommands();
}

function startCommands() {
  if (!voiceOn() || session) return;
  stopDictation();
  const s = session = { rec: null, off: new AbortController(), restarts: [] };
  const listen = () => {
    const rec = s.rec = new Recognition();
    rec.lang = navigator.language || "en-US";
    rec.continuous = true;
    rec.interimResults = true;
    rec.maxAlternatives = 3;
    rec.onresult = (e) => {
      if (session !== s) return;
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const r = e.results[i];
        if (!r.isFinal) { status(`“${r[0].transcript.trim()}”`); continue; }
        const m = match(r);
        if (m) runCmd(m);
        else status(`Heard “${r[0].transcript.trim()}”: not a command`);
      }
    };
    rec.onerror = (e) => {
      if (e.error === "aborted" || session !== s) return;
      toast(MESSAGES[e.error] || `Voice commands stopped (${e.error}).`, true);
      stopCommands();
    };
    // The browser ends a long listen on its own: listen again, unless it
    // keeps ending at once.
    rec.onend = () => {
      if (session !== s) return;
      const now = Date.now();
      s.restarts = [...s.restarts.filter((t) => now - t < 10000), now];
      if (s.restarts.length > 3) { toast("Voice commands stopped: the browser kept ending them.", true); return stopCommands(); }
      try { listen(); } catch { stopCommands(); }
    };
    rec.start();
  };
  try { listen(); } catch (err) {
    session = null;
    toast(`Voice commands didn't start: ${err.message}`, true);
    return;
  }
  const signal = s.off.signal;
  document.addEventListener("visibilitychange", () => { if (document.hidden) stopCommands(); }, { signal });
  // Esc stops listening, before it closes anything.
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape" || e.isComposing) return;
    e.preventDefault();
    e.stopPropagation();
    stopCommands();
  }, { capture: true, signal });
  // Spoken replies stop, so Dash doesn't hear itself (speech.js).
  document.dispatchEvent(new Event("dash:listening"));
  status("Say a command, or “stop listening”.");
  $("#toasts").append(bar);
  raiseToasts();
  btn.classList.add("listening");
  btn.setAttribute("aria-pressed", "true");
  btn.setAttribute("aria-label", "Stop voice commands");
  btn.title = "Stop voice commands (Esc)";
}

export function stopCommands() {
  const s = session;
  if (!s) return;
  session = null;
  s.off.abort();
  try { s.rec?.abort(); } catch { /* already ended */ }
  bar.remove();
  btn.classList.remove("listening");
  btn.setAttribute("aria-pressed", "false");
  btn.setAttribute("aria-label", "Voice commands");
  btn.title = "Voice commands (Ctrl-Shift-,)";
}
speech.stopCommands = stopCommands;
speech.commandsListening = () => !!session;

// The top bar's mic keeps the focus where it was, so "send" finds the field.
for (const ev of ["pointerdown", "mousedown"]) btn.addEventListener(ev, (e) => e.preventDefault());
btn.addEventListener("click", toggleCommands);
// Ctrl/⌘-Shift-, toggles command mode anywhere, in a field too.
document.addEventListener("keydown", (e) => {
  if (!(e.ctrlKey || e.metaKey) || !e.shiftKey || e.altKey || e.code !== "Comma" || !voiceOn()) return;
  e.preventDefault();
  toggleCommands();
}, true);
