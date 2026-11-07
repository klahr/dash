const $ = (s) => document.querySelector(s);
const loginForm = $("#login");
const totpForm = $("#totp");
const enrolCard = $("#enrol");
let ticket = "";

function show(card) {
  for (const c of [loginForm, totpForm, enrolCard]) c.hidden = c !== card;
  card.querySelector("input")?.focus();
}

// Only back to a live preview or an invitation link: anything else could be
// an open redirect.
function go() {
  const next = new URLSearchParams(location.search).get("next") || "";
  const ok = /^\/preview\/[a-z0-9-]+(\?|$)/.test(next) || /^\/invite\.html#[A-Za-z0-9_-]+$/.test(next);
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
    const { res, data } = await postJSON("/api/login", { username: loginForm.username.value, password: loginForm.password.value });
    if (res.ok) {
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
