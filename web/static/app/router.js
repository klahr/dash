import { $ } from "./dom.js";
import { cp, state } from "./state.js";
import { fail, railOf, toast } from "./dialogs.js";
import { SHEETS, findProject, href, initial, parse, place } from "./routes.js";
import { showView } from "./nav.js";
import { selectProject } from "./projects.js";
import { projectKey } from "./events.js";
import { closeDetail, openDetail } from "./detail.js";
import { closeGoal, gp, loadGoals, openGoal } from "./goals.js";
import { closeDeploy, dp, loadDeploys, openDeploy } from "./deploys.js";
import { closeCommit, selectCommit } from "./history.js";
import { showIssue } from "./issues.js";
import { cardError, cardId, closeCard, openCard, planningFilters, setPlanningFilters } from "./planning.js";
import { currentBoard, showBoard } from "./boards.js";
import { openSettings } from "./settings.js";
import { ACCOUNT_MOVED, openAccount } from "./accountsheet.js";
import { openOrg } from "./account.js";
import { openRunners } from "./runners.js";

// ------------------------------------------------------------------ router
// Keeps the address bar on what the page shows, so Back and Forward step
// through it and a copied URL opens the same place for someone else. The
// URLs themselves are in routes.js (the scheme is written down there).
//
// Modules say that something changed with routes.js's changed(mode), an
// event this listens to:
//   "push"     somewhere Back should return from: another view or project,
//              a task, goal, commit, card or sheet opened
//   "replace"  a small change: a filter, the board shown, a sheet's section
//   "close"    a pane or sheet closed: back to the entry under it if that is
//              where it was opened from (so Back won't open it again), else
//              the entry becomes the page without it
// Several in one turn make one entry; a push to the same place (another
// task while one is open: see place()) replaces. Back and Forward (popstate)
// apply the URL without pushing again.

// var: other modules can send changes before this one has run (they are
// ignored until start).
var rt;
function R() { return rt ??= { ready: false, starting: false, applying: 0, push: false, close: false, timer: 0 }; }

document.addEventListener("dash:route", (e) => {
  const s = R();
  // While a URL is being applied, the changes are its own (or the user's
  // in the meantime: settled when it is done, in apply).
  if (!s.ready || s.applying) return;
  if (e.detail === "push") s.push = true;
  else if (e.detail === "close") s.close = true;
  if (!s.timer) s.timer = setTimeout(flush);
});

// current is the route of what the page shows now.
function current() {
  const r = { view: state.view, task: null, goal: null, sha: null, issue: null, deploy: null, card: null, board: null, plProject: "", plTag: "", sheet: null, section: null };
  if (state.view === "planning") {
    r.card = cardId();
    const f = planningFilters();
    r.plProject = f.project;
    r.plTag = f.tag;
  } else {
    if (state.detail) r.task = state.detail;
    else if (state.view === "goals" && gp.open) r.goal = gp.open;
    else if (state.view === "history" && cp.sha) r.sha = cp.sha;
    else if (state.view === "deploys" && dp.open) r.deploy = dp.open;
    r.board = currentBoard() || null;
  }
  // The sheet on top, if several are open (Runners can open over Settings).
  for (const k of [...SHEETS].reverse()) {
    const d = $(`#${k}-sheet`);
    if (!d?.open) continue;
    r.sheet = k;
    r.section = k === "runners" ? null : railOf(d.id)?.current || null;
    break;
  }
  return r;
}

// hrefNow is the URL of what the page shows now.
export const hrefNow = () => href(current(), state.project);

function flush() {
  const s = R();
  const { push, close } = s;
  s.timer = 0;
  s.push = s.close = false;
  if (!s.ready || s.applying) return;
  const url = hrefNow();
  const here = location.pathname + location.search;
  if (url === here) return;
  const st = history.state;
  if (!push && close && st?.dash && st.back === url) { history.back(); return; }
  if (!push || place(url) === place(here)) history.replaceState(st, "", url);
  else history.pushState({ dash: 1, back: here }, "", url);
}

// start applies the URL the page was opened at, once the first project list
// is in (projects.js), and from then on keeps the URL current. A plain /
// keeps the last view and project (localStorage) and shows their URL.
export async function start() {
  const s = R();
  if (s.ready || s.starting) return;
  s.starting = true;
  const shut = { task: null, goal: null, sha: null, card: null, sheet: null, section: null };
  // A link to something open (a task, goal, commit, card, sheet): its page
  // without it goes under it, so Back (a phone's too) closes it in Dash.
  // Before it opens: a Back while it loads must not leave Dash.
  const p = initial.project ? findProject(state.projects, initial.project) : state.project;
  let pushed = false;
  if (initial.named && (p || !initial.project)) {
    const under = href({ ...initial, ...shut }, p), url = href(initial, p);
    if (under !== url) {
      history.replaceState({ dash: 1 }, "", under);
      history.pushState({ dash: 1, back: under }, "", url);
      pushed = true;
    }
  }
  try {
    if (initial.named) await apply(initial, { startup: true });
  } catch (e) {
    fail(e);
  } finally {
    s.ready = true;
  }
  // Back (or Forward) while the link opened: show where that went.
  if (s.popped) { s.popped = false; const r = parse(location); if (r.named) await apply(r).catch(fail); return; }
  const url = hrefNow();
  const under = href({ ...current(), ...shut }, state.project);
  if (pushed) {
    // Its URL as the page has it now (what couldn't open drops out).
    if (url === history.state?.back) history.back();
    else history.replaceState(history.state, "", url);
  } else if (under !== url) {
    history.replaceState({ dash: 1 }, "", under);
    history.pushState({ dash: 1, back: under }, "", url);
  } else history.replaceState({ dash: 1 }, "", url);
}

window.addEventListener("popstate", () => {
  const s = R();
  if (!s.ready) { s.popped = s.starting; return; }
  const r = parse(location);
  if (r.named) apply(r).catch(fail);
});

// go opens url (a link into the app, as a notification's) in this page,
// as a new entry.
export async function go(url) {
  const r = parse(new URL(url, location.origin));
  if (!r.named) return;
  await apply(r, { settle: false });
  const s = R();
  s.push = true;
  flush();
}

const SHEET_ID = (k) => `${k}-sheet`;

// apply makes the page show route r, without adding history entries:
// the URL is already r (Back, Forward), or the caller writes it (settle false).
async function apply(r, { startup = false, settle = !startup } = {}) {
  const s = R();
  s.applying++;
  const missing = [];
  try {
    if (r.project) {
      const p = findProject(state.projects, r.project);
      if (!p) {
        toast("That project isn't on this dashboard, or you can't see it.", { tone: "error" });
        if (r.view !== "planning") r = { ...r, view: startup ? state.view : r.view, task: null, goal: null, sha: null, issue: null, deploy: null, sheet: null };
      } else if (projectKey(p) !== projectKey(state.project)) await selectProject(p);
    }
    for (const k of SHEETS) {
      const d = $(`#${SHEET_ID(k)}`);
      if (d?.open && k !== r.sheet) d.close();
    }
    // A task opens over the view it was opened on; a link to one, over the board.
    let view = r.view;
    if (r.task && !startup && state.view !== "planning") view = state.view;
    if (view && view !== state.view) showView(view, { now: true });
    if (view === "board" && !r.task) showBoard(r.board || "");
    if (view === "planning") setPlanningFilters(r.plProject || "", r.plTag || "");

    if (r.task) {
      if (state.detail !== r.task) {
        if (state.todos.has(r.task)) await openDetail(r.task);
        else { closeDetail(); missing.push("task"); }
      }
    } else if (state.detail) closeDetail();

    if (r.goal) {
      if (gp.open !== r.goal) {
        if (!state.goals.has(r.goal)) await loadGoals().catch(() => {});
        if (state.goals.has(r.goal)) await openGoal(r.goal);
        else missing.push("goal");
      }
    } else if (gp.open) closeGoal();

    if (r.deploy) {
      if (dp.open !== r.deploy) {
        if (!dp.loaded || dp.project?.id !== state.project?.id) await loadDeploys().catch(() => {});
        if (dp.list.some((d) => d.id === r.deploy)) await openDeploy(r.deploy);
        else missing.push("deploy");
      }
    } else if (dp.open) closeDeploy();

    if (r.sha) {
      if (cp.sha !== r.sha) {
        await selectCommit(r.sha);
        if (cp.error) { closeCommit(); missing.push("commit"); }
      }
    } else if (cp.sha) closeCommit();

    if (r.issue && !(await showIssue(r.issue))) missing.push("issue");

    if (r.card) {
      if (cardId() !== r.card) {
        await openCard(r.card);
        if (cardError()) { closeCard(); missing.push("card"); }
      }
    } else if (cardId()) closeCard();

    if (r.sheet) await openSheet(r.sheet, r.section);
  } finally {
    s.applying--;
  }
  if (missing.length) toast(`That ${missing[0]} isn't here any more, or you can't see it.`, { tone: "error" });
  if (!settle || s.applying) return; // start() and go() write the URL
  // What couldn't open drops out of the URL; something closed while it
  // loaded (Esc on a card still coming in) goes back, as a close does.
  const url = hrefNow();
  if (url === location.pathname + location.search) return;
  if (!missing.length && history.state?.dash && history.state.back === url) history.back();
  else history.replaceState(history.state, "", url);
}

// Sections that moved to another place in the Organization sheet: from
// Account (the wall view, Claude status) or merged into Sign-in.
const ORG_MOVED = { wall: "wall", claudestatus: "claudestatus", access: "signin", sso: "signin" };

// openSheet opens sheet k (one of SHEETS) at its section, if it names one.
async function openSheet(k, section) {
  const d = $(`#${SHEET_ID(k)}`);
  try {
    if (k === "settings") {
      if (!state.project) return;
      if (!d.open) openSettings();
      if (section) railOf(d.id)?.show(section);
    } else if (k === "account" && ORG_MOVED[section]) {
      // Moved to Organization: an old link opens it there.
      await openOrg();
      railOf("org-sheet")?.show(ORG_MOVED[section]);
    } else if (k === "account") {
      if (!d.open || (section && railOf(d.id)?.current !== (ACCOUNT_MOVED[section] || section))) await openAccount(section || undefined);
    } else if (k === "org") {
      section = ORG_MOVED[section] || section;
      if (!d.open) await openOrg();
      if (section) railOf(d.id)?.show(section);
    } else if (k === "runners") {
      if (!d.open) await openRunners(section || undefined);
    }
  } catch (e) {
    fail(e);
  }
}

// The sheets the URL names: opening one is a step Back returns from,
// another section in it is not, closing it goes back.
for (const k of SHEETS) {
  const d = $(`#${SHEET_ID(k)}`);
  if (!d) continue;
  d.addEventListener("close", () => document.dispatchEvent(new CustomEvent("dash:route", { detail: "close" })));
  d.addEventListener("railchange", () => document.dispatchEvent(new CustomEvent("dash:route", { detail: "replace" })));
}
