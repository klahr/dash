# GitHub webhooks

A hub that GitHub can reach doesn't have to poll GitHub for builds. It adds a webhook to each
project's repository, and GitHub tells it when a workflow run starts, changes or ends. Release
builds then show up as published or failed within seconds.

## What Dash does

- When you add a project with a GitHub repository, and when the hub starts, Dash registers a
  webhook on the repository. It sends `workflow_run` and `release` events to
  `https://dash.example.com/api/github/hook?org=ID`, where `ID` is the project's organization.
- Each organization gets its own webhook and secret for a repository. When two organizations have
  projects of the same repository, each one registers its own webhook with its own runners. GitHub
  signs every delivery with the secret, and Dash refuses deliveries it can't verify. Dash makes a
  new secret each time it registers the webhook. The secret stays on the hub and the runner that
  registers it, and shows up in no page, API answer or log.
- Dash never trusts what a delivery says about a run. It reads the run again from GitHub, then
  updates the release build the same way a poll would.
- A run on the default branch makes Dash check the project's builds a few seconds later.
- When a webhook with the same URL is already on the repository, Dash updates it. It doesn't add a second one.
  A webhook from an older Dash, on `https://dash.example.com/api/github/hook` without `?org=`, is updated too.
- When you remove an organization's last project that uses the repository, Dash deletes that
  organization's webhook.

The hub has no `gh` of its own. It registers the webhook with the `gh` of a runner linked to the
project, so a runner must be online. It never uses another organization's runner.

## What it needs

- A public URL that GitHub can reach: start the hub with `-public-url` (or `DASH_PUBLIC_URL`):

  ```sh
  DASH_PUBLIC_URL=https://dash.example.com
  ```

- Admin rights on the repository for the runner's `gh` login. Its token must be allowed to
  manage webhooks (`admin:repo_hook`). To add that scope, run this on the runner:

  ```sh
  gh auth refresh -h github.com -s admin:repo_hook
  ```

## When there is no webhook

Dash polls GitHub as before ([Rate limits](github.md#rate-limits)) when:

- The hub has no public URL.
- The project runs on a laptop runner.
- GitHub refuses the webhook (HTTP 403 or 404: no admin rights, or the token can't manage
  webhooks). Dash tries again every 6 hours.
- Registering fails another way, for example when no runner is online. Dash tries again within 10 minutes.
- GitHub's deliveries don't arrive.

## Polling with a webhook

While a webhook works, Dash still polls, but only as a safety net for lost deliveries and
cancelled runs:

- A release that is building is checked every 3 minutes instead of every 15 seconds.
- A tag that no workflow started for is still marked as such after 3 minutes.
- A deploy [waiting for the release build](deploys.md#wait-for-the-release-build) starts as soon
  as the build passes. Dash checks waiting deploys every 3 minutes instead of every 15 seconds.
- A building release or a running build no longer makes the project poll every minute.

GitHub sends nothing between builds. When a webhook has been quiet for an hour, Dash asks GitHub
to ping it. If no ping or other delivery arrives within 10 minutes, the webhook counts as not
reachable and Dash polls at the normal pace again.

## See its status

Open **Builds** (in the **▾** menu beside **Run**). On a hub with a public URL, the sheet has a
**Webhook** line:

- **active**: GitHub's deliveries arrive.
- **waiting**: registered, with no delivery yet.
- **not reachable**: GitHub's deliveries don't arrive. Check that the public URL is reachable
  from the internet.
- **no permission**: GitHub refused it. The line gives the reason.
- **not registered**: registering failed another way. The line gives the reason.

The repository's **Settings → Webhooks** page on GitHub lists every delivery with its response.
