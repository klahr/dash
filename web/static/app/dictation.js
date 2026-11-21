import { h, ico } from "./dom.js";
import { toast } from "./dialogs.js";
import * as store from "./store.js";

// ------------------------------------------------------------------ dictation (docs/dictation.md)
// A mic button on a prompt field that types what you say, with the browser's
// Web Speech API. Everything happens in the browser: Chrome sends the audio to
// Google to transcribe it, Safari may do it on the device. It is a preference
// of this browser (Account → Dictation), off by default, and the button stays
// hidden where the API is missing (Firefox).
//
// dictation(field, opts) attaches a button to a textarea, or to an mdEditor
// (editor.js) through { ed }. While it listens the field shows the words heard
// so far; the final words go in at the cursor (or over the selection), with a
// space between them and the text around them, and the field gets an input
// event. Click again, Esc or Ctrl/⌘-Shift-. stops; typing, leaving the field,
// closing its dialog or hiding the page stops too and keeps what is shown.
//
// Voice commands (voice.js) share the microphone: starting one stops the other.
// With them on, a command said on its own while dictating ("send", "stop
// listening") runs instead of being typed; see speech.fieldCommand.

const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;
export const dictationSupported = !!Recognition;
const KEY = "dash.dictation";
export const dictationOn = () => dictationSupported && store.flag(KEY);

// The buttons show while the preference is on (style.css: .dictate-btn).
const showButtons = () => document.documentElement.classList.toggle("dictation-on", dictationOn());
showButtons();
addEventListener("storage", (e) => { if (e.key === KEY) { showButtons(); if (!dictationOn()) end(true); } });

// voice.js fills these in. fieldCommand(result, ctl) gets each final result
// heard in a field and returns a function to run in place of typing it, or
// null; stopCommands ends command mode before dictation starts.
export const speech = { fieldCommand: null, stopCommands: null };

export function setDictation(on) {
  store.setFlag(KEY, on);
  showButtons();
  if (!on) end(true);
}

// A field the button types into: a plain textarea, or an mdEditor's view.
function textareaTarget(ta) {
  return {
    owner: ta,
    readOnly: () => ta.readOnly || ta.disabled,
    value: () => ta.value,
    range: () => [ta.selectionStart, ta.selectionEnd],
    replace(from, to, text) {
      ta.setRangeText(text, from, to, "end");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    },
    focus: () => ta.focus(),
  };
}

function editorTarget(ed) {
  const v = ed.view;
  return {
    owner: ed.el,
    readOnly: () => v.state.readOnly,
    value: () => v.state.doc.toString(),
    range: () => [v.state.selection.main.from, v.state.selection.main.to],
    // Interim text is left out of undo; a final replace first takes the
    // interim text away (also untracked), so one undo removes what was said.
    replace(from, to, text, { final, interim }) {
      if (final && interim && to > from) {
        v.dispatch({ changes: { from, to, insert: "" }, annotations: ed.untracked });
        to = from;
      }
      v.dispatch({
        changes: { from, to, insert: text }, selection: { anchor: from + text.length }, scrollIntoView: true,
        userEvent: "input.dictate", ...(final ? {} : { annotations: ed.untracked }),
      });
    },
    focus: () => v.focus(),
  };
}

const MESSAGES = {
  "not-allowed": "Dash can't use the microphone. Allow it in the browser's site settings.",
  "service-not-allowed": "This browser doesn't allow speech recognition here.",
  "audio-capture": "No microphone found.",
  "no-speech": "No speech heard, so dictation stopped.",
  "network": "Dictation needs the network, and it failed.",
  "language-not-supported": `Dictation doesn't support your browser's language (${navigator.language}).`,
};

const fields = new WeakMap(); // a field's element or its button → its control, for the shortcut
let session = null;

// dictation attaches a mic button to field (a textarea, or { ed } for an
// mdEditor) and returns it. place(button) puts it in the page (default: right
// after the field); text adds a label beside the icon; cls is the button's class.
// send, where the field has one way to send what is in it (a chat, a comment),
// sends it and returns whether there was something to send: voice commands' "send".
export function dictation(field, { place, text = false, cls = "icon-btn", send = null } = {}) {
  const target = field.ed ? editorTarget(field.ed) : textareaTarget(field);
  const btn = h("button", {
    type: "button", class: `${cls} dictate-btn`, "aria-pressed": "false",
    "aria-label": "Dictate", title: "Dictate (Ctrl-Shift-.)",
  }, ico("mic"), text ? "Dictate" : null);
  const ctl = { target, btn, send };
  // The field keeps the focus (and a phone its keyboard) when the button is pressed.
  for (const ev of ["pointerdown", "mousedown"]) btn.addEventListener(ev, (e) => e.preventDefault());
  btn.addEventListener("click", () => toggle(ctl));
  for (const el of [target.owner, btn]) {
    el.addEventListener("keydown", (e) => keydown(ctl, e), true);
    // A tick later, so focus moving between the field and its button isn't leaving.
    el.addEventListener("focusout", () => setTimeout(() => {
      const a = document.activeElement;
      if (session?.ctl === ctl && !target.owner.contains(a) && a !== btn) end(true);
    }));
    // Typing, pasting or dropping ends dictation and keeps what it shows.
    el.addEventListener("beforeinput", () => { if (session?.ctl === ctl) end(true); }, true);
  }
  fields.set(target.owner, ctl);
  fields.set(btn, ctl);
  if (place) place(btn); else (field.ed ? field.ed.el : field).after(btn);
  return btn;
}

const isShortcut = (e) => (e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.code === "Period";

function keydown(ctl, e) {
  if (session?.ctl !== ctl) return;
  if (e.currentTarget === ctl.btn && (e.key === "Enter" || e.key === " ")) return; // the button's own click stops it
  if (e.key === "Escape") {
    // Esc stops listening; it doesn't close the dialog too.
    e.preventDefault();
    e.stopPropagation();
    stop();
  } else if (!isShortcut(e) && !["Shift", "Control", "Alt", "Meta"].includes(e.key) && !e.isComposing) {
    end(true); // a key, Enter (send) included: keep what is shown, then let it act
  }
}

// fieldControl is the dictation control of the field el is in, or null:
// { target, btn, send }.
export function fieldControl(el) {
  for (; el; el = el.parentElement) {
    const ctl = fields.get(el);
    if (ctl) return ctl;
  }
  return null;
}

// Ctrl/⌘-Shift-. toggles dictation in the focused field that has a button.
document.addEventListener("keydown", (e) => {
  if (!isShortcut(e) || !dictationOn()) return;
  const ctl = fieldControl(document.activeElement);
  if (!ctl) return;
  e.preventDefault();
  toggle(ctl);
}, true);

function toggle(ctl) {
  if (session?.ctl === ctl) return stop();
  start(ctl);
}

function start(ctl) {
  end(true);
  const { target, btn } = ctl;
  if (!dictationOn() || target.readOnly()) return;
  speech.stopCommands?.();
  if (!target.owner.contains(document.activeElement)) target.focus();
  const rec = new Recognition();
  rec.lang = navigator.language || "en-US";
  rec.continuous = true;
  rec.interimResults = true;
  const [from, to] = target.range();
  // [from, to) is what the next words replace: the selection at first, then
  // the interim text shown (interim true).
  const s = session = { ctl, rec, from, to, interim: false, interimText: "", done: false, off: new AbortController() };
  const signal = s.off.signal;

  rec.onresult = (e) => {
    if (s.done) return;
    if (!target.owner.isConnected || !target.owner.getClientRects().length) return end(true); // the field went away
    let fin = "", mid = "";
    for (let i = e.resultIndex; i < e.results.length; i++) {
      const r = e.results[i];
      const cmd = r.isFinal && speech.fieldCommand?.(r, ctl);
      if (cmd) {
        // The words before it go in; the command's own words don't.
        if (fin.trim()) write(s, fin, true);
        end(false);
        cmd();
        return;
      }
      if (r.isFinal) fin += ` ${r[0].transcript}`; else mid += ` ${r[0].transcript}`;
    }
    if (fin.trim()) write(s, fin, true);
    write(s, mid, false);
  };
  rec.onerror = (e) => {
    if (e.error === "aborted") return;
    toast(MESSAGES[e.error] || `Dictation stopped (${e.error}).`, true);
    end(true);
  };
  rec.onend = () => { if (session === s) end(true); };

  target.owner.closest("dialog")?.addEventListener("close", () => end(true), { signal });
  document.addEventListener("visibilitychange", () => { if (document.hidden) end(true); }, { signal });
  try { rec.start(); } catch (err) {
    session = null;
    toast(`Dictation didn't start: ${err.message}`, true);
    return;
  }
  btn.classList.add("listening");
  btn.setAttribute("aria-pressed", "true");
  btn.setAttribute("aria-label", "Stop dictating");
  btn.title = "Stop dictating (Esc)";
}

// write puts text over [from, to): interim text stays replaceable, final text
// is kept and the next words go after it. A space separates it from the text
// around it where needed.
function write(s, text, final) {
  const t = s.ctl.target;
  let ins = text.replace(/\s+/g, " ").trim();
  if (!ins && !s.interim) return;
  if (s.to > t.value().length) s.from = s.to = t.value().length; // changed under us: add at the end
  // The words replace the selection: take it away first, so undo brings it back.
  if (!s.interim && s.to > s.from) { t.replace(s.from, s.to, "", { final: true, interim: false }); s.to = s.from; }
  const v = t.value();
  let trail = 0;
  if (ins) {
    const before = v[s.from - 1], after = v[s.to];
    if (before && !/\s/.test(before) && !/^[,.;:!?]/.test(ins)) ins = ` ${ins}`;
    if (after && !/[\s,.;:!?)\]}]/.test(after)) { ins += " "; trail = 1; }
  }
  t.replace(s.from, s.to, ins, { final, interim: s.interim });
  if (final) {
    // The next words go before the trailing space, so they get one of their own.
    s.from = s.to = s.from + ins.length - trail;
    s.interim = false;
    s.interimText = "";
  } else {
    s.to = s.from + ins.length;
    s.interim = !!ins;
    s.interimText = text;
  }
}

// stopDictation ends dictation in any field, keeping what it shows.
export const stopDictation = () => end(true);

// stop asks for the last words, which arrive as a final result before onend.
function stop() {
  if (!session) return;
  try { session.rec.stop(); } catch { end(true); }
}

// end stops listening now; keep turns the interim text shown into final text.
function end(keep) {
  const s = session;
  if (!s) return;
  session = null;
  s.done = true;
  s.off.abort();
  if (s.interim && s.ctl.target.owner.isConnected) {
    if (keep) write(s, s.interimText, true);
    else write(s, "", false);
  }
  try { s.rec.abort(); } catch { /* already ended */ }
  const { btn } = s.ctl;
  btn.classList.remove("listening");
  btn.setAttribute("aria-pressed", "false");
  btn.setAttribute("aria-label", "Dictate");
  btn.title = "Dictate (Ctrl-Shift-.)";
}
