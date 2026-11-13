# Open signup and email

Dash is invite-only by default: the first account comes from `DASH_USERNAME` /
`DASH_PASSWORD`, and everyone else joins through an [invitation link](users.md#invitations).
If you host Dash for other people, you can let anyone create an account with their email
address instead. That needs Dash to send email, which also gives everyone
**"Forgot password?"** on the sign-in page.

Nothing here is on unless you configure it. Leave it alone and Dash behaves exactly as
before.

Before you open signup, publish terms of service, a privacy policy and a contact address,
and link them: see [Your data, terms and privacy](privacy.md). Dash warns at every start
while open signup is on without them.

## Email (SMTP)

Dash sends its few emails (confirming an address, resetting a password) through an SMTP
server you already have: your mail provider's submission server, or a transactional email
service. Set, in `.env` or the environment of the service:

| Variable              | Flag          | What                                                            |
|-----------------------|---------------|-----------------------------------------------------------------|
| `DASH_SMTP_HOST`      | `-smtp-host`  | the SMTP server, e.g. `smtp.example.com`                        |
| `DASH_SMTP_PORT`      | `-smtp-port`  | `587` (default; STARTTLS) or `465` (TLS from the start)         |
| `DASH_SMTP_USERNAME`  | `-smtp-user`  | the account to sign in to the server with (optional)            |
| `DASH_SMTP_PASSWORD`  | (none)        | its password; set it in the environment only                    |
| `DASH_SMTP_FROM`      | `-smtp-from`  | the sender, e.g. `Dash <dash@example.com>`                      |
| `DASH_PUBLIC_URL`     | `-public-url` | the address people open Dash at, e.g. `https://dash.example.com` |

The links in the emails always point at `DASH_PUBLIC_URL`, never at whatever host a request
claims to be for, so nobody can get a reset link pointing at their own site. Setting
`DASH_PUBLIC_URL` also turns on the [remote MCP endpoint](mcp.md) for claude.ai connectors.

When the server offers STARTTLS on port 587, Dash uses it, and it only sends a password
over TLS (or to `localhost`). Check that your provider accepts `DASH_SMTP_FROM` as a
sender; most want its domain verified (SPF/DKIM) first, or the emails end up in spam.

On start, Dash logs `email on: password reset by email` once email works, or
`email is off` with the reason (a missing variable, an address it can't parse, no public
URL). Delivery errors are logged as `send email`, without the link.

## Turning signup on

Add one of these and restart:

    DASH_SIGNUP=1          # in .env or the environment
    dash -signup           # or the flag

Signup needs email: if SMTP or the public URL isn't configured, Dash logs
`open signup stays off (invite-only)` with the reason and keeps running invite-only. With
it on, it logs `open signup on`, and the sign-in page shows **Create an account**.

A minimal `.env` for a hosted Dash:

    DASH_USERNAME=admin
    DASH_PASSWORD=a-long-random-password
    DASH_PUBLIC_URL=https://dash.example.com
    DASH_SIGNUP=1
    DASH_SMTP_HOST=smtp.example.com
    DASH_SMTP_USERNAME=dash@example.com
    DASH_SMTP_PASSWORD=…
    DASH_SMTP_FROM="Dash <dash@example.com>"

Remember that Dash runs commands and Claude Code on its runners, as their owners; the hub
itself runs none. Open signup lets strangers sign up, register runners and use your hub:
only turn it on for an install built for it (see [Deploy the hub](deploy.md)).

## What signing up does

1. Someone enters an email address. Dash answers **"If you can sign up with that address,
   we've sent it a link"**, whatever the case:
   - a new address gets an account that can't sign in yet, and a link to confirm it;
   - an address waiting for its link gets a new link, which replaces the old one;
   - an address that has an account gets an email saying so, with a link to reset the
     password. The account doesn't change.

   So the page never tells whether an address has an account, the same rule as for
   invitations. Dash does the same work in every case and sends email in the background,
   so the answer takes as long either way.
2. The link (`https://dash.example.com/login.html#verify=…`) works once, for 24 hours. A
   new one replaces it: signing up again with the address sends one (so does
   `POST /api/signup/resend`).
3. Opening it asks for a password (at least 8 characters, hashed with Argon2id like every
   account). Choosing one confirms the address, creates the account's **personal
   workspace** and signs them in there.

   The password is chosen only there, by whoever can read the address's email, and never
   when signing up. That way nobody can sign up for someone else's address first and keep
   a password the real owner doesn't know. `POST /api/signup` ignores a `password` sent to
   it; `POST /api/signup/verify` takes `{token, password}`. An unconfirmed account whose
   links have all expired is deleted, so the address and username are free again.

The username is made from the address (`ann@example.com` becomes `ann`, or `ann2`, …).
People sign in with their email address or that username.

### Personal workspaces

With signup on, every account has a personal workspace: an organization named
"*username*'s workspace" with that account as its owner and only member. It can't invite
anyone; to work with others, anyone can create an organization (**New organization** in
the account menu, `POST /api/orgs`) and invite people to it with the usual
[invitation links](users.md#invitations).

Accounts that existed before signup was turned on, or that join through an invitation, get
their personal workspace the next time they sign in; they keep working in the org they
were in. The first account's **Default** org is left as it is. With signup off, nobody gets
a personal workspace and only platform admins create organizations, as before.

`GET /api/orgs/{id}/members` includes `member_count`, the number of members of the org.

## Forgot password

Once email works (signup on or not), the sign-in page has **Forgot password?**. It asks for
the account's email address and answers the same whether or not there is one. The link
(`/login.html#reset=…`) works once, for an hour, and a newer one replaces it.

Choosing a new password signs the account out everywhere and stops its API tokens and
connected apps (runners keep working), like any password change. It also unlinks Google
sign-in: the link may be the first proof that whoever holds the address owns the account, so
a Google account linked before it isn't trusted. Link Google again in **Account → Google account**
afterwards. It doesn't sign in:
accounts with two-factor sign-in still need their code. The `DASH_USERNAME` account gets
no reset link, because its password comes from `DASH_PASSWORD`; change it there.

## Rate limits

Signing up and asking for the link again share a limit; asking for a reset link has its
own. Each allows 10 requests an hour per client address and 3 an hour per email address,
then answers 429 until the hour has passed. Behind a reverse proxy, run Dash with
`-trust-proxy` so the limits see the client's address rather than the proxy's. Setting a
password from a link (reset or confirmation) counts towards the same lockout as failed
sign-ins.

## API

| Method and path                             | What                                                  |
|---------------------------------------------|-------------------------------------------------------|
| `GET /api/signup`                           | `{open, password_reset}`: what the sign-in page shows |
| `POST /api/signup {email}`                   | sign up; always `202` with a message                  |
| `POST /api/signup/resend {email}`           | the confirmation link again; always `202`             |
| `POST /api/signup/verify {token, password}` | confirm, choose the password and sign in              |
| `POST /api/password/forgot {email}`         | a reset link; always `202`                            |
| `POST /api/password/reset {token, password}` | set the new password                                 |

With signup off, the signup routes answer 404, and so does `/api/password/forgot` without
email.
