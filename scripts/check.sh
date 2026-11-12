#!/bin/sh
# Runs CI's Go and JavaScript checks: gofmt, go vet, staticcheck, the tests
# and node --check. Use it as the Dash project's test command, so a task is
# checked in its worktree before Dash merges it:
#
#   ./scripts/check.sh
#
# Set DASH_CHECK_SHORT=1 to run `go test -short` (skips the end-to-end tests).
set -eu
cd "$(dirname "$0")/.."

echo "== gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "These files need gofmt:"
	echo "$unformatted"
	exit 1
fi

echo "== go vet"
go vet ./...

echo "== staticcheck"
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./... # the version CI pins

echo "== go test"
if [ "${DASH_CHECK_SHORT:-}" = 1 ]; then
	go test -short ./...
else
	go test ./...
fi

if command -v node >/dev/null 2>&1; then
	echo "== node --check"
	for f in web/static/*.js web/static/app/*.js; do
		node --check "$f"
	done
fi

echo "All checks passed."
