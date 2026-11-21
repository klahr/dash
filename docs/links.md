# Links

The address bar shows what you are looking at. Back and Forward step through it, a reload
opens the same place, and a copied address opens it for someone else.

## Share a task, a card or a goal

- A task: open it and click **Copy link** (the link icon beside **More actions**).
- A Planning card: open it and click **Copy link**.
- A goal: open it on **Goals** and click **Copy link**.

Anyone who can see the project (or, for a card, is in the organization) opens the same item.
Someone who isn't signed in signs in first and then lands on it. A link to something that is
gone, or that they can't see, opens the normal page with a short note saying so.

Any other page can be shared by copying the address bar.

## Addresses

| Address                         | Opens                                    |
|---------------------------------|------------------------------------------|
| `/p/<project>/board`            | a project's board                        |
| `/p/<project>/<view>`           | `issues`, `scans`, `run` (the **Shell** at main), `try` (the **Shell** at a commit), `history`, `goals`, `files` |
| `/p/<project>/tasks/<id>`       | a task, over the board                   |
| `/p/<project>/goals/<id>`       | a goal, on **Goals**                     |
| `/p/<project>/history/<sha>`    | a commit, on **History**                 |
| `/p/<project>/issues/<id>`      | an issue, expanded on **Issues**         |
| `/planning`                     | **Planning**                             |
| `/planning/cards/<id>`          | a Planning card                          |

`<project>` is the project's id, so a link keeps working when the project is renamed. A
project on another computer of a hub is `<id>@<computer>`.

An old link to a project's checklist (`/p/<project>/checklist`) opens its board.

These can be added to any of them:

| Query                              | Means                                              |
|------------------------------------|----------------------------------------------------|
| `?project=<id>`, `?tag=<tag>`, `?target=<target>`, `?mine=1` | Planning's filters; `tag` may be given more than once |
| `?sheet=settings&section=board`    | a sheet open over the page, at a section: `settings`, `account`, `org` or `runners` |

For example, **Project settings → Board** of a project:

```
https://dash.example.com/p/a1b2c3d4e5f6/board?sheet=settings&section=board
```

A plain `/` opens the view and project you had last.

## Back and Forward

Switching view or project, and opening a task, goal, commit, card or sheet, are steps that
Back returns from. Filters, the board shown and a sheet's section only change the address.

Closing a pane with `Esc` or its close button is the same as Back. On a phone, the
browser's or the system's Back closes a full-screen task, card, goal or commit.

## Older links

Notifications sent before these addresses existed, `/?project=<id>&task=<id>` (and
`goal=`, `issue=`), still open.

Planners use Planning only: a link into the app opens their Planning page, at the card if
it names one.
