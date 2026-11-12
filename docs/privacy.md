# Your data: export and account deletion

Everyone can download what Dash keeps about them and delete their own account. A platform admin
can delete someone else's. This covers the rights of access, portability and erasure (GDPR
articles 15, 17 and 20) on a hub with open signup.

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
  -H 'Content-Type: application/json' -b 'dash_session=…' -d '{"password":"…"}'
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
