# TODO

Most important first within each section.

## Next

- [ ] Use Dash for a week on a real repo with real Claude Code and GitHub; fix what breaks.
      Only stand-ins have exercised: PR mode, failed-build investigation, several computers,
      run limits.
- [ ] Push notifications (Web Push) for ready for review, failed, approval needed and failed
      build, so the installed app notifies you when it's closed.
- [ ] One merge worker per project, so a long test run in one project doesn't hold up merges
      in the others.

## Quality

- [ ] Split `web/static/app.js` (~2,000 lines) into modules.
- [x] Browser tests for the main flows: new todo, review, rework with comments, approve,
      drag and drop, permission approval.
- [ ] More TUI tests (10% coverage) and API handler tests (69%; authorization is covered table-driven in internal/api/orgs_test.go and authz_test.go — add new endpoints to recordEndpoints or orgEndpoints there).
- [x] Back up `dash.db` (`dash backup`, `-backup-dir`), and cap or rotate todo logs (`-log-max-mb`).
- [ ] Try PR mode and failed-build investigation against a real GitHub repository.

## Features

- [ ] Choose the model per todo (e.g. Opus for hard tasks, Sonnet for small ones).
- [ ] Personal notification webhooks (one user's, across their projects) besides the per-project
      ones, and a delivery history per webhook rather than only the last result.
- [ ] Todos that wait for another todo to merge.
- [ ] Split a large task into several todos (Claude proposes, you confirm).
- [ ] Run the same task twice and compare the attempts.
- [ ] Edit a diff in the UI before approving.
- [ ] Autosave the TUI todo form like the web editor.

## Running it

- [x] Docker image for the hub (`Dockerfile`, `ghcr.io/klahr/dash`) and docker-compose
      (`deploy/docker-compose.yml`); git, gh and Claude Code run on the runners.
- [x] Optional 2FA (TOTP) for the login.
- [ ] Optional sandbox for Claude's runs (container or user with limited rights).
