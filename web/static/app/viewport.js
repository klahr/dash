
// ------------------------------------------------------------------ viewport & on-screen keyboard
export const wsURL = (path) => `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}${path}`;
export const desktop = matchMedia("(min-width: 1024px)");

// A tap on a composer's buttons (send, Send to agent, a file chip's ×) must not
// take focus from its text field: the keyboard would close, the composer move
// down with it, and the click miss the button, so it took a second tap. Holding
// on to focus also keeps the keyboard up after sending, as in a messaging app.
// Only touch and pen: a mouse click (desktop) behaves as before, and keyboard and
// screen reader activation don't go through these events at all.
{
  let touch = false;
  const keep = (e) => {
    const b = e.target.closest?.(".composer button");
    if (!b || !b.closest(".composer").contains(document.activeElement) || !document.activeElement.matches("textarea")) return;
    e.preventDefault();
  };
  document.addEventListener("pointerdown", (e) => { touch = e.pointerType !== "mouse"; if (touch) keep(e); }, true);
  // The mousedown a tap fires after touchend is what moves focus.
  document.addEventListener("mousedown", (e) => { if (touch) keep(e); }, true);
}

// On-screen keyboard (mobile). The chat page and the task detail sheet
// are fitted to the part of the screen the keyboard leaves free: their head
// stays at the top, only the scroller (chat log / detail body) gets shorter
// and the composer sits right on the keyboard. How a browser tells us about
// the keyboard depends on the display mode:
//  - Chrome in a tab or as an installed app shrinks both viewports
//    (interactive-widget=resizes-content in the viewport meta).
//  - iOS keeps the layout viewport and shrinks and pans the visual viewport
//    instead (--vv-top follows the pan).
// So whenever a composer has focus we turn on overlaysContent and the
// VirtualKeyboard API reports the keyboard the same way in every mode. The free
// part ends at whichever is higher: the visual viewport's bottom or the
// keyboard's top edge. We never take one off the other, so a keyboard that
// both report is not counted twice. If neither reports it, CSS falls back to
// env(keyboard-inset-height) (.kb-env).
// ?debug=kb shows these numbers on the page, for checking on a phone (?debug=off hides them).
const vv = window.visualViewport;
if (vv) {
  const root = document.documentElement;
  const vk = navigator.virtualKeyboard;
  const displayMode = () => ["fullscreen", "standalone", "minimal-ui", "browser"].find((m) => matchMedia(`(display-mode: ${m})`).matches) || "?";
  const isComposer = (el) => !desktop.matches && !!el?.matches?.(".composer textarea");
  const editing = () => !!document.activeElement?.matches?.("input, textarea, select, [contenteditable]");
  // The scroller a composer belongs to: the chat log, or the task's detail body.
  const scrollerOf = (el) => el.closest(".chat-panel")?.querySelector(".chat-log") || el.closest(".detail-body");
  let stick = null; // scroller that was at its end when the keyboard opened
  let pageY = null; // page scroll when a sheet's composer took focus
  let kbY = 0, kbH = 0; // the keyboard's top edge and height (layout px) per the VirtualKeyboard API; 0: none
  let fullH = innerHeight, fullW = innerWidth; // layout viewport with no keyboard up
  let debug = null;
  const fit = () => {
    if (!editing() || innerWidth !== fullW) { fullH = innerHeight; fullW = innerWidth; }
    // Pinch-zoomed, the visual viewport is a zoomed-in window: keep the sheets full-size.
    const zoomed = vv.scale > 1.01;
    const top = zoomed ? 0 : vv.offsetTop;
    const vvBottom = zoomed ? innerHeight : vv.offsetTop + vv.height;
    // Everything from the keyboard's top edge down is covered (the rect is the keys alone: below them
    // Android still shows its navigation bar, back / hide keyboard). No top edge reported: count up from the bottom.
    const kbTop = kbH > 0 ? (kbY > 0 ? kbY : innerHeight - kbH) : Infinity;
    const bottom = Math.min(vvBottom, kbTop);
    root.style.setProperty("--vv-top", `${Math.round(top)}px`);
    root.style.setProperty("--vv-h", `${Math.round(Math.max(0, bottom - top))}px`);
    // The part of the keyboard the visual viewport doesn't already leave out (bottom sheets use it).
    root.style.setProperty("--kb-h", `${Math.round(vvBottom - bottom)}px`);
    // A composer has focus but nothing reports a keyboard: neither viewport shrank and the API is silent.
    const shrunk = innerHeight < fullH - 80 || innerHeight - vv.height > 80;
    root.classList.toggle("kb-env", isComposer(document.activeElement) && !kbH && !shrunk && !zoomed);
    // iOS can also scroll the page behind the sheet to reveal the input; undo that.
    if (pageY !== null && scrollY !== pageY) scrollTo(scrollX, pageY);
    debug?.();
  };
  // Keep the latest messages in view as the scroller shrinks.
  const keepEnd = () => { if (stick?.isConnected && isComposer(document.activeElement)) stick.scrollTop = stick.scrollHeight; };
  // Ask the VirtualKeyboard API to report the keyboard (and lay it over the
  // page) while a composer has focus, so that it is measured the same way in a tab and an installed app.
  const overlay = (on = isComposer(document.activeElement)) => {
    if (!vk) return;
    if (vk.overlaysContent !== on) vk.overlaysContent = on;
    if (!on) kbY = kbH = 0;
  };
  vv.addEventListener("resize", () => { fit(); keepEnd(); });
  vv.addEventListener("scroll", fit);
  if (vk) {
    vk.addEventListener("geometrychange", () => {
      const r = vk.boundingRect;
      [kbY, kbH] = vk.overlaysContent && r.height > 0 ? [r.y, r.height] : [0, 0];
      fit();
      keepEnd();
    });
    // Before the keyboard comes up, so it already overlays instead of resizing.
    // If the touch didn't focus it after all (a scroll), go back.
    document.addEventListener("pointerdown", (e) => {
      if (!isComposer(e.target)) return;
      overlay(true);
      setTimeout(modeChange, 1000);
    }, true);
  }
  const modeChange = () => { overlay(); fit(); };
  // iOS may scroll only the page (not the visual viewport) to reveal a focused input.
  addEventListener("scroll", () => { if (pageY !== null && scrollY !== pageY) scrollTo(scrollX, pageY); }, { passive: true });
  document.addEventListener("focusin", (e) => {
    if (!isComposer(e.target)) return;
    const sc = scrollerOf(e.target);
    stick = sc && sc.scrollHeight - sc.scrollTop - sc.clientHeight < 120 ? sc : null;
    pageY = e.target.closest(".chat-pane, .detail") ? scrollY : null;
    overlay(true);
    fit();
  });
  document.addEventListener("focusout", () => {
    stick = null;
    pageY = null;
    // activeElement is only settled once the focus move is done.
    setTimeout(modeChange);
  });

  const flag = new URLSearchParams(location.search).get("debug");
  if (flag === "kb") localStorage.setItem("dash.debugKb", "1");
  else if (flag === "off") localStorage.removeItem("dash.debugKb");
  if (localStorage.getItem("dash.debugKb")) {
    const box = document.createElement("pre");
    box.className = "kb-debug";
    const probe = document.createElement("div");
    probe.className = "kb-probe";
    document.body.append(box, probe);
    const n = (x) => Math.round(x * 10) / 10;
    const rect = (el) => { const r = el?.getBoundingClientRect(); return r ? `${n(r.top)}–${n(r.bottom)}` : "–"; };
    debug = () => {
      const r = vk?.boundingRect;
      const css = getComputedStyle(root);
      const comp = document.activeElement?.closest(".composer")
        || [...document.querySelectorAll(".chat-pane .composer, .detail .composer")].find((c) => c.offsetParent);
      box.textContent = [
        `innerHeight ${innerHeight} (no kb ${fullH})  screen ${screen.height} ×${n(devicePixelRatio)}`,
        `vv.height ${n(vv.height)}  vv.offsetTop ${n(vv.offsetTop)}  scale ${n(vv.scale)}`,
        vk ? `vk overlaysContent ${vk.overlaysContent}  rect y ${n(r.y)} h ${n(r.height)}` : "vk: no VirtualKeyboard API",
        `kbH ${n(kbH)} (top ${n(kbY)})  env(kb-inset-h) ${probe.offsetHeight}  kb-env ${root.classList.contains("kb-env")}`,
        `--vv-top ${css.getPropertyValue("--vv-top")}  --vv-h ${css.getPropertyValue("--vv-h")}  --kb-h ${css.getPropertyValue("--kb-h")}`,
        `display-mode ${displayMode()}`,
        `composer ${rect(comp)}  focused ${isComposer(document.activeElement)}`,
        `pane ${rect(document.querySelector(".app.chat-open .chat-pane, .detail:not([hidden])"))}`,
      ].join("\n");
    };
    setInterval(debug, 500);
  }
  fit();
}
