const $ = (s) => document.querySelector(s);
const loginForm = $("#login");
const totpForm = $("#totp");
const enrolCard = $("#enrol");
const signupForm = $("#signup");
const forgotForm = $("#forgot");
const resetForm = $("#reset");
const verifyForm = $("#verify");
const noticeCard = $("#notice");
let ticket = "";
let googleLink = ""; // a Google account waiting for this password to link it

// Decorative pictures that fail to load go away (not with an inline
// onerror: the Content-Security-Policy allows no inline script).
for (const img of document.querySelectorAll("img[data-optional]")) {
  if (img.complete && !img.naturalWidth && img.loading !== "lazy") img.remove(); // failed already (a lazy one may not have started)
  else img.addEventListener("error", () => img.remove());
}

function show(card) {
  for (const c of [loginForm, totpForm, enrolCard, signupForm, forgotForm, verifyForm, resetForm, noticeCard]) c.hidden = c !== card;
  card.querySelector("input")?.focus();
}

function notice(text) {
  $("#notice-text").textContent = text;
  show(noticeCard);
}

// Only back to a live preview, the wall view, an invitation link or an org's subdomain
// (/go/<slug>, which checks its own next): anything else could be an open
// redirect.
function go() {
  const next = new URLSearchParams(location.search).get("next") || "";
  const ok = /^\/preview\/[a-z0-9-]+(\?|$)/.test(next) || /^\/invite\.html#[A-Za-z0-9_-]+$/.test(next) ||
    /^\/go\/[a-z0-9-]+(\?|$)/.test(next) || /^\/wall(\?[A-Za-z0-9=&%_-]*)?$/.test(next);
  location.replace(ok ? next : "/");
}

async function postJSON(path, body) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  return { res, data: await res.json().catch(() => ({})) };
}

// An org requires two-factor sign-in and this account hasn't set it up.
async function enrol() {
  show(enrolCard);
  const body = $("#enrol-body");
  try {
    await DashTwoFactor.enrol(body, {
      intro: "An organization you belong to requires a second step when you sign in: a code from an app on your phone.",
      done: go,
    });
  } catch (e) {
    body.replaceChildren(Object.assign(document.createElement("p"), { className: "login-error", textContent: e.message }));
  }
}

loginForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("#login-error");
  const button = loginForm.querySelector("button");
  err.textContent = "";
  button.disabled = true;
  try {
    const { res, data } = await postJSON("/api/login", { username: loginForm.username.value, password: loginForm.password.value, link: googleLink });
    if (res.ok) {
      if (data.google_linked) googleLink = "";
      if (data.totp_required) {
        ticket = data.ticket;
        loginForm.password.value = "";
        totpForm.reset();
        $("#totp-error").textContent = "";
        show(totpForm);
      } else if (data.enrol_2fa) {
        await enrol();
      } else {
        go();
      }
      return;
    }
    err.textContent = data.error || `Sign-in failed (${res.status})`;
    if (data.code === "unverified" && loginForm.username.value.includes("@")) {
      const email = loginForm.username.value.trim();
      err.append(" ", Object.assign(document.createElement("button"), {
        type: "button", className: "link inline", textContent: "Send the link again",
        onclick: async () => { const d = await submit(null, "/api/signup/resend", { email }); if (d) notice(d.message); },
      }));
    }
    loginForm.password.select();
  } catch {
    err.textContent = "Can't reach the Dash server.";
  } finally {
    button.disabled = false;
  }
});

let recoveryMode = false;
$("#totp-recovery").addEventListener("click", () => {
  recoveryMode = !recoveryMode;
  const input = totpForm.code;
  input.inputMode = recoveryMode ? "text" : "numeric";
  input.autocomplete = recoveryMode ? "off" : "one-time-code";
  $("#totp-label").textContent = recoveryMode ? "Recovery code" : "6-digit code";
  $("#totp-prompt").textContent = recoveryMode ? "Enter one of the recovery codes you saved. It works once." : "Enter the code from your authenticator app.";
  $("#totp-recovery").textContent = recoveryMode ? "Use the authenticator app instead" : "Use a recovery code instead";
  input.value = "";
  input.focus();
});

totpForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("#totp-error");
  const button = totpForm.querySelector("button.primary");
  err.textContent = "";
  button.disabled = true;
  try {
    const { res, data } = await postJSON("/api/login/totp", { ticket, code: totpForm.code.value });
    if (res.ok) {
      if (data.recovery_used) {
        const left = data.recovery_codes_left;
        totpForm.replaceChildren(
          Object.assign(document.createElement("p"), {
            textContent: `You signed in with a recovery code. ${left === 0 ? "You have none left" : `${left} left`}: make new ones under Two-factor sign-in in the account menu${left < 3 ? " now" : ""}.`,
          }),
          Object.assign(document.createElement("button"), { className: "btn primary", type: "button", textContent: "Continue", onclick: go }),
        );
        totpForm.querySelector("button").focus();
        return;
      }
      go();
      return;
    }
    if (data.code === "ticket_expired") {
      show(loginForm);
      $("#login-error").textContent = data.error;
      return;
    }
    err.textContent = data.error || `Sign-in failed (${res.status})`;
    totpForm.code.select();
  } catch {
    err.textContent = "Can't reach the Dash server.";
  } finally {
    button.disabled = false;
  }
});

$("#enrol-logout").addEventListener("click", async () => {
  await fetch("/api/logout", { method: "POST" }).catch(() => {});
  location.replace("/login.html");
});

// Sent here by the server to set up two-factor sign-in (signed in already).
if (new URLSearchParams(location.search).has("enrol")) {
  fetch("/api/me").then(async (res) => {
    if (!res.ok) return;
    const me = await res.json();
    if (me.needs_2fa) await enrol();
    else go();
  }).catch(() => {});
}

// ---- signup, verification and password reset by email ----

// submit posts body to path for a form (or none), and shows what the server
// says: the same message whether or not the address has an account.
async function submit(form, path, body) {
  const err = form?.querySelector(".login-error");
  const button = form?.querySelector("button.primary");
  if (err) err.textContent = "";
  if (button) button.disabled = true;
  try {
    const { res, data } = await postJSON(path, body);
    if (res.ok) return data;
    if (err) err.textContent = data.error || `Something went wrong (${res.status})`;
    else notice(data.error || `Something went wrong (${res.status})`);
  } catch {
    if (err) err.textContent = "Can't reach the Dash server.";
    else notice("Can't reach the Dash server.");
  } finally {
    if (button) button.disabled = false;
  }
  return null;
}

for (const b of document.querySelectorAll(".to-login")) {
  b.addEventListener("click", () => { history.replaceState(null, "", location.pathname + location.search); show(loginForm); });
}
$("#to-signup").addEventListener("click", () => show(signupForm));
$("#to-forgot").addEventListener("click", () => {
  if (loginForm.username.value.includes("@")) forgotForm.email.value = loginForm.username.value.trim();
  show(forgotForm);
});

signupForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const data = await submit(signupForm, "/api/signup", { email: signupForm.email.value });
  if (data) { signupForm.reset(); notice(data.message); }
});

forgotForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const data = await submit(forgotForm, "/api/password/forgot", { email: forgotForm.email.value });
  if (data) notice(data.message);
});

let verifyToken = "";
verifyForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const data = await submit(verifyForm, "/api/signup/verify", { token: verifyToken, password: verifyForm.password.value });
  if (data) location.replace("/");
});

let resetToken = "";
resetForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const data = await submit(resetForm, "/api/password/reset", { token: resetToken, password: resetForm.password.value });
  if (!data) return;
  resetForm.reset();
  history.replaceState(null, "", location.pathname);
  show(loginForm);
  loginForm.username.value = data.username || "";
  loginForm.password.focus();
  $("#login-error").textContent = "";
  loginForm.querySelector("p").textContent = "Password changed. Sign in with your new password.";
});

// Sign in with Google, when this Dash (or the org being signed in to) has
// it: the org is the one /go/<slug> in ?next leads to.
async function setupGoogle() {
  const next = new URLSearchParams(location.search).get("next") || "";
  const org = (next.match(/^\/go\/([a-z0-9-]+)/) || [])[1] || new URLSearchParams(location.search).get("org") || "";
  try {
    const res = await fetch("/api/auth/google" + (org ? `?org=${encodeURIComponent(org)}` : ""));
    if (!res.ok) return;
    const g = await res.json();
    if (!g.enabled) return;
    const q = new URLSearchParams();
    if (org) q.set("org", org);
    if (next) q.set("next", next);
    for (const area of document.querySelectorAll(".google-area")) {
      area.querySelector("a").href = g.start + (q.size ? `?${q}` : "");
      area.hidden = false;
    }
  } catch {}
}
setupGoogle();

// Links from emails carry their token in the fragment, which isn't sent to
// the server or in a Referer; so do Google sign-ins that need a second step.
(async () => {
  const hash = new URLSearchParams(location.hash.slice(1));
  if (hash.has("google_totp")) {
    // Google signed in; the authenticator code is still to come.
    ticket = hash.get("google_totp");
    history.replaceState(null, "", location.pathname + location.search);
    totpForm.reset();
    show(totpForm);
    return;
  }
  if (hash.has("google_link")) {
    googleLink = hash.get("google_link");
    history.replaceState(null, "", location.pathname + location.search);
    loginForm.querySelector("p").textContent = "Sign in with your password once to link your Google account. After that, Google signs you in.";
  }
  let status = { open: false, password_reset: false };
  try {
    const res = await fetch("/api/signup");
    if (res.ok) status = await res.json();
  } catch {}
  $("#to-signup").hidden = !status.open;
  $("#to-forgot").hidden = !status.password_reset;
  if (hash.has("verify")) {
    verifyToken = hash.get("verify");
    history.replaceState(null, "", location.pathname);
    show(verifyForm);
  } else if (hash.has("reset")) {
    resetToken = hash.get("reset");
    history.replaceState(null, "", location.pathname);
    show(resetForm);
  } else if (location.hash === "#forgot" && status.password_reset) {
    show(forgotForm);
  } else if (location.hash === "#signup" && status.open) {
    show(signupForm);
  }
})();
