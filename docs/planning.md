# Planning

**Planning** is the org's plan, for a product owner: a board of cards, each something to build.
Cards link to the tasks and goals that carry them out, and show how that work is going in
plain words. Planning is kept in Dash's database, not in a repository or `TODO.md`, and
belongs to the org.

Open it from the sidebar: **Planning**, below the **Organization** divider, apart from the
project's sections (or `gp`). On a phone it is in the menu. A
[planner](users.md#planners) lands on it when they sign in. Their sidebar has only
**Planning** and their account.

## Cards

The board has four columns: **Ideas → Planned → In progress → Done**. The columns are fixed.
It looks like a project's **Board**: on a phone, tap a column's name at the top to show it.

- **Add card** at the bottom of a column adds a new card there; **New card** at the top adds
  one to **Ideas**. Type on the card what should be built: the first line is the title, the
  lines after it the description. `Enter` adds it and starts the next one, `Shift+Enter`
  starts a new line, `Esc` cancels. Clicking elsewhere adds it, if you typed something.
- Drag a card to another column, or up and down its own. A gap opens where it would land,
  and the cards around it move aside. Hold it near the top or bottom of the screen to scroll. With the keyboard, focus a card
  and press `Alt` with an arrow key. On a phone, drag it onto a column's name at the top, or change **Column** on the card.
- The project filter at the top shows one project's cards. A card may name a project, or
  none yet. Picking a project in the sidebar picks it here too; opening the page keeps the
  filter you had.
- **Mine** at the top shows only the cards you are a member of: your cards. Click it again to
  show everyone's.
- **Filter…** at the top shows only the cards with a tag, or with a target. Pick more tags to
  narrow it: a card must have all of them. One target at a time, such as `1.4`, so a target
  works as a milestone. Clicking a tag in an open card picks it too. Click **×** on a filter
  at the top to take it off.
- A card added while filters are on gets their tags and target, and you as a member with
  **Mine** on, so it shows.
- The filters stay as you left them, and are in the page's address, so a link shows the same
  cards (see [Links](links.md)).

Click a card to open it beside the board. On a phone it opens full screen, with **Back**.
`Esc` closes it. A card has:

- a title: **Rename**
- **Priority** (high, medium, low or none) and **Target**: free text, such as a version
  (`1.4`), a milestone or a date. Give cards of one milestone the same target, and filter by it.
- **Members**: the people in the org on the card, any number. Pick one from the list to add
  them, and click **×** on a member to take them off. Their pictures (or initials) show on the
  card on the board. Anyone in the org, planners too, can be a member and can add members. On
  the board, point at a card and press `Space` to put yourself on it; press it again to take
  yourself off. Someone you add gets a [notification](notifications.md#planning-cards).
  **Owner** on a member makes them the card's one owner, who answers for it; click it again for
  no owner. The owner's face comes first on the board, ringed. Taking the owner off the card
  leaves it with no owner.
- **Tags**: short labels, such as `backend` or `Q3`, any number up to 20. Type one in the box
  and press `Enter` (or a comma) to add it; the org's other tags are offered as you type.
  Click **×** on a tag to take it off. Tags show on the card on the board. Case doesn't
  matter: `Backend` and `backend` are the same tag.
- **Description** (markdown): type in the box. It saves when you click elsewhere, or press
  `Ctrl+Enter`.
  If someone else saved the title or description while you were editing it, Dash asks:
  **Reload theirs** drops your text, **Keep mine** replaces theirs.
- **Files**: pictures (PNG, JPEG, GIF, WebP, SVG), PDFs and text files (`.txt`, `.md`, `.log`,
  `.json`, `.csv`), up to 20 MB each and 30 a card. Click **Attach file**, paste into the
  description or a comment, or drop files on the open card, or on a card on the board. Anyone in
  the org, planners too, can attach and remove files. A card with a picture shows the first one
  across its top on the board, and how many files it has by a paper clip.
- **Depends on**: the cards that must be done before this one. **Add a card…** picks one;
  **×** takes it off. A card can't depend on itself, or on a card that already depends on it.
  **Needed by** lists the cards that depend on this one. Click a card in either list to open it.
- **Delete card…**: the card's creator, its owner or an admin can delete a card. Before it
  goes, the card names the tasks and goals that lose their card and the cards that no longer
  depend on it. It can't be undone.
- **Comments**: anyone in the org can comment; a comment's author or an admin can delete it.
  A card with comments shows how many on the board, by a speech bubble. Write `@` and
  someone's username, such as `@pia`, to mention them: they get a
  [notification](notifications.md#planning-cards).
- **Work**: the tasks and goals linked to it, each with its status

**Copy link** in the card's head copies its address, `/planning/cards/<id>`. It opens the card
for anyone in the org, after signing in if needed (see [Links](links.md)).

**Ask in chat** in the card's head opens the chat about the card: its project's chat, or the
**General** chat for a card of no project. The message box starts with the card's title and id;
add your question and send it. Claude reads the card with `get_card`. Planners don't have it:
they have no chat.

## Dependencies

A card that depends on cards not in **Done** yet shows **Waits for** and how many, on the
board. Point at it to see their titles. It goes away when they are all in **Done**. Dash
doesn't stop you moving or starting a card that waits: it only shows it.

Deleting a card takes it off the cards that depend on it.

## Previews

Click a file to preview it over the page:

- pictures at full size
- text files to read: Markdown formatted, JSON indented, CSV as a table
- other files (PDFs) with their name, size and **Download**

`←` and `→` step through the card's files. `Esc` or a click beside the file closes it.
`Ctrl`-click (`Cmd`-click on a Mac) opens the file in a new tab instead. Files attached to
tasks and chat messages preview the same way.

## Status

A card's status is worked out from its linked work each time it is shown. It is never
stored. It says, for example:

| Status                    | When                                                           |
|---------------------------|----------------------------------------------------------------|
| Nothing started yet       | nothing is linked                                              |
| Not started               | the linked tasks are drafts that never ran                     |
| Waiting to start          | the linked work is queued and nothing else is running          |
| Waiting for a runner: ask a developer to take it | queued work that no runner may run for whoever it runs for |
| Waiting for a runner to come online | queued work whose runners are all offline            |
| In progress               | work is running, or some of it is done                         |
| Waiting for review        | a developer has to review a task                               |
| Waiting for a decision    | a goal waits for someone to accept its plan or answer it       |
| Out of budget, needs its owner | a goal used up its budget                                 |
| Failed, needs a developer | a linked task failed                                           |
| Stopped                   | a goal was stopped, or a task that ran was moved back to drafts |
| All done                  | every task is merged and every goal is finished                |
| Shipped in 1.4.0          | all of it is in a release (the first release tag that has it)  |
| Live in production since 4 Nov 2026 | all of it is deployed and live in an environment     |
| Work was deleted          | every linked task or goal was deleted                          |

With more than one linked piece of work, the card also counts it: "3 of 5 done".
A linked task or goal that was deleted shows **Removed (work was deleted)**. It is not done,
and the card leaves it out of the count and the status.
**Live in** comes from the project's [deploys](deploys.md#where-it-is-live): a task is live in an
environment once a deploy that shipped it went live there. The card names production when all of
it is live there, else the environment furthest along that has all of it, and the latest date one
of its pieces went live. It works on a hub whose projects live on runners: the hub holds the
deploy records. Each linked task or goal shows its own **Live in** too.

**Shipped in** uses the project's release tags (see [Releases](releases.md)). Dash tells it
from the project's repository, so a hub whose projects live on runners shows **All done**
instead, unless the work is **Live in** an environment. **Live in** comes first when both apply.

The card shows only owner-facing words: titles and states. It never shows branches,
worktrees, diffs or runners.

Linking work that has started moves the card to **In progress** if it was in **Ideas** or
**Planned**. So does a linked draft task that is queued later. Dash never moves a card back:
a task moved back to drafts leaves its card in **In progress**. When everything linked is done, the card offers **Move to Done**. Dash never
moves a card to **Done** on its own.

## Start

**Start…** on a card makes the work for it:

1. Choose the project.
2. Choose **A task** (queued for Claude now) or **A goal** (a coordinator plans it into
   tasks; see [Goals](goals.md)).
3. As a planner, choose a developer (a member who isn't a planner): who a task **Runs for**,
   or a goal's **Owner**. The card's first developer is suggested.
4. Click **Start**.

On a hub, the project list holds the org's shared projects only. A
[runner's own project](several-computers.md#projects-of-a-runners-own) isn't in it, as the
hub doesn't keep its tasks. To start cards in it, make it a shared project: push it to a git
remote and add that with **+ Add project** (**From GitHub** or **From URL**). See
[Two kinds of projects](several-computers.md#two-kinds-of-projects). With no shared project
yet, **Start…** says so.

The card's title and description become the task's prompt or the goal's description. A task
also gets the card's files, for Claude to read. On a hub it doesn't: files can't be sent to
runners yet. Dash links the new work to the card and moves the card to
**In progress**.

Planners and members can both press **Start**. Work a member starts is theirs and runs on
their runners. A planner can't own a runner, so a task a planner starts is made in the name of
the developer it **Runs for**, and runs on that developer's runners (or a shared one). A goal
a planner starts belongs to its **Owner** the same way. Without a choice for a task (the
`start_card` tool), Dash picks the card's first developer, else whoever added the project,
else the organization's owner. A goal always needs its **Owner** chosen.

If no runner can take the work, the card says so, for example **Waiting for a runner: ask a
developer to take it**. Ask the developer to connect a runner for the project, or to turn on
**Take other members' tasks** for a runner in **Runners**.

The **Start this card** sheet warns, but still lets you start, when the card already has work
in progress (a second **Start** makes the same work again) or waits for cards not in **Done**
yet (it names them).

A task a planner starts is the card's: its news goes to the card's members who aren't planners,
as if they had created it. Put the developer on the card before you press **Start**. The
planners on the card, and the planner who pressed **Start**, hear of it as card news (see
[Planning cards](notifications.md#planning-cards)).

A goal a planner starts belongs to the developer they chose as its **Owner**. Its coordinator
runs as that developer, on their runner. Its first plan of tasks waits for them to accept it on
**Goals**, and they are notified. The card says **Waiting for a decision** until they do. A
member who presses **Start** owns the goal themselves.

## Link existing work

Members can link work that is already on a board:

- On the card: **Link existing…**, then pick a task or goal.
- On a task: **⋯ → Link to a planning card…**.
- On a goal: **Link to card**.

The **×** beside a linked task or goal unlinks it. Planners can't link or unlink work: they
see it only through the card.

## Live updates

The board and an open card update by themselves when anyone changes a card, comments, or
when linked work moves on.

## From the chat and MCP

The chats, `dash-mcp` and the claude.ai connector have tools for the plan (see
[MCP](mcp.md#planning)). They are for members: a planner can't use the MCP tools.

- `list_cards`: the cards, with their column, priority, target, members, owner, tags, dependencies and status
- `get_card`: one card, with its description, linked work and comments
- `create_card`: add a card, and the cards it depends on
- `update_card`: change a card, or move it to another column
- `comment_card`: comment on a card
- `start_card`: start a task or a goal for a card, as **Start…** does
- `link_card`: link a task or goal to a card

A project's chat reaches that project's cards and cards of no project.

## API

Planning is under `/api/planning/`. Column names match in any case. Adding or changing a card refuses a field it doesn't take, with `400`.

| Call                                                  | What it does                                     |
|-------------------------------------------------------|--------------------------------------------------|
| `GET /api/planning/cards?project=<id>`                | the cards, with status and linked work           |
| `POST /api/planning/cards`                            | add a card: `title`, `description`, `project`, `column`, `priority` (`high`, `medium`, `low`, or `none`), `target`, `members` (user ids; `[]` is nobody), `owner` (a user id, who joins the members if they aren't one; `0` is nobody), `depends_on` (card ids; `[]` is none), `tags` (up to 20; `[]` is none) |
| `GET`, `PATCH`, `DELETE /api/planning/cards/{id}`     | one card (`GET` has its comments). A card has `deps` and `needed_by` (id, title, column, done), `waiting`: its dependencies not done yet, and `version`. Send `version` with `PATCH` to get `409` instead of overwriting a change someone made since; a move or a file doesn't change it. `PATCH` takes the fields `POST` takes but `column`: move a card with `…/move` |
| `POST /api/planning/cards/{id}/move`                  | `column` and `position` (0 is the top)           |
| `GET`, `POST /api/planning/cards/{id}/comments`       | its comments; add one: `text`                    |
| `DELETE /api/planning/cards/{id}/comments/{comment}`  | delete a comment                                 |
| `POST /api/planning/cards/{id}/files?name=<name>`     | attach a file: the request's body                |
| `GET /api/planning/cards/{id}/files/{file}/{name}`    | a card's file                                    |
| `DELETE /api/planning/cards/{id}/files/{file}`        | take a file off the card                         |
| `POST /api/planning/cards/{id}/start`                 | `project` and `kind` (`task` or `goal`); the card, with `started`: the new work's `kind` and `id` |
| `GET /api/planning/projects`                          | the org's projects' ids, names, colors and icon URLs (on a hub, shared projects only) |
| `GET /api/planning/projects/{id}/icon`                | a project's icon image                           |
| `GET /api/planning/people`                            | the org's members to add to cards: ids, usernames and picture versions |
| `POST /api/planning/cards/{id}/links`                 | link `kind` and `id` (members)                   |
| `DELETE /api/planning/cards/{id}/links/{kind}/{work}` | unlink (members)                                 |
| `GET /api/planning/work?project=<id>`                 | tasks and goals to link (members)                |
| `GET /api/planning/linked/{kind}/{work}`              | the cards a task or goal is linked to (members)  |

## Not yet

Custom columns, estimates, sorting cards by priority, written progress summaries,
approval before **Start**, and syncing with Jira or Linear aren't in this version. See
[Task boards](boards.md) for how an outside tracker can own a project's board.
