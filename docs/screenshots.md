# Before/after screenshots

When a task of a web project finishes, Dash can take screenshots of the pages it changed, before
and after the change, at a wide and a phone size. They show in Review, so most UI changes can be
judged without starting the [preview](previews.md).

## Turn them on

Screenshots are off by default. In **Project settings → Screenshots**:

1. Turn on **Take before/after screenshots of finished tasks**.
2. Set **UI paths**, one per line (`web/`, `src/components`). Only a task that changes a file
   under one of them gets screenshots. Leave it empty to take them for any change. A path may be a
   glob of one folder level each, such as `src/*/views`.
3. Optionally, set **Pages to always capture**, one per line: a path, then a label.

   ```
   /settings Settings
   /board Board
   ```

4. Optionally, set a **Screenshot setup command** (see below).

The project needs a **Start server command** (**Project settings → Commands**). Projects that
preview tasks in a terminal get no screenshots.

## Which pages

Claude lists the pages its change affects at the end of its summary, after a line
`Screenshot pages:`, one per line:

```
Screenshot pages:
- /settings — Settings → Board
- /p/app/history?tab=deploys — History → Deploys
```

Dash tells Claude to do this in every task of a project with screenshots on. Dash captures the
project's own pages first, then up to 4 that Claude lists, at most 6 in all. A path starts with
`/`; full URLs are ignored. With no pages listed and none set, nothing is captured, and the task
says so.

## What Dash does

Once the task's work is handed off (to Review, the merge queue or its pull request), and without
holding it up:

1. Dash runs the **Start server command** in the task's worktree, with `$PORT` set.
2. Once the server answers, it runs the **Screenshot setup command**, if there is one.
3. It opens each page in headless Chrome at 1280×800 and at a phone's 390×844, and keeps a
   picture of each. A long page is cut off after about two screens.
4. It does the same for the commit the task started from, in a throwaway worktree: the
   **Files to copy into worktrees** first, then the **Setup command**, then the server.

The pictures are small WebP files. They are kept with the task in Dash's data
folder (`screenshots/<task>/`) and deleted with the task. A task's next run replaces them.

The whole capture stops after 6 minutes. A server gets 3 minutes to answer, and a page 25 seconds.

## The screenshot setup command

It runs in the same folder as the server, once the server answers, with `$PORT` and
`$DASH_APP_URL` (`http://localhost:<port>`). Use it to seed data or make a test user. To sign the
browser in, print cookies, one per line:

```sh
./scripts/seed-demo.sh
echo "Set-Cookie: session=$(./scripts/test-login.sh)"
```

On a hub it is a project command like the others: a runner runs it only once an org admin or the
runner's owner approved it (see [Several computers](several-computers.md)).

## In Review

Open the task and choose **Changes**. Above the diff, each page shows its **Before** and
**After** picture.

- **Wide** and **Phone** switch the size.
- **Side by side** shows the two pictures next to each other. **Slider** puts one on top of the
  other: drag the slider to compare. On a phone, **Slider** is the default.
- The Review card shows a small picture of the first page. Click it to open the task's **Changes**.

The browser remembers the size and the view you picked.

## Mark up a screenshot

Click a picture (with **Slider**: **Draw on before** or **Draw on after**) to open it in
[Draw](draw.md). Mark it up, then press **Add to rework notes**, and send it with **Rework**, as
with a picture from the preview.

## When there are none

Screenshots never fail a task or hold it up. When they can't be taken, the task's **Changes** says
why in one line, for example:

- the computer that ran the task has no Chrome or Chromium. Install one, or set `DASH_CHROME` to
  its path, then restart Dash;
- the server didn't start or didn't answer on its port, with the last line of its log;
- a page timed out.

Pictures that were taken are still shown. The task's log has a line for each step, starting with 📸.
