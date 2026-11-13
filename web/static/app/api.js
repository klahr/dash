import { state } from "./state.js";

// ------------------------------------------------------------------ REST client
// Several computers can share this dashboard. Each runs its own Dash; the
// one serving this page (the hub) forwards /peer/<name>/… to the others,
// runners (`dash runner`) through the connection they keep open to it. A project's
// API calls go to the computer it lives on (its prefix); login, logout and
// the computer list always go to this one.
const HUB_ONLY = /^\/api\/(login|logout|me|machines|version|chat|connector|runners|auth|orgs|invitations|push)\b/;
export const here = () => state.project?.prefix || "";

// Links from notifications open a task, /?project=<id>&task=<id>, or a goal, /?project=<id>&goal=<id>.
export const deepLink = (() => {
  const q = new URLSearchParams(location.search);
  const link = { project: q.get("project"), task: q.get("task"), goal: q.get("goal") };
  if (!link.project) return null;
  q.delete("project");
  q.delete("task");
  q.delete("goal");
  history.replaceState(null, "", location.pathname + (q.size ? `?${q}` : "") + location.hash);
  return link;
})();
export const on = (prefix, path) => (HUB_ONLY.test(path) ? path : `${prefix}${path}`);

export async function api(method, path, body, prefix = here(), { signal } = {}) {
  const res = await fetch(on(prefix, path), {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
    signal,
  });
  if (res.status === 401) {
    location.replace("/login.html");
    throw new Error("Sign in again to continue.");
  }
  const ct = res.headers.get("content-type") || "";
  const data = ct.includes("json") ? await res.json() : await res.text();
  if (res.status === 403 && data.code === "2fa_required") {
    // An org now requires two-factor sign-in: set it up on the login page.
    location.replace("/login.html?enrol=1");
    throw new Error(data.error);
  }
  if (res.status === 403 && data.code === "google_required") {
    // The org takes only Google sign-ins from this account: the page says how.
    location.reload();
    throw new Error(data.error);
  }
  if (res.status === 402 && data.code === "billing_read_only") document.dispatchEvent(new Event("dash:billing-read-only")); // billing shows the banner
  if (!res.ok) throw Object.assign(new Error(data.error || data || res.statusText), { status: res.status, data });
  return data;
}
