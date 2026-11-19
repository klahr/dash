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
| `5j`           | a count before a key repeats it                        |

## Sections

| Key        | Opens                          |
|------------|--------------------------------|
| `gb`       | **Board**                      |
| `gi`       | **Issues**                     |
| `gn`       | **Scans**                      |
| `gs`       | **Shell**                      |
| `gh`       | **History**                    |
| `go`       | **Goals**                      |
| `gf`       | **Files**                      |
| `gl`       | **Checklist**                  |
| `gp`       | **Planning** (the organization's) |
| `gt` `gT`  | next / previous section        |

## Board and tasks

On the board the keys act on the task under the cursor; in an open task, on that task.

| Key                  | Does                                                              |
|----------------------|-------------------------------------------------------------------|
| `h` `j` `k` `l`      | move the cursor (board); `h`/`l` switch timeline and changes (open task) |
| `w` `b`              | next / previous lane with tasks                                   |
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

## Approvals

| Key | Does                                  |
|-----|---------------------------------------|
| `y` | allow the permission request in view  |
| `A` | always allow that tool in the project |
| `X` | deny                                  |

## Shell

| Key           | Does                       |
|---------------|----------------------------|
| `i` `Enter`   | type into the terminal     |
| `Ctrl-]`      | leave the terminal         |
| `x`           | stop what runs in it       |
| `R`           | run the server             |
| `T`           | run the tests              |
| `D`           | deploy                     |
| `!`           | a new shell                |

## History, Goals and Checklist

- **History**: `j`/`k` next / previous commit, `gg`/`G` first / last, `n`/`p` next / previous
  changed file of the open commit, `q`/`Esc` close it.
- **Goals**: `j`/`k` open the next / previous goal, `q`/`Esc` close it.
- **Checklist**: `j`/`k` move, `x`/`Space` check, `a` add, `e` edit, `dd` delete, `+`/`-` move up
  or down, `m` move to a draft task, `M` move and queue, `r` read TODO.md again.

## Commands

Type `:` and a command: `:project name`, `:newproject name [path]`, `:task …`, `:new`,
`:run cmd`, `:shell`, `:try` (the Shell at a commit), `:push`, `:ci`, `:cleanup`, `:pause`,
`:resume`, `:usage`, `:settings`, `:prefs`, `:connector`, `:perms`, `:board`, `:mine`, `:chat`,
`:pin`, `:history`, `:clear`, `:notify`, `:vim`, `:list`, `:q`.

## Changed keys

- **Scans** is `gn` (it was `gS`, too close to `gs` for the Shell).
- `a` only approves; a new task is `n`.
- Deny is `X` (it was `n`, which is also a new task).
- Draw on a preview is `P` (it was `D`, which is also Deploy on the Shell page).
