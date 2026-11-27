import { invisiblesOn, vimOn } from "../editor.js";
import { stepCommit } from "../commitnav.js";
import { $, h, ico } from "./dom.js";
import { desktop } from "./viewport.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { draft, promptEditor } from "./todoform.js";
import { LANES, STATUS_LABEL, statusLabel, cp, hist, state } from "./state.js";
import { navView, showView } from "./nav.js";
import { boardFilter, setScanFilter } from "./tags.js";
import { baseBranch, projectKey } from "./events.js";
import { cleanup, grouped, loadProjects, toggleStar, menuOpen, notifyLabel, openDeleteProject, openNewProject, openPlanner, openProjects, projectWhere, selectProject, setMenu, setPicker, setSideCollapsed, sideCollapsed, stepProject, toggleProjectPause } from "./projects.js";
import { mineOn, multiMember } from "./people.js";
import { pushDone } from "./board.js";
import { byPriority, title } from "./branches.js";
import { openQuickAdd, openTodoForm } from "./todos.js";
import { closeDetail, openDetail } from "./detail.js";
import { hidePreview, pv } from "./preview.js";
import { renderPane, startDraw } from "./drawmode.js";
import { toggleViewedKey } from "./review.js";
import { toggleInvisibles, toggleVim } from "./diffedit.js";
import { openPrefs, showTipsAgain } from "./prefs.js";
import { openRunners } from "./runners.js";
import { logout } from "./myaccount.js";
import { openConnector } from "./wallview.js";
import { openAccount } from "./accountsheet.js";
import { notifyOn, toggleNotifications } from "./notifications.js";
import { renderUsage } from "./usage.js";
import { openCI } from "./actions.js";
import { openClaudeStatus } from "./claudestatus.js";
import { openStatus } from "./status.js";
import { openSettings } from "./settings.js";
import { chat, todoChat } from "./chat.js";
import { isStarred, starsOn } from "./stars.js";
import { pane, paneOverlays, projectChatFor, toggleChatPane, togglePin } from "./chatpane.js";
import { attached, openShell, shellBlocked, startProc, techFor, term } from "./terminal.js";
import { closeCommit, commitScroller, moveCommit, refreshHistory, stepCommitFile } from "./history.js";
import { closeCommitPreview, previewingCommit } from "./commitpreview.js";
import { closeGoal, goalsOn, gp, openGoalSheet, stepGoal } from "./goals.js";
import { closeDeploy, dp, stepDeploy } from "./deploys.js";
import { cardOpen, closeCard } from "./planning.js";
import { closeNeed, needsScroller, openSelected, openedPerm, stepNeed } from "./needs.js";
import { focusTry, openTry } from "./try.js";
import { startVoiceMode } from "./voicemode.js";
import { BOUND, CTRL_KEYS, VIEWS, ctrlScrolls, openKeys, cursorTodo, enterKey, isTyping, keys, laneCount, laneSkipped, laneTodos, moveCursor, openCommandLine, openDialog, permKey, runCommand, scrollBy, scroller, setMode, stepLane, todoKey } from "./vim.js";

// ------------------------------------------------------------------ palette
// Ctrl-k: one search over commands, projects and the current project's todos.
// A leading > limits it to commands, @ to projects, # to todos.
const palette = { items: [], sel: 0 };

// Every word must appear in the text, as a substring (best) or in order.
export function fuzzyScore(text, q) {
  text = text.toLowerCase();
  let total = 0;
  for (const w of q.toLowerCase().split(/\s+/).filter(Boolean)) {
    const i = text.indexOf(w);
    if (i >= 0) { total += 100 - Math.min(i, 50) + (i === 0 || /\W/.test(text[i - 1]) ? 40 : 0); continue; }
    let from = 0, prev = -2, s = 0;
    for (const c of w) {
      const j = text.indexOf(c, from);
      if (j < 0) return -1;
      s += j === prev + 1 ? 3 : 1;
      prev = j; from = j + 1;
    }
    total += s;
  }
  return total;
}

function paletteCommands() {
  const p = state.project;
  const go = (view) => () => { closeDetail(); showView(view); };
  const serve = techFor("web:run"), deploy = techFor("task:deploy");
  return [
    p && ["New task", "plus", () => openQuickAdd(), "n", "add create todo quick"],
    p && ["New task with all options", "plus", () => openTodoForm(), "N", "add create todo full form title priority dependencies attachments"],
    goalsOn(p) && ["New goal", "target", () => openGoalSheet(), "", "goal coordinator plan bigger project objective"],
    ["Board", "flag", go("board"), "gb", "view lanes"],
    p && multiMember() && [mineOn() ? "Show everyone's tasks" : "Show only my tasks", "users", () => runCommand("mine"), ":mine", "mine my own created by me filter board"],
    p && ["Scans", "scan", go("scans"), "gn", "scan security review dependency audit schedule cron run board"],
    p && [boardFilter.showScans ? "Show every task" : "Show only tasks from scans", "bug", () => { closeDetail(); showView("board"); setScanFilter(); }, "", "from scans filter board drafts findings issues"],
    ["Toggle chat", "comment", () => toggleChatPane(), "gc", "chat open close claude ask project general"],
    ["Start voice mode", "mic", startVoiceMode, "gv", "voice hands-free speak listen dictate microphone talk"],
    [pane.pinned ? "Unpin chat" : "Pin chat", "pin", togglePin, ":pin", "pin unpin chat overlay dock"],
    ["Code: Shell", "terminal", go("run"), "gs", "shell run terminal processes view main checkout"],
    p && ["Code: Shell at a commit (try)", "terminal", openTry, ":try", "try commit older version cli runner worktree"],
    ["History: Commits", "log", go("history"), "gh", "history git graph tree commits view"],
    ["Goals", "target", go("goals"), "go", "goal coordinator plan progress budget"],
    ["Code: Files", "code", go("files"), "gf", "files browse edit source code tree editor"],
    ["Needs you", "inbox", go("needs"), "gi", "needs you inbox waiting review reviews approve questions answer permissions failed retry all projects"],
    ["Planning", "plan", go("planning"), "gp", "plan roadmap product owner cards ideas planned"],
    ["Switch project", "repo", openProjects, "p", "projects"],
    p && starsOn() && [isStarred(p) ? "Unstar project" : "Star project", "star", () => toggleStar(p), "", "favorite favourite pin sidebar starred"],
    ["Add project", "plus", () => openNewProject(), "", "new create local repository runner github clone url"],
    ["Write a project brief", "spark", () => openPlanner(), "", "idea brief interview plan new project goal"],
    p && ["Project settings", "settings", openSettings, ":settings", "configure commands"],
    p?.run_command && [serve.label || "Run in checkout", serve.icon || "play", () => startProc({ kind: "web", preset: "run" }), "R", `run web server checkout ${serve.icon || ""}`],
    p?.test_command && ["Run tests", "check", () => startProc({ kind: "task", preset: "test" }), "T", "test"],
    ["History: Deploys", "rocket", go("deploys"), "gd", "deploys deploy environments production staging live release ship promote rollback"],
    p?.environments?.length && [deploy.label || "Deploy", deploy.icon || "rocket", go("deploys"), "D", `release ship environment `],
    p && !shellBlocked(p) && ["Open a shell", "terminal", () => openShell(), "!", "terminal pty"],
    p && state.unpushed > 0 && ["Push merged work", "push", pushDone, ":push", "git"],
    p && [p.paused ? "Resume this project" : "Pause this project", p.paused ? "play" : "pause", toggleProjectPause, "", "pause stop hold tasks"],
    p && ["Clean up leftovers", "broom", cleanup, ":cleanup", "clean worktrees branches logs uploads disk"],
    p && ["Delete project…", "trash", openDeleteProject, "", "remove repo clone github"],
    ["Builds", "checks", openCI, ":ci", "ci github actions"],
    (state.claudeStatus?.enabled || state.me?.user?.is_platform_admin) && ["Claude status", "spark", openClaudeStatus, "", "status.claude.com incidents outage down degraded webhook"],
    ["Status", "alert", openStatus, ":status", "health live connection origin runners previews merge queue rate limit billing sync checkouts"],
    ["Claude usage", "clock", () => { renderUsage(); $("#usage-sheet").showModal(); }, ":usage", "limits"],
    state.usage && state.me?.user?.is_platform_admin && [state.usage.paused ? "Resume the queue (all projects)" : "Pause the queue (all projects)", state.usage.paused ? "play" : "pause", () => runCommand(state.usage.paused ? "resume" : "pause"), "", "queue"],
    ["Permission requests", "eye", () => runCommand("perms"), ":perms", "approvals allow deny"],
    state.runners && ["Runners", "computer", openRunners, ":runners", "computers machines dash runner add install revoke"],
    ["Preferences", "prefs", openPrefs, ":prefs", "preferences vim invisibles whitespace notifications browser chat suggestions"],
    ["Show review tips again", "eye", showTipsAgain, ":tips", "help hints walkthrough review diff comment rework approve onboarding"],
    ["Notification settings", "bell", () => openAccount("notifications"), "", "notifications summary daily weekly digest email push webhooks issues"],
    ["Connect Claude", "plug", openConnector, ":connector", "claude.ai connector mcp mobile app remote oauth"],
    [notifyLabel(), notifyOn() ? "bell-off" : "bell", toggleNotifications, ":notify", "notifications"],
    [vimOn() ? "Turn off vim keys in the editor" : "Turn on vim keys in the editor", "code", toggleVim, ":vim", "vim keybindings prompt editor"],
    [invisiblesOn() ? "Hide invisible characters in the editor" : "Show invisible characters in the editor", "code", toggleInvisibles, ":list", "invisibles whitespace tabs spaces trailing"],
    desktop.matches && [sideCollapsed() ? "Expand sidebar" : "Collapse sidebar", "minimize", () => setSideCollapsed(!sideCollapsed()), "[", "sidebar"],
    ["Refresh projects", "redo", loadProjects, ":refresh", "reload"],
    ["Keyboard shortcuts", "code", openKeys, "?", "help keys"],
    ["Sign out", "logout", logout, "", "log out"],
  ].filter(Boolean).map(([label, icon, run, keys, words]) => ({ kind: "Command", label, icon, run, keys, text: `${label} ${words}` }));
}

function paletteProjects() {
  // Starred projects first, then the rest in their order.
  const others = state.projects.filter((p) => projectKey(p) !== projectKey(state.project));
  return [...others.filter(isStarred), ...others.filter((p) => !isStarred(p))].map((p) => ({
    kind: "Project", label: p.name, icon: "repo", hint: grouped() ? projectWhere(p) : baseBranch(p),
    text: `${p.name} ${p.machine} ${projectWhere(p)} ${p.path}`, run: () => selectProject(p),
  }));
}

// Every todo, lane by lane, regardless of the board's filter or hidden old ones.
function paletteTodos() {
  const all = [...state.todos.values()];
  return LANES.flatMap((l) => all.filter((t) => l.statuses.includes(t.status)).sort(byPriority).map((t) => ({
    kind: "Task", label: title(t), icon: "draft", hint: t.paused ? "Paused" : statusLabel(t),
    text: `${title(t)} ${t.prompt} ${t.id} ${STATUS_LABEL[t.status] || ""}`, run: () => { showView("board"); return openDetail(t.id); },
  })));
}

function paletteResults(raw) {
  const mode = { ">": "Command", "@": "Project", "#": "Task" }[raw[0]];
  const q = (mode ? raw.slice(1) : raw).trim();
  const sources = { Command: paletteCommands, Project: paletteProjects, Task: () => (state.project ? paletteTodos() : []) };
  const pools = (mode ? [mode] : ["Command", "Project", "Task"]).map((k) => sources[k]());
  let out;
  if (!q) out = pools.flatMap((p) => p.slice(0, mode ? 200 : 8));
  else {
    out = pools.flat().map((it) => {
      const s = fuzzyScore(it.label, q);
      return { it, s: s >= 0 ? s * 2 : fuzzyScore(it.text, q) };
    }).filter((x) => x.s >= 0).sort((a, b) => b.s - a.s).slice(0, 60).map((x) => x.it);
  }
  if (q && !mode && state.project) {
    out.push(
      { kind: "Action", label: `New task: ${q}`, icon: "plus", run: () => { openTodoForm(); $("#todo-form").prompt.value = q; promptEditor.render(); draft.changed(); } },
      { kind: "Action", label: `Ask the project chat: ${q}`, icon: "comment", run: () => { const c = projectChatFor(state.project).chat; askChat(c.input, c.input.form, q, () => toggleChatPane(true, "project")); } },
    );
  }
  if (q && !mode) out.push({ kind: "Action", label: `Ask Claude: ${q}`, icon: "spark", run: () => askChat(chat.input, $("#chat-form"), q, () => toggleChatPane(true, "general")) });
  return out;
}

export function askChat(input, form, q, show) {
  show();
  input.value = q;
  input.dispatchEvent(new Event("input"));
  form.requestSubmit();
}

function renderPalette() {
  const list = $("#palette-list");
  const input = $("#palette-input");
  palette.sel = Math.max(0, Math.min(palette.items.length - 1, palette.sel));
  let group = null;
  const rows = [];
  palette.items.forEach((it, i) => {
    if (!input.value.trim() && it.kind !== group) rows.push(h("li", { class: "palette-group", role: "presentation" }, `${it.kind}s`));
    group = it.kind;
    rows.push(h("li", {
      id: `palette-${i}`, role: "option", class: "palette-item", "aria-selected": String(i === palette.sel),
      onmousemove: () => { if (palette.sel !== i) { palette.sel = i; renderPalette(); } },
      onclick: () => runPalette(i),
    }, ico(it.icon), h("span", { class: "palette-label" }, it.label),
    it.hint ? h("span", { class: "palette-hint" }, it.hint) : null,
    it.keys ? h("kbd", {}, it.keys) : input.value.trim() ? h("span", { class: "palette-kind" }, it.kind) : null));
  });
  list.replaceChildren(...(rows.length ? rows : [h("li", { class: "palette-empty", role: "presentation" }, "No matches")]));
  if (palette.items.length) input.setAttribute("aria-activedescendant", `palette-${palette.sel}`);
  else input.removeAttribute("aria-activedescendant");
  $(`#palette-${palette.sel}`)?.scrollIntoView({ block: "nearest" });
}

function updatePalette() {
  palette.items = paletteResults($("#palette-input").value);
  palette.sel = 0;
  renderPalette();
}

function openPalette(prefill = "") {
  setPicker(false);
  setMenu(false);
  const input = $("#palette-input");
  input.value = prefill;
  updatePalette();
  $("#palette").showModal();
  input.focus();
}

function runPalette(i) {
  const it = palette.items[i];
  if (!it) return;
  $("#palette").close();
  attempt(it.run)();
}

$("#palette-input").addEventListener("input", updatePalette);
$("#palette-input").addEventListener("keydown", (e) => {
  const step = { ArrowDown: 1, ArrowUp: -1 }[e.key] ?? (e.ctrlKey && { n: 1, p: -1 }[e.key]);
  const page = { PageDown: 8, PageUp: -8 }[e.key];
  if (step || page) {
    const n = palette.items.length;
    if (n) palette.sel = page ? palette.sel + page : (palette.sel + step + n) % n;
    renderPalette();
  } else if (e.key === "Enter" && !e.isComposing) runPalette(palette.sel);
  else return;
  e.preventDefault();
});
// A click on the backdrop of any modal closes it. The backdrop's clicks land on
// the dialog itself, outside its box. Both press and release must be outside,
// so dragging a text selection out of a field doesn't close the sheet.
// close("cancel") like a Cancel button: sheets save only on their own values.
// It fires a cancelable "cancel" first, as Esc does, so a sheet that guards
// unsaved changes (diffedit.js, goals.js, scans.js) can ask before closing.
const outsideDialog = (d, e) => {
  if (e.target !== d || !d.open) return false;
  const r = d.getBoundingClientRect();
  return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
};
let backdropDown = null;
document.addEventListener("pointerdown", (e) => { backdropDown = e.target instanceof HTMLDialogElement && outsideDialog(e.target, e) ? e.target : null; });
document.addEventListener("click", (e) => {
  const d = backdropDown;
  backdropDown = null;
  if (d && outsideDialog(d, e) && d.dispatchEvent(new Event("cancel", { cancelable: true }))) d.close("cancel");
});
$("#palette-open").addEventListener("click", () => openPalette());
// Ctrl-k (⌘K on a Mac) works everywhere but the terminal, even while typing.
document.addEventListener("keydown", (e) => {
  if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey || e.key.toLowerCase() !== "k" || e.target.closest?.(".xterm")) return;
  const d = openDialog();
  if (d && d.id !== "palette") return;
  e.preventDefault();
  e.stopPropagation();
  d ? d.close() : openPalette();
}, true);

document.addEventListener("keydown", (e) => {
  if (e.metaKey || e.altKey || e.isComposing) return;
  const target = e.target;

  // Insert mode: only Esc (leave) is ours; Ctrl-] is handled by the terminal.
  // The task's composer, focused as the task opens, while still empty: Esc
  // closes the task, as it would have before you clicked in it.
  if (isTyping(target)) {
    if (e.key !== "Escape" || target.closest("dialog")) return;
    const closeTask = !e.defaultPrevented && state.detail && target === todoChat.input && !target.value.trim();
    target.blur(); setMode(); e.preventDefault();
    if (closeTask) escTask();
    return;
  }
  if (openDialog()) return; // dialogs handle their own keys (Esc closes)

  let k = e.key;
  if (e.ctrlKey) {
    if (!CTRL_KEYS.includes(k) || !ctrlScrolls()) return;
    k = `ctrl+${k}`;
  }
  if (k === "Shift" || k === "Control") return;
  // Tab stays native, in the sidebar too: switching between the sections and
  // projects lists on Tab trapped keyboard users there (WCAG 2.1.2), and hid
  // the other sections from Tab. o switches between the lists instead.
  if (k === "Tab") return;
  // Keyboard users activating a focused control keep the browser's behaviour
  // (except sections, which also close the open todo and leave the list).
  // Enter on a focused card's open button opens the task; it never approves.
  if ((k === "Enter" || k === " ") && target.closest?.("button, a, summary, input, [role=tab]") && sidebarList(target) !== SECTIONS) return;

  // Counts: 5j, 10G …
  if (/^[0-9]$/.test(k) && !(k === "0" && !keys.count) && !keys.pending) {
    keys.count = keys.count * 10 + Number(k);
    setMode();
    return;
  }
  const n = Math.max(1, keys.count);
  const hadCount = keys.count > 0;
  keys.count = 0;
  if (keys.pending) {
    k = keys.pending + k;
    keys.pending = "";
  } else if (["g", "d"].includes(k)) {
    keys.pending = k;
    setMode();
    return;
  }
  setMode();
  if (!BOUND.test(k)) return; // leave Tab, browser shortcuts etc. alone
  e.preventDefault();
  attempt(handleNormal)(k, n, hadCount);
});

// Sidebar lists: o focuses the projects and then toggles between them and the
// sections, p the projects (in the phone menu), Tab switches between them. j/k move, Enter opens the focused item, Esc, q
// or l leave the list.
// The sections: the nav's own buttons and the drawer's (.menu-views), which style.css
// orders among them in the side rail; j/k go by where they show.
const SECTIONS = ".nav [data-view]";
// The projects and the Other projects fold between them (stars.js).
const PROJECTS = "#side-project-list .side-project, #side-others-fold";
function sidebarList(el) {
  if (el?.matches?.(SECTIONS)) return SECTIONS;
  if (el?.closest?.("#side-project-list")) return PROJECTS;
  return null;
}

function openSections() {
  setMenu(false);
  $(`.nav [data-view="${navView(state.view)}"]`)?.focus();
}

// o: the projects list first; pressed again, it switches to the sections
// list and back.
function toggleSidebarList() {
  const picker = $("#project-picker");
  if (sidebarList(document.activeElement) === PROJECTS || !picker.hidden) {
    if (!picker.hidden) setPicker(false);
    return openSections();
  }
  return openProjects();
}

function openSection(name) {
  closeDetail();
  document.activeElement?.blur();
  showView(name);
  setMode();
}

function sidebarKey(k, n, hadCount) {
  const list = sidebarList(document.activeElement);
  let btns = [...document.querySelectorAll(list)];
  if (list === SECTIONS) {
    btns = btns.filter((b) => b.getClientRects().length);
    const top = new Map(btns.map((b) => [b, b.getBoundingClientRect().top]));
    btns.sort((a, b) => top.get(a) - top.get(b));
  }
  const i = btns.indexOf(document.activeElement);
  const go = (j) => btns[Math.max(0, Math.min(btns.length - 1, j))]?.focus();
  switch (k) {
    case "j": case "ArrowDown": return go(i + n);
    case "k": case "ArrowUp": return go(i - n);
    case "gg": return go(hadCount ? n - 1 : 0);
    case "G": return go(hadCount ? n - 1 : Infinity);
    case "Enter": case " ":
      if (list === SECTIONS) return openSection(document.activeElement.dataset.view);
      break;
    case "Escape": case "q": case "l": document.activeElement.blur(); setMenu(false); setMode(); return;
  }
  return false;
}

// escTask closes the open task: a phone's full-screen preview of it first.
function escTask() {
  if (pv.todo && !desktop.matches) hidePreview();
  else closeDetail();
}

async function handleNormal(k, n, hadCount) {
  if (sidebarList(document.activeElement) && sidebarKey(k, n, hadCount) !== false) return;
  // An open commit (desktop): n/p step through its files, before n (new
  // task) and p (projects) as global keys.
  if ((k === "n" || k === "p") && state.view === "history" && !state.detail && stepCommitFile(k === "n" ? n : -n)) return;
  // Global keys.
  switch (k) {
    case ":": return openCommandLine();
    case "?": return openKeys();
    case "gt": return showView(VIEWS[(VIEWS.indexOf(navView(state.view)) + n) % VIEWS.length]);
    case "gT": return showView(VIEWS[(VIEWS.indexOf(navView(state.view)) - n + VIEWS.length * n) % VIEWS.length]);
    case "gb": return showView("board");
    case "gc": return toggleChatPane();
    case "c": return toggleChatPane(true, "project");
    case "gs": return showView("run");
    case "gh": return showView("history");
    case "go": return showView("goals");
    case "gd": return showView("deploys");
    case "gf": return showView("files");
    case "gp": return showView("planning");
    case "gi": return showView("needs");
    case "gv": return startVoiceMode();
    case "gn": return showView("scans");
    case "p": return openProjects();
    case "J": case "K": return stepProject(k === "J" ? n : -n);
    case "[": return desktop.matches && setSideCollapsed(!sideCollapsed());
    case "o": return toggleSidebarList();
    case "Escape": case "q":
      if (menuOpen()) return setMenu(false);
      if (k === "Escape" && pane.open && paneOverlays()) return toggleChatPane(false);
      if (state.detail) return escTask();
      if (cp.sha) return previewingCommit() ? closeCommitPreview() : closeCommit(); // a commit's preview first
      if (gp.open) return closeGoal();
      if (dp.open) return closeDeploy();
      if (cardOpen()) return closeCard();
      if (state.needs.open) return closeNeed();
      return;
    // Permission requests: y allow, A always allow, X deny. (Not n: n is
    // always a new task, so it can't deny by surprise.)
    case "y": case "A": case "X":
      // On Needs you: the open item's request.
      if (state.view === "needs" && !state.detail && openedPerm()) return permKey(k, openedPerm(), "");
      if (await permKey(k) === false) toast("No permission request waiting");
      return;
    case "n": case "N":
      // n is a new task (quick sheet), N one with all options (full form).
      if (!state.project) return;
      return k === "n" ? openQuickAdd() : openTodoForm();
    case "/":
      if (state.project) return openPalette("#");
      break;
  }

  // Detail view.
  if (state.detail) {
    const t = state.todos.get(state.detail);
    if (["j", "k", "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "gg", "G"].includes(k)) return scrollBy(scroller(), k, n);
    if (["h", "l"].includes(k)) {
      const panes = ["timeline", "diff"];
      state.pane = panes[(panes.indexOf(state.pane) + (k === "l" ? 1 : panes.length - 1)) % panes.length];
      return renderPane();
    }
    if (state.pane === "timeline" && k === "i") return todoChat.input.focus();
    if (t && k === "v" && state.pane === "diff") return toggleViewedKey();
    if (t && k === "P" && pv.todo === t.id) return startDraw(); // P: pen on the preview (D is Deploy, on the Shell page)
    if (t && k === "Enter" && enterKey(t)) return todoKey(t, enterKey(t));
    if (t) await todoKey(t, k);
    return;
  }

  switch (state.view) {
    case "board": {
      if (!state.project) return;
      const { lane, row } = keys.cur;
      switch (k) {
        case "j": case "ArrowDown": return moveCursor(lane, keys.active ? row + n : row);
        case "k": case "ArrowUp": return moveCursor(lane, row - n);
        case "h": case "ArrowLeft": return moveCursor(stepLane(lane, -n), row);
        case "l": case "ArrowRight": return moveCursor(stepLane(lane, n), row);
        case "gg": return moveCursor(lane, hadCount ? n - 1 : 0);
        case "G": return moveCursor(lane, hadCount ? n - 1 : Infinity);
        case "ctrl+d": case "ctrl+u": case "ctrl+f": case "ctrl+b": return scrollBy(scroller(), k, n);
        case "w": case "b": {
          const step = k === "w" ? 1 : -1;
          for (let li = lane + step; li >= 0 && li < laneCount(); li += step) {
            if (laneTodos(li).length && !laneSkipped(li)) return moveCursor(li, 0);
          }
          return;
        }
        case "Enter": case "Space": case " ": {
          const t = cursorTodo();
          if (t && k === "Enter" && enterKey(t)) return todoKey(t, enterKey(t));
          if (t) return openDetail(t.id);
          return;
        }
        // a approves, as in an open task: only the task under a shown cursor,
        // in Review. (New task is n.)
        case "a": {
          const t = keys.active && cursorTodo();
          if (t?.status !== "review") throw new Error("a approves the task under the cursor, in Review. A new task is n.");
          return todoKey(t, "a");
        }
      }
      const t = cursorTodo();
      if (t) await todoKey(t, k);
      return;
    }
    case "run": {
      const cur = state.procs.find((p) => p.id === attached);
      switch (k) {
        case "i": case "Enter": return term.focus();
        case "x": if (cur?.running) { await api("DELETE", `/api/processes/${cur.id}`); toast("Stopping"); } return;
        case "R": return startProc({ kind: "web", preset: "run" });
        case "T": return startProc({ kind: "task", preset: "test" });
        case "D": return showView("deploys");
        case "!": return openShell();
      }
      return;
    }
    case "try": // the Shell page at a commit
      if (k === "i" || k === "Enter") return focusTry();
      return;
    case "history": {
      const hashes = hist.commits.map((c) => c.hash);
      switch (k) {
        case "j": case "ArrowDown": return moveCommit(stepCommit(hashes, cp.sha, n));
        case "k": case "ArrowUp": return moveCommit(stepCommit(hashes, cp.sha, -n));
        case "gg": return moveCommit(hadCount ? hashes[Math.min(n, hashes.length) - 1] : hashes[0]);
        case "G": return moveCommit(hadCount ? hashes[Math.min(n, hashes.length) - 1] : hashes.at(-1));
        case "r": return refreshHistory();
        case "P": if (pv.commit) return startDraw(); break; // P: pen on the commit's preview
      }
      // With a commit open, the keys that scroll read it.
      if (["ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b"].includes(k)) return scrollBy(cp.sha ? commitScroller() : scroller(), k, n);
      return;
    }
    case "goals":
      switch (k) {
        case "j": case "ArrowDown": return stepGoal(n);
        case "k": case "ArrowUp": return stepGoal(-n);
      }
      if (["ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b"].includes(k)) return scrollBy(gp.open ? $("#gd-body") : scroller(), k, n);
      return;
    case "needs":
      switch (k) {
        case "j": case "ArrowDown": return stepNeed(n);
        case "k": case "ArrowUp": return stepNeed(-n);
        case "gg": return stepNeed(-Infinity);
        case "G": return stepNeed(Infinity);
        case "Enter": case " ": return openSelected();
      }
      if (["ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b"].includes(k)) return scrollBy(needsScroller() || scroller(), k, n);
      return;
    case "deploys":
      switch (k) {
        case "j": case "ArrowDown": return stepDeploy(n);
        case "k": case "ArrowUp": return stepDeploy(-n);
      }
      if (["ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b"].includes(k)) return scrollBy(dp.open ? $("#dd-body") : scroller(), k, n);
      return;
  }
}

// Forms: Ctrl-Enter (⌘-Enter on a Mac) adds to the queue, Ctrl-Shift-Enter or Ctrl-S saves a draft.
$("#todo-form").addEventListener("keydown", (e) => {
  const mod = (e.ctrlKey || e.metaKey) && !e.altKey;
  if (mod && e.key === "Enter") { e.preventDefault(); $("#todo-sheet").close(e.shiftKey ? "draft" : "queue"); }
  if (mod && e.key.toLowerCase() === "s") { e.preventDefault(); $("#todo-sheet").close("draft"); }
});
