import * as store from "./store.js";


// ------------------------------------------------------------------ state
export const LANES = [
  { key: "draft", label: "Drafts", statuses: ["draft"], empty: "Drafts wait here until you queue them." },
  { key: "queued", label: "Queued", statuses: ["queued"], empty: "Nothing waiting. New tasks start here." },
  { key: "in_progress", label: "Running", statuses: ["in_progress"], empty: "No agents working right now." },
  { key: "review", label: "Review", statuses: ["review", "failed"], empty: "Nothing to review. Finished and failed work lands here." },
  { key: "merging", label: "Merging", statuses: ["merging"], empty: "The merge queue is clear." },
  { key: "done", label: "Done", statuses: ["merged"], empty: "Merged tasks show up here." },
];
export const STATUS_LABEL = { draft: "Draft", queued: "Queued", in_progress: "Running", review: "Ready for review", merging: "Merging", merged: "Merged", failed: "Failed" };

// The project's Try session (see "try" below).
export const tryState = {
  pid: null, loaded: false, runners: [], disabled: "", session: null,
  runner: "", // the runner picked (the only one, or the session's)
  starting: "", // what is being checked out, while it is
  ws: null, proc: null, refused: null, got: false,
};
export const state = {
  projects: [],
  projectsLoaded: false, // the board's empty states wait for the first project list
  project: null,
  todos: new Map(),
  goals: new Map(),   // the project's goals by id, for their tasks' chips
  goalFilter: null,   // the board shows only this goal's tasks (null: every task)
  lastLine: new Map(),
  activity: new Map(), // todo id → what it is busy with right now (see trackActivity)
  lane: null,         // mobile: selected lane key
  follow: null,       // { ids, lane, until }: new tasks the selected lane follows for a moment
  detail: null,        // open todo id
  pane: "timeline",
  diff: "",
  diffOpen: new Set(), // diff files the user expanded in the open todo
  procs: [],
  previews: new Map(), // "machine/todo id" → live preview, on every computer
  perms: [],           // pending permission requests for this project
  pushOn: false,       // this browser gets the hub's Web Push notifications
  usage: null,
  view: "board",
  showOld: false,      // show merged todos older than a day
  unpushed: 0,         // commits on the default branch origin doesn't have
  unpushedTodos: null, // ids of merged todos origin doesn't have yet (null until known)
  comments: loadComments(), // todo id → [{file, line, side, code, text}] (diff review notes)
  collapsed: new Set(arrayOf(store.getJSON("dash.collapsed"))), // desktop: minimized lane keys
};
export const DAY = 24 * 3600 * 1000;
// The History graph as last drawn, and the commit its pane shows (see "commit pane").
export const hist = { commits: [], byHash: new Map(), remote: "" };
// file: the path selected in the desktop file list; texts: the open commit's loaded diffs.
export const cp = { sha: null, project: null, detail: null, error: "", opener: null, diffs: null, file: null, texts: new Map(), msgOpen: false, listFocus: false };

// arrayOf keeps stored data that isn't a list (stale or tampered) from breaking startup.
function arrayOf(v) { return Array.isArray(v) ? v : []; }
function loadComments() {
  return store.getJSON("dash.comments", {});
}
export function saveComments() {
  store.setJSON("dash.comments", state.comments);
}
