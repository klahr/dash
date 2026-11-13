# Users and organizations

Dash has user accounts, and each account belongs to one or more **organizations** with a
role in each:

| Role   | Can                                                                          |
|--------|------------------------------------------------------------------------------|
| Owner  | everything an admin can, and make or demote owners                           |
| Admin  | invite people, change roles up to admin, remove members who aren't owners    |
| Member | use Dash in the org, see who else is in it, and leave it                     |

An org always keeps at least one owner. Separately from org roles, an account can be a
**platform admin** of the Dash instance: it can create new orgs, and use what belongs to
the whole instance rather than to one org (see below). With [open signup](signup.md) on,
every account can create orgs and has a personal workspace of its own.

## What belongs to an org

Everything you work with belongs to the org you're working in, and only its members see
it:

- **Projects**, and with them their tasks, issues, scans and scan runs, task and project
  chats, attachments, live previews, running commands and terminals, and Claude's
  permission prompts. Another org's project, or anything in it, answers "not found".
- **Live updates**: the dashboard only receives events of its org's projects.
- **The general chat** is your own: each user has one per org, which nobody else sees. On a
  hub with several users it runs on your own runner, with your Claude login (see
  [several computers](several-computers.md#claude-runs-on-each-users-own-runner)).
- **Projects on a runner** (**Add project** → **Local only**, clones of shared projects, or added
  from its computer) go in the org's own folder in the runner's projects directory, `<projects root>/<org slug>/` (e.g.
  `~/dash-projects/default/my-app`).
- Tasks and chat messages record who created them (`created_by`).

Runners belong to the org of the person who registered them: every member sees the org's
runners and can register their own (see [several computers](several-computers.md)). What
belongs to the whole Dash, and only platform admins can use: runners' own dashboards
(`/peer/…`, and the computer picker; everyone else sees just this computer) and pausing or
resuming the task queue, which every org shares. The subscription usage
meter and the merge queue's length are shown to everyone.

The MCP endpoints act as a person in an org too. The chat's task tools act as the user
chatting, in their org; a task's tools act as the user who created it, in the task's org. A
claude.ai connector (`/mcp`) acts as the account that connected it, in the org chosen when it
was allowed (see [tokens](#tokens)), and calls Dash directly rather than through the REST API.

## The first account

On first start Dash creates an account from `DASH_USERNAME` and `DASH_PASSWORD`, makes it a
platform admin and the owner of an org named **Default**, and puts everything already in
Dash (projects, tasks, issues, scans, chats) in that org. The general chat from before
becomes that account's own. An install that had a single
account keeps working with the same `.env`, and nobody is signed out by the upgrade.

That account stays tied to the environment: change `DASH_USERNAME` or `DASH_PASSWORD` and
restart, and it's renamed or gets the new password, and its sessions and Claude connectors
are signed out. Once there are users, the variables are optional.

Once [email](signup.md#email-smtp) is set up, the sign-in page offers "Forgot password?"
(a reset link by email).

Passwords are hashed with Argon2id. Signing in takes the same time whether or not a name
exists, and five failed tries from an address lock it out for 15 minutes. Turn on
[two-factor sign-in](#two-factor-sign-in) if Dash is reachable from the internet.

## Two-factor sign-in

Anyone can turn on two-factor sign-in under **Two-factor sign-in** in the Account dialog (account menu → Account):
scan the QR code with an authenticator app (1Password, Google Authenticator, Authy, …)
and type the code it shows to confirm. Dash then shows ten **recovery codes**, once.
Each works once in place of a code, for when the phone is lost; they're stored only as
hashes. **New recovery codes** replaces them, and turning two-factor sign-in off asks for
your password again.

With it on, signing in asks for a code after the password, and so does connecting
Claude (the connector's sign-in page has a field for it). Each code works once. Wrong
codes count towards the same lockout as wrong passwords: five failed tries from an
address, password or code, lock it out for 15 minutes (the defaults; see
[Deploy the hub](deploy.md#the-login-is-internet-facing)), and a right password doesn't
reset the count until the code is right too. Wrong codes also count per account, so
someone with a stolen password can't keep guessing from other addresses.

**Requiring it.** An org's owners can tick **Require two-factor sign-in** in
**Organization** (after turning it on for themselves). Members without it are sent to set
it up, at their next sign-in or their next click if they're signed in, and can't do
anything else until they have; their Claude connectors stop working until then too.
Because records aren't separated by org yet, the requirement applies to anyone who belongs
to such an org, whichever org they're working in. Members can't turn it off while an org
of theirs requires it, and the member list shows who has it.

**Locked out?** Someone who lost both their phone and their recovery codes needs the
server's operator, who runs, on the server:

    dash reset-2fa [-data <dir>] <username or email>

That turns it off for the account (it works while Dash runs). If an org requires it,
they set it up again at their next sign-in.

## Invitations

In the account menu, **Organization** lists the members of the org you're working in.
Owners and admins can change roles, remove members and invite people there. Each of
those changes asks for your password again.

Inviting someone gives you a link to send them; Dash doesn't email it. Whoever opens the
link sees the org and role, and joins by signing in to an existing account or by
creating one. They aren't a member until they accept, and they can decline instead.
Dash says the same thing whether or not the address already has an account. A link works
once and for 7 days, and inviting the same address again replaces it. Because the link
is what grants access, send it only to the person you're inviting. A
[personal workspace](signup.md#personal-workspaces) can't invite anyone.

## Several orgs

When you belong to more than one org, the account menu lets you switch between them
(`POST /api/auth/switch-org`). Membership is checked on every request: someone removed
from an org loses access at once (open live connections close within seconds), and if
they belong to other orgs they continue in one of those.

## Tokens

Besides browser sessions, Dash signs in with tokens, all kept in one table, hashed, and listed
under **Tokens & apps** in the Account dialog (account menu → Account):

- **API tokens** you create there, for scripts and MCP clients: `Authorization: Bearer dash_…`
  on `/api/…` and `/mcp`. Creating one asks for your password; the token is shown once.
- **Connected apps**: what an OAuth client such as a claude.ai connector got when you allowed it
  (see [the connector](mcp.md#claudeai-and-the-claude-app-custom-connector)).
- **Runners** you registered (see [several computers](several-computers.md)).

Every token acts as you **in one organization**, chosen when it is made: the org picker when
creating an API token, the consent page for a connector (if you're in several orgs), your active
org for a runner code. There is no default: a token without an org isn't made, and a token whose
org you leave stops working; it never moves to another of your orgs. Revoking a token ends it at
once, and changing your password ends API tokens and connectors (not runners).

Scopes narrow a token further: **read** allows `GET` requests (and the live event stream),
**write** everything else. Whatever its scopes, a token can't manage members, invitations,
tokens or two-factor sign-in, change org settings, switch orgs or confirm a password: those need
a signed-in browser. While an org of yours requires two-factor sign-in that you haven't set up,
your API tokens and connectors are refused too (your runners keep working).

The tokens Dash gives its own Claude runs (for the task tools and permission prompts) carry the
task's user and org too: the user who created the task (or, for tasks from before users, the env
account) in the task's org. Their tools only see that org's projects, and stop working if the user
leaves it.

## Sign in with Google

Organizations on Google Workspace can let their people sign in with their Google accounts
(OpenID Connect: Dash checks Google's signed ID token, with PKCE, state, a nonce and a cookie
tying the sign-in to the browser that started it). Without a Google client nothing changes: there
is no button, and `/auth/google/*` answers 404.

### Creating the Google OAuth client

1. In the [Google Cloud console](https://console.cloud.google.com/), pick or create a project.
2. **APIs & Services → OAuth consent screen**: user type **Internal** if only your Workspace should
   sign in (Google then refuses everyone else), **External** for an instance serving several
   organizations. The scopes are `openid`, `email` and `profile`.
3. **Credentials → Create credentials → OAuth client ID**, type **Web application**, with this one
   **authorized redirect URI**:

   ```
   https://<your Dash>/auth/google/callback
   ```

   `<your Dash>` is the host of `DASH_PUBLIC_URL`. With org subdomains it's the base domain
   (`https://dash.example.com/auth/google/callback`), never an org's subdomain: everyone comes back
   there and is then handed to their org's address, so you register one URI however many orgs there
   are. The org settings page shows the exact URI to copy.

### The instance's client, or an org's own

- **The instance's** serves every organization that has none of its own. Set
  `DASH_GOOGLE_CLIENT_ID` and `DASH_GOOGLE_CLIENT_SECRET` (in the environment or `.env`), or, if they
  aren't set, a platform admin enters them under **Organization → Google sign-in**.
- **An org's own**: an owner enters a client ID and secret under **Organization → Google sign-in**.
  Its consent screen shows their organization and can be Internal to their Workspace. Signing in
  to that org then uses it, and it only lets in accounts of the org's verified domains who are (or
  become, by the rules below) members.

Client secrets entered in Dash are encrypted (AES-256-GCM) with the hub's secret key before they
reach the database, and the API never returns them: it says whether one is set. The key is
`DASH_SECRET_KEY` (64 hex digits, e.g. `openssl rand -hex 32`), or else `<data>/secret.key`, made
on first start. Back it up with the database: without it, stored client secrets can't be read and
have to be entered again.

### Org settings (owners and admins)

Under **Organization → Google sign-in**:

- **Google Workspace domains**, e.g. `example.com` (several allowed). A domain counts once it's
  verified. After you add it, Dash shows a TXT record like
  `dash-domain-verification=…`. Add it to the domain's DNS, then press **Check DNS**. A platform
  admin can also approve a domain. Signing in with a Google account of the Workspace doesn't
  verify it: every employee has one. A verified domain belongs to one organization only. For
  sign-ins Dash trusts Google's `hd` (hosted domain) claim, never the address's domain alone;
  consumer `gmail.com` accounts have none.
- **Auto-join**: someone whose Google account is in a verified domain joins the org on their
  *first* Google sign-in, as member (or admin; never owner). Someone an admin removes doesn't come
  back by signing in again. Off, they need an invitation as before.
- **Require Google sign-in**: members whose address is in a verified domain can't use the org with
  a password session, or with a Google account outside the verified domains (a personal Gmail);
  they're asked to sign in with their work Google account, and can't link another one. Owners keep
  their password, so a broken
  Google setup can't lock everyone out. API tokens and runners are unaffected.
- **Also ask for the two-factor code after Google** (off by default): members with two-factor
  sign-in enter their code after Google too. An org that requires two-factor sign-in always asks.
- **Test Google sign-in** goes through Google with the org's client and shows what Google said
  (address, Workspace domain, whether it's verified). It never signs anyone in.
- **Sign out** on a member ends their sessions in the org at once.
- **Recent sign-in activity**: Google sign-ins and failures, links, joins and setting changes (the
  audit log, also in the server log as `auth event`).

### Who gets in

Google accounts are matched on Google's stable account id (`sub`), not the email address.

- **Invitations**: the invitation page has **Join with Google**. The Google address must be the
  invited one unless the inviter ticked *Let them join with any Google account*.
- **A new account** with no invitation joins an org by auto-join only. Otherwise, with open signup
  it gets a personal workspace, as signing up by email would; without, the sign-in page says the
  organization hasn't enabled Dash. Nobody becomes an owner by signing in.
- **An address that already has an account**: Google's first sign-in links to it only if its `hd`
  is a verified domain of an org that account belongs to. Otherwise Dash asks for the account's
  password once to confirm the link, so whoever holds a Google account with someone's address
  can't take over their Dash account.
- **Account → Google account** links or unlinks Google. An account made by signing in with Google
  has no password: it can't unlink until it chooses one (*Forgot password?*), and where Dash asks
  for the password before a change, a Google sign-in within the last 10 minutes confirms instead.
  Choosing a password from a reset link unlinks Google; link it again afterwards.
- Failed Google sign-ins count towards the sign-in lockout, and answer with a page naming the reason
  (never Google's raw error).

### Removing people

Dash checks with Google at every sign-in: someone suspended or removed in Google Workspace can't
sign in with Google again, and an org that requires Google refuses an account no longer in its
domain. **Sessions they already have last until they expire (7 days)**: end them with **Sign out**
on the member (or remove the member, which takes effect at once). Their API tokens keep working
until revoked or the member is removed.

To delete an account altogether, see [Your data](privacy.md): everyone can delete their own in
**Account → Your data**, and platform admins can delete someone else's from the members list.

## API

| Method and path                                   | What                                      |
|---------------------------------------------------|-------------------------------------------|
| `GET /api/me`                                     | user, active org, all their orgs          |
| `POST /api/auth/switch-org {org_id}`              | work in another org                       |
| `GET /api/orgs`, `POST /api/orgs {name}`          | your orgs; create one (platform admins, or anyone with open signup) |
| `GET /api/orgs/{id}/members`                      | members, `member_count`, and for admins the invitations |
| `PATCH /api/orgs/{id}/members/{uid} {role, password}` | change a role                         |
| `DELETE /api/orgs/{id}/members/{uid} {password}`  | remove a member, or leave                 |
| `POST /api/orgs/{id}/invitations {email, role, any_email, password}` | invite; returns the link's `path` |
| `DELETE /api/orgs/{id}/invitations/{iid}`         | revoke an invitation                      |
| `POST /api/invitations/{lookup,accept,register,decline} {token, …}` | the invitation page    |
| `GET /api/tokens`                                 | your API tokens, connected apps, runners  |
| `POST /api/tokens {name, org_id, scopes, password}` | an API token; returns its `secret` once |
| `DELETE /api/tokens/{id}`                         | revoke a token, disconnect an app or runner |
| `POST /api/login/totp {ticket, code}`             | second sign-in step: `/api/login` answers `{totp_required, ticket}` |
| `GET /api/account/2fa`                            | on?, recovery codes left, required?       |
| `POST /api/account/2fa/setup`                     | a new secret, its `otpauth://` URI and QR |
| `POST /api/account/2fa/enable {code}`             | confirm; returns the recovery codes       |
| `POST /api/account/2fa/disable {password}`        | turn it off                               |
| `POST /api/account/2fa/recovery-codes {password}` | new recovery codes                        |
| `PATCH /api/orgs/{id} {require_2fa, password}`    | owners: require it of every member        |
| `GET /auth/google/start?org=&next=` (or `POST` with `invite`) | sign in with Google; `purpose=link` links the signed-in account, `purpose=test` tests an org's setup |
| `GET /api/auth/google?org=`                       | whether the sign-in page offers Google    |
| `GET /api/orgs/{id}/sso`, `PATCH … {client_id, client_secret, auto_join, join_role, require_google, google_2fa, password}` | an org's Google sign-in settings |
| `POST /api/orgs/{id}/sso/domains {domain, password}`, `DELETE …/{domain}` | its Workspace domains |
| `POST /api/orgs/{id}/sso/domains/{domain}/check`  | admins: verify a domain by its DNS TXT record |
| `POST /api/orgs/{id}/members/{uid}/sign-out {password}` | admins: end a member's sessions in the org |
| `GET /api/account/google`, `DELETE … {password}`  | your linked Google account; unlink        |
| `GET /api/admin/sso`, `PUT /api/admin/sso/google`, `POST /api/admin/sso/domains/verify` | platform admins: the instance's client, approving domains |
| `GET /api/account/export`, `DELETE /api/account {password}` | your data as JSON; delete your account ([privacy.md](privacy.md)) |
| `DELETE /api/admin/users/{uid} {password}`        | platform admins: delete someone's account |

With a token, `GET /api/me` shows the token and its org, and the routes that manage members,
invitations, tokens, two-factor sign-in and orgs answer 403.
