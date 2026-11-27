# Keyboard

Dash's keys are vim-style. Typing in a field or a terminal is insert mode; `Esc` (`Ctrl-]` in a
terminal) returns to normal mode, where these keys work.

Press `?` to see them in the app: the **Keyboard** sheet has a table per area and opens at the
page you are on.

## Everywhere

| Key            | Does                                                   |
|----------------|--------------------------------------------------------|
| `?`            | the **Keyboard** sheet                                 |
| `:`            | the command line (see [Commands](#commands))           |
| `Ctrl-k`       | search projects, tasks and commands                    |
| `/`            | search the project's tasks                             |
| `n`            | new task                                               |
| `N`            | new task with all options                              |
| `p`            | switch project                                         |
| `J` `K`        | next / previous project                                |
| `o`            | focus the projects list; again for the sections list   |
| `c`            | the project's chat                                     |
| `gc`           | open or close the chat pane                            |
| `[`            | collapse or expand the sidebar (desktop)               |
| `Esc` `q`      | close what is open                                     |
| `Esc`          | stop reading aloud, while Dash speaks (Spoken replies) |
| `5j`           | a count before a key repeats it                        |

## Sections

| Key        | Opens                          |
|------------|--------------------------------|
| `gb`       | **Board**                      |
| `gn`       | **Scans**, over the Board      |
| `go`       | **Goals**                      |
| `gh`       | **History → Commits**          |
| `gd`       | **History → Deploys**          |
| `gf`       | **Code → Files**               |
| `gs`       | **Code → Shell**               |
| `gi`       | **Needs you** (every project's) |
| `gp`       | **Planning** (the organization's) |
| `gt` `gT`  | next / previous page           |

## Board and tasks

On the board the keys act on the task under the cursor; in an open task, on that task.

| Key                  | Does                                                              |
|----------------------|-------------------------------------------------------------------|
| `h` `j` `k` `l`      | move the cursor (board); `h`/`l` switch timeline and changes (open task) |
| `w` `b`              | next / previous lane with tasks                                   |
| `gg` `G`             | first / last task in the lane                                     |
| `Ctrl-d` `Ctrl-u`    | scroll half a page                                                |
| `j` `k`              | scroll (open task)                                                |
| `Enter`              | approve a task in Review (close it when it changed no files; open its pull request when GitHub reviews it), queue a draft, else open it |
| `Space`              | open the task                                                     |
| `a`                  | approve (a task in Review), or close one that changed no files    |
| `s`                  | queue                                                             |
| `x`                  | stop, or cancel to drafts                                         |
| `z`                  | pause / resume                                                    |
| `r`                  | rework                                                            |
| `e`                  | edit                                                              |
| `dd`                 | delete                                                            |
| `+` `-` `_`          | raise / lower / clear the priority                                |
| `t`                  | live preview                                                      |
| `P`                  | draw on the live preview (see [Draw](draw.md))                    |
| `!`                  | shell in the task's worktree                                      |
| `T`                  | run the tests again                                               |
| `Y`                  | duplicate as a draft                                              |
| `E`                  | open the worktree in your editor                                  |
| `u`                  | revert a merged task                                              |
| `gx`                 | open its pull request                                             |
| `i`                  | type in the task chat                                             |
| `v`                  | mark the file in view viewed, or not (changes of a task in Review) |

## Drawing

On a live preview, a task's or a commit's in **History**, `P` starts drawing (see [Draw](draw.md)).

| Key                     | Does                             |
|-------------------------|----------------------------------|
| `p` `a` `o` `b` `c`     | pen, arrow, circle, box, crop    |
| `1` `2` `3`             | colour                           |
| `x`                     | exact capture (desktop Chrome)   |
| `Ctrl-z`                | undo                             |
| `Enter`                 | add to the rework notes (a task) or attach to the chat (a commit) |
| `Esc`                   | done                             |

## Approvals

| Key | Does                                  |
|-----|---------------------------------------|
| `y` | allow the permission request in view  |
| `A` | always allow that tool in the project |
| `X` | deny                                  |

A question from Claude has no key for an answer: answer it in its card. `X` declines it.

## Code → Shell

| Key           | Does                       |
|---------------|----------------------------|
| `i` `Enter`   | type into the terminal     |
| `Ctrl-]`      | leave the terminal         |
| `x`           | stop what runs in it       |
| `R`           | run the server in the checkout |
| `T`           | run the tests              |
| `D`           | open **History → Deploys** |
| `!`           | a new shell                |

## History, Goals and Needs you

- **History → Commits**: `j`/`k` next / previous commit, `gg`/`G` first / last, `n`/`p` next / previous
  changed file of the open commit, `Ctrl-d`/`Ctrl-u` scroll it, `q`/`Esc` close it (its preview
  first, if it shows one), `P` draw on its live preview, `r` refresh (fetch origin and show its new commits).
- **Goals**: `j`/`k` open the next / previous goal, `q`/`Esc` close it.
- **History → Deploys**: `j`/`k` open the next / previous deploy, `q`/`Esc` close it.
- **Needs you**: `j`/`k` next / previous row (with one open, open that one), `gg`/`G` first / last,
  `Enter` open it, `y`/`A`/`X` allow, always allow or deny its permission request, `q`/`Esc` close it.

## Chat

| Key         | Does                                                                 |
|-------------|----------------------------------------------------------------------|
| `Enter`     | send                                                                 |
| `!command`  | run a shell command in a project or task chat (Claude sees it next turn) |
| `Esc`       | stop typing; again to close the overlay                              |

## Forms

| Key                     | Does                                                  |
|-------------------------|-------------------------------------------------------|
| `Enter`                 | new task sheet: save a draft                          |
| `Shift-Enter`           | new task sheet: a new line in the task                |
| `Alt-Enter`             | new task sheet: another task                          |
| `Backspace`             | new task sheet: in an empty field, remove it          |
| `Ctrl-Enter`            | new task sheet and task form: queue                   |
| `Ctrl-Shift-Enter` `Ctrl-s` | task form: save a draft                           |
| `Ctrl-Shift-.`          | dictate into the field you are in ([Dictation](dictation.md)) |
| `Ctrl-Shift-,`          | start or stop [voice commands](dictation.md#voice-commands) |
| `Esc`                   | close or cancel                                       |

On a Mac, `Ctrl` here is `Cmd` and `Alt` is `Option`.

## Commands

Type `:` and a command.

| Command                         | Does                                                      |
|---------------------------------|-----------------------------------------------------------|
| `:project name`                 | switch project (`name@runner` picks the runner)           |
| `:newproject [name]`            | **Add project**, with the name filled in                  |
| `:addclone`                     | **Runners**, to add a repository already on your runner   |
| `:task …`                       | queue a task                                              |
| `:new [prompt]`                 | the new task form                                         |
| `:run cmd` `:shell`             | run a command / open **Code → Shell**                     |
| `:try`                          | **Code → Shell** at a commit                              |
| `:push`                         | push merged work                                          |
| `:ci`                           | builds                                                    |
| `:cleanup`                      | clean up leftovers                                        |
| `:pause` `:resume`              | pause or resume the whole queue, every project (platform admins only) |
| `:usage`                        | Claude usage                                              |
| `:status`                       | the status popover                                        |
| `:settings` `:prefs`            | **Project settings** / preferences                        |
| `:tips`                         | show the first-review tips again                          |
| `:connector`                    | connect Claude                                            |
| `:runners` `:org` `:account`    | **Runners**, **Organization**, **Account**                |
| `:perms`                        | permission requests                                       |
| `:board` `:mine`                | the board / only your tasks                               |
| `:chat` `:pin` `:clear`         | chat: open, pin, new chat                                 |
| `:history`                      | **History**                                               |
| `:notify` `:vim` `:list`        | notifications, vim keys in the editor, invisibles         |
| `:refresh`                      | load the projects again                                   |
| `:help` `:logout`               | the **Keyboard** sheet / sign out                         |
| `:5`                            | the cursor to the fifth task in the lane                  |
| `:q`                            | close                                                     |

To pause one project rather than the queue, use **Pause this project** in `Ctrl-k`.

## Changed keys

- Scans, Shell, Files and Deploys are no longer pages of their own; their keys open where they
  moved: `gn` the **Scans** sheet over the Board, `gs` **Code → Shell**, `gf` **Code → Files**,
  `gd` **History → Deploys**.
- **Scans** is `gn` (it was `gS`, too close to `gs` for the Shell).
- `a` only approves; a new task is `n`.
- Deny is `X` (it was `n`, which is also a new task).
- Draw on a preview is `P` (it was `D`, which is also Deploy on the Shell page).
- `D` on **Code → Shell** opens **History → Deploys** (it ran the deploy command, which is an environment now).
