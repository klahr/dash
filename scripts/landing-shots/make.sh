#!/bin/sh
# Turns shoot.mjs's PNGs in $1 into the landing page's WebP files, from the
# repository root. Needs ImageMagick 7 (magick) with WebP.
set -eu
in=$1
out=web/static/img/landing
# webp <output> <input and operations…>
webp() { o=$1; shift; magick "$@" -strip -quality 80 -define webp:method=6 "$o"; }

# The board: 1680×1000 at 2×, resized to 2400 wide; its first four lanes
# (Drafts to Review) for small screens.
webp "$out/board.webp" "$in/board.png" -resize 2400x
webp "$out/board-crop.webp" "$in/board.png" -crop 1920x1170+480+112 +repage -resize 1100x
# The task in review: the detail pane only.
webp "$out/review.webp" "$in/review.png" -crop 1520x2000+1840+0 +repage -resize 1140x
# The Goals page, with a goal open beside the list; the goal alone for small screens.
webp "$out/goals.webp" "$in/goals.png" -resize 2400x
webp "$out/goals-crop.webp" "$in/goals.png" -crop 1640x2000+1720+0 +repage -resize 1100x
# The wall, at 1920×1080.
webp "$out/wall.webp" "$in/wall.png"
# The phone: 390×640 at 3×, resized to 2×.
webp "$out/phone.webp" "$in/phone.png" -resize 780x

for f in board board-crop review goals goals-crop wall phone; do
	printf '%s.webp %s\n' "$f" "$(magick identify -format '%wx%h %b' "$out/$f.webp")"
done
