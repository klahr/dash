// The invitation page: the link's token is in the fragment, so it never
// reaches server logs or other sites. It shows what the invitation is for
// and lets the invitee accept with the account signed in here, create one,
// or decline.
const $ = (s) => document.querySelector(s);
const token = decodeURIComponent(location.hash.slice(1));
const err = $("#invite-error");
const { el, CANT_REACH } = DashPage;

// post sends the invitation's token along with body.
const post = (path, body) => DashPage.post(path, { token, ...body });

// busy makes a click handler that runs fn with btn disabled, showing what
// goes wrong in the page's error line.
const busy = (btn, fn) => () => DashPage.busy({ button: btn, error: err }, fn);

const article = (role) => (role === "admin" || role === "owner" ? "an" : "a");

async function load() {
  if (!token) throw new Error("This link has no invitation in it. Ask for the link again.");
  const inv = await post("/api/invitations/lookup");
  const what = $("#invite-what");
  what.replaceChildren(
    inv.invited_by ? `${inv.invited_by} invited you` : "You're invited",
    " to join ", el("strong", {}, inv.org.name),
    ` as ${article(inv.role)} ${inv.role}.`);
  document.title = `Join ${inv.org.name} · ${document.querySelector("meta[name=dash-name]")?.content || "Dash"}`;
  if (inv.google && !inv.signed_in_as) {
    $("#invite-google").hidden = false;
    $("#invite-google-hint").textContent = inv.any_email ? "With any Google account." : `With the Google account of ${inv.email}.`;
  }
  if (inv.signed_in_as) {
    $("#invite-accept").textContent = `Accept as ${inv.signed_in_as}`;
    $("#invite-signed-in").hidden = false;
  } else {
    $("#invite-register").hidden = false;
    $("#invite-login").href = "/login.html?next=" + encodeURIComponent("/invite.html#" + token);
  }
  $("#invite-decline").hidden = false;
}

$("#invite-accept").addEventListener("click", busy($("#invite-accept"), async () => {
  await post("/api/invitations/accept");
  location.replace("/");
}));
const register = $("#invite-register");
register.addEventListener("submit", (e) => {
  e.preventDefault();
  busy(register.querySelector("button.primary"), async () => {
    await post("/api/invitations/register", { username: register.username.value.trim(), password: register.password.value });
    location.replace("/");
  })();
});
// Joining with Google: the token goes in a form's body, never in a URL.
$("#invite-google-btn").addEventListener("click", () => {
  const f = el("form", { method: "POST", action: "/auth/google/start" },
    el("input", { type: "hidden", name: "invite", value: token }));
  document.body.append(f);
  f.submit();
});
$("#invite-decline").addEventListener("click", busy($("#invite-decline"), async () => {
  await post("/api/invitations/decline");
  $("#invite-signed-in").hidden = $("#invite-register").hidden = $("#invite-decline").hidden = true;
  $("#invite-what").textContent = "Invitation declined. You can close this page.";
}));
$("#invite-signout").addEventListener("click", busy($("#invite-signout"), async () => {
  await fetch("/api/logout", { method: "POST" });
  location.reload();
}));

load().catch((e) => {
  $("#invite-what").textContent = e instanceof TypeError ? CANT_REACH : e.message; // a TypeError: fetch failed
});
