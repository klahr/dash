# TODO

Most important first within each section.

## Next

- [ ] Local only projects: always `git init` the new folder, never adopt an enclosing repository
      (a dotfiles `$HOME` makes `~/dash-projects/x` part of it). Fix `SeedRepo` and the delete check.
- [ ] Tell a member who has no runner, on the board and on each waiting card, that their tasks
      can't run, and what to do (add a runner, or ask for a shared one). Show admins who has a
      runner in **Organization → Members**.
- [ ] Planning **Start** by a planner: pick the developer who runs the task or owns the goal, so
      the work runs on a runner and the goal's plan can be accepted. Show the waiting reason on
      the card.
- [ ] Let owners rename the organization (it is "Default" for every first account).

## Quality

- [ ] Planning card status: don't count deleted work as done; give a stopped or out-of-budget
      goal its own words; use the same words for queued work in the line and the headline.
- [ ] An approved task whose conflict fix leaves no diff: close it as "already on main" instead
      of sending it back to Review.
- [ ] Waiting and error texts: report the owner rule first, no "no runner: no runner", no
      "bad request\n" prefix, no "any" shown as a label, no gh advice for other hosts.
- [ ] Docs: replace the old **Organization** page references for runners with **Runners**, and
      check every bold UI name against the app.
- [ ] Planning cards: answer 409 when two people edit the same card at once.

## Features

- [ ] A short "Set up a team" doc and an **Organization** checklist: name the org, invite,
      require 2FA, give each developer a runner or a shared runner, share projects by labels, what
      planners can do. Add a contents list to docs/several-computers.md.
- [ ] Notifications for Planning: mentions, card members, linked work in review or failed; send
      news about a planner-started task to the card's members.
- [ ] Team usage: tokens and cost by member and project, per week, for owners and admins.
- [ ] Planning for multi-week work: a "mine" filter, target as a filterable milestone, several
      tag filters, one owner among a card's members.
- [ ] Let planners start **Write a project brief** from a card: the interview on a shared runner,
      the brief kept on the card for a developer to launch.

## Running it

- [ ] A supported way to give a service-installed shared runner its `ANTHROPIC_API_KEY` (an env
      file `dash runner install` keeps).

## Go live

- [ ] Turn on GitHub security for the repo: secret scanning with push protection, Dependabot
      alerts and security updates, and private vulnerability reporting (the README links to advisories).
