import { api, on } from "./api.js";
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
// (Account → Notifications). p is the project it's about (the current one
// by default), where how it's named: the body's first line, as the pushed
// ones have it, unless the title already names it.
export function notify(what, t, detail = "", pushed = false, kind = "", p = state.project, where = p?.name) {
  if (!notifyOn() || !("Notification" in window) || Notification.permission !== "granted") return;
  if (!wanted(kind) || othersTask(kind, t)) return;
  if (document.visibilityState === "visible" && document.hasFocus()) return; // the page already shows it
  if (pushed && state.pushOn) return;
  // One notification per task (a newer one replaces it); one each for events with none.
  const tag = t.id ? `dash-${projectKey(p)}-${t.id}` : `dash-${kind || what}-${projectKey(p)}-${++untagged}`;
  show(`${what}: ${title(t)}`, where === title(t) ? "" : where, detail, tag, p, () => {
    window.focus();
    (async () => {
      if (p && projectKey(p) !== projectKey(state.project)) await selectProject(p);
      showView("board");
      if (t.id) await openDetail(t.id);
    })().catch(fail);
  });
}
let untagged = 0;
// notifyGoal tells you about a goal (in project p, else the current one,
// named where) that waits for you, is done or out of budget. The hub pushes
// these too.
export function notifyGoal(what, g, detail = "", p = null, where = (p || state.project)?.name) {
  if (!notifyOn() || !("Notification" in window) || Notification.permission !== "granted") return;
  if (!wanted("goal.attention")) return;
  if (document.visibilityState === "visible" && document.hasFocus()) return;
  if (state.pushOn) return;
  show(`${what}: ${g.title}`, where, detail, `dash-goal-${g.id}`, p || state.project, () => {
    window.focus();
    (async () => {
      if (p && projectKey(p) !== projectKey(state.project)) await selectProject(p);
      await showGoal(g.id);
    })().catch(fail);
  });
}
// show shows a notification about project p (none: Dash's own), with its
// icon, and where and detail on the body's lines.
function show(head, where, detail, tag, p, open) {
  projectIconURL(p).then((icon) => {
    const n = new Notification(head, { body: [where, detail].filter(Boolean).join("\n"), tag, icon, silent: false });
    n.onclick = () => { n.close(); open(); };
  }).catch((e) => console.warn("notification", e));
}

// A notification about a project shows its icon, as a PNG (many platforms
// don't show an SVG): its image, else its initial on its color, as the
// sidebar has it; Dash's own icon when neither can be drawn. sw.js draws
// the pushed ones' alike.
const DASH_ICON = "/img/icon-192.png", ICON_PX = 192;
const icons = new Map(); // what an icon is drawn from → a promise of its data: URL
function projectIconURL(p) {
  if (!p) return Promise.resolve(DASH_ICON);
  const src = p.icon ? on(p.prefix || "", `/api/uploads/${p.icon}`) : "";
  const initial = [...(p.name || "")][0]?.toUpperCase() || "";
  const key = `${src}|${initial}|${p.color || ""}`;
  if (!icons.has(key)) icons.set(key, drawIcon(src, initial, p.color || "").catch(() => DASH_ICON));
  return icons.get(key);
}
async function drawIcon(src, initial, color) {
  const c = document.createElement("canvas");
  c.width = c.height = ICON_PX;
  const g = c.getContext("2d");
  if (g.roundRect) { g.beginPath(); g.roundRect(0, 0, ICON_PX, ICON_PX, ICON_PX * 0.2); g.clip(); }
  if (src) {
    try {
      // The upload needs the session: an <img> of the same origin sends it.
      const img = new Image();
      img.src = src;
      await img.decode();
      coverImage(g, img, img.naturalWidth, img.naturalHeight, ICON_PX);
      return c.toDataURL("image/png");
    } catch { /* gone, or not an image it can draw: the initial */ }
  }
  if (!initial) return DASH_ICON;
  drawInitial(g, initial, color, ICON_PX);
  return c.toDataURL("image/png");
}
// coverImage draws img (w×h) to fill the size×size square, cropped to it.
function coverImage(g, img, w, h, size) {
  if (!w || !h) { g.drawImage(img, 0, 0, size, size); return; } // an SVG with no size of its own
  const s = Math.max(size / w, size / h);
  g.drawImage(img, (size - w * s) / 2, (size - h * s) / 2, w * s, h * s);
}
// drawInitial fills the square with color ("#rrggbb"; gray without one) and
// draws initial on it, light on a dark color, dark on a light one.
function drawInitial(g, initial, color, size) {
  const bg = /^#[0-9a-f]{6}$/i.test(color) ? color : "#6b7280";
  const n = parseInt(bg.slice(1), 16);
  const light = 0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255) > 160;
  g.fillStyle = bg;
  g.fillRect(0, 0, size, size);
  g.fillStyle = light ? "#1f2328" : "#ffffff";
  g.font = `700 ${Math.round(size * 0.56)}px system-ui, -apple-system, "Segoe UI", sans-serif`;
  g.textAlign = "center";
  const m = g.measureText(initial);
  g.fillText(initial, size / 2, size / 2 + (m.actualBoundingBoxAscent - m.actualBoundingBoxDescent) / 2);
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
