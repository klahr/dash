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
- A tag filter shows only the cards with one tag. Click a tag in an open card to pick it, and
  click **×** on the tag at the top to show all cards again. A card added while a tag is
  picked gets that tag.

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
- **Tags**: short labels, such as `backend` or `Q3`, any number up to 20. Type one in the box
  and press `Enter` (or a comma) to add it; the org's other tags are offered as you type.
  Click **×** on a tag to take it off. Tags show on the card on the board. Case doesn't
  matter: `Backend` and `backend` are the same tag.
- **Description** (markdown): type in the box. It saves when you click elsewhere, or press
  `Ctrl+Enter`.
- **Files**: pictures (PNG, JPEG, GIF, WebP, SVG), PDFs and text files (`.txt`, `.md`, `.log`,
  `.json`, `.csv`), up to 20 MB each and 30 a card. Click **Attach file**, paste into the
  description or a comment, or drop files on the open card, or on a card on the board. Anyone in
  the org, planners too, can attach and remove files. A card with a picture shows the first one
  across its top on the board, and how many files it has by a paper clip.
- **Depends on**: the cards that must be done before this one. **Add a card…** picks one;
  **×** takes it off. A card can't depend on itself, or on a card that already depends on it.
  **Needed by** lists the cards that depend on this one. Click a card in either list to open it.
- **Comments**: anyone in the org can comment; a comment's author or an admin can delete it.
  A card with comments shows how many on the board, by a speech bubble.
- **Work**: the tasks and goals linked to it, each with its status

**Copy link** in the card's head copies its address, `/planning/cards/<id>`. It opens the card
for anyone in the org, after signing in if needed (see [Links](links.md)).

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
| Not started               | the linked tasks are drafts                                    |
| In progress               | work is queued or running                                      |
| Waiting for review        | a developer has to review a task                               |
| Waiting for a decision    | a goal waits for someone to accept its plan or answer it       |
| Failed, needs a developer | a linked task failed                                           |
| All done                  | every task is merged and every goal is done                    |
| Shipped in 1.4.0          | all of it is in a release (the first release tag that has it)  |
| Live in production since 4 Nov 2026 | all of it is deployed and live in an environment     |

With more than one linked piece of work, the card also counts it: "3 of 5 done".
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
**Planned**. When everything linked is done, the card offers **Move to Done**. Dash never
moves a card to **Done** on its own.

## Start

**Start…** on a card makes the work for it:

1. Choose the project.
2. Choose **A task** (queued for Claude now) or **A goal** (a coordinator plans it into
   tasks; see [Goals](goals.md)).
3. Click **Start**.

The card's title and description become the task's prompt or the goal's description. A task
also gets the card's files, for Claude to read. On a hub it doesn't: files can't be sent to
runners yet. Dash links the new work to the card and moves the card to
**In progress**. Planners and members
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

- `list_cards`: the cards, with their column, priority, target, members, dependencies and status
- `create_card`: add a card, and the cards it depends on
- `link_card`: link a task or goal to a card

## API

Planning is under `/api/planning/`:

| Call                                                  | What it does                                     |
|-------------------------------------------------------|--------------------------------------------------|
| `GET /api/planning/cards?project=<id>`                | the cards, with status and linked work           |
| `POST /api/planning/cards`                            | add a card: `title`, `description`, `project`, `column`, `priority`, `target`, `members` (user ids; `[]` is nobody), `depends_on` (card ids; `[]` is none) |
| `GET`, `PATCH`, `DELETE /api/planning/cards/{id}`     | one card (`GET` has its comments). A card has `deps` and `needed_by` (id, title, column, done) and `waiting`: its dependencies not done yet |
| `POST /api/planning/cards/{id}/move`                  | `column` and `position` (0 is the top)           |
| `GET`, `POST /api/planning/cards/{id}/comments`       | its comments; add one: `text`                    |
| `DELETE /api/planning/cards/{id}/comments/{comment}`  | delete a comment                                 |
| `POST /api/planning/cards/{id}/files?name=<name>`     | attach a file: the request's body                |
| `GET /api/planning/cards/{id}/files/{file}/{name}`    | a card's file                                    |
| `DELETE /api/planning/cards/{id}/files/{file}`        | take a file off the card                         |
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
