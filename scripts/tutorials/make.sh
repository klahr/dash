#!/bin/sh
# Makes a narrated tutorial video of Dash from a script (see README.md):
#
#   scripts/tutorials/make.sh scripts/tutorials/first-task.yaml [--no-voice]
#   scripts/tutorials/make.sh --brand-preview "<title>" [--next "<title>"]
#
# It makes the narration, starts the demo Dash (scripts/landing-shots/demo.sh)
# with the script's seed scene, records it, stops it and writes
# scripts/tutorials/out/<name>.mp4 and .srt. --brand-preview renders only
# the intro and outro (with stills) to out/.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
cd "$root"

for tool in node ffmpeg ffprobe; do
	command -v "$tool" > /dev/null || { echo "make.sh: $tool is needed" >&2; exit 1; }
done

if [ "${1:-}" = --brand-preview ]; then
	[ $# -ge 2 ] || { echo "usage: make.sh --brand-preview \"<title>\" [--next \"<title>\"]" >&2; exit 2; }
	shift
	exec node "$here/tutorial.mjs" brand "$@"
fi

[ $# -ge 1 ] || { echo "usage: make.sh <script> [--no-voice] | --brand-preview \"<title>\"" >&2; exit 2; }
script=$1
shift
command -v go > /dev/null || { echo "make.sh: go is needed for the demo Dash" >&2; exit 1; }

# The narration first: it fails fast without a key, before the demo starts.
node "$here/tutorial.mjs" voice "$script" "$@"

export DEMO_DIR="${DEMO_DIR:-/tmp/tutorial-demo}"
stop() { [ -f "$DEMO_DIR/pids" ] && kill $(cat "$DEMO_DIR/pids") 2> /dev/null || true; }
trap stop EXIT INT TERM
echo "starting the demo Dash in $DEMO_DIR" >&2
SEED_SCENE=$(node "$here/tutorial.mjs" scene "$script") sh "$root/scripts/landing-shots/demo.sh" > "$DEMO_DIR.log" 2>&1 ||
	{ echo "make.sh: the demo didn't start; see $DEMO_DIR.log" >&2; exit 1; }
node "$here/tutorial.mjs" make "$script" "$@"
