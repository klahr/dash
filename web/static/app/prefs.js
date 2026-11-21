import { invisiblesOn, vimOn } from "../editor.js";
import { $, applyClockPref, chatSuggestOn, clockPref, h, ico } from "./dom.js";
import * as store from "./store.js";
import { attempt } from "./dialogs.js";
import { saveField } from "./autosave.js";
import { state } from "./state.js";
import { toggleInvisibles, toggleVim } from "./diffedit.js";
import { openConnector } from "./wallview.js";
import { notifyOn, testPush, toggleNotifications } from "./notifications.js";
import { renderUsage } from "./usage.js";
import { copyBlock } from "./install.js";
import { accountSection, openAccount } from "./accountsheet.js";
import { dictationOn, dictationSupported, setDictation } from "./dictation.js";
import * as speech from "./speech.js";
import { setVoice, voiceOn } from "./voice.js";

// ------------------------------------------------------------------ preferences (this browser only)
export function syncPrefs() {
  const f = $("#prefs-form");
  f.vim.checked = vimOn();
  f.invisibles.checked = invisiblesOn();
  f.notify.checked = notifyOn();
  $("#push-hint").hidden = !(notifyOn() && state.pushOn);
  f.clock.value = clockPref();
  f.chatSuggest.checked = chatSuggestOn();
  f.dictation.checked = dictationOn();
  f.dictation.disabled = !dictationSupported;
  f.voice.checked = voiceOn();
  f.voice.disabled = !dictationSupported;
  $("#dictation-missing").hidden = dictationSupported;
  syncSpeech();
}
// Spoken replies: the switches for what is read wait for the main one.
function syncSpeech() {
  const f = $("#prefs-form"), on = speech.speechOn();
  f.speech.checked = on;
  f.speech.disabled = !speech.speechSupported;
  $("#speech-missing").hidden = speech.speechSupported;
  for (const [name, kind] of [["speechReplies", "replies"], ["speechApprovals", "approvals"], ["speechStatus", "status"]]) {
    f[name].checked = speech.speechKind(kind);
    f[name].disabled = !on;
  }
  f.speechLong.value = speech.speechLong();
  f.speechRate.value = String(speech.speechRate());
  if (!f.speechRate.value) f.speechRate.value = "1";
  // The voices, when the browser lists several; online ones say so.
  const voices = speech.speechVoices();
  $("#speech-voice-field").hidden = voices.length < 2;
  f.speechVoice.replaceChildren(h("option", { value: "" }, "Automatic (your browser's language)"),
    ...voices.map((v) => h("option", { value: v.voiceURI }, `${v.name} (${v.lang})${v.localService ? "" : " · online"}`)));
  f.speechVoice.value = speech.speechVoice();
  if (f.speechVoice.value !== speech.speechVoice()) f.speechVoice.value = "";
  for (const el of [f.speechLong, f.speechRate, f.speechVoice]) el.disabled = !on;
}
document.addEventListener("dash:voices", () => { if ($("#account-sheet").open) syncSpeech(); });
// The preferences are the Account sheet's first sections (Editors, Time, Chat, Dictation, Spoken replies), and
// this browser's push switch in Notifications.
export function openPrefs() { return openAccount("editors"); }
for (const key of ["editors", "notifications", "clock", "chat", "dictation", "speech"]) accountSection(key, syncPrefs);
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
pref("dictation", (el) => setDictation(el.checked));
pref("speech", (el) => { speech.setSpeech(el.checked); syncSpeech(); speech.speechSample(); });
pref("speechReplies", (el) => speech.setSpeechKind("replies", el.checked));
pref("speechApprovals", (el) => speech.setSpeechKind("approvals", el.checked));
pref("speechStatus", (el) => speech.setSpeechKind("status", el.checked));
pref("speechLong", (el) => speech.setSpeechLong(el.value));
pref("speechRate", (el) => { speech.setSpeechRate(el.value); speech.speechSample(); });
pref("speechVoice", (el) => { speech.setSpeechVoice(el.value); speech.speechSample(); });
pref("voice", (el) => setVoice(el.checked));
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

// copyBlock lives in app/install.js; the app's modules take it from here.
export { copyBlock };

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
