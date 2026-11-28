# Codex

A project's tasks can run on OpenAI's [Codex CLI](https://github.com/openai/codex) instead of
Claude Code. Each project picks its agent. Claude Code is the default.

## Install and sign in

Do this on each computer that runs `dash runner` and should run Codex tasks.

1. Install the Codex CLI:

   ```sh
   npm install -g @openai/codex
   ```

2. Sign in with your ChatGPT account, or with an API key:

   ```sh
   codex login
   ```

   ```sh
   printenv OPENAI_API_KEY | codex login --with-api-key
   ```

3. Check that it is signed in:

   ```sh
   codex login status
   ```

The runner checks the CLI when it starts, then every hour, and whether it is signed in every
five minutes. **Runners** shows each runner's agents, their versions and whether they are signed
in, with the command to sign in when one isn't. A runner gets the `codex` label once Codex is
installed and signed in there. The runner finds `codex` on its `PATH` and in the usual install
folders (see [Agents](several-computers.md#agents)).

For a `codex` binary somewhere else, start the runner with `-codex /path/to/codex`. Extra
flags for task runs go in `-codex-args`, for example:

```sh
dash runner -codex-args "--model gpt-5.5"
```

## Pick Codex for a project

1. Open **Project settings → Agent & runners**.
2. Choose **Codex** under **Agent**.

The change saves at once. Queued tasks wait for a runner that has Codex installed and signed in.
**Project settings → Agent & runners** says when no online runner has it, or, on a runner's own page,
when Codex isn't installed or signed in on that computer.

## What Codex does differently

Codex runs tasks in its own sandbox: it can write in the task's worktree and use the network,
and nothing else. Nobody is asked before a command, so a command the sandbox blocks fails.
Your `-codex-args` win over these defaults. To run tasks without the network:

```sh
dash runner -codex-args "-c sandbox_workspace_write.network_access=false"
```

Summaries, titles and other short prompts run Codex without tools: it can't run commands, read
files or search the web for them.

These don't work with Codex:

- **Permission prompts.** Dash can't ask you before a tool call. Codex's sandbox decides.
- **Usage limits.** Codex doesn't report them: Dash can't pause the queue before Codex reaches
  a limit, and the usage meter shows Claude only.
- **Turn limit.** **Turn limit per run** doesn't apply. **Time limit per run** still does.
- **Cost.** Codex reports tokens, not cost. Dash estimates the cost from the tokens, shows it
  with ≈ and "(estimate)" in **Token use**, and counts it toward [spending caps](spending.md).
  Task logs show no turns.
- **Claude models.** A task's model is passed only if it isn't a Claude one (`opus`,
  `claude-…`). Otherwise Codex uses its default model.
- **Editing from the chat.** The project and task chats run on Codex too, but read only. They
  can read the code, answer, and create tasks. `!` commands still work.
- **Scans.** Scans always run on Claude Code: Dash keeps a scan's file reads inside its
  checkout, and Codex can't do that.

Titles, summaries and splits use Claude Code when it is signed in, else Codex.

These work as with Claude Code: resuming a task's session after notes or a restart, a follow-up
task that continues another's session, and Dash's task tools (`run_tests`, `report_issue`, active work).

## Work for other members

A runner shared with other members runs their tasks on its API key, not its owner's login. For
Codex, set `CODEX_API_KEY` in the runner's environment. Without it, those tasks stop with a
message.
