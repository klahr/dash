import { $, h } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { copyBlock } from "./prefs.js";
import { agoText } from "./scans.js";

// ------------------------------------------------------------------ board backend
// Where the project's tasks are planned: Dash's own board (the default) or
// an external tracker synced both ways (internal/tracker). Saved with its
// own button, not the settings form. Credentials are write-only.
const BOARD_STATUS = { draft: "Draft", queued: "Queued", in_progress: "Running", review: "Review", merging: "Merging", merged: "Merged", failed: "Failed" };
const board = { state: null };
const boardReg = (name) => board.state?.backends.find((b) => b.name === name) || null;
const boardLabel = (name) => boardReg(name)?.label || name;

export async function loadBoard() {
  const p = state.project;
  let res;
  try {
    res = await api("GET", `/api/projects/${p.id}/tracker`);
  } catch {
    $("#settings-board-field").hidden = true; // e.g. an older Dash on another computer
    return;
  }
  if (state.project !== p) return;
  board.state = res;
  $("#settings-board-field").hidden = !res.enabled;
  const hint = $("#settings-board-hint");
  hint.hidden = res.enabled;
  hint.textContent = "This project's board is Dash's own here: external trackers are set up on the hub.";
  $("#board-backend").replaceChildren(...res.backends.map((b) => h("option", { value: b.name }, b.label || b.name)));
  $("#board-backend").value = res.config.backend || "dash";
  $("#board-webhook").hidden = true;
  renderBoardForm();
  renderBoardStatus();
}

// renderBoardForm lays out the chosen backend's settings: what is saved,
// when it is the saved backend, else its defaults.
function renderBoardForm() {
  const st = board.state;
  const cfg = st.config;
  const name = $("#board-backend").value;
  const reg = boardReg(name) || {};
  const native = !!reg.native;
  const saved = cfg.backend || "dash";
  const same = name === saved;
  $("#board-backend-hint").textContent = native
    ? (st.backends.length > 1 ? "Tasks are planned on Dash's board." : "Tasks are planned on Dash's board. External trackers (Favro first, then Trello and Jira) appear here as their backends are added.")
    : `Tasks are planned in ${reg.label || name}: titles, descriptions, priority, dependencies, status and comments sync both ways. Runs, reviews, checks and costs stay in Dash.`;
  $("#board-external").hidden = native;
  const values = same && cfg.config ? cfg.config : {};
  const fields = reg.fields || [];
  $("#board-fields").replaceChildren(...fields.map((f) => h("label", { class: "field" },
    h("span", {}, f.label || f.name, f.hint ? h("em", {}, ` ${f.hint}`) : null),
    h("input", {
      "data-board-field": f.name, "data-secret": f.secret ? "1" : "", type: f.secret ? "password" : "text",
      autocomplete: f.secret ? "new-password" : "off", spellcheck: "false", value: f.secret ? "" : (values[f.name] ?? ""),
      placeholder: f.secret && same && cfg.has_credentials ? "Saved: type a new one to replace it" : (f.placeholder || ""),
    }))));
  // A backend without a form of its own (yet) takes its settings as JSON.
  $("#board-config-raw-row").hidden = native || fields.some((f) => !f.secret);
  $("#board-config-raw").value = same && cfg.config ? JSON.stringify(cfg.config) : "";
  $("#board-creds-clear-row").hidden = native || !(same && cfg.has_credentials);
  $("#board-creds-clear").checked = false;
  const map = (same && cfg.status_map) || reg.default_status_map || {};
  $("#board-map").replaceChildren(...st.statuses.map((s) => h("label", { class: "board-map-row" },
    h("span", {}, BOARD_STATUS[s] || s),
    h("input", { "data-board-status": s, value: map[s] || "", list: "board-columns", spellcheck: "false", autocomplete: "off", placeholder: "not moved" }))));
  // Leaving Dash's board: the person decides what happens to the open tasks.
  $("#board-export-row").hidden = native || saved !== "dash";
  $("#board-open-count").textContent = st.unlinked ? `${st.unlinked} open` : "existing";
  for (const r of document.querySelectorAll('input[name="board_export_choice"]')) r.checked = false;
  $("#board-save").hidden = native && same;
  $("#board-test").hidden = native;
  $("#board-sync").hidden = $("#board-secret").hidden = native || !same;
}

function renderBoardStatus() {
  const st = board.state;
  const cfg = st.config;
  const external = (cfg.backend || "dash") !== "dash";
  const bits = [];
  if (external) {
    bits.push(st.running ? `Synced with ${boardLabel(cfg.backend)}.` : `Not syncing with ${boardLabel(cfg.backend)}.`);
    if (cfg.last_sync_at) bits.push(cfg.last_error ? `Last sync ${agoText(cfg.last_sync_at)} failed: ${cfg.last_error}` : `Last sync ${agoText(cfg.last_sync_at)}.`);
    else bits.push("Not synced yet.");
    bits.push(`${st.linked} task${st.linked === 1 ? "" : "s"} linked to cards${st.unlinked ? `, ${st.unlinked} Dash-only` : ""}.`);
  }
  const el = $("#board-status");
  el.hidden = !bits.length;
  el.textContent = bits.join(" ");
  el.classList.toggle("err-text", external && !!cfg.last_error);
  $("#board-events").replaceChildren(...(st.events || []).slice(0, 5).map((ev) =>
    h("li", { class: "runner-row" }, h("span", { class: "runner-name" }, ev.text), h("span", { class: "runner-seen hint" }, agoText(ev.at)))));
}

// boardBody is the form as the API takes it.
function boardBody() {
  const name = $("#board-backend").value;
  if (boardReg(name)?.native) return { backend: name };
  let config = {};
  const raw = $("#board-config-raw").value.trim();
  if (!$("#board-config-raw-row").hidden && raw) {
    try { config = JSON.parse(raw); } catch { throw new Error("The settings aren't valid JSON."); }
  }
  const credentials = {};
  for (const el of $("#board-fields").querySelectorAll("input")) {
    const v = el.value.trim();
    if (el.dataset.secret) { if (v) credentials[el.dataset.boardField] = v; } else if (v) config[el.dataset.boardField] = v;
  }
  const status_map = Object.fromEntries([...$("#board-map").querySelectorAll("input")].map((el) => [el.dataset.boardStatus, el.value.trim()]));
  const body = { backend: name, config, credentials, status_map };
  if ($("#board-creds-clear").checked) body.clear_credentials = true;
  return body;
}

async function saveBoard(extra = {}) {
  const p = state.project;
  const body = { ...boardBody(), ...extra };
  if (!$("#board-export-row").hidden && body.export === undefined) {
    const pick = document.querySelector('input[name="board_export_choice"]:checked');
    if (!pick) throw new Error("Choose what happens to the project's open tasks first.");
    body.export = pick.value === "yes";
  }
  let res;
  try {
    res = await api("PUT", `/api/projects/${p.id}/tracker`, body);
  } catch (e) {
    if (e.status !== 409 || !e.data?.needs) throw e;
    if (e.data.needs === "export") {
      $("#board-export-row").hidden = false;
      throw new Error("Choose what happens to the project's open tasks, then save again.");
    }
    // Leaving an external tracker drops the links to its cards: ask first.
    const to = boardLabel(body.backend);
    const ok = await ask({
      title: body.backend === "dash" ? "Move the board back to Dash?" : `Move the board to ${to}?`,
      body: `This project's tasks lose their links to the cards on ${boardLabel(e.data.from)}. The cards stay there; the tasks stay in Dash` +
        (body.backend === "dash" ? " as Dash's own." : ", and only tasks made from now on get cards on the new board."),
      ok: "Move board", danger: true,
    });
    if (!ok) return;
    return saveBoard({ ...extra, export: body.export ?? false, confirm: true });
  }
  if (state.project !== p) return;
  board.state = res;
  $("#board-backend").value = res.config.backend || "dash";
  renderBoardForm();
  renderBoardStatus();
  toast("Board saved", { tone: "ok" });
}

$("#board-backend").addEventListener("change", () => renderBoardForm());
$("#board-save").addEventListener("click", attempt(async (e) => {
  e.target.disabled = true;
  try { await saveBoard(); } finally { e.target.disabled = false; }
}));
$("#board-test").addEventListener("click", attempt(async (e) => {
  e.target.disabled = true;
  try {
    const res = await api("POST", `/api/projects/${state.project.id}/tracker/test`, boardBody());
    if (!res.ok) throw new Error(`Connection failed: ${res.error}`);
    // The tracker's columns, to pick from in the mapping.
    $("#board-columns").replaceChildren(...(res.columns || []).map((c) => h("option", { value: c.id }, c.name)));
    toast(res.columns?.length ? `Connected: ${res.columns.length} columns found` : "Connected", { tone: "ok" });
  } finally { e.target.disabled = false; }
}));
$("#board-sync").addEventListener("click", attempt(async (e) => {
  e.target.disabled = true;
  try {
    const res = await api("POST", `/api/projects/${state.project.id}/tracker/sync`);
    if (res.state) { board.state = res.state; renderBoardStatus(); }
    if (!res.ok) throw new Error(`Sync failed: ${res.error}`);
    toast("Synced", { tone: "ok" });
  } finally { e.target.disabled = false; }
}));
$("#board-secret").addEventListener("click", attempt(async () => {
  if (board.state.config.has_webhook_secret && !await ask({ title: "Make a new webhook secret?", body: "The current one stops working at once: update the tracker's webhook with the new one.", ok: "New secret" })) return;
  const res = await api("POST", `/api/projects/${state.project.id}/tracker/webhook-secret`);
  board.state.config.has_webhook_secret = true;
  const el = $("#board-webhook");
  el.replaceChildren(
    h("p", {}, "Point the tracker's webhook at this address, with this secret. Copy it now: Dash doesn't show it again."),
    copyBlock(location.origin + res.path, "Address copied", { pre: true }),
    copyBlock(res.secret, "Secret copied", { pre: true }),
    h("p", { class: "hint" }, "Trackers without signed webhooks send it as the ", h("code", {}, "X-Dash-Tracker-Secret"), " header or a ", h("code", {}, "?secret="), " parameter."));
  el.hidden = false;
}));
