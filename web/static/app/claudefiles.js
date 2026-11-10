import { $, h, ib } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, fail, toast } from "./dialogs.js";
import { state } from "./state.js";
import { projectKey } from "./events.js";
import { showView } from "./nav.js";
import { formSnapshot, markSettingsSaved, settingsSnapshot } from "./settings.js";
import { loadFiles, newFile, openFile } from "./files.js";

// ------------------------------------------------------------------ Claude files
// The Claude section of the project settings lists the repo's Claude Code
// files (CLAUDE.md, skills, subagents, .mcp.json, .claude/settings.json,
// CLAUDE.local.md). They are shortcuts into the file editor, not settings:
// saving a tracked file opens a task that commits it, like any edit there.
const CLAUDE_GROUPS = [
  { kind: "claude_md", title: "CLAUDE.md", note: "The root file only: project instructions Claude Code reads at the start of every session." },
  { kind: "skill", title: "Skills", note: "One folder per skill under .claude/skills, each with a SKILL.md.", add: "New skill" },
  { kind: "agent", title: "Subagents", note: "Markdown files under .claude/agents.", add: "New subagent" },
  { kind: "mcp", title: "MCP servers", note: "Servers shared with everyone who works on the repo." },
  { kind: "settings", title: "Hooks & permissions", note: "Shared Claude Code settings: hooks, permission rules and more." },
  { kind: "local_md", title: "CLAUDE.local.md", note: "Your personal instructions, kept out of git." },
];
const CLAUDE_TEMPLATES = {
  "CLAUDE.md": "# CLAUDE.md\n\nGuidance for Claude Code in this repository.\n\n## Commands\n\n## Architecture\n\n## Conventions\n",
  ".mcp.json": '{\n  "mcpServers": {}\n}\n',
  ".claude/settings.json": '{\n  "permissions": {\n    "allow": [],\n    "deny": []\n  },\n  "hooks": {}\n}\n',
};
let claudeFilesSeq = 0;

export async function loadClaudeFiles() {
  const p = state.project, box = $("#claude-files"), seq = ++claudeFilesSeq;
  box.replaceChildren(h("p", { class: "hint" }, "Looking for Claude files…"));
  let res;
  try {
    res = await api("GET", `/api/projects/${p.id}/claude-files`);
  } catch (e) {
    if (seq === claudeFilesSeq) box.replaceChildren(h("p", { class: "hint warn" }, `Couldn't read the repository's Claude files: ${e.message}`));
    return;
  }
  if (seq !== claudeFilesSeq || projectKey(state.project) !== projectKey(p)) return;
  renderClaudeFiles(res);
}

function renderClaudeFiles(res) {
  const p = state.project;
  $("#claude-files-hint").textContent = `Files Claude Code reads from the project, as on ${res.branch}. Edit opens them in the file editor: saving a tracked file opens a task to commit the change; files git doesn't track are saved straight to disk.`;
  const groups = CLAUDE_GROUPS.map((g) => {
    const files = res.files.filter((f) => f.kind === g.kind);
    if (g.kind === "local_md" && !files.length) return null;
    const head = h("div", { class: "claude-group-head" },
      h("h4", {}, g.title),
      g.add ? ib({ type: "button", class: "btn small", onclick: () => newClaudeItem(g.kind).catch(fail) }, "plus", g.add) : null);
    const rows = files.map((f) => claudeFileRow(f, p));
    if (!rows.length) rows.push(h("li", { class: "claude-file empty" }, h("span", { class: "hint" }, g.kind === "skill" ? "No skills yet." : "No subagents yet.")));
    return h("section", { class: "claude-group" }, head, h("p", { class: "hint" }, g.note), h("ul", { class: "claude-file-list" }, rows));
  });
  $("#claude-files").replaceChildren(...groups.filter(Boolean));
}

function claudeFileRow(f, p) {
  const where = !f.exists ? h("span", { class: "scope-tag missing" }, "missing")
    : f.local ? h("span", { class: "scope-tag local", title: "Git doesn't track it: saving writes it straight to disk" }, "local, not in git")
      : h("span", { class: "scope-tag tracked", title: "Saving opens a task to commit the change" }, "in git");
  const title = f.name && f.name !== f.path ? h("span", {}, h("strong", {}, f.name), " ", h("code", {}, f.path)) : h("code", {}, f.path);
  const actions = h("div", { class: "claude-file-actions" });
  if (f.exists) {
    actions.append(ib({ type: "button", class: "btn small", onclick: () => editClaudeFile(f.path).catch(fail) }, "edit", "Edit"));
  } else {
    actions.append(ib({ type: "button", class: "btn small", onclick: () => editClaudeFile(f.path, CLAUDE_TEMPLATES[f.path] ?? "").catch(fail) }, "plus", "Create"));
    if (f.kind === "claude_md") actions.append(h("button", { type: "button", class: "btn small", onclick: () => generateClaudeMd().catch(fail) }, "Generate with /init"));
  }
  const body = h("div", { class: "claude-file-main" }, h("div", { class: "claude-file-title" }, title, where));
  if (f.description) body.append(h("p", { class: "claude-file-desc" }, f.description));
  if (f.summary && f.kind !== "skill" && f.kind !== "agent") body.append(h("pre", { class: "claude-file-preview" }, f.summary));
  if (f.exists) body.append(h("p", { class: "hint" }, f.local ? "Saving writes it straight to disk." : "Saving opens a task to commit this change."));
  else if (f.kind !== "local_md") body.append(h("p", { class: "hint" }, "Creating it opens a task that commits the new file."));
  if (f.kind === "local_md") body.append(localMdWarning(p));
  return h("li", { class: "claude-file" }, body, actions);
}

// CLAUDE.local.md isn't in git, so task worktrees only get it when it is a
// file to copy, and runners (with clones of their own) never do.
function localMdWarning(p) {
  const listed = (p.copy_files || []).some((x) => x.replace(/^\.\//, "") === "CLAUDE.local.md");
  const box = h("div", { class: "claude-file-warn" });
  if (!listed) {
    box.append(h("p", { class: "hint warn" }, "Task worktrees don't get this file: it isn't in git, and it isn't in Files to copy into worktrees."),
      h("button", { type: "button", class: "btn small", onclick: attempt(async () => {
        const cur = state.project;
        const saved = await api("PATCH", `/api/projects/${cur.id}`, { copy_files: [...(cur.copy_files || []), "CLAUDE.local.md"] });
        state.project = { ...cur, copy_files: saved.copy_files };
        state.projects = state.projects.map((x) => (x === cur ? state.project : x));
        const f = $("#settings-form");
        const lines = f.copy_files.value.split("\n").map((x) => x.trim()).filter(Boolean);
        if (!lines.includes("CLAUDE.local.md")) f.copy_files.value = [...lines, "CLAUDE.local.md"].join("\n");
        markSettingsSaved(f);
        toast("CLAUDE.local.md is copied into new worktrees");
        loadClaudeFiles().catch(fail);
      }) }, "Add to copy_files"));
  }
  box.append(h("p", { class: "hint warn" }, p.runners?.length
    ? "This project's tasks run on runners, which never get this file: put what they need in CLAUDE.md or the extra instructions below."
    : "Runners never get this file, even when it is copied."));
  return box;
}

// editClaudeFile closes the settings and opens path in the file editor;
// with a template, as a new file whose save opens a task that creates it.
async function editClaudeFile(path, template = null) {
  const d = $("#settings-sheet"), f = $("#settings-form");
  let how = "cancel";
  if (formSnapshot(f) !== settingsSnapshot) {
    if (!(await ask({ title: "Save your settings first?", body: "Your changes to the project settings are saved, then the file opens in the editor.", ok: "Save and open" }))) return;
    how = "save";
  }
  d.close(how);
  showView("files");
  await loadFiles();
  if (template == null) await openFile(path);
  else await newFile(path, template);
}

async function newClaudeItem(kind) {
  const what = kind === "skill" ? "skill" : "subagent";
  const name = (await ask({ title: `New ${what}`, input: true, placeholder: kind === "skill" ? "release-notes" : "code-reviewer", ok: "Create",
    body: `A short name in lowercase letters, digits and hyphens. It opens in the file editor; saving opens a task that commits it.` }))?.trim().toLowerCase();
  if (!name) return;
  if (!/^[a-z0-9][a-z0-9-]{0,63}$/.test(name)) throw new Error("Use lowercase letters, digits and hyphens, like release-notes.");
  if (kind === "skill") {
    await editClaudeFile(`.claude/skills/${name}/SKILL.md`,
      `---\nname: ${name}\ndescription: What this skill does, and when Claude should use it.\n---\n\n# ${name}\n\nInstructions for Claude.\n`);
  } else {
    await editClaudeFile(`.claude/agents/${name}.md`,
      `---\nname: ${name}\ndescription: When Claude should hand work to this subagent.\ntools: Read, Grep, Glob\n---\n\nYou are a subagent that…\n`);
  }
}

async function generateClaudeMd() {
  const p = state.project;
  if (!(await ask({ title: "Generate CLAUDE.md with /init?", ok: "Queue task",
    body: "Queues a task that runs Claude Code's /init: Claude looks through the repository and writes a CLAUDE.md, which you review like any other task." }))) return;
  await api("POST", `/api/projects/${p.id}/todos`, { title: "Generate CLAUDE.md with /init", prompt: "/init", enqueue: true });
  toast("Queued a task to generate CLAUDE.md");
}
