import { state } from "./state.js";

// ------------------------------------------------------------------ REST client
// Several computers can share this dashboard. Each runs its own Dash; the
// one serving this page (the hub) forwards /peer/<name>/… to the others,
// runners (`dash runner`) through the connection they keep open to it. A project's
// API calls go to the computer it lives on (its prefix); login, logout and
// the computer list always go to this one.
const HUB_ONLY = /^\/api\/(login|logout|me|machines|version|chat|connector|runners|auth|orgs|invitations|push)\b/;
export const here = () => state.project?.prefix || "";

// signIn goes to the sign-in page, which comes back to this page's URL.
export function signIn() {
  const here = location.pathname + location.search;
  location.replace("/login.html" + (here !== "/" ? `?next=${encodeURIComponent(here)}` : ""));
}
export const on = (prefix, path) => (HUB_ONLY.test(path) ? path : `${prefix}${path}`);

// request sends one call and handles what every call can meet: a lapsed
// sign-in, an org's two-factor or Google rule, a read-only billing state and
// the server's error message. It returns the answer (data) and the response
// (res), for callers that read a header. body is sent as JSON, or with raw as
// it is (a File or Blob, with its own Content-Type).
export async function request(method, path, { body, raw = false, prefix = here(), signal } = {}) {
  let headers = {};
  if (raw && body) headers = { "Content-Type": body.type || "application/octet-stream" };
  else if (body) headers = { "Content-Type": "application/json" };
  const res = await fetch(on(prefix, path), {
    method,
    headers,
    body: body ? (raw ? body : JSON.stringify(body)) : undefined,
    signal,
  });
  if (res.status === 401) {
    signIn();
    throw new Error("Sign in again to continue.");
  }
  const ct = res.headers.get("content-type") || "";
  const data = ct.includes("json") ? await res.json() : await res.text();
  if (res.status === 403 && data.code === "2fa_required") {
    // An org now requires two-factor sign-in: set it up on the login page.
    location.replace("/login.html?enrol=1");
    throw new Error(data.error);
  }
  if (res.status === 403 && data.code === "google_required" && !data.org_id) {
    // The active org takes only Google sign-ins from this account: the page says how.
    // With org_id it's about another org (a token for it): the error says so.
    location.reload();
    throw new Error(data.error);
  }
  if (res.status === 402 && data.code === "billing_read_only") document.dispatchEvent(new Event("dash:billing-read-only")); // billing shows the banner
  if (!res.ok) throw Object.assign(new Error(data.error || data || res.statusText), { status: res.status, data });
  return { data, res };
}

// api is request for the usual call: a JSON body in, the answer out.
export async function api(method, path, body, prefix = here(), { signal } = {}) {
  return (await request(method, path, { body, prefix, signal })).data;
}
