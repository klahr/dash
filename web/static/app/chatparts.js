// What a chat with Claude shows in its log, shared by the app's chats
// (chat.js, timeline.js) and the planner's chat on planning.html
// (plannerclaude.js): tool calls, approval requests and how long things took.
// It wires up nothing on the page when it loads (bar the elapsed-time
// ticker), so any page can import it.
import { h, ib } from "./dom.js";
import { SECOND, durationText } from "../time.js";

// "8s", "1m 5s", "1h 2m"
export const duration = (ms) => durationText(ms, SECOND);
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

// Tool calls in the chat: a short readable line ("Read chatpane.js"), and
// the raw name and full input when expanded.
const firstLine = (s) => String(s ?? "").split("\n")[0];
const baseName = (p) => firstLine(p).replace(/[\\/]+$/, "").split(/[\\/]/).pop();
const hostOf = (u) => { try { return new URL(u).host; } catch { return firstLine(u); } };
const quoted = (s) => (s ? `“${firstLine(s)}”` : "");
// Each tool's label and what it acted on, from its input i.
const TOOL_LABELS = {
  Read: (i) => ["Read", baseName(i.file_path)],
  Edit: (i) => ["Edited", baseName(i.file_path)],
  MultiEdit: (i) => ["Edited", baseName(i.file_path)],
  Write: (i) => ["Wrote", baseName(i.file_path)],
  NotebookEdit: (i) => ["Edited", baseName(i.notebook_path)],
  NotebookRead: (i) => ["Read", baseName(i.notebook_path)],
  Grep: (i) => ["Searched for", quoted(i.pattern)],
  Glob: (i) => ["Looked for", firstLine(i.pattern)],
  LS: (i) => ["Listed", baseName(i.path)],
  WebFetch: (i) => ["Fetched", hostOf(i.url)],
  WebSearch: (i) => ["Searched the web for", quoted(i.query)],
  Task: (i) => ["Ran an agent", firstLine(i.description)],
  Agent: (i) => ["Ran an agent", firstLine(i.description)],
  TodoWrite: () => ["Updated the to-do list", ""],
  AskUserQuestion: () => ["Asked you", ""],
  ToolSearch: () => ["Loaded tools", ""],
  Skill: (i) => ["Used a skill", firstLine(i.skill)],
  BashOutput: () => ["Read a command's output", ""],
  KillShell: () => ["Stopped a command", ""],
};
// Dash's own tools (mcp__dash_board__…), by their short name.
const task = (i) => firstLine(i.title || i.id || i.todo_id || "");
const DASH_LABELS = {
  create_todo: (i) => ["Created a draft task", task(i)],
  update_todo: (i) => ["Changed a task", task(i)],
  queue_todo: (i) => ["Queued a task", task(i)],
  stop_todo: (i) => ["Stopped a task", task(i)],
  restart_todo: (i) => ["Restarted a task", task(i)],
  delete_todo: (i) => ["Deleted a task", task(i)],
  approve_todo: (i) => ["Approved a task", task(i)],
  send_back: (i) => ["Sent a task back", task(i)],
  get_todo: (i) => ["Read a task", task(i)],
  list_todos: () => ["Listed tasks", ""],
  get_diff: (i) => ["Read a task's changes", task(i)],
  get_log: (i) => ["Read a task's log", task(i)],
  add_log: () => ["Added to the log", ""],
  list_projects: () => ["Listed projects", ""],
  get_project: () => ["Read the project", ""],
  update_project: () => ["Changed the project", ""],
  report_issue: (i) => ["Reported an issue", firstLine(i.title)],
  show_image: (i) => ["Showed an image", baseName(i.path)],
  read_try_terminal: () => ["Read the Try terminal", ""],
  run_tests: () => ["Ran the tests", ""],
  run_command: (i) => ["Ran", firstLine(i.command)],
  active_work: () => ["Checked other tasks' work", ""],
  create_goal: (i) => ["Created a goal", task(i)],
  get_goal: () => ["Read a goal", ""],
  list_goals: () => ["Listed goals", ""],
  update_goal: (i) => ["Changed a goal", task(i)],
  stop_goal: () => ["Stopped a goal", ""],
  delete_goal: () => ["Deleted a goal", ""],
  create_release: () => ["Created a release", ""],
  list_deploys: () => ["Listed deploys", ""],
  create_card: (i) => ["Created a card", task(i)],
  get_card: () => ["Read a card", ""],
  list_cards: () => ["Listed cards", ""],
  update_card: (i) => ["Changed a card", task(i)],
  ask_user: () => ["Asked you", ""],
};
// toolLabel is tool call (tool, i)'s line: [label, what it acted on]. A tool
// it doesn't know reads as its name without the mcp__server__ prefix.
export function toolLabel(tool, i = {}) {
  i = i && typeof i === "object" ? i : {};
  const mcp = /^mcp__(.+?)__(.+)$/.exec(tool || "");
  const [table, key] = mcp ? [mcp[1] === "dash_board" ? DASH_LABELS : {}, mcp[2]] : [TOOL_LABELS, tool];
  const known = Object.hasOwn(table, key) ? table[key] : null;
  if (known) return known(i);
  const name = (mcp ? mcp[2] : String(tool || "?")).replace(/_/g, " ");
  return [name.charAt(0).toUpperCase() + name.slice(1), firstLine(i.command ?? i.file_path ?? i.pattern ?? i.path ?? i.title ?? i.query ?? i.url ?? "")];
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
  const [label, what] = toolLabel(tool, input);
  const head = tool === "Bash" && input?.command
    ? [h("span", { class: "tool-cmd" }, h("b", {}, "$"), ` ${firstLine(input.command)}`)]
    : [h("b", {}, label), what ? ` ${what}` : null];
  const time = h("span", { class: "tool-time", "data-since": String(since), "data-quiet": "" });
  const el = h("details", { class: "tool running" }, h("summary", {}, h("span", { class: "act-spin", "aria-hidden": "true" }), h("span", { class: "tool-head" }, ...head), time),
    h("div", { class: "tool-raw" }, tool), h("pre", { class: "tool-in" }, toolDetail(tool, input)), out);
  return { el, out, time, since };
}
export function toolResult(t, d) {
  t.out.hidden = false;
  t.out.textContent = d.text || "(no output)";
  t.el.classList.toggle("error", !!d.is_error);
  toolSettled(t, d.at ? Date.parse(d.at) : Date.now());
}
// savedToolCall is tool call c as saved with its answer (model.ChatTool):
// done, as it ended. since is when it began on a clock of the answer's
// own (its calls' durations end to end), so a group's line adds them up.
export function savedToolCall(c, since = 0) {
  const t = toolCall(c.tool || "?", c.input || {}, since);
  if (!c.cut) {
    t.out.hidden = false;
    t.out.textContent = c.output || "(no output)";
    t.el.classList.toggle("error", !!c.is_error);
  }
  toolSettled(t, since + (c.duration_ms || 0));
  return t;
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

// questionGroups builds the questions of Claude's question request p
// (AskUserQuestion): each with its header, its options (checkboxes when
// several may be chosen) and an Other field for an answer in the user's own
// words. Inputs are named from prefix, which must be unique on the page.
// answers() returns {out}, the answers keyed by question (several choices
// joined with ", "), or {missing} for the first group left unanswered.
export function questionGroups(p, prefix) {
  const qs = (Array.isArray(p.input?.questions) ? p.input.questions : []).filter((q) => q && typeof q.question === "string");
  const groups = qs.map((q, qi) => {
    const type = q.multiSelect ? "checkbox" : "radio", name = `${prefix}-${qi}`;
    const otherPick = h("input", { type, name, value: "", "aria-label": "Other" });
    const other = h("input", { type: "text", class: "ask-other-text", placeholder: "Your own answer", maxLength: 2000, "aria-label": `Other answer: ${q.question}` });
    other.addEventListener("input", () => { if (other.value.trim()) otherPick.checked = true; });
    otherPick.addEventListener("change", () => { if (otherPick.checked) other.focus(); });
    const el = h("fieldset", { class: "choice ask-q" },
      h("legend", {}, q.header ? h("span", { class: "ask-head" }, q.header) : null, h("span", { class: "ask-text" }, q.question)),
      q.multiSelect ? h("p", { class: "hint ask-multi" }, "Choose any that apply.") : null,
      ...(Array.isArray(q.options) ? q.options : []).map((o) => h("label", { class: "radio" },
        h("input", { type, name, value: String(o?.label ?? "") }),
        h("span", {}, String(o?.label ?? ""), o?.description ? h("em", {}, o.description) : null))),
      h("label", { class: "radio ask-other" }, otherPick, h("span", {}, "Other", other)));
    return { q, el, otherPick, other };
  });
  const answers = () => {
    const out = {};
    for (const g of groups) {
      const picked = [...g.el.querySelectorAll("input:checked")].map((i) => i === g.otherPick ? g.other.value.trim() : i.value).filter(Boolean);
      if (!picked.length) return { missing: g };
      out[g.q.question] = picked.join(", ");
    }
    return { out };
  };
  // focusMissing puts focus where group g wants an answer.
  const focusMissing = (g) => (g.el.querySelector("input:checked") ? g.other : g.el.querySelector("input"))?.focus();
  return { qs, groups, answers, focusMissing };
}

// chatQuestionCard answers Claude's question request p in a chat: decide
// gets "allow" with the answers, or "deny".
function chatQuestionCard(p, decide) {
  const { qs, groups, answers, focusMissing } = questionGroups(p, `chat-ask-${p.id}`);
  const hint = h("p", { class: "hint", role: "status", style: "margin:0 0 8px", hidden: true });
  const done = () => card.querySelectorAll("button, input").forEach((el) => { el.disabled = true; });
  const card = h("form", { class: "perm ask", "aria-label": `Claude asks: ${qs.map((q) => q.question).join(" ")}`, "data-id": p.id, onsubmit: (e) => {
    e.preventDefault();
    const a = answers();
    if (a.missing) {
      hint.textContent = `Answer “${a.missing.q.header || a.missing.q.question}” first`;
      hint.hidden = false;
      focusMissing(a.missing);
      return;
    }
    hint.hidden = true;
    done();
    decide("allow", a.out);
  } },
    h("p", { class: "perm-q" }, qs.length > 1 ? "Claude has some questions" : "Claude has a question"),
    ...groups.map((g) => g.el),
    hint,
    h("div", { class: "perm-actions" },
      ib({ class: "btn allow", type: "submit" }, "check", "Submit"),
      ib({ class: "btn danger", type: "button", onclick: () => { done(); decide("deny"); } }, "x", "Decline")));
  return card;
}

// An approval request from a chat, answered over its own socket: decide
// gets "allow" or "deny", and for a question (see isQuestion) the answers.
export function chatPermCard(p, decide) {
  if (isQuestion(p)) return chatQuestionCard(p, decide);
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

// chatCloseReason says why the chat's websocket closed (its close event e)
// when the user must do something other than send again: the server refuses
// a conversation this long (close code 1009, see maxChatFrame in
// internal/api/ws.go). Else it's "": the connection was just lost.
export const chatCloseReason = (e) => e?.code === 1009 ? "This conversation is too long to send. Clear it, or start a new one." : "";

// The stop square a chat's send button shows while a reply streams.
const STOP_LOOK = `<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="6.5" y="6.5" width="11" height="11" rx="2" fill="currentColor"/></svg>`;

// sendStopButton returns set(stopping, { label, title }), which shows a
// chat's send button btn as is (Send) or, while a reply streams, as Stop.
// label and title default to "Stop the reply" and "Stop" when stopping, and
// to btn's own label and no title when not. set.label is btn's own label.
export function sendStopButton(btn) {
  const look = { html: btn.innerHTML, label: btn.getAttribute("aria-label") || "Send" };
  const set = (stopping, { label = stopping ? "Stop the reply" : look.label, title = stopping ? "Stop" : "" } = {}) => {
    btn.classList.toggle("stop", stopping);
    btn.innerHTML = stopping ? STOP_LOOK : look.html;
    btn.setAttribute("aria-label", label);
    btn.title = title;
  };
  set.label = look.label;
  return set;
}
