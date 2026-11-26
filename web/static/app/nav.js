import { $, $$ } from "./dom.js";
import * as store from "./store.js";
import { chatBeside, desktop } from "./viewport.js";
import { fail, reduceMotion } from "./dialogs.js";
import { state } from "./state.js";
import { applyBrand, setMenu } from "./projects.js";
import { pane, renderTryBack, toggleChatPane } from "./chatpane.js";
import { ensureShell, fitTerm } from "./terminal.js";
import { enterTry, leaveTry } from "./try.js";
import { closeCommit, loadGraph } from "./history.js";
import { loadFiles } from "./files.js";
import { openScans } from "./scans.js";
import { closeGoal, loadGoals } from "./goals.js";
import { closeDeploy, loadDeploys } from "./deploys.js";
import { closeCard, planningShown } from "./planning.js";
import { changed, initial } from "./routes.js";
import { boardSeen } from "./branches.js";
import { closeDetail, syncDetailMode } from "./detail.js";
import { projectKey } from "./events.js";

// ------------------------------------------------------------------ navigation
// The nav's pages: Board, Goals, History, Code (and Planning, the org's).
// Some have tabs, each a view of its own (state.view, its URL in routes.js):
//   History  Commits (view "history") and Deploys ("deploys")
//   Code     Files ("files") and Shell; the Shell tab has two places, main
//            ("run") and a commit ("try", Try's old name and link), both
//            #view-run
// Scans, once a page, are the Board's From scans filter and the Scans sheet
// (scans.js); "scans" still opens there, for old links and keys.
// (Function declarations: modules in an import cycle call into nav.js before it runs.)
export function navView(name) {
  if (name === "files" || name === "run" || name === "try") return "code";
  if (name === "deploys") return "history";
  return name;
}
// tabOf is the page tab view name shows under (the Shell tab for both its places).
export function tabOf(name) { return name === "try" ? "run" : name; }
// sectionOf is the view's <section> (#view-…).
function sectionOf(name) { return name === "try" ? "run" : name; }

// Code opens on the tab it showed last (Files at first).
const CODE_TAB = "dash.codeTab";
function codeTab() { return store.get(CODE_TAB) === "run" ? "run" : "files"; }

// resolve is the view a name opens: a page (code) opens its tab, an old
// page (scans) the place it moved to.
function resolve(name) {
  if (name === "code") return codeTab();
  if (name === "scans") return "board";
  return name;
}

// now: at once, without the transition (Back and Forward: router.js).
export function showView(name, { now = false } = {}) {
  if (name === "scans") { showView("board", { now }); openScans().catch(fail); return; }
  name = resolve(name);
  const apply = () => applyView(name);
  if (!now && name !== state.view && document.startViewTransition && !reduceMotion.matches) {
    document.startViewTransition(apply);
    return;
  }
  apply();
}

// The views a project can open in (projectView), and the pages the nav has.
const PROJECT_VIEWS_OK = ["board", "goals", "history", "deploys", "files", "run"];

// Each project remembers the view it was last shown in (localStorage), so
// switching projects returns to it (projects.js: selectProject). Planning is
// the organization's, across projects, so it isn't one of them; a commit in
// Shell (try) is that project's, so it is kept as Shell. A view Dash no
// longer has (scans) opens where it moved to.
const PROJECT_VIEWS = "dash.projectViews";
export function projectView(p) {
  const v = p && store.getJSON(PROJECT_VIEWS, {})[projectKey(p)];
  const r = v && resolve(v);
  return r && PROJECT_VIEWS_OK.includes(r) ? r : null;
}
function rememberView(name) {
  if (!state.project || name === "planning") return;
  const views = store.getJSON(PROJECT_VIEWS, {});
  views[projectKey(state.project)] = tabOf(name);
  store.setJSON(PROJECT_VIEWS, views);
}

function applyView(name) {
  const prev = state.view;
  state.view = name;
  store.set("dash.view", name);
  rememberView(name);
  if ((prev === "planning") !== (name === "planning")) applyBrand(); // Planning is across projects: the default brand
  if (navView(name) === "code") store.set(CODE_TAB, tabOf(name));
  renderNavActive();
  $$(".view").forEach((v) => v.classList.toggle("active", v.id === `view-${sectionOf(name)}`));
  $(".app").classList.toggle("on-goals", name === "goals"); // the chat lies beside the page there (style.css)
  $("#view-run").classList.toggle("at-commit", name === "try");
  $("#shell-at-main").setAttribute("aria-pressed", String(name === "run"));
  $("#shell-at-commit").setAttribute("aria-pressed", String(name === "try"));
  if (name === "run") requestAnimationFrame(() => { fitTerm(); ensureShell(); });
  if (name === "history") loadGraph().catch(fail);
  else closeCommit();
  if (name === "goals") loadGoals().catch(fail);
  else closeGoal();
  if (name === "deploys" && state.project) loadDeploys().catch(fail);
  else closeDeploy();
  if (name === "planning") planningShown();
  else closeCard();
  if (name === "files" && state.project) loadFiles().catch(fail); // at startup the project (and the files code below) isn't ready yet
  if (name === "try") requestAnimationFrame(() => enterTry().catch(fail));
  else leaveTry();
  // A task's pane goes with the page it was opened on; on the Board it lies beside the board.
  if (state.detail && prev !== name && name !== "board") closeDetail();
  else if (state.detail) syncDetailMode();
  renderTryBack();
  changed("push");
}
// On a phone the chat is a page of its own (the nav's Chat tab): while it is
// open, Chat is the active section rather than the view under it. On a
// tablet it sits beside the view: both are lit.
// (.chat-open, not pane: applyView runs at startup before pane exists.)
export function chatPage() { return !chatBeside.matches && $(".app").classList.contains("chat-open"); }
export function renderNavActive() {
  const chatOn = chatPage();
  const mark = (b, on) => {
    b.classList.toggle("active", on);
    b.toggleAttribute("aria-current", on);
    if (on) b.setAttribute("aria-current", "page");
  };
  $$(".nav [data-view]").forEach((b) => mark(b, !chatOn && b.dataset.view === navView(state.view)));
  mark($("#nav-chat"), chatOn);
  const beside = !desktop.matches && chatBeside.matches && $(".app").classList.contains("chat-open");
  if (!chatOn) $("#nav-chat").classList.toggle("active", beside);
  $("#nav-chat").setAttribute("aria-expanded", String(chatOn || beside));
  // The page tabs (History's, Code's): the one shown.
  $$(".page-tabs [data-tab]").forEach((b) => {
    const on = b.dataset.tab === tabOf(state.view);
    b.classList.toggle("active", on);
    if (on) b.setAttribute("aria-current", "page"); else b.removeAttribute("aria-current");
  });
  boardSeen();
}
desktop.addEventListener("change", () => renderNavActive());
chatBeside.addEventListener("change", () => renderNavActive());
// The nav's pages: the bottom nav's, and on mobile the menu drawer's (those that
// don't fit the bottom nav: History, Code), which are in the side rail on desktop.
// Picking one leaves the phone's chat page (a chat beside the view stays).
$$(".nav [data-view]").forEach((b) => b.addEventListener("click", () => {
  if (!chatBeside.matches && pane.open) toggleChatPane(false);
  if (b.closest(".menu")) setMenu(false);
  showView(b.dataset.view);
}));
// A page's tabs: each opens its view (the Shell tab, from Files: main).
$$(".page-tabs [data-tab]").forEach((b) => b.addEventListener("click", () => {
  if (b.dataset.tab !== tabOf(state.view)) showView(b.dataset.tab);
}));
// The view a link names (routes.js), else the one shown last (a view Dash
// no longer has opens where it moved to).
{
  const linked = initial.named && initial.view;
  const saved = linked || store.get("dash.view");
  const v = saved && resolve(saved);
  if (v && (linked ? $(`#view-${sectionOf(v)}`) : v === "planning" || PROJECT_VIEWS_OK.includes(v))) applyView(v);
}
