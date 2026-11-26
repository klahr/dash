import { $, h } from "./dom.js";
import { api } from "./api.js";
import { state, statusLabel } from "./state.js";
import { title } from "./branches.js";
import { cursorTodo, keys } from "./vim.js";
import { stopDictation } from "./dictation.js";
import { hands, hasReadAll, readAll, sayLine, stopSpeaking } from "./speech.js";
import { commandOf, describeCommand, runVoiceCommand, stopCommands } from "./voice.js";
import { todoChat } from "./chat.js";
import { projectChatFor, toggleChatPane } from "./chatpane.js";
import { showNewTasks } from "./todos.js";
import { afterEnd, backoff, classify, hints, initial, modeWord, step } from "../voiceloop.js";

// ------------------------------------------------------------------ voice mode (docs/dictation.md#voice-mode)
// A full-screen view for working hands-free. Starting it (⋯ → Start voice
// mode, or g v) is the user's consent to listening all the time: it listens
// until it is left (Leave, Esc, or "exit voice mode"), and nothing else.
//
// What is said moves one state machine (voiceloop.js: listening, dictating,
// confirming, speaking, paused); this module carries out its effects. The
// app's voice commands (voice.js) work too, and what changes something
// (queueing, sending, approving) is read back and done only on "yes".
//
// Turn-taking: while Dash speaks (speech.js, with hands.on everything is read
// in short), only "stop", "pause" and "exit voice mode" are heard, so Dash
// doesn't obey its own voice; when it is quiet again, recognition restarts,
// which drops what it heard of Dash. A key press cuts speech off too.
//
// The browser ends a listen on its own (after silence, a minute, or a network
// error): voice mode listens again, at once after a normal end, with backoff
// after failures (voiceloop.js: afterEnd, backoff). Only errors that listening
// again can't fix (no microphone permission, no recognition) are shown and said.

const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;
const dlg = $("#voice-mode");
const LABEL = { listening: "Listening", dictating: "Dictating", confirming: "Say yes or no", speaking: "Speaking", paused: "Paused", off: "Not listening" };
const FATAL = {
  "not-allowed": "Dash can't use the microphone. Allow it in the browser's site settings, then start voice mode again.",
  "service-not-allowed": "This browser doesn't allow speech recognition here.",
  "audio-capture": "No microphone found. Connect one, then start voice mode again.",
  "language-not-supported": `Speech recognition doesn't support your browser's language (${navigator.language}).`,
};

// vm is voice mode while it is on: { m (the machine), rec, run (the current
// listen), timer, bo (backoff), fatal, interim, audio, off, ticker }.
let vm = null;

// What the machine asks of the app (voiceloop.js step's ctx).
const openTask = () => (state.detail && state.todos.get(state.detail)) || null;
const ctx = {
  project: () => state.project?.name || "",
  to: () => {
    const t = openTask();
    if (t) return `the chat of ${title(t)}`;
    return state.project ? `${state.project.name}'s chat` : "";
  },
  describe: describeCommand,
};

export function startVoiceMode() {
  if (vm) return;
  $("#project-menu").open = false;
  stopCommands();
  stopDictation();
  stopSpeaking();
  const s = vm = { m: initial(), rec: null, run: null, timer: 0, bo: backoff(), fatal: "", interim: "", audio: null, off: new AbortController(), ticker: 0, shown: "" };
  // Made on the click or key that starts voice mode: browsers allow sound then.
  try { s.audio = new (window.AudioContext || window.webkitAudioContext)(); } catch { /* no sounds */ }
  hands.on = true;
  hands.onSpeak = (text) => { if (vm === s) { showSaid(text); dispatch({ type: "speaking" }); } };
  hands.onQuiet = () => { if (vm === s) { dispatch({ type: "quiet" }); relisten(); } };

  const signal = s.off.signal;
  // On window, before the app's own keys (vim.js, palette.js) see them: in
  // voice mode Esc leaves and any other key cuts speech off; buttons still work.
  addEventListener("keydown", (e) => {
    if (vm !== s || e.isComposing) return;
    e.stopPropagation();
    if (e.key === "Escape") { e.preventDefault(); stopVoiceMode(); return; }
    if (!["Shift", "Control", "Alt", "Meta"].includes(e.key)) dispatch({ type: "key" });
  }, { capture: true, signal });
  dlg.addEventListener("cancel", (e) => { e.preventDefault(); stopVoiceMode(); }, { signal });
  // Hidden, it keeps listening where the browser lets it; seen again, it
  // listens at once instead of waiting out a backoff.
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && vm === s && !s.rec && !s.fatal) listen();
  }, { signal });
  addEventListener("pagehide", stopVoiceMode, { signal });
  s.ticker = setInterval(renderWhere, 1000);

  $("#vm-error").hidden = true;
  showSaid("");
  note("");
  dlg.showModal();
  $("#vm-leave").focus();
  render();
  tone(s, "listening");
  if (!Recognition) return fatal("This browser can't recognise speech. Use Chrome, Edge or Safari for voice mode.");
  speak("Voice mode on. Say help to hear what you can say.");
  listen();
}

export function stopVoiceMode() {
  const s = vm;
  if (!s) return;
  vm = null;
  s.off.abort();
  clearTimeout(s.timer);
  clearInterval(s.ticker);
  try { s.rec?.abort(); } catch { /* already ended */ }
  hands.on = false;
  hands.onSpeak = hands.onQuiet = null;
  stopSpeaking();
  tone(s, "exit");
  setTimeout(() => s.audio?.close?.().catch(() => {}), 600);
  if (dlg.open) dlg.close();
}

// ---- the machine
function dispatch(ev) {
  const s = vm;
  if (!s) return;
  const before = s.m.state;
  const { m, out } = step(s.m, ev, ctx);
  s.m = m;
  if (m.state !== before) { tone(s, m.state); if (m.state !== "dictating") s.interim = ""; }
  render();
  for (const e of out) {
    if (vm !== s) return;
    effect(e);
  }
}

function effect(e) {
  switch (e.do) {
    case "say": return speak(e.text);
    case "stopSpeaking": return stopSpeaking();
    case "note": return note(e.text);
    case "readAll": return hasReadAll() ? readAll() : speak("Nothing was said in short yet.");
    case "exit": return stopVoiceMode();
    case "run": return run(e.id);
    case "act": return act(e.action);
  }
}

// speak says text, and shows it (also where the browser can't speak).
function speak(text) {
  showSaid(text);
  sayLine(text);
}

// run runs an app command that needs no read-back, and says how it went.
async function run(id) {
  const r = await runVoiceCommand(id);
  if (!vm) return;
  if (!r.ok) return speak(r.text);
  if (r.text) return speak(r.text);
  if (id === "next" || id === "previous") {
    const t = openTask() || (keys.active && state.view === "board" ? cursorTodo() : null);
    if (t) speak(`${title(t)}. ${statusLabel(t)}.`);
    return;
  }
  note(`Done: ${id.replace(/^open-/, "opened ")}.`);
}

// act carries out what was confirmed with "yes".
async function act(a) {
  try {
    if (a.kind === "task") {
      const p = state.project;
      if (!p) throw new Error("Open a project first.");
      const t = await api("POST", `/api/projects/${p.id}/todos`, { title: "", prompt: a.text, enqueue: true }, p.prefix || "");
      if (t?.id && state.project === p) showNewTasks("queued", [t.id]);
      speak("Queued.");
    } else if (a.kind === "message") {
      speak(sendMessage(a.text) === "queued" ? "Queued." : "Sent.");
    } else {
      const r = await runVoiceCommand(a.id);
      speak(r.ok ? r.text || "Done." : r.text);
    }
  } catch (e) {
    speak(`That didn't work. ${e.message || e}`);
  }
}

// sendMessage sends text to the open task's side chat (Ask: the agent
// doesn't see it), or else to the project's chat. What is written in the
// composer and its mode stay. It returns "sent" or "queued" (a reply runs).
function sendMessage(text) {
  if (openTask()) return todoChat.post(text);
  if (!state.project) throw new Error("Open a project first.");
  toggleChatPane(true, "project");
  return projectChatFor(state.project).chat.post(text);
}

// ---- listening
// listen starts a listen; its end starts the next (after the backoff).
function listen() {
  const s = vm;
  if (!s || s.fatal || s.rec) return;
  clearTimeout(s.timer);
  const rec = new Recognition();
  rec.lang = navigator.language || "en-US";
  rec.continuous = true;
  rec.interimResults = true;
  rec.maxAlternatives = 3;
  const run = { startedAt: Date.now(), heard: false, error: "", restart: false };
  const mine = () => vm === s && s.run === run;
  s.rec = rec;
  s.run = run;
  rec.onresult = (e) => {
    if (!mine() || run.restart) return;
    run.heard = true;
    s.bo.ok();
    let interim = "";
    for (let i = e.resultIndex; i < e.results.length; i++) {
      const r = e.results[i];
      if (!r.isFinal) { interim += r[0].transcript; continue; }
      // A mode word in any alternative wins ("done" heard as "Dunn").
      const alts = Array.from(r, (a) => a.transcript);
      const text = alts.find((t) => modeWord(t)) ?? alts[0];
      s.interim = "";
      dispatch({ type: "heard", text, cmd: commandOf(r) });
      if (vm !== s) return;
    }
    setInterim(interim);
  };
  rec.onerror = (e) => {
    if (!mine()) return;
    run.error = e.error;
    if (classify(e.error) === "fatal") fatal(FATAL[e.error]);
  };
  rec.onend = () => {
    if (!mine()) return;
    s.rec = null;
    if (s.fatal) return;
    if (!run.restart) {
      const r = afterEnd({ startedAt: run.startedAt, endedAt: Date.now(), heard: run.heard, error: run.error });
      if (r === "fail") s.bo.fail(); else if (r === "ok") s.bo.ok();
    }
    if (s.bo.failures >= 4) note(`Trouble listening${run.error ? ` (${run.error})` : ""}. Trying again…`);
    schedule(run.restart ? 0 : s.bo.delay());
  };
  try {
    rec.start();
  } catch {
    s.rec = null;
    s.bo.fail();
    schedule(s.bo.delay());
  }
}

function schedule(ms) {
  const s = vm;
  if (!s) return;
  clearTimeout(s.timer);
  s.timer = setTimeout(listen, ms);
}

// relisten restarts the listen once Dash is quiet: what it heard of Dash's
// own voice is dropped. The next listen starts when this one has ended.
function relisten() {
  const s = vm;
  if (!s?.rec || s.fatal) return;
  s.run.restart = true;
  try { s.rec.abort(); } catch { s.rec = null; schedule(0); }
}

// fatal shows and says why voice mode can't listen, and stops listening.
function fatal(msg) {
  const s = vm;
  if (!s) return;
  s.fatal = msg || "Voice mode can't listen.";
  try { s.rec?.abort(); } catch { /* already ended */ }
  s.rec = null;
  clearTimeout(s.timer);
  const el = $("#vm-error");
  el.textContent = s.fatal;
  el.hidden = false;
  tone(s, "off");
  render();
  speak(s.fatal);
}

// ---- the view
function render() {
  const s = vm;
  if (!s) return;
  const st = s.fatal ? "off" : s.m.state;
  dlg.dataset.state = st;
  // Set only on a change: the live region reads each new text.
  const label = LABEL[st];
  if ($("#vm-state").textContent !== label) $("#vm-state").textContent = label;
  const paused = st === "paused";
  const pause = $("#vm-pause");
  // The label names the action (as the spoken commands do), so this is a
  // plain button, not a toggle: aria-pressed would read "Resume, pressed".
  pause.textContent = paused ? "Resume" : "Pause";
  pause.disabled = !!s.fatal;
  renderWhere();
  renderDraft();
  const list = s.fatal ? ["exit voice mode"] : hints(s.m);
  const key = list.join("|");
  if (s.shown !== key) {
    s.shown = key;
    $("#vm-hints").replaceChildren(...list.map((x) => h("li", {}, `“${x}”`)));
  }
}

// renderWhere shows the project and the open task, and its state.
function renderWhere() {
  const el = $("#vm-where"), t = openTask();
  const parts = state.project ? ["Project ", h("strong", {}, state.project.name)] : ["No project open"];
  if (t) parts.push(" · Task ", h("strong", {}, title(t)), ` · ${statusLabel(t)}`);
  const text = parts.map((p) => typeof p === "string" ? p : p.textContent).join("");
  if (el.textContent !== text) el.replaceChildren(...parts);
}

// renderDraft shows what is dictated, with the words being heard.
function renderDraft() {
  const s = vm, m = s.m;
  const show = m.state === "dictating" || m.state === "confirming" || (m.state === "paused" && m.draft.length > 0);
  $("#vm-heard-box").hidden = !show;
  if (!show) return;
  $("#vm-heard-h").textContent = m.target === "task" ? "New task" : m.target === "message" ? `Message to ${ctx.to()}` : "Dictated";
  const body = m.pending?.text || m.draft.join(" ");
  $("#vm-heard").replaceChildren(body, s.interim ? h("span", { class: "vm-interim" }, `${body ? " " : ""}${s.interim}`) : "");
}

function setInterim(text) {
  const s = vm;
  if (!s) return;
  text = text.trim();
  // Words heard while Dash speaks are most likely Dash: not shown.
  if (s.m.state === "speaking") return;
  if (s.m.state === "dictating") { s.interim = text; renderDraft(); } else if (text) note(`“${text}”`);
}

const showSaid = (text) => { $("#vm-said").textContent = text || "Nothing yet."; };
const note = (text) => { $("#vm-note").textContent = text; };

$("#vm-leave").addEventListener("click", stopVoiceMode);
$("#vm-pause").addEventListener("click", () => dispatch({ type: "heard", text: vm?.m.state === "paused" ? "resume" : "pause" }));
$("#voicemode-open").addEventListener("click", startVoiceMode);

// ---- sounds
// A short tone on each change of state, so the state is known without
// looking: rising when listening, higher for dictating, a double beep for a
// question, falling for paused, low for an error or leaving.
const TONES = {
  listening: [660, 880], dictating: [880, 1320], confirming: [988, 0, 988], speaking: [523],
  paused: [660, 440], off: [330, 0, 220], exit: [523, 392, 262],
};
function tone(s, name) {
  const ac = s.audio, notes = TONES[name];
  if (!ac || !notes) return;
  try {
    if (ac.state === "suspended") ac.resume?.();
    let t = ac.currentTime + 0.02;
    for (const f of notes) {
      if (f) {
        const o = ac.createOscillator(), g = ac.createGain();
        o.type = "sine";
        o.frequency.value = f;
        g.gain.setValueAtTime(0.0001, t);
        g.gain.exponentialRampToValueAtTime(0.12, t + 0.015);
        g.gain.exponentialRampToValueAtTime(0.0001, t + 0.11);
        o.connect(g).connect(ac.destination);
        o.start(t);
        o.stop(t + 0.12);
      }
      t += 0.12;
    }
  } catch { /* no sound: the screen and the live region still say it */ }
}
