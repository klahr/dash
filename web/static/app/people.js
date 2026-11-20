import { api } from "./api.js";
import { state } from "./state.js";
import * as store from "./store.js";
import { ask } from "./dialogs.js";

// ------------------------------------------------------------------ the org's people
// The org's members, by user id: their username and picture's version, from
// GET /api/planning/people (every role may read it, and it has no emails).
// The Board shows who created each task with them, and offers the Mine filter
// once the org has more than one member.
export const people = { byId: new Map(), loaded: false, asked: new Set(), onChange: () => {} };

let loading = null;
export function loadPeople() {
  // Hub only: the org's members aren't a project's, wherever it runs.
  loading ??= api("GET", "/api/planning/people", null, "").then((list) => {
    people.byId = new Map((Array.isArray(list) ? list : []).map((u) => [u.id, u]));
    people.loaded = true;
    state.mine = store.flag(mineKey());
    people.onChange();
  }).finally(() => { loading = null; });
  return loading;
}

// multiMember: the org has more than one member, so who made a task matters.
export const multiMember = () => people.byId.size > 1;

// creator is the member who created task t ({id, username, avatar}), or null
// when Dash made it (0), their account was deleted (-1) or they have left
// the org. Someone not in the list asks for it again, once: they may have
// joined since it was loaded.
export function creator(t) {
  const id = t?.created_by;
  if (!id || id < 0) return null;
  const u = people.byId.get(id);
  if (!u && people.loaded && !people.asked.has(id)) {
    people.asked.add(id);
    loadPeople().catch(() => {});
  }
  return u || null;
}

// startsRunAs reports whether saving changes to task t (the fields
// PATCH /api/todos/{id} takes), or queuing it, starts its run for the
// signed-in member while it is another member's: the hub then makes them
// its run_by (store.startsRun), so the run is for both of them and only a
// shared runner takes it, not the creator's own.
function startsRunAs(t, changes, queuing) {
  const me = myID();
  if (!t || !me || !multiMember() || !(t.created_by > 0) || t.created_by === me || t.run_by === me) return false;
  if (queuing) return true;
  if (t.status !== "queued" || t.paused) return false; // a draft's run starts when it is queued
  const had = (t.attachments || []).map((a) => a.id);
  return ("title" in changes && changes.title !== t.title) ||
    ("prompt" in changes && changes.prompt !== t.prompt) ||
    ("builds_on" in changes && (changes.builds_on || "") !== (t.builds_on || "")) ||
    ("attachments" in changes && (changes.attachments.length !== had.length || changes.attachments.some((id, i) => id !== had[i])));
}

// confirmRunAs asks before such a change (startsRunAs) of another member's
// task. Resolves true to go on: also when there is nothing to ask.
export async function confirmRunAs(t, changes = {}, queuing = false) {
  if (!startsRunAs(t, changes, queuing)) return true;
  const name = creator(t)?.username || "its creator";
  return !!await ask({
    title: `Run ${name}'s task as yours too?`,
    body: `Then the task is ${name}'s and yours, so only a shared runner (one that takes other members' tasks) runs it, not ${name}'s own runner.`,
    ok: queuing ? "Queue" : "Save",
  });
}

// ---- the Board's Mine filter: only the tasks the signed-in user created.
// Kept per user in this browser.
const mineKey = () => `dash.mine.${state.me?.user?.id ?? 0}`;
export const myID = () => state.me?.user?.id || 0;
// mineOn: the filter is on and applies (it is hidden, and off, in an org of one).
export const mineOn = () => !!state.mine && multiMember() && !!myID();
export function setMine(on) {
  state.mine = on;
  store.setFlag(mineKey(), on);
}
