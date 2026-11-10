import { $, h, timeLocale } from "./dom.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { agoText } from "./branches.js";

// ------------------------------------------------------------------ origin (background fetch)
// Dash fetches origin's default branch when a project is selected and every
// minute after, so commits pushed from another computer show up early.
// fetch=false only keeps the project marked as selected on the server.
export async function loadRemote(fetch, force) {
  const p = state.project;
  if (!p?.remote_url) return;
  const st = await api(fetch ? "POST" : "GET", `/api/projects/${p.id}/remote${fetch ? `/fetch${force ? "?force=1" : ""}` : ""}`);
  if (projectKey(state.project) === projectKey(p)) { state.remote = st; renderRemote(); }
}

export function renderRemote() {
  const st = state.remote, pill = $("#remote-pill");
  const n = (k, one, many) => `${k} ${k === 1 ? one : many}`;
  pill.hidden = !(st?.enabled && (st.behind || st.ahead || st.error));
  if (pill.hidden) return;
  const b = st.branch;
  const parts = [st.behind && `origin is ${n(st.behind, "commit", "commits")} ahead`, st.ahead && `local is ${n(st.ahead, "commit", "commits")} ahead`].filter(Boolean);
  pill.classList.toggle("remote-error", !!st.error);
  pill.textContent = st.error ? "Can't reach origin" : st.behind && st.ahead ? `${b}: ↓${st.behind} ↑${st.ahead}` : st.behind ? `origin/${b} +${st.behind}` : `${b} +${st.ahead} unpushed`;
  pill.title = [
    parts.length ? `${b}: ${parts.join(", ")}.` : "",
    st.behind && !st.ahead ? "New tasks start from origin's version." : "",
    st.behind && st.ahead ? "The next merge reconciles them." : "",
    st.error ? `Last fetch failed: ${st.error}. Retrying ${st.retry_at ? `at ${new Date(st.retry_at).toLocaleTimeString(timeLocale, { hour: "2-digit", minute: "2-digit" })}` : "soon"}.` : "",
    st.fetched ? `Fetched ${agoText(st.fetched)}. Click to fetch now.` : "Click to fetch now.",
  ].filter(Boolean).join(" ");
  pill.setAttribute("aria-label", pill.title);
}
$("#remote-pill").addEventListener("click", attempt(async () => { await loadRemote(true, true); toast("Fetching origin"); }));
// Checkouts behind origin: when another runner pushes, the project's other
// runners fetch at once and fast-forward a clean checkout; one with
// uncommitted changes or local commits is left alone and listed here, with
// a Pull button (fast-forward only; the runner refuses anything else).
export async function loadCheckouts() {
  const p = state.project;
  if (!p?.remote_url) return;
  const list = await api("GET", `/api/projects/${p.id}/checkouts`);
  if (projectKey(state.project) === projectKey(p)) { state.checkouts = list || []; renderCheckouts(); }
}

export function renderCheckouts() {
  const box = $("#checkouts"), list = state.checkouts || [];
  box.hidden = !list.length;
  box.replaceChildren(...list.map((c) => {
    const where = c.runner_name || "This checkout";
    const n = `${c.behind} ${c.behind === 1 ? "commit" : "commits"}`;
    const why = c.stuck ? `Left alone: ${c.stuck}.` : "Catching up with origin…";
    return h("div", { class: "checkout-behind", title: `${where}: ${c.branch} is ${n} behind origin. ${why}` },
      h("span", {}, `${where}: ${n} behind origin`, h("span", { class: "hint" }, c.stuck || `catching up on ${c.branch}`)),
      h("button", { class: "btn small", type: "button", title: "Fast-forward to origin (only if nothing local is in the way)",
        onclick: attempt(async (e) => {
          e.currentTarget.disabled = true;
          const q = c.runner ? `?runner=${encodeURIComponent(c.runner)}` : "";
          try {
            const res = await api("POST", `/api/projects/${state.project.id}/remote/pull${q}`);
            toast(res?.moved ? `Pulled ${res.moved} ${res.moved === 1 ? "commit" : "commits"} into ${where === "This checkout" ? "the checkout" : where}` : "Already up to date");
          } finally {
            loadCheckouts().catch(() => {});
          }
        }) }, "Pull"));
  }));
}

// Keep the open project marked as selected so the server keeps fetching it.
setInterval(() => { if (!document.hidden) loadRemote(false).catch(() => {}); }, 4 * 60000);
document.addEventListener("visibilitychange", () => { if (!document.hidden) loadRemote(true).catch(() => {}); });
