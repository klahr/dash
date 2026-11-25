import { $, $$, fill, h, plural, timeLocale } from "./dom.js";
import { api } from "./api.js";
import { attempt } from "./dialogs.js";
import { orgSection } from "./accountsheet.js";
import { fmtTokens } from "./runstats.js";

// ------------------------------------------------------------------ team usage
// Organization → Billing & usage, for owners and admins: what the org's Claude runs
// used and cost by member and by project, per week or month
// (GET /api/usage/team), and the same as a CSV.
let period = "week";

const query = (extra = {}) => new URLSearchParams({ period, tz: String(new Date().getTimezoneOffset()), ...extra }).toString();
const money = (n) => `$${n.toFixed(2)}`;
const tokensOf = (s) => s.input_tokens + s.output_tokens + s.cache_read_tokens + s.cache_creation_tokens;

function periodLabel(day) {
  const d = new Date(`${day}T00:00:00`);
  return period === "month" ? d.toLocaleDateString(timeLocale, { month: "short", year: "numeric" }) :
    d.toLocaleDateString(timeLocale, { month: "short", day: "numeric" });
}

// table is one row per member (or project), one column per period, and a total.
function table(u, label, keyOf, nameOf) {
  const rows = new Map();
  for (const r of u.rows) {
    const k = keyOf(r);
    let row = rows.get(k);
    if (!row) rows.set(k, row = { name: nameOf(r), cells: {}, total: { cost_usd: 0, tokens: 0, runs: 0 } });
    const c = row.cells[r.period] ||= { cost_usd: 0, tokens: 0, runs: 0 };
    for (const x of [c, row.total]) { x.cost_usd += r.cost_usd; x.tokens += tokensOf(r); x.runs += r.runs; }
  }
  const sorted = [...rows.values()].sort((a, b) => b.total.cost_usd - a.total.cost_usd);
  const cell = (c, tag = "td") => c ?
    h(tag, { title: `${plural(c.runs, "run")} · ${fmtTokens(c.tokens)} tokens` }, money(c.cost_usd)) :
    h(tag, { class: "none" }, "–");
  return h("div", { class: "usage-table-wrap" },
    h("table", { class: "usage-table" },
      h("thead", {}, h("tr", {}, h("th", { scope: "col" }, label),
        ...u.periods.map((p) => h("th", { scope: "col" }, periodLabel(p))), h("th", { scope: "col" }, "Total"))),
      h("tbody", {}, ...sorted.map((row) => h("tr", {}, h("th", { scope: "row" }, row.name),
        ...u.periods.map((p) => cell(row.cells[p])), cell(row.total))))));
}

async function loadUsage() {
  const box = $("#org-usage");
  if (box.hidden) return;
  $$("#org-usage .segmented button").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.period === period)));
  $("#org-usage-csv").href = `/api/usage/team?${query({ format: "csv" })}`;
  const u = await api("GET", `/api/usage/team?${query()}`, null, "");
  const body = $("#org-usage-body");
  if (!u.rows.length) {
    fill(body, h("p", { class: "hint" }, `No Claude runs from ${u.from} to ${u.to}.`));
    return;
  }
  fill(body,
    h("p", { class: "usage-total" }, h("strong", {}, money(u.total.cost_usd)),
      ` · ${fmtTokens(tokensOf(u.total))} tokens · ${plural(u.total.runs, "run")} · ${u.from} to ${u.to}`),
    h("h3", { class: "section-head" }, "By member"),
    table(u, "Member", (r) => r.member_id, (r) => r.member),
    h("h3", { class: "section-head" }, "By project"),
    table(u, "Project", (r) => r.project_id, (r) => r.project || "Deleted project"));
  // The latest periods and the totals first: a narrow screen scrolls back to older ones.
  for (const w of $$(".usage-table-wrap", body)) w.scrollLeft = w.scrollWidth;
}

for (const b of $$("#org-usage .segmented button")) {
  b.addEventListener("click", attempt(async () => { period = b.dataset.period; await loadUsage(); }));
}
orgSection("billing", loadUsage);
