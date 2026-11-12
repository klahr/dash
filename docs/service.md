# Run Dash as a service

To keep Dash running in the background and start it at boot, run it as a systemd user service,
with [`deploy/dash.service`](../deploy/dash.service). From `~/dash` (the release unpacked there,
or the repository built there, see the [README](../README.md#quick-start)):

```sh
mkdir -p ~/.config/dash && cp .env.example ~/.config/dash/env
chmod 600 ~/.config/dash/env && ${EDITOR:-nano} ~/.config/dash/env   # DASH_USERNAME, DASH_PASSWORD, ...
mkdir -p ~/.config/systemd/user && cp deploy/dash.service ~/.config/systemd/user/
loginctl enable-linger "$USER"    # keep running after you log out
systemctl --user daemon-reload
systemctl --user enable --now dash
journalctl --user -u dash -f      # the log
```

The unit starts Dash with `-trust-proxy`, for a hub behind a proxy on the same computer
([Deploy the hub](deploy.md)).

Run it as an ordinary user rather than root. On a fresh server, as root, make one and give it
your SSH key:

```sh
adduser --disabled-password --gecos "" dash
cp -r ~/.ssh /home/dash/ && chown -R dash:dash /home/dash/.ssh
```

Then log in as that user over SSH (`ssh dash@dash.example.com`) for the steps above. `su dash`
isn't enough: it starts no user session, so `systemctl --user` can't connect.

It's a *user* service, not a system one, so the files it keeps (its database and logs) are in
your home directory. The hub runs nothing on its own computer: it needs no Claude Code, no `git`
and no `gh`, and keeps no projects on its own disk. Tasks, chats, git, `gh` and the projects'
commands run on [runners](several-computers.md), and a hub needs at least one: start `dash runner`
on a computer with git and Claude Code logged in (and `gh` for pull requests, GitHub repositories,
releases and CI runs). [`dash runner install`](several-computers.md) keeps a runner running as a
user service the same way; a runner runs `git` and `gh` as you, with your `PATH` and `gh auth`
login.

The unit assumes Dash is in `~/dash`, with the binary at `~/dash/bin/dash`. If yours is
elsewhere, edit `WorkingDirectory` and `ExecStart` in your copy of the unit. Extra flags
(`-trust-proxy`, `-public-url`, `-name`, …) go on the `ExecStart` line; secrets stay in
the env file.

After pulling changes: rebuild, then `systemctl --user restart dash`.

## Back up and restore

The hub keeps its state in `~/.local/share/dash` (the `-data` directory): the SQLite database
`dash.db`, which is what matters, plus sessions, task logs and uploads. A runner keeps its own in
`~/.local/share/dash-runner`; the commands below take `-data` for it.

**Scheduled backups.** Set a backup directory in `~/.config/dash/env` and restart the service:

```sh
DASH_BACKUP_DIR=backups     # relative to the data directory, or an absolute path
DASH_BACKUP_KEEP=7          # the newest 7 are kept; 0 keeps all
DASH_BACKUP_EVERY=24h
```

(or `-backup-dir`, `-backup-keep` and `-backup-every` on the `ExecStart` line). Dash then writes
a consistent copy of the database, `dash-<UTC time>.db`, every day, and removes older ones beyond
the newest 7. The first is written at start unless one younger than the interval is already
there, so restarts don't push older backups out. `journalctl --user -u dash` shows
`backup written` for each, or why one failed. Point it at another disk, or copy the directory
off the computer, to survive losing this one.

**A backup now**, while Dash runs:

```sh
bin/dash backup ~/dash-backups/          # writes ~/dash-backups/dash-<UTC time>.db
bin/dash backup -keep 14 ~/dash-backups/ # ... and keeps only the newest 14 there
bin/dash backup ~/dash-before-upgrade.db # or to a file of your choice (it must not exist)
```

It uses SQLite's `VACUUM INTO`: a single consistent snapshot, taken without stopping Dash or
blocking its writes, and checked before it is given its name. It is safe to run from cron or a
systemd timer instead of `DASH_BACKUP_DIR`. The copy holds password hashes and tokens, so it is
readable only by you; keep it that way.

**Restore.** Stop Dash, put the backup in place as `dash.db`, and remove the old database's
write-ahead log (`dash.db-wal`, `dash.db-shm`): left beside a different database, SQLite could
apply it to the restored one.

```sh
systemctl --user stop dash
cd ~/.local/share/dash
mv dash.db dash.db.before-restore
rm -f dash.db-wal dash.db-shm
cp backups/dash-20260131T030000Z.db dash.db
systemctl --user start dash
```

A backup from an older version is migrated at start; one from a newer version is refused, so
restore with the version that took it or a newer one. Delete `dash.db.before-restore` once all is
well.

**Upgrades.** Before a new version migrates the database, Dash copies it to
`dash.db.pre-migration-<from>-to-<to>-<UTC time>` beside it. Migrations only go forward, so that
copy (or your own backup) is the way back to the older version. See
[Upgrades, restarts and rollback](deploy.md#upgrades-restarts-and-rollback) for the whole
procedure and what happens to running tasks.

## Disk use

Each task's log is capped: a log file past 32 MB is cut to its newest half, behind a note that
earlier output was trimmed (`-log-max-mb`; 0 = no cap). The daily cleanup (`-cleanup-days`,
14 by default) removes the logs of deleted tasks, the worktrees of tasks that failed that many
days ago, and orphaned worktrees, branches and uploads.
