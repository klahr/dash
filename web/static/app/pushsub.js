import { api } from "./api.js";
import { state } from "./state.js";

// ------------------------------------------------------------------ Web Push subscription
// This browser's subscription to the hub's pushes (sw.js shows them, even
// with Dash closed). The app (notifications.js) and a planner's Planning
// page (planning-page.js) both use it.
export const pushSupported = () => "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
const b64urlBytes = (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
// pushSubscribe subscribes this browser to the hub's pushes (permission
// must be granted) and reports whether it did: false where the browser or
// the hub can't (a runner, an old browser, iOS Safari outside the installed app).
export async function pushSubscribe() {
  state.pushOn = false;
  if (!pushSupported() || Notification.permission !== "granted") return false;
  const info = await api("GET", "/api/push");
  if (!info.enabled) return false;
  const reg = await navigator.serviceWorker.ready;
  const key = b64urlBytes(info.key);
  let sub = await reg.pushManager.getSubscription();
  if (sub && sub.options?.applicationServerKey) {
    const had = new Uint8Array(sub.options.applicationServerKey);
    if (had.length !== key.length || had.some((b, i) => b !== key[i])) { await sub.unsubscribe(); sub = null; } // another hub's key
  }
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  await api("PUT", "/api/push/subscription", sub.toJSON());
  state.pushOn = true;
  return true;
}
export async function pushUnsubscribe() {
  state.pushOn = false;
  if (!pushSupported()) return;
  const reg = await navigator.serviceWorker.getRegistration();
  const sub = await reg?.pushManager.getSubscription();
  if (!sub) return;
  await api("DELETE", "/api/push/subscription", { endpoint: sub.endpoint }).catch(() => {});
  await sub.unsubscribe();
}
// pushTest pushes a test notification to this browser.
export async function pushTest() {
  const sub = await (await navigator.serviceWorker.ready).pushManager.getSubscription();
  const res = await api("POST", "/api/push/test", { endpoint: sub.endpoint });
  if (!res.ok) throw new Error(`The push didn't go through: ${res.error}`);
}
