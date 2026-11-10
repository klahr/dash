# TODO

Most important first within each section.

## Next


## Quality

- [ ] Split `web/static/app.js` (~2,000 lines) into modules.
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

## Running it

- [x] Docker image for the hub (`Dockerfile`, `ghcr.io/klahr/dash`) and docker-compose
      (`deploy/docker-compose.yml`); git, gh and Claude Code run on the runners.
- [x] Optional 2FA (TOTP) for the login.
- [ ] Optional sandbox for Claude's runs (container or user with limited rights).

## Go live

- [ ] Replace the hard-coded checkout path `%h/dev/github/klahr/dash` in `deploy/dash.service`
      (WorkingDirectory, ExecStart) with a neutral one, and update `docs/service.md` to match.
- [ ] Replace the `dashhost.dev` example domain in `docs/deploy.md` with a reserved one (`example.dev`).
- [ ] Turn on GitHub security for the repo: secret scanning with push protection, Dependabot
      alerts and security updates, and private vulnerability reporting (the README links to advisories).
- [ ] Protect `main`: require the CI and CLA checks to pass before merging.
- [ ] Do a clean install from the docs alone, on a fresh VPS with Caddy and with Docker
      (`docs/deploy.md`), and fix every step that trips you up.
- [ ] Production hub: 2FA on the admin account, scheduled `dash backup` with one restore
      tested, and an alert on `/healthz` and the metrics (`docs/deploy.md#monitoring`).
- [ ] If others will sign up on your hub: SMTP with SPF/DKIM set up for the sending domain
      (`docs/signup.md`), and Stripe in live mode with Tax, the customer portal and the webhook
      (`docs/billing.md`), tested with one real payment and a refund.
- [ ] If others will sign up on your hub: publish terms of service, a privacy policy (GDPR:
      what you store, where, for how long) and a contact address.
- [ ] Cut the launch release (`docs/releases.md`), check the archives and the
      `ghcr.io/klahr/dash` image, then set the repo's description, homepage and topics on GitHub.
