# Design note: agents over the Agent Client Protocol (ACP)

Status: built, November 2026. Dash has one generic ACP provider and a Gemini CLI profile
(`gemini`). For setup and use, see [ACP agents and Gemini CLI](acp-agents.md).

Question: can one generic provider run any agent CLI that speaks
[ACP](https://agentclientprotocol.com), instead of one native provider per CLI like Claude Code
and Codex?

Answer: yes, for tasks and resume, with gaps. Dash has one ACP provider with a small profile per
agent. Claude Code and Codex stay native.

## What was built

The code is in `internal/agent`:

| File | What |
|---|---|
| `acp_rpc.go` | A small JSON-RPC client over the agent's stdin and stdout. No SDK: `acp-go-sdk` wasn't needed for this much. |
| `acp_session.go` | Start, `initialize`, `authenticate`, `session/new`, `load`, `resume`, `fork`, modes, models, config options, prompt, cancel. |
| `acp_events.go` | `session/update` to Dash's events. |
| `acp_perm.go` | `session/request_permission` through Dash's `permission_prompt` tool. |
| `acp.go` | The provider (`ACP`) and the profile (`ACPProfile`). |
| `acp_cli.go` | The CLI check: name, version and abilities from `initialize`, sign-in from `auth_required` (another error: sign-in unknown). |
| `acp_chat.go` | The read-only chat. |
| `gemini.go` | The Gemini CLI profile. |

Choices made:

- ACP v1 only. An agent that answers another version fails with a message.
- Features come from the agent's `initialize` answer, which the CLI check keeps: `resume` when
  it has `session/resume` or `loadSession`, `mcp` when it has `mcpCapabilities.http`,
  `permission-hook` with `mcp`. No `tool-rules`, no `usage`.
- Updates the agent replays while it loads a session are left out of the task log.
- A follow-up task forks only when the agent has `session/fork`. Otherwise it starts fresh:
  loading the parent's session would change it.
- `AppendSystem` leads the first prompt in `<system-instructions>`. ACP has no system prompt.
- An MCP tool call is named `mcp__server__tool` when the agent says which server and tool in
  the call's `_meta` (Gemini's `_meta["gemini-cli"].mcp`). Dash allows the ones in
  `AllowMCPTools` without asking.
- Without a permission tool (an unattended run), Dash rejects every request but for tools
  `AllowedTools` names outright.
- `Prompt` fails: `Set.Prompter` skips ACP agents, so titles and summaries stay on Claude Code or
  Codex.
- `dash mcp-proxy` wasn't built: Gemini takes HTTP MCP servers. An agent without them runs
  without Dash's MCP servers, with a note in the log.

Gemini CLI, checked against its 0.71 source:

- With `fs` off it reads and writes files itself. It needs no file serving from Dash.
- It has `loadSession` and HTTP MCP servers. It has no `session/resume` or `session/fork`.
- Its modes are `default`, `autoEdit`, `yolo` and `plan`. Tasks run in `autoEdit`; the chat
  and unattended runs in `default`.
- Signed out, `session/new` answers `auth_required`. With `GEMINI_API_KEY` set, Dash then calls
  `authenticate` with `gemini-api-key` and tries again.
- `usage_update` has tokens but no cost.

The tests (`acp_test.go`) run a fake agent that plays recorded ACP traffic
(`testdata/acp/*.jsonl`), built from Gemini CLI's and OpenCode's messages. They don't run a real
CLI.

## What Dash needs from a provider

The interface is in `internal/agent/provider.go`. A provider is an `Executor` (task runs), a
`Prompter` (titles, summaries) and a `Chat`. Optional abilities are separate interfaces:

| Ability | Interface | Feature |
|---|---|---|
| Continue a session, fork it | `Resumer` | `resume` |
| Dash's MCP servers (task tools) | `MCPConfigurer` | `mcp` |
| Ask Dash before a tool call | `PermissionHook` | `permission-hook` |
| Allow and deny rules per tool (scans) | `ToolRuler` | `tool-rules` |
| Account rate limits | `RateLimitReporter` | `usage` |
| Version and sign-in check | `StatusReporter` | — |

Claude Code has all of them. Codex has resume, MCP and the CLI check (see [Codex](codex.md)).

## ACP in brief

- JSON-RPC 2.0 over the agent's stdin and stdout, one message per line.
- The client starts the agent, then calls `initialize`, `session/new` (with `cwd` and
  `mcpServers`) and `session/prompt`. The agent streams `session/update` notifications and
  calls back `session/request_permission` before tool calls that need it.
- v1 is stable. v2 has been in preview since 2026-11-20 and isn't frozen. In v2 the client no
  longer serves files and terminals, which suits Dash.
- There is no official Go SDK. `github.com/coder/acp-go-sdk` is the leading community one.

Agents that speak ACP today:

| Agent | How | Start command | State |
|---|---|---|---|
| Gemini CLI | native | `gemini --acp` | stable flag (was `--experimental-acp`) |
| OpenCode | native | `opencode acp` | mature: load, resume, fork, list, usage, MCP over HTTP |
| GitHub Copilot CLI | native | `copilot --acp --stdio` | public preview |
| Goose | native | `goose acp` | supported; features not checked |
| Qwen Code, Cline, Augment (Auggie), Cursor, Kiro, Mistral Vibe, Kimi | native | see the [registry](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json) | varies |
| Claude Code | adapter | `npx @agentclientprotocol/claude-agent-acp` | mature, but less than Dash's native provider |
| Codex | adapter | `npx @agentclientprotocol/codex-acp` | mature, but less than Dash's native provider |
| Amp | community adapter | `amp-acp` | unofficial |

Optional features differ per agent. An agent's `initialize` answer says which it has.

## Fit, ability by ability

**Run headless in a worktree: yes.** Start the agent in the worktree and call `session/new`
with `cwd` set to the worktree. Tell the agent that the client serves no files or terminals
(`fs` and `terminal` off), so it uses its own tools. Cancel with `session/cancel`, then kill the
process group. A [sandbox](sandbox.md) command wraps the agent as it does Codex.

Gaps:

- No turn limit. `MaxTurns` can't be passed. The time limit still applies.
- `AppendSystem` needs the agent's `systemPrompt` capability. Without it, Dash puts the text in
  the first prompt.
- `AddDirs` needs the `additionalDirectories` capability. Without it, attachments must be
  copied into the worktree.
- `Model` and `Effort` are session config options whose values differ per agent. The profile
  maps them.
- Gemini CLI may expect the client to serve file reads and writes. Test it with `fs` off.

**Stream events: yes.** `agent_message_chunk` becomes `text`, `tool_call` becomes `tool_use`,
and a `tool_call_update` that completes or fails becomes `tool_result`. The end of
`session/prompt` becomes `result`.

Gaps:

- Text comes in chunks. Dash joins them into one event per message.
- Tool names and inputs are the agent's, not Claude Code's. `toolLine` reads `command` and
  `file_path`, so it needs a fallback to the tool call's `title`, `kind` and `locations`.

**Resume: mostly.** `session/resume` (stable) and `session/load` continue a session when the
agent says it can. An error on resume sets `ResumeFailed`, and Dash starts over.

Gaps:

- `session/fork` is still unstable. A follow-up task can fork only on agents that offer it.
  Otherwise it starts fresh with the full prompt.
- Some agents (OpenCode) refuse a session loaded in another `cwd`. A follow-up task runs in a
  new worktree, so it can't continue such a session.

**Dash's MCP servers: yes, on most agents.** `session/new` takes `mcpServers`. Dash's servers
are HTTP with a bearer token, which needs the agent's `mcpCapabilities.http`.

Gaps:

- Every agent must take stdio servers, but not HTTP ones. For agents without HTTP, Dash needs a
  small stdio-to-HTTP bridge command (`dash mcp-proxy`) that keeps the token in its
  environment.
- `AllowMCPTools` has no ACP field. Dash answers permission requests for those tools itself.
- `ToolTimeout` has no ACP field. A long `run_tests` call may hit the agent's own timeout.

**Permission hook: yes, and better than Codex.** ACP has `session/request_permission` built in.
The provider answers it by calling Dash's `permission_prompt` tool, whose URL and token are in
`ExecRequest.MCPServers`. Nothing changes in the queue or in `internal/perm`. Dash picks the
agent's `allow_once` or `reject_once` option and keeps "always" in its own grants.

Gaps:

- Saved grants and the **Needs you** list use Claude Code's tool names (`Bash`, `Edit`). The
  provider maps ACP's `kind` (`execute`, `edit`, `read`, `fetch`) to those names.
- Each agent decides which calls it asks about. Many read files without asking.
- There is no portable "never ask" mode. An unattended run sets the profile's mode, or Dash
  rejects every request outside `AllowedTools`.
- `AskUserQuestion` has no match. ACP's `elicitation` is new and few agents use it.

**Tool rules: no.** ACP has no allow and deny rules. Dash can apply rules only to calls the
agent asks about, and reads are often not asked about. So Dash can't keep a scan's reads in its
checkout, and scans stay on Claude Code. Some agents have their own switches (Copilot's
`--excluded-tools`, the Claude adapter's `disallowedTools`), but they aren't portable.

**Usage: partly.**

- `usage_update` (stable) gives the context size and, if the agent reports it, the cost so far.
  A reported cost can feed `OnCost` and spending caps.
- Tokens per turn (input, output, cache) are only a draft
  ([end-turn token usage](https://agentclientprotocol.com/rfds/end-turn-token-usage)). Without
  them, and without a reported cost, Dash can't estimate cost. Spending caps then can't stop the
  run.
- Account rate limits (the `usage` feature) aren't in ACP at all.

**Prompts and chat: weak.** ACP can't start a session without tools. A one-shot prompt would
run with the agent's tools, and its input may be a task's text. Titles and summaries should keep
using Claude Code or Codex, as they already do when those are signed in. A read-only chat works
by rejecting every permission request, but reads that aren't asked about still happen.

**CLI check and sign-in: partly.** `initialize` gives the agent's name and version. Sign-in
shows only as an `auth_required` error on `session/new`. API keys are each agent's own
environment variables, so the profile names the key for work for other members (`Shared`).

## Summary of gaps

1. No turn limit.
2. No tool rules: scans can't run on ACP agents.
3. No tokens per turn yet: cost is known only when the agent reports it.
4. No account rate limits.
5. No tool-free one-shot prompt.
6. Fork is unstable; resume across worktrees varies.
7. HTTP MCP servers are optional: a stdio bridge is needed for some agents.
8. Tool names and inputs differ per agent: permission grants and log lines need a mapping.
9. Models, modes and the "never ask" mode are agent-specific strings.
10. v2 is in preview: the client must handle both v1 and v2, or wait for v2 to settle.

## Recommendation (followed)

Build one generic ACP provider, with a profile per agent. Keep Claude Code and Codex native:
they do more natively (tool rules, rate limits, turn limit, tool-free prompts) than through
their ACP adapters.

A profile is a small Go value: the start command, the API key variable, the mode for
unattended runs, the model and effort mapping, and the tool `kind` to name mapping. Each
profile registers under its own name (`gemini`, `opencode`, `copilot`) with `RegisterProvider`,
so projects pick it like Codex. The provider reports its features from the agent's
`initialize` answer, not from a fixed list.

Start with OpenCode (most complete) and Gemini CLI, then Copilot CLI. Gemini CLI came first;
OpenCode and Copilot CLI are each a new profile value.

Native adapters per agent cost much more. Each CLI has its own headless mode, JSON events and
flags, and most have no permission hook outside ACP.

## Size (estimated before the work)

| Work | Code | Tests |
|---|---|---|
| Generic ACP provider: client, session lifecycle, event translation, resume | ~1,200 lines | ~800 lines |
| Permission bridge to `permission_prompt`, tool name mapping | ~250 lines | ~200 lines |
| `dash mcp-proxy` (stdio to HTTP), for agents without HTTP MCP | ~150 lines | ~100 lines |
| CLI check from `initialize` | ~150 lines | ~100 lines |
| One profile and its docs page | ~80 lines | ~100 lines |
| **First agent, total** | **~1,850 lines** | **~1,300 lines** |
| **Each further agent** | **~80 lines** | **~100 lines**, plus a manual test |

A native adapter per agent costs about what Codex did: about 1,100 lines of code and 700 of
tests (`codex.go`, `codex_events.go`, `codex_cli.go`, `codex_chat.go`). That's per agent, and
again each time its CLI changes its flags or events.

The generic provider pays off from the second agent.
