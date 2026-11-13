import { $, dateTimeText, h, plural } from "./dom.js";

// ------------------------------------------------------------------ run stats
// The tokens each Claude run on a task took, one line per run and a total,
// in the task's detail (#d-runs). Tasks from before Dash kept them have none.

// fmtTokens writes a token count short: 950, 1.2k, 28k, 1.4M.
export const fmtTokens = (n) => n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)}k` : String(n);

const KIND = { first: "first run", notes: "with notes", interrupted: "after interruption", retry: "fresh retry" };

// runParts are "1.4M in (92% cached)", "28k out", "31 turns", "$0.84".
function runParts(r) {
  const cached = r.cache_read_tokens || 0;
  const input = (r.input_tokens || 0) + cached + (r.cache_creation_tokens || 0);
  const parts = [`${fmtTokens(input)} in${input && cached ? ` (${Math.round(100 * cached / input)}% cached)` : ""}`,
    `${fmtTokens(r.output_tokens || 0)} out`];
  if (r.turns) parts.push(plural(r.turns, "turn"));
  parts.push(`$${(r.cost_usd || 0).toFixed(2)}`);
  return parts;
}

// runNums shows them in a line that wraps between parts, not inside one.
const runNums = (r) => h("span", { class: "run-nums" },
  ...runParts(r).flatMap((p, i, all) => [i ? " " : "", h("span", {}, i < all.length - 1 ? `${p} ·` : p)]));

function total(runs) {
  const sum = { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0, turns: 0, cost_usd: 0 };
  for (const r of runs) for (const k in sum) sum[k] += r[k] || 0;
  return sum;
}

export function renderRunStats(t) {
  const box = $("#d-runs");
  const runs = t.run_stats || [];
  box.hidden = !runs.length;
  if (!runs.length) return;
  $("#d-runs-meta").textContent = [plural(runs.length, "run"), ...runParts(total(runs))].join(" · ");
  const first = (t.runs || runs.length) - runs.length; // runs from before Dash kept records
  $("#d-runs-list").replaceChildren(
    ...runs.map((r, i) => h("li", { title: [r.at && dateTimeText(r.at), r.model].filter(Boolean).join(" · ") },
      h("span", { class: "run-name" }, `Run ${first + i + 1}`),
      h("span", { class: "run-kind" }, KIND[r.kind] || r.kind),
      runNums(r))),
    ...(runs.length > 1 ? [h("li", { class: "run-total" },
      h("span", { class: "run-name" }, "Total"), h("span", { class: "run-kind" }, ""), runNums(total(runs)))] : []));
}
