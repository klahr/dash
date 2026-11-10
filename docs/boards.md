# Task boards

Each project's task board has a **board backend**. By default it is Dash's
own board (`dash`). A project can also be planned in an external tracker
instead, such as Favro (first), Trello or Jira. Dash then keeps the tracker
and its own tasks in sync, both ways. Set it in **Project settings → Board**.

Besides Dash's own board, **Favro** ships: see [favro.md](favro.md) for how
to set it up. This page describes the model and the interface the tracker
backends are written against (`internal/tracker`).

## Per project

Everything about a backend belongs to one project, stored in
`tracker_projects` (migration `0014_board_backends.sql`), not in global
config:

- the backend's name and its own settings (JSON: board or collection ids…)
- the **status mapping**: the tracker's column, list or workflow state for
  each Dash status. Several statuses may share a column. An empty one means
  cards aren't moved for that status.
- its **credentials**: stored like the notification webhooks' secrets and
  never sent back to the browser (`has_credentials` says they are set)
- a **webhook secret** of its own, shown once when made
- the sync state: last sync, last error, how far the tracker's changes were read

Projects on one Dash, or one hub, may use different backends at the same
time. They may also use the same provider with different boards and
accounts. The sync engine runs **one worker per project** with an external
backend. Workers start when Dash starts and restart when the project's
settings change; other projects' workers aren't touched. Projects on Dash's
own board run no worker, and nothing about them changes. On a hub, the hub
syncs: a runner's clone of a hub project has no backend of its own.

## Intent vs execution

The tracker owns a task's **intent**: title, description (the prompt),
priority, dependencies, status column and comments.

**Execution** stays in Dash and is never sent to a tracker: worktree,
Claude session, checks, auto fixes, feedback, diff size, cost, pull
request, merged commit, revert, runner, paused, reviewing and so on. The
queue, dispatcher and runners keep working on Dash's store as before.

Each linked task has an `external_ref` (`provider`, `id`, `url`).

## How syncing works

- **Outbound.** The worker follows the store's events for its project. It
  pushes intent changes and status moves, and comments on the card when a
  task is ready for review (with Claude's summary and the pull request),
  fails (with the reason) or merges.
- **Inbound.** The worker polls the tracker (every minute), and sooner when
  the tracker calls the webhook. It applies card changes to the tasks. A new
  card in a column mapped to *draft* or *queued* becomes a task in that
  project.
- **Conflicts.** Each link remembers each side's fields as of the last sync.
  A field changed on one side goes to the other. A field changed on both
  goes to the later writer. Before Dash pushes, it reads the tracker's latest
  changes, so a newer edit there isn't overwritten unseen.
- **The state machine wins.** A tracker may move a task back to draft or into
  the queue, where Dash's transition rules allow it. Running, review,
  merging, merged and failed are Dash's to reach. A card moved anywhere else
  goes back to the task's column, with a comment saying why.
- **Prompts.** A description edited while the task is a draft, queued or
  failed changes its prompt. A task that ran before also gets the change as
  feedback, so its resumed Claude session sees it. While the task runs or is
  in review, the edit waits until it is queued again, and the card says so.
- **No echoes.** Changes Dash applied from the tracker aren't pushed back, and
  Dash's own comments aren't read back as people's.
- **Deleted.** A task deleted in Dash deletes its card. A card deleted in the
  tracker leaves its task in Dash, unlinked.
- **Comments as rework feedback.** Comments by people go to the task's chat.
  A backend can let its project choose more for comments on a task in
  review: keep them as rework notes, or send the task back to Claude with
  them (as the Rework button does).
- **Webhooks.** A backend that registers its own webhook with the tracker
  (Favro) does so when the project's worker starts, at
  `<public URL>/api/projects/{id}/tracker/webhook`, when Dash has a public
  URL (`-public-url`, i.e. hub setups). It removes it when the project
  leaves that board or is deleted. Without a public URL, Dash only polls.

## Switching a project's board

A switch is never silent, and each one is logged as a project event
(`board.switched`):

- **Dash → tracker.** Choose whether the project's open tasks (draft,
  queued, running, review, failed) become new cards, or stay Dash-only.
  Running tasks keep running and are linked when exported. Tasks created
  from then on always get cards.
- **Tracker → Dash, or to another board.** After you confirm, the links and
  sync state are dropped. The cards stay where they are, and the tasks stay
  in Dash as its own.

The API answers `409` with `needs: "export"` or `needs: "confirm"` until the
choice is made.

## API

    GET  /api/projects/{id}/tracker                 backend, sync status, backends to pick from, recent events
    PUT  /api/projects/{id}/tracker                 {backend, config, status_map, credentials, clear_credentials, export, confirm}
    POST /api/projects/{id}/tracker/test            try settings (or the saved ones) without saving; answers the tracker's columns
    POST /api/projects/{id}/tracker/options?field=  a dropdown's choices (e.g. Favro's organizations), for unsaved settings
    POST /api/projects/{id}/tracker/sync            sync now
    POST /api/projects/{id}/tracker/webhook-secret  new webhook secret, answered once
    POST /api/projects/{id}/tracker/webhook         the tracker's webhook (no session: the project's secret)

`credentials` are merged into the saved ones for the same backend: an empty
value removes a key, and a missing key keeps it. No answer ever includes
credentials.

## Writing a backend

Implement `tracker.Backend` for one project's board:

```go
type Backend interface {
	Name() string
	List(ctx context.Context, since time.Time) ([]Item, error) // cards changed since (zero: all)
	Create(ctx context.Context, it Item) (Ref, error)         // in it.Column
	Update(ctx context.Context, ref Ref, it Item) error         // title, description, priority, dependencies
	SetStatus(ctx context.Context, ref Ref, status model.TodoStatus) error
	Comment(ctx context.Context, ref Ref, markdown string) error
	Delete(ctx context.Context, ref Ref) error
}
```

Then register it with a factory that builds an instance from the project's
config and credentials:

```go
tracker.Register(tracker.Registration{
	Name: "favro", Label: "Favro",
	New:  func(cfg tracker.ProjectConfig, creds tracker.Credentials) (tracker.Backend, error) { … },
	Fields: []tracker.Field{{Name: "collection", Label: "Collection"}, {Name: "token", Label: "API token", Secret: true}},
	Board: func(cfg json.RawMessage) string { … }, // which board: changing it is a switch
	DefaultStatusMap: …,
})
```

- Status goes through the project's mapping. `SetStatus` and `Create` use
  `cfg.ColumnFor(status)`, and `List` reports each card's raw column in
  `Item.Column`. The engine turns it back into a status. A workflow
  transition (Jira) is the backend's business inside `SetStatus`.
- Fill `Item.UpdatedAt`, plus `Version` when the tracker has one. Set
  `Deleted` when the change feed reports deleted or archived cards, and
  `Comments` if you can (their ids matter).
- Optional interfaces: `Webhooker` verifies webhook calls with the
  project's secret (its signature scheme). `WebhookRegistrar` registers and
  removes the webhook on the tracker (with `cfg.WebhookURL`). `Tester` backs
  the settings' Test connection button. `ColumnLister` offers the board's
  columns in the mapping editor. `CommentFeedbacker` picks what comments on
  a task in review do. `FieldSkipper` leaves fields the board can't hold
  (priority) to Dash instead of reading their absence as a clear.
- Settings fields can be dropdowns (`Type: "select"`), with fixed `Options`
  or `Dynamic` ones the registration's `Options` func lists (reloaded when
  a field in `DependsOn` changes).
- `internal/tracker/trackertest` is an in-memory provider to test against.
