# The demo Dash

A demo Dash on your computer, for the landing page's screenshots, the
[tutorial videos](../tutorials) and the 90-second demo video. Nothing of yours is used: the demo
has its own HOME and git settings, the project's remote is `git.example.com`, and every address is
at `example.com`.

| File | What it is |
|---|---|
| `demo.sh` | Starts the demo: a hub, its runners and the acme-shop project. |
| `repo.sh` | Makes acme-shop, a small web shop. |
| `seed.mjs` | Fills the board for a scene (`SEED_SCENE`). |
| `video-tasks.mjs` | The demo video's five tasks. |
| `fakeclaude.mjs` | A stand-in for Claude Code, with a canned change for each demo task. |
| `preview.mjs` | Starts a task's live preview on its runner. |
| `gitserve` | Serves the project's remote over HTTPS. |
| `shoot.mjs`, `make.sh` | Take the landing page's screenshots. |

You need Go, git and Node 22 or later.

## The demo video

### Start it

From the repository root:

```sh
DEMO_DIR=/tmp/video-demo SEED_SCENE=demo-video scripts/landing-shots/demo.sh
```

Then open <http://dash.localhost:7791> and sign in as `demo` with the password
`demo-password-for-shots`. Use `dash.localhost`, not `127.0.0.1`: the live previews are at
`<task>-<runner>.preview.dash.localhost:7791`, and their sign-in only works from a Dash on the same
site. Browsers send every `*.localhost` name to your own computer, so there is no DNS to set up.

You get:

- two runners, **studio** and **workshop**, with three tasks at once each;
- the **acme-shop** project, started with `npm start` (on `$PORT`) and tested with `node --test`;
- five drafts: *Add dark mode*, *Validate the webhook URL*, *Fix the date bug in exports*, *Add tests
  for the billing page* and *Update the README*.

To start with the five already queued, add `SEED_QUEUE=1`.

What is planted in acme-shop for them:

- The webhook settings page (`/settings/webhooks`) saves any URL, and its **Save** button sits lower
  than the URL field. The task's prompt doesn't mention the button and asks to keep the layout, so
  the flaw is still there in review.
- The orders export (`/export/orders.csv`) writes every date a month early (`2026-00-14`).
- The billing page (`/billing`) has no tests.
- The README says Node 14, Yarn, Jest and port 3000, and lists PayPal and gift cards.
- `public/style.css` has hard-coded colours, ready for a dark theme.
- *Add dark mode* and *Update the README* must both change the README's line
  `- Light theme only: dark mode is planned`. Whichever merges second conflicts, every time.

### The shot list

1. **Queue five tasks.** Open **Drafts**, and press **Queue** on each of the five. They start at once:
   three on one runner, two on the other. The cards of *Add dark mode* and *Update the README* show
   that both change `README.md`.
2. **Review with a live preview.** When *Validate the webhook URL* is in **Review**, open it and
   press **Preview**, go to **Webhooks**, press **Draw** (<kbd>P</kbd>), circle the **Save** button,
   **Add to rework notes**, write "Line the Save button up with the URL field", and press
   **Rework…**. See [Preview on a runner](#preview-on-a-runner) first: this step doesn't work yet.
3. **Merge three.** Approve *Add dark mode*, then *Update the README*, then *Fix the date bug in
   exports*. Open **Merging**: dark mode lands, the README task conflicts in `README.md` and goes back
   to Claude, then lands with both changes. The date fix lands too.
4. **The rework comes back.** *Validate the webhook URL* returns to **Review** with the button lined
   up. Preview it again if you like, and **Approve** it. Approve *Add tests for the billing page* too,
   on camera or off.
5. **The Wall view.** Open <http://dash.localhost:7791/wall>: the merges, the runners and the spend.

Record with a 1920×1080 window and speed the real runs up in the edit.

### Preview on a runner

Today the hub's task pane offers no **Preview**, and so no **Draw**, for a task that runs on a
runner, and rework notes can't carry a picture to a runner. Every task of the demo runs on a runner,
so step 2 can't be filmed as written until that is fixed in Dash.

Until then, start the preview from a terminal and open it in a browser tab:

```sh
node scripts/landing-shots/preview.mjs "Validate the webhook URL"
```

It prints the preview's address, e.g. `http://7785d2058ddb-workshop.preview.dash.localhost:7791/`.
Add `settings/webhooks` to it to see the low **Save** button. Then send the task back with
**Rework…** and the note "Line the Save button up with the URL field".

### Reset between takes

Run the start command again. `demo.sh` stops the old demo, deletes `$DEMO_DIR` and starts over from
the same seed. Stop it with:

```sh
kill $(cat /tmp/video-demo/pids)
```

### Real Claude or the stand-in

The stand-in, `fakeclaude.mjs`, is the default. Use it to rehearse the camera moves: each task works
for about 15 seconds, the README conflict happens and is resolved, and the webhook task leaves the
button low until it is reworked with any note. `FAKECLAUDE_PACE_MS` sets the time between its steps
(2500 by default for these tasks).

For the video itself, run the real Claude Code. The demo has a HOME of its own, so your Claude login
isn't there: make a token once and pass it in.

```sh
claude setup-token                       # prints a token; keep it out of shell history files
export CLAUDE_CODE_OAUTH_TOKEN=…
DEMO_CLAUDE=real DEMO_DIR=/tmp/video-demo SEED_SCENE=demo-video scripts/landing-shots/demo.sh
```

`DEMO_CLAUDE=real` runs `claude` from your `PATH`; set `DEMO_CLAUDE` to a path to run another one.
`ANTHROPIC_API_KEY` works instead of the token. Real runs use your Claude usage and take a few
minutes each.

With real Claude, the runners report the Claude account they run as. Don't open **Runners** or a
runner's details on camera, or check what they show first.

### A preview domain on a reserved name

The previews use `preview.dash.localhost` because it needs no setup. To show a name like
`preview.example.dev` instead, set `DEMO_PREVIEW_DOMAIN=preview.example.dev`, and make
`dash.example.dev` and every `*.preview.example.dev` name point to `127.0.0.1` with a local DNS
server (for example dnsmasq: `address=/example.dev/127.0.0.1`). `/etc/hosts` can't hold wildcard
names. Then open Dash at `http://dash.example.dev:7791`.

### Check the setup

`demo_test.mjs` checks the seed, each task's canned change, the conflict in both merge orders and
the rework, without starting a Dash. `go test ./scripts/landing-shots` runs it.

## The landing page's screenshots

```sh
scripts/landing-shots/demo.sh
```

The default scene, `landing`, fills every lane and adds goals. `shoot.mjs` takes the screenshots
and `make.sh` turns them into the landing page's WebP files.
