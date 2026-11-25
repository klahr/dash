// Commands to copy: the runner's one-line installers, shell quoting for the
// values put in them, the copy block that shows a command with a Copy
// button, and the "Add a runner" pane built from them (runnerCodePane). Both the app (app/runners.js) and planning.html
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
function installSteps(hub, code, { name = "", projects = "" } = {}) {
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
function runnerNameInput() {
  const input = h("input", { type: "text", placeholder: "its host name", autocomplete: "off", spellcheck: false, pattern: "[a-z0-9][a-z0-9-]{0,31}" });
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); input.blur(); } });
  return input;
}

// runnerCodePane is the body of "Add a runner" for the one-time code, which
// expires at expires: the install commands, the optional name (and, with
// withDir, projects directory) they are re-rendered for, the status line and
// how long the code works. steps(cmds) places the commands element among the
// caller's own steps; onInput({ name, projects }) re-renders anything else
// that shows them. foot (a Cancel button) goes beside the status, statusId is
// its id, and note follows the code's lifetime in the last hint.
export function runnerCodePane({ hub, code, expires, withDir = false, steps = (cmds) => [cmds], onInput = null, foot = null, statusId, note = [] }) {
  const mins = Math.round((new Date(expires) - Date.now()) / 60000);
  const name = runnerNameInput();
  const dir = withDir ? h("input", { type: "text", placeholder: "~/dash-projects", autocomplete: "off", spellcheck: false }) : null;
  const cmds = h("div", {});
  const render = () => {
    const v = { name: name.value.trim().toLowerCase(), projects: dir ? dir.value.trim() : "" };
    cmds.replaceChildren(...installSteps(hub, code, v));
    onInput?.(v);
  };
  name.addEventListener("input", render);
  dir?.addEventListener("input", render);
  const field = (label, input) => h("label", { class: "field" }, h("span", {}, label), input);
  const body = steps(cmds);
  render();
  const status = h("p", { class: "runner-add-status", id: statusId, role: "status" }, "Waiting for the runner to connect…");
  return [
    ...body,
    h("details", { class: "runner-add-opts" },
      h("summary", {}, dir ? "Name and projects directory (optional)" : "Name (optional)"),
      dir ? h("div", { class: "runner-add-fields" }, field("Name", name), field("Projects directory", dir)) : field("Name", name)),
    foot ? h("div", { class: "runner-add-foot" }, status, foot) : status,
    h("p", { class: "hint" }, `The code works once, for ${mins} minutes. `, note),
  ];
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
