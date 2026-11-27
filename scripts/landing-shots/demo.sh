#!/bin/sh
# Starts a demo Dash for the landing page's screenshots, the tutorial videos
# and the 90-second demo video, from the repository root: a hub, a runner
# with a stand-in for Claude Code (fakeclaude.mjs), and the acme-shop
# project, filled by seed.mjs. Everything lives in $DEMO_DIR (default
# /tmp/landing-demo), which is wiped first.
#
# SEED_SCENE picks seed.mjs's starting state (default landing). The
# demo-video scene (see README.md here) adds a second runner, three tasks
# at once on each, and live previews at <task>-<runner>.preview.dash.localhost.
# DEMO_CLAUDE=real runs the real Claude Code (`claude` on PATH, or set
# DEMO_CLAUDE to its path) instead of the stand-in; it signs in with
# CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token`) or ANTHROPIC_API_KEY,
# as the demo has a HOME of its own.
#
# gh is a stand-in (fakegh.mjs) that keeps pull requests in $DEMO_DIR, and
# https://github.com/ maps to the local gitserve too, so a project whose
# remote is https://github.com/alex/<repo>.git works in pull request mode
# with no network or account. DEMO_GAME=1 seeds the Star Catcher tutorial
# series' demo instead (seed-game.mjs; see scripts/tutorials/README.md).
#
# Needs go, git and node. Sign in at http://127.0.0.1:7791 (the demo video:
# http://dash.localhost:7791) as demo / demo-password-for-shots; stop it
# with: kill $(cat $DEMO_DIR/pids)
set -eu
here=$(cd "$(dirname "$0")" && pwd)
D=${DEMO_DIR:-/tmp/landing-demo}
hub=http://127.0.0.1:7791
scene=${SEED_SCENE:-landing}

claude=$here/fakeclaude.mjs
case ${DEMO_CLAUDE:-} in
'' | stand-in) ;;
real) claude=$(command -v claude) || { echo "demo.sh: DEMO_CLAUDE=real needs claude on PATH" >&2; exit 1; } ;;
*) claude=$DEMO_CLAUDE ;;
esac
if [ "$claude" != "$here/fakeclaude.mjs" ] && [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}${ANTHROPIC_API_KEY:-}" ]; then
	echo "demo.sh: the real Claude Code needs CLAUDE_CODE_OAUTH_TOKEN (run \`claude setup-token\`) or ANTHROPIC_API_KEY" >&2
	exit 1
fi

if [ -f "$D/pids" ]; then kill $(cat "$D/pids") 2>/dev/null || true; sleep 1; fi
rm -f "$D/hold"
rm -rf "$D"
mkdir -p "$D/bin" "$D/origin" "$D/projects" "$D/home"
go build -o "$D/bin/dash" ./cmd/dash
go build -o "$D/bin/gitserve" ./scripts/landing-shots/gitserve
cp "$here/fakegh.mjs" "$D/bin/gh"
chmod +x "$D/bin/gh"
# The demo's own home and git settings: nothing of yours is used.
export DEMO_DIR=$D HOME=$D/home GIT_CONFIG_GLOBAL=$D/gitconfig
export DASH_USERNAME=${DASH_USERNAME:-demo} DASH_PASSWORD=demo-password-for-shots
# DEMO_GAME: the Star Catcher tutorial series' demo (seed-game.mjs) instead
# of acme-shop: pull requests open ready for review, and deploys copy the
# game to $D/site.
# The game's site (site-server.mjs) is star-catcher.r8.rs: the runner
# reaches it through it as a proxy, the recording's Chrome over HTTPS; Dash is opened at dash.localhost, so previews get
# subdomains of preview.dash.localhost and Draw works.
hubflags=""
if [ -n "${DEMO_GAME:-}" ]; then
	export FAKE_GH_NO_DRAFTS=1 SITE_DIR=$D/site HTTP_PROXY=http://127.0.0.1:7795 NO_PROXY=127.0.0.1,localhost,.localhost
	# Follow pull requests every few seconds, so Merge pull request shows soon.
	export DASH_PR_POLL=3s
	hubflags="-preview-domain preview.dash.localhost"
fi
# The hub and the runner find the stand-in gh first.
export PATH="$D/bin:$PATH"
# What start-runner.sh needs to start the runner in this environment later.
cat > "$D/env" <<ENV
export DEMO_DIR='$D' HOME='$D/home' GIT_CONFIG_GLOBAL='$D/gitconfig' PATH='$PATH'
export DASH_USERNAME='$DASH_USERNAME' DASH_PASSWORD='$DASH_PASSWORD'
export FAKE_GH_NO_DRAFTS='${FAKE_GH_NO_DRAFTS:-}' SITE_DIR='${SITE_DIR:-}' HTTP_PROXY='${HTTP_PROXY:-}' NO_PROXY='${NO_PROXY:-}' DASH_PR_POLL='${DASH_PR_POLL:-}'
ENV

# The project's remote is a reserved example.com address, which git maps to
# the local gitserve; so are github.com remotes (bare repositories in
# $D/origin/<owner>/<repo>.git, which fakegh creates). Conflicts show the
# base too (diff3), which the stand-in needs to resolve them.
cat > "$GIT_CONFIG_GLOBAL" <<'CONFIG'
[user]
	name = Demo
	email = demo@example.com
[commit]
	gpgsign = false
[tag]
	gpgsign = false
[merge]
	conflictStyle = diff3
[url "https://127.0.0.1:7792/"]
	insteadOf = https://git.example.com/acme/
	insteadOf = https://github.com/
[http]
	sslVerify = false
CONFIG
sh "$here/repo.sh" "$D/seed"
git clone -q --bare "$D/seed" "$D/origin/acme-shop.git"
git -C "$D/origin/acme-shop.git" config http.receivepack true

# The demo video previews each task at <task>-<runner>.<domain>; browsers
# resolve every *.localhost name to this computer.
previews=
if [ "$scene" = demo-video ]; then
	previews="-preview-domain ${DEMO_PREVIEW_DOMAIN:-preview.dash.localhost}"
fi

cd "$D"
./bin/gitserve -root "$D/origin" -addr 127.0.0.1:7792 -backend "$(git --exec-path)/git-http-backend" > gitserve.log 2>&1 &
echo $! > pids
./bin/dash -addr 127.0.0.1:7791 -data "$D/data" -env /dev/null -name hub $previews $hubflags > hub.log 2>&1 &
echo $! >> pids
if [ -n "${DEMO_GAME:-}" ]; then
	mkdir -p "$SITE_DIR"
	openssl req -x509 -newkey rsa:2048 -nodes -keyout site.key -out site.crt -days 30 -subj /CN=star-catcher.r8.rs \
		-addext subjectAltName=DNS:star-catcher.r8.rs 2> /dev/null
	node "$here/site-server.mjs" "$SITE_DIR" 7795 7796 site.crt site.key > site.log 2>&1 &
	echo $! >> pids
fi
sleep 2
curl -sf -c cj -X POST $hub/api/login -d "{\"username\":\"$DASH_USERNAME\",\"password\":\"$DASH_PASSWORD\"}" > /dev/null
if [ "${SEED_SCENE:-}" = fresh ]; then
	# No runner yet: video 1 adds it (runner-hook.sh).
	node "$here/seed-game.mjs" $hub fresh
	echo "demo Dash at $hub (no runner)"
	exit 0
fi
if [ -n "${DEMO_GAME:-}" ]; then
	# The Star Catcher series: one runner, started the way video 1 does it.
	code=$(curl -sf -b cj -X POST $hub/api/runners/codes -d '{}' | sed 's/.*"code":"\([^"]*\)".*/\1/')
	sh "$here/start-runner.sh" "$code"
	node "$here/seed-game.mjs" $hub "$scene"
	echo "demo Dash at $hub (scene $scene)"
	exit 0
fi

# runner <name> <listen address>: registers and starts a runner, three
# tasks at once, and prints its id.
runner() {
	code=$(curl -sf -b cj -X POST $hub/api/runners/codes -d '{}' | sed 's/.*"code":"\([^"]*\)".*/\1/')
	./bin/dash runner -addr "$2" -data "$D/runner-$1" -projects "$D/projects/$1" -name "$1" -env /dev/null -parallel 3 \
		-claude "$claude" --server $hub --code "$code" > "runner-$1.log" 2>&1 &
	echo $! >> pids
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		sleep 1
		id=$(curl -sf -b cj $hub/api/runners | node -e "const r=JSON.parse(require('fs').readFileSync(0,'utf8')).runners.find((r)=>r.name==='$1');if(r)console.log(r.id)")
		[ -n "$id" ] && break
	done
	[ -n "$id" ] || { echo "demo.sh: runner $1 didn't register; see $D/runner-$1.log" >&2; exit 1; }
	# A small /tmp would warn of low disk space on every page.
	curl -sf -b cj -X PATCH $hub/api/runners/$id -d '{"disk":{"min_free_gb":0.1,"warn_percent":1,"warn_gb":0.2}}' > /dev/null
	echo "$id"
}
runners=\"$(runner studio 127.0.0.1:7793)\"
if [ "$scene" = demo-video ]; then
	runners="$runners,\"$(runner workshop 127.0.0.1:7794)\""
fi
sleep 2
project=$(curl -sf -b cj -X POST $hub/api/projects -d "{\"name\":\"acme-shop\",\"remote_url\":\"https://git.example.com/acme/acme-shop.git\",\"runners\":[$runners],\"test_command\":\"node --test\",\"run_command\":\"npm start\",\"merge_mode\":\"local\"}" | sed 's/^{"id":"\([^"]*\)".*/\1/')
node "$here/seed.mjs" $hub "$project" "$scene"
if [ "$scene" = demo-video ]; then hub=http://dash.localhost:7791; fi
echo "demo Dash at $hub (project $project)"
