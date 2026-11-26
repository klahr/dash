import { desktop } from "./viewport.js";
import { leave } from "./dialogs.js";

// ------------------------------------------------------------------ side detail pane
// A list's detail pane (a goal, a commit, a planning card): beside the list on a
// desktop (not modal), full screen on a phone (modal, its close button reads
// Back). Each pane loads and draws its own content; this keeps the steps they
// share: remembering what opened the pane, the modal state, hiding it with its
// closing animation and giving focus back.
//
//   pane()        the pane's element
//   closeBtn()    its close button
//   background()  the elements a phone makes inert while it is open
//   isOpen()      whether it shows something (false once close has begun)
//   rowFor(id)    the list's row for id, which gets focus back on close
//   onHidden()    runs once the pane is hidden
//   onViewport()  redraws the open pane when crossing the desktop breakpoint
//   modal()       whether a phone's open pane is modal now (default: always)
export function detailPane({ pane, closeBtn, background, isOpen, rowFor, onHidden, onViewport, modal }) {
  let opener = null;
  const setModal = () => {
    const on = !pane().hidden && !desktop.matches && (modal?.() ?? true);
    (background() || []).forEach((el) => { el.inert = on; });
    closeBtn()?.setAttribute("aria-label", desktop.matches ? "Close" : "Back");
  };
  desktop.addEventListener("change", () => { if (isOpen()) { setModal(); onViewport?.(); } });
  const shown = (el) => el?.isConnected && !el.closest("[hidden]") ? el : null;
  return {
    setModal,
    // open shows the pane; the first open remembers what had focus.
    open() {
      const el = pane();
      if (el.hidden) {
        const a = document.activeElement;
        opener = a && a !== document.body ? a : null;
      }
      el.hidden = false;
      setModal();
    },
    // close hides the pane that showed id. Focus goes back to id's row (or what
    // opened the pane) only if it was in the pane or lost: a close caused by
    // something else leaves focus where it is.
    close(id) {
      const el = pane();
      const wasOpen = !el.hidden;
      const hide = () => { if (!isOpen()) { el.hidden = true; onHidden?.(); setModal(); } };
      if (wasOpen) leave(el, "closing", hide); else hide();
      (background() || []).forEach((b) => { b.inert = false; });
      const back = opener;
      opener = null;
      const a = document.activeElement;
      if (!wasOpen || (a && a !== document.body && !el.contains(a))) return;
      (shown(rowFor(id)) || shown(back))?.focus();
    },
  };
}
