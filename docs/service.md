# Run Dash as a service

To keep Dash running in the background and start it at boot, run it as a systemd user service.
The steps are at the top of [`deploy/dash.service`](../deploy/dash.service). In short: put your
credentials in `~/.config/dash/env`, copy the unit to `~/.config/systemd/user/`, run
`loginctl enable-linger "$USER"`, then `systemctl --user enable --now dash`. Read the logs with
`journalctl --user -u dash -f`.

It's a *user* service, not a system one: Dash runs `claude`, `git` and `gh` as you, so it needs
your home directory, your `~/.claude` login and your `PATH`. (A hub with `-hub-claude off`, or
with several users or runners, never runs `claude`; see
[several computers](several-computers.md#claude-runs-on-each-users-own-runner).)

The unit assumes the repository is checked out at `~/dev/github/klahr/dash`. If yours is
elsewhere, edit `WorkingDirectory` and `ExecStart` in your copy of the unit. Extra flags
(`-trust-proxy`, `-public-url`, `-name`, …) go on the `ExecStart` line; secrets stay in
the env file.

After pulling changes: rebuild, then `systemctl --user restart dash`.
