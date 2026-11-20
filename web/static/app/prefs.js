import { invisiblesOn, vimOn } from "../editor.js";
import { $, applyClockPref, chatSuggestOn, clockPref, h, ico } from "./dom.js";
import * as store from "./store.js";
import { attempt, copy } from "./dialogs.js";
import { saveField } from "./autosave.js";
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
  f.chatSuggest.checked = chatSuggestOn();
}
// The preferences are the Account sheet's first sections (Editors, Time, Chat), and
// this browser's push switch in Notifications.
export function openPrefs() { return openAccount("editors"); }
for (const key of ["editors", "notifications", "clock", "chat"]) accountSection(key, syncPrefs);
// Each saves as it changes, with "Saved" in the sheet's header.
const pref = (name, fn) => $("#prefs-form")[name].addEventListener("change", (e) => saveField(e.target, async () => fn(e.target)));
pref("vim", (el) => { if (el.checked !== vimOn()) toggleVim(); });
pref("invisibles", (el) => { if (el.checked !== invisiblesOn()) toggleInvisibles(); });
pref("clock", (el) => {
  store.set("dash.clock", el.value || null);
  applyClockPref();
  renderUsage();
});
pref("chatSuggest", (el) => {
  store.setFlag("dash.chatSuggest", el.checked);
  // Turned off: the chats drop a suggestion they show.
  document.dispatchEvent(new Event("dash:chatsuggest"));
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

// commandParts splits a command into words the browser may not break inside,
// so "--code C8R6-DYXQ" never wraps as "-" and "-code …". A flag keeps its
// value with it. Long words (URLs) stay breakable so they can't overflow a
// phone screen. The text itself is unchanged: the parts join back to it.
const NOBREAK_MAX = 40;
function commandParts(text) {
  const words = text.split(/( )/);
  const out = [];
  for (let i = 0; i < words.length; i++) {
    let w = words[i];
    const next = words[i + 2];
    if (w.startsWith("-") && next && !next.startsWith("-") && !w.includes("\n") && !next.includes("\n")
        && w.length + 1 + next.length <= NOBREAK_MAX) {
      w = `${w} ${next}`;
      i += 2;
    }
    out.push(w && w !== " " && w.length <= NOBREAK_MAX && !w.includes("\n") ? h("span", { class: "nobreak" }, w) : w);
  }
  return out;
}

export function copyBlock(text, msg, { pre = false } = {}) {
  return h("div", { class: "copy-row" },
    h(pre ? "pre" : "code", { class: "copy-text" }, pre ? commandParts(text) : text),
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
    h("p", { class: "hint" }, "Changing your password signs every connector out."),
    h("footer", { class: "sheet-actions" },
      h("button", { class: "btn", type: "button", onclick: attempt(openConnector) }, ico("redo"), "Check again")),
  ];
}
