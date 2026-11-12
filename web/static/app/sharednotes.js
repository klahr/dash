import { $, h } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, busy, toast } from "./dialogs.js";
import { state } from "./state.js";
import { isHub } from "./projects.js";
import { agoText } from "./branches.js";
import { copyBlock } from "./prefs.js";

// ------------------------------------------------------------------ shared notes
// A hub project's shared notes, in its settings. Notes are for runners
// only: this page gets counts from the project's online runners (through
// the hub), never a note, and holds no key. Removing sends a command; each
// runner applies it to its own copy and answers how many notes it removed.
const KINDS = {
  all: "All notes",
  task: "Notes written by a task",
  runner: "Notes written by a runner",
  expired: "Expired notes",
};
const notes = { view: null };

const size = (n) => n < 1024 ? `${n} B` : n < 1 << 20 ? `${Math.round(n / 1024)} KiB` : `${(n / (1 << 20)).toFixed(1)} MiB`;
const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
const sameVector = (a = {}, b = {}) => {
  const ka = Object.keys(a), kb = Object.keys(b);
  return ka.length === kb.length && ka.every((k) => a[k] === b[k]);
};

export async function loadSharedNotes() {
  const p = state.project;
  const field = $("#settings-notes-field");
  $("#notes-remove-result").hidden = true;
  if (!p || p.prefix || !isHub()) { field.hidden = true; return; }
  let v;
  try {
    v = await api("GET", `/api/projects/${p.id}/notes`);
  } catch {
    field.hidden = true; // e.g. an older hub
    return;
  }
  if (state.project !== p) return;
  notes.view = v;
  field.hidden = false;
  renderSharedNotes();
}

function renderSharedNotes() {
  const v = notes.view;
  const el = $("#settings-notes");
  $("#notes-remove-field").hidden = !!v.why;
  if (v.why) return el.replaceChildren(h("p", { class: "hint" }, v.why));
  const online = v.runners.filter((r) => r.status);
  // The project key now: the latest epoch any runner seals with.
  let current = null;
  for (const { status: s } of online) {
    if (s.key_id && (!current || s.key_epoch > current.epoch || s.key_epoch === current.epoch && s.key_id > current.id)) current = { id: s.key_id, epoch: s.key_epoch };
  }
  const out = [];
  if (!v.online) {
    out.push(h("p", { class: "hint" }, "No runner of this project is online, so there are no counts to show. They appear once one connects."));
  } else if (online.length > 1) {
    const [a, ...rest] = online;
    const agree = rest.every((r) => r.status.notes === a.status.notes && sameVector(r.status.vector, a.status.vector));
    out.push(h("p", { class: `hint${agree ? "" : " warn"}` }, agree
      ? "The online runners' copies agree."
      : "The online runners' copies differ for now. They catch up with each other on the next write, and at least every 15 minutes."));
  }
  out.push(h("ul", { class: "item-list" }, ...v.runners.map((r) => runnerRow(r, current))));
  const waiting = waitingRows(v.runners);
  if (waiting.length) {
    out.push(h("h4", { class: "section-head" }, "Waiting for approval"),
      h("p", { class: "hint" }, "These runners asked for the project's key. Approve one on the runner named, after checking its fingerprint matches the one ", h("code", {}, "dash runner notes"), " shows on the runner itself. This page can't vouch for it: the hub relays the keys."),
      h("ul", { class: "item-list" }, ...waiting));
  }
  if (v.pending.length) {
    out.push(h("h4", { class: "section-head" }, "Waiting for a runner"),
      h("ul", { class: "item-list" }, ...v.pending.map(pendingRow)));
  }
  el.replaceChildren(...out);
  renderRemoveForm();
}

function runnerRow(r, current) {
  const st = r.status;
  let line;
  if (!r.online) line = h("span", { class: "item-sub hint" }, "offline");
  else if (!st) line = h("span", { class: "item-sub hint err-text", title: r.error || "" }, `no answer${r.error ? `: ${r.error}` : ""}`);
  else {
    const bits = [plural(st.notes, "note"), size(st.bytes)];
    if (st.expired) bits.push(`${st.expired} expired`);
    bits.push(st.newest ? `last write ${agoText(st.newest)}` : "nothing written yet");
    if (st.stale) bits.push("waits for a full resync");
    line = h("span", { class: "item-sub hint" }, bits.join(" · "));
  }
  let key = null;
  if (st) {
    const holds = st.key_id && current && st.key_id === current.id;
    key = h("span", { class: `item-sub hint${holds ? "" : " warn"}` }, !st.key_id ? "no project key yet"
      : holds ? `holds the current project key (${st.key_id.slice(0, 8)}, epoch ${st.key_epoch})`
        : `holds an older project key (epoch ${st.key_epoch}): it gets the current one from an approved runner`);
  }
  const fp = st?.fingerprint || r.fingerprint;
  return h("li", { class: "item-row" },
    h("span", { class: `runner-dot ${r.online ? "on" : ""}`, "aria-hidden": "true" }),
    h("span", { class: "runner-name" }, r.name),
    fp ? h("code", { class: "runner-fingerprint", title: "This runner's key fingerprint" }, fp) : null,
    line, key);
}

// waitingRows are the runners waiting for approval, on the runner that
// would approve them (each runner approves its peers itself).
function waitingRows(runners) {
  const rows = [];
  for (const r of runners) {
    for (const w of r.status?.waiting || []) {
      const who = runners.find((x) => x.id === w.runner)?.name || w.name || w.runner;
      rows.push(h("li", { class: "item-row" },
        h("span", { class: "runner-name" }, who),
        h("span", { class: "hint" }, ` waits on ${r.name} · asked ${agoText(w.at)}`),
        h("code", { class: "runner-fingerprint" }, w.fingerprint),
        h("span", { class: "item-sub hint" }, `On ${r.name}, run:`),
        copyBlock(`dash runner notes approve ${w.fingerprint}`, "Command copied", { pre: true })));
    }
  }
  return rows;
}

function removalText(x) {
  if (x.kind === "task") {
    const t = state.todos.get(x.task);
    return `${KINDS.task}: ${t ? t.title || t.prompt?.slice(0, 60) || x.task : x.task}`;
  }
  if (x.kind === "runner") return `${KINDS.runner}: ${notes.view.runners.find((r) => r.id === x.runner)?.name || x.runner}`;
  return KINDS[x.kind] || x.kind;
}

function pendingRow(x) {
  const p = state.project;
  return h("li", { class: "item-row" },
    h("span", { class: "runner-name" }, removalText(x)),
    h("span", { class: "item-sub hint" }, `asked ${agoText(x.before)}; goes to the first of the project's runners that connects`),
    h("button", { class: "btn small", type: "button", onclick: attempt(async () => {
      if (!await ask({ title: "Cancel this removal?", body: "No runner has it yet: the notes stay.", ok: "Cancel removal" })) return;
      await api("DELETE", `/api/projects/${p.id}/notes/pending/${x.id}`);
      await loadSharedNotes();
    }) }, "Cancel"));
}

function renderRemoveForm() {
  const v = notes.view;
  const tasks = [...state.todos.values()].filter((t) => t.project_id === state.project?.id)
    .sort((a, b) => String(b.created_at || "").localeCompare(String(a.created_at || "")));
  const taskSel = $("#notes-remove-task"), runnerSel = $("#notes-remove-runner");
  const keepTask = taskSel.value, keepRunner = runnerSel.value;
  taskSel.replaceChildren(...tasks.map((t) => h("option", { value: t.id }, t.title || t.prompt?.slice(0, 60) || t.id)));
  runnerSel.replaceChildren(...v.runners.map((r) => h("option", { value: r.id }, r.name)));
  if (tasks.some((t) => t.id === keepTask)) taskSel.value = keepTask;
  if (v.runners.some((r) => r.id === keepRunner)) runnerSel.value = keepRunner;
  syncRemoveKind();
}

function syncRemoveKind() {
  const kind = $("#notes-remove-kind").value;
  $("#notes-remove-task-row").hidden = kind !== "task";
  $("#notes-remove-runner-row").hidden = kind !== "runner";
  $("#notes-remove").disabled = kind === "task" && !$("#notes-remove-task").options.length;
}
$("#notes-remove-kind").addEventListener("change", syncRemoveKind);

$("#notes-remove").addEventListener("click", attempt((e) => busy(e.currentTarget, async () => {
  const p = state.project, v = notes.view;
  const kind = $("#notes-remove-kind").value;
  const req = { kind };
  if (kind === "task") req.task = $("#notes-remove-task").value;
  if (kind === "runner") req.runner = $("#notes-remove-runner").value;
  const what = removalText(req);
  const body = `${what}. ` + (v.online
    ? "Each runner removes them from its own copy, for good. Runners offline now remove them when they catch up."
    : "No runner of the project is online: the first one that connects removes them. Until then you can cancel it.");
  if (!await ask({ title: "Remove shared notes?", body, ok: "Remove", danger: true })) return;
  const res = await api("POST", `/api/projects/${p.id}/notes/remove`, req);
  if (state.project !== p) return;
  const list = $("#notes-remove-result");
  list.replaceChildren(...res.results.map((x) => h("li", { class: "item-row" },
    h("span", { class: "runner-name" }, x.name),
    x.error ? h("span", { class: "item-sub hint err-text" }, `failed: ${x.error}`) : h("span", { class: "item-sub hint" }, `removed ${plural(x.removed, "note")}`))));
  list.hidden = !res.results.length;
  if (res.pending) toast("No runner is online: the removal waits for the first that connects");
  else toast(`Removed ${plural(res.results.reduce((n, x) => n + x.removed, 0), "note")}`, { tone: "ok" });
  await loadSharedNotes();
  list.hidden = !res.results.length;
})));
