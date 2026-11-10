import { $, h } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, busy, toast } from "./dialogs.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { copyBlock } from "./prefs.js";
import { agoText } from "./branches.js";

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
  if (projectKey(state.project) !== projectKey(p)) return;
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
    f.type === "select" ? boardSelect(f, values[f.name] ?? "") : h("input", {
      "data-board-field": f.name, "data-secret": f.secret ? "1" : "", type: f.secret ? "password" : "text",
      autocomplete: f.secret ? "new-password" : "off", spellcheck: "false", value: f.secret ? "" : (values[f.name] ?? ""),
      placeholder: f.secret && same && cfg.has_credentials ? "Saved: type a new one to replace it" : (f.placeholder || ""),
    }))));
  // Dropdowns the tracker fills (organizations, boards…) load once what
  // they depend on is there: the saved credentials, or the ones typed in.
  board.loads = {};
  if (fields.some((f) => f.dynamic)) loadBoardOptions(fields.filter((f) => f.dynamic).map((f) => f.name));
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

// boardSelect is a select field: its fixed options, or (dynamic) the saved
// value until the tracker's choices load.
function boardSelect(f, value) {
  const el = h("select", { "data-board-field": f.name, "data-dynamic": f.dynamic ? "1" : "" });
  const opts = f.dynamic ? (value ? [{ value, label: "Loading…" }] : [{ value: "", label: "Enter the credentials first" }]) : (f.options || []);
  el.replaceChildren(...opts.map((o) => h("option", { value: o.value }, o.label || o.value)));
  el.value = value;
  return el;
}

const boardField = (name) => $("#board-fields").querySelector(`[data-board-field="${name}"]`);

// loadBoardOptions loads the choices of dynamic fields, then of the ones
// depending on them, keeping each field's value when it is still a choice.
async function loadBoardOptions(names) {
  const name = $("#board-backend").value;
  const fields = boardReg(name)?.fields || [];
  const seq = (board.loadSeq = (board.loadSeq || 0) + 1);
  const p = state.project;
  for (const f of fields.filter((x) => x.dynamic && names.includes(x.name))) {
    const el = boardField(f.name);
    if (!el) continue;
    const keep = el.value;
    let res;
    try {
      res = await api("POST", `/api/projects/${p.id}/tracker/options?field=${encodeURIComponent(f.name)}`, boardBody({ partial: true }));
    } catch (e) {
      res = { ok: false, error: e.message };
    }
    if (seq !== board.loadSeq || projectKey(state.project) !== projectKey(p) || $("#board-backend").value !== name) return; // superseded
    if (!res.ok) {
      el.replaceChildren(h("option", { value: keep }, keep ? `${keep} (couldn't load: ${res.error})` : `Couldn't load: ${res.error}`));
      el.value = keep;
      el.title = res.error || "";
      continue;
    }
    el.title = "";
    const opts = res.options || [];
    const list = f.required && !opts.some((o) => o.value === "") ? [{ value: "", label: `Pick the ${(f.label || f.name).toLowerCase()}` }, ...opts] : opts;
    el.replaceChildren(...list.map((o) => h("option", { value: o.value }, o.label || o.value)));
    el.value = list.some((o) => o.value === keep) ? keep : (list[0]?.value ?? "");
  }
}

// A field changed: reload the dropdowns that depend on it, and theirs.
function boardFieldChanged(changed) {
  const fields = boardReg($("#board-backend").value)?.fields || [];
  const todo = new Set();
  const walk = (n) => fields.filter((f) => f.dynamic && (f.depends_on || []).includes(n)).forEach((f) => { if (!todo.has(f.name)) { todo.add(f.name); walk(f.name); } });
  walk(changed);
  if (todo.size) loadBoardOptions(fields.map((f) => f.name).filter((n) => todo.has(n)));
  if (changed === "widget_common_id") loadBoardColumns();
}

// Column names Dash looks for when it maps a board's columns by name.
const COLUMN_GUESS = {
  draft: ["backlog", "draft", "ideas", "inbox"],
  queued: ["todo", "to do", "ready", "planned", "next", "queued", "selected"],
  in_progress: ["doing", "in progress", "wip", "ongoing", "running", "started"],
  review: ["review", "in review", "code review", "testing", "qa"],
  merging: ["merging"],
  merged: ["done", "merged", "complete", "completed", "finished", "shipped"],
  failed: ["failed", "blocked", "stuck"],
};
const colKey = (s) => s.toLowerCase().replace(/[^\p{L}\p{N}]/gu, "");

// mapByName fills the empty (or default) rows of the mapping with the
// board's columns whose names match.
function mapByName(columns, force) {
  const reg = boardReg($("#board-backend").value) || {};
  const defaults = reg.default_status_map || {};
  const byKey = new Map(columns.map((c) => [colKey(c.name), c.id]));
  for (const el of $("#board-map").querySelectorAll("input")) {
    const s = el.dataset.boardStatus;
    const cur = el.value.trim();
    if (!force && cur && !(cur === defaults[s] && !byKey.has(colKey(cur)))) continue;
    const hit = (COLUMN_GUESS[s] || []).map(colKey).find((k) => byKey.has(k));
    el.value = hit ? byKey.get(hit) : (byKey.has(colKey(cur)) ? cur : "");
  }
}

// loadBoardColumns reads the picked board's columns for the mapping.
async function loadBoardColumns() {
  const p = state.project;
  let res;
  try { res = await api("POST", `/api/projects/${p.id}/tracker/test`, boardBody({ partial: true })); } catch { return; }
  if (projectKey(state.project) !== projectKey(p) || !res.ok) return;
  showBoardColumns(res.columns || []);
}

function showBoardColumns(columns) {
  $("#board-columns").replaceChildren(...columns.map((c) => h("option", { value: c.id }, c.name)));
  if (columns.length) mapByName(columns, false);
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
    if (boardReg(cfg.backend)?.auto_webhook) {
      bits.push(st.webhook_url
        ? `Dash registers its webhook on the board itself (${st.webhook_url}), so changes arrive at once.`
        : "This Dash has no public URL, so it can't get webhooks: it reads the board every minute.");
    }
  }
  const el = $("#board-status");
  el.hidden = !bits.length;
  el.textContent = bits.join(" ");
  el.classList.toggle("err-text", external && !!cfg.last_error);
  $("#board-events").replaceChildren(...(st.events || []).slice(0, 5).map((ev) =>
    h("li", { class: "runner-row" }, h("span", { class: "runner-name" }, ev.text), h("span", { class: "runner-seen hint" }, agoText(ev.at)))));
}

// boardBody is the form as the API takes it (partial: for loading choices,
// the form needn't be complete).
function boardBody({ partial = false } = {}) {
  const name = $("#board-backend").value;
  if (boardReg(name)?.native) return { backend: name };
  let config = {};
  const raw = $("#board-config-raw").value.trim();
  if (!$("#board-config-raw-row").hidden && raw) {
    try { config = JSON.parse(raw); } catch (e) { if (!partial) throw new Error("The settings aren't valid JSON."); }
  }
  const credentials = {};
  for (const el of $("#board-fields").querySelectorAll("input, select")) {
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
  if (projectKey(state.project) !== projectKey(p)) return;
  board.state = res;
  $("#board-backend").value = res.config.backend || "dash";
  renderBoardForm();
  renderBoardStatus();
  toast("Board saved", { tone: "ok" });
}

$("#board-backend").addEventListener("change", () => renderBoardForm());
$("#board-fields").addEventListener("change", (e) => {
  const name = e.target.dataset?.boardField;
  if (name) boardFieldChanged(name);
});
$("#board-save").addEventListener("click", attempt((e) => busy(e.currentTarget, saveBoard)));
$("#board-test").addEventListener("click", attempt((e) => busy(e.currentTarget, async () => {
  const res = await api("POST", `/api/projects/${state.project.id}/tracker/test`, boardBody());
  if (!res.ok) throw new Error(`Connection failed: ${res.error}`);
  // The tracker's columns, to pick from in the mapping (and to map by name).
  showBoardColumns(res.columns || []);
  toast(res.columns?.length ? `Connected: ${res.columns.length} columns found` : "Connected", { tone: "ok" });
})));
$("#board-sync").addEventListener("click", attempt((e) => busy(e.currentTarget, async () => {
  const res = await api("POST", `/api/projects/${state.project.id}/tracker/sync`);
  if (res.state) { board.state = res.state; renderBoardStatus(); }
  if (!res.ok) throw new Error(`Sync failed: ${res.error}`);
  toast("Synced", { tone: "ok" });
})));
$("#board-secret").addEventListener("click", attempt(async () => {
  if (board.state.config.has_webhook_secret && !await ask({ title: "Make a new webhook secret?", body: "The current one stops working at once: update the tracker's webhook with the new one.", ok: "New secret" })) return;
  const res = await api("POST", `/api/projects/${state.project.id}/tracker/webhook-secret`);
  board.state.config.has_webhook_secret = true;
  const el = $("#board-webhook");
  if (boardReg(board.state.config.backend)?.auto_webhook && res.url) {
    // The backend registers its own webhook: the restarted sync did it.
    el.replaceChildren(h("p", {}, "New secret made: Dash registered the board's webhook again with it."));
    el.hidden = false;
    return;
  }
  el.replaceChildren(
    h("p", {}, "Point the tracker's webhook at this address, with this secret. Copy it now: Dash doesn't show it again."),
    copyBlock(res.url || location.origin + res.path, "Address copied", { pre: true }),
    copyBlock(res.secret, "Secret copied", { pre: true }),
    h("p", { class: "hint" }, "Trackers without signed webhooks send it as the ", h("code", {}, "X-Dash-Tracker-Secret"), " header or a ", h("code", {}, "?secret="), " parameter."));
  el.hidden = false;
}));
