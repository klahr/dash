import { $, h, ib } from "./dom.js";
import { api } from "./api.js";
import { attempt, busy, toast } from "./dialogs.js";
import { projectFirst, state } from "./state.js";
import { ago, agoText, title } from "./branches.js";
import { openDetail } from "./detail.js";

// ------------------------------------------------------------------ GitHub Actions
const CI_LABEL = { passing: "Build passing", failing: "Build failing", running: "Build running", none: "No builds yet", unknown: "Builds unavailable" };
export async function loadCI() {
  const id = state.project?.id;
  if (!id || !state.project.remote_url) return;
  const st = await api("GET", `/api/projects/${id}/ci`);
  if (state.project?.id === id) { state.ci = st; renderCI(); }
}

export function runState(r) {
  if (r.status !== "completed") return ["running", "Running"];
  return { success: ["passing", "Passed"], failure: ["failing", "Failed"], timed_out: ["failing", "Timed out"],
    startup_failure: ["failing", "Couldn't start"], cancelled: ["muted", "Canceled"], skipped: ["muted", "Skipped"] }[r.conclusion] || ["muted", r.conclusion];
}

const RELEASE_LABEL = { building: "building", published: "published", failed: "failed", no_build: "pushed, but no workflow started for it" };
// A release counts as news in the top bar while it builds, and for a day if it failed.
const releaseShown = (rel) => rel && (rel.state === "building" || (rel.state === "failed" && Date.now() - new Date(rel.pushed) < 864e5));

function runRow(r) {
  const [cls, label] = runState(r);
  return h("li", {}, h("a", { class: `run run-${cls}`, href: r.url, target: "_blank", rel: "noopener" },
    h("span", { class: "run-dot", "aria-hidden": "true" }),
    h("span", { class: "run-main" }, h("strong", {}, r.workflow), h("span", { class: "run-title" }, r.title)),
    h("span", { class: "run-side" }, label, h("span", { class: "run-sha" }, `${r.sha.slice(0, 7)} · ${ago(r.created_at)}`))));
}

// releaseNews toasts how a release build ended.
export function releaseNews(prev, rel) {
  if (!rel || prev?.tag !== rel.tag || prev.state !== "building" || rel.state === "building") return;
  const view = rel.url ? { label: "View", run: () => window.open(rel.url, "_blank", "noopener") } : null;
  if (rel.state === "published") toast("", { tone: "ok", icon: "tag", head: `Released ${rel.tag}`, action: view });
  else if (rel.state === "failed") toast("The release workflow failed.", { tone: "error", head: `Release ${rel.tag} failed`, action: { label: "Ask Claude to fix it", run: investigateCI } });
  else if (rel.state === "no_build") toast("No workflow started for it: check the tag format in the project settings.", { tone: "info", icon: "tag", head: `Pushed ${rel.tag}`, action: view });
}

// A running build shows as a pulsing ring on the current project's row in the
// sidebar (renderSideProjects places it); it opens the builds sheet.
export const ciBuilding = h("button", { type: "button", class: "side-ci-build", "aria-haspopup": "dialog", hidden: true });
ciBuilding.addEventListener("click", attempt(openCI));

export function renderCI() {
  const st = state.ci;
  const rel = st?.release;
  // The project has builds to show: the sidebar's Builds action opens them,
  // saying how the latest went (a running one is also the ring on the project's row).
  const has = !!(state.project?.remote_url && st && st.state !== "unknown" && st.state !== "none") || st?.state === "failing" || releaseShown(rel);
  const btn = $("#pm-builds");
  btn.hidden = !has;
  const relFailed = releaseShown(rel) && rel.state === "failed";
  const failing = has && (relFailed || st?.state === "failing");
  const running = has && !failing && (rel?.state === "building" || st?.state === "running");
  btn.classList.toggle("bad", failing);
  btn.classList.toggle("ok", has && !failing && !running && st?.state === "passing");
  btn.querySelector("span").textContent = relFailed ? `Release ${rel.tag} failed` : rel?.state === "building" && has ? `Building ${rel.tag}` : CI_LABEL[st?.state] || "Builds";
  btn.title = "GitHub Actions on the default branch";
  const building = has && rel?.state === "building" ? `Building ${rel.tag}`
    : has && st?.state === "running" ? `Build running on ${st.branch || "the default branch"}` : "";
  ciBuilding.hidden = !building;
  ciBuilding.title = building;
  ciBuilding.setAttribute("aria-label", building);
  if (!st) return;
  $("#ci-release").hidden = !rel;
  if (rel) {
    $("#ci-release").replaceChildren(
      h("p", { class: `hint ci-release-${rel.state}` },
        h("strong", {}, `Release ${rel.tag}`), ` ${RELEASE_LABEL[rel.state] || rel.state}`,
        rel.state === "building" && !rel.runs.length ? " (waiting for its workflow to start)" : "",
        ". ", rel.url ? h("a", { href: rel.url, target: "_blank", rel: "noopener" }, "GitHub release") : ""),
      ...(rel.runs.length ? [h("ul", { class: "runs" }, ...rel.runs.map(runRow))] : []));
  }
  const fix = st.todo_id && state.todos.get(st.todo_id);
  $("#ci-summary").replaceChildren(
    `${CI_LABEL[st.state]} on ${st.branch || "the default branch"}`,
    st.sha ? ` at ${st.sha.slice(0, 8)}` : "", ". ",
    st.error ? h("span", { class: "err-text" }, st.error) : `Checked ${agoText(st.checked)}.`,
    fix ? h("span", { class: "ci-fix" }, " Claude is fixing it: ", ib({ type: "button", class: "link inline", onclick: () => { $("#ci-sheet").close(); openDetail(fix.id); } }, "right", title(fix))) : "");
  $("#ci-runs").replaceChildren(...(st.runs || []).slice(0, 12).map(runRow));
  $("#ci-investigate").hidden = !((st.state === "failing" || rel?.state === "failed") && !fix);
  // A finished build can be run again on GitHub (failed jobs only when it failed).
  const failed = st.state === "failing" || rel?.state === "failed";
  $("#ci-retry").hidden = !(failed || st.state === "passing");
  $("#ci-retry .label").textContent = failed ? "Retry failed jobs" : "Re-run build";
}

export async function openCI() {
  if (!state.project) throw new Error(projectFirst(" (p)"));
  if (!state.project.remote_url) throw new Error("Connect a GitHub repository first (Projects → Create GitHub repository).");
  if (!state.ci) await loadCI();
  renderCI();
  $("#ci-sheet").showModal();
}
$("#pm-builds").addEventListener("click", attempt(openCI));
$("#ci-refresh").addEventListener("click", attempt(async () => {
  state.ci = await api("POST", `/api/projects/${state.project.id}/ci/refresh`);
  renderCI();
  toast("Builds checked");
}));
$("#ci-retry").addEventListener("click", attempt(async () => {
  state.ci = await busy($("#ci-retry"), () => api("POST", `/api/projects/${state.project.id}/ci/retry`));
  renderCI();
  toast("Build restarted on GitHub");
}));
async function investigateCI() {
  const t = await api("POST", `/api/projects/${state.project.id}/ci/investigate`);
  if ($("#ci-sheet").open) $("#ci-sheet").close();
  toast(`Queued: ${title(t)}`);
}
$("#ci-investigate").addEventListener("click", attempt(investigateCI));
