import { COLORS, drawSurface } from "../draw.js";
import { $, $$, h, ib, ico, plural } from "./dom.js";
import * as store from "./store.js";
import { desktop } from "./viewport.js";
import { api, here } from "./api.js";
import { ask, attempt, fail, toast } from "./dialogs.js";
import { attachThumb, uploadFile } from "./attachments.js";
import { saveComments, state } from "./state.js";
import { baseBranch, multi, projectKey } from "./events.js";
import { selectProject } from "./projects.js";
import { title } from "./branches.js";
import { renderActions } from "./detail.js";
import { PV_LABEL, hidePreview, isTermPreview, previewOf, previewTodo, pv, renderPreview } from "./preview.js";
import { canEditDiff, openDiffEdit } from "./diffedit.js";
import { commentsFor } from "./review.js";
import { todoChat } from "./chat.js";
import { isTyping, openDialog } from "./vim.js";
import { askChat } from "./palette.js";

// ------------------------------------------------------------------ preview draw mode
// Draw (D) freezes the preview: the page in the iframe renders itself to a
// PNG (the proxy adds a small capture script to every page it serves, see
// internal/preview/capture.go), which replaces the live page. Marks and the
// crop are drawn on a canvas in Dash's page, so they can't drift. Add to
// review uploads the result as a picture for the rework notes.
export const draw = { on: false, busy: false, todo: null, meta: null, surface: null };
// Pictures and notes waiting to be sent back, per task: { items: [upload], text, prefix }.
const shots = new Map();

function captureFrame(frame, origin) {
  return new Promise((resolve, reject) => {
    const id = Math.random().toString(36).slice(2);
    let acked = false;
    const finish = (fn, v) => { clearTimeout(noAck); clearTimeout(tooLong); removeEventListener("message", onMsg); fn(v); };
    const onMsg = (e) => {
      if (e.source !== frame.contentWindow || e.origin !== origin || e.data?.id !== id) return;
      if (e.data.type === "dash:capture-ack") acked = true;
      else if (e.data.type === "dash:captured") e.data.error ? finish(reject, new Error(`The page couldn't draw itself: ${e.data.error}`)) : finish(resolve, e.data);
    };
    addEventListener("message", onMsg);
    const noAck = setTimeout(() => acked || finish(reject, new Error("The page didn't answer. It may still be loading, or it isn't a page from the preview: reload it and try again.")), 2500);
    const tooLong = setTimeout(() => finish(reject, new Error("Taking the snapshot took too long.")), 30000);
    try { frame.contentWindow.postMessage({ type: "dash:capture", id }, origin); } catch (e) { finish(reject, e); }
  });
}

// askFrame sends the preview's capture script a quick question (where the
// page is, which elements are under some points) and resolves to its answer,
// or null when it doesn't answer in time (an old page, or one still loading).
function askFrame(frame, origin, msg, answer, ms = 1500) {
  return new Promise((resolve) => {
    const id = Math.random().toString(36).slice(2);
    const finish = (v) => { clearTimeout(timer); removeEventListener("message", onMsg); resolve(v); };
    const onMsg = (e) => {
      if (e.source === frame.contentWindow && e.origin === origin && e.data?.id === id && e.data.type === answer) finish(e.data);
    };
    addEventListener("message", onMsg);
    const timer = setTimeout(() => finish(null), ms);
    try { frame.contentWindow.postMessage({ ...msg, id }, origin); } catch { finish(null); }
  });
}

// Exact capture (desktop Chrome and Edge): the browser's own tab capture,
// cropped to the preview, for pages a DOM rendering gets wrong (WebGL, video,
// some CSS). It asks for permission each time, so it's an option, not the default.
const exactCapture = {
  // preferCurrentTab and Region Capture (cropTo) are Chromium's, on desktop:
  // not on phones (iOS has no screen capture at all), Firefox or Safari.
  supported: () => !!navigator.mediaDevices?.getDisplayMedia && "CropTarget" in window && !!window.BrowserCaptureMediaStreamTrack?.prototype.cropTo && desktop.matches,
  get on() { return store.flag("dash.drawExact"); },
  set on(v) { store.setFlag("dash.drawExact", v); },
};

// grabTab captures one frame of this tab, cropped to frame, as a picture of
// frame's CSS size. Resolves to null when the user says no.
async function grabTab(frame) {
  let stream;
  try {
    stream = await navigator.mediaDevices.getDisplayMedia({
      video: { displaySurface: "browser", frameRate: 30 }, audio: false,
      preferCurrentTab: true, selfBrowserSurface: "include", surfaceSwitching: "exclude", monitorTypeSurfaces: "exclude",
    });
  } catch (e) {
    if (e.name === "NotAllowedError" || e.name === "AbortError") return null;
    throw e;
  }
  const [track] = stream.getVideoTracks();
  const video = document.createElement("video");
  try {
    try {
      await track.cropTo(await CropTarget.fromElement(frame)); // fails unless this tab is the one shared
    } catch {
      throw new Error("Share this tab (not another tab or window) to capture the preview exactly.");
    }
    video.muted = true;
    video.playsInline = true;
    video.srcObject = stream;
    await video.play();
    // Wait for frames of the cropped area: sharing may also show a bar that
    // makes the page (and the preview) a little shorter.
    const nextFrame = () => new Promise((ok) => { video.requestVideoFrameCallback?.(() => ok()); setTimeout(ok, 100); });
    const until = performance.now() + 3000;
    let fresh = 0;
    for (;;) {
      await nextFrame();
      const w = frame.clientWidth, h = frame.clientHeight;
      const fits = video.readyState >= 2 && video.videoWidth && Math.abs(video.videoWidth / video.videoHeight - w / h) < 0.02;
      if ((fits && ++fresh >= 3) || performance.now() > until) break;
    }
    const w = frame.clientWidth, h = frame.clientHeight;
    const canvas = document.createElement("canvas");
    canvas.width = video.videoWidth;
    canvas.height = Math.round(h * (video.videoWidth / w));
    canvas.getContext("2d").drawImage(video, 0, 0, canvas.width, canvas.height);
    return { bitmap: await createImageBitmap(canvas), width: w, height: h };
  } finally {
    track.stop();
    video.srcObject = null;
  }
}

function drawSurfaceOnce() {
  draw.surface ||= drawSurface($("#draw-stage"), { onChange: renderDrawBar });
  return draw.surface;
}

// snapshot takes the picture to draw on: the page's own rendering, or with
// exact, the tab's real pixels. Resolves to { bitmap, width, height, path,
// scroll, … }, or null when the user turned down the screen capture.
async function snapshot(frame, origin, exact) {
  if (!exact) {
    $("#draw-wait").hidden = false;
    try {
      const shot = await captureFrame(frame, origin);
      return { ...shot, bitmap: await createImageBitmap(shot.blob), exact: false };
    } finally {
      $("#draw-wait").hidden = true;
    }
  }
  // Nothing of Dash's may cover the preview while the tab is captured.
  const where = await askFrame(frame, origin, { type: "dash:where" }, "dash:here");
  const shot = await grabTab(frame);
  return shot && { ...where, ...shot, exact: true };
}

export async function startDraw() {
  if (draw.on || draw.busy) return;
  const t = state.todos.get(pv.todo);
  const p = t && previewOf(t.id);
  if (isTermPreview(p)) return; // nothing to draw on: Send back with transcript instead
  if (!p || p.state !== "live" || !pv.shown) throw new Error("Start the preview first: draw mode works on the live page.");
  if (!p.url) throw new Error("Drawing needs a preview domain (-preview-domain): the snapshot is taken through Dash's preview proxy.");
  draw.busy = true;
  try {
    const shot = await snapshot($("#pv-iframe"), new URL(p.url).origin, exactCapture.supported() && exactCapture.on);
    if (!shot) return;
    if (pv.todo !== t.id) return shot.bitmap.close(); // closed meanwhile
    draw.on = true;
    draw.todo = t.id;
    draw.meta = shot;
    $("#d-preview").classList.add("drawing");
    $("#draw-stage").hidden = false;
    $("#draw-bar").hidden = false;
    drawSurfaceOnce().open(shot.bitmap, shot.width, shot.height);
    renderPreview();
  } finally {
    draw.busy = false;
  }
}

// retakeDraw swaps the picture for one taken the other way (exact or not),
// keeping the marks; the choice is remembered for the next time.
async function retakeDraw(exact) {
  const t = state.todos.get(draw.todo);
  const p = t && previewOf(t.id);
  if (!draw.on || draw.busy || !p?.url) return;
  draw.busy = true;
  const preview = $("#d-preview");
  // Show the live page for a moment: that's what a screen capture sees.
  if (exact) { preview.classList.remove("drawing"); $("#draw-stage").hidden = true; }
  try {
    const shot = await snapshot($("#pv-iframe"), new URL(p.url).origin, exact);
    if (!shot) return;
    exactCapture.on = exact;
    if (!draw.on) return shot.bitmap.close();
    draw.meta = shot;
    draw.surface.replace(shot.bitmap, shot.width, shot.height);
    if (exact) toast("Exact capture: real pixels from the screen");
  } finally {
    draw.busy = false;
    if (draw.on) { preview.classList.add("drawing"); $("#draw-stage").hidden = false; }
    renderDrawBar();
  }
}

export function endDraw() {
  if (!draw.on) return;
  draw.on = false;
  draw.todo = null;
  draw.surface?.close();
  $("#d-preview").classList.remove("drawing");
  $("#draw-stage").hidden = true;
  $("#draw-bar").hidden = true;
  if (pv.todo) renderPreview();
}

function renderDrawBar() {
  const s = draw.surface;
  if (!s) return;
  for (const b of $$("#draw-bar [data-tool]")) b.setAttribute("aria-pressed", String(b.dataset.tool === s.tool));
  for (const b of $$("#draw-bar [data-color]")) b.setAttribute("aria-pressed", String(COLORS[b.dataset.color] === s.color));
  $("#draw-undo").disabled = !s.canUndo;
  $("#draw-clear").disabled = s.empty;
  const { w, h } = s.size;
  $("#draw-size").textContent = s.crop ? `${Math.round(w)}×${Math.round(h)}` : "";
  const ex = $("#draw-exact");
  ex.hidden = !exactCapture.supported();
  ex.setAttribute("aria-pressed", String(!!draw.meta?.exact));
}

// markHints asks the page which element is under each mark (an arrow's tip,
// the middle of a circle, box or pen stroke), for the notes: "circle → button.save ("Save settings")". The page is
// still there behind the picture, at the same scroll. Empty when it can't tell.
async function markHints(origin) {
  const marks = draw.surface.marks();
  if (!marks.length) return [];
  const res = await askFrame($("#pv-iframe"), origin, { type: "dash:inspect", points: marks.map(({ x, y }) => ({ x, y })), scroll: draw.meta.scroll }, "dash:inspected");
  if (!res || !Array.isArray(res.hints)) return [];
  if (draw.meta.path && res.path !== draw.meta.path) return []; // another page by now
  const colours = new Set(marks.map((m) => m.color)).size > 1;
  return marks.map((m, i) => {
    const h = res.hints[i];
    if (!h?.selector) return null;
    const what = colours && m.color ? `${m.color} ${m.t}` : m.t;
    return `${what} → ${h.selector}${h.text ? ` (${JSON.stringify(h.text)})` : ""}`;
  }).filter(Boolean);
}

// "/settings · 390×844 · dash/todo-3f9a" for the notes: where the picture is from.
function shotContext(t) {
  const m = draw.meta;
  return [m.path || "/", `${m.width}×${m.height}`, t.worktree?.branch].filter(Boolean).join(" · ");
}

const shotName = (path) => `preview${(path || "/").split(/[?#]/)[0].replace(/[^a-z0-9]+/gi, "-").replace(/-+$/, "") || ""}.png`;

async function addShot() {
  const t = state.todos.get(draw.todo);
  const p = t && previewOf(t.id);
  if (!t || draw.busy) return;
  draw.busy = true;
  $("#draw-add").disabled = true;
  try {
    const [blob, hints] = await Promise.all([
      draw.surface.flatten(),
      p?.url ? markHints(new URL(p.url).origin).catch(() => []) : [],
    ]);
    const prefix = p?.prefix ?? here();
    const a = await uploadFile(new File([blob], shotName(draw.meta.path), { type: "image/png" }), prefix);
    const entry = shots.get(t.id) || { items: [], text: "", prefix };
    entry.items.push(a);
    // The marks' elements go with the picture, one per line, and the note after them.
    const about = hints.length ? `${shotContext(t)}\n${hints.map((x) => `- ${x}`).join("\n")}\n` : `${shotContext(t)}: `;
    entry.text = `${entry.text.trimEnd()}${entry.text.trim() ? "\n\n" : ""}${about}`;
    shots.set(t.id, entry);
    endDraw();
    renderReworkBox();
    if (desktop.matches) {
      const ta = $("#d-rework textarea");
      ta?.focus();
      ta?.setSelectionRange(ta.value.length, ta.value.length);
    } else if (await reworkTodo(t)) {
      hidePreview();
    }
  } finally {
    draw.busy = false;
    $("#draw-add").disabled = false;
  }
}

async function copyShot() {
  const s = draw.surface;
  if (!s || !draw.on) return;
  const png = s.flatten();
  try {
    if (!window.ClipboardItem || !navigator.clipboard?.write) throw new Error("no image clipboard");
    await navigator.clipboard.write([new ClipboardItem({ "image/png": png })]); // a promise keeps Safari's user gesture
    toast("Picture copied");
  } catch {
    copyThis(await png); // clipboard needs https or localhost
  }
}

// The "Copy this" fallback for a picture: shown big, to copy or save by hand.
function copyThis(blob) {
  const url = URL.createObjectURL(blob);
  const d = h("dialog", { class: "sheet small copy-sheet", "aria-label": "Copy this" },
    h("form", { method: "dialog" },
      h("h2", {}, "Copy this"),
      h("img", { class: "copy-img", src: url, alt: "The picture from the preview" }),
      h("p", { class: "hint" }, "This browser can't put pictures on the clipboard here. Right-click or long-press the picture to copy or save it."),
      h("footer", { class: "sheet-actions" },
        h("a", { class: "btn", href: url, download: shotName(draw.meta?.path) }, ico("down"), "Download"),
        ib({ class: "btn primary", value: "ok" }, "check", "Done"))));
  d.addEventListener("close", () => { d.remove(); URL.revokeObjectURL(url); });
  document.body.append(d);
  d.showModal();
}

// The rework box sits beside the preview (above the task's action bar): the
// pictures added so far and the notes that go with them.
export function renderReworkBox() {
  const box = $("#d-rework");
  const t = state.todos.get(state.detail);
  const entry = t && shots.get(t.id);
  if (!entry?.items.length || !canRework(t)) { box.hidden = true; box.replaceChildren(); return; }
  if (box.dataset.todo === t.id && !box.hidden && box.querySelectorAll(".chip").length === entry.items.length) return; // keep focus and caret
  box.dataset.todo = t.id;
  const n = entry.items.length;
  const ta = h("textarea", { rows: 3, placeholder: "What should change?", "aria-label": "Rework notes", value: entry.text,
    oninput: () => { entry.text = ta.value; },
    onkeydown: (e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); sendShots(t); } } });
  ta.value = entry.text;
  const discard = () => { shots.delete(t.id); renderReworkBox(); };
  box.replaceChildren(
    h("div", { class: "rework-head" },
      h("strong", {}, "Rework notes"),
      h("span", { class: "hint" }, `${plural(n, "picture")} from the preview`),
      h("span", { class: "pv-spacer" }),
      ib({ class: "link", type: "button", onclick: discard, title: "Throw these pictures and notes away" }, "x", "Discard")),
    h("div", { class: "attach" }, entry.items.map((a) => attachThumb(a, () => {
      entry.items = entry.items.filter((x) => x !== a);
      renderReworkBox();
    }, entry.prefix))),
    ta,
    h("div", { class: "rework-actions" },
      h("span", { class: "hint" }, pv.todo === t.id ? "Draw (D) to add more" : ""),
      ib({ class: "btn small primary", type: "button", title: "Send back for rework with these notes (Ctrl+Enter)", onclick: attempt(() => sendShots(t)) }, "redo", "Send back")));
  box.hidden = false;
}

async function sendShots(t) {
  const entry = shots.get(t.id);
  if (!entry) return;
  await api("POST", `/api/todos/${t.id}/rework`, { feedback: entry.text.trim(), attachments: entry.items.map((a) => a.id) }, entry.prefix);
  shots.delete(t.id);
  renderReworkBox();
  toast("Sent back for rework");
  hidePreview();
}

$("#pv-draw-btn").addEventListener("click", attempt(() => (draw.on ? endDraw() : startDraw())));
$("#draw-done").addEventListener("click", endDraw);
$("#draw-exact").addEventListener("click", attempt(() => retakeDraw(!draw.meta?.exact)));
$("#draw-add").addEventListener("click", attempt(addShot));
$("#draw-copy").addEventListener("click", attempt(copyShot));
$("#draw-undo").addEventListener("click", () => draw.surface?.undo());
$("#draw-clear").addEventListener("click", () => draw.surface?.clear());
$$("#draw-bar [data-tool]").forEach((b) => b.addEventListener("click", () => { drawSurfaceOnce().tool = b.dataset.tool; }));
$$("#draw-bar [data-color]").forEach((b) => {
  b.style.setProperty("--swatch", COLORS[b.dataset.color]);
  b.addEventListener("click", () => { drawSurfaceOnce().color = COLORS[b.dataset.color]; });
});
// Draw mode's keys come before the app's (Esc would close the task).
const DRAW_KEYS = { p: "pen", a: "arrow", o: "circle", b: "box", c: "crop" };
addEventListener("keydown", (e) => {
  if (!draw.on || e.altKey || e.isComposing || openDialog() || isTyping(e.target)) return;
  const k = e.key.length === 1 ? e.key.toLowerCase() : e.key;
  const s = draw.surface;
  if (k === "Shift" || k === "Control" || k === "Meta") return;
  e.stopImmediatePropagation();
  if ((e.ctrlKey || e.metaKey) && k === "z") { e.preventDefault(); return s.undo(); }
  if (e.ctrlKey || e.metaKey) return; // the browser's own (copy, zoom, …)
  if (k === "Escape") { e.preventDefault(); return endDraw(); }
  if (k === "Enter") { e.preventDefault(); return attempt(addShot)(); }
  if (DRAW_KEYS[k]) { s.tool = DRAW_KEYS[k]; return; }
  if (k === "x" && exactCapture.supported()) { attempt(() => retakeDraw(!draw.meta?.exact))(); return; }
  if (["1", "2", "3"].includes(k)) { s.color = Object.values(COLORS)[k - 1]; return; }
}, true);

// "Previews (n)" in the top bar: every running preview, on every computer.
export let previewsSheet = null;
export function renderPreviewsBtn() {
  const list = [...state.previews.values()];
  const btn = $("#previews-btn");
  btn.hidden = !list.length;
  btn.dataset.state = list.some((p) => p.state === "crashed") ? "crashed" : list.some((p) => p.state === "live") ? "live" : "starting";
  // The dot's colour alone isn't enough (WCAG 1.4.1): say a crash in words too.
  const crashed = list.filter((p) => p.state === "crashed").length;
  $("#previews-label").replaceChildren(h("span", { class: "pv-word" }, "Previews "), `(${list.length})`,
    crashed ? h("span", { class: "pv-warn" }, " · crashed") : "");
  btn.title = plural(list.length, "running preview") +
    (crashed ? `, ${crashed} crashed` : "");
}

$("#previews-btn").addEventListener("click", () => {
  const body = h("div", { class: "pv-list" });
  const d = h("dialog", { class: "sheet small previews-sheet", "aria-labelledby": "pv-list-title" },
    h("form", { method: "dialog" },
      h("h2", { id: "pv-list-title" }, "Running previews"),
      body,
      h("footer", { class: "sheet-actions" },
        ib({ class: "btn", value: "cancel" }, "x", "Close"),
        ib({ class: "btn danger", type: "button", onclick: attempt(stopAllPreviews) }, "stop", "Stop all"))));
  const projectFor = (p) => (state.projects || []).find((x) => x.id === p.project_id && x.machine === p.machine);
  const render = () => {
    const list = [...state.previews.values()].sort((a, b) => b.started_at.localeCompare(a.started_at));
    if (!list.length) return d.close();
    body.replaceChildren(...list.map((p) => {
      const proj = projectFor(p);
      return h("div", { class: "pv-row" },
        h("span", { class: `pv-dot ${p.state}`, "aria-hidden": "true" }),
        h("button", { class: "pv-row-main", type: "button", onclick: attempt(async () => {
          d.close();
          if (proj && projectKey(proj) !== projectKey(state.project)) await selectProject(proj);
          const t = state.todos.get(p.todo_id) || await api("GET", `/api/todos/${p.todo_id}`, null, p.prefix);
          state.todos.set(t.id, t);
          await previewTodo(t);
        }) },
          h("span", { class: "pv-row-title" }, p.title || p.todo_id),
          h("span", { class: "pv-row-sub" }, [proj?.name, multi() ? p.machine : null, PV_LABEL[p.state] || p.state, p.kind === "terminal" ? "terminal" : `:${p.port}`].filter(Boolean).join(" · "))),
        ib({ class: "icon-btn", type: "button", title: "Stop", "aria-label": `Stop the preview of ${p.title}`, onclick: attempt(async () => {
          await api("DELETE", `/api/todos/${p.todo_id}/preview`, null, p.prefix);
        }) }, "stop"));
    }));
  };
  previewsSheet = { render };
  d.addEventListener("close", () => { previewsSheet = null; d.remove(); });
  render();
  document.body.append(d);
  d.showModal();
});

async function stopAllPreviews() {
  const prefixes = new Set([...state.previews.values()].map((p) => p.prefix));
  let n = 0;
  for (const prefix of prefixes) n += (await api("DELETE", "/api/previews", null, prefix)).stopped || 0;
  hidePreview();
  toast(`Stopped ${plural(n, "preview")}`);
}

// The task chat already gives Claude the task's instructions and diff, and runs
// in its worktree; a project can replace this prompt in its settings.
export const DEFAULT_REVIEW_PROMPT = `Review this task's changes as a strict senior reviewer, before they are merged. Don't modify any files.

Check, in this order:
1. Correctness: does it fully do what the instructions ask? Bugs, unhandled edge cases, regressions, callers that break.
2. Security and data loss risks.
3. Tests: are the changes covered where the project has tests? Run the relevant ones if you can.
4. Scope: unrelated changes, leftover debug code, dead code, code that doesn't match the surrounding style.

Read the surrounding code where you need to. Start with a one-line verdict, "Ready to merge" or "Needs changes", then list each must-fix issue with file:line and a concrete fix, then optional nice-to-haves. Leave out praise and anything you haven't verified.`;

let reviewNext = false;
// takeReviewNext tells whether the turn being sent is the Review button's, once.
export function takeReviewNext() { const review = reviewNext; reviewNext = false; return review; }
export function reviewTodo() {
  if (todoChat.busy) return toast("Wait for Claude to finish answering");
  reviewNext = true;
  const prompt = state.project?.review_prompt || DEFAULT_REVIEW_PROMPT;
  askChat(todoChat.input, $("#d-chat-form"), prompt, () => { state.pane = "timeline"; renderPane(); });
}

export const canRework = (t) => !!t?.worktree && ["review", "failed"].includes(t.status);
// text prefills the notes, e.g. from the task chat. Pictures added from the
// preview's draw mode (and their notes) come along. Resolves true once sent.
export async function reworkTodo(t, text = "") {
  const shot = shots.get(t.id);
  const value = [shot?.text.trim(), text].filter(Boolean).join("\n\n");
  const res = await ask({ title: "Send back for rework", body: "Claude continues in the same worktree with your notes. Paste or attach a screenshot to show what's wrong.", input: true, value, placeholder: "What should change?", ok: "Send back", attach: true, attachments: shot?.items || [] });
  if (res === null) return false;
  await api("POST", `/api/todos/${t.id}/rework`, { feedback: res.text, attachments: res.attachments });
  shots.delete(t.id);
  renderReworkBox();
  toast("Sent back for rework");
  return true;
}

// "Send to agent" turns what's in the task chat's composer into rework notes.
$("#d-to-agent").addEventListener("click", attempt(async () => {
  const t = state.todos.get(state.detail);
  if (!canRework(t)) return;
  const input = todoChat.input;
  if (await reworkTodo(t, input.value.trim())) {
    input.value = "";
    input.dispatchEvent(new Event("input"));
  }
}));

export async function revertTodo(t) {
  if (t.status !== "merged" || t.revert) throw new Error("Only merged tasks can be reverted.");
  const pr = state.project?.merge_mode === "pr";
  const pushes = state.project?.auto_push || !!state.project?.runners?.length;
  const ok = await ask({
    title: `Revert "${title(t)}"?`,
    body: pr ? `Opens a pull request that undoes this change on ${baseBranch(state.project)}.`
      : `Adds a commit that undoes this change on ${baseBranch(state.project) || "the default branch"}${pushes ? " and pushes it" : ""}. If later work conflicts, Claude gets a task to undo it by hand.`,
    ok: pr ? "Open revert PR" : "Revert", danger: true,
  });
  if (!ok) return;
  await api("POST", `/api/todos/${t.id}/revert`);
  toast("Reverting…");
}

// A small action sheet built on the ask dialog. A choice is [icon, label, fn]
// or [icon, label, fn, { disabled, note }]: a disabled one shows why (note).
export function menu(choices, heading = "Actions") {
  const d = $("#ask-sheet");
  $("#ask-title").textContent = heading;
  $("#ask-body").hidden = true;
  $("#ask-input").hidden = true;
  $("#ask-ok").hidden = true;
  let picked = null;
  const list = h("div", { class: "project-list" }, choices.map(([icon, label, fn, { disabled = false, note = "" } = {}]) => ib({
    type: "button", class: "btn menu-choice", style: "width:100%;margin-bottom:8px", disabled, title: note || null,
    onclick: () => { picked = fn; d.close(); },
  }, icon, label, note ? h("em", { class: "menu-note" }, note) : null)));
  $("#ask-body").after(list);
  d.showModal();
  // Run the choice only after "close" fired: it may reopen this dialog via ask().
  return new Promise((r) => d.addEventListener("close", () => { list.remove(); $("#ask-ok").hidden = false; r(); }, { once: true }))
    .then(() => picked && attempt(picked)());
}

$$(".pane-tabs .segmented button").forEach((b) => b.addEventListener("click", () => { state.pane = b.dataset.pane; renderPane(); }));

export function renderPane() {
  $$(".pane-tabs .segmented button").forEach((b) => b.setAttribute("aria-selected", String(b.dataset.pane === state.pane)));
  $("#d-diff").hidden = state.pane !== "diff";
  const chatShown = $("#d-chat").hidden && state.pane === "timeline";
  $("#d-chat").hidden = state.pane !== "timeline";
  renderDiff(state.diff);
  if (chatShown) todoChat.toEnd();
}

// A patch (git diff/show output) as its files: path, the lines after the
// header, and +/- counts.
export function parseDiff(text) {
  const files = [];
  let cur = null;
  for (const line of text.split("\n")) {
    if (line.startsWith("diff --git")) {
      const path = line.replace(/^diff --git a\/(.*) b\/.*$/, "$1");
      // to: the path after the change (a rename's new name).
      cur = { path, to: line.replace(/^diff --git a\/.* b\/(.*)$/, "$1") || path, lines: [], add: 0, del: 0, deleted: false, binary: false };
      files.push(cur);
    } else if (cur) {
      if (line.startsWith("deleted file")) cur.deleted = true;
      if (line.startsWith("Binary files ")) cur.binary = true;
      if (/^(index |new file|deleted file|similarity|rename |old mode|new mode|--- |\+\+\+ )/.test(line)) continue;
      if (line.startsWith("+")) cur.add++;
      else if (line.startsWith("-")) cur.del++;
      cur.lines.push(line);
    }
  }
  return files;
}

function renderDiff(text) {
  const files = parseDiff(text);
  const add = files.reduce((n, f) => n + f.add, 0);
  const del = files.reduce((n, f) => n + f.del, 0);
  $("#d-diffstat").replaceChildren(...(files.length ? [h("span", { class: "plus" }, `+${add}`), " ", h("span", { class: "minus" }, `−${del}`)] : []));

  if (!files.length) {
    const t = state.todos.get(state.detail);
    const msg = text.startsWith("error:") ? text : t?.worktree ? "No changes compared to the default branch." : "Changes appear here once Claude has worked on this task.";
    $("#d-diff").replaceChildren(h("p", { class: "pane-empty" }, msg));
    return;
  }
  const commented = new Set(commentsFor(state.detail).map((c) => c.file));
  const t = state.todos.get(state.detail), editable = canEditDiff(t);
  $("#d-diff").replaceChildren(...files.map((f) => h("details", {
    class: "diff-file", open: state.diffOpen.has(f.path) || commented.has(f.path),
    ontoggle: (e) => state.diffOpen[e.target.open ? "add" : "delete"](f.path),
  },
    h("summary", {}, h("span", { class: "path" }, f.path), h("span", { class: "counts" }, h("span", { class: "plus" }, `+${f.add}`), " ", h("span", { class: "minus" }, `−${f.del}`)),
      editable && !f.deleted && !f.binary ? ib({
        type: "button", class: "btn small diff-edit", title: `Edit ${f.to} before approving`, "aria-label": `Edit ${f.to}`,
        onclick: (e) => { e.preventDefault(); e.stopPropagation(); openDiffEdit(t.id, f.to).catch(fail); },
      }, "edit", "Edit") : null),
    h("div", { class: "diff-lines" }, diffLines(f, {
      comments: commentsFor(state.detail).filter((c) => c.file === f.path),
      commentable: ["review", "failed"].includes(state.todos.get(state.detail)?.status),
    })))));
}

// Rows for one file, numbered like the new file (removed lines use old numbers),
// with a task's saved review comments under their lines.
export function diffLines(f, { comments = [], commentable = false } = {}) {
  const rows = [];
  let oldLn = 0, newLn = 0;
  for (const l of f.lines) {
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)/.exec(l);
    if (hunk) {
      oldLn = Number(hunk[1]); newLn = Number(hunk[2]);
      rows.push(h("div", { class: "hunk" }, l));
      continue;
    }
    if (l.startsWith("\\")) { rows.push(h("div", { class: "meta" }, l)); continue; }
    const side = l.startsWith("-") ? "old" : "new";
    const ln = side === "old" ? oldLn++ : newLn++;
    if (l.startsWith(" ") || l === "") oldLn++;
    // The button gives keyboard and screen reader users a way to open the comment box.
    rows.push(h("div", { class: l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : "", "data-ln": ln, "data-side": side, "data-file": f.path, title: commentable ? "Click to comment" : null },
      commentable ? h("button", { type: "button", class: "ln-comment", "aria-label": `Comment on ${f.path} ${side === "old" ? "removed line" : "line"} ${ln}` }, "+") : null,
      h("span", { class: "code" }, l || " ")));
    for (const c of comments.filter((c) => c.line === ln && c.side === side)) {
      rows.push(h("div", { class: "comment" },
        h("span", {}, c.text),
        ib({ class: "link", type: "button", "aria-label": "Delete comment", onclick: () => {
          state.comments[state.detail] = commentsFor(state.detail).filter((x) => x !== c);
          saveComments(); renderPane(); renderActions(state.todos.get(state.detail));
        } }, "trash", "Delete")));
    }
  }
  return rows;
}
