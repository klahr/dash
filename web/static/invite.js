// The invitation page: the link's token is in the fragment, so it never
// reaches server logs or other sites. It shows what the invitation is for
// and lets the invitee accept with the account signed in here, create one,
// or decline.
const $ = (s) => document.querySelector(s);
const token = decodeURIComponent(location.hash.slice(1));
const err = $("#invite-error");

async function post(path, body) {
  const res = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token, ...body }) });
  const data = res.status === 204 ? {} : await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `Failed (${res.status})`);
  return data;
}

function busy(btn, fn) {
  return async (e) => {
    e?.preventDefault();
    err.textContent = "";
    btn.disabled = true;
    try { await fn(); } catch (e) { err.textContent = e.message || "Can't reach the Dash server."; } finally { btn.disabled = false; }
  };
}

const article = (role) => (role === "admin" || role === "owner" ? "an" : "a");

async function load() {
  if (!token) throw new Error("This link has no invitation in it. Ask for the link again.");
  const inv = await post("/api/invitations/lookup");
  const what = $("#invite-what");
  what.replaceChildren(
    inv.invited_by ? `${inv.invited_by} invited you` : "You're invited",
    " to join ", Object.assign(document.createElement("strong"), { textContent: inv.org.name }),
    ` as ${article(inv.role)} ${inv.role}.`);
  document.title = `Join ${inv.org.name} · Dash`;
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
$("#invite-register").addEventListener("submit", (e) => busy(e.target.querySelector("button"), async () => {
  const f = e.target;
  await post("/api/invitations/register", { username: f.username.value.trim(), password: f.password.value });
  location.replace("/");
})(e));
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
  $("#invite-what").textContent = e.message || "Can't reach the Dash server.";
});
