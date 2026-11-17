// planning.html: Planning for planners (product owners), who land here and
// use nothing else (internal/api/planner.go). The board is app/planning.js,
// as in the app's Planning view; this page adds the top bar (org, account
// menu) and the live updates, whose stream sends a planner Planning's
// events only. Anyone else is sent to the app, on its Planning view.
import { $, fill, h, ico } from "./app/dom.js";
import { api } from "./app/api.js";
import { attempt, fail } from "./app/dialogs.js";
import * as store from "./app/store.js";
import { wsURL } from "./app/viewport.js";
import { loadPlanning, mountPlanning, onPlanningEvent } from "./app/planning.js";

const me = await api("GET", "/api/me", null, "");
if (me.org?.role !== "planner") {
  store.set("dash.view", "planning");
  location.replace("/");
} else {
  start();
}

function start() {
  $("#pl-user").textContent = me.username;
  $("#pl-org").textContent = me.org.name;
  document.title = `Planning · ${me.org.name}`;
  const orgs = me.orgs || [];
  const sw = $("#org-switch");
  sw.hidden = orgs.length < 2;
  fill(sw, ...orgs.map((o) => h("button", {
    type: "button", class: o.id === me.org.id ? "active" : "", "aria-current": o.id === me.org.id ? "true" : null,
    onclick: attempt(() => switchOrg(o)),
  }, ico(o.id === me.org.id ? "check" : "users"), o.name)));
  $("#pl-logout").addEventListener("click", attempt(async () => {
    await fetch("/api/logout", { method: "POST" });
    location.replace("/login.html");
  }));

  mountPlanning($("#planning-main"), {
    planner: true,
    paneParent: $(".app"),
    hooks: {
      background: () => [$(".pl-topbar"), $("#planning-main")],
      me: () => ({ id: me.user.id, admin: false }),
    },
  });
  loadPlanning().catch(fail);
  connect();
}

async function switchOrg(o) {
  if (o.id === me.org.id) return;
  const hosts = me.org_domains;
  if (hosts) { location.assign(`${hosts.base_url}/go/${encodeURIComponent(o.slug)}`); return; }
  await api("POST", "/api/auth/switch-org", { org_id: o.id }, "");
  location.replace("/"); // a planner there comes back here; anyone else gets the app
}

// connect keeps the event stream open: Planning's events refresh the board.
function connect() {
  const ws = new WebSocket(wsURL("/ws/events"));
  const live = (on) => {
    $("#live").classList.toggle("on", on);
    $("#live").title = on ? "Live updates connected" : "Live updates disconnected — reconnecting";
    $("#live-label").textContent = on ? "Live" : "Offline";
  };
  ws.onopen = () => { live(true); loadPlanning().catch(() => {}); };
  ws.onmessage = (msg) => {
    const ev = JSON.parse(msg.data);
    if (ev.type?.startsWith("planning.")) onPlanningEvent(ev);
  };
  ws.onclose = async () => {
    live(false);
    const res = await fetch("/api/me").catch(() => null);
    if (res?.status === 401) { location.replace("/login.html"); return; }
    setTimeout(connect, 2000);
  };
}
document.addEventListener("visibilitychange", () => { if (!document.hidden) loadPlanning().catch(() => {}); });
