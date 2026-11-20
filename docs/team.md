# Set up a team

How to set up Dash for a small team, for example 2 planners and 2 developers. The details are in
[Several computers](several-computers.md) and [Users and organizations](users.md).

**The rule to know first: a runner only does its owner's work.** A runner runs Claude with its
owner's login, so the hub never sends one member's work to another member's runner. Each
developer needs their own runner, or a shared runner that uses an API key.

**Organization → Set up your team** checks these steps for owners and admins: the org's name,
invitations, required two-factor sign-in, a runner for each developer (or a shared one), project
labels and commands waiting for approval. Each item has a button to where it's set. The sheet
opens on it until every item is done.

## 1. Put the hub online

Runners connect to the hub over HTTPS. Put it on a server or behind a tunnel, as
[Deploy the hub](deploy.md) says. The hub runs nothing itself: it needs no `claude`, `git` or `gh`.

## 2. Invite the team

Open the account menu → **Organization** and invite each person. Dash gives you a link to send
them; it sends no email.

- Developers get the role **Member** (or **Admin**, to manage the org).
- Product owners get the role **Planner**. They see [Planning](planning.md) only.

Rename the org under **Organization → Members → Name**, and require two-factor sign-in under
**Organization → Sign-in** (owners).

See [Invitations](users.md#invitations) and [Planners](users.md#planners).

## 3. Give each developer a runner

Pick one of these for each developer.

**Their own runner (recommended).** The developer signs in, opens the account menu → **Runners** →
**Add a runner**, and runs the command it shows on their computer:

```sh
curl -fsSL https://dash.example.com/runner/install.sh | sh -s -- --code ABCD-EFGH
```

The runner is theirs and uses their own Claude login (Pro, Max, Team or Enterprise). See
[Add a runner](several-computers.md#add-a-runner).

**A shared runner.** One runner with an Anthropic API key takes the tasks of every member. A
Claude subscription can't be shared, so a shared runner has no Claude login:

```sh
umask 077
mkdir -p ~/.local/share/dash-runner
echo 'ANTHROPIC_API_KEY=sk-ant-...' >> ~/.local/share/dash-runner/runner.env
dash runner install
```

The runner reads `runner.env` in its data directory each time it starts, under its background
service too. Then in **Runners**, open it, and under **Settings** turn on **Take other members' tasks**. See
[Shared runners use an API key](several-computers.md#shared-runners-use-an-api-key).

A shared runner runs tasks, scans, tests and deploys for everyone. It doesn't run anything that
is a member's alone: for that, the member needs a runner of their own. Without one, a member
can't:

- chat with Claude (general, project or task chat), or run `!` commands in a chat
- open a terminal on a runner
- **Split…** a task or **Clean up leftovers**
- get a title from Claude for a task without one (the task is named after its first line instead)

See [Claude runs on each user's own runner](several-computers.md#claude-runs-on-each-users-own-runner).

## 4. Share the projects by labels

Add each project with **+ Add project** on the hub, from GitHub or a remote URL. That makes it a
**shared project**: everyone in the org sees it and adds tasks to it.

A shared project runs on **labels**, not on named runners. Set them in
**Project settings → Runners → Runs on**. Every runner that has all of them clones the project.
`any` is every runner of the org. To use your own labels (for example `team`), give them to each
runner on its page in **Runners → Labels**.

Each member's tasks then run on their own runners with those labels, or on a shared runner. A task
with no runner to go to waits and says why. See [Runner labels](several-computers.md#runner-labels)
and [Whose work a runner does](several-computers.md#whose-work-a-runner-does).

A project's commands (setup, test, run, deploy) need approval on another member's runner: an org
admin approves them in **Project settings → Commands**. See
[Project commands need approval](several-computers.md#project-commands-need-approval).

## 5. What planners do

Planners keep the org's plan in **Planning**: cards from Ideas to Done. They can't see projects,
tasks, chats or runners. Each card shows the status of its work in plain words.

**Start…** on a card makes a task or a goal in a project. A goal needs an **Owner**, a developer:
its coordinator runs on their runner, and they accept its plan. A task is the planner's, and a
planner has no runners, so a task they start runs only on a shared runner, and waits without one. With no
shared runner, let a developer press **Start…** instead: the work is then theirs and runs on their
runner. See [Planning](planning.md#start).

## 6. See who spent what

Owners and admins see what Claude cost in **Organization → Usage** (account menu →
Organization): by member and by project, per week or per month. Hover over an amount for its
runs and tokens. **Download CSV** saves the same figures, one row per period, member and
project, to open in a spreadsheet.

It adds up every Claude run on the org's tasks and scans, at the price Claude Code reports
(the API price, also on a subscription). A task's runs count for the member who made it; a task
Dash made counts for the member its work was for, and scans count for **Dash**. A deleted task's
runs are no longer counted.

The same figures come from the API, for an owner's or admin's token:

```sh
curl -H "Authorization: Bearer $DASH_TOKEN" "https://dash.example.com/api/usage/team?period=month"
curl -H "Authorization: Bearer $DASH_TOKEN" -o usage.csv \
  "https://dash.example.com/api/usage/team?period=week&from=2026-01-05&to=2026-03-29&format=csv"
```

- `period`: `week` (from Monday, the default) or `month`.
- `from` and `to`: dates, `YYYY-MM-DD`. Without them: the last 8 weeks or 6 months.
- `tz`: minutes behind UTC (as JavaScript's `getTimezoneOffset()` gives them); the server's
  time zone without it.
- `format`: `json` (the default) or `csv`.
