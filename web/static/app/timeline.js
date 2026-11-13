import { renderMarkdown } from "../editor.js";
import { $, clockTime, entering, h, ib, plural, timeLocale } from "./dom.js";
import { attempt, copy } from "./dialogs.js";
import { state } from "./state.js";
import { duration } from "./activity.js";
import { toolCall, toolResult, toolSettled } from "./chat.js";

// ------------------------------------------------------------------ task timeline
// The task chat's log is the task's timeline: the worker's runs (labelled
// Agent) among the side chat's turns (labelled Chat), in time order. Only the
// worker's events are kept here; the chat keeps its own history.
export const worker = { id: null, events: [], live: [], cur: null, tools: new Map(), phase: null };

// A step Dash runs itself (setup, tests): a spinner and a timer while it runs,
// its output underneath, and how it went once it ends.
function phaseBlock(label, cmd, since) {
  const spin = h("span", { class: "act-spin", "aria-hidden": "true" });
  const name = h("b", {}, label);
  const time = h("span", { class: "act-time", "data-since": String(since) }, duration(Date.now() - since));
  const log = h("pre", { class: "log tl-log", hidden: true });
  const el = h("details", { class: "tl-phase running", open: true }, h("summary", {}, spin, name, h("code", {}, cmd), time), log);
  return { el, spin, name, time, log, since };
}

// endPhase closes the open phase at event e. A test result line becomes its
// header (and true is returned, as it says nothing more); any other event
// means the phase is over too.
function endPhase(e) {
  const p = worker.phase;
  if (!p) return false;
  worker.phase = null;
  const text = e.kind === "status" ? e.text || "" : "";
  const res = /^([✔✖]) (tests (passed|failed)[^]*)/.exec(text);
  const cls = res ? (res[1] === "✔" ? "pass" : "fail") : e.kind === "run_end" && e.is_error ? "stopped" : "done";
  p.el.classList.replace("running", cls);
  p.spin.replaceWith(h("span", { class: "phase-mark", "aria-hidden": "true" }, { pass: "✓", fail: "✗", stopped: "■", done: "✓" }[cls]));
  delete p.time.dataset.since;
  if (res) {
    p.name.textContent = res[2][0].toUpperCase() + res[2].slice(1);
    p.time.remove(); // the line has the duration
  } else {
    p.time.textContent = duration((Date.parse(e.at) || Date.now()) - p.since);
    if (cls === "stopped") p.name.textContent += " (stopped)";
  }
  if (cls === "pass" || cls === "done") p.el.open = false; // the output matters when it failed
  return !!res;
}


// timeline merges a saved chat's message nodes with the worker's events.
export function timeline(nodes, saved) {
  const at = (iso) => (iso ? Date.parse(iso) : Infinity); // chats from before timestamps go last
  const items = [
    ...saved.map((m, i) => ({ at: at(m.at), i, node: nodes[i] })),
    ...worker.events.map((e, i) => ({ at: at(e.at), i: saved.length + i, ev: e })),
  ].sort((a, b) => a.at - b.at || a.i - b.i);
  const frag = document.createDocumentFragment();
  worker.cur = null;
  worker.phase = null;
  worker.tools = new Map();
  for (const it of items) it.ev ? workerNode(frag, it.ev) : frag.append(it.node);
  return [...frag.childNodes];
}

// A worker event streamed in while its todo is open.
export function addWorkerEvent(id, e) {
  if (worker.id !== id) { worker.live.push(e); return; } // still loading
  worker.events.push(e);
  const log = $("#d-chat-log"), sc = $("#detail .detail-body");
  const stick = sc.scrollHeight - sc.scrollTop - sc.clientHeight < 120;
  $("#d-chat-empty").hidden = true;
  workerNode(log, e);
  if (stick && state.pane === "timeline") sc.scrollTop = sc.scrollHeight;
}

// workerNode adds one worker event to parent: text and tool calls go into the
// Agent turn at its end (or a new one), Dash's notes into a log block, and run
// boundaries become dividers.
function workerNode(parent, e) {
  // Live events animate in; a timeline being rebuilt (into a fragment) doesn't.
  const enter = (el) => parent.nodeType === Node.ELEMENT_NODE ? entering(el) : el;
  const last = (kind) => (worker.cur?.kind === kind && worker.cur.el === parent.lastElementChild ? worker.cur : null);
  const divider = (text, cls = "") => { worker.cur = null; parent.append(h("div", { class: `tl-divider ${cls}`, role: "separator" }, h("span", {}, text))); };
  switch (e.kind) {
    case "run_start":
      return divider(`Agent · ${e.text || "Started"} · ${clockTime(e.at)}`, "agent");
    case "run_end":
      // Whatever didn't report its end stopped with the run.
      endPhase(e);
      for (const t of worker.tools.values()) toolSettled(t);
      divider(`Agent ${e.is_error ? "stopped" : "finished"} · ${clockTime(e.at)}`, e.is_error ? "agent error" : "agent");
      if (e.is_error && e.text) parent.append(h("p", { class: "tl-error" }, e.text));
      return;
    case "prompt":
      worker.cur = null;
      return parent.append(enter(h("div", { class: "msg user to-agent" },
        ib({ type: "button", class: "btn small copy-raw", title: "Copy the raw markdown", onclick: attempt(() => copy(e.text || "", "Copied")) }, "copy", "Copy"),
        h("div", { class: "msg-from" }, "To agent"), h("div", { class: "md", innerHTML: renderMarkdown(e.text || "") }))));
    case "status": {
      const text = e.text || "";
      const start = /^▶ (setup|tests): ([^]*)/.exec(text);
      if (start) {
        endPhase(e);
        const p = worker.phase = phaseBlock(start[1] === "tests" ? "Running tests" : "Setting up the worktree", start[2], Date.parse(e.at));
        worker.cur = null;
        parent.append(p.el);
        return;
      }
      if (worker.phase && text.startsWith("│ ")) {
        const log = worker.phase.log, end = log.scrollHeight - log.scrollTop - log.clientHeight < 24;
        log.hidden = false;
        log.append(`${text.slice(2)}\n`);
        if (end) log.scrollTop = log.scrollHeight;
        return;
      }
      if (worker.phase && endPhase(e)) return; // the phase's own result line
      let cur = last("notes");
      if (!cur) { cur = worker.cur = { kind: "notes", el: h("pre", { class: "log tl-log" }) }; parent.append(cur.el); }
      cur.el.append(`${new Date(e.at).toLocaleTimeString(timeLocale)} ${e.text}\n`);
      return;
    }
    case "tool_result": {
      const t = worker.tools.get(e.id);
      if (t) toolResult(t, e);
      return;
    }
  }
  let cur = last("agent");
  if (!cur) {
    const reply = h("div", { class: "reply" });
    cur = worker.cur = { kind: "agent", el: enter(h("div", { class: "msg assistant agent" }, h("div", { class: "msg-from" }, "Agent"), reply)), reply, seg: "" };
    parent.append(cur.el);
  }
  switch (e.kind) {
    case "text":
      cur.seg += (cur.seg ? "\n\n" : "") + e.text;
      cur.reply.innerHTML = renderMarkdown(cur.seg);
      break;
    case "tool_use": {
      // Later text goes below the tool call, in a reply of its own.
      const t = toolCall(e.tool, e.input || {}, Date.parse(e.at) || Date.now());
      worker.tools.set(e.id, t);
      cur.el.append(enter(t.el));
      cur.reply = h("div", { class: "reply" });
      cur.el.append(cur.reply);
      cur.seg = "";
      break;
    }
    case "result":
      cur.el.append(e.is_error
        ? h("p", { class: "tl-error" }, e.text)
        : h("p", { class: "agent-meta" }, `${plural(e.turns, "turn")}${e.cost_usd ? ` · ≈$${e.cost_usd.toFixed(2)}` : ""}`));
      worker.cur = null;
      break;
  }
}
