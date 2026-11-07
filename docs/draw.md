# Draw on a preview

**Draw** (<kbd>D</kbd>, or Draw in the floating button's menu on a phone) freezes a
[live preview](previews.md): the page renders what it currently shows (scroll position, open menus
and signed-in state included) to a picture, which replaces the live page until **Done**
(<kbd>Esc</kbd>). Mark it up with pen, arrow, circle and box (<kbd>Shift</kbd> for straight lines,
perfect circles and squares) in red, yellow or blue, undo with <kbd>Ctrl-z</kbd>, and crop
(<kbd>C</kbd>) to the part that matters.

**Add to review** (<kbd>Enter</kbd>) uploads the picture into the rework notes beside the preview
with a line saying where it's from (`/settings · 390×844 · dash/todo-3f9a1c`) and, for each mark (an
arrow's tip, the middle of a circle, box or pen stroke), the element it points at (`circle → form#settings button.save ("Save settings")`),
so Claude knows what you mean without guessing from the picture; add several, then send them back
together. On a phone one finger draws, two pan and zoom (with a pen, fingers only pan and zoom, and
a resting palm is ignored), and Add to review opens the rework sheet. **Copy** puts the picture on
the clipboard.

Drawing needs a [preview domain](previews.md#setting-up-a-preview-domain), since the picture is
taken through the proxy (see [what the proxy does](previews.md#what-the-proxy-does)).

The picture is the page's own rendering of itself, which can be wrong for WebGL, video or unusual
CSS. On desktop Chrome and Edge, **Exact** (<kbd>X</kbd>) in draw mode takes it from the screen
instead (the browser asks to share the tab; the marks are kept), and stays on for next time.
