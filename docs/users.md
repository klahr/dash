# Users and organizations

Dash has user accounts, and each account belongs to one or more **organizations** with a
role in each:

| Role   | Can                                                                          |
|--------|------------------------------------------------------------------------------|
| Owner  | everything an admin can, and make or demote owners                           |
| Admin  | invite people, change roles up to admin, remove members who aren't owners    |
| Member | use Dash in the org, see who else is in it, and leave it                     |

An org always keeps at least one owner. Separately from org roles, an account can be a
**platform admin** of the Dash instance; for now that means it can create new orgs.

Projects, tasks and the rest aren't separated by org yet: every org sees the same
records. Today, orgs decide who can sign in and with which role.

## The first account

On first start Dash creates an account from `DASH_USERNAME` and `DASH_PASSWORD`, makes it a
platform admin and the owner of an org named **Default**, and puts everything already in
Dash (projects, tasks, issues, scans, chats) in that org. An install that had a single
account keeps working with the same `.env`, and nobody is signed out by the upgrade.

That account stays tied to the environment: change `DASH_USERNAME` or `DASH_PASSWORD` and
restart, and it's renamed or gets the new password, and its sessions and Claude connectors
are signed out. Once there are users, the variables are optional.

Passwords are hashed with Argon2id. Signing in takes the same time whether or not a name
exists, and five failed tries from an address lock it out for 15 minutes.

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
