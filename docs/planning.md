# Planning

**Planning** is the org's plan, for a product owner: a board of cards, each something to build.
Cards link to the tasks and goals that carry them out, and show how that work is going in
plain words. Planning is kept in Dash's database, not in a repository or `TODO.md`, and
belongs to the org.

Open it from the sidebar: **Planning** (or `gp`). On a phone it is in the menu. A
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
  and the cards around it move aside. With the keyboard, focus a card
  and press `Alt` with an arrow key. On a phone, drag it onto a column's name at the top, or change **Column** on the card.
- The project filter at the top shows one project's cards. A card may name a project, or
  none yet.

Click a card to open it beside the board. On a phone it opens full screen, with **Back**.
`Esc` closes it. A card has:

- a title: **Rename**
- **Priority** (high, medium, low or none) and **Target**: free text, such as a version
  (`1.4`) or a date
- **Members**: the people in the org on the card, any number. Pick one from the list to add
  them, and click **×** on a member to take them off. Their pictures (or initials) show on the
  card on the board. Anyone in the org, planners too, can be a member and can add members. On
  the board, point at a card and press `Space` to put yourself on it; press it again to take
  yourself off.
- **Description** (markdown): type in the box. It saves when you click elsewhere, or press
  `Ctrl+Enter`.
- **Comments**: anyone in the org can comment; a comment's author or an admin can delete it.
  A card with comments shows how many on the board, by a speech bubble.
- **Work**: the tasks and goals linked to it, each with its status

## Status

A card's status is worked out from its linked work each time it is shown. It is never
stored. It says, for example:

| Status                    | When                                                           |
|---------------------------|----------------------------------------------------------------|
| Nothing started yet       | nothing is linked                                              |
| Not started               | the linked tasks are drafts                                    |
| In progress               | work is queued or running                                      |
| Waiting for review        | a developer has to review a task                               |
| Waiting for a decision    | a goal waits for someone to accept its plan or answer it       |
| Failed, needs a developer | a linked task failed                                           |
| All done                  | every task is merged and every goal is done                    |
| Shipped in 1.4.0          | all of it is in a release (the first release tag that has it)  |

With more than one linked piece of work, the card also counts it: "3 of 5 done".
**Shipped in** uses the project's release tags (see [Releases](releases.md)). Dash tells it
from the project's repository, so a hub whose projects live on runners shows **All done**
instead.

The card shows only owner-facing words: titles and states. It never shows branches,
worktrees, diffs or runners.

Linking work that has started moves the card to **In progress** if it was in **Ideas** or
**Planned**. When everything linked is done, the card offers **Move to Done**. Dash never
moves a card to **Done** on its own.

## Start

**Start…** on a card makes the work for it:

1. Choose the project.
2. Choose **A task** (queued for Claude now) or **A goal** (a coordinator plans it into
   tasks; see [Goals](goals.md)).
3. Click **Start**.

The card's title and description become the task's prompt or the goal's description. Dash
links the new work to the card and moves the card to **In progress**. Planners and members
can both press **Start**.

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

## From the chat

The chat has three tools for the plan (see [MCP](mcp.md#the-chats-task-tools)):

- `list_cards`: the cards, with their column, priority, target, members and status
- `create_card`: add a card
- `link_card`: link a task or goal to a card

## API

Planning is under `/api/planning/`:

| Call                                                  | What it does                                     |
|-------------------------------------------------------|--------------------------------------------------|
| `GET /api/planning/cards?project=<id>`                | the cards, with status and linked work           |
| `POST /api/planning/cards`                            | add a card: `title`, `description`, `project`, `column`, `priority`, `target`, `members` (user ids; `[]` is nobody) |
| `GET`, `PATCH`, `DELETE /api/planning/cards/{id}`     | one card (`GET` has its comments)                |
| `POST /api/planning/cards/{id}/move`                  | `column` and `position` (0 is the top)           |
| `GET`, `POST /api/planning/cards/{id}/comments`       | its comments; add one: `text`                    |
| `DELETE /api/planning/cards/{id}/comments/{comment}`  | delete a comment                                 |
| `POST /api/planning/cards/{id}/start`                 | `project` and `kind` (`task` or `goal`)          |
| `GET /api/planning/projects`                          | the org's projects' ids, names, colors and icon URLs |
| `GET /api/planning/projects/{id}/icon`                | a project's icon image                           |
| `GET /api/planning/people`                            | the org's members to add to cards: ids, usernames and picture versions |
| `POST /api/planning/cards/{id}/links`                 | link `kind` and `id` (members)                   |
| `DELETE /api/planning/cards/{id}/links/{kind}/{work}` | unlink (members)                                 |
| `GET /api/planning/work?project=<id>`                 | tasks and goals to link (members)                |
| `GET /api/planning/linked/{kind}/{work}`              | the cards a task or goal is linked to (members)  |

## Not yet

Custom columns, labels, written progress summaries, approval before
**Start**, and syncing with Jira or Linear aren't in this version. See
[Task boards](boards.md) for how an outside tracker can own a project's board.
