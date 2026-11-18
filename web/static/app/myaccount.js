import { $, h, ico, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { accountSection } from "./accountsheet.js";

// ------------------------------------------------------------------ your own account
// The Account sheet's sections about the signed-in user that every role may
// use (internal/api/planner.go lets planners have them too): two-factor
// sign-in, the linked Google account, and their data. The app (account.js)
// and a planner's Planning page (planning-page.js) both load this module;
// each sets the hooks for what differs between them.
export const accountHooks = {
  // reloadMe reads /api/me again into state.me (and redraws what shows it).
  reloadMe: async () => {},
  // deleted runs once the account is deleted, before going to the sign-in page.
  deleted: async () => {},
};

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

// ---- the Google account linked to this one
accountSection("google", () => renderGoogle());

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

// ---- your data: download it, or delete the account
accountSection("data", () => {
  // The env account is made again on every start: deleting it would be undone.
  const hint = $("#account-delete-hint");
  hint.hidden = !state.me?.env_account;
  hint.textContent = "This is the account DASH_USERNAME describes: Dash makes it again on every start, so it can't be deleted here.";
  $("#account-delete").disabled = !!state.me?.env_account;
});

$("#account-delete").addEventListener("click", attempt(async () => {
  const name = state.me?.username || "";
  const typed = await ask({
    title: "Delete your account?",
    body: `This can't be undone. Download your data first if you want a copy. Type your username, ${name}, to go on.`,
    input: true, placeholder: name, ok: "Delete account", danger: true,
  });
  if (typed == null) return;
  if (typed !== name) throw new Error("That isn't your username: nothing was deleted.");
  const pw = await askPassword("Confirm deleting your account", "Enter your password once more.");
  if (pw == null) return;
  await api("DELETE", "/api/account", { password: pw }, "");
  await accountHooks.deleted().catch(() => {});
  location.replace("/login.html");
}));

// ---- two-factor sign-in (the shared steps are in twofactor.js)
accountSection("twofactor", () => renderTwoFactor());

async function renderTwoFactor() {
  const body = $("#twofactor-body");
  const st = await api("GET", "/api/account/2fa", null, "");
  if (!st.enabled) {
    body.replaceChildren(
      h("p", { class: "hint" }, "Sign in with a code from an app on your phone as well as your password, so a stolen password isn't enough."),
      h("button", { class: "btn primary", type: "button", onclick: attempt(async () => {
        await DashTwoFactor.enrol(body, { done: async () => {
          toast("Two-factor sign-in is on", { tone: "ok" });
          await accountHooks.reloadMe();
          await renderTwoFactor();
        } });
      }) }, ico("lock"), "Set up"));
    return;
  }
  const left = st.recovery_codes_left;
  body.replaceChildren(
    h("p", {}, ico("check"), " On. Signing in asks for a code from your authenticator app."),
    h("p", { class: `hint${left < 3 ? " warn" : ""}` }, `${plural(left, "recovery code")} left.`),
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
        await accountHooks.reloadMe();
        await renderTwoFactor();
      }) }, ico("x"), "Turn off")),
    st.required ? h("p", { class: "hint" }, "An organization you belong to requires it, so it can't be turned off.") : null);
}
