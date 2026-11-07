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
the whole instance rather than to one org (see below).

## What belongs to an org

Everything you work with belongs to the org you're working in, and only its members see
it:

- **Projects**, and with them their tasks, issues, scans and scan runs, task and project
  chats, attachments, live previews, running commands and terminals, and Claude's
  permission prompts. Another org's project, or anything in it, answers "not found".
- **Live updates**: the dashboard only receives events of its org's projects.
- **The general chat** is your own: each user has one per org, which nobody else sees.
- **New projects** are created in the org's own folder, `<projects root>/<org slug>/`
  (e.g. `~/dash-projects/default/my-app`). Adopting an existing folder only works inside
  it; platform admins may adopt a folder anywhere.
- Tasks and chat messages record who created them (`created_by`).

What belongs to the whole Dash, and only platform admins can use: other computers
(runners and peers, `/peer/…`, the Runners page; everyone else sees just this computer)
and pausing or resuming the task queue, which every org shares. The subscription usage
meter and the merge queue's length are shown to everyone.

The MCP endpoints act as a person in an org too. The chat's task tools act as the user
chatting, in their org. A claude.ai connector (`/mcp`) acts as the account that connected
it, in its first org, and calls Dash directly rather than through the REST API.

## The first account

On first start Dash creates an account from `DASH_USERNAME` and `DASH_PASSWORD`, makes it a
platform admin and the owner of an org named **Default**, and puts everything already in
Dash (projects, tasks, issues, scans, chats) in that org. The general chat from before
becomes that account's own. An install that had a single
account keeps working with the same `.env`, and nobody is signed out by the upgrade.

That account stays tied to the environment: change `DASH_USERNAME` or `DASH_PASSWORD` and
restart, and it's renamed or gets the new password, and its sessions and Claude connectors
are signed out. Once there are users, the variables are optional.

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
address, password or code, lock it out for 15 minutes, and a right password doesn't
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

Inviting someone gives you a link to send them; Dash doesn't send email. Whoever opens the
link sees the org and role, and joins by signing in to an existing account or by
creating one. They aren't a member until they accept, and they can decline instead.
Dash says the same thing whether or not the address already has an account. A link works
once and for 7 days, and inviting the same address again replaces it. Because the link
is what grants access, send it only to the person you're inviting.

## Several orgs

When you belong to more than one org, the account menu lets you switch between them
(`POST /api/auth/switch-org`). Membership is checked on every request: someone removed
from an org loses access at once (open live connections close within seconds), and if
they belong to other orgs they continue in one of those.

## API

| Method and path                                   | What                                      |
|---------------------------------------------------|-------------------------------------------|
| `GET /api/me`                                     | user, active org, all their orgs          |
| `POST /api/auth/switch-org {org_id}`              | work in another org                       |
| `GET /api/orgs`, `POST /api/orgs {name}`          | your orgs; create one (platform admins)   |
| `GET /api/orgs/{id}/members`                      | members, and for admins the invitations   |
| `PATCH /api/orgs/{id}/members/{uid} {role, password}` | change a role                         |
| `DELETE /api/orgs/{id}/members/{uid} {password}`  | remove a member, or leave                 |
| `POST /api/orgs/{id}/invitations {email, role, password}` | invite; returns the link's `path` |
| `DELETE /api/orgs/{id}/invitations/{iid}`         | revoke an invitation                      |
| `POST /api/invitations/{lookup,accept,register,decline} {token, …}` | the invitation page    |
| `POST /api/login/totp {ticket, code}`             | second sign-in step: `/api/login` answers `{totp_required, ticket}` |
| `GET /api/account/2fa`                            | on?, recovery codes left, required?       |
| `POST /api/account/2fa/setup`                     | a new secret, its `otpauth://` URI and QR |
| `POST /api/account/2fa/enable {code}`             | confirm; returns the recovery codes       |
| `POST /api/account/2fa/disable {password}`        | turn it off                               |
| `POST /api/account/2fa/recovery-codes {password}` | new recovery codes                        |
| `PATCH /api/orgs/{id} {require_2fa, password}`    | owners: require it of every member        |
