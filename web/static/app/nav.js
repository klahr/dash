import { $, $$ } from "./dom.js";
import * as store from "./store.js";
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
import { closeGoal, loadGoals } from "./goals.js";
import { closeCard, planningShown } from "./planning.js";
import { changed, initial } from "./routes.js";

// ------------------------------------------------------------------ navigation
// Shell is one page with two places, main (view "run") and a commit (view
// "try", Try's old name and link): both show #view-run under the nav's Shell.
// (A function declaration: modules in an import cycle call into nav.js before it runs.)
export function navView(name) { return name === "try" ? "run" : name; }

// now: at once, without the transition (Back and Forward: router.js).
export function showView(name, { now = false } = {}) {
  const apply = () => applyView(name);
  if (!now && name !== state.view && document.startViewTransition && !reduceMotion.matches) {
    document.startViewTransition(apply);
    return;
  }
  apply();
}

function applyView(name) {
  state.view = name;
  store.set("dash.view", name);
  renderNavActive();
  $$(".view").forEach((v) => v.classList.toggle("active", v.id === `view-${navView(name)}`));
  $("#view-run").classList.toggle("at-commit", name === "try");
  $("#shell-at-main").setAttribute("aria-pressed", String(name === "run"));
  $("#shell-at-commit").setAttribute("aria-pressed", String(name === "try"));
  if (name === "run") requestAnimationFrame(() => { fitTerm(); ensureShell(); });
  if (name === "history") loadGraph().catch(fail);
  else closeCommit();
  if (name === "goals" && state.project) loadGoals().catch(fail);
  else closeGoal();
  if (name === "planning") planningShown();
  else closeCard();
  if (name === "files" && state.project) loadFiles().catch(fail); // at startup the project (and the files code below) isn't ready yet
  if (name === "checklist" && state.project) loadChecklist().catch(fail);
  else if (state.project && typeof cl !== "undefined" && cl.pending.length) flushChecklist().catch(fail); // leaving it saves at once
  if (name === "issues" && state.project) loadIssues().catch(fail);
  if (name === "scans" && state.project) loadScans().catch(fail);
  if (name === "try") requestAnimationFrame(() => enterTry().catch(fail));
  else leaveTry();
  renderTryBack();
  changed("push");
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
  $$(".nav > button[data-view], .menu-views [data-view]").forEach((b) => mark(b, !chatOn && b.dataset.view === navView(state.view)));
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
// The view a link names (routes.js), else the one shown last.
{
  const linked = initial.named && initial.view;
  const saved = linked || store.get("dash.view");
  if (saved && $(linked ? `#view-${navView(saved)}` : `.nav > button[data-view="${saved}"]`)) applyView(saved);
}
