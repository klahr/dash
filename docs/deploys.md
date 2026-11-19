# Deploys

Your own scripts do the deploying. Dash runs them on a runner, tells them what they deploy, and
keeps a record of each deploy, so you can see what is live where.

## Environments

An environment is a place the project is deployed to, such as `production` or `staging`. Add
them in **Project settings → Deploy**. Each has:

- **Name**: a lowercase word (`a-z`, `0-9`, `-`, `_`), up to 32 characters.
- **Deploy command**: a bash script, one line or several.
- **Build command** (optional): runs first. If it fails, the deploy stops there.
- **URL** (optional): checked once the deploy command is done.
- **Runs on** (on a hub): the runner labels its deploys run on. Empty runs on any runner the
  project runs on. See [Where deploys run](several-computers.md#where-deploys-run).

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
  then **Live** or **Failed**. A live deploy becomes **Replaced** when the next one goes live;
- the tasks and Planning cards it shipped: those the commits since the previous deploy name;
- its log, with secrets masked.

**Stop** stops a running deploy where it runs. **Cancel** takes a queued one off the queue.

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
| `DASH_OUTPUT`      | a file the script may write results to                       |

A script may write `key=value` lines to `$DASH_OUTPUT`. Dash keeps these:

```sh
echo "url=https://staging.example.com" >> "$DASH_OUTPUT"
echo "version=1.4.2" >> "$DASH_OUTPUT"
echo "artifact=web-1.4.2.tar.gz" >> "$DASH_OUTPUT"
```

`url` replaces the environment's URL for this deploy (and is the one checked). A script that
ignores all of this still works.

The URL check asks the URL for up to a minute. Any answer below 500 passes.

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

The environments are the `environments` field of `PATCH /api/projects/{id}` and of the
`update_project` tool: a list of `{"name", "deploy_command", "build_command", "runs_on", "url"}`,
which replaces the project's environments.
