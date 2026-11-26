# Sandbox Claude's runs

A runner starts Claude Code as you by default, so Claude's tools can read and write whatever
you can: your SSH keys, your `gh` login, your other projects. To give Claude less than that, run
the runner with `-claude-sandbox`: a command that Dash starts in the task's worktree with
`claude` and its flags as arguments. It runs Claude for tasks and scans. Chats and titles still
run Claude as you.

Dash ships two such commands in [`deploy/sandbox`](../deploy/sandbox) (in the release archives
as well):

- `claude-bwrap` runs Claude in a [bubblewrap](https://github.com/containers/bubblewrap)
  container. Linux only, no root needed. This is the easier one.
- `claude-as-user` runs Claude as another user, with `sudo`. Linux and macOS.

Either one can be a starting point for your own (Docker, Podman, firejail, a VM). Dash passes
the run's environment to the command, with `DASH_WORKTREE` set to the worktree. The command
must keep Claude's standard input and output, run it in the worktree and exit with its status.

## Turn it on

Put the script somewhere the runner can run it, e.g. `~/.local/bin`, then start the runner with
the flag:

```sh
dash runner -claude-sandbox ~/.local/bin/claude-bwrap
```

For the background service (`dash runner install`), set `DASH_CLAUDE_SANDBOX` in a systemd
drop-in, which a reinstall keeps:

```sh
systemctl --user edit dash-runner
```

```ini
[Service]
Environment=DASH_CLAUDE_SANDBOX=%h/.local/bin/claude-bwrap
```

```sh
systemctl --user restart dash-runner
journalctl --user -u dash-runner | grep sandbox   # "Claude runs tasks and scans in a sandbox"
```

The flag is a runner's: the hub ignores it. If the command fails to start, the task fails and
its log says why.

## bubblewrap

Install bubblewrap (`sudo apt install bubblewrap`, `sudo pacman -S bubblewrap`,
`sudo dnf install bubblewrap`). Inside the container:

- the system is read-only, and your home folder is empty except for Claude Code itself and your
  git config;
- Claude Code uses a config folder of its own, `~/.local/share/dash-runner/claude-sandbox`
  (set `DASH_SANDBOX_CLAUDE` for another). Your `settings.json`, `CLAUDE.md`, rules, agents,
  commands and skills are there to read, and your login is shared. Your own `~/.claude` and
  `~/.claude.json` are out of reach, so a task can't add hooks or MCP servers that your own
  Claude would run;
- the task's worktree can be written. Its `.git` file and the project's `.git` can only be read,
  so Claude can't move branches, commit, or point git at a config of its own;
- the task's attachments can be read;
- `/run` is empty (no Docker socket, no ssh or gpg agent), and Dash's own variables and anything
  named like a token, password or secret are removed from the environment. Claude's
  (`ANTHROPIC_*`, `CLAUDE_*`) stay;
- the network is the same as yours: Claude reaches its API, Dash's MCP servers on the runner,
  and package registries.

To give the sandbox a login of its own instead of yours, log in to its folder once:

```sh
CLAUDE_CONFIG_DIR=~/.local/share/dash-runner/claude-sandbox claude   # log in, then /exit
```

A build that keeps caches in your home folder (Go, npm, Cargo) starts with an empty one on each
run. Let it keep them by listing the folders Claude may write in `DASH_SANDBOX_BIND`, and
folders it may only read in `DASH_SANDBOX_RO` (space separated):

```ini
[Service]
Environment=DASH_CLAUDE_SANDBOX=%h/.local/bin/claude-bwrap
Environment="DASH_SANDBOX_BIND=%h/.cache %h/go"
```

## Another user

Claude runs as a user of its own (`dash-claude` here; set `DASH_SANDBOX_USER` for another
name), with its own Claude Code login. It shares a group with you, so it can write the files in
a task's worktree, but not the projects' `.git` folders: git runs as you there, and a config or
hook that Claude could write would run as you too. Make the user and let yourself run commands
as it without a password:

```sh
sudo useradd --create-home --shell /bin/bash dash-claude
sudo usermod -aG "$(id -gn)" dash-claude
echo "$USER ALL=(dash-claude) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/dash-claude
sudo chmod 440 /etc/sudoers.d/dash-claude
```

Install Claude Code for it and log in:

```sh
sudo -iu dash-claude
curl -fsSL https://claude.ai/install.sh | bash
claude        # log in, then /exit
git config --global safe.directory '*'
exit
```

Let your group reach the projects, Dash's worktrees and the task's attachments, and read them,
but not write them:

```sh
chmod g+x ~ ~/.local ~/.local/share ~/.local/share/dash-runner
chmod -R g+rX,g-w ~/dash-projects ~/.local/share/dash-runner/worktrees ~/.local/share/dash-runner/uploads
```

Don't set `core.sharedRepository`, `UMask=0002` or `safe.directory '*'` for yourself: they
would make the `.git` folders writable by the group again, or let your git trust a `.git` that
Claude made. If an earlier setup set them, remove them:

```sh
git config --global --unset core.sharedRepository
systemctl --user edit dash-runner   # delete a UMask=0002 line
```

Then turn it on as above:

```ini
[Service]
Environment=DASH_CLAUDE_SANDBOX=%h/.local/bin/claude-as-user
```

Before each run the script gives your group write access to the worktree's files (not its
`.git` file), and takes it away from the project's `.git` folder and the folders that hold it.
It refuses to run if `core.sharedRepository` is set or the `.git` folder has files that aren't
yours.

Claude can then read only what your group, or everyone, can read. SSH keys, the `gh` login and
Claude's own login are yours alone (mode 600); check other secrets in your home folder the same
way. Unlike the container, this user can read every project in the projects folder.

## Scans

With `-claude-sandbox`, the built-in `flaky-tests` scan runs on this runner without approval:
it runs the repository's tests, and the sandbox keeps them away from your credentials. Without
it, the scan needs approval per project (see [scans](scans.md#shell-commands)). Scans that may run
commands also turn on Claude Code's own sandbox inside yours; where that can't start (in a
container without bubblewrap, say), the commands run in your sandbox only.

## Limits

- A sandbox limits what Claude's tools reach on the computer, not the network. A task can still
  send what it can read to the internet.
- The worktree itself is Claude's: what it writes there is committed for review, as without a
  sandbox. Review the changes before merging.
- Commands in a project's **Project settings** (setup, tests, previews) run as you, outside the
  sandbox.
- Dash's own git commands (commit, rebase, merge) run as you, without git hooks or an fsmonitor.
  In a worktree they use the worktree's folder in the project's `.git`, never the folder the
  worktree's `.git` file names. Dash refuses to run git in a worktree whose `.git` no longer
  points to the project's `.git`, and fails a sandboxed run that changed it.
- The sandbox's Claude config folder is kept from run to run, so one run can change it for the
  next (MCP servers in its `.claude.json`). Your own Claude config is never changed.
