import { $, h, ico } from "./dom.js";
import { api } from "./api.js";

// The platform admins' notice (Admin → Incident response, or dash lockdown):
// a banner at the top of the page after sign-in, until this person
// dismisses it or an admin clears it.
export async function showNotice() {
  let n = null;
  try {
    n = (await api("GET", "/api/notice", undefined, "")).notice;
  } catch {
    return;
  }
  $("#site-notice")?.remove();
  if (!n) return;
  const paras = n.body.split(/\n\s*\n/).map((p) => p.trim()).filter(Boolean);
  const bar = h("div", { class: "site-notice", id: "site-notice", role: "status" },
    h("div", { class: "site-notice-body" },
      h("strong", {}, "A message from the administrators"),
      ...paras.map((p) => h("p", {}, p))),
    h("button", {
      class: "icon-btn", type: "button", "aria-label": "Dismiss the message", title: "Dismiss",
      onclick: () => {
        bar.remove();
        api("POST", `/api/notice/${n.id}/dismiss`, undefined, "").catch(() => {});
      },
    }, ico("x")));
  document.querySelector("main:not([hidden])")?.prepend(bar);
}

showNotice();
