// The logic of voice mode (app/voicemode.js, docs/dictation.md#voice-mode)
// and the phrase matching voice commands share (app/voice.js): what is said
// moves one state machine, and recognition is restarted with backoff. It
// touches no DOM and no browser API, so Node tests can load it.

// ---- matching what is said
// Words recognizers hear for the command words, and words around a command.
const SOUNDS_LIKE = { cue: "queue", q: "queue", kew: "queue", approved: "approve", denied: "deny", sent: "send" };
const FILLER = new Set(["the", "this", "that", "a", "an", "please", "now", "ok", "okay", "hey", "dash"]);

// normalize makes a phrase comparable: lower case, no punctuation, no filler
// words, and the words recognizers mishear for command words fixed.
export function normalize(text) {
  return String(text).toLowerCase().replace(/['’]/g, "").replace(/[^\p{L}\p{N}]+/gu, " ").trim().split(" ")
    .map((w) => SOUNDS_LIKE[w] || w).filter((w) => w && !FILLER.has(w)).join(" ");
}

// phrases maps each normalized phrase of the commands ({ say: [...] }) to its command.
export const phrases = (commands) => new Map(commands.flatMap((c) => c.say.map((p) => [normalize(p), c])));

// matchAlternatives returns the command one of a recognition result's
// alternatives says, and the words heard: { cmd, said } or null. alts is a
// list of { transcript }, as SpeechRecognitionResult is.
export function matchAlternatives(table, alts) {
  for (let i = 0; i < alts.length; i++) {
    const said = String(alts[i].transcript).trim();
    const cmd = table.get(normalize(said));
    if (cmd) return { cmd, said };
  }
  return null;
}

// ---- voice mode's own words
// They win over the app's commands (voice.js), which voice mode runs too.
const MODE = [
  { id: "newtask", say: ["new task", "add task", "create task", "new todo"] },
  { id: "dictate", say: ["dictate", "new message", "message", "dictate message", "write message"] },
  { id: "done", say: ["done", "im done", "finished", "thats it", "end message", "stop dictating"] },
  { id: "send", say: ["send", "send it", "send message", "submit"] },
  { id: "cancel", say: ["cancel", "cancel that", "never mind", "discard", "forget it"] },
  { id: "scratch", say: ["scratch that", "delete that", "undo", "undo that"] },
  { id: "pause", say: ["pause", "pause listening", "stop listening", "stop voice commands", "go to sleep"] },
  { id: "resume", say: ["resume", "continue", "start listening", "wake up", "resume listening"] },
  { id: "yes", say: ["yes", "yeah", "yep", "yes do it", "confirm", "do it", "go ahead", "yes confirm"] },
  { id: "no", say: ["no", "nope", "not yet", "wait", "no wait"] },
  { id: "stop", say: ["stop", "stop talking", "quiet", "be quiet", "shut up", "enough", "skip", "silence"] },
  { id: "readall", say: ["read it all", "read all", "read all of it", "read everything", "read whole thing", "read it in full"] },
  { id: "repeat", say: ["repeat", "say again", "say it again", "read it back", "repeat that"] },
  { id: "help", say: ["help", "what can i say", "commands", "list commands"] },
  { id: "exit", say: ["exit voice mode", "leave voice mode", "stop voice mode", "end voice mode", "exit", "goodbye"] },
];
const MODE_PHRASES = phrases(MODE);
export const modeWord = (text) => MODE_PHRASES.get(normalize(text))?.id || null;

// The app's commands (voice.js ids) that change something: voice mode reads
// them back and runs them only on "yes".
export const CONFIRMED = new Set(["queue", "approve"]);

// The states: paused (only "resume" and "exit voice mode" work), listening
// for commands, dictating (a task or a message), confirming (a read-back
// waits for "yes") and speaking (Dash talks: only "stop", "pause" and
// "exit voice mode" are heard, so Dash doesn't obey its own voice).

// initial is the machine as voice mode starts.
export const initial = () => ({ state: "listening", target: null, draft: [], pending: null, after: null, last: "" });

// The longest read-back, in words: the screen shows all of it.
const READ_BACK_WORDS = 30;
export function shorten(text, max = READ_BACK_WORDS) {
  const words = String(text).trim().split(/\s+/);
  return words.length <= max ? words.join(" ") : `${words.slice(0, max).join(" ")}, and more`;
}
const sentence = (s) => /[.!?]$/.test(s) ? s : `${s}.`;

// readBack is what Dash says before it acts on pending (see step).
export function readBack(p) {
  switch (p.kind) {
    case "task": return `Queue a new task: ${sentence(shorten(p.text))} Say yes to confirm.`;
    case "message": return `Send${p.to ? ` to ${p.to}` : ""}: ${sentence(shorten(p.text))} Say yes to confirm.`;
    default: return `${sentence(p.text)} Say yes to confirm.`;
  }
}

// step moves machine m on event ev, and returns the new machine (m is left
// as it is) and the effects for voice mode to carry out, in order:
//   { do: "say", text }          speak text
//   { do: "stopSpeaking" }       cut speech off
//   { do: "run", id }            run the app's command id (voice.js)
//   { do: "act", action }        carry out a confirmed action: { kind: "task" | "message", text, to? } or { kind: "command", id, text, ref? }
//                                or { kind: "command", id }
//   { do: "readAll" }            read the last thing said in short in full
//   { do: "note", text }         show text (not said): what was heard and ignored
//   { do: "exit" }               leave voice mode
// Events:
//   { type: "heard", text, cmd } a final utterance; cmd the app command it matches (voice.js id) or null
//   { type: "speaking" }         Dash started to speak
//   { type: "quiet" }            Dash stopped speaking
//   { type: "key" }              a key was pressed (it cuts speech off)
// ctx gives what step can't know: project() is the project's name or "";
// to() names where a message goes ("the project chat"); describe(id) is the
// read-back of app command id ({ text }) or why it can't run ({ error }).
export function step(m, ev, ctx = {}) {
  const n = { ...m, draft: [...m.draft] };
  const out = [];
  const say = (text) => { out.push({ do: "say", text }); n.last = text; };
  const to = (state) => { n.state = state; };

  if (ev.type === "speaking") {
    if (n.state !== "speaking") { n.after = n.state; to("speaking"); }
    return { m: n, out };
  }
  if (ev.type === "quiet") {
    if (n.state === "speaking") { to(n.after || "listening"); n.after = null; }
    return { m: n, out };
  }
  if (ev.type === "key") {
    if (n.state === "speaking") out.push({ do: "stopSpeaking" });
    return { m: n, out };
  }
  if (ev.type !== "heard") return { m: n, out };

  const text = String(ev.text || "").trim();
  if (!text) return { m: n, out };
  const word = modeWord(text);
  if (word === "exit") { out.push({ do: "stopSpeaking" }, { do: "exit" }); return { m: n, out }; }

  switch (n.state) {
    case "speaking":
      // Only a few words cut in: the rest may be Dash's own voice.
      if (word === "stop") { out.push({ do: "stopSpeaking" }); to(n.after || "listening"); n.after = null; }
      else if (word === "pause") { out.push({ do: "stopSpeaking" }); n.resume = n.after === "paused" ? n.resume : n.after; to("paused"); n.after = null; }
      return { m: n, out };

    case "paused":
      if (word === "resume") { to(n.resume || "listening"); n.resume = null; say(n.state === "dictating" ? "Dictating again." : "Listening."); }
      return { m: n, out };

    case "dictating":
      switch (word) {
        case "done": case "send": {
          const body = n.draft.join(" ").trim();
          if (!body) { say("Nothing dictated yet. Speak, or say cancel."); return { m: n, out }; }
          n.pending = n.target === "task" ? { kind: "task", text: body } : { kind: "message", text: body, to: ctx.to?.() || "" };
          to("confirming");
          say(readBack(n.pending));
          return { m: n, out };
        }
        case "cancel":
          n.draft = []; n.target = null; to("listening"); say("Cancelled.");
          return { m: n, out };
        case "scratch":
          n.draft.pop();
          out.push({ do: "note", text: n.draft.length ? "Took the last part out." : "Nothing left." });
          return { m: n, out };
        case "pause":
          n.resume = "dictating"; to("paused"); say("Paused. Say resume to go on dictating.");
          return { m: n, out };
        case "stop":
          out.push({ do: "stopSpeaking" });
          return { m: n, out };
        case "help":
          say(spokenHints(n));
          return { m: n, out };
        default:
          n.draft.push(text);
          return { m: n, out };
      }

    case "confirming":
      switch (word) {
        case "yes": {
          const p = n.pending;
          n.pending = null; n.draft = []; n.target = null; to("listening");
          out.push({ do: "act", action: p });
          return { m: n, out };
        }
        case "no":
          // A dictated text goes back to dictating, to say more; a command is dropped.
          if (n.pending?.kind === "task" || n.pending?.kind === "message") { n.pending = null; to("dictating"); say("Not yet. Go on dictating, or say cancel."); }
          else { n.pending = null; to("listening"); say("Not done."); }
          return { m: n, out };
        case "cancel":
          n.pending = null; n.draft = []; n.target = null; to("listening"); say("Cancelled.");
          return { m: n, out };
        case "repeat":
          say(readBack(n.pending));
          return { m: n, out };
        case "pause":
          n.resume = "confirming"; to("paused"); say("Paused.");
          return { m: n, out };
        case "stop":
          out.push({ do: "stopSpeaking" });
          return { m: n, out };
        default:
          // Not said aloud: an echo of the read-back mustn't start a loop.
          out.push({ do: "note", text: "Say yes or no." });
          return { m: n, out };
      }

    default: // listening
      switch (word) {
        case "newtask":
          if (!ctx.project?.()) { say("Open a project first."); return { m: n, out }; }
          n.target = "task"; n.draft = []; to("dictating"); say("New task. Say what Claude should do, then say done.");
          return { m: n, out };
        case "dictate":
          if (!ctx.to?.()) { say("Open a project first."); return { m: n, out }; }
          n.target = "message"; n.draft = []; to("dictating"); say(`Message to ${ctx.to()}. Speak, then say send.`);
          return { m: n, out };
        case "pause":
          n.resume = "listening"; to("paused"); say("Paused. Say resume to go on.");
          return { m: n, out };
        case "resume":
          say("Already listening.");
          return { m: n, out };
        case "stop":
          out.push({ do: "stopSpeaking" });
          return { m: n, out };
        case "readall":
          out.push({ do: "readAll" });
          return { m: n, out };
        case "repeat":
          if (n.last) say(n.last); else out.push({ do: "note", text: "Nothing said yet." });
          return { m: n, out };
        case "help":
          say(spokenHints(n));
          return { m: n, out };
        case "send": case "done":
          say("Nothing to send. Say dictate, or new task, first.");
          return { m: n, out };
        case "yes": case "no": case "cancel": case "scratch":
          out.push({ do: "note", text: "Nothing waits for that." });
          return { m: n, out };
      }
      if (ev.cmd && CONFIRMED.has(ev.cmd)) {
        const d = ctx.describe?.(ev.cmd) || { error: "That can't run here." };
        if (d.error) { say(d.error); return { m: n, out }; }
        // ref names what was read back (the permission request): "yes" acts on it, not on whatever is first by then.
        n.pending = { kind: "command", id: ev.cmd, text: d.text, ...(d.ref != null ? { ref: d.ref } : {}) };
        to("confirming");
        say(readBack(n.pending));
        return { m: n, out };
      }
      if (ev.cmd) { out.push({ do: "run", id: ev.cmd }); return { m: n, out }; }
      out.push({ do: "note", text: `Heard “${text}”: not a command.` });
      return { m: n, out };
  }
}

// hints lists the commands that work in machine m's state, for the screen.
export function hints(m) {
  switch (m.state) {
    case "paused": return ["resume", "exit voice mode"];
    case "speaking": return ["stop", "pause", "exit voice mode"];
    case "dictating": return [m.target === "task" ? "done" : "send", "scratch that", "cancel", "pause"];
    case "confirming": return ["yes", "no", "cancel", "repeat"];
    default: return ["new task", "dictate", "approve", "deny", "next card", "previous card", "open board", "read it all", "pause", "exit voice mode"];
  }
}
const spokenHints = (m) => `You can say: ${hints(m).join(", ")}.`;

// ---- listening again
// classify sorts a SpeechRecognition error: "fatal" can't be recovered by
// listening again (voice mode shows and says why, and stops listening);
// "quiet" is normal (no speech heard, or our own abort): listen again at
// once; "retry" is transient (the network): listen again after a backoff.
const FATAL = new Set(["not-allowed", "service-not-allowed", "audio-capture", "language-not-supported"]);
export function classify(error) {
  if (FATAL.has(error)) return "fatal";
  if (error === "no-speech" || error === "aborted") return "quiet";
  return "retry";
}

// A listen that ends this soon after it started, without hearing anything,
// failed: the browser refused it, or it is ending in a loop.
export const QUICK_END = 1000; // ms

// backoff counts failures in a row and gives the wait before listening
// again: none after a normal end, then base, doubling up to max. ok() (speech
// heard, or a long listen) starts over.
export function backoff({ base = 500, max = 30000 } = {}) {
  let failures = 0;
  return {
    fail() { failures++; },
    ok() { failures = 0; },
    get failures() { return failures; },
    delay() { return failures ? Math.min(max, base * 2 ** (failures - 1)) : 0; },
  };
}

// afterEnd is what a listen's end means for the backoff: "fail" when it
// failed (a retry error, or a quick end with nothing heard, unless we ended
// it: "aborted"), else "ok" when it heard something, else "same" (a long
// listen that heard nothing, as the browser ends one after a while).
export function afterEnd({ startedAt, endedAt, heard, error = "" }) {
  if (error && classify(error) === "retry") return "fail";
  if (heard) return "ok";
  if (endedAt - startedAt < QUICK_END && error !== "aborted") return "fail";
  return "same";
}
