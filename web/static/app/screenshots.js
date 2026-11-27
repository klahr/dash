import { h, ib, plural } from "./dom.js";
import * as store from "./store.js";
import { desktop } from "./viewport.js";
import { here, on } from "./api.js";
import { attempt } from "./dialogs.js";
import { openDetail } from "./detail.js";
import { canRework, drawScreenshot, renderPane } from "./drawmode.js";

// ------------------------------------------------------------------ before/after screenshots
// When a task of a web project finishes, Dash takes screenshots of the pages
// it changed, on its branch (after) and on the commit it started from
// (before), at a wide and a phone size (internal/screenshot). The task's
// Changes pane shows each page's pair side by side, or on top of each other
// with a slider; a picture opens in Draw, whose marks go back as rework.

const shotURL = (t, name) => on(here(), `/api/todos/${encodeURIComponent(t.id)}/screenshots/${encodeURIComponent(name)}`);

// shotsKey changes whenever what the pane shows of t's screenshots does.
export const shotsKey = (t) => { const s = t?.screenshots; return s ? `${s.state}/${s.run}/${s.at}` : ""; };

// The views picked last, kept in this browser: which size, and how to compare.
const view = {
  get size() { return store.get("dash.shotSize", "wide"); },
  set size(v) { store.set("dash.shotSize", v); },
  // Side by side on a desktop; on a phone, two wide pictures side by side are too small to see.
  get mode() { return store.get("dash.shotMode", desktop.matches ? "side" : "slider"); },
  set mode(v) { store.set("dash.shotMode", v); },
};

const pick = (label, opts, cur, set) => h("div", { class: "segmented shot-pick", role: "group", "aria-label": label },
  opts.map(([v, text]) => h("button", { type: "button", "aria-pressed": String(v === cur), onclick: () => { set(v); renderPane(); } }, text)));

// shotsView is the screenshots section above a task's changes, or null.
export function shotsView(t) {
  const s = t?.screenshots;
  if (!s) return null;
  if (s.state === "capturing") {
    return h("section", { class: "shots", "aria-label": "Screenshots" },
      h("p", { class: "hint shots-wait" }, h("span", { class: "spinner", "aria-hidden": "true" }),
        `Taking before/after screenshots of ${plural(s.pages?.length || 0, "page")}…`));
  }
  const pages = (s.pages || []).filter((p) => p.wide_before || p.wide_after || p.phone_before || p.phone_after);
  const note = s.note ? h("p", { class: "hint shots-note" }, s.note) : null;
  if (!pages.length) return h("section", { class: "shots", "aria-label": "Screenshots" }, note);
  const size = view.size === "phone" ? "phone" : "wide", mode = view.mode === "slider" ? "slider" : "side";
  return h("section", { class: "shots", "data-size": size, "data-mode": mode, "aria-label": "Screenshots" },
    h("div", { class: "shots-head" },
      h("strong", {}, "Screenshots"),
      pick("Size", [["wide", "Wide"], ["phone", "Phone"]], size, (v) => { view.size = v; }),
      pick("Compare", [["side", "Side by side"], ["slider", "Slider"]], mode, (v) => { view.mode = v; })),
    note,
    pages.map((p) => shotPage(t, p, size, mode)));
}

// One page: its label and path, and its pair of pictures at size.
function shotPage(t, p, size, mode) {
  const before = p[`${size}_before`], after = p[`${size}_after`];
  const draws = canRework(t);
  const meta = (side) => ({ path: p.path, scale: size === "phone" ? 2 : 1, about: `${p.label || p.path} · ${side} · ${size} screenshot` });
  const open = (side, name) => attempt(() => drawScreenshot(t, shotURL(t, name), meta(side)));
  const img = (side, name) => {
    if (!name) return h("div", { class: "shot-missing" }, side === "before" ? "No before picture" : "No after picture");
    const el = h("img", { src: shotURL(t, name), alt: `${p.label || p.path}, ${side} the change (${size})`, loading: "lazy", decoding: "async" });
    return draws
      ? h("button", { type: "button", class: "shot-img", title: `Draw on the ${side} picture, to send it back as rework`, onclick: open(side, name) }, el)
      : h("a", { class: "shot-img", href: el.src, target: "_blank", rel: "noopener", title: "Open the picture" }, el);
  };
  const head = h("figcaption", { class: "shot-cap" }, h("span", { class: "shot-label" }, p.label || p.path),
    p.label && p.label !== p.path ? h("code", { class: "shot-path" }, p.path) : null);
  if (mode === "slider" && before && after) {
    const box = h("div", { class: "shot-compare", style: "--pos: 50%" },
      h("img", { class: "shot-after", src: shotURL(t, after), alt: `${p.label || p.path}, after the change (${size})`, loading: "lazy", decoding: "async" }),
      h("img", { class: "shot-before", src: shotURL(t, before), alt: `${p.label || p.path}, before the change (${size})`, loading: "lazy", decoding: "async" }));
    const range = h("input", { type: "range", min: 0, max: 100, value: 50, class: "shot-range", "aria-label": `Before and after: ${p.label || p.path}`,
      oninput: () => box.style.setProperty("--pos", `${range.value}%`) });
    return h("figure", { class: "shot-page" }, head, box,
      h("div", { class: "shot-slide" }, h("span", { class: "shot-side before" }, "Before"), range, h("span", { class: "shot-side after" }, "After")),
      draws ? h("div", { class: "shot-draws" },
        ib({ type: "button", class: "btn small", onclick: open("before", before) }, "edit", "Draw on before"),
        ib({ type: "button", class: "btn small", onclick: open("after", after) }, "edit", "Draw on after")) : null);
  }
  return h("figure", { class: "shot-page" }, head,
    h("div", { class: "shot-pair" },
      h("div", { class: "shot-side-col" }, h("span", { class: "shot-side before" }, "Before"), img("before", before)),
      h("div", { class: "shot-side-col" }, h("span", { class: "shot-side after" }, "After"), img("after", after))));
}

// shotThumb is the Review card's small picture of the first page after the
// change, which opens the task on its changes; null without one.
export function shotThumb(t) {
  const s = t.screenshots;
  if (t.status !== "review" || s?.state !== "done") return null;
  const first = (s.pages || []).find((p) => p.wide_after || p.phone_after);
  if (!first) return null;
  const n = (s.pages || []).length;
  return h("button", {
    type: "button", class: "shot-thumb", title: `Screenshots of ${plural(n, "page")}: ${first.label || first.path}`,
    "aria-label": `Screenshots of ${plural(n, "page")}`,
    onclick: attempt(() => openDetail(t.id, { pane: "diff" })),
  }, h("img", { src: shotURL(t, first.wide_after || first.phone_after), alt: "", loading: "lazy", decoding: "async" }));
}
