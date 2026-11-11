import { $, h, ico } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { accountSection, openAccount } from "./accountsheet.js";
import { connectorSteps, copyBlock } from "./prefs.js";
import { agoText } from "./branches.js";

// ------------------------------------------------------------------ wall view
// Configures /wall: which projects it shows and privacy mode (kept in this
// browser), a link to open it here, and the org admins' TV links (wall
// tokens). A TV link's secret is shown only when it's made; while the sheet
// stays open its link follows the options.
const wall = { secret: "", tokenName: "" };
const wallOpts = () => {
  const f = $("#wall-form");
  return { project: f.project.value, privacy: f.privacy.checked };
};
function wallURL(secret = "") {
  const o = wallOpts(), q = new URLSearchParams();
  if (secret) q.set("token", secret);
  if (o.project) q.set("project", o.project);
  if (o.privacy) q.set("privacy", "1");
  const s = q.toString();
  return `${location.origin}/wall${s ? `?${s}` : ""}`;
}

accountSection("wall", loadWall);
async function loadWall() {
  if (!state.me?.org) throw new Error("You aren't in an organization.");
  const f = $("#wall-form");
  let saved = {};
  try { saved = JSON.parse(localStorage.getItem("dash.wall") || "{}"); } catch {}
  const projects = await api("GET", "/api/projects", null, "").catch(() => []);
  $("#wall-project").replaceChildren(h("option", { value: "" }, "All projects"),
    ...projects.map((p) => h("option", { value: p.name }, p.name)));
  f.project.value = projects.some((p) => p.name === saved.project) ? saved.project : "";
  f.privacy.checked = !!saved.privacy;
  wall.secret = "";
  $("#wall-token-secret").hidden = true;
  $("#wall-org").textContent = state.me.org.name;
  renderWallLink();
  await renderWallTokens();
}

function renderWallLink() {
  const url = wallURL();
  $("#wall-link").replaceChildren(
    h("div", { class: "copy-row" },
      h("code", { class: "copy-text" }, url),
      h("a", { class: "btn small shrink", href: url, target: "_blank", rel: "noopener" }, ico("external"), "Open")),
    h("p", { class: "hint" }, "Opened signed in, it shows your active organization. On a TV, use a TV link below instead."));
  if (wall.secret) {
    const link = wallURL(wall.secret);
    $("#wall-token-secret").replaceChildren(
      h("p", {}, `Copy the link for ${wall.tokenName} now: Dash doesn't show it again.`),
      copyBlock(link, "Wall link copied", { pre: true }),
      h("p", { class: "hint" }, "Open it on the TV. It follows the options above while this stays open; later, change ",
        h("code", {}, "project"), " and ", h("code", {}, "privacy"), " in the link itself."));
  }
}

async function renderWallTokens() {
  let list;
  try {
    list = await api("GET", "/api/wall/tokens", null, "");
  } catch (e) {
    if (e.status !== 403) throw e;
    $("#wall-tokens").replaceChildren(h("li", { class: "hint" }, "Only the organization's owners and admins make TV links."));
    $("#wall-token-new").hidden = true;
    return;
  }
  $("#wall-token-new").hidden = false;
  $("#wall-tokens").replaceChildren(...(list.length ? list.map((t) => h("li", { class: "item-row" },
    h("span", { class: "runner-name" }, t.name, h("code", { class: "hint" }, ` ${t.prefix}…`)),
    h("span", { class: "item-sub hint" }, [t.last_used ? `used ${agoText(t.last_used)}` : "never used", `created ${new Date(t.created).toLocaleDateString()}`].join(" · ")),
    h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
      if (!await ask({ title: `Revoke ${t.name}?`, body: "Screens opened with this link stop updating and show that it was revoked.", ok: "Revoke", danger: true })) return;
      await api("DELETE", `/api/wall/tokens/${t.id}`, null, "");
      toast(`${t.name} revoked`);
      await renderWallTokens();
    }) }, "Revoke"))) : [h("li", { class: "hint" }, "No TV links.")]));
}

async function createWallToken() {
  const name = $("#wall-token-name").value.trim() || "Wall";
  const res = await api("POST", "/api/wall/tokens", { name }, "");
  wall.secret = res.secret;
  wall.tokenName = res.token.name;
  $("#wall-token-name").value = "";
  $("#wall-token-secret").hidden = false;
  renderWallLink();
  await renderWallTokens();
}

$("#wall-token-create").addEventListener("click", attempt(createWallToken));
$("#wall-token-name").addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); attempt(createWallToken)(); } });
$("#wall-form").addEventListener("change", () => {
  try { localStorage.setItem("dash.wall", JSON.stringify(wallOpts())); } catch {}
  renderWallLink();
});
$("#account-sheet").addEventListener("close", () => { wall.secret = ""; $("#wall-token-secret").replaceChildren(); });

export function openConnector() { return openAccount("connector"); }
accountSection("connector", loadConnector);
async function loadConnector() {
  const c = await api("GET", "/api/connector");
  $("#connector-body").replaceChildren(...connectorSteps(c));
}
