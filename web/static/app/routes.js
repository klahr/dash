// ------------------------------------------------------------------ routes
// The app's URLs, as paths a person can read, copy and share. This module
// only turns a URL into a route and a route into a URL; router.js reads the
// app's state into a route and applies a route to the app. It imports
// nothing, so any module may use it as it loads.
//
// The scheme (the server serves the app at each path: internal/applink):
//
//   /                              no place named: the last view and project (localStorage)
//   /p/{project}/{view}            a project's view: board, issues, scans, run, try,
//                                  history, goals, deploys, files (/p/{project} is its board;
//                                  run and try are the Shell page at main and at a commit;
//                                  checklist, a view Dash no longer has, opens the board)
//   /p/{project}/tasks/{id}        a task, open (over the board, after a reload)
//   /p/{project}/goals/{id}        a goal, open on Goals
//   /p/{project}/history/{sha}     a commit, open on History
//   /p/{project}/deploys/{id}      a deploy, open on Deploys
//   /p/{project}/issues/{id}       an issue, expanded on Issues
//   /planning                      Planning (the org's, not one project's)
//   /planning/cards/{id}           a Planning card, open
//
// Query parameters, on any of them:
//   board=<id>, project=<id>, tag=<tag> (any number), target=<target>, mine=1
//                                  Planning's board (left out: the default one) and
//                                  filters (/planning only)
//   sheet=settings|account|org|runners, section=<key>
//                                  a sheet open over the page, at a section of its rail
//                                  (Project settings → Board: ?sheet=settings&section=board)
//
// {project} is the project's id for a project of this Dash, and {id}@{computer}
// for one on another computer of a hub (only platform admins see those). Not
// the project's name, which can change, nor projectKey ("computer/id"), whose
// slash would need escaping.
//
// Older links, /?project=<id>&task=<id> (also goal=, issue=), still open.

export const VIEWS = ["board", "issues", "scans", "run", "try", "history", "goals", "deploys", "files", "planning"];
export const SHEETS = ["settings", "account", "org", "runners"];
const ITEMS = { tasks: "task", goals: "goal", history: "sha", issues: "issue", deploys: "deploy" };
const ITEM_VIEW = { task: "board", goal: "goals", sha: "history", issue: "issues", deploy: "deploys" };
// OLD_VIEWS are views Dash no longer has, and the view their links open.
const OLD_VIEWS = { checklist: "board" };

const dec = (s) => { try { return decodeURIComponent(s); } catch { return null; } };
const enc = encodeURIComponent;

// projectSegment is project p's part of a path: its id, with @computer for
// another computer's project.
export function projectSegment(p) {
  if (!p) return "";
  return p.prefix ? `${enc(p.id)}@${enc(p.machine)}` : enc(p.id);
}

// parseProject splits a decoded {project} segment into { id, machine }.
function parseProject(s) {
  const at = s.lastIndexOf("@");
  return at > 0 ? { id: s.slice(0, at), machine: s.slice(at + 1) } : { id: s, machine: "" };
}

// findProject is the project a route's { id, machine } names in list.
export function findProject(list, ref) {
  if (!ref?.id) return null;
  return list.find((p) => p.id === ref.id && (ref.machine ? p.machine === ref.machine : !p.prefix)) ||
    (ref.machine ? null : list.find((p) => p.id === ref.id)) || null;
}

// parse turns a URL (a Location or URL) into a route:
//   { project: {id, machine} | null, view, task, goal, sha, issue, deploy, card,
//     plBoard, plProject, plTags, plTarget, plMine, sheet, section, named }
// named is false for a plain / (no place in it: the saved one applies). A
// path that isn't the app's parses as / with named false.
export function parse(loc) {
  const q = new URLSearchParams(loc.search);
  const r = { project: null, view: null, task: null, goal: null, sha: null, issue: null, deploy: null, card: null,
    plBoard: "", plProject: null, plTags: [], plTarget: "", plMine: false, sheet: null, section: null, named: false };
  const sheet = q.get("sheet");
  if (SHEETS.includes(sheet)) { r.sheet = sheet; r.section = q.get("section"); }
  const parts = loc.pathname.split("/").slice(1).map(dec);
  if (parts.some((s) => s === null)) return r;
  if (parts[0] === "planning" && (parts.length === 1 || (parts.length === 3 && parts[1] === "cards" && parts[2]))) {
    r.named = true;
    r.view = "planning";
    r.card = parts[2] || null;
    r.plBoard = q.get("board") || "";
    r.plProject = q.get("project") || "";
    r.plTags = q.getAll("tag").filter(Boolean);
    r.plTarget = q.get("target") || "";
    r.plMine = q.get("mine") === "1";
    return r;
  }
  if (parts[0] === "p" && parts[1] && parts.length <= 4) {
    r.project = parseProject(parts[1]);
    r.named = true;
    if (parts.length === 2) r.view = "board";
    else if (parts.length === 3 && VIEWS.includes(parts[2]) && parts[2] !== "planning") r.view = parts[2];
    else if (parts.length === 3 && Object.hasOwn(OLD_VIEWS, parts[2])) r.view = OLD_VIEWS[parts[2]];
    else if (parts.length === 4 && ITEMS[parts[2]] && parts[3]) {
      const k = ITEMS[parts[2]];
      r[k] = parts[3];
      r.view = ITEM_VIEW[k];
    } else { r.project = null; r.named = false; }
    return r;
  }
  // An older link: /?project=<id>&task=<id> (goal=, issue=).
  if (loc.pathname === "/" && q.get("project")) {
    r.project = { id: q.get("project"), machine: "" };
    r.named = true;
    r.task = q.get("task");
    r.goal = !r.task && q.get("goal") || null;
    r.issue = !r.task && !r.goal && q.get("issue") || null;
    r.view = r.goal ? "goals" : r.issue ? "issues" : "board";
  }
  return r;
}

// href is the URL (path and query) of route r, with p the project it is in
// (a project object; null for none).
export function href(r, p) {
  const q = new URLSearchParams();
  let path = "/";
  if (r.view === "planning") {
    path = "/planning" + (r.card ? `/cards/${enc(r.card)}` : "");
    if (r.plBoard) q.set("board", r.plBoard);
    if (r.plProject) q.set("project", r.plProject);
    for (const t of r.plTags || []) q.append("tag", t);
    if (r.plTarget) q.set("target", r.plTarget);
    if (r.plMine) q.set("mine", "1");
  } else if (p) {
    const base = `/p/${projectSegment(p)}`;
    if (r.task) path = `${base}/tasks/${enc(r.task)}`;
    else if (r.goal) path = `${base}/goals/${enc(r.goal)}`;
    else if (r.sha) path = `${base}/history/${enc(r.sha)}`;
    else if (r.issue) path = `${base}/issues/${enc(r.issue)}`;
    else if (r.deploy) path = `${base}/deploys/${enc(r.deploy)}`;
    else path = `${base}/${r.view || "board"}`;
  }
  if (r.sheet) {
    q.set("sheet", r.sheet);
    if (r.section) q.set("section", r.section);
  }
  const query = q.toString(); // not q.size: newer than some browsers Dash runs in
  return path + (query ? `?${query}` : "");
}

// place is what of a URL Back steps between: the page and which kind of
// thing is open on it, and the sheet; not which task, goal, commit or card
// it is (stepping from one to the next replaces), nor filters or a sheet's
// section.
export function place(url) {
  const u = new URL(url, "http://x");
  const parts = u.pathname.split("/");
  const item = (parts[1] === "p" && parts.length === 5) || (parts[1] === "planning" && parts.length === 4);
  return (item ? parts.slice(0, -1).join("/") + "/*" : u.pathname) + "?" + (u.searchParams.get("sheet") || "");
}

// The URL the page was opened at, as a route: read before anything changes it.
export const initial = parse(location);

// changed tells the router (router.js) that what the page shows changed:
// mode "push" (Back returns from it), "replace" (a small change) or
// "close" (a pane or sheet closed). An event, so modules that Planning's
// own page (planning-page.js) shares need not load the app's router.
export function changed(mode = "push") {
  document.dispatchEvent(new CustomEvent("dash:route", { detail: mode }));
}

// linkTo is the full URL of route r in project p, to copy and share.
export const linkTo = (r, p = null) => location.origin + href(r, p);
