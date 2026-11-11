import { $ } from "./dom.js";
import { attempt, railOf } from "./dialogs.js";

// ------------------------------------------------------------------ the Account sheet
// The user's own settings (preferences, two-factor, Google, Connect Claude,
// tokens, personal webhooks, the wall view) are sections down one rail sheet,
// like Settings. Each module registers how its section loads; the account
// menu's Account item opens the sheet at the section last shown (kept in
// localStorage), the palette and :commands open it at a given section, and
// picking another on the rail loads that one.
const loaders = new Map();
export function accountSection(key, load) { loaders.set(key, load); }

const sheet = $("#account-sheet");
const LAST = "dash.accountSection";
const lastSection = () => { try { return localStorage.getItem(LAST) || "editors"; } catch { return "editors"; } };
const remember = (key) => { try { localStorage.setItem(LAST, key); } catch {} };
let opening = false;

// openAccount shows the sheet at section key (the last one shown, without
// one), loaded (again, if it is open).
export async function openAccount(key = lastSection()) {
  opening = true;
  try { railOf("account-sheet").show(key); } finally { opening = false; }
  const current = railOf("account-sheet").current;
  remember(current);
  await loaders.get(current)?.();
  if (!sheet.open) sheet.showModal();
}

sheet.addEventListener("railchange", (e) => {
  if (!sheet.open || opening) return;
  remember(e.detail);
  const load = loaders.get(e.detail);
  if (load) attempt(load)();
});
