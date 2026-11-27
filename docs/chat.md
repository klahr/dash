# Chat

Dash has three kinds of chat with Claude: the **general chat** (not about any project), a
**project chat** (Claude runs in the project's checkout) and a **task chat** (on the task's
Timeline tab; Claude runs in the task's worktree while it has one, and sees its instructions and
changes). Each conversation is saved, so it survives reloads and restarts. On a hub, every chat
runs on one of your own runners, with its Claude login (see
[Several computers](several-computers.md#claude-runs-on-each-users-own-runner)).

Open the chat pane with **⋯ → Chat** in the top bar, or press `gc`. On a phone or tablet, tap
**Chat** in the bottom bar. On a tablet the chat sits beside the page; on a phone it fills the
screen.

The pane's first tab is the selected project's chat, named after the project, with its icon
ringed. **General** is next, then any chats you added with **+**.

## Which runner a project chat runs on

On a hub, a project chat runs on one of your runners that works on the project: one with the
labels in **Project settings → Agent & runners → Runs on**. When two or more of them are online,
a runner menu shows at the top of the chat. Pick one there. The next turn, and `!` commands, run
on it.

- Each project remembers its own pick, apart from **General**'s.
- If the runner you picked is offline, or no longer works on the project, the chat runs on another
  of yours that does, as it would without a pick.
- The line above the messages says which runner the last turn ran on.
- With one runner or none, there is no menu.

## A planner's chat

A [planner](users.md#planners) has the general chat only: **Chat** in their sidebar. It runs on
their own runner, with their own Claude login. Its Dash tools are Planning's: list, read,
create, update, comment on and start cards, nothing about projects, tasks, goals or
runners. Without a runner of theirs online, **Chat** says **Install a runner to use Claude here**
and links to the setup steps. See [A planner's runner](several-computers.md#a-planners-runner).

## Task chat: Ask Claude or Rework

A switch above the task chat's message box says where your message goes. The send button's label
follows it. Hover over either for a reminder of where it sends.

- **Ask Claude**: a side conversation about the task. Claude sees the task's instructions and
  changes, so it can help you sharpen a draft into a better prompt. The agent working on the task
  doesn't see this chat. The button says **Ask**, and **Reply** once Claude
  has answered in it.
- **Rework**: sends the message (and any pictures or files attached to it) to the agent as
  rework notes. It continues in the task's worktree. The button says **Rework**; with an
  empty box it opens the **Rework** sheet. Only for a task in review or failed that has a worktree.
  Comments waiting on the diff's lines go along, before the message (see
  [Comment on lines](tasks.md#comment-on-lines)).

<kbd>Enter</kbd> sends either way. Opening another task switches back to **Ask Claude**.

## Your own project chats

Each member has their own chat about each project. Other members don't see it, and neither do
the organization's owners and admins.

Your project chat is the same on each of your devices and browser windows:

- What one device sends and Claude's reply show on the others when the reply is done. While
  Claude answers, the others say **Claude is answering on another of your devices…**.
- A message sent from a window that hasn't caught up yet isn't sent: the chat reloads, and your
  message goes back in the box to send again. A window that is behind never removes messages.

In the composer, <kbd>Enter</kbd> sends, messages written while Claude answers are queued, `/clear`
starts a new chat (as **New chat** does) and `/help` lists these. In a task's chat, `/clear` asks
first: a task keeps no past chats, so its chat is deleted for everyone who sees the task.

Each task keeps what you were writing in its chat, and its mode (**Ask** or **Rework**), while you
look at another task.

<kbd>Up</kbd> on the first line of the message box shows the message you sent before it, as a
shell's history does; <kbd>Down</kbd> on the last line goes forward again, back to what you were
writing. Typing stops the stepping, so an edited message stays as you left it.

To change the last message you sent, press the pencil beside it: it goes back in the box, and
sending it replaces that message and Claude's answer. <kbd>Esc</kbd> or **×** keeps them as they
were. **Retry** under Claude's last answer asks for it again. A goal's chat has neither: it is
what its coordinator remembers.

The paperclip beside the box attaches files; you can also paste or drop them. When you've scrolled
up, **↓** over the end of the conversation takes you back to the latest message.

## New chat and past chats

**New chat** (the pencil in the chat pane's header) starts an empty conversation in General or a
project's chat. The one it replaces is kept as a past chat.

The clock beside it lists that chat's past chats, newest first:

- Click one to open it. The conversation you had open becomes a past chat in its place.
- Click its trash icon to delete it. **Undo** in the message that follows brings it back.

Past chats are yours, like the chats themselves. A goal's chat has no **New chat**: its
conversation is what its coordinator remembers.

Tool calls Claude made (`▸ ✓ Bash …`) are saved with its answer, so they still show after a
reload. Long input and output is cut short in the saved copy.

## Suggested next message

After each reply, Dash guesses what you'll say next and shows it greyed out in the empty message
box, as Claude Code's CLI does. The guess answers Claude's last message: if Claude asked a question,
it answers it; if Claude offered something, it accepts or turns it down. It never repeats your last
message or asks for something Claude already did, such as a task it just created or queued.

- <kbd>Enter</kbd> or **Send** sends the suggestion as it is.
- <kbd>Tab</kbd> puts it in the box, so you can edit it before sending. A small *Tab to edit*
  cue shows above the box while there is a suggestion.
- Typing hides it. Erase what you wrote and it shows again.
- Sending or a new reply clears it. No suggestion shows when no reply clearly fits, for example
  after a finished answer with no question or offer.

The guess is a short call to Haiku with the last few messages, on the same Claude login as the
chat (on a hub, your own runner). If it fails or no runner of yours is online, the box keeps its
usual placeholder.

To turn it off, open **Account → General** and clear **Suggest what to say next**. It's on by default
and applies to this browser only. When it's off, no call is made.

## Code in the chat

A code block in a reply is coloured by its language, as in the prompt editor, when the block names
one (```` ```go ````, ```` ```sh ````, ```` ```sql ````, ```` ```diff ````). Task prompts and goals
colour their code blocks the same way. While a reply is coming in, a block is coloured once it is
complete. A block with no language, a language Dash doesn't know, or more than 100 KB of code
stays plain. The copy button on a block copies the plain code.

## Pictures in the chat

Claude can show you a picture in its reply. Ask it to draw something (a diagram, a sketch, an
icon) or to show an image it made.

- **A drawing**: Claude writes the SVG in a code block marked `svg`. The reply shows the picture.
  **Code** under it shows the SVG, with a copy button. While the reply is still coming in, the
  block shows as code.
- **An image file**: Claude writes a PNG, JPEG, GIF, WebP or SVG file of at most 10 MB, and calls
  the `show_image` tool with its path. The image shows under the reply's text and is saved with
  the conversation. The file is read on the computer the chat runs on (on a hub, your runner),
  and stored like a file you attach. A read-only chat can't write files, so it can only show
  images that are already there.

Pictures fit the reply's width and keep their shape. Click one to see it full size.

An SVG never runs in the page: it shows as an image, so scripts in it don't run and links to
other files don't load.

## Speak instead of typing

Turn on **Account → Voice** for a mic button on the message box. See
[Dictation](dictation.md).

To hear Claude's replies read aloud, turn on **Account → Voice**. See
[Spoken replies](dictation.md#spoken-replies).

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
  the runner the chat runs on, never on the hub: the runner that works on the project, or the one
  that ran the task.
- **What you see**: the command streams its output (stdout and stderr together) into a block in
  the chat, with a Stop button (the composer's send button) while it runs and its exit code when
  it ends. While its text starts with `!`, the composer shows a `$` and an accent border.
- **Claude sees it**: the command and its output are saved in the conversation as your message,
  so on your next message Claude reads them as a command you ran yourself (not one it ran).
  Long output keeps its first and last 16 KB, with a note of how much was left out between them;
  that is what is saved and what Claude reads.
- **Timeout**: a command that runs longer than 5 minutes is stopped, with everything it started.
  The runner's `-chat-command-timeout` flag changes that (e.g. `-chat-command-timeout 15m`).
  Stop does the same at once. What it printed until then is kept.
- **No terminal**: commands run without a TTY and without input, with `$SHELL -c` (else
  `/bin/sh`), `TERM=dumb` and no pager. Interactive programs (vim, top, less, `ssh` asking for a
  password, …) fail, or wait until the timeout. Use **Code → Shell** (a real terminal) for
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
