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
# On Windows the test cache costs more than it saves: the tests run git
# thousands of times, each run logs a stat for every PATH entry and
# extension, and go re-checks every logged path (slowly, on Windows) before
# it trusts a cached result. That took over 20 minutes; running afresh takes 3.
nocache=
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) nocache=-count=1 ;; esac
if [ "${DASH_CHECK_SHORT:-}" = 1 ]; then
	go test $nocache -short ./...
else
	go test $nocache ./...
fi

if command -v node >/dev/null 2>&1; then
	echo "== node --check"
	for f in web/static/*.js web/static/app/*.js scripts/landing-shots/*.mjs scripts/tutorials/*.mjs scripts/tutorials/lib/*.mjs; do
		node --check "$f"
	done
fi

echo "All checks passed."
