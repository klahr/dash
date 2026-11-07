Artwork for the web UI. The icons are rendered from ../favicon.svg:

    magick -background none -density 1200 favicon.svg -resize 512x512 img/icon-512.png

The illustrations are optional; the UI hides them until these files exist:

- empty-board.webp  960×720   first-run empty state
- empty-chat.webp   960×720   empty chat
- login-art.webp    1280×800  above the sign-in form

The landing page's screenshots (landing/) are of a real Dash with a demo project, taken
with headless Chromium in the dark theme at 2× (the phone at 3×, 390×640):

- landing/board.webp        the board, 1680×1000 viewport, resized to 2400 wide
- landing/board-crop.webp   its lanes cropped, shown instead on small screens
- landing/review.webp       the task panel of a task in review with a file's diff open
- landing/phone.webp        the board's Review lane on a phone

The technology logos on the Run and Deploy buttons (TECH_ICONS in ../app.js) are inlined from devicon (https://github.com/devicons/devicon), MIT License, Copyright (c) 2015 konpa.
