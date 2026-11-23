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
import { pushSubscribe, pushTest, pushUnsubscribe } from "./pushsub.js";

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
    const v = await api("GET", "/api/me/notifications");
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

// ---- Web Push: notifications while Dash is closed (pushsub.js)
export { pushUnsubscribe };
export async function testPush() {
  if (!notifyOn() || !state.pushOn) throw new Error("Turn push on first (Account → Notifications → Push to this browser); this browser or Dash may not support pushes.");
  await pushTest();
  toast("Test notification sent", { tone: "ok" });
}
// Notifications on: keep this browser subscribed to pushes, in the org it's in now.
// Here, after notifyOn: a const called before its line throws
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
