# Run Dash as a service

To keep Dash running in the background and start it at boot, run it as a systemd user service.
The steps are at the top of [`deploy/dash.service`](../deploy/dash.service). In short: put your
credentials in `~/.config/dash/env`, copy the unit to `~/.config/systemd/user/`, run
`loginctl enable-linger "$USER"`, then `systemctl --user enable --now dash`. Read the logs with
`journalctl --user -u dash -f`.

It's a *user* service, not a system one, so the files it keeps (its database and logs) are in
your home directory. The hub runs nothing on its own computer: it needs no Claude Code, no `git`
and no `gh`, and keeps no projects on its own disk. Tasks, chats, git, `gh` and the projects'
commands run on [runners](several-computers.md), and a hub needs at least one: start `dash runner`
on a computer with git and Claude Code logged in (and `gh` for pull requests, GitHub repositories,
releases and CI runs). [`dash runner install`](several-computers.md) keeps a runner running as a
user service the same way; a runner runs `git` and `gh` as you, with your `PATH` and `gh auth`
login.

The unit assumes the repository is checked out at `~/dev/github/klahr/dash`. If yours is
elsewhere, edit `WorkingDirectory` and `ExecStart` in your copy of the unit. Extra flags
(`-trust-proxy`, `-public-url`, `-name`, …) go on the `ExecStart` line; secrets stay in
the env file.

After pulling changes: rebuild, then `systemctl --user restart dash`.
