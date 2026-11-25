// Tests for where an org's billing stands and the account menu's badge
// (web/static/billingstate.js). Run with `node --test web/`; groups_test.go
// runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { DAY } from "./static/time.js";
import { billingBadge, billingPhase, trialDaysLeft } from "./static/billingstate.js";

const now = Date.parse("2026-11-25T12:00:00Z");
const day = (iso) => `<${iso.slice(0, 10)}>`;
const at = (ms) => new Date(ms).toISOString();
// An owner's view of a team of three, billing on.
const team = (o) => ({ enabled: true, can_manage: true, members: 3, free: false, required: false, read_only: false, ...o });
const badge = (b) => billingBadge(b, { now, day });
const brief = (x) => x && [x.text, x.short, x.tone, x.attention];

test("each state gets its badge", () => {
  const cases = [
    ["active team", team({ status: "active", plan: "team", seats: 3 }), ["Team", "Team", "neutral", false]],
    ["active individual", { enabled: true, can_manage: true, free: true, individual: true, status: "active", plan: "individual", seats: 1 }, ["Individual", "Individual", "neutral", false]],
    ["trial", team({ status: "trialing", plan: "team", seats: 3, trial_end: at(now + 8.5 * DAY) }), ["Trial · 9 days", "Trial · 9d", "neutral", false]],
    ["trial ending", team({ status: "trialing", trial_end: at(now + 2.2 * DAY) }), ["Trial · 3 days", "Trial · 3d", "warn", true]],
    ["trial, last day", team({ status: "trialing", trial_end: at(now + 0.3 * DAY) }), ["Trial · 1 day", "Trial · 1d", "warn", true]],
    ["trial over, webhook late", team({ status: "trialing", trial_end: at(now - DAY) }), ["Trial ends today", "Trial · 0d", "warn", true]],
    ["one member", { enabled: true, can_manage: true, free: true, members: 1, required: false }, ["Free", "Free", "muted", false]],
    ["chose free", { enabled: true, can_manage: true, free: true, individual: true, chose_free: true }, ["Free", "Free", "muted", false]],
    ["admin waiver", team({ free: true, free_forever: true }), ["Free", "Free", "muted", false]],
    ["waiver until a date", team({ free: true, free_until: at(now + 30 * DAY) }), ["Free", "Free", "muted", false]],
    ["needs a subscription", team({ required: true, grace_until: "2026-12-02T10:00:00Z" }), ["Subscribe by <2026-12-02>", "Due <2026-12-02>", "warn", true]],
    ["payment failed", team({ status: "past_due", required: true, grace_until: at(now + DAY) }), ["Payment failed", "Unpaid", "warn", true]],
    ["unpaid", team({ status: "unpaid", required: true }), ["Payment failed", "Unpaid", "warn", true]],
    ["one member, payment failed", { enabled: true, can_manage: true, free: true, members: 1, status: "past_due", seats: 1 }, ["Payment failed", "Unpaid", "warn", true]],
    ["individual, payment failed", { enabled: true, can_manage: true, free: true, individual: true, status: "past_due", plan: "individual", seats: 1 }, ["Payment failed", "Unpaid", "warn", true]],
    ["read-only", team({ required: true, read_only: true }), ["Read-only", "Read-only", "bad", true]],
    ["read-only after a failed payment", team({ status: "past_due", required: true, read_only: true }), ["Read-only", "Read-only", "bad", true]],
    ["cancels at period end", team({ status: "active", plan: "team", seats: 3, cancel_at_period_end: true, period_end: "2026-12-30T00:00:00Z" }), ["Ends <2026-12-30>", "Ends <2026-12-30>", "muted", false]],
  ];
  for (const [name, b, want] of cases) assert.deepEqual(brief(badge(b)), want, name);
});

test("no badge for anyone but owners, with billing off, or in the Default org", () => {
  assert.equal(badge(null), null);
  assert.equal(badge({ enabled: false }), null);
  assert.equal(badge(team({ can_manage: false, required: true, read_only: true })), null, "members and admins");
  assert.equal(badge(team({ exempt: true, free: true })), null, "the Default org");
});

test("phases follow Billing & usage's order", () => {
  assert.equal(billingPhase(team({ free_forever: true, status: "past_due" })), "waived");
  assert.equal(billingPhase(team({ status: "past_due", required: true, read_only: true })), "read_only");
  assert.equal(billingPhase(team({ status: "canceled", required: true })), "required");
  assert.equal(billingPhase({ free: true, individual: true, status: "trialing", plan: "individual" }), "active");
  assert.equal(billingPhase({ free: true, individual: true, ask: true }), "free");
  assert.equal(billingPhase({ free: true, members: 1, status: "canceled" }), "free");
});

test("trial days count whole days up", () => {
  assert.equal(trialDaysLeft({}, now), null);
  assert.equal(trialDaysLeft({ trial_end: at(now + DAY) }, now), 1);
  assert.equal(trialDaysLeft({ trial_end: at(now + DAY + 1) }, now), 2);
  assert.equal(trialDaysLeft({ trial_end: at(now - 1) }, now), 0);
});
