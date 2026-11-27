# Sandbox Claude's runs

A runner starts Claude Code as you by default, so Claude's tools can read and write whatever
you can: your SSH keys, your `gh` login, your other projects. To give Claude less than that, run
the runner with `-claude-sandbox`: a command that Dash starts in the task's worktree with
`claude` and its flags as arguments. It runs Claude for tasks and scans. Chats and titles still
run Claude as you.

Dash ships three such commands in [`deploy/sandbox`](../deploy/sandbox). They are in the release
archives, and the install script puts them next to `dash` (`~/.local/bin`); `dash runner update`
keeps them up to date there.

- `claude-bwrap` runs Claude in a [bubblewrap](https://github.com/containers/bubblewrap)
  container. Linux only, no root needed.
- `claude-seatbelt` runs Claude under macOS's `sandbox-exec`. macOS only, no root needed.
- `claude-as-user` runs Claude as another user, with `sudo`. Linux and macOS.

Any of them can be a starting point for your own (Docker, Podman, firejail, a VM). Dash passes
the run's environment to the command, with `DASH_WORKTREE` set to the worktree. The command
must keep Claude's standard input and output, run it in the worktree and exit with its status.

## Turn it on

`dash runner setup` tells you when Claude would run unsandboxed and a bundled script fits the
computer, with the command that turns it on. It never turns it on by itself. The runner's
**Doctor** (**Runners** → the runner) shows the same as a yellow **Sandbox** check.

The command adds `DASH_CLAUDE_SANDBOX` to `runner.env` in the runner's data directory, which
the runner reads each time it starts, under systemd and launchd alike. Then it restarts the
background service. On macOS:

```sh
echo "DASH_CLAUDE_SANDBOX=$HOME/.local/bin/claude-seatbelt" >> ~/.local/share/dash-runner/runner.env
dash runner install
```

On Linux, use `claude-bwrap` instead. Check that it took:

```sh
dash runner status                              # "runner.env sets DASH_CLAUDE_SANDBOX"
grep sandbox ~/Library/Logs/dash-runner.log     # macOS: "Claude runs tasks and scans in a sandbox"
journalctl --user -u dash-runner | grep sandbox # Linux
```

A runner you start by hand takes the flag as well:

```sh
dash runner -claude-sandbox ~/.local/bin/claude-seatbelt
```

On Linux you can also set it in a systemd drop-in (`systemctl --user edit dash-runner`), which a
reinstall keeps:

```ini
[Service]
Environment=DASH_CLAUDE_SANDBOX=%h/.local/bin/claude-bwrap
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
folders it may only read in `DASH_SANDBOX_RO` (space separated, full paths: `runner.env`
doesn't expand `$HOME`), then restart the runner:

```sh
echo "DASH_SANDBOX_BIND=$HOME/.cache $HOME/go" >> ~/.local/share/dash-runner/runner.env
dash runner install
```

## macOS (sandbox-exec)

`claude-seatbelt` runs Claude under `sandbox-exec` with a profile of its own. Apple marks
`sandbox-exec` as deprecated, but it still ships with macOS, and Claude Code's own sandbox uses
it too. Nothing to install. Under the profile:

- Claude can write only in the task's worktree, a temp folder of the run's own (`TMPDIR`), `/tmp`
  and its own config folder;
- it can't read `~/.ssh`, `~/.aws`, `~/.config/gh`, `~/.gnupg`, `~/.docker`, `~/.kube`, `~/.npmrc`,
  `~/.netrc`, your keychains (files and the keychain service), Dash's data folder (other tasks'
  worktrees, its database, the runner's login), the folder of your projects, your own
  `~/.claude`, or `~/Library/Application Support`, `Cookies`, `Mail` and `Messages`. The rest
  of the system can be read, as without a sandbox: list more to hide in `DASH_SANDBOX_HIDE`;
- the worktree's `.git` file and the project's `.git` can only be read, so Claude can't move
  branches, commit, or point git at a config of its own;
- it can't reach the ssh, gpg or Docker agents, the clipboard, or other apps (AppleEvents,
  `open`), which would run outside the sandbox;
- Dash's own variables and anything named like a token, password or secret are removed from the
  environment;
- the network is the same as yours.

Claude Code uses its own config folder, as with bubblewrap: your settings, `CLAUDE.md`, rules,
agents, commands and skills are linked in to read, and `settings.json` is copied in fresh each
run.

Your Claude login is in the keychain, which the sandbox can't reach. The script reads Claude's
current access token from it before each run and hands that to Claude, never the refresh token,
so your own Claude stays logged in. If the token has expired (your own Claude hasn't run for
hours), run `claude` once. For a login that doesn't depend on yours, make a long-lived token
and give it to the runner:

```sh
claude setup-token     # prints a token
echo 'CLAUDE_CODE_OAUTH_TOKEN=<the token>' >> ~/.local/share/dash-runner/runner.env
dash runner install
```

Builds can't write their caches in your home folder (`~/Library/Caches`, `~/go`). Go's and npm's
caches go to the run's temp folder unless you set `DASH_SANDBOX_BIND`, e.g.:

```sh
echo "DASH_SANDBOX_BIND=$HOME/Library/Caches/go-build $HOME/go" >> ~/.local/share/dash-runner/runner.env
dash runner install
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

Then turn it on as above, with `claude-as-user`:

```sh
echo "DASH_CLAUDE_SANDBOX=$HOME/.local/bin/claude-as-user" >> ~/.local/share/dash-runner/runner.env
dash runner install
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
