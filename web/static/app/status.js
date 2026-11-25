import { $, h, plural } from "./dom.js";

// ------------------------------------------------------------------ status
// One status icon in the top bar and a popover with the details. Each part
// of the app that has something to say (the live connection, Claude's
// status, usage, origin, GitHub's rate limit, checkouts, runners, the merge
// queue, previews, billing, sync) calls setStatus with a row, or null when
// it has nothing to say. The icon stays quiet while every row is fine and
// shows a dot, coloured by the worst row, when one needs the user.
//
// A row: { tone, label, text, title?, detail?, action?: { label, run } }.
// tone: "ok" or "info" (quiet), "warn" or "bad" (lights the dot).
// This module imports only dom.js, so any module can import it in a cycle.

// The rows in the popover, in this order.
const ORDER = ["live", "billing", "claude", "usage", "runner", "disk", "sync", "remote", "github", "checkouts", "merge", "previews", "runners"];
const RANK = { ok: 0, info: 0, warn: 1, bad: 2 };
const rows = new Map();
let queued = false;
// While a row's action runs, closing the popover doesn't move focus back to
// the icon: the action moves it (to a sheet it opens, say).
let acting = false;

export function setStatus(key, row) {
  if (row) rows.set(key, row);
  else rows.delete(key);
  if (queued) return;
  queued = true;
  queueMicrotask(() => { queued = false; render(); });
}

const pop = () => $("#status-pop");
const btn = () => $("#status-btn");
const needs = () => [...rows.values()].filter((r) => RANK[r.tone] > 0);

function render() {
  const worst = needs().reduce((w, r) => Math.max(w, RANK[r.tone]), 0);
  const n = needs().length;
  const b = btn();
  b.dataset.tone = worst === 2 ? "bad" : worst === 1 ? "warn" : "ok";
  b.dataset.live = rows.get("live")?.tone === "ok" ? "on" : "off";
  const label = n ? `Status: ${plural(n, "thing needs", "things need")} you` : "Status: all is well";
  b.setAttribute("aria-label", label);
  b.title = n ? `${label} (${needs().map((r) => r.label).join(", ")})` : label;
  $("#status-sum").textContent = n ? plural(n, "thing needs", "things need") + " you" : "All is well";
  renderList();
}

function renderList() {
  // Keep the focus on the same action across a re-render.
  const focused = document.activeElement?.closest?.("#status-list") ? document.activeElement.dataset.focusKey : "";
  const list = $("#status-list");
  list.replaceChildren(...ORDER.filter((k) => rows.has(k)).map((k) => {
    const r = rows.get(k);
    return h("li", { class: `status-row status-${r.tone}`, "data-key": k, title: r.title || "" },
      h("span", { class: "status-row-dot", "aria-hidden": "true" }),
      h("div", { class: "status-row-body" },
        h("div", { class: "status-row-head" },
          h("strong", {}, r.label),
          r.text ? h("span", { class: "status-row-text" }, r.text) : null,
          RANK[r.tone] ? h("span", { class: "sr-only" }, r.tone === "bad" ? " (needs you now)" : " (needs you)") : null),
        r.detail || null),
      r.action ? h("button", {
        type: "button", class: "btn small status-row-act", "data-focus-key": `${k}:${r.action.label}`,
        onclick: () => runAction(r.action),
      }, r.action.label) : null);
  }));
  if (focused) list.querySelector(`[data-focus-key="${CSS.escape(focused)}"]`)?.focus();
}

// runAction closes the popover, then goes where the row's fix is.
async function runAction(a) {
  acting = true;
  pop().close();
  acting = false;
  await a.run();
}

// closeStatus closes the popover (detail rows' own buttons call it before
// they open something).
export function closeStatus() {
  acting = true;
  if (pop().open) pop().close();
  acting = false;
}

export function openStatus() {
  if (!pop().open) pop().showModal();
  $("#status-title").focus();
}

btn().addEventListener("click", () => pop().open ? pop().close() : openStatus());
$("#status-close").addEventListener("click", () => pop().close());
// A click beside the popover (on its backdrop) closes it. Esc closes it as any dialog.
pop().addEventListener("click", (e) => { if (e.target === pop()) pop().close(); });
pop().addEventListener("close", () => { btn().setAttribute("aria-expanded", "false"); if (!acting) btn().focus(); });
pop().addEventListener("toggle", () => btn().setAttribute("aria-expanded", String(pop().open)));
