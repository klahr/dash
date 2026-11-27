import { $, $$, h, ib, ico, resizeHandle } from "./dom.js";
import * as store from "./store.js";
import { chatBeside, desktop } from "./viewport.js";
import { api, request } from "./api.js";
import { attempt, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { chatPage, renderNavActive } from "./nav.js";
import { baseBranch, projectKey } from "./events.js";
import { grouped, projectDot, projectIcon, projectWhere, selectProject, sharedMark } from "./projects.js";
import { openDetail } from "./detail.js";
import { chat, chatActions, chatDismissed, chatUnread, makeChat, paneChatState, projectRunnerPickers, runnerPicker, saveChatUnread, todoChat } from "./chat.js";
import { addWorkerEvent, worker } from "./timeline.js";
import { fitTerm } from "./terminal.js";
import { fitTryTerm } from "./try.js";
import { fuzzyScore } from "./palette.js";
import { agoText } from "../time.js";

// ------------------------------------------------------------------ chat pane
// One pane on the right holds every chat but the task chats: General, the
// selected project's ("This project") and project chats kept as tabs. Each chat
// has its own controller, so a reply keeps streaming while another tab is shown
// or the pane is closed; the pane only switches which one is visible.
// Overlay (default) floats over the content; pinned takes a grid column, but
// only on wide screens. On a tablet it sits beside the content, above the
// bottom nav; on a phone it is a page of its own.
const wide = matchMedia("(min-width: 1280px)");
const CHAT_MIN_W = 280;
export const pane = (() => {
  // projectTabs: the chats added with + in each project, by projectKey.
  // goal: the one goal chat, opened with Talk to coordinator ({key, id, pk, title}): it
  // follows the goal open on Goals (followGoal).
  const base = { open: false, pinned: false, width: null, tab: null, projectTabs: {}, goal: null };
  const goalSlot = (t) => (t && typeof t.id === "string" && typeof t.pk === "string" && t.key === `goal:${t.pk}/${t.id}`
    ? { key: t.key, id: t.id, pk: t.pk, title: String(t.title || "Goal") } : null);
  // Only well-formed tab lists survive: stored data may be stale or from an older version.
  const tabLists = (v) => Object.fromEntries(Object.entries(v && typeof v === "object" && !Array.isArray(v) ? v : {})
    .map(([k, list]) => [k, Array.isArray(list) ? [...new Set(list.filter((t) => typeof t === "string" && t && t !== k))] : []])
    .filter(([, list]) => list.length));
  try {
    const s = store.getJSON("dash.chatPane");
    if (s && typeof s === "object") {
      // The old shared tab list ("tabs") and goal tabs ("goalTabs") are dropped, not carried over.
      const { tabs, goalTabs, ...rest } = s;
      // A goal's chat outlives a reload only pinned: no goal is open yet.
      const goal = s.pinned ? goalSlot(s.goal) : null;
      const tab = typeof s.tab === "string" && (!s.tab.startsWith("goal:") || s.tab === goal?.key) ? s.tab : null;
      return { ...base, ...rest, tab, projectTabs: tabLists(s.projectTabs), goal };
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
  // Which of your runners that work on the project the chat runs on, when
  // there's a choice; kept per project, apart from General's.
  pc.runner = runnerPicker(panel.querySelector(".chat-runner"), {
    key: `dash.chatRunner.${key}`, menuOnly: true,
    path: () => `/api/claude?project=${encodeURIComponent(proj().id)}`,
    prefix: () => proj().prefix || "",
    changed: () => pc.chat?.place(),
  });
  projectRunnerPickers.add(pc.runner);
  const paneState = paneChatState(key, () => pc.chat);
  let wasBusy = false;
  pc.chat = makeChat({
    log: panel.querySelector(".chat-log"), empty: panel.querySelector(".chat-empty"), form: panel.querySelector("form"),
    target: () => { const runner = pc.runner.choice(); return { prefix: proj().prefix || "", project: proj().id, ...(runner && { runner }) }; },
    onRunner: (name) => pc.runner.render(name),
    // New chat (and /clear): the server keeps the conversation as a past chat.
    clear: async () => {
      if (!pc.chat.history.length) return;
      await api("DELETE", `/api/projects/${proj().id}/chat`, null, proj().prefix || "");
      pc.chat.past = (pc.chat.past || 0) + 1;
      renderChatHead();
    },
    cwd: panel.querySelector(".chat-cwd"),
    // With a runner menu, the menu names the runner and the place doesn't;
    // without, the place does: the one runner online, until a turn says where it ran.
    where: () => ({ name: proj().name, machine: pc.runner.sole() || projectWhere(proj()), runnerMenu: pc.runner.menu(), path: proj().path, mode: proj().chat_permission_mode || "" }),
    // A turn left streaming when the chat was cleared is saved when it finishes.
    saved: () => { if (!pc.chat.busy) loadProjectChat(proj(), true).catch(fail); },
    // Sent from an out-of-date copy: the server's is the one to show.
    stale: () => loadProjectChat(proj(), true).catch(fail),
    actions: chatActions,
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
    // The server takes the last turn out with rewind (see chatEdits).
    edit: true,
    // The drafts Claude makes here show with Queue, Open and Discard.
    tasks: {
      prefix: () => proj().prefix || "",
      local: () => projectKey(proj()) === projectKey(state.project),
      open: (id) => openChatTask(proj(), id),
      note: async (todo, kind) => {
        const m = await api("POST", `/api/projects/${proj().id}/chat/actions`, { todo, kind }, proj().prefix || "");
        pc.chat.addNote(m);
      },
    },
  });
  // Talking in a chat again wants its reply, even if its tab was closed mid-reply.
  pc.chat.input.form.addEventListener("submit", () => chatDismissed.delete(key));
  pc.chat.past = 0; // how many past chats it has (see pastPlace)
  projectChats.set(key, pc);
  return pc;
}
// openChatTask opens task id of project p from its card in p's chat, as
// the board opens it: in p (selected first, when another is). On a phone,
// where the chat is a page of its own, the chat closes first.
async function openChatTask(p, id) {
  if (projectKey(p) !== projectKey(state.project)) await selectProject(p);
  if (!chatBeside.matches) toggleChatPane(false);
  await openDetail(id);
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
  // The runner menu loads with the chat, and again when your runners change (loadChatRunners).
  if (!pc.loaded) pc.runner.load().catch(() => {});
  pc.loaded = true;
  pc.dirty = false;
  const seq = ++pc.seq;
  let saved, res;
  try {
    ({ data: saved, res } = await request("GET", `/api/projects/${p.id}/chat`, { prefix: p.prefix || "" }));
  } catch (e) {
    pc.loaded = false;
    throw e;
  }
  if (seq !== pc.seq) return; // a newer load is on its way
  pc.remoteBusy = !!res.headers.get("X-Chat-Busy");
  pc.chat.past = Number(res.headers.get("X-Chat-Past")) || 0;
  if (pc.chat.busy) pc.dirty = true; // a turn began meanwhile: load when it ends
  else if (!sameChat(pc.chat.history, saved || [])) pc.chat.reset(saved || []);
  renderRemote(pc);
  renderChatHead(); // the clock, for past chats put aside on another device
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
// wake-ups add to (docs/goals.md). Talk to coordinator opens it in the
// pane's one goal tab, which then follows the goal open on Goals and closes
// with it, unless the chat is pinned. It runs no commands and can't be
// cleared: the conversation is the coordinator's memory. Each goal's chat
// keeps its controller, so a reply streams on after the tab moved on.
const goalChats = new Map(); // tab key → { panel, chat, loaded, tab }
const isGoalTab = (key) => typeof key === "string" && key.startsWith("goal:");
const goalTab = (key) => (pane.goal?.key === key ? pane.goal : null);
const goalKey = (g, p) => `goal:${projectKey(p)}/${g.id}`;
function goalChatFor(t) {
  if (goalChats.has(t.key)) return goalChats.get(t.key);
  const panel = $("#project-chat-tpl").content.firstElementChild.cloneNode(true);
  panel.dataset.chat = t.key;
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
    where: () => (proj() ? { name: proj().name, machine: projectWhere(proj()) } : null),
    saved: () => { if (!gc.chat.busy) { gc.loaded = false; loadGoalChat(gc).catch(fail); } },
    actions: chatActions,
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
  // Only the latest load's answer is shown; a failed one is tried again next time.
  const seq = gc.loadSeq = (gc.loadSeq || 0) + 1;
  let saved;
  try {
    saved = await api("GET", `/api/goals/${gc.tab.id}/chat`, null, p.prefix || "");
  } catch (e) {
    if (gc.loadSeq === seq) gc.loaded = false;
    throw e;
  }
  if (gc.loadSeq === seq && !gc.chat.busy) gc.chat.reset(saved);
}
// setGoalSlot puts goal g (of project p) in the goal tab, in place of the
// goal there; a tab shown on the old goal shows the new one.
function setGoalSlot(g, p) {
  const key = goalKey(g, p), old = pane.goal;
  if (old?.key === key) { old.title = g.title || old.title; return key; }
  if (old) leaveGoalChat(old.key);
  pane.goal = { key, id: g.id, pk: projectKey(p), title: g.title || "Goal" };
  if (old && pane.tab === old.key) pane.tab = key;
  return key;
}
function leaveGoalChat(key) {
  if (goalChats.get(key)?.chat.busy) chatDismissed.add(key);
  if (chatUnread.delete(key)) saveChatUnread();
}
// openGoalChat opens the chat with goal g's coordinator (g in project p).
export function openGoalChat(g, p) {
  const key = setGoalSlot(g, p);
  chatDismissed.delete(key);
  toggleChatPane(true, key);
}
// followGoal: goal g (of project p) is the one open on Goals now, or none
// (null: closed, or Goals left). The goal tab, if there is one, follows it;
// with none open it closes (and the pane with it, if it showed it), unless
// the chat is pinned.
export function followGoal(g, p) {
  if (!pane.goal) return;
  if (g && p) {
    const showing = pane.open && pane.tab === pane.goal.key;
    const key = setGoalSlot(g, p);
    if (showing) chatDismissed.delete(key);
    savePane();
    renderChatPane();
    return;
  }
  if (pane.pinned) return;
  const showing = pane.open && pane.tab === pane.goal.key;
  closeGoalTab(pane.goal.key);
  if (showing) toggleChatPane(false);
}
function closeGoalTab(key) {
  if (pane.goal?.key !== key) return;
  pane.goal = null;
  leaveGoalChat(key);
  if (pane.tab === key) pane.tab = "project";
  savePane();
  renderChatPane();
}
// goalChatEvent: a goal changed (a wake-up may have added to its
// conversation); its chat, when loaded and idle, reads it again.
export function goalChatEvent(g, m) {
  const t = pane.goal;
  if (t && t.id === g?.id && g.title && t.title !== g.title && projectByKey(t.pk)?.machine === m.name) { t.title = g.title; savePane(); renderChatPane(); }
  for (const gc of goalChats.values()) {
    if (gc.tab.id !== g?.id || projectByKey(gc.tab.pk)?.machine !== m.name || gc.chat.busy) continue;
    if (g.title) gc.tab.title = g.title;
    if (!gc.loaded || g.running) continue; // read once its turn is over
    gc.loaded = false;
    if (viewingChat(gc.tab.key)) loadGoalChat(gc).catch(() => {});
  }
}
// goalChatGone: goal g was deleted (here or elsewhere); its chat's tab closes.
export function goalChatGone(g, m) {
  const t = pane.goal;
  if (t && t.id === g?.id && projectByKey(t.pk)?.machine === m.name) closeGoalTab(t.key);
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

// chatTargets lists the chats a picture can be attached to (the preview's
// Draw → Attach to chat): [{ tab, label, icon, current }]. The first is where
// Attach goes: the chat shown in the open pane, else this project's chat.
export function chatTargets() {
  const list = [];
  if (state.project) list.push({ tab: "project", label: state.project.name, icon: "repo" });
  list.push({ tab: "general", label: "General", icon: "spark" });
  for (const p of addedTabs().map(projectByKey).filter(Boolean)) list.push({ tab: projectKey(p), label: p.name, icon: "repo" });
  if (pane.goal) list.push({ tab: pane.goal.key, label: `Coordinator of ${pane.goal.title}`, icon: "target" });
  const cur = pane.open ? paneTab() : state.project ? "project" : "general";
  const at = Math.max(0, list.findIndex((x) => x.tab === cur));
  list[at].current = true;
  return [list[at], ...list.filter((_, i) => i !== at)];
}

// attachToChat shows tab's chat in the pane and adds files (uploaded to that
// chat's computer) and text to its composer, to say something about them and send.
export function attachToChat(tab, files, text = "") {
  toggleChatPane(true, tab);
  const c = activeChat();
  if (!c) throw new Error("That chat isn't there any more.");
  c.attach(files, text);
  if (desktop.matches) c.input.focus();
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
  // Where the chat shown runs, as its project's settings say now.
  if (pane.open) activeChat()?.place();

  const tab = paneTab();
  const cur = tabProject(tab);
  const tabBtn = (id, label, kids, title = label) => h("button", {
    type: "button", role: "tab", class: "chat-pane-tab-btn", "aria-selected": String(tab === id), title,
    "data-chat": tabChatKey(id) || "", "data-title": title,
    onclick: () => showChatTab(id),
  }, ...kids, h("span", { class: "chat-tab-name" }, label), h("span", { class: "chat-tab-unread", "aria-hidden": "true" }));
  // The selected project's chat leads (General is rarely used), named after
  // it, its icon ringed as the sidebar rings the current project's.
  $("#chat-tabs").replaceChildren(
    h("div", { class: "chat-pane-tab chat-tab-current" }, tabBtn("project", state.project?.name || "Project", [state.project ? projectIcon(state.project) : ico("repo")],
      state.project && grouped() ? `${state.project.name} · ${projectWhere(state.project)}` : state.project?.name || "Project")),
    h("div", { class: "chat-pane-tab" }, tabBtn("general", "General", [ico("spark")])),
    ...keptTabs().map((p) => h("div", { class: "chat-pane-tab" },
      tabBtn(projectKey(p), p.name, [projectIcon(p)], grouped() ? `${p.name} · ${projectWhere(p)}` : p.name),
      ib({ type: "button", class: "icon-btn chat-tab-close", "aria-label": `Close the ${p.name} chat`, title: "Close this tab", onclick: () => closeChatTab(projectKey(p)) }, "x"))),
    ...(pane.goal ? [pane.goal] : []).map((t) => h("div", { class: "chat-pane-tab chat-tab-goal" },
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
  // An empty chat shows what it talks to: the project's name, the goal's title.
  const emptyTitle = (panel, text) => { panel.querySelector(".chat-empty .empty-title").textContent = text; };
  for (const [key, pc] of projectChats) { emptyTitle(pc.panel, pc.proj().name); show(pc.panel, !!cur && key === projectKey(cur)); }
  const gt = isGoalTab(tab) ? goalTab(tab) : null;
  if (gt && pane.open) goalChatFor(gt);
  for (const [key, gc] of goalChats) { emptyTitle(gc.panel, gc.tab.title); show(gc.panel, key === tab); }
  if (gt && pane.open) loadGoalChat(goalChats.get(tab)).catch((e) => toast(`Couldn't load the coordinator's conversation: ${e.message}`, true));
  renderChatHead();
  renderTryBack();
  renderGoalBack();
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
  if (b) b.hidden = !(state.view === "try" && !chatBeside.matches);
  // The Try view's Terminal/Chat tabs say which of the two is showing.
  const chatOn = chatPage(); // not pane: applyView calls this at startup before pane exists
  for (const [id, on] of [["#try-tab-term", !chatOn], ["#try-tab-chat", chatOn]]) {
    const t = $(id);
    if (!t) continue;
    t.setAttribute("aria-selected", String(on));
    t.tabIndex = on ? 0 : -1; // one tab stop for the tablist; arrows move between tabs
  }
}

// On a phone, where the chat is a page over the goal it talks about, its Goal
// button leads back to the goal. The pane (and goals.js) hear of each change.
function renderGoalBack() {
  const on = !chatBeside.matches && pane.open && state.view === "goals" && !!state.goalOpen && isGoalTab(paneTab());
  $("#chat-goal-back").hidden = !on;
  document.dispatchEvent(new CustomEvent("chatpane"));
}
$("#chat-goal-back").addEventListener("click", () => toggleChatPane(false));
chatBeside.addEventListener("change", renderGoalBack);

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
  if (!open) { setChatTabPicker(false); setPastList(false); }
  savePane();
  renderChatPane();
  // On mobile the chat is a page: opening it doesn't pop the keyboard up over the nav.
  if (open) { if (desktop.matches) activeChat()?.input.focus(); }
  else if ($("#chat-pane").contains(document.activeElement)) document.activeElement.blur();
  // The phone's chat page hid the shell's view, and the tablet's chat beside it
  // narrows it: fit its terminal again.
  if (!desktop.matches && state.view === "run") requestAnimationFrame(fitTerm);
  if (!desktop.matches && state.view === "try") requestAnimationFrame(fitTryTerm);
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
// Like any other tab, tapping it while it's shown keeps it; beside the content
// (a tablet), it closes it again.
$("#nav-chat").addEventListener("click", () => {
  if (!pane.open) toggleChatPane(true, state.project ? "project" : "general");
  else if (chatBeside.matches) toggleChatPane(false);
});
// The Chat buttons' badge (the bottom nav's, and the top bar's ⋮ menu and its Chat item): a pane chat waits
// on a permission request ("!"), is streaming an answer (a breathing dot), or has
// a reply you haven't read (a green dot), in that order.
export function renderChatBadge() {
  const chats = [chat, ...[...projectChats.values(), ...goalChats.values()].map((pc) => pc.chat)];
  const waiting = chats.some((c) => c.waiting), busy = chats.some((c) => c.busy);
  // Only chats that still have a tab count: a closed tab's reply can't be read.
  const tabs = new Set(["general", state.project && projectKey(state.project), ...keptTabs().map(projectKey), pane.goal?.key]);
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
  const nav = $("#nav-chat");
  nav.setAttribute("aria-label", says ? `Chat: ${says}` : "Chat");
  nav.title = says ? `Chat: ${says} (gc)` : "Chat (gc)";
  // The ⋮ menu's Chat item shows its shortcut; the menu's button wears the badge too.
  $("#pc-open").setAttribute("aria-label", says ? `Chat: ${says}` : "Chat");
  const more = $("#project-menu > summary");
  more.setAttribute("aria-label", says ? `More (chat: ${says})` : "More");
  more.title = says ? `More · Chat: ${says}` : "More";
  renderChatHead(); // New chat waits for a reply to end
}
$("#pc-close").addEventListener("click", () => toggleChatPane(false));
$("#pc-pin").addEventListener("click", togglePin);

// ------------------------------------------------------------------ new and past chats
// New chat starts an empty conversation in General or a project's chat;
// the server keeps the one it replaces as a past chat. The clock lists
// them (newest first): one opens in place of the current conversation
// (which becomes a past chat in turn), and × deletes one, with Undo. A
// goal's chat has neither: its conversation is its coordinator's memory.

// pastPlace is the visible chat's {chat, base, prefix} (base: its API
// path), or null for a chat without past chats.
function pastPlace() {
  const tab = paneTab();
  if (tab === "general") return { chat, base: "/api/chat", prefix: "" };
  if (isGoalTab(tab)) return null;
  const p = tabProject(tab);
  return p ? { chat: projectChatFor(p).chat, base: `/api/projects/${p.id}/chat`, prefix: p.prefix || "" } : null;
}
// renderChatHead shows New chat and the clock for the visible chat.
export function renderChatHead() {
  const e = pastPlace(), busy = !!e?.chat.busy;
  const nb = $("#pc-new"), pb = $("#pc-past");
  nb.hidden = !e;
  nb.disabled = !e || busy || !e.chat.history.length;
  pb.hidden = !e || !(e.chat.past > 0);
  pb.disabled = busy;
  if (pb.hidden || busy) setPastList(false);
}
$("#pc-new").addEventListener("click", attempt(async () => {
  const e = pastPlace();
  if (!e || e.chat.busy) return;
  await e.chat.clear();
  renderChatHead();
  if (desktop.matches) e.chat.input.focus();
}));

// The open list's place (a token: pastPlace makes a new one each time) and what it shows.
let pastFor = null, pastList = [];
async function setPastList(open) {
  const el = $("#chat-past");
  if (el.hidden === !open) return;
  el.hidden = !open;
  $("#pc-past").setAttribute("aria-expanded", String(open));
  if (!open) { pastFor = null; return; }
  setChatTabPicker(false);
  const e = pastFor = pastPlace();
  el.replaceChildren();
  if (e) await loadPastList(e, true);
}
async function loadPastList(e, focus = false) {
  const list = await api("GET", `${e.base}/past`, null, e.prefix);
  if (pastFor !== e) return;
  e.chat.past = list.length;
  pastList = list;
  renderPastList(e, focus);
}
function renderPastList(e, focus = false) {
  const el = $("#chat-past");
  if (!pastList.length) { setPastList(false); renderChatHead(); return; }
  el.replaceChildren(...pastList.map((pc) => h("div", { class: "chat-past-row", role: "none", "data-id": pc.id },
    h("button", { type: "button", role: "menuitem", class: "chat-past-open", title: pc.title, onclick: attempt(() => openPast(e, pc)) },
      h("span", { class: "chat-past-title" }, pc.title), h("span", { class: "chat-past-when" }, agoText(pc.at))),
    ib({ type: "button", role: "menuitem", class: "chat-past-del", "aria-label": `Delete ${pc.title}`, title: "Delete", onclick: attempt(() => deletePast(e, pc)) }, "trash"))));
  if (focus) el.querySelector("button")?.focus();
}
async function openPast(e, pc) {
  if (e.chat.busy) return;
  const had = e.chat.history.length > 0;
  const msgs = await api("POST", `${e.base}/past/${pc.id}/open`, null, e.prefix);
  if (!had) e.chat.past = Math.max(0, e.chat.past - 1);
  setPastList(false);
  e.chat.reset(msgs);
  renderChatHead();
  if (desktop.matches) e.chat.input.focus();
  else $("#pc-past").focus();
}
async function deletePast(e, pc) {
  const el = $("#chat-past"), rows = [...el.children], i = rows.findIndex((r) => r.dataset.id === pc.id);
  await api("DELETE", `${e.base}/past/${pc.id}`, null, e.prefix);
  e.chat.past = Math.max(0, e.chat.past - 1);
  if (pastFor === e) {
    pastList = pastList.filter((x) => x.id !== pc.id);
    renderPastList(e);
    // The focus goes to the row that took its place.
    const next = el.children[Math.min(i, el.children.length - 1)];
    next?.querySelector(".chat-past-open")?.focus();
  }
  renderChatHead();
  if (el.hidden) $("#pc-past").hidden ? e.chat.input.focus() : $("#pc-past").focus();
  toast(pc.title, { head: "Chat deleted", action: { label: "Undo", run: async () => {
    await api("POST", `${e.base}/past/${pc.id}/restore`, null, e.prefix);
    e.chat.past++;
    renderChatHead();
    if (pastFor && pastFor.base === e.base) await loadPastList(pastFor);
  } } });
}
$("#pc-past").addEventListener("click", attempt(() => setPastList($("#chat-past").hidden)));
document.addEventListener("click", (e) => { if (!$("#chat-past").hidden && !e.target.closest("#chat-past, #pc-past")) setPastList(false); });
$("#chat-past").addEventListener("keydown", (e) => {
  const btns = $$("#chat-past button"), i = btns.indexOf(document.activeElement);
  if (e.key === "Escape") { setPastList(false); $("#pc-past").focus(); }
  else if (e.key === "ArrowDown" || e.key === "j") (btns[i + 1] || btns[0])?.focus();
  else if (e.key === "ArrowUp" || e.key === "k") (btns[i - 1] || btns.at(-1))?.focus();
  else if (e.key === "Delete" || e.key === "Backspace") document.activeElement?.closest(".chat-past-row")?.querySelector(".chat-past-del")?.click();
  else return;
  e.preventDefault(); e.stopPropagation();
});

// Desktop: drag the pane's left edge to resize it, overlaid or pinned.
resizeHandle($("#pc-resize"), $("#chat-pane"), {
  edge: "left",
  min: CHAT_MIN_W,
  apply(w, save) {
    pane.width = w;
    if (w) $(".app").style.setProperty("--chat-w", `${w}px`);
    else $(".app").style.removeProperty("--chat-w");
    if (save) savePane();
  },
});
renderChatPane();

// Every task shares todoChat: each one's draft (with what was queued, and
// the files) is kept here while another is shown, and put back when it is.
let todoChatId = null;
const todoDrafts = new Map();
export async function loadTodoChat() {
  const id = state.detail;
  // Reopening the todo whose reply is still streaming keeps it on screen.
  // The events file only grows, so the worker's events from while it was closed are those past ours.
  if (todoChatId === id && todoChat.busy) {
    const events = await api("GET", `/api/todos/${id}/events`);
    if (state.detail === id && worker.id === id) events.slice(worker.events.length).forEach((e) => addWorkerEvent(id, e));
    return todoChat.toEnd();
  }
  const switched = todoChatId !== id;
  if (switched && todoChatId) {
    const d = todoChat.takeDraft();
    d ? todoDrafts.set(todoChatId, d) : todoDrafts.delete(todoChatId);
  }
  todoChatId = id;
  worker.id = null;
  worker.events = worker.live = [];
  todoChat.reset();
  if (switched) {
    todoChat.putDraft(todoDrafts.get(id));
    todoDrafts.delete(id);
  }
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
