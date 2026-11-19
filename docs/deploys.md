# Deploys

Your own scripts do the deploying. Dash runs them on a runner, tells them what they deploy, and
keeps a record of each deploy, so you can see what is live where.

## Environments

An environment is a place the project is deployed to, such as `production` or `staging`. Add
them in **Project settings → Deploy**. Each has:

- **Name**: a lowercase word (`a-z`, `0-9`, `-`, `_`), up to 32 characters.
- **Deploy command**: a bash script, one line or several.
- **Build command** (optional): runs first. If it fails, the deploy stops there.
- **URL** (optional): where the environment is reached. Its health is checked once the deploy
  command is done.
- **Runs on** (on a hub): the runner labels its deploys run on. Empty runs on any runner the
  project runs on. See [Where deploys run](several-computers.md#where-deploys-run).
- **Checks** (optional): what decides that a deploy is live. See [Checks](#checks).

The environments are settings kept by Dash. Nothing goes in the repository.

A project that had a **Deploy command** before environments now has one environment,
`production`, with that command and its **Deploy runs on** labels.

## Deploy

Open **Deploys** in the sidebar (`gd`). Each environment has a card:

- what is live there, since when, and its version;
- how far it is ahead of the others ("staging is 3 commits ahead of production");
- a deploy that is running or queued;
- on a hub, the runners its deploys may run on, and a warning when none of them can, or none has
  its secrets file.

Press **Deploy** on a card. Dash deploys the tip of the default branch, as origin has it.
**Deploy again** in a deploy's pane deploys that deploy's commit again.

One deploy runs at a time per environment. The others wait in the queue in order.

Below the cards are the deploys, newest first. A deploy opens in a pane beside the list (full
screen with **Back** on a phone; `Esc` closes it, `j`/`k` step through the list). The pane shows:

- the commit, and the deploy it replaced;
- who started it, the runner it ran on, its exit code;
- its phases and how long each took: **Queued**, **Building**, **Deploying**, **Verifying**,
  then **Live** or **Failed**. A live deploy becomes **Replaced** when the next one goes live, or
  **Rolled back** when a rollback replaces it;
- how its checks went;
- the tasks and Planning cards it shipped: those the commits since the previous deploy name;
- its log, with secrets masked.

**Stop** stops a running deploy where it runs. **Cancel** takes a queued one off the queue.

## Checks

The deploy command exiting with 0 means the deploy is *deployed*. The checks then decide whether
it is *live*. While they run, the deploy is **Verifying**. If one fails, the deploy is **Failed**,
though the environment may now run it. Set the checks in **Project settings → Deploy**, under
**Checks**:

- **Health check path**: the path on the environment's URL to ask for, such as `/healthz`. Empty
  asks for the URL itself. The check needs a URL.
- **Expected status**: the HTTP status it wants, such as `200`. Empty passes any status below 500.
- **Timeout**: how long, in seconds, the check keeps trying while the deploy comes up. Empty is 60.
- **Verify command**: smoke tests, a bash script run after the health check, in the same checkout.
  It gets the URL as `$DASH_URL`. Exit code 0 passes.
- **Soak time**: how long, in seconds, to watch a deploy that passed before it is live. Its health
  is checked again every 15 seconds. Empty: no soak.
- **Roll back on its own when the checks fail**: see [Roll back](#roll-back).

A verify command, for example:

```sh
curl -fsS "$DASH_URL/api/status"
npm run smoke -- --base-url "$DASH_URL"
```

The checks run in that order: health check, verify command, soak. The first that fails fails the
deploy. With no URL, verify command or soak time, a deploy is live once its deploy command exits
with 0.

## Roll back

A card shows **Roll back to <commit>** when the environment has a good deploy to go back to. So
does the pane of the deploy it would replace. Dash picks the last good deploy: the newest one
before it that went live, of a commit that was never rolled back, and of another commit than the
one it replaces.

It replaces what the environment may be running now: the live deploy, or a newer one that failed
its checks.

A rollback is a deploy. It runs the environment's commands on the old commit, with
`DASH_ROLLBACK=1` and the old deploy's artifact in `DASH_ARTIFACT`. It goes ahead of queued
deploys, and is checked like any other. Once it is live, the deploy it replaced is **Rolled back**.

Tick **Roll back on its own when the checks fail** to have Dash roll back as soon as a deploy fails
its checks. A deploy whose build or deploy command fails isn't rolled back, nor is a rollback.

## When a deploy fails

Dash sends a `deploy.failed` notification, to the browsers and webhooks that want it (see
[Notifications](notifications.md)). A deploy you cancel or stop doesn't notify.

A deploy whose scripts ran also gets a draft task, **Fix failed deploy to <environment>**. Its
prompt says why the deploy failed and which checks failed, with the end of the log. The whole log
is attached, with secrets masked. The deploy's pane links to it. While that task is still a draft,
the next failure in the same environment updates it, instead of adding another. Queue the task when
you want Claude to look into it.

## Where it runs

On a hub, a deploy runs on a runner of the project with the environment's labels, for the person
who started it: one of their own runners, or a shared one. A Dash that isn't a hub runs it on its
own computer.

There, Dash keeps a checkout of the project for each environment, under its data folder
(`deploys/<project>/<environment>`). Each deploy checks out the commit in it, then runs the build
and deploy commands there. Files git ignores (installed dependencies, build caches) stay between
deploys. The project's own checkout is never touched.

## The script contract

The build and deploy commands get these environment variables:

| Variable           | Value                                                        |
|--------------------|--------------------------------------------------------------|
| `DASH_ENV`         | the environment's name                                       |
| `DASH_COMMIT`      | the commit deployed                                          |
| `DASH_PREV_COMMIT` | the commit live before (empty for the first deploy)          |
| `DASH_ARTIFACT`    | the artifact to deploy again, on a rollback; else empty      |
| `DASH_DEPLOY_ID`   | the deploy's id                                              |
| `DASH_ROLLBACK`    | `1` on a rollback; unset otherwise                           |
| `DASH_URL`         | the environment's URL (the verify command: the deploy's)     |
| `DASH_OUTPUT`      | a file the script may write results to                       |

A script may write `key=value` lines to `$DASH_OUTPUT`. Dash keeps these:

```sh
echo "url=https://staging.example.com" >> "$DASH_OUTPUT"
echo "version=1.4.2" >> "$DASH_OUTPUT"
echo "artifact=web-1.4.2.tar.gz" >> "$DASH_OUTPUT"
```

`url` replaces the environment's URL for this deploy (and is the one checked). A script that
ignores all of this still works.

## Secrets

Secrets stay on the runners. Put an environment's secrets in a file on each runner that deploys
it:

```sh
mkdir -p ~/.dash/env
chmod 700 ~/.dash/env
cat > ~/.dash/env/production.env <<'EOF'
API_TOKEN=…
DATABASE_URL=postgres://…
EOF
chmod 600 ~/.dash/env/production.env
```

One `KEY=VALUE` per line. `export ` before a key and quotes around a value are allowed; blank
lines and `#` comments are skipped. The file is named after the environment.

The build and deploy commands get these as environment variables. The runner masks their values
(those of 4 characters or more) in the log before it sends it to the hub.

The hub never sees the values. A runner only tells it which environments it has a file for, so
**Project settings → Deploy** and the Deploys page can warn, for example: "No runner with prod has
a production env file".

## API

- `GET /api/projects/{id}/environments`: each environment with what is live, what runs, how far
  it is ahead of the others, and (on a hub) its runners.
- `GET /api/projects/{id}/deploys?environment=`: the deploys, newest first, without logs.
- `POST /api/projects/{id}/deploys` with `{"environment": "production", "commit": ""}`: queue a
  deploy. An empty `commit` deploys the tip of the default branch.
- `GET /api/deploys/{id}`: one deploy, with its log.
- `DELETE /api/deploys/{id}`: cancel a queued deploy, or stop a running one.
- `POST /api/deploys/{id}/rollback`: roll back deploy `{id}` to the last good deploy before it.
  `GET /api/projects/{id}/environments` has the rollback each environment allows now in `rollback`
  (`{"of", "to"}`), and `GET /api/deploys/{id}` in `rollback_target`.

The environments are the `environments` field of `PATCH /api/projects/{id}` and of the
`update_project` tool: a list of `{"name", "deploy_command", "build_command", "runs_on", "url",
"health_path", "health_status", "health_timeout", "verify_command", "soak", "auto_rollback"}`,
which replaces the project's environments.
