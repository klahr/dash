import { $, h } from "./dom.js";
import { api } from "./api.js";
import { attempt, fail, toast } from "./dialogs.js";
import { uploadFile } from "./attachments.js";
import { state } from "./state.js";
import { baseBranch, projectKey } from "./events.js";
import { projectIcon, renderProjectHeader, renderProjectList } from "./projects.js";
import { agoText, renderBoard } from "./branches.js";
import { DEFAULT_REVIEW_PROMPT } from "./drawmode.js";
import { loadClaudeFiles } from "./claudefiles.js";
import { loadWebhooks } from "./webhooks.js";
import { loadBoard } from "./backend.js";

// ------------------------------------------------------------------ project settings
export function openSettings() {
  const p = state.project;
  const f = $("#settings-form");
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
  f.auto_push.checked = !!p.auto_push;
  f.auto_approve.checked = !!p.auto_approve;
  f.merge_mode.value = p.merge_mode || "local";
  f.merge_green_prs.checked = p.merge_green_prs !== false;
  f.investigate_ci.checked = p.investigate_ci !== false;
  f.tag_format.value = p.tag_format || "X.Y.Z";
  checkTagFormat();
  f.max_run_minutes.value = p.max_run_minutes || "";
  f.max_turns.value = p.max_turns || "";
  f.use_color.checked = !!p.color;
  f.color.value = p.color || "#8aa4ff";
  f.color.disabled = !p.color;
  f.icon.value = p.icon || "";
  projectIcon(p, $("#settings-icon"));
  renderRunnerPicker(p);
  syncMergeHint();
  $("#webhook-secret").hidden = true;
  loadWebhooks().catch(fail);
  loadBoard().catch(fail);
  loadClaudeFiles().catch(fail);
  loadRepoInfo();
  settingsSnapshot = formSnapshot(f);
  $("#settings-sheet").returnValue = "";
  $("#settings-sheet").showModal();
}
export let settingsSnapshot = "";
// markSettingsSaved takes the form as it is now as saved (opening a Claude file won't ask to save it first).
export function markSettingsSaved(f) { settingsSnapshot = formSnapshot(f); }
export const formSnapshot = (f) => new URLSearchParams(new FormData(f)).toString();
// The runners a project of this hub can be linked to: the org's. A member
// links their own; admins any. Linked, its tasks run only there and the
// runner pushes approved work to the default branch.
function renderRunnerPicker(p) {
  const runners = !p.prefix && state.runners;
  $("#settings-runners-field").hidden = !runners?.length && !p.runners?.length;
  $("#settings-runners").replaceChildren(...(runners || []).map((r) => {
    const linked = (p.runners || []).includes(r.id);
    return h("label", { class: "runner-pick" },
      h("input", { type: "checkbox", name: "runner", value: r.id, checked: linked, disabled: !linked && !r.can_manage }),
      h("span", { class: `runner-dot ${r.online ? "on" : ""}`, "aria-hidden": "true" }),
      h("span", { class: "runner-name" }, r.name),
      h("span", { class: "hint" }, `${r.mine ? "yours" : r.owner?.username || ""}${!linked && !r.can_manage ? " · only its owner or an admin can link it" : ""}`));
  }));
}
const pickedRunners = () => [...$("#settings-form").querySelectorAll('input[name="runner"]:checked')].map((x) => x.value);

function syncMergeHint() {
  const f = $("#settings-form");
  const p = state.project;
  const onRunners = !$("#settings-runners-field").hidden && pickedRunners().length > 0;
  const hint = $("#settings-runners-hint");
  hint.hidden = $("#settings-runners-field").hidden;
  hint.textContent = !onRunners
    ? "Not linked: tasks, git history, files and commands wait until you link a runner (this dashboard runs nothing itself)."
    : !p.remote_url ? "Runners clone the project's git remote: connect a GitHub repository first."
      : "Tasks, git, files and commands run only on these runners, each in its own clone. If all of them are offline, tasks wait in the queue.";
  const pr = f.merge_mode.value === "pr";
  const green = f.merge_green_prs.checked;
  // On runners, merging is pushing: there's no separate push to turn on.
  $("#auto-push-row").hidden = pr || onRunners;
  $("#merge-green-row").hidden = !pr;
  const who = onRunners ? "The runner" : "Dash";
  $("#merge-hint").textContent = !p.remote_url
    ? (pr ? "Connect a GitHub repository (Projects → Create GitHub repository) to open pull requests." : "")
    : pr ? `${who} pushes the task's branch and opens a pull request against ${baseBranch(p)}. ${green
      ? "Once GitHub's checks pass it merges it (squashed); failing checks and conflicts go back to Claude."
      : "Failing checks and conflicts go back to Claude; merge it on GitHub, and Dash marks the task merged."}`
    : onRunners ? `The runner squashes the task onto ${baseBranch(p)}, runs the tests and pushes it, without waiting for GitHub's checks. If someone pushed first, it merges again on top; conflicts and failing tests go back to Claude.`
      : f.auto_push.checked ? `Before each merge Dash pulls ${baseBranch(p)} from GitHub, so pushes don't get rejected. GitHub's checks run only after it's pushed.` : "";
}
$("#settings-form").addEventListener("change", syncMergeHint);

// The tag format is checked as it is typed: placeholders, a valid git tag,
// and whether the repository's release workflows run for its tags.
let tagFormatTimer = 0, tagFormatSeq = 0;
function checkTagFormat() {
  clearTimeout(tagFormatTimer);
  tagFormatTimer = setTimeout(attempt(async () => {
    const p = state.project, f = $("#settings-form");
    const format = f.tag_format.value.trim() || "X.Y.Z";
    const seq = ++tagFormatSeq;
    const res = await api("GET", `/api/projects/${p.id}/tag-format?format=${encodeURIComponent(format)}`);
    if (seq !== tagFormatSeq) return;
    const hint = $("#tag-format-hint");
    f.tag_format.setCustomValidity(res.valid ? "" : res.error);
    hint.className = `hint${!res.valid ? " err-text" : res.hint ? " warn" : ""}`;
    if (!res.valid) return hint.replaceChildren(res.error);
    hint.replaceChildren(`Tags look like ${res.example}. Tag a release from a commit in History.`);
    if (res.hint) {
      hint.append(" ", res.hint.message);
      if (res.hint.suggest) hint.append(" ", h("button", { type: "button", class: "link inline", onclick: () => { f.tag_format.value = res.hint.suggest; checkTagFormat(); } }, `Use ${res.hint.suggest}`));
    }
  }), 250);
}
$("#settings-form").tag_format.addEventListener("input", checkTagFormat);
$("#settings-form").use_color.addEventListener("change", (e) => { e.target.form.color.disabled = !e.target.checked; });
$("#settings-form").color.addEventListener("input", (e) => { e.target.form.use_color.checked = true; });
const settingsIcon = (icon) => {
  $("#settings-form").icon.value = icon;
  projectIcon({ ...state.project, icon }, $("#settings-icon"));
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
$("#settings-sheet").addEventListener("close", attempt(async () => {
  if ($("#settings-sheet").returnValue !== "save") return;
  const f = $("#settings-form");
  const old = state.project;
  const saved = await api("PATCH", `/api/projects/${old.id}`, {
    test_command: f.test_command.value.trim(),
    max_auto_fixes: Math.max(0, Number(f.max_auto_fixes.value) || 0),
    setup_command: f.setup_command.value.trim(),
    copy_files: f.copy_files.value.split("\n").map((x) => x.trim()).filter(Boolean),
    allowed_tools: f.allowed_tools.value.split("\n").map((x) => x.trim()).filter(Boolean),
    chat_permission_mode: f.chat_permission_mode.value,
    review_prompt: f.review_prompt.value.trim(),
    task_instructions: f.task_instructions.value.trim(),
    run_command: f.run_command.value.trim(),
    preview_kind: f.preview_kind.value,
    preview_command: f.preview_command.value.trim(),
    deploy_command: f.deploy_command.value.trim(),
    auto_push: f.auto_push.checked,
    auto_approve: f.auto_approve.checked,
    merge_mode: f.merge_mode.value,
    merge_green_prs: f.merge_green_prs.checked,
    investigate_ci: f.investigate_ci.checked,
    tag_format: f.tag_format.value.trim() || "X.Y.Z",
    max_run_minutes: Number(f.max_run_minutes.value) || 0,
    max_turns: Number(f.max_turns.value) || 0,
    color: f.use_color.checked ? f.color.value : "",
    icon: f.icon.value,
    ...($("#settings-runners-field").hidden ? {} : { runners: pickedRunners() }),
  });
  // Keep the client-only fields, and the list entry in step for its color and icon.
  state.project = { ...saved, machine: old.machine, prefix: old.prefix };
  state.projects = state.projects.map((x) => (x === old ? state.project : x));
  renderProjectHeader();
  renderProjectList();
  renderBoard(); // auto approve shows or hides Review
  toast("Settings saved");
}));

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
const fmtDate = (iso) => new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
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
      ? [ext(rel.url, rel.tag), rel.name && rel.name !== rel.tag ? ` “${rel.name}”` : "", isSet(rel.published_at) ? h("span", { class: "hint" }, ` · ${fmtDate(rel.published_at)}`) : null]
      : h("span", { class: "hint" }, "No GitHub release")),
    res.latest_tag && res.latest_tag !== rel?.tag ? row("Latest tag", h("code", {}, res.latest_tag), h("span", { class: "hint" }, " in this clone")) : null,
    g.default_branch ? row("Default branch", h("code", {}, g.default_branch)) : null,
    g.language ? row("Language", g.language) : null,
    g.parent ? row("Forked from", ext(`https://github.com/${g.parent}`, g.parent)) : null,
    row("Activity", `★ ${g.stars} · ${g.forks} forks · ${g.watchers} watching`),
    row("Open", ext(`${g.url}/issues`, `${g.open_issues} issues`), " · ", ext(`${g.url}/pulls`, `${g.open_prs} pull requests`)),
    isSet(g.pushed_at) ? row("Last push", agoText(g.pushed_at)) : null,
    isSet(g.created_at) ? row("Created", fmtDate(g.created_at)) : null,
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
    p.merge_mode === "pr" && !g.squash_merge && "Dash merges pull requests squashed, but squash merging is off for this repository: turn it on in GitHub's settings.",
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
