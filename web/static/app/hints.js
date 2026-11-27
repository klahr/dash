import { $, h } from "./dom.js";
import * as store from "./store.js";
import { state } from "./state.js";
import { myID } from "./people.js";
import { approvable, closable, waitsOnGitHub } from "./branches.js";
import { baseBranch } from "./events.js";

// ------------------------------------------------------------------ first-review tips
// The first time someone reviews a task they get three short tips, one at a
// time, each beside what it is about:
//   diff     under the Timeline · Changes tabs: what Changes shows
//   comment  above the diff's files: how to comment on a line
//   actions  above the action bar: what Rework… and Approve do
// Each shows until it is dismissed. Dismissals are kept per user in this
// browser (localStorage: there is no server-side preference store), and
// Account → General → Help (or :tips) shows them again (resetHints).

const ORDER = ["diff", "comment", "actions"];
const key = () => `dash.tips.${myID()}`;
const dismissed = () => new Set(store.getJSON(key(), []));
function dismiss(ids) {
  store.setJSON(key(), [...new Set([...dismissed(), ...ids])]);
}

// resetHints brings the tips back; an open task in Review shows the first at once.
export function resetHints() {
  store.remove(key());
  renderHints();
}

const b = (text) => h("strong", {}, text);

// The tips task t can show in the pane as it is now, by id, each with
// where it goes and what it says. A tip that can't show here waits.
function available(t) {
  const out = new Map();
  if (t?.status !== "review" || t.reviewing || closable(t)) return out;
  const base = baseBranch(state.project) || "the default branch";
  out.set("diff", {
    slot: "#d-tip-tabs",
    body: [b("Changes"), ` shows the diff: what Claude changed on the task's branch, compared to ${base}. Check `, b("Viewed"), " on a file to fold it once you've read it."],
    open: state.pane !== "diff",
  });
  // Commenting needs lines to click.
  if (state.pane === "diff" && $("#d-diff .diff-lines > div[data-ln]")) {
    out.set("comment", {
      slot: "#d-diff",
      body: ["Click a line to comment on it (tap it on a phone); shift-click another for a range. Your comments go to Claude together, with ", b("Rework…"), "."],
    });
  }
  // Rework… and Approve are both in the bar only when Dash merges the task.
  if (t.worktree && approvable(t) && !waitsOnGitHub(t)) {
    out.set("actions", {
      slot: "#d-tip-actions",
      body: [b("Rework…"), " sends the task back to Claude with your notes and comments; it goes on in the same branch and comes back here. ",
        b("Approve"), ` merges the branch into ${base}.`],
    });
  }
  return out;
}

// renderHints shows the first tip not yet dismissed that the open task can
// show, and takes away any other. Called whenever the task pane redraws its
// changes or its actions.
export function renderHints() {
  const open = $("#detail")?.hidden === false;
  const can = open ? available(state.todos.get(state.detail)) : new Map();
  const gone = can.size ? dismissed() : new Set();
  const id = ORDER.find((i) => can.has(i) && !gone.has(i));
  const tip = id && can.get(id);
  // The tip already shown stays as it is (the task redraws often while in
  // review, and its buttons may have focus); any other goes.
  let kept = null;
  for (const el of document.querySelectorAll("#detail .tip")) {
    if (tip && el.dataset.tip === id && el.parentElement === $(tip.slot) && !!el.querySelector(".tip-open") === !!tip.open) kept = el;
    else el.remove();
  }
  for (const s of ["#d-tip-tabs", "#d-tip-actions"]) { const el = $(s); if (el) el.hidden = !el.querySelector(".tip"); }
  if (!tip || kept) return kept;
  const left = ORDER.filter((i) => !gone.has(i)).length;
  const close = (ids) => (e) => {
    const hadFocus = e.currentTarget === document.activeElement;
    dismiss(ids);
    const next = renderHints();
    if (!hadFocus) return;
    // Keyboard users carry on from where the tip was.
    if (next) next.querySelector("button").focus();
    else if (!$("#d-diff").hidden) $("#d-diff").focus({ preventScroll: true });
    else $('.pane-tabs [data-pane="diff"]')?.focus();
  };
  const el = h("aside", { class: `tip tip-${id}`, "data-tip": id, "aria-label": "Review tip" },
    h("p", {}, h("span", { class: "tip-label" }, "Tip"), " ", ...tip.body),
    h("div", { class: "tip-actions" },
      h("button", { type: "button", class: "btn small", onclick: close([id]) }, "Got it"),
      tip.open ? h("button", { type: "button", class: "btn small tip-open", onclick: () => { $('.pane-tabs [data-pane="diff"]').click(); } }, "Show changes") : null,
      left > 1 ? h("button", { type: "button", class: "btn small tip-skip", onclick: close(ORDER) }, "Hide all tips") : null));
  const slot = $(tip.slot);
  if (tip.slot === "#d-diff") {
    // Above the first file, under the notes and the review progress.
    slot.insertBefore(el, slot.querySelector(".diff-file"));
  } else {
    slot.replaceChildren(el);
    slot.hidden = false;
  }
  return el;
}
