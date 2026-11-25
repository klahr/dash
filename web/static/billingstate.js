// Where an organization's billing stands, from its state (GET
// /api/orgs/{id}/billing, app/billing.js loadBilling). Organization →
// Billing & usage (ownerBillingLines in app/billing.js) and the account
// menu's badge both read it from here, so the two never disagree. Pure, so
// billingstate_test.mjs can cover it.

import { DAY } from "./time.js";

// pastDue: Stripe couldn't take the last payment.
export const pastDue = (b) => b?.status === "past_due" || b?.status === "unpaid";

// waived: a platform admin made the org free, forever or until a date.
export const waived = (b) => !!(b?.free_forever || b?.free_until);

// billingPhase is one word for where the org's billing stands, in the order
// that matters most to an owner:
//   "waived"    a platform admin's free override holds
//   "read_only" it needs a subscription and the grace period is over
//   "past_due"  the last payment failed
//   "required"  it needs a subscription, within the grace period (grace_until)
//   "trial"     a team subscription in its trial (trial_end)
//   "active"    a paid subscription (plan "team" or "individual")
//   "free"      nothing to pay: one member, a personal workspace, an
//               individual who chose free or hasn't subscribed
export function billingPhase(b) {
  if (waived(b)) return "waived";
  if (b.individual) {
    // An individual is never made to pay: subscribed or free.
    if (b.plan === "individual" || b.seats) return pastDue(b) ? "past_due" : "active";
    return "free";
  }
  if (b.read_only) return "read_only";
  if (pastDue(b)) return "past_due";
  if (b.required) return "required";
  if (b.status === "trialing") return "trial";
  if (b.status === "active") return "active";
  return "free";
}

// A trial ending within this many days needs the owner's attention.
export const TRIAL_SOON_DAYS = 3;

// trialDaysLeft is the whole days until the trial ends (0 once it has), or
// null without an end.
export function trialDaysLeft(b, now = Date.now()) {
  if (!b?.trial_end) return null;
  return Math.max(0, Math.ceil((new Date(b.trial_end).getTime() - now) / DAY));
}

// billingBadge is the account menu's badge for the org, or null when there
// is none: billing off, the instance's Default org (never billed), or not an
// owner (can_manage). text is the full label, short the one for a phone's
// width or a narrow menu; tone is "neutral", "muted", "warn" or "bad";
// attention says the avatar shows a dot while the menu is closed. day
// formats a date as the badge shows it.
export function billingBadge(b, { now = Date.now(), day = (iso) => new Date(iso).toLocaleDateString() } = {}) {
  if (!b?.enabled || !b.can_manage || b.exempt) return null;
  const badge = (text, tone, attention = false, short = text) => ({ text, short, tone, attention });
  switch (billingPhase(b)) {
    case "read_only": return badge("Read-only", "bad", true);
    case "past_due": return badge("Payment failed", "warn", true, "Unpaid");
    case "required":
      return b.grace_until ? badge(`Subscribe by ${day(b.grace_until)}`, "warn", true, `Due ${day(b.grace_until)}`) : badge("Subscribe", "warn", true);
    case "trial": {
      const left = trialDaysLeft(b, now);
      const soon = left != null && left <= TRIAL_SOON_DAYS;
      if (left == null) return badge("Trial", "neutral");
      if (left === 0) return badge("Trial ends today", "warn", true, "Trial · 0d");
      return badge(`Trial · ${left} ${left === 1 ? "day" : "days"}`, soon ? "warn" : "neutral", soon, `Trial · ${left}d`);
    }
    case "active":
      if (b.cancel_at_period_end && b.period_end) return badge(`Ends ${day(b.period_end)}`, "muted");
      return badge(b.plan === "individual" ? "Individual" : "Team", "neutral");
    default: // "waived", "free"
      return badge("Free", "muted");
  }
}
