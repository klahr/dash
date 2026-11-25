// The agent CLIs this Dash runs tasks on (GET /api/agents): Claude Code,
// and any other registered provider, each with the optional features it
// has ("resume", "mcp", "permission-hook", "tool-rules", "usage"). A project picks one in
// Project settings → Agent & runners; the task view leaves out what it can't do.
import { api } from "./api.js";
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
// when this computer lacks either.
export const SETUP = {
  claude: { install: "install Claude Code (npm install -g @anthropic-ai/claude-code)", login: "claude" },
  codex: { install: "install the Codex CLI (npm install -g @openai/codex)", login: "codex login" },
};

// NOTES are what else people should know of an agent in Project settings.
export const NOTES = {
  codex: "Its chat only reads: it can't edit files from the chat. See docs/codex.md.",
};

// cantSay is why project p's agent leaves out feature f, for a tooltip.
export const cantSay = (p, f) => `${agentTitle(projectAgent(p))}, this project's agent, can't ${FEATURES[f]?.does || f}`;
