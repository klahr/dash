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
// What counts: the components a platform admin chose (Account → Claude
// status → Components) and the incidents that affect one or name none. The
// hub marks the rest ignored.
const counted = (xs) => (xs || []).filter((x) => !x.ignored);
const ignoredOf = (xs) => (xs || []).filter((x) => x.ignored);

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
  // Only incidents that count; one that starts counting when an admin
  // changes the components isn't news.
  const was = new Set((before.active || []).map((i) => i.id));
  const now = new Set((st.active || []).map((i) => i.id));
  const details = { label: "Details", run: openClaudeStatus };
  for (const i of counted(st.active)) {
    if (!was.has(i.id)) toast(i.updates?.[0]?.body || "", { tone: "error", head: `Claude: ${i.name}`, action: details });
  }
  for (const i of counted(before.active)) {
    if (!now.has(i.id)) toast(i.name, { tone: "ok", head: "Claude incident resolved", action: details });
  }
}

// The page's own indicator covers every component, so it doesn't turn the
// pill on by itself: only what counts does.
const troubled = (st) => !!st?.enabled && (counted(st.active).length > 0 || counted(st.components).length > 0);

export function renderClaudeStatus() {
  const st = state.claudeStatus;
  const pill = $("#claude-pill");
  pill.hidden = !troubled(st);
  if (!pill.hidden) {
    const active = counted(st.active), comps = counted(st.components);
    const tone = (st.level && st.level !== "none" && TONE[st.level]) || "warn";
    const text = active.length === 1 ? `Claude: ${label(IMPACT, active[0].impact).toLowerCase()} incident`
      : active.length ? `Claude: ${plural(active.length, "incident")}`
      : comps.length === 1 ? `Claude: ${comps[0].name} ${label(COMPONENT, comps[0].status).toLowerCase()}`
      : `Claude: ${plural(comps.length, "component")} affected`;
    pill.className = `usage-pill claude-pill claude-${tone}`;
    pill.textContent = text;
    pill.title = `${[...active.map((i) => i.name), ...comps.map((c) => c.name)].join(", ")} (status.claude.com)`;
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
      i.components?.length ? ` · ${i.components.join(", ")}` : "",
      i.ignored ? h("span", { class: "claude-ignored-tag" }, "Not counted") : null),
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
  const active = counted(st.active), comps = counted(st.components);
  const ignActive = ignoredOf(st.active), ignComps = ignoredOf(st.components);
  // The page's own words cover every component: say so when none of it counts.
  const onlyIgnored = !troubled(st) && st.indicator && st.indicator !== "none";
  summary.className = `claude-summary claude-${onlyIgnored ? "ok" : tone}`;
  fill(summary, h("strong", {}, onlyIgnored ? "Nothing Dash watches is affected"
    : st.description || label(INDICATOR, st.indicator) || "Nothing heard yet"),
  onlyIgnored ? h("span", { class: "hint" }, ` · the page says ${st.description || label(INDICATOR, st.indicator)}`) : null,
  st.updated ? h("span", { class: "hint" }, ` · updated ${agoText(st.updated)}`) : null);
  const compList = (cs) => h("ul", { class: "claude-components" }, ...cs.map((c) =>
    h("li", { class: `claude-${c.status === "major_outage" || c.status === "partial_outage" ? "bad" : "warn"}` },
      h("span", {}, c.name), h("span", { class: "claude-component-status" }, label(COMPONENT, c.status)))));
  const incidentList = (is) => h("ul", { class: "claude-incidents" }, ...is.map((i) =>
    h("li", { class: "claude-incident" }, ...incidentHead(i), updateList(i))));
  const ignored = ignActive.length + ignComps.length;
  fill($("#claude-status-body"),
    comps.length ? h("h3", { class: "section-head" }, "Affected components") : null,
    comps.length ? compList(comps) : null,
    h("h3", { class: "section-head" }, "Active incidents"),
    active.length ? incidentList(active) : h("p", { class: "hint" }, "None right now."),
    // What doesn't count, folded away: why the pill is off.
    ignored ? h("details", { class: "claude-ignored" },
      h("summary", {}, h("span", { class: "section-head" }, `Not counted (${ignored})`)),
      h("p", { class: "hint" }, isAdmin() ? "These affect only components Dash doesn't watch. Choose them in Account → Claude status → Components."
        : "These affect only components Dash doesn't watch, so the Claude pill stays off for them."),
      ignComps.length ? compList(ignComps) : null,
      ignActive.length ? incidentList(ignActive) : null) : null,
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
    ...componentPicker(a),
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

// Components: which of the page's components count for the pill and the
// notices. Each change is saved at once.
function componentPicker(a) {
  const comps = a.components || [];
  const boxes = comps.map((c) => h("input", { type: "checkbox", value: c.id || c.name, checked: c.watched }));
  const list = h("div", { class: "claude-watch" }, ...comps.map((c, k) => h("label", { class: "runner-pick" },
    boxes[k],
    h("span", { class: "runner-name" }, c.name),
    h("span", { class: `hint claude-${c.status && c.status !== "operational" ? (c.status === "major_outage" || c.status === "partial_outage" ? "bad" : "warn") : "muted"}` },
      c.status ? label(COMPONENT, c.status) : "Not heard of yet"))));
  list.addEventListener("change", attempt(async (e) => {
    boxes.forEach((b) => { b.disabled = true; });
    try {
      await save({ components: boxes.filter((b) => b.checked).map((b) => b.value) });
    } catch (err) {
      e.target.checked = !e.target.checked;
      boxes.forEach((b) => { b.disabled = false; });
      throw err;
    }
    toast("Saved", { tone: "ok" });
  }));
  return [
    h("h3", { class: "section-head" }, "Components"),
    h("p", { class: "hint" }, "The Claude pill and the notices count only these components, the incidents that affect them, and incidents that name no component. The rest still show under Not counted in the status."),
    comps.length ? list : h("p", { class: "hint" }, "No components yet. Check now reads them from the status page."),
    !comps.some((c) => c.status) ? h("p", { class: "hint" }, "Check now lists all the page's components.") : null,
    a.default_watch ? h("p", { class: "hint" }, "Defaults: Claude Code, the Claude API and claude.ai.")
      : h("div", { class: "billing-actions" }, h("button", { class: "btn small", type: "button", onclick: attempt(async () => {
        await save({ default_components: true });
        toast("Back to the default components", { tone: "ok" });
      }) }, "Use the defaults")),
  ];
}
