import { $, dateTimeText, fill, h } from "./dom.js";
import { api } from "./api.js";
import { attempt, busy, toast } from "./dialogs.js";
import { state } from "./state.js";
import { accountSection, openAccount } from "./accountsheet.js";
import { WEBHOOK_EVENTS, WEBHOOK_FORMATS, webhookStatus } from "./webhooks.js";

// ------------------------------------------------------------------ Account → Notifications
// The user's notification settings in their org, kept by the hub (GET/PUT
// /api/me/notifications): a daily or weekly summary (when, of which
// projects, where it goes, what is in it), whose tasks they hear about,
// which events push to their browsers, and their personal webhooks, which
// get each event as it happens (edited in My webhooks, not here).
const PUSH_LABELS = {
  "task.review": "A task is ready for review",
  "task.failed": "A task failed",
  "permission.needed": "Claude needs your approval",
  "goal.attention": "A goal needs you, is done or out of budget",
  "build.failing": "The build starts failing",
  "runner.disk": "A runner's disk is nearly full",
  "issue.new": "A scan or Claude finds a new issue",
};
const INCLUDE = ["issues", "review", "failed", "merged", "goals", "builds"];
const browserZone = () => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"; } catch { return "UTC"; } };
const hookName = (w) => w.name || (() => { try { return new URL(w.url).host; } catch { return w.url; } })();
const form = $("#notify-form");
let view = null; // the last GET/PUT answer

accountSection("notifications", loadNotify);
async function loadNotify() {
  view = await api("GET", "/api/me/notifications");
  renderNotify();
}

function renderNotify() {
  const s = view.settings;
  const org = state.me?.org?.name;
  $("#notify-intro").textContent = `Kept for you in ${org || "this organization"}, on every device you sign in on.`;
  const off = $("#notify-off");
  off.hidden = view.enabled;
  off.textContent = view.enabled ? "" : "This Dash doesn't send summaries (a runner: its hub does).";
  form.summary.value = s.summary;
  form.weekday.value = String(s.weekday);
  form.time.value = s.time;
  form.scope.value = s.all_projects ? "all" : "some";
  form.tasks.value = s.tasks === "all" ? "all" : "mine";
  const picked = new Set(s.projects);
  fill($("#notify-projects"), view.projects.length
    ? view.projects.map((p) => h("label", { class: "toggle" }, h("input", { type: "checkbox", name: "project", value: p.id, checked: picked.has(p.id) }), h("span", {}, p.name)))
    : h("p", { class: "hint" }, "No projects yet."));
  // Email: greyed out when this Dash can't send it.
  form.email.checked = s.email && view.email_available;
  form.email.disabled = !view.email_available;
  $("#notify-email").textContent = view.email ? `to ${view.email}` : "";
  $("#notify-email-note").hidden = view.email_available;
  $("#notify-email-note").textContent = view.email_note || "";
  const dests = new Set(s.webhooks);
  fill($("#notify-dests"), view.webhooks.length
    ? view.webhooks.map((w) => h("label", { class: "toggle notify-dest-hook" },
      h("input", { type: "checkbox", name: "dest", value: w.id, checked: dests.has(w.id) }),
      h("span", {}, hookName(w), h("em", {}, ` · ${WEBHOOK_FORMATS[w.format] || w.format}${w.enabled ? "" : " · paused"}`))))
    : h("p", { class: "hint" }, "No webhooks yet: add one in ", h("button", { type: "button", class: "link inline", "data-account": "webhooks" }, "My webhooks"), " to get the summary in Slack or ntfy."));
  for (const k of INCLUDE) form[k].checked = !!s.include[k];
  form.min_severity.value = s.include.min_severity || "medium";
  const push = new Set(s.push);
  fill($("#notify-push"), view.push_choices.map((e) => h("label", { class: "toggle" },
    h("input", { type: "checkbox", name: "push", value: e, checked: push.has(e) }), h("span", {}, PUSH_LABELS[e] || WEBHOOK_EVENTS[e] || e))));
  $("#notify-push-hint").hidden = false;
  state.pushEvents = push; // the page's own notifications follow it too (notifications.js)
  state.pushTasks = form.tasks.value;
  renderHooks();
  syncNotify();
  renderState();
}

// syncNotify shows what applies to the choices made so far.
function syncNotify() {
  const on = form.summary.value !== "off";
  $("#notify-on").hidden = !on;
  $("#notify-weekday-row").hidden = form.summary.value !== "weekly";
  $("#notify-projects").hidden = form.scope.value !== "some";
  form.min_severity.disabled = !form.issues.checked;
  $("#notify-test").hidden = !on && !form.email.checked && !form.querySelector('input[name="dest"]:checked');
  const zone = browserZone();
  const saved = view?.settings.time_zone;
  $("#notify-tz").textContent = saved && saved !== zone && view.settings.summary !== "off"
    ? `In your time zone, ${zone}. It was set in ${saved}: saving moves it to ${zone}.`
    : `In your time zone, ${zone} (from this browser).`;
}

function renderState() {
  const { settings: s, state: st } = view;
  const parts = [];
  if (s.summary !== "off" && st.next_at) parts.push(`Next summary: ${dateTimeText(st.next_at)}.`);
  if (st.last_sent) parts.push(`Last sent ${dateTimeText(st.last_sent)}.`);
  $("#notify-state").textContent = parts.join(" ");
  $("#notify-state").hidden = !parts.length;
}

// renderHooks lists the user's personal webhooks, as My webhooks does, without its editor.
function renderHooks() {
  fill($("#notify-hooks"), view.webhooks.length
    ? view.webhooks.map((w) => h("li", { class: "item-row" },
      h("span", { class: "runner-name" }, hookName(w), h("span", { class: "hint" }, ` · ${WEBHOOK_FORMATS[w.format] || w.format}${w.enabled ? "" : " · paused"}`)),
      h("span", { class: "item-sub hint" }, w.events.length ? w.events.map((e) => WEBHOOK_EVENTS[e] || e).join(", ") : "no events (summary only)"),
      webhookStatus(w)))
    : h("li", { class: "hint" }, "No webhooks yet."));
}

function collect() {
  const picks = (name) => [...form.querySelectorAll(`input[name="${name}"]:checked`)].map((x) => x.value);
  const include = { min_severity: form.min_severity.value };
  for (const k of INCLUDE) include[k] = form[k].checked;
  return {
    summary: form.summary.value,
    time: form.time.value,
    weekday: Number(form.weekday.value),
    time_zone: browserZone(),
    all_projects: form.scope.value === "all",
    projects: picks("project"),
    email: form.email.checked && !form.email.disabled,
    webhooks: picks("dest"),
    include,
    push: picks("push"),
    tasks: form.tasks.value || "mine",
  };
}

function check(body) {
  if (body.summary === "off") return;
  if (!body.time) throw new Error("Choose the time the summary goes out.");
  if (!body.email && !body.webhooks.length) throw new Error("Choose where the summary goes: email or one of your webhooks.");
  if (!body.all_projects && !body.projects.length) throw new Error("Pick the projects the summary covers, or All projects.");
}

async function save() {
  const body = collect();
  check(body);
  view = await api("PUT", "/api/me/notifications", body);
  renderNotify();
}

form.addEventListener("change", (e) => {
  if (e.target.name === "push" || e.target.name === "summary" || e.target.name === "scope" || e.target.name === "issues" || e.target.name === "email" || e.target.name === "dest") syncNotify();
});
form.addEventListener("submit", attempt(async (e) => {
  e.preventDefault();
  await busy($("#notify-save"), save);
  toast("Notification settings saved", { tone: "ok" });
}));
$("#notify-test").addEventListener("click", attempt((e) => busy(e.currentTarget, async () => {
  await save(); // what's on the page is what goes out
  const res = await api("POST", "/api/me/notifications/test-summary");
  view = await api("GET", "/api/me/notifications");
  renderNotify();
  const failed = res.results.filter((r) => !r.ok);
  if (!failed.length) toast(`Summary sent to ${res.results.map((r) => r.name).join(", ")}`, { tone: "ok" });
  else throw new Error(`Not sent to ${failed.map((r) => `${r.name}: ${r.error}`).join("; ")}`);
})));
// Links between the Account sheet's sections: data-account="<section>".
document.addEventListener("click", (e) => {
  const b = e.target.closest?.("[data-account]");
  if (!b) return;
  e.preventDefault();
  attempt(() => openAccount(b.dataset.account))();
});
