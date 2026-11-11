import { $, h } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { refreshMachines } from "./events.js";
import { copyBlock } from "./prefs.js";
import { askPassword } from "./account.js";
import { agoText } from "./branches.js";
import { accountSection, openAccount } from "./accountsheet.js";

// ------------------------------------------------------------------ tokens & connected apps
// Everything that signs in as the user without a browser session: API
// tokens, OAuth clients (claude.ai connectors) they allowed, and runners
// they registered. Each is pinned to one org.
export function openTokens() { return openAccount("tokens"); }
accountSection("tokens", loadTokens);
async function loadTokens() {
  $("#token-secret").hidden = true;
  const orgs = state.me?.orgs || [];
  $("#token-org").replaceChildren(...orgs.map((o) => h("option", { value: o.id, selected: o.id === state.me?.org?.id }, o.name)));
  $("#token-org").hidden = orgs.length < 2;
  await renderTokens();
}

function tokenDetail(t) {
  const parts = [t.org.name];
  if (t.kind !== "runner") parts.push(t.scopes.includes("write") ? "read and write" : "read only");
  parts.push(t.last_used ? `used ${agoText(t.last_used)}` : "never used");
  if (t.kind === "oauth" && t.expires) parts.push(`signed in until ${new Date(t.expires).toLocaleDateString()} unless used`);
  else parts.push(`created ${new Date(t.created).toLocaleDateString()}`);
  return parts.join(" · ");
}

async function renderTokens() {
  const list = await api("GET", "/api/tokens", null, "");
  const row = (t) => {
    const what = { oauth: "Disconnect", runner: "Revoke", api: "Revoke" }[t.kind];
    return h("li", { class: "runner-row" },
      h("span", { class: "runner-name" }, t.name, t.kind === "api" ? h("code", { class: "hint" }, ` ${t.prefix}…`) : null),
      h("span", { class: "runner-seen hint" }, tokenDetail(t)),
      h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
        const body = {
          oauth: "The app is signed out at once and has to be connected again to use Dash.",
          runner: "The runner is disconnected now and its token stops working. Register it again with a new code to bring it back.",
          api: "Anything using this token loses access at once.",
        }[t.kind];
        if (!await ask({ title: `${what} ${t.name}?`, body, ok: what, danger: true })) return;
        await api("DELETE", `/api/tokens/${t.id}`, null, "");
        toast(`${t.name} ${t.kind === "oauth" ? "disconnected" : "revoked"}`);
        await renderTokens();
        if (t.kind === "runner") await refreshMachines();
      }) }, what));
  };
  const of = (kind, empty) => {
    const rows = list.filter((t) => t.kind === kind).map(row);
    return rows.length ? rows : empty ? [h("li", { class: "hint" }, empty)] : [];
  };
  $("#tokens-oauth").replaceChildren(...of("oauth", "No apps connected. See Connect Claude in the account menu."));
  $("#tokens-api").replaceChildren(...of("api", "No API tokens."));
  const runners = of("runner");
  $("#tokens-runner").replaceChildren(...runners);
  $("#tokens-runner-head").hidden = !runners.length;
}

export async function createToken() {
  const name = $("#token-name").value.trim();
  if (!name) { $("#token-name").focus(); throw new Error("Name the token, after what will use it."); }
  const orgID = Number($("#token-org").value || state.me?.org?.id);
  const org = (state.me?.orgs || []).find((o) => o.id === orgID);
  if (!org) throw new Error("Pick the organization the token works in.");
  const write = $("#token-scope").value === "write";
  const pw = await askPassword(`Create the token “${name}”?`,
    `It ${write ? "reads and changes" : "reads"} everything you can in ${org.name}, and nothing in your other organizations.`);
  if (pw == null) return;
  const res = await api("POST", "/api/tokens", { name, org_id: org.id, scopes: write ? ["read", "write"] : ["read"], password: pw }, "");
  $("#token-secret").replaceChildren(
    h("p", {}, "Copy the token now: Dash doesn't show it again."),
    copyBlock(res.secret, "Token copied", { pre: true }),
    h("p", { class: "hint" }, "Send it as ", h("code", {}, "Authorization: Bearer <token>"), ` to Dash's API (/api/…) or MCP endpoint (/mcp). It works in ${org.name} until you revoke it here, change your password or leave the organization.`));
  $("#token-secret").hidden = false;
  $("#token-name").value = "";
  await renderTokens();
}
