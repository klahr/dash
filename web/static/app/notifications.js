import { api } from "./api.js";
import * as store from "./store.js";
import { fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { showView } from "./nav.js";
import { renderProjectList, selectProject } from "./projects.js";
import { title } from "./branches.js";
import { openDetail } from "./detail.js";
import { projectKey } from "./events.js";
import { showGoal, waitingGoals } from "./goals.js";
import { go } from "./router.js";

// ------------------------------------------------------------------ notifications & tab title
export const notifyOn = () => store.flag("dash.notify");
// pushed: a notification the hub also pushes to this browser (Web Push),
// which shows it even with Dash closed; the page leaves those to the push.
// kind: its event (task.review, …), shown only if the user wants it pushed
// (Account → Notifications).
export function notify(what, t, detail = "", pushed = false, kind = "") {
  if (!notifyOn() || !("Notification" in window) || Notification.permission !== "granted") return;
  if (!wanted(kind) || othersTask(kind, t)) return;
  if (document.visibilityState === "visible" && document.hasFocus()) return; // the page already shows it
  if (pushed && state.pushOn) return;
  const n = new Notification(`${what}: ${title(t)}`, { body: detail || "", tag: `dash-${t.id}`, silent: false });
  n.onclick = () => { window.focus(); showView("board"); openDetail(t.id); n.close(); };
}
// notifyGoal tells you about a goal (in project p, else the current one)
// that waits for you, is done or out of budget. The hub pushes these too.
export function notifyGoal(what, g, detail = "", p = null) {
  if (!notifyOn() || !("Notification" in window) || Notification.permission !== "granted") return;
  if (!wanted("goal.attention")) return;
  if (document.visibilityState === "visible" && document.hasFocus()) return;
  if (state.pushOn) return;
  const n = new Notification(`${what}: ${g.title}`, { body: detail || "", tag: `dash-goal-${g.id}`, silent: false });
  n.onclick = () => {
    window.focus();
    n.close();
    (async () => {
      if (p && projectKey(p) !== projectKey(state.project)) await selectProject(p);
      await showGoal(g.id);
    })().catch(fail);
  };
}
// The events the user wants pushed (Account → Notifications); null until
// known, or on an older hub: all of them.
function wanted(kind) { return !kind || !state.pushEvents || state.pushEvents.has(kind); }
// othersTask reports whether t's event is about another member's task
// while the user hears of their own only (Account → Notifications → Tasks).
// Tasks Dash made (created_by 0) or whose creator's account is gone (-1)
// are everyone's.
const TASK_EVENTS = new Set(["task.review", "task.failed", "task.merged", "permission.needed"]);
function othersTask(kind, t) {
  if (!TASK_EVENTS.has(kind) || state.pushTasks !== "mine") return false;
  const by = t?.created_by || 0;
  return by > 0 && by !== state.me?.user?.id;
}
async function loadPushEvents() {
  try {
    const v = await api("GET", "/api/me/notifications", null, "");
    state.pushEvents = new Set(v.settings.push);
    state.pushTasks = v.settings.tasks || null;
  } catch { state.pushEvents = state.pushTasks = null; }
}
export async function toggleNotifications() {
  if (!("Notification" in window)) throw new Error("This browser doesn't support notifications.");
  if (notifyOn()) {
    store.setFlag("dash.notify", false);
    await pushUnsubscribe().catch(() => {});
    toast("Notifications off");
  } else {
    const perm = await Notification.requestPermission();
    if (perm !== "granted") throw new Error("Notifications are blocked for this site in your browser settings.");
    store.setFlag("dash.notify", true);
    loadPushEvents();
    let pushed = false;
    try { pushed = await pushSubscribe(); } catch (e) { console.warn("web push", e); }
    toast(pushed
      ? "Notifications on: you'll hear about what you chose in Account → Notifications, even with Dash closed"
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
  if (!notifyOn() || !state.pushOn) throw new Error("Turn push on first (Account → Notifications → Push to this browser); this browser or Dash may not support pushes.");
  const sub = await (await navigator.serviceWorker.ready).pushManager.getSubscription();
  const res = await api("POST", "/api/push/test", { endpoint: sub.endpoint }, "");
  if (!res.ok) throw new Error(`The push didn't go through: ${res.error}`);
  toast("Test notification sent", { tone: "ok" });
}
// Notifications on: keep this browser subscribed to pushes, in the org it's in now.
// Here, after notifyOn and pushSupported: a const called before its line throws
// and would stop the rest of this script.
if (notifyOn()) {
  pushSubscribe().catch((e) => console.warn("web push", e));
  loadPushEvents();
}
// A click on a pushed notification while Dash is open: go to its task.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.addEventListener("message", (e) => {
    if (e.data?.type !== "dash.open") return;
    go(e.data.url).catch(fail); // a link into the app (routes.js), older ones too
  });
}
// Tab title shows how many things wait for you: reviews, permission requests
// and goals waiting for you.
export function updateNeedsYou(items) {
  state.needsYou = (items?.review?.length || 0) + state.perms.length + waitingGoals().length;
  renderTitle();
}
export function renderTitle() {
  const base = state.project ? `${state.project.name} | Dash` : "Dash";
  document.title = state.needsYou ? `(${state.needsYou}) ${base}` : base;
}
