import { api } from "./api.js";
import { state } from "./state.js";
import * as store from "./store.js";

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
