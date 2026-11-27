// A fake mouse pointer for the tutorial recordings, injected into every
// page: headless Chrome draws none. It sits in the top layer (as a manual
// popover) so it shows above open dialogs, glides with an ease to where
// the recorder points it and draws a ripple on a click. The recorder calls
// window.__tutCursor.move(x, y, ms) and .click().
(() => {
  if (window.__tutCursor) return;
  const install = () => {
    const el = document.createElement('div');
    el.setAttribute('popover', 'manual');
    el.id = '__tut-cursor';
    el.innerHTML = '<svg width="34" height="34" viewBox="0 0 34 34" aria-hidden="true">'
      + '<path d="M6 3 L6 26 L12 20.5 L16.2 30 L20.2 28.2 L16 18.8 L24 18.8 Z" fill="#fff" stroke="#0d1424" stroke-width="2" stroke-linejoin="round"/></svg>';
    const style = document.createElement('style');
    style.textContent = `
      #__tut-cursor { all: initial; position: fixed; inset: 0; width: 100vw; height: 100vh; margin: 0; padding: 0;
        border: 0; background: transparent; pointer-events: none; overflow: visible; display: block; }
      #__tut-cursor .p { position: absolute; left: 0; top: 0; width: 34px; height: 34px; margin: -3px 0 0 -6px;
        filter: drop-shadow(0 2px 4px rgba(0,0,0,.45)); will-change: transform; }
      #__tut-cursor .p svg { display: block; transform-origin: 6px 3px; transition: transform .12s ease-out; }
      #__tut-cursor .p.down svg { transform: scale(.86); }
      #__tut-cursor .r { position: absolute; width: 44px; height: 44px; margin: -22px 0 0 -22px; border-radius: 50%;
        border: 3px solid rgba(138,164,255,.95); background: rgba(138,164,255,.22); box-sizing: border-box; }`;
    const p = document.createElement('div');
    p.className = 'p';
    p.append(el.firstChild);
    el.append(style, p);
    document.documentElement.append(el);
    let x = innerWidth * 0.62, y = innerHeight * 0.58;
    p.style.transform = `translate(${x}px, ${y}px)`;
    const raise = () => {
      try { if (el.matches(':popover-open')) el.hidePopover(); el.showPopover(); } catch { /* not connected yet */ }
    };
    raise();
    // Dialogs opened later go above it in the top layer: raise it again.
    new MutationObserver(() => raise()).observe(document.documentElement, { subtree: true, attributeFilter: ['open'] });
    window.__tutCursor = {
      get pos() { return { x, y }; },
      raise,
      // move glides to (tx, ty) in ms and resolves once it is there.
      move(tx, ty, ms) {
        raise();
        const from = `translate(${x}px, ${y}px)`, to = `translate(${tx}px, ${ty}px)`;
        x = tx; y = ty;
        p.style.transform = to;
        if (!ms) return Promise.resolve();
        // An S-curve: slow start, quick middle, a gentle landing.
        return p.animate([{ transform: from }, { transform: to }], { duration: ms, easing: 'cubic-bezier(.45,.05,.2,1)' }).finished.then(() => {});
      },
      // down and up press and release the pointer; up draws the ripple,
      // unless quiet (the end of a drag).
      down() { raise(); p.classList.add('down'); },
      up(quiet) {
        p.classList.remove('down');
        if (quiet) return;
        const r = document.createElement('div');
        r.className = 'r';
        r.style.left = x + 'px';
        r.style.top = y + 'px';
        el.append(r);
        r.animate([{ transform: 'scale(.3)', opacity: 1 }, { transform: 'scale(1.5)', opacity: 0 }],
          { duration: 520, easing: 'cubic-bezier(.2,.7,.3,1)' }).finished.then(() => r.remove());
      },
    };
  };
  if (document.documentElement) install();
  else document.addEventListener('DOMContentLoaded', install, { once: true });
})();
