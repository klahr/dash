# ACP agents and Gemini CLI

A project's tasks can run on an agent CLI that speaks the
[Agent Client Protocol](https://agentclientprotocol.com) (ACP). Dash has one provider for all of
them, with a small profile per agent. The first one is Google's
[Gemini CLI](https://github.com/google-gemini/gemini-cli), as **Gemini CLI**.

Claude Code stays the default. Codex has its own page: [Codex](codex.md).

## Install and sign in

Do this on each computer that runs `dash runner` and should run Gemini tasks.

1. Install the Gemini CLI:

   ```sh
   npm install -g @google/gemini-cli
   ```

2. Run it once and sign in with your Google account:

   ```sh
   gemini
   ```

   Or set an API key in the runner's environment instead. Dash signs in with it when the CLI asks:

   ```sh
   export GEMINI_API_KEY=your-key
   ```

The runner starts `gemini --acp` when it starts, then every hour, and every five minutes to see
whether it is signed in. **Runners** shows each runner's agents, their versions and whether they
are signed in. A runner gets the `gemini` label once the Gemini CLI is installed and signed in
there.

If `gemini` isn't on the runner's `PATH`, start the runner with `-gemini /path/to/gemini`. Extra
flags for task runs go in `-gemini-args`, for example:

```sh
dash runner -gemini-args "--debug"
```

## Pick Gemini CLI for a project

1. Open **Project settings → Agent & runners**.
2. Choose **Gemini CLI** under **Agent**.

Queued tasks wait for a runner that has the Gemini CLI installed and signed in.

## What works

- **Tasks.** Gemini runs in the task's worktree, in its **Auto Edit** mode: it edits files
  without asking, and asks before commands.
- **Permission prompts.** Each command Gemini asks about shows in **Needs you**, named as Claude
  Code names it (`Bash`, `Edit`, `Read`). **Always** grants work as with Claude Code.
- **Dash's task tools.** `run_tests`, `report_issue` and active work work, and Dash allows them
  without asking.
- **Resume.** After notes or a restart, a task continues its Gemini session.
- **Attachments.** Dash passes their folder with `--include-directories`.
- **The chat.** It reads only: Dash rejects every change Gemini asks to make. It can read the
  code, answer, and create tasks.

## What doesn't

- **Follow-up tasks** start a fresh session with the full prompt. Gemini can't fork a session.
- **Turn limit.** **Turn limit per run** doesn't apply. **Time limit per run** still does.
- **Cost.** Gemini reports tokens, not cost, so Dash shows no cost for its runs, and spending
  caps can't stop them.
- **Models.** A task's model is passed if it isn't a Claude one (`opus`, `claude-…`). Otherwise
  Gemini uses its default model. Effort doesn't apply.
- **Usage limits.** Gemini doesn't report them.
- **Scans.** Scans always run on Claude Code: Dash can't keep an ACP agent's file reads inside
  the scan's checkout.
- **Titles, summaries and splits** use Claude Code or Codex, whichever is signed in. An ACP
  agent can't answer without its tools.

Gemini decides itself which calls it asks about. Reads are never asked about.

## Work for other members

A runner shared with other members runs their tasks on its API key, not its owner's login. Set
`GEMINI_API_KEY` in the runner's environment. Without it, those tasks stop with a message. Such a
task gets a Gemini config folder of its own (`GEMINI_CLI_HOME`).

## Sandbox

A [sandbox](sandbox.md) command (`-claude-sandbox`) wraps `gemini --acp` as it wraps Claude Code
and Codex.

## How it works

Dash starts the agent in the task's worktree and talks JSON-RPC to it on its stdin and stdout
(ACP version 1). It tells the agent that Dash serves no files and no terminals, so the agent
uses its own tools. It opens a session with the worktree and Dash's MCP servers, sends the
prompt, and turns the agent's updates into the task log. Stopping a task sends
`session/cancel`, then ends the agent's processes.

What an agent can do (resume, MCP servers over HTTP) comes from its answer to `initialize`.
The design note is [ACP](acp.md).

## Add another ACP agent

An agent is a profile in `internal/agent`: a Go value with its start command, its API key
variable, its session modes and its tool names. See `gemini.go`. Register it with
`RegisterACPProfile`, add its `-<name>` flags in `cmd/dash/startup.go`, and its install hint in
`web/static/app/agents.js`.
