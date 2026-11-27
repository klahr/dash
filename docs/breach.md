# Responding to a breach

If someone may have a copy of Dash's database (a leaked `dash.db` or backup), or got into an admin
account, end every way in at once. A platform admin does it in the app, or the server's operator
does it on the server, even with the server stopped.

## What it does

Each step can be turned on or off. All are on by default.

- **Sign out everyone.** Ends every session and deletes every API token, connected app (such as a
  claude.ai connector) and runner token. Cancels pending invitations, sign-up and password reset
  links, runner registration codes and wall view links.
- **Ask for new passwords.** Everyone with a password must choose a new one at their next sign-in.
  Until then the sign-in only lets them do that, and their sessions and tokens are refused. The
  new password follows the usual rules and can't be the old one. Accounts that sign in only with
  Google aren't asked. The account whose password is `DASH_PASSWORD` isn't either: change it on
  the server.
- **Reset two-factor sign-in.** Turns off two-factor sign-in for everyone who has it, and deletes
  their recovery codes. They set it up again, with a new secret, at their next sign-in.
- **Rotate secrets.** Makes new secrets for notification webhooks, board backends' webhooks and
  GitHub webhooks, and a new Web Push (VAPID) key. Clears what Dash can't make anew: webhook
  access tokens and board backend credentials. Clears push subscriptions: people turn
  notifications on again. The new secrets are encrypted with the secret key, like the old ones.
- **Tell everyone.** You write a message. Dash emails it to every active account, a few a second,
  and shows how many were sent and failed. It also shows it as a banner after sign-in, until each
  person dismisses it or you clear it. Without email set up, Dash says so: download the names and
  addresses as CSV and send it yourself.

Every step goes in the sign-in log: who ran it, when, and how many of each it touched.

## In the app

Open **Admin → Incident response** (platform admins only). Choose the steps, write the message,
read what will happen, type `LOCKDOWN` and your password, and select **Run incident response**.

You stay signed in in that browser. Your other sessions end. Your own account is left out of the
password and two-factor steps, so you can read the checklist. Afterwards, sign out and choose a
new password with **Forgot password?** (or ask another platform admin for a reset link), and turn
two-factor sign-in off and on again under **Account → Sign-in**.

**Clear it** under **Admin → Incident response** stops showing the message.

## On the server

Use this when the web app can't be trusted. Stop the server first, then run as the user that runs
it:

```sh
dash lockdown -data /var/lib/dash --all --notice-file notice.txt
```

The steps one by one:

```sh
dash lockdown --sessions --passwords --2fa --rotate-secrets --notice-file notice.txt
```

- `--yes` skips typing `LOCKDOWN`.
- `--no-email` or `--no-banner` leaves out one half of the message.
- `--csv users.csv` writes the active accounts' names and email addresses.
- `-env` is the file with `DASH_SECRET_KEY` and `DASH_SMTP_*`, as for the server (default
  `.env`).

It leaves nobody out, and needs the server's secret key: `DASH_SECRET_KEY`, or `secret.key` in
the data directory. It never makes a new one. It emails the message only with `DASH_SMTP_HOST`,
`DASH_SMTP_FROM` and `DASH_PUBLIC_URL` set. It prints what it did and the checklist below, with
the new secrets. Start the server again when it's done.

## Outside Dash

Dash shows (or prints) a checklist of what's left. New secrets show only then: copy them.

- **Notification webhooks:** put each new signing secret in its receiver. A Slack webhook URL is
  itself a secret: make a new one in Slack, delete the old one and enter the new URL. For a
  webhook whose access token was cleared, revoke the old token at the receiving service and enter
  a new one in the webhook's settings.
- **GitHub webhooks:** Dash registers each again with a new secret at its next check, through a
  runner of the project. Runners must be registered again first. If a project's GitHub status
  says it couldn't, delete the webhook under the repository's **Settings → Webhooks** and Dash
  adds it again.
- **Board backends:** revoke their credentials at the tracker, make new ones and enter them in
  **Project settings → Board**. Put the new webhook secret in the tracker's webhook.
- **Runners:** make a registration code under **Runners** and register each again:

  ```sh
  dash runner setup --code <code>
  ```

- **API tokens and connected apps:** their owners make new ones and connect their apps again.
- **Wall view links and invitations:** make them again.
- **`DASH_PASSWORD`:** set a new one on the server and restart.

### If the secret key leaked too

If `DASH_SECRET_KEY` or `secret.key` may have leaked with the database, replace it before you
rotate secrets:

1. Stop the server and make a new key:

   ```sh
   openssl rand -hex 32
   ```

2. Set it as `DASH_SECRET_KEY` (or write it to `<data>/secret.key`).
3. Run `dash lockdown --all` with the new key. The secrets encrypted with the old key are
   replaced or cleared, and two-factor sign-in is reset.
4. Make new client secrets for the Google sign-in clients in the checklist, and enter them again.
5. Start the server.

Secrets kept outside the database, such as `DASH_SMTP_PASSWORD` and the Stripe keys, aren't
touched: change them where they are set if they may have leaked.

See [Secrets are encrypted](privacy.md#secrets-are-encrypted) for what the key protects.
