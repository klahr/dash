// Dash service worker: makes Dash installable and opens it offline, but
// always prefers the network, so an online app is never an old cached copy.
// The page itself compares versions and offers a reload after an update.
const CACHE = "dash-shell-2"; // -2: drops a shell an older worker cached from another page
const SHELL = ["/", "/style.css", "/app.js", "/pagekit.js", "/twofactor.js", "/editor.js", "/codehl.js", "/markdown.js", "/draw.js", "/groups.js", "/graphlayout.js", "/commitnav.js", "/runnerversion.js", "/activityclear.js", "/usagetime.js", "/eta.js", "/deployeta.js", "/buildeta.js", "/claudepill.js", "/tagtext.js", "/time.js", "/voiceloop.js", "/billingstate.js", "/brandcolor.js", "/needslogic.js",
  // app.js's modules (web/static/app/)
  "/app/account.js", "/app/accountsheet.js", "/app/admin.js", "/app/actions.js", "/app/activity.js", "/app/agents.js", "/app/api.js", "/app/attachments.js", "/app/autosave.js", "/app/avatar.js", "/app/backend.js", "/app/billing.js", "/app/board.js", "/app/branches.js", "/app/carddrag.js", "/app/chat.js", "/app/chatpane.js", "/app/chatparts.js", "/app/claudefiles.js", "/app/claudestatus.js", "/app/commitpreview.js", "/app/deploys.js", "/app/detail.js", "/app/detailpane.js", "/app/dialogs.js", "/app/dictation.js", "/app/diffedit.js", "/app/dnd.js", "/app/dom.js", "/app/drawmode.js", "/app/events.js", "/app/files.js", "/app/github.js", "/app/glide.js", "/app/goals.js", "/app/hints.js", "/app/history.js", "/app/hovercard.js", "/app/inlineedit.js", "/app/install.js", "/app/labelpicker.js", "/app/lightbox.js", "/app/lockdown.js", "/app/myaccount.js", "/app/nav.js", "/app/needs.js", "/app/notice.js", "/app/notifications.js", "/app/notifysettings.js", "/app/origin.js", "/app/palette.js", "/app/people.js", "/app/planner.js", "/app/plannerclaude.js", "/app/planning.js", "/app/planningview.js", "/app/popmenu.js", "/app/prefs.js", "/app/pushsub.js", "/app/preview.js", "/app/projects.js", "/app/releaseform.js", "/app/releases.js", "/app/review.js", "/app/roles.js", "/app/router.js", "/app/routes.js", "/app/runners.js", "/app/runstats.js", "/app/scans.js", "/app/screenshots.js", "/app/settings.js", "/app/sheetdraft.js", "/app/spending.js", "/app/speech.js", "/app/stars.js", "/app/state.js", "/app/status.js", "/app/store.js", "/app/tags.js", "/app/teamsetup.js", "/app/teamusage.js", "/app/terminal.js", "/app/timeline.js", "/app/todoform.js", "/app/todos.js", "/app/tokens.js", "/app/try.js", "/app/updates.js", "/app/usage.js", "/app/viewport.js", "/app/vim.js", "/app/voice.js", "/app/voicemode.js", "/app/wallview.js", "/app/webhooks.js",
  "/vendor/codemirror.js", "/vendor/xterm/xterm.js", "/vendor/xterm/addon-fit.js", "/vendor/xterm/xterm.css", "/vendor/fonts/fonts.css", "/favicon.svg", "/manifest.webmanifest", "/img/icon-192.png"];
// isShell: res is the app's page (internal/api/static.go marks it).
const isShell = (res) => res.headers.get("X-Dash-App-Shell") === "1";
const LIVE = /^\/(api|ws|peer|mcp)\//; // never cache data, sockets or other computers

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => Promise.all(SHELL.map((u) =>
    fetch(u, { cache: "no-store" }).then((r) => (r.ok && !r.redirected && (u !== "/" || isShell(r)) ? c.put(u, r) : null)).catch(() => null)))));
  self.skipWaiting();
});

self.addEventListener("activate", (e) => {
  e.waitUntil(caches.keys().then((ks) => Promise.all(ks.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  // The wall view and a planner's Planning page aren't the app: caching them as "/" would replace the app's shell.
  if (req.method !== "GET" || url.origin !== location.origin || LIVE.test(url.pathname) || url.pathname === "/wall" || url.pathname === "/planning.html") return;
  e.respondWith((async () => {
    try {
      const res = await fetch(req, { cache: "no-cache" });
      // A page is cached as the shell only if it's the app's own: sign-in,
      // landing, legal and invite pages would replace it.
      const nav = req.mode === "navigate";
      if (res.ok && !res.redirected && (!nav || isShell(res))) {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put(nav ? "/" : req, copy));
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
// focused Dash shows them itself, so there the push shows only briefly and
// silently: a push must show a notification, or the browser shows its own.
self.addEventListener("push", (e) => {
  let p = {};
  try { p = e.data ? e.data.json() : {}; } catch { p = { title: e.data ? e.data.text() : "Dash" }; }
  e.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const brief = p.type !== "webhook.test" && wins.some((c) => c.visibilityState === "visible" && c.focused);
    const tag = p.tag || `dash-push-${Date.now()}`;
    const actions = (p.actions || []).slice(0, 2).map((a) => ({ action: a.action, title: a.title }));
    const icon = await projectIcon(p.icon || "", p.initial || "", p.color || "").catch(() => DASH_ICON);
    await self.registration.showNotification(p.title || "Dash", {
      body: p.body || "", tag, renotify: !!p.tag && !brief, silent: brief, icon, badge: DASH_ICON,
      requireInteraction: !brief && p.type === "permission.needed", actions,
      data: { url: p.url || "/", actions: p.actions || [] },
    });
    if (!brief) return;
    await new Promise((r) => setTimeout(r, 4000));
    for (const n of await self.registration.getNotifications({ tag })) n.close();
  })());
});

// A pushed notification about a project shows its icon, as a PNG (many
// platforms don't show an SVG): its image (src, which needs the session the
// fetch sends), else its initial on its color, as the sidebar has it; Dash's
// own icon when neither can be drawn. The page draws its own alike
// (app/notifications.js). A worker can't decode an SVG: those get the initial.
const DASH_ICON = "/img/icon-192.png", ICON_PX = 192;
const icons = new Map(); // what an icon is drawn from → a promise of its data: URL
function projectIcon(src, initial, color) {
  if ((!src && !initial) || typeof OffscreenCanvas === "undefined") return Promise.resolve(DASH_ICON);
  const key = `${src}|${initial}|${color}`;
  if (!icons.has(key)) icons.set(key, drawIcon(src, initial, color).catch(() => DASH_ICON));
  return icons.get(key);
}
async function drawIcon(src, initial, color) {
  const c = new OffscreenCanvas(ICON_PX, ICON_PX);
  const g = c.getContext("2d");
  if (g.roundRect) { g.beginPath(); g.roundRect(0, 0, ICON_PX, ICON_PX, ICON_PX * 0.2); g.clip(); }
  let drawn = false;
  if (src) {
    try {
      const res = await fetch(src, { credentials: "same-origin" });
      if (!res.ok) throw new Error(res.statusText);
      const img = await createImageBitmap(await res.blob());
      const s = Math.max(ICON_PX / img.width, ICON_PX / img.height);
      g.drawImage(img, (ICON_PX - img.width * s) / 2, (ICON_PX - img.height * s) / 2, img.width * s, img.height * s);
      drawn = true;
    } catch { /* gone, or an SVG: the initial */ }
  }
  if (!drawn) {
    if (!initial) return DASH_ICON;
    const bg = /^#[0-9a-f]{6}$/i.test(color) ? color : "#6b7280";
    const n = parseInt(bg.slice(1), 16);
    const light = 0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255) > 160;
    g.fillStyle = bg;
    g.fillRect(0, 0, ICON_PX, ICON_PX);
    g.fillStyle = light ? "#1f2328" : "#ffffff";
    g.font = `700 ${Math.round(ICON_PX * 0.56)}px system-ui, -apple-system, "Segoe UI", sans-serif`;
    g.textAlign = "center";
    const m = g.measureText(initial);
    g.fillText(initial, ICON_PX / 2, ICON_PX / 2 + (m.actualBoundingBoxAscent - m.actualBoundingBoxDescent) / 2);
  }
  // A worker has no object URLs a notification could show: a data: URL.
  const bytes = new Uint8Array(await (await c.convertToBlob({ type: "image/png" })).arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return `data:image/png;base64,${btoa(bin)}`;
}

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
