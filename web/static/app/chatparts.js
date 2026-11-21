// What a chat with Claude shows in its log, shared by the app's chats
// (chat.js, timeline.js) and the planner's chat on planning.html
// (plannerclaude.js): tool calls, approval requests and how long things took.
// It wires up nothing on the page when it loads (bar the elapsed-time
// ticker), so any page can import it.
import { h, ib } from "./dom.js";

// "8s", "1m 05s", "1h 02m"
export function duration(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(s / 3600)}h ${String(Math.floor(s / 60) % 60).padStart(2, "0")}m`;
}
// Elapsed-time labels on whatever is still running tick once a second.
setInterval(() => {
  if (document.hidden) return;
  const now = Date.now();
  for (const el of document.querySelectorAll("[data-since]")) {
    const ms = now - Number(el.dataset.since);
    el.textContent = el.dataset.quiet != null && ms < 3000 ? "" : duration(ms);
  }
}, 1000);

// isQuestion says whether request p is Claude asking the user questions
// (AskUserQuestion), answered with a question card instead of Allow/Deny.
export const isQuestion = (p) => p?.tool === "AskUserQuestion";
// permHead heads a toast or notification about request p.
export const permHead = (p) => isQuestion(p) ? "Claude has a question" : "Claude needs your approval";

// describePerm says what an approval request p would let Claude do.
export function describePerm(p) {
  const i = p.input || {};
  switch (p.tool) {
    case "AskUserQuestion": return { title: "ask you a question", detail: (Array.isArray(i.questions) ? i.questions : []).map((q) => q?.question).filter(Boolean).join("\n") };
    case "Bash": return { title: "run a command", detail: i.command, note: i.description };
    case "Write": case "Edit": case "MultiEdit": case "NotebookEdit": return { title: `edit ${i.file_path || i.notebook_path || "a file"}`, detail: i.file_path };
    case "WebFetch": return { title: "fetch a web page", detail: i.url };
    case "WebSearch": return { title: "search the web", detail: i.query };
    default: return { title: `use ${p.tool}`, detail: JSON.stringify(i, null, 2) };
  }
}

// Tool calls in the chat: a one-line summary, and the full input when expanded.
function toolSummary(tool, i = {}) {
  const s = i.command ?? i.file_path ?? i.pattern ?? i.path ?? "";
  return String(s).split("\n")[0];
}
function toolDetail(tool, i = {}) {
  const lines = (s, p) => String(s ?? "").split("\n").map((l) => p + l).join("\n");
  switch (tool) {
    case "Bash": return i.command;
    case "Edit": return `${i.file_path}\n${lines(i.old_string, "- ")}\n${lines(i.new_string, "+ ")}`;
    case "Write": return `${i.file_path}\n${i.content}`;
    default: return JSON.stringify(i, null, 2);
  }
}

// A collapsible tool call: its input, and its output once filled in. Until
// then it shows a spinner (a check or cross once done, in the same space so the
// text doesn't move), and how long it has run once that's a while.
export function toolCall(tool, input, since = Date.now()) {
  const out = h("pre", { class: "tool-out", hidden: true });
  const head = tool === "Bash" && input?.command
    ? [h("b", {}, "$"), ` ${toolSummary(tool, input)}`]
    : [h("b", {}, tool), ` ${toolSummary(tool, input)}`];
  const time = h("span", { class: "tool-time", "data-since": String(since), "data-quiet": "" });
  const el = h("details", { class: "tool running" }, h("summary", {}, h("span", { class: "act-spin", "aria-hidden": "true" }), h("span", { class: "tool-head" }, ...head), time),
    h("pre", { class: "tool-in" }, toolDetail(tool, input)), out);
  return { el, out, time, since };
}
export function toolResult(t, d) {
  t.out.hidden = false;
  t.out.textContent = d.text || "(no output)";
  t.el.classList.toggle("error", !!d.is_error);
  toolSettled(t, d.at ? Date.parse(d.at) : Date.now());
}
// A tool call is done: its spinner turns into a mark in the same space, and a long one keeps how long it took.
export function toolSettled(t, end = null) {
  if (t.done) return;
  t.done = true;
  t.end = end;
  t.el.classList.remove("running");
  // No result means it stopped with the turn: neither passed nor failed.
  const mark = toolMark(t);
  t.el.querySelector(".act-spin")?.replaceWith(h("span", { class: `tool-mark ${mark[0]}`, "aria-hidden": "true" }, mark[1]));
  delete t.time.dataset.since;
  const ms = end - t.since;
  t.time.textContent = end && ms >= 3000 ? duration(ms) : "";
  t.group?.update();
}
const toolCut = (t) => t.done && t.out.hidden;
const toolFailed = (t) => t.el.classList.contains("error");
const toolMark = (t) => toolCut(t) ? ["cut", "■"] : toolFailed(t) ? ["bad", "✗"] : ["ok", "✓"];

// placeToolCall puts the tool call t in the log just before ref. Calls in a
// row, with nothing between them, share one line (see toolGroup): t joins
// the group right before ref, if there is one. It returns the element it
// added to the log (to animate in), or null when t joined a group, whose
// line updates in place instead.
export function placeToolCall(t, ref) {
  const g = ref.previousElementSibling?.toolGroup;
  if (g) {
    g.add(t);
    return null;
  }
  ref.before(toolGroup(t).el);
  return t.el;
}

// toolGroup starts a group of tool calls in a row with first. A group of one
// is just its call. From the second on it's one line: the call running (or
// the last), how many came before it and how long it has run, and once all
// are done a mark for the lot: ✓ all passed, ✗ some failed (and how many), ■
// the turn stopped while one ran. It opens to show each call as it is alone.
function toolGroup(first) {
  const g = { calls: [first], el: first.el };
  first.group = first.el.toolGroup = g;
  let slot, head, more, failed, time, list;
  g.add = (t) => {
    if (!list) {
      // The second call: the line takes the first one's place.
      slot = h("span", { class: "act-spin", "aria-hidden": "true" });
      head = h("span", { class: "tool-head" });
      more = h("span", { class: "tool-more" });
      failed = h("span", { class: "tool-failed" });
      time = h("span", { class: "tool-time", "data-quiet": "" });
      list = h("div", { class: "tool-list" });
      // The line changes with each call: a log read aloud doesn't say each one.
      const el = h("details", { class: "tool tool-group running", "aria-live": "off" },
        h("summary", { title: "Show each tool call" }, slot, head, more, failed, time), list);
      first.el.replaceWith(el);
      list.append(first.el);
      delete first.el.toolGroup;
      g.el = el;
      el.toolGroup = g;
    }
    g.calls.push(t);
    t.group = g;
    list.append(t.el);
    g.update();
  };
  g.update = () => {
    if (!list) return;
    const running = g.calls.filter((t) => !t.done), now = running.at(-1) || g.calls.at(-1);
    head.replaceChildren(...[...now.el.querySelector(".tool-head").childNodes].map((n) => n.cloneNode(true)));
    more.textContent = `· ${g.calls.length - 1} more`;
    const bad = g.calls.filter(toolFailed).length;
    failed.textContent = bad ? `· ${bad} failed` : "";
    g.el.classList.toggle("error", bad > 0);
    g.el.classList.toggle("running", running.length > 0);
    if (running.length) {
      if (!slot.classList.contains("act-spin")) slot.replaceWith(slot = h("span", { class: "act-spin", "aria-hidden": "true" }));
      if (time.dataset.since !== String(now.since)) {
        time.dataset.since = String(now.since);
        time.textContent = "";
      }
      return;
    }
    const mark = g.calls.some(toolCut) ? ["cut", "■"] : bad ? ["bad", "✗"] : ["ok", "✓"];
    slot.replaceWith(slot = h("span", { class: `tool-mark ${mark[0]}`, "aria-hidden": "true" }, mark[1]));
    delete time.dataset.since;
    // How long the lot took, from the first call to the last to end.
    const end = Math.max(...g.calls.map((t) => t.end || 0)), ms = end - first.since;
    time.textContent = end && ms >= 3000 ? duration(ms) : "";
  };
  return g;
}

// An approval request from a chat, answered over its own socket: decide
// gets "allow" or "deny".
export function chatPermCard(p, decide) {
  const d = describePerm(p);
  const answer = (decision) => (e) => {
    e.currentTarget.parentElement.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    decide(decision);
  };
  return h("div", { class: "perm", role: "group", "aria-label": `Permission request: ${d.title}`, "data-id": p.id },
    h("p", { class: "perm-q" }, `Claude wants to ${d.title}`),
    d.note ? h("p", { class: "hint", style: "margin:0" }, d.note) : null,
    h("code", { class: "perm-cmd" }, toolDetail(p.tool, p.input || {})),
    h("div", { class: "perm-actions" },
      ib({ class: "btn allow", onclick: answer("allow") }, "check", "Approve"),
      ib({ class: "btn danger", onclick: answer("deny") }, "x", "Deny")));
}

// permDecided shows, in log, how the approval request d.id was answered
// (a permission_decided message: d.text is allow, deny or timeout).
export function permDecided(log, d) {
  const st = log.querySelector(`.perm[data-id="${CSS.escape(d.id || "")}"] .perm-actions`);
  st?.replaceChildren(h("span", { class: "perm-status" }, permStatus(d.text)));
}
export const permStatus = (decision) => ({ allow: "Approved", deny: "Denied", timeout: "Not answered in time: denied" })[decision] || decision;
