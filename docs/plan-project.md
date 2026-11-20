# Write a project brief

Start from an idea instead of a repository. Claude interviews you about it, a few questions at a
time, and writes a project brief as you answer. It asks about the decisions you haven't made yet,
so you don't have to write a long prompt. When the brief is ready, Dash creates the project,
commits the brief and starts a [goal](goals.md) from it.

The same guide writes a brief for a feature of a project you have, onto a
[Planning](planning.md) card. See [Write a feature brief](#write-a-feature-brief).

[Planners](users.md#planners) can write a feature brief, on a runner of their own (see
[A planner's runner](several-computers.md#a-planners-runner)). They can't write a project brief:
Launch adds a project, which planners can't.

## Start a brief

1. Click **+ Add project** and pick **Write a project brief** at the top of the list.
2. Click **New brief**.
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
2. Fill it in and click **Use for the brief**.
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

A brief is saved as you go. Pick it again from the **Drafts** list under **Write a project brief**
to carry on where you left off, or delete it there. The repository is picked again after a
reload. A launched brief leaves the list.

A draft is yours: nobody else in the organization sees it, admins included. Leaving the
organization deletes your drafts there.

## Where Claude runs

The interview runs like Dash's other short calls without a project (titles, splitting a task): on
the first of your own runners that is online, with that computer's `claude` login. The idea step
says which. With none of your runners online, start `dash runner` on your computer.

## Write a feature brief

For a feature of a project you have, the guide writes the brief onto a [Planning](planning.md)
card, and can start a task or a goal from it. The interview, the brief and the drafts work as
above. Start it from any of these:

- On a card in **Planning**: **Write a feature brief**. The card's description is the idea. A
  long description is the brief Claude starts from.
- In **Planning**: **New card**, type the idea, then **Write a feature brief**. The guide adds
  the card when you start the interview.
- In **New task** or the full task form: **Write a feature brief**. What you typed is the idea,
  and the guide adds a card for it.

The steps are **Idea**, **Interview**, **Brief** and **Start**. On **Idea**, pick the project if
it isn't set. The topics are: the problem and who it's for, behaviour and scope, where it shows
(screens, commands, API), data changes, edge cases and errors, done when, and out of scope.

With each round, Claude also reads the project's `CLAUDE.md`, README and file list from its
default branch, cut to a fixed size. It doesn't ask what those already answer. They are read on
one of the project's runners; when none is online, the interview goes on without them. The
interview itself runs on your own runner, as above; update Dash on it if it says it is from
before feature briefs. A planner's rounds read the same, unless an owner turned that off in
**Organization → Planners**: then they get only the card and the project's name.

On **Start**, pick one:

- **A task**: queued for Claude now.
- **A goal**: a coordinator breaks it into tasks. See [Goals](goals.md).
- **Save to Planned**: nothing starts yet. Start it from the card later.

A planner has **A task** and **Save to Planned**. The task runs for a developer, as **Start…**
does. To start a goal, save to Planned, then use **Start…** on the card to choose its owner.

Dash puts the brief on the card as its description (a new card in **Ideas** if the card was
deleted), then starts the task or goal the way **Start…** on a card does: it is linked to the
card, and the card moves to **In progress**. **Save to Planned** moves a card in **Ideas** to
**Planned**. Nothing is committed to the repository.

A feature's draft is listed under **All briefs** in the **Write a feature brief** sheet, as
"Feature · project name". Opening the guide on a card that has a draft opens that draft.

## API

| Endpoint | What it does |
| --- | --- |
| `GET /api/plans` | your drafts in the org |
| `POST /api/plans` | make a plan: `{"idea", "name"}`. A feature's: `{"kind": "feature", "project", "card"}` with `idea` and `name`, or without them to take them from the card. Without a card, it adds one |
| `GET /api/plans/{id}` | a plan, with its rounds, topics, brief, open questions and verdict |
| `PATCH /api/plans/{id}` | change `idea`, `name`, `brief` or `step` |
| `DELETE /api/plans/{id}` | delete a plan |
| `POST /api/plans/{id}/interview` | answer the latest round, `{"answers": [{"question", "text", "default"}], "message"}`, and get Claude's next one; `{}` starts the interview. An empty `text` skips a question |
| `GET /api/plans/{id}/launch` | `BRIEF.md`, the starter `CLAUDE.md` and the goal's title and description |
| `POST /api/plans/{id}/launched` | mark it launched: `{"project_id", "goal_id"}` |
| `POST /api/plans/{id}/feature` | a feature's Start: `{"start": "task"}`, `"goal"` or `"planned"`. Answers `{"plan", "card"}` |
| `POST /api/projects/{id}/brief` | commit `{"brief", "claude_md"}` as `BRIEF.md` and `CLAUDE.md` on the default branch |
