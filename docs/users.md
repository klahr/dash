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
- **New projects** are created in the org's own folder, `<projects root>/<org slug>/`
  (e.g. `~/dash-projects/default/my-app`). Adopting an existing folder only works inside
  it; platform admins may adopt a folder anywhere.
- Tasks and chat messages record who created them (`created_by`).

What belongs to the whole Dash, and only platform admins can use: other computers
(runners, `/peer/…`, the Runners page; everyone else sees just this computer)
and pausing or resuming the task queue, which every org shares. The subscription usage
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

Anyone can turn on two-factor sign-in under **Two-factor sign-in** in the account menu:
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
under **Tokens & apps** in the account menu:

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

## API

| Method and path                                   | What                                      |
|---------------------------------------------------|-------------------------------------------|
| `GET /api/me`                                     | user, active org, all their orgs          |
| `POST /api/auth/switch-org {org_id}`              | work in another org                       |
| `GET /api/orgs`, `POST /api/orgs {name}`          | your orgs; create one (platform admins, or anyone with open signup) |
| `GET /api/orgs/{id}/members`                      | members, `member_count`, and for admins the invitations |
| `PATCH /api/orgs/{id}/members/{uid} {role, password}` | change a role                         |
| `DELETE /api/orgs/{id}/members/{uid} {password}`  | remove a member, or leave                 |
| `POST /api/orgs/{id}/invitations {email, role, password}` | invite; returns the link's `path` |
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

With a token, `GET /api/me` shows the token and its org, and the routes that manage members,
invitations, tokens, two-factor sign-in and orgs answer 403.
