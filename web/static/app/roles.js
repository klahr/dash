// An organization's roles: their names, who may grant which, and the picker
// that changes a member's role (Organization → Members, and the Admin
// sheet's account and organization panes). It wires up nothing on load.
import { h } from "./dom.js";
import { attempt, toast } from "./dialogs.js";
import { askPassword } from "./myaccount.js";

export const ROLE_LABEL = { owner: "Owner", admin: "Admin", member: "Member", planner: "Planner" };
// ROLES are the roles, highest first.
export const ROLES = ["owner", "admin", "member", "planner"];
// RANK is who may grant what: admins grant up to admin, owners anything. A
// planner (Planning only) ranks lowest here; what they may use is the
// server's to say (internal/api/planner.go).
export const RANK = { planner: 0, member: 1, admin: 2, owner: 3 };

// roleSelect is a select of roles (current chosen) labelled label. Choosing
// another asks for the password with confirmText(role) ("Make ann admin?",
// role in lowercase), then save(role, password)s it, says doneText(role)
// and calls after(). Cancelled or failed, the select goes back to current.
// cls is the select's class.
export function roleSelect({ label, current, roles = ROLES, confirmText, doneText, save, after = () => {}, cls = null }) {
  const sel = h("select", { "aria-label": label, class: cls },...roles.map((r) => h("option", { value: r, selected: r === current }, ROLE_LABEL[r])));
  sel.addEventListener("change", attempt(async () => {
    const role = sel.value, name = ROLE_LABEL[role].toLowerCase();
    const pw = await askPassword(confirmText(name));
    if (pw == null) { sel.value = current; return; }
    try { await save(role, pw); } catch (err) { sel.value = current; throw err; }
    toast(doneText(name), { tone: "ok" });
    await after();
  }));
  return sel;
}
