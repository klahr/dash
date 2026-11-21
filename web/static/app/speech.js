import { ib } from "./dom.js";
import * as store from "./store.js";
import { state } from "./state.js";
import { describePerm, isQuestion } from "./chatparts.js";
import { listening } from "./dictation.js";

// ------------------------------------------------------------------ spoken replies (docs/dictation.md)
// Reads Claude's chat replies, approval requests and task status changes
// aloud with the browser's speechSynthesis, so the chats and tasks can be
// followed without looking. Everything happens in the browser; some voices
// (Chrome's Google voices, Edge's online ones) are an online service the
// browser sends the text to. It is a preference of this browser
// (Account → Spoken replies), off by default, with a switch for each kind of
// news; where the API is missing the setting is greyed out and nothing shows.
//
// What is read is plain prose: Markdown is stripped, a code block, diff or
// table is named and counted ("A code block, 12 lines."), a long path is its
// file name, a link its text and a bare URL its host. A long reply is read up to its first part,
// then "There's more" (or in full: Account → Spoken replies → Long replies).
// The speaker button on a reply reads all of it, or stops.
//
// Esc, sending a message or pressing a speaker button again stops it. Dash
// doesn't speak while dictation or voice commands listen (they send
// "dash:listening" as they start), so it doesn't hear itself.

const synth = window.speechSynthesis;
export const speechSupported = !!synth && typeof window.SpeechSynthesisUtterance === "function";

const KEY = "dash.speech";
export const speechOn = () => speechSupported && store.flag(KEY);
// The kinds of news to read: on unless switched off.
const KINDS = { replies: "dash.speech.replies", approvals: "dash.speech.approvals", status: "dash.speech.status" };
export const speechKind = (kind) => store.get(KINDS[kind]) !== "0";
export const setSpeechKind = (kind, on) => store.setFlag(KINDS[kind], on);
export const speechVoice = () => store.get("dash.speech.voice", "");
export const setSpeechVoice = (uri) => store.set("dash.speech.voice", uri || null);
export const speechRate = () => Number(store.get("dash.speech.rate")) || 1;
export const setSpeechRate = (r) => store.set("dash.speech.rate", r && Number(r) !== 1 ? r : null);
export const speechLong = () => store.get("dash.speech.long", "");
export const setSpeechLong = (v) => store.set("dash.speech.long", v || null);

// The speaker buttons show while the preference is on (style.css: .speak-btn).
const showButtons = () => document.documentElement.classList.toggle("speech-on", speechOn());
showButtons();
addEventListener("storage", (e) => { if (e.key === KEY) { showButtons(); if (!speechOn()) stopSpeaking(); } });

export function setSpeech(on) {
  store.setFlag(KEY, on);
  showButtons();
  if (!on) stopSpeaking();
}

// ---- voices
// The browser lists its voices late (voiceschanged); "dash:voices" tells the
// settings to show them.
let voices = [];
function loadVoices() {
  voices = synth.getVoices() || [];
  document.dispatchEvent(new Event("dash:voices"));
}
if (speechSupported) {
  loadVoices();
  if (synth.addEventListener) synth.addEventListener("voiceschanged", loadVoices);
  else synth.onvoiceschanged = loadVoices;
}
const langOf = (v) => (v || "").replace(/_/g, "-").toLowerCase();
const myLang = () => langOf(navigator.language || "en-US");

// speechVoices lists the voices for the settings: the browser's language first.
export function speechVoices() {
  const mine = myLang(), primary = mine.split("-")[0];
  const rank = (v) => langOf(v.lang) === mine ? 0 : langOf(v.lang).split("-")[0] === primary ? 1 : 2;
  return [...voices].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
}

// pickVoice is the voice chosen in the settings, or one in the browser's
// language (its default, or one on the device, preferred), or null: the
// browser picks from the utterance's lang.
function pickVoice() {
  const chosen = speechVoice();
  if (chosen) {
    const v = voices.find((x) => x.voiceURI === chosen);
    if (v) return v;
  }
  const mine = myLang(), primary = mine.split("-")[0];
  const best = (list) => list.find((v) => v.default) || list.find((v) => v.localService) || list[0];
  return best(voices.filter((v) => langOf(v.lang) === mine)) || best(voices.filter((v) => langOf(v.lang).split("-")[0] === primary)) || null;
}

// ---- Markdown to speech
const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
// A path of three parts or more is read as its last one: "web/static/app/chat.js" → "chat.js".
const PATH = /(?:~|\.{1,2})?\/?(?:[\w@.+-]+\/){2,}([\w@.+-]+)/g;
const shortPaths = (s) => s.replace(PATH, "$1");
const URL_RE = /\bhttps?:\/\/[^\s)>\]]+/g;
const hostOf = (u) => { try { return new URL(u).hostname.replace(/^www\./, ""); } catch { return "a link"; } };

// inline makes a line of Markdown plain prose.
function inline(s) {
  return shortPaths(s
    .replace(/<[^>]+>/g, "")
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, (_, alt) => alt ? `image: ${alt}` : "an image")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/`([^`]+)`/g, (_, code) => {
      const c = shortPaths(code.trim());
      return c.length > 40 ? "some code" : c;
    })
    .replace(URL_RE, hostOf)
    .replace(/(\*\*|__)(.+?)\1/g, "$2")
    .replace(/~~(.+?)~~/g, "$1")
    .replace(/(^|[^\w*])[*_](?=\S)(.+?)(?<=\S)[*_](?![\w*])/g, "$1$2"))
    .replace(/\s+/g, " ")
    .trim();
}
// A line read on its own (a heading, an item) ends with a pause.
const stop = (s) => !s || /[.!?:;]$/.test(s) ? s : `${s}.`;

// speakable turns Markdown into what is read aloud: paragraphs of plain
// prose, separated by blank lines.
export function speakable(md) {
  const lines = String(md || "").replace(/\r\n?/g, "\n").split("\n");
  const out = [];
  let para = [];
  const flush = () => { if (para.length) out.push(para.join(" ")); para = []; };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const fence = /^\s{0,3}(`{3,}|~{3,})\s*([\w+-]*)/.exec(line);
    if (fence) {
      flush();
      const body = [];
      for (i++; i < lines.length && !lines[i].trimStart().startsWith(fence[1]); i++) body.push(lines[i]);
      const diff = fence[2] === "diff" || fence[2] === "patch" || (body.length > 1 && body.every((l) => /^([+\- @]|$)/.test(l)) && body.some((l) => /^[+-]/.test(l)));
      out.push(`${diff ? "A diff" : "A code block"}, ${plural(body.length, "line")}.`);
      continue;
    }
    if (/^\s*\|/.test(line)) {
      flush();
      let rows = 0;
      for (; i < lines.length && /^\s*\|/.test(lines[i]); i++) if (!/^\s*\|[\s|:-]+\|?\s*$/.test(lines[i])) rows++;
      i--;
      out.push(`A table, ${plural(Math.max(0, rows - 1), "row")}.`);
      continue;
    }
    if (!line.trim() || /^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) { flush(); continue; }
    const head = /^\s{0,3}#{1,6}\s+(.*)$/.exec(line);
    if (head) { flush(); out.push(stop(inline(head[1].replace(/\s#+\s*$/, "")))); continue; }
    const item = /^\s*(?:>\s*)*(?:[-*+]|\d+[.)])\s+(.*)$/.exec(line);
    if (item) { flush(); para.push(stop(inline(item[1].replace(/^\[[ xX]\]\s*/, "")))); flush(); continue; }
    const text = inline(line.replace(/^\s*(?:>\s?)+/, ""));
    if (text) para.push(text);
  }
  flush();
  return out.filter(Boolean).join("\n\n");
}

// firstPart is the start of text, cut at a sentence, and a note that there's
// more, when text is long. The speaker button reads all of it.
const FIRST = 600;
export function firstPart(text) {
  if (text.length <= FIRST * 1.4) return text;
  const head = text.slice(0, FIRST);
  const cut = Math.max(head.lastIndexOf(". "), head.lastIndexOf("! "), head.lastIndexOf("? "), head.lastIndexOf("\n"));
  return `${(cut > FIRST / 3 ? head.slice(0, cut + 1) : head.slice(0, head.lastIndexOf(" ") + 1)).trim()} … There's more: use the speaker button to hear all of it.`;
}

// chunks splits text into pieces of a sentence or so: Chrome stops a long
// utterance after about 15 seconds.
function chunks(text, max = 220) {
  const out = [];
  for (const sentence of text.match(/[^.!?\n]+(?:[.!?]+|\n+|$)/g) || []) {
    let s = sentence.trim();
    while (s.length > max) {
      let at = s.lastIndexOf(", ", max);
      if (at < max / 3) at = s.lastIndexOf(" ", max);
      if (at < max / 3) at = max;
      out.push(s.slice(0, at + 1).trim());
      s = s.slice(at + 1).trim();
    }
    const last = out.length - 1;
    if (s && last >= 0 && out[last].length + s.length < max / 2) out[last] += ` ${s}`;
    else if (s) out.push(s);
  }
  return out;
}

// ---- speaking
// Items wait in a queue and are read one after another; a speaker button
// reads its reply now instead. An item: {text, btn}, btn the speaker button
// pressed while it is read.
let queue = [];
let current = null;
export const speaking = () => !!current;

function press(btn, on) {
  if (!btn) return;
  btn.setAttribute("aria-pressed", String(on));
  btn.classList.toggle("speaking", on);
  btn.title = on ? "Stop reading (Esc)" : "Read this reply aloud";
}

// say queues text (now: stops what is read and reads it at once). It reads
// nothing while dictation listens, or with the preference off.
function say(text, { btn = null, now = false } = {}) {
  if (!speechOn() || listening() || !text?.trim()) return;
  if (now) stopSpeaking();
  queue.push({ text, btn });
  if (!current) next();
}

function next() {
  const item = current = queue.shift() || null;
  if (!item) return;
  press(item.btn, true);
  const voice = pickVoice(), rate = speechRate();
  const parts = chunks(item.text);
  if (!parts.length) { finish(item); return; }
  // The utterances are kept on the item: Chrome drops events of one nobody holds.
  item.utts = parts.map((part, k) => {
    const u = new SpeechSynthesisUtterance(part);
    u.lang = voice?.lang || navigator.language || "en-US";
    if (voice) u.voice = voice;
    u.rate = rate;
    if (k === parts.length - 1) u.onend = () => finish(item);
    u.onerror = (e) => { if (e.error !== "interrupted" && e.error !== "canceled") finish(item); };
    return u;
  });
  synth.resume?.(); // Safari and Chrome can be left paused
  for (const u of item.utts) synth.speak(u);
}

function finish(item) {
  if (current !== item) return;
  current = null;
  press(item.btn, false);
  next();
}

// stopSpeaking stops what is read and drops what waits.
export function stopSpeaking() {
  queue = [];
  const item = current;
  current = null;
  if (item) press(item.btn, false);
  if (speechSupported && (item || synth.speaking || synth.pending)) synth.cancel();
}

// Esc stops speaking (and does nothing else, so a sheet stays open).
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !current) return;
  e.preventDefault();
  e.stopPropagation();
  stopSpeaking();
}, true);
// Dictation (or a voice command) starting to listen stops it, so Dash doesn't hear itself.
document.addEventListener("dash:listening", stopSpeaking);
addEventListener("pagehide", stopSpeaking);

// speechSample reads a line in the chosen voice and speed. Called from the
// settings' change events, it is also the tap Safari on iOS (and Chrome after
// a reload) wants before a page may speak on its own.
export const speechSample = () => say("This is how Dash reads Claude's replies.", { now: true });

// ---- what is read
// speakButton is the speaker button under a chat reply (content: its
// Markdown): it reads all of it, or stops.
export function speakButton(content) {
  const btn = ib({ type: "button", class: "link speak-btn", "aria-pressed": "false", title: "Read this reply aloud" }, "speaker", "Read aloud");
  btn.addEventListener("click", () => {
    if (current?.btn === btn) return stopSpeaking();
    say(speakable(content), { btn, now: true });
  });
  return btn;
}

// speakReply reads a reply that has just finished (btn: its speaker button).
export function speakReply(content, btn = null) {
  if (!speechOn() || !speechKind("replies")) return;
  const text = speakable(content);
  say(speechLong() === "all" ? text : firstPart(text), { btn });
}

// speakApproval reads approval request p in short: what Claude wants to do
// (where: the project, when it isn't the one shown).
export function speakApproval(p, where = "") {
  if (!speechOn() || !speechKind("approvals")) return;
  const d = describePerm(p);
  let text;
  if (isQuestion(p)) {
    text = `Claude has a question: ${stop(inline(d.detail.split("\n")[0] || ""))}`;
  } else {
    text = `Claude wants to ${inline(d.title)}`;
    if (p.tool === "Bash" && d.detail) {
      const cmd = shortPaths(String(d.detail).split("\n")[0].trim());
      text += `: ${cmd.length > 80 ? `${cmd.slice(0, cmd.lastIndexOf(" ", 80) > 20 ? cmd.lastIndexOf(" ", 80) : 80)} and more` : cmd}`;
    }
    text = stop(text);
  }
  say(where ? `${text} In ${where}.` : text);
}

// speakStatus reads a task's news (head: "Ready for review", "Failed", …),
// for the tasks the user hears about (Account → Notifications → Tasks).
export function speakStatus(head, t, where = "") {
  if (!speechOn() || !speechKind("status")) return;
  const by = t?.created_by || 0;
  if (state.pushTasks === "mine" && by > 0 && by !== state.me?.user?.id) return;
  const name = inline(t?.title || String(t?.prompt || "").split("\n")[0].slice(0, 120) || "a task");
  say(`${head}: ${stop(name)}${where ? ` In ${where}.` : ""}`);
}
