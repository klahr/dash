#!/bin/sh
# Video 1's terminal (scripts/tutorials/series/01-runner.yaml): registers the
# demo's runner with the one-time code from the Add a runner pane, the way the
# install command would, and prints what the installer says. TERMINAL_FROM is
# that pane's command; DEMO_DIR is the demo (demo.sh).
set -eu
D=${DEMO_DIR:?}
code=$(printf '%s' "${TERMINAL_FROM:-}" | sed -n 's/.*--code \([A-Z0-9-]*\).*/\1/p')
[ -n "$code" ] || { echo "runner-hook: no code in: ${TERMINAL_FROM:-}" >&2; exit 1; }
sh "$(dirname "$0")/start-runner.sh" "$code"
echo "✓ dash installed in ~/.local/bin"
echo "✓ git, claude (logged in), https://dash.r8.rs"
echo "✓ registered as studio"
echo "✓ dash-runner.service started"
