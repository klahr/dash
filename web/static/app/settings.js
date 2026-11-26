import { $, $$, dateText, docsLink, fill, h, ib } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, fail, toast } from "./dialogs.js";
import { checked, fieldError, saveField, saveQueue } from "./autosave.js";
import { uploadFile } from "./attachments.js";
import { state } from "./state.js";
import { baseBranch, projectKey } from "./events.js";
import { projectIcon, renderProjectHeader, renderProjectList } from "./projects.js";
import { renderBoard } from "./branches.js";
import { agoText } from "../time.js";
import { DEFAULT_REVIEW_PROMPT } from "./drawmode.js";
import { loadClaudeFiles } from "./claudefiles.js";
import { changed } from "./routes.js";
import { loadWebhooks } from "./webhooks.js";
import { loadBoard } from "./backend.js";
import { agentRunners, missingLabels, parseLabels, projectAgent, servesProject } from "../groups.js";
import { FEATURES, NOTES, SETUP, agentTitle, loadAgents } from "./agents.js";
import { docsLabels, labelPicker, matchList, setLabels } from "./labelpicker.js";

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
  f.auto_push.checked = !!p.auto_push;
  f.auto_approve.checked = !!p.auto_approve;
  f.close_no_changes.checked = !!p.close_no_changes;
  f.clear_merged_zone.checked = !!p.clear_merged_zone;
  clearMergedHint(p.clear_merged_zone);
  f.merge_mode.value = p.merge_mode || "local";
  f.merge_method.value = p.merge_method || "";
  setMergeBatch(f.merge_batch, p.merge_batch > 1 ? p.merge_batch : 1);
  f.investigate_ci.checked = p.investigate_ci !== false;
  f.tag_format.value = p.tag_format || "X.Y.Z";
  checkTagFormat();
  f.max_run_minutes.value = p.max_run_minutes || "";
  f.max_turns.value = p.max_turns || "";
  f.max_parallel.value = p.max_parallel || "";
  showColor(f, p.color || "");
  f.icon.value = p.icon || "";
  projectIcon(p, $("#settings-icon"));
  renderAgentPicker(p);
  renderRunnerPicker(p);
  renderEnvEditor(p);
  syncMergeHint();
  renderApprove();
  $("#webhook-secret").hidden = true;
  loadWebhooks().catch(fail);
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
// runs-on). The labels are picked as chips; below them, the org's runners
// show whether they would work on it, live as the labels change. A member may
// only pick labels that send work to their own runners; admins any (the hub
// says so when saving).
function renderRunnerPicker(p) {
  const hub = !p.prefix && Array.isArray(state.runners);
  $("#settings-runners-field").hidden = !hub && !p.runs_on?.length;
  shownRunsOn = labelKey(p.runs_on);
  setLabels($("#settings-runs-on"), p.runs_on || []);
  renderRunsOnPreview();
}

// renderAgentPicker offers the agents this Dash can run (GET /api/agents),
// the project's own first if this Dash doesn't know it.
function renderAgentPicker(p) {
  const sel = $("#settings-agent");
  const fill = () => {
    const names = (state.agents || []).map((a) => a.name);
    if (!names.includes(projectAgent(p))) names.unshift(projectAgent(p));
    sel.replaceChildren(...names.map((n) => h("option", { value: n }, agentTitle(n))));
    sel.value = projectAgent(p);
    sel.disabled = names.length < 2;
    renderAgentHint();
  };
  fill();
  if (!state.agents) loadAgents().then(() => { if (state.project === p) { fill(); markSettingSaved("agent"); } });
}

// renderAgentHint says what the chosen agent can't do, and, on a hub, warns
// when no online runner can run it, as the labels do: its tasks go only to
// runners where it is installed and logged in (its label).
function renderAgentHint() {
  const name = $("#settings-agent").value || projectAgent(state.project);
  const info = (state.agents || []).find((a) => a.name === name);
  const lacks = info ? Object.keys(FEATURES).filter((f) => !(info.features || []).includes(f)) : [];
  // warns are why its tasks won't run, on a line of their own; notes what
  // it can't do, below them, in the hint's own color.
  const warns = [], notes = [];
  if (lacks.length) notes.push(`${agentTitle(name)} can't ${lacks.map((f) => FEATURES[f].does).join(", or ")}.`);
  if (NOTES[name]) notes.push(NOTES[name]);
  const hint = $("#settings-agent-hint");
  // A runner's own page: what this computer found of the CLI.
  const st = info?.status;
  if (st?.checked_at && !(st.installed && st.logged_in)) {
    const setup = SETUP[name];
    warns.push(!st.installed
      ? `${agentTitle(name)} isn't installed on this computer: ${setup?.install || "install its CLI"}, then sign in with \`${setup?.login || name}\`.`
      : `${agentTitle(name)} isn't signed in on this computer: run \`${setup?.login || name}\`.`);
  }
  if (Array.isArray(state.runners) && !state.project.prefix) {
    const { ready, why } = agentRunners(state.runners, name);
    if (!ready.length) {
      warns.push(`No online runner has ${agentTitle(name)} installed and logged in: this project's tasks wait until one does.${why.length ? ` ${why.join("; ")}.` : ""}`);
    }
  }
  hint.replaceChildren(...warns.map((w) => h("span", { class: "agent-warn" }, w)),
    ...(notes.length ? [h("span", { class: "agent-note" }, notes.join(" "))] : []));
  hint.hidden = !warns.length && !notes.length;
  hint.classList.toggle("warn", warns.length > 0);
}

// deployRunners are the runners a project on runsOn deploys on with deploy.
export const deployRunners = (runsOn, deploy) =>
  (state.runners || []).filter((r) => servesProject(r, { runs_on: runsOn, agent: state.project?.agent })
    && (!deploy.length || servesProject(r, { runs_on: deploy, agent: state.project?.agent })));

// ------------------------------------------------------------------ environments
// Project settings → Deploy: the project's environments, each with its name,
// deploy and build commands, URL and (on a hub) the runner labels its
// deploys run on, with the runners that would run them and whether any of
// them has its secrets file (~/.dash/env/<name>.env; the hub only learns
// whether it is there). The whole list saves when one of its fields
// changes, once every environment has a name and a deploy command.
let envDraft = [];    // the environments as edited
let shownEnvs = "[]"; // as last loaded or saved (JSON of cleanEnvs)
const envOnHub = () => !$("#settings-runners-field").hidden;
const script = (s) => (s || "").replace(/\r\n?/g, "\n").trim();
const num = (v) => Math.max(0, parseInt(v, 10) || 0);
// An environment's roles, as the hub fills them in when it has none
// (model.Environment.WithDefaults): production is admins', the rest members'.
const isProd = (name) => name === "production" || name === "prod";
const roleDefaults = (e) => {
  const name = (e.name || "").trim().toLowerCase();
  return {
    deploy_role: e.deploy_role || (isProd(name) ? "admin" : "member"),
    approve_role: e.approve_role || (isProd(name) ? "admin" : "none"),
    settings_role: e.settings_role || "admin",
  };
};
const cleanEnvs = (list) => list.map((e) => ({
  name: (e.name || "").trim().toLowerCase(), deploy_command: script(e.deploy_command), build_command: script(e.build_command),
  runs_on: (e.runs_on || []).filter((l) => l.toLowerCase() !== "any"), url: (e.url || "").trim(),
  health_path: (e.health_path || "").trim(), health_status: num(e.health_status), health_timeout: num(e.health_timeout),
  verify_command: script(e.verify_command), soak: num(e.soak), auto_rollback: !!e.auto_rollback,
  ...roleDefaults(e), require_tests: !!e.require_tests, require_live_on: (e.require_live_on || "").trim().toLowerCase(),
  freezes: (e.freezes || []).map((f) => ({ from: (f.from || "").trim(), to: (f.to || "").trim(), note: (f.note || "").trim() })).filter((f) => f.from || f.to || f.note),
  time_zone: (e.time_zone || "").trim(), trigger: e.trigger || "manual", release_tags_only: !!e.release_tags_only,
  wait_for_build: !!e.wait_for_build,
}));
const TRIGGER_OPTS = [["manual", "No, only when someone deploys"], ["merge", "On each merge to the default branch"], ["release", "On each release tag"]];
const ROLE_OPTS = [["member", "Members"], ["admin", "Admins"], ["owner", "Owners"]];
const browserZone = () => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch { return ""; } };

// envDrafts keeps, per project, environments left unfinished when the sheet
// closed, to open with again.
const envDrafts = new Map();
const ENV_INCOMPLETE = "Give each environment a name and a deploy command to save it.";

function renderEnvEditor(p) {
  const kept = envDrafts.get(projectKey(p));
  envDrafts.delete(projectKey(p));
  envDraft = cleanEnvs(p.environments || []);
  shownEnvs = JSON.stringify(envDraft);
  if (kept) envDraft = kept;
  fieldError($("#settings-envs"), kept ? ENV_INCOMPLETE : "");
  drawEnvs();
  $("#settings-env-history").replaceChildren();
  loadEnvHistory().catch(() => {});
}

// ------------------------------------------------------------------ settings history
// History, under the environments: each change to one, who made it, when,
// and each setting before and after.
const ENV_FIELD = {
  deploy_command: "Deploy command", build_command: "Build command", runs_on: "Runs on", url: "URL", health_path: "Health check path",
  health_status: "Expected status", health_timeout: "Health check timeout", verify_command: "Verify command", soak: "Soak time",
  auto_rollback: "Roll back on its own", deploy_role: "Who may deploy", approve_role: "Needs approval from", settings_role: "Who may change settings",
  require_tests: "Tests must pass", require_live_on: "Live on first", freezes: "Freeze windows", time_zone: "Time zone",
  trigger: "Deploys on its own", release_tags_only: "Deploys: release tags only",
  wait_for_build: "Wait for the release build before deploying",
};
const ENV_ACTION = { added: "added", changed: "changed", removed: "removed" };

async function loadEnvHistory() {
  const p = state.project;
  if (!p) return;
  const list = await api("GET", `/api/projects/${p.id}/environments/history`, null, p.prefix || "");
  if (state.project !== p) return;
  const el = $("#settings-env-history");
  $("#settings-env-history-head").hidden = !list?.length;
  const short = (v) => (v.length > 120 ? `${v.slice(0, 120)}…` : v);
  el.replaceChildren(...(list || []).map((c) => h("li", { class: "env-change" },
    h("div", { class: "env-change-head" },
      h("strong", {}, c.by_name || "someone"), ` ${ENV_ACTION[c.action] || c.action} `, h("code", {}, c.environment), " ",
      h("time", { class: "hint", datetime: c.at, title: dateText(c.at) }, agoText(c.at))),
    c.diff?.length ? h("ul", { class: "env-diff" }, ...c.diff.map((d) => h("li", {},
      h("span", { class: "env-diff-field" }, ENV_FIELD[d.field] || d.field), " ",
      d.from ? h("del", {}, short(d.from)) : null, d.from && d.to ? " → " : null, d.to ? h("ins", {}, short(d.to)) : null,
      !d.from && !d.to ? h("span", { class: "hint" }, "cleared") : null))) : null)));
}

function drawEnvs() {
  const list = $("#settings-envs");
  list.replaceChildren(...(envDraft.length ? envDraft.map(envEditor) : [h("li", { class: "hint" }, "No environments: the project isn't deployed from Dash.")]));
  renderEnvHints();
}

function envEditor(e, i) {
  const field = (label, em, control) => h("label", { class: "field" }, h("span", {}, label, " ", h("em", {}, em)), control);
  const text = (k, props) => h(props.rows ? "textarea" : "input", { ...props, value: e[k] || "", spellcheck: false, autocomplete: "off", autocapitalize: "off",
    oninput: (ev) => { e[k] = ev.target.value; if (k === "name") renderEnvHints(); } });
  const runsOn = envOnHub() ? labelPicker({ value: (e.runs_on || []).join(", "), label: `${e.name || "Environment"} runs on`, placeholder: "e.g. prod",
    onChange: (ls) => { e.runs_on = ls; renderEnvHints(); saveEnvs(); } }) : null;
  return h("li", { class: "env-edit" },
    h("div", { class: "env-edit-head" },
      field("Name", "a lowercase word", text("name", { placeholder: "production", maxlength: 32, pattern: "[a-z0-9][a-z0-9_\\-]*" })),
      ib({ type: "button", class: "btn small danger", title: `Remove ${e.name || "this environment"}`, onclick: () => removeEnv(i) }, "trash", "Remove")),
    field("Deploy command", ["a bash script · ", docsLink("deploys.md#the-script-contract", "Variables")],
      text("deploy_command", { rows: 4, class: "script-field", placeholder: "set -e\n# $DASH_ENV, $DASH_COMMIT, $DASH_PREV_COMMIT\n./deploy.sh \"$DASH_ENV\" \"$DASH_COMMIT\"" })),
    field("Build command", "optional; runs first, in the same checkout", text("build_command", { rows: 2, class: "script-field", placeholder: "npm ci && npm run build" })),
    field("URL", "optional; checked once a deploy is done", text("url", { type: "url", placeholder: "https://example.com" })),
    runsOn ? h("div", { class: "field" }, h("span", {}, "Runs on ", h("em", {}, "empty: any runner the project runs on")), runsOn.el) : null,
    h("fieldset", { class: "env-checks" },
      h("legend", {}, "Checks ", h("em", {}, "decide whether a deploy whose command exited 0 is live")),
      h("div", { class: "env-check-row" },
        field("Health check path", "on the URL", text("health_path", { placeholder: "/healthz" })),
        field("Expected status", "empty: below 500", text("health_status", { type: "number", min: 100, max: 599, placeholder: "200", inputmode: "numeric" })),
        field("Timeout", "seconds", text("health_timeout", { type: "number", min: 0, max: 600, placeholder: "60", inputmode: "numeric" }))),
      field("Verify command", "optional smoke tests; gets the URL as $DASH_URL", text("verify_command", { rows: 2, class: "script-field", placeholder: "curl -fsS \"$DASH_URL/api/status\"" })),
      field("Soak time", "seconds to watch it before it is live; empty: none", text("soak", { type: "number", min: 0, max: 3600, placeholder: "0", inputmode: "numeric" })),
      h("label", { class: "toggle" },
        h("input", { type: "checkbox", checked: !!e.auto_rollback, onchange: (ev) => { e.auto_rollback = ev.target.checked; } }),
        h("span", {}, "Roll back on its own when the checks fail ", h("em", {}, "to the last good deploy")))),
    envPolicy(e, field),
    h("p", { class: "hint env-hint" }));
}

// envPolicy edits who may deploy, approve and change e, what a deploy
// needs first and e's freeze windows (the hub enforces them).
function envPolicy(e, field) {
  Object.assign(e, roleDefaults(e));
  // Merges deploy nothing while only release tags may: say so where both are set.
  const mergeNote = h("p", { class: "hint warn env-merge-note" });
  // Waiting for the release build is about release-triggered deploys only.
  const waitBuild = h("label", { class: "toggle env-wait-build" },
    h("input", { type: "checkbox", checked: !!e.wait_for_build, onchange: (ev) => { e.wait_for_build = ev.target.checked; } }),
    h("span", {}, "Wait for the release build before deploying ",
      h("em", {}, "a release tag deploys once its GitHub Actions runs pass; if they fail, it doesn't deploy and you get a notice")));
  const syncMergeNote = () => {
    waitBuild.hidden = e.trigger !== "release";
    mergeNote.hidden = !(e.trigger === "merge" && e.release_tags_only);
    mergeNote.textContent = "Merges deploy nothing while “Deploys: release tags only” is on: deploy release tags, or set it to deploy on each release tag.";
  };
  syncMergeNote();
  const select = (k, opts) => h("select", { onchange: (ev) => { e[k] = ev.target.value; syncMergeNote(); } },
    ...opts.map(([v, label]) => h("option", { value: v, selected: e[k] === v }, label)));
  const liveOn = h("select", { class: "env-live-on", onchange: (ev) => { e.require_live_on = ev.target.value; } });
  fillLiveOn(liveOn, e);
  const freezes = h("ul", { class: "env-freezes" });
  const drawFreezes = () => freezes.replaceChildren(...(e.freezes || []).map((f, i) => h("li", { class: "env-freeze" },
    h("input", { value: f.from || "", placeholder: "fri 15:00", "aria-label": "Frozen from", spellcheck: false, oninput: (ev) => { f.from = ev.target.value; } }),
    h("span", { "aria-hidden": "true" }, "→"),
    h("input", { value: f.to || "", placeholder: "mon 07:00", "aria-label": "Frozen until", spellcheck: false, oninput: (ev) => { f.to = ev.target.value; } }),
    h("input", { value: f.note || "", placeholder: "why, e.g. weekend", "aria-label": "Why", class: "env-freeze-note", oninput: (ev) => { f.note = ev.target.value; } }),
    ib({ type: "button", class: "btn small ghost", title: "Remove this freeze window", "aria-label": "Remove this freeze window", onclick: () => { e.freezes.splice(i, 1); drawFreezes(); saveEnvs(); } }, "x", ""))));
  drawFreezes();
  const zone = h("input", { value: e.time_zone || "", placeholder: browserZone() || "UTC", spellcheck: false, autocomplete: "off", oninput: (ev) => { e.time_zone = ev.target.value; } });
  return h("fieldset", { class: "env-checks env-policy" },
    h("legend", {}, "Who and when ", h("em", {}, "the hub enforces these")),
    h("div", { class: "env-check-row env-role-row" },
      field("Who may deploy", "and roll back", select("deploy_role", ROLE_OPTS)),
      field("Needs approval from", "not their own deploy", select("approve_role", [["none", "No one"], ...ROLE_OPTS])),
      field("Who may change settings", "of this environment", select("settings_role", ROLE_OPTS))),
    field("Deploys on its own", "through the approvals, requirements and freezes below, like any deploy", select("trigger", TRIGGER_OPTS)),
    waitBuild,
    h("label", { class: "toggle" },
      h("input", { type: "checkbox", checked: !!e.release_tags_only, onchange: (ev) => { e.release_tags_only = ev.target.checked; syncMergeNote(); } }),
      h("span", {}, "Deploys: release tags only ", h("em", {}, "a commit without a release tag is refused, whoever asks (a rollback too); a new release tag always deploys"))),
    mergeNote,
    h("label", { class: "toggle" },
      h("input", { type: "checkbox", checked: !!e.require_tests, onchange: (ev) => { e.require_tests = ev.target.checked; } }),
      h("span", {}, "Tests must pass on the commit ", h("em", {}, "Dash runs the test command first unless they passed already"))),
    field("Live on first", "a deploy's commit must have been live there", liveOn),
    h("div", { class: "field" }, h("span", {}, "Freeze windows ", h("em", {}, "nothing deploys then; rollbacks still do. Weekly (fri 15:00 → mon 07:00) or dates (2026-12-23 12:00)")),
      freezes,
      ib({ type: "button", class: "btn small", onclick: () => {
        (e.freezes ||= []).push({ from: "fri 15:00", to: "mon 07:00", note: "" });
        if (!e.time_zone) { e.time_zone = browserZone(); zone.value = e.time_zone; }
        drawFreezes();
        saveEnvs();
      } }, "plus", "Add freeze window")),
    field("Time zone", "freeze windows are read in it; empty: UTC", zone));
}

// fillLiveOn lists the other environments in sel, with e's choice picked.
function fillLiveOn(sel, e) {
  const others = envDraft.map((x) => (x.name || "").trim().toLowerCase()).filter((n) => n && n !== (e.name || "").trim().toLowerCase());
  sel.replaceChildren(h("option", { value: "" }, "No requirement"),
    ...others.map((n) => h("option", { value: n, selected: e.require_live_on === n }, n)));
  if (e.require_live_on && !others.includes(e.require_live_on)) sel.append(h("option", { value: e.require_live_on, selected: true }, e.require_live_on));
}

// renderEnvHints says, under each environment, where its deploys run and
// where its (optional) secrets file goes.
function renderEnvHints() {
  $$("#settings-envs .env-edit").forEach((li, i) => {
    const e = envDraft[i], hint = $(".env-hint", li), name = (e?.name || "").trim().toLowerCase() || "<name>";
    if (!e || !hint) return;
    const liveOn = $(".env-live-on", li);
    if (liveOn) fillLiveOn(liveOn, e);
    if (!envOnHub()) {
      fill(hint, "Secrets: this computer's ", h("code", {}, `~/.dash/env/${name}.env`), ", if there.");
      return;
    }
    const runners = deployRunners(typedRunsOn(), e.runs_on || []);
    matchList(hint, runners, { lead: "Deploys on", none: "No runner the project runs on has these labels: deploys fail." });
    if (runners.length) hint.append(" · Secrets: each runner's ", h("code", {}, `~/.dash/env/${name}.env`), ", if there.");
  });
}

function addEnv() {
  const names = envDraft.map((e) => e.name);
  const name = ["production", "staging", "preview"].find((n) => !names.includes(n)) || "";
  envDraft.push({ name, deploy_command: "", build_command: "", runs_on: [], url: "" });
  drawEnvs();
  $$("#settings-envs .env-edit").at(-1)?.querySelector("textarea")?.focus();
}

async function removeEnv(i) {
  const e = envDraft[i];
  if (e.deploy_command && !await ask({ title: `Remove ${e.name || "this environment"}?`, body: "Its deploys stay in the list, but nothing deploys there from Dash any more.", ok: "Remove", danger: true })) return;
  envDraft.splice(i, 1);
  drawEnvs();
  saveEnvs();
}

// saveEnvs saves the environments as they are now, to the project they are of.
function saveEnvs() {
  const p = state.project, el = $("#settings-envs"), draft = envDraft, shownThen = shownEnvs;
  const v = cleanEnvs(draft), json = JSON.stringify(v), closing = !$("#settings-sheet").open;
  return queueSave(async () => {
    const here = isOpenProject(p);
    if (json === (here ? shownEnvs : shownThen)) { if (here) fieldError(el, ""); return; }
    if (v.some((e) => !e.name || !e.deploy_command)) {
      if (!closing) { fieldError(el, ENV_INCOMPLETE); return; }
      // Closed unfinished: kept for the next open, and said so now.
      envDrafts.set(projectKey(p), draft);
      toast(ENV_INCOMPLETE, { tone: "error", head: `${p.name}: environments not saved`, action: { label: "Finish", run: () => {
        if (!isOpenProject(p) || $("#settings-sheet").open) return;
        openSettings();
        $('#settings-sheet .rail-item[data-rail="deploy"]')?.click();
      } } });
      return;
    }
    await saveField(el, async () => {
      const saved = await api("PATCH", `/api/projects/${p.id}`, { environments: v }, p.prefix || "");
      if (!savedProject(p, saved)) return;
      shownEnvs = JSON.stringify(cleanEnvs(saved.environments || []));
      renderProjectHeader();
      renderApprove();
      loadEnvHistory().catch(() => {});
    });
  });
}
$("#settings-env-add").addEventListener("click", addEnv);
$("#settings-envs").addEventListener("change", (e) => { if (e.target.matches("input:not([type=hidden]), textarea, select")) saveEnvs(); });
// Enter in one of its one-line fields saves the list (the form's own handler keeps it from submitting).
$("#settings-envs").addEventListener("keydown", (e) => { if (e.key === "Enter" && e.target.matches("input:not(.label-picker-input)")) saveEnvs(); });
$("#settings-sheet").addEventListener("close", () => { if (JSON.stringify(cleanEnvs(envDraft)) !== shownEnvs) saveEnvs(); });

// renderRunsOnPreview lists the org's runners as the typed labels would
// have it: those that would work on the project first.
function renderRunsOnPreview() {
  const want = { runs_on: typedRunsOn(), agent: $("#settings-agent").value || projectAgent(state.project) };
  const rs = [...(state.runners || [])].map((r) => ({ r, ok: servesProject(r, want) })).sort((a, b) => b.ok - a.ok);
  $("#settings-runners").replaceChildren(...rs.map(({ r, ok }) => {
    const missing = missingLabels(r, want);
    return h("div", { class: `runner-pick ${ok ? "on" : ""}` },
      h("span", { class: `runner-dot ${r.online ? "on" : ""}`, "aria-hidden": "true" }),
      h("span", { class: "runner-name" }, r.name),
      // The dot's colour alone doesn't say it: the hint names the state in words.
      h("span", { class: "hint" }, `${r.online ? "online" : "offline"} · `
        + (ok ? `works on it · ${r.mine ? "yours" : r.owner?.username || ""}`
          : want.runs_on.length ? `lacks ${missing.join(", ")}` : "no labels: runs nothing")));
  }));
  renderEnvHints();
  renderAgentHint();
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
  const n = (state.runners || []).filter((r) => servesProject(r, { runs_on: typedRunsOn(), agent: $("#settings-agent").value })).length;
  hint.textContent = !onRunners
    ? "No labels: its work waits until it has some a runner has."
    : !p.remote_url ? "Runners clone the project's git remote: connect a GitHub repository first."
      : !n ? "No runner has all these labels: its work waits until one does."
        : "Its work runs on these runners, each in its own clone.";
  const pr = f.merge_mode.value === "pr";
  // On runners, merging is pushing: there's no separate push to turn on.
  $("#auto-push-row").hidden = pr || onRunners;
  $("#merge-method-row").hidden = !pr;
  // Only direct merges here batch: not pull requests, nor merges on runners.
  $("#merge-batch-row").hidden = pr || onRunners;
  $("#auto-approve-hint").textContent = pr
    ? "Pull requests whose checks pass skip Dash's review; you still merge them."
    : "Tasks whose tests pass skip Review and go straight to the merge queue.";
  const merge = $("#merge-hint");
  fill(merge, !p.remote_url
    ? (pr ? "Connect a GitHub repository to open pull requests." : "")
    : pr ? ["Dash never merges a pull request on its own. ", docsLink("github.md#pull-requests")]
      : onRunners ? `The runner pushes to ${baseBranch(p)} without waiting for GitHub's checks.`
        : f.auto_push.checked ? `GitHub's checks run only after the push.` : "");
  merge.hidden = !merge.textContent;
}

// setMergeBatch picks n in the batch size select, adding a choice for a
// size it doesn't list (saved before, or set through the API).
function setMergeBatch(sel, n) {
  if (![...sel.options].some((o) => o.value === String(n))) {
    sel.append(h("option", { value: String(n) }, `Up to ${n} together`));
    [...sel.options].sort((a, b) => a.value - b.value).forEach((o) => sel.append(o));
  }
  sel.value = String(n);
}
$("#settings-form").addEventListener("change", syncMergeHint);
$("#settings-runs-on-help").append(" ", docsLabels());
labelPicker({ input: $("#settings-runs-on"), any: true, label: "Runs on", onChange: () => { renderRunsOnPreview(); syncMergeHint(); } });

// The tag format is checked as it is typed: placeholders, a valid git tag,
// and whether the repository's release workflows run for its tags.
let tagFormatTimer = 0, tagFormatSeq = 0;
const tagFormatCheck = (format, p = state.project) => api("GET", `/api/projects/${p.id}/tag-format?format=${encodeURIComponent(format)}`, null, p.prefix || "");
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
    hint.replaceChildren(`Tags look like ${res.example}.`);
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
  auto_push: { read: (f) => f.auto_push.checked },
  auto_approve: { read: (f) => f.auto_approve.checked },
  close_no_changes: { read: (f) => f.close_no_changes.checked },
  // On: the zone it is on in already, else this browser's; off: "".
  clear_merged_zone: { read: (f) => (f.clear_merged_zone.checked ? state.project.clear_merged_zone || browserZone() || "UTC" : "") },
  merge_mode: { read: (f) => f.merge_mode.value },
  merge_method: { read: (f) => f.merge_method.value },
  merge_batch: { read: (f) => Math.min(10, Math.max(1, Math.round(Number(f.merge_batch.value) || 1))) },
  investigate_ci: { read: (f) => f.investigate_ci.checked },
  tag_format: { read: (f) => f.tag_format.value.trim() || "X.Y.Z", check: async (v, p) => {
    const res = await tagFormatCheck(v, p);
    if (!res.valid) throw new Error(res.error);
  } },
  max_run_minutes: { read: (f) => Number(f.max_run_minutes.value) || 0 },
  max_turns: { read: (f) => Number(f.max_turns.value) || 0 },
  max_parallel: { read: (f) => Number(f.max_parallel.value) || 0 },
  color: { read: (f) => (f.color_pick.value === "custom" ? f.color.value : f.color_pick.value), el: () => $("#settings-color") },
  icon: { read: (f) => f.icon.value, el: () => $("#settings-icon-pick") },
  runs_on: { read: () => typedRunsOn(), skip: () => $("#settings-runners-field").hidden },
  agent: { read: (f) => f.agent.value, skip: () => !$("#settings-agent").options.length },
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
const isOpenProject = (p) => projectKey(state.project) === projectKey(p);
// savedProject takes saved, the answer to a PATCH of p, as p's state (with
// the client-only fields kept), and as the open project's only while p still
// is it. Returns whether it is.
function savedProject(p, saved) {
  const next = { ...saved, machine: p.machine, prefix: p.prefix };
  state.projects = state.projects.map((x) => (projectKey(x) === projectKey(p) ? next : x));
  if (!isOpenProject(p)) return false;
  state.project = next;
  return true;
}

// saveSetting saves the field key as it is now, to the project it is of,
// even when the save waits in the queue until another project is open.
function saveSetting(key) {
  const f = $("#settings-form"), field = FIELDS[key], el = fieldEl(f, key), p = state.project;
  if (field.skip?.()) return Promise.resolve();
  const v = field.read(f), json = JSON.stringify(v), shownThen = shown[key];
  let invalid = null;
  try { checked(el); } catch (e) { invalid = e; }
  return queueSave(async () => {
    const here = isOpenProject(p);
    if (json === (here ? shown[key] : shownThen)) { if (here) fieldError(el, ""); return; }
    await saveField(el, async () => {
      await field.check?.(v, p);
      if (invalid) throw invalid;
      const saved = await api("PATCH", `/api/projects/${p.id}`, { [key]: v }, p.prefix || "");
      const still = savedProject(p, saved);
      renderProjectList(); // the list entry in step for its name, color and icon
      if (!still) return;
      shown[key] = json;
      if (key === "runs_on") shownRunsOn = labelKey(saved.runs_on);
      renderProjectHeader();
      renderApprove();
      if (key === "auto_approve") renderBoard(); // auto approve shows or hides Review
      if (key === "clear_merged_zone") clearMergedHint(saved.clear_merged_zone);
      if (key === "agent") { renderRunsOnPreview(); syncMergeHint(); }
    });
  });
}
// clearMergedHint names the time zone whose midnight clears merged tasks.
function clearMergedHint(zone) {
  $("#settings-clear-merged-hint").textContent = `After midnight${zone ? ` (${zone})` : ""}, merged tasks from earlier days are deleted. Their commits stay.`;
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
const COMMAND_FIELDS = { run: "run_command", test: "test_command", deploy: "environments", setup: "setup_command", preview: "preview_command" };
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
  // Saved again unchanged: the deploy command is the environments.
  const body = Object.fromEntries(unapproved().map(([k]) => [COMMAND_FIELDS[k], p[COMMAND_FIELDS[k]] || (k === "deploy" ? [] : "")]).filter(([k]) => k));
  const saved = await api("PATCH", `/api/projects/${p.id}`, body, p.prefix || "");
  if (savedProject(p, saved)) renderApprove();
})));

// ------------------------------------------------------------------ repository
// Project settings → General, under Repository: what GitHub says about
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
