// Tests for voice mode's state machine and listening again (web/static/voiceloop.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { CONFIRMED, afterEnd, backoff, classify, hints, initial, matchAlternatives, modeWord, normalize, phrases, readBack, shorten, step } from "./static/voiceloop.js";

// A context with a project open and an approval waiting.
const ctx = {
  project: () => "dash",
  to: () => "dash's chat",
  describe: (id) => id === "approve" ? { text: "Approve: Claude wants to run go test" } : { error: "Open a draft first." },
};
const noProject = { project: () => "", to: () => "", describe: () => ({ error: "x" }) };

// run steps through events from m (each a string heard, or an event) and
// returns the last machine and all effects.
function run(events, m = initial(), c = ctx) {
  const out = [];
  for (const ev of events) {
    const r = step(m, typeof ev === "string" ? { type: "heard", text: ev, cmd: null } : ev, c);
    m = r.m;
    out.push(...r.out);
  }
  return { m, out };
}
const said = (out) => out.filter((e) => e.do === "say").map((e) => e.text);

test("normalize drops case, punctuation and filler, and fixes misheard words", () => {
  assert.equal(normalize("Queue the task, please!"), "queue task");
  assert.equal(normalize("Cue it"), "queue it");
  assert.equal(normalize("That's it."), "thats it");
  assert.equal(normalize("OK"), "");
});

test("the app's commands match any alternative, as voice.js uses them", () => {
  const table = phrases([{ id: "next", say: ["next card"] }, { id: "approve", say: ["approve"] }]);
  assert.equal(matchAlternatives(table, [{ transcript: "next cart" }, { transcript: "Next card." }]).cmd.id, "next");
  assert.equal(matchAlternatives(table, [{ transcript: "Approved" }]).cmd.id, "approve");
  assert.equal(matchAlternatives(table, [{ transcript: "hello" }]), null);
});

test("mode words", () => {
  assert.equal(modeWord("New task."), "newtask");
  assert.equal(modeWord("Exit voice mode"), "exit");
  assert.equal(modeWord("stop listening"), "pause");
  assert.equal(modeWord("fix the login redirect"), null);
});

test("new task: dictate, done, read back, yes queues", () => {
  const { m, out } = run(["new task", "fix the login redirect", "on the settings page", "done"]);
  assert.equal(m.state, "confirming");
  assert.deepEqual(m.pending, { kind: "task", text: "fix the login redirect on the settings page" });
  assert.equal(said(out).at(-1), "Queue a new task: fix the login redirect on the settings page. Say yes to confirm.");
  const yes = run(["yes"], m);
  assert.equal(yes.m.state, "listening");
  assert.deepEqual(yes.out, [{ do: "act", action: { kind: "task", text: "fix the login redirect on the settings page" } }]);
  assert.deepEqual(yes.m.draft, []);
});

test("nothing is acted on without yes", () => {
  const { m } = run(["new task", "fix it", "done"]);
  for (const word of ["maybe", "say yes to confirm", "queue a new task fix it"]) {
    const r = run([word], m);
    assert.equal(r.m.state, "confirming");
    assert.ok(!r.out.some((e) => e.do === "act"), word);
    assert.ok(!r.out.some((e) => e.do === "say"), "an echo isn't answered aloud");
  }
  const no = run(["no"], m);
  assert.equal(no.m.state, "dictating");
  assert.deepEqual(no.m.draft, ["fix it"]);
  const cancel = run(["cancel"], m);
  assert.equal(cancel.m.state, "listening");
  assert.deepEqual(cancel.m.draft, []);
});

test("dictate a message, send it", () => {
  const { m, out } = run(["dictate", "looks good, ship it", "send"]);
  assert.equal(said(out)[0], "Message to dash's chat. Speak, then say send.");
  assert.deepEqual(m.pending, { kind: "message", text: "looks good, ship it", to: "dash's chat" });
  assert.equal(said(out).at(-1), "Send to dash's chat: looks good, ship it. Say yes to confirm.");
  assert.equal(run(["yeah"], m).out[0].action.kind, "message");
});

test("dictating: scratch that, cancel, done with nothing", () => {
  let r = run(["new task", "one", "two", "scratch that"]);
  assert.deepEqual(r.m.draft, ["one"]);
  r = run(["new task", "done"]);
  assert.equal(r.m.state, "dictating");
  assert.match(said(r.out).at(-1), /Nothing dictated/);
  r = run(["new task", "one", "cancel"]);
  assert.equal(r.m.state, "listening");
  assert.equal(r.m.target, null);
});

test("app commands run while listening, but are words while dictating", () => {
  let r = run([{ type: "heard", text: "next card", cmd: "next" }]);
  assert.deepEqual(r.out, [{ do: "run", id: "next" }]);
  r = run(["new task", { type: "heard", text: "next card", cmd: "next" }]);
  assert.deepEqual(r.out.filter((e) => e.do === "run"), []);
  assert.deepEqual(r.m.draft, ["next card"]);
});

test("approve and queue are read back first; deny is not", () => {
  assert.ok(CONFIRMED.has("approve") && CONFIRMED.has("queue") && !CONFIRMED.has("deny"));
  let r = run([{ type: "heard", text: "approve", cmd: "approve" }]);
  assert.equal(r.m.state, "confirming");
  assert.equal(said(r.out)[0], "Approve: Claude wants to run go test. Say yes to confirm.");
  assert.deepEqual(run(["yes"], r.m).out, [{ do: "act", action: { kind: "command", id: "approve", text: "Approve: Claude wants to run go test" } }]);
  assert.equal(run(["no"], r.m).m.state, "listening");
  r = run([{ type: "heard", text: "queue it", cmd: "queue" }]);
  assert.equal(r.m.state, "listening");
  assert.deepEqual(said(r.out), ["Open a draft first."]);
  r = run([{ type: "heard", text: "deny", cmd: "deny" }]);
  assert.deepEqual(r.out, [{ do: "run", id: "deny" }]);
});

test("yes acts on the permission request that was read back (its ref)", () => {
  const c = { ...ctx, describe: () => ({ text: "Approve: Claude wants to run go test", ref: "perm-1" }) };
  const r = run([{ type: "heard", text: "approve", cmd: "approve" }], initial(), c);
  assert.deepEqual(run(["yes"], r.m, c).out, [{ do: "act", action: { kind: "command", id: "approve", text: "Approve: Claude wants to run go test", ref: "perm-1" } }]);
});

test("without a project, new task and dictate say so", () => {
  const r = run(["new task", "dictate"], initial(), noProject);
  assert.equal(r.m.state, "listening");
  assert.deepEqual(said(r.out), ["Open a project first.", "Open a project first."]);
});

test("speaking: only stop, pause and exit are heard, then back to where it was", () => {
  let r = run(["new task", "fix it", "done", { type: "speaking" }]);
  assert.equal(r.m.state, "speaking");
  assert.equal(r.m.after, "confirming");
  const echo = run(["yes", "say yes to confirm"], r.m);
  assert.equal(echo.m.state, "speaking");
  assert.deepEqual(echo.out, []);
  assert.equal(run([{ type: "quiet" }], r.m).m.state, "confirming");
  const stop = run(["stop"], r.m);
  assert.equal(stop.m.state, "confirming");
  assert.deepEqual(stop.out, [{ do: "stopSpeaking" }]);
  // A quiet after stop changes nothing more.
  assert.equal(run([{ type: "quiet" }], stop.m).m.state, "confirming");
  assert.deepEqual(run([{ type: "key" }], r.m).out, [{ do: "stopSpeaking" }]);
  assert.deepEqual(run([{ type: "key" }]).out, [], "a key while not speaking does nothing");
  const pause = run(["pause", "hello", "resume"], r.m);
  assert.equal(pause.m.state, "confirming");
  assert.deepEqual(pause.m.pending, { kind: "task", text: "fix it" });
});

test("pause and resume", () => {
  let r = run(["pause", "new task", { type: "heard", text: "approve", cmd: "approve" }]);
  assert.equal(r.m.state, "paused");
  assert.ok(!r.out.some((e) => e.do === "run" || e.do === "act"));
  r = run(["resume"], r.m);
  assert.equal(r.m.state, "listening");
  r = run(["new task", "fix", "pause", "resume", "it", "done"]);
  assert.deepEqual(r.m.pending, { kind: "task", text: "fix it" });
  // Dash speaks news while paused: it stays paused afterwards.
  r = run(["pause", { type: "speaking" }, { type: "quiet" }]);
  assert.equal(r.m.state, "paused");
});

test("exit works in every state", () => {
  for (const pre of [[], ["pause"], ["new task"], ["new task", "x", "done"], [{ type: "speaking" }]]) {
    const r = run([...pre, "exit voice mode"]);
    assert.deepEqual(r.out.slice(-2), [{ do: "stopSpeaking" }, { do: "exit" }], JSON.stringify(pre));
  }
});

test("read it all, repeat and help", () => {
  assert.deepEqual(run(["read it all"]).out, [{ do: "readAll" }]);
  const r = run(["new task", "repeat"]);
  assert.equal(r.m.state, "dictating");
  assert.equal(r.m.draft.at(-1), "repeat", "while dictating, repeat is a word");
  const l = run(["send", "repeat"]);
  assert.equal(said(l.out)[1], said(l.out)[0]);
  assert.match(said(run(["help"]).out)[0], /new task/);
});

test("read-backs are short", () => {
  const long = Array.from({ length: 50 }, (_, i) => `w${i}`).join(" ");
  assert.match(shorten(long), /w29, and more$/);
  assert.equal(readBack({ kind: "task", text: "fix it." }), "Queue a new task: fix it. Say yes to confirm.");
});

test("hints follow the state", () => {
  assert.deepEqual(hints({ state: "paused" }), ["resume", "exit voice mode"]);
  assert.deepEqual(hints({ state: "confirming" }), ["yes", "no", "cancel", "repeat"]);
  assert.equal(hints({ state: "dictating", target: "task" })[0], "done");
  assert.equal(hints({ state: "dictating", target: "message" })[0], "send");
});

test("errors: fatal ones stop, the rest listen again", () => {
  for (const e of ["not-allowed", "service-not-allowed", "audio-capture", "language-not-supported"]) assert.equal(classify(e), "fatal");
  for (const e of ["no-speech", "aborted"]) assert.equal(classify(e), "quiet");
  for (const e of ["network", "bad-grammar", "something-new"]) assert.equal(classify(e), "retry");
});

test("a listen's end: failed, fine or neither", () => {
  const t = 1_000_000;
  assert.equal(afterEnd({ startedAt: t, endedAt: t + 60000, heard: true }), "ok");
  assert.equal(afterEnd({ startedAt: t, endedAt: t + 8000, heard: false, error: "no-speech" }), "same");
  assert.equal(afterEnd({ startedAt: t, endedAt: t + 60000, heard: false }), "same");
  assert.equal(afterEnd({ startedAt: t, endedAt: t + 200, heard: false }), "fail");
  assert.equal(afterEnd({ startedAt: t, endedAt: t + 200, heard: false, error: "no-speech" }), "fail");
  assert.equal(afterEnd({ startedAt: t, endedAt: t + 200, heard: false, error: "aborted" }), "same");
  assert.equal(afterEnd({ startedAt: t, endedAt: t + 9000, heard: true, error: "network" }), "fail");
});

test("backoff doubles up to its cap and starts over on ok", () => {
  const b = backoff({ base: 500, max: 4000 });
  assert.equal(b.delay(), 0);
  const delays = [];
  for (let i = 0; i < 6; i++) { b.fail(); delays.push(b.delay()); }
  assert.deepEqual(delays, [500, 1000, 2000, 4000, 4000, 4000]);
  assert.equal(b.failures, 6);
  b.ok();
  assert.equal(b.delay(), 0);
  assert.equal(b.failures, 0);
});
