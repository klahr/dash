// The Claude pill's words (web/static/app/claudestatus.js), and the labels
// it shares with the status sheet. Pure, so claudepill_test.mjs can cover it.

export const IMPACT = { none: "No impact", minor: "Minor", major: "Major", critical: "Critical", maintenance: "Maintenance" };
export const COMPONENT = {
  operational: "Operational", degraded_performance: "Degraded performance", partial_outage: "Partial outage",
  major_outage: "Major outage", under_maintenance: "Under maintenance",
};
export const label = (map, k) => map[k] || (k || "").replaceAll("_", " ");

// A component's name for matching, as the hub matches them: lower case,
// single spaces.
const key = (n) => (n || "").toLowerCase().split(/\s+/).filter(Boolean).join(" ");

// affectedServices are the distinct services that count: the counted
// components, then those the counted incidents name that count (the hub
// lists them in counted). Each has the component's status when the page
// reported one, else the impact of the first incident that names it.
export function affectedServices(active, comps) {
  const out = new Map();
  for (const c of comps) if (!out.has(key(c.name))) out.set(key(c.name), { name: c.name, status: c.status });
  for (const i of active) {
    for (const n of i.counted || []) if (!out.has(key(n))) out.set(key(n), { name: n, impact: i.impact });
  }
  return [...out.values()];
}

// claudePillText is the pill's words for the incidents and components that
// count: how many services are affected, the one service and its status,
// or, when no service can be named (a general outage), the incidents.
export function claudePillText(active, comps) {
  const svcs = affectedServices(active, comps);
  if (svcs.length > 1) return `Claude: ${svcs.length} services affected`;
  if (svcs.length === 1 && svcs[0].status) return `Claude: ${svcs[0].name} ${label(COMPONENT, svcs[0].status).toLowerCase()}`;
  if (active.length === 1) return `Claude: ${label(IMPACT, active[0].impact).toLowerCase()} incident`;
  return `Claude: ${active.length} incidents`;
}

// serviceTone is an affected service's colour in the sheet: bad (red) for
// a partial or major outage, or an incident of major or critical impact;
// warn (yellow) otherwise.
export function serviceTone(s) {
  if (s.status) return s.status === "major_outage" || s.status === "partial_outage" ? "bad" : "warn";
  return s.impact === "major" || s.impact === "critical" ? "bad" : "warn";
}

// claudePillTone is the pill's colour, as the sheet shows what counts: red
// when at least one affected service is red, or when an incident that names
// no service is major or critical; yellow otherwise.
export function claudePillTone(active, comps) {
  if (affectedServices(active, comps).some((s) => serviceTone(s) === "bad")) return "bad";
  return active.some((i) => !i.counted?.length && serviceTone({ impact: i.impact }) === "bad") ? "bad" : "warn";
}
