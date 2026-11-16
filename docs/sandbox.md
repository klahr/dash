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

- the system is read-only, and your home folder is empty except for Claude Code itself, its
  settings and login (`~/.claude`, `~/.claude.json`) and your git config;
- the task's worktree can be written. The project's `.git` can only be read, except the
  worktree's own index, so Claude can't move branches or commit;
- the task's attachments can be read;
- `/run` is empty (no Docker socket, no ssh or gpg agent), and Dash's own variables and anything
  named like a token, password or secret are removed from the environment. Claude's
  (`ANTHROPIC_*`, `CLAUDE_*`) stay;
- the network is the same as yours: Claude reaches its API, Dash's MCP servers on the runner,
  and package registries.

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
name), with its own Claude Code login. It shares a group with you, so you can both write the
worktrees. Make the user and let yourself run commands as it without a password:

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
git config --global core.sharedRepository group
exit
```

Give your group the projects and Dash's worktrees, and keep what git and the runner make there
group-writable:

```sh
git config --global core.sharedRepository group
chmod g+x ~ ~/.local ~/.local/share ~/.local/share/dash-runner
chmod -R g+rwX ~/dash-projects ~/.local/share/dash-runner/worktrees
chmod -R g+rX ~/.local/share/dash-runner/uploads
find ~/dash-projects ~/.local/share/dash-runner/worktrees -type d -exec chmod g+s {} +
```

Then turn it on as above, with `UMask=0002` in the drop-in as well:

```ini
[Service]
UMask=0002
Environment=DASH_CLAUDE_SANDBOX=%h/.local/bin/claude-as-user
```

Claude can then read only what your group, or everyone, can read. SSH keys, the `gh` login and
Claude's own login are yours alone (mode 600); check other secrets in your home folder the same
way. Unlike the container, this user can write anywhere in the projects folder.

## Limits

- A sandbox limits what Claude's tools reach on the computer, not the network. A task can still
  send what it can read to the internet.
- The worktree itself is Claude's: what it writes there is committed for review, as without a
  sandbox. Review the changes before merging.
- Commands in a project's **Project settings** (setup, tests, previews) run as you, outside the
  sandbox.
