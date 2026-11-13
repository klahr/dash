// Dash service worker: makes Dash installable and opens it offline, but
// always prefers the network, so an online app is never an old cached copy.
// The page itself compares versions and offers a reload after an update.
const CACHE = "dash-shell";
const SHELL = ["/", "/style.css", "/app.js", "/pagekit.js", "/twofactor.js", "/editor.js", "/draw.js", "/groups.js", "/graphlayout.js", "/commitnav.js", "/runnerversion.js", "/activityclear.js", "/usagetime.js",
  // app.js's modules (web/static/app/)
  "/app/account.js", "/app/accountsheet.js", "/app/actions.js", "/app/activity.js", "/app/api.js", "/app/attachments.js", "/app/backend.js", "/app/billing.js", "/app/board.js", "/app/branches.js", "/app/chat.js", "/app/chatpane.js", "/app/checklist.js", "/app/claudefiles.js", "/app/detail.js", "/app/dialogs.js", "/app/diffedit.js", "/app/dnd.js", "/app/dom.js", "/app/drawmode.js", "/app/events.js", "/app/files.js", "/app/history.js", "/app/hovercard.js", "/app/inlineedit.js", "/app/issues.js", "/app/nav.js", "/app/notifications.js", "/app/origin.js", "/app/palette.js", "/app/prefs.js", "/app/preview.js", "/app/projects.js", "/app/releases.js", "/app/review.js", "/app/runners.js", "/app/runstats.js", "/app/scans.js", "/app/settings.js", "/app/sharednotes.js", "/app/state.js", "/app/terminal.js", "/app/timeline.js", "/app/todoform.js", "/app/todos.js", "/app/tokens.js", "/app/try.js", "/app/updates.js", "/app/usage.js", "/app/viewport.js", "/app/vim.js", "/app/wallview.js", "/app/webhooks.js",
  "/vendor/codemirror.js", "/vendor/xterm/xterm.js", "/vendor/xterm/addon-fit.js", "/vendor/xterm/xterm.css", "/vendor/fonts/fonts.css", "/favicon.svg", "/manifest.webmanifest", "/img/icon-192.png"];
const LIVE = /^\/(api|ws|peer|mcp)\//; // never cache data, sockets or other computers

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => Promise.all(SHELL.map((u) =>
    fetch(u, { cache: "no-store" }).then((r) => (r.ok && !r.redirected ? c.put(u, r) : null)).catch(() => null)))));
  self.skipWaiting();
});

self.addEventListener("activate", (e) => {
  e.waitUntil(caches.keys().then((ks) => Promise.all(ks.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  // The wall view isn't the app: caching it as "/" would replace the app's shell.
  if (req.method !== "GET" || url.origin !== location.origin || LIVE.test(url.pathname) || url.pathname === "/wall") return;
  e.respondWith((async () => {
    try {
      const res = await fetch(req, { cache: "no-cache" });
      if (res.ok && !res.redirected) {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put(req.mode === "navigate" ? "/" : req, copy));
      }
      return res;
    } catch (err) {
      const hit = await caches.match(req.mode === "navigate" ? "/" : req);
      if (hit) return hit;
      throw err;
    }
  })());
});

// Web Push: the hub pushes reviews, failures, approvals and failing builds
// (internal/notify/webpush.go), so they arrive while Dash is closed. An open,
// focused Dash shows them itself, so the push is dropped there.
self.addEventListener("push", (e) => {
  let p = {};
  try { p = e.data ? e.data.json() : {}; } catch { p = { title: e.data ? e.data.text() : "Dash" }; }
  e.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    if (p.type !== "webhook.test" && wins.some((c) => c.visibilityState === "visible" && c.focused)) return;
    const actions = (p.actions || []).slice(0, 2).map((a) => ({ action: a.action, title: a.title }));
    await self.registration.showNotification(p.title || "Dash", {
      body: p.body || "", tag: p.tag || undefined, renotify: !!p.tag, icon: "/img/icon-192.png", badge: "/img/icon-192.png",
      requireInteraction: p.type === "permission.needed", actions,
      data: { url: p.url || "/", actions: p.actions || [] },
    });
  })());
});

self.addEventListener("notificationclick", (e) => {
  const n = e.notification;
  const data = n.data || {};
  n.close();
  const act = (data.actions || []).find((a) => a.action === e.action);
  e.waitUntil((async () => {
    if (act) {
      // Approve / Deny: answer without opening Dash; say so if it didn't work.
      const res = await fetch(act.url, { method: "POST" }).catch(() => null);
      if (!res || !res.ok) {
        const why = res && res.status === 409 ? "It was already answered, or the run ended." : "Open Dash to answer it.";
        await self.registration.showNotification("Couldn't answer the request", { body: why, tag: n.tag });
      }
      return;
    }
    const url = new URL(data.url || "/", self.location.origin).href;
    const wins = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const win = wins.find((c) => new URL(c.url).origin === self.location.origin);
    if (win) {
      win.postMessage({ type: "dash.open", url });
      return win.focus();
    }
    return self.clients.openWindow(url);
  })());
});

// The browser renewed the subscription: tell the hub (no page is open).
self.addEventListener("pushsubscriptionchange", (e) => {
  e.waitUntil((async () => {
    const info = await fetch("/api/push").then((r) => (r.ok ? r.json() : null)).catch(() => null);
    if (!info || !info.enabled) return;
    const key = Uint8Array.from(atob(info.key.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
    const sub = e.newSubscription || await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    await fetch("/api/push/subscription", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(sub) });
  })());
});
