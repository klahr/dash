import { h, ib } from "./dom.js";
import { api } from "./api.js";
import { leave } from "./dialogs.js";
import { draft } from "./todoform.js";

// ------------------------------------------------------------------ app updates
// Installable (PWA), but never stuck on an old copy: the service worker
// prefers the network, and the page compares the version it was loaded with
// against the server's, offering a reload when Dash was updated.
const loadedVersion = document.querySelector('meta[name="dash-version"]')?.content;
let updateShown = false;
export async function checkForUpdate() {
  if (updateShown || !loadedVersion || loadedVersion.startsWith("{{")) return;
  const { version } = await api("GET", "/api/version").catch(() => ({}));
  if (!version || version === loadedVersion) return;
  updateShown = true;
  const bar = h("div", { class: "update-bar", role: "status" },
    h("span", {}, "Dash was updated."),
    ib({ type: "button", class: "btn small primary", onclick: () => { draft.flush(); location.reload(); } }, "redo", "Reload"),
    ib({ type: "button", class: "link inline", onclick: () => leave(bar, "leaving", () => bar.remove()) }, "clock", "Later"));
  document.body.append(bar);
}
setInterval(() => { if (!document.hidden) checkForUpdate(); }, 5 * 60000);
document.addEventListener("visibilitychange", () => { if (!document.hidden) checkForUpdate(); });
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").then((reg) => {
    setInterval(() => reg.update().catch(() => {}), 30 * 60000);
  }).catch(() => {});
}
