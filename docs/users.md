# Users and organizations

Dash has user accounts, and each account belongs to one or more **organizations** with a
role in each:

| Role    | Can                                                                          |
|---------|------------------------------------------------------------------------------|
| Owner   | everything an admin can, and make or demote owners                           |
| Admin   | invite people, change roles up to admin, remove members who aren't owners    |
| Member  | use Dash in the org, see who else is in it, and leave it                     |
| Planner | use [Planning](planning.md) and their own account; with a runner of their own, the general chat and feature briefs (see [Planners](#planners)) |

Each deploy environment also says which of these roles may deploy, approve and change its
settings (see [Roles and approvals](deploys.md#roles-and-approvals)).

An org always keeps at least one owner. Separately from org roles, an account can be a
**platform admin** of the Dash instance: it can create new orgs, and use what belongs to
the whole instance rather than to one org (see below). With [open signup](signup.md) on,
every account can create orgs and has a personal workspace of its own.

## What belongs to an org

Everything you work with belongs to the org you're working in, and only its members see
it:

- **Projects**, and with them their tasks, issues, scans and scan runs, task chats,
  attachments, live previews, running commands and terminals, and Claude's
  permission prompts. Another org's project, or anything in it, answers "not found".
- **Live updates**: the dashboard only receives events of its org's projects.
- **The general chat** is your own: each user has one per org, which nobody else sees. On a
  hub with several users it runs on your own runner, with your Claude login (see
  [several computers](several-computers.md#claude-runs-on-each-users-own-runner)). Leaving
  the org, or being removed from it, deletes yours.
- **Project chats** are your own too: each member has one per project, which nobody else sees,
  owners and admins included (see [Chat](chat.md#your-own-project-chats)). Leaving the org, or
  being removed from it, deletes yours.
- **Projects on a runner** (**Add project** → **Local only**, clones of shared projects, or added
  from its computer) go in the org's own folder in the runner's projects directory, `<projects root>/<org slug>/` (e.g.
  `~/dash-projects/default/my-app`).
- Tasks and chat messages record who created them (`created_by`). The board shows the creator
  on each card and in the task's pane, and **Mine** filters the board to your own tasks (see
  [Who made a task](tasks.md#who-made-a-task)).

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
Dash (projects, tasks, issues, scans, chats) in that org. Rename it under
**Organization → Members → Name** (owners and admins); its address stays. The general chat
from before becomes that account's own. An install that had a single
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

**Requiring it.** An org's owners can tick **Require two-factor sign-in of every member** in
**Organization → Sign-in** (after turning it on for themselves). Members without it are sent to set
it up, at their next sign-in or their next click if they're signed in, and can't do
anything else until they have; their Claude connectors stop working until then too.
Because records aren't separated by org yet, the requirement applies to anyone who belongs
to such an org, whichever org they're working in. Members can't turn it off while an org
of theirs requires it, and the member list shows who has it.

**Locked out?** Someone who lost both their phone and their recovery codes asks an owner or
admin of their org to turn it off (see
[Helping a member who can't sign in](#helping-a-member-who-cant-sign-in)), or a platform admin
(see [Admin](#admin)). Without either, the server's operator runs, on the server:

    dash reset-2fa [-data <dir>] <username or email>

That turns it off for the account (it works while Dash runs). If an org requires it,
they set it up again at their next sign-in.

## Helping a member who can't sign in

Owners and admins help members of their org from **Organization → Members**. Each member has:

- **Reset password…**: Dash makes a one-time link for them to choose a new password. If Dash
  sends [email](signup.md#email-smtp), it emails them the link. You also see the link once,
  with **Copy**, to hand over another way. It works once, for an hour, like a "Forgot
  password?" link. Using it signs them out everywhere. Tick **Also sign them out now** to end
  their sessions at once. Accounts that sign in only with Google have no password, so they
  have no **Reset password…**.
- **Turn off 2FA…**: for a member who lost their phone and their recovery codes. They sign in
  with their password alone until they turn it on again. An org that requires it asks them at
  their next sign-in. Dash emails them that it was turned off.
- **Sign out…**: ends their sessions and stops their API tokens and connected apps. Their
  runners carry on.

Each asks for your password. Nobody types a new password for someone else.

Dash checks who may, on the server:

- You can act on an account only if **every** org it belongs to is one where you are an owner or
  admin. Otherwise an admin of one org could take over an account that is also in another.
  The buttons are then off, with the reason: "Also a member of another organization: ask a
  platform admin". **Sign out…** still works, for this org only: it ends their sessions and
  tokens here.
- Not on a platform admin (unless you are one), and not on yourself: use **Account** for your
  own account.
- Admins can't act on owners. Owners can act on admins and members.

Each action goes in the org's sign-in log (**Organization → Sign-in → Recent sign-in activity**)
and the server log, with who did it, to whom and when.

## Admin

Platform admins have **Admin** in the account menu: every account and every org on this Dash.

**Users** lists every account, with the total at the top, and a search by name or email. Each
row shows the account's orgs and role in each, whether it is a platform admin, has two-factor
sign-in, has a password or signs in only with Google, when it was made and its last sign-in.
Pick one to open it in a pane beside the list (full screen on a phone, with **Back**; `Esc`
closes it). There you can:

- **Reset password…**, **Turn off two-factor…** and **Sign out everywhere…**, as in
  [Helping a member who can't sign in](#helping-a-member-who-cant-sign-in), for any account.
- **Disable…**: they are signed out at once and can't sign in, and their tokens and runners
  stop working, until you **Enable…** it again. Nothing is deleted.
- **Make platform admin…** or **Remove platform admin…**. Dash always keeps one platform admin
  who isn't disabled: it refuses to remove or disable the last one, you included.
- Add them to an org with a role, change their role, or take them out of an org. An org always
  keeps an owner.
- **Delete account…** (see [Your data](privacy.md)).

**Organizations** lists every org, with its members, projects and when it was made. Open one
to rename it, change its address (its slug), manage its members, or delete it. To delete an
org, type its name to confirm. That ends its members' access to it and deletes its
invitations, chats, planning boards, sign-in settings, the tokens that act in it and its
sign-in log. Its members' accounts stay. An org with projects or a subscription can't be
deleted until they are gone, and neither can the org Dash keeps records without an org in.

Every change asks for your password, and goes in the sign-in log and the server log.

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

## Planners

A **planner** is a product owner: someone who plans what to build, not a developer. They use
[Planning](planning.md) and their own account. With a runner of their own, they also use Claude
there: the general chat and **Write a feature brief**.

A planner can:

- see, add, edit, move and delete the org's planning cards, and comment on them
- press **Start** on a card, which makes a task or a goal in a project and links it (a goal
  needs a developer as its owner)
- see each card's linked work through its status and titles
- see deploys through the API, read only: what is live where, the deploys (without their
  logs) and the environments' settings history (see [Deploys](deploys.md#api))
- get [card news](notifications.md#planning-cards) pushed to their browser: they were mentioned
  or added to a card, or a card's work is ready for review, failed or merged
- use their own account: the account menu at the bottom of the sidebar (in the menu on a phone)
  switches orgs, and has **Account** (**Profile picture**, **Notifications**, **Two-factor
  sign-in**, **Google account** and **Your data**), **Runners** and **Sign out**
- add, see, rename and remove their own runners in **Runners** (see
  [A planner's runner](several-computers.md#a-planners-runner))

With a runner of their own online, a planner also has:

- **Chat**: the general chat, on their runner, with their own Claude login. Its Dash tools are
  Planning's only: list, read, create, update, comment on and start cards. See
  [Chat](chat.md#a-planners-chat).
- **Write a feature brief** and **Ask in chat** on a card. Each interview round runs on their
  runner. It reads the project's `CLAUDE.md`, README and file tree, unless an owner turned that
  off in **Organization → Planners**. Then it gets only the card and the project's name.

Without one, **Chat** and the card say **Install a runner to use Claude here**, which opens the
setup steps. Everything else works the same. A Claude subscription is one person's, so a
runner of their own is the only way a planner uses Claude without an org API key.

A planner's runner runs only their chat and feature briefs. It never gets the org's work: no
tasks, goals, scans, tests, deploys, previews or clones of projects, and it can't be shared.

A planner can't see or use projects, tasks, goals, project and task chats, terminals, other
people's runners, previews, scans, issues, the MCP tools, **Write a project brief** or
**Organization**. Every such request answers `403`. They do see the names and pictures of the
org's members, to add them to cards. Their live updates carry Planning's changes only. When a
planner signs in, Dash opens Planning, in the same sidebar as everyone else's, with
**Planning** and **Chat** as its sections.

To make someone a planner, open **Organization**, and either invite them with the role
**Planner**, or change a member's role to **Planner**. Only owners and admins can. A planner
can be made a member later, the same way.

A planner's role is per org: someone can be a planner in one org and a member in another.

Don't mix these up:

- **Planning**: the org's boards of cards, from Ideas to Done. Everyone uses it.
- **Planner**: the role of someone who uses Planning (and, with a runner of their own, the chat
  and feature briefs).
- **Write a project brief** (in **+ Add project**): Claude interviews a developer about an idea
  and starts a new project from the brief. **Write a feature brief** (on a Planning card) does
  the same for a feature, onto the card. Planners can write feature briefs on their own runner,
  not project briefs.
- A goal's **plan**: the tasks its coordinator proposes, which the goal's owner accepts. A
  planner who starts a goal names a developer as its owner (see [Start](planning.md#start)).

## Profile picture

Open **Account → Profile picture** and click **Choose picture…**. Dash crops the picture to a square
in the middle and makes it small before saving it. It shows in the account menu, in the
members list of **Organization**, on the board's cards you created and on the Planning cards
you are a member of. Only members of your organizations see it. **Delete** goes back
to your initial.

Dash takes PNG, JPEG, GIF and WebP pictures, up to 1 MB once made small.

## Several orgs

When you belong to more than one org, the account menu lets you switch between them
(`POST /api/auth/switch-org`). Membership is checked on every request: someone removed
from an org loses access at once (open live connections close within seconds), and if
they belong to other orgs they continue in one of those. Their runners in that org get no
more of its work, and their tunnels close within seconds.

## Tokens

Besides browser sessions, Dash signs in with tokens, all kept in one table, hashed. **Account →
Tokens & apps** lists API tokens and connected apps; **Runners → Overview → Runners you registered**
lists runner tokens:

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

Owners and admins see every member's API tokens and connected apps in the org under
**Organization → Members' tokens**, with when each was last used. They can revoke those of members
whose role isn't above theirs; it asks for the password. Runner tokens are under **Runners**.

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
  aren't set, a platform admin enters them under **Organization → Sign-in**.
- **An org's own**: an owner enters a client ID and secret under **Organization → Sign-in**.
  Its consent screen shows their organization and can be Internal to their Workspace. Signing in
  to that org then uses it, and it only lets in accounts of the org's verified domains who are (or
  become, by the rules below) members.

Client secrets entered in Dash are encrypted (AES-256-GCM) with the hub's secret key before they
reach the database, and the API never returns them: it says whether one is set. The key is
`DASH_SECRET_KEY` (64 hex digits, e.g. `openssl rand -hex 32`), or else `<data>/secret.key`, made
on first start. Back it up with the database: without it, stored client secrets can't be read and
have to be entered again.

### Org settings (owners and admins)

Under **Organization → Sign-in → Google sign-in**:

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
until revoked or the member is removed: revoke them under **Organization → Members' tokens**.

Removing a member, or a member leaving, moves their queued tasks in the org back to **Drafts**:
a task runs only on its owner's runners or a shared one (see
[Whose work a runner does](several-computers.md#whose-work-a-runner-does)), so no runner would take
them any more. This counts the tasks they made and the ones they queued for someone else. A task
they made stays theirs: queued again, it needs a shared runner. Dash's own tasks that were theirs
(a failing build's fix, a tracker card) stay queued and go to the next member in line. Their tasks
in other states stay as they are.

To delete an account altogether, see [Your data](privacy.md): everyone can delete their own in
**Account → Your data**, and platform admins can delete someone else's from the members list or
in **Admin → Users**. To keep someone out without deleting anything, a platform admin disables
the account in **Admin → Users**.

## API

| Method and path                                   | What                                      |
|---------------------------------------------------|-------------------------------------------|
| `GET /api/me`                                     | user, active org, all their orgs          |
| `POST /api/auth/switch-org {org_id}`              | work in another org                       |
| `GET /api/orgs`, `POST /api/orgs {name}`          | your orgs; create one (platform admins, or anyone with open signup) |
| `GET /api/orgs/{id}/members`                      | members, `member_count`, and for admins the invitations |
| `PATCH /api/orgs/{id} {name, slug, require_2fa, password}` | rename (owners and admins); the slug and 2FA rule are owners' |
| `PATCH /api/orgs/{id}/members/{uid} {role, password}` | change a role                         |
| `DELETE /api/orgs/{id}/members/{uid} {password}`  | remove a member, or leave                 |
| `POST /api/orgs/{id}/invitations {email, role, any_email, password}` | invite; returns the link's `path` |
| `DELETE /api/orgs/{id}/invitations/{iid}`         | revoke an invitation                      |
| `POST /api/invitations/{lookup,accept,register,decline} {token, …}` | the invitation page    |
| `GET /api/tokens`                                 | your API tokens, connected apps, runners  |
| `POST /api/tokens {name, org_id, scopes, password}` | an API token; returns its `secret` once |
| `DELETE /api/tokens/{id}`                         | revoke a token, disconnect an app or runner |
| `GET /api/orgs/{id}/tokens`                       | admins: members' API tokens and connected apps |
| `DELETE /api/orgs/{id}/tokens/{tid} {password}`   | admins: revoke one (not of a higher role) |
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
| `POST /api/orgs/{id}/members/{uid}/sign-out {password}` | admins: end a member's sessions and tokens in the org (everywhere, for an account they may manage) |
| `POST /api/orgs/{id}/members/{uid}/password-reset {password, sign_out}` | admins: a one-time reset link (`path`, `url`), emailed if Dash sends email |
| `DELETE /api/orgs/{id}/members/{uid}/2fa {password}` | admins: turn off a member's two-factor sign-in |
| `GET /api/account/google`, `DELETE … {password}`  | your linked Google account; unlink        |
| `GET /api/admin/sso`, `PUT /api/admin/sso/google`, `POST /api/admin/sso/domains/verify` | platform admins: the instance's client, approving domains |
| `GET /api/account/export`, `DELETE /api/account {password}` | your data as JSON; delete your account ([privacy.md](privacy.md)) |
| `DELETE /api/admin/users/{uid} {password}`        | platform admins: delete someone's account |
| `GET /api/admin/users?q=`, `GET /api/admin/users/{uid}` | platform admins: every account (`total`, `matched`), one with its sign-in log |
| `POST /api/admin/users/{uid}/password-reset {password, sign_out}`, `DELETE …/2fa {password}`, `POST …/sign-out {password}` | platform admins: help someone sign in |
| `PUT /api/admin/users/{uid}/disabled {disabled, password}`, `PUT …/platform-admin {platform_admin, password}` | platform admins: disable or enable; make or remove a platform admin |
| `PUT /api/admin/users/{uid}/orgs/{oid} {role, password}`, `DELETE … {password}` | platform admins: add to an org or change the role; take out |
| `GET /api/admin/orgs?q=`, `GET /api/admin/orgs/{oid}` | platform admins: every org; one with its members |
| `PATCH /api/admin/orgs/{oid} {name, slug, password}`, `DELETE … {name, password}` | platform admins: rename, change the address, delete |
| `PUT /api/account/avatar` (the image as the body), `DELETE …` | set or remove your profile picture |
| `GET /api/users/{uid}/avatar`                     | a picture: yours, or a member's of your org. `avatar` in `/api/me` and the members list is its version (`0`: none) |

With a token, `GET /api/me` shows the token and its org, and the routes that manage members,
invitations, tokens, two-factor sign-in and orgs answer 403.
