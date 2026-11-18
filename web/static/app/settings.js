import { $, dateText, h } from "./dom.js";
import { api } from "./api.js";
import { attempt, fail } from "./dialogs.js";
import { checked, fieldError, saveField, saveQueue } from "./autosave.js";
import { uploadFile } from "./attachments.js";
import { state } from "./state.js";
import { baseBranch, projectKey } from "./events.js";
import { projectIcon, renderProjectHeader, renderProjectList } from "./projects.js";
import { agoText, renderBoard } from "./branches.js";
import { DEFAULT_REVIEW_PROMPT } from "./drawmode.js";
import { loadClaudeFiles } from "./claudefiles.js";
import { changed } from "./routes.js";
import { loadWebhooks } from "./webhooks.js";
import { loadBoard } from "./backend.js";
import { renderBoardSettings } from "./boards.js";
import { missingLabels, parseLabels, servesProject } from "../groups.js";

// ------------------------------------------------------------------ project settings
export function openSettings() {
  const p = state.project;
  const f = $("#settings-form");
  f.elements.name.value = p.name || "";
  f.test_command.value = p.test_command || "";
  f.max_auto_fixes.value = p.max_auto_fixes ?? 2;
  f.setup_command.value = p.setup_command || "";
  f.copy_files.value = (p.copy_files || []).join("\n");
  f.allowed_tools.value = (p.allowed_tools || []).join("\n");
  // Check the radio directly: setting RadioNodeList.value to "" doesn't check value="" in Chromium.
  for (const r of f.chat_permission_mode) r.checked = r.value === (p.chat_permission_mode || "");
  f.review_prompt.value = p.review_prompt || "";
  f.review_prompt.placeholder = DEFAULT_REVIEW_PROMPT;
  f.task_instructions.value = p.task_instructions || "";
  f.run_command.value = p.run_command || "";
  f.preview_kind.value = p.preview_kind || "web";
  f.preview_command.value = p.preview_command || "";
  f.deploy_command.value = p.deploy_command || "";
  f.deploy_runs_on.value = (p.deploy_runs_on || []).join(", ");
  f.auto_push.checked = !!p.auto_push;
  f.auto_approve.checked = !!p.auto_approve;
  f.merge_mode.value = p.merge_mode || "local";
  f.merge_method.value = p.merge_method || "";
  f.investigate_ci.checked = p.investigate_ci !== false;
  f.tag_format.value = p.tag_format || "X.Y.Z";
  checkTagFormat();
  f.max_run_minutes.value = p.max_run_minutes || "";
  f.max_turns.value = p.max_turns || "";
  f.max_parallel.value = p.max_parallel || "";
  showColor(f, p.color || "");
  f.icon.value = p.icon || "";
  projectIcon(p, $("#settings-icon"));
  renderRunnerPicker(p);
  syncMergeHint();
  renderApprove();
  $("#webhook-secret").hidden = true;
  loadWebhooks().catch(fail);
  renderBoardSettings();
  loadBoard().catch(fail);
  loadClaudeFiles().catch(fail);
  loadRepoInfo();
  markShown(f);
  for (const el of f.querySelectorAll(".field-error")) el.remove();
  for (const el of f.querySelectorAll("[aria-invalid]")) el.removeAttribute("aria-invalid");
  $("#settings-sheet").showModal();
  changed("push");
}
// A project of this hub runs on runner labels, never on runners it names:
// its tasks go to the org's runners that have every label (as GitHub's
// runs-on). The field takes the labels; below it, the org's runners show
// whether they would work on it, live as the labels are typed. A member may
// only pick labels that send work to their own runners; admins any (the hub
// says so when saving).
function renderRunnerPicker(p) {
  const hub = !p.prefix && Array.isArray(state.runners);
  $("#settings-runners-field").hidden = !hub && !p.runs_on?.length;
  shownRunsOn = labelKey(p.runs_on);
  $("#settings-runs-on").value = (p.runs_on || []).join(", ");
  // The deploy's own labels, beside the deploy command: a hub's only.
  $("#settings-deploy-runs-on-row").hidden = $("#settings-runners-field").hidden;
  renderRunsOnPreview();
}

// renderDeployRunsOn says which of the runners the project would run on
// (as the labels are typed) its deploy goes to.
function renderDeployRunsOn() {
  const hint = $("#settings-deploy-runs-on-hint"), deploy = typedDeployRunsOn();
  hint.hidden = $("#settings-deploy-runs-on-row").hidden;
  hint.classList.remove("warn");
  if (hint.hidden) return;
  if (!deploy.length) {
    hint.textContent = "Deploys run on any runner the project runs on (Runners). Name labels to keep them to some of those runners.";
    return;
  }
  const names = (state.runners || []).filter((r) => servesProject(r, { runs_on: typedRunsOn() }) && servesProject(r, { runs_on: deploy })).map((r) => r.name);
  hint.classList.toggle("warn", !names.length);
  hint.textContent = names.length
    ? `Deploys go only to ${names.join(", ")}. If none of them is online, the deploy fails instead of running elsewhere.`
    : `No runner the project runs on has ${deploy.join(", ")}: deploys fail until one does. Give a runner labels on its page in Runners.`;
}
const typedDeployRunsOn = () => parseLabels($("#settings-deploy-runs-on").value).filter((l) => l.toLowerCase() !== "any");

// renderRunsOnPreview lists the org's runners as the typed labels would
// have it: those that would work on the project first.
function renderRunsOnPreview() {
  const want = { runs_on: typedRunsOn() };
  const rs = [...(state.runners || [])].map((r) => ({ r, ok: servesProject(r, want) })).sort((a, b) => b.ok - a.ok);
  $("#settings-runners").replaceChildren(...rs.map(({ r, ok }) => {
    const missing = missingLabels(r, want);
    return h("div", { class: `runner-pick ${ok ? "on" : ""}` },
      h("span", { class: `runner-dot ${r.online ? "on" : ""}`, "aria-hidden": "true" }),
      h("span", { class: "runner-name" }, r.name),
      h("span", { class: "hint" }, ok ? `works on it · ${r.mine ? "yours" : r.owner?.username || ""}`
        : want.runs_on.length ? `lacks ${missing.join(", ")}` : "no labels: runs nothing"));
  }));
  renderDeployRunsOn();
}
const typedRunsOn = () => parseLabels($("#settings-runs-on").value);
// shownRunsOn is the project's labels as the field was last drawn with.
let shownRunsOn = "";
const labelKey = (ls) => [...(ls || [])].map((l) => l.toLowerCase()).sort().join(",");

// syncSettingsRunners redraws the open settings' labels when the project's
// changed elsewhere (another browser), unless they are being changed here,
// and its runner list when the runners changed (their labels, say).
export function syncSettingsRunners() {
  const p = state.project;
  if (!$("#settings-sheet").open || !p) return;
  if (labelKey(p.runs_on) === shownRunsOn || labelKey(typedRunsOn()) !== shownRunsOn) {
    renderRunsOnPreview();
    return;
  }
  renderRunnerPicker(p);
  syncMergeHint();
  markSettingSaved("runs_on");
}

function syncMergeHint() {
  const f = $("#settings-form");
  const p = state.project;
  const onRunners = !$("#settings-runners-field").hidden && typedRunsOn().length > 0;
  const hint = $("#settings-runners-hint");
  hint.hidden = $("#settings-runners-field").hidden;
  const n = (state.runners || []).filter((r) => servesProject(r, { runs_on: typedRunsOn() })).length;
  hint.textContent = !onRunners
    ? "No labels: tasks, git history, files and commands wait until it runs on labels a runner has (this dashboard runs nothing itself)."
    : !p.remote_url ? "Runners clone the project's git remote: connect a GitHub repository first."
      : !n ? "No runner has all these labels yet: tasks wait until one does. Give a runner labels on its page in Runners."
        : "Tasks, git, files and commands run only on runners with all these labels, each in its own clone. If all of them are offline, tasks wait in the queue.";
  const pr = f.merge_mode.value === "pr";
  // On runners, merging is pushing: there's no separate push to turn on.
  $("#auto-push-row").hidden = pr || onRunners;
  $("#merge-method-row").hidden = !pr;
  const who = onRunners ? "the runner" : "Dash";
  $("#auto-approve-hint").textContent = pr
    ? "Pull requests whose checks pass wait for you to merge them, on GitHub or with Merge pull request in Dash, without waiting for an approving review first. Dash never merges a pull request on its own. Reviews asking for changes still go back to Claude."
    : "Finished tasks whose tests pass go straight to the merge queue instead of waiting for review, and the Review column is hidden while it's empty.";
  $("#merge-hint").textContent = !p.remote_url
    ? (pr ? "Connect a GitHub repository (Projects → Create GitHub repository) to open pull requests." : "")
    : pr ? `When Claude finishes a task, ${who} pushes its branch and opens a draft pull request against ${baseBranch(p)}, marked ready for review once its checks pass. The task waits in Review while you review it on GitHub: failing checks, conflicts and reviews asking for changes go back to Claude, whose fixes are pushed to the same pull request. Closing it rejects the task. Once it is approved and green, merge it on GitHub or with Merge pull request on the task (done with the gh account signed in on ${onRunners ? "the runner that ran it" : "this computer"}); Dash then marks the task merged. Dash never merges it on its own.`
    : onRunners ? `Approved tasks: the runner squashes the task onto ${baseBranch(p)}, runs the tests and pushes it, without waiting for GitHub's checks. If someone pushed first, it merges again on top; conflicts and failing tests go back to Claude.`
      : f.auto_push.checked ? `Before each merge Dash pulls ${baseBranch(p)} from GitHub, so pushes don't get rejected. GitHub's checks run only after it's pushed.` : "";
}
$("#settings-form").addEventListener("change", syncMergeHint);
$("#settings-runs-on").addEventListener("input", () => { renderRunsOnPreview(); syncMergeHint(); });
$("#settings-deploy-runs-on").addEventListener("input", renderDeployRunsOn);

// The tag format is checked as it is typed: placeholders, a valid git tag,
// and whether the repository's release workflows run for its tags.
let tagFormatTimer = 0, tagFormatSeq = 0;
const tagFormatCheck = (format) => api("GET", `/api/projects/${state.project.id}/tag-format?format=${encodeURIComponent(format)}`);
function checkTagFormat() {
  clearTimeout(tagFormatTimer);
  tagFormatTimer = setTimeout(attempt(async () => {
    const f = $("#settings-form");
    const format = f.tag_format.value.trim() || "X.Y.Z";
    const seq = ++tagFormatSeq;
    const res = await tagFormatCheck(format);
    if (seq !== tagFormatSeq) return;
    const hint = $("#tag-format-hint");
    f.tag_format.setCustomValidity(res.valid ? "" : res.error);
    hint.className = `hint${!res.valid ? " err-text" : res.hint ? " warn" : ""}`;
    if (!res.valid) return hint.replaceChildren(res.error);
    hint.replaceChildren(`Tags look like ${res.example}. Tag a release from a commit in History.`);
    if (res.hint) {
      hint.append(" ", res.hint.message);
      if (res.hint.suggest) hint.append(" ", h("button", { type: "button", class: "link inline", onclick: () => { f.tag_format.value = res.hint.suggest; checkTagFormat(); saveSetting("tag_format"); } }, `Use ${res.hint.suggest}`));
    }
  }), 250);
}
$("#settings-form").tag_format.addEventListener("input", checkTagFormat);
// The project color: one picker with None, a few colors, and any other
// color from the color input (picking one there chooses it).
const SWATCHES = [["#8aa4ff", "Blue"], ["#5ec4a8", "Teal"], ["#7fcf6b", "Green"], ["#f2c14e", "Yellow"],
  ["#f39a4e", "Orange"], ["#ef6b6b", "Red"], ["#e57ad2", "Pink"], ["#a98bf0", "Purple"]];
{
  const custom = $("#settings-swatches .swatch-custom");
  for (const [color, name] of SWATCHES) {
    const sw = h("label", { class: "swatch", title: name },
      h("input", { type: "radio", name: "color_pick", value: color, "aria-label": name }));
    sw.style.setProperty("--swatch", color);
    custom.before(sw);
  }
}
function showColor(f, color) {
  const preset = SWATCHES.some(([c]) => c === color.toLowerCase());
  for (const r of f.color_pick) r.checked = r.value === (!color ? "" : preset ? color.toLowerCase() : "custom");
  f.color.value = color && !preset ? color : "#8aa4ff";
}
$("#settings-form").color.addEventListener("input", (e) => { e.target.form.color_pick.value = "custom"; });
const settingsIcon = (icon) => {
  $("#settings-form").icon.value = icon;
  projectIcon({ ...state.project, icon }, $("#settings-icon"));
  saveSetting("icon");
};
$("#settings-icon-pick").addEventListener("click", () => $("#settings-icon-file").click());
$("#settings-icon-clear").addEventListener("click", () => settingsIcon(""));
$("#settings-icon-file").addEventListener("change", attempt(async (e) => {
  const file = e.target.files[0];
  e.target.value = "";
  if (!file) return;
  const a = await uploadFile(file, state.project.prefix || "");
  if (!a.type.startsWith("image/")) throw new Error("Choose a PNG, JPEG, GIF, WebP or SVG image.");
  settingsIcon(`${a.id}/${a.name}`);
}));
// Each field saves on its own as it changes (text fields on blur or Enter),
// with "Saved" in the sheet's header or an error under the field
// (autosave.js). FIELDS reads a field's value as the API takes it; check, if
// any, throws what is wrong with it.
const lines = (v) => v.split("\n").map((x) => x.trim()).filter(Boolean);
const FIELDS = {
  name: { read: (f) => f.elements.name.value.trim(), check: (v) => { if (!v) throw new Error("Give the project a name."); } },
  test_command: { read: (f) => f.test_command.value.trim() },
  max_auto_fixes: { read: (f) => Math.max(0, Number(f.max_auto_fixes.value) || 0) },
  setup_command: { read: (f) => f.setup_command.value.trim() },
  copy_files: { read: (f) => lines(f.copy_files.value) },
  allowed_tools: { read: (f) => lines(f.allowed_tools.value) },
  chat_permission_mode: { read: (f) => f.chat_permission_mode.value },
  review_prompt: { read: (f) => f.review_prompt.value.trim() },
  task_instructions: { read: (f) => f.task_instructions.value.trim() },
  run_command: { read: (f) => f.run_command.value.trim() },
  preview_kind: { read: (f) => f.preview_kind.value },
  preview_command: { read: (f) => f.preview_command.value.trim() },
  deploy_command: { read: (f) => f.deploy_command.value.trim() },
  auto_push: { read: (f) => f.auto_push.checked },
  auto_approve: { read: (f) => f.auto_approve.checked },
  merge_mode: { read: (f) => f.merge_mode.value },
  merge_method: { read: (f) => f.merge_method.value },
  investigate_ci: { read: (f) => f.investigate_ci.checked },
  tag_format: { read: (f) => f.tag_format.value.trim() || "X.Y.Z", check: async (v) => {
    const res = await tagFormatCheck(v);
    if (!res.valid) throw new Error(res.error);
  } },
  max_run_minutes: { read: (f) => Number(f.max_run_minutes.value) || 0 },
  max_turns: { read: (f) => Number(f.max_turns.value) || 0 },
  max_parallel: { read: (f) => Number(f.max_parallel.value) || 0 },
  color: { read: (f) => (f.color_pick.value === "custom" ? f.color.value : f.color_pick.value), el: () => $("#settings-color") },
  icon: { read: (f) => f.icon.value, el: () => $("#settings-icon-pick") },
  runs_on: { read: () => typedRunsOn(), skip: () => $("#settings-runners-field").hidden },
  deploy_runs_on: { read: () => typedDeployRunsOn(), skip: () => $("#settings-deploy-runs-on-row").hidden },
};
// A form control's name → the field it saves.
const FIELD_OF = { color_pick: "color" };
const fieldEl = (f, key) => {
  if (FIELDS[key].el) return FIELDS[key].el(f);
  const el = f.elements[key];
  return el instanceof RadioNodeList ? el[0].closest("fieldset") : el;
};
// shown: each field's value as last loaded or saved (JSON), so only what changed is saved.
const shown = {};
function markShown(f) { for (const key of Object.keys(FIELDS)) shown[key] = JSON.stringify(FIELDS[key].read(f)); }
// markSettingSaved takes a field's value in the form as saved (the API was
// told about it elsewhere, as Add to copy_files does).
export function markSettingSaved(key) { shown[key] = JSON.stringify(FIELDS[key].read($("#settings-form"))); }
const dirty = (f, key) => !FIELDS[key].skip?.() && JSON.stringify(FIELDS[key].read(f)) !== shown[key];

const queueSave = saveQueue();
function saveSetting(key) {
  return queueSave(async () => {
    const f = $("#settings-form"), field = FIELDS[key], el = fieldEl(f, key);
    if (field.skip?.()) return;
    const v = field.read(f), json = JSON.stringify(v);
    if (json === shown[key]) { fieldError(el, ""); return; }
    await saveField(el, async () => {
      await field.check?.(v);
      checked(el);
      const saved = await api("PATCH", `/api/projects/${state.project.id}`, { [key]: v });
      shown[key] = json;
      if (key === "runs_on") shownRunsOn = labelKey(saved.runs_on);
      // Keep the client-only fields, and the list entry in step for its name, color and icon.
      const cur = state.project;
      state.project = { ...saved, machine: cur.machine, prefix: cur.prefix };
      state.projects = state.projects.map((x) => (x === cur ? state.project : x));
      renderProjectHeader();
      renderProjectList();
      renderApprove();
      if (key === "auto_approve") renderBoard(); // auto approve shows or hides Review
    });
  });
}
const settingKey = (el) => { const key = FIELD_OF[el.name] || el.name; return FIELDS[key] ? key : null; };
$("#settings-form").addEventListener("change", (e) => {
  const key = settingKey(e.target);
  if (key) saveSetting(key);
});
// Enter in a one-line field saves it, instead of submitting the form (which closes the sheet).
$("#settings-form").addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || e.target.localName !== "input" || e.isComposing) return;
  if (["checkbox", "radio", "file", "button", "submit", "color"].includes(e.target.type)) return;
  e.preventDefault();
  const key = settingKey(e.target);
  if (key) saveSetting(key);
});
// Closing the sheet saves a field that was still being typed in.
$("#settings-sheet").addEventListener("close", () => {
  const f = $("#settings-form");
  for (const key of Object.keys(FIELDS)) if (dirty(f, key)) saveSetting(key);
});

// On a hub, a runner runs a project's command only if an org admin, its
// owner or someone it trusts set it (docs/several-computers.md). Saving a
// command unchanged approves it; with no Save button that is this button,
// shown while a command someone else set waits for your approval.
const COMMAND_FIELDS = { run: "run_command", test: "test_command", deploy: "deploy_command", setup: "setup_command", preview: "preview_command" };
function unapproved() {
  const p = state.project, me = state.me?.user?.id, admin = ["owner", "admin"].includes(state.me?.org?.role);
  return Object.entries(p.commands_by || {}).filter(([, by]) => !by.admin && by.user !== me && (admin || !(by.approved || []).includes(me)));
}
function renderApprove() {
  const wait = unapproved();
  $("#settings-approve").hidden = !wait.length;
  if (!wait.length) return;
  const names = [...new Set(wait.map(([, by]) => by.name).filter(Boolean))];
  const admin = ["owner", "admin"].includes(state.me?.org?.role);
  $("#settings-approve-hint").textContent = `${names.length ? names.join(", ") : "Someone"} changed commands of this project (${wait.map(([k]) => k).join(", ")}). ` +
    (admin ? "Runners don't run them until an admin approves them." : "Your runners don't run them until you approve them.");
}
$("#settings-approve-commands").addEventListener("click", (e) => queueSave(() => saveField(e.currentTarget, async () => {
  const p = state.project;
  const body = Object.fromEntries(unapproved().map(([k]) => [COMMAND_FIELDS[k], p[COMMAND_FIELDS[k]] || ""]).filter(([k]) => k));
  const saved = await api("PATCH", `/api/projects/${p.id}`, body);
  const cur = state.project;
  state.project = { ...saved, machine: cur.machine, prefix: cur.prefix };
  state.projects = state.projects.map((x) => (x === cur ? state.project : x));
  renderApprove();
})));

// ------------------------------------------------------------------ repository
// The Repository section of the project settings: what GitHub says about
// origin (description, license, latest release, counts and the repository's
// own settings), read with gh each time the settings open. Read-only: it is
// changed on GitHub.
let repoInfoSeq = 0;
async function loadRepoInfo() {
  const p = state.project, box = $("#settings-repo"), seq = ++repoInfoSeq;
  box.replaceChildren(h("p", { class: "hint" }, "Asking GitHub about the repository…"));
  let res;
  try {
    res = await api("GET", `/api/projects/${p.id}/repo-info`);
  } catch (err) {
    res = { remote_url: p.remote_url, github: null, error: err.message };
  }
  if (seq !== repoInfoSeq || projectKey(state.project) !== projectKey(p)) return;
  box.replaceChildren(...renderRepoInfo(p, res).filter(Boolean));
}

const LICENSE_NAMES = { other: "Other (GitHub couldn't tell which)" };
const fmtSize = (kb) => (kb >= 1024 * 1024 ? `${(kb / 1024 / 1024).toFixed(1)} GB` : kb >= 1024 ? `${(kb / 1024).toFixed(1)} MB` : `${kb} KB`);
// ext links to an outside web page; anything but an absolute http(s) URL
// (javascript:, data:, a path on Dash itself, …) is shown as plain text, since
// the links come from a runner's answer (the hub drops the ones it won't show).
const ext = (href, text) => {
  let ok = false;
  try { ok = ["https:", "http:"].includes(new URL(href).protocol); } catch {}
  return ok ? h("a", { href, target: "_blank", rel: "noopener" }, text) : h("span", {}, text);
};
const isSet = (iso) => iso && !iso.startsWith("0001-");

function renderRepoInfo(p, res) {
  const g = res.github;
  const remote = res.remote_url || p.remote_url;
  if (!g) {
    return [
      remote ? h("p", {}, "Origin: ", h("code", {}, remote)) : null,
      h("p", { class: "hint" }, res.error || "GitHub has nothing to say about this repository."),
      res.latest_tag ? h("p", {}, "Latest release tag: ", h("code", {}, res.latest_tag)) : null,
    ];
  }
  const tags = [
    h("span", { class: `scope-tag ${g.visibility === "PUBLIC" ? "repo" : "dash"}` }, (g.visibility || "").toLowerCase() || "private"),
    g.is_archived && h("span", { class: "scope-tag local" }, "archived"),
    g.is_fork && h("span", { class: "scope-tag" }, "fork"),
    g.is_template && h("span", { class: "scope-tag" }, "template"),
  ];
  const row = (label, ...value) => [h("dt", {}, label), h("dd", {}, ...value)];
  const on = (x) => (x ? "on" : "off");
  const rel = g.latest_release;
  const facts = [
    row("License", g.license ? (LICENSE_NAMES[g.license.key] || g.license.name || g.license.key) : h("span", { class: "hint" }, "None found")),
    row("Latest release", rel
      ? [ext(rel.url, rel.tag), rel.name && rel.name !== rel.tag ? ` “${rel.name}”` : "", isSet(rel.published_at) ? h("span", { class: "hint" }, ` · ${dateText(rel.published_at)}`) : null]
      : h("span", { class: "hint" }, "No GitHub release")),
    res.latest_tag && res.latest_tag !== rel?.tag ? row("Latest tag", h("code", {}, res.latest_tag), h("span", { class: "hint" }, " in this clone")) : null,
    g.default_branch ? row("Default branch", h("code", {}, g.default_branch)) : null,
    g.language ? row("Language", g.language) : null,
    g.parent ? row("Forked from", ext(`https://github.com/${g.parent}`, g.parent)) : null,
    row("Activity", `★ ${g.stars} · ${g.forks} forks · ${g.watchers} watching`),
    row("Open", ext(`${g.url}/issues`, `${g.open_issues} issues`), " · ", ext(`${g.url}/pulls`, `${g.open_prs} pull requests`)),
    isSet(g.pushed_at) ? row("Last push", agoText(g.pushed_at)) : null,
    isSet(g.created_at) ? row("Created", dateText(g.created_at)) : null,
    g.disk_kb ? row("Size", fmtSize(g.disk_kb)) : null,
    g.viewer_permission ? row("Your access", g.viewer_permission.toLowerCase()) : null,
  ];
  const methods = [g.squash_merge && "squash", g.merge_commit && "merge commit", g.rebase_merge && "rebase"].filter(Boolean);
  const settings = [
    row("Pull request merges", methods.length ? methods.join(", ") : "none allowed"),
    row("Delete branch on merge", on(g.delete_branch_on_merge)),
    row("Features", Object.entries({ Issues: g.has_issues, Wiki: g.has_wiki, Discussions: g.has_discussions, Projects: g.has_projects })
      .map(([k, v]) => `${k} ${on(v)}`).join(" · ")),
  ];
  // Things that trip Dash up, from the repository's settings.
  const warnings = [
    g.default_branch && p.default_branch && g.default_branch !== p.default_branch && `GitHub's default branch is ${g.default_branch}, but this project's is ${p.default_branch}.`,
    g.is_archived && "The repository is archived: it is read-only, so pushes fail.",
  ].filter(Boolean);
  const admin = g.viewer_permission === "ADMIN";
  return [
    h("h3", { class: "claude-scope-head" }, ext(g.url, g.name_with_owner), ...tags),
    h("p", { class: g.description ? "repo-desc" : "hint" }, g.description || "No description."),
    g.homepage ? h("p", {}, ext(g.homepage, g.homepage)) : null,
    g.topics.length ? h("p", { class: "repo-topics" }, ...g.topics.map((t) => ext(`https://github.com/topics/${encodeURIComponent(t)}`, t))) : null,
    ...warnings.map((w) => h("p", { class: "hint warn" }, w)),
    h("dl", { class: "repo-facts" }, ...facts.flat().filter(Boolean)),
    h("h3", { class: "claude-scope-head repo-sub" }, "GitHub settings"),
    h("dl", { class: "repo-facts" }, ...settings.flat()),
    h("p", { class: "hint" }, "Read from GitHub when the settings open; ",
      admin ? ext(`${g.url}/settings`, "change them on GitHub") : "an admin of the repository changes them on GitHub", "."),
  ];
}
