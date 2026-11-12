# Dash: rules for Claude

Read [CONTRIBUTING.md](CONTRIBUTING.md) for the build, the tests and the code style. These are
the rules to follow on every change.

## Before you finish

Run these from the repository root, and fix what they report:

```sh
gofmt -l .                                              # must print nothing
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...  # the version CI pins
go test ./...
```

`go test -short ./...` is fine while you work; run the full `go test ./...` before you finish.
When you change JavaScript in `web/static`, also run
`for f in web/static/*.js web/static/app/*.js; do node --check "$f"; done`.
Say in your summary which checks you ran and what they printed. If one can't run here (no
network for staticcheck, no Chrome for the browser tests), say so.

## Docs style

Docs in [`docs`](docs) and the README are short and plain:

- Say what to do, in short sentences. Commands are in code blocks that can be copied as they are.
- Name UI places in bold, as the app shows them: **Project settings → Board**.
- Example domains are reserved ones (`example.com`, `example.dev`), never real ones.
- Update the docs in the same change when you change behaviour, flags or setup.

## UI: the two-pane pattern

A larger sheet (Settings, Account, Organization, Runners, Add project) is a **rail sheet**: its
sections down a rail on the left, the chosen one beside it; on a phone the rail becomes a strip
across the top. Don't build a new layout for a sheet like this: use the same markup
(`dialog.sheet.rail-sheet`, `.rail-layout`, `.rail-nav` with `.rail-item[data-rail]`,
`.rail-main` with `.rail-section[data-section]`), which `railLayout` in
[`web/static/app/dialogs.js`](web/static/app/dialogs.js) wires up (keys, focus, hidden sections)
and the "rail layout" block in [`web/static/style.css`](web/static/style.css) styles. Copy an
existing sheet in [`web/static/index.html`](web/static/index.html).

A page with a list and a detail opens the detail in a pane beside the list (full screen on a
phone, with Back), as History's commit pane does; `Esc` closes it.

Try UI changes in a narrow, phone-sized window as well as a wide one.
