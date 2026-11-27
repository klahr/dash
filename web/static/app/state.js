import * as store from "./store.js";


// ------------------------------------------------------------------ state
export const LANES = [
  { key: "draft", label: "Drafts", statuses: ["draft"] },
  { key: "queued", label: "Queued", statuses: ["queued"] },
  { key: "in_progress", label: "Running", statuses: ["in_progress"] },
  { key: "review", label: "Review", statuses: ["review", "failed"] },
  { key: "merging", label: "Merging", statuses: ["merging"] },
  { key: "done", label: "Merged", statuses: ["merged"] },
];
export const STATUS_LABEL = { draft: "Draft", queued: "Queued", in_progress: "Running", review: "Ready for review", merging: "Merging", merged: "Merged", failed: "Failed" };
// statusLabel is task t's status in words: one closed without changes is
// Merged on the server, but nothing was merged.
export const statusLabel = (t) => t.status === "merged" && t.no_changes && !t.merged_commit ? "Closed · no changes" : STATUS_LABEL[t.status] || t.status;

// The project's Try session (see "try" below).
export const tryState = {
  pid: null, loaded: false, runners: [], disabled: "", session: null,
  webDisabled: "", // why a commit preview (a web project's Run, History's Preview) can't run
  candidates: [], // every runner working on the project, with why it can't run it (the Run button's list)
  default: "", // the runner picked when only one can
  stopAsked: false, // Terminate was pressed while it was starting
  runner: "", // the runner picked (the only one, or the session's)
  starting: "", // what is being checked out, while it is
  ws: null, proc: null, refused: null,
  pending: null, // { ref, runner }: a commit picked before the Shell page showed it
};
export const state = {
  projects: [],
  projectsLoaded: false, // the board's empty states wait for the first project list
  project: null,
  todos: new Map(),
  todosOf: null, // the project whose todos are loaded (app/board.js)
  goals: new Map(),   // the project's goals by id, for their tasks' chips
  goalFilter: null,   // the board shows only this goal's tasks (null: every task)
  mine: false,        // the board shows only the tasks the user created (see people.js)
  lastLine: new Map(),
  activity: new Map(), // todo id → what it is busy with right now (see trackActivity)
  lane: null,         // mobile: selected lane key
  follow: null,       // { ids, lane, until }: new tasks the selected lane follows for a moment
  detail: null,        // open todo id
  pane: "timeline",
  diff: "",
  diffSince: null,    // what the open todo's Changes shows back in Review after a rework (review.js fetchChanges), with its todo id
  diffOpen: new Set(), // diff files the user expanded in the open todo
  procs: [],
  previews: new Map(), // "machine/todo id" → live preview, on every computer
  perms: [],           // pending permission requests for this project
  goalOpen: null,      // id of the goal open in its pane
  needs: { list: [], loaded: false, open: null, sel: null }, // Needs you (needs.js): its items, the one open and the selected row
  pushOn: false,       // this browser gets the hub's Web Push notifications
  pushEvents: null,    // Set of the events the user wants pushed (Account → Notifications); null: all
  pushTasks: null,     // whose task events: "mine" or "all" (Account → Notifications → Tasks); null: all
  usage: null,
  view: "board",
  showOld: false,      // show merged todos older than a day
  unpushed: 0,         // commits on the default branch origin doesn't have
  unpushedTodos: null, // ids of merged todos origin doesn't have yet (null until known)
  collapsed: new Set(arrayOf(store.getJSON("dash.collapsed"))), // desktop: minimized lane keys
};
// projectFirst says what to do when an action needs a project and none is
// selected: with none at all, selecting one can't help. hint: e.g. " (p)".
export const projectFirst = (hint = "") => (state.projects.length ? `Select a project first${hint}.` : "Create a project first.");
export const DAY = 24 * 3600 * 1000;
// The History graph as last drawn, and the commit its pane shows (see "commit pane").
export const hist = { commits: [], byHash: new Map(), remote: "", branch: "", live: new Map() }; // live: commit → the environments it is live in; branch: the graph's
// file: the path selected in the desktop file list; texts: the open commit's loaded diffs.
export const cp = { sha: null, project: null, detail: null, error: "", diffs: null, file: null, texts: new Map(), msgOpen: false, listFocus: false };

// arrayOf keeps stored data that isn't a list (stale or tampered) from breaking startup.
function arrayOf(v) { return Array.isArray(v) ? v : []; }
