import { api } from "./api.js";
import { fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { showView } from "./nav.js";
import { renderProjectList, selectProject } from "./projects.js";
import { title } from "./branches.js";
import { openDetail } from "./detail.js";

// ------------------------------------------------------------------ notifications & tab title
export const notifyOn = () => { try { return localStorage.getItem("dash.notify") === "1"; } catch { return false; } };
// pushed: a notification the hub also pushes to this browser (Web Push),
// which shows it even with Dash closed; the page leaves those to the push.
export function notify(what, t, detail = "", pushed = false) {
  if (!notifyOn() || !("Notification" in window) || Notification.permission !== "granted") return;
  if (document.visibilityState === "visible" && document.hasFocus()) return; // the page already shows it
  if (pushed && state.pushOn) return;
  const n = new Notification(`${what}: ${title(t)}`, { body: detail || "", tag: `dash-${t.id}`, silent: false });
  n.onclick = () => { window.focus(); showView("board"); openDetail(t.id); n.close(); };
}
export async function toggleNotifications() {
  if (!("Notification" in window)) throw new Error("This browser doesn't support notifications.");
  if (notifyOn()) {
    try { localStorage.setItem("dash.notify", "0"); } catch {}
    await pushUnsubscribe().catch(() => {});
    toast("Notifications off");
  } else {
    const perm = await Notification.requestPermission();
    if (perm !== "granted") throw new Error("Notifications are blocked for this site in your browser settings.");
    try { localStorage.setItem("dash.notify", "1"); } catch {}
    let pushed = false;
    try { pushed = await pushSubscribe(); } catch (e) { console.warn("web push", e); }
    toast(pushed
      ? "Notifications on: you'll hear about reviews, failures, approvals and failing builds, even with Dash closed"
      : "Notifications on: you'll hear about reviews, failures and approvals while Dash is in the background");
  }
  renderProjectList();
}

// ---- Web Push: notifications while Dash is closed (sw.js shows them)
const pushSupported = () => "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
const b64urlBytes = (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
// pushSubscribe subscribes this browser to the hub's pushes (permission
// must be granted) and reports whether it did: false where the browser or
// the hub can't (a runner, an old browser, iOS Safari outside the installed app).
async function pushSubscribe() {
  state.pushOn = false;
  if (!pushSupported() || Notification.permission !== "granted") return false;
  const info = await api("GET", "/api/push", null, "");
  if (!info.enabled) return false;
  const reg = await navigator.serviceWorker.ready;
  const key = b64urlBytes(info.key);
  let sub = await reg.pushManager.getSubscription();
  if (sub && sub.options?.applicationServerKey) {
    const had = new Uint8Array(sub.options.applicationServerKey);
    if (had.length !== key.length || had.some((b, i) => b !== key[i])) { await sub.unsubscribe(); sub = null; } // another hub's key
  }
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  await api("PUT", "/api/push/subscription", sub.toJSON(), "");
  state.pushOn = true;
  return true;
}
export async function pushUnsubscribe() {
  state.pushOn = false;
  if (!pushSupported()) return;
  const reg = await navigator.serviceWorker.getRegistration();
  const sub = await reg?.pushManager.getSubscription();
  if (!sub) return;
  await api("DELETE", "/api/push/subscription", { endpoint: sub.endpoint }, "").catch(() => {});
  await sub.unsubscribe();
}
export async function testPush() {
  if (!notifyOn() || !state.pushOn) throw new Error("Turn notifications on first (in Preferences); this browser or Dash may not support pushes.");
  const sub = await (await navigator.serviceWorker.ready).pushManager.getSubscription();
  const res = await api("POST", "/api/push/test", { endpoint: sub.endpoint }, "");
  if (!res.ok) throw new Error(`The push didn't go through: ${res.error}`);
  toast("Test notification sent", { tone: "ok" });
}
// Notifications on: keep this browser subscribed to pushes, in the org it's in now.
// Here, after notifyOn and pushSupported: a const called before its line throws
// and would stop the rest of this script.
if (notifyOn()) pushSubscribe().catch((e) => console.warn("web push", e));
// A click on a pushed notification while Dash is open: go to its task.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.addEventListener("message", (e) => {
    if (e.data?.type !== "dash.open") return;
    const q = new URL(e.data.url, location.origin).searchParams;
    const p = state.projects.find((x) => x.id === q.get("project"));
    (async () => {
      if (p && p.id !== state.project?.id) await selectProject(p);
      if (q.get("task") && p) { showView("board"); await openDetail(q.get("task")); }
    })().catch(fail);
  });
}
// Tab title shows how many things wait for you: reviews + permission requests.
export function updateNeedsYou(items) {
  state.needsYou = (items?.review?.length || 0) + state.perms.length;
  renderTitle();
}
export function renderTitle() {
  const base = state.project ? `${state.project.name} | Dash` : "Dash";
  document.title = state.needsYou ? `(${state.needsYou}) ${base}` : base;
}
