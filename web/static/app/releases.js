import { $, h, setLabel } from "./dom.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { openCI, runState } from "./actions.js";
import { githubURL, refreshGraph } from "./history.js";

// ------------------------------------------------------------------ releases
// "Tag release…" on a pushed commit: pick the next version in the project's
// tag format, edit the notes, and Dash tags the commit and pushes the tag;
// the builds sheet then follows the release workflow.
function tagFormat(fmt) {
  fmt = fmt || "X.Y.Z";
  const [x, y, z] = ["X", "Y", "Z"].map((c) => fmt.indexOf(c));
  const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const n = "(0|[1-9]\\d*)";
  const parts = [fmt.slice(0, x), fmt.slice(x + 1, y), fmt.slice(y + 1, z), fmt.slice(z + 1)];
  const re = new RegExp(`^${esc(parts[0])}${n}${esc(parts[1])}${n}${esc(parts[2])}${n}${esc(parts[3])}$`);
  return {
    render: (v) => parts[0] + v[0] + parts[1] + v[1] + parts[2] + v[2] + parts[3],
    parse: (t) => { const m = re.exec(t); return m ? [+m[1], +m[2], +m[3]] : null; },
  };
}

let release = null; // { commit, info, format, seq } while the sheet is open
let releaseSeq = 0;

export async function openRelease(c) {
  const p = state.project;
  const seq = ++releaseSeq;
  release = { commit: c, info: null, seq, project: p };
  const f = $("#release-form");
  const gh = githubURL(p.remote_url);
  $("#release-commit").replaceChildren(
    gh ? h("a", { href: `${gh}/commit/${c.sha}`, target: "_blank", rel: "noopener" }, h("code", {}, c.short)) : h("code", {}, c.short),
    ` ${c.subject}`);
  $("#release-loading").hidden = false;
  $("#release-loading").textContent = "Fetching tags from origin…";
  $("#release-body").hidden = true;
  $("#release-ok").disabled = true;
  f.custom.value = "";
  $("#release-sheet").returnValue = "";
  $("#release-sheet").showModal();
  let info;
  try {
    info = await api("GET", `/api/projects/${p.id}/release?sha=${encodeURIComponent(c.sha)}`);
  } catch (e) {
    if (release?.seq === seq) $("#release-loading").replaceChildren(h("span", { class: "err-text" }, e.message));
    return;
  }
  if (release?.seq !== seq) return;
  release.info = info;
  release.format = tagFormat(info.format);
  renderRelease();
}

function renderRelease() {
  const { info } = release;
  const f = $("#release-form");
  const s = info.suggestions;
  $("#release-loading").hidden = true;
  $("#release-body").hidden = false;
  $("#release-latest").textContent = info.previous ? `latest: ${info.previous}` : `no ${info.format} tag yet`;
  const choice = (label, tag, note) => h("label", { class: "radio" },
    h("input", { type: "radio", name: "version", value: tag }),
    h("span", {}, label, " ", h("code", {}, tag), h("em", {}, note)));
  $("#release-choices").replaceChildren(...(s.first
    ? [choice("First release", s.first, "no release tag in this format yet")]
    : [choice("Patch", s.patch, "fixes only"), choice("Minor", s.minor, "new features, nothing breaks"), choice("Major", s.major, "breaking changes")]));
  f.querySelector('input[name="version"]').checked = true;
  f.notes.value = info.notes || "";
  f.notes.placeholder = `Release ${s.first || s.patch}`;

  const warn = (text, extra) => h("li", { class: "hint warn" }, text, extra || "");
  const ci = {
    failing: "The build failed on this commit.",
    running: "Builds are still running on this commit.",
    none: "No builds ran on this commit.",
    unknown: githubURL(state.project.remote_url) ? "Couldn't tell whether this commit's build passed." : "",
  }[info.ci.state];
  const failed = info.ci.runs.filter((r) => runState(r)[0] === "failing");
  const items = [
    !info.can_tag && info.cant_tag ? h("li", { class: "hint err-text" }, info.cant_tag) : null,
    ci ? warn(ci, failed.length ? [" ", ...failed.slice(0, 3).map((r) => h("a", { href: r.url, target: "_blank", rel: "noopener" }, r.workflow))] : "") : null,
    info.running ? warn(`${info.running} ${info.running === 1 ? "task is" : "tasks are"} still running or merging; ${info.running === 1 ? "its" : "their"} work won't be in this release.`) : null,
    info.fetch_error ? warn(`Couldn't fetch tags from origin (${info.fetch_error}); the suggestions may be out of date.`) : null,
    info.hint ? warn(info.hint.message, info.hint.suggest ? ` Set the tag format to ${info.hint.suggest} in the project settings.` : "") : null,
  ].filter(Boolean);
  $("#release-warnings").replaceChildren(...items);
  $("#release-warnings").hidden = !items.length;
  syncRelease();
}

// releaseTag is the tag the form would create, or { error }.
function releaseTag() {
  const f = $("#release-form");
  const picked = f.querySelector('input[name="version"]:checked')?.value;
  if (picked !== "custom") return { tag: picked };
  const raw = f.custom.value.trim();
  if (!raw) return { error: "Type a version, like 1.4.0." };
  const fmt = release.format;
  let v = fmt.parse(raw);
  let tag = raw;
  if (!v) {
    const m = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(raw);
    if (!m) return { error: `Use a version like 1.4.0 or a tag like ${fmt.render([1, 4, 0])} (format ${release.info.format}).` };
    v = [+m[1], +m[2], +m[3]];
    tag = fmt.render(v);
  }
  if (release.info.tags.includes(tag)) return { error: `${tag} already exists.` };
  const latest = release.info.previous && fmt.parse(release.info.previous);
  const lower = latest && (v[0] - latest[0] || v[1] - latest[1] || v[2] - latest[2]) < 0;
  return { tag, note: `Tags ${tag}${lower ? ` — lower than the latest release, ${release.info.previous}` : ""}.` };
}

function syncRelease() {
  if (!release?.info) return;
  const r = releaseTag();
  const custom = $("#release-form").querySelector('input[name="version"][value="custom"]').checked;
  const hint = $("#release-custom-hint");
  hint.hidden = !custom;
  hint.className = `hint${r.error ? " err-text" : ""}`;
  hint.textContent = r.error || r.note || "";
  $("#release-ok").disabled = !release.info.can_tag || !!r.error;
  setLabel($("#release-ok"), "tag", r.tag ? `Tag ${r.tag} and push` : "Tag and push");
}

$("#release-form").addEventListener("input", syncRelease);
$("#release-form").addEventListener("change", syncRelease);
$("#release-form").custom.addEventListener("focus", (e) => {
  e.target.form.querySelector('input[name="version"][value="custom"]').checked = true;
  syncRelease();
});
$("#release-form").addEventListener("submit", attempt(async (e) => {
  if (e.submitter?.value !== "ok") return;
  e.preventDefault(); // stays open until the tag is pushed (or it fails)
  const r = releaseTag();
  if (r.error || !release?.info) return;
  const { commit, project: p } = release;
  const ok = $("#release-ok");
  ok.disabled = true;
  setLabel(ok, "tag", `Pushing ${r.tag}…`);
  try {
    const out = await api("POST", `/api/projects/${p.id}/tags`, { sha: commit.sha, version: r.tag, notes: $("#release-form").notes.value.trim() });
    release = null;
    $("#release-sheet").close();
    toast(out.build ? "The release workflow is starting; Builds shows how it goes." : `Pushed to origin.`, {
      tone: "ok", icon: "tag", head: `Tagged ${out.tag}`,
      action: out.build ? { label: "Builds", run: openCI } : out.url ? { label: "View", run: () => window.open(out.url, "_blank", "noopener") } : null,
    });
    refreshGraph();
  } finally {
    syncRelease();
  }
}));
$("#release-sheet").addEventListener("close", () => { release = null; });
