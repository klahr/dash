// Dash service worker: makes Dash installable and opens it offline, but
// always prefers the network, so an online app is never an old cached copy.
// The page itself compares versions and offers a reload after an update.
const CACHE = "dash-shell";
const SHELL = ["/", "/style.css", "/app.js", "/twofactor.js", "/editor.js", "/draw.js", "/groups.js", "/vendor/codemirror.js", "/vendor/xterm/xterm.js", "/vendor/xterm/addon-fit.js", "/vendor/xterm/xterm.css", "/vendor/fonts/fonts.css", "/favicon.svg", "/manifest.webmanifest", "/img/icon-192.png"];
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
  if (req.method !== "GET" || url.origin !== location.origin || LIVE.test(url.pathname)) return;
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
