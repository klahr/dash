import { $, dateText, fill, h, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { switchOrg } from "./account.js";

// ------------------------------------------------------------------ billing
// Only when the hub has Stripe configured: an org with two or more members
// needs a subscription, billed per member. Owners subscribe in Stripe
// Checkout and manage the card, invoices and cancelling in Stripe's portal.
export async function loadBilling() {
  const org = state.me?.org;
  if (!org) return null;
  const b = await api("GET", `/api/orgs/${org.id}/billing`, null, "");
  state.billing = b;
  renderBillingBanner();
  return b;
}
document.addEventListener("dash:billing-read-only", () => loadBilling().catch(() => {}));


async function startCheckout() {
  const r = await api("POST", `/api/orgs/${state.me.org.id}/billing/checkout`, null, "");
  location.href = r.url;
}

async function openBillingPortal() {
  const r = await api("POST", `/api/orgs/${state.me.org.id}/billing/portal`, null, "");
  location.href = r.url;
}

function billingButtons(b) {
  if (!b?.can_manage) return [];
  return [
    b.can_subscribe ? h("button", { class: "btn small primary", type: "button", onclick: attempt(startCheckout) },
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

// renderOrgBilling fills the Billing section of the org sheet (owners only).
export function renderOrgBilling(b) {
  $("#org-billing").hidden = !b?.enabled || !b.can_manage;
  if ($("#org-billing").hidden) return;
    const lines = [];
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
  $("#org-billing-body").replaceChildren(...lines.map((l) => h("p", { class: "hint" }, l)),
    h("div", { class: "billing-actions" }, ...billingButtons(b)));
}

// offerSubscription follows an invitation that will make the org need a
// subscription: owners can start it straight away.
export async function offerSubscription(notice) {
  const b = await loadBilling();
  if (!b?.enabled || !b.required && !b.free) return;
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
  const o = await api("POST", "/api/orgs", { name }, "");
  toast(`${o.name} created`, { tone: "ok" });
  await switchOrg(o);
}
