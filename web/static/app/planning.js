import { renderMarkdown } from "../editor.js";
import { $, $$, avatar, dateTimeText, fill, h, ib, ico } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, busy, fail, leave, toast } from "./dialogs.js";
import * as store from "./store.js";
import { desktop } from "./viewport.js";

// ------------------------------------------------------------------ Planning
// The org's plan, for a product owner (docs/planning.md): cards in four
// fixed columns, each linked to the tasks and goals that carry it out, with
// a status worked out on the server from those, in owner-facing words.
// A card opens in a pane beside the board (full screen with Back on a
// phone; Esc closes), as History's commit pane does.
//
// One module for both places Planning shows: the app's Planning view
// (members, admins, owners; index.html) and planning.html, the page a
// planner lands on. It uses only dom, api, dialogs, store and viewport,
// which load outside the app too, and builds its own markup in the root
// it is mounted on. Planning is the hub's (api prefix "").

// s: the task board's lane colour a column takes (style.css: .s-*).
export const COLUMNS = [
  { key: "ideas", title: "Ideas", s: "draft", empty: "No ideas yet." },
  { key: "planned", title: "Planned", s: "queued", empty: "Nothing planned yet." },
  { key: "in_progress", title: "In progress", s: "in_progress", empty: "Nothing in progress." },
  { key: "done", title: "Done", s: "merged", empty: "Nothing done yet." },
];
const COLUMN_TITLE = Object.fromEntries(COLUMNS.map((c) => [c.key, c.title]));
const PRIORITY = { high: "High", medium: "Medium", low: "Low" };
const call = (method, path, body) => api(method, path, body, "");
const cardAPI = (id = "", rest = "") => `/api/planning/cards${id ? `/${encodeURIComponent(id)}` : ""}${rest}`;

const pl = {
  root: null,
  planner: false,
  hooks: {},
  cards: [],
  projects: [],
  people: [], // the org's members a card may be assigned to
  filter: store.get("dash.planning.project", ""),
  loaded: false,
  open: null, // the open card's id
  detail: null, // its view, with its discussion
  error: "",
  editing: false,
  opener: null,
  timer: 0,
  pending: null, // a press on a card that may become a drag
  drag: null, // the card being dragged
  suppressClick: false,
  lane: "", // the column a phone shows (one at a time, as the task board)
};

const byId = (id) => pl.cards.find((c) => c.id === id);
const paneEl = () => $("#card-detail");

// mountPlanning builds Planning in root. planner hides what a planner
// can't do (link existing work, open it). hooks:
//   visible()    whether the board shows (default: always), to load it
//   background() the elements the pane covers on a phone (made inert)
//   openWork(link) opens a linked task or goal (members)
//   me()         the signed-in user: { id, admin } (to delete comments)
// The card's pane goes in paneParent (default root): outside what
// background() returns, which a phone makes inert while the pane is open.
export function mountPlanning(root, { planner = false, hooks = {}, paneParent = root } = {}) {
  pl.root = root;
  pl.planner = planner;
  pl.hooks = hooks;
  const filter = h("select", { id: "pl-filter", "aria-label": "Project", onchange: (e) => {
    pl.filter = e.target.value;
    store.set("dash.planning.project", pl.filter);
    loadPlanning().catch(fail);
  } });
  fill(root,
    h("div", { class: "page planning" },
      h("header", { class: "cl-head pl-head" },
        h("h2", { class: "cl-title page-title" }, "Planning"),
        h("label", { class: "pl-filter" }, h("span", { class: "sr-only" }, "Project"), filter),
        h("span", { class: "cl-state", id: "pl-state", "aria-live": "polite" }),
        ib({ type: "button", class: "btn small primary pl-new", title: "Add an idea to the plan", onclick: attempt(() => newCard("ideas")) }, "plus", "New card")),
      h("p", { class: "hint pl-intro", id: "pl-intro", hidden: true },
        "The plan: cards move from Ideas to Done. Start a card to make a task or a goal for a developer's project; its status follows the work."),
      // The task board's look (style.css: .lanes, .board, .col, .rail, .todo):
      // on a phone a strip of lanes and one column at a time, on a desktop
      // the columns side by side. Each column ends in Add card.
      h("div", { class: "lanes pl-lanes", role: "tablist", "aria-label": "Columns" },
        ...COLUMNS.map((col) => h("button", { type: "button", role: "tab", class: `lane-tab s-${col.s}`, "data-col": col.key,
          onclick: () => { pl.lane = col.key; renderBoard(); } }, col.title, h("span", { class: "n", id: `pl-tab-n-${col.key}` })))),
      h("div", { class: "board pl-board", id: "pl-board" },
        ...COLUMNS.map((col) => h("section", { class: `col pl-col s-${col.s}`, "data-col": col.key, "aria-labelledby": `pl-col-${col.key}` },
          h("header", { class: "col-head" },
            h("span", { class: "col-label", id: `pl-col-${col.key}` }, col.title), h("span", { class: "n", id: `pl-count-${col.key}` })),
          h("div", { class: "col-body" },
            h("ol", { class: "rail pl-cards", "data-col": col.key, "aria-labelledby": `pl-col-${col.key}` }),
            h("p", { class: "col-empty", id: `pl-empty-${col.key}` }, col.empty),
            ib({ type: "button", class: "pl-add", title: `Add a card to ${col.title}`, onclick: attempt(() => newCard(col.key)) }, "plus", "Add card")))))));
  paneParent.append(
    h("aside", { class: "detail card-detail", id: "card-detail", hidden: true, "aria-labelledby": "pd-title" },
      h("header", { class: "detail-head" },
        h("button", { class: "icon-btn", id: "pd-close", type: "button", "aria-label": "Back", title: "Close (Esc)", onclick: closeCard, innerHTML:
          '<svg class="cd-back" viewBox="0 0 24 24" aria-hidden="true"><path d="m15 6-6 6 6 6" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>' +
          '<svg class="cd-x" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6 6 18" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/></svg>' }),
        h("div", { class: "detail-titles" },
          h("h2", { id: "pd-title" }),
          h("div", { class: "detail-meta", id: "pd-meta" }),
          h("div", { class: "commit-actions", id: "pd-actions" }))),
      h("div", { class: "detail-body", id: "pd-body" })));
  wireDrag(root);
  renderFilter();
}

// loadPlanning reads the cards (of the project picked, or all) and the
// projects, and shows them.
export async function loadPlanning() {
  if (!pl.root) return;
  const q = pl.filter ? `?project=${encodeURIComponent(pl.filter)}` : "";
  const [cards, projects, people] = await Promise.all([call("GET", cardAPI() + q), call("GET", "/api/planning/projects"), call("GET", "/api/planning/people")]);
  pl.cards = cards;
  pl.projects = projects;
  pl.people = people;
  if (pl.filter && !projects.some((p) => p.id === pl.filter)) {
    pl.filter = "";
    store.set("dash.planning.project", "");
  }
  pl.loaded = true;
  renderFilter();
  renderBoard();
  if (pl.open) {
    const c = byId(pl.open);
    if (c && pl.detail) { pl.detail = { ...pl.detail, ...c }; renderCard(); }
  }
}

// onPlanningEvent takes a planning.* event of the event stream: a card, its
// comments or links changed, or work linked to cards did. The board (and
// the open card) are read again, once for a burst of events.
export function onPlanningEvent(ev) {
  if (!pl.root) return;
  const cards = ev.data?.cards || (ev.data?.card ? [ev.data.card] : []);
  clearTimeout(pl.timer);
  pl.timer = setTimeout(() => {
    if (pl.hooks.visible && !pl.hooks.visible() && !pl.open) { pl.loaded = false; return; }
    loadPlanning().catch(() => {});
    if (pl.open && (cards.includes(pl.open) || !cards.length)) {
      if (ev.data?.deleted && ev.data.card === pl.open) { closeCard(); return; }
      loadCard(pl.open).catch(() => {});
    }
  }, 250);
}

// planningShown is called when the Planning view is shown: it loads the
// board if it isn't current.
export function planningShown() {
  if (!pl.loaded) loadPlanning().catch(fail);
}

function renderFilter() {
  const sel = $("#pl-filter");
  if (!sel) return;
  fill(sel, h("option", { value: "" }, "All projects"),
    ...pl.projects.map((p) => h("option", { value: p.id, selected: p.id === pl.filter }, p.name)));
  sel.value = pl.filter;
  sel.hidden = pl.projects.length < 2 && !pl.filter;
}

// ---- the board

function renderBoard() {
  const counts = Object.fromEntries(COLUMNS.map((col) => [col.key, pl.cards.filter((c) => c.column === col.key).length]));
  // A phone opens on the first column with cards in it.
  if (!pl.lane) pl.lane = (pl.cards.length && COLUMNS.find((col) => counts[col.key])?.key) || "ideas";
  for (const col of COLUMNS) {
    const ol = $(`.pl-cards[data-col="${col.key}"]`, pl.root);
    const cards = pl.cards.filter((c) => c.column === col.key).sort((a, b) => a.position - b.position);
    const focused = document.activeElement?.closest?.(".pl-card")?.dataset.id;
    fill(ol, ...cards.map(cardEl));
    ol.hidden = !cards.length;
    $(`#pl-empty-${col.key}`).hidden = cards.length > 0;
    if (focused && ol.querySelector(`[data-id="${focused}"]`)) $(`[data-id="${focused}"] .pl-open`, ol)?.focus({ preventScroll: true });
    $(`#pl-count-${col.key}`).textContent = String(cards.length);
    $(`#pl-tab-n-${col.key}`).textContent = String(cards.length);
    $(`.pl-col[data-col="${col.key}"]`, pl.root).classList.toggle("active", pl.lane === col.key);
    $(`.pl-lanes [data-col="${col.key}"]`, pl.root).setAttribute("aria-selected", String(pl.lane === col.key));
  }
  $("#pl-intro").hidden = pl.cards.length > 0;
  $("#pl-state").textContent = "";
  markSelected();
}

function statusEl(st, cls = "pl-status") {
  return h("span", { class: `${cls} t-${st.tone}` },
    h("span", { class: "pl-status-text" }, st.text),
    st.progress ? h("span", { class: "pl-progress" }, st.progress) : null);
}

// assignee is the card's assignee as avatar() takes a user (null: nobody).
const assignee = (c) => c.assignee ? { id: c.assignee, username: c.assignee_name, avatar: c.assignee_avatar } : null;

// assigneeEl is the assignee's face, with their name for a tooltip and a
// screen reader.
function assigneeEl(c) {
  const u = assignee(c);
  if (!u) return null;
  return h("span", { class: "pl-assignee", title: `Assigned to ${u.username}` },
    avatar(u), h("span", { class: "sr-only" }, `Assigned to ${u.username}`));
}

function cardEl(c) {
  const meta = [
    c.priority ? h("span", { class: `pl-chip p-${c.priority}`, title: "Priority" }, PRIORITY[c.priority]) : null,
    c.target ? h("span", { class: "pl-chip pl-target", title: "Target" }, ico("flag"), c.target) : null,
    !pl.filter && c.project_name ? h("span", { class: "pl-project" }, c.project_name) : null,
    c.comments ? h("span", { class: "pl-comments", title: `${c.comments} comment${c.comments === 1 ? "" : "s"}` }, ico("comment"), String(c.comments)) : null,
    assigneeEl(c),
  ].filter(Boolean);
  if (c.status?.suggest_done) meta.push(h("span", { class: "pl-suggest" }, "Ready for Done"));
  // Under the title: the status of the linked work, or else the start of the description.
  const about = (c.description || "").replace(/[#*_`>]+/g, "").replace(/\s+/g, " ").trim();
  const sub = c.status?.total ? h("span", { class: "todo-sub" }, statusEl(c.status))
    : about ? h("span", { class: "todo-sub" }, about) : null;
  // Laid out as the task board's card (app/branches.js: todoRow): the node
  // in the status's colour (filled once Done), the title the button that
  // opens the card, and a press anywhere else on it opens it too.
  return h("li", { class: "pl-card", "data-id": c.id },
    h("div", { class: `todo t-${c.status?.tone || "none"}${c.column === "done" ? " pushed" : ""}${c.id === pl.open ? " selected" : ""}`,
      onclick: () => openCard(c.id).catch(fail) },
      h("span", { class: "node", "aria-hidden": "true" }),
      h("span", { class: "todo-main" },
        h("button", { type: "button", class: "todo-open pl-open", "aria-controls": "card-detail", "aria-current": c.id === pl.open ? "true" : null,
          title: "Open (Alt+arrows move it; Space over it assigns it to you)", onkeydown: (e) => cardKeys(e, c) },
          h("span", { class: "todo-title" }, c.title)),
        sub),
      meta.length ? h("span", { class: "todo-side" }, ...meta) : null));
}

// ---- dragging, as the task board drags (dnd.js): a mouse drags after a
// few pixels, a finger after a long press (a quick swipe still scrolls).
// A ghost of the card follows the pointer; a line shows where it lands.

// dropIndex is where in ol (among its cards but the one dragged) a drop at
// y goes, and the card it lands before (or the last, to land after).
function dropIndex(ol, y) {
  const items = $$(".pl-card", ol).filter((li) => li.dataset.id !== pl.drag?.id);
  const i = items.findIndex((li) => { const r = li.getBoundingClientRect(); return y < r.top + r.height / 2; });
  return { index: i < 0 ? items.length : i, before: i < 0 ? null : items[i], last: items.at(-1) };
}

function clearDropMarks() {
  $$(".drop-over", pl.root).forEach((x) => x.classList.remove("drop-over"));
  $$(".pl-card.drop-before, .pl-card.drop-after", pl.root).forEach((x) => x.classList.remove("drop-before", "drop-after"));
}

// dropTarget is the list of the column under the point, if any: its column,
// or (on a phone, one column at a time) its lane tab, which drops at the end.
function dropTarget(x, y) {
  const el = document.elementFromPoint(x, y);
  const at = el?.closest?.(".pl-col, .pl-lanes .lane-tab");
  if (!at || !pl.root.contains(at)) return null;
  return $(`.pl-cards[data-col="${at.dataset.col}"]`, pl.root);
}

function startDrag(x, y) {
  const p = pl.pending;
  pl.pending = null;
  if (!p || !p.li.isConnected) return;
  const r = p.li.getBoundingClientRect();
  const ghost = p.li.cloneNode(true);
  ghost.classList.add("pl-ghost");
  ghost.style.width = `${r.width}px`;
  document.body.append(ghost);
  pl.drag = { id: p.id, li: p.li, ghost, dx: p.x - r.left, dy: p.y - r.top };
  p.li.classList.add("dragging");
  moveDrag(x, y);
}

function moveDrag(x, y) {
  const d = pl.drag;
  d.ghost.style.transform = `translate(${x - d.dx}px, ${y - d.dy}px)`;
  clearDropMarks();
  const ol = dropTarget(x, y);
  if (!ol) return;
  const tab = document.elementFromPoint(x, y)?.closest?.(".pl-lanes .lane-tab");
  (tab || ol.closest(".pl-col")).classList.add("drop-over");
  if (tab) return; // to the end of that column
  const at = dropIndex(ol, y);
  if (at.before) at.before.classList.add("drop-before");
  else at.last?.classList.add("drop-after");
}

// endDrag drops the card at the point (drop), or puts it back.
function endDrag(x, y, drop) {
  const d = pl.drag;
  if (!d) return;
  d.ghost.remove();
  d.li.classList.remove("dragging");
  clearDropMarks();
  const ol = drop ? dropTarget(x, y) : null;
  const onTab = ol && document.elementFromPoint(x, y)?.closest?.(".pl-lanes .lane-tab");
  const index = !ol ? 0 : onTab ? 1 << 20 : dropIndex(ol, y).index;
  pl.drag = null;
  // The click that ends a mouse drag isn't a click on the card.
  pl.suppressClick = true;
  setTimeout(() => { pl.suppressClick = false; }, 0);
  if (ol) moveCard(d.id, ol.dataset.col, index).catch(fail);
}

function cancelPending() {
  if (pl.pending?.timer) clearTimeout(pl.pending.timer);
  pl.pending = null;
}

function wireDrag(root) {
  root.addEventListener("pointerdown", (e) => {
    const li = e.target.closest?.(".pl-card");
    if (!li || e.button > 0 || !root.contains(li)) return;
    pl.pending = { li, id: li.dataset.id, x: e.clientX, y: e.clientY, touch: e.pointerType !== "mouse" };
    if (pl.pending.touch) pl.pending.timer = setTimeout(() => pl.pending && startDrag(pl.pending.x, pl.pending.y), 320);
  });
  document.addEventListener("pointermove", (e) => {
    if (pl.drag) { moveDrag(e.clientX, e.clientY); return; }
    const p = pl.pending;
    if (!p) return;
    const dist = Math.hypot(e.clientX - p.x, e.clientY - p.y);
    if (p.touch) { if (dist > 10) cancelPending(); return; } // a scroll
    if (dist > 6) startDrag(e.clientX, e.clientY);
  });
  document.addEventListener("pointerup", (e) => { if (pl.drag) endDrag(e.clientX, e.clientY, true); cancelPending(); });
  document.addEventListener("pointercancel", () => { if (pl.drag) endDrag(0, 0, false); cancelPending(); });
  document.addEventListener("touchmove", (e) => { if (pl.drag) e.preventDefault(); }, { passive: false });
  document.addEventListener("keydown", (e) => { if (pl.drag && e.key === "Escape") { e.stopPropagation(); endDrag(0, 0, false); } }, true);
  // A long press opens no menu on the card it drags.
  root.addEventListener("contextmenu", (e) => { if (pl.drag || pl.pending?.touch) e.preventDefault(); });
  root.addEventListener("click", (e) => {
    if (pl.suppressClick && e.target.closest?.(".pl-card")) { e.stopPropagation(); e.preventDefault(); }
  }, true);
}

// cardKeys: Alt+Left/Right moves a card to the column beside, Alt+Up/Down
// up or down its column. (On a phone, the card's pane has the column.)
function cardKeys(e, c) {
  if (!e.altKey || e.ctrlKey || e.metaKey) return;
  const cols = COLUMNS.map((x) => x.key);
  const same = pl.cards.filter((x) => x.column === c.column).sort((a, b) => a.position - b.position);
  const at = same.findIndex((x) => x.id === c.id);
  let to = null;
  if (e.key === "ArrowLeft" && cols.indexOf(c.column) > 0) to = [cols[cols.indexOf(c.column) - 1], 1 << 20];
  if (e.key === "ArrowRight" && cols.indexOf(c.column) < cols.length - 1) to = [cols[cols.indexOf(c.column) + 1], 1 << 20];
  if (e.key === "ArrowUp" && at > 0) to = [c.column, at - 1];
  if (e.key === "ArrowDown" && at < same.length - 1) to = [c.column, at + 1];
  if (!to) return;
  e.preventDefault();
  moveCard(c.id, to[0], to[1]).then(() => $(`.pl-card[data-id="${c.id}"] .pl-open`, pl.root)?.focus()).catch(fail);
}

// Space over a card (the mouse on it) assigns it to you, or unassigns it if
// it is yours already. Not while typing, a dialog is open, a card is being
// dragged, or the board isn't shown. A focused button elsewhere isn't
// pressed: the keyup that would press it is swallowed too.
let spaceTaken = false;
document.addEventListener("keydown", (e) => {
  if (e.key !== " " || e.repeat || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || !pl.root || pl.drag) return;
  if (document.querySelector("dialog[open]") || (pl.hooks.visible && !pl.hooks.visible())) return;
  if (e.target.closest?.("input, textarea, select, [contenteditable]:not([contenteditable=false]), .cm-editor")) return;
  const li = pl.root.querySelector(".pl-card:hover");
  const c = li && byId(li.dataset.id);
  if (!c) return;
  e.preventDefault();
  spaceTaken = true;
  toggleMine(c).catch(fail);
});
document.addEventListener("keyup", (e) => {
  if (e.key === " " && spaceTaken) { spaceTaken = false; e.preventDefault(); }
});

async function toggleMine(c) {
  const me = pl.hooks.me?.();
  if (!me?.id) return;
  const mine = c.assignee === me.id;
  const v = await call("PATCH", cardAPI(c.id), { assignee: mine ? 0 : me.id });
  const i = pl.cards.findIndex((x) => x.id === c.id);
  if (i >= 0) { pl.cards[i] = { ...pl.cards[i], ...v }; renderBoard(); }
  if (pl.open === c.id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  toast(mine ? "Unassigned" : "Assigned to you");
}

// moveCard puts a card in column col at index (among the column's other
// cards), on the board at once and then on the server.
async function moveCard(id, col, index) {
  const c = byId(id);
  if (!c) return;
  const others = pl.cards.filter((x) => x.column === col && x.id !== id).sort((a, b) => a.position - b.position);
  const pos = Math.max(0, Math.min(index, others.length));
  if (c.column === col) {
    const now = pl.cards.filter((x) => x.column === col).sort((a, b) => a.position - b.position).findIndex((x) => x.id === id);
    if (now === pos) return;
  }
  const from = c.column;
  others.splice(pos, 0, c);
  others.forEach((x, i) => { x.position = i; });
  c.column = col;
  pl.cards.filter((x) => x.column === from && x.id !== id).sort((a, b) => a.position - b.position).forEach((x, i) => { x.position = i; });
  renderBoard();
  try {
    const v = await call("POST", cardAPI(id, "/move"), { column: col, position: pos });
    Object.assign(c, v);
    if (pl.open === id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  } catch (err) {
    loadPlanning().catch(() => {});
    throw err;
  }
}

async function newCard(col) {
  const title = await ask({ title: `New card in ${COLUMN_TITLE[col]}`, body: "What should be built? You can add the details, a priority and a target on the card.", input: true, placeholder: "Export reports as PDF", ok: "Add card" });
  if (!title) return;
  const c = await call("POST", cardAPI(), { title, column: col, project: pl.filter || undefined });
  pl.cards.push(c);
  pl.lane = col;
  renderBoard();
  await openCard(c.id);
}

// ---- the card's pane

function setModal() {
  const on = !paneEl().hidden && !desktop.matches;
  (pl.hooks.background?.() || []).forEach((el) => { el.inert = on; });
  $("#pd-close").setAttribute("aria-label", desktop.matches ? "Close" : "Back");
}
desktop.addEventListener("change", () => { if (pl.open) { setModal(); renderCard(); } });

export async function openCard(id) {
  const pane = paneEl();
  if (pl.open === id && !pane.hidden) return;
  if (pane.hidden) {
    const a = document.activeElement;
    pl.opener = a && a !== document.body ? a : null;
  }
  pl.open = id;
  pl.detail = byId(id) || null;
  pl.error = "";
  pl.editing = false;
  pane.hidden = false;
  pl.root.classList.add("card-open");
  setModal();
  markSelected();
  renderCard();
  $("#pd-body").scrollTop = 0;
  await loadCard(id);
}

async function loadCard(id) {
  try {
    const c = await call("GET", cardAPI(id));
    if (pl.open !== id) return;
    pl.detail = c;
    pl.error = "";
    const i = pl.cards.findIndex((x) => x.id === id);
    if (i >= 0) pl.cards[i] = { ...c, discussion: undefined };
  } catch (e) {
    if (pl.open !== id) return;
    pl.error = e.status === 404 ? "This card was deleted." : e.message;
  }
  renderCard();
}

export function closeCard() {
  if (!pl.open) return;
  const pane = paneEl();
  const id = pl.open;
  pl.open = pl.detail = null;
  pl.editing = false;
  markSelected();
  const hide = () => { if (!pl.open) { pane.hidden = true; pl.root.classList.remove("card-open"); setModal(); } };
  if (!pane.hidden) leave(pane, "closing", hide); else hide();
  (pl.hooks.background?.() || []).forEach((el) => { el.inert = false; });
  const opener = pl.opener;
  pl.opener = null;
  const a = document.activeElement;
  if (a && a !== document.body && !pane.contains(a)) return;
  ($(`.pl-card[data-id="${id}"] .pl-open`, pl.root) || (opener?.isConnected ? opener : null))?.focus();
}
export const cardOpen = () => !!pl.open;

// Esc closes the card (not while a dialog over it, or a field in it, has it).
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !pl.open || document.querySelector("dialog[open]")) return;
  if (e.target.closest?.("#card-detail textarea, #card-detail input")) { e.target.blur(); return; }
  e.preventDefault();
  closeCard();
});

function markSelected() {
  for (const li of $$(".pl-card", pl.root)) {
    const on = li.dataset.id === pl.open;
    $(".todo", li)?.classList.toggle("selected", on);
    $(".pl-open", li)?.toggleAttribute("aria-current", on);
  }
}

const patchCard = (body) => async () => {
  const id = pl.open;
  const v = await call("PATCH", cardAPI(id), body);
  if (pl.open === id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  const i = pl.cards.findIndex((x) => x.id === id);
  if (i >= 0) { pl.cards[i] = { ...pl.cards[i], ...v }; renderBoard(); }
};

// assigneeOptions are the people a card may be assigned to, as [id, name]:
// the org's members, and the card's assignee if the list doesn't have them.
function assigneeOptions(c) {
  const opts = pl.people.map((u) => [String(u.id), u.username]);
  if (c.assignee && !pl.people.some((u) => u.id === c.assignee)) opts.push([String(c.assignee), c.assignee_name || "Someone"]);
  return opts;
}

function field(label, control) {
  return h("label", { class: "field pl-field" }, h("span", {}, label), control);
}

function renderCard() {
  const c = pl.detail;
  if (!c || paneEl().hidden) return;
  $("#pd-title").textContent = c.title || "";
  fill($("#pd-meta"),
    h("span", { class: "pl-col-tag" }, COLUMN_TITLE[c.column] || c.column),
    c.status ? statusEl(c.status) : null,
    c.created_at ? h("span", { title: dateTimeText(c.created_at) }, `added ${new Date(c.created_at).toLocaleDateString()}`) : null);
  const started = (c.links || []).length > 0;
  fill($("#pd-actions"),
    ib({ type: "button", class: `btn small ${started ? "" : "primary"}`, title: "Make a task or a goal in a project for this card", onclick: attempt(() => startSheet(c)) }, "play", "Start…"),
    pl.planner ? null : ib({ type: "button", class: "btn small", title: "Link a task or goal that's already on a board", onclick: attempt(() => linkExistingSheet(c)) }, "link", "Link existing…"),
    ib({ type: "button", class: "btn small", title: "Change the title and description", onclick: () => { pl.editing = true; renderCard(); $("#pd-edit-title")?.focus(); } }, "edit", "Edit"));
  const body = $("#pd-body");
  if (pl.error) { fill(body, h("p", { class: "error-note" }, pl.error)); return; }

  const sel = (opts, value, onchange, label) => h("select", { "aria-label": label, onchange }, ...opts.map(([v, t]) => h("option", { value: v, selected: v === value }, t)));
  const facts = h("div", { class: "pl-fields" },
    field("Column", sel(COLUMNS.map((x) => [x.key, x.title]), c.column, (e) => moveCard(c.id, e.target.value, 1 << 20).catch(fail), "Column")),
    field("Priority", sel([["", "None"], ["high", "High"], ["medium", "Medium"], ["low", "Low"]], c.priority || "", (e) => patchCard({ priority: e.target.value })().catch(fail), "Priority")),
    field("Target", h("input", { value: c.target || "", placeholder: "1.4, or a date", maxlength: 60, autocomplete: "off",
      onchange: (e) => patchCard({ target: e.target.value })().catch(fail) })),
    field("Project", sel([["", "None yet"], ...pl.projects.map((p) => [p.id, p.name])], c.project_id || "",
      (e) => patchCard({ project: e.target.value })().catch(fail), "Project")),
    field("Assignee", h("span", { class: "pl-assign-row" }, assignee(c) ? avatar(assignee(c)) : null,
      sel([["", "Nobody"], ...assigneeOptions(c)], c.assignee ? String(c.assignee) : "",
        (e) => patchCard({ assignee: Number(e.target.value) || 0 })().catch(fail), "Assignee"))));

  const suggest = c.status?.suggest_done
    ? h("div", { class: "pl-suggest-box" },
      h("p", {}, "Everything linked is done."),
      ib({ type: "button", class: "btn small primary", onclick: attempt((e) => busy(e.currentTarget, () => moveCard(c.id, "done", 0))) }, "check", "Move to Done"))
    : null;

  let desc;
  if (pl.editing) {
    const t = h("input", { id: "pd-edit-title", value: c.title, maxlength: 200, autocomplete: "off", "aria-label": "Title" });
    const d = h("textarea", { rows: 8, placeholder: "What it is, who it's for, and how you'll know it's done. Markdown works.", "aria-label": "Description" }, c.description || "");
    const save = attempt((e) => busy(e.currentTarget, async () => { await patchCard({ title: t.value, description: d.value })(); pl.editing = false; renderCard(); }));
    desc = h("div", { class: "pl-edit" }, field("Title", t), field("Description", d),
      h("div", { class: "pl-edit-actions" },
        ib({ type: "button", class: "btn small", onclick: () => { pl.editing = false; renderCard(); } }, "x", "Cancel"),
        ib({ type: "button", class: "btn small primary", onclick: save }, "check", "Save")));
  } else {
    desc = c.description
      ? h("div", { class: "pl-desc reply", innerHTML: renderMarkdown(c.description) })
      : h("p", { class: "hint" }, "No description yet. ", h("button", { type: "button", class: "link", onclick: () => { pl.editing = true; renderCard(); } }, "Add one"));
  }

  const links = c.links || [];
  const work = h("section", { class: "pl-section", "aria-labelledby": "pd-work" },
    h("h3", { id: "pd-work" }, "Work"),
    links.length
      ? h("ul", { class: "pl-links" }, ...links.map((l) => h("li", { class: `pl-link t-${l.tone}` },
        h("span", { class: "pl-link-kind", title: l.kind === "goal" ? "Goal" : "Task" }, ico(l.kind === "goal" ? "target" : "checks")),
        h("span", { class: "pl-link-main" },
          !pl.planner && l.id && pl.hooks.openWork
            ? h("button", { type: "button", class: "link pl-link-title", title: `Open the ${l.kind}`, onclick: () => pl.hooks.openWork(l) }, l.title)
            : h("span", { class: "pl-link-title" }, l.title),
          h("span", { class: "pl-link-sub" }, l.project ? `${l.kind === "goal" ? "Goal" : "Task"} in ${l.project}` : l.kind === "goal" ? "Goal" : "Task")),
        h("span", { class: `pl-state t-${l.tone}` }, l.state),
        !pl.planner && l.id ? ib({ type: "button", class: "icon-btn small", "aria-label": `Unlink ${l.title}`, title: "Unlink",
          onclick: attempt((e) => busy(e.currentTarget, () => unlink(c, l))) }, "x") : null)))
      : h("p", { class: "hint" }, "Nothing started yet. Start makes a task or a goal for this card in a project."));

  const comments = c.discussion || [];
  const me = pl.hooks.me?.();
  const box = h("textarea", { rows: 3, placeholder: "Add a comment", "aria-label": "Comment", maxlength: 10000 });
  const discussion = h("section", { class: "pl-section", "aria-labelledby": "pd-comments" },
    h("h3", { id: "pd-comments" }, "Comments"),
    comments.length ? h("ol", { class: "pl-comments-list" }, ...comments.map((m) => h("li", { class: "pl-comment" },
      h("div", { class: "pl-comment-head" },
        h("strong", {}, m.author_name || "Someone"),
        h("time", { datetime: m.created_at, title: dateTimeText(m.created_at) }, dateTimeText(m.created_at)),
        me && (m.author === me.id || me.admin) ? ib({ type: "button", class: "icon-btn small", "aria-label": "Delete comment", title: "Delete",
          onclick: attempt(async () => { if (await ask({ title: "Delete this comment?", ok: "Delete", danger: true })) await call("DELETE", cardAPI(c.id, `/comments/${m.id}`)); await loadCard(c.id); }) }, "trash") : null),
      h("p", { class: "pl-comment-text" }, m.text)))) : null,
    h("form", { class: "pl-comment-form", onsubmit: attempt(async (e) => {
      e.preventDefault();
      const text = box.value.trim();
      if (!text) return;
      await busy(e.submitter || $("button", e.currentTarget), () => call("POST", cardAPI(c.id, "/comments"), { text }));
      box.value = "";
      await loadCard(c.id);
    }) }, box, ib({ type: "submit", class: "btn small" }, "comment", "Comment")));

  const danger = h("div", { class: "pl-danger" },
    ib({ type: "button", class: "btn small danger", onclick: attempt(async () => {
      if (!await ask({ title: "Delete this card?", body: "Its comments and links go too. The tasks and goals it links to stay.", ok: "Delete", danger: true })) return;
      await call("DELETE", cardAPI(c.id));
      pl.cards = pl.cards.filter((x) => x.id !== c.id);
      closeCard();
      renderBoard();
      toast("Card deleted");
    }) }, "trash", "Delete card"));

  const keep = body.scrollTop;
  fill(body, suggest, facts, h("section", { class: "pl-section" }, h("h3", {}, "Description"), desc), work, discussion, danger);
  body.scrollTop = keep;
}

async function unlink(c, l) {
  const v = await call("DELETE", cardAPI(c.id, `/links/${l.kind}/${encodeURIComponent(l.id)}`));
  pl.detail = { ...pl.detail, ...v };
  renderCard();
}

// ---- sheets

// sheet shows a small dialog built from kids, with OK labelled ok; it
// resolves to the form's data when OK is chosen (null when cancelled).
function sheet(title, kids, ok, icon = "check") {
  const form = h("form", { method: "dialog" },
    h("header", { class: "sheet-head" }, h("h2", {}, title),
      ib({ class: "icon-btn", value: "cancel", formnovalidate: true, "aria-label": "Close" }, "x")),
    ...kids,
    h("footer", { class: "sheet-actions" },
      ib({ class: "btn", value: "cancel", formnovalidate: true }, "x", "Cancel"),
      ok ? ib({ class: "btn primary", value: "ok" }, icon, ok) : null));
  const d = h("dialog", { class: "sheet small pl-sheet" }, form);
  document.body.append(d);
  d.showModal();
  return new Promise((resolve) => {
    d.addEventListener("close", () => {
      const data = d.returnValue === "ok" ? new FormData(form) : null;
      d.remove();
      resolve(data);
    }, { once: true });
  });
}

// startSheet asks for the project and whether to make a task or a goal,
// then starts it: the card's title and description are what to do.
async function startSheet(c) {
  if (!pl.projects.length) await loadPlanning();
  if (!pl.projects.length) throw new Error("There are no projects to start work in yet. Ask a developer to add one.");
  const data = await sheet("Start this card", [
    h("p", { class: "hint" }, "Dash makes the work from the card's title and description, links it to the card and moves the card to In progress."),
    h("label", { class: "field" }, h("span", {}, "Project"),
      h("select", { name: "project", required: true },
        ...pl.projects.map((p) => h("option", { value: p.id, selected: p.id === (c.project_id || pl.filter) }, p.name)))),
    h("fieldset", { class: "field choice" },
      h("legend", {}, "Make"),
      h("label", { class: "radio" }, h("input", { type: "radio", name: "kind", value: "task", checked: true }),
        h("span", {}, "A task ", h("em", {}, "one piece of work, queued for Claude now"))),
      h("label", { class: "radio" }, h("input", { type: "radio", name: "kind", value: "goal" }),
        h("span", {}, "A goal ", h("em", {}, "something bigger: a coordinator plans it into tasks")))),
  ], "Start", "play");
  if (!data) return;
  const v = await call("POST", cardAPI(c.id, "/start"), { project: data.get("project"), kind: data.get("kind") });
  toast(data.get("kind") === "goal" ? "Goal started and linked" : "Task queued and linked", { tone: "ok" });
  if (pl.open === c.id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  await loadPlanning();
}

// linkExistingSheet lists the org's tasks and goals (of the card's project
// first) to link one to the card (members).
async function linkExistingSheet(c) {
  const q = h("input", { type: "search", placeholder: "Filter by title", "aria-label": "Filter", autocomplete: "off" });
  const list = h("ul", { class: "pl-pick" }, h("li", { class: "hint" }, "Loading…"));
  let items = [];
  let picked = null;
  const linked = new Set((c.links || []).map((l) => `${l.kind}:${l.id}`));
  const render = () => {
    const f = q.value.trim().toLowerCase();
    const shown = items.filter((x) => !linked.has(`${x.kind}:${x.id}`) && (!f || x.title.toLowerCase().includes(f))).slice(0, 100);
    fill(list, ...(shown.length ? shown.map((x) => h("li", {},
      h("button", { type: "button", class: "pl-pick-item", onclick: (e) => { picked = x; e.currentTarget.closest("dialog").close("ok"); } },
        ico(x.kind === "goal" ? "target" : "checks"),
        h("span", { class: "pl-link-main" }, h("span", { class: "pl-link-title" }, x.title || "(untitled)"),
          h("span", { class: "pl-link-sub" }, `${x.kind === "goal" ? "Goal" : "Task"} in ${x.project} · ${x.state}`)))))
      : [h("li", { class: "hint" }, items.length ? "Nothing matches." : "No tasks or goals yet.")]));
  };
  q.addEventListener("input", render);
  const done = sheet("Link existing work", [q, list], null);
  call("GET", `/api/planning/work${c.project_id ? `?project=${encodeURIComponent(c.project_id)}` : ""}`).then((xs) => { items = xs; render(); }).catch(fail);
  q.focus();
  await done;
  if (!picked) return;
  const v = await call("POST", cardAPI(c.id, "/links"), { kind: picked.kind, id: picked.id });
  if (pl.open === c.id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  await loadPlanning();
}

// linkToCard links a task or goal to a card, from the task's or goal's
// detail: it lists the org's cards, those linked already marked.
export async function linkToCard(kind, id) {
  const [cards, linked] = await Promise.all([call("GET", cardAPI()), call("GET", `/api/planning/linked/${kind}/${encodeURIComponent(id)}`)]);
  const on = new Set(linked.map((c) => c.id));
  const q = h("input", { type: "search", placeholder: "Filter cards", "aria-label": "Filter", autocomplete: "off" });
  const list = h("ul", { class: "pl-pick" });
  let picked = null;
  const render = () => {
    const f = q.value.trim().toLowerCase();
    const shown = cards.filter((c) => !f || c.title.toLowerCase().includes(f)).slice(0, 100);
    fill(list, ...(shown.length ? shown.map((c) => h("li", {},
      h("button", { type: "button", class: "pl-pick-item", disabled: on.has(c.id), onclick: (e) => { picked = c; e.currentTarget.closest("dialog").close("ok"); } },
        ico(on.has(c.id) ? "check" : "plan"),
        h("span", { class: "pl-link-main" }, h("span", { class: "pl-link-title" }, c.title),
          h("span", { class: "pl-link-sub" }, [COLUMN_TITLE[c.column], c.project_name, on.has(c.id) ? "linked" : ""].filter(Boolean).join(" · "))))))
      : [h("li", { class: "hint" }, cards.length ? "No card matches." : "There are no cards yet: add them on the Planning page.")]));
  };
  q.addEventListener("input", render);
  render();
  const done = sheet(`Link this ${kind} to a card`, [h("p", { class: "hint" }, "The card on the Planning page then shows this work's status."), q, list], null);
  q.focus();
  await done;
  if (!picked) return;
  await call("POST", cardAPI(picked.id, "/links"), { kind, id });
  toast(`Linked to “${picked.title}”`, { tone: "ok" });
}

