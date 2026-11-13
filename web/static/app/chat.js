import { renderMarkdown } from "../editor.js";
import { $, chatSuggestOn, entering, h, ib } from "./dom.js";
import * as store from "./store.js";
import { wsURL } from "./viewport.js";
import { api, here, on } from "./api.js";
import { ask, attempt, busy, copy, fail, toast } from "./dialogs.js";
import { attachBox, attachThumb } from "./attachments.js";
import { state } from "./state.js";
import { showView } from "./nav.js";
import { projectKey } from "./events.js";
import { duration } from "./activity.js";
import { closeDetail, openTodo } from "./detail.js";
import { canRework, reworkTodo, takeReviewNext } from "./drawmode.js";
import { describePerm } from "./review.js";
import { loadTodoChat, renderChatBadge, renderChatDots, renderChatPane, viewingChat } from "./chatpane.js";
import { timeline } from "./timeline.js";
import { fmtTokens } from "./runstats.js";

// ------------------------------------------------------------------ chat
// usageLine shows what an answer took: tokens in and out, time spent
// thinking and in all. Null for answers saved before Dash recorded it.
const fmtSecs = (ms) => {
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
};
function usageLine(u) {
  if (!u) return null;
  const cacheRead = u.cache_read_tokens || 0, cacheNew = u.cache_creation_tokens || 0;
  const input = (u.input_tokens || 0) + cacheRead + cacheNew;
  const parts = [`${fmtTokens(input)} in`, `${fmtTokens(u.output_tokens || 0)} out`];
  if (u.thinking_ms >= 500) parts.push(`thought ${fmtSecs(u.thinking_ms)}`);
  if (u.duration_ms) parts.push(fmtSecs(u.duration_ms));
  const title = [
    `Input: ${(u.input_tokens || 0).toLocaleString()} tokens`,
    cacheRead ? `Read from cache: ${cacheRead.toLocaleString()}` : null,
    cacheNew ? `Written to cache: ${cacheNew.toLocaleString()}` : null,
    `Output: ${(u.output_tokens || 0).toLocaleString()} tokens (thinking included)`,
    u.thinking_ms ? `Thinking: ${fmtSecs(u.thinking_ms)}` : null,
    u.duration_ms ? `Total: ${fmtSecs(u.duration_ms)}` : null,
  ].filter(Boolean).join("\n");
  return h("div", { class: "msg-usage", title }, parts.join(" · "));
}

// makeChat drives one conversation UI: a log, its empty state and a composer.
// target() names where it runs ({prefix, ...fields sent with each turn});
// clear() deletes its saved conversation.
// The client owns the history and sends it in full each turn; the server saves
// every finished turn. With resume, a turn cut off by a lost connection (Dash
// restarting, say) is sent again once Dash is back. labels ({user, assistant})
// head each message with who it's from; merge(nodes, saved) places a saved
// conversation's messages among other nodes. With shell (project and task
// chats), a message starting with "!" runs as a shell command where the
// chat runs instead of going to Claude (see shellRunner).
//
// After a reply, the server may suggest the user's next message (with the
// Account → Chat preference on): it shows as the empty composer's
// placeholder, Send (or Enter) sends it as it is, and Tab puts it in the
// composer to edit. Typing, sending or a new turn clears it.
//
// Its parts (chatQueue, shellRunner, chatStreamHandler) share c: the chat's
// state and the helpers they call one another through.
export function makeChat({ log, empty, form, target, actions, cwd, clear, scroller = () => log, saved = () => {}, resume = false, labels = null, merge = (nodes) => nodes, onRunner = () => {}, onState = () => {}, shell = false }) {
  // waiting: ids of the permission requests the current turn waits on an answer to.
  // queue: messages written while a reply streams ({q, files}), sent together
  // as one message once it's done.
  const c = { history: [], current: null, ws: null, prefix: "", waiting: new Set(), queue: [], log, empty, form, target, actions, labels, scroller, onState };
  const input = c.input = form.q;
  // While a reply streams, the send button stops it instead, unless there's
  // something in the composer: then it queues that.
  const sendBtn = form.querySelector("button:not([type=button])");
  const sendLook = { html: sendBtn.innerHTML, label: sendBtn.getAttribute("aria-label") };
  const stopLook = `<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="6.5" y="6.5" width="11" height="11" rx="2" fill="currentColor"/></svg>`;
  const composing = () => !!input.value.trim() || att.items().length > 0;
  c.busyButton = () => {
    const stopping = !!c.current && !composing();
    sendBtn.disabled = false;
    sendBtn.classList.toggle("stop", stopping);
    sendBtn.innerHTML = stopping ? stopLook : sendLook.html;
    const what = c.current?.shell ? "command" : "reply";
    sendBtn.setAttribute("aria-label", stopping ? `Stop the ${what}` : c.current ? "Queue the message" : sendLook.label);
    sendBtn.title = stopping ? "Stop" : c.current ? `Queue: sent when the ${what === "reply" ? "reply" : "command"} is done` : "";
  };
  // A turn that ends sends what's queued (done) or, when it was stopped or
  // failed, puts it back in the composer to send again or not.
  const finish = c.finish = (drain = false) => {
    c.current = null;
    c.waiting.clear();
    c.busyButton();
    onState();
    drain ? queue.drain() : queue.restore();
  };
  // smooth glides there when a new message arrives and the end is within a screen.
  const toEnd = c.toEnd = (smooth = false) => {
    if (!log.offsetParent) return;
    const sc = scroller(), near = sc.scrollHeight - sc.scrollTop - sc.clientHeight < sc.clientHeight;
    const glide = smooth && near && !matchMedia("(prefers-reduced-motion: reduce)").matches;
    sc.scrollTo({ top: sc.scrollHeight, behavior: glide ? "smooth" : "instant" });
  };
  const from = c.from = (who) => labels ? h("div", { class: "msg-from" }, labels[who]) : null;
  const assistant = (m) => h("div", { class: "msg assistant" }, from("assistant"), h("div", { class: "reply", innerHTML: renderMarkdown(m.content) }), usageLine(m.usage), actions(m.content));
  const prefixOf = () => target().prefix || "";
  const user = (m) => m.shell ? sh.block(m, labels && from("user")).el : h("div", { class: "msg user" }, from("user"), h("div", { class: "md", innerHTML: renderMarkdown(m.content || "") }),
    m.attachments?.length ? h("div", { class: "msg-attach" }, ...m.attachments.map((a) => attachThumb(a, null, prefixOf()))) : null);

  // "!" commands: the composer shows shell mode while its text starts with
  // "!". A read-only chat (or a hub whose runners don't allow them) runs none:
  // the text goes to Claude as it is.
  const shellOn = c.shellOn = () => shell && chatRunner.info?.commands !== false;
  const isShell = c.isShell = (q) => /^!\s*\S/.test(q || "");
  const shellTag = h("div", { class: "shell-tag", role: "status", hidden: true });
  form.prepend(shellTag);
  const shellMode = () => {
    const bang = shell && isShell(input.value.trimStart());
    const on = bang && shellOn();
    form.classList.toggle("shell-mode", on);
    shellTag.hidden = !bang;
    shellTag.replaceChildren(...(on
      ? [h("b", {}, "! Shell mode"), " Runs in the chat's folder, not sent to Claude. Claude sees the command and its output next turn. No terminal: interactive programs don't work."]
      : [h("b", {}, "! commands are off"), " This chat is read-only, so it runs no commands: this goes to Claude as a message."]));
  };
  // Images and files dropped onto the chat or pasted into its text field.
  const attachRow = h("div", { class: "attach", hidden: true });
  form.prepend(attachRow);
  const att = c.att = attachBox(attachRow, input, () => c.busyButton(), { zone: form.parentElement, compact: true, prefix: prefixOf });

  // The suggested next message, while the composer is empty.
  const placeholder = input.placeholder, placeholderTitle = input.title;
  c.suggestion = "";
  const suggest = c.suggest = (text = "") => {
    c.suggestion = text;
    input.placeholder = text || placeholder;
    input.title = text ? (matchMedia("(pointer: coarse)").matches ? "Send sends this suggestion" : "Enter sends this suggestion; Tab puts it in the box to edit") : placeholderTitle;
    form.classList.toggle("suggesting", !!text);
  };
  document.addEventListener("dash:chatsuggest", () => { if (!chatSuggestOn()) suggest(); });

  const queue = chatQueue(c);
  const sh = shellRunner(c);
  c.runShell = sh.run;
  const onStream = chatStreamHandler(c);

  c.socket = (prefix) => {
    if (c.ws && c.ws.readyState <= 1 && c.prefix === prefix) return c.ws;
    c.ws?.close();
    const ws = c.ws = new WebSocket(wsURL(on(prefix, "/ws/chat")));
    c.prefix = prefix;
    ws.onmessage = (m) => {
      const d = JSON.parse(m.data);
      d.text ??= ""; // omitted when empty
      // An abandoned socket (see reset) finishes its reply so the server saves it.
      if (ws !== c.ws) {
        // Nobody sees its approval requests any more.
        if (d.kind === "permission") ws.send(JSON.stringify({ type: "permission", id: d.id, decision: "deny" }));
        if (d.kind === "done" || d.kind === "error" || d.kind === "shell_done") { ws.close(); if (d.kind !== "error") saved(ws.fields); }
        return;
      }
      if (d.kind === "suggestion") {
        // Only for an idle, empty composer: a late one (a new turn started, or
        // the user is writing) is dropped.
        if (!c.current && !composing() && chatSuggestOn() && d.text) suggest(d.text);
        return;
      }
      if (d.kind === "cwd" && cwd) { cwd.hidden = false; cwd.textContent = cwd.title = `Runs in ${d.text}`; }
      if (d.kind === "runner") onRunner(d.text);
      onStream(ws, d);
    };
    ws.onclose = (e) => {
      if (ws !== c.ws || !c.current) return;
      if (e.code === 1009) {
        // The server refuses a conversation this long (see maxChatFrame).
        c.ws = null;
        c.current.el.classList.remove("pending");
        c.current.el.classList.add("error");
        c.current.reply.textContent = "This conversation is too long to send. Clear it, or start a new one.";
        c.history.pop();
        finish();
        return;
      }
      if (c.current.shell) {
        // Whether it ran (and was saved) isn't known: the saved chat says.
        sh.ended(c.current, { error: "Connection lost: reload to see whether it ran" }, false);
        return;
      }
      if (resume) {
        c.ws = null;
        c.current.reply.textContent = "Connection lost. Picking up when Dash is back…";
        setTimeout(() => { if (c.current && !c.ws) turn(c.current.target); }, 1500);
        return;
      }
      c.current.el.classList.remove("pending");
      c.current.reply.textContent ||= "Connection lost. Send your message again.";
      c.history.pop();
      finish();
    };
    return ws;
  };

  // settleTurn ends the reply cur: done, failed or stopped. What it said
  // stays in the history; a reply with nothing in it takes its question back
  // out. A reply that's done with something in it sends what's queued.
  c.settleTurn = (cur, { stopped = false, failed = false } = {}) => {
    cur.el.classList.remove("pending");
    if (stopped) for (const st of cur.el.querySelectorAll(".perm-actions")) st.replaceChildren(h("span", { class: "perm-status" }, "Stopped: denied"));
    for (const t of cur.tools.values()) toolSettled(t);
    const u = stopped ? h("div", { class: "msg-usage" }, "Stopped") : entering(usageLine(cur.usage));
    if (u) cur.el.append(u);
    if (cur.content) {
      cur.el.append(entering(actions(cur.content)));
      c.history.push({ role: "assistant", content: cur.content, at: cur.at, ...(cur.usage && !stopped ? { usage: cur.usage } : {}) });
    } else {
      c.history.pop(); // keep history alternating
    }
    finish(!stopped && !failed && !!cur.content);
  };

  // reset shows another (or a cleared) conversation. A reply still streaming
  // is left to finish on its own socket.
  function reset(saved = []) {
    if (c.current) { c.ws = null; finish(); }
    suggest();
    c.history = [...saved];
    const nodes = merge(saved.map((m) => m.role === "user" ? user(m) : assistant(m)), saved);
    empty.hidden = nodes.length > 0;
    log.replaceChildren(empty, ...nodes);
    toEnd();
  }

  const send = c.send = (q, attachments = []) => {
    empty.hidden = true;
    const m = { role: "user", content: q, at: new Date().toISOString(), ...(attachments.length ? { attachments } : {}) };
    c.history.push(m);
    log.append(entering(user(m)));
    turn(target());
  };

  // stop ends the reply streaming now. What it said so far stays (the server
  // saves it too); a reply with nothing in it yet takes its question back out.
  function stop() {
    const cur = c.current;
    if (!cur) return;
    if (cur.shell) return sh.stop(cur);
    if (c.ws?.readyState === 1) c.ws.send(JSON.stringify({ type: "cancel" }));
    c.settleTurn(cur, { stopped: true });
  }

  // turn asks for an answer to the last question in history, replacing
  // whatever is left of an earlier try.
  function turn({ prefix = "", ...fields }) {
    const thinking = h("div", { class: "thinking md" });
    const reply = h("div", { class: "reply" });
    const el = entering(h("div", { class: "msg assistant pending" }, from("assistant"), thinking, reply));
    c.current ? c.current.el.replaceWith(el) : log.append(el);
    toEnd(true);
    c.current = { el, thinking, reply, content: "", seg: "", tools: new Map(), target: { prefix, ...fields }, at: new Date().toISOString() };
    suggest();
    c.busyButton();
    onState();
    const ws = c.socket(prefix);
    ws.fields = fields;
    const go = () => ws.send(JSON.stringify({ type: "send", ...fields, ...(chatSuggestOn() && { suggest: true }), messages: c.history }));
    ws.readyState === 1 ? go() : ws.addEventListener("open", go, { once: true });
  }

  // Slash commands run here instead of going to Claude. Anything else starting
  // with "/" (a path, say) is sent as a message.
  const commands = {
    clear: async () => { await clear(); reset(); },
    help: () => toast(`/clear starts a new conversation. /help shows this.${shell ? " !command runs a shell command where the chat runs (no terminal); Claude sees it and its output next turn." : ""} Messages sent while Claude answers are queued and go once it's done; Up in an empty composer edits the last one. A suggested next message shows greyed out in the empty composer: Enter sends it, Tab puts it there to edit.`, { head: "Chat commands" }),
  };
  commands.new = commands.reset = commands.clear;

  const autoGrow = c.autoGrow = () => { input.style.height = "auto"; input.style.height = `${input.scrollHeight}px`; shellMode(); };
  input.addEventListener("input", () => { autoGrow(); if (c.suggestion) suggest(); if (c.current) c.busyButton(); });
  form.addEventListener("submit", attempt(async (e) => {
    e.preventDefault();
    // With nothing written, only a press of the button stops a reply: Enter in the text field doesn't.
    if (c.current && !composing()) return e.submitter === sendBtn ? stop() : undefined;
    await att.settled();
    const files = att.items();
    // An empty composer sends the suggested message, if there is one.
    const q = input.value.trim() || (!files.length && !c.current ? c.suggestion : "");
    suggest();
    if (!q && !files.length) return;
    const cmd = /^\/(\w+)$/.exec(q)?.[1].toLowerCase();
    input.value = "";
    autoGrow();
    if (!files.length && Object.hasOwn(commands, cmd)) return commands[cmd]();
    att.reset();
    // While Claude answers, the message waits its turn.
    if (c.current) return queue.push({ q, files });
    if (!files.length && shellOn() && isShell(q)) return sh.run(q);
    send(q, files);
  }));
  input.addEventListener("keydown", (e) => {
    // Tab in an empty composer puts the suggested message in it to edit;
    // otherwise Tab moves the focus as usual.
    if (e.key === "Tab" && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey && c.suggestion && !composing()) {
      e.preventDefault();
      input.value = c.suggestion;
      suggest();
      autoGrow();
      input.setSelectionRange(input.value.length, input.value.length);
      return;
    }
    // Up in an empty composer takes the last queued message back to edit.
    if (e.key === "ArrowUp" && !input.value && c.queue.length) {
      e.preventDefault();
      return queue.unqueue(c.queue.at(-1), true);
    }
    // Enter sends on devices with a hardware keyboard; on touch keyboards Enter adds a line.
    if (e.key === "Enter" && !e.shiftKey && !matchMedia("(pointer: coarse)").matches) {
      e.preventDefault();
      form.requestSubmit();
    }
  });

  // resumeLast answers a question left unanswered in a saved conversation.
  function resumeLast() {
    if (c.current || c.history.at(-1)?.role !== "user" || c.history.at(-1).shell) return;
    empty.hidden = true;
    turn(target());
  }

  return { reset, resumeLast, stop, input, toEnd, clear: commands.clear, get history() { return c.history; }, get busy() { return !!c.current; }, get waiting() { return c.waiting.size > 0; } };
}

// chatQueue holds the messages written while a reply streams (c.queue). The
// queue shows above the composer: tap a message to take it back to edit, ×
// drops it.
function chatQueue(c) {
  const { input, att } = c;
  const row = h("div", { class: "chat-queue", hidden: true });
  c.form.prepend(row);
  const render = () => {
    row.hidden = !c.queue.length;
    row.replaceChildren(...c.queue.map((m) => h("div", { class: "queued" },
      h("button", { type: "button", class: "queued-text", title: "Edit this queued message", onclick: () => unqueue(m, true) },
        h("span", { class: "queued-tag" }, "Queued"),
        h("span", { class: "queued-body" }, m.q || `${m.files.length} file${m.files.length > 1 ? "s" : ""}`),
        m.q && m.files.length ? h("span", { class: "queued-files" }, `+${m.files.length}`) : null),
      h("button", { type: "button", class: "chip-x", "aria-label": "Remove from the queue", title: "Remove", onclick: () => unqueue(m) }, "×"))));
    c.busyButton();
  };
  // edit puts the messages back in the composer, ahead of what's in it already.
  const toComposer = (msgs) => {
    if (!msgs.length) return;
    c.suggest();
    input.value = [...msgs.map((m) => m.q), input.value.trim()].filter(Boolean).join("\n\n");
    att.reset([...msgs.flatMap((m) => m.files), ...att.items()]);
    c.autoGrow();
  };
  function unqueue(m, edit = false) {
    c.queue = c.queue.filter((x) => x !== m);
    if (edit) { toComposer([m]); input.focus(); }
    render();
  }
  function push(m) {
    c.queue.push(m);
    render();
  }
  function restore() {
    if (!c.queue.length) return;
    toComposer(c.queue);
    c.queue = [];
    render();
  }
  function drain() {
    if (!c.queue.length) return;
    // A queued "!" command runs on its own; what's queued after it waits for it.
    const cmd = (m) => !m.files.length && c.shellOn() && c.isShell(m.q);
    if (cmd(c.queue[0])) {
      const m = c.queue.shift();
      render();
      return c.runShell(m.q);
    }
    const i = c.queue.findIndex(cmd), take = i < 0 ? c.queue : c.queue.slice(0, i);
    c.queue = i < 0 ? [] : c.queue.slice(i);
    const q = take.map((m) => m.q).filter(Boolean).join("\n\n"), files = take.flatMap((m) => m.files);
    render();
    c.send(q, files);
  }
  return { push, unqueue, restore, drain };
}

// chatStreamHandler returns what handles a message from the chat's current
// socket ws: it fills in the reply streaming (or the command running) in it.
function chatStreamHandler(c) {
  return (ws, d) => {
    const cur = c.current;
    if (!cur) return;
    if (cur.shell) return c.shell.delta(cur, d);
    switch (d.kind) {
      case "thinking":
        cur.think = (cur.think || "") + d.text;
        cur.thinking.innerHTML = renderMarkdown(cur.think);
        break;
      case "text":
        cur.content += d.text;
        cur.seg += d.text;
        cur.reply.innerHTML = renderMarkdown(cur.seg);
        break;
      case "tool_use": {
        // Later text goes below the tool call, in a reply of its own. A reply
        // still empty moves below it instead, so consecutive tool calls share
        // one "Thinking…" at the end rather than one after each call.
        const t = toolCall(d.tool, d.input), { el } = t;
        cur.tools.set(d.id, t);
        if (cur.seg) {
          cur.reply.after(el);
          cur.reply = h("div", { class: "reply" });
          el.after(cur.reply);
          cur.seg = "";
        } else {
          cur.reply.before(el);
        }
        entering(el);
        break;
      }
      case "tool_result": {
        const t = cur.tools.get(d.id);
        if (t) toolResult(t, d);
        break;
      }
      case "permission":
        cur.reply.before(entering(chatPermCard(d, (decision) => ws.send(JSON.stringify({ type: "permission", id: d.id, decision })))));
        c.waiting.add(d.id);
        c.onState();
        break;
      case "permission_decided": {
        if (c.waiting.delete(d.id)) c.onState();
        const st = c.log.querySelector(`.perm[data-id="${d.id}"] .perm-actions`);
        st?.replaceChildren(h("span", { class: "perm-status" }, { allow: "Approved", deny: "Denied", timeout: "Not answered in time: denied" }[d.text] || d.text));
        break;
      }
      case "usage":
        cur.usage = d.usage;
        break;
      case "error":
        cur.el.classList.add("error");
        cur.reply.append(h("p", {}, d.text));
        c.settleTurn(cur, { failed: true });
        break;
      case "done":
        c.settleTurn(cur);
        break;
    }
    const sc = c.scroller();
    if (sc.scrollHeight - sc.scrollTop - sc.clientHeight < 120) c.toEnd();
  };
}

// shellRunner runs "!" commands where the chat runs, each shown as a block in
// the log. Its message, with the result, is saved in the conversation, which
// Claude sees next turn.
function shellRunner(c) {
  // A "!" command as a block in the log: the command, where it ran, its
  // output (streaming in while it runs) and how it ended. head: the "You"
  // label of a chat whose messages are labelled.
  function block(m, head = null) {
    const sh = m.shell, command = sh?.command ?? m.content.replace(/^\s*!\s*/, "");
    const since = m.at ? Date.parse(m.at) : Date.now();
    const status = h("span", { class: "shell-status" });
    const where = h("div", { class: "shell-where" });
    const out = h("pre", { class: "shell-out" });
    const note = h("div", { class: "shell-note", hidden: true });
    const el = h("div", { class: "msg shell" }, head,
      h("div", { class: "shell-head" }, h("code", { class: "shell-line", title: command }, h("b", {}, "!"), ` ${command}`), status), where, out, note);
    const b = { el, status, where, out, note, since };
    if (sh) result(b, sh);
    else {
      el.classList.add("running");
      status.replaceChildren(h("span", { class: "act-spin", "aria-hidden": "true" }), h("span", { class: "tool-time", "data-since": String(since) }));
      out.textContent = "";
    }
    return b;
  }
  // result shows how a command ended (sh: the server's record of it).
  function result(b, sh) {
    b.el.classList.remove("running");
    const failed = !!sh.error || sh.exit_code !== 0;
    b.el.classList.toggle("failed", failed);
    const time = sh.duration_ms >= 1000 ? ` · ${duration(sh.duration_ms)}` : "";
    const how = sh.error ? sh.error : `exit ${sh.exit_code}`;
    b.status.replaceChildren(h("span", { class: `shell-exit ${failed ? "bad" : "ok"}`, title: sh.error || `Exit code ${sh.exit_code}` }, how + time));
    if (sh.dir || sh.runner) { b.where.hidden = false; b.where.textContent = `in ${sh.dir || "?"}${sh.runner ? ` on ${sh.runner}` : ""}`; }
    else b.where.hidden = true;
    b.out.textContent = sh.output || (sh.error ? "" : "(no output)");
    b.out.hidden = !b.out.textContent;
    const notes = [];
    if (sh.truncated) notes.push(h("p", {}, "Long output: its start and end are kept, and that's what Claude sees."));
    if (failed && sh.exit_code !== undefined) {
      notes.push(h("p", {}, "Commands here run without a terminal or input: interactive programs (vim, top, less, a password prompt…) fail or wait until the timeout. ",
        ib({ class: "link", onclick: () => { closeDetail(); showView("run"); } }, "terminal", "Open the Shell view"), " for those."));
    }
    b.note.hidden = !notes.length;
    b.note.replaceChildren(...notes);
  }
  // run runs q ("!command") where the chat runs.
  function run(q) {
    c.suggest();
    c.empty.hidden = true;
    const m = { role: "user", content: q, at: new Date().toISOString() };
    c.history.push(m);
    const b = block(m, c.labels && c.from("user"));
    c.log.append(entering(b.el));
    const id = Math.random().toString(36).slice(2, 10);
    c.current = { shell: true, id, m, b, el: b.el, out: "" };
    c.toEnd(true);
    c.busyButton();
    c.onState();
    const { prefix = "", ...fields } = c.target();
    delete fields.review; // a command reviews nothing
    const ws = c.socket(prefix);
    ws.fields = fields;
    const go = () => ws.send(JSON.stringify({ type: "shell", id, ...fields, messages: c.history }));
    ws.readyState === 1 ? go() : ws.addEventListener("open", go, { once: true });
  }
  // The live output is capped: the saved result keeps its start and end.
  const maxLive = 200_000;
  function delta(cur, d) {
    if (d.id && d.id !== cur.id) return;
    if (d.kind === "shell_output") {
      cur.out += d.text;
      if (cur.out.length > maxLive) cur.out = `[… earlier output not shown …]\n${cur.out.slice(-maxLive * 3 / 4)}`;
      const o = cur.b.out, end = o.scrollHeight - o.scrollTop - o.clientHeight < 24;
      o.textContent = cur.out;
      if (end) o.scrollTop = o.scrollHeight;
    } else if (d.kind === "shell_done" && d.shell) {
      ended(cur, d.shell, true);
    } else if (d.kind === "error") {
      ended(cur, { error: d.text }, false);
    }
  }
  // ended settles the current command: ran (its message, with sh, stays
  // in the conversation, as the server saved it) or not (taken back out).
  function ended(cur, sh, ran) {
    if (c.current !== cur) return;
    clearTimeout(cur.giveUp);
    if (ran) {
      cur.m.shell = sh;
      result(cur.b, sh);
    } else {
      cur.b.el.classList.remove("running");
      cur.b.el.classList.add("failed", "error");
      cur.b.status.replaceChildren(h("span", { class: "shell-exit bad" }, "didn't run"));
      cur.b.note.hidden = false;
      cur.b.note.replaceChildren(h("p", {}, sh.error));
      if (c.history.at(-1) === cur.m) c.history.pop();
    }
    c.finish(ran);
  }
  // stop stops the command; its result (what it printed until then)
  // still comes back, and is kept.
  function stop(cur) {
    if (cur.stopping) return;
    cur.stopping = true;
    cur.b.status.replaceChildren(h("span", { class: "act-spin", "aria-hidden": "true" }), " Stopping…");
    if (c.ws?.readyState === 1) c.ws.send(JSON.stringify({ type: "cancel" }));
    cur.giveUp = setTimeout(() => ended(cur, { command: cur.m.content.replace(/^\s*!\s*/, ""), output: cur.out, exit_code: -1, error: "stopped" }, true), 15000);
  }
  return c.shell = { block, run, delta, ended, stop };
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
  t.el.classList.remove("running");
  // No result means it stopped with the turn: neither passed nor failed.
  const mark = t.out.hidden ? ["cut", "■"] : t.el.classList.contains("error") ? ["bad", "✗"] : ["ok", "✓"];
  t.el.querySelector(".act-spin")?.replaceWith(h("span", { class: `tool-mark ${mark[0]}`, "aria-hidden": "true" }, mark[1]));
  delete t.time.dataset.since;
  const ms = end - t.since;
  t.time.textContent = end && ms >= 3000 ? duration(ms) : "";
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

// An approval request from the chat, answered over its own socket.
function chatPermCard(p, decide) {
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

// "Draft task" saves the answer as a draft right away, its instructions
// cleaned up first (as the form's Clean up does); if that fails the answer is
// kept as it is. key: the project a project chat is about; its tasks go there
// even when another project is selected. The general chat's go to the selected project.
export const chatActions = (pick, key) => (answer) => h("div", { class: "msg-actions" },
  ib({ class: "link", onclick: attempt(async (e) => {
    const p = (key && state.projects?.find((x) => projectKey(x) === key)) || (!key && state.project);
    if (!p) return toast(pick && !state.projects?.length ? "Create a project first" : "Select a project first", true);
    await busy(e.currentTarget, async () => {
      let prompt = answer.trim(), cleaned = true;
      try { prompt = (await api("POST", "/api/refine", { prompt })).prompt?.trim() || prompt; } catch { cleaned = false; }
      const t = await api("POST", `/api/projects/${p.id}/todos`, { title: "", prompt, enqueue: false }, p.prefix || "");
      const other = projectKey(p) !== projectKey(state.project);
      if (!other) state.lane = "draft";
      toast(`Saved as draft${other ? ` in ${p.name}` : ""}${cleaned ? "" : " (instructions not cleaned up)"}`,
        { tone: "ok", action: other || !t?.id ? undefined : openTodo(t.id) });
    }, { label: "Saving draft…" });
  }) }, "plus", "Draft task"),
  ib({ class: "link", onclick: () => copy(answer, "Copied") }, "copy", "Copy"));

// Where the general chat runs. The hub never runs Claude: the chat runs on
// one of your own runners (your Claude login, your subscription). The bar
// says which, lets you pick when you have several, and says how to start one
// when none is online. (A runner's own dashboard answers itself: no bar.)
export const chatRunner = {
  info: null, // GET /api/claude: {local, runners: [{id, name, online}]}
  get picked() { return store.get("dash.chatRunner", ""); },
  set picked(id) { store.set("dash.chatRunner", id); },
  // The runner the next turn asks for ("" = the hub picks the first online).
  choice() {
    const rs = this.info?.runners || [];
    const pick = rs.find((r) => r.id === this.picked && r.online);
    return pick ? pick.id : "";
  },
  async load() {
    this.info = await api("GET", "/api/claude");
    this.render();
  },
  render(running = "") {
    const bar = $("#chat-runner"), text = $("#chat-runner-text"), sel = $("#chat-runner-pick");
    const info = this.info;
    if (!info || info.local) { bar.hidden = true; return; }
    bar.hidden = false;
    const online = info.runners.filter((r) => r.online);
    const current = running || online.find((r) => r.id === this.choice())?.name || online[0]?.name;
    bar.classList.toggle("online", !!current);
    bar.classList.toggle("offline", !current);
    if (current) text.textContent = `Runs on ${current}, with its Claude login`;
    else if (info.runners.length) text.textContent = `Your runner${info.runners.length > 1 ? "s are" : ` ${info.runners[0].name} is`} offline: run \`dash runner\` on it to chat`;
    else text.textContent = "A runner is required to chat: this dashboard doesn't run Claude itself. Start `dash runner` on a computer of yours with Claude Code (account menu → Runners → Add a runner); the chat runs there with your Claude login";
    text.title = text.textContent;
    sel.hidden = online.length < 2;
    if (!sel.hidden) {
      sel.replaceChildren(...online.map((r) => h("option", { value: r.id }, r.name)));
      sel.value = this.choice() || online[0].id;
    }
    $("#chat-empty-where").textContent = current
      ? `A general chat, run on your runner ${current} in a scratch folder, with that computer's Claude login. For questions about one project, use This project.`
      : "A general chat, run on one of your own runners with its Claude login. This hub doesn't run Claude itself.";
  },
};
$("#chat-runner-pick").addEventListener("change", (e) => { chatRunner.picked = e.target.value; chatRunner.render(); });
chatRunner.load().catch(() => {}); // an older Dash without /api/claude: the bar stays hidden

// Pane chats with a reply nobody has looked at yet, by chat key ("general" or a
// projectKey). Kept in localStorage so a reload still shows them.
const savedUnread = store.getJSON("dash.chatUnread", []);
export const chatUnread = new Set(Array.isArray(savedUnread) ? savedUnread : []);
export function saveChatUnread() {
  store.setJSON("dash.chatUnread", [...chatUnread]);
}
// Another project's chats whose tab you closed while a reply streamed: the
// reply doesn't bring the tab back (until you talk in that chat again).
export const chatDismissed = new Set();
// onState for a pane chat: a turn that ends while you aren't looking at the
// chat leaves it unread.
export function paneChatState(key, get) {
  let wasBusy = false;
  return () => {
    const busy = get().busy;
    if (wasBusy && !busy && chatDismissed.delete(key)) {
      // Its tab was closed: nobody is waiting for this reply.
    } else if (wasBusy && !busy && !viewingChat(key) && !chatUnread.has(key)) {
      chatUnread.add(key);
      saveChatUnread();
      renderChatDots();
    }
    // Another project's chat shows as a tab while it streams or is unread.
    const changed = wasBusy !== busy;
    wasBusy = busy;
    if (changed && key !== "general" && key !== projectKey(state.project)) renderChatPane();
    else renderChatBadge();
  };
}

// The global chat isn't about any project: it runs in the home directory of
// this machine, or on one of your runners (chatRunner).
export const chat = makeChat({
  log: $("#chat-log"), empty: $("#chat-empty"), form: $("#chat-form"),
  target: () => { const runner = chatRunner.choice(); return runner ? { runner } : {}; },
  onRunner: (name) => chatRunner.render(name),
  clear: () => api("DELETE", "/api/chat"),
  actions: chatActions(true),
  cwd: $("#chat-cwd"),
  resume: true,
  onState: paneChatState("general", () => chat),
});

// The server saves every turn, so the conversation survives restarts. A
// question it never got to answer is asked again, unless another window's
// turn is still running.
export async function loadChat() {
  const res = await fetch("/api/chat");
  if (!res.ok) throw new Error(res.statusText);
  const saved = await res.json();
  if (!saved.length || chat.history.length || chat.busy) return;
  chat.reset(saved);
  if (!res.headers.get("X-Chat-Busy")) chat.resumeLast();
}
loadChat().catch((e) => toast(`Couldn't load chat history: ${e.message}`, true));

// A conversation about the open todo, run on the todo's computer. Claude sees
// the todo's instructions, so a draft can be talked into a better prompt.
const EDITABLE = ["draft", "queued", "failed"];
export const todoChat = makeChat({
  log: $("#d-chat-log"), empty: $("#d-chat-empty"), form: $("#d-chat-form"),
  labels: { user: "You · chat only", assistant: "Chat" },
  merge: (nodes, saved) => timeline(nodes, saved),
  // reviewNext marks the turn the Review button sends, which locks the task until it ends.
  target: () => { const review = takeReviewNext(); return { prefix: here(), todo: state.detail, ...(review && { review }) }; },
  clear: () => api("DELETE", `/api/todos/${state.detail}/chat`),
  scroller: () => $("#detail .detail-body"),
  shell: true,
  cwd: $("#d-chat-cwd"),
  // A turn left streaming when its todo was closed is saved when it finishes.
  saved: ({ todo }) => { if (todo === state.detail && !todoChat.busy) loadTodoChat().catch(fail); },
  actions: (answer) => {
    const t = state.todos.get(state.detail);
    return h("div", { class: "msg-actions" },
      t && EDITABLE.includes(t.status) ? ib({ class: "link", onclick: attempt(async () => {
        if (!await ask({ title: "Replace the instructions?", body: "This answer becomes the task's instructions for Claude.", ok: "Replace" })) return;
        await api("PATCH", `/api/todos/${t.id}`, { prompt: answer });
        toast("Instructions updated");
      }) }, "draft", "Use as instructions") : null,
      canRework(t) ? ib({ class: "link", onclick: attempt(() => reworkTodo(t, answer)) }, "redo", "Send to agent") : null,
      ib({ class: "link", onclick: () => copy(answer, "Copied") }, "copy", "Copy"));
  },
});
