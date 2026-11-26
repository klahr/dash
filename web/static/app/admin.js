import { $, avatar, dateText, dateTimeText, debounce, fill, h, ico, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, openSheet, railOf, toast } from "./dialogs.js";
import { askPassword } from "./myaccount.js";
import { copyBlock } from "./prefs.js";
import { state } from "./state.js";
import { ROLES, ROLE_LABEL, roleSelect } from "./roles.js";

// The Admin sheet (account menu → Admin, platform admins only): every
// account and organization on this Dash, a list with the chosen one's
// detail in a pane beside it (full screen on a phone, with Back; Esc
// closes it). The account actions (reset password, turn off two-factor,
// sign out everywhere) are shared with Organization → Members (account.js).

// ------------------------------------------------------------------ account actions
// base is the account's route: /api/admin/users/{id} (platform admins) or
// /api/orgs/{org}/members/{id} (an org's owners and admins).

// resetPassword makes a one-time reset link for u, which Dash emails to
// them if it can, and shows it once to hand over another way.
export async function resetPassword(base, u) {
  const signOut = h("input", { type: "checkbox", name: "signout" });
  const pw = h("input", { type: "password", name: "password", required: true, autocomplete: "current-password" });
  const noPassword = state.me?.has_password === false; // a recent Google sign-in confirms
  let picked = null;
  const { closed } = openSheet({
    title: `Reset ${u.username}'s password?`,
    body: [
      h("p", { class: "hint" }, `Dash makes a link for ${u.username} to choose a new password${u.email ? ", emails it to them if this Dash sends email," : ""} and shows it to you once, to hand over another way. It works once, for an hour. Using it signs them out everywhere.`),
      h("label", { class: "org-require-2fa" }, signOut, " Also sign them out now"),
      noPassword ? null : h("label", { class: "field" }, h("span", {}, "Your password ", h("em", {}, "to confirm")), pw),
    ],
    footer: h("button", { class: "btn primary", value: "ok" }, ico("key"), "Make reset link"),
    onClose: (v) => { if (v === "ok") picked = { password: noPassword ? "" : pw.value, sign_out: signOut.checked }; },
  });
  if (!noPassword) pw.focus();
  await closed;
  if (!picked) return false;
  const res = await api("POST", `${base}/password-reset`, picked, "");
  const link = res.url || (state.me?.org_domains?.base_url || location.origin) + res.path;
  openSheet({
    title: `Reset link for ${u.username}`,
    body: [
      h("p", {}, res.emailed ? `Dash emailed this link to ${u.email}. You can also hand it over another way:` : `Send ${u.username} this link another way: Dash didn't email it.`),
      copyBlock(link, "Reset link copied"),
      h("p", { class: "hint" }, `Only now: Dash doesn't show it again. It works once, until ${dateTimeText(res.expires)}. Whoever opens it can choose ${u.username}'s password, so give it only to them.` +
        (res.sessions != null ? ` ${u.username} was signed out (${plural(res.sessions, "session")}).` : "")),
    ],
    footer: h("button", { class: "btn primary", value: "ok" }, "Done"),
  });
  return true;
}

// turnOff2FA turns off u's two-factor sign-in.
export async function turnOff2FA(base, u) {
  const pw = await askPassword(`Turn off two-factor sign-in for ${u.username}?`,
    `For someone who lost their phone and their recovery codes. ${u.username} signs in with their password alone until they turn it on again (an organization that requires it asks them at their next sign-in). They get an email saying so, if this Dash sends email.`);
  if (pw == null) return false;
  await api("DELETE", `${base}/2fa`, { password: pw }, "");
  toast(`Two-factor sign-in is off for ${u.username}`, { tone: "ok" });
  return true;
}

// signOutEverywhere ends u's sessions, API tokens and connected apps.
export async function signOutEverywhere(base, u, where = "everywhere") {
  const pw = await askPassword(`Sign ${u.username} out ${where}?`,
    `Their sessions end at once, and their API tokens and connected apps (such as a claude.ai connector) stop working. Their runners carry on. They can sign in again.`);
  if (pw == null) return false;
  const r = await api("POST", `${base}/sign-out`, { password: pw }, "");
  toast(`${u.username} signed out ${r.everywhere ? "everywhere" : "here"} (${plural(r.sessions, "session")}, ${plural(r.tokens ?? 0, "token")})`, { tone: "ok" });
  return true;
}

// ------------------------------------------------------------------ the sheet
const sheet = $("#admin-sheet");
const admin = { users: { q: "", sel: 0 }, orgs: { q: "", sel: 0 }, orgList: [] };

export async function openAdmin(section) {
  if (!sheet.open) {
    railOf("admin-sheet").show(section || "users");
    closeDetail("users", false);
    closeDetail("orgs", false);
    await loadSection();
    sheet.showModal();
  } else if (section) {
    railOf("admin-sheet").show(section);
  }
}

const current = () => railOf("admin-sheet")?.current || "users";
const loadSection = () => (current() === "orgs" ? loadOrgs() : loadUsers());
sheet?.addEventListener("railchange", attempt(() => {
  sheet.classList.toggle("pane-open", !detailOf(current()).hidden);
  return sheet.open && loadSection();
}));

const splitOf = (k) => $(k === "orgs" ? "#admin-orgs" : "#admin-users");
const detailOf = (k) => $(k === "orgs" ? "#admin-org-detail" : "#admin-user-detail");

function openDetail(k) {
  splitOf(k).classList.add("open");
  detailOf(k).hidden = false;
  sheet.classList.add("pane-open"); // on a phone the pane takes the rail's place too (style.css)
  detailOf(k).querySelector("h3")?.focus();
}

// closeDetail closes section k's pane, and gives the list's chosen row the
// focus back (refocus).
function closeDetail(k, refocus = true) {
  const split = splitOf(k);
  if (!split) return;
  const was = admin[k].sel;
  split.classList.remove("open");
  detailOf(k).hidden = true;
  sheet.classList.toggle("pane-open", !detailOf(current()).hidden);
  admin[k].sel = 0;
  for (const b of split.querySelectorAll(".admin-row[aria-current]")) b.removeAttribute("aria-current");
  if (refocus && was) split.querySelector(`.admin-row[data-id="${was}"]`)?.focus();
}

// Esc closes the pane first, then the sheet.
sheet?.addEventListener("cancel", (e) => {
  const k = current();
  if (!detailOf(k).hidden) { e.preventDefault(); closeDetail(k); }
});

// Back on a phone and in a narrow sheet; Close where the pane is beside the list (style.css).
const backButton = (k) => h("button", { class: "btn small admin-back", type: "button", onclick: () => closeDetail(k) },
  h("span", { class: "ico-back" }, ico("left")), h("span", { class: "ico-close" }, ico("x")),
  h("span", { class: "back-word" }, "Back"), h("span", { class: "close-word" }, "Close"));

const markCurrent = (b, on) => (on ? b.setAttribute("aria-current", "true") : b.removeAttribute("aria-current"));

const tag = (text, cls = "", title = "") => h("span", { class: `admin-tag${cls ? ` ${cls}` : ""}`, title: title || null }, text);

// ---- users
const signInText = (u) => !u.has_password ? "Google only" : u.sso ? "Password and Google" : "Password";

function userTags(u) {
  return [
    u.is_platform_admin ? tag("Platform admin", "accent") : null,
    u.disabled ? tag("Disabled", "bad") : null,
    u.unverified ? tag("Email not confirmed", "wait") : null,
    tag(u.totp_enabled ? "2FA" : "no 2FA", u.totp_enabled ? "ok" : "", u.totp_enabled ? "Signs in with two factors" : "No two-factor sign-in"),
    !u.has_password ? tag("Google only", "", "Signs in only with Google: no password") : u.sso ? tag("Google", "", "Has a password and a linked Google account") : null,
  ];
}

const orgsText = (u) => u.orgs.length ? u.orgs.map((m) => `${m.name} (${ROLE_LABEL[m.role] || m.role})`).join(", ") : "No organization";

async function loadUsers() {
  const q = admin.users.q;
  const r = await api("GET", `/api/admin/users${q ? `?q=${encodeURIComponent(q)}` : ""}`, null, "");
  if (q !== admin.users.q) return; // typed on meanwhile
  $("#admin-users-count").textContent = `${plural(r.total, "account")} on this Dash` +
    (q ? ` · ${r.matched} matching` : "") + (r.matched > r.users.length ? ` · showing the first ${r.users.length}` : "");
  fill($("#admin-users-list"), r.users.length ? r.users.map((u) => h("li", {},
    h("button", { type: "button", class: `admin-row${u.disabled ? " off" : ""}`, "data-id": u.id, "aria-current": u.id === admin.users.sel ? "true" : null,
      onclick: attempt(() => showUser(u.id)) },
    avatar(u, "member-avatar"),
    h("span", { class: "admin-row-main" },
      h("span", { class: "admin-row-name" }, u.username, u.id === state.me?.user?.id ? h("span", { class: "hint" }, " (you)") : null, ...userTags(u)),
      h("span", { class: "admin-row-sub" }, u.email || "No email"),
      h("span", { class: "admin-row-sub" }, orgsText(u)),
      h("span", { class: "admin-row-meta" }, `${signInText(u)} · created ${dateText(u.created)} · ${u.last_login ? `last sign-in ${dateText(u.last_login)}` : "no sign-in yet"}`))))) :
    [h("li", { class: "hint admin-empty" }, q ? "No account matches." : "No accounts.")]);
}
$("#admin-users-q")?.addEventListener("input", debounce((e) => { admin.users.q = e.target.value.trim(); attempt(loadUsers)(); }));

async function showUser(id) {
  admin.users.sel = id;
  for (const b of $("#admin-users-list").querySelectorAll(".admin-row")) markCurrent(b, Number(b.dataset.id) === id);
  const [r, orgs] = await Promise.all([api("GET", `/api/admin/users/${id}`, null, ""), allOrgs()]);
  if (admin.users.sel !== id) return; // another row was picked meanwhile
  renderUser(r, orgs);
  openDetail("users");
}

async function allOrgs() {
  const r = await api("GET", "/api/admin/orgs", null, "");
  admin.orgList = r.orgs;
  return r.orgs;
}

// refreshUser shows the user again after a change, and the list.
async function refreshUser(id) {
  await loadUsers();
  if (admin.users.sel === id) {
    const [r, orgs] = await Promise.all([api("GET", `/api/admin/users/${id}`, null, ""), allOrgs()]);
    if (admin.users.sel === id) renderUser(r, orgs);
  }
}

function renderUser({ user: u, events }, orgs) {
  const base = `/api/admin/users/${u.id}`;
  const self = u.id === state.me?.user?.id;
  const act = (label, fn, { danger = false, icon = null, hidden = false } = {}) => hidden ? null :
    h("button", { class: `btn small${danger ? " danger" : ""}`, type: "button", disabled: self, title: self ? "That's you: use Account for your own account" : null,
      onclick: attempt(async () => { if (await fn()) await refreshUser(u.id); }) }, icon ? ico(icon) : null, label);
  const resetWhy = u.env_account ? "Its password comes from DASH_PASSWORD on the server." : u.unverified ? "They haven't confirmed their email address yet." :
    !u.has_password ? "Signs in only with Google: there's no password to reset." : "";
  const status = u.disabled ? "Disabled: can't sign in" : u.unverified ? "Waiting for them to confirm their email address" : "Active";

  // Memberships
  const memberRows = u.orgs.map((m) => {
    const sel = roleSelect({
      label: `Role of ${u.username} in ${m.name}`, current: m.role,
      confirmText: (role) => `Make ${u.username} ${role} of ${m.name}?`,
      doneText: (role) => `${u.username} is now ${role} of ${m.name}`,
      save: (role, password) => api("PUT", `${base}/orgs/${m.id}`, { role, password }, ""),
      after: () => refreshUser(u.id),
    });
    return h("li", { class: "item-row" },
      h("span", { class: "runner-name" }, m.name, m.personal ? h("span", { class: "hint" }, " · personal workspace") : null),
      sel,
      h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
        const pw = await askPassword(`Take ${u.username} out of ${m.name}?`, `They lose access to ${m.name} at once, and their queued tasks there go back to Drafts.`);
        if (pw == null) return;
        await api("DELETE", `${base}/orgs/${m.id}`, { password: pw }, "");
        toast(`${u.username} removed from ${m.name}`);
        await refreshUser(u.id);
      }) }, "Remove…"));
  });
  const addable = orgs.filter((o) => !o.personal && !u.orgs.some((m) => m.id === o.id));
  const addOrg = h("select", { "aria-label": "Organization to add them to" }, ...addable.map((o) => h("option", { value: o.id }, o.name)));
  const addRole = h("select", { "aria-label": "Role" }, ...ROLES.map((r) => h("option", { value: r, selected: r === "member" }, ROLE_LABEL[r])));
  const add = addable.length ? h("div", { class: "org-invite" }, addOrg, addRole, h("button", { class: "btn", type: "button", onclick: attempt(async () => {
    const o = addable.find((x) => String(x.id) === addOrg.value);
    const pw = await askPassword(`Add ${u.username} to ${o.name} as ${ROLE_LABEL[addRole.value].toLowerCase()}?`);
    if (pw == null) return;
    await api("PUT", `${base}/orgs/${o.id}`, { role: addRole.value, password: pw }, "");
    toast(`${u.username} added to ${o.name}`, { tone: "ok" });
    await refreshUser(u.id);
  }) }, ico("plus"), "Add")) : null;

  fill(detailOf("users"),
    h("div", { class: "admin-detail-head" }, backButton("users"),
      h("h3", { tabindex: "-1" }, avatar(u, "member-avatar"), u.username)),
    h("div", { class: "admin-tags" }, ...userTags(u)),
    h("dl", { class: "admin-facts" },
      h("dt", {}, "Email"), h("dd", {}, u.email || "None"),
      h("dt", {}, "Status"), h("dd", {}, status),
      h("dt", {}, "Signs in with"), h("dd", {}, signInText(u)),
      h("dt", {}, "Two-factor"), h("dd", {}, u.totp_enabled ? "On" : "Off"),
      h("dt", {}, "Platform admin"), h("dd", {}, u.is_platform_admin ? "Yes" : "No"),
      h("dt", {}, "Created"), h("dd", {}, dateTimeText(u.created)),
      h("dt", {}, "Last sign-in"), h("dd", {}, u.last_login ? dateTimeText(u.last_login) : "None recorded")),
    h("h4", { class: "section-head" }, "Help them sign in"),
    self ? h("p", { class: "hint" }, "That's you: use Account for your own account.") : null,
    h("div", { class: "admin-actions" },
      act("Reset password…", () => resetPassword(base, u), { icon: "key", hidden: !!resetWhy }),
      act("Turn off two-factor…", () => turnOff2FA(base, u), { icon: "lock", hidden: !u.totp_enabled }),
      act("Sign out everywhere…", () => signOutEverywhere(base, u), { icon: "logout" })),
    resetWhy && !self ? h("p", { class: "hint" }, `No password reset: ${resetWhy}`) : null,
    h("h4", { class: "section-head" }, "Account"),
    h("div", { class: "admin-actions" },
      act(u.disabled ? "Enable…" : "Disable…", async () => {
        const pw = await askPassword(u.disabled ? `Enable ${u.username}'s account?` : `Disable ${u.username}'s account?`,
          u.disabled ? "They can sign in again, and their tokens and runners work again." :
            "They're signed out at once and can't sign in, and their tokens and runners stop working, until you enable it again. Nothing is deleted.");
        if (pw == null) return false;
        await api("PUT", `${base}/disabled`, { disabled: !u.disabled, password: pw }, "");
        toast(u.disabled ? `${u.username} enabled` : `${u.username} disabled`, { tone: "ok" });
        return true;
      }, { danger: !u.disabled }),
      h("button", { class: `btn small${u.is_platform_admin ? " danger" : ""}`, type: "button", onclick: attempt(async () => {
        const pw = await askPassword(u.is_platform_admin ? `Remove ${self ? "yourself" : u.username} as platform admin?` : `Make ${u.username} a platform admin?`,
          u.is_platform_admin ? (self ? "You lose this sheet and the rest of what's the whole Dash's at once." : "") :
            "Platform admins manage every account and organization on this Dash, its runners and its task queue.");
        if (pw == null) return;
        await api("PUT", `${base}/platform-admin`, { platform_admin: !u.is_platform_admin, password: pw }, "");
        toast(u.is_platform_admin ? `${u.username} is no longer a platform admin` : `${u.username} is now a platform admin`, { tone: "ok" });
        if (self) { location.reload(); return; }
        await refreshUser(u.id);
      }) }, u.is_platform_admin ? "Remove platform admin…" : "Make platform admin…"),
      act("Delete account…", async () => {
        if (!await ask({
          title: `Delete ${u.username}'s account?`,
          body: `${u.username} is signed out everywhere and can't sign in again. Their account, tokens and devices are deleted, and so is each organization only they are in. What they wrote in other organizations stays, without their name. This can't be undone.`,
          ok: "Delete account", danger: true,
        })) return false;
        const pw = await askPassword(`Confirm deleting ${u.username}'s account`);
        if (pw == null) return false;
        await api("DELETE", base, { password: pw }, "");
        toast(`${u.username}'s account deleted`);
        closeDetail("users", false);
        await loadUsers();
        return false;
      }, { danger: true, hidden: u.env_account })),
    h("h4", { class: "section-head" }, "Organizations"),
    memberRows.length ? h("ul", { class: "item-list" }, ...memberRows) : h("p", { class: "hint" }, "Not in any organization."),
    add,
    events.length ? h("h4", { class: "section-head" }, "Recent sign-in activity") : null,
    events.length ? h("ul", { class: "sso-events" }, ...events.map((e) => h("li", {},
      h("span", { class: "hint" }, dateTimeText(e.at), " · "), EVENT_TEXT[e.kind] || e.kind,
      e.actor ? h("span", { class: "hint" }, ` · by ${e.actor}`) : null,
      e.detail ? h("span", { class: "hint" }, ` · ${e.detail}`) : null))) : null);
}

export const EVENT_TEXT = {
  "signin.google": "signed in with Google", "signin.google.failed": "Google sign-in failed", "signin.google.2fa": "signed in with Google, code next",
  "google.linked": "linked Google", "google.unlinked": "unlinked Google", "google.link_pending": "Google waits for the password to link",
  "member.joined.domain": "joined by domain", "member.joined.invitation": "joined by invitation",
  "member.signed_out": "signed out by an admin", "token.revoked": "token revoked by an admin",
  "admin.password_reset": "password reset by an admin", "admin.2fa_off": "two-factor sign-in turned off by an admin",
  "admin.signed_out": "signed out everywhere by an admin", "admin.disabled": "account disabled", "admin.enabled": "account enabled",
  "admin.platform_admin.granted": "made platform admin", "admin.platform_admin.removed": "no longer platform admin",
  "admin.member.added": "added to the organization by a platform admin", "admin.member.role": "role changed by a platform admin",
  "admin.member.removed": "removed from the organization by a platform admin",
};

// ---- organizations
async function loadOrgs() {
  const q = admin.orgs.q;
  const r = await api("GET", `/api/admin/orgs${q ? `?q=${encodeURIComponent(q)}` : ""}`, null, "");
  if (q !== admin.orgs.q) return;
  $("#admin-orgs-count").textContent = q ? `${plural(r.total, "organization")} matching` : `${plural(r.total, "organization")} on this Dash`;
  fill($("#admin-orgs-list"), r.orgs.length ? r.orgs.map((o) => h("li", {},
    h("button", { type: "button", class: "admin-row", "data-id": o.id, "aria-current": o.id === admin.orgs.sel ? "true" : null, onclick: attempt(() => showOrg(o.id)) },
      h("span", { class: "admin-row-main" },
        h("span", { class: "admin-row-name" }, o.name, o.personal ? tag("Personal") : null, o.default ? tag("Default", "accent") : null),
        h("span", { class: "admin-row-sub" }, o.slug),
        h("span", { class: "admin-row-meta" }, `${plural(o.member_count, "member")} · ${plural(o.project_count, "project")} · created ${dateText(o.created)}`))))) :
    [h("li", { class: "hint admin-empty" }, q ? "No organization matches." : "No organizations.")]);
}
$("#admin-orgs-q")?.addEventListener("input", debounce((e) => { admin.orgs.q = e.target.value.trim(); attempt(loadOrgs)(); }));

async function showOrg(id) {
  admin.orgs.sel = id;
  for (const b of $("#admin-orgs-list").querySelectorAll(".admin-row")) markCurrent(b, Number(b.dataset.id) === id);
  const r = await api("GET", `/api/admin/orgs/${id}`, null, "");
  if (admin.orgs.sel !== id) return; // another row was picked meanwhile
  renderOrg(r);
  openDetail("orgs");
}

async function refreshOrg(id) {
  await loadOrgs();
  if (admin.orgs.sel !== id) return;
  const r = await api("GET", `/api/admin/orgs/${id}`, null, "");
  if (admin.orgs.sel === id) renderOrg(r);
}

function renderOrg({ org: o, members }) {
  const base = `/api/admin/orgs/${o.id}`;
  const name = h("input", { type: "text", value: o.name, maxlength: 100, autocomplete: "off", "aria-label": "Organization name" });
  const slug = h("input", { type: "text", value: o.slug, minlength: 3, maxlength: 40, autocomplete: "off", spellcheck: false, autocapitalize: "off",
    pattern: "[a-z0-9](?:[a-z0-9-]{1,38})[a-z0-9]", "aria-label": "Address (slug)" });
  const patch = async (body, title, text = "") => {
    const pw = await askPassword(title, text);
    if (pw == null) return;
    await api("PATCH", base, { ...body, password: pw }, "");
    toast("Saved", { tone: "ok" });
    await refreshOrg(o.id);
  };
  const rows = members.map((m) => {
    const sel = roleSelect({
      label: `Role of ${m.username}`, current: m.role,
      confirmText: (role) => `Make ${m.username} ${role} of ${o.name}?`,
      doneText: (role) => `${m.username} is now ${role}`,
      save: (role, password) => api("PUT", `/api/admin/users/${m.id}/orgs/${o.id}`, { role, password }, ""),
      after: () => refreshOrg(o.id),
    });
    return h("li", { class: "item-row" },
      avatar(m, "member-avatar"),
      h("span", { class: "runner-name" }, m.username),
      h("span", { class: "item-sub hint" }, m.email || "", h("span", { class: `tfa-tag${m.totp_enabled ? " on" : ""}` }, m.totp_enabled ? "2FA" : "no 2FA")),
      sel,
      h("button", { class: "btn small", type: "button", title: `Reset password, two-factor, sign out, disable and more for ${m.username}`, onclick: attempt(async () => {
        await openAdmin("users");
        await showUser(m.id);
      }) }, "Account…"),
      h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
        const pw = await askPassword(`Take ${m.username} out of ${o.name}?`, `They lose access to ${o.name} at once, and their queued tasks there go back to Drafts.`);
        if (pw == null) return;
        await api("DELETE", `/api/admin/users/${m.id}/orgs/${o.id}`, { password: pw }, "");
        toast(`${m.username} removed`);
        await refreshOrg(o.id);
      }) }, "Remove…"));
  });
  const who = h("input", { type: "text", placeholder: "Username or email", autocomplete: "off", spellcheck: false, "aria-label": "Account to add" });
  const role = h("select", { "aria-label": "Role" }, ...ROLES.map((r) => h("option", { value: r, selected: r === "member" }, ROLE_LABEL[r])));
  const addMember = attempt(async () => {
    const q = who.value.trim();
    if (!q) { who.focus(); return; }
    const r = await api("GET", `/api/admin/users?q=${encodeURIComponent(q)}`, null, "");
    const u = r.users.find((x) => x.username.toLowerCase() === q.toLowerCase() || (x.email || "").toLowerCase() === q.toLowerCase());
    if (!u) throw new Error(`No account is named ${q} or has that email. To bring someone new, invite them from the organization's own Members section.`);
    const pw = await askPassword(`Add ${u.username} to ${o.name} as ${ROLE_LABEL[role.value].toLowerCase()}?`);
    if (pw == null) return;
    await api("PUT", `/api/admin/users/${u.id}/orgs/${o.id}`, { role: role.value, password: pw }, "");
    toast(`${u.username} added to ${o.name}`, { tone: "ok" });
    await refreshOrg(o.id);
  });
  const blocked = [o.project_count ? `it has ${plural(o.project_count, "project")}: delete them first` : "",
    o.subscribed ? "it has a subscription: cancel it first" : "", o.default ? "it's where this Dash keeps records without an organization" : ""].filter(Boolean);

  fill(detailOf("orgs"),
    h("div", { class: "admin-detail-head" }, backButton("orgs"), h("h3", { tabindex: "-1" }, o.name)),
    h("div", { class: "admin-tags" }, o.personal ? tag("Personal workspace") : null, o.default ? tag("Default", "accent") : null, o.subscribed ? tag("Subscribed", "ok") : null),
    h("dl", { class: "admin-facts" },
      h("dt", {}, "Members"), h("dd", {}, String(o.member_count)),
      h("dt", {}, "Projects"), h("dd", {}, String(o.project_count)),
      h("dt", {}, "Created"), h("dd", {}, dateTimeText(o.created))),
    h("h4", { class: "section-head" }, "Name"),
    h("div", { class: "org-invite" }, name, h("button", { class: "btn", type: "button", onclick: attempt(() => {
      const n = name.value.trim();
      if (!n || n === o.name) { name.focus(); return; }
      return patch({ name: n }, `Rename ${o.name} to ${n}?`);
    }) }, "Rename")),
    h("h4", { class: "section-head" }, "Address"),
    h("div", { class: "org-invite" }, slug, h("button", { class: "btn", type: "button", onclick: attempt(() => {
      const s = slug.value.trim().toLowerCase();
      if (s === o.slug) return;
      if (!slug.checkValidity()) { slug.reportValidity(); return; }
      return patch({ slug: s }, `Change ${o.name}'s address to ${s}?`, "With organization subdomains, the old address keeps leading here for 30 days. Its projects' folders stay where they are.");
    }) }, "Change")),
    h("h4", { class: "section-head" }, "Members"),
    rows.length ? h("ul", { class: "item-list org-members" }, ...rows) : h("p", { class: "hint" }, "No members."),
    o.personal ? null : h("div", { class: "org-invite" }, who, role, h("button", { class: "btn", type: "button", onclick: addMember }, ico("plus"), "Add")),
    h("h4", { class: "section-head" }, "Delete organization"),
    h("p", { class: "hint" }, `Deleting ${o.name} ends its members' access to it, and deletes its invitations, chats, planning boards, sign-in settings, API tokens and runners' tokens in it, and its sign-in log. Its members' accounts stay.`),
    blocked.length ? h("p", { class: "hint" }, `It can't be deleted yet: ${blocked.join("; ")}.`) : null,
    h("div", { class: "admin-actions" }, h("button", { class: "btn small danger", type: "button", disabled: blocked.length > 0, onclick: attempt(async () => {
      const typed = await ask({ title: `Delete ${o.name}?`, body: `Type its name, ${o.name}, to confirm. This can't be undone.`, input: true, placeholder: o.name, ok: "Delete organization", danger: true });
      if (typed == null) return;
      if (typed !== o.name) throw new Error("The name doesn't match: nothing was deleted.");
      const pw = await askPassword(`Confirm deleting ${o.name}`);
      if (pw == null) return;
      await api("DELETE", base, { name: typed, password: pw }, "");
      toast(`${o.name} deleted`);
      closeDetail("orgs", false);
      await loadOrgs();
      if (o.id === state.me?.org?.id) location.replace("/");
    }) }, ico("trash"), "Delete organization…")));
}

// ------------------------------------------------------------------ the account menu
export function showAdminEntry() {
  const b = $("#admin-open");
  if (b) b.hidden = !state.me?.user?.is_platform_admin;
}
$("#admin-open")?.addEventListener("click", attempt(() => { $("#foot-menu")?.removeAttribute("open"); return openAdmin(); }));
