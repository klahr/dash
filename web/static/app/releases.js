import { $, h, setLabel } from "./dom.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { openCI } from "./actions.js";
import { githubURL, refreshGraph } from "./history.js";
import { releaseFields, releaseWarnings } from "./releaseform.js";

// ------------------------------------------------------------------ releases
// "Tag release…" on a pushed commit: pick the next version in the project's
// tag format, edit the notes, and Dash tags the commit and pushes the tag;
// the builds sheet then follows the release workflow. The version, notes and
// warnings are releaseform.js's, as in Deploy's picker.
let release = null; // { commit, info, fields, seq } while the sheet is open
let releaseSeq = 0;

// c is the commit: { sha, short, subject }; sha "" tags the tip of origin's
// default branch (as the server resolves it).
export async function openRelease(c) {
  const p = state.project;
  const seq = ++releaseSeq;
  release = { commit: c, info: null, seq, project: p };
  const gh = githubURL(p.remote_url);
  $("#release-commit").replaceChildren(
    gh ? h("a", { href: `${gh}/commit/${c.sha}`, target: "_blank", rel: "noopener" }, h("code", {}, c.short)) : h("code", {}, c.short),
    ` ${c.subject}`);
  $("#release-loading").hidden = false;
  $("#release-loading").textContent = "Fetching tags from origin…";
  $("#release-body").hidden = true;
  $("#release-ok").disabled = true;
  $("#release-fields").replaceChildren();
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
  if (!c.sha) {
    // Asked for the branch's tip (Deploy's picker, with no tag yet): tag the commit it is now.
    Object.assign(c, { sha: info.sha, short: info.sha.slice(0, 7) });
    $("#release-commit").replaceChildren(gh ? h("a", { href: `${gh}/commit/${c.sha}`, target: "_blank", rel: "noopener" }, h("code", {}, c.short)) : h("code", {}, c.short), ` ${c.subject}`);
  }
  release.info = info;
  renderRelease();
}

function renderRelease() {
  const { info } = release;
  $("#release-loading").hidden = true;
  $("#release-body").hidden = false;
  release.fields = releaseFields(info, { onChange: syncRelease });
  $("#release-fields").replaceChildren(release.fields.version, release.fields.notes);
  const items = releaseWarnings(info);
  $("#release-warnings").replaceChildren(...items);
  $("#release-warnings").hidden = !items.length;
  release.fields.sync();
}

function syncRelease(r) {
  if (!release?.info) return;
  $("#release-ok").disabled = !release.info.can_tag || !!r.error;
  setLabel($("#release-ok"), "tag", r.tag ? `Tag ${r.tag} and push` : "Tag and push");
}

$("#release-form").addEventListener("submit", attempt(async (e) => {
  if (e.submitter?.value !== "ok") return;
  e.preventDefault(); // stays open until the tag is pushed (or it fails)
  if (!release?.fields) return;
  const r = release.fields.read();
  if (r.error) return;
  const mine = release, { commit, project: p } = release;
  const ok = $("#release-ok");
  ok.disabled = true;
  setLabel(ok, "tag", `Pushing ${r.tag}…`);
  try {
    const out = await api("POST", `/api/projects/${p.id}/tags`, { sha: commit.sha, version: r.tag, notes: r.notes });
    // The sheet may have been closed meanwhile and opened on another commit:
    // that one stays open.
    if (release === mine) {
      release = null;
      $("#release-sheet").close();
    }
    toast(out.build ? "The release workflow is starting; Builds shows how it goes." : `Pushed to origin.`, {
      tone: "ok", icon: "tag", head: `Tagged ${out.tag}`,
      action: out.build ? { label: "Builds", run: openCI } : out.url ? { label: "View", run: () => window.open(out.url, "_blank", "noopener") } : null,
    });
    refreshGraph();
  } finally {
    release?.fields?.sync();
  }
}));
$("#release-sheet").addEventListener("close", () => { release = null; });
