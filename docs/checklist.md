# Checklist (TODO.md)

The Checklist view shows the project's `TODO.md`, at the root of its repository, as a list you
can tick off. Open it from the side rail (on a phone, from the menu), with `gl`, or from the
command palette (`Ctrl-k`, "Checklist").

Dash reads GitHub-style task lists:

```markdown
# TODO

Most important first within each section.

## Next

- [ ] Ship the importer
      (continuation lines, indented under an item, belong to it)
  - [ ] A nested item
- [x] Done already
```

`#` and `##` headings are sections. Lines indented under an item continue it, nested items keep
their indentation, and anything else (paragraphs, quotes, code blocks) shows as read-only text.

## Changing it

Tick a box, click an item's text to edit it, delete it, or add one at the end of a section.
To reprioritise, drag an item by its grip (left of its checkbox) to another place: it lands in
the gap you drop it in, indented like the item below it (or above it, at the end of a list), or at
the top of a section when dropped on its heading. Its continuation lines and nested items move
with it.
Every change edits only the lines it touches: the rest of the file stays byte for byte (order,
wrapping, blank lines, CRLF or LF line endings, a missing final newline). With no `TODO.md`,
the view offers to create one; adding an item creates the file.

Where a change goes depends on the file:

- **Committed `TODO.md`** (the usual case): Dash commits the change straight onto the default
  branch, with a message like `TODO.md: check off Ship the importer`. No task and no Claude run
  are involved. The commit is made against the branch's current tip without touching any
  checkout, then lands the way merges do: synced with origin first when the project pushes,
  pushed with auto-push, and pushed to origin before the branch moves in a project shared
  through origin (one on runners). Changes you make within about two seconds of each other go
  into one commit.
- **A `TODO.md` git doesn't track** is saved straight to the project's folder, like other
  local files in the file browser.
- **Projects that land work through pull requests** never get commits straight on their default
  branch. A change there creates a task that commits it, as an edit in the file browser does,
  and the list shows the change once that task is merged. On a hub, such a project's `TODO.md`
  can't be changed from the checklist.

The list updates by itself when `TODO.md` changes elsewhere: a change from another tab or
device, from a chat's or Claude's checklist tools, or a merge, pull or sync that brings in a new
`TODO.md`. On a hub, the runner that committed the change tells the hub, which tells the page.
While you have changes that aren't saved yet, or are editing an item, the list waits and reads the
file once you're done. If the file changed since you loaded it (someone pushed, or another tab
saved), the save is refused, the view reloads `TODO.md`, and a message asks you to make your
change again.

Dash doesn't watch the files in the checkout: after editing an untracked `TODO.md` by hand, press
`r` on the Checklist view to read it again.

## Moving an item to the board

Each unchecked item has a **Move to draft** button, with **Move & queue** in its menu. Moving
creates a task first: the item's text becomes its title, and its text with its continuation
lines, any items nested under it, and the section it was in become its prompt. Dash then removes
the item (and those lines) from `TODO.md` in a commit like `TODO.md: move Ship the importer to
task 1a2b3c`. A toast links to the new task.

If removing the item fails (for example `TODO.md` changed meanwhile), the task stays and the
toast says the item is still in the file; nothing is created twice.

## Keys

On the Checklist view: `j`/`k` move between items, `x` (or `Space`) checks or unchecks, `m`
moves the item to a draft, `M` moves and queues it, `+`/`-` move it up or down a place (past
its neighbour at its level, or into the section above or below), `e` (or `Enter`) edits, `dd` deletes, `a`
focuses the "Add an item" field, `r` reads `TODO.md` again.

## MCP tools

Dash's chats (`dash_board`) and the `dash-mcp` server and claude.ai connector have the
checklist tools. They all take the usual `project` parameter (optional in a project's chat):

| Tool | What it does |
| --- | --- |
| `read_checklist` | Sections with their items (`ref`, `checked`, `text`, continuation `more`, `indent`) and other text, plus the file's `version` |
| `add_checklist_item(text, section?)` | Adds an item at the end of a section (a new section if there's none by that name) or of the file; creates `TODO.md` if needed |
| `check_checklist_item(ref, checked?)` | Checks off (default) or unchecks an item |
| `edit_checklist_item(ref, text, more?)` | Changes an item's text; `more` replaces its continuation lines |
| `delete_checklist_item(ref)` | Removes an item with its continuation lines and nested items |
| `reorder_checklist_item(ref, before?, after?, section?)` | Moves an item, with its continuation lines and nested items, just before or after another item (taking its indentation) or to the end of a section |
| `move_checklist_item_to_task(ref, queue?, priority?, depends_on?)` | Creates a task from an item and removes it; a draft unless `queue` is true, which the chat sets only when you ask for the task to run |

An item's `ref` is its line number and a hash of its text (`7:3f9a0c1b2d`). A ref from before
the file changed fails with a conflict ("call read_checklist again") instead of changing the
wrong line. Tool calls commit right away. Read-only connections can call `read_checklist` only.

When you ask a chat to "add X to the todo list" or to TODO.md, it adds a checklist item; board
tasks are what `create_todo` makes.

## API

- `GET /api/projects/{id}/checklist`: the parsed checklist, its `version` (the file's blob on
  the default branch, or a hash of a local file) and `via` (`commit`, `local` or `task`).
- `POST /api/projects/{id}/checklist` with `{"version": "…", "ops": [{"op": "check", "ref": "…",
  "checked": true}, {"op": "add", "text": "…", "section": "Next"}, {"op": "edit", "ref": "…",
  "text": "…"}, {"op": "delete", "ref": "…"}]}`: makes the changes as one commit. Answers the new
  checklist and `refs`, mapping each old ref to its new one. `409` when the version or a ref is
  stale.
- `POST /api/projects/{id}/checklist/move` with `{"ref": "…", "queue": false, "priority": 1,
  "depends_on": []}`: answers `{"task": …, "removed": true}`, or `removed: false` with `error`
  when the task was made but the item is still there.

The `/ws/events` stream sends `checklist.updated` (with `project_id` and `data.version`, the
file's new version, empty when it is gone or, on a hub, not known) whenever `TODO.md` changes on
the default branch, or a checklist save writes an untracked one.
