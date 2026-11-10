# Favro boards

A project can plan its tasks on a [Favro](https://www.favro.com) board
instead of Dash's own. Cards and tasks stay in sync both ways (see
[boards.md](boards.md) for the general model). Each project has its own
Favro settings, token, sync and webhook. Two projects can use different
boards, or different Favro accounts.

## 1. Get a Favro API token

1. In Favro, open your profile (your avatar → **My profile**).
2. Go to **API tokens** and create one, e.g. named "Dash".
3. Copy it. Favro shows it only once.

Dash calls Favro as that user, with their email address and this token.
That user must be a member of the organization and be able to see and edit
the board. A shared "bot" user keeps the comments Dash posts apart from
yours.

Dash stores the token with the project's credentials. It is never shown
again, sent to the browser or logged. To replace it, type a new one. To
remove it, use **Remove the saved credentials**.

## 2. Pick the board

In **Project settings → Task board**, choose **Favro**:

1. Enter the **Favro user email** and the **API token**. When you leave the
   token field, Dash loads the organizations the token can see.
2. Pick the **Organization**, and optionally a **Collection** to narrow the
   list of boards.
3. Pick the **Board**. Dash reads its columns and maps them (next step).
4. Optional:
   - **Only cards tagged**, e.g. `Claude`: Dash takes only cards with this
     tag, and tags the cards it makes. People can keep their other cards
     on the same board.
   - **Priority field**: a Number custom field holding the task's priority
     (lower runs first). Or **Priority tags** with a prefix, e.g. `P`, for
     tags `P1`, `P2`… Without either, priority stays Dash's alone.
   - **Comments on a card in review**: they always go to the task's chat.
     Here you can also keep them as rework notes for Claude, or have a
     comment send the task straight back to Claude as rework.

**Test connection** checks the token, organization and board without saving.

## 3. Map the columns

Each Dash status can map to one of the board's columns. Dash fills in the
mapping by column name (for example *Backlog* → draft, *To do* or *Ready* →
queued, *Doing* or *In progress* → running, *Review* → review, *Done* →
merged). Change any row by picking another column or typing its name. Case
and punctuation don't matter, and column ids work too. Several statuses may
share a column. Leave a status empty if its cards should stay where they
are.

What the mapping means:

- A new card in a column mapped to **draft** or **queued** becomes a task.
  If the column is mapped to queued, the task starts.
- People may move a card back to draft or into the queue. Other columns
  belong to Dash: dragging a card to the "merged" column, for example,
  moves it back, with a comment saying why.
- Dash moves the card as the task runs, goes to review, merges or fails. It
  comments with Claude's summary and the pull request link, or the reason
  for a failure.

Then **Save board**. If the project already has tasks, choose whether to
export the open ones as new cards or leave them in Dash.

## Webhooks

If this Dash has a public URL (`-public-url`, as hubs do), it registers a
webhook on the board itself, at
`https://<your dash>/api/projects/<id>/tracker/webhook`. Changes in Favro
then arrive at once. Calls are checked against the project's webhook
secret (Favro's `X-Favro-Webhook` signature). **New webhook secret**
re-registers the webhook with the new one. Switching the project to
another board, or back to Dash's, removes it.

Without a public URL, Dash reads the board every minute instead.

## Good to know

- Favro's API doesn't say when a card changed. If a field changes on both
  sides between two reads of the board, Dash's edit wins. Webhooks keep
  that window short.
- Dash archives a card when its task is deleted. A card archived or
  deleted in Favro leaves its task in Dash, unlinked.
- Dash backs off when Favro rate-limits it (HTTP 429 and the
  `X-RateLimit-*` headers). Projects that use the same account share that
  backoff.
