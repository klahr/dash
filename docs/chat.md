# Chat

Dash has three kinds of chat with Claude: the **general chat** (not about any project), a
**project chat** (Claude runs in the project's checkout) and a **task chat** (on the task's
Timeline tab; Claude runs in the task's worktree while it has one, and sees its instructions and
changes). Each conversation is saved, so it survives reloads and restarts. On a hub, every chat
runs on one of your own runners, with its Claude login (see
[Several computers](several-computers.md#claude-runs-on-each-users-own-runner)).

## Your own project chats

Each member has their own chat about each project. Other members don't see it, and neither do
the organization's owners and admins. `/clear` clears only yours.

Your project chat is the same on each of your devices and browser windows:

- What one device sends and Claude's reply show on the others when the reply is done. While
  Claude answers, the others say **Claude is answering on another of your devices…**.
- A message sent from a window that hasn't caught up yet isn't sent: the chat reloads, and your
  message goes back in the box to send again. A window that is behind never removes messages.

In the composer, <kbd>Enter</kbd> sends, messages written while Claude answers are queued, `/clear`
starts a new conversation and `/help` lists these.

<kbd>Up</kbd> on the first line of the message box shows the message you sent before it, as a
shell's history does; <kbd>Down</kbd> on the last line goes forward again, back to what you were
writing. Typing stops the stepping, so an edited message stays as you left it.

## Suggested next message

After each reply, Dash guesses what you'll say next and shows it greyed out in the empty message
box, as Claude Code's CLI does. The guess answers Claude's last message: if Claude asked a question,
it answers it; if Claude offered something, it accepts or turns it down. It never repeats your last
message or asks for something Claude already did, such as a task it just created or queued.

- <kbd>Enter</kbd> or **Send** sends the suggestion as it is.
- <kbd>Tab</kbd> puts it in the box, so you can edit it before sending.
- Typing hides it. Erase what you wrote and it shows again.
- Sending or a new reply clears it. No suggestion shows when no reply clearly fits, for example
  after a finished answer with no question or offer.

The guess is a short call to Haiku with the last few messages, on the same Claude login as the
chat (on a hub, your own runner). If it fails or no runner of yours is online, the box keeps its
usual placeholder.

To turn it off, open **Account → Chat** and clear **Suggest what to say next**. It's on by default
and applies to this browser only. When it's off, no call is made.

## Shell commands with `!`

In a project chat or a task chat, a message starting with `!` isn't sent to Claude: the rest of
it runs as a shell command where the chat runs, as in Claude Code.

```
!git status
!go test ./internal/api/...
!ls -la build/
```

- **Where it runs**: the project's checkout for a project chat, the task's worktree for a task
  chat (the project's checkout once the task is merged or has no worktree). On a hub, that is on
  the runner the chat runs on, never on the hub: the runner linked to the project, or the one
  that ran the task.
- **What you see**: the command streams its output (stdout and stderr together) into a block in
  the chat, with a Stop button (the composer's send button) while it runs and its exit code when
  it ends. The composer shows **Shell mode** while its text starts with `!`.
- **Claude sees it**: the command and its output are saved in the conversation as your message,
  so on your next message Claude reads them as a command you ran yourself (not one it ran).
  Long output keeps its first and last 16 KB, with a note of how much was left out between them;
  that is what is saved and what Claude reads.
- **Timeout**: a command that runs longer than 5 minutes is stopped, with everything it started.
  The runner's `-chat-command-timeout` flag changes that (e.g. `-chat-command-timeout 15m`).
  Stop does the same at once. What it printed until then is kept.
- **No terminal**: commands run without a TTY and without input, with `$SHELL -c` (else
  `/bin/sh`), `TERM=dumb` and no pager. Interactive programs (vim, top, less, `ssh` asking for a
  password, …) fail, or wait until the timeout. Use the **Shell** view (a real terminal) for
  those.
- **No approval prompt**: you typed the command, so it runs as it is, as your user on that
  computer. Claude's permission settings don't apply to it.

Who may run them:

- Only the runner's owner, the same rule as for the chat itself: a runner always takes `!` commands
  from the hub, but, like chat turns, runs them only for the user who registered it, checked on
  the runner.
- Not in a **read-only chat**: a runner started with `-chat-tools readonly` runs no commands, so
  it refuses `!` commands too, and the composer says so (the message goes to Claude as text).
- Not in the **general chat**, which has no project folder to run in.
- On a hub, only on a runner that has told the hub it runs them: a runner older than this feature
  doesn't, so update it first.

A message that starts with `!` while Claude is answering is queued like any other, and runs on
its own once the answer is done.
