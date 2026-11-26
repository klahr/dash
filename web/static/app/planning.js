import { $, $$, avatarURL, dateText, dateTimeText, debounce, fill, h, ib, ico, nameHue, plural, sizeText } from "./dom.js";
import { previewText, renderMarkdown } from "../markdown.js";
import { agoText } from "../time.js";
import { popMenu } from "./popmenu.js";
import { api } from "./api.js";
import { EASE, ask, attempt, busy, copy, fail, openSheet, reduceMotion, toast } from "./dialogs.js";
import { detailPane } from "./detailpane.js";
import { changed, linkTo } from "./routes.js";
import * as store from "./store.js";
import { desktop } from "./viewport.js";
import { dropBefore } from "./glide.js";
import { makeCardDrag } from "./carddrag.js";
import { ACCEPT, clip, postFile } from "./attachments.js";
import { extOf, isImage } from "./lightbox.js";
import { dictation } from "./dictation.js";

// ------------------------------------------------------------------ Planning
// The org's plan, for a product owner (docs/planning.md): cards in four
// fixed columns, each linked to the tasks and goals that carry it out, with
// a status worked out on the server from those, in owner-facing words.
// A card opens in a pane beside the board (full screen with Back on a
// phone; Esc closes), as History's commit pane does.
//
// An org has several boards of cards: their tabs head the page, with All
// boards (every board's cards) when there is more than one. A board is added
// with + after the tabs, and renamed, reordered, made the default or deleted
// from its tab's menu (right click, or ⋯ on the tab shown); a double click
// renames it.
//
// One module for both places Planning shows: the app's Planning view
// (members, admins, owners; index.html) and planning.html, the page a
// planner lands on. It uses only dom, api, dialogs, store, viewport, glide, carddrag,
// attachments, lightbox, dictation, popmenu, markdown and time,
// which load outside the app too, and builds its own markup in the root
// it is mounted on. Planning is the hub's (api prefix "").

// s: the task board's lane colour a column takes (style.css: .s-*).
export const COLUMNS = [
  { key: "ideas", title: "Ideas", s: "draft" },
  { key: "planned", title: "Planned", s: "queued" },
  { key: "in_progress", title: "In progress", s: "in_progress" },
  { key: "done", title: "Done", s: "merged" },
];
const COLUMN_TITLE = Object.fromEntries(COLUMNS.map((c) => [c.key, c.title]));
const PRIORITY = { high: "High", medium: "Medium", low: "Low" };
const call = (method, path, body) => api(method, path, body, "");
const cardAPI = (id = "", rest = "") => `/api/planning/cards${id ? `/${encodeURIComponent(id)}` : ""}${rest}`;
const boardAPI = (id = "") => `/api/planning/boards${id ? `/${encodeURIComponent(id)}` : ""}`;
// fileURL is a card's file, through the card (a planner can't read uploads).
const fileURL = (c, f) => cardAPI(c.id, `/files/${encodeURIComponent(f.id)}/${encodeURIComponent(f.name)}`);

// BOARDS_ALL, as the board shown, is every board's cards.
const BOARDS_ALL = "all";

const pl = {
  root: null,
  planner: false,
  hooks: {},
  cards: [],
  projects: [],
  people: [], // the org's members a card may be assigned to
  boards: [], // the org's boards, in their order, with how many cards each has
  loads: 0, // loadPlanning's count: only the latest one's answer is shown
  board: store.get("dash.planning.board", ""), // the board shown ("": the default one)
  filter: store.get("dash.planning.project", ""),
  // The board's other filters: only the cards with all of tags, with
  // target ("" any), and (mine) with the signed-in user among their members.
  tags: savedTags(),
  target: store.get("dash.planning.target", ""),
  mine: store.flag("dash.planning.mine"),
  q: "", // the search: only the cards with each of its words in their title or description
  searching: false, // the search field is open
  boardEdit: null, // a board's name being typed in its tab: { id ("": a new board), input }
  loaded: false,
  open: null, // the open card's id
  detail: null, // its view, with its discussion
  error: "",
  editing: false,
  lane: "", // the column a phone shows (one at a time, as the task board)
  composer: null, // the new card being typed on the board: { col, li, ta, busy }
  uploading: {}, // card id → how many files are on their way to it
};

// savedTags are the tags picked last time (an older Dash kept one, as a string).
function savedTags() {
  const tags = store.getJSON("dash.planning.tags", null);
  if (Array.isArray(tags)) return tags.filter((t) => typeof t === "string" && t);
  const one = store.get("dash.planning.tag", "");
  return one ? [one] : [];
}

const byId = (id) => pl.cards.find((c) => c.id === id);
const defaultBoard = () => pl.boards.find((b) => b.default);
// boardShown is the id of the board shown ("" until the boards are in;
// BOARDS_ALL for every board's cards).
const boardShown = () => pl.board || defaultBoard()?.id || "";
const allBoards = () => pl.board === BOARDS_ALL;
// boardOrder is where card c's board is among the boards (All boards sorts by it).
const boardOrder = (c) => pl.boards.findIndex((b) => b.id === c.board_id);
// saveBoard makes id the board shown, for next time too ("": the default).
function saveBoard(id) {
  pl.board = id && id !== defaultBoard()?.id ? id : "";
  store.set("dash.planning.board", pl.board);
}
const sameTag = (a, b) => a.toLowerCase() === b.toLowerCase();
const tagged = (c, tag) => (c.tags || []).some((t) => sameTag(t, tag));
const tagPicked = (tag) => pl.tags.some((t) => sameTag(t, tag));
const meId = () => pl.hooks.me?.()?.id || 0;
// filtered: whether a filter other than the project's (or the search) hides cards.
const filtered = () => pl.tags.length > 0 || !!pl.target || (pl.mine && !!meId()) || !!pl.q.trim();
// matches: whether each word of the search is in card c's title or description.
function matches(c) {
  const words = pl.q.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return true;
  const text = `${c.title || ""}\n${c.description || ""}`.toLowerCase();
  return words.every((w) => text.includes(w));
}
// shows: whether the board shows card c: it has every tag picked, the
// target picked, the user among its members, and the search's words, as far
// as those are picked.
function shows(c) {
  if (!matches(c)) return false;
  if (!pl.tags.every((t) => tagged(c, t))) return false;
  if (pl.target && !sameTag(c.target || "", pl.target)) return false;
  const me = meId();
  return !(pl.mine && me && !(c.members || []).includes(me));
}
// shown are the cards the board shows.
const shown = () => filtered() ? pl.cards.filter(shows) : pl.cards;
// distinct is each of values once, whatever its case, in order: A to Z,
// with numbers in it by their value (1.9 before 1.10).
function distinct(values) {
  const seen = new Map();
  for (const v of values) if (v && !seen.has(v.toLowerCase())) seen.set(v.toLowerCase(), v);
  return [...seen.values()].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base", numeric: true }));
}
// allTags are the tags on the cards loaded; allTargets their targets.
const allTags = () => distinct(pl.cards.flatMap((c) => c.tags || []));
const allTargets = () => distinct(pl.cards.map((c) => c.target || ""));
const paneEl = () => $("#card-detail");

// mountPlanning builds Planning in root. planner hides what a planner
// can't do (link existing work, open it). hooks:
//   visible()    whether the board shows (default: always), to load it
//   background() the elements the pane covers on a phone (made inert)
//   openWork(link) opens a linked task or goal (members)
//   askChat(card) opens the chat about the card: its project's chat, or
//                the general chat (a planner's only one)
//   featureBrief({ card, project, idea }) opens the feature brief guide:
//                for card, or for a new card from idea
//   claudeNeeded() what shows in place of those two when Claude can't run
//                for the user (a planner without a runner online): a new
//                element each call, or null when it can
//   me()         the signed-in user: { id, admin } (to delete comments)
// The card's pane goes in paneParent (default root): outside what
// background() returns, which a phone makes inert while the pane is open.
export function mountPlanning(root, { planner = false, hooks = {}, paneParent = root } = {}) {
  pl.root = root;
  pl.planner = planner;
  pl.hooks = hooks;
  fill(root,
    h("div", { class: "page planning" },
      // One row: the boards' tabs (the page's title), + to add one; search,
      // the filters picked, Mine, Filter and New card. On a phone, Mine, New
      // card and + are under ⋯, and the open search takes the tabs' place.
      h("header", { class: "pl-head", id: "pl-head" },
        h("h2", { class: "sr-only" }, "Planning"),
        h("div", { class: "pl-boards", id: "pl-boards", role: "tablist", "aria-label": "Boards", onkeydown: boardTabsKey }),
        ib({ type: "button", class: "icon-btn pl-board-add", id: "pl-board-add", "aria-label": "Add board", title: "Add board", onclick: () => editBoard("") }, "plus"),
        h("div", { class: "pl-search", id: "pl-search", role: "search" },
          ib({ type: "button", class: "icon-btn", id: "pl-search-open", "aria-label": "Search cards", title: "Search cards", "aria-controls": "pl-search-input", "aria-expanded": "false",
            onclick: () => (pl.searching || pl.q ? closeSearch() : openSearch()) }, "search"),
          h("input", { type: "search", id: "pl-search-input", "aria-label": "Search cards", autocomplete: "off", spellcheck: "false", enterkeyhint: "search", hidden: true,
            oninput: (e) => setSearch(e.target.value),
            onkeydown: (e) => {
              if (e.key !== "Escape" || e.isComposing) return;
              e.preventDefault();
              e.stopPropagation(); // not the card's pane
              closeSearch();
              $("#pl-search-open").focus();
            },
            onblur: () => { if (!pl.q && pl.searching) { pl.searching = false; renderSearch(); } } })),
        h("span", { class: "pl-tag-filter", id: "pl-tag-filter", hidden: true }),
        ib({ type: "button", class: "btn small pl-mine", id: "pl-mine", "aria-pressed": "false", hidden: true, onclick: () => pickMine(!pl.mine) }, "user", "Mine"),
        h("button", { type: "button", class: "btn small pl-filter-btn", id: "pl-filter", "aria-haspopup": "menu", "aria-expanded": "false", hidden: true,
          onclick: (e) => filterMenu(e.currentTarget) },
        ico("filter"), h("span", { class: "pl-filter-label" }, "Filter"), h("span", { class: "n", id: "pl-filter-n" })),
        ib({ type: "button", class: "btn small primary pl-new", onclick: () => openComposer("ideas") }, "plus", "New card"),
        ib({ type: "button", class: "icon-btn pl-head-more", id: "pl-head-more", "aria-label": "More", title: "More", "aria-haspopup": "menu", "aria-expanded": "false",
          onclick: (e) => headMenu(e.currentTarget) }, "more")),
      // The task board's layout (style.css: .lanes, .board, .col): on a
      // phone a strip of lanes and one column at a time, on a desktop the
      // columns side by side. A column is a list of bordered cards (no rail
      // line or nodes; style.css: .pl-cards) and ends in Add card, which
      // opens a new card to type on at the end of the column (openComposer).
      h("div", { class: "lanes pl-lanes", role: "tablist", "aria-label": "Columns" },
        ...COLUMNS.map((col) => h("button", { type: "button", role: "tab", class: `lane-tab s-${col.s}`, "data-col": col.key,
          onclick: () => { pl.lane = col.key; renderBoard(); } }, h("span", { class: "lane-label" }, col.title), h("span", { class: "n", id: `pl-tab-n-${col.key}` })))),
      h("div", { class: "board pl-board", id: "pl-board" },
        ...COLUMNS.map((col) => h("section", { class: `col pl-col s-${col.s}`, "data-col": col.key, "aria-labelledby": `pl-col-${col.key}` },
          h("header", { class: "col-head" },
            h("span", { class: "col-label", id: `pl-col-${col.key}` }, col.title), h("span", { class: "n", id: `pl-count-${col.key}` })),
          h("div", { class: "col-body" },
            h("ol", { class: "pl-cards", "data-col": col.key, "aria-labelledby": `pl-col-${col.key}` }),
            ib({ type: "button", class: "pl-add", "data-col": col.key, "aria-label": `Add card to ${col.title}`, onclick: () => openComposer(col.key) }, "plus", "Add card")))))));
  paneParent.append(
    h("aside", { class: "detail card-detail", id: "card-detail", hidden: true, "aria-labelledby": "pd-title" },
      h("header", { class: "detail-head" },
        h("button", { class: "icon-btn", id: "pd-close", type: "button", "aria-label": "Back", title: "Close (Esc)", onclick: closeCard, innerHTML:
          '<svg class="cd-back" viewBox="0 0 24 24" aria-hidden="true"><path d="m15 6-6 6 6 6" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>' +
          '<svg class="cd-x" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6 6 18" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/></svg>' }),
        h("div", { class: "detail-titles" },
          h("h2", { id: "pd-title" }),
          h("div", { class: "detail-meta", id: "pd-meta" }),
          h("div", { class: "commit-actions pl-actions", id: "pd-actions" })),
        ib({ type: "button", class: "icon-btn", id: "pd-copy", "aria-label": "Copy link", title: "Copy link",
          onclick: () => pl.open && copy(linkTo({ view: "planning", card: pl.open }), "Link copied") }, "link")),
      h("div", { class: "detail-body", id: "pd-body" })),
    h("datalist", { id: "pl-tag-options" }));
  cardDrag.attach(root);
  wireSwipe($("#pl-board", root));
  wireFileDrop(paneEl(), () => pl.open);
  wireFileDrop(root, (e) => e.target.closest?.(".pl-card:not(.pl-composer)")?.dataset.id);
  desktop.addEventListener?.("change", () => renderTagFilter());
  renderFilter();
}

// loadPlanning reads the boards, the cards of the board shown (of the
// project picked, or all) and the projects, and shows them. A board that
// is gone (deleted elsewhere) gives way to the default one.
export async function loadPlanning() {
  if (!pl.root) return;
  const seq = ++pl.loads, want = pl.board;
  const q = new URLSearchParams();
  if (want) q.set("board", want);
  if (pl.filter) q.set("project", pl.filter);
  const qs = q.toString() ? `?${q}` : "";
  const [cards, projects, people, boards] = await Promise.all([
    call("GET", cardAPI() + qs).catch((e) => { if (e.status === 404 && want) return null; throw e; }),
    call("GET", "/api/planning/projects"), call("GET", "/api/planning/people"), call("GET", boardAPI())]);
  // A later load (another board or project picked, or an event) shows its own answer.
  if (seq !== pl.loads || pl.board !== want) return;
  pl.boards = boards;
  if (!cards) {
    saveBoard("");
    changed("replace");
    if (pl.open && pl.detail?.board_id === want) closeCard();
    return loadPlanning();
  }
  saveBoard(want); // the default board's id is ""
  pl.cards = cards;
  pl.projects = projects;
  pl.people = people;
  if (pl.filter && !projects.some((p) => p.id === pl.filter)) {
    pl.filter = "";
    store.set("dash.planning.project", "");
  }
  pl.loaded = true;
  renderFilter();
  renderBoards();
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
  reloadPlanning(cards, ev);
}
const reloadPlanning = debounce((cards, ev) => {
  if (pl.hooks.visible && !pl.hooks.visible() && !pl.open) { pl.loaded = false; return; }
  loadPlanning().catch(() => {});
  if (pl.open && (cards.includes(pl.open) || !cards.length)) {
    if (ev.data?.deleted && ev.data.card === pl.open) { closeCard(); return; }
    loadCard(pl.open).catch(() => {});
  }
}, 250);

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

// ---- the filters: a menu of the projects, the tags and the targets
// (Filter), the ones picked beside it with × to take them off (on a desktop),
// and Mine. On a phone, Mine is in the menu too, and Filter counts them all.

// planningIcon is a project's icon, or its initial on its color, as the
// sidebar shows it (style.css: .side-project-icon).
function planningIcon(p) {
  const el = h("span", { class: "side-project-icon", "aria-hidden": "true" });
  el.style.setProperty("--project", p.color || "");
  el.append(p.icon ? h("img", { src: p.icon, alt: "" }) : [...p.name][0]?.toUpperCase() || "?");
  return el;
}

function pickFilter(id) {
  if (id === pl.filter) return;
  pl.filter = id;
  store.set("dash.planning.project", pl.filter);
  renderFilter();
  changed("replace");
  loadPlanning().catch(fail);
}

// filterMenu lists what the cards can be filtered by, in groups: projects,
// tags, targets (Mine too, on a phone); a pick puts a filter on or off.
function filterMenu(anchor) {
  const group = (heading, items) => Object.assign(items, { heading });
  const projects = pl.projects.length > 1 || pl.filter ? group("Project", [
    ["repo", "All projects", () => pickFilter(""), { checked: !pl.filter }],
    ...pl.projects.map((p) => [planningIcon(p), p.name, () => pickFilter(p.id === pl.filter ? "" : p.id), { checked: p.id === pl.filter }])]) : [];
  const tags = group("Tags", allTags().map((t) => ["tag", t, () => pickTag(t, !tagPicked(t)), { checked: tagPicked(t) }]));
  const targets = group("Targets", allTargets().map((t) => ["flag", t, () => pickTarget(sameTag(t, pl.target) ? "" : t), { checked: sameTag(t, pl.target) }]));
  const mine = !desktop.matches && meId() ? [["user", "Mine", () => pickMine(!pl.mine), { checked: pl.mine }]] : [];
  const any = pl.filter || pl.tags.length || pl.target || (pl.mine && mine.length);
  return popMenu(anchor, [mine, projects, tags, targets, any ? [["x", "Clear filters", clearFilters]] : []], { heading: "Filter" });
}

function clearFilters() {
  pl.tags = [];
  pl.target = "";
  if (!desktop.matches) pl.mine = false;
  saveFilters();
  pickFilter("");
}

// headMenu is a phone's ⋯: New card, Add board, and Mine.
function headMenu(anchor) {
  return popMenu(anchor, [[
    ["plus", "New card", () => openComposer("ideas")],
    ["plan", "Add board", () => editBoard("")],
  ], meId() ? [["user", "Mine", () => pickMine(!pl.mine), { checked: pl.mine }]] : []]);
}

function renderFilter() {
  renderTagFilter();
}

// ---- search: the magnifier opens a field that shows only the cards with
// each of its words in their title or description, as you type. Esc clears
// it; left empty, it folds back to the magnifier.

function renderSearch() {
  const input = $("#pl-search-input");
  if (!input) return;
  if (document.activeElement !== input && input.value !== pl.q) input.value = pl.q;
  const open = pl.searching || !!pl.q;
  input.hidden = !open;
  $("#pl-head").classList.toggle("searching", open);
  $("#pl-search-open").setAttribute("aria-expanded", String(open));
}

function openSearch() {
  pl.searching = true;
  renderSearch();
  $("#pl-search-input").focus();
}

function closeSearch() {
  pl.searching = false;
  setSearch("");
}

function setSearch(q) {
  if (q === pl.q) { renderSearch(); return; }
  pl.q = q;
  renderSearch();
  renderBoard();
}

// ---- the boards: a tab each at the head of the page (All boards first,
// when there is more than one). Arrows, Home and End move between them, as
// between tabs; F2 renames the one focused. A tab's menu (right click, or ⋯
// on the tab shown) renames, moves, makes default or deletes its board.

function renderBoards() {
  const el = $("#pl-boards");
  if (!el) return;
  const on = allBoards() ? BOARDS_ALL : boardShown();
  const tab = (id, name, props = {}) => h("button", {
    type: "button", role: "tab", class: "pl-board-tab", "data-board": id, "aria-selected": String(id === on), tabindex: id === on ? "0" : "-1",
    onclick: () => pickBoard(id), ...props,
  }, h("span", { class: "pl-board-name" }, name));
  const kids = pl.boards.map((b) => {
    if (pl.boardEdit?.id === b.id) return pl.boardEdit.input;
    const t = tab(b.id, b.name, { ondblclick: () => editBoard(b.id), oncontextmenu: (e) => { e.preventDefault(); boardMenu(b, e.currentTarget); } });
    if (b.id !== on) return t;
    return h("span", { class: "pl-board-on", role: "none" }, t,
      ib({ type: "button", class: "icon-btn pl-board-more", "aria-label": `${b.name}: board menu`, title: "Board menu", "aria-haspopup": "menu", "aria-expanded": "false",
        onclick: (e) => boardMenu(b, e.currentTarget) }, "more"));
  });
  if (pl.boards.length > 1) kids.unshift(tab(BOARDS_ALL, "All boards"));
  if (pl.boardEdit?.id === "") kids.push(pl.boardEdit.input);
  // Keep the box being typed in where it is, with its text and focus. Moving
  // it takes its focus (a blur): that isn't leaving the box, so it doesn't save.
  const edit = pl.boardEdit;
  const focused = edit && (document.activeElement === edit.input || edit.moving);
  if (focused) edit.moving = true;
  fill(el, ...kids);
  if (focused) {
    edit.input.focus();
    queueMicrotask(() => {
      edit.moving = false;
      if (pl.boardEdit === edit && edit.input.isConnected && document.activeElement !== edit.input) edit.input.focus();
    });
  }
}

function boardTabsKey(e) {
  if (e.target.matches?.(".pl-board-input")) return;
  const tabs = $$(".pl-board-tab", e.currentTarget);
  const i = tabs.indexOf(document.activeElement);
  if (i < 0) return;
  if (e.key === "F2" && tabs[i].dataset.board !== BOARDS_ALL) { e.preventDefault(); editBoard(tabs[i].dataset.board); return; }
  const to = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
  if (to === undefined) return;
  e.preventDefault();
  const t = tabs[Math.max(0, Math.min(tabs.length - 1, to))];
  t.focus();
  pickBoard(t.dataset.board);
}

// pickBoard shows board id (its cards; the filters stay; BOARDS_ALL: every
// board's). A card of another board that is open closes.
export function pickBoard(id) {
  const was = pl.board || boardShown();
  saveBoard(id);
  if ((pl.board || boardShown()) === was) return;
  if (pl.open && !allBoards() && pl.detail?.board_id && pl.detail.board_id !== boardShown()) closeCard();
  changed("replace");
  renderBoards();
  loadPlanning().catch(fail);
}

// boardMenu is board b's menu, under anchor.
function boardMenu(b, anchor) {
  const i = pl.boards.findIndex((x) => x.id === b.id);
  const patch = (body) => () => boardChange(() => call("PATCH", boardAPI(b.id), body));
  return popMenu(anchor, [[
    ["edit", "Rename", () => editBoard(b.id)],
    ["star", "Default", patch({ default: true }), { checked: !!b.default, disabled: !!b.default }],
    i > 0 ? ["left", "Move left", patch({ position: i - 1 })] : null,
    i < pl.boards.length - 1 ? ["right", "Move right", patch({ position: i + 1 })] : null,
  ].filter(Boolean), b.default ? [] : [["trash", "Delete…", () => deleteBoard(b), { danger: true }]]], { heading: b.name });
}

// boardChange runs a change to the boards, then shows them as they are now.
async function boardChange(fn) {
  try { await fn(); } finally { await loadPlanning().catch(fail); }
}

async function deleteBoard(b) {
  const n = b.cards || 0;
  const ok = await ask({ title: `Delete the board ${b.name}?`, ok: "Delete", danger: true,
    body: n ? `Its ${plural(n, "card")} move${n === 1 ? "s" : ""} to ${defaultBoard()?.name || "the default board"}.` : "It has no cards." });
  if (!ok) return;
  if (b.id === boardShown() && !allBoards()) pickBoard("");
  await boardChange(() => call("DELETE", boardAPI(b.id)));
  toast(`Board “${b.name}” deleted`);
}

// editBoard turns board id's tab into a box with its name ("": a new
// board's, after the tabs). Enter or leaving it saves, Esc cancels.
function editBoard(id) {
  if (pl.boardEdit) return;
  const b = pl.boards.find((x) => x.id === id);
  if (id && !b) return;
  const input = h("input", { class: "pl-board-input", value: b?.name || "", maxlength: 60, autocomplete: "off", size: Math.max(8, (b?.name || "").length + 2),
    "aria-label": b ? `Name of the board ${b.name}` : "New board" });
  let done = false;
  const end = (focus) => {
    pl.boardEdit = null;
    renderBoards();
    if (focus) ($(`#pl-boards .pl-board-tab[data-board="${focus}"]`) || $("#pl-board-add"))?.focus();
  };
  const save = async () => {
    if (done) return;
    done = true;
    const name = input.value.trim();
    if (!name || name === b?.name) { end(id || ""); return; }
    input.readOnly = true;
    try {
      const v = b ? await call("PATCH", boardAPI(b.id), { name }) : await call("POST", boardAPI(), { name });
      pl.boardEdit = null;
      if (!b) { toast(`Board “${v.name}” added`); saveBoard(v.id); changed("replace"); }
      await loadPlanning();
      $(`#pl-boards .pl-board-tab[data-board="${v.id}"]`)?.focus();
    } catch (e) {
      fail(e);
      end(id || "");
    }
  };
  input.addEventListener("input", () => { input.size = Math.max(8, input.value.length + 2); });
  input.addEventListener("keydown", (e) => {
    if (e.isComposing) return;
    if (e.key === "Enter") { e.preventDefault(); save(); }
    else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); done = true; end(id || ""); }
  });
  const edit = { id, input, moving: false };
  input.addEventListener("blur", () => { if (document.hasFocus() && !done && !edit.moving) save(); });
  pl.boardEdit = edit;
  renderBoards();
  input.focus();
  input.select();
  input.scrollIntoView?.({ block: "nearest", inline: "nearest" });
}

// ---- the board

// renderBoard draws the columns; cards glide from where they were to where
// they are now (as the task board's do), new ones fade in. Not under a
// drag: the board is drawn again once the card is dropped.
function renderBoard() {
  if (cardDrag.active) return;
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

// saveFilters keeps the board's filters for next time, puts them in the
// address, and shows the board (and the open card) with them.
function saveFilters() {
  store.setJSON("dash.planning.tags", pl.tags);
  store.remove("dash.planning.tag");
  store.set("dash.planning.target", pl.target);
  store.setFlag("dash.planning.mine", pl.mine);
  changed("replace");
  renderBoard();
  if (pl.open) renderCard();
}

// pickTag adds tag to the tags the board shows the cards of (on false: takes
// it off them).
export function pickTag(tag, on = true) {
  const others = pl.tags.filter((t) => !sameTag(t, tag));
  pl.tags = on ? [...others, tag] : others;
  saveFilters();
}

// pickTarget shows only the cards with target ("": any target).
function pickTarget(target) {
  pl.target = target;
  saveFilters();
}

// pickMine shows only the cards the signed-in user is a member of (or all).
function pickMine(on) {
  pl.mine = on;
  saveFilters();
}

// renderTagFilter shows the filters picked, each with × to take it off (a
// desktop); Mine; and Filter, with how many are on.
function renderTagFilter() {
  const off = (label, run) => ib({ type: "button", class: "icon-btn small", "aria-label": label, title: "Remove", onclick: run }, "x");
  const el = $("#pl-tag-filter");
  if (!el) return;
  const project = pl.projects.find((p) => p.id === pl.filter);
  el.hidden = !pl.tags.length && !pl.target && !project;
  fill(el,
    project ? h("span", { class: "pl-chip pl-tag pl-project-on pl-tag-on" }, planningIcon(project), h("span", { class: "pl-tag-name" }, project.name),
      off(`Remove the filter ${project.name}`, () => pickFilter(""))) : null,
    ...pl.tags.map((t) => tagChip(t, "span", { class: "pl-tag-on" }, ico("tag"), h("span", { class: "pl-tag-name" }, t), off(`Remove the filter ${t}`, () => pickTag(t, false)))),
    pl.target ? h("span", { class: "pl-chip pl-tag pl-target-on pl-tag-on" }, ico("flag"), h("span", { class: "pl-tag-name" }, pl.target),
      off(`Remove the filter ${pl.target}`, () => pickTarget(""))) : null);
  const mine = $("#pl-mine");
  mine.hidden = !meId();
  mine.setAttribute("aria-pressed", String(pl.mine));
  mine.classList.toggle("on", pl.mine);
  // Filter shows while there is something to filter by, or a filter is on.
  const n = (pl.filter ? 1 : 0) + pl.tags.length + (pl.target ? 1 : 0) + (!desktop.matches && pl.mine && meId() ? 1 : 0);
  const btn = $("#pl-filter");
  btn.hidden = !n && pl.projects.length < 2 && !allTags().length && !allTargets().length && (desktop.matches || !meId());
  btn.classList.toggle("on", n > 0);
  $("#pl-filter-n").textContent = n ? String(n) : "";
  btn.setAttribute("aria-label", n ? `Filter: ${n} on` : "Filter");
  fill($("#pl-tag-options"), ...allTags().map((t) => h("option", { value: t })));
}

function renderColumns() {
  renderTagFilter();
  const counts = Object.fromEntries(COLUMNS.map((col) => [col.key, shown().filter((c) => c.column === col.key).length]));
  // A phone opens on the first column with cards in it.
  if (!pl.lane) pl.lane = (pl.cards.length && COLUMNS.find((col) => counts[col.key])?.key) || "ideas";
  for (const col of COLUMNS) {
    const ol = $(`.pl-cards[data-col="${col.key}"]`, pl.root);
    const cards = shown().filter((c) => c.column === col.key).sort((a, b) => boardOrder(a) - boardOrder(b) || a.position - b.position);
    const focused = document.activeElement?.closest?.(".pl-card")?.dataset.id;
    // A card being typed stays where it is, last, with its text and focus.
    const comp = pl.composer?.col === col.key ? pl.composer.li : null;
    if (comp) {
      if (comp.parentNode !== ol) ol.append(comp);
      for (const n of [...ol.children]) if (n !== comp) n.remove();
      comp.before(...cards.map(cardEl));
    } else fill(ol, ...cards.map(cardEl));
    ol.hidden = !cards.length && !comp;
    if (focused && ol.querySelector(`[data-id="${focused}"]`)) $(`[data-id="${focused}"] .pl-open`, ol)?.focus({ preventScroll: true });
    $(`#pl-count-${col.key}`).textContent = String(cards.length);
    // A lane's count shows only when it has cards, so the four lanes fit a phone.
    $(`#pl-tab-n-${col.key}`).textContent = cards.length ? String(cards.length) : "";
    $(`.pl-col[data-col="${col.key}"]`, pl.root).classList.toggle("active", pl.lane === col.key);
    $(`.pl-lanes [data-col="${col.key}"]`, pl.root).setAttribute("aria-selected", String(pl.lane === col.key));
  }
  renderSearch();
  markSelected();
}

function statusEl(st, cls = "pl-status") {
  return h("span", { class: `${cls} t-${st.tone}` },
    h("span", { class: "pl-status-text" }, st.text),
    st.progress ? h("span", { class: "pl-progress" }, st.progress) : null);
}

// MEMBER_FACES is how many members' faces a card shows; more are a count.
const MEMBER_FACES = 3;

// initials are a name's two letters: the first of its first two words
// ("ana.berg": AB), or its first two ("admin": AD, "ana": AN).
export function initials(name) {
  const words = (name || "").split(/[\s._@-]+/).filter(Boolean);
  const chars = words.length > 1 ? [[...words[0]][0], [...words[1]][0]] : [...(words[0] || "?")].slice(0, 2);
  return chars.join("").toUpperCase();
}

// face is u's picture, or their initials on a colour of their own (from
// their id, the same everywhere), in a circle; cls adds classes.
function face(u, cls = "") {
  const el = h("span", { class: `avatar pl-face${cls ? ` ${cls}` : ""}`, "aria-hidden": "true", title: u.username, style: `--face-h: ${nameHue(String(u.id))}` });
  const letters = () => { el.classList.add("pl-initials"); el.replaceChildren(initials(u.username)); };
  const url = avatarURL(u);
  if (!url) { letters(); return el; }
  const img = h("img", { src: url, alt: "", decoding: "async", draggable: false });
  img.addEventListener("error", letters, { once: true });
  el.replaceChildren(img);
  return el;
}

// membersEl is the card's members' faces, overlapping, with their names for
// a screen reader. The owner comes first, ringed.
function membersEl(c) {
  const owner = (c.people || []).find((p) => p.id === c.owner);
  const ps = owner ? [owner, ...c.people.filter((p) => p !== owner)] : c.people || [];
  if (!ps.length) return null;
  const names = (owner ? `Owner: ${owner.username}. ` : "") + `Members: ${ps.map((p) => p.username).join(", ")}`;
  const more = ps.length - MEMBER_FACES;
  const faces = more > 0 ? MEMBER_FACES - 1 : MEMBER_FACES;
  return h("span", { class: "pl-members" },
    ...ps.slice(0, faces).map((p) => face(p, p === owner ? "pl-owner-face" : "")),
    more > 0 ? h("span", { class: "avatar pl-more", "aria-hidden": "true", title: ps.slice(faces).map((p) => p.username).join(", ") }, `+${more + 1}`) : null,
    h("span", { class: "sr-only" }, names));
}

// waitsEl marks a card that waits for cards it depends on, not done yet.
function waitsEl(c) {
  if (!c.waiting) return null;
  const titles = (c.deps || []).filter((d) => !d.done).map((d) => d.title).join(", ");
  return h("span", { class: "pl-chip pl-waits", title: `Waits for ${titles}` }, ico("lock"), `Waits for ${c.waiting}`,
    h("span", { class: "sr-only" }, `: ${titles}`));
}

// tagChip is a tag t as an element tag (props and kids added), in the
// tag's own colour, as the task board's tags are (style.css: .hued).
function tagChip(t, tag, props, ...kids) {
  return h(tag, { ...props, class: `pl-chip pl-tag hued${props.class ? ` ${props.class}` : ""}`, style: `--tag-h: ${nameHue(t.toLowerCase())}` }, ...kids);
}

// projectChip names card c's project, with its icon (or its initial on its
// colour), unless the board shows one project's cards only.
function projectChip(c) {
  if (pl.filter || !c.project_id) return null;
  const p = pl.projects.find((x) => x.id === c.project_id);
  const name = p?.name || c.project_name;
  if (!name) return null;
  return h("span", { class: "pl-chip pl-project", "aria-label": `Project: ${name}` }, p ? planningIcon(p) : ico("repo"), h("span", { class: "pl-project-name" }, name));
}

// peopleEl is the card's members' faces and, while you aren't one of them,
// a Join slot after them (shown on hover, or focus).
function peopleEl(c) {
  const me = meId();
  const join = me && !(c.members || []).includes(me) ? h("button", { type: "button", class: "avatar pl-join", "aria-label": `Join ${c.title}`, title: "Join",
    onclick: (e) => { e.stopPropagation(); toggleMine(c).catch(fail); } }, ico("plus")) : null;
  const faces = membersEl(c);
  return faces || join ? h("span", { class: "pl-people" }, faces, join) : null;
}

function cardEl(c) {
  const meta = [
    // Each kind of chip has a look of its own (style.css): a priority is the
    // task board's quiet label (High with an up arrow), a target has a flag,
    // a tag is a pill in its own colour, a project has its icon.
    c.priority ? h("span", { class: `meta-chip prio-chip pl-prio p-${c.priority}${c.priority === "high" ? " prio-high" : ""}`, "aria-label": `Priority: ${PRIORITY[c.priority]}` },
      c.priority === "high" ? ico("up") : c.priority === "low" ? ico("down") : null, PRIORITY[c.priority]) : null,
    waitsEl(c),
    c.target ? h("span", { class: "pl-chip pl-target", "aria-label": `Target: ${c.target}` }, ico("flag"), c.target) : null,
    ...(c.tags || []).map((t) => tagChip(t, "span", { "aria-label": `Tag: ${t}` }, t)),
    allBoards() && c.board_name ? h("span", { class: "pl-chip pl-board-chip", "aria-label": `Board: ${c.board_name}` }, ico("plan"), c.board_name) : null,
    projectChip(c),
    c.comments ? h("span", { class: "pl-comments", "aria-label": plural(c.comments, "comment") }, ico("comment"), String(c.comments)) : null,
    c.files?.length ? h("span", { class: "pl-comments pl-file-count", "aria-label": plural(c.files.length, "file") }, clip(), String(c.files.length)) : null,
    peopleEl(c),
  ].filter(Boolean);
  // Under the title: the status of the linked work, or else the start of the description.
  const about = previewText(c.description);
  const sub = c.status?.total || c.status?.removed ? h("span", { class: "todo-sub" }, statusEl(c.status))
    : about ? h("span", { class: "todo-sub" }, about) : null;
  // A bordered card (style.css: .pl-card), built from the task board's
  // card parts (app/branches.js: todoRow) but without its rail node: the
  // status line's dot shows the status's colour, Done cards lie flat. The
  // title is the button that opens the card; a press anywhere on it does too.
  // The card's first picture, across its top.
  const cover = (c.files || []).find(isImage);
  return h("li", { class: "pl-card", "data-id": c.id, title: c.title },
    h("div", { class: `todo t-${c.status?.tone || "none"}${c.column === "done" ? " pushed" : ""}${c.id === pl.open ? " selected" : ""}`,
      onclick: () => openCard(c.id).catch(fail) },
      cover ? h("img", { class: "pl-cover", src: fileURL(c, cover), alt: "", loading: "lazy", draggable: false }) : null,
      h("span", { class: "todo-main" },
        h("button", { type: "button", class: "todo-open pl-open", "aria-controls": "card-detail", "aria-current": c.id === pl.open ? "true" : null,
          onkeydown: (e) => cardKeys(e, c) },
          h("span", { class: "todo-title" }, c.title)),
        sub),
      meta.length ? h("span", { class: "todo-side" }, ...meta) : null));
}

// ---- dragging, as the task board drags (carddrag.js): the card leaves
// its place for a gap where it would land; a drop moves it there.
const cardDrag = makeCardDrag({
  item: ".pl-card:not(.pl-composer)", li: ".pl-card", list: ".pl-cards", ghost: "pl-ghost", slot: "pl-slot",
  pick: (li) => li.dataset.id,
  move: moveDrag,
  drop: dropCard,
  end: clearDropMarks,
  abort: () => renderBoard(), // back to its place, with what changed meanwhile
});

// cardsOf are ol's cards, but the one dragged and one being typed.
const cardsOf = (ol) => $$(".pl-card:not(.pl-composer):not(.pl-slot)", ol).filter((li) => li.dataset.id !== cardDrag.data);

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

function moveDrag(x, y) {
  clearDropMarks();
  const ol = dropTarget(x, y);
  if (!ol) { cardDrag.place(null); return; }
  const tab = document.elementFromPoint(x, y)?.closest?.(".pl-lanes .lane-tab");
  (tab || ol.closest(".pl-col")).classList.add("drop-over");
  if (tab) { cardDrag.place(null); return; } // to the end of that column
  // Measured where the cards rest, not mid-glide, so the gap doesn't flicker.
  const cards = cardsOf(ol);
  const before = dropBefore(cards, y);
  if (ol.hidden) ol.hidden = false; // an empty column's list, to hold the gap
  cardDrag.place(ol, before || cards.at(-1)?.nextSibling || (cards.length ? null : ol.firstChild));
}

// dropCard is the move of the card let go at the point (null: none, it goes back).
function dropCard(x, y) {
  const id = cardDrag.data, slot = cardDrag.slot;
  const ol = dropTarget(x, y);
  if (!ol) return null;
  const onTab = document.elementFromPoint(x, y)?.closest?.(".pl-lanes .lane-tab");
  // Where the gap is: the cards before it in its column.
  // (In All boards, among the cards of the dragged card's board.)
  const board = byId(id)?.board_id;
  const index = onTab || slot.parentNode !== ol ? 1 << 20
    : cardsOf(ol).filter((li) => byId(li.dataset.id)?.board_id === board && li.compareDocumentPosition(slot) & Node.DOCUMENT_POSITION_FOLLOWING).length;
  return () => moveCard(id, ol.dataset.col, index).catch(fail);
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
    if (desktop.matches || e.touches.length !== 1 || cardDrag.active) return;
    const t = e.touches[0];
    if (t.clientX < SWIPE.edge || t.clientX > innerWidth - SWIPE.edge) return;
    if (e.target.closest?.("input, textarea, select, [contenteditable]:not([contenteditable=false])")) return;
    s = { x: t.clientX, y: t.clientY };
  }, { passive: true });
  board.addEventListener("touchcancel", () => { s = null; }, { passive: true });
  board.addEventListener("touchend", (e) => {
    const start = s;
    s = null;
    if (!start || cardDrag.active || desktop.matches) return;
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
  const same = shown().filter((x) => x.column === c.column && x.board_id === c.board_id).sort((a, b) => a.position - b.position);
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
  if (e.key !== " " || e.repeat || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || !pl.root || cardDrag.active) return;
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
// cards of its board the board shows: with a tag picked, it goes just before
// the one shown there, or after the last shown), on the board at once and
// then on the server. The board is drawn
// even when that is where it is: a drop leaves the card where it fell, and
// what changed during the drag waits for it.
async function moveCard(id, col, index) {
  const c = byId(id);
  if (!c) { renderBoard(); return; }
  const mates = pl.cards.filter((x) => x.board_id === c.board_id);
  const others = mates.filter((x) => x.column === col && x.id !== id).sort((a, b) => a.position - b.position);
  const seen = others.filter((x) => shown().includes(x));
  const pos = index < seen.length ? others.indexOf(seen[index])
    : seen.length ? others.indexOf(seen.at(-1)) + 1 : Math.max(0, Math.min(index, others.length));
  if (c.column === col) {
    const now = mates.filter((x) => x.column === col).sort((a, b) => a.position - b.position).findIndex((x) => x.id === id);
    if (now === pos) { renderBoard(); return; }
  }
  const from = c.column;
  others.splice(pos, 0, c);
  others.forEach((x, i) => { x.position = i; });
  c.column = col;
  mates.filter((x) => x.column === from && x.id !== id).sort((a, b) => a.position - b.position).forEach((x, i) => { x.position = i; });
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
    const ta = h("textarea", { class: "pl-compose-text", rows: 1,
      "aria-label": `New card in ${COLUMN_TITLE[col]}`, "aria-describedby": "pl-compose-help" });
    const c = { col, ta, busy: null };
    // Write a feature brief: what was typed is the idea, and the guide
    // makes the card (in Ideas) when it saves it. Pressing it keeps the
    // focus in the text, so leaving the composer doesn't add the card first.
    const brief = pl.hooks.claudeNeeded?.() || (pl.hooks.featureBrief ? ib({ type: "button", class: "link pl-compose-brief",
      onmousedown: (e) => e.preventDefault(),
      onclick: () => {
        if (c.busy) return;
        const idea = ta.value.trim();
        closeComposer(false);
        pl.hooks.featureBrief({ project: pl.filter, idea });
      } }, "spark", "Write a feature brief") : null);
    const li = h("li", { class: "pl-card pl-composer" },
      h("div", { class: "todo t-none" },
        h("span", { class: "todo-main" }, ta,
          h("span", { class: "sr-only", id: "pl-compose-help" }, "Enter adds the card, Shift+Enter starts a new line, Escape cancels."))),
      brief);
    c.li = li;
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
      const card = await call("POST", cardAPI(), { title, description, column: c.col, project: pl.filter || undefined, board: allBoards() ? undefined : boardShown() || undefined, ...newCardFilters() });
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

const cardPane = detailPane({
  pane: paneEl,
  closeBtn: () => $("#pd-close"),
  background: () => pl.hooks.background?.(),
  isOpen: () => !!pl.open,
  rowFor: (id) => $(`.pl-card[data-id="${id}"] .pl-open`, pl.root),
  onHidden: () => pl.root.classList.remove("card-open"),
  onViewport: () => renderCard(),
});

export async function openCard(id) {
  const pane = paneEl();
  if (pl.open === id && !pane.hidden) return;
  pl.open = id;
  pl.detail = byId(id) || null;
  pl.error = "";
  pl.editing = false;
  pl.desc = null;
  pl.root.classList.add("card-open");
  cardPane.open();
  markSelected();
  renderCard();
  $("#pd-body").scrollTop = 0;
  // On a desktop the board narrows beside the pane: the card opened stays in sight.
  if (desktop.matches) requestAnimationFrame(() => $(".pl-card .todo.selected", pl.root)?.scrollIntoView({ block: "nearest", inline: "nearest" }));
  changed("push");
  await loadCard(id);
}

async function loadCard(id) {
  try {
    const c = await call("GET", cardAPI(id));
    if (pl.open !== id) return;
    pl.detail = c;
    pl.error = "";
    // A card opens on its board (a link to it, a card it depends on).
    if (c.board_id && !pl.boards.length) pl.boards = await call("GET", boardAPI());
    if (pl.open !== id) return;
    if (c.board_id && !allBoards() && c.board_id !== boardShown()) {
      saveBoard(c.board_id);
      changed("replace");
      renderBoards();
      loadPlanning().catch(fail);
    }
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
  const id = pl.open;
  pl.open = pl.detail = null;
  pl.editing = false;
  changed("close");
  markSelected();
  cardPane.close(id);
}
export const cardOpen = () => !!pl.open;
// The open card's id (null: none), and why it couldn't be read ("" if it was).
export const cardId = () => pl.open;
export const cardError = () => pl.error;
// newCardFilters are what a card added on the board takes from the filters,
// so that it shows: the tags and target picked, and the user, with Mine on.
function newCardFilters() {
  const me = meId();
  return { tags: pl.tags.length ? pl.tags : undefined, target: pl.target || undefined, members: pl.mine && me ? [me] : undefined };
}

// planningFilters are the board, project, tags, target and Mine Planning
// shows the cards of ("": the default board; "", [] and false: all).
export const planningFilters = () => ({ board: pl.board, project: pl.filter, tags: pl.tags, target: pl.target, mine: pl.mine });
// setPlanningFilters shows the cards of board ("": the default one), of
// project, with all of tags, of target and (mine) of the user (a link's:
// router.js, planning-page.js).
export function setPlanningFilters(project, tags = [], target = "", mine = false, board = "") {
  const same = project === pl.filter && target === pl.target && mine === pl.mine && board === pl.board &&
    tags.length === pl.tags.length && tags.every((t, i) => t === pl.tags[i]);
  if (same) return;
  const reload = project !== pl.filter || board !== pl.board;
  if (board !== pl.board && board !== BOARDS_ALL && pl.open && pl.detail?.board_id && pl.detail.board_id !== (board || defaultBoard()?.id)) closeCard();
  saveBoard(board);
  pl.filter = project;
  pl.tags = tags;
  pl.target = target;
  pl.mine = mine;
  store.set("dash.planning.project", project);
  store.setJSON("dash.planning.tags", tags);
  store.remove("dash.planning.tag");
  store.set("dash.planning.target", target);
  store.setFlag("dash.planning.mine", mine);
  if (!pl.root) return;
  renderFilter();
  renderBoards();
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

// saveText saves a card's title or description (key), edited on the card as
// it was at version, when the field was base. If someone changed the card
// meanwhile (409) but not this field, it saves again on the new version; if
// they changed this field, the planner picks: keep theirs (reload) or keep
// mine. It answers the card's new view, or null when theirs stays.
async function saveText(id, key, text, version, base) {
  for (;;) {
    try {
      const v = await call("PATCH", cardAPI(id), { [key]: text, version });
      applyCard(v);
      return v;
    } catch (e) {
      if (e.status !== 409) throw e;
    }
    const now = await call("GET", cardAPI(id));
    const theirs = now[key] || "";
    if (theirs !== base && theirs !== text && !await keepMine(key, theirs)) {
      if (pl.desc?.id === id && key === "description") pl.desc = null; // the box shows theirs
      applyCard(now);
      return null;
    }
    version = now.version;
    base = theirs;
  }
}

// keepMine asks whether the planner's text replaces the one someone else
// just saved in the card's field key.
async function keepMine(key, theirs) {
  const what = key === "title" ? "title" : "description";
  const { closed } = openSheet({ title: `Someone else changed the ${what}`, cls: "pl-sheet",
    body: [
      h("p", {}, `They saved this ${what} while you were editing. Keep theirs (your text is dropped) or keep yours (theirs is replaced).`),
      h("textarea", { class: "pl-desc-edit", rows: key === "title" ? 1 : 6, readonly: true, "aria-label": `Their ${what}` }, theirs),
    ],
    footer: [
      ib({ class: "btn", value: "reload" }, "reload", "Reload theirs"),
      ib({ class: "btn primary", value: "keep" }, "check", "Keep mine"),
    ] });
  return (await closed) === "keep";
}

// moveToBoard moves card c to another board, to the end of its column
// there. It stays open; the board shown loses it.
async function moveToBoard(c, board) {
  const v = await call("PATCH", cardAPI(c.id), { board });
  if (pl.open === c.id) { pl.detail = { ...pl.detail, ...v }; renderCard(); }
  pl.cards = pl.cards.filter((x) => x.id !== c.id);
  if (allBoards() || v.board_id === boardShown()) pl.cards.push(v);
  renderBoard();
  toast(`Moved to ${v.board_name || "another board"}`);
  loadPlanning().catch(() => {});
}

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

// filesSection is the card's files, when it has some (or some on their way):
// a picture's thumbnail or a file's extension, each opening its preview,
// with × to take it off the card. The Add row's File adds one.
function filesSection(c) {
  const files = c.files || [];
  const n = pl.uploading[c.id] || 0;
  if (!files.length && !n) return null;
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
      n ? h("span", { class: "chip uploading" }, h("span", { class: "spinner", "aria-hidden": "true" }), `Uploading ${n}…`) : null));
}

// addRow is the small Add actions for what the card has none of yet, or
// more of: a file, a card it depends on. Their sections show once there is
// something in them.
function addRow(c) {
  const input = h("input", { type: "file", accept: ACCEPT, multiple: true, hidden: true,
    onchange: () => { attachFiles(c.id, input.files); input.value = ""; } });
  return h("div", { class: "pl-add-row" },
    ib({ type: "button", class: "btn small pl-file-add", onclick: () => input.click() }, "plus", "File"),
    ib({ type: "button", class: "btn small pl-dep-add", onclick: attempt(() => depSheet(c)) }, "plus", "Dependency"),
    input);
}

// prop is a row of the card's properties: its name, and the control that
// changes it. group names a control that is a group of them, not a field.
function prop(label, control, group = "") {
  if (!group) control.id ||= `pd-${label.toLowerCase()}`;
  return h("div", { class: "pl-prop" },
    group ? h("span", { class: "pl-prop-name", id: group }, label) : h("label", { class: "pl-prop-name", for: control.id }, label),
    control);
}

// membersField is the card's members, each with × to take them off, the
// owner marked Owner, and (for who may change the owner) a star on each:
// filled on the owner, a click makes them the owner (on the owner: none).
// A list of the org's other members adds one.
function membersField(c) {
  const ids = c.members || [];
  const set = (members) => patchCard({ members })().catch(fail);
  const others = pl.people.filter((u) => !ids.includes(u.id));
  // Who may delete the card may change its owner (the server checks too).
  const me = pl.hooks.me?.();
  const mayOwn = me && (me.admin || me.id === c.created_by || me.id === c.owner);
  return prop("Members", h("div", { class: "pl-member-list pl-members-field", role: "group", "aria-labelledby": "pd-members" },
    ...(c.people || []).map((p) => h("span", { class: `pl-member${p.id === c.owner ? " pl-owner" : ""}` },
      face(p, p.id === c.owner ? "pl-owner-face" : ""), h("span", { class: "pl-member-name" }, p.username),
      p.id === c.owner ? h("span", { class: "pl-owner-badge" }, "Owner") : null,
      mayOwn ? ib({ type: "button", class: "icon-btn small pl-owner-btn", "aria-pressed": String(p.id === c.owner),
        "aria-label": `Owner: ${p.username}`,
        title: p.id === c.owner ? "No owner" : "Make owner",
        onclick: () => patchCard({ owner: p.id === c.owner ? 0 : p.id })().catch(fail) }, "star") : null,
      ib({ type: "button", class: "icon-btn small", "aria-label": `Take ${p.username} off the card`, title: "Remove",
        disabled: p.id === c.owner && !mayOwn,
        onclick: () => set(ids.filter((id) => id !== p.id)) }, "x"))),
    me?.id && !ids.includes(me.id) ? h("button", { type: "button", class: "pl-join-member", onclick: () => set([...ids, me.id]) },
      h("span", { class: "avatar pl-join", "aria-hidden": "true" }, ico("plus")), h("span", { class: "pl-member-name" }, "Join")) : null,
    others.filter((u) => u.id !== me?.id).length ? h("select", { "aria-label": "Add a member", onchange: (e) => e.target.value && set([...ids, Number(e.target.value)]) },
      h("option", { value: "" }, "Add…"),
      ...others.filter((u) => u.id !== me?.id).map((u) => h("option", { value: String(u.id) }, u.username))) : null), "pd-members");
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
    placeholder: "Add…",
    onkeydown: (e) => {
      if (e.key === "Enter" || e.key === ",") { e.preventDefault(); add(e.target); }
      else if (e.key === "Backspace" && !e.target.value && tags.length) set(tags.slice(0, -1));
    },
    onchange: (e) => { if (e.target.value.trim()) add(e.target); } });
  return prop("Tags", h("div", { class: "pl-member-list pl-tag-list pl-tags-field", role: "group", "aria-labelledby": "pd-tags" },
    ...tags.map((t) => tagChip(t, "span", { class: tagPicked(t) ? "pl-tag-on" : "" },
      h("button", { type: "button", class: "pl-tag-name", "aria-pressed": String(tagPicked(t)),
        "aria-label": `Filter by ${t}`, onclick: () => pickTag(t, !tagPicked(t)) }, t),
      ib({ type: "button", class: "icon-btn small", "aria-label": `Remove the tag ${t}`, title: "Remove", onclick: () => set(tags.filter((x) => x !== t)) }, "x"))),
    tags.length < 20 ? input : null), "pd-tags");
}

// ---- the description: shown as Markdown; a click on it (or Edit) makes it
// a text box, which leaving or Ctrl/Cmd+Enter saves and Esc cancels. Empty,
// it is the box. pl.desc is the description's state while the card is open:
// { id, el (the box, or null), saved (the text last loaded or saved),
// version (the card's then, sent so the save is refused if someone else
// changed the card since), editing, saving (the save on its way) }.

// A press in progress (pointerdown, not up yet): what leaving the box
// redraws waits for it, so the click it is part of lands where it was aimed.
let pressing = false;
document.addEventListener("pointerdown", () => { pressing = true; }, true);
document.addEventListener("pointerup", () => { pressing = false; }, true);
document.addEventListener("pointercancel", () => { pressing = false; }, true);
const afterPress = (fn) => pressing ? document.addEventListener("click", () => setTimeout(fn), { once: true, capture: true }) : fn();

// redrawDesc draws the description's section again, alone.
function redrawDesc() {
  const sec = $("#pd-desc-sec"), c = pl.detail;
  if (sec && c) sec.replaceWith(descSection(c));
}

function editDesc() {
  const d = pl.desc;
  if (!d || pl.open !== d.id) return;
  d.editing = true;
  redrawDesc();
  const el = pl.desc?.el;
  el?.focus();
  el?.setSelectionRange(el.value.length, el.value.length);
}

function descBox(d) {
  const el = h("textarea", { class: "pl-desc-edit", id: "pd-desc", rows: 4, maxlength: 20000, "aria-label": "Description" }, d.saved);
  // finish saves the text if it changed, then shows it rendered.
  const finish = () => {
    const d = pl.desc;
    if (d?.el !== el || d.saving) return;
    const text = el.value;
    if (text.trim() === d.saved.trim()) { d.editing = false; afterPress(() => { if (pl.desc?.el === el && document.activeElement !== el) redrawDesc(); }); return; }
    d.saving = saveText(d.id, "description", text, d.version, d.saved).then((v) => {
      if (v && pl.desc?.el === el) Object.assign(pl.desc, { saved: text, version: v.version, editing: false });
    }).catch(fail).finally(() => {
      if (pl.desc?.el !== el) return;
      pl.desc.saving = null;
      if (document.activeElement !== el) afterPress(redrawDesc);
    });
  };
  // Not when the window, rather than the box, loses focus.
  // Nor when a redraw moves the box (descSection gives it its focus back):
  // Chrome fires both change and blur on a moved box that was typed in, and a
  // save started then would make the Esc that follows do nothing.
  el.addEventListener("change", () => { if (!pl.desc?.moving) finish(); });
  el.addEventListener("blur", () => { if (document.hasFocus() && !pl.desc?.moving) finish(); });
  el.addEventListener("paste", pasteFiles);
  el.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      finish(); // before the blur, so it doesn't hang on whether the blur calls it
      el.blur();
      const d = pl.desc;
      if (d?.el === el && !d.saving) { d.el = null; redrawDesc(); } // unchanged: shown rendered now
    }
    else if (e.key === "Escape" && !e.isComposing) {
      e.preventDefault();
      e.stopPropagation(); // not the pane's Esc (close)
      const d = pl.desc;
      if (d?.el !== el || d.saving) return;
      el.value = d.saved;
      d.editing = false;
      d.el = null; // let go of the box, so the redraw shows the text rendered whatever the focus
      redrawDesc();
      $("#pd-desc-sec .pl-desc-edit-btn")?.focus();
    }
  });
  d.el = el;
  return el;
}

// descSection is the description: the box being typed in stays across
// redraws of the card (while it has focus, unsaved text or a save on its
// way), so a change from elsewhere doesn't take what is being typed.
function descSection(c) {
  let d = pl.desc;
  if (d?.id !== c.id) d = pl.desc = { id: c.id, el: null, saved: c.description || "", version: c.version, editing: false, saving: null };
  const focused = d.el && (document.activeElement === d.el || d.moving);
  const keep = d.el && (focused || d.el.value !== d.saved || d.saving);
  if (focused) {
    // Moving the box into the new section takes its focus: give it back once
    // the section is in place, and don't take the blur for leaving the box.
    const el = d.el;
    d.moving = true;
    queueMicrotask(() => {
      if (pl.desc !== d) return;
      d.moving = false;
      if (d.el === el && el.isConnected && document.activeElement !== el) el.focus({ preventScroll: true });
    });
  }
  if (!keep) {
    d.saved = c.description || "";
    d.version = c.version;
    d.el = null;
  }
  const boxed = keep || d.editing || !d.saved.trim();
  const view = keep ? d.el : boxed ? descBox(d) : h("div", { class: "md pl-desc", id: "pd-desc", innerHTML: renderMarkdown(d.saved),
    onclick: (e) => { if (!e.target.closest("a, button, input, summary") && !getSelection()?.toString()) editDesc(); } });
  return h("section", { class: "pl-section pl-desc-sec", id: "pd-desc-sec", "aria-labelledby": "pd-desc-h" },
    h("h3", { class: "pl-section-head" }, h("span", { id: "pd-desc-h" }, "Description"),
      boxed ? null : ib({ type: "button", class: "icon-btn small pl-desc-edit-btn", "aria-label": "Edit the description", title: "Edit", onclick: editDesc }, "edit")),
    view);
}

// ---- the card's head: its title (a click edits it), the column it is in
// (a menu moves it), its status, and its actions.

// renderTitle shows the title, a button that edits it in place, unless it
// is being edited.
function renderTitle(c) {
  const el = $("#pd-title");
  if (pl.editing && el.contains($("#pd-edit-title"))) return;
  pl.editing = false;
  el.classList.add("editable");
  fill(el, h("button", { type: "button", class: "title-edit", onclick: editTitle }, c.title || ""));
}

// editTitle makes the title a box: Enter (or leaving it) saves, Esc cancels.
function editTitle() {
  const c = pl.detail;
  if (!c || pl.editing) return;
  pl.editing = true;
  const { id, version } = c, base = c.title || "";
  const el = $("#pd-title");
  const t = h("input", { id: "pd-edit-title", class: "title-input", value: base, maxlength: 200, autocomplete: "off", "aria-label": "Title" });
  let done = false;
  const end = (refocus) => {
    if (pl.open !== id) return;
    pl.editing = false;
    if (pl.detail) renderTitle(pl.detail);
    if (refocus) $("#pd-title .title-edit")?.focus();
  };
  const save = async (refocus) => {
    if (done) return;
    done = true;
    const v = t.value.trim();
    if (v && v !== base) {
      t.readOnly = true;
      try { await saveText(id, "title", v, version, base); } catch (e) { fail(e); }
    }
    end(refocus);
  };
  t.addEventListener("keydown", (e) => {
    if (e.isComposing) return;
    if (e.key === "Enter") { e.preventDefault(); save(true); }
    else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); done = true; end(true); }
  });
  t.addEventListener("blur", () => { if (document.hasFocus()) save(false); });
  el.classList.remove("editable");
  fill(el, t);
  t.focus();
  t.select();
}

// columnPicker is the column the card is in, a button whose menu moves it
// to another.
function columnPicker(c) {
  const col = COLUMNS.find((x) => x.key === c.column);
  const name = col?.title || c.column;
  return h("button", { type: "button", class: `pl-col-tag s-${col?.s || "draft"}`, id: "pd-column", "aria-haspopup": "menu", "aria-expanded": "false", "aria-label": `Column: ${name}`,
    onclick: (e) => popMenu(e.currentTarget, [COLUMNS.map((x) => [x.key === c.column ? "check" : "right", x.title,
      () => x.key !== c.column && moveCard(c.id, x.key, 1 << 20), { title: x.key === c.column ? "" : `Move to ${x.title}` }])], { heading: "Column" }) },
    h("span", {}, name), ico("down"));
}

// actionsEl is the card's actions: Start… (the main one), Write a feature
// brief, and the rest in a menu. Without Claude for the user
// (hooks.claudeNeeded: a planner without a runner online), what it needs
// shows in place of the brief, and Ask in chat isn't offered.
function actionsEl(c) {
  const needed = pl.hooks.claudeNeeded?.();
  const more = [
    pl.planner ? null : ["link", "Link existing…", () => linkExistingSheet(c)],
    !needed && pl.hooks.askChat ? ["comment", "Ask in chat", () => pl.hooks.askChat(c)] : null,
  ].filter(Boolean);
  const moves = COLUMNS.filter((x) => x.key !== c.column).map((x) => ["right", `Move to ${x.title}`, () => moveCard(c.id, x.key, 1 << 20)]);
  return [
    ib({ type: "button", class: "btn small primary", id: "pd-start", onclick: attempt(() => startSheet(c)) }, "play", "Start…"),
    needed || (pl.hooks.featureBrief ? ib({ type: "button", class: "btn small", id: "pd-brief",
      onclick: () => pl.hooks.featureBrief({ card: c, project: pl.filter }) }, "spark", "Write a feature brief") : null),
    ib({ type: "button", class: "icon-btn pl-more-btn", id: "pd-more", "aria-label": "More actions", title: "More", "aria-haspopup": "menu", "aria-expanded": "false",
      onclick: (e) => popMenu(e.currentTarget, [more, moves]) }, "more"),
  ];
}

// claudeChanged shows the open card's Claude buttons again, after whether
// Claude can run for the user changed (hooks.claudeNeeded).
export function claudeChanged() { renderCard(); }

// renderCard draws the open card: its head, then the description first
// (on a phone it shows without scrolling), its properties, the sections
// with something in them, the Add row, the comments.
function renderCard() {
  const c = pl.detail;
  if (!c || paneEl().hidden) return;
  renderTitle(c);
  fill($("#pd-meta"),
    columnPicker(c),
    c.status ? statusEl(c.status) : null,
    c.created_at ? h("span", { title: dateTimeText(c.created_at) }, `added ${dateText(c.created_at)}`) : null);
  fill($("#pd-actions"), ...actionsEl(c));
  const body = $("#pd-body");
  if (pl.error) { fill(body, h("p", { class: "error-note" }, pl.error)); return; }
  const keep = body.scrollTop;
  fill(body, descSection(c), propsEl(c), workSection(c), depsSection(c), filesSection(c), addRow(c),
    commentsSection(c), dangerEl(c));
  body.scrollTop = keep;
}

// propsEl is the card's properties, compact: board, priority, target,
// project, members and tags. (The column is the head's picker.)
function propsEl(c) {
  const sel = (opts, value, onchange) => h("select", { onchange }, ...opts.map(([v, t]) => h("option", { value: v, selected: v === value }, t)));
  return h("div", { class: "pl-props" },
    pl.boards.length > 1 ? prop("Board", sel(pl.boards.map((b) => [b.id, b.name]), c.board_id, (e) => moveToBoard(c, e.target.value).catch(fail))) : null,
    prop("Priority", sel([["", "None"], ["high", "High"], ["medium", "Medium"], ["low", "Low"]], c.priority || "", (e) => patchCard({ priority: e.target.value })().catch(fail))),
    prop("Target", h("input", { value: c.target || "", maxlength: 60, autocomplete: "off",
      onchange: (e) => patchCard({ target: e.target.value })().catch(fail) })),
    prop("Project", sel([["", "None"], ...pl.projects.map((p) => [p.id, p.name])], c.project_id || "",
      (e) => patchCard({ project: e.target.value })().catch(fail))),
    membersField(c),
    tagsField(c));
}

// workSection lists the tasks and goals started for the card, when there are some.
function workSection(c) {
  const links = c.links || [];
  if (!links.length) return null;
  return h("section", { class: "pl-section", "aria-labelledby": "pd-work" },
    h("h3", { id: "pd-work" }, "Work"),
    h("ul", { class: "pl-links" }, ...links.map((l) => h("li", { class: `pl-link t-${l.tone}` },
      h("span", { class: "pl-link-kind", title: l.kind === "goal" ? "Goal" : "Task" }, ico(l.kind === "goal" ? "target" : "checks")),
      h("span", { class: "pl-link-main" },
        !pl.planner && l.id && pl.hooks.openWork
          ? h("button", { type: "button", class: "link pl-link-title", onclick: () => pl.hooks.openWork(l) }, l.title)
          : h("span", { class: "pl-link-title" }, l.title),
        h("span", { class: "pl-link-sub" }, l.project ? `${l.kind === "goal" ? "Goal" : "Task"} in ${l.project}` : l.kind === "goal" ? "Goal" : "Task")),
      h("span", { class: `pl-state t-${l.tone}` }, l.state),
      !pl.planner && l.id ? ib({ type: "button", class: "icon-btn small", "aria-label": `Unlink ${l.title}`, title: "Unlink",
        onclick: attempt((e) => busy(e.currentTarget, () => unlink(c, l))) }, "x") : null))));
}

// commentsSection is the card's discussion, each comment in Markdown with
// how long ago it was written (the date on hover), and the box to add one.
// Its author may delete a comment (the icon shows on hover or focus).
function commentsSection(c) {
  const comments = c.discussion || [];
  const me = pl.hooks.me?.();
  const box = h("textarea", { rows: 3, placeholder: "Add a comment", "aria-label": "Comment", maxlength: 10000, onpaste: pasteFiles });
  return h("section", { class: "pl-section", "aria-labelledby": "pd-comments" },
    h("h3", { id: "pd-comments" }, "Comments"),
    comments.length ? h("ol", { class: "pl-comments-list" }, ...comments.map((m) => h("li", { class: `pl-comment${m.dash ? " pl-comment-dash" : ""}` },
      h("div", { class: "pl-comment-head" },
        h("strong", {}, m.dash ? "Dash" : m.author_name || "Someone"),
        h("time", { datetime: m.created_at, title: dateTimeText(m.created_at) }, agoText(m.created_at)),
        me && m.author === me.id ? ib({ type: "button", class: "icon-btn small pl-comment-del", "aria-label": "Delete comment", title: "Delete",
          onclick: attempt(async () => { if (await ask({ title: "Delete this comment?", ok: "Delete", danger: true })) await call("DELETE", cardAPI(c.id, `/comments/${m.id}`)); await loadCard(c.id); }) }, "trash") : null),
      h("div", { class: "md pl-comment-text", innerHTML: renderMarkdown(m.text || "") })))) : null,
    h("form", { class: "pl-comment-form", onsubmit: attempt(async (e) => {
      e.preventDefault();
      const text = box.value.trim();
      if (!text) return;
      await busy(e.submitter || $("button", e.currentTarget), () => call("POST", cardAPI(c.id, "/comments"), { text }));
      box.value = "";
      await loadCard(c.id);
    }) }, box, h("div", { class: "pl-comment-actions" },
      dictation(box, { place: () => {}, cls: "icon-btn small", send: () => !!box.value.trim() && (box.form.requestSubmit(), true) }),
      ib({ type: "submit", class: "btn small" }, "comment", "Comment"))));
}

// dangerEl is the card's Delete button: its creator, its owner or an admin
// deletes a card (the server checks too).
function dangerEl(c) {
  const me = pl.hooks.me?.();
  if (!me || !(me.admin || me.id === c.created_by || me.id === c.owner)) return null;
  return h("div", { class: "pl-danger" },
    ib({ type: "button", class: "btn small danger", onclick: attempt(async () => {
      if (!await ask({ title: "Delete this card?", body: deleteCardText(c), ok: "Delete", danger: true })) return;
      await call("DELETE", cardAPI(c.id));
      pl.cards = pl.cards.filter((x) => x.id !== c.id);
      closeCard();
      renderBoard();
      toast("Card deleted");
    }) }, "trash", "Delete card…"));
}

// deleteCardText is the confirmation for deleting c: it names the work that
// loses its card and the cards that lose their dependency on it.
function deleteCardText(c) {
  const quote = (xs) => xs.map((x) => `“${x}”`).join(", ");
  const work = (c.links || []).filter((l) => !l.removed).map((l) => l.title);
  const needed = (c.needed_by || []).map((d) => d.title);
  const lines = ["Its comments and files go too. This can't be undone."];
  if (work.length) lines.push(`${work.length === 1 ? "This task or goal stays, but is" : "These tasks and goals stay, but are"} no longer on a card: ${quote(work)}.`);
  if (needed.length) lines.push(`${needed.length === 1 ? "This card no longer depends" : "These cards no longer depend"} on it: ${quote(needed)}.`);
  return lines.join(" ");
}

// ---- dependencies: the cards to be done before this one, and those that
// wait for it. A card is done in the Done column.

// cardRow is a card a card depends on (or is needed by), which opens it;
// remove, if given, takes it off.
function cardRow(d, remove) {
  return h("li", { class: `pl-link t-${d.done ? "done" : "idle"}` },
    h("span", { class: "pl-link-kind", title: "Card" }, ico("plan")),
    h("span", { class: "pl-link-main" },
      h("button", { type: "button", class: "link pl-link-title", onclick: () => openCard(d.id).catch(fail) }, d.title),
      h("span", { class: "pl-link-sub" }, [COLUMN_TITLE[d.column] || d.column, d.board && pl.boards.length > 1 && d.board !== pl.detail?.board_name ? `on ${d.board}` : ""].filter(Boolean).join(" · "))),
    h("span", { class: `pl-state t-${d.done ? "done" : "idle"}` }, d.done ? "Done" : "Not done yet"),
    remove ? ib({ type: "button", class: "icon-btn small", "aria-label": `Remove ${d.title}`, title: "Remove",
      onclick: attempt((e) => busy(e.currentTarget, remove)) }, "x") : null);
}

// depsSection is the cards this one depends on, and those that wait for
// it, each when there are some. The Add row's Dependency adds one.
function depsSection(c) {
  const deps = c.deps || [];
  const ids = deps.map((d) => d.id);
  const needed = c.needed_by || [];
  return [
    deps.length ? h("section", { class: "pl-section", "aria-labelledby": "pd-deps" },
      h("h3", { id: "pd-deps" }, "Depends on"),
      h("ul", { class: "pl-links" }, ...deps.map((d) => cardRow(d, () => patchCard({ depends_on: ids.filter((x) => x !== d.id) })())))) : null,
    needed.length ? h("section", { class: "pl-section", "aria-labelledby": "pd-needed" },
      h("h3", { id: "pd-needed" }, "Needed by"),
      h("ul", { class: "pl-links" }, ...needed.map((d) => cardRow(d)))) : null,
  ];
}

// depSheet lists the org's cards, of every board, to add one this card
// depends on. Those it depends on already, and those that depend on it (by
// way of others too), can't be picked.
async function depSheet(c) {
  const cards = await call("GET", cardAPI("", "?board=all"));
  const many = new Set(cards.map((x) => x.board_id)).size > 1;
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
            h("span", { class: "pl-link-sub" }, [many ? x.board_name : "", COLUMN_TITLE[x.column], x.project_name, why].filter(Boolean).join(" · ")))));
    }) : [h("li", { class: "hint" }, cards.length > 1 ? "No card matches." : "There are no other cards yet.")]));
  };
  q.addEventListener("input", render);
  render();
  const done = sheet("Depends on", [q, list], null);
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

// startSheet asks for the project, whether to make a task or a goal, and
// its text (the card's title and description to begin with), then starts
// it: a member's task as a draft or queued, as they pick. A planner's goal
// also needs its owner, a developer: its coordinator runs as them and they
// accept its plan (the card's first developer is suggested). A planner's
// task runs for a developer too (Runs for), on their runners, as a draft
// they queue.
async function startSheet(c) {
  if (!pl.projects.length) await loadPlanning();
  if (!pl.projects.length) throw new Error("There are no projects to start work in yet. Planning starts work in shared projects only, not in a runner's own project. Ask a developer to add a shared one with + Add project, from GitHub or a URL.");
  // Dash doesn't stop either, but says so: work already under way for the
  // card (a second Start makes the same work again), and cards it waits for.
  const open = (c.links || []).filter((l) => !l.removed && !["done", "shipped", "live"].includes(l.tone));
  const waits = (c.deps || []).filter((d) => !d.done).map((d) => d.title);
  const kinds = new Set(open.map((l) => l.kind));
  const noun = kinds.size > 1 ? "tasks or goals" : `${kinds.has("goal") ? "goal" : "task"}${open.length === 1 ? "" : "s"}`;
  const devs = pl.people.filter((u) => !u.planner);
  const onCard = (c.people || []).map((p) => p.id);
  const suggested = devs.find((u) => onCard.includes(u.id));
  const devOptions = (selected) => [
    h("option", { value: "", selected: !selected }, devs.length ? "Choose a developer" : "No developers in the organization"),
    ...devs.map((u) => h("option", { value: String(u.id), selected: u === selected }, u.username))];
  const owner = pl.planner ? h("label", { class: "field", hidden: true }, h("span", {}, "Owner"), h("select", { name: "owner" }, ...devOptions(suggested))) : null;
  const runFor = pl.planner ? h("label", { class: "field" }, h("span", {}, "Runs for"),
    h("select", { name: "run_by", required: true }, ...devOptions(suggested || devs[0]))) : null;
  // The text: a task's prompt is the title and the description; a goal's
  // description is the card's. Switching kinds swaps it, unless edited.
  const texts = { task: c.title + (c.description ? `\n\n${c.description}` : ""), goal: c.description || c.title };
  const text = h("textarea", { name: "prompt", class: "pl-start-text", rows: 8, maxlength: 20000, required: true, id: "pl-start-text" }, texts.task);
  const textLabel = h("label", { for: "pl-start-text" }, "Prompt");
  const draftBtn = ib({ class: "btn primary", value: "draft" }, "draft", "Create draft");
  const queueBtn = pl.planner ? null : ib({ class: "btn", value: "queue" }, "queue", "Queue task");
  const goalBtn = ib({ class: "btn primary", value: "goal", hidden: true }, "target", pl.planner ? "Create goal" : "Start goal");
  const kindChanged = (e) => {
    const kind = e.target.value, goal = kind === "goal";
    if (text.value === texts[goal ? "task" : "goal"]) text.value = texts[kind];
    textLabel.textContent = goal ? "Description" : "Prompt";
    draftBtn.hidden = goal;
    if (queueBtn) queueBtn.hidden = goal;
    goalBtn.hidden = !goal;
    if (!owner) return;
    owner.hidden = !goal;
    owner.querySelector("select").required = goal;
    runFor.hidden = goal;
    runFor.querySelector("select").required = !goal;
  };
  const { form, closed } = openSheet({ title: "Start this card", cls: "pl-sheet pl-start-sheet", body: [
    open.length ? h("p", { class: "hint warn" }, ico("alert"), ` Already ${open.length} ${noun} in progress.`) : null,
    waits.length ? h("p", { class: "hint warn" }, ico("lock"), ` Waits for: ${waits.join(", ")} (not done).`) : null,
    h("label", { class: "field" }, h("span", {}, "Project"),
      h("select", { name: "project", required: true },
        ...pl.projects.map((p) => h("option", { value: p.id, selected: p.id === (c.project_id || pl.filter) }, p.name)))),
    runFor,
    h("fieldset", { class: "field choice pl-start-kind" },
      h("legend", {}, "Make"),
      h("label", { class: "radio" }, h("input", { type: "radio", name: "kind", value: "task", checked: true, onchange: kindChanged }), h("span", {}, "Task")),
      h("label", { class: "radio" }, h("input", { type: "radio", name: "kind", value: "goal", onchange: kindChanged }), h("span", {}, "Goal"))),
    owner,
    h("div", { class: "field" }, textLabel, text),
  ], footer: [queueBtn, draftBtn, goalBtn] });
  const how = await closed;
  if (!["draft", "queue", "goal"].includes(how)) return;
  const data = new FormData(form);
  const body = { project: data.get("project"), kind: data.get("kind"), prompt: data.get("prompt"), draft: how === "draft" };
  if (body.kind === "goal" && data.get("owner")) body.owner = Number(data.get("owner"));
  if (body.kind !== "goal" && data.get("run_by")) body.run_by = Number(data.get("run_by"));
  const v = await call("POST", cardAPI(c.id, "/start"), body);
  toast(body.kind === "goal" ? "Goal started and linked" : how === "queue" ? "Task queued and linked" : "Draft created and linked", { tone: "ok" });
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
  const [cards, linked] = await Promise.all([call("GET", cardAPI("", "?board=all")), call("GET", `/api/planning/linked/${kind}/${encodeURIComponent(id)}`)]);
  const many = new Set(cards.map((c) => c.board_id)).size > 1;
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
          h("span", { class: "pl-link-sub" }, [many ? c.board_name : "", COLUMN_TITLE[c.column], c.project_name, on.has(c.id) ? "linked" : ""].filter(Boolean).join(" · "))))))
      : [h("li", { class: "hint" }, cards.length ? "No card matches." : "No cards yet.")]));
  };
  q.addEventListener("input", render);
  render();
  const done = sheet(`Link this ${kind} to a card`, [q, list], null);
  q.focus();
  await done;
  if (!picked) return;
  await call("POST", cardAPI(picked.id, "/links"), { kind, id });
  toast(`Linked to “${picked.title}”`, { tone: "ok" });
}

