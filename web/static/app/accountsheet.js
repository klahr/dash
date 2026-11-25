import { $ } from "./dom.js";
import * as store from "./store.js";
import { attempt, railOf } from "./dialogs.js";
import { changed } from "./routes.js";

// ------------------------------------------------------------------ the Account sheet
// The user's own settings (preferences, voice, profile, notifications,
// sign-in, Connect Claude and tokens) are sections down one rail sheet, like
// Settings. A section merged into another (Dictation into Voice…) is named
// in its entry's data-rail-also, so links and URLs to it still work.
// Each module registers how its section loads (several may, for one
// section); the account menu's Account item opens the sheet at the section
// last shown (kept in localStorage), the palette and :commands open it at a
// given section, and picking another on the rail loads that one.
const loaders = new Map();
export function accountSection(key, load) { loaders.set(key, [...(loaders.get(key) || []), load]); }
const loadSection = (key) => Promise.all((loaders.get(key) || []).map((load) => load()));

const sheet = $("#account-sheet");
const LAST = "dash.accountSection";
const lastSection = () => store.get(LAST, "general");
const remember = (key) => store.set(LAST, key);
let opening = false;

// openAccount shows the sheet at section key (the last one shown, without
// one), loaded (again, if it is open).
export async function openAccount(key = lastSection()) {
  opening = true;
  try { railOf("account-sheet").show(key); } finally { opening = false; }
  const current = railOf("account-sheet").current;
  remember(current);
  await loadSection(current);
  if (!sheet.open) sheet.showModal();
  changed("push");
}

sheet.addEventListener("railchange", (e) => {
  if (!sheet.open || opening) return;
  remember(e.detail);
  if (loaders.has(e.detail)) attempt(() => loadSection(e.detail))();
});

// ------------------------------------------------------------------ the Organization sheet
// Most of it is drawn as it opens (account.js); a section that loads on its
// own (the wall view, Claude status) registers here and loads when picked.
// Several may load one section (Billing & usage).
const orgLoaders = new Map();
export function orgSection(key, load) { orgLoaders.set(key, [...(orgLoaders.get(key) || []), load]); }
const loadOrg = (key) => Promise.all((orgLoaders.get(key) || []).map((load) => load()));
const orgSheet = document.getElementById("org-sheet"); // not on the Planning page
// loadOrgSection loads the Organization sheet's section shown now.
export async function loadOrgSection() { await loadOrg(railOf("org-sheet")?.current); }
orgSheet?.addEventListener("railchange", (e) => {
  if (!orgSheet.open) return;
  if (orgLoaders.has(e.detail)) attempt(() => loadOrg(e.detail))();
});
