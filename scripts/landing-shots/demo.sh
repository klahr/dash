#!/bin/sh
# Starts a demo Dash for the landing page's screenshots, from the repository
# root: a hub, one runner with a stand-in for Claude Code (fakeclaude.mjs),
# and the acme-shop project with tasks in every lane and a goal. Everything
# lives in $DEMO_DIR (default /tmp/landing-demo), which is wiped first.
# Needs go, git and node. Sign in at http://127.0.0.1:7791 as demo /
# demo-password-for-shots; stop it with: kill $(cat $DEMO_DIR/pids)
set -eu
here=$(cd "$(dirname "$0")" && pwd)
D=${DEMO_DIR:-/tmp/landing-demo}
hub=http://127.0.0.1:7791

if [ -f "$D/pids" ]; then kill $(cat "$D/pids") 2>/dev/null || true; sleep 1; fi
rm -f "$D/hold"
rm -rf "$D"
mkdir -p "$D/bin" "$D/origin" "$D/projects" "$D/home"
go build -o "$D/bin/dash" ./cmd/dash
go build -o "$D/bin/gitserve" ./scripts/landing-shots/gitserve
# The demo's own home and git settings: nothing of yours is used.
export DEMO_DIR=$D HOME=$D/home GIT_CONFIG_GLOBAL=$D/gitconfig
export DASH_USERNAME=demo DASH_PASSWORD=demo-password-for-shots

# The project's remote is a reserved example.com address, which git maps to
# the local gitserve.
cat > "$GIT_CONFIG_GLOBAL" <<'EOF'
[user]
	name = Demo
	email = demo@example.com
[commit]
	gpgsign = false
[tag]
	gpgsign = false
[url "https://127.0.0.1:7792/"]
	insteadOf = https://git.example.com/acme/
[http]
	sslVerify = false
EOF
sh "$here/repo.sh" "$D/seed"
git clone -q --bare "$D/seed" "$D/origin/acme-shop.git"
git -C "$D/origin/acme-shop.git" config http.receivepack true

cd "$D"
./bin/gitserve -root "$D/origin" -addr 127.0.0.1:7792 -backend "$(git --exec-path)/git-http-backend" > gitserve.log 2>&1 &
echo $! > pids
./bin/dash -addr 127.0.0.1:7791 -data "$D/data" -env /dev/null > hub.log 2>&1 &
echo $! >> pids
sleep 2
curl -sf -c cj -X POST $hub/api/login -d "{\"username\":\"$DASH_USERNAME\",\"password\":\"$DASH_PASSWORD\"}" > /dev/null
code=$(curl -sf -b cj -X POST $hub/api/runners/codes -d '{}' | sed 's/.*"code":"\([^"]*\)".*/\1/')
./bin/dash runner -data "$D/runner" -projects "$D/projects" -name studio -env /dev/null \
	-claude "$here/fakeclaude.mjs" --server $hub --code "$code" > runner.log 2>&1 &
echo $! >> pids
sleep 3
runner=$(curl -sf -b cj $hub/api/runners | sed 's/.*"runners":\[{"id":"\([^"]*\)".*/\1/')
# A small /tmp would warn of low disk space on every page.
curl -sf -b cj -X PATCH $hub/api/runners/$runner -d '{"disk":{"min_free_gb":0.1,"warn_percent":1,"warn_gb":0.2}}' > /dev/null
project=$(curl -sf -b cj -X POST $hub/api/projects -d "{\"name\":\"acme-shop\",\"remote_url\":\"https://git.example.com/acme/acme-shop.git\",\"runners\":[\"$runner\"],\"test_command\":\"node --test\",\"run_command\":\"node src/server.js\",\"merge_mode\":\"local\"}" | sed 's/^{"id":"\([^"]*\)".*/\1/')
node "$here/seed.mjs" $hub "$project"
echo "demo Dash at $hub (project $project)"
