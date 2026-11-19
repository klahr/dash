import { h, ico } from "./dom.js";
import { state } from "./state.js";
import { LABEL_ANY, parseLabels, runnerLabels } from "../groups.js";

// Runner labels picked as chips (Runs on, an environment's Runs on, a runner's own
// labels): the labels that exist in the org are offered below the field,
// and a new one can still be typed (Enter, comma or space adds it). The
// picked labels live in a hidden input, comma-separated, so a form reads
// and snapshots them as before; changing them fires input and change on it.

// docsLabels links to the docs on runner labels.
export const docsLabels = (text = "How labels work") =>
  h("a", { href: "https://github.com/klahr/dash/blob/main/docs/several-computers.md#runner-labels", target: "_blank", rel: "noopener" }, text);

const lower = (l) => String(l).toLowerCase();
const isAny = (l) => lower(l) === LABEL_ANY;

// knownLabels are the labels the org's runners have and its projects run
// on, by name, each once whatever its case; with any first when wanted.
export function knownLabels({ any = false } = {}) {
  const seen = new Map();
  const add = (l) => { if (l && !isAny(l) && !seen.has(lower(l))) seen.set(lower(l), l); };
  for (const r of state.runners || []) runnerLabels(r).forEach(add);
  for (const p of state.projects || []) [...(p.runs_on || []), ...(p.environments || []).flatMap((e) => e.runs_on || [])].forEach(add);
  const list = [...seen.values()].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base", numeric: true }));
  return any ? [LABEL_ANY, ...list] : list;
}

const pickers = new WeakMap();
// live are the pickers' redraws, so their offers follow the org's labels.
const live = new Set();

// refreshLabelPickers redraws the pickers on the page (the runners or the
// projects changed, and with them the labels that exist).
export function refreshLabelPickers() {
  for (const p of live) {
    if (p.el.isConnected) p.redraw();
    else if (!p.input.isConnected) live.delete(p);
  }
}

// labelPicker builds a picker. input is a form's field to take over (made
// hidden, the picker put after it), or none for one of its own. any offers
// "any"; exclude are labels never offered (a runner's own system, say);
// label names the field for screen readers; onChange gets the labels.
export function labelPicker({ input, value, any = false, exclude = [], label = "Labels", placeholder = "Add a label", onChange } = {}) {
  input ||= h("input", { type: "hidden" });
  input.type = "hidden";
  let labels = parseLabels(value ?? input.value);
  input.value = labels.join(", ");
  const chips = h("span", { class: "label-chips" });
  const text = h("input", { type: "text", class: "label-picker-input", placeholder, autocomplete: "off", autocapitalize: "off", spellcheck: false, "aria-label": label });
  const box = h("div", { class: "label-picker-box", onclick: (e) => { if (e.target === box || e.target === chips) text.focus(); } }, chips, text);
  const offer = h("div", { class: "label-offer", role: "group", "aria-label": `Labels to add to ${label}` });
  // A click on a chip's or an offered label's button leaves the focus in
  // the text box (so it doesn't blur and add half a typed label).
  const el = h("div", { class: "label-picker", onmousedown: (e) => { if (e.target.closest("button")) e.preventDefault(); } }, box, offer);

  const has = (l) => labels.some((x) => lower(x) === lower(l));
  const changed = () => {
    input.value = labels.join(", ");
    render();
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
    onChange?.([...labels]);
  };
  const add = (ls) => {
    const fresh = ls.filter((l) => !has(l) && !exclude.some((x) => lower(x) === lower(l)));
    if (!fresh.length) return false;
    labels = [...labels, ...fresh];
    changed();
    return true;
  };
  const remove = (l) => { labels = labels.filter((x) => x !== l); changed(); };
  // commit adds what was typed.
  const commit = () => {
    const typed = parseLabels(text.value);
    text.value = "";
    if (!add(typed)) render();
  };
  function render() {
    chips.replaceChildren(...labels.map((l) => h("span", { class: `label-chip${isAny(l) ? " any" : ""}` },
      h("span", {}, l),
      h("button", { type: "button", class: "label-chip-x", "aria-label": `Remove ${l}`, title: `Remove ${l}`,
        onclick: () => { remove(l); text.focus(); } }, ico("x")))));
    const q = lower(text.value.trim());
    const skip = new Set([...labels, ...exclude].map(lower));
    const known = knownLabels({ any }).filter((l) => !skip.has(lower(l)));
    const shown = known.filter((l) => !q || lower(l).includes(q)).slice(0, 16);
    const typedNew = q && !/[\s,]/.test(q) && !skip.has(q) && !known.some((l) => lower(l) === q);
    offer.replaceChildren(...[
      typedNew ? h("button", { type: "button", class: "label-option new", onclick: () => { commit(); text.focus(); } }, ico("plus"), `New: ${text.value.trim()}`) : null,
      ...shown.map((l) => h("button", { type: "button", class: `label-option${isAny(l) ? " any" : ""}`, title: isAny(l) ? "Any runner" : `Add ${l}`,
        onclick: () => { text.value = ""; add([l]); text.focus(); } }, ico("plus"), l)),
    ].filter(Boolean));
    offer.hidden = !offer.childElementCount;
  }
  text.addEventListener("input", () => { if (/[\s,]/.test(text.value)) commit(); else render(); });
  text.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      // Never submits the form around it, nor closes the sheet.
      e.preventDefault();
      e.stopPropagation();
      commit();
    } else if (e.key === "Backspace" && !text.value && labels.length) {
      e.preventDefault();
      remove(labels[labels.length - 1]);
    }
  });
  // Enter's keypress, should it come on its own, mustn't submit the form either.
  text.addEventListener("keypress", (e) => { if (e.key === "Enter") e.preventDefault(); });
  // Leaving the field keeps what was typed (Save right after typing a label
  // saves it), unless the focus went to one of the picker's own buttons.
  text.addEventListener("blur", (e) => { if (text.value.trim() && !el.contains(e.relatedTarget)) commit(); });
  text.addEventListener("focus", render);

  const api = {
    el, input, text,
    get: () => [...labels],
    set: (ls) => { labels = parseLabels(Array.isArray(ls) ? ls.join(",") : ls); input.value = labels.join(", "); text.value = ""; render(); },
  };
  pickers.set(input, api);
  live.add({ el, input, redraw: () => render() });
  if (input.isConnected) input.after(el);
  render();
  return api;
}

// setLabels shows labels in the picker that took input over (silently: no
// change event, as when a form is filled in).
export function setLabels(input, labels) {
  const p = pickers.get(input);
  if (p) p.set(labels);
  else input.value = (labels || []).join(", ");
}

// matchList fills el with the runners that have the labels, as small chips
// with their online dot, or empty (none) when no runner has them.
export function matchList(el, runners, { lead = "Matches", none = "No runner has these labels yet." } = {}) {
  el.classList.add("label-match");
  el.classList.toggle("warn", !runners.length);
  el.replaceChildren(...(runners.length
    ? [`${lead}: `, ...runners.map((r) => h("span", { class: "label-match-runner", title: r.online ? "Online" : "Offline" },
      h("span", { class: `runner-dot${r.online ? " on" : ""}`, "aria-hidden": "true" }), r.name))]
    : [none]));
}
