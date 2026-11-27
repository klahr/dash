# Tutorial videos

Makes narrated tutorial videos of Dash for YouTube, from a script. It starts a demo Dash, clicks
through it in headless Chrome while recording, adds an ElevenLabs voice and subtitles, and puts
the same animated intro and outro around every video.

The demo Dash comes from [`../landing-shots`](../landing-shots): `demo.sh` starts it,
`seed.mjs` fills it, `fakeclaude.mjs` stands in for Claude Code and `cdp.mjs` drives Chrome.
The 90-second demo video, recorded by hand with real Claude runs, uses the same demo: see
[its README](../landing-shots/README.md#the-demo-video).

## What you need

- Go, git and Node 22 or later.
- ffmpeg and ffprobe, with libx264.
- Chrome or Chromium. Set `CHROME` to its path if it isn't in a usual place.
- An ElevenLabs API key, for the voice. Without one, use `--no-voice`.
- The ElevenLabs SDK, for the voice. Install it once:

  ```sh
  npm ci --prefix scripts/tutorials
  ```

## Make a video

From the repository root:

```sh
npx -y @elevenlabs/cli@latest onboard init   # once: signs you in and saves the key in .env.local
scripts/tutorials/make.sh scripts/tutorials/first-task.yaml
```

`make.sh` reads `ELEVENLABS_API_KEY` from `.env.local` in the repository root, which git ignores.
A key already set in the environment wins.

It writes `scripts/tutorials/out/first-task.mp4` (1920×1080, H.264 and AAC) and
`first-task.srt`. Upload the `.srt` as the video's subtitles.

To try the pipeline without a key or a network, make silent clips of an estimated length:

```sh
scripts/tutorials/make.sh scripts/tutorials/first-task.yaml --no-voice
```

`out/first-task.work/timing.json` lists where each line starts in the video, how long it is and
how long its step took. When a step's actions take longer than its line, `make.sh` says so: the
video then holds quiet until the actions are done. Make the line longer, or the actions quicker.

## Settings

| Variable | What it does |
|---|---|
| `ELEVENLABS_API_KEY` | The ElevenLabs key. Needed unless you pass `--no-voice`. Read from `.env.local` when not set. |
| `ELEVENLABS_VOICE_ID` | The voice. Overrides the script's `voice:`. The default is ElevenLabs' "George". |
| `ELEVENLABS_MODEL` | The model. Overrides the script's `model:`. The default is `eleven_multilingual_v2`. |
| `ELEVENLABS_VOICE_SETTINGS` | Voice settings as JSON, e.g. `{"stability":0.5,"speed":1.05}`. Overrides `voice_settings:`. |
| `TUTORIAL_STING` | A sound file to play at the start of the intro. Overrides the script's `sting:`. |
| `CHROME` | The Chrome to record with. |
| `DEMO_DIR` | Where the demo Dash lives while recording. The default is `/tmp/tutorial-demo`. |
| `TUTORIAL_CHROME_FLAGS` | More flags for the recording's Chrome, each starting with `--`. |
| `TUTORIAL_KEEP_FRAMES` | Set it to keep the recorded frames in `out/<name>.work/frames`. |

There is no music by default. Only add a sting you have the rights to, and don't commit it.

## Write a script

One YAML (or JSON) file per video. The header names the video; each step has a line to say and
the actions to do while it is said:

```yaml
title: Your first task on the board      # shown in the intro
next: "Goals: let Claude plan the work"  # optional, shown in the outro
seed: first-task                         # seed.mjs's scene: the board's starting state

steps:
  - say: Press New task, and describe the change you want.
    do:
      - click: "#new-todo"
      - type: Tell shoppers how much more they need to spend for free shipping.
        into: "#quick-rows .quick-row"
```

A step runs its actions, then holds the screen until its line has been said. Its line starts as
the step starts.

The actions:

| Action | What it does |
|---|---|
| `click: <selector>` | Glides the pointer to the element and clicks it. |
| `hover: <selector>` | Glides the pointer to the element. |
| `type: <text>` | Types the text. `into:` clicks an element first and types after what it already holds; `cps:` sets the speed in characters a second. |
| `key: Enter` | Presses a key (`Enter`, `Escape`, `Tab` or one character). |
| `wait: <selector>` | Waits until the element shows. `gone: true` waits until it goes. |
| `scroll: <selector>` | Scrolls the element into view, or scrolls its box by `by:` pixels. |
| `drag: <selector>` | Presses on the element, moves and lets go: to `to: [x, y]` in a line, or around a `circle: <radius>`. `from: [x, y]` is where it starts (the middle by default) and `seconds:` how long it takes (1.2). Points are fractions of the element's box, from 0 to 1; a circle's radius is a share of its smaller side. |
| `pause: <seconds>` | Waits. |
| `open: <path>` | Opens a page of the demo Dash, e.g. `/wall`, or another site with a whole URL. |
| `terminal: <command>` | Draws a terminal window over the page, types the command after an `alex@studio ~ %` prompt and prints `output:` (a list of lines) one by one. See below. |
| `caption: <text>` | Shows big text in the lower third, for a video watched without sound. It stays until the next caption; `caption: ""` clears it. |

Selectors are CSS. On `click`, `hover`, `wait` and `scroll`:

- `text:` picks the first match whose text includes it.
- `in:` with `text:` picks the container (e.g. `in: "#board .todo"`, `text: Free-shipping`) and
  looks the selector up inside it.
- `timeout:` sets the seconds to wait for the element (default 30, and 60 for `wait`).

A terminal stays up until the next action that isn't a `terminal`; a second `terminal` types into
the same window. It fades out then. `close: <seconds>` keeps it that long after its output and
closes it. Its other options:

- `from: <selector>` reads the text of an element before the window opens, e.g. the command in
  **Add a runner**. `{from}` in the command is replaced with it, so the typed command is the one
  on screen.
- `run: <shell command>` runs a command for real once the command has been typed, before the
  output prints. It runs with `sh` in the repository's root, with the recorder's environment
  (`DEMO_DIR` included), `TERMINAL_FROM` set to the text `from:` read and `TERMINAL_COMMAND` to
  the typed command. The recording waits for it and fails if it fails. Without `output:`, the
  terminal prints what it printed.

```yaml
- terminal: "{from}"
  from: "#runners-code .copy-row pre.copy-text"
  run: node scripts/landing-shots/my-hook.mjs     # a demo hook: registers a runner with $TERMINAL_FROM
  output:
    - "✓ registered as studio"
```

A line that starts with `✓` or `✗` gets its mark in green or red.

Other header fields:

- `voice`, `model` and `voice_settings` pick the voice. The environment overrides them.
- `start` is the page to start on. The default is `/`.
- `sign_in: false` starts signed out. The recorder signs in before the first step otherwise. Start
  on the sign-in page (`start: /login.html`) and sign in with the steps.
- `brand: short` puts a 1.5 s logo sting before the video and a 2.5 s end card with the link after
  it, instead of the intro and the outro. It is for short, muted videos such as the landing
  page's. The default is `full`.
- `gap` is the seconds between two lines (default 0.4).
- `lead` and `tail` are the seconds before the first step and after the last (default 0.8 and
  1.2).
- `sting` is a sound file, relative to the script.

A step can also have `hold: <seconds>`: extra time after its line.

A step with `speed: <factor>` (above 1) plays that many times faster in the video, e.g. `speed: 4`
for Claude's work to pass in a few seconds. Its line is still said at the normal pace: the step
holds until its line fits at the sped-up rate, so it records for at least its line's length (and
the gap and `hold`) times the factor. The narration, the subtitles and `timing.json` use the
video's times. `timing.json` also gives a sped-up step's `speed` and how long it took to record.

If a video needs another starting state, add a scene to `seed.mjs` in `../landing-shots` and name
it in `seed:`. A task Claude should do in the video needs a canned change in `fakeclaude.mjs`; a
`slow: true` task takes a few seconds per step, so it can be watched as it runs.

## The intro and the outro

They are web pages in [`brand`](brand): `intro.html`, `outro.html`, with `brand.css` and
`brand.js` shared between them. They use Dash's logo (`web/static/favicon.svg`), its colours (the
`:root` block of `web/static/style.css`) and its font. Only the title, the next video and the link
change between videos.

- The intro is 4.7 s: the logo builds itself, a light sweeps across it, the wordmark slides out
  and the title rises in.
- The outro is 5.5 s and then holds still, so YouTube's end screens fit. Its text stays in the top
  left; the right side and the bottom are left clear for the end-screen elements.

[`brand/brand.json`](brand/brand.json) holds the link shown in the outro, the
series' name, the outro's text and how long its last frame holds.

To work on them, render only the intro and the outro:

```sh
scripts/tutorials/make.sh --brand-preview "Your first task on the board" --next "Goals: let Claude plan the work"
```

It writes `out/brand-intro.mp4`, `out/brand-outro.mp4` and a few stills of each.

The short brand (`brand: short`) has its own pages, `sting.html` (1.5 s: the logo and the
wordmark) and `endcard.html` (2.5 s: the logo, the wordmark and the link, still from 2 s). To
render them:

```sh
scripts/tutorials/make.sh --brand-preview "Dash" --brand short
```

It writes `out/brand-sting.mp4`, `out/brand-endcard.mp4` and their stills.

Each frame is rendered on its own: the animations are paused, set to the frame's time and
captured, at 30 frames a second. Keep the motion in CSS animations under `body.go` (or in
`brand.js`'s background, which draws any moment on request), and it renders smoothly however slow
the machine is.

## Caching

`scripts/tutorials/cache` (git-ignored) keeps what costs time or money:

- `cache/voice` has one clip per line, named by a hash of its text, voice, model and settings. A
  run only asks ElevenLabs for lines that changed.
- `cache/brand` has the rendered intro and outro, named by a hash of the `brand` files, the app's
  colours, logo and fonts, and the title.

Delete the folder to start over.

## How the recording works

Headless Chrome records with `Page.startScreencast` at 1920×1080. Every frame keeps Chrome's
timestamp, and ffmpeg turns them into an even 30 frames a second. Chrome sends frames whenever the
page changes, about 60 a second while something moves, so the video is smooth without a visible
Chrome under Xvfb. Headless Chrome draws no mouse pointer, so the recorder adds one to the page
(`lib/cursor.js`). The terminal and the captions are drawn into the page too (`lib/overlay.js`),
above open dialogs and below the pointer. A caption is kept in the page's `sessionStorage`, so it
stays up when the page changes.

A step with `speed` is recorded at its own pace; when the frames become the video
(`framesToVideo` in `lib/media.mjs`), its frames are moved closer together, and the recording's
times are mapped to the video's (`timeMap`) for the narration and the subtitles.

The finished video crossfades from the intro (or sting) to the tutorial and on to the outro (or end
card), 0.5 s each.
The narration fades in as the intro ends, and the subtitles are shifted by the same amount.

## Upload to YouTube

Not built yet. The YouTube Data API's `videos.insert`
could upload `out/<name>.mp4` with the script's title, and `captions.insert` the `.srt`. It needs
an OAuth client for the channel, kept outside the repository like the ElevenLabs key.
