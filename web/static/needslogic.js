// Needs you's list, apart from its drawing (app/needs.js), so Node tests can
// check it: which tasks and requests wait on the user, in what order, how
// live events change the list, and which row comes next once the open one
// is dealt with. GET /api/needs (internal/api/needs.go) gives the first list;
// the event stream keeps it current. Pure, with only time.js imported.
import { durationText } from "./time.js";

// A need is { kind, project_id, since, todo?, perm? }: kind is review,
// failed, question or permission; todo is the task (none for a request
// outside a task), perm the request of a question or permission.

export const KIND_LABEL = { review: "Review", failed: "Failed", question: "Question", permission: "Permission" };

// needKey names need n in the list and its URL: its request's id, else its
// task's.
export const needKey = (n) => n.perm?.id || n.todo?.id || "";

// reviewSince is since when task t, in review, has waited on a person, or
// null when it doesn't: as the server's reviewWait, a pull request open on
// GitHub waits on a person only while its waits says one has to act there.
export function reviewSince(t) {
  let since = t.finished_at || t.updated_at;
  if (t.pr?.state === "OPEN") {
    if (!t.pr.waits) return null;
    if (t.pr.waits_since && new Date(t.pr.waits_since) > new Date(since)) since = t.pr.waits_since;
  }
  return since;
}

// taskNeed is the need task t is, or null: in review (waiting on a person)
// or failed.
export function taskNeed(t) {
  if (t?.status === "review") {
    const since = reviewSince(t);
    return since ? { kind: "review", project_id: t.project_id, since, todo: t } : null;
  }
  if (t?.status === "failed") return { kind: "failed", project_id: t.project_id, since: t.finished_at || t.updated_at, todo: t };
  return null;
}

// permNeed is the need of pending request p, of task todo (if known).
export function permNeed(p, todo = null) {
  const n = { kind: p.tool === "AskUserQuestion" ? "question" : "permission", project_id: p.project_id, since: p.created, perm: p };
  if (todo) n.todo = todo;
  return n;
}

// Oldest first.
const byAge = (a, b) => new Date(a.since) - new Date(b.since);
export const sortNeeds = (needs) => [...needs].sort(byAge);

// applyEvent is needs after live event ev: a task's update (todo.updated)
// adds, moves or drops its need and refreshes the task its requests show;
// a deleted task (todo.deleted) drops its needs; a request
// (permission.updated) is added while pending and dropped once decided.
// Other events, and those of tasks that neither need you nor have requests
// listed, leave needs as they are (the same array).
export function applyEvent(needs, ev) {
  let out;
  const listed = () => needs.some((n) => (n.todo?.id || n.perm?.todo_id) === ev.todo_id);
  switch (ev.type) {
    case "todo.updated": {
      const t = ev.data;
      if (!taskNeed(t) && !listed()) return needs;
      out = needs.filter((n) => n.perm || n.todo?.id !== ev.todo_id)
        .map((n) => ((n.todo?.id || n.perm?.todo_id) === ev.todo_id ? { ...n, todo: t } : n));
      const n = taskNeed(t);
      if (n) out.push(n);
      break;
    }
    case "todo.deleted":
      if (!listed()) return needs;
      out = needs.filter((n) => n.todo?.id !== ev.todo_id);
      break;
    case "permission.updated": {
      const p = ev.data;
      out = needs.filter((n) => n.perm?.id !== p.id);
      if (!p.decision) out.push(permNeed(p, needs.find((n) => n.todo?.id === p.todo_id)?.todo));
      break;
    }
    default:
      return needs;
  }
  return sortNeeds(out);
}

// nextKey is the row to show once the list went from before to after while
// row key was open: key itself while it is still there; else the first row
// after it that is still listed (the next one to work on), else the last
// one before it, else the first of the list; null once it is empty.
export function nextKey(before, after, key) {
  const has = (k) => after.some((n) => needKey(n) === k);
  if (key && has(key)) return key;
  if (!after.length) return null;
  const i = before.findIndex((n) => needKey(n) === key);
  if (i >= 0) {
    for (const n of before.slice(i + 1)) if (has(needKey(n))) return needKey(n);
    for (const n of before.slice(0, i).reverse()) if (has(needKey(n))) return needKey(n);
  }
  return needKey(after[0]);
}

// waitedText is how long a need has waited: "<1m", "45m", "2h 15m", "3d 6h".
export const waitedText = (since, now = Date.now()) => durationText(now - new Date(since));
