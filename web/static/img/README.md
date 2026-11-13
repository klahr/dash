Artwork for the web UI. The icons are rendered from ../favicon.svg:

    magick -background none -density 1200 favicon.svg -resize 512x512 img/icon-512.png

The illustrations are optional; the UI hides them until these files exist:

- empty-board.webp  960×720   first-run empty state
- empty-chat.webp   960×720   empty chat
- login-art.webp    1280×800  above the sign-in form

The landing page's screenshots (landing/) are of a real Dash with a demo project, acme-shop,
taken with headless Chromium in the dark theme at 2× (the phone at 3×, 390×640), and saved
as WebP at quality 80:

- landing/board.webp        the board, 1680×1000 viewport, resized to 2400 wide
- landing/board-crop.webp   its first four lanes (Drafts to Review), shown instead on small screens
- landing/review.webp       the detail pane of a task in review with a file's diff open, 1140×1500
- landing/goals.webp        the Goals page with a goal open beside the list, resized to 2400 wide
- landing/goals-crop.webp   the open goal alone, shown instead on small screens
- landing/wall.webp         the wall view at 1920×1080
- landing/phone.webp        the board's Review lane on a phone, resized to 780 wide

To take them again, from the repository root (needs go, git, node, ImageMagick 7 and a
headless Chromium, e.g. Playwright's chrome-headless-shell):

    sh scripts/landing-shots/demo.sh
    CHROME=/path/to/chrome-headless-shell node scripts/landing-shots/shoot.mjs /tmp/landing-demo/shots
    sh scripts/landing-shots/make.sh /tmp/landing-demo/shots
    kill $(cat /tmp/landing-demo/pids)

demo.sh starts a hub and a runner in /tmp/landing-demo with a stand-in for Claude Code
(fakeclaude.mjs), which makes a canned change for each demo task, and seeds tasks in every
lane and three goals (seed.mjs). The project's remote is `https://git.example.com/acme/acme-shop.git`,
which the demo's own git settings map to a local server (gitserve). No database is kept.

The technology logos on the Run and Deploy buttons (TECH_ICONS in ../app/dom.js) are inlined from devicon (https://github.com/devicons/devicon), MIT License, Copyright (c) 2015 konpa.
