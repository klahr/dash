import { $, h } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { copyBlock } from "./prefs.js";
import { agoText } from "./scans.js";

// ------------------------------------------------------------------ notification webhooks
// Outgoing webhooks: a project's, in its settings, and the user's personal
// ones (every project of their org), from the account menu. They save as
// you go, not with the settings form. Each row can show its recent
// deliveries.
const WEBHOOK_EVENTS = {
  "task.review": "Ready for review",
  "task.failed": "Failed",
  "task.merged": "Merged",
  "permission.needed": "Claude needs approval",
  "build.failing": "Build failing",
  "checkout.behind": "Checkout left behind origin",
  "webhook.test": "Test",
};
const WEBHOOK_FORMATS = { json: "JSON", slack: "Slack", ntfy: "ntfy" };
// A scope is one list of webhooks: where it shows, and where it's read and added.
const hookScopes = {
  project: { list: [], info: null, el: "#settings-webhooks", hint: "#settings-webhooks-hint", secret: "#webhook-secret",
    path: () => `/api/projects/${state.project.id}/webhooks`, owner: () => state.project },
  personal: { list: [], info: null, el: "#me-webhooks", hint: "#me-webhooks-hint", secret: "#me-webhook-secret",
    path: () => "/api/me/webhooks", owner: () => "me", personal: true },
};
// editing: the webhook in the edit sheet (null: a new one), in scope;
// history: webhook id → its deliveries, for the rows showing them.
const hooks = { editing: null, scope: hookScopes.project, history: new Map() };
const webhookHost = (u) => { try { return new URL(u).host; } catch { return u; } };

export async function loadWebhooks(sc = hookScopes.project) {
  const owner = sc.owner();
  let res;
  try {
    res = await api("GET", sc.path());
  } catch (e) {
    if (sc.personal) throw e;
    $("#settings-webhooks-field").hidden = true; // e.g. an older Dash on another computer
    return;
  }
  if (sc.owner() !== owner) return;
  sc.list = res.webhooks;
  sc.info = res;
  if (!sc.personal) $("#settings-webhooks-field").hidden = false;
  renderWebhooks(sc);
}

async function openMyWebhooks() {
  const sc = hookScopes.personal;
  $(sc.secret).hidden = true;
  const org = state.me?.org?.name;
  $("#me-webhooks-intro").textContent = `Your own notification webhooks: Slack, ntfy or your own service hear about every project of ${org || "this organization"}, for as long as you are a member. Only you see them. A project's own webhooks are in its settings.`;
  await loadWebhooks(sc);
  const d = $("#me-webhooks-sheet");
  if (!d.open) d.showModal();
}

function webhookStatus(w) {
  if (!w.last_at) return h("span", { class: "runner-seen hint" }, "nothing sent yet");
  const what = WEBHOOK_EVENTS[w.last_event] || w.last_event;
  if (w.last_error) return h("span", { class: "runner-seen hint err-text", title: w.last_error }, `${what} failed ${agoText(w.last_at)}: ${w.last_error}`);
  return h("span", { class: "runner-seen hint" }, `${what} delivered ${agoText(w.last_at)}`);
}

// webhookHistory is a webhook's recent deliveries, newest first, under its row.
function webhookHistory(w, sc) {
  const ds = hooks.history.get(w.id);
  if (!ds) return h("li", { class: "webhook-history hint" }, "Loading…");
  if (!ds.length) return h("li", { class: "webhook-history hint" }, "Nothing sent yet.");
  return h("li", { class: "webhook-history", "aria-label": `Deliveries to ${w.name || webhookHost(w.url)}` },
    h("ol", {}, ...ds.map((d) => {
      const about = [sc.personal || !d.project_id || d.project_id !== state.project?.id ? d.project : "", d.subject].filter(Boolean).join(" · ");
      const result = [d.error ? "failed" : "delivered", d.status ? `HTTP ${d.status}` : "", d.attempts > 1 ? `${d.attempts} tries` : "", `${d.duration_ms} ms`].filter(Boolean).join(" · ");
      return h("li", {},
        h("time", { class: "wh-when", datetime: d.at, title: new Date(d.at).toLocaleString() }, agoText(d.at)),
        h("span", { class: "wh-what" }, WEBHOOK_EVENTS[d.event] || d.event, about ? h("span", { class: "hint" }, ` · ${about}`) : null),
        h("span", { class: `wh-result ${d.error ? "err-text" : "hint"}` }, result),
        d.error ? h("span", { class: "wh-error hint err-text" }, d.error) : null);
    })));
}

async function loadWebhookHistory(w, sc) {
  const res = await api("GET", `/api/webhooks/${w.id}/deliveries`);
  if (!hooks.history.has(w.id)) return; // closed meanwhile
  hooks.history.set(w.id, res.deliveries);
  renderWebhooks(sc);
}

function renderWebhooks(sc = hookScopes.project) {
  const hint = $(sc.hint);
  const notes = [];
  if (sc.info && !sc.info.enabled) notes.push("This Dash doesn't send notifications (a runner: its hub does).");
  if (sc.info && sc.info.enabled && !sc.info.links) notes.push("Set -public-url so notifications link back to the task.");
  hint.hidden = !notes.length;
  hint.textContent = notes.join(" ");
  const owner = sc.owner();
  const done = (w) => {
    sc.list = sc.list.map((x) => (x.id === w.id ? w : x));
    if (hooks.history.has(w.id)) loadWebhookHistory(w, sc).catch(fail);
    renderWebhooks(sc);
  };
  const rows = [];
  for (const w of sc.list) {
    const open = hooks.history.has(w.id);
    rows.push(h("li", { class: "runner-row webhook-row" },
      h("input", { type: "checkbox", checked: w.enabled, title: w.enabled ? "On: click to pause" : "Paused: click to turn on", "aria-label": `${w.name || webhookHost(w.url)} on`,
        onchange: attempt(async (e) => done(await api("PATCH", `/api/webhooks/${w.id}`, { enabled: e.target.checked }))) }),
      h("span", { class: "runner-name" }, w.name || webhookHost(w.url), h("span", { class: "hint" }, ` · ${WEBHOOK_FORMATS[w.format] || w.format}${w.enabled ? "" : " · paused"}`)),
      webhookStatus(w),
      h("span", { class: "webhook-actions" },
        h("button", { class: "btn small", type: "button", onclick: attempt(async (e) => {
          e.target.disabled = true;
          try {
            const res = await api("POST", `/api/webhooks/${w.id}/test`);
            if (res.webhook) done(res.webhook);
            if (res.ok) toast("Test notification delivered", { tone: "ok" });
            else throw new Error(`Test failed: ${res.error}`);
          } finally { e.target.disabled = false; }
        }) }, "Send test"),
        h("button", { class: "btn small", type: "button", "aria-expanded": String(open), onclick: attempt(async () => {
          if (open) { hooks.history.delete(w.id); renderWebhooks(sc); return; }
          hooks.history.set(w.id, null);
          renderWebhooks(sc);
          await loadWebhookHistory(w, sc);
        }) }, open ? "Hide history" : "History"),
        h("button", { class: "btn small", type: "button", onclick: () => editWebhook(w, sc) }, "Edit"),
        w.format === "json" ? h("button", { class: "btn small", type: "button", onclick: attempt(async () => {
          if (!await ask({ title: "Rotate the signing secret?", body: "The current secret stops working at once: update your receiver with the new one.", ok: "Rotate" })) return;
          const res = await api("POST", `/api/webhooks/${w.id}/rotate`);
          showWebhookSecret(res.secret, sc);
        }) }, "Rotate secret") : null,
        h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
          if (!await ask({ title: `Delete ${w.name || webhookHost(w.url)}?`, body: "Dash stops sending notifications to it, and forgets its history.", ok: "Delete", danger: true })) return;
          await api("DELETE", `/api/webhooks/${w.id}`);
          sc.list = sc.list.filter((x) => x.id !== w.id);
          hooks.history.delete(w.id);
          if (sc.owner() === owner) renderWebhooks(sc);
        }) }, "Delete"))));
    if (open) rows.push(webhookHistory(w, sc));
  }
  $(sc.el).replaceChildren(...(rows.length ? rows : [h("li", { class: "hint" }, "No webhooks yet.")]));
}

function showWebhookSecret(secret, sc = hookScopes.project) {
  const el = $(sc.secret);
  el.replaceChildren(
    h("p", {}, "Copy the signing secret now: Dash doesn't show it again."),
    copyBlock(secret, "Secret copied", { pre: true }),
    h("p", { class: "hint" }, "Each delivery has a ", h("code", {}, "Dash-Signature: t=<unix time>,v1=<signature>"),
      " header: the hex HMAC-SHA256 of ", h("code", {}, "<t>.<body>"), " with this secret. Check it, and that t is recent, before trusting the request."));
  el.hidden = false;
}

function editWebhook(w = null, sc = hookScopes.project) {
  hooks.editing = w;
  hooks.scope = sc;
  const f = $("#webhook-form");
  $("#webhook-title").textContent = `${w ? "Edit" : "Add"}${sc.personal ? " personal" : ""} webhook`;
  f.format.value = w?.format || "slack";
  f.url.value = w?.url || "";
  f.name.value = w?.name || "";
  f.token.value = "";
  f.token_clear.checked = false;
  f.enabled.checked = w ? w.enabled : true;
  // A runner's checkout left behind origin is opt-in: it matters to whoever sits at that runner.
  const on = new Set(w ? w.events : Object.keys(WEBHOOK_EVENTS).filter((e) => e !== "checkout.behind"));
  $("#webhook-events").replaceChildren(...(sc.info?.events || Object.keys(WEBHOOK_EVENTS).filter((e) => e !== "webhook.test")).map((e) =>
    h("label", { class: "toggle" }, h("input", { type: "checkbox", name: "event", value: e, checked: on.has(e) }), h("span", {}, WEBHOOK_EVENTS[e] || e))));
  syncWebhookForm();
  $("#webhook-sheet").returnValue = "";
  $("#webhook-sheet").showModal();
}

function syncWebhookForm() {
  const f = $("#webhook-form");
  const fmt = f.format.value;
  const w = hooks.editing;
  const info = hooks.scope.info;
  $("#webhook-token-row").hidden = fmt !== "ntfy" && !w?.has_token;
  $("#webhook-token-clear-row").hidden = !w?.has_token;
  f.token.placeholder = w?.has_token ? "Saved: type a new one to replace it" : "";
  f.url.placeholder = { slack: "https://hooks.slack.com/services/…", ntfy: "https://ntfy.sh/my-dash-topic", json: "https://example.com/dash-webhook" }[fmt];
  const priv = info && !info.allow_private ? " Private and local network addresses aren't allowed on this Dash." : "";
  $("#webhook-format-hint").textContent = {
    slack: "A short message with a link to the task.",
    ntfy: "Approvals and failures arrive with high priority; tapping opens the task. Approval requests get Approve and Deny buttons that work for 15 minutes.",
    json: "Dash shows the signing secret once, after you save.",
  }[fmt] + priv;
}
$("#webhook-form").addEventListener("change", (e) => { if (e.target.name === "format") syncWebhookForm(); });
$("#webhook-add").addEventListener("click", () => editWebhook(null, hookScopes.project));
$("#me-webhook-add").addEventListener("click", () => editWebhook(null, hookScopes.personal));
$("#me-webhooks-open").addEventListener("click", attempt(openMyWebhooks));
$("#webhook-sheet").addEventListener("close", attempt(async () => {
  if ($("#webhook-sheet").returnValue !== "save") return;
  const f = $("#webhook-form");
  const w = hooks.editing;
  const sc = hooks.scope;
  const body = {
    format: f.format.value,
    url: f.url.value.trim(),
    name: f.name.value.trim(),
    enabled: f.enabled.checked,
    events: [...f.querySelectorAll('input[name="event"]:checked')].map((x) => x.value),
  };
  if (f.token.value.trim()) body.token = f.token.value.trim();
  else if (f.token_clear.checked) body.token = "";
  try {
    if (w) {
      const saved = await api("PATCH", `/api/webhooks/${w.id}`, body);
      sc.list = sc.list.map((x) => (x.id === saved.id ? saved : x));
      toast("Webhook saved");
    } else {
      const res = await api("POST", sc.path(), body);
      sc.list = [...sc.list, res.webhook];
      if (res.webhook.format === "json") showWebhookSecret(res.secret, sc);
      toast("Webhook added: try Send test", { tone: "ok" });
    }
  } catch (e) {
    $("#webhook-sheet").showModal(); // keep what was typed
    throw e;
  }
  renderWebhooks(sc);
}));
