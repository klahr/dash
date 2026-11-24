// Commands to copy: the runner's one-line installers, shell quoting for the
// values put in them, the copy block that shows a command with a Copy
// button, and the "Add a runner" steps built from them. Both the app (app/runners.js) and planning.html
// (app/plannerclaude.js) use them, so this module wires up nothing on load.
import { h, ico } from "./dom.js";
import { copy } from "./dialogs.js";

// shq quotes s for a POSIX shell, leaving a leading ~/ for it to expand.
export const shq = (s) => {
  if (s.startsWith("~/")) return `~/${s.length > 2 ? shq(s.slice(2)) : ""}`;
  return /^[\w@%+=:,./-]+$/.test(s) ? s : `'${s.replaceAll("'", `'\\''`)}'`;
};
// psq quotes s for PowerShell (a single-quoted string expands nothing).
export const psq = (s) => /^[\w%+=:./\\-]+$/.test(s) ? s : `'${s.replace(/['‘’‚‛]/g, (q) => q + q)}'`;

// installCmd installs (or updates) a runner from the hub at hub, with args
// as the installer's flags (--code …).
export const installCmd = (hub, args = "") => `curl -fsSL ${hub}/runner/install.sh | sh${args ? ` -s -- ${args}` : ""}`;
// installCmdWin is installCmd for Windows: the PowerShell installer, with
// args as PowerShell parameters (-Code …).
export const installCmdWin = (hub, args = "") => `& ([scriptblock]::Create((irm ${hub}/runner/install.ps1)))${args ? ` ${args}` : ""}`;

// installSteps shows how to install a runner from the hub at hub with the
// one-time code: a line for each system and its command to copy. name and
// projects (optional) are the runner's name and projects directory.
export function installSteps(hub, code, { name = "", projects = "" } = {}) {
  let args = `--code ${code}`, psArgs = `-Code ${code}`;
  if (name) { args += ` --name ${shq(name)}`; psArgs += ` -Name ${psq(name)}`; }
  if (projects) { args += ` --projects ${shq(projects)}`; psArgs += ` -Projects ${psq(projects)}`; }
  return [
    h("p", {}, "On Linux or macOS, in a terminal:"),
    copyBlock(installCmd(hub, args), "Command copied", { pre: true }),
    h("p", {}, "On Windows, in PowerShell (needs ", h("code", {}, "winget install Git.Git"), "):"),
    copyBlock(installCmdWin(hub, psArgs), "Command copied", { pre: true }),
  ];
}

// runnerNameInput is the field for a new runner's name. Enter in it keeps
// the name rather than submitting the sheet's form (which would close it).
export function runnerNameInput() {
  const input = h("input", { type: "text", placeholder: "its host name", autocomplete: "off", spellcheck: false, pattern: "[a-z0-9][a-z0-9-]{0,31}" });
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); input.blur(); } });
  return input;
}

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

// copyBlock shows text with a Copy button that copies it and says msg. pre
// shows a command as one, wrapping only between its words.
export function copyBlock(text, msg, { pre = false } = {}) {
  return h("div", { class: "copy-row" },
    h(pre ? "pre" : "code", { class: "copy-text" }, pre ? commandParts(text) : text),
    h("button", { class: "btn small shrink", type: "button", onclick: () => copy(text, msg) }, ico("copy"), "Copy"));
}
