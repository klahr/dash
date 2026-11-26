# Draw on a preview

**Draw** (<kbd>P</kbd>, or Draw in the floating button's menu on a phone) freezes a
[live preview](previews.md): the page renders what it currently shows (scroll position, open menus
and signed-in state included) to a picture, which replaces the live page until **Done**
(<kbd>Esc</kbd>). Mark it up with pen, arrow, circle and box (<kbd>Shift</kbd> for straight lines,
perfect circles and squares) in red, yellow or blue, undo with <kbd>Ctrl-z</kbd>, and crop
(<kbd>C</kbd>) to the part that matters.

**Add to rework notes** (<kbd>Enter</kbd>) uploads the picture into the rework notes beside the preview
with a line saying where it's from (`/settings · 390×844 · dash/todo-3f9a1c`) and, for each mark (an
arrow's tip, the middle of a circle, box or pen stroke), the element it points at (`circle → form#webhook-form button.primary ("Save webhook")`),
so Claude knows what you mean without guessing from the picture; add several, then send them with
**Rework**. On a phone one finger draws, two pan and zoom (with a pen, fingers only pan and zoom, and
a resting palm is ignored), and **Add to rework notes** opens the rework sheet. **Copy** puts the picture on
the clipboard.

Drawing needs a [preview domain](previews.md#setting-up-a-preview-domain), since the picture is
taken through the proxy (see [what the proxy does](previews.md#what-the-proxy-does)). On a hub,
Draw works the same, and the picture goes to the task's runner with the rework notes.

## On a commit preview

A [commit preview](previews.md#preview-a-commit-from-history) (**History**, or the header's **Run**)
has **Draw** too, with the same tools. It has no task, so **Attach to chat** (<kbd>Enter</kbd>)
takes the place of **Add to rework notes**:

1. Press **Draw** (<kbd>P</kbd>) on the live page.
2. Circle what you mean with the pen, or use the other tools.
3. Press **Attach to chat**. The picture goes into the composer of the chat shown in the chat pane,
   or this project's chat when the pane is closed, with where it's from and the elements under the marks.
4. Write what you want, and send.

To attach it to another chat (General, another project's tab, a goal's coordinator), press **▾**
beside **Attach to chat** and pick one. **Copy** works as on a task's preview.

Started with **Run**, the preview offers **New commit abc1234 · Update** once the fix is merged.
Press it to see the fix (see [Run and Terminate](previews.md#run-and-terminate-the-latest-commit-in-one-click)).

## Exact capture

The picture is the page's own rendering of itself, which can be wrong for WebGL, video or unusual
CSS. On desktop Chrome and Edge, **Exact** (<kbd>X</kbd>) in draw mode takes it from the screen
instead (the browser asks to share the tab; the marks are kept), and stays on for next time.
