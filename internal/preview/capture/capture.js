// Dash draw mode: on request from the Dash page that shows this preview in
// its iframe, render what this page currently shows (the viewport, at its
// scroll position) to a PNG and send it back; and, for the marks drawn on it,
// say which element is under each point. It does nothing else.
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
    return { blob, width: w, height: h, scale, ...where() };
  };
  const where = () => ({ path: location.pathname + location.search + location.hash, title: document.title, scroll: { x: scrollX, y: scrollY } });

  // ---- element hints: "button.save" and "Save settings" for a point.
  const clip = (s, n) => (s.length > n ? `${s.slice(0, n - 1).trimEnd()}…` : s);
  const squash = (s) => (s || "").replace(/\s+/g, " ").trim();
  // Class names that say what a thing is, not hashes (css-1x2y3z) or utilities (px-4).
  const UTIL = /^-?(p[xytrbl]?|m[xytrbl]?|w|h|min-[wh]|max-[wh]|size|gap|space-[xy]|text|font|leading|tracking|bg|from|to|via|border|rounded|shadow|ring|outline|opacity|z|top|left|right|bottom|inset|flex|grid|col|row|items|justify|content|self|place|order|basis|grow|shrink|overflow|cursor|transition|duration|ease|delay|animate|fill|stroke|object|aspect|line-clamp|truncate|block|inline|inline-block|inline-flex|hidden|relative|absolute|fixed|sticky|static|container|sr-only|underline|italic|uppercase|lowercase|capitalize|visible|invisible|select|pointer-events|whitespace|break|align|decoration|divide)(-|$)/;
  const hashy = (c) => /[:[\]/()!@]/.test(c) || /^(css|sc|jsx|svelte|emotion|chakra|tw)-/.test(c) || /\d/.test(c) && /[a-z]\d|\d[a-z]/i.test(c) && !/^[a-z]+(-[a-z]+)*-\d+$/i.test(c) || c.length > 32;
  const goodId = (id) => id && !hashy(id) && !/^(:|radix-|headlessui-|react-|ember\d)/.test(id);
  const part = (el) => {
    const tag = el.tagName.toLowerCase();
    if (goodId(el.id)) return `${tag}#${CSS.escape(el.id)}`;
    const test = el.getAttribute("data-testid") || el.getAttribute("data-test") || el.getAttribute("data-cy");
    if (test) return `${tag}[data-testid="${clip(test, 40)}"]`;
    const cls = [...el.classList].filter((c) => !hashy(c) && !UTIL.test(c)).slice(0, 2);
    let s = tag + cls.map((c) => `.${CSS.escape(c)}`).join("");
    const name = el.getAttribute("name");
    if (name && /^(input|select|textarea|button)$/.test(tag)) s += `[name="${clip(name, 30)}"]`;
    else if (tag === "input") s += `[type="${el.type}"]`;
    return s;
  };
  const ACTIVE = "a[href],button,input,select,textarea,label,summary,[role=button],[role=link],[role=tab],[role=menuitem],[role=option],[role=checkbox],[role=radio],[role=switch],[contenteditable=''],[contenteditable=true]";
  const AROUND = "header,nav,main,footer,aside,dialog,form,[role=dialog],[role=navigation],[role=menu],[role=toolbar],table,ul,ol,section[id],[id]";
  const hit = (x, y) => {
    let el = document.elementFromPoint(x, y);
    for (let i = 0; el?.shadowRoot && i < 8; i++) {
      const inner = el.shadowRoot.elementFromPoint(x, y);
      if (!inner || inner === el) break;
      el = inner;
    }
    if (!el) return null;
    while (el.ownerSVGElement) el = el.ownerSVGElement; // the icon, not a path in it
    return el.closest(ACTIVE) || el;
  };
  const textOf = (el) => {
    const t = el.getAttribute("aria-label") || el.getAttribute("alt") || el.getAttribute("title")
      || (el.labels?.[0] && el.labels[0].innerText)
      || el.getAttribute("placeholder")
      || (/^(submit|button|reset)$/.test(el.type) && el.value)
      || (el instanceof SVGElement ? el.querySelector("title")?.textContent : el.innerText);
    return clip(squash(t), 60);
  };
  const describe = (x, y) => {
    const el = hit(x, y);
    if (!el || el === document.documentElement) return null;
    let sel = part(el);
    if (el !== document.body) {
      const ctx = el.parentElement?.closest(AROUND);
      if (ctx && ctx !== document.body && ctx !== document.documentElement) sel = `${part(ctx)} ${sel}`;
    }
    return { selector: sel, text: el === document.body ? "" : textOf(el) };
  };
  // inspect describes the element under each point (the page's CSS px at the
  // scroll the snapshot was taken at; the page is put back there meanwhile).
  const inspect = (points, at) => {
    const back = { x: scrollX, y: scrollY };
    const move = at && (at.x !== back.x || at.y !== back.y);
    if (move) scrollTo({ left: at.x, top: at.y, behavior: "instant" });
    try {
      return points.map((p) => { try { return describe(p.x, p.y); } catch { return null; } });
    } finally {
      if (move) scrollTo({ left: back.x, top: back.y, behavior: "instant" });
    }
  };
  window.addEventListener("message", async (e) => {
    const d = e.data;
    if (!d || e.source !== window.parent || !allowed(e.origin)) return;
    if (d.type === "dash:where") return e.source.postMessage({ type: "dash:here", id: d.id, ...where() }, e.origin);
    if (d.type === "dash:inspect") {
      const hints = Array.isArray(d.points) ? inspect(d.points.slice(0, 50), d.scroll) : [];
      return e.source.postMessage({ type: "dash:inspected", id: d.id, hints, ...where() }, e.origin);
    }
    if (d.type !== "dash:capture") return;
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
