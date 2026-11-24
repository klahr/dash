// Claude for planners, on planning.html: a planner's own runners (the
// Runners sheet: pairing one, renaming and removing it), the general chat
// (the Chat view) and what Write a feature brief needs. A planner's runner
// runs only their chat and their feature brief interviews, with their own
// Claude login (internal/api/planner.go); it never gets the org's work.
// Without one online, where Claude would be the page shows what's needed
// instead (claudeNeeded): "Install a runner to use Claude here", which opens
// the setup steps.
import { $, docsLink, entering, fill, h, ib, ico, dateTimeText } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, fail, railOf, toast } from "./dialogs.js";
import { installSteps, runnerNameInput } from "./install.js";
import { wsURL } from "./viewport.js";
import { renderMarkdown } from "../editor.js";
import { chatCloseReason, chatPermCard, permDecided, placeToolCall, sendStopButton, toolCall, toolResult, toolSettled } from "./chatparts.js";

const call = (method, path, body) => api(method, path, body, "");
// onPage: this is planning.html. The app (app.js) imports this module too,
// as it does every module, but has its own chat and Runners sheet: there it
// wires up nothing.
const onPage = !!document.querySelector("#pl-chat-form");
const SHEET = "runners-sheet";

// cl: the planner's runners (GET /api/runners: their own only), the hub's
// address for the install command, the pairing in progress, and who to tell
// when whether Claude can run changes.
const cl = { runners: null, hub: location.origin, adding: null, listeners: [], poll: 0 };

const online = () => (cl.runners || []).find((r) => r.online) || null;

// ready reports whether Claude can run for the planner: a runner of theirs online.
export const claudeReady = () => !!online();

// onClaudeChange calls fn whenever claudeReady may have changed.
export const onClaudeChange = (fn) => cl.listeners.push(fn);

// claudeNeeded is what shows where Claude would (a card's Write a feature
// brief and Ask in chat, the chat): null when it can run, else a link to
// what it needs, a new element each call.
export function claudeNeeded() {
  if (online()) return null;
  const any = cl.runners?.[0];
  const text = any ? `Start your runner ${any.name} to use Claude here` : "Install a runner to use Claude here";
  return h("button", { type: "button", class: "link inline claude-needed", title: any ? "Your runner is offline" : "How to install a runner of your own",
    onclick: attempt(() => openRunners(any ? "overview" : "add")) }, ico("spark"), text);
}

// loadClaude reads the planner's runners and tells the listeners.
export async function loadClaude() {
  const out = await call("GET", "/api/runners");
  if (out.hub) cl.hub = out.hub;
  const was = claudeReady(), had = JSON.stringify(cl.runners);
  cl.runners = out.runners || [];
  if (cl.adding && cl.runners.some((r) => !cl.adding.known.has(r.id))) {
    const r = cl.runners.find((x) => !cl.adding.known.has(x.id));
    cl.adding = null;
    toast(`${r.name} is connected: Claude is ready here`, { tone: "ok" });
    if ($(`#${SHEET}`).open) railOf(SHEET)?.show("overview");
  }
  if (JSON.stringify(cl.runners) !== had) renderRunners();
  if (claudeReady() !== was || had === "null") for (const fn of cl.listeners) fn();
}

// keepWatching refreshes the runners now and then: often while the sheet
// waits for a new runner, else every half minute while the page shows.
function keepWatching() {
  clearTimeout(cl.poll);
  const ms = cl.adding && $(`#${SHEET}`).open ? 3000 : 30000;
  cl.poll = setTimeout(async () => {
    if (!document.hidden) await loadClaude().catch(() => {});
    keepWatching();
  }, ms);
}
if (onPage) document.addEventListener("visibilitychange", () => { if (!document.hidden) loadClaude().catch(() => {}); });

// ---- the Runners sheet

// openRunners opens the Runners sheet on section (overview, or add: the
// install steps with a new code).
export async function openRunners(section = "overview") {
  const d = $(`#${SHEET}`);
  if (!d.open) d.showModal();
  await loadClaude();
  railOf(SHEET)?.show(section);
  if (section === "add") await startAdd();
}

function renderRunners() {
  const list = $("#pl-runners-list");
  if (!list) return;
  const rs = cl.runners || [];
  $("#pl-runners-none").hidden = rs.length > 0;
  fill(list, rs.map((r) => h("li", { class: "item-row" },
    h("span", { class: `dot runner-dot${r.online ? " on" : ""}`, "aria-hidden": "true" }),
    h("span", { class: "runner-name" }, r.name),
    h("span", { class: "item-sub hint" }, r.online ? "Online: your chat and feature briefs run on it" : r.last_seen ? `Offline since ${dateTimeText(r.last_seen)}` : "Offline"),
    ib({ type: "button", class: "btn small", onclick: attempt(() => rename(r)) }, "edit", "Rename"),
    ib({ type: "button", class: "btn small danger", onclick: attempt(() => remove(r)) }, "trash", "Remove"))));
}

async function rename(r) {
  const name = await ask({ title: `Rename ${r.name}`, body: "Lowercase letters, digits and dashes.", input: true, value: r.name, ok: "Rename" });
  if (name == null || !name.trim() || name.trim() === r.name) return;
  await call("PATCH", `/api/runners/${encodeURIComponent(r.id)}`, { name: name.trim().toLowerCase() });
  await loadClaude();
  toast("Renamed", { tone: "ok" });
}

async function remove(r) {
  if (!await ask({ title: `Remove ${r.name}?`, body: "It disconnects now and its token stops working. To use it again, add it again with a new code.", ok: "Remove", danger: true })) return;
  await call("DELETE", `/api/runners/${encodeURIComponent(r.id)}`);
  await loadClaude();
  toast(`${r.name} removed`);
}

// startAdd gets a one-time code and shows how to install a runner with it,
// the same pairing members use (app/runners.js); the sheet notices the
// runner that registers with it.
async function startAdd() {
  const pane = $("#pl-runner-add");
  if (cl.adding && cl.adding.expires > Date.now() + 60000) { keepWatching(); return; }
  const { code, expires, hub } = await call("POST", "/api/runners/codes");
  if (hub) cl.hub = hub;
  cl.adding = { known: new Set((cl.runners || []).map((r) => r.id)), expires: new Date(expires).getTime() };
  const mins = Math.round((cl.adding.expires - Date.now()) / 60000);
  const name = runnerNameInput();
  const cmds = h("div", {});
  const render = () => fill(cmds, installSteps(cl.hub, code, { name: name.value.trim().toLowerCase() }));
  name.addEventListener("input", render);
  render();
  fill(pane,
    h("ol", { class: "steps" },
      h("li", {}, h("strong", {}, "Sign in to Claude Code on your computer. "), "Install it and run ", h("code", {}, "claude"), " once to sign in with your own Claude account."),
      h("li", {}, h("strong", {}, "Install the runner. "), "Paste the command below. It installs Dash and starts the runner in the background, with no admin rights.", cmds),
      h("li", {}, h("strong", {}, "Come back here. "), "Once it connects, the chat and Write a feature brief use it.")),
    h("details", { class: "runner-add-opts" },
      h("summary", {}, "Name (optional)"),
      h("label", { class: "field" }, h("span", {}, "Name"), name)),
    h("p", { class: "runner-add-status", role: "status" }, "Waiting for the runner to connect…"),
    h("p", { class: "hint" }, `The code works once, for ${mins} minutes. Your runner runs only your chat and feature briefs, never tasks or other project work. `,
      docsLink("several-computers.md#a-planners-runner")));
  keepWatching();
}

if (onPage) {
  $(`#${SHEET}`).addEventListener("click", (e) => {
    if (e.target.closest('[data-rail="add"], #pl-runners-add')) attempt(startAdd)();
  });
  $("#pl-runners-add").addEventListener("click", () => railOf(SHEET)?.show("add"));
}

// ---- the general chat (the Chat view)

// ch: the conversation as saved (GET /api/chat), the socket, and the reply
// streaming now (cur).
const ch = { history: [], ws: null, cur: null, loaded: false };

const log = () => $("#pl-chat-log");

// loadChat reads the saved conversation and shows it.
export async function loadChat() {
  ch.history = (await call("GET", "/api/chat")) || [];
  ch.loaded = true;
  renderChat();
}

const userMsg = (m) => h("div", { class: "msg user" }, h("div", { class: "md", innerHTML: renderMarkdown(m.content || "") }));
const assistantMsg = (m) => h("div", { class: "msg assistant" }, h("div", { class: "reply", innerHTML: renderMarkdown(m.content || "") }));

// renderChat shows the conversation, or what Claude needs.
export function renderChat() {
  const needed = claudeNeeded();
  const bar = $("#pl-chat-runner");
  const r = online();
  bar.hidden = !r;
  if (r) fill($("#pl-chat-runner-text"), `On your runner ${r.name}`);
  $("#pl-chat-needed").hidden = !needed;
  if (needed) fill($("#pl-chat-needed-link"), needed);
  $("#pl-chat-form").hidden = !!needed;
  if (ch.cur) return; // a reply is streaming into the log
  const msgs = ch.history.filter((m) => m.role === "user" || m.role === "assistant");
  $("#pl-chat-empty").hidden = msgs.length > 0;
  fill(log(), $("#pl-chat-empty"), msgs.map((m) => (m.role === "user" ? userMsg(m) : assistantMsg(m))));
  toEnd();
}

const toEnd = () => { const l = log(); l.scrollTop = l.scrollHeight; };

// socket is the chat's WebSocket, opened when first needed.
function socket() {
  if (ch.ws && ch.ws.readyState <= 1) return ch.ws;
  const ws = new WebSocket(wsURL("/ws/chat"));
  ws.onmessage = (e) => delta(JSON.parse(e.data));
  ws.onclose = (e) => {
    if (ch.ws === ws) ch.ws = null;
    // settle takes the unanswered question back out of the conversation and
    // puts it in the composer: after a refusal (too long to send), to send
    // once the chat is cleared.
    if (ch.cur) settle(chatCloseReason(e) || "The connection to Dash was lost. Send your message again.");
  };
  ch.ws = ws;
  return ws;
}

function sendWS(msg) {
  const ws = socket();
  const go = () => ws.send(JSON.stringify(msg));
  ws.readyState === 1 ? go() : ws.addEventListener("open", go, { once: true });
}

// send asks Claude q, after the saved conversation.
function send(q) {
  ch.history.push({ role: "user", content: q });
  $("#pl-chat-empty").hidden = true;
  log().append(entering(userMsg({ content: q })));
  const thinking = h("div", { class: "thinking md" });
  const reply = h("div", { class: "reply" });
  const el = entering(h("div", { class: "msg assistant pending" }, thinking, reply));
  log().append(el);
  ch.cur = { el, thinking, reply, text: "", q, tools: new Map() };
  busyButton();
  toEnd();
  sendWS({ type: "send", messages: ch.history });
}

// delta shows one piece of the reply (see internal/api/ws.go chatWS).
function delta(d) {
  const cur = ch.cur;
  if (!cur) return;
  switch (d.kind) {
    case "thinking":
      cur.thinking.textContent += d.text;
      break;
    case "text":
      cur.text += d.text;
      cur.reply.innerHTML = renderMarkdown(cur.text);
      break;
    case "tool_use": {
      const t = toolCall(d.tool, d.input);
      cur.tools.set(d.id, t);
      const el = placeToolCall(t, cur.reply);
      if (el) entering(el);
      break;
    }
    case "tool_result": {
      const t = cur.tools.get(d.id);
      if (t) toolResult(t, d);
      break;
    }
    case "permission":
      cur.reply.before(entering(chatPermCard(d, (decision, answers) => decide(d.id, decision, answers))));
      break;
    case "permission_decided":
      permDecided(cur.el, d);
      break;
    case "done":
      if (cur.text) ch.history.push({ role: "assistant", content: cur.text });
      settle("");
      break;
    case "error":
      settle(d.text || "Claude couldn't answer");
      break;
  }
  toEnd();
}

function decide(id, decision, answers) {
  sendWS({ type: "permission", id, decision, answers });
}

// settle ends the reply: done, or failed with why (the question is taken
// back out, as the server does, and put back in the composer).
function settle(why) {
  const cur = ch.cur;
  ch.cur = null;
  cur.el.classList.remove("pending");
  for (const t of cur.tools.values()) toolSettled(t);
  if (why) {
    if (!cur.text) {
      if (ch.history.at(-1)?.role === "user") ch.history.pop();
      const input = $("#pl-chat-form textarea");
      if (!input.value) input.value = cur.q;
    }
    toast(why, true);
    renderChat();
    loadClaude().catch(() => {}); // the runner may have gone
  }
  busyButton();
}

function stop() {
  if (!ch.cur) return;
  sendWS({ type: "cancel" });
  const cur = ch.cur;
  ch.cur = null;
  cur.el.classList.remove("pending");
  for (const t of cur.tools.values()) toolSettled(t);
  if (cur.text) ch.history.push({ role: "assistant", content: cur.text });
  else { ch.history.pop(); cur.el.remove(); }
  busyButton();
}

// busyButton shows the send button as Stop while a reply streams.
let sendLook = null;
function busyButton() {
  sendLook ||= sendStopButton($("#pl-chat-send"));
  sendLook(!!ch.cur);
}

// askAbout starts a message about card c in the chat, for Claude to read
// it with get_card; nothing is sent until the planner adds their question.
export function askAbout(c) {
  const input = $("#pl-chat-form textarea");
  const about = `About the planning card "${c.title}" (card ${c.id}): `;
  if (!input.value.includes(`(card ${c.id})`)) input.value = about + input.value;
  grow();
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
}

function grow() {
  const input = $("#pl-chat-form textarea");
  input.style.height = "auto";
  input.style.height = `${input.scrollHeight + input.offsetHeight - input.clientHeight}px`;
}

if (onPage) {
  const form = $("#pl-chat-form");
  const input = $("textarea", form);
  input.addEventListener("input", grow);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); form.requestSubmit(); }
  });
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const q = input.value.trim();
    if (ch.cur) { if (!q) stop(); return; }
    if (!q) return;
    if (/^\/(clear|new|reset)$/i.test(q)) { input.value = ""; grow(); clearChat().catch(fail); return; }
    input.value = "";
    grow();
    send(q);
  });
  $("#pl-chat-clear").addEventListener("click", attempt(async () => {
    if (!ch.history.length || !await ask({ title: "Clear the chat?", body: "The conversation is deleted.", ok: "Clear", danger: true })) return;
    await clearChat();
  }));
}

async function clearChat() {
  if (ch.cur) stop();
  await call("DELETE", "/api/chat");
  ch.history = [];
  renderChat();
}

// chatShown is called when the Chat view shows: it loads the conversation
// once, and puts the focus in the composer.
export async function chatShown() {
  if (!ch.loaded) await loadChat();
  else renderChat();
  if (claudeReady()) $("#pl-chat-form textarea").focus();
}

onClaudeChange(() => { if (ch.loaded) renderChat(); });
