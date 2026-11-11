import { $, fill, h, ico } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { copyBlock } from "./prefs.js";
import { loadBilling, offerSubscription, renderOrgBilling } from "./billing.js";
import { pushUnsubscribe } from "./notifications.js";

// ------------------------------------------------------------------ account & organizations
export async function loadMe() {
  const res = await fetch("/api/me");
  if (!res.ok) return;
  const me = await res.json();
  state.me = me;
  const name = me.username;
  if (!name) return;
  $("#foot-user").textContent = me.org ? `${name} · ${me.org.name}` : name;
  $("#foot-avatar").textContent = [...name][0].toUpperCase();
  $("#foot-menu summary").title = `Account menu · ${name}${me.org ? ` in ${me.org.name}` : ""}`;
  const orgs = me.orgs || [];
  const sw = $("#org-switch");
  sw.hidden = orgs.length < 2;
  sw.replaceChildren(...(orgs.length < 2 ? [] : [
    h("span", { class: "org-switch-label" }, "Switch organization"),
    ...orgs.map((o) => h("button", {
      type: "button", class: o.id === me.org?.id ? "active" : "",
      "aria-current": o.id === me.org?.id ? "true" : null,
      onclick: attempt(() => switchOrg(o)),
    }, ico(o.id === me.org?.id ? "check" : "users"), o.name)),
  ]));
  $("#org-open").hidden = !me.org;
  $("#wall-open").hidden = !me.org;
  $("#google-open").hidden = !me.google_available;
  // The queue pause is the whole Dash's, for platform admins.
  const admin = !!me.user?.is_platform_admin;
  $("#queue-toggle").hidden = !admin;
  if (!me.org) {
    await ask({
      title: "No organization",
      body: orgs.length ? "Pick an organization from the account menu." :
        "Your account isn't a member of any organization any more. Ask an owner or admin for an invitation link, or sign out.",
      ok: "Sign out",
    }) && logout();
  }
}

export async function switchOrg(o) {
  if (o.id === state.me?.org?.id) return;
  // With org subdomains each org is its own address: go there (the base
  // domain hands this browser's sign-in over).
  const hosts = state.me?.org_domains;
  if (hosts) { location.assign(`${hosts.base_url}/go/${encodeURIComponent(o.slug)}`); return; }
  await api("POST", "/api/auth/switch-org", { org_id: o.id }, "");
  location.reload(); // everything on the page belongs to the org it was loaded in
}

const ROLE_LABEL = { owner: "Owner", admin: "Admin", member: "Member" };
const RANK = { member: 1, admin: 2, owner: 3 };

// askPassword asks for the user's password again before a sensitive change.
// An account made by signing in with Google has none: a recent Google
// sign-in confirms instead, so it only asks to go ahead ("" for the server).
export async function askPassword(title, body = "") {
  if (state.me?.has_password === false) {
    return (await ask({ title, body: body || "Your recent Google sign-in confirms this.", ok: "Confirm" })) ? "" : null;
  }
  const d = $("#password-sheet");
  const f = $("#password-form");
  f.reset();
  $("#password-title").textContent = title;
  $("#password-body").textContent = body;
  $("#password-body").hidden = !body;
  d.returnValue = "";
  d.showModal();
  f.password.focus();
  return new Promise((resolve) => {
    d.addEventListener("close", () => resolve(d.returnValue === "ok" ? f.password.value : null), { once: true });
  });
}

export async function openOrg() {
  await renderOrg();
  $("#org-invite-link").hidden = true;
  const d = $("#org-sheet");
  if (!d.open) d.showModal();
}

async function renderOrg() {
  const me = state.me;
  if (!me?.org) throw new Error("You aren't in an organization.");
  const org = await api("GET", `/api/orgs/${me.org.id}/members`, null, "");
  const mine = org.role;
  const manage = RANK[mine] >= RANK.admin;
  $("#org-title").textContent = org.org.name;
  $("#org-require-2fa-row").hidden = mine !== "owner";
  $("#org-require-2fa").checked = !!org.org.require_2fa;
  // With org subdomains: the org's address, which owners may rename.
  $("#org-address").hidden = !org.url;
  if (org.url) {
    const old = Object.entries(org.slug_redirects || {});
    $("#org-address-url").replaceChildren(h("a", { href: org.url + "/" }, org.url.replace(/^https?:\/\//, "")),
      ...old.map(([s, until]) => h("span", {}, ` · ${s} leads here until ${new Date(until).toLocaleDateString()}`)));
    $("#org-slug-row").hidden = $("#org-slug-hint").hidden = mine !== "owner";
    $("#org-slug").value = org.org.slug;
  }
  $("#org-role").textContent = `You are ${mine === "admin" ? "an admin" : `${mine === "owner" ? "an owner" : "a member"}`} of ${org.org.name}.` +
    (org.org.personal ? " This is your personal workspace: create an organization to work with others." :
      manage ? " Owners and admins invite people and change roles; only owners make owners." : "");
  const roleSelect = (m) => {
    const sel = h("select", { "aria-label": `Role of ${m.username}`, class: "org-role-select" },
      ...Object.keys(ROLE_LABEL).filter((r) => RANK[r] <= RANK[mine]).map((r) => h("option", { value: r, selected: r === m.role }, ROLE_LABEL[r])));
    sel.addEventListener("change", attempt(async () => {
      const pw = await askPassword(`Make ${m.username} ${ROLE_LABEL[sel.value].toLowerCase()}?`);
      if (pw == null) { sel.value = m.role; return; }
      try {
        await api("PATCH", `/api/orgs/${org.org.id}/members/${m.id}`, { role: sel.value, password: pw }, "");
      } catch (err) { sel.value = m.role; throw err; }
      toast(`${m.username} is now ${ROLE_LABEL[sel.value].toLowerCase()}`, { tone: "ok" });
      if (m.id === me.user.id) await loadMe();
      await renderOrg();
    }));
    return sel;
  };
  const rows = org.members.map((m) => {
    const self = m.id === me.user.id;
    const canEdit = manage && RANK[m.role] <= RANK[mine];
    return h("li", { class: "runner-row" },
      h("span", { class: "runner-name" }, m.username, self ? h("span", { class: "hint" }, " (you)") : null),
      h("span", { class: "runner-seen hint" }, m.email || "", manage || org.org.require_2fa ? h("span", { class: `tfa-tag${m.totp_enabled ? " on" : ""}`, title: m.totp_enabled ? "Signs in with two factors" : "No two-factor sign-in yet" }, m.totp_enabled ? "2FA" : "no 2FA") : null),
      canEdit ? roleSelect(m) : h("span", { class: "org-role-tag" }, ROLE_LABEL[m.role]),
      (canEdit && !self) ? h("button", { class: "btn small", type: "button", title: `End ${m.username}'s sessions in ${org.org.name}`, onclick: attempt(async () => {
        const pw = await askPassword(`Sign ${m.username} out everywhere?`,
          `Their sessions in ${org.org.name} end at once; they can sign in again. To keep someone out, remove them (and in Google Workspace).`);
        if (pw == null) return;
        const r = await api("POST", `/api/orgs/${org.org.id}/members/${m.id}/sign-out`, { password: pw }, "");
        toast(`${m.username} signed out (${r.sessions} session${r.sessions === 1 ? "" : "s"})`, { tone: "ok" });
      }) }, "Sign out") : null,
      (canEdit || self) ? h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
        const pw = await askPassword(self ? `Leave ${org.org.name}?` : `Remove ${m.username}?`,
          self ? "You lose access at once. Someone has to invite you again to come back." : `${m.username} loses access to ${org.org.name} at once.`);
        if (pw == null) return;
        await api("DELETE", `/api/orgs/${org.org.id}/members/${m.id}`, { password: pw }, "");
        if (self) { location.reload(); return; }
        toast(`${m.username} removed`);
        await renderOrg();
      }) }, self ? "Leave" : "Remove") : null);
  });
  $("#org-members").replaceChildren(...rows);
  // A personal workspace is just its owner's: new orgs are for inviting people.
  $("#org-invite-area").hidden = !manage || !!org.org.personal;
  if (manage) {
    for (const opt of $("#org-invite-role").options) opt.hidden = opt.disabled = RANK[opt.value] > RANK[mine];
    const pending = (org.invitations || []).map((i) => h("li", { class: "runner-row" },
      h("span", { class: "runner-name" }, i.email),
      h("span", { class: "runner-seen hint" }, `${ROLE_LABEL[i.role]} · expires ${new Date(i.expires).toLocaleDateString()}${i.invited_by ? ` · by ${i.invited_by}` : ""}`),
      h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
        await api("DELETE", `/api/orgs/${org.org.id}/invitations/${i.id}`, null, "");
        toast(`Invitation for ${i.email} revoked`);
        await renderOrg();
      }) }, "Revoke")));
    $("#org-pending-head").hidden = !pending.length;
    $("#org-pending").replaceChildren(...pending);
  }
  $("#org-new").hidden = !me.can_create_orgs;
  $("#org-invite-any-row").hidden = !me.google_available;
  await renderOrgSSO(org.org, mine).catch((err) => toast(err.message, true));
  renderOrgBilling(await loadBilling().catch(() => null));
}

// ---- Google sign-in for the org (owners and admins)
const SSO_EVENTS = {
  "signin.google": "signed in with Google", "signin.google.failed": "Google sign-in failed", "signin.google.2fa": "signed in with Google, code next",
  "google.linked": "linked Google", "google.unlinked": "unlinked Google", "google.link_pending": "Google waits for the password to link",
  "member.joined.domain": "joined by domain", "member.joined.invitation": "joined by invitation", "member.signed_out": "signed out by an admin",
  "sso.settings": "settings changed", "sso.domain.added": "domain added", "sso.domain.removed": "domain removed",
  "sso.domain.verified": "domain verified", "sso.test.ok": "test passed", "sso.test.failed": "test failed",
};

async function renderOrgSSO(o, mine) {
  const box = $("#org-sso");
  if (RANK[mine] < RANK.admin || o.personal) { box.hidden = true; box.replaceChildren(); return; }
  const s = await api("GET", `/api/orgs/${o.id}/sso`, null, "");
  const owner = mine === "owner";
  const patch = async (body, title, text = "") => {
    const pw = await askPassword(title, text);
    if (pw == null) { await renderOrgSSO(o, mine); return; }
    try { await api("PATCH", `/api/orgs/${o.id}/sso`, { ...body, password: pw }, ""); }
    finally { await renderOrgSSO(o, mine); }
    toast("Google sign-in settings saved", { tone: "ok" });
  };
  const check = (label, key, title, text, disabled = false) => {
    const input = h("input", { type: "checkbox", checked: !!s[key], disabled });
    input.addEventListener("change", attempt(() => patch({ [key]: input.checked }, title(input.checked), text(input.checked))));
    return h("label", { class: "org-require-2fa" }, input, " ", label);
  };
  const status = s.available
    ? (s.own_client ? `Members sign in with ${o.name}'s own Google client.` : "Members can sign in with this Dash's Google client.")
    : "Google sign-in isn't set up: this Dash has no Google client. An owner can enter the organization's own below.";

  // Domains
  const domainInput = h("input", { type: "text", placeholder: "example.com", autocomplete: "off", spellcheck: false, "aria-label": "Google Workspace domain" });
  const addDomain = attempt(async () => {
    const domain = domainInput.value.trim();
    if (!domain) { domainInput.focus(); return; }
    const pw = await askPassword(`Add ${domain}?`, "People with Google Workspace accounts of a verified domain can join (with auto-join on) and must use Google (with that on).");
    if (pw == null) return;
    const d = await api("POST", `/api/orgs/${o.id}/sso/domains`, { domain, password: pw }, "");
    toast(d.verified ? `${d.domain} added and verified` : `${d.domain} added: it waits for verification`, { tone: "ok" });
    await renderOrgSSO(o, mine);
  });
  const domains = (s.domains || []).map((d) => h("li", { class: "runner-row" },
    h("span", { class: "runner-name" }, d.domain),
    h("span", { class: `runner-seen hint ${d.verified ? "tag-ok" : "tag-wait"}` }, d.verified
      ? `Verified${d.how === "admin" ? " by a platform admin" : d.how === "google" ? " by an owner's Google sign-in" : ""}`
      : "Not verified yet: an owner signs in with Google with an account of this domain (Test below), or a platform admin approves it"),
    h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
      const pw = await askPassword(`Remove ${d.domain}?`, "Its people can't join by it any more, nor are required to use Google.");
      if (pw == null) return;
      await api("DELETE", `/api/orgs/${o.id}/sso/domains/${encodeURIComponent(d.domain)}`, { password: pw }, "");
      await renderOrgSSO(o, mine);
    }), hidden: d.verified && !owner }, "Remove")));

  // The org's own client (owners)
  const cid = h("input", { type: "text", value: s.client_id || "", placeholder: "…apps.googleusercontent.com", autocomplete: "off", spellcheck: false, "aria-label": "Client ID" });
  const secret = h("input", { type: "password", placeholder: s.client_secret_set ? "Set (enter a new one to replace it)" : "Client secret", autocomplete: "new-password", "aria-label": "Client secret" });
  const client = owner ? [
    h("h3", { class: "org-section" }, "Your own Google client (optional)"),
    h("p", { class: "hint" }, "Create an OAuth client (Web application) in your Google Cloud project, with its consent screen set to Internal so only your Workspace can use it, and this redirect URI:"),
    copyBlock(s.redirect_uri, "Redirect URI copied"),
    h("div", { class: "org-invite" }, cid),
    h("div", { class: "org-invite" }, secret,
      h("button", { class: "btn", type: "button", onclick: attempt(() => patch({ client_id: cid.value.trim(), client_secret: secret.value },
        cid.value.trim() ? `Use this Google client for ${o.name}?` : `Stop using ${o.name}'s own Google client?`)) }, "Save")),
    h("p", { class: "hint" }, `Secret: ${s.client_secret_set ? "set" : "not set"}. It's stored encrypted and never shown again.${s.can_store_secret ? "" : " This Dash has no secret key to encrypt it with."}`),
  ] : [];

  const events = (s.events || []).slice(0, 20).map((e) => h("li", {},
    h("span", { class: "hint" }, new Date(e.at).toLocaleString(), " · "), e.user || e.actor || "", " ", SSO_EVENTS[e.kind] || e.kind,
    e.detail ? h("span", { class: "hint" }, ` · ${e.detail}`) : null));

  fill(box,
    h("p", { class: "hint" }, status),
    h("h3", { class: "org-section" }, "Google Workspace domains"),
    h("ul", { class: "runner-list sso-domains" }, ...domains),
    h("div", { class: "org-invite" }, domainInput, h("button", { class: "btn", type: "button", onclick: addDomain }, "Add domain")),
    h("h3", { class: "org-section" }, "Joining and signing in"),
    check("Let people of verified domains join on their first Google sign-in", "auto_join",
      (on) => on ? "Let verified domains' people join?" : "Stop letting domains' people join?",
      (on) => on ? "They join with the role below. Others still need an invitation." : "Others need an invitation, as before."),
    h("label", { class: "org-require-2fa" }, "They join as ", (() => {
      const sel = h("select", { "aria-label": "Role of people who join by domain" },
        ...["member", "admin"].map((r) => h("option", { value: r, selected: s.join_role === r }, ROLE_LABEL[r])));
      sel.addEventListener("change", attempt(() => patch({ join_role: sel.value }, `People join as ${ROLE_LABEL[sel.value].toLowerCase()}s?`)));
      return sel;
    })()),
    check("Require Google sign-in of the domains' members (owners keep their password)", "require_google",
      (on) => on ? `Require Google sign-in in ${o.name}?` : "Stop requiring Google sign-in?",
      (on) => on ? "Members whose address is in a verified domain can't use a password session here. API tokens and runners carry on." : "", !owner),
    check("Also ask for the Dash two-factor code after Google", "google_2fa",
      (on) => on ? "Ask for the two-factor code after Google?" : "Stop asking for the code after Google?",
      () => "For members who have two-factor sign-in set up.", !owner),
    s.available ? h("p", {}, h("a", { class: "btn", href: s.test_url }, "Test Google sign-in"),
      h("span", { class: "hint" }, " Signs in at Google to check the setup; it never signs anyone in here.")) : null,
    ...client,
    events.length ? h("h3", { class: "org-section" }, "Recent sign-in activity") : null,
    events.length ? h("ul", { class: "sso-events" }, ...events) : null,
    h("div", { id: "org-sso-admin" }));
  box.hidden = false;
  if (state.me?.user?.is_platform_admin) await renderPlatformSSO($("#org-sso-admin")).catch(() => {});
}

// Platform admins: the instance's Google client and domains waiting for approval.
async function renderPlatformSSO(el) {
  const s = await api("GET", "/api/admin/sso", null, "");
  const cid = h("input", { type: "text", value: s.client_id || "", placeholder: "Client ID", disabled: s.from_env, "aria-label": "Instance client ID" });
  const secret = h("input", { type: "password", placeholder: s.client_secret_set ? "Set" : "Client secret", disabled: s.from_env, autocomplete: "new-password", "aria-label": "Instance client secret" });
  const pending = (s.pending_domains || []).map((d) => h("li", { class: "runner-row" },
    h("span", { class: "runner-name" }, d.domain), h("span", { class: "runner-seen hint" }, d.org),
    h("button", { class: "btn small", type: "button", onclick: attempt(async () => {
      const pw = await askPassword(`Approve ${d.domain} for ${d.org}?`, "Only if you know the organization owns the domain: its Google accounts can then join it.");
      if (pw == null) return;
      await api("POST", "/api/admin/sso/domains/verify", { org_id: d.org_id, domain: d.domain, password: pw }, "");
      toast(`${d.domain} verified for ${d.org}`, { tone: "ok" });
      await renderPlatformSSO(el);
    }) }, "Approve")));
  fill(el,
    h("h3", { class: "org-section" }, "This Dash's Google client (platform admins)"),
    h("p", { class: "hint" }, s.from_env ? "Set by DASH_GOOGLE_CLIENT_ID and DASH_GOOGLE_CLIENT_SECRET." : "Used by every organization without its own. Redirect URI:"),
    copyBlock(s.redirect_uri, "Redirect URI copied"),
    s.from_env ? null : h("div", { class: "org-invite" }, cid),
    s.from_env ? null : h("div", { class: "org-invite" }, secret, h("button", { class: "btn", type: "button", onclick: attempt(async () => {
      const pw = await askPassword("Save this Dash's Google client?");
      if (pw == null) return;
      await api("PUT", "/api/admin/sso/google", { client_id: cid.value.trim(), client_secret: secret.value, password: pw }, "");
      toast("Saved", { tone: "ok" });
      await loadMe();
      await renderPlatformSSO(el);
    }) }, "Save")),
    pending.length ? h("h3", { class: "org-section" }, "Domains waiting for approval") : null,
    pending.length ? h("ul", { class: "runner-list" }, ...pending) : null);
}

// ---- the Google account linked to this one
export async function openGoogle() {
  await renderGoogle();
  const d = $("#google-sheet");
  if (!d.open) d.showModal();
}

async function renderGoogle() {
  const body = $("#google-body");
  const g = await api("GET", "/api/account/google", null, "");
  if (!g.linked) {
    body.replaceChildren(
      h("p", { class: "hint" }, "Link your Google account to sign in with it instead of your password."),
      h("a", { class: "btn primary", href: g.link_url }, ico("key"), "Link Google account"));
    return;
  }
  body.replaceChildren(
    h("p", {}, ico("check"), ` Linked to ${g.identity.email}${g.identity.hd ? ` (${g.identity.hd})` : ""}.`),
    g.has_password ? h("button", { class: "btn danger", type: "button", onclick: attempt(async () => {
      const pw = await askPassword("Unlink your Google account?", "You'll sign in with your password.");
      if (pw == null) return;
      await api("DELETE", "/api/account/google", { password: pw }, "");
      toast("Google account unlinked");
      await renderGoogle();
    }) }, ico("x"), "Unlink")
      : h("p", { class: "hint" }, "Google is your only way to sign in, so it can't be unlinked. Choose a password with “Forgot password?” on the sign-in page first."));
}

export async function renameOrg() {
  const input = $("#org-slug");
  const slug = input.value.trim().toLowerCase();
  const org = state.me.org;
  if (slug === org.slug) return;
  if (!input.checkValidity()) { input.reportValidity(); return; }
  const pw = await askPassword(`Move ${org.name} to ${slug}.${state.me.org_domains.domain}?`,
    "Bookmarks, runners and connected apps using the old address keep working for 30 days. Update them before then.");
  if (pw == null) return;
  const o = await api("PATCH", `/api/orgs/${org.id}`, { slug, password: pw }, "");
  toast(`${o.name} moved to ${o.slug}.${state.me.org_domains.domain}`, { tone: "ok" });
  location.assign(`${state.me.org_domains.base_url}/go/${encodeURIComponent(o.slug)}`);
}

export async function setRequire2FA() {
  const box = $("#org-require-2fa");
  const on = box.checked;
  const name = state.me.org.name;
  const pw = await askPassword(on ? `Require two-factor sign-in in ${name}?` : `Stop requiring two-factor sign-in in ${name}?`,
    on ? "Members without it are asked to set it up before they can do anything else, from their next click." : "Members keep two-factor sign-in if they have it, and can turn it off.");
  if (pw == null) { box.checked = !on; return; }
  try {
    await api("PATCH", `/api/orgs/${state.me.org.id}`, { require_2fa: on, password: pw }, "");
  } catch (err) { box.checked = !on; throw err; }
  toast(on ? "Two-factor sign-in is now required" : "Two-factor sign-in is optional", { tone: "ok" });
  await loadMe();
  await renderOrg();
}

// ---- two-factor sign-in (the shared steps are in twofactor.js)
export async function openTwoFactor() {
  await renderTwoFactor();
  const d = $("#twofactor-sheet");
  if (!d.open) d.showModal();
}

async function renderTwoFactor() {
  const body = $("#twofactor-body");
  const st = await api("GET", "/api/account/2fa", null, "");
  if (!st.enabled) {
    body.replaceChildren(
      h("p", { class: "hint" }, "Sign in with a code from an app on your phone as well as your password, so a stolen password isn't enough."),
      h("button", { class: "btn primary", type: "button", onclick: attempt(async () => {
        await DashTwoFactor.enrol(body, { done: async () => {
          toast("Two-factor sign-in is on", { tone: "ok" });
          await loadMe();
          await renderTwoFactor();
        } });
      }) }, ico("lock"), "Set up"));
    return;
  }
  const left = st.recovery_codes_left;
  body.replaceChildren(
    h("p", {}, ico("check"), " On. Signing in asks for a code from your authenticator app."),
    h("p", { class: `hint${left < 3 ? " warn" : ""}` }, `${left} recovery code${left === 1 ? "" : "s"} left.`),
    h("div", { class: "sheet-actions" },
      h("button", { class: "btn", type: "button", onclick: attempt(async () => {
        const pw = await askPassword("Make new recovery codes?", "The codes you have now stop working.");
        if (pw == null) return;
        const r = await api("POST", "/api/account/2fa/recovery-codes", { password: pw }, "");
        DashTwoFactor.recoveryCodes(body, r.recovery_codes, () => renderTwoFactor().catch(() => {}), "Done");
      }) }, ico("redo"), "New recovery codes"),
      st.required ? null : h("button", { class: "btn danger", type: "button", onclick: attempt(async () => {
        const pw = await askPassword("Turn off two-factor sign-in?", "Your password alone will sign you in again. Your recovery codes stop working.");
        if (pw == null) return;
        await api("POST", "/api/account/2fa/disable", { password: pw }, "");
        toast("Two-factor sign-in is off");
        await loadMe();
        await renderTwoFactor();
      }) }, ico("x"), "Turn off")),
    st.required ? h("p", { class: "hint" }, "An organization you belong to requires it, so it can't be turned off.") : null);
}

export async function sendInvite() {
  const email = $("#org-invite-email").value.trim();
  const role = $("#org-invite-role").value;
  if (!email) { $("#org-invite-email").focus(); throw new Error("Enter the email address to invite."); }
  const pw = await askPassword(`Invite ${email} as ${ROLE_LABEL[role].toLowerCase()}?`);
  if (pw == null) return;
  const res = await api("POST", `/api/orgs/${state.me.org.id}/invitations`, { email, role, password: pw, any_email: $("#org-invite-any").checked }, "");
  if (res.billing) offerSubscription(res.billing).catch((err) => toast(err.message, true));
  const link = (state.me?.org_domains?.base_url || location.origin) + res.path; // invitations open on the base domain
  $("#org-invite-link").replaceChildren(
    h("p", {}, `Send ${email} this link. Dash doesn't email it.`),
    copyBlock(link, "Invitation link copied", { pre: true }),
    h("p", { class: "hint" }, `Whoever opens it can join as ${ROLE_LABEL[role].toLowerCase()}, with an account they have or a new one, so share it only with them. It works once, for 7 days; inviting the same address again replaces it.`));
  $("#org-invite-link").hidden = false;
  $("#org-invite-email").value = "";
  await renderOrg();
}

export async function logout() {
  await pushUnsubscribe().catch(() => {}); // this browser stops getting the account's notifications
  await fetch("/api/logout", { method: "POST" });
  location.replace("/login.html");
}
$("#logout").addEventListener("click", logout);
