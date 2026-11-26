# Planning

**Planning** is the org's plan, for a product owner: boards of cards, each something to build.
Cards link to the tasks and goals that carry them out, and show how that work is going in
plain words. Planning is kept in Dash's database, not in a repository or `TODO.md`, and
belongs to the org.

Open it from the sidebar: **Planning**, below the **Organization** divider, apart from the
project's sections (or `gp`). On a phone it is a tab in the bottom bar. A
[planner](users.md#planners) lands on it when they sign in. Their sidebar has **Planning**,
**Chat** and their account.

## The top of the page

One row, from the left:

- The boards' tabs. Click one to show its cards.
- **+**: add a board.
- The magnifier: search the cards.
- The filters that are on, each with **×** to take it off.
- **Mine**, **Filter** and **New card**.

On a phone the row has the tabs, search, **Filter** and **⋯**. **⋯** has **New card**,
**Add board** and **Mine**. **Filter** shows how many filters are on.

## Boards

An org can have several boards, for example one for each product or team. Every org has a
default board; the cards made before there were boards are on it.

- **All boards**, the first tab when there is more than one board, shows every board's cards.
  Each card then names its board.
- To add a board, click **+** after the tabs, type its name and press `Enter`. `Esc` cancels.
- Double-click a tab to rename its board, or press `F2` on it.
- Right-click a tab for its menu: **Rename**, **Default**, **Move left**, **Move right** and
  **Delete…**. The tab shown has **⋯**, which opens the same menu.
- **Default** makes the board the default one: a card given no board goes there.
- Deleting a board moves its cards to the end of the default board's columns. Dash asks first.
  The default board can't be deleted.
- The board shown is in the page's address, so a link opens the same board. A card's link
  opens its board.
- The filters and the search work within the board shown.

Anyone in the org, planners too, can manage the boards.

## Cards

A board has four columns: **Ideas → Planned → In progress → Done**. The columns are fixed.
It looks like a project's **Board**: on a phone, tap a column's name at the top to show it.

- **Add card** at the bottom of a column adds a new card there; **New card** at the top adds
  one to **Ideas**. A new card goes on the board shown. Type on the card what should be built: the first line is the title, the
  lines after it the description. `Enter` adds it and starts the next one, `Shift+Enter`
  starts a new line, `Esc` cancels. Clicking elsewhere adds it, if you typed something.
- Drag a card to another column, or up and down its own. A gap opens where it would land,
  and the cards around it move aside. Hold it near the top or bottom of the screen to scroll.
  On a phone, press and hold a card, then drag it onto a column's name at the top. An open
  card's **⋯** has **Move to** each other column too.
- Point at a card and click the empty circle after its faces to join it.
- The magnifier at the top searches the cards' titles and descriptions as you type. `Esc`
  clears it.
- **Filter** at the top has three lists: **Project**, **Tags** and **Targets**.
  - A project shows only that project's cards. Picking a project in the sidebar picks it here
    too; opening the page keeps the filter you had.
  - Pick more tags to narrow it: a card must have all of them. Clicking a tag in an open card
    picks it too.
  - One target at a time, such as `1.4`, so a target works as a milestone.
  - Pick a ticked one again to take it off, or **Clear filters**.
- **Mine** at the top shows only the cards you are a member of: your cards. Click it again to
  show everyone's.
- A card names its project with the project's icon, unless the board shows one project only.
- The four columns' names fit across a phone's screen. A column's count shows when it has
  cards.
- A card added while filters are on gets their tags and target, and you as a member with
  **Mine** on, so it shows.
- The filters stay as you left them, and are in the page's address, so a link shows the same
  cards (see [Links](links.md)).

Click a card to open it beside the board. The board narrows, so the card stays in sight. On a
phone it opens full screen, with **Back**. `Esc` closes it.

The card's head has:

- the title: click it to edit it. `Enter` saves, `Esc` cancels.
- the column, such as **Planned**: click it to move the card to another column.
- the status of its work, and when it was added.
- **Start…**: start a task or a goal for the card (see [Start](#start)).
- **Write a feature brief** (see [Write a feature brief](#write-a-feature-brief)).
- **⋯**: **Link existing…**, **Ask in chat**, and **Move to** each other column.
- the link icon: copy the card's link.

Below the head:

- **Description** (markdown), shown formatted. Click it, or the pencil, to edit it. It saves
  when you click elsewhere, or press `Ctrl+Enter` (`Cmd+Enter` on a Mac). `Esc` cancels. The
  card on the board shows its first paragraph as plain text, without headings.
  If someone else saved the title or description while you were editing it, Dash asks:
  **Reload theirs** drops your text, **Keep mine** replaces theirs.
- **Board**, with more than one board: pick another to move the card there, to the end of its
  column. It leaves the board shown.
- **Priority** (high, medium, low or none) and **Target**: free text, such as a version
  (`1.4`), a milestone or a date. Give cards of one milestone the same target, and filter by it.
- **Project**: the project the card is for, or none yet.
- **Members**: the people in the org on the card, any number. **Join** puts you on it. Pick
  someone from the list to add them, and click **×** on a member to take them off. Their
  pictures show on the card on the board; someone without a picture shows as their initials on
  a colour of their own. Anyone in the org, planners too, can be a member and can add members.
  Someone you add gets a [notification](notifications.md#planning-cards).
  The star on a member makes them the card's one owner, who answers for it; click the owner's
  star again for no owner. The owner is marked **Owner**, and their face comes first on the
  board, ringed. Taking the owner off the card
  leaves it with no owner. The owner can delete the card, so only the card's creator, its
  owner or an admin can change who owns it, or take the owner off.
- **Tags**: short labels, such as `backend` or `Q3`, any number up to 20. Type one in the box
  and press `Enter` (or a comma) to add it; the org's other tags are offered as you type.
  Click **×** on a tag to take it off. Tags show on the card on the board, each in a colour of
  its own. Case doesn't matter: `Backend` and `backend` are the same tag.
- **Work**: the tasks and goals linked to it, each with its status.
- **Depends on**: the cards that must be done before this one, on any board. **+ Dependency**
  picks one; **×** takes it off. A card can't depend on itself, or on a card that already
  depends on it. **Needed by** lists the cards that depend on this one. Click a card in either
  list to open it.
- **Files**: pictures (PNG, JPEG, GIF, WebP, SVG), PDFs and text files (`.txt`, `.md`, `.log`,
  `.json`, `.csv`), up to 20 MB each and 30 a card. Click **+ File**, paste into the
  description or a comment, or drop files on the open card, or on a card on the board. Anyone in
  the org, planners too, can attach and remove files. A card with a picture shows the first one
  across its top on the board, and how many files it has by a paper clip.
- **Comments**: anyone in the org can comment, in markdown. Dash adds a note of its own when it
  moves the card to **Done** (see [Done](#done)). Each shows how long ago it was
  written; point at the time for the date. Point at your own comment to delete it. A card with
  comments shows how many on the board, by a speech bubble. Write `@` and
  someone's username, such as `@pia`, to mention them: they get a
  [notification](notifications.md#planning-cards).
- **Delete card…**: the card's creator, its owner or an admin can delete a card. Before it
  goes, the card names the tasks and goals that lose their card and the cards that no longer
  depend on it. It can't be undone.

Work, Depends on, Needed by and Files show only when they have something in them.

The link icon in the card's head copies its address, `/planning/cards/<id>`. It opens the card
for anyone in the org, after signing in if needed (see [Links](links.md)).

**Ask in chat**, under **⋯** in the card's head, opens the chat about the card: its project's chat, or the
**General** chat for a card of no project. The message box starts with the card's title and id;
add your question and send it. Claude reads the card with `get_card`. A planner's opens their
**Chat**, on their own runner. Without one online, the card says **Install a runner to use
Claude here** instead.

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
a task moved back to drafts leaves its card in **In progress**.

## Done

When all of a card's linked work is done (every task merged, every goal finished), Dash moves
the card to the top of **Done**. Its comments get a note from Dash that says why, for
example "Moved to Done: all 2 pieces of its linked work are done."

- A card with nothing linked, or whose linked work was all deleted, stays where it is.
- Move a card out of **Done** and Dash leaves it there. It moves to **Done** again only after
  new work is linked to it and that work is done too.

## Write a feature brief

You can have Claude interview you about a card and write its description as a feature
brief: **Write a feature brief** on the card, or **New card** then **Write a feature brief**.
The brief's last step starts a task or a goal from the card, or saves it to **Planned**. See
[Write a feature brief](plan-project.md#write-a-feature-brief). Planners have it with a runner of
their own online (see [Planners](users.md#planners)).

## Start

**Start…** on a card makes the work for it:

1. Choose the project.
2. Choose **Task** or **Goal** (a coordinator plans it into tasks; see [Goals](goals.md)).
3. As a planner, choose a developer (a member who isn't a planner): who a task **Runs for**,
   or a goal's **Owner**. The card's first developer is suggested.
4. Read the text the work starts with, and edit it if you like: a task's **Prompt** is the
   card's title and description, a goal's **Description** is the card's.
5. Click **Create draft** or **Queue task** for a task, or **Start goal** for a goal. A planner
   has **Create draft** and **Create goal**.

On a hub, the project list holds the org's shared projects only. A
[runner's own project](several-computers.md#projects-of-a-runners-own) isn't in it, as the
hub doesn't keep its tasks. To start cards in it, make it a shared project: push it to a git
remote and add that with **+ Add project** (**From GitHub** or **From URL**). See
[Two kinds of projects](several-computers.md#two-kinds-of-projects). With no shared project
yet, **Start…** says so.

A task also gets the card's files, for Claude to read. On a hub it doesn't: files can't be
sent to runners yet. Dash links the new work to the card. A queued task or a started goal
moves the card to **In progress**; a draft moves it there once you queue the draft from the
project's **Board**.

Planners and members can both press **Start**. Work a member starts is theirs and runs on
their runners. A planner's own runner runs no tasks, so a task a planner starts is made in the name of
the developer it **Runs for**, and runs on that developer's runners (or a shared one). A goal
a planner starts belongs to its **Owner** the same way. Without a choice for a task (the
`start_card` tool), Dash picks the card's first developer, else whoever added the project,
else the organization's owner. A goal always needs its **Owner** chosen.

Nothing a planner starts runs until that developer agrees:

- A task waits as a draft. The developer reads it and clicks **Queue**.
- A goal starts paused. The owner reads it on **Goals** and clicks **Resume**.

The developer is notified, and the card gets a comment from the planner saying what they
started and for whom. The card moves to **In progress** once the developer starts the work.

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

A goal a planner starts belongs to the developer they chose as its **Owner**. Once they resume
it, its coordinator runs as that developer, on their runner. Its first plan of tasks waits for
them to accept it on **Goals**, and they are notified. The card says **Waiting for a decision** until they do. A
member who presses **Start** owns the goal themselves.

## Link existing work

Members can link work that is already on a board:

- On the card: **⋯** → **Link existing…**, then pick a task or goal.
- On a task: **⋯ → Link to a planning card…**.
- On a goal: **Link to card**.

The **×** beside a linked task or goal unlinks it. Planners can't link or unlink work: they
see it only through the card.

## Keyboard

The keys are in the app's **Keyboard** sheet (`?`), under **Planning**:

- `Space` over a card joins it, or takes you off it.
- `Alt` with an arrow key moves the focused card to the column beside, or up and down.

## Live updates

The board and an open card update by themselves when anyone changes a card, comments, or
when linked work moves on.

## From the chat and MCP

The chats, `dash-mcp` and the claude.ai connector have tools for the plan (see
[MCP](mcp.md#planning)). They are for members: a planner can't use the MCP tools.

- `list_cards`: a board's cards, with their board, column, priority, target, members, owner, tags,
  dependencies and status
- `get_card`: one card, with its description, linked work and comments
- `create_card`: add a card to a board, and the cards it depends on
- `update_card`: change a card, or move it to another column or board

`list_cards`, `create_card` and `update_card` take a `board`: its name or id. Left out, it is
the default board.
- `comment_card`: comment on a card
- `start_card`: start a task or a goal for a card, as **Start…** does
- `link_card`: link a task or goal to a card

A project's chat reaches that project's cards and cards of no project.

## API

Planning is under `/api/planning/`. Column names match in any case. Adding or changing a card refuses a field it doesn't take, with `400`.
A `board` is a board's id or its name, in any case. Left out, it is the default board.

| Call                                                  | What it does                                     |
|-------------------------------------------------------|--------------------------------------------------|
| `GET /api/planning/cards?board=<board>&project=<id>`  | a board's cards (`board=all`: every board's), with status and linked work. A card whose linked work is all done moves to Done first |
| `POST /api/planning/cards`                            | add a card: `title`, `description`, `project`, `board`, `column`, `priority` (`high`, `medium`, `low`, or `none`), `target`, `members` (user ids; `[]` is nobody), `owner` (a user id, who joins the members if they aren't one; `0` is nobody), `depends_on` (card ids; `[]` is none), `tags` (up to 20; `[]` is none) |
| `GET`, `PATCH`, `DELETE /api/planning/cards/{id}`     | one card (`GET` has its comments). A card has `deps` and `needed_by` (id, title, column, done), `waiting`: its dependencies not done yet, and `version`. Send `version` with `PATCH` to get `409` instead of overwriting a change someone made since; a move or a file doesn't change it. `PATCH` takes the fields `POST` takes but `column`: move a card with `…/move`. A new `board` moves it to the end of its column there. A card has `board_id` and `board_name`; `deps` and `needed_by` name each card's `board` |
| `POST /api/planning/cards/{id}/move`                  | `column` and `position` (0 is the top)           |
| `GET`, `POST /api/planning/cards/{id}/comments`       | its comments (Dash's own notes have `dash: true`); add one: `text` |
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
| `GET`, `POST /api/planning/boards`                    | the org's boards in their order, each with `id`, `name`, `position`, `default` and `cards` (how many); add one: `name` |
| `PATCH /api/planning/boards/{board}`                  | `name` renames it; `position` moves it (0 is the first); `default: true` makes it the default board |
| `DELETE /api/planning/boards/{board}`                 | delete it; its cards move to the default board, which can't be deleted |

## Not yet

Custom columns, estimates, sorting cards by priority, written progress summaries,
approval before **Start**, and syncing with Jira or Linear aren't in this version. See
[Task boards](boards.md) for how an outside tracker can own a project's board.
