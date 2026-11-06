Artwork for the web UI. The icons are rendered from ../favicon.svg:

    magick -background none -density 1200 favicon.svg -resize 512x512 img/icon-512.png

The illustrations are optional; the UI hides them until these files exist:

- empty-board.webp  960×720   first-run empty state
- empty-chat.webp   960×720   empty chat
- login-art.webp    1280×800  above the sign-in form

The technology logos on the Run and Deploy buttons (TECH_ICONS in ../app.js) are inlined from devicon (https://github.com/devicons/devicon), MIT License, Copyright (c) 2015 konpa.
