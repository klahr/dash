import { renderMarkdown } from "../editor.js";
import { $, chatSuggestOn, entering, h, ib } from "./dom.js";
import * as store from "./store.js";
import { wsURL } from "./viewport.js";
import { api, here, on, request } from "./api.js";
import { ask, attempt, copy, fail, toast } from "./dialogs.js";
import { attachBox, attachThumb } from "./attachments.js";
import { STATUS_LABEL, state } from "./state.js";
import { showView } from "./nav.js";
import { projectKey } from "./events.js";
import { chatCloseReason, chatPermCard, duration, permDecided, placeToolCall, sendStopButton, toolCall, toolResult, toolSettled } from "./chatparts.js";
import { closeDetail } from "./detail.js";
import { canRework, reworkLabel, reworkTodo, takeReviewNext } from "./drawmode.js";
import { loadTodoChat, renderChatBadge, renderChatDots, renderChatPane, viewingChat } from "./chatpane.js";
import { timeline } from "./timeline.js";
import { fmtTokens } from "./runstats.js";
import { confirmRunAs } from "./people.js";
import { dictation } from "./dictation.js";
import { speakApproval, speakButton, speakReply, stopSpeaking } from "./speech.js";

// ------------------------------------------------------------------ chat
// usageLine shows what an answer took: tokens in and out, time spent
// thinking and in all. Null for answers saved before Dash recorded it.
function usageLine(u) {
  if (!u) return null;
  const cacheRead = u.cache_read_tokens || 0, cacheNew = u.cache_creation_tokens || 0;
  const input = (u.input_tokens || 0) + cacheRead + cacheNew;
  const parts = [`${fmtTokens(input)} in`, `${fmtTokens(u.output_tokens || 0)} out`];
  if (u.thinking_ms >= 500) parts.push(`thought ${duration(u.thinking_ms)}`);
  if (u.duration_ms) parts.push(duration(u.duration_ms));
  const title = [
    `Input: ${(u.input_tokens || 0).toLocaleString()} tokens`,
    cacheRead ? `Read from cache: ${cacheRead.toLocaleString()}` : null,
    cacheNew ? `Written to cache: ${cacheNew.toLocaleString()}` : null,
    `Output: ${(u.output_tokens || 0).toLocaleString()} tokens (thinking included)`,
    u.thinking_ms ? `Thinking: ${duration(u.thinking_ms)}` : null,
    u.duration_ms ? `Total: ${duration(u.duration_ms)}` : null,
  ].filter(Boolean).join("\n");
  return h("div", { class: "msg-usage", title }, parts.join(" · "));
}

// makeChat drives one conversation UI: a log, its empty state and a composer.
// target() names where it runs ({prefix, ...fields sent with each turn});
// clear() deletes its saved conversation.
// The client sends the history in full each turn, with base: how many of its
// messages the server has saved (all but the new one); the server saves every
// finished turn. A project chat's server keeps the conversation itself and
// refuses a turn from an out-of-date copy ("stale": another device added to it
// or cleared it): the message goes back in the composer and stale() reloads. With resume, a turn cut off by a lost connection (Dash
// restarting, say) is sent again once Dash is back. labels ({user, assistant})
// head each message with who it's from; merge(nodes, saved) places a saved
// conversation's messages among other nodes. With shell (project and task
// chats), a message starting with "!" runs as a shell command where the
// chat runs instead of going to Claude (see shellRunner).
//
// After a reply, the server may suggest the user's next message (with the
// Account → Chat preference on): it shows as the empty composer's
// placeholder, Send (or Enter) sends it as it is, and Tab puts it in the
// composer to edit. Writing hides it and emptying the composer again brings
// it back; sending, a new turn or a new suggestion drops it.
//
// mode() (the task chat's switch) names where the composer sends: null for the
// chat itself, or {text, label, title, placeholder} for the send button's text
// and labels and the box's placeholder, reply: {text, label, title} for the
// button once Claude has answered, with send(q, files) when the message
// goes elsewhere (Rework): it returns whether it was sent, which empties the
// composer. c.modeChanged() shows a new mode.
//
// Its parts share c: the chat's state (history, the turn running as current,
// its socket, the waiting approvals and the queue), what the chat runs on
// (target) and the helpers they call one another through. Each is a function
// of c and of the options it uses: chatView (rendering), chatComposer (the
// composer: chatSendButton, chatSuggestion, chatRecall with its keys,
// chatQueue), chatTurns (sending and settling turns), shellRunner, and
// chatSocket with chatStreamHandler (the connection, where the chat runs and
// on which runner).
export function makeChat({ log, empty, form, target, actions, cwd, clear, scroller = () => log, saved = () => {}, stale = () => {}, resume = false, labels = null, merge = (nodes) => nodes, onRunner = () => {}, onState = () => {}, shell = false, mode = () => null, tasks = null }) {
  // waiting: ids of the permission requests the current turn waits on an answer to.
  // queue: messages written while a reply streams ({q, files}), sent together
  // as one message once it's done.
  const c = { history: [], current: null, ws: null, prefix: "", waiting: new Set(), queue: [], target };
  chatView(c, { log, empty, labels, actions, scroller, merge, tasks });
  const composer = chatComposer(c, { form, mode, shell, clear });
  const turns = chatTurns(c, { stale, onState });
  shellRunner(c);
  c.socket = chatSocket(c, { cwd, saved, resume, onRunner, onStream: chatStreamHandler(c) });
  return { ...turns, ...composer, toEnd: c.toEnd, get history() { return c.history; }, get busy() { return !!c.current; }, get waiting() { return c.waiting.size > 0; } };
}

// chatView renders the conversation in log (empty: what shows while it has
// no messages). It sets on c what the other parts render with: c.from(who)
// (the label heading a message, with labels), c.user(m) and c.assistant(m)
// (a message), c.cards(tasks) (the cards of the tasks an answer made, with
// tasks), c.actionRow(content) (the row under an answer: actions(content)
// and its speaker button), c.toEnd(smooth) (scrolls scroller() to the end),
// c.prefixOf() (the Dash the chat runs on) and c.show(saved) (shows a saved
// conversation, its messages placed by merge). c.log, c.empty, c.labels,
// c.scroller and c.tasks are the options themselves.
function chatView(c, { log, empty, labels, actions, scroller, merge, tasks }) {
  Object.assign(c, { log, empty, labels, scroller, tasks });
  // smooth glides there when a new message arrives and the end is within a screen.
  const toEnd = c.toEnd = (smooth = false) => {
    if (!log.offsetParent) return;
    const sc = scroller(), near = sc.scrollHeight - sc.scrollTop - sc.clientHeight < sc.clientHeight;
    const glide = smooth && near && !matchMedia("(prefers-reduced-motion: reduce)").matches;
    sc.scrollTo({ top: sc.scrollHeight, behavior: glide ? "smooth" : "instant" });
  };
  const from = c.from = (who) => labels ? h("div", { class: "msg-from" }, labels[who]) : null;
  // The tasks the answer made show as cards under it (see taskCard).
  const cards = c.cards = (list) => (tasks ? (list || []).map((t) => taskCard(t, tasks)) : []);
  // The row under an answer ends with its speaker button (speech.js: shown while Spoken replies is on).
  const actionRow = c.actionRow = (content) => {
    const row = actions(content);
    row?.append(speakButton(content));
    return row;
  };
  const prefixOf = c.prefixOf = () => c.target().prefix || "";
  // The images an answer showed (show_image) fit the reply's width; a click opens the lightbox.
  const images = c.images = (list = []) => h("div", { class: "msg-images", hidden: !list.length }, ...list.map((a) => attachThumb(a, null, prefixOf())));
  const assistant = c.assistant = (m) => h("div", { class: "msg assistant" }, from("assistant"), h("div", { class: "reply", innerHTML: renderMarkdown(m.content) }),
    m.attachments?.length ? images(m.attachments) : null, ...cards(m.tasks), usageLine(m.usage), actionRow(m.content));
  const user = c.user = (m) => m.action ? actionNote(m) : m.shell ? c.shell.block(m, labels && from("user")).el : h("div", { class: "msg user" }, from("user"), h("div", { class: "md", innerHTML: renderMarkdown(m.content || "") }),
    m.attachments?.length ? h("div", { class: "msg-attach" }, ...m.attachments.map((a) => attachThumb(a, null, prefixOf()))) : null);
  c.show = (saved) => {
    const nodes = merge(saved.map((m) => m.role === "user" ? user(m) : assistant(m)), saved);
    empty.hidden = nodes.length > 0;
    log.replaceChildren(empty, ...nodes);
    toEnd();
  };
}

// chatComposer wires up the composer, form: its send button (mode() says
// where it sends, see makeChat), dictation, shell mode (with shell), the
// attachments, the suggested next message, the queue, the keys and the
// slash commands (/clear calls clear()), and sending. It sets c.form,
// c.input, c.composing(), c.att, c.shellOn(), c.isShell(q), c.autoGrow(),
// c.recall, c.queued (with c.unqueue) and c.modeChanged(), and returns what
// makeChat's caller uses of it.
function chatComposer(c, { form, mode, shell, clear }) {
  c.form = form;
  const input = c.input = form.q;
  const composing = c.composing = () => !!input.value.trim() || att.items().length > 0;
  const sendBtn = chatSendButton(c, form, mode, composing);
  // "send" (voice.js) is Enter: it sends, or queues while a reply streams.
  dictation(input, { place: (b) => sendBtn.before(b), send: () => composing() && (form.requestSubmit(), true) });

  // "!" commands: the composer shows shell mode while its text starts with
  // "!". A read-only chat (or a hub whose runners don't allow them) runs none:
  // the text goes to Claude as it is.
  const shellOn = c.shellOn = () => shell && chatRunner.info?.commands !== false;
  const isShell = c.isShell = (q) => /^!\s*\S/.test(q || "");
  const shellTag = h("div", { class: "shell-tag", role: "status", hidden: true });
  form.prepend(shellTag);
  const shellMode = () => {
    const bang = shell && !mode()?.send && isShell(input.value.trimStart());
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
  const att = c.att = attachBox(attachRow, input, () => { c.busyButton(); suggestion.show(); }, { zone: form.parentElement, compact: true, prefix: c.prefixOf });
  const suggestion = chatSuggestion(c, input, form, att, mode);
  const queue = c.queued = chatQueue(c);
  c.unqueue = queue.unqueue;

  // Slash commands run here instead of going to Claude. Anything else starting
  // with "/" (a path, say) is sent as a message.
  const commands = {
    clear: async () => { await clear(); c.reset(); },
    help: () => toast(`/clear starts a new conversation. /help shows this.${shell ? " !command runs a shell command where the chat runs (no terminal); Claude sees it and its output next turn." : ""} Messages sent while Claude answers are queued and go once it's done; Up in an empty composer edits the last one; otherwise Up and Down step through the messages sent before. A suggested next message shows grayed out in the empty composer: Enter sends it, Tab puts it there to edit.`, { head: "Chat commands" }),
  };
  commands.new = commands.reset = commands.clear;

  // The box is border-box, so the height takes the border on top of scrollHeight;
  // without it the text overflows by the border's width and a scrollbar shows.
  const autoGrow = c.autoGrow = () => {
    input.style.height = "auto";
    input.style.height = `${input.scrollHeight + input.offsetHeight - input.clientHeight}px`;
    shellMode();
    suggestion.show();
  };
  const recall = c.recall = chatRecall(c, input, autoGrow);
  input.addEventListener("input", () => { recall.end(); autoGrow(); if (c.current) c.busyButton(); });
  c.modeChanged = () => { c.busyButton(); suggestion.show(); shellMode(); };
  form.addEventListener("submit", attempt(async (e) => {
    e.preventDefault();
    stopSpeaking(); // a new message stops what is read aloud
    const m = mode();
    if (m?.send) {
      await att.settled();
      const q = input.value.trim(), files = att.items();
      // The composer empties as it goes, not once the server answers (a second
      // Enter meanwhile sends nothing twice); what wasn't sent comes back.
      const empty = (text, items) => {
        recall.end();
        input.value = text;
        att.reset(items);
        autoGrow();
        c.busyButton();
      };
      if (q || files.length) empty("", []);
      let sent = false;
      try {
        sent = await m.send(q, files);
      } finally {
        if (!sent && (q || files.length) && !composing()) empty(q, files);
      }
      return;
    }
    // With nothing written, only a press of the button stops a reply: Enter in the text field doesn't.
    if (c.current && !composing()) return e.submitter === sendBtn ? c.stop() : undefined;
    await att.settled();
    const files = att.items();
    // An empty composer sends the suggested message, if it shows.
    const q = input.value.trim() || (!c.current && suggestion.shown() ? c.suggestion : "");
    c.suggest();
    if (!q && !files.length) return;
    const cmd = /^\/(\w+)$/.exec(q)?.[1].toLowerCase();
    recall.end();
    input.value = "";
    autoGrow();
    if (!files.length && Object.hasOwn(commands, cmd)) return commands[cmd]();
    att.reset();
    // While Claude answers, the message waits its turn.
    if (c.current) return queue.push({ q, files });
    if (!files.length && shellOn() && isShell(q)) return c.shell.run(q);
    c.send(q, files);
  }));
  return { input, modeChanged: c.modeChanged, clear: commands.clear };
}

// chatTurns sends turns and settles them: c.send(q, attachments) adds a
// message and asks for its answer (c.turn(target)), c.settleTurn ends one,
// c.staleTurn takes back one the server refused (and calls stale()),
// c.finish ends whichever turn runs (and calls onState(), as every change of
// whether one runs does). It returns makeChat's reset, resumeLast, stop and
// addNote.
function chatTurns(c, { stale, onState }) {
  c.onState = onState;
  // A turn that ends sends what's queued (done) or, when it was stopped or
  // failed, puts it back in the composer to send again or not.
  const finish = c.finish = (drain = false) => {
    c.current = null;
    c.waiting.clear();
    c.busyButton();
    onState();
    drain ? c.queued.drain() : c.queued.restore();
  };

  // settleTurn ends the reply cur: done, failed or stopped. What it said,
  // and the tasks it made, stay in the history; a reply with neither takes
  // its question back out. A reply that's done with something in it sends
  // what's queued. The tasks' cards go under the answer, where assistant()
  // puts them when the chat is loaded again: the server has saved the answer
  // with them by now (by the time a stopped one's card is used, at least),
  // so their Queue and Discard can note what they did.
  c.settleTurn = (cur, { stopped = false, failed = false } = {}) => {
    cur.el.classList.remove("pending");
    if (stopped) for (const st of cur.el.querySelectorAll(".perm-actions")) st.replaceChildren(h("span", { class: "perm-status" }, "Stopped: denied"));
    for (const t of cur.tools.values()) toolSettled(t);
    for (const card of c.cards(cur.tasks)) cur.el.append(entering(card));
    const u = stopped ? h("div", { class: "msg-usage" }, "Stopped") : entering(usageLine(cur.usage));
    if (u) cur.el.append(u);
    const kept = !!cur.content || cur.tasks.length > 0 || cur.images.length > 0;
    if (cur.content) {
      const row = c.actionRow(cur.content);
      cur.el.append(entering(row));
      // A finished answer is read aloud, with Spoken replies on.
      if (!stopped && !failed) speakReply(cur.content, row?.querySelector(".speak-btn"));
    }
    if (kept) {
      c.history.push({ role: "assistant", content: cur.content, at: cur.at, ...(cur.usage && !stopped ? { usage: cur.usage } : {}), ...(cur.tasks.length ? { tasks: cur.tasks } : {}), ...(cur.images.length ? { attachments: cur.images } : {}) });
    } else {
      c.history.pop(); // keep history alternating
    }
    finish(!stopped && !failed && kept);
  };

  // reset shows another (or a cleared) conversation. A reply still streaming
  // is left to finish on its own socket.
  function reset(saved = []) {
    if (c.current) { c.ws = null; finish(); }
    c.suggest();
    c.recall.end();
    c.history = [...saved];
    c.busyButton();
    c.show(saved);
  }
  c.reset = reset;

  c.send = (q, attachments = []) => {
    c.empty.hidden = true;
    const m = { role: "user", content: q, at: new Date().toISOString(), ...(attachments.length ? { attachments } : {}) };
    c.history.push(m);
    c.log.append(c.lastUser = entering(c.user(m)));
    turn(c.target());
  };

  // staleTurn takes back a turn the server refused because this copy of the
  // conversation is out of date: its message goes back in the composer (with
  // what's queued) and the chat reloads.
  c.staleTurn = (text) => {
    const cur = c.current;
    if (!cur) return;
    const m = c.history.at(-1);
    if (m?.role === "user") {
      c.history.pop();
      c.queue.unshift({ q: m.content || "", files: m.attachments || [] });
    }
    cur.el.remove();
    if (!cur.shell) c.lastUser?.remove();
    finish();
    toast(text, { head: "Chat reloaded" });
    stale();
  };

  // stop ends the reply streaming now. What it said so far stays (the server
  // saves it too); a reply with nothing in it yet takes its question back out.
  function stop() {
    const cur = c.current;
    if (!cur) return;
    if (cur.shell) return c.shell.stop(cur);
    if (c.ws?.readyState === 1) c.ws.send(JSON.stringify({ type: "cancel" }));
    c.settleTurn(cur, { stopped: true });
  }
  c.stop = stop;

  // turn asks for an answer to the last question in history, replacing
  // whatever is left of an earlier try.
  const turn = c.turn = ({ prefix = "", ...fields }) => {
    const thinking = h("div", { class: "thinking md" });
    const reply = h("div", { class: "reply" });
    const el = entering(h("div", { class: "msg assistant pending" }, c.from("assistant"), thinking, reply));
    c.current ? c.current.el.replaceWith(el) : c.log.append(el);
    c.toEnd(true);
    c.current = { el, thinking, reply, content: "", seg: "", tools: new Map(), tasks: [], images: [], target: { prefix, ...fields }, at: new Date().toISOString() };
    c.suggest();
    c.busyButton();
    onState();
    const ws = c.socket(prefix);
    ws.fields = fields;
    const go = () => ws.send(JSON.stringify({ type: "send", ...fields, ...(chatSuggestOn() && { suggest: true }), base: c.history.length - 1, messages: c.history }));
    ws.readyState === 1 ? go() : ws.addEventListener("open", go, { once: true });
  };

  // resumeLast answers a question left unanswered in a saved conversation.
  function resumeLast() {
    if (c.current || c.history.at(-1)?.role !== "user" || c.history.at(-1).shell || c.history.at(-1).action) return;
    c.empty.hidden = true;
    turn(c.target());
  }

  // addNote shows m, an action note the server saved at the end of the
  // conversation (see taskCard). While a turn runs, the server has it
  // before the turn's messages: the chat reloads once the turn ends.
  function addNote(m) {
    if (c.current || c.history.some((x) => x.action && x.action.kind === m.action?.kind && x.action.todo === m.action?.todo)) return;
    c.empty.hidden = true;
    c.history.push(m);
    c.log.append(entering(c.user(m)));
    c.toEnd(true);
  }

  return { reset, resumeLast, stop, addNote };
}

// chatSendButton sets c.busyButton, which shows the send button for the
// moment: while a reply streams it stops it instead, unless there's something
// in the composer (composing()): then it queues that. mode() is the chat's
// mode (see makeChat). It returns the button.
function chatSendButton(c, form, mode, composing) {
  const sendBtn = form.querySelector("button:not([type=button])");
  const look = sendStopButton(sendBtn);
  c.busyButton = () => {
    // Once Claude has answered, the next message is a reply, not a first question.
    const m0 = mode(), m = m0?.reply && c.history.some((x) => x.role === "assistant") ? { ...m0, ...m0.reply } : m0;
    // A message sent elsewhere doesn't wait for the reply, so it's never queued.
    const replying = !!c.current && !m?.send;
    const stopping = replying && !composing();
    sendBtn.disabled = false;
    const what = c.current?.shell ? "command" : "reply";
    look(stopping, {
      label: stopping ? `Stop the ${what}` : replying ? "Queue the message" : m?.label || look.label,
      title: stopping ? "Stop" : replying ? `Queue: sent when the ${what} is done` : m?.title || "",
    });
    const text = sendBtn.querySelector(".send-text");
    if (text && m?.text) text.textContent = replying ? "Queue" : m.text;
  };
  return sendBtn;
}

// chatSuggestion shows the suggested next message (c.suggestion). It stays
// pending while the composer has something in it (written or attached in
// att), and shows (as the placeholder) whenever it's empty. It sets
// c.suggest(text), which sets it ("" drops it), and returns show(), which
// shows it or the usual placeholder, and shown(), whether it shows.
function chatSuggestion(c, input, form, att, mode) {
  const placeholder = input.placeholder, placeholderTitle = input.title, inputLabel = input.getAttribute("aria-label");
  c.suggestion = "";
  // A small cue beside the grayed-out suggestion: Tab takes it to edit.
  const cue = h("div", { class: "suggest-cue", "aria-hidden": "true", hidden: true }, h("kbd", {}, "Tab"), " to edit");
  form.prepend(cue);
  // A suggestion is for the chat: a message sent elsewhere doesn't show it.
  const shown = () => !!c.suggestion && !input.value && !att.items().length && !mode()?.send;
  const show = () => {
    const on = shown();
    cue.hidden = !on;
    input.placeholder = on ? c.suggestion : mode()?.placeholder || placeholder;
    if (inputLabel) input.setAttribute("aria-label", mode()?.inputLabel || inputLabel);
    input.title = on ? (matchMedia("(pointer: coarse)").matches ? "Send sends this suggestion" : "Enter sends this suggestion; Tab puts it in the box to edit") : placeholderTitle;
    form.classList.toggle("suggesting", on);
  };
  c.suggest = (text = "") => {
    c.suggestion = text;
    show();
  };
  document.addEventListener("dash:chatsuggest", () => { if (!chatSuggestOn()) c.suggest(); });
  c.suggestionShown = shown;
  return { show, shown };
}

// chatSocket returns c.socket(prefix): the chat's websocket to the Dash at
// prefix, opened anew when there's none (or it's to another Dash). Messages
// from it go to onStream; cwd (an element) shows where the chat runs and
// onRunner(name) which runner it runs on. An abandoned socket (one reset left
// streaming) finishes its reply on its own, and saved(fields) is told when it
// has. With resume, a turn cut off by a lost connection is sent again.
function chatSocket(c, { cwd, saved, resume, onRunner, onStream }) {
  return (prefix) => {
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
      if (d.kind === "stale") return c.staleTurn(d.text);
      if (d.kind === "suggestion") {
        // Only for an idle, empty composer: a late one (a new turn started, or
        // the user is writing) is dropped.
        if (!c.current && !c.composing() && chatSuggestOn() && d.text) c.suggest(d.text);
        return;
      }
      if (d.kind === "cwd" && cwd) { cwd.hidden = false; cwd.textContent = cwd.title = `Runs in ${d.text}`; }
      if (d.kind === "runner") onRunner(d.text);
      onStream(ws, d);
    };
    ws.onclose = (e) => {
      if (ws !== c.ws || !c.current) return;
      const refused = chatCloseReason(e);
      if (refused) {
        // The server refuses a conversation this long (see maxChatFrame).
        c.ws = null;
        c.current.el.classList.remove("pending");
        c.current.el.classList.add("error");
        c.current.reply.textContent = refused;
        c.history.pop();
        c.finish();
        return;
      }
      if (c.current.shell) {
        // Whether it ran (and was saved) isn't known: the saved chat says.
        c.shell.ended(c.current, { error: "Connection lost: reload to see whether it ran" }, false);
        return;
      }
      if (resume) {
        c.ws = null;
        c.current.reply.textContent = "Connection lost. Picking up when Dash is back…";
        setTimeout(() => { if (c.current && !c.ws) c.turn(c.current.target); }, 1500);
        return;
      }
      c.current.el.classList.remove("pending");
      c.current.reply.textContent ||= "Connection lost. Send your message again.";
      c.history.pop();
      c.finish();
    };
    return ws;
  };
}

// chatRecall steps through the messages sent in this conversation, as a
// shell's history does: Up on the composer's first line shows the one before,
// Down on its last line the one after, and past the newest what was being
// written. Typing ends the browsing (end()), so an edited message isn't lost
// to a later Down. It handles the composer's other keys too: Tab takes the
// suggestion, Up in an empty composer the last queued message, Enter sends.
function chatRecall(c, input, autoGrow) {
  const recall = {
    pos: null, list: [], draft: "",
    end() { recall.pos = null; },
    step(dir) {
      const v = input.value, a = input.selectionStart, b = input.selectionEnd;
      if (dir < 0 ? v.lastIndexOf("\n", a - 1) >= 0 : v.indexOf("\n", b) >= 0) return false;
      if (recall.pos === null) {
        if (dir > 0) return false;
        recall.list = c.history.filter((m) => m.role === "user" && !m.action && m.content?.trim()).map((m) => m.content)
          .filter((q, i, all) => q !== all[i - 1]);
        if (!recall.list.length) return false;
        recall.draft = v;
        recall.pos = recall.list.length;
      }
      const next = recall.pos + dir;
      if (next < 0) return false;
      input.value = next >= recall.list.length ? recall.draft : recall.list[next];
      recall.pos = next >= recall.list.length ? null : next;
      autoGrow();
      input.setSelectionRange(input.value.length, input.value.length);
      c.busyButton();
      return true;
    },
  };
  input.addEventListener("keydown", (e) => {
    // Tab in an empty composer puts the suggested message in it to edit;
    // otherwise Tab moves the focus as usual.
    if (e.key === "Tab" && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey && c.suggestionShown()) {
      e.preventDefault();
      // It stays pending: emptying the composer shows it again.
      input.value = c.suggestion;
      autoGrow();
      input.setSelectionRange(input.value.length, input.value.length);
      return;
    }
    // Up in an empty composer takes the last queued message back to edit.
    if (e.key === "ArrowUp" && !input.value && c.queue.length) {
      e.preventDefault();
      return c.unqueue(c.queue.at(-1), true);
    }
    // Up on the first line and Down on the last step through the messages sent before.
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey && recall.step(e.key === "ArrowUp" ? -1 : 1)) {
      e.preventDefault();
      return;
    }
    // Enter sends on devices with a hardware keyboard; on touch keyboards Enter adds a line.
    if (e.key === "Enter" && !e.shiftKey && !matchMedia("(pointer: coarse)").matches) {
      e.preventDefault();
      sentByEnter = true;
      c.form.requestSubmit();
    }
  });
  // The Enter that sent mustn't also add a line to the emptied box: an Enter
  // delivered as a key and a separate character (as automation and some IMEs
  // do) types its line break after the keydown's preventDefault.
  let sentByEnter = false;
  input.addEventListener("beforeinput", (e) => {
    if (sentByEnter && (e.inputType === "insertLineBreak" || e.inputType === "insertParagraph")) e.preventDefault();
    sentByEnter = false;
  });
  input.addEventListener("keyup", (e) => { if (e.key === "Enter") sentByEnter = false; });
  return recall;
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
      return c.shell.run(m.q);
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
        // still empty stays below it, so consecutive tool calls share one
        // line (see placeToolCall) and one "Thinking…" at the end.
        const t = toolCall(d.tool, d.input);
        cur.tools.set(d.id, t);
        if (cur.seg) {
          const next = h("div", { class: "reply" });
          cur.reply.after(next);
          cur.reply = next;
          cur.seg = "";
        }
        const el = placeToolCall(t, cur.reply);
        if (el) entering(el);
        break;
      }
      case "tool_result": {
        const t = cur.tools.get(d.id);
        if (t) toolResult(t, d);
        // A task create_todo made: its card shows under the answer once the
        // turn ends (settleTurn), when the server has saved it with the task.
        if (t && d.task?.id && !d.is_error && c.tasks) cur.tasks.push(d.task);
        // An image show_image showed: below the reply's text, as when the chat is loaded again.
        if (d.attachment?.id && !d.is_error) {
          cur.images.push(d.attachment);
          const row = cur.el.querySelector(":scope > .msg-images") || cur.el.appendChild(c.images());
          row.hidden = false;
          row.append(entering(attachThumb(d.attachment, null, c.prefixOf())));
        }
        break;
      }
      case "permission":
        cur.reply.before(entering(chatPermCard(d, (decision, answers) => ws.send(JSON.stringify({ type: "permission", id: d.id, decision, answers })))));
        speakApproval(d);
        c.waiting.add(d.id);
        c.onState();
        break;
      case "permission_decided": {
        if (c.waiting.delete(d.id)) c.onState();
        permDecided(c.log, d);
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
        ib({ class: "link", onclick: () => { closeDetail(); showView("run"); } }, "terminal", "Open the Shell"), " for those."));
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
    const go = () => ws.send(JSON.stringify({ type: "shell", id, ...fields, base: c.history.length - 1, messages: c.history }));
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

// ------------------------------------------------------------------ task cards
// A task Claude made in a project chat (create_todo) shows as a card under
// the answer, once the turn has ended (and the answer is saved). While
// it's a draft, Queue, Open and Discard act on it right away, with the
// task's own routes; Queue and Discard then add a note to the chat ("You
// queued *…*.") that Claude sees next turn. Once it isn't a draft (queued or
// deleted here or elsewhere) the card says what it is now. ops (the chat's
// tasks option): {prefix(), local() (whether state.todos has the chat's
// project's tasks), open(id), note(todo, kind)}.
const TASK_DONE = { queued: "Queued ✓", discarded: "Discarded" };
// The cards shown (or once shown: those gone from the page are dropped as
// they're met), to follow their tasks' changes.
const taskCards = new Set();

// taskStatus is t's status for its card: "deleted" when it's gone, null
// when it can't be read.
async function taskStatus(id, ops) {
  const t = ops.local?.() && state.todos.get(id);
  if (t) return t.status;
  try {
    return (await api("GET", `/api/todos/${id}`, null, ops.prefix())).status;
  } catch (e) {
    return e.status === 404 ? "deleted" : null;
  }
}

// noteTask adds the chat's note that task id was queued or discarded (kind).
// A stopped answer is saved, with its tasks, a moment after the chat let go
// of it: until then the server knows no such task in the chat (404), so the
// note is tried again for a few seconds.
async function noteTask(ops, id, kind) {
  for (let i = 0; ; i++) {
    try {
      return await ops.note(id, kind);
    } catch (x) {
      if (x.status !== 404 || i >= 8) throw x;
      await new Promise((r) => setTimeout(r, 500));
    }
  }
}

function taskCard(task, ops) {
  const label = task.title || task.id;
  const tag = h("span", { class: "chat-task-tag" }, "Task");
  const actions = h("div", { class: "chat-task-actions" });
  const err = h("p", { class: "chat-task-error", role: "alert", hidden: true });
  const el = h("div", { class: "chat-task", role: "group", tabindex: "-1", "aria-label": `Task: ${label}`, "data-todo": task.id },
    h("div", { class: "chat-task-head" }, tag, h("span", { class: "chat-task-title", title: label }, label)), actions, err);
  const card = { el, task, ops, status: undefined, done: "" };
  const open = () => ib({ type: "button", class: "btn small", title: "Open the task", onclick: attempt(() => ops.open(task.id)) }, "external", "Open");
  // act runs one of the buttons: the call, then the chat's note. An error
  // shows on the card and leaves the buttons.
  const act = (kind, call) => async () => {
    const bs = [...actions.querySelectorAll("button")];
    bs.forEach((b) => { b.disabled = true; });
    err.hidden = true;
    try {
      await call();
    } catch (x) {
      bs.forEach((b) => { b.disabled = false; });
      err.hidden = false;
      err.textContent = x.message || String(x);
      return;
    }
    card.done = kind;
    // The button pressed is gone: the focus stays on the card.
    const refocus = el.contains(document.activeElement) || !document.activeElement || document.activeElement === document.body;
    renderTaskCard(card, kind === "queued" ? "queued" : "deleted");
    if (refocus) (actions.querySelector("button") || el).focus();
    try {
      await noteTask(ops, task.id, kind);
    } catch (x) {
      err.hidden = false;
      err.textContent = `${kind === "queued" ? "Queued" : "Discarded"}, but the chat couldn't note it: ${x.message || x}`;
    }
  };
  card.buttons = () => [
    ib({ type: "button", class: "btn small primary", title: "Queue it to run", onclick: act("queued", () => api("POST", `/api/todos/${task.id}/enqueue`, null, ops.prefix())) }, "queue", "Queue"),
    open(),
    ib({ type: "button", class: "btn small danger", title: "Delete this draft", onclick: act("discarded", () => api("DELETE", `/api/todos/${task.id}`, null, ops.prefix())) }, "trash", "Discard"),
  ];
  card.open = open;
  card.tag = tag;
  card.actions = actions;
  taskCards.add(card);
  renderTaskCard(card, undefined);
  taskStatus(task.id, ops).then((s) => { if (card.status === undefined) renderTaskCard(card, s); });
  return el;
}

// renderTaskCard shows card's task as having status s (undefined: not
// known yet; null: couldn't be read).
function renderTaskCard(card, s) {
  card.status = s;
  const { actions, tag } = card;
  if (s === undefined) { tag.textContent = "Task"; actions.replaceChildren(); return; }
  if (s === "draft") {
    tag.textContent = "Draft";
    actions.replaceChildren(...card.buttons());
    return;
  }
  const word = s === "deleted" ? (card.done === "discarded" ? TASK_DONE.discarded : "Deleted")
    : s === "queued" && card.done === "queued" ? TASK_DONE.queued
      : s ? STATUS_LABEL[s] || s : "Task";
  tag.textContent = "Task";
  const badge = h("span", { class: `chat-task-state s-${s || "unknown"}`, role: "status" }, word);
  actions.replaceChildren(...(s === "deleted" ? [badge] : [badge, card.open()]));
}

// chatTaskEvent follows a task's change (a todo.updated or todo.deleted
// event, from whichever project) on the cards that show it.
export function chatTaskEvent(ev) {
  for (const card of taskCards) {
    if (!card.el.isConnected) { taskCards.delete(card); continue; }
    if (card.task.id !== ev.todo_id) continue;
    const s = ev.type === "todo.deleted" ? "deleted" : ev.data?.status;
    if (s && s !== card.status) renderTaskCard(card, s);
  }
}

// An action note in the chat: "You queued *Fix login*."
const actionNote = (m) => h("div", { class: "chat-note", role: "note" }, h("span", { class: "md", innerHTML: renderMarkdown(m.content || "") }));

// The row under a chat answer (general, project and goal chats).
export const chatActions = (answer) => h("div", { class: "msg-actions" },
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
    else text.textContent = "Chat needs a runner of yours: use Runners → Add a runner";
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
  actions: chatActions,
  cwd: $("#chat-cwd"),
  resume: true,
  onState: paneChatState("general", () => chat),
});

// The server saves every turn, so the conversation survives restarts. A
// question it never got to answer is asked again, unless another window's
// turn is still running.
export async function loadChat() {
  const { data: saved, res } = await request("GET", "/api/chat");
  if (!saved.length || chat.history.length || chat.busy) return;
  chat.reset(saved);
  if (!res.headers.get("X-Chat-Busy")) chat.resumeLast();
}
loadChat().catch((e) => toast(`Couldn't load chat history: ${e.message}`, true));

// A conversation about the open todo, run on the todo's computer. Claude sees
// the todo's instructions, so a draft can be talked into a better prompt.
const EDITABLE = ["draft", "queued", "failed"];

// The task chat's switch: Ask Claude (the side chat, which the agent doesn't
// see) or Rework (to the agent, which continues in its worktree). Rework is
// for a task in review with a worktree; opening another task goes back to Ask.
const TODO_MODES = {
  chat: { text: "Ask", label: "Ask Claude in the side chat", title: "Ask Claude in the side chat: the agent doesn't see it",
    placeholder: "Ask Claude about this task", inputLabel: "Ask Claude about this task in the side chat; the agent doesn't see it",
    // How the button reads once Claude has answered in the side chat (see chatSendButton).
    reply: { text: "Reply", label: "Reply to Claude in the side chat", title: "Reply to Claude in the side chat: the agent doesn't see it" } },
  rework: { text: "Rework", label: "Rework: send this to the agent", title: "Rework: the agent continues in its worktree with this",
    placeholder: "Tell the agent what to change", inputLabel: "Rework notes for the agent, which continues in its worktree", send: sendRework },
};
let todoMode = "chat", todoModeFor = null;
async function sendRework(q, files) {
  const t = state.todos.get(state.detail);
  if (!canRework(t)) { setTodoChatMode("chat"); return false; }
  // Nothing written: the Rework sheet, where notes and pictures can be added.
  if (!q && !files.length) return reworkTodo(t);
  await api("POST", `/api/todos/${t.id}/rework`, { feedback: q, attachments: files.map((a) => a.id) });
  toast("Sent for rework");
  return true;
}
export function setTodoChatMode(mode) {
  todoMode = mode === "rework" && canRework(state.todos.get(state.detail)) ? "rework" : "chat";
  for (const b of document.querySelectorAll("#d-chat-mode button")) b.setAttribute("aria-checked", b.dataset.mode === todoMode);
  $("#d-chat-form").classList.toggle("rework-mode", todoMode === "rework");
  todoChat.modeChanged();
}
// renderTodoChatMode shows the switch for the open task t.
export function renderTodoChatMode(t) {
  const rework = $("#d-chat-mode button[data-mode=rework]");
  rework.disabled = !canRework(t);
  rework.title = rework.disabled ? "Rework is for a task in review that has a worktree" : TODO_MODES.rework.title;
  setTodoChatMode(todoModeFor === t.id ? todoMode : "chat");
  todoModeFor = t.id;
}
$("#d-chat-mode").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-mode]");
  if (!b || b.disabled) return;
  setTodoChatMode(b.dataset.mode);
  todoChat.input.focus();
});
// Arrow keys move between the two, as in any radio group.
$("#d-chat-mode").addEventListener("keydown", (e) => {
  if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(e.key)) return;
  const next = [...e.currentTarget.querySelectorAll("button:not(:disabled)")].find((b) => b.dataset.mode !== todoMode);
  if (!next) return;
  e.preventDefault();
  setTodoChatMode(next.dataset.mode);
  next.focus();
});

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
  mode: () => TODO_MODES[todoMode],
  // A turn left streaming when its todo was closed is saved when it finishes.
  saved: ({ todo }) => { if (todo === state.detail && !todoChat.busy) loadTodoChat().catch(fail); },
  actions: (answer) => {
    const t = state.todos.get(state.detail);
    return h("div", { class: "msg-actions" },
      t && EDITABLE.includes(t.status) ? ib({ class: "link", onclick: attempt(async () => {
        if (!await ask({ title: "Replace the instructions?", body: "This answer becomes the task's instructions for Claude.", ok: "Replace" })) return;
        if (!await confirmRunAs(t, { prompt: answer })) return;
        await api("PATCH", `/api/todos/${t.id}`, { prompt: answer });
        toast("Instructions updated");
      }) }, "draft", "Use as instructions…") : null,
      canRework(t) ? ib({ class: "link", onclick: attempt(() => reworkTodo(t, answer)) }, "redo", reworkLabel(t)) : null,
      ib({ class: "link", onclick: () => copy(answer, "Copied") }, "copy", "Copy"));
  },
});
