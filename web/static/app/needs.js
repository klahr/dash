import { renderMarkdown } from "../editor.js";
import { stepCommit } from "../commitnav.js";
import { KIND_LABEL, applyEvent, needKey, nextKey, sortNeeds, waitedText } from "../needslogic.js";
import { $, dateTimeText, fill, h } from "./dom.js";
import { api } from "./api.js";
import { fail } from "./dialogs.js";
import { statusLabel, state } from "./state.js";
import { latest, projectName } from "./events.js";
import { projectDot } from "./projects.js";
import { title } from "./branches.js";
import { describePerm, isQuestion } from "./chatparts.js";
import { detailBackground, fillActions } from "./detail.js";
import { forgetQuestionCards, permCard } from "./review.js";
import { diffImages, diffLines, isDiffImage, parseDiff } from "./drawmode.js";
import { detailPane } from "./detailpane.js";
import { changed, href } from "./routes.js";
import { go } from "./router.js";
import { showView } from "./nav.js";

// ------------------------------------------------------------------ needs you
// One list of what waits on the user across every project: tasks in Review,
// Claude's questions and permission requests, and failed tasks, oldest
// first (GET /api/needs, kept current by the event stream: onNeedsEvent).
// Its nav entry counts them. j/k move between the rows and Enter opens one
// in a pane beside the list (full screen on a phone, with Back), where the
// task pane's own buttons act on it (detail.js fillActions) and the board's
// cards answer a request (review.js permCard). Once the open item is dealt
// with, here or anywhere, the pane moves on to the next row.
//
// Only this Dash's projects are listed: on a hub that is every project its
// people see (a project on another computer of the hub, which only platform
// admins see, is not). Its state is state.needs, and nd a function
// declaration, so that nav.js can show and close the view before this
// module has run.
function nd() { return state.needs; }

// project is need n's project, as the project list has it.
const projectOf = (n) => (state.projects || []).find((p) => p.id === n.project_id && !p.prefix) || null;

// needTitle is what a row and the pane call need n: its task's title, else
// what the request asks.
function needTitle(n) {
  if (n.todo) return title(n.todo);
  if (isQuestion(n.perm)) return describePerm(n.perm).detail.split("\n")[0] || "Claude has a question";
  return `Claude wants to ${describePerm(n.perm).title}`;
}

// What a request asks, under its task's title.
const askLine = (n) => (n.perm ? (isQuestion(n.perm) ? "Claude has a question" : `Claude wants to ${describePerm(n.perm).title}`) : "");

// loadNeeds reads the list; events seen while it was on its way are replayed
// over it (events.js latest).
export async function loadNeeds() {
  const { data, seen } = await latest(() => api("GET", "/api/needs", null, ""));
  let list = sortNeeds(data || []);
  for (const { ev, m } of seen) if (m.local) list = applyEvent(list, ev);
  setNeeds(list);
  nd().loaded = true;
  renderNeeds();
}

// onNeedsEvent keeps the list current from this Dash's events.
export function onNeedsEvent(ev) {
  if (!nd().loaded) return;
  let list = applyEvent(nd().list, ev);
  if (list === nd().list) return;
  if (ev.type === "permission.updated" && !ev.data.decision && ev.data.todo_id) list = withTask(list, ev.data);
  setNeeds(list);
}

// withTask gives request p's need its task, which a live request doesn't
// carry, when the list doesn't know it: the board's copy, else read.
function withTask(list, p) {
  const n = list.find((x) => x.perm?.id === p.id);
  if (!n || n.todo) return list;
  const t = state.todos.get(p.todo_id);
  if (t) return list.map((x) => (x === n ? { ...x, todo: t } : x));
  api("GET", `/api/todos/${encodeURIComponent(p.todo_id)}`, null, "").then((got) => {
    if (got?.id) setNeeds(nd().list.map((x) => (x.perm?.id === p.id && !x.todo ? { ...x, todo: got } : x)));
  }).catch(() => {});
  return list;
}

// setNeeds takes list as the new list: the open item, dealt with, gives way
// to the next row; it and the list are drawn again.
function setNeeds(list) {
  const before = nd().list, open = nd().open;
  nd().list = list;
  forgetQuestionCards("needs", list.filter((n) => n.perm).map((n) => n.perm.id));
  if (nd().sel && !list.some((n) => needKey(n) === nd().sel)) nd().sel = nextKey(before, list, nd().sel);
  renderNeeds();
  if (!open) return;
  const next = nextKey(before, list, open);
  if (next === open) renderNeed();
  else if (next && state.view === "needs") openNeed(next, { moved: true });
  else closeNeed();
}

// ---- the list

// renderNeeds draws the list (when its page shows) and the nav's count.
export function renderNeeds() {
  const list = nd().list;
  const badge = $("#needs-badge");
  if (badge) {
    badge.hidden = !list.length;
    badge.textContent = list.length > 99 ? "99+" : String(list.length);
  }
  const nav = $('.nav [data-view="needs"]');
  nav?.setAttribute("aria-label", list.length ? `Needs you: ${list.length} waiting` : "Needs you");
  const ul = $("#needs-list");
  if (!ul || state.view !== "needs") return;
  $("#needs-empty").hidden = !nd().loaded || list.length > 0;
  $("#needs-state").textContent = !nd().loaded ? "Loading…" : list.length ? `${list.length} waiting` : "";
  const focused = document.activeElement?.closest?.("#needs-list li")?.dataset.key;
  const now = Date.now();
  ul.replaceChildren(...list.map((n) => {
    const key = needKey(n), p = projectOf(n), on = key === nd().open, sel = key === (nd().open || nd().sel);
    return h("li", { class: `need k-${n.kind}${sel ? " selected" : ""}`, "data-key": key },
      h("button", { type: "button", class: "need-open", "aria-controls": "need-detail", "aria-current": on ? "true" : null, onclick: () => openNeed(key).catch(fail) },
        h("span", { class: "need-kind" }, KIND_LABEL[n.kind]),
        h("span", { class: "need-main" },
          h("span", { class: "need-title" }, needTitle(n)),
          h("span", { class: "need-sub" },
            p ? h("span", { class: "need-project" }, projectDot(p), projectName(p)) : null,
            n.todo && n.perm ? h("span", { class: "need-ask" }, askLine(n)) : null)),
        h("span", { class: "need-age", title: `Waiting since ${dateTimeText(n.since)}` }, waitedText(n.since, now))));
  }));
  if (focused) $(`#needs-list li[data-key="${CSS.escape(focused)}"] .need-open`)?.focus({ preventScroll: true });
}

// The ages count up while the page shows.
setInterval(() => { if (state.view === "needs" && !document.hidden) renderNeeds(); }, 30000);

// needsShown draws the page as nav.js shows it, which may be at startup,
// before this module has run: so once every module has.
export function needsShown() {
  queueMicrotask(() => {
    renderNeeds();
    if (!nd().loaded) loadNeeds().catch(fail);
  });
}

// stepNeed moves the selection n rows on (back, if negative; Infinity is
// the last): the pane follows while it is open, as History's does.
export function stepNeed(n) {
  const keys = nd().list.map(needKey);
  const cur = nd().open || nd().sel;
  const next = n === -Infinity ? keys[0] : stepCommit(keys, cur, n);
  if (!next) return;
  if (nd().open) { openNeed(next, { moved: true }).catch(fail); return; }
  nd().sel = next;
  renderNeeds();
  const btn = $(`#needs-list li[data-key="${CSS.escape(next)}"] .need-open`);
  btn?.focus({ preventScroll: true });
  btn?.closest("li").scrollIntoView({ block: "nearest" });
}

// openSelected opens the row the selection is on (Enter).
export function openSelected() {
  const key = nd().sel || needKey(nd().list[0] || {});
  if (key) openNeed(key).catch(fail);
}

// ---- the pane

const needPane = detailPane({
  pane: () => $("#need-detail"),
  closeBtn: () => $("#nd-close"),
  background: () => detailBackground(), // detail.js imports this module: read it when called
  isOpen: () => !!nd().open,
  rowFor: (key) => $(`#needs-list li[data-key="${CSS.escape(key)}"] .need-open`),
  onHidden: () => $(".app").classList.remove("need-pane"),
  onViewport: () => renderNeed(),
});

// openedNeed is the open item, as the list has it now.
export const openedNeed = () => nd().list.find((n) => needKey(n) === nd().open) || null;

// openNeed opens item key in the pane. moved: the list moved on to it (j/k,
// or the open one was dealt with), so focus goes along if it was in the pane.
export async function openNeed(key, { moved = false } = {}) {
  const pane = $("#need-detail");
  if (!nd().list.some((n) => needKey(n) === key)) return false;
  const inPane = pane.contains(document.activeElement) || document.activeElement === document.body;
  nd().open = nd().sel = key;
  needPane.open();
  $(".app").classList.add("need-pane"); // the list narrows (style.css)
  renderNeeds();
  renderNeed();
  $("#nd-body").scrollTop = 0;
  $(`#needs-list li[data-key="${CSS.escape(key)}"]`)?.scrollIntoView({ block: "nearest" });
  changed("push");
  if (!moved || inPane) focusPane();
  return true;
}

// focusPane puts focus where the open item is acted on: its question's first
// answer, else its main button, else Close (Back).
function focusPane() {
  const el = $("#nd-body .perm :is(input, textarea)") || $("#nd-body .perm .btn") || $("#nd-actions .btn.grow:not([disabled])") || $("#nd-close");
  el?.focus({ preventScroll: true });
}

export function closeNeed() {
  const key = nd().open;
  if (!key) return;
  nd().open = null;
  changed("close");
  needPane.close(key);
  renderNeeds();
}
$("#nd-close").addEventListener("click", closeNeed);
$("#nd-open").addEventListener("click", () => {
  const n = openedNeed(), p = n && projectOf(n);
  if (!n?.todo || !p) return;
  // On the Board: a task opened from here would open over this page.
  showView("board", { now: true });
  go(href({ view: "board", task: n.todo.id }, p)).catch(fail);
});

// A task's buttons are busy while it is approved or closed (branches.js whileBusy).
document.addEventListener("dash:busy", (e) => { if (openedNeed()?.todo?.id === e.detail) renderNeed(); });

// The changes of the task open, as of its last update: { id, at, text }
// (text null while they load).
let diff = null;

// diffOf is task t's changes, read again once t was updated since.
function diffOf(t) {
  if (diff?.id === t.id && diff.at === t.updated_at) return diff.text;
  const mine = diff = { id: t.id, at: t.updated_at, text: null };
  api("GET", `/api/todos/${t.id}/diff`, null, "").catch((e) => `error: ${e.message}`).then((text) => {
    if (diff !== mine) return;
    mine.text = text;
    renderNeed();
  });
  return null;
}

// renderNeed draws the open item: what it is and where, what to do about it
// (the task pane's buttons, or the request's card), then what the task was
// asked, why it failed, its tests and its changes.
export function renderNeed() {
  const n = openedNeed();
  if (!n || $("#need-detail").hidden) return;
  const t = n.todo, p = projectOf(n);
  $("#nd-title").textContent = needTitle(n);
  fill($("#nd-meta"),
    h("span", { class: `need-kind k-${n.kind}` }, KIND_LABEL[n.kind]),
    p ? h("span", { class: "need-project" }, projectDot(p), projectName(p)) : null,
    h("span", { title: dateTimeText(n.since) }, `waiting ${waitedText(n.since)}`),
    t && n.perm ? h("span", { class: `status s-${t.status}` }, statusLabel(t)) : null,
    t?.pr ? h("a", { class: "pr-link", href: t.pr.url, target: "_blank", rel: "noopener" }, `Pull request #${t.pr.number}`) : null);
  $("#nd-open").hidden = !t || !p;
  const actions = $("#nd-actions");
  if (t && !n.perm) fillActions(actions, t, { project: p, taskPane: false });
  else actions.replaceChildren();
  actions.hidden = !actions.children.length;

  // A request's card is kept across renders (a choice made in it stays), and
  // left in place while it is the same one, so focus in it stays too.
  const card = n.perm ? permCard(n.perm, "needs", "") : null;
  if ($("#nd-perm").firstChild !== card) fill($("#nd-perm"), card);
  $("#nd-perm").hidden = !card;
  const why = n.kind === "failed" ? t.error || "" : "";
  $("#nd-error").hidden = !why;
  $("#nd-error").textContent = why;
  fill($("#nd-checks"), t?.checks ? h("p", { class: `checks-state ${t.checks.passed ? "pass" : "fail"}` },
    t.checks.passed ? "Tests pass" : `Tests fail (exit ${t.checks.exit_code})`) : null);
  $("#nd-prompt-wrap").hidden = !t;
  if (t) $("#nd-prompt").innerHTML = renderMarkdown(t.prompt || "");
  // The changes of a task to review or retry (a running one's say little
  // yet), drawn again only when they change: files opened stay open.
  const changes = t?.worktree && !n.perm;
  $("#nd-diff-wrap").hidden = !changes;
  if (!changes) return;
  const text = diffOf(t), at = `${t.id}@${t.updated_at}@${text != null}`;
  if ($("#nd-diff").dataset.at === at) return;
  $("#nd-diff").dataset.at = at;
  $("#nd-diffstat").replaceChildren();
  fill($("#nd-diff"), diffView(t, text));
}

// diffView is task t's changes: a fold per file, as in the task pane.
function diffView(t, text) {
  if (text == null) return h("p", { class: "pane-empty" }, "Loading…");
  const files = parseDiff(text);
  if (!files.length) return h("p", { class: "pane-empty" }, text.startsWith("error:") ? text : "No changes compared to the default branch.");
  const add = files.reduce((s, f) => s + f.add, 0), del = files.reduce((s, f) => s + f.del, 0);
  $("#nd-diffstat").replaceChildren(h("span", { class: "plus" }, `+${add}`), " ", h("span", { class: "minus" }, `−${del}`));
  return files.map((f) => h("details", { class: "diff-file" },
    h("summary", {}, h("span", { class: "path" }, f.path),
      h("span", { class: "counts" }, f.binary ? "binary" : [h("span", { class: "plus" }, `+${f.add}`), " ", h("span", { class: "minus" }, `−${f.del}`)])),
    f.binary && (isDiffImage(f.path) || isDiffImage(f.to))
      ? diffImages(f.added ? null : f.path, f.deleted ? null : f.to,
        (side, path) => `/api/todos/${t.id}/image?side=${side}&path=${encodeURIComponent(path)}&v=${f.blobs?.[side === "old" ? 0 : 1] || ""}`)
      : h("div", { class: "diff-lines" }, diffLines(f))));
}

// needsScroller is what Ctrl-d/u scroll on the page: the open item's pane.
export const needsScroller = () => (nd().open ? $("#nd-body") : null);

// For the keys: the open item's request, which y, A and X answer.
export const openedPerm = () => openedNeed()?.perm || null;
