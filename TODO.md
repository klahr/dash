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
- [ ] Detect Claude Code CLI changes: check the version and warn when the stream-json output,
      `rate_limit_event` or `--permission-prompt-tool` no longer look as expected.

## Quality

- [ ] Split `web/static/app.js` (~2,000 lines) into modules.
- [ ] Browser tests for the main flows: new todo, review, rework with comments, approve,
      drag and drop, permission approval.
- [ ] More TUI tests (7% coverage) and API handler tests (18%).
- [ ] Back up `state.json`, and cap or rotate todo logs.
- [ ] Try PR mode and failed-build investigation against a real GitHub repository.

## Features

- [ ] Choose the model per todo (e.g. Opus for hard tasks, Sonnet for small ones).
- [ ] Todos that wait for another todo to merge.
- [ ] Split a large task into several todos (Claude proposes, you confirm).
- [ ] Run the same task twice and compare the attempts.
- [ ] Edit a diff in the UI before approving.
- [ ] Autosave the TUI todo form like the web editor.
- [ ] Find other Dash computers on the tailnet automatically instead of `-peer` flags.
- [ ] `-peer-proxy`, to reach peers through a second (userspace) Tailscale instance.

## Running it

- [ ] Docker image (Dash, git, gh, Claude Code, common toolchains) and docker-compose with a
      Tailscale sidecar and volumes for repos, data, Claude login and gh config.
- [ ] Optional sandbox for Claude's runs (container or user with limited rights).
- [ ] Optional 2FA (TOTP) for the login.
