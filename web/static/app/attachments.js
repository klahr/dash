import { h } from "./dom.js";
import { here, on } from "./api.js";
import { fail } from "./dialogs.js";

// ------------------------------------------------------------------ attachments
// Screenshots and files for Claude: an "Attach file" button, paste into the
// text field, or drop onto the sheet.
const CLIP = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 11.5 12.6 19.9a5 5 0 0 1-7.1-7.1l8.5-8.5a3.3 3.3 0 0 1 4.7 4.7l-8.5 8.5a1.7 1.7 0 0 1-2.4-2.4l7.8-7.8" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>';
export const clip = () =>h("span", { class: "clip", innerHTML: CLIP });
const uploadURL = (a, prefix = here()) => on(prefix, `/api/uploads/${a.id}/${encodeURIComponent(a.name)}`);
const ACCEPT = "image/png,image/jpeg,image/gif,image/webp,image/svg+xml,.pdf,.txt,.md,.log,.json,.csv";

export async function uploadFile(file, prefix = here()) {
  const res = await fetch(on(prefix, `/api/uploads?name=${encodeURIComponent(file.name || "pasted")}`), {
    method: "POST", body: file, headers: { "Content-Type": file.type || "application/octet-stream" },
  });
  if (res.status === 401) { location.replace("/login.html"); throw new Error("Sign in again to continue."); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

export function attachThumb(a, onRemove, prefix = here()) {
  const isImg = a.type.startsWith("image/");
  return h("span", { class: "chip" },
    h("a", { class: "chip-link", href: uploadURL(a, prefix), target: "_blank", rel: "noopener", title: a.name },
      isImg ? h("img", { src: uploadURL(a, prefix), alt: "", loading: "lazy" }) : h("span", { class: "chip-icon" }, (a.name.split(".").pop() || "file").toUpperCase()),
      h("span", { class: "chip-name" }, a.name)),
    onRemove ? h("button", { type: "button", class: "chip-x", "aria-label": `Remove ${a.name}`, onclick: onRemove }, "×") : null);
}

// compact (the chat composers) drops the button and hides the row while it's
// empty; files arrive by paste or drop onto zone. prefix names the computer
// that stores the uploads.
export function attachBox(el, textarea, onChange = () => {}, { zone = el.closest("dialog") || el, compact = false, prefix = here } = {}) {
  let items = [], pending = 0, busy = Promise.resolve();
  const input = h("input", { type: "file", accept: ACCEPT, multiple: true, hidden: true, onchange: () => { add(input.files); input.value = ""; } });
  const render = () => {
    if (compact) el.hidden = !items.length && !pending;
    el.replaceChildren(
      ...items.map((a) => attachThumb(a, () => { items = items.filter((x) => x !== a); render(); onChange(); }, prefix())),
      ...(pending ? [h("span", { class: "chip uploading" }, h("span", { class: "spinner", "aria-hidden": "true" }), `Uploading ${pending}…`)] : []),
      ...(compact ? [] : [h("button", { type: "button", class: "btn small attach-btn", onclick: () => input.click() },
        clip(),
        "Attach file"),
      input]));
  };
  const off = () => !compact && el.hidden;
  const add = (files) => {
    files = [...files];
    if (!files.length) return;
    pending += files.length;
    render();
    const at = prefix();
    busy = busy.then(async () => {
      for (const f of files) {
        try { items.push(await uploadFile(f, at)); onChange(); } catch (e) { fail(e); }
        pending--;
        render();
      }
    });
  };
  textarea?.addEventListener("paste", (e) => {
    const files = [...(e.clipboardData?.files || [])];
    if (files.length) { e.preventDefault(); add(files); }
  });
  zone.addEventListener("dragover", (e) => {
    if (off() || !e.dataTransfer?.types.includes("Files")) return;
    e.preventDefault();
    zone.classList.add("file-over");
  });
  zone.addEventListener("dragleave", (e) => { if (!zone.contains(e.relatedTarget)) zone.classList.remove("file-over"); });
  zone.addEventListener("drop", (e) => {
    zone.classList.remove("file-over");
    if (off() || !e.dataTransfer?.files.length) return;
    e.preventDefault();
    add(e.dataTransfer.files);
  });
  render();
  return {
    reset(list = []) { items = [...list]; pending = 0; render(); },
    ids: () => items.map((a) => a.id),
    items: () => [...items],
    settled: () => busy,
  };
}
