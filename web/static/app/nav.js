import { $, $$ } from "./dom.js";
import { desktop } from "./viewport.js";
import { fail, reduceMotion } from "./dialogs.js";
import { state } from "./state.js";
import { setMenu } from "./projects.js";
import { pane, renderTryBack, toggleChatPane } from "./chatpane.js";
import { ensureShell, fitTerm } from "./terminal.js";
import { enterTry, leaveTry } from "./try.js";
import { closeCommit, loadGraph } from "./history.js";
import { loadFiles } from "./files.js";
import { loadIssues } from "./issues.js";
import { loadScans } from "./scans.js";
import { cl, flushChecklist, loadChecklist } from "./checklist.js";

// ------------------------------------------------------------------ navigation
export function showView(name) {
  const apply = () => applyView(name);
  if (name !== state.view && document.startViewTransition && !reduceMotion.matches) {
    document.startViewTransition(apply);
    return;
  }
  apply();
}

function applyView(name) {
  state.view = name;
  try { localStorage.setItem("dash.view", name); } catch {}
  renderNavActive();
  $$(".view").forEach((v) => v.classList.toggle("active", v.id === `view-${name}`));
  if (name === "run") requestAnimationFrame(() => { fitTerm(); ensureShell(); });
  if (name === "history") loadGraph().catch(fail);
  else closeCommit();
  if (name === "files" && state.project) loadFiles().catch(fail); // at startup the project (and the files code below) isn't ready yet
  if (name === "checklist" && state.project) loadChecklist().catch(fail);
  else if (state.project && typeof cl !== "undefined" && cl.pending.length) flushChecklist().catch(fail); // leaving it saves at once
  if (name === "issues" && state.project) loadIssues().catch(fail);
  if (name === "scans" && state.project) loadScans().catch(fail);
  if (name === "try") requestAnimationFrame(() => enterTry().catch(fail));
  else leaveTry();
  renderTryBack();
}
// On mobile the chat is a page of its own (the nav's Chat tab): while it is
// open, Chat is the active section rather than the view under it.
// (.chat-open, not pane: applyView runs at startup before pane exists.)
export function chatPage() { return !desktop.matches && $(".app").classList.contains("chat-open"); }
export function renderNavActive() {
  const chatOn = chatPage();
  const mark = (b, on) => {
    b.classList.toggle("active", on);
    b.toggleAttribute("aria-current", on);
    if (on) b.setAttribute("aria-current", "page");
  };
  $$(".nav > button[data-view], .menu-views [data-view]").forEach((b) => mark(b, !chatOn && b.dataset.view === state.view));
  mark($("#nav-chat"), chatOn);
}
desktop.addEventListener("change", () => renderNavActive());
// The nav's sections, and the menu drawer's copies of those that don't fit the
// bottom nav on mobile (Scans, History). Picking one leaves the mobile chat page.
$$(".nav > button[data-view], .menu-views [data-view]").forEach((b) => b.addEventListener("click", () => {
  if (!desktop.matches && pane.open) toggleChatPane(false);
  if (b.closest(".menu-views")) setMenu(false);
  showView(b.dataset.view);
}));
{
  const saved = (() => { try { return localStorage.getItem("dash.view"); } catch { return null; } })();
  if (saved && $(`.nav > button[data-view="${saved}"]`)) applyView(saved);
}
