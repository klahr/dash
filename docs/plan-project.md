# Plan a new project

Start from an idea instead of a repository. Claude interviews you about it, a few questions at a
time, and writes a project brief as you answer. It asks about the decisions you haven't made yet,
so you don't have to write a long prompt. When the brief is ready, Dash creates the project,
commits the brief and starts a [goal](goals.md) from it.

## Start a plan

1. Click **+ Add project** and pick **Plan a new project** at the top of the list.
2. Click **New plan**.
3. Describe the idea in a sentence or two. A project name is optional.
4. Click **Start the interview**.

A bar across the top shows the five steps: **Idea**, **Interview**, **Brief**, **Repository** and
**Launch** (on a phone, a line such as "Step 2 of 5 · Interview"). Click a finished step to go
back to it.

## The interview

Claude asks two to four questions at a time, by topic: users and purpose, core features, data and
storage, platform and stack, auth and accounts, deployment and hosting, and what is out of scope.
The topics above the questions show which are done, open or skipped.

For each question:

- click Claude's suggestion (marked with a spark) or another option,
- or type your own answer,
- or click **Decide later**.

**Use all suggestions** answers every question with Claude's suggestion. Write anything else in
the box under the questions, then click **Send answers** or press <kbd>Enter</kbd>. A question you
don't answer is left for later, and the brief marks it open.

When your answers leave out a decision the project needs, such as login, Claude says so and asks
about it ("You didn't mention login: single user or accounts?"). Those questions are marked
**Not mentioned yet**.

The brief fills in beside the questions as you answer (on a phone, under the **Brief** tab).
Open decisions are marked **Open**. When Claude has no more questions, it says so.

## The brief

Click **Review the brief**. At the top, Claude says whether the brief is **Enough to start**, and
lists what can wait. Edit the brief as Markdown; it saves as you type. **Preview** shows it
rendered. Going back to the interview sends Claude your edited brief.

## Repository and launch

1. Click **Choose the repository** and pick a source: **From GitHub**, **New GitHub repository**,
   **From URL** or **Local only**. These are the same as for any [new
   project](several-computers.md#add-project-on-the-hub); the name is filled in from the plan.
2. Fill it in and click **Use for the plan**.
3. Check the summary on **Launch**. **BRIEF.md** and **CLAUDE.md** show what will be committed.
4. Click **Launch**.

Dash then:

1. adds the project, and the runner clones it,
2. commits `BRIEF.md` and a starter `CLAUDE.md` to its default branch (a file that is already
   there is kept),
3. starts a goal, "Build the first version of …", in **ask before queuing** mode, and opens it.

The starter `CLAUDE.md` takes the stack and the commands to run and check the project from the
brief, and adds conventions every task follows. Edit both files like any other.

If the files can't be committed (the project lands changes through pull requests, or the runner
hasn't cloned it yet), the goal is asked to add them in its first task.

The goal's coordinator reads the brief and proposes the first tasks for you to check, as for any
[goal](goals.md).

## Drafts

A plan is saved as you go. Pick it again from the **Drafts** list under **Plan a new project** to
carry on where you left off, or delete it there. The repository is picked again after a reload.
A launched plan leaves the list.

A plan is yours: nobody else in the organization sees it, admins included. Leaving the
organization deletes your plans there.

## Where Claude runs

The interview runs like Dash's other short calls without a project (titles, splitting a task): on
the first of your own runners that is online, with that computer's `claude` login. The idea step
says which. With none of your runners online, start `dash runner` on your computer.

## API

| Endpoint | What it does |
| --- | --- |
| `GET /api/plans` | your drafts in the org |
| `POST /api/plans` | make a plan: `{"idea", "name"}` |
| `GET /api/plans/{id}` | a plan, with its rounds, topics, brief, open questions and verdict |
| `PATCH /api/plans/{id}` | change `idea`, `name`, `brief` or `step` |
| `DELETE /api/plans/{id}` | delete a plan |
| `POST /api/plans/{id}/interview` | answer the latest round, `{"answers": [{"question", "text", "default"}], "message"}`, and get Claude's next one; `{}` starts the interview. An empty `text` skips a question |
| `GET /api/plans/{id}/launch` | `BRIEF.md`, the starter `CLAUDE.md` and the goal's title and description |
| `POST /api/plans/{id}/launched` | mark it launched: `{"project_id", "goal_id"}` |
| `POST /api/projects/{id}/brief` | commit `{"brief", "claude_md"}` as `BRIEF.md` and `CLAUDE.md` on the default branch |
