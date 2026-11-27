#!/bin/sh
# Starts the demo's runner "studio" with a one-time code, for demo.sh and
# runner-hook.sh (video 1), and waits for it to connect. It takes the demo's
# environment from $DEMO_DIR/env, which demo.sh writes.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
D=${DEMO_DIR:?}
. "$D/env"
unset DASH_URL
hub=http://127.0.0.1:7791
cd "$D"
# setsid: it outlives whoever started it (the recorder runs runner-hook.sh).
# macOS has no setsid; nohup does the same job there.
# -codex: a path with nothing there, so a Codex CLI on this computer isn't
# offered (or shown, logged out) by the demo's runner.
detach=nohup
command -v setsid > /dev/null && detach=setsid
$detach ./bin/dash runner -data "$D/runner" -projects "$D/projects" -name studio -env /dev/null \
	-claude "$here/fakeclaude.mjs" -codex "$D/bin/no-codex" --server $hub --code "$1" > runner.log 2>&1 < /dev/null &
echo $! >> pids
for _ in $(seq 60); do
	runner=$(curl -sf -b cj $hub/api/runners | sed -n 's/.*"runners":\[{"id":"\([^"]*\)".*/\1/p')
	[ -n "$runner" ] && break
	sleep 0.5
done
if [ -z "$runner" ]; then
	echo "start-runner: the runner didn't register (code $1). Runners: $(curl -s -b cj $hub/api/runners | head -c 300)" >&2
	tail -5 runner.log >&2
	cp runner.log runner-failed.log
	exit 1
fi
# A small /tmp would warn of low disk space on every page.
curl -sf -b cj -X PATCH $hub/api/runners/$runner -d '{"disk":{"min_free_gb":0.1,"warn_percent":1,"warn_gb":0.2}}' > /dev/null
