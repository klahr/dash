// Dash draw mode: on request from the Dash page that shows this preview in
// its iframe, render what this page currently shows (the viewport, at its
// scroll position) to a PNG and send it back. It does nothing else.
// The proxy serves this file with `ms` (modern-screenshot) and `CFG`
// ({ origins, hosts }: who may ask) in scope.
if (window.parent !== window && !window.__dashCapture) {
  window.__dashCapture = true;
  const allowed = (origin) => {
    if (CFG.origins.includes(origin)) return true;
    try { return CFG.hosts.includes(new URL(origin).hostname); } catch { return false; }
  };
  // The first colour that isn't transparent, as the page's backdrop (a
  // page's body background normally paints the whole viewport).
  const backdrop = () => {
    for (const el of [document.body, document.documentElement]) {
      const c = el && getComputedStyle(el).backgroundColor;
      if (c && c !== "transparent" && !/^rgba\(.*,\s*0\)$/.test(c)) return c;
    }
    return "#ffffff";
  };
  const shoot = async () => {
    const w = window.innerWidth, h = window.innerHeight;
    const body = document.body;
    const scale = Math.min(window.devicePixelRatio || 1, 2);
    // Render <body> into a box the size of the viewport, moved up and left by
    // the scroll: relative positioning (not a transform) keeps fixed elements
    // where they are on screen, and absolute ones move with the page.
    const r = body.getBoundingClientRect();
    const scroller = document.scrollingElement;
    const own = scroller === body; // quirks mode: body scrolls, children are moved by the library
    const canvas = await ms.domToCanvas(body, {
      width: w, height: h, scale,
      backgroundColor: backdrop(),
      features: { restoreScrollPosition: true },
      timeout: 8000,
      style: {
        position: "relative", margin: "0",
        left: `${own ? 0 : r.left}px`, top: `${own ? 0 : r.top}px`,
        width: `${r.width}px`, height: own ? `${h}px` : `${r.height}px`,
      },
    });
    const blob = await new Promise((ok, no) => canvas.toBlob((b) => (b ? ok(b) : no(new Error("encode failed"))), "image/png"));
    return { blob, width: w, height: h, scale, path: location.pathname + location.search + location.hash, title: document.title };
  };
  window.addEventListener("message", async (e) => {
    const d = e.data;
    if (!d || d.type !== "dash:capture" || e.source !== window.parent || !allowed(e.origin)) return;
    e.source.postMessage({ type: "dash:capture-ack", id: d.id }, e.origin); // the render takes a moment
    let reply;
    try {
      reply = { type: "dash:captured", id: d.id, ...(await shoot()) };
    } catch (err) {
      reply = { type: "dash:captured", id: d.id, error: String(err && err.message || err) };
    }
    e.source.postMessage(reply, e.origin);
  });
}
