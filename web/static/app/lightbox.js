import { fill, h, ib, ico } from "./dom.js";

// ------------------------------------------------------------------ lightbox
// Previews attached files over the page: images at full size; text files
// read in place (Markdown rendered, JSON indented, CSV as a table); other
// files (PDFs) with their name, size and a download. ← and → step through
// the files shown together; Esc closes.
//
// A link with data-preview opens here (its href is the file; data-type,
// data-name and data-size describe it). A click with a modifier key, or the
// middle button, still opens it the browser's way. The files shown together
// are the data-preview links in the link's closest [data-preview-group], or
// else in its grandparent (a row of chips).
//
// It uses dom.js only, so Planning's page (planning.html) can load it too.

// TEXT_MAX is how much of a text file is shown.
const TEXT_MAX = 512 << 10;
// CSV_ROWS is how many rows of a CSV file the table shows.
const CSV_ROWS = 500;

let dlg = null, stage, title, sub, count, dl, prev, next;
let files = [], at = 0, shown = 0;

export const isImage = (f) => (f.type || "").startsWith("image/");
const isText = (f) => /^text\/|^application\/json$/.test(f.type || "");

// sizeText is a file's size for people: "12 KB", "3.4 MB".
export function sizeText(n) {
  if (!(n >= 0)) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

// extOf is a file name's extension in capitals, for a file without a picture.
export const extOf = (name) => (String(name).includes(".") ? String(name).split(".").pop() : "file").toUpperCase().slice(0, 5);

function build() {
  title = h("span", { class: "lb-name" });
  sub = h("span", { class: "lb-sub" });
  count = h("span", { class: "lb-count" });
  dl = h("a", { class: "btn small", download: "", title: "Download" }, ico("down"), "Download");
  prev = ib({ type: "button", class: "icon-btn lb-step lb-prev", "aria-label": "Previous file", title: "Previous (←)", onclick: () => step(-1) }, "left");
  next = ib({ type: "button", class: "icon-btn lb-step lb-next", "aria-label": "Next file", title: "Next (→)", onclick: () => step(1) }, "right");
  stage = h("div", { class: "lb-stage" });
  dlg = h("dialog", { class: "lightbox", "aria-labelledby": "lb-name" },
    h("header", { class: "lb-head" },
      h("span", { class: "lb-title" }, title, h("span", { class: "lb-meta" }, count, sub)),
      dl,
      ib({ type: "button", class: "icon-btn", "aria-label": "Close", title: "Close (Esc)", onclick: () => dlg.close() }, "x")),
    h("div", { class: "lb-body" }, prev, stage, next));
  title.id = "lb-name";
  dlg.addEventListener("keydown", (e) => {
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      if (e.target.closest?.("input, textarea, select")) return;
      e.preventDefault();
      step(e.key === "ArrowLeft" ? -1 : 1);
    }
  });
  // A click beside the file (on the dim backdrop) closes.
  dlg.addEventListener("click", (e) => { if (e.target === dlg || e.target === stage) dlg.close(); });
  dlg.addEventListener("close", () => { fill(stage); files = []; shown++; });
  document.body.append(dlg);
}

function step(d) {
  if (files.length < 2) return;
  at = (at + d + files.length) % files.length;
  render();
}

// openPreview shows files[index] (each { url, name, type, size }) in the
// lightbox, with the others a step away.
export function openPreview(list, index = 0) {
  if (!list.length) return;
  if (!dlg) build();
  files = list;
  at = Math.max(0, Math.min(index, list.length - 1));
  render();
  if (!dlg.open) dlg.showModal();
}

function render() {
  const f = files[at];
  const me = ++shown;
  title.textContent = f.name;
  sub.textContent = sizeText(f.size);
  count.textContent = files.length > 1 ? `${at + 1} of ${files.length}` : "";
  dl.href = f.url;
  dl.setAttribute("download", f.name);
  prev.hidden = next.hidden = files.length < 2;
  if (isImage(f)) {
    fill(stage, h("img", { class: "lb-img", src: f.url, alt: f.name }));
  } else if (isText(f)) {
    fill(stage, h("p", { class: "lb-note" }, "Loading…"));
    textOf(f).then((el) => { if (me === shown) fill(stage, el); },
      (e) => { if (me === shown) fill(stage, fileCard(f, `Can't show it: ${e.message}`)); });
  } else {
    fill(stage, fileCard(f, "This kind of file can't be shown here. Download it to open it."));
  }
}

function fileCard(f, note) {
  return h("div", { class: "lb-file" },
    h("span", { class: "lb-ext", "aria-hidden": "true" }, extOf(f.name)),
    h("p", { class: "lb-note" }, note));
}

async function textOf(f) {
  const res = await fetch(f.url, { credentials: "same-origin" });
  if (!res.ok) throw new Error(res.status === 404 ? "it is gone" : res.statusText);
  let text = await res.text();
  const cut = text.length > TEXT_MAX;
  if (cut) text = text.slice(0, TEXT_MAX);
  const more = cut ? h("p", { class: "lb-note" }, `Showing the first ${sizeText(TEXT_MAX)}. Download it for the rest.`) : null;
  const name = f.name.toLowerCase();
  if (f.type === "application/json" && !cut) {
    try { text = JSON.stringify(JSON.parse(text), null, 2); } catch { /* shown as it is */ }
  }
  if (f.type === "text/csv") return h("div", { class: "lb-text" }, csvTable(text), more);
  if (f.type === "text/markdown" || name.endsWith(".md")) {
    const { renderMarkdown } = await import("../editor.js");
    return h("div", { class: "lb-text lb-md md", innerHTML: renderMarkdown(text) }, more);
  }
  return h("div", { class: "lb-text" }, h("pre", {}, text), more);
}

// csvTable is CSV text as a table: the first row its head, then up to
// CSV_ROWS rows. Quoted fields may hold commas, quotes ("") and new lines.
function csvTable(text) {
  const rows = [];
  let row = [], cell = "", quoted = false;
  for (let i = 0; i < text.length && rows.length <= CSV_ROWS; i++) {
    const ch = text[i];
    if (quoted) {
      if (ch === '"' && text[i + 1] === '"') { cell += '"'; i++; } else if (ch === '"') quoted = false; else cell += ch;
    } else if (ch === '"') quoted = true;
    else if (ch === ",") { row.push(cell); cell = ""; } else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && text[i + 1] === "\n") i++;
      row.push(cell); rows.push(row); row = []; cell = "";
    } else cell += ch;
  }
  if (cell || row.length) { row.push(cell); rows.push(row); }
  const [head = [], ...body] = rows;
  const more = body.length > CSV_ROWS;
  return h("div", { class: "lb-table" },
    h("table", {},
      h("thead", {}, h("tr", {}, ...head.map((c) => h("th", {}, c)))),
      h("tbody", {}, ...body.slice(0, CSV_ROWS).map((r) => h("tr", {}, ...r.map((c) => h("td", {}, c)))))),
    more ? h("p", { class: "lb-note" }, `Showing the first ${CSV_ROWS} rows.`) : null);
}

// previewOf describes the file a data-preview link points at.
const previewOf = (a) => ({ url: a.href, name: a.dataset.name || a.textContent.trim(), type: a.dataset.type || "", size: Number(a.dataset.size) });

document.addEventListener("click", (e) => {
  const a = e.target.closest?.("a[data-preview]");
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  const group = a.closest("[data-preview-group]") || a.parentElement?.parentElement || a.parentElement;
  const links = [...group.querySelectorAll("a[data-preview]")];
  openPreview(links.map(previewOf), links.indexOf(a));
});
