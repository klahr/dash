import { $, h, timeLocale } from "./dom.js";
import { api } from "./api.js";
import { attempt } from "./dialogs.js";
import { state } from "./state.js";
import { openSettings } from "./settings.js";

// ------------------------------------------------------------------ spending
// Project settings → Spending (docs/spending.md): the caps per task, day and
// month (one setting, "spending", saved by settings.js like the others), the
// spend of the last 30 days as a bar chart with the cost per merged task
// (GET /api/projects/{pid}/spending), and the board's banner while a cap
// holds the project's tasks back (project.spend_hold).

const SVG = "http://www.w3.org/2000/svg";
const browserZone = () => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch { return ""; } };
// money writes dollars short: $0.42, $12.50, $1,240.
export const money = (n) => `$${n >= 1000 ? Math.round(n).toLocaleString(timeLocale) : n.toFixed(2)}`;
const capText = (n) => `$${Number.isInteger(n) ? n : n.toFixed(2)}`;

// showSpending fills the caps' fields from project p.
export function showSpending(f, p) {
  const s = p.spending || {};
  f.spend_task.value = s.task_usd || "";
  f.spend_day.value = s.day_usd || "";
  f.spend_month.value = s.month_usd || "";
  spendingHint(p);
}

// readSpending is the caps as the API takes them; days start where they
// did, else in this browser's time zone.
export function readSpending(f) {
  const n = (el) => Math.max(0, Math.round((Number(el.value) || 0) * 100) / 100);
  const s = { task_usd: n(f.spend_task), day_usd: n(f.spend_day), month_usd: n(f.spend_month) };
  if (!s.task_usd && !s.day_usd && !s.month_usd) return {};
  s.zone = state.project?.spending?.zone || browserZone();
  return s;
}

export function spendingHint(p) {
  const s = p.spending, el = $("#settings-spending-hint");
  if (!s) { el.textContent = "Off. Chats aren't counted: Dash doesn't record their cost."; return; }
  el.textContent = `Days and months start at midnight${s.zone ? ` (${s.zone})` : ""}. Tasks and scans count; chats don't. Codex costs are estimates from its tokens.`;
}

// loadSpendTrend draws the open project's last 30 days.
export async function loadSpendTrend() {
  const p = state.project, box = $("#spend-trend");
  if (!p) return;
  box.replaceChildren(h("p", { class: "hint" }, "Loading…"));
  const v = await api("GET", `/api/projects/${p.id}/spending`, null, p.prefix || "");
  if (state.project?.id !== p.id) return;
  box.replaceChildren(...trend(v));
}

function dayLabel(d, opts = { month: "short", day: "numeric" }) {
  return new Date(`${d}T00:00:00`).toLocaleDateString(timeLocale, opts);
}

// trend is the stat line, the chart and the merged tasks of view v.
function trend(v) {
  const est = v.estimated ? " (some estimated)" : "";
  const stats = h("div", { class: "spend-stats" },
    stat("Today", money(v.today_usd), v.spending?.day_usd && `of ${capText(v.spending.day_usd)}`),
    stat("This month", money(v.month_usd), v.spending?.month_usd && `of ${capText(v.spending.month_usd)}`),
    stat("Per merged task", v.merged.length ? money(v.per_merged_usd) : "–", v.merged.length ? `${v.merged.length} merged` : "none merged"));
  const total = v.days.reduce((a, d) => a + d.cost_usd, 0);
  const out = [stats, chart(v.days), h("p", { class: "hint" }, `${money(total)} in 30 days${est}.`)];
  if (v.merged.length) {
    out.push(h("details", { class: "spend-merged" }, h("summary", {}, "Cost per merged task"),
      h("table", { class: "spend-table" },
        h("thead", {}, h("tr", {}, h("th", {}, "Task"), h("th", {}, "Merged"), h("th", { class: "num" }, "Cost"))),
        h("tbody", {}, ...v.merged.map((m) => h("tr", {},
          h("td", {}, m.title), h("td", {}, dayLabel(m.merged_at.slice(0, 10))),
          h("td", { class: "num" }, (m.estimated ? "≈" : "") + money(m.cost_usd))))))));
  }
  return out;
}

const stat = (label, value, sub) => h("div", { class: "spend-stat" },
  h("span", { class: "spend-stat-label" }, label), h("strong", {}, value), sub ? h("span", { class: "spend-stat-sub" }, sub) : null);

function svg(tag, attrs = {}, ...kids) {
  const el = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  el.append(...kids);
  return el;
}

// chart is one bar per day with a tooltip on hover (or tap), labels in HTML
// under it (the drawing stretches to the pane's width), and a table of the
// same days for screen readers.
function chart(days) {
  const W = 300, H = 80, top = 2;
  const max = Math.max(...days.map((d) => d.cost_usd), 0.01);
  const step = W / days.length, bw = Math.max(1, step - 2); // a 2px gap between bars
  const tip = h("div", { class: "spend-tip", hidden: true });
  const g = svg("g");
  days.forEach((d, i) => {
    const hgt = d.cost_usd > 0 ? Math.max(2, (H - top) * d.cost_usd / max) : 0;
    const x = i * step + (step - bw) / 2;
    const text = `${dayLabel(d.date, { weekday: "short", month: "short", day: "numeric" })}: ${d.estimated ? "≈" : ""}${money(d.cost_usd)}`;
    const r = Math.min(4, bw / 2, hgt); // rounded top, square at the baseline
    const bar = hgt ? svg("path", { class: "spend-bar", d: `M${x},${H}V${H - hgt + r}Q${x},${H - hgt} ${x + r},${H - hgt}H${x + bw - r}Q${x + bw},${H - hgt} ${x + bw},${H - hgt + r}V${H}Z` }) : null;
    const hit = svg("rect", { class: "spend-hit", x: i * step, y: 0, width: step, height: H });
    const show = () => {
      tip.textContent = text;
      tip.hidden = false;
      tip.style.left = `${((i + 0.5) / days.length) * 100}%`;
      tip.classList.toggle("end", i > days.length * 0.7);
      tip.classList.toggle("start", i < days.length * 0.3);
      for (const b of g.querySelectorAll(".spend-bar.on")) b.classList.remove("on");
      bar?.classList.add("on");
    };
    hit.addEventListener("pointerenter", show);
    hit.addEventListener("pointerdown", show);
    if (bar) g.append(bar);
    g.append(hit);
  });
  const pic = svg("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", "aria-hidden": "true" }, g);
  pic.addEventListener("pointerleave", () => { tip.hidden = true; for (const b of g.querySelectorAll(".spend-bar.on")) b.classList.remove("on"); });
  const labels = h("div", { class: "spend-labels" }, h("span", {}, dayLabel(days[0].date)), h("span", {}, `peak ${money(max)}`), h("span", {}, "today"));
  const table = h("table", { class: "sr-only" }, h("caption", {}, "Cost per day, last 30 days"),
    h("tbody", {}, ...days.map((d) => h("tr", {}, h("td", {}, d.date), h("td", {}, money(d.cost_usd))))));
  return h("div", { class: "spend-chart" }, h("div", { class: "spend-plot" }, pic, tip), labels, table);
}

// renderSpendBanner says above the board why the project's tasks don't
// start, while a spending cap holds them, with a way to the setting.
export function renderSpendBanner(p) {
  const el = $("#spend-banner"), hold = p?.spend_hold;
  el.hidden = !hold || !!p?.paused;
  if (el.hidden) return;
  const per = hold.period === "month" ? "monthly" : "daily";
  const until = hold.period === "month" ? new Date(hold.until).toLocaleDateString(timeLocale, { month: "long", day: "numeric" }) : "midnight";
  el.replaceChildren(h("strong", {}, `Spending cap reached: ${money(hold.spent_usd)} of ${capText(hold.cap_usd)} ${per}`),
    h("span", {}, `No new tasks start until ${until}, or until you raise the cap. Running tasks finish.`),
    h("button", { type: "button", class: "btn small", onclick: attempt(openSpendingSettings) }, "Spending settings"));
}

// openSpendingSettings opens Project settings → Spending.
export function openSpendingSettings() {
  openSettings();
  $('#settings-sheet .rail-item[data-rail="spending"]')?.click();
}
