# TODO

Most important first within each section.

## Next

- [ ] Add a `CLAUDE.md` with the project rules: run `gofmt -l .`, `go vet ./...`, staticcheck and
      `go test ./...` before finishing; follow the docs style and the two-pane UI pattern.


## Quality

- [ ] Try PR mode and failed-build investigation against a real GitHub repository.
- [ ] Run an accessibility scan of the web UI (labels, focus, contrast, keyboard) and fix the findings.

## Features

- [x] Todos that wait for another todo to merge.
- [ ] Run the same task twice and compare the attempts.

## Running it

- [ ] Optional sandbox for Claude's runs (container or user with limited rights).

## Go live

- [x] Replace the hard-coded checkout path `%h/dev/github/klahr/dash` in `deploy/dash.service`
      (WorkingDirectory, ExecStart) with a neutral one, and update `docs/service.md` to match.
- [x] Replace the `dashhost.dev` example domain in `docs/deploy.md` with a reserved one (`example.dev`).
- [ ] Turn on GitHub security for the repo: secret scanning with push protection, Dependabot
      alerts and security updates, and private vulnerability reporting (the README links to advisories).
- [ ] Production hub: 2FA on the admin account, scheduled `dash backup` with one restore
      tested, and an alert on `/healthz` and the metrics (`docs/deploy.md#monitoring`).
- [ ] If others will sign up on your hub: SMTP with SPF/DKIM set up for the sending domain
      (`docs/signup.md`), and Stripe in live mode with Tax, the customer portal and the webhook
      (`docs/billing.md`), tested with one real payment and a refund.
- [ ] Cut the launch release (`docs/releases.md`), check the archives and the
      `ghcr.io/klahr/dash` image, then set the repo's description, homepage and topics on GitHub.
