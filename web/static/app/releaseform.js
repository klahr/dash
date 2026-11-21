import { h } from "./dom.js";
import { state } from "./state.js";
import { runState } from "./actions.js";
import { githubURL } from "./history.js";

// ------------------------------------------------------------------ release form
// The parts of making a release that History's Tag release… sheet and
// Deploy's picker share: the next version (Patch, Minor, Major, or Other,
// checked against the project's tag format), the notes drafted from the
// tasks merged since the last release, and the warnings (a red or running
// build, tasks still running). info is GET /api/projects/{pid}/release's
// answer.

// tagFormat parses a tag format ("vX.Y.Z"): render a version, parse a tag.
export function tagFormat(fmt) {
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

// checkVersion is the tag a custom version raw ("1.4.0", or a tag in the
// format) makes, with a note, or { error }.
export function checkVersion(info, raw) {
  raw = (raw || "").trim();
  if (!raw) return { error: "Type a version, like 1.4.0." };
  const fmt = tagFormat(info.format);
  let v = fmt.parse(raw);
  let tag = raw;
  if (!v) {
    const m = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(raw);
    if (!m) return { error: `Use a version like 1.4.0 or a tag like ${fmt.render([1, 4, 0])} (format ${info.format}).` };
    v = [+m[1], +m[2], +m[3]];
    tag = fmt.render(v);
  }
  if (info.tags.includes(tag)) return { error: `${tag} already exists.` };
  const latest = info.previous && fmt.parse(info.previous);
  const lower = latest && (v[0] - latest[0] || v[1] - latest[1] || v[2] - latest[2]) < 0;
  return { tag, note: `Tags ${tag}${lower ? ` — lower than the latest release, ${info.previous}` : ""}.` };
}

// releaseWarnings are the reasons to wait before tagging, as list items:
// why the caller can't tag, the commit's builds, running tasks, origin's
// tags out of reach, a release workflow that won't run for the format.
export function releaseWarnings(info, { cantTag = true } = {}) {
  const warn = (text, extra) => h("li", { class: "hint warn" }, text, extra || "");
  const ci = {
    failing: "The build failed on this commit.",
    running: "Builds are still running on this commit.",
    none: "No builds ran on this commit.",
    unknown: githubURL(state.project?.remote_url) ? "Couldn't tell whether this commit's build passed." : "",
  }[info.ci?.state];
  const failed = (info.ci?.runs || []).filter((r) => runState(r)[0] === "failing");
  return [
    cantTag && !info.can_tag && info.cant_tag ? h("li", { class: "hint err-text" }, info.cant_tag) : null,
    ci ? warn(ci, failed.length ? [" ", ...failed.slice(0, 3).map((r) => h("a", { href: r.url, target: "_blank", rel: "noopener" }, r.workflow))] : "") : null,
    info.running ? warn(`${info.running} ${info.running === 1 ? "task is" : "tasks are"} still running or merging; ${info.running === 1 ? "its" : "their"} work won't be in this release.`) : null,
    info.fetch_error ? warn(`Couldn't fetch tags from origin (${info.fetch_error}); the suggestions may be out of date.`) : null,
    info.hint ? warn(info.hint.message, info.hint.suggest ? ` Set the tag format to ${info.hint.suggest} in the project settings.` : "") : null,
  ].filter(Boolean);
}

// releaseFields builds the version choice and the notes for info, and
// keeps the Other version's hint up to date; onChange gets read()'s answer
// whenever the choice changes. read() is { tag, note } or { error }, with
// notes (the notes as typed, trimmed).
export function releaseFields(info, { onChange = () => {}, rows = 7 } = {}) {
  const s = info.suggestions || {};
  const choice = (label, tag, note) => h("label", { class: "radio" },
    h("input", { type: "radio", name: "version", value: tag }),
    h("span", {}, label, " ", h("code", {}, tag), h("em", {}, note)));
  const custom = h("input", { name: "custom", class: "release-custom", spellcheck: false, autocomplete: "off", "aria-label": "Other version" });
  const customRadio = h("input", { type: "radio", name: "version", value: "custom" });
  const hint = h("p", { class: "hint", hidden: true });
  const version = h("fieldset", { class: "field choice release-version" },
    h("legend", {}, "Version ", h("em", {}, info.previous ? `latest: ${info.previous}` : `no ${info.format} tag yet`)),
    ...(s.first
      ? [choice("First release", s.first, "no release tag in this format yet")]
      : [choice("Patch", s.patch, "fixes only"), choice("Minor", s.minor, "new features, nothing breaks"), choice("Major", s.major, "breaking changes")]),
    h("label", { class: "radio" }, customRadio, h("span", {}, "Other ", custom)),
    hint);
  version.querySelector('input[name="version"]').checked = true;
  const area = h("textarea", { name: "notes", rows, spellcheck: true, placeholder: `Release ${s.first || s.patch}` });
  area.value = info.notes || "";
  const notes = h("label", { class: "field" }, h("span", {}, "Release notes ", h("em", {}, "the tag's message; from the tasks merged since the last release")), area);

  const read = () => {
    const picked = version.querySelector('input[name="version"]:checked')?.value;
    const r = picked === "custom" ? checkVersion(info, custom.value) : { tag: picked };
    return { ...r, notes: area.value.trim() };
  };
  const sync = () => {
    const r = read();
    hint.hidden = !customRadio.checked;
    hint.className = `hint${r.error ? " err-text" : ""}`;
    hint.textContent = r.error || r.note || "";
    onChange(r);
  };
  version.addEventListener("input", sync);
  version.addEventListener("change", sync);
  custom.addEventListener("focus", () => { customRadio.checked = true; sync(); });
  // Enter would press the form's first button, its Close: keep what was typed.
  custom.addEventListener("keydown", (ev) => { if (ev.key === "Enter") ev.preventDefault(); });
  return { version, notes, read, sync };
}
