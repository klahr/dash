import { invisiblesOn, vimOn } from "../editor.js";
import { $, applyClockPref, clockPref, h, ico } from "./dom.js";
import { attempt, copy } from "./dialogs.js";
import { state } from "./state.js";
import { toggleInvisibles, toggleVim } from "./diffedit.js";
import { openConnector } from "./wallview.js";
import { notifyOn, testPush, toggleNotifications } from "./notifications.js";
import { renderUsage } from "./usage.js";
import { accountSection, openAccount } from "./accountsheet.js";

// ------------------------------------------------------------------ preferences (this browser only)
export function syncPrefs() {
  const f = $("#prefs-form");
  f.vim.checked = vimOn();
  f.invisibles.checked = invisiblesOn();
  f.notify.checked = notifyOn();
  $("#push-hint").hidden = !(notifyOn() && state.pushOn);
  f.clock.value = clockPref();
}
export function openPrefs() { return openAccount("prefs"); }
accountSection("prefs", syncPrefs);
$("#prefs-form").vim.addEventListener("change", (e) => { if (e.target.checked !== vimOn()) toggleVim(); });
$("#prefs-form").invisibles.addEventListener("change", (e) => { if (e.target.checked !== invisiblesOn()) toggleInvisibles(); });
$("#prefs-form").clock.addEventListener("change", (e) => {
  try { e.target.value ? localStorage.setItem("dash.clock", e.target.value) : localStorage.removeItem("dash.clock"); } catch {}
  applyClockPref();
  renderUsage();
});
$("#push-test").addEventListener("click", attempt(testPush));
// Asking for permission needs this change event's user gesture.
$("#prefs-form").notify.addEventListener("change", attempt(async (e) => {
  try { if (e.target.checked !== notifyOn()) await toggleNotifications(); } finally { syncPrefs(); }
}));

// ------------------------------------------------------------------ Connect Claude (claude.ai custom connector)
// A guide to adding Dash as a custom connector in claude.ai, so Claude can
// work with your board from any chat. The connector only exists when the
// dashboard is served with -public-url; otherwise this says so.
const CLAUDE_CONNECTORS = "https://claude.ai/settings/connectors";

export function copyBlock(text, msg, { pre = false } = {}) {
  return h("div", { class: "copy-row" },
    h(pre ? "pre" : "code", { class: "copy-text" }, text),
    h("button", { class: "btn small shrink", type: "button", onclick: () => copy(text, msg) }, ico("copy"), "Copy"));
}

export function connectorSteps(c) {
  const step = (head, ...body) => h("li", {}, h("strong", {}, head), ...body);
  if (!c.enabled) {
    const admin = !!state.me?.user?.is_platform_admin;
    return [
      h("p", { class: "connector-status off" }, "The claude.ai connector isn't turned on for this dashboard."),
      admin
        ? h("p", { class: "hint" }, "Start Dash with ", h("code", {}, "-public-url"), " (or ", h("code", {}, "DASH_PUBLIC_URL"),
          ") set to the public HTTPS address people open it at. See docs/mcp.md.")
        : h("p", { class: "hint" }, "Ask whoever runs this dashboard to turn it on."),
    ];
  }
  const n = c.connected;
  const org = state.me?.org?.name;
  return [
    h("p", { class: `connector-status ${n ? "on" : ""}` },
      n ? `Connected: ${n} connector${n === 1 ? " is" : "s are"} signed in.` : "Ready. No connector has signed in yet."),
    h("ol", { class: "steps" },
      step("Copy the connector URL.", copyBlock(c.url, "Connector URL copied")),
      step("Open Connectors in claude.ai settings.", h("p", {},
        h("a", { class: "btn small", href: CLAUDE_CONNECTORS, target: "_blank", rel: "noopener" }, ico("external"), "Open claude.ai"))),
      step("Add a custom connector.", h("p", { class: "hint" }, "Name it Dash, paste the URL and add it.")),
      step("Connect and sign in.", h("p", { class: "hint" },
        "Dash's sign-in page opens. Sign in as you do here, then choose Allow.",
        org ? ` If you're in several organizations, pick the one Claude should work in (you're in ${org} now).` : " If you're in several organizations, pick the one Claude should work in.")),
      step("Use it in a chat.", h("p", { class: "hint" }, "Turn Dash on in the chat's tools menu. The connector also works in the Claude desktop and mobile apps."))),
    h("p", { class: "hint" }, "A connector stays signed in while it's used at least every 30 days. Disconnect it under Tokens & apps; changing your password signs every connector out."),
    h("footer", { class: "sheet-actions" },
      h("button", { class: "btn", type: "button", onclick: attempt(openConnector) }, ico("redo"), "Check again")),
  ];
}
