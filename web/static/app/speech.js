import { ib } from "./dom.js";
import * as store from "./store.js";
import { state } from "./state.js";
import { describePerm, isQuestion } from "./chatparts.js";
import { listening } from "./dictation.js";
import { api } from "./api.js";

// ------------------------------------------------------------------ spoken replies (docs/dictation.md)
// Reads Claude's chat replies, approval requests and task status changes
// aloud with the browser's speechSynthesis, so the chats and tasks can be
// followed without looking. Everything happens in the browser; some voices
// (Chrome's Google voices, Edge's online ones) are an online service the
// browser sends the text to. It is a preference of this browser
// (Account → Voice), off by default, with a switch for each kind of
// news; where the API is missing the setting is greyed out and nothing shows.
//
// What is read is plain prose: Markdown is stripped, a code block, diff or
// table is named and counted ("A code block, 12 lines."), a long path is its
// file name, a link its text and a bare URL its host. A long reply is read up to its first part,
// then "There's more" (or in full: Account → Voice → Long replies).
// The speaker button on a reply reads all of it, or stops.
//
// With Summarise spoken replies on (Account → Voice, on by default), a long
// reply, approval request or task's news is said as a sentence or two the
// title model writes (POST /api/speech/summary). Short text, a summary that
// fails or takes longer than SUMMARY_WAIT, is read as above. The voice
// command "read it all" reads the last item said in short in full (readAll).
//
// Esc, sending a message or pressing a speaker button again stops it. Dash
// doesn't speak while dictation or voice commands listen (they send
// "dash:listening" as they start), so it doesn't hear itself.
//
// Voice mode (voicemode.js) sets hands.on: then everything is read, in short,
// whatever the settings, while it listens; it takes turns itself, through
// hands.onSpeak (Dash starts to say text) and hands.onQuiet (it stopped).

const synth = window.speechSynthesis;
export const speechSupported = !!synth && typeof window.SpeechSynthesisUtterance === "function";

const KEY = "dash.speech";
export const speechOn = () => speechSupported && store.flag(KEY);
export const hands = { on: false, onSpeak: null, onQuiet: null };
const on = () => speechOn() || (speechSupported && hands.on);
// The kinds of news to read: on unless switched off.
const KINDS = { replies: "dash.speech.replies", approvals: "dash.speech.approvals", status: "dash.speech.status" };
export const speechKind = (kind) => store.get(KINDS[kind]) !== "0";
const wants = (kind) => hands.on || speechKind(kind);
export const setSpeechKind = (kind, on) => store.setFlag(KINDS[kind], on);
export const speechVoice = () => store.get("dash.speech.voice", "");
export const setSpeechVoice = (uri) => store.set("dash.speech.voice", uri || null);
export const speechRate = () => Number(store.get("dash.speech.rate")) || 1;
export const setSpeechRate = (r) => store.set("dash.speech.rate", r && Number(r) !== 1 ? r : null);
export const speechLong = () => store.get("dash.speech.long", "");
export const setSpeechLong = (v) => store.set("dash.speech.long", v || null);
export const speechSummaries = () => store.get("dash.speech.summary") !== "0";
export const setSpeechSummaries = (on) => store.setFlag("dash.speech.summary", on);

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

// ---- summaries
// summary asks for a spoken summary of text (kind: reply, approval or
// status; agent.SpeakKinds), or resolves to "" when there's none: the
// setting is off, the text is short (the server's minSpokenWords too, so
// short text isn't sent at all), the call fails or takes too long. key names
// the message: each is summarised once.
const SUMMARY_WAIT = 10000; // ms
const MIN_WORDS = 40;
const summaries = new Map(); // key → Promise of the summary
export function summary(kind, text, key = `${kind}:${text}`) {
  if (!(speechSummaries() || hands.on) || String(text).trim().split(/\s+/).length < MIN_WORDS) return Promise.resolve("");
  if (summaries.has(key)) return summaries.get(key);
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), SUMMARY_WAIT);
  const p = api("POST", "/api/speech/summary", { kind, text }, "", { signal: ctl.signal })
    .then((r) => r?.summary || "")
    .catch(() => "")
    .finally(() => clearTimeout(timer));
  // A failure isn't kept: the next message asks again.
  p.then((s) => { if (!s) summaries.delete(key); });
  summaries.set(key, p);
  if (summaries.size > 200) summaries.delete(summaries.keys().next().value);
  return p;
}

// last is the full text of the latest item said in short (a summary, or
// the first part of a long reply), for readAll.
let last = "";
export const hasReadAll = () => on() && !!last;

// readAll reads the latest item said in short in full ("read it all").
// false when there's none, or Spoken replies are off.
export function readAll() {
  if (!on() || !last) return false;
  say(last, { now: true });
  return true;
}

// ---- speaking
// Items wait in a queue and are read one after another; a speaker button
// reads its reply now instead. An item: {text, btn, short?, full?}, btn the
// speaker button pressed while it is read; short, a promise of a summary
// said instead of text when it comes, full the whole text ("read it all").
let queue = [];
let current = null;

function press(btn, on) {
  if (!btn) return;
  btn.setAttribute("aria-pressed", String(on));
  btn.classList.toggle("speaking", on);
  btn.title = on ? "Stop reading (Esc)" : "Read this reply aloud";
}

// say queues text (now: stops what is read and reads it at once). It reads
// nothing while dictation listens, or with the preference off.
function say(text, { btn = null, now = false, short = null, full = "" } = {}) {
  if (!canSay() || !text?.trim()) return;
  if (now) stopSpeaking();
  queue.push({ text, btn, short, full });
  if (!current) next();
}
const canSay = () => on() && (hands.on || !listening());

// sayLine says text for voice mode (now: at once, cutting off what is said).
export function sayLine(text, now = false) { say(text, { now }); }

// next reads the next item: once its summary is in, when it waits for one
// (the items after it wait too, so they stay in order).
function next() {
  const item = current = queue.shift() || null;
  if (!item) { hands.onQuiet?.(); return; }
  if (item.short) {
    const short = item.short;
    item.short = null;
    short.then((s) => {
      if (current !== item) return; // stopped meanwhile
      if (s) item.text = s;
      if (s || item.text !== item.full) last = item.full;
      next1(item);
    });
    return;
  }
  next1(item);
}

function next1(item) {
  press(item.btn, true);
  hands.onSpeak?.(item.text);
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
  if (item) hands.onQuiet?.();
}

// Esc stops speaking (and does nothing else, so a sheet stays open). Only
// while something is read: not while a summary is still awaited.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !current?.utts || hands.on) return; // voice mode has its own keys
  e.preventDefault();
  e.stopPropagation();
  stopSpeaking();
}, true);
// Dictation (or a voice command) starting to listen stops it, so Dash doesn't hear itself.
document.addEventListener("dash:listening", () => { if (!hands.on) stopSpeaking(); });
addEventListener("pagehide", stopSpeaking);

// speechSample reads a line in the chosen voice and speed. Called from the
// settings' change events, it is also the tap Safari on iOS (and Chrome after
// a reload) wants before a page may speak on its own.
export const speechSample = () => say("This is how Dash reads Claude's replies.", { now: true });

// ---- what is read
// speakButton is the speaker button under a chat reply (content: its
// Markdown): it reads all of it, or stops.
export function speakButton(content) {
  const btn = ib({ type: "button", class: "msg-icon speak-btn", "aria-pressed": "false", "aria-label": "Read aloud", title: "Read aloud" }, "speaker");
  btn.addEventListener("click", () => {
    if (current?.btn === btn) return stopSpeaking();
    say(speakable(content), { btn, now: true });
  });
  return btn;
}

// speakReply reads a reply that has just finished (btn: its speaker
// button): in short, or as it is (up to its first part, or all of it).
export function speakReply(content, btn = null) {
  if (!canSay() || !wants("replies")) return;
  const text = speakable(content);
  say(speechLong() === "all" ? text : firstPart(text), { btn, short: summary("reply", content), full: text });
}

// speakApproval reads approval request p in short: what Claude wants to do
// (where: the project, when it isn't the one shown).
export function speakApproval(p, where = "") {
  if (!canSay() || !wants("approvals")) return;
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
  const full = where ? `${text} In ${where}.` : text;
  // In short: what and why, as a question, from all of the request.
  const all = [`Claude wants to ${d.title}.`, d.note ? `Why: ${d.note}` : "", d.detail || "", where ? `In ${where}.` : ""].filter(Boolean).join("\n");
  say(full, { short: summary("approval", all, p.id ? `approval:${p.id}` : undefined), full: [full, d.detail ? speakable(String(d.detail)) : ""].filter(Boolean).join("\n\n") });
}

// speakStatus reads a task's news (head: "Ready for review", "Failed", …),
// for the tasks the user hears about (Account → Notifications → Tasks).
export function speakStatus(head, t, where = "") {
  if (!canSay() || !wants("status")) return;
  const by = t?.created_by || 0;
  if (state.pushTasks === "mine" && by > 0 && by !== state.me?.user?.id) return;
  const name = inline(t?.title || String(t?.prompt || "").split("\n")[0].slice(0, 120) || "a task");
  const text = `${head}: ${stop(name)}${where ? ` In ${where}.` : ""}`;
  // In short: the result and the tests, from what the task says of them.
  const c = t?.checks;
  const all = [text, t?.summary ? `What Claude did: ${t.summary}` : "", t?.error ? `Error: ${t.error}` : "",
    c ? `Tests (${c.command}): ${c.passed ? "passed" : `failed, exit code ${c.exit_code}`}.` : ""].filter(Boolean).join("\n");
  say(text, { short: summary("status", all, t?.id ? `status:${t.id}:${head}` : undefined), full: speakable(all) });
}
