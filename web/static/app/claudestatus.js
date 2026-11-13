import { $, dateTimeText, fill, h, ico, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { accountSection } from "./accountsheet.js";
import { copyBlock } from "./prefs.js";
import { agoText } from "./branches.js";

// ------------------------------------------------------------------ Claude's status
// Claude's status page (status.claude.com) posts its incidents to this Dash's
// webhook, which a platform admin sets up in Account → Claude status. A pill
// beside the board shows while something is wrong; it opens the incidents.
// Always the hub's (this Dash's), whichever project is open.
const INDICATOR = { none: "All systems operational", minor: "Minor outage", major: "Partial outage", critical: "Major outage", maintenance: "Maintenance" };
const TONE = { none: "ok", minor: "warn", maintenance: "warn", major: "bad", critical: "bad" };
const IMPACT = { none: "No impact", minor: "Minor", major: "Major", critical: "Critical", maintenance: "Maintenance" };
const STATUS = {
  investigating: "Investigating", identified: "Identified", monitoring: "Monitoring", resolved: "Resolved", postmortem: "Postmortem",
  scheduled: "Scheduled", in_progress: "In progress", verifying: "Verifying", completed: "Completed",
};
const COMPONENT = {
  operational: "Operational", degraded_performance: "Degraded performance", partial_outage: "Partial outage",
  major_outage: "Major outage", under_maintenance: "Under maintenance",
};
const label = (map, k) => map[k] || (k || "").replaceAll("_", " ");
const isAdmin = () => !!state.me?.user?.is_platform_admin;

export async function loadClaudeStatus() {
  state.claudeStatus = await api("GET", "/api/claude-status", null, "");
  renderClaudeStatus();
}

// onClaudeStatus takes the status a live event brought: a new incident or
// an incident's end is news.
export function onClaudeStatus(st) {
  const before = state.claudeStatus;
  state.claudeStatus = st;
  renderClaudeStatus();
  if (!before?.enabled || !st?.enabled) return;
  const was = new Set((before.active || []).map((i) => i.id));
  const now = new Set((st.active || []).map((i) => i.id));
  const details = { label: "Details", run: openClaudeStatus };
  for (const i of st.active || []) {
    if (!was.has(i.id)) toast(i.updates?.[0]?.body || "", { tone: "error", head: `Claude: ${i.name}`, action: details });
  }
  for (const i of before.active || []) {
    if (!now.has(i.id)) toast(i.name, { tone: "ok", head: "Claude incident resolved", action: details });
  }
}

const troubled = (st) => !!st?.enabled && ((st.indicator && st.indicator !== "none") || st.active?.length > 0 || st.components?.length > 0);

export function renderClaudeStatus() {
  const st = state.claudeStatus;
  const pill = $("#claude-pill");
  pill.hidden = !troubled(st);
  if (!pill.hidden) {
    const tone = TONE[st.indicator] || (st.active.length ? "warn" : "ok");
    const text = st.active.length === 1 ? `Claude: ${label(IMPACT, st.active[0].impact).toLowerCase()} incident`
      : st.active.length ? `Claude: ${plural(st.active.length, "incident")}`
      : st.indicator && st.indicator !== "none" ? `Claude: ${label(INDICATOR, st.indicator).toLowerCase()}`
      : `Claude: ${plural(st.components.length, "component")} affected`;
    pill.className = `usage-pill claude-pill claude-${tone}`;
    pill.textContent = text;
    pill.title = `${st.description || label(INDICATOR, st.indicator)} (status.claude.com)`;
  }
  if ($("#claude-status-sheet").open) renderSheet();
}

function updateList(i) {
  return h("ol", { class: "claude-updates" }, ...(i.updates || []).map((u) => h("li", {},
    h("span", { class: "claude-update-head" }, h("strong", {}, label(STATUS, u.status)), " ",
      h("time", { datetime: u.at, title: dateTimeText(u.at) }, agoText(u.at))),
    h("p", { class: "claude-update-body" }, u.body))));
}

function incidentHead(i) {
  return [
    i.url ? h("a", { class: "claude-incident-name", href: i.url, target: "_blank", rel: "noopener" }, i.name) : h("span", { class: "claude-incident-name" }, i.name),
    h("span", { class: "claude-incident-meta" },
      h("span", { class: `claude-impact claude-${TONE[i.impact] || "warn"}` }, label(IMPACT, i.impact)),
      ` ${label(STATUS, i.status)}`,
      i.components?.length ? ` · ${i.components.join(", ")}` : ""),
  ];
}

function renderSheet() {
  const st = state.claudeStatus;
  $("#claude-status-page").href = st?.page_url || "https://status.claude.com";
  $("#claude-status-sync").hidden = !isAdmin() || !st?.enabled;
  const summary = $("#claude-status-summary");
  if (!st?.enabled) {
    summary.className = "claude-summary";
    fill(summary, isAdmin() ? "This Dash doesn't follow Claude's status yet. Turn it on in Account → Claude status."
      : "This Dash doesn't follow Claude's status. A platform admin can turn it on.");
    fill($("#claude-status-body"));
    return;
  }
  const tone = TONE[st.indicator] || "muted";
  summary.className = `claude-summary claude-${tone}`;
  fill(summary, h("strong", {}, st.description || label(INDICATOR, st.indicator) || "Nothing heard yet"),
    st.updated ? h("span", { class: "hint" }, ` · updated ${agoText(st.updated)}`) : null);
  fill($("#claude-status-body"),
    st.components.length ? h("h3", { class: "section-head" }, "Affected components") : null,
    st.components.length ? h("ul", { class: "claude-components" }, ...st.components.map((c) =>
      h("li", { class: `claude-${c.status === "major_outage" || c.status === "partial_outage" ? "bad" : "warn"}` },
        h("span", {}, c.name), h("span", { class: "claude-component-status" }, label(COMPONENT, c.status))))) : null,
    h("h3", { class: "section-head" }, "Active incidents"),
    st.active.length ? h("ul", { class: "claude-incidents" }, ...st.active.map((i) =>
      h("li", { class: "claude-incident" }, ...incidentHead(i), updateList(i))))
      : h("p", { class: "hint" }, "None right now."),
    h("h3", { class: "section-head" }, "Resolved this week"),
    st.recent.length ? h("ul", { class: "claude-incidents" }, ...st.recent.map((i) =>
      h("li", { class: "claude-incident resolved" }, h("details", {},
        h("summary", {}, ...incidentHead(i),
          i.resolved ? h("span", { class: "hint claude-resolved" }, `Resolved ${dateTimeText(i.resolved)}`) : null),
        updateList(i)))))
      : h("p", { class: "hint" }, "No incidents."));
}

export async function openClaudeStatus() {
  const d = $("#claude-status-sheet");
  renderSheet();
  if (!d.open) d.showModal();
  await loadClaudeStatus().catch(() => {});
}

$("#claude-pill").addEventListener("click", attempt(openClaudeStatus));
$("#claude-status-sync").addEventListener("click", attempt(async () => {
  await api("POST", "/api/admin/claude-status/sync", null, "");
  await loadClaudeStatus();
  toast("Checked the status page", { tone: "ok" });
}));

// ---- Account → Claude status: platform admins set it up
accountSection("claudestatus", loadAdmin);

async function loadAdmin() {
  if (!isAdmin()) return;
  renderAdmin(await api("GET", "/api/admin/claude-status", null, ""));
}

async function save(body) {
  const a = await api("PUT", "/api/admin/claude-status", body, "");
  renderAdmin(a);
  await loadClaudeStatus().catch(() => {});
  return a;
}

$("#claude-status-on").addEventListener("change", attempt(async (e) => {
  const on = e.target.checked;
  try {
    await save({ enabled: on });
  } catch (err) { e.target.checked = !on; throw err; }
  toast(on ? "Claude's status is shown to everyone" : "Claude's status is off", { tone: "ok" });
}));

function renderAdmin(a) {
  $("#claude-status-on").checked = a.enabled;
  const el = $("#claude-status-config");
  if (!a.enabled) {
    fill(el, h("p", { class: "hint" }, "Off: nobody sees Claude's status, and the webhook URL refuses what is posted to it."));
    return;
  }
  const url = a.webhook_url || a.webhook_path;
  const local = /^https?:\/\/(localhost|127\.|\[::1\])/.test(url);
  const step = (head, ...body) => h("li", {}, h("strong", {}, head), ...body);
  const page = h("input", { type: "url", value: a.page_url, "aria-label": "Status page address", placeholder: "https://status.claude.com" });
  const poll = h("input", { type: "checkbox", checked: a.poll });
  poll.addEventListener("change", attempt(async () => {
    try { await save({ poll: poll.checked }); } catch (err) { poll.checked = !poll.checked; throw err; }
  }));
  const deliveries = (a.deliveries || []).map((d) => h("li", { class: "item-row" },
    h("span", { class: "runner-name" }, d.source === "sync" ? "Checked" : "Webhook"),
    h("span", { class: `item-sub ${d.error ? "err-text" : "hint"}` }, d.error || d.summary || ""),
    h("time", { class: "hint", datetime: d.at, title: dateTimeText(d.at) }, agoText(d.at))));
  fill(el,
    h("h3", { class: "section-head" }, "Subscribe to its webhooks"),
    h("ol", { class: "steps" },
      step("Copy this webhook URL.", copyBlock(url, "Webhook URL copied"),
        h("p", { class: "hint" }, "Anyone with it can post to it: treat it like a password."),
        local ? h("p", { class: "hint err-text" }, "The status page can't reach this address. Start Dash with -public-url set to its public HTTPS address, or turn on checking every 5 minutes below.") : null),
      step("Open the status page.", h("p", {},
        h("a", { class: "btn small", href: a.page_url, target: "_blank", rel: "noopener" }, ico("external"), "Open the status page"))),
      step("Subscribe with the webhook.", h("p", { class: "hint" },
        "Choose Subscribe to updates, then the webhook option. Paste the URL, give an email address for delivery failures, and subscribe.")),
      step("Done.", h("p", { class: "hint" }, "Incidents show beside everyone's board as the page posts them. Check now reads the page at once, for what happened before."))),
    h("div", { class: "billing-actions" },
      h("button", { class: "btn small", type: "button", onclick: attempt(async () => {
        // A failed read shows under Recently heard too.
        try {
          renderAdmin(await api("POST", "/api/admin/claude-status/sync", null, ""));
        } catch (err) { await loadAdmin().catch(() => {}); throw err; }
        await loadClaudeStatus().catch(() => {});
        toast("Checked the status page", { tone: "ok" });
      }) }, ico("redo"), "Check now"),
      h("button", { class: "btn small", type: "button", onclick: attempt(async () => {
        if (!await ask({ title: "Make a new webhook URL?", body: "The current URL stops working: subscribe the new one on the status page, and unsubscribe the old one.", ok: "New URL", danger: true })) return;
        renderAdmin(await api("POST", "/api/admin/claude-status/rotate", null, ""));
        toast("New webhook URL made", { tone: "ok" });
      }) }, "New URL…")),
    h("h3", { class: "section-head" }, "Without webhooks"),
    h("label", { class: "toggle" }, poll, h("span", {}, "Also check the status page every 5 minutes")),
    h("p", { class: "hint" }, "For a Dash the status page can't reach: behind a firewall, or without a public address."),
    h("h3", { class: "section-head" }, "Status page"),
    h("div", { class: "org-invite" }, page, h("button", { class: "btn", type: "button", onclick: attempt(async () => {
      await save({ page_url: page.value.trim() });
      toast("Saved", { tone: "ok" });
    }) }, "Save")),
    h("p", { class: "hint" }, "Any Atlassian Statuspage works. Another page forgets the incidents heard from this one."),
    h("h3", { class: "section-head" }, "Recently heard"),
    deliveries.length ? h("ul", { class: "item-list" }, ...deliveries)
      : h("p", { class: "hint" }, "Nothing yet."));
}
