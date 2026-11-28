// The agent CLIs this Dash runs tasks on (GET /api/agents): Claude Code,
// and any other registered provider, each with the optional features it
// has ("resume", "mcp", "permission-hook", "tool-rules", "usage"). A project picks one in
// Project settings → Agent & runners; the task view leaves out what it can't do.
import { api } from "./api.js";
import { h } from "./dom.js";
import { state } from "./state.js";
import { DEFAULT_AGENT, projectAgent } from "../groups.js";

// FEATURES names the features for people, in the order they are shown:
// a short label, and what it lets the agent do.
export const FEATURES = {
  resume: { label: "Resume", does: "continue a task's earlier session" },
  mcp: { label: "MCP", does: "use Dash's MCP servers (the task tools)" },
  "permission-hook": { label: "Permission prompts", does: "ask you in Dash before a tool call it isn't allowed" },
  "tool-rules": { label: "Scans", does: "keep a scan's file reads inside its checkout (scans use Claude Code)" },
  usage: { label: "Usage", does: "report its account's usage limits" },
};

let loading = null;
// loadAgents fetches the list once (it changes only with the Dash binary).
export function loadAgents() {
  loading ||= api("GET", "/api/agents").then((list) => { state.agents = list || []; }).catch(() => { loading = null; });
  return loading;
}

// agentTitle is agent name's name for people, e.g. "Claude Code".
export const agentTitle = (name) => (state.agents || []).find((a) => a.name === (name || DEFAULT_AGENT))?.title
  || (!name || name === DEFAULT_AGENT ? "Claude Code" : name);

// agentCan reports whether project p's agent has feature f. An agent this
// Dash doesn't list (the list not loaded yet) isn't held back.
export function agentCan(p, f) {
  const a = (state.agents || []).find((x) => x.name === projectAgent(p));
  return !a || (a.features || []).includes(f);
}

// SETUP is how to install each agent's CLI and sign it in, for the hint
// when a computer lacks either: what GET /api/agents says (install, login),
// else these, for a hub older than that.
const SETUP_FALLBACK = {
  claude: { install: "install it with `npm install -g @anthropic-ai/claude-code`", login: "claude" },
  codex: { install: "install it with `npm install -g @openai/codex`", login: "codex login" },
  gemini: { install: "install it with `npm install -g @google/gemini-cli`", login: "gemini" },
};
export function agentSetup(name) {
  const a = (state.agents || []).find((x) => x.name === name);
  const f = SETUP_FALLBACK[name] || {};
  return { install: a?.install || f.install || "install its CLI", login: a?.login || f.login || name };
}

// withCode is text with its `quoted` parts as code elements, for hints
// such as agentSetup's.
export const withCode = (text) => String(text).split("`").map((part, i) => i % 2 ? h("code", {}, part) : part);

// signInState is how agent a, as a runner reported it, stands on signing
// in: "signed-in", "signed-out", "unknown" (Dash couldn't tell, as with an
// ACP agent until a session fails with auth_required), or "" when it isn't
// installed or checked yet. An older runner sends only logged_in.
export function signInState(a) {
  if (!a?.checked_at || !a.installed) return "";
  return a.sign_in || (a.logged_in ? "signed-in" : "signed-out");
}

// agentReadyOn reports whether runner r, online, has agent name installed
// and known to be signed in. A runner that says nothing of its agents runs
// Claude Code, as every runner did. An unknown sign-in isn't ready here.
export function agentReadyOn(r, name) {
  if (!r?.online) return false;
  if (!r.agents) return name === DEFAULT_AGENT;
  return signInState((r.agents || []).find((a) => a.name === name)) === "signed-in";
}

// suggestAgent is the agent a new project should run on, given the runner
// picked for it (first) and the others (runners): Claude Code when a
// connected runner has it ready, else the first agent that the picked
// runner, then any other, has ready, else Claude Code.
export function suggestAgent(picked, runners) {
  const names = (state.agents || []).map((a) => a.name);
  if (!names.includes(DEFAULT_AGENT)) names.unshift(DEFAULT_AGENT);
  const online = [picked, ...(runners || []).filter((r) => r !== picked)].filter((r) => r?.online);
  if (online.some((r) => agentReadyOn(r, DEFAULT_AGENT))) return DEFAULT_AGENT;
  for (const r of online) {
    const n = names.find((x) => agentReadyOn(r, x));
    if (n) return n;
  }
  return DEFAULT_AGENT;
}

// featureLabel names feature f for people: FEATURES' label, else as the
// agent reported it (an ACP agent may report features Dash doesn't name).
export const featureLabel = (f) => FEATURES[f]?.label || f;

// NOTES are what else people should know of an agent in Project settings.
export const NOTES = {
  codex: "Its chat only reads: it can't edit files from the chat. See docs/codex.md.",
  gemini: "Runs over ACP. Its chat only reads, and scans stay on Claude Code. See docs/acp-agents.md.",
};

// cantSay is why project p's agent leaves out feature f, for a tooltip.
export const cantSay = (p, f) => `${agentTitle(projectAgent(p))}, this project's agent, can't ${FEATURES[f]?.does || f}`;
