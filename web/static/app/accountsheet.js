import { $ } from "./dom.js";
import { attempt, railOf } from "./dialogs.js";

// ------------------------------------------------------------------ the Account sheet
// The user's own settings (preferences, two-factor, Google, Connect Claude,
// tokens, personal webhooks, the wall view) are sections down one rail sheet,
// like Settings. Each module registers how its section loads; the account
// menu, the palette and :commands open the sheet at a section, and picking
// another on the rail loads that one.
const loaders = new Map();
export function accountSection(key, load) { loaders.set(key, load); }

const sheet = $("#account-sheet");
let opening = false;

// openAccount shows the sheet at section key, loaded (again, if it is open).
export async function openAccount(key) {
  opening = true;
  try { railOf("account-sheet").show(key); } finally { opening = false; }
  await loaders.get(railOf("account-sheet").current)?.();
  if (!sheet.open) sheet.showModal();
}

sheet.addEventListener("railchange", (e) => {
  if (!sheet.open || opening) return;
  const load = loaders.get(e.detail);
  if (load) attempt(load)();
});
