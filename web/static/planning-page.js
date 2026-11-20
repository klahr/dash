// planning.html: Planning for planners (product owners), who land here and
// use nothing else (internal/api/planner.go). The board is app/planning.js,
// as in the app's Planning view. This page adds the app's shell around it:
// the sidebar on desktop (which the menu button collapses to its icons) and
// the menu drawer on a phone, with Planning and Chat as its sections and the
// account menu at the bottom (Account, Runners, org switch, Sign out). Chat,
// a card's Write a feature brief and Ask in chat run on the planner's own
// runner (app/plannerclaude.js, app/planner.js); without one online they
// show what's needed instead. It also keeps the live updates, whose stream
// sends a planner Planning's events only. Anyone else is sent to the app, on
// its Planning view.
import { $, $$, h, ico } from "./app/dom.js";
import { api } from "./app/api.js";
import { attempt, fail, toast } from "./app/dialogs.js";
import * as store from "./app/store.js";
import { state } from "./app/state.js";
import { desktop, wsURL } from "./app/viewport.js";
import { cardError, cardId, claudeChanged, closeCard, loadPlanning, mountPlanning, onPlanningEvent, openCard } from "./app/planning.js";
import { askAbout, chatShown, claudeNeeded, loadClaude, onClaudeChange, openRunners } from "./app/plannerclaude.js";
import { openFeatureBrief } from "./app/planner.js";
import { accountSection, openAccount } from "./app/accountsheet.js";
import { pushSubscribe, pushSupported, pushTest, pushUnsubscribe } from "./app/pushsub.js";
import { showOwnAvatar } from "./app/avatar.js";
import { accountHooks } from "./app/myaccount.js";

const me = await api("GET", "/api/me", null, "");
// The card a link opens (/planning/cards/{id} sends a planner here, ?card=<id>).
const linkedCard = new URLSearchParams(location.search).get("card");
if (me.org?.role !== "planner") {
  store.set("dash.view", "planning");
  location.replace(linkedCard ? `/planning/cards/${encodeURIComponent(linkedCard)}` : "/planning");
} else {
  start();
}

function start() {
  showMe(me);
  document.title = `Planning · ${me.org.name}`;
  accountHooks.reloadMe = async () => showMe(await api("GET", "/api/me", null, ""));
  $("#account-open").addEventListener("click", attempt(async () => { closeMenus(); await openAccount(); }));
  $("#logout").addEventListener("click", attempt(async () => {
    await fetch("/api/logout", { method: "POST" });
    location.replace("/login.html");
  }));
  $("#runners-open").addEventListener("click", attempt(async () => { closeMenus(); await openRunners(); }));
  // The sections: Planning and Chat.
  for (const b of $$(".nav > [data-view], .menu-views [data-view]")) {
    b.addEventListener("click", () => { setMenu(false); showView(b.dataset.view); });
  }

  mountPlanning($("#planning-main"), {
    planner: true,
    paneParent: $(".app"),
    hooks: {
      background: () => [$(".topbar"), $(".sidebar"), $("#planning-main")],
      me: () => ({ id: me.user.id, admin: false }),
      claudeNeeded,
      askChat: (c) => { closeCard(); showView("chat"); askAbout(c); },
      // The brief lands on the card, which Planning shows open.
      featureBrief: ({ card, project, idea }) => openFeatureBrief({ card, project, idea,
        onDone: (c) => loadPlanning().then(() => openCard(c.id)).catch(fail) }).catch(fail),
    },
  });
  onClaudeChange(claudeChanged);
  loadClaude().catch(fail);
  loadPlanning().catch(fail);
  connect();
  followCards();
  notifications();
}

// showView shows a section: planning (the board) or chat.
function showView(view) {
  const chat = view === "chat";
  if (chat) closeCard(); // the card's pane would cover the chat
  $("#planning-main").hidden = chat;
  $("#chat-main").hidden = !chat;
  for (const b of $$(".nav > [data-view], .menu-views [data-view]")) {
    const on = b.dataset.view === view;
    b.classList.toggle("active", on);
    if (on) b.setAttribute("aria-current", "page"); else b.removeAttribute("aria-current");
  }
  document.title = `${chat ? "Chat" : "Planning"} · ${me.org.name}`;
  if (chat) chatShown().catch(fail);
}

// notifications: a planner's only ones are card news (they were mentioned
// or added to a card, or its work is ready for review, failed or merged),
// pushed to the browsers they turned push on in (Account → Notifications).
// The switch is this browser's, as in the app (the same preference); the
// rest is kept by the hub. Clicking a pushed card opens it here.
function notifications() {
  const NOTIFY = "dash.notify";
  const form = $("#notify-form");
  const sync = () => {
    form.browser.checked = store.flag(NOTIFY);
    $("#push-hint").hidden = !(store.flag(NOTIFY) && state.pushOn);
  };
  const subscribe = async () => {
    const ok = await pushSubscribe().catch((e) => { console.warn("web push", e); return false; });
    sync();
    return ok;
  };
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js").catch(() => {});
    navigator.serviceWorker.addEventListener("message", (e) => {
      if (e.data?.type !== "dash.open") return;
      const m = new URL(e.data.url, location.origin).pathname.match(/^\/planning\/cards\/([^/]+)$/);
      if (m) attempt(() => openCard(decodeURIComponent(m[1])))();
    });
  }
  if (store.flag(NOTIFY)) subscribe();
  accountSection("notifications", async () => {
    const v = await api("GET", "/api/me/notifications");
    form.cards.checked = v.settings.cards !== false;
    const off = !v.push_enabled ? "This Dash doesn't push notifications." : !pushSupported() ? "This browser can't get pushes (on an iPhone or iPad, add Dash to the home screen first)." : "";
    $("#push-off").textContent = off;
    $("#push-off").hidden = !off;
    sync();
  });
  form.addEventListener("submit", (e) => e.preventDefault());
  form.browser.addEventListener("change", attempt(async () => {
    try {
      if (!form.browser.checked) {
        store.setFlag(NOTIFY, false);
        await pushUnsubscribe().catch(() => {});
        toast("Notifications off");
        return;
      }
      if (!("Notification" in window)) throw new Error("This browser doesn't support notifications.");
      if (await Notification.requestPermission() !== "granted") throw new Error("Notifications are blocked for this site in your browser settings.");
      store.setFlag(NOTIFY, true);
      if (!(await subscribe())) {
        store.setFlag(NOTIFY, false);
        throw new Error("This browser or Dash can't push notifications.");
      }
      toast("Notifications on: you'll hear about your cards, even with Dash closed");
    } finally { sync(); }
  }));
  form.cards.addEventListener("change", attempt(async () => {
    await api("PUT", "/api/me/notifications", { cards: form.cards.checked });
  }));
  $("#push-test").addEventListener("click", attempt(async () => {
    await pushTest();
    toast("Test notification sent", { tone: "ok" });
  }));
}

// followCards keeps ?card=<id> in the URL on the card open, so Back closes
// it (a phone's too) and a reload opens it again; it opens the linked card.
function followCards() {
  const url = (id) => "/planning.html" + (id ? `?card=${encodeURIComponent(id)}` : "");
  history.replaceState({ dash: 1 }, "", url(""));
  let applying = false;
  const show = async (id) => {
    applying = true;
    try {
      if (!id) closeCard();
      else if (cardId() !== id) {
        await openCard(id);
        if (cardError()) { closeCard(); toast("That card isn't here any more, or you can't see it.", { tone: "error" }); }
      }
    } finally { applying = false; }
  };
  document.addEventListener("dash:route", () => setTimeout(() => {
    if (applying) return;
    const want = url(cardId());
    if (want === location.pathname + location.search) return;
    if (!cardId() && history.state?.back === want) history.back();
    else if (cardId() && !history.state?.back) history.pushState({ dash: 1, back: location.pathname + location.search }, "", want);
    else history.replaceState(history.state, "", want);
  }));
  window.addEventListener("popstate", () => show(new URLSearchParams(location.search).get("card")).catch(fail));
  if (linkedCard) {
    show(linkedCard).then(() => {
      if (cardId()) history.pushState({ dash: 1, back: url("") }, "", url(cardId()));
    }).catch(fail);
  }
}

// showMe shows who is signed in: the account menu (name, picture, the orgs
// to switch to) and the org in the top bar. The Account sheet reads state.me.
function showMe(m) {
  state.me = m;
  $("#pl-org").textContent = m.org.name;
  $("#foot-user").textContent = m.username;
  $("#foot-menu summary").title = `Account menu · ${m.username} in ${m.org.name}`;
  $("#foot-menu summary").setAttribute("aria-label", `${m.username}, account menu`);
  showOwnAvatar();
  $("#google-body").hidden = !m.google_available;
  const orgs = m.orgs || [];
  const sw = $("#org-switch");
  sw.hidden = orgs.length < 2;
  sw.replaceChildren(...(orgs.length < 2 ? [] : [
    h("span", { class: "org-switch-label" }, "Switch organization"),
    ...orgs.map((o) => h("button", {
      type: "button", class: o.id === m.org.id ? "active" : "", "aria-current": o.id === m.org.id ? "true" : null,
      onclick: attempt(() => switchOrg(o)),
    }, ico(o.id === m.org.id ? "check" : "users"), o.name)),
  ]));
}

async function switchOrg(o) {
  if (o.id === me.org.id) return;
  const hosts = me.org_domains;
  if (hosts) { location.assign(`${hosts.base_url}/go/${encodeURIComponent(o.slug)}`); return; }
  await api("POST", "/api/auth/switch-org", { org_id: o.id }, "");
  location.replace("/"); // a planner there comes back here; anyone else gets the app
}

// ---- the sidebar (desktop) and the menu drawer (phone), as in the app
// (app/projects.js): the menu button collapses the sidebar to its icons on
// desktop and opens the drawer on a phone, where the drawer is modal.
const app = $(".app");
const menuBtn = $("#menu-open");
const menuOpen = () => app.classList.contains("menu-open");
const sideCollapsed = () => app.classList.contains("side-collapsed");
function syncMenuBtn() {
  const label = !desktop.matches ? "Menu" : sideCollapsed() ? "Expand sidebar" : "Collapse sidebar";
  menuBtn.setAttribute("aria-expanded", String(desktop.matches ? !sideCollapsed() : menuOpen()));
  menuBtn.setAttribute("aria-label", label);
  menuBtn.title = desktop.matches ? `${label} ([)` : "";
}
function setSideCollapsed(on) {
  app.classList.toggle("side-collapsed", on);
  store.setFlag("dash.sideCollapsed", on);
  syncMenuBtn();
}
function setMenu(open) {
  open = open && !desktop.matches;
  const wasOpen = menuOpen();
  const menu = $("#menu");
  app.classList.toggle("menu-open", open);
  $("#menu-scrim").hidden = !open;
  if (open !== wasOpen) {
    for (const el of [$(".topbar"), $("#planning-main"), $("#chat-main")]) el.inert = open;
    if (open) { menu.setAttribute("role", "dialog"); menu.setAttribute("aria-modal", "true"); menu.setAttribute("aria-label", "Menu"); }
    else { menu.removeAttribute("role"); menu.removeAttribute("aria-modal"); menu.removeAttribute("aria-label"); }
    if (open) $(".menu-view", menu).focus();
    else if (!document.activeElement || document.activeElement === document.body || menu.contains(document.activeElement)) menuBtn.focus();
  }
  syncMenuBtn();
}
function closeMenus() {
  $("#foot-menu").open = false;
  setMenu(false);
}
app.classList.toggle("side-collapsed", store.flag("dash.sideCollapsed"));
syncMenuBtn();
menuBtn.addEventListener("click", () => desktop.matches ? setSideCollapsed(!sideCollapsed()) : setMenu(!menuOpen()));
$("#menu-close").addEventListener("click", () => setMenu(false));
$("#menu-scrim").addEventListener("click", () => setMenu(false));
desktop.addEventListener("change", () => setMenu(false));
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && menuOpen()) { e.preventDefault(); setMenu(false); return; }
  // [ collapses the sidebar, as in the app; not while typing.
  if (e.key === "[" && desktop.matches && !e.ctrlKey && !e.metaKey && !e.altKey &&
    !e.target.closest?.("input, textarea, select, [contenteditable], dialog")) setSideCollapsed(!sideCollapsed());
});
// The account menu closes on picking an item, a click outside it or Esc.
{
  const menu = $("#foot-menu");
  $(".foot-menu-list", menu).addEventListener("click", () => { menu.open = false; });
  document.addEventListener("click", (e) => { if (!menu.contains(e.target)) menu.open = false; });
  menu.addEventListener("keydown", (e) => {
    if (e.key !== "Escape" || !menu.open) return;
    e.stopPropagation(); menu.open = false; $("summary", menu).focus();
  });
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
