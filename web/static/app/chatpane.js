import { $, $$, h, ib, ico } from "./dom.js";
import * as store from "./store.js";
import { desktop } from "./viewport.js";
import { api, on } from "./api.js";
import { attempt, copy, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { chatPage, renderNavActive } from "./nav.js";
import { baseBranch, projectKey } from "./events.js";
import { grouped, projectDot, projectIcon, projectWhere, sharedMark } from "./projects.js";
import { chat, chatActions, chatDismissed, chatUnread, makeChat, paneChatState, saveChatUnread, todoChat } from "./chat.js";
import { addWorkerEvent, worker } from "./timeline.js";
import { fitTerm } from "./terminal.js";
import { fitTryTerm } from "./try.js";
import { fuzzyScore } from "./palette.js";

// ------------------------------------------------------------------ chat pane
// One pane on the right holds every chat but the task chats: General, the
// selected project's ("This project") and project chats kept as tabs. Each chat
// has its own controller, so a reply keeps streaming while another tab is shown
// or the pane is closed; the pane only switches which one is visible.
// Overlay (default) floats over the content; pinned takes a grid column, but
// only on wide screens. On mobile it is a full-screen sheet.
const wide = matchMedia("(min-width: 1280px)");
const CHAT_MIN_W = 280;
export const pane = (() => {
  // projectTabs: the chats added with + in each project, by projectKey.
  // goalTabs: the goals' coordinator chats opened with Talk to coordinator ({key, id, pk, title}).
  const base = { open: false, pinned: false, width: null, tab: null, projectTabs: {}, goalTabs: [] };
  const goalTabList = (v) => (Array.isArray(v) ? v : []).filter((t) => t && typeof t.id === "string" && typeof t.pk === "string" && t.key === `goal:${t.pk}/${t.id}`)
    .map((t) => ({ key: t.key, id: t.id, pk: t.pk, title: String(t.title || "Goal") }));
  // Only well-formed tab lists survive: stored data may be stale or from an older version.
  const tabLists = (v) => Object.fromEntries(Object.entries(v && typeof v === "object" && !Array.isArray(v) ? v : {})
    .map(([k, list]) => [k, Array.isArray(list) ? [...new Set(list.filter((t) => typeof t === "string" && t && t !== k))] : []])
    .filter(([, list]) => list.length));
  try {
    const s = store.getJSON("dash.chatPane");
    if (s && typeof s === "object") {
      // The old shared tab list ("tabs") is dropped, not carried over.
      const { tabs, ...rest } = s;
      return { ...base, ...rest, tab: typeof s.tab === "string" ? s.tab : null, projectTabs: tabLists(s.projectTabs), goalTabs: goalTabList(s.goalTabs) };
    }
  } catch {}
  // Carry over the board chat's settings: it pushed the board aside, like pinned.
  const old = store.get("dash.boardChat");
  return { ...base, open: old === "1", pinned: old != null, width: Number(store.get("dash.boardChatW")) || null, tab: old === "1" ? "project" : null };
})();
function savePane() {
  store.setJSON("dash.chatPane", pane);
  ["dash.boardChat", "dash.chatTabs", "dash.boardChatW"].forEach(store.remove);
}
savePane();
export const paneOverlays = () => !pane.pinned || !wide.matches;

// Project chats, created on first use. key: projectKey.
const projectChats = new Map();
const projectByKey = (key) => state.projects?.find((p) => projectKey(p) === key) || null;
export function projectChatFor(p) {
  const key = projectKey(p);
  if (projectChats.has(key)) return projectChats.get(key);
  const proj = () => projectByKey(key) || p;
  const panel = $("#project-chat-tpl").content.firstElementChild.cloneNode(true);
  panel.dataset.chat = key;
  $("#chat-pane").append(panel);
  // remote: says Claude is answering in another of the user's windows or
  // devices (the chat is theirs alone, the same on each); its reply loads
  // when it's done.
  const remote = h("p", { class: "chat-remote", role: "status", hidden: true },
    h("span", { class: "act-spin", "aria-hidden": "true" }), " Claude is answering on another of your devices…");
  panel.querySelector("form").before(remote);
  const pc = { panel, loaded: false, remote, remoteBusy: false, dirty: false, seq: 0, proj };
  const paneState = paneChatState(key, () => pc.chat);
  let wasBusy = false;
  pc.chat = makeChat({
    log: panel.querySelector(".chat-log"), empty: panel.querySelector(".chat-empty"), form: panel.querySelector("form"),
    target: () => ({ prefix: proj().prefix || "", project: proj().id }),
    clear: () => api("DELETE", `/api/projects/${proj().id}/chat`, null, proj().prefix || ""),
    cwd: panel.querySelector(".chat-cwd"),
    // A turn left streaming when the chat was cleared is saved when it finishes.
    saved: () => { if (!pc.chat.busy) loadProjectChat(proj(), true).catch(fail); },
    // Sent from an out-of-date copy: the server's is the one to show.
    stale: () => loadProjectChat(proj(), true).catch(fail),
    actions: chatActions(false, key),
    // Changes saved from elsewhere during a turn here load once it ends.
    onState: () => {
      paneState();
      // The busy chat was (most likely) this turn: its end may come after the reply.
      if (wasBusy && !pc.chat.busy) pc.remoteBusy = false;
      wasBusy = pc.chat.busy;
      renderRemote(pc);
      if (!pc.chat.busy && pc.dirty) loadProjectChat(proj(), true).catch(() => {});
    },
    shell: true,
  });
  // Talking in a chat again wants its reply, even if its tab was closed mid-reply.
  pc.chat.input.form.addEventListener("submit", () => chatDismissed.delete(key));
  projectChats.set(key, pc);
  return pc;
}
const renderRemote = (pc) => { pc.remote.hidden = !pc.remoteBusy || pc.chat.busy; };
// sameChat reports whether a conversation shown (history) is the one saved.
const sameChat = (history, saved) => history.length === saved.length &&
  history.every((m, i) => m.role === saved[i].role && (m.content || "") === (saved[i].content || ""));
// The server keeps each user's own project chat, the same on each of their
// devices: it is loaded when first shown, and again (force) when it changed
// elsewhere, unless a turn is running here. A conversation that is already
// the one shown isn't drawn again, so a turn's tool calls stay.
async function loadProjectChat(p, force = false) {
  const pc = projectChatFor(p);
  if ((pc.loaded && !force) || pc.chat.busy) return;
  pc.loaded = true;
  pc.dirty = false;
  const seq = ++pc.seq;
  const res = await fetch(on(p.prefix || "", `/api/projects/${p.id}/chat`));
  const saved = await res.json().catch(() => null);
  if (!res.ok) {
    pc.loaded = false;
    throw new Error(saved?.error || res.statusText);
  }
  if (seq !== pc.seq) return; // a newer load is on its way
  pc.remoteBusy = !!res.headers.get("X-Chat-Busy");
  if (pc.chat.busy) pc.dirty = true; // a turn began meanwhile: load when it ends
  else if (!sameChat(pc.chat.history, saved || [])) pc.chat.reset(saved || []);
  renderRemote(pc);
}
// projectChatEvent: the user's own chat about ev's project (on computer m)
// was saved or cleared, or a turn of it began or ended (ev.data.busy), on
// this device or another. A chat loaded here follows it.
export function projectChatEvent(ev, m) {
  const pc = projectChats.get(`${m.name}/${ev.project_id}`);
  if (!pc?.loaded) return; // read when it's first shown
  if (typeof ev.data?.busy === "boolean") pc.remoteBusy = ev.data.busy;
  renderRemote(pc);
  if (ev.data?.busy) return; // nothing saved yet: the reply loads when it's done
  if (pc.chat.busy) pc.dirty = true;
  else loadProjectChat(pc.proj(), true).catch(() => {});
}

// ------------------------------------------------------------------ goal chats
// A goal's chat talks to its coordinator: the same conversation its
// wake-ups add to (docs/goals.md). Opened with Talk to coordinator, a tab
// each, kept until closed, whichever project is selected. It runs no
// commands and can't be cleared: the conversation is the coordinator's memory.
const goalChats = new Map(); // tab key → { panel, chat, loaded, tab }
const isGoalTab = (key) => typeof key === "string" && key.startsWith("goal:");
const goalTab = (key) => pane.goalTabs.find((t) => t.key === key) || null;
function goalChatFor(t) {
  if (goalChats.has(t.key)) return goalChats.get(t.key);
  const panel = $("#project-chat-tpl").content.firstElementChild.cloneNode(true);
  panel.dataset.chat = t.key;
  panel.querySelector(".empty-title").textContent = "Talk to the coordinator";
  panel.querySelector(".chat-empty p:last-child").textContent = "It sees the goal, its plan and its tasks, and remembers what it did. It can change the goal's tasks, never approve or merge them.";
  const q = panel.querySelector("textarea");
  q.placeholder = "Message the coordinator";
  q.setAttribute("aria-label", "Message the goal's coordinator");
  $("#chat-pane").append(panel);
  const gc = { panel, loaded: false, tab: t };
  const proj = () => projectByKey(gc.tab.pk);
  gc.chat = makeChat({
    log: panel.querySelector(".chat-log"), empty: panel.querySelector(".chat-empty"), form: panel.querySelector("form"),
    target: () => ({ prefix: proj()?.prefix || "", project: proj()?.id || "", goal: gc.tab.id }),
    clear: async () => { throw new Error("A goal's conversation can't be cleared: it is what its coordinator remembers."); },
    cwd: panel.querySelector(".chat-cwd"),
    saved: () => { if (!gc.chat.busy) { gc.loaded = false; loadGoalChat(gc).catch(fail); } },
    actions: (answer) => h("div", { class: "msg-actions" }, ib({ class: "link", onclick: () => copy(answer, "Copied") }, "copy", "Copy")),
    onState: paneChatState(t.key, () => gc.chat),
  });
  goalChats.set(t.key, gc);
  return gc;
}
async function loadGoalChat(gc) {
  if (gc.loaded || gc.chat.busy) return;
  const p = projectByKey(gc.tab.pk);
  if (!p) return;
  gc.loaded = true;
  const saved = await api("GET", `/api/goals/${gc.tab.id}/chat`, null, p.prefix || "");
  if (!gc.chat.busy) gc.chat.reset(saved);
}
// openGoalChat opens the chat with goal g's coordinator (g in project p).
export function openGoalChat(g, p) {
  const pk = projectKey(p), key = `goal:${pk}/${g.id}`;
  const t = goalTab(key);
  if (t) t.title = g.title;
  else pane.goalTabs = [...pane.goalTabs, { key, id: g.id, pk, title: g.title }];
  chatDismissed.delete(key);
  toggleChatPane(true, key);
}
function closeGoalTab(key) {
  pane.goalTabs = pane.goalTabs.filter((t) => t.key !== key);
  if (goalChats.get(key)?.chat.busy) chatDismissed.add(key);
  if (chatUnread.delete(key)) saveChatUnread();
  if (pane.tab === key) pane.tab = "project";
  savePane();
  renderChatPane();
}
// goalChatEvent: a goal changed (a wake-up may have added to its
// conversation); its chat, when loaded and idle, reads it again.
export function goalChatEvent(g, m) {
  for (const gc of goalChats.values()) {
    if (gc.tab.id !== g?.id || projectByKey(gc.tab.pk)?.machine !== m.name || gc.chat.busy) continue;
    if (g.title && gc.tab.title !== g.title) { gc.tab.title = g.title; savePane(); renderChatPane(); }
    if (!gc.loaded || g.running) continue; // read once its turn is over
    gc.loaded = false;
    if (viewingChat(gc.tab.key)) loadGoalChat(gc).catch(() => {});
  }
}

// Tabs are This project, General, then the chats added with + in the selected
// project (each project has its own), then other projects' chats with a reply
// in flight or unread, until it has been seen ("passing" tabs).
const addedTabs = () => pane.projectTabs[projectKey(state.project)] || [];
// Passing chats stay while they stream, are unread, or are the tab you're on.
const passingTabs = new Set();
let passingProject = null;
function keptTabs() {
  const cur = projectKey(state.project), added = addedTabs();
  // A seen reply's tab doesn't follow you into another project.
  const moved = passingProject !== cur;
  passingProject = cur;
  const busy = (k) => !!projectChats.get(k)?.chat.busy;
  for (const k of [...projectChats.keys(), ...chatUnread]) {
    if (k !== "general" && !isGoalTab(k) && k !== cur && !added.includes(k) && !chatDismissed.has(k) && (busy(k) || chatUnread.has(k))) passingTabs.add(k);
  }
  for (const k of passingTabs) {
    if (k === cur || added.includes(k) || chatDismissed.has(k) || (!busy(k) && !chatUnread.has(k) && (pane.tab !== k || moved))) passingTabs.delete(k);
  }
  return [...new Set([...added, ...passingTabs])].map(projectByKey).filter((p) => p && projectKey(p) !== cur);
}
function addChatTab(key) {
  const cur = projectKey(state.project);
  if (!cur || key === cur) return;
  const list = addedTabs();
  if (!list.includes(key)) pane.projectTabs[cur] = [...list, key];
  showChatTab(key);
}
function closeChatTab(key) {
  const cur = projectKey(state.project), list = addedTabs().filter((k) => k !== key);
  if (list.length) pane.projectTabs[cur] = list;
  else delete pane.projectTabs[cur];
  passingTabs.delete(key);
  // Closed mid-reply: the reply doesn't reopen it.
  if (projectChats.get(key)?.chat.busy) chatDismissed.add(key);
  if (chatUnread.delete(key)) saveChatUnread();
  if (pane.tab === key) pane.tab = "project";
  savePane();
  renderChatPane();
}
// The tab shown: "general", "project", or a kept project chat's key.
function paneTab() {
  const t = pane.tab;
  if (t === "general") return t;
  if (isGoalTab(t) && goalTab(t)) return t;
  if (t === "project" || (t && t === projectKey(state.project))) return "project";
  if (t && keptTabs().some((p) => projectKey(p) === t)) return t;
  // Unset, or a kept chat whose project is gone (or isn't loaded yet).
  return state.project ? "project" : "general";
}
const tabProject = (tab) => (tab === "general" || isGoalTab(tab) ? null : tab === "project" ? state.project : projectByKey(tab));
// The controller of the visible tab, if it has one.
export function activeChat() {
  const tab = paneTab();
  if (tab === "general") return chat;
  if (isGoalTab(tab)) return goalChatFor(goalTab(tab)).chat;
  const p = tabProject(tab);
  return p ? projectChatFor(p).chat : null;
}

export function renderChatPane() {
  const app = $(".app");
  app.classList.toggle("chat-open", pane.open);
  app.classList.toggle("chat-pinned", pane.pinned);
  app.classList.toggle("chat-overlay", !pane.pinned);
  if (pane.width) app.style.setProperty("--chat-w", `${Math.max(CHAT_MIN_W, Math.round(pane.width))}px`);
  else app.style.removeProperty("--chat-w");
  $("#chat-pane").hidden = !pane.open;
  $("#pc-open").setAttribute("aria-pressed", String(pane.open));
  renderNavActive();
  const pin = $("#pc-pin");
  pin.setAttribute("aria-pressed", String(pane.pinned));
  pin.title = pane.pinned ? "Unpin the chat (overlay)" : "Pin the chat beside the content";
  pin.setAttribute("aria-label", pin.title);

  const tab = paneTab();
  const cur = tabProject(tab);
  const tabBtn = (id, label, kids, title = label) => h("button", {
    type: "button", role: "tab", class: "chat-pane-tab-btn", "aria-selected": String(tab === id), title,
    "data-chat": tabChatKey(id) || "", "data-title": title,
    onclick: () => showChatTab(id),
  }, ...kids, h("span", { class: "chat-tab-name" }, label), h("span", { class: "chat-tab-unread", "aria-hidden": "true" }));
  // "This project" leads: General is rarely used.
  $("#chat-tabs").replaceChildren(
    h("div", { class: "chat-pane-tab" }, tabBtn("project", "This project", [state.project ? projectIcon(state.project) : ico("repo")],
      state.project ? `${state.project.name} (follows the project picker)` : "Follows the project picker")),
    h("div", { class: "chat-pane-tab" }, tabBtn("general", "General", [ico("spark")], "General chat, in your home directory")),
    ...keptTabs().map((p) => h("div", { class: "chat-pane-tab" },
      tabBtn(projectKey(p), p.name, [projectIcon(p)], grouped() ? `${p.name} · ${projectWhere(p)}` : p.name),
      ib({ type: "button", class: "icon-btn chat-tab-close", "aria-label": `Close the ${p.name} chat`, title: "Close this tab", onclick: () => closeChatTab(projectKey(p)) }, "x"))),
    ...pane.goalTabs.map((t) => h("div", { class: "chat-pane-tab" },
      tabBtn(t.key, t.title, [ico("target")], `Coordinator of the goal ${t.title}`),
      ib({ type: "button", class: "icon-btn chat-tab-close", "aria-label": `Close the chat with the coordinator of ${t.title}`, title: "Close this tab", onclick: () => closeGoalTab(t.key) }, "x"))),
  );
  // + adds another project's chat as a tab, in this project only.
  $("#chat-tab-add").hidden = !state.project;
  if (!state.project) setChatTabPicker(false);

  // Show the tab's panel; the others (and their streams) stay as they are.
  const show = (el, on) => {
    if (on && el.hidden && pane.open) requestAnimationFrame(() => { const log = el.querySelector(".chat-log"); if (log) log.scrollTop = log.scrollHeight; });
    el.hidden = !on;
  };
  show($("#chat-general"), tab === "general");
  show($("#chat-noproject"), tab === "project" && !state.project);
  if (cur && pane.open) projectChatFor(cur);
  for (const [key, pc] of projectChats) show(pc.panel, !!cur && key === projectKey(cur));
  const gt = isGoalTab(tab) ? goalTab(tab) : null;
  if (gt && pane.open) goalChatFor(gt);
  for (const [key, gc] of goalChats) show(gc.panel, key === tab);
  if (gt && pane.open) loadGoalChat(goalChats.get(tab)).catch((e) => toast(`Couldn't load the coordinator's conversation: ${e.message}`, true));
  $("#pc-clear").disabled = !activeChat() || !!gt;
  renderTryBack();
  if (cur && pane.open) loadProjectChat(cur).catch((e) => toast(`Couldn't load the project chat: ${e.message}`, true));
  markChatSeen();
}

// The + tab's picker: every project but this one and those with a tab already,
// filtered by what you type.
function chatTabCandidates() {
  const have = new Set([projectKey(state.project), ...keptTabs().map(projectKey)]);
  const seen = new Set();
  return (state.projects || []).filter((p) => {
    const k = projectKey(p);
    if (have.has(k) || seen.has(k)) return false;
    seen.add(k);
    return true;
  });
}
function renderChatTabOptions() {
  const q = $("#chat-tab-search").value.trim();
  let list = chatTabCandidates();
  if (q) {
    list = list.map((p) => {
      const s = fuzzyScore(p.name, q);
      // Not the path: its long temp or home directories match almost any letters in order.
      return { p, s: s >= 0 ? s * 2 : fuzzyScore(`${p.name} ${p.machine} ${projectWhere(p)}`, q) };
    }).filter((x) => x.s >= 0).sort((a, b) => b.s - a.s).map((x) => x.p);
  }
  $("#chat-tab-options").replaceChildren(...(list.length ? list.map((p) => h("button", {
    type: "button", role: "menuitem", title: p.path,
    onclick: () => { setChatTabPicker(false); addChatTab(projectKey(p)); },
  }, h("span", { class: "project-name" }, projectDot(p), p.name, sharedMark(p)), h("span", { class: "project-branch" }, grouped() ? projectWhere(p) : baseBranch(p))))
    : [h("div", { class: "machine-empty" }, q ? "No matching projects" : "No other projects")]));
}
function setChatTabPicker(open) {
  const el = $("#chat-tab-picker");
  if (el.hidden === !open) return;
  el.hidden = !open;
  $("#chat-tab-add")?.setAttribute("aria-expanded", String(open));
  if (!open) return;
  $("#chat-tab-search").value = "";
  renderChatTabOptions();
  $("#chat-tab-search").focus();
}
$("#chat-tab-search").addEventListener("input", renderChatTabOptions);
$("#chat-tab-add").addEventListener("click", () => setChatTabPicker($("#chat-tab-picker").hidden));
document.addEventListener("click", (e) => { if (!$("#chat-tab-picker").hidden && !e.target.closest("#chat-tab-picker, #chat-tab-add")) setChatTabPicker(false); });
$("#chat-tab-picker").addEventListener("keydown", (e) => {
  const btns = $$("#chat-tab-options button");
  const i = btns.indexOf(document.activeElement);
  const inSearch = e.target === $("#chat-tab-search");
  if (e.key === "Escape") { setChatTabPicker(false); $("#chat-tab-add")?.focus(); }
  else if (e.key === "Enter" && inSearch) btns[0]?.click();
  else if (e.key === "ArrowDown" || (!inSearch && e.key === "j")) (btns[i + 1] || btns[0])?.focus();
  else if (e.key === "ArrowUp" || (!inSearch && e.key === "k")) (i <= 0 ? $("#chat-tab-search") : btns[i - 1]).focus();
  else return;
  e.preventDefault(); e.stopPropagation();
});

// The chat key of a tab: "general", or the projectKey of its project.
function tabChatKey(tab) {
  if (tab === "general") return "general";
  if (isGoalTab(tab)) return tab;
  const p = tabProject(tab);
  return p ? projectKey(p) : null;
}
// Whether you're looking at that chat right now: its tab is shown in the open
// pane, and the page isn't in the background.
export function viewingChat(key) {
  return pane.open && !document.hidden && tabChatKey(paneTab()) === key;
}
// The chat on screen has been seen: it's no longer unread.
function markChatSeen() {
  const key = tabChatKey(paneTab());
  if (key && viewingChat(key) && chatUnread.delete(key)) saveChatUnread();
  renderChatDots();
}
document.addEventListener("visibilitychange", () => { if (!document.hidden) markChatSeen(); });
// Another window read (or left) a reply: follow its unread chats.
window.addEventListener("storage", (e) => {
  if (e.key !== "dash.chatUnread") return;
  chatUnread.clear();
  try { for (const k of JSON.parse(e.newValue) || []) chatUnread.add(k); } catch {}
  renderChatPane(); // an unread chat shows as a tab; marks the one on screen seen
});
// The green dots on the unread chats' tabs, and the Chat buttons' badge.
export function renderChatDots() {
  for (const b of $$("#chat-tabs .chat-pane-tab-btn")) {
    const on = !!b.dataset.chat && chatUnread.has(b.dataset.chat);
    b.classList.toggle("unread", on);
    b.title = on ? `${b.dataset.title} · new reply` : b.dataset.title;
  }
  renderChatBadge();
}

// On phones the chat is a page over the Try view: its Terminal button leads back.
export function renderTryBack() {
  const b = $("#chat-try-back");
  if (b) b.hidden = !(state.view === "try" && !desktop.matches);
  // The Try view's Terminal/Chat tabs say which of the two is showing.
  const chatOn = chatPage(); // not pane: applyView calls this at startup before pane exists
  for (const [id, on] of [["#try-tab-term", !chatOn], ["#try-tab-chat", chatOn]]) {
    const t = $(id);
    if (!t) continue;
    t.setAttribute("aria-selected", String(on));
    t.tabIndex = on ? 0 : -1; // one tab stop for the tablist; arrows move between tabs
  }
}

function showChatTab(tab) {
  pane.tab = tab;
  savePane();
  renderChatPane();
  if (desktop.matches) activeChat()?.input.focus();
}
// open: true/false, or toggle. tab: the tab to show, or the last one.
export function toggleChatPane(open = !pane.open, tab = null) {
  pane.open = open;
  if (tab) pane.tab = tab;
  if (!open) setChatTabPicker(false);
  savePane();
  renderChatPane();
  // On mobile the chat is a page: opening it doesn't pop the keyboard up over the nav.
  if (open) { if (desktop.matches) activeChat()?.input.focus(); }
  else {
    if ($("#chat-pane").contains(document.activeElement)) document.activeElement.blur();
    // The mobile chat page hid the shell's view; fit its terminal again.
    if (!desktop.matches && state.view === "run") requestAnimationFrame(fitTerm);
    if (!desktop.matches && state.view === "try") requestAnimationFrame(fitTryTerm);
  }
}
export function togglePin() {
  pane.pinned = !pane.pinned;
  savePane();
  renderChatPane();
  if (pane.pinned && !wide.matches) toast("Pinned. The chat sits beside the content on wider windows.");
}
wide.addEventListener("change", renderChatPane);
$("#pc-open").addEventListener("click", () => toggleChatPane());
// The mobile bottom nav's Chat tab: opens the chat page on the current project's chat.
// Like any other tab, tapping it while it's shown keeps it.
$("#nav-chat").addEventListener("click", () => { if (!pane.open) toggleChatPane(true, state.project ? "project" : "general"); });
// The Chat buttons' badge (the bottom nav's and the top bar's): a pane chat waits
// on a permission request ("!"), is streaming an answer (a breathing dot), or has
// a reply you haven't read (a green dot), in that order.
export function renderChatBadge() {
  const chats = [chat, ...[...projectChats.values(), ...goalChats.values()].map((pc) => pc.chat)];
  const waiting = chats.some((c) => c.waiting), busy = chats.some((c) => c.busy);
  // Only chats that still have a tab count: a closed tab's reply can't be read.
  const tabs = new Set(["general", state.project && projectKey(state.project), ...keptTabs().map(projectKey), ...pane.goalTabs.map((t) => t.key)]);
  const unread = [...chatUnread].some((k) => tabs.has(k));
  const kind = waiting ? "perm" : busy ? "busy" : unread ? "unread" : "";
  const says = { perm: "Claude needs approval", busy: "Claude is answering", unread: "new reply" }[kind];
  for (const b of $$(".chat-badge")) {
    b.hidden = !kind;
    b.classList.toggle("perm", waiting);
    b.classList.toggle("unread", kind === "unread");
    b.textContent = waiting ? "!" : "";
    b.title = kind === "unread" ? "New reply" : says || "";
  }
  for (const btn of [$("#nav-chat"), $("#pc-open")]) {
    btn.setAttribute("aria-label", says ? `Chat: ${says}` : "Chat");
    btn.title = says ? `Chat: ${says} (gc)` : "Chat (gc)";
  }
}
$("#pc-close").addEventListener("click", () => toggleChatPane(false));
$("#pc-pin").addEventListener("click", togglePin);

$("#pc-clear").addEventListener("click", attempt(async () => {
  const c = activeChat();
  if (c?.history.length) await c.clear();
}));

// Desktop: drag the pane's left edge to resize it, overlaid or pinned.
function setChatWidth(w, save = true) {
  pane.width = w == null ? null : Math.max(CHAT_MIN_W, Math.round(w));
  if (pane.width) $(".app").style.setProperty("--chat-w", `${pane.width}px`);
  else $(".app").style.removeProperty("--chat-w");
  if (save) savePane();
}
{
  const handle = $("#pc-resize"), panel = $("#chat-pane");
  handle.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    handle.setPointerCapture(e.pointerId);
    handle.classList.add("dragging");
    const right = panel.getBoundingClientRect().right;
    const move = (ev) => setChatWidth(right - ev.clientX, false);
    const up = () => {
      handle.removeEventListener("pointermove", move);
      handle.classList.remove("dragging");
      setChatWidth(panel.getBoundingClientRect().width);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up, { once: true });
    handle.addEventListener("pointercancel", up, { once: true });
  });
  handle.addEventListener("dblclick", () => setChatWidth(null));
  handle.addEventListener("keydown", (e) => {
    const step = e.shiftKey ? 64 : 16;
    const d = e.key === "ArrowLeft" ? step : e.key === "ArrowRight" ? -step : 0;
    if (!d) return;
    e.preventDefault();
    e.stopPropagation();
    setChatWidth(panel.getBoundingClientRect().width + d);
  });
}
renderChatPane();

let todoChatId = null;
export async function loadTodoChat() {
  const id = state.detail;
  // Reopening the todo whose reply is still streaming keeps it on screen.
  // The events file only grows, so the worker's events from while it was closed are those past ours.
  if (todoChatId === id && todoChat.busy) {
    const events = await api("GET", `/api/todos/${id}/events`);
    if (state.detail === id && worker.id === id) events.slice(worker.events.length).forEach((e) => addWorkerEvent(id, e));
    return todoChat.toEnd();
  }
  todoChatId = id;
  worker.id = null;
  worker.events = worker.live = [];
  todoChat.reset();
  const [saved, events] = await Promise.all([api("GET", `/api/todos/${id}/chat`), api("GET", `/api/todos/${id}/events`)]);
  if (state.detail !== id) return;
  // Events that streamed in while loading, unless the load has them already.
  const key = (e) => `${e.at}|${e.kind}|${e.id || ""}`;
  const seen = new Set(events.map(key));
  worker.id = id;
  worker.events = [...events, ...worker.live.filter((e) => !seen.has(key(e)))];
  worker.live = [];
  if (!todoChat.history.length && !todoChat.busy) todoChat.reset(saved);
}
