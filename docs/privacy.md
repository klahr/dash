# Your data, terms and privacy

If people other than you sign up on your hub, publish three things before you open it:

- **Terms of service:** who runs the hub, what it may be used for, what you promise and what
  you don't, and how either side ends it.
- **A privacy policy:** what you store about people, where, for how long, who else gets it,
  and how they reach you to see or delete it. In the EU (GDPR) this is required.
- **A contact address** people can write to.

They depend on who you are, where you are and what you charge, so Dash can't write them
for you. It ships a starting point for each: [`deploy/legal/terms.html`](../deploy/legal/terms.html)
and [`deploy/legal/privacy.html`](../deploy/legal/privacy.html). The privacy policy already
lists what an unmodified hub stores, why, and for how long (the tables at the end of this
page). Replace every `[bracketed]` part, delete what you don't use, add what your own setup
adds, and have someone who knows your jurisdiction read them.

Let the hub serve them itself, at `/terms` and `/privacy`, without signing in:

```sh
cp deploy/legal/terms.html deploy/legal/privacy.html /etc/dash/   # then edit them there
```

```sh
DASH_TERMS_FILE=/etc/dash/terms.html
DASH_PRIVACY_FILE=/etc/dash/privacy.html
DASH_CONTACT_EMAIL=help@example.com
```

The files are HTML without `<html>` or `<body>`: Dash puts them in a page with your name, logo
and footer. It reads them on every request, so an edit shows without a restart. Scripts in them
don't run. With Docker, copy them into the data volume and point at `/data/terms.html` and
`/data/privacy.html` (see `deploy/docker.env.example`).

Or host them on your own site and link them:

```sh
DASH_TERMS_URL=https://example.com/terms
DASH_PRIVACY_URL=https://example.com/privacy
DASH_CONTACT_EMAIL=help@example.com
```

Set a URL or a file for each, not both. Dash then links all three in every page's footer, the terms and privacy policy under the
signup form ("By creating an account, you accept the terms of service …"), and the contact
address at the end of every email (see [Branding](branding.md)). With
[open signup](signup.md) on and any of the three missing, Dash logs `open signup on without
terms, privacy policy or contact` at every start, naming what's missing.

Everyone can download what Dash keeps about them and delete their own account, and a
platform admin can delete someone else's. This covers the rights of access, portability and
erasure (GDPR articles 15, 17 and 20). The end of this page lists what a Dash hub stores, for
your privacy policy. It describes an unmodified Dash; add what your own setup adds (your
reverse proxy's access logs, your host's backups, your mail provider).

## Download your data

Open **Account → Your data** and click **Download**. You get a JSON file with:

- your account: username, email address, when it was made, whether two-factor sign-in is on
- your organizations and roles
- linked Google accounts
- your sessions, tokens, connected apps and runners
- browsers that get your push notifications, and your personal webhooks
- the sign-in log entries about you, with their IP addresses
- the tasks you created and the chat messages you wrote

Secrets are left out: password and token hashes, the authenticator secret, push keys and webhook
secrets. With a session, the same file is at `GET /api/account/export`.

## Delete your account

Open **Account → Your data** and click **Delete account…**. Type your username, then confirm with
your password. An account that signs in with Google confirms with a Google sign-in from the last
10 minutes instead.

This happens at once:

- Your account goes, with its sessions, tokens, connected apps, linked Google account, recovery
  codes, push subscriptions and personal webhooks. Every browser and connector is signed out.
- Your general chats with Claude are deleted.
- Each organization you are the only member of is deleted, your personal workspace too.
- Tasks and messages you wrote in other organizations stay there, without your name. Those tasks
  can't run again.
- The sign-in log keeps that something happened, without your account, IP address or details.
- Runners you registered stop connecting.

Dash refuses, and says why, while:

- you are the only owner of an organization with other members. Make someone else an owner in
  **Organization** first.
- an organization only you are in still has projects. Delete each with
  **Project menu → Delete project…**.
- an organization only you are in has a subscription. Cancel it in **Organization → Billing**.

The account `DASH_USERNAME` describes can't be deleted: Dash makes it again on every start. The
last platform admin can't be deleted either.

## Delete someone else's account

Platform admins see **Delete account** beside each member in **Organization**. It does what
deleting your own account does, with the same checks. The API:

```sh
curl -X DELETE https://dash.example.com/api/admin/users/42 \
  -H 'Content-Type: application/json' -b '__Host-dash_session=…' -d '{"password":"…"}'
```

## The sign-in log

The sign-in log (Google sign-ins, links, setting changes, members signed out) keeps IP addresses.
Entries older than a year are deleted. Change that with `-auth-log-days` or `DASH_AUTH_LOG_DAYS`
(`0` keeps every entry):

```sh
DASH_AUTH_LOG_DAYS=90
```

## Backups

Backups (`-backup-dir`, `dash backup`) are copies of the database from before a deletion. A
deleted account stays in them until they are removed: with `DASH_BACKUP_KEEP=7` and daily backups,
for a week. Restoring an old backup brings deleted accounts back, so delete them again after a
restore.

## API

| Method and path                         | What                                             |
|-----------------------------------------|--------------------------------------------------|
| `GET /api/account/export`               | your data, as a JSON file                        |
| `DELETE /api/account {password}`        | delete your account; `409` says what is in the way |
| `DELETE /api/admin/users/{uid} {password}` | platform admins: delete someone else's account |

These need a browser session: with a token they answer 403.

## What the hub stores, and where

Everything is on the hub's own disk, in its data directory (`-data`, by default
`~/.local/share/dash`): the database `dash.db` (SQLite) and the files next to it.

| What                                    | Kept                                                                          |
|-----------------------------------------|-------------------------------------------------------------------------------|
| Account: email address, username, password hash (Argon2id), two-factor secret and hashed recovery codes, when it was created | until the account is deleted (see [Delete your account](#delete-your-account)) |
| Unconfirmed signup                      | deleted 24 hours after its last confirmation link expires                     |
| Confirmation and password reset links   | 24 hours and 1 hour                                                           |
| Invitations (the invitee's email)       | 7 days                                                                        |
| Sessions (a hash of the cookie; no IP address or browser) | 7 days                                                      |
| API tokens and connected apps (hashed)  | until they expire or are revoked; a password change ends them                 |
| Google sign-in link (Google's account id and email) | until it is unlinked                                              |
| Sign-in events (Google sign-in, linking, single sign-on, signing members out), with the IP address | a year (`-auth-log-days`) |
| Push subscriptions (the browser's push address and its user agent) | until notifications are turned off on that device  |
| Webhooks (the URL), and their last 50 deliveries | until deleted                                                        |
| Work: projects, tasks, chats, issues, scans, with who created them | until deleted; deleting a task deletes its chat and logs, deleting a project deletes its tasks |
| Logs of Claude's runs (`logs/`)         | with their task; at most `-log-max-mb` (32 MB) a task                         |
| Attachments and drawings (`uploads/`)   | with what uses them; unused ones are removed after a day                      |
| Billing (with [Stripe](billing.md)): the Stripe customer and subscription ids, plan and period | while the org exists. Card, address and tax id stay at Stripe |
| Backups (`dash backup`, `-backup-dir`)  | the last 7 by default (`-keep`). Copies made before an upgrade (`dash.db.pre-migration-*`) are kept until you delete them |

Not stored in the database:

- **IP addresses** for sign-in lockouts and signup rate limits are kept in memory only,
  for the lockout's window (15 minutes) or the limit's (1 hour), and are gone when Dash
  restarts. A lockout and a failed Google sign-in write the IP address to the server's log
  (journald or the container's log), kept as long as that log keeps it.
- **Code and repositories** aren't on the hub. They are on the runners, the users' own
  computers, which run Claude Code with their owners' own Claude accounts.

## Who else gets data

- **Your email provider** (`DASH_SMTP_*`): the addresses Dash writes to, and the links.
- **Stripe**, with billing on: the org's name and its owner's email address.
- **Google**, with Google sign-in on.
- **Browser push services** (Google, Mozilla, Apple), for push notifications: encrypted.
- **Webhooks:** the addresses users add receive their tasks' events.
- **Anthropic:** the hub never calls it. Runners send prompts, code and attachments to
  Anthropic through Claude Code, under their owner's own Claude account and terms.

Dash has no analytics or tracking, and loads no fonts or scripts from other sites.

## Cookies

| Cookie                 | What for                                     | Kept        |
|------------------------|----------------------------------------------|-------------|
| `__Host-dash_session`  | staying signed in (`dash_session` over HTTP) | 7 days      |
| `dash_google_…`, `dash_glink_…` | a Google sign-in in progress        | 10 minutes  |
| `dash_preview`         | opening [live previews](previews.md)         | 12 hours    |

All are needed for the service to work, so no cookie banner is needed for them. The app
keeps its view settings (layout, collapsed panes) in the browser's local storage.
