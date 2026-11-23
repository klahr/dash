import { $, dateText, fill, h, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { switchOrg } from "./myaccount.js";

// ------------------------------------------------------------------ billing
// Only when the hub has Stripe configured: an org with two or more members
// needs a subscription, billed per member. Owners subscribe in Stripe
// Checkout and manage the card, invoices and cancelling in Stripe's portal.
export async function loadBilling() {
  const org = state.me?.org;
  if (!org) return null;
  const b = await api("GET", `/api/orgs/${org.id}/billing`);
  state.billing = b;
  renderBillingBanner();
  return b;
}
document.addEventListener("dash:billing-read-only", () => loadBilling().catch(() => {}));


async function startCheckout() {
  const r = await api("POST", `/api/orgs/${state.me.org.id}/billing/checkout`);
  location.href = r.url;
}

async function openBillingPortal() {
  const r = await api("POST", `/api/orgs/${state.me.org.id}/billing/portal`);
  location.href = r.url;
}

function billingButtons(b) {
  if (!b?.can_manage) return [];
  return [
    b.can_subscribe && !freeText(b) ? h("button", { class: "btn small primary", type: "button", onclick: attempt(startCheckout) },
      b.trial_days ? `Start ${b.trial_days}-day trial` : "Subscribe") : null,
    b.has_account ? h("button", { class: "btn small", type: "button", onclick: attempt(openBillingPortal) }, "Manage billing") : null,
  ].filter(Boolean);
}

function renderBillingBanner() {
  const b = state.billing;
  const el = $("#billing-banner");
  if (!b?.enabled || !b.required) { el.hidden = true; return; }
  const pastDue = b.status === "past_due" || b.status === "unpaid";
  const head = b.read_only ? "This board is read-only" :
    pastDue ? "The last payment failed" : `${state.me.org.name} needs a subscription`;
  let text = b.read_only ?
    "Organizations with two or more members need an active subscription. Everything is still here and can be read; tasks start again once it is active." :
    `Organizations with two or more members are billed per member. Until ${dateText(b.grace_until)} the board works as usual; after that it becomes read-only.`;
  if (pastDue && b.has_account) text += " Update the card in the billing portal.";
  if (!b.can_manage) text += " Ask an owner of the organization to subscribe.";
  el.classList.toggle("read-only", !!b.read_only);
  fill(el, h("strong", {}, head), h("span", {}, text),
    b.can_manage ? h("div", { class: "billing-actions" }, ...billingButtons(b)) : null);
  el.hidden = false;
}

// freeText is a platform admin's override as members see it, or "".
const freeText = (b) => b?.free_forever ? "Free forever" : b?.free_until ? `Free until ${dateText(b.free_until)}` : "";

// renderOrgBilling fills the Billing section of the org sheet: owners see
// the subscription, members a free override, platform admins the override's
// controls.
export function renderOrgBilling(b) {
  const admin = !!state.me?.user?.is_platform_admin;
  const free = freeText(b);
  $("#org-billing").hidden = !b?.enabled || !(b.can_manage || free || admin);
  if ($("#org-billing").hidden) return;
  const body = $("#org-billing-body");
  if (!b.can_manage) {
    body.replaceChildren(...(free ? [h("p", { class: "hint" }, `${free}: a platform admin waived the subscription for this organization.`)] : []),
      h("div", { id: "org-billing-admin" }));
  } else {
    body.replaceChildren(...ownerBillingLines(b, free).map((l) => h("p", { class: "hint" }, l)),
      h("div", { class: "billing-actions" }, ...billingButtons(b)), h("div", { id: "org-billing-admin" }));
  }
  if (admin) renderFreeOverride($("#org-billing-admin")).catch((err) => toast(err.message, true));
}

function ownerBillingLines(b, free) {
  const lines = [];
  if (free) {
    lines.push(`${free}: a platform admin waived the subscription for this organization, whatever its members.`);
    if (b.seats) lines.push("The subscription carries on as before: cancel it in the billing portal if you don't need it.");
    else if (b.free_until) lines.push(`After that, organizations with two or more members need a subscription, billed per member.`);
    return lines;
  }
  switch (b.status) {
    case "active":
      lines.push(`Subscribed · ${plural(b.seats, "seat")} · ${b.cancel_at_period_end ? "ends" : "renews"} ${b.period_end ? dateText(b.period_end) : ""}`);
      break;
    case "trialing":
      lines.push(`Trial · ${plural(b.seats, "seat")}${b.trial_end ? ` · until ${dateText(b.trial_end)}` : ""}${b.cancel_at_period_end ? " · then ends" : ""}`);
      break;
    case "past_due": case "unpaid":
      lines.push("The last payment failed. Update the card in the billing portal.");
      break;
    default:
      lines.push(b.free ? "Free." : "No subscription.");
  }
  if (b.free) {
    lines.push(b.seats ? "With one member this organization is free; cancel the subscription in the portal if you don't need it any more." :
      "Personal workspaces and organizations with one member are free. Inviting a second member needs a subscription, billed per member.");
  } else if (b.required) {
    lines.push(b.read_only ? "The board is read-only until the subscription is active. Nothing has been deleted." :
      `Needed by ${dateText(b.grace_until)}: after that the board becomes read-only.`);
  } else {
    lines.push("Seats follow the members: adding or removing someone updates the subscription, prorated.");
  }
  return lines;
}

// localDay is t's day here, as a date input's value.
const localDay = (t) => {
  const d = new Date(t);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};

// renderFreeOverride: platform admins make the org free, forever or until
// the end of a day, or clear it.
async function renderFreeOverride(el) {
  const org = state.me.org;
  const path = `/api/admin/orgs/${org.id}/billing`;
  const o = await api("GET", path, null, "");
  const kind = h("select", { "aria-label": "How long the organization is free" },
    h("option", { value: "until", selected: !o.free_forever }, "Free until"),
    h("option", { value: "forever", selected: o.free_forever }, "Free forever"));
  const tomorrow = localDay(Date.now() + 864e5);
  const day = h("input", { type: "date", min: localDay(Date.now()), value: o.free_until ? localDay(o.free_until) : tomorrow, "aria-label": "Last free day" });
  const note = h("input", { type: "text", value: o.note || "", maxlength: "500", placeholder: "Why (only platform admins see it)", "aria-label": "Note" });
  const sync = () => { day.hidden = kind.value === "forever"; };
  kind.addEventListener("change", sync);
  sync();
  const done = async (msg) => {
    toast(msg, { tone: "ok" });
    renderOrgBilling(await loadBilling()); // and the board's banner
  };
  const save = attempt(async () => {
    const body = { note: note.value };
    if (kind.value === "forever") body.free_forever = true;
    else {
      if (!day.value) throw new Error("Pick the last free day.");
      body.free_until = new Date(`${day.value}T23:59:59`).toISOString(); // the end of that day, here
    }
    const r = await api("PUT", path, body, "");
    await done(`${org.name} is ${freeText(r.state).toLowerCase() || "free"}`);
  });
  const clear = attempt(async () => {
    const ok = await ask({ title: `Clear ${org.name}'s free override?`,
      body: "The usual rule applies again: with two or more members it needs a subscription, after a full grace period from now.", ok: "Clear" });
    if (!ok) return;
    await api("DELETE", path, null, "");
    await done("Free override cleared");
  });
  let status = "Not set: the usual rule applies.";
  if (o.free_forever || o.free_until) {
    status = o.active ? (o.free_forever ? "Free forever" : `Free until ${dateText(o.free_until)}`) : `Ended ${dateText(o.free_until)}`;
    if (o.set_by) status += ` · set by ${o.set_by}${o.set_at ? ` on ${dateText(o.set_at)}` : ""}`;
    if (o.note) status += ` · ${o.note}`;
  }
  el.replaceChildren(
    h("h3", { class: "section-head" }, "Free override (platform admins)"),
    h("p", { class: "hint" }, status),
    h("p", { class: "hint" }, "The organization needs no subscription while it holds, whatever its members. A subscription it has isn't touched."),
    h("div", { class: "org-invite" }, kind, day, note),
    h("div", { class: "billing-actions" },
      h("button", { class: "btn small primary", type: "button", onclick: save }, o.active ? "Update" : "Make free"),
      (o.free_forever || o.free_until) ? h("button", { class: "btn small danger", type: "button", onclick: clear }, "Clear") : null));
}

// offerSubscription follows an invitation that will make the org need a
// subscription: owners can start it straight away.
export async function offerSubscription(notice) {
  const b = await loadBilling();
  if (!b?.enabled || !b.required && !b.free || b.free_forever || b.free_until) return;
  if (!b.can_manage) {
    toast("Once they join, an owner needs to subscribe: organizations with two or more members are billed per member.", { tone: "info" });
    return;
  }
  const trial = notice.trial_days || b.trial_days;
  const ok = await ask({
    title: trial ? `Start your ${trial}-day trial?` : "Subscribe?",
    body: `${notice.message} ${trial ? `The first ${trial} days are free; cancel any time in the billing portal.` : ""} Stripe takes you through it and brings you back here.`,
    ok: trial ? "Start trial" : "Subscribe",
  });
  if (ok) await startCheckout();
}

// Back from Stripe Checkout or the portal.
{
  const q = new URLSearchParams(location.search);
  const back = q.get("billing");
  if (back) {
    q.delete("billing");
    history.replaceState(null, "", location.pathname + (q.size ? `?${q}` : "") + location.hash);
    if (back === "done") toast("Thanks! The subscription is active as soon as Stripe confirms it.", { tone: "ok" });
    if (back === "canceled") toast("Checkout canceled: nothing was charged.");
    setTimeout(() => loadBilling().catch(() => {}), 2500); // the webhook may take a moment
  }
}

export async function newOrg() {
  const name = await ask({ title: "New organization", body: "You become its owner and can invite people to it.", input: true, placeholder: "Name", ok: "Create" });
  if (!name) return;
  const o = await api("POST", "/api/orgs", { name });
  toast(`${o.name} created`, { tone: "ok" });
  await switchOrg(o);
}
