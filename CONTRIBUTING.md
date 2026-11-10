# Contributing to Dash

Thanks for helping. Bug reports, fixes, docs and features are all welcome. For anything bigger
than a small fix, open an issue first so we can agree on the approach before you write it.

## Contributor License Agreement

Every contribution needs a signed [Contributor License Agreement](CLA.md). You keep the
copyright in your work; the CLA gives the project owner a license to use it, including under
licenses other than AGPL-3.0 (Dash is AGPL-3.0 today, and a paid hosted version is planned).

You sign once, on your first pull request: the CLA assistant comments on it, and you reply with

```
I have read the CLA Document and I hereby sign the CLA
```

Your signature is recorded in the repository and covers your later pull requests too. A pull
request can't be merged until everyone who committed to it has signed. If the check doesn't
update, comment `recheck`.

## Build

You need git and Go (the version in [`go.mod`](go.mod)). Node is not needed to build.

```sh
cp .env.example .env          # set DASH_USERNAME and DASH_PASSWORD
go build -o bin/dash ./cmd/dash
go build -o bin/dash-tui ./cmd/dash-tui
go build -o bin/dash-mcp ./cmd/dash-mcp
./bin/dash
```

Then open <http://127.0.0.1:7788>. See the [README](README.md) for the rest of the setup.

The web client in [`web/static`](web/static) is plain JavaScript modules, embedded in the
binary; rebuild `dash` to see your changes. The one bundled dependency, CodeMirror, is built
from [`web/editor-bundle`](web/editor-bundle) and its output is committed; only rebuild it
(`npm ci && npm run build` there) when you change `entry.js` or its dependencies.

## Test

Run what [CI](.github/workflows/ci.yml) runs before you open a pull request:

```sh
gofmt -l .                                  # must print nothing
go mod tidy && git diff --exit-code go.mod go.sum
go vet ./...
go build ./...
go test -race ./...
for f in web/static/*.js; do node --check "$f"; done
node --test web/                            # also run by go test, when node is installed
```

For a quick loop while you work, `go test -short ./...` skips the end-to-end tests in
[`cmd/dash`](cmd/dash) (the `TestHub…` and `TestBrowser…` tests, which build dash and run a
real hub and runner). Run the full suite, without `-short`, before you open a pull request; CI
always does.

Tests live next to the code they test (`foo.go` and `foo_test.go`) and use the standard
`testing` package. They must not depend on your own setup: no network, no real Claude, no
GitHub. Tests that run git create throwaway repositories in `t.TempDir()`; see
[`internal/queue/main_test.go`](internal/queue/main_test.go) for how they keep your git config
(such as commit signing) out of the way. For UI changes, also try them in the browser, and on a
narrow phone-sized window.

[`cmd/dash/browser_test.go`](cmd/dash/browser_test.go) clicks through the web client's main
flows (sign in, new task, review, rework with comments, approve and merge, drag and drop,
permission approval) in headless Chrome, against a real hub and runner with
[`testdata/fakeclaude`](cmd/dash/testdata/fakeclaude/main.go) for Claude Code. It uses the
Chrome or Chromium on your `PATH`, or `DASH_TEST_CHROME=/path/to/chrome`, and is skipped
without one (CI sets `DASH_TEST_BROWSER=require` so it can't be skipped there). Run it alone
with `go test -run TestBrowserMainFlows -v ./cmd/dash`; when a step fails it logs the page's
text and saves a screenshot.

## Code style

- **Go:** `gofmt`, and `go vet` clean. Standard library first; add a dependency only when it
  clearly pays for itself, and keep `go.mod` tidy. Builds are static (CGO off), so no cgo
  dependencies (SQLite is `modernc.org/sqlite`).
- **JavaScript:** plain ES modules in `web/static`, no framework and no build step. Keep it
  working without a bundler.
- **Comments** say why, not what: the reason for a choice, a constraint, or a trap, in full
  sentences. Every package and non-obvious function has one.
- **Small and direct.** Prefer the simplest code that works over abstractions for later. Match
  the style of the code around you.
- **Docs** in [`docs`](docs) and the README are short and plain: say what to do, with commands
  that can be copied. Update them in the same pull request when you change behaviour, flags or
  setup.
- **Security:** Dash runs commands on people's computers. Treat anything from the network as
  untrusted, keep data scoped to its organization, and don't log secrets.

## Pull requests

1. Fork the repository and branch from `main`.
2. Keep each pull request to one change. Unrelated fixes go in their own pull request.
3. Add or update tests and docs for what you change.
4. Make sure the checks above pass.
5. Write a short title that says what the change does, as in the existing history (for
   example "TOTP two-factor login" or "Org scoping of all data and live state"), and say in the
   description why, and how you tested it.
6. Sign the [CLA](CLA.md) when the assistant asks.

Pull requests are squashed into one commit on `main` when they're merged.

## Reporting security issues

Please don't open a public issue for a vulnerability. Use GitHub's
[private vulnerability reporting](https://github.com/klahr/dash/security/advisories/new)
instead.

## For maintainers

The [CLA workflow](.github/workflows/cla.yml) stores signatures in `signatures/cla.json` on the
`cla-signatures` branch, so signing never needs a commit on a protected `main`. Create that
branch once before the first outside pull request:

```sh
git push origin main:cla-signatures
```

The repository owner and bot accounts (`*[bot]`) are exempt.
