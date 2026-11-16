# TODO

Most important first within each section.

## Next



## Quality

- [ ] Run an accessibility scan of the web UI (labels, focus, contrast, keyboard) and fix the findings.

## Features


## Running it


## Go live

- [ ] Turn on GitHub security for the repo: secret scanning with push protection, Dependabot
      alerts and security updates, and private vulnerability reporting (the README links to advisories).
- [ ] Production hub: 2FA on the admin account, scheduled `dash backup` with one restore
      tested, and an alert on `/healthz` and the metrics (`docs/deploy.md#monitoring`).
- [ ] If others will sign up on your hub: SMTP with SPF/DKIM set up for the sending domain
      (`docs/signup.md`), and Stripe in live mode with Tax, the customer portal and the webhook
      (`docs/billing.md`), tested with one real payment and a refund.
- [ ] Cut the launch release (`docs/releases.md`), check the archives and the
      `ghcr.io/klahr/dash` image, then set the repo's description, homepage and topics on GitHub.
