// Starred projects: the sidebar's PROJECTS list shows the ones the user
// starred first and folds the rest away (projects.js renders it). Stars are
// the person's, kept on the server (GET/PUT/DELETE /api/me/starred-projects),
// so they follow them to every browser. Whether Other projects is open is
// this browser's view choice only (localStorage).
import { starKey } from "../groups.js";
import { api } from "./api.js";
import { state } from "./state.js";
import * as store from "./store.js";

// state.stars is the Set of starred starKeys, or null where there are no
// stars (not loaded, or no person behind the session: the list is plain).
state.stars = null;

const PATH = "/api/me/starred-projects";

// loadStars fetches the stars, from the Dash serving the page (never a
// runner's: stars are the hub's, about all the projects it shows).
export async function loadStars() {
  try {
    const res = await api("GET", PATH, null, "");
    state.stars = new Set(res.projects || []);
  } catch {
    state.stars = null;
  }
}

export const starsOn = () => state.stars instanceof Set;
export const isStarred = (p) => !!p && starsOn() && state.stars.has(starKey(p));

// setStar stars or unstars p at once, calling render, and saves it; if
// saving fails it puts the star back as it was, renders again and throws.
export async function setStar(p, on, render) {
  if (!starsOn() || !p) return;
  const key = starKey(p);
  const was = state.stars.has(key);
  if (was === on) return;
  const apply = (v) => { if (v) state.stars.add(key); else state.stars.delete(key); render(); };
  apply(on);
  try {
    await api(on ? "PUT" : "DELETE", `${PATH}/${encodeURIComponent(key)}`, null, "");
  } catch (e) {
    apply(was);
    throw new Error(`Couldn't ${on ? "star" : "unstar"} ${p.name}: ${e.message}`);
  }
}

// Other projects is folded unless the user opened it in this browser.
const OPEN = "dash.otherProjectsOpen";
export const othersOpen = () => store.flag(OPEN);
export const setOthersOpen = (open) => store.setFlag(OPEN, open);
