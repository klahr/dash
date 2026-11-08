# Run Dash as a service

To keep Dash running in the background and start it at boot, run it as a systemd user service.
The steps are at the top of [`deploy/dash.service`](../deploy/dash.service). In short: put your
credentials in `~/.config/dash/env`, copy the unit to `~/.config/systemd/user/`, run
`loginctl enable-linger "$USER"`, then `systemctl --user enable --now dash`. Read the logs with
`journalctl --user -u dash -f`.

It's a *user* service, not a system one: Dash runs `git` as you, so it needs your home directory
and your `PATH`. The hub never runs `claude` and needs no Claude Code installed: Claude runs on
[runners](several-computers.md), and a hub needs at least one. Start `dash runner` on a computer
with git and Claude Code logged in; [`dash runner install`](several-computers.md) keeps a runner
running as a service the same way. `gh` is only
needed for optional features: pull requests (the default for GitHub projects; without `gh` they
merge on the default branch instead), GitHub repositories (create, clone,
delete) and releases, and CI runs; it runs as you too, with your `gh auth` login.

The unit assumes the repository is checked out at `~/dev/github/klahr/dash`. If yours is
elsewhere, edit `WorkingDirectory` and `ExecStart` in your copy of the unit. Extra flags
(`-trust-proxy`, `-public-url`, `-name`, …) go on the `ExecStart` line; secrets stay in
the env file.

After pulling changes: rebuild, then `systemctl --user restart dash`.
