// Draw mode's surface: a frozen picture of the live preview with marks (pen,
// arrow, circle, box) and a crop on top, drawn on a canvas in Dash's page.
// One finger or the mouse draws; two fingers pan and zoom (ctrl + wheel too).
// Coordinates are the preview's CSS pixels; the picture has `scale` image
// pixels per CSS pixel, and flatten() renders at that resolution.

export const COLORS = { red: "#ff3b30", yellow: "#ffd60a", blue: "#0a84ff" };
export const TOOLS = ["pen", "arrow", "circle", "box", "crop"];

const LINE = 4;            // stroke width, CSS px of the picture
const HANDLE = 10;         // crop handle hit radius, screen px
const MIN_CROP = 6;
const DIM = "rgba(5, 8, 14, .62)";

export function drawSurface(host, { onChange = () => {} } = {}) {
  const canvas = document.createElement("canvas");
  canvas.className = "draw-canvas";
  const view = document.createElement("div");
  view.className = "draw-view";
  view.append(canvas);
  host.replaceChildren(view);
  const ctx = canvas.getContext("2d");

  let img = null, W = 0, H = 0, R = 1;  // picture, its CSS size, image px per CSS px
  let shapes = [], crop = null, history = [];
  let tool = "pen", color = COLORS.red;
  let cur = null;                         // shape or crop drag in progress
  let fit = 1, z = { s: 1, x: 0, y: 0 };  // view: screen = picture * s + (x, y)
  const pointers = new Map();            // pointerId → {x, y} in host px
  let pinch = null, shift = false, last = null, raf = 0;

  // ---- view
  const hostRect = () => host.getBoundingClientRect();
  const apply = () => { view.style.transform = `translate(${z.x}px, ${z.y}px) scale(${z.s})`; };
  function layout(reset = false) {
    if (!img) return;
    const r = hostRect();
    const prev = fit;
    fit = Math.min(1, r.width / W, r.height / H) || 1;
    if (reset || z.s <= prev + 1e-6) z = { s: fit, x: (r.width - W * fit) / 2, y: Math.max(0, (r.height - H * fit) / 2) };
    clampView();
    apply();
  }
  function clampView() {
    const r = hostRect();
    z.s = Math.min(Math.max(z.s, fit), 8);
    const w = W * z.s, h = H * z.s;
    z.x = w <= r.width ? (r.width - w) / 2 : Math.min(0, Math.max(r.width - w, z.x));
    z.y = h <= r.height ? (r.height - h) / 2 : Math.min(0, Math.max(r.height - h, z.y));
  }
  function zoomAt(px, py, s) {
    const cx = (px - z.x) / z.s, cy = (py - z.y) / z.s;
    z.s = Math.min(Math.max(s, fit), 8);
    z.x = px - cx * z.s;
    z.y = py - cy * z.s;
    clampView();
    apply();
  }
  const ro = new ResizeObserver(() => layout());
  ro.observe(host);

  // host px → picture px
  const toPic = (p) => [(p.x - z.x) / z.s, (p.y - z.y) / z.s];
  const local = (e) => { const r = hostRect(); return { x: e.clientX - r.left, y: e.clientY - r.top }; };
  const clampPt = ([x, y]) => [Math.min(Math.max(x, 0), W), Math.min(Math.max(y, 0), H)];

  // ---- drawing
  function paintShape(c, s) {
    c.save();
    c.strokeStyle = c.fillStyle = s.c;
    c.lineWidth = LINE;
    c.lineCap = c.lineJoin = "round";
    c.shadowColor = "rgba(0, 0, 0, .45)";
    c.shadowBlur = 3;
    c.beginPath();
    if (s.t === "pen") {
      const [p0, ...rest] = s.pts;
      c.moveTo(p0[0], p0[1]);
      if (!rest.length) c.lineTo(p0[0] + .01, p0[1]);
      for (const p of rest) c.lineTo(p[0], p[1]);
      c.stroke();
    } else if (s.t === "arrow") {
      const [[x1, y1], [x2, y2]] = [s.a, s.b];
      const ang = Math.atan2(y2 - y1, x2 - x1), len = Math.hypot(x2 - x1, y2 - y1);
      const head = Math.min(18, Math.max(10, len / 3));
      const bx = x2 - Math.cos(ang) * head * .8, by = y2 - Math.sin(ang) * head * .8;
      c.moveTo(x1, y1);
      c.lineTo(bx, by);
      c.stroke();
      c.beginPath();
      c.moveTo(x2, y2);
      c.lineTo(x2 - head * Math.cos(ang - Math.PI / 7), y2 - head * Math.sin(ang - Math.PI / 7));
      c.lineTo(x2 - head * Math.cos(ang + Math.PI / 7), y2 - head * Math.sin(ang + Math.PI / 7));
      c.closePath();
      c.fill();
    } else if (s.t === "circle") {
      const [x, y, w, h] = box(s.a, s.b);
      c.ellipse(x + w / 2, y + h / 2, Math.max(w / 2, .5), Math.max(h / 2, .5), 0, 0, Math.PI * 2);
      c.stroke();
    } else if (s.t === "box") {
      const [x, y, w, h] = box(s.a, s.b);
      c.roundRect ? c.roundRect(x, y, w, h, 3) : c.rect(x, y, w, h);
      c.stroke();
    }
    c.restore();
  }
  // Marks are drawn in picture px on a context scaled to image px.
  function paint(c, overlay) {
    c.drawImage(img, 0, 0, W * R, H * R);
    c.save();
    c.scale(R, R);
    for (const s of shapes) paintShape(c, s);
    if (cur && cur.t !== "crop") paintShape(c, cur);
    const k = cur?.t === "crop" ? cur.rect : crop;
    if (overlay && k) {
      c.fillStyle = DIM;
      c.beginPath();
      c.rect(0, 0, W, H);
      c.rect(k.x, k.y + k.h, k.w, -k.h); // a hole (opposite winding)
      c.fill("evenodd");
      c.strokeStyle = "#fff";
      c.lineWidth = 1.5 / z.s;
      c.setLineDash([6 / z.s, 4 / z.s]);
      c.strokeRect(k.x, k.y, k.w, k.h);
      c.setLineDash([]);
      if (tool === "crop") {
        const r = 5 / z.s;
        c.fillStyle = "#fff";
        c.strokeStyle = "rgba(0, 0, 0, .5)";
        c.lineWidth = 1 / z.s;
        for (const [hx, hy] of handles(k)) { c.beginPath(); c.arc(hx, hy, r, 0, Math.PI * 2); c.fill(); c.stroke(); }
      }
    }
    c.restore();
  }
  function render() {
    raf = 0;
    if (!img) return;
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    paint(ctx, true);
  }
  const redraw = () => { if (!raf) raf = requestAnimationFrame(render); };

  // ---- shapes
  function box(a, b) {
    return [Math.min(a[0], b[0]), Math.min(a[1], b[1]), Math.abs(b[0] - a[0]), Math.abs(b[1] - a[1])];
  }
  // Shift: straight pen lines, 45° arrows, perfect circles and squares.
  function constrain(t, a, b) {
    if (!shift) return b;
    const dx = b[0] - a[0], dy = b[1] - a[1];
    if (t === "circle" || t === "box") {
      const d = Math.max(Math.abs(dx), Math.abs(dy));
      return [a[0] + Math.sign(dx || 1) * d, a[1] + Math.sign(dy || 1) * d];
    }
    const ang = Math.round(Math.atan2(dy, dx) / (Math.PI / 4)) * (Math.PI / 4), len = Math.hypot(dx, dy);
    return [a[0] + Math.cos(ang) * len, a[1] + Math.sin(ang) * len];
  }
  const handles = (k) => [
    [k.x, k.y, "nw"], [k.x + k.w / 2, k.y, "n"], [k.x + k.w, k.y, "ne"], [k.x + k.w, k.y + k.h / 2, "e"],
    [k.x + k.w, k.y + k.h, "se"], [k.x + k.w / 2, k.y + k.h, "s"], [k.x, k.y + k.h, "sw"], [k.x, k.y + k.h / 2, "w"],
  ];
  const save = () => { history.push({ shapes: shapes.slice(), crop }); if (history.length > 200) history.shift(); };

  function begin(p) {
    const pt = clampPt(toPic(p));
    if (tool === "crop") {
      const hit = crop && handles(crop).find(([hx, hy]) => Math.hypot(hx - pt[0], hy - pt[1]) * z.s <= HANDLE + 4);
      const inside = crop && pt[0] > crop.x && pt[0] < crop.x + crop.w && pt[1] > crop.y && pt[1] < crop.y + crop.h;
      cur = { t: "crop", from: pt, start: crop ? { ...crop } : null, mode: hit ? hit[2] : inside ? "move" : "new", rect: crop || { x: pt[0], y: pt[1], w: 0, h: 0 } };
    } else if (tool === "pen") {
      cur = { t: "pen", c: color, pts: [pt], raw: [pt] };
    } else {
      cur = { t: tool, c: color, a: pt, b: pt, raw: pt };
    }
    last = p;
    redraw();
  }
  function move(p) {
    if (!cur) return;
    last = p;
    const pt = clampPt(toPic(p));
    if (cur.t === "crop") return moveCrop(pt);
    if (cur.t === "pen") {
      cur.raw.push(pt);
      cur.pts = shift ? [cur.raw[0], constrain("pen", cur.raw[0], pt)] : cur.raw.slice();
    } else {
      cur.raw = pt;
      cur.b = clampPt(constrain(cur.t, cur.a, pt));
    }
    redraw();
  }
  function moveCrop(pt) {
    const { from, start, mode } = cur;
    if (mode === "new") {
      const [x, y, w, h] = box(from, constrain("box", from, pt));
      cur.rect = { x, y, w, h };
    } else if (mode === "move") {
      const dx = pt[0] - from[0], dy = pt[1] - from[1];
      cur.rect = { ...start, x: Math.min(Math.max(start.x + dx, 0), W - start.w), y: Math.min(Math.max(start.y + dy, 0), H - start.h) };
    } else {
      let x1 = start.x, y1 = start.y, x2 = start.x + start.w, y2 = start.y + start.h;
      if (mode.includes("w")) x1 = pt[0];
      if (mode.includes("e")) x2 = pt[0];
      if (mode.includes("n")) y1 = pt[1];
      if (mode.includes("s")) y2 = pt[1];
      const [x, y, w, h] = box([x1, y1], [x2, y2]);
      cur.rect = { x, y, w, h };
    }
    redraw();
  }
  function end() {
    if (!cur) return;
    const c = cur;
    cur = null;
    if (c.t === "crop") {
      const r = c.rect;
      const changed = !c.start || r.x !== c.start.x || r.y !== c.start.y || r.w !== c.start.w || r.h !== c.start.h;
      if (changed) {
        save();
        crop = r.w >= MIN_CROP && r.h >= MIN_CROP ? { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.w), h: Math.round(r.h) } : null;
      }
    } else {
      delete c.raw;
      save();
      shapes.push(c);
    }
    redraw();
    onChange();
  }

  // ---- pointers
  host.addEventListener("pointerdown", (e) => {
    if (!img || (e.pointerType === "mouse" && e.button !== 0)) return;
    e.preventDefault();
    host.setPointerCapture(e.pointerId);
    pointers.set(e.pointerId, local(e));
    shift = e.shiftKey;
    if (pointers.size === 2) {
      cur = null; // the first finger's stroke was the start of a pinch
      redraw();
      const [a, b] = [...pointers.values()];
      pinch = { d: Math.hypot(a.x - b.x, a.y - b.y) || 1, m: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, z: { ...z } };
    } else if (pointers.size === 1) {
      begin(local(e));
    }
  });
  host.addEventListener("pointermove", (e) => {
    if (!pointers.has(e.pointerId)) return;
    pointers.set(e.pointerId, local(e));
    shift = e.shiftKey;
    if (pinch && pointers.size >= 2) {
      const [a, b] = [...pointers.values()];
      const d = Math.hypot(a.x - b.x, a.y - b.y), m = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
      const s = Math.min(Math.max(pinch.z.s * d / pinch.d, fit), 8);
      const cx = (pinch.m.x - pinch.z.x) / pinch.z.s, cy = (pinch.m.y - pinch.z.y) / pinch.z.s;
      z = { s, x: m.x - cx * s, y: m.y - cy * s };
      clampView();
      apply();
      redraw(); // handle and dash sizes follow the zoom
    } else if (!pinch) {
      for (const ev of e.getCoalescedEvents?.() || [e]) move(local(ev));
    }
  });
  const up = (e) => {
    if (!pointers.delete(e.pointerId)) return;
    if (pinch) { if (!pointers.size) pinch = null; return; }
    if (e.type === "pointercancel") { cur = null; redraw(); return; }
    end();
  };
  host.addEventListener("pointerup", up);
  host.addEventListener("pointercancel", up);
  host.addEventListener("wheel", (e) => {
    if (!img) return;
    e.preventDefault();
    const p = local(e);
    if (e.ctrlKey || e.metaKey) return zoomAt(p.x, p.y, z.s * Math.exp(-e.deltaY / 300));
    z.x -= e.deltaX;
    z.y -= e.deltaY;
    clampView();
    apply();
  }, { passive: false });
  const onShift = (e) => {
    if (e.key !== "Shift" || shift === (e.type === "keydown")) return;
    shift = e.type === "keydown";
    if (cur && last) move(last);
  };
  addEventListener("keydown", onShift);
  addEventListener("keyup", onShift);

  return {
    // open shows picture (an ImageBitmap or image) of width×height CSS px.
    open(picture, width, height) {
      img = picture;
      W = width;
      H = height;
      R = picture.width / width || 1;
      canvas.width = Math.round(W * R);
      canvas.height = Math.round(H * R);
      canvas.style.width = `${W}px`;
      canvas.style.height = `${H}px`;
      shapes = []; crop = null; history = []; cur = null; pinch = null; pointers.clear();
      layout(true);
      render();
      onChange();
    },
    close() {
      img?.close?.();
      img = null;
      cur = null;
      ctx.clearRect(0, 0, canvas.width, canvas.height);
    },
    destroy() {
      this.close();
      ro.disconnect();
      removeEventListener("keydown", onShift);
      removeEventListener("keyup", onShift);
    },
    get tool() { return tool; },
    set tool(t) { tool = t; redraw(); onChange(); },
    get color() { return color; },
    set color(c) { color = c; onChange(); },
    get size() { return crop ? { w: crop.w, h: crop.h } : { w: W, h: H }; },
    get crop() { return crop; },
    get empty() { return !shapes.length && !crop; },
    get canUndo() { return history.length > 0; },
    get zoomed() { return z.s > fit + 1e-3; },
    undo() {
      const s = history.pop();
      if (!s) return;
      shapes = s.shapes;
      crop = s.crop;
      redraw();
      onChange();
    },
    clear() {
      if (this.empty) return;
      save();
      shapes = [];
      crop = null;
      redraw();
      onChange();
    },
    fit() { layout(true); redraw(); },
    // flatten renders the picture, the marks and the crop (without the
    // dimming) to a PNG at the picture's resolution.
    flatten() {
      const k = crop || { x: 0, y: 0, w: W, h: H };
      const out = document.createElement("canvas");
      out.width = Math.max(1, Math.round(k.w * R));
      out.height = Math.max(1, Math.round(k.h * R));
      const c = out.getContext("2d");
      c.translate(-k.x * R, -k.y * R);
      paint(c, false);
      return new Promise((ok, no) => out.toBlob((b) => (b ? ok(b) : no(new Error("Couldn't make the picture."))), "image/png"));
    },
  };
}
