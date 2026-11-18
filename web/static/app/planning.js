import { $, $$, avatar, dateTimeText, fill, h, ib, ico } from "./dom.js";
import { api } from "./api.js";
import { EASE, ask, attempt, busy, copy, fail, leave, openSheet, reduceMotion, toast } from "./dialogs.js";
import { changed, linkTo } from "./routes.js";
import * as store from "./store.js";
import { desktop } from "./viewport.js";
import { dropBefore, edgeScroller, glide } from "./glide.js";
import { ACCEPT, clip, postFile } from "./attachments.js";
import { extOf, isImage, sizeText } from "./lightbox.js";

// ------------------------------------------------------------------ Planning
// The org's plan, for a product owner (docs/planning.md): cards in four
// fixed columns, each linked to the tasks and goals that carry it out, with
// a status worked out on the server from those, in owner-facing words.
// A card opens in a pane beside the board (full screen with Back on a
// phone; Esc closes), as History's commit pane does.
//
// One module for both places Planning shows: the app's Planning view
// (members, admins, owners; index.html) and planning.html, the page a
// planner lands on. It uses only dom, api, dialogs, store, viewport, glide,
// attachments and lightbox,
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
// fileURL is a card's file, through the card (a planner can't read uploads).
const fileURL = (c, f) => cardAPI(c.id, `/files/${encodeURIComponent(f.id)}/${encodeURIComponent(f.name)}`);

const pl = {
  root: null,
  planner: false,
  hooks: {},
  cards: [],
  projects: [],
  people: [], // the org's members a card may be assigned to
  filter: store.get("dash.planning.project", ""),
  tag: store.get("dash.planning.tag", ""), // only the cards with this tag ("" all)
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
  composer: null, // the new card being typed on the board: { col, li, ta, busy }
  uploading: {}, // card id → how many files are on their way to it
};

const byId = (id) => pl.cards.find((c) => c.id === id);
const sameTag = (a, b) => a.toLowerCase() === b.toLowerCase();
const tagged = (c, tag) => (c.tags || []).some((t) => sameTag(t, tag));
// shown are the cards the board shows: with the tag picked, if one is.
const shown = () => pl.tag ? pl.cards.filter((c) => tagged(c, pl.tag)) : pl.cards;
// allTags are the tags on the cards loaded, each once, A to Z.
function allTags() {
  const seen = new Map();
  for (const c of pl.cards) for (const t of c.tags || []) if (!seen.has(t.toLowerCase())) seen.set(t.toLowerCase(), t);
  return [...seen.values()].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }));
}
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
  fill(root,
    h("div", { class: "page planning" },
      h("header", { class: "cl-head pl-head" },
        h("h2", { class: "cl-title page-title" }, "Planning"),
        filterPicker(),
        h("span", { class: "pl-tag-filter", id: "pl-tag-filter", hidden: true }),
        h("span", { class: "cl-state", id: "pl-state", "aria-live": "polite" }),
        ib({ type: "button", class: "btn small primary pl-new", title: "Add an idea to the plan", onclick: () => openComposer("ideas") }, "plus", "New card")),
      h("p", { class: "hint pl-intro", id: "pl-intro", hidden: true },
        "The plan: cards move from Ideas to Done. Start a card to make a task or a goal for a developer's project; its status follows the work."),
      // The task board's layout (style.css: .lanes, .board, .col): on a
      // phone a strip of lanes and one column at a time, on a desktop the
      // columns side by side. A column is a list of bordered cards (no rail
      // line or nodes; style.css: .pl-cards) and ends in Add card, which
      // opens a new card to type on at the end of the column (openComposer).
      h("div", { class: "lanes pl-lanes", role: "tablist", "aria-label": "Columns" },
        ...COLUMNS.map((col) => h("button", { type: "button", role: "tab", class: `lane-tab s-${col.s}`, "data-col": col.key,
          onclick: () => { pl.lane = col.key; renderBoard(); } }, col.title, h("span", { class: "n", id: `pl-tab-n-${col.key}` })))),
      h("div", { class: "board pl-board", id: "pl-board" },
        ...COLUMNS.map((col) => h("section", { class: `col pl-col s-${col.s}`, "data-col": col.key, "aria-labelledby": `pl-col-${col.key}` },
          h("header", { class: "col-head" },
            h("span", { class: "col-label", id: `pl-col-${col.key}` }, col.title), h("span", { class: "n", id: `pl-count-${col.key}` })),
          h("div", { class: "col-body" },
            h("ol", { class: "pl-cards", "data-col": col.key, "aria-labelledby": `pl-col-${col.key}` }),
            h("p", { class: "col-empty", id: `pl-empty-${col.key}` }, col.empty),
            ib({ type: "button", class: "pl-add", "data-col": col.key, title: `Add a card to ${col.title}`, onclick: () => openComposer(col.key) }, "plus", "Add card")))))));
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
      h("div", { class: "detail-body", id: "pd-body" })),
    h("datalist", { id: "pl-tag-options" }));
  wireDrag(root);
  wireSwipe($("#pl-board", root));
  wireFileDrop(paneEl(), () => pl.open);
  wireFileDrop(root, (e) => e.target.closest?.(".pl-card:not(.pl-composer)")?.dataset.id);
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

// followProject points the project filter at the project picked in the
// app (projects.js: selectProject), so Planning shows that project's cards,
// not the ones of the project shown before. A card of another project that
// is open closes. The board is read again now if it shows, or when it
// is next shown.
export function followProject(id) {
  if (!id || id === pl.filter) return;
  pl.filter = id;
  store.set("dash.planning.project", id);
  changed("replace");
  if (!pl.root) return; // not mounted yet: mountPlanning reads pl.filter
  if (pl.open && pl.detail?.project_id !== id) closeCard();
  pl.loaded = false;
  renderFilter();
  if (!pl.hooks.visible || pl.hooks.visible()) loadPlanning().catch(fail);
}

// ---- the project filter
// A button that opens a list of the projects, each with its icon (a select
// can't show pictures). Arrows move in the list, Enter picks, Esc closes.

// planningIcon is a project's icon, or its initial on its color, as the
// sidebar shows it (style.css: .side-project-icon).
function planningIcon(p) {
  const el = h("span", { class: "side-project-icon", "aria-hidden": "true" });
  el.style.setProperty("--project", p.color || "");
  el.append(p.icon ? h("img", { src: p.icon, alt: "" }) : [...p.name][0]?.toUpperCase() || "?");
  return el;
}

function filterPicker() {
  const list = h("ul", { class: "pl-filter-list", id: "pl-filter-list", role: "listbox", "aria-label": "Project", hidden: true, onkeydown: filterListKey,
    onclick: (e) => { const o = e.target.closest("[role=option]"); if (o) pickFilter(o.dataset.value); } });
  const btn = h("button", { type: "button", class: "btn small pl-filter-btn", id: "pl-filter", "aria-haspopup": "listbox", "aria-controls": "pl-filter-list", "aria-expanded": "false",
    onclick: () => toggleFilter(list.hidden),
    onkeydown: (e) => { if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); toggleFilter(true); } } });
  return h("div", { class: "pl-filter", id: "pl-filter-wrap",
    onfocusout: (e) => { if (!e.currentTarget.contains(e.relatedTarget)) toggleFilter(false, false); } }, btn, list);
}

function toggleFilter(open, focus = true) {
  const btn = $("#pl-filter"), list = $("#pl-filter-list");
  if (!btn || list.hidden === !open) return;
  list.hidden = !open;
  btn.setAttribute("aria-expanded", String(open));
  if (!focus) return;
  if (open) ($("[aria-selected=true]", list) || $("[role=option]", list))?.focus();
  else btn.focus();
}

function pickFilter(id) {
  toggleFilter(false);
  if (id === pl.filter) return;
  pl.filter = id;
  store.set("dash.planning.project", pl.filter);
  renderFilter();
  changed("replace");
  loadPlanning().catch(fail);
}

function filterListKey(e) {
  const opts = $$("[role=option]", e.currentTarget);
  const i = opts.indexOf(document.activeElement);
  const to = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: opts.length - 1 }[e.key];
  if (to !== undefined) opts[Math.max(0, Math.min(opts.length - 1, to))]?.focus();
  else if (e.key === "Enter" || e.key === " ") pickFilter(document.activeElement?.dataset.value ?? pl.filter);
  else if (e.key === "Escape") toggleFilter(false);
  else if (e.key === "Tab") { toggleFilter(false, false); return; }
  else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
    // Typing a letter goes to the next project starting with it.
    const k = e.key.toLowerCase();
    const next = [...opts.slice(i + 1), ...opts.slice(0, i + 1)].find((o) => o.textContent.trim().toLowerCase().startsWith(k));
    next?.focus();
  } else return;
  e.preventDefault();
  e.stopPropagation();
}

function renderFilter() {
  const btn = $("#pl-filter"), list = $("#pl-filter-list");
  if (!btn) return;
  const chosen = pl.projects.find((p) => p.id === pl.filter);
  const label = chosen ? chosen.name : "All projects";
  fill(btn, chosen ? planningIcon(chosen) : ico("repo"), h("span", { class: "pl-filter-name" }, label), ico("down"));
  btn.setAttribute("aria-label", `Project: ${label}`);
  btn.title = "Show the cards of one project";
  const option = (id, icon, name) => h("li", { role: "option", tabindex: "-1", "data-value": id, "aria-selected": String(id === pl.filter) },
    icon, h("span", { class: "pl-filter-name" }, name), id === pl.filter ? ico("check") : null);
  fill(list, option("", ico("repo"), "All projects"), ...pl.projects.map((p) => option(p.id, planningIcon(p), p.name)));
  $("#pl-filter-wrap").hidden = pl.projects.length < 2 && !pl.filter;
}

// ---- the board

// renderBoard draws the columns; cards glide from where they were to where
// they are now (as the task board's do), new ones fade in. Not under a
// drag: the board is drawn again once the card is dropped.
function renderBoard() {
  if (pl.drag) return;
  const was = new Map($$(".pl-card[data-id]", pl.root).map((li) => [li.dataset.id, li.getBoundingClientRect()]));
  renderColumns();
  if (reduceMotion.matches || !was.size) return;
  for (const li of $$(".pl-card[data-id]", pl.root)) {
    const now = li.getBoundingClientRect();
    if (!now.height) continue; // a column a phone doesn't show
    const r = was.get(li.dataset.id);
    if (!r?.height) { li.animate([{ opacity: 0, transform: "scale(.97)" }, { opacity: 1, transform: "none" }], { duration: 240, easing: EASE }); continue; }
    const dx = r.left - now.left, dy = r.top - now.top;
    if (Math.abs(dx) + Math.abs(dy) >= 1) li.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }], { duration: 320, easing: EASE });
  }
}

// pickTag shows only the cards with tag on the board ("" all of them).
export function pickTag(tag) {
  pl.tag = tag;
  store.set("dash.planning.tag", tag);
  changed("replace");
  renderBoard();
  if (pl.open) renderCard();
}

function renderTagFilter() {
  const el = $("#pl-tag-filter");
  el.hidden = !pl.tag;
  fill(el, pl.tag ? h("span", { class: "pl-chip pl-tag pl-tag-on" }, ico("tag"), h("span", { class: "pl-tag-name" }, pl.tag),
    ib({ type: "button", class: "icon-btn small", "aria-label": `Show all cards, not only those tagged ${pl.tag}`, title: "Show all cards", onclick: () => pickTag("") }, "x")) : null);
  fill($("#pl-tag-options"), ...allTags().map((t) => h("option", { value: t })));
}

function renderColumns() {
  renderTagFilter();
  const counts = Object.fromEntries(COLUMNS.map((col) => [col.key, shown().filter((c) => c.column === col.key).length]));
  // A phone opens on the first column with cards in it.
  if (!pl.lane) pl.lane = (pl.cards.length && COLUMNS.find((col) => counts[col.key])?.key) || "ideas";
  for (const col of COLUMNS) {
    const ol = $(`.pl-cards[data-col="${col.key}"]`, pl.root);
    const cards = shown().filter((c) => c.column === col.key).sort((a, b) => a.position - b.position);
    const focused = document.activeElement?.closest?.(".pl-card")?.dataset.id;
    // A card being typed stays where it is, last, with its text and focus.
    const comp = pl.composer?.col === col.key ? pl.composer.li : null;
    if (comp) {
      if (comp.parentNode !== ol) ol.append(comp);
      for (const n of [...ol.children]) if (n !== comp) n.remove();
      comp.before(...cards.map(cardEl));
    } else fill(ol, ...cards.map(cardEl));
    ol.hidden = !cards.length && !comp;
    $(`#pl-empty-${col.key}`).hidden = cards.length > 0 || !!comp;
    $(`#pl-empty-${col.key}`).textContent = pl.tag ? `Nothing tagged ${pl.tag}.` : col.empty;
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

// MEMBER_FACES is how many members' faces a card shows; more are a count.
const MEMBER_FACES = 3;

// membersEl is the card's members' faces, overlapping, with their names for
// a tooltip and a screen reader.
function membersEl(c) {
  const ps = c.people || [];
  if (!ps.length) return null;
  const names = ps.map((p) => p.username).join(", ");
  const more = ps.length - MEMBER_FACES;
  return h("span", { class: "pl-members", title: `Members: ${names}` },
    ...ps.slice(0, more > 0 ? MEMBER_FACES - 1 : MEMBER_FACES).map((p) => avatar(p)),
    more > 0 ? h("span", { class: "avatar pl-more", "aria-hidden": "true" }, `+${more + 1}`) : null,
    h("span", { class: "sr-only" }, `Members: ${names}`));
}

// waitsEl marks a card that waits for cards it depends on, not done yet.
function waitsEl(c) {
  if (!c.waiting) return null;
  const titles = (c.deps || []).filter((d) => !d.done).map((d) => d.title).join(", ");
  return h("span", { class: "pl-chip pl-waits", title: `Waits for ${titles}` }, ico("lock"), `Waits for ${c.waiting}`,
    h("span", { class: "sr-only" }, `: ${titles}`));
}

function cardEl(c) {
  const meta = [
    // Each chip names what it is (data-kind: Priority, Target, Tag) on a desktop, not only by its colour.
    c.priority ? h("span", { class: `pl-chip p-${c.priority}`, title: "Priority", "data-kind": "Priority" }, PRIORITY[c.priority]) : null,
    waitsEl(c),
    c.target ? h("span", { class: "pl-chip pl-target", title: "Target", "data-kind": "Target" }, ico("flag"), c.target) : null,
    ...(c.tags || []).map((t) => h("span", { class: "pl-chip pl-tag", title: "Tag", "data-kind": "Tag" }, t)),
    !pl.filter && c.project_name ? h("span", { class: "pl-project" }, c.project_name) : null,
    c.comments ? h("span", { class: "pl-comments", title: `${c.comments} comment${c.comments === 1 ? "" : "s"}` }, ico("comment"), String(c.comments)) : null,
    c.files?.length ? h("span", { class: "pl-comments pl-file-count", title: `${c.files.length} file${c.files.length === 1 ? "" : "s"}` }, clip(), String(c.files.length)) : null,
    membersEl(c),
  ].filter(Boolean);
  if (c.status?.suggest_done) meta.push(h("span", { class: "pl-suggest" }, "Ready for Done"));
  // Under the title: the status of the linked work, or else the start of the description.
  const about = (c.description || "").replace(/[#*_`>]+/g, "").replace(/\s+/g, " ").trim();
  const sub = c.status?.total ? h("span", { class: "todo-sub" }, statusEl(c.status))
    : about ? h("span", { class: "todo-sub" }, about) : null;
  // A bordered card (style.css: .pl-card), built from the task board's
  // card parts (app/branches.js: todoRow) but without its rail node: the
  // status line's dot shows the status's colour, Done cards lie flat. The
  // title is the button that opens the card; a press anywhere on it does too.
  // The card's first picture, across its top.
  const cover = (c.files || []).find(isImage);
  return h("li", { class: "pl-card", "data-id": c.id },
    h("div", { class: `todo t-${c.status?.tone || "none"}${c.column === "done" ? " pushed" : ""}${c.id === pl.open ? " selected" : ""}`,
      onclick: () => openCard(c.id).catch(fail) },
      cover ? h("img", { class: "pl-cover", src: fileURL(c, cover), alt: "", loading: "lazy", draggable: false }) : null,
      h("span", { class: "todo-main" },
        h("button", { type: "button", class: "todo-open pl-open", "aria-controls": "card-detail", "aria-current": c.id === pl.open ? "true" : null,
          title: "Open (Alt+arrows move it; Space over it adds or takes you off it)", onkeydown: (e) => cardKeys(e, c) },
          h("span", { class: "todo-title" }, c.title)),
        sub),
      meta.length ? h("span", { class: "todo-side" }, ...meta) : null));
}

// ---- dragging, as the task board drags (dnd.js): a mouse drags after a
// few pixels, a finger after a long press (a quick swipe still scrolls).
// A ghost of the card follows the pointer. The card leaves its place for a
// gap (the slot) where it would land, and the cards around it glide aside.

// cardsOf are ol's cards, but the one dragged and one being typed.
const cardsOf = (ol) => $$(".pl-card:not(.pl-composer):not(.pl-slot)", ol).filter((li) => li.dataset.id !== pl.drag?.id);

// placeSlot moves the gap to before ref in ol (nowhere, without ol); the
// cards where it was and where it goes glide.
// The gap and the dragged card (hidden) take no place of their own, so a ref
// that is either of them means the node after it, and a gap already there
// stays put: moving it would restart its fade-in and it would never show.
function placeSlot(ol, ref = null) {
  const s = pl.drag.slot;
  const skip = (n) => { while (n && (n === s || n === pl.drag.li)) n = n.nextSibling; return n; };
  ref = skip(ref);
  if (ol ? s.parentNode === ol && skip(s.nextSibling) === ref : !s.parentNode) return;
  const around = [s.parentNode, ol].filter(Boolean).flatMap((x) => [...x.children]).filter((el) => el !== s && el !== pl.drag.li);
  glide(around, () => ol ? ol.insertBefore(s, ref) : s.remove());
}

function clearDropMarks() {
  $$(".drop-over", pl.root).forEach((x) => x.classList.remove("drop-over"));
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
  // The gap starts where the card was, as tall.
  const slot = h("li", { class: "pl-slot", "aria-hidden": "true" });
  slot.style.height = `${r.height}px`;
  // It fades in once; moved later, it shows at once.
  slot.addEventListener("animationend", () => slot.classList.add("placed"), { once: true });
  p.li.before(slot);
  p.li.classList.add("dragging");
  pl.drag = { id: p.id, li: p.li, ghost, slot, dx: p.x - r.left, dy: p.y - r.top };
  moveDrag(x, y);
}

// Held near the top or bottom, the column (or the page, on a phone) scrolls.
const edge = edgeScroller((x, y) => pl.drag && moveDrag(x, y));

function moveDrag(x, y) {
  const d = pl.drag;
  edge.move(x, y);
  d.ghost.style.transform = `translate(${x - d.dx}px, ${y - d.dy}px)`;
  clearDropMarks();
  const ol = dropTarget(x, y);
  if (!ol) { placeSlot(null); return; }
  const tab = document.elementFromPoint(x, y)?.closest?.(".pl-lanes .lane-tab");
  (tab || ol.closest(".pl-col")).classList.add("drop-over");
  if (tab) { placeSlot(null); return; } // to the end of that column
  // Measured where the cards rest, not mid-glide, so the gap doesn't flicker.
  const cards = cardsOf(ol);
  const before = dropBefore(cards, y);
  if (ol.hidden) ol.hidden = false; // an empty column's list, to hold the gap
  placeSlot(ol, before || cards.at(-1)?.nextSibling || (cards.length ? null : ol.firstChild));
}

// endDrag drops the card at the point (drop), or puts it back.
function endDrag(x, y, drop) {
  const d = pl.drag;
  if (!d) return;
  edge.stop();
  d.ghost.remove();
  clearDropMarks();
  const ol = drop ? dropTarget(x, y) : null;
  const onTab = ol && document.elementFromPoint(x, y)?.closest?.(".pl-lanes .lane-tab");
  // Where the gap is: the cards before it in its column.
  const index = !ol ? 0 : onTab || d.slot.parentNode !== ol ? 1 << 20 : cardsOf(ol).filter((li) => li.compareDocumentPosition(d.slot) & Node.DOCUMENT_POSITION_FOLLOWING).length;
  // The card takes the gap's place, so the board drawn after the drop glides
  // it and its neighbours on from where they are.
  if (ol && d.slot.parentNode === ol) d.slot.replaceWith(d.li); else d.slot.remove();
  d.li.classList.remove("dragging");
  pl.drag = null;
  // The click that ends a mouse drag isn't a click on the card.
  pl.suppressClick = true;
  setTimeout(() => { pl.suppressClick = false; }, 0);
  if (ol) moveCard(d.id, ol.dataset.col, index).catch(fail);
  else renderBoard(); // back to its place, with what changed meanwhile
}

function cancelPending() {
  if (pl.pending?.timer) clearTimeout(pl.pending.timer);
  pl.pending = null;
}

function wireDrag(root) {
  root.addEventListener("pointerdown", (e) => {
    const li = e.target.closest?.(".pl-card");
    if (!li || li.matches(".pl-composer") || e.button > 0 || !root.contains(li)) return;
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

// ---- swiping between columns (phones), as the task board does
// (branches.js): a clear sideways swipe on the board moves to the next
// column (left) or the previous one (right), as the tabs do. Passive, so
// vertical scrolling is never held up. Not from the screen's edges (iOS's
// back gesture), nor in a field, nor once a long press drags a card.
const SWIPE = { min: 50, edge: 20 };

function wireSwipe(board) {
  let s = null;
  board.addEventListener("touchstart", (e) => {
    s = null;
    if (desktop.matches || e.touches.length !== 1 || pl.drag) return;
    const t = e.touches[0];
    if (t.clientX < SWIPE.edge || t.clientX > innerWidth - SWIPE.edge) return;
    if (e.target.closest?.("input, textarea, select, [contenteditable]:not([contenteditable=false])")) return;
    s = { x: t.clientX, y: t.clientY };
  }, { passive: true });
  board.addEventListener("touchcancel", () => { s = null; }, { passive: true });
  board.addEventListener("touchend", (e) => {
    const start = s;
    s = null;
    if (!start || pl.drag || desktop.matches) return;
    const t = e.changedTouches[0];
    if (!t) return;
    const dx = t.clientX - start.x, dy = t.clientY - start.y;
    // Clearly sideways: well past the threshold and mostly horizontal.
    if (Math.abs(dx) <= SWIPE.min || Math.abs(dx) < 2 * Math.abs(dy)) return;
    const d = dx < 0 ? 1 : -1;
    const next = COLUMNS[COLUMNS.findIndex((col) => col.key === pl.lane) + d];
    if (!next) return;
    pl.lane = next.key;
    renderBoard();
    $(".pl-lanes .lane-tab[aria-selected=true]", pl.root)?.scrollIntoView({ block: "nearest", inline: "nearest" });
    if (reduceMotion.matches) return;
    $(".pl-col.active", pl.root)?.animate(
      [{ transform: `translateX(${d * 48}px)`, opacity: 0.4 }, { transform: "none", opacity: 1 }],
      { duration: 220, easing: EASE });
  }, { passive: true });
}

// cardKeys: Alt+Left/Right moves a card to the column beside, Alt+Up/Down
// up or down its column. (On a phone, the card's pane has the column.)
function cardKeys(e, c) {
  if (!e.altKey || e.ctrlKey || e.metaKey) return;
  const cols = COLUMNS.map((x) => x.key);
  const same = shown().filter((x) => x.column === c.column).sort((a, b) => a.position - b.position);
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
  const members = c.members || [];
  const mine = members.includes(me.id);
  const v = await call("PATCH", cardAPI(c.id), { members: mine ? members.filter((id) => id !== me.id) : [...members, me.id] });
  const i = pl.cards.findIndex((x) => x.id === c.id);
  if (i >= 0) { pl.cards[i] = { ...pl.cards[i], ...v }; renderBoard(); }
  if (pl.open === c.id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  toast(mine ? "You're off the card" : "You're on the card");
}

// moveCard puts a card in column col at index (among the column's other
// cards the board shows: with a tag picked, it goes just before the one
// shown there, or after the last shown), on the board at once and then on
// the server. The board is drawn
// even when that is where it is: a drop leaves the card where it fell, and
// what changed during the drag waits for it.
async function moveCard(id, col, index) {
  const c = byId(id);
  if (!c) { renderBoard(); return; }
  const others = pl.cards.filter((x) => x.column === col && x.id !== id).sort((a, b) => a.position - b.position);
  const seen = others.filter((x) => shown().includes(x));
  const pos = index < seen.length ? others.indexOf(seen[index])
    : seen.length ? others.indexOf(seen.at(-1)) + 1 : Math.max(0, Math.min(index, others.length));
  if (c.column === col) {
    const now = pl.cards.filter((x) => x.column === col).sort((a, b) => a.position - b.position).findIndex((x) => x.id === id);
    if (now === pos) { renderBoard(); return; }
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

// ---- adding cards: typed on the board, as a new card at the end of a
// column. The first line is the title, the lines after it the description.
// Enter adds it and leaves a new empty one under it; Shift+Enter is a new
// line; Esc closes it; leaving it adds it if there's text. One at a time.

const addButton = (col) => $(`.pl-add[data-col="${col}"]`, pl.root);

function grow(ta) {
  ta.style.height = "auto";
  ta.style.height = `${ta.scrollHeight}px`;
}

// openComposer opens the new card in col (closing, and adding, the one open
// in another column) and focuses it. On a phone it shows col's lane first.
async function openComposer(col) {
  if (pl.composer?.busy) await pl.composer.busy;
  const cur = pl.composer;
  if (cur && cur.col !== col) {
    if (cur.ta.value.trim()) {
      await saveComposer(false);
      if (pl.composer) return; // it failed: keep its text
    } else closeComposer(false);
  }
  if (!pl.composer) {
    const ta = h("textarea", { class: "pl-compose-text", rows: 1, placeholder: "What should be built?",
      "aria-label": `New card in ${COLUMN_TITLE[col]}`, "aria-describedby": "pl-compose-help" });
    const li = h("li", { class: "pl-card pl-composer" },
      h("div", { class: "todo t-none" },
        h("span", { class: "todo-main" }, ta,
          h("span", { class: "sr-only", id: "pl-compose-help" }, "Enter adds the card, Shift+Enter starts a new line, Escape cancels."))));
    const c = { col, li, ta, busy: null };
    ta.addEventListener("input", () => grow(ta));
    ta.addEventListener("keydown", (e) => {
      if (e.isComposing) return;
      if (e.key === "Enter" && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
        e.preventDefault();
        saveComposer(true);
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation(); // not the card's pane, nor the keyboard's modes
        if (!c.busy) closeComposer(true);
      }
    });
    // Leaving it (a click or Tab elsewhere, not another window) adds it.
    li.addEventListener("focusout", (e) => {
      if (pl.composer !== c || li.contains(e.relatedTarget) || !document.hasFocus()) return;
      if (c.ta.value.trim()) saveComposer(false);
      else closeComposer(false);
    });
    pl.composer = c;
  }
  pl.lane = col;
  renderBoard();
  pl.composer.ta.focus({ preventScroll: true });
  pl.composer.li.scrollIntoView({ block: "nearest" });
  grow(pl.composer.ta);
}

// closeComposer drops the new card, unsaved; focusAdd puts the focus back
// on its column's Add card.
function closeComposer(focusAdd) {
  const c = pl.composer;
  if (!c) return;
  pl.composer = null;
  c.li.remove();
  renderBoard();
  if (focusAdd) addButton(c.col)?.focus();
}

// saveComposer adds the typed card. next leaves an empty one under it to
// type the next card in; otherwise it closes. Empty text adds nothing (and
// closes, unless next). A failed request keeps the text, and it open.
function saveComposer(next) {
  const c = pl.composer;
  if (!c) return Promise.resolve();
  if (c.busy) return c.busy;
  const [first, ...rest] = c.ta.value.trim().split("\n");
  const title = first.trim();
  if (!title) {
    if (!next) closeComposer(false);
    return Promise.resolve();
  }
  const description = rest.join("\n").trim();
  c.ta.readOnly = true;
  c.li.setAttribute("aria-busy", "true");
  c.busy = (async () => {
    try {
      const card = await call("POST", cardAPI(), { title, description, column: c.col, project: pl.filter || undefined, tags: pl.tag ? [pl.tag] : undefined });
      if (!byId(card.id)) pl.cards.push(card);
      if (next && pl.composer === c) {
        c.ta.value = "";
        grow(c.ta);
        renderBoard();
        c.li.scrollIntoView({ block: "nearest" });
      } else if (pl.composer === c) closeComposer(false);
      else renderBoard();
    } catch (err) {
      fail(err);
    } finally {
      c.busy = null;
      c.ta.readOnly = false;
      c.li.removeAttribute("aria-busy");
    }
  })();
  return c.busy;
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
  changed("push");
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
  changed("close");
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
// The open card's id (null: none), and why it couldn't be read ("" if it was).
export const cardId = () => pl.open;
export const cardError = () => pl.error;
// planningFilters are the project and tag Planning shows the cards of ("": all).
export const planningFilters = () => ({ project: pl.filter, tag: pl.tag });
// setPlanningFilters shows the cards of project and tag (a link's: router.js).
export function setPlanningFilters(project, tag) {
  if (project === pl.filter && tag === pl.tag) return;
  const reload = project !== pl.filter;
  pl.filter = project;
  pl.tag = tag;
  store.set("dash.planning.project", project);
  store.set("dash.planning.tag", tag);
  if (!pl.root) return;
  renderFilter();
  if (reload) { pl.loaded = false; if (!pl.hooks.visible || pl.hooks.visible()) loadPlanning().catch(fail); }
  else renderBoard();
}

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

// applyCard shows v, a card's new view, on the board and in its pane.
function applyCard(v) {
  if (pl.open === v.id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  const i = pl.cards.findIndex((x) => x.id === v.id);
  if (i >= 0) { pl.cards[i] = { ...pl.cards[i], ...v }; renderBoard(); }
}

const patchCard = (body) => async () => applyCard(await call("PATCH", cardAPI(pl.open), body));

// ---- files: attached with the Files section's button, pasted into the
// description or a comment, or dropped on the card's pane or on the card
// on the board. A click on one previews it (lightbox.js).

// attachFiles sends files to card id, one after another.
async function attachFiles(id, files) {
  files = [...files];
  if (!id || !files.length) return;
  pl.uploading[id] = (pl.uploading[id] || 0) + files.length;
  if (pl.open === id) renderCard();
  for (const f of files) {
    try { applyCard(await postFile(cardAPI(id, "/files"), f)); } catch (e) { fail(e); }
    pl.uploading[id]--;
    if (pl.open === id) renderCard();
  }
  delete pl.uploading[id];
}

// wireFileDrop lets files be dropped on el, for the card cardOf(event) names.
function wireFileDrop(el, cardOf) {
  let over = null;
  const mark = (to) => { over?.classList.remove("file-over"); over = to; over?.classList.add("file-over"); };
  el.addEventListener("dragover", (e) => {
    if (!e.dataTransfer?.types.includes("Files") || !cardOf(e)) return;
    e.preventDefault();
    e.stopPropagation();
    mark(el === paneEl() ? el : e.target.closest(".pl-card .todo"));
  });
  el.addEventListener("dragleave", (e) => { if (!over?.contains(e.relatedTarget)) mark(null); });
  el.addEventListener("drop", (e) => {
    mark(null);
    const id = cardOf(e);
    if (!id || !e.dataTransfer?.files.length) return;
    e.preventDefault();
    e.stopPropagation();
    attachFiles(id, e.dataTransfer.files);
  });
}

// pasteFiles attaches the files pasted into a text box to the open card.
function pasteFiles(e) {
  const files = [...(e.clipboardData?.files || [])];
  if (files.length && pl.open) { e.preventDefault(); attachFiles(pl.open, files); }
}

// filesSection is the card's files: a picture's thumbnail or a file's
// extension, each opening its preview, with × to take it off the card.
function filesSection(c) {
  const files = c.files || [];
  const n = pl.uploading[c.id] || 0;
  const input = h("input", { type: "file", accept: ACCEPT, multiple: true, hidden: true,
    onchange: () => { attachFiles(c.id, input.files); input.value = ""; } });
  return h("section", { class: "pl-section", "aria-labelledby": "pd-files" },
    h("h3", { id: "pd-files" }, "Files"),
    h("div", { class: "attach pl-files", "data-preview-group": "" },
      ...files.map((f) => h("span", { class: "chip" },
        h("a", { class: "chip-link", href: fileURL(c, f), target: "_blank", rel: "noopener", title: `${f.name} (${sizeText(f.size)})`,
          "data-preview": "", "data-name": f.name, "data-type": f.type, "data-size": f.size },
        isImage(f) ? h("img", { src: fileURL(c, f), alt: "", loading: "lazy" }) : h("span", { class: "chip-icon" }, extOf(f.name)),
        h("span", { class: "chip-name" }, f.name)),
        h("button", { type: "button", class: "chip-x", "aria-label": `Delete ${f.name}`, title: "Delete",
          onclick: attempt(async () => {
            if (!await ask({ title: `Delete ${f.name}?`, body: "It is deleted from the card. Tasks already started from the card keep it.", ok: "Delete", danger: true })) return;
            applyCard(await call("DELETE", cardAPI(c.id, `/files/${encodeURIComponent(f.id)}`)));
          }) }, "×"))),
      n ? h("span", { class: "chip uploading" }, h("span", { class: "spinner", "aria-hidden": "true" }), `Uploading ${n}…`) : null,
      h("button", { type: "button", class: "btn small attach-btn", onclick: () => input.click() }, clip(), "Attach file"),
      input),
    files.length ? null : h("p", { class: "hint" }, "Pictures, PDFs and text files, up to 20 MB each. You can paste or drop them here too."));
}

function field(label, control) {
  return h("label", { class: "field pl-field" }, h("span", {}, label), control);
}

// membersField is the card's members, each with × to take them off, and a
// list of the org's other members to add one.
function membersField(c) {
  const ids = c.members || [];
  const set = (members) => patchCard({ members })().catch(fail);
  const others = pl.people.filter((u) => !ids.includes(u.id));
  return h("div", { class: "field pl-field pl-members-field", role: "group", "aria-labelledby": "pd-members" },
    h("span", { id: "pd-members" }, "Members"),
    h("div", { class: "pl-member-list" },
      ...(c.people || []).map((p) => h("span", { class: "pl-member" }, avatar(p), h("span", { class: "pl-member-name" }, p.username),
        ib({ type: "button", class: "icon-btn small", "aria-label": `Take ${p.username} off the card`, title: "Take off the card",
          onclick: () => set(ids.filter((id) => id !== p.id)) }, "x"))),
      others.length ? h("select", { "aria-label": "Add a member", onchange: (e) => e.target.value && set([...ids, Number(e.target.value)]) },
        h("option", { value: "" }, ids.length ? "Add…" : "Add a member…"),
        ...others.map((u) => h("option", { value: String(u.id) }, u.username))) : null));
}

// tagsField is the card's tags, each a button that shows only the cards
// with it and × to take it off, and a box to add one (Enter or a comma
// adds it; the org's other tags are offered as you type).
function tagsField(c) {
  const tags = c.tags || [];
  const set = (next) => patchCard({ tags: next })().catch(fail);
  const add = (input) => {
    const fresh = input.value.split(",").map((t) => t.trim()).filter((t) => t && !tags.some((x) => sameTag(x, t)));
    input.value = "";
    if (fresh.length) set([...tags, ...fresh]).then(() => $("#card-detail .pl-tag-input")?.focus());
  };
  const input = h("input", { class: "pl-tag-input", list: "pl-tag-options", maxlength: 40, autocomplete: "off", "aria-label": "Add a tag",
    placeholder: tags.length ? "Add…" : "Add a tag…",
    onkeydown: (e) => {
      if (e.key === "Enter" || e.key === ",") { e.preventDefault(); add(e.target); }
      else if (e.key === "Backspace" && !e.target.value && tags.length) set(tags.slice(0, -1));
    },
    onchange: (e) => { if (e.target.value.trim()) add(e.target); } });
  return h("div", { class: "field pl-field pl-tags-field", role: "group", "aria-labelledby": "pd-tags" },
    h("span", { id: "pd-tags" }, "Tags"),
    h("div", { class: "pl-member-list pl-tag-list" },
      ...tags.map((t) => h("span", { class: `pl-chip pl-tag${pl.tag && sameTag(pl.tag, t) ? " pl-tag-on" : ""}` },
        h("button", { type: "button", class: "pl-tag-name", title: `Show only the cards tagged ${t}`, onclick: () => pickTag(pl.tag && sameTag(pl.tag, t) ? "" : t) }, t),
        ib({ type: "button", class: "icon-btn small", "aria-label": `Remove the tag ${t}`, title: "Remove", onclick: () => set(tags.filter((x) => x !== t)) }, "x"))),
      tags.length < 20 ? input : null));
}

// descriptionEl is the card's description: a text box that is always there,
// saved when it loses focus (or on Ctrl+Enter). The same box stays across
// redraws of the card while it has focus or unsaved text, so a change from
// elsewhere doesn't take what is being typed.
function descriptionEl(c) {
  const d = pl.desc;
  if (d?.id === c.id && (document.activeElement === d.el || d.el.value !== d.saved)) return d.el;
  const el = h("textarea", { class: "pl-desc-edit", rows: 4, maxlength: 20000, "aria-label": "Description",
    placeholder: "What it is, who it's for, and how you'll know it's done. Markdown works." }, c.description || "");
  const save = () => {
    if (pl.desc?.el !== el || el.value.trim() === (pl.desc.saved || "").trim()) return;
    const text = el.value;
    patchCard({ description: text })().then(() => { if (pl.desc?.el === el) pl.desc.saved = text; }).catch(fail);
  };
  el.addEventListener("change", save);
  el.addEventListener("paste", pasteFiles);
  el.addEventListener("keydown", (e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); save(); } });
  pl.desc = { id: c.id, el, saved: c.description || "" };
  return el;
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
    ib({ type: "button", class: "btn small", title: "Change the title", onclick: () => { pl.editing = true; renderCard(); $("#pd-edit-title")?.focus(); } }, "edit", "Rename"),
    ib({ type: "button", class: "btn small", title: "Copy a link that opens this card", onclick: () => copy(linkTo({ view: "planning", card: c.id }), "Link copied") }, "link", "Copy link"));
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
    membersField(c),
    tagsField(c));

  const suggest = c.status?.suggest_done
    ? h("div", { class: "pl-suggest-box" },
      h("p", {}, "Everything linked is done."),
      ib({ type: "button", class: "btn small primary", onclick: attempt((e) => busy(e.currentTarget, () => moveCard(c.id, "done", 0))) }, "check", "Move to Done"))
    : null;

  // Rename: the title in a box, above the rest.
  let rename = null;
  if (pl.editing) {
    const t = h("input", { id: "pd-edit-title", value: c.title, maxlength: 200, autocomplete: "off", "aria-label": "Title" });
    const save = attempt((e) => busy(e.currentTarget, async () => { await patchCard({ title: t.value })(); pl.editing = false; renderCard(); }));
    t.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); save({ currentTarget: t }); }
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); pl.editing = false; renderCard(); }
    });
    rename = h("div", { class: "pl-edit" }, field("Title", t),
      h("div", { class: "pl-edit-actions" },
        ib({ type: "button", class: "btn small", onclick: () => { pl.editing = false; renderCard(); } }, "x", "Cancel"),
        ib({ type: "button", class: "btn small primary", onclick: save }, "check", "Save")));
  }
  const desc = descriptionEl(c);
  desc.id = "pd-desc";

  const links = c.links || [];
  const deps = depsSection(c);
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
  const box = h("textarea", { rows: 3, placeholder: "Add a comment", "aria-label": "Comment", maxlength: 10000, onpaste: pasteFiles });
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
      if (!await ask({ title: "Delete this card?", body: "Its comments, files and links go too, and cards that depend on it no longer do. The tasks and goals it links to stay.", ok: "Delete", danger: true })) return;
      await call("DELETE", cardAPI(c.id));
      pl.cards = pl.cards.filter((x) => x.id !== c.id);
      closeCard();
      renderBoard();
      toast("Card deleted");
    }) }, "trash", "Delete card…"));

  const keep = body.scrollTop;
  fill(body, rename, suggest, facts, h("section", { class: "pl-section" }, h("h3", {}, h("label", { for: "pd-desc" }, "Description")), desc), filesSection(c), deps, work, discussion, danger);
  body.scrollTop = keep;
}

// ---- dependencies: the cards to be done before this one, and those that
// wait for it. A card is done in the Done column.

// cardRow is a card a card depends on (or is needed by), which opens it;
// remove, if given, takes it off.
function cardRow(d, remove) {
  return h("li", { class: `pl-link t-${d.done ? "done" : "idle"}` },
    h("span", { class: "pl-link-kind", title: "Card" }, ico("plan")),
    h("span", { class: "pl-link-main" },
      h("button", { type: "button", class: "link pl-link-title", title: "Open the card", onclick: () => openCard(d.id).catch(fail) }, d.title),
      h("span", { class: "pl-link-sub" }, COLUMN_TITLE[d.column] || d.column)),
    h("span", { class: `pl-state t-${d.done ? "done" : "idle"}` }, d.done ? "Done" : "Not done yet"),
    remove ? ib({ type: "button", class: "icon-btn small", "aria-label": `Remove ${d.title}`, title: "No longer depends on it",
      onclick: attempt((e) => busy(e.currentTarget, remove)) }, "x") : null);
}

function depsSection(c) {
  const deps = c.deps || [];
  const ids = deps.map((d) => d.id);
  const needed = c.needed_by || [];
  return [
    h("section", { class: "pl-section", "aria-labelledby": "pd-deps" },
      h("h3", { id: "pd-deps" }, "Depends on"),
      deps.length
        ? h("ul", { class: "pl-links" }, ...deps.map((d) => cardRow(d, () => patchCard({ depends_on: ids.filter((x) => x !== d.id) })())))
        : h("p", { class: "hint" }, "No other cards. Add the ones that must be done before this one."),
      ib({ type: "button", class: "btn small pl-dep-add", title: "Add a card that must be done before this one", onclick: attempt(() => depSheet(c)) }, "plus", "Add a card…")),
    needed.length ? h("section", { class: "pl-section", "aria-labelledby": "pd-needed" },
      h("h3", { id: "pd-needed" }, "Needed by"),
      h("ul", { class: "pl-links" }, ...needed.map((d) => cardRow(d)))) : null,
  ];
}

// depSheet lists the org's cards to add one this card depends on. Those it
// depends on already, and those that depend on it (by way of others too),
// can't be picked.
async function depSheet(c) {
  const cards = await call("GET", cardAPI());
  const byID = new Map(cards.map((x) => [x.id, x]));
  const has = new Set((c.deps || []).map((d) => d.id));
  // waits reports whether card x depends, by way of others too, on c.
  const waits = (x, seen = new Set()) => {
    if (seen.has(x.id)) return false;
    seen.add(x.id);
    return (x.depends_on || []).some((id) => id === c.id || (byID.has(id) && waits(byID.get(id), seen)));
  };
  const q = h("input", { type: "search", placeholder: "Filter cards", "aria-label": "Filter", autocomplete: "off" });
  const list = h("ul", { class: "pl-pick" });
  let picked = null;
  const render = () => {
    const f = q.value.trim().toLowerCase();
    const shown = cards.filter((x) => x.id !== c.id && (!f || x.title.toLowerCase().includes(f))).slice(0, 100);
    fill(list, ...(shown.length ? shown.map((x) => {
      const why = has.has(x.id) ? "added" : waits(x) ? "depends on this card" : "";
      return h("li", {},
        h("button", { type: "button", class: "pl-pick-item", disabled: !!why, onclick: (e) => { picked = x; e.currentTarget.closest("dialog").close("ok"); } },
          ico(has.has(x.id) ? "check" : "plan"),
          h("span", { class: "pl-link-main" }, h("span", { class: "pl-link-title" }, x.title),
            h("span", { class: "pl-link-sub" }, [COLUMN_TITLE[x.column], x.project_name, why].filter(Boolean).join(" · ")))));
    }) : [h("li", { class: "hint" }, cards.length > 1 ? "No card matches." : "There are no other cards yet.")]));
  };
  q.addEventListener("input", render);
  render();
  const done = sheet("Depends on", [h("p", { class: "hint" }, "Pick a card that must be done before this one."), q, list], null);
  q.focus();
  await done;
  if (!picked) return;
  const id = c.id;
  const v = await call("PATCH", cardAPI(id), { depends_on: [...has, picked.id] });
  if (pl.open === id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  await loadPlanning();
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
  const { form, closed } = openSheet({ title, cls: "pl-sheet", body: kids,
    footer: ok ? ib({ class: "btn primary", value: "ok" }, icon, ok) : null });
  return closed.then((v) => (v === "ok" ? new FormData(form) : null));
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

