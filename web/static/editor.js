// Markdown prompt editor: a textarea with a syntax-highlighted layer behind
// it (same font and metrics, so the caret lines up), plus autosaved drafts
// in localStorage so a closed sheet, a reload or a crash loses nothing.

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// Inline markdown on one escaped line. Code spans go first and are kept out
// of the other rules.
function inline(line) {
  const codes = [];
  line = line.replace(/(`+)([^`]|[^`].*?[^`])\1(?!`)/g, (m) => {
    codes.push(m);
    return `\u0000${codes.length - 1}\u0000`;
  });
  line = line
    .replace(/(\*\*|__)(?=\S)(.+?\S)\1/g, '<span class="md-strong">$1$2$1</span>')
    .replace(/(^|[^*\w])([*_])(?=\S)([^*_]*?\S)\2(?![*\w])/g, '$1<span class="md-em">$2$3$2</span>')
    .replace(/(!?\[)([^\]]*)(\]\()([^)\s]*)(\))/g,
      '<span class="md-punct">$1</span><span class="md-link">$2</span><span class="md-punct">$3</span><span class="md-url">$4</span><span class="md-punct">$5</span>')
    .replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g, '$1<span class="md-url">$2</span>');
  return line.replace(/\u0000(\d+)\u0000/g, (_, i) => `<span class="md-code">${codes[i]}</span>`);
}

export function highlight(text) {
  let fence = null;
  const out = text.split("\n").map((raw) => {
    const line = esc(raw);
    const f = raw.match(/^\s*(```+|~~~+)/);
    if (fence) {
      if (f && f[1][0] === fence[0] && f[1].length >= fence.length) fence = null;
      return `<span class="md-fence">${line}</span>`;
    }
    if (f) { fence = f[1]; return `<span class="md-fence">${line}</span>`; }
    let m;
    if ((m = line.match(/^(#{1,6})(\s.*)?$/))) return `<span class="md-h"><span class="md-punct">${m[1]}</span>${inline(m[2] || "")}</span>`;
    if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(raw)) return `<span class="md-punct">${line}</span>`;
    if ((m = line.match(/^(\s*&gt;\s?)(.*)$/))) return `<span class="md-quote"><span class="md-punct">${m[1]}</span>${inline(m[2])}</span>`;
    if ((m = line.match(/^(\s*)([-*+]|\d+[.)])(\s+)(\[[ xX]\]\s)?(.*)$/))) {
      return `${m[1]}<span class="md-list">${m[2]}</span>${m[3]}${m[4] ? `<span class="md-task">${m[4]}</span>` : ""}${inline(m[5])}`;
    }
    return inline(line);
  });
  // A trailing newline needs a line box of its own, or the layers drift apart.
  return out.join("\n") + (text.endsWith("\n") ? " " : "");
}

// mdEditor wraps a textarea in place.
export function mdEditor(textarea) {
  const wrap = document.createElement("div");
  wrap.className = "md-editor";
  const layer = document.createElement("pre");
  layer.className = "md-layer";
  layer.setAttribute("aria-hidden", "true");
  textarea.classList.add("md-input");
  textarea.spellcheck = true;
  textarea.replaceWith(wrap);
  wrap.append(layer, textarea);
  const render = () => {
    layer.innerHTML = highlight(textarea.value);
    layer.scrollTop = textarea.scrollTop;
  };
  textarea.addEventListener("input", render);
  textarea.addEventListener("scroll", () => { layer.scrollTop = textarea.scrollTop; });
  new ResizeObserver(() => { layer.style.height = `${textarea.offsetHeight}px`; render(); }).observe(textarea);
  // Tab indents list items and code instead of leaving the field; Shift-Tab outdents.
  textarea.addEventListener("keydown", (e) => {
    if (e.key !== "Tab" || e.ctrlKey || e.metaKey || e.altKey) return;
    const { selectionStart: a, selectionEnd: b, value: v } = textarea;
    const start = v.lastIndexOf("\n", a - 1) + 1;
    const line = v.slice(start, v.indexOf("\n", a) === -1 ? v.length : v.indexOf("\n", a));
    if (a === b && !/^\s*([-*+]|\d+[.)])\s/.test(line) && !insideFence(v, a)) return; // plain text: Tab moves focus
    e.preventDefault();
    if (e.shiftKey) {
      const n = line.match(/^ {1,2}/)?.[0].length || 0;
      if (!n) return;
      textarea.setRangeText("", start, start + n, "preserve");
    } else {
      textarea.setRangeText("  ", start, start, "preserve");
    }
    render();
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  });
  // Enter continues a list ("- ", "1. ", "- [ ] "); Enter on an empty item ends it.
  textarea.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" || e.shiftKey || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return;
    const { selectionStart: a, selectionEnd: b, value: v } = textarea;
    if (a !== b) return;
    const start = v.lastIndexOf("\n", a - 1) + 1;
    const m = v.slice(start, a).match(/^(\s*)([-*+]|\d+)([.)]?)(\s+)(\[[ xX]\]\s)?(.*)$/);
    if (!m || (!m[3] && !/[-*+]/.test(m[2])) || (m[3] && !/^\d+$/.test(m[2]))) return;
    e.preventDefault();
    if (!m[6].trim()) { // empty item: end the list
      textarea.setRangeText("", start, a, "end");
    } else {
      const marker = m[3] ? `${Number(m[2]) + 1}${m[3]}` : m[2];
      textarea.setRangeText(`\n${m[1]}${marker}${m[4]}${m[5] ? "[ ] " : ""}`, a, a, "end");
    }
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  });
  render();
  return { render };
}

function insideFence(v, pos) {
  return ((v.slice(0, pos).match(/^\s*(```|~~~)/gm) || []).length % 2) === 1;
}

// ---- drafts

const PREFIX = "dash.draft.";
const store = {
  get(k) { try { return JSON.parse(localStorage.getItem(PREFIX + k)); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(PREFIX + k, JSON.stringify(v)); return true; } catch { return false; } },
  del(k) { try { localStorage.removeItem(PREFIX + k); } catch {} },
};

// drafts keeps the open form saved under a key while it is being edited.
// read() returns the form's fields, write(d) fills them.
export function drafts({ read, write, onStatus }) {
  let key = null, timer = null, base = "";
  const snapshot = () => JSON.stringify(read());
  const save = () => {
    clearTimeout(timer);
    timer = null;
    if (!key) return;
    const now = snapshot();
    if (now === base) { store.del(key); return; } // back to what's saved on the server
    if (store.set(key, { ...JSON.parse(now), at: Date.now() })) onStatus("saved");
  };
  addEventListener("pagehide", save);
  document.addEventListener("visibilitychange", () => { if (document.hidden) save(); });
  return {
    // open starts editing under key; returns the restored draft, if any.
    open(k) {
      key = k;
      base = snapshot();
      const d = store.get(k);
      if (!d) { onStatus(""); return null; }
      write(d);
      if (snapshot() === base) { store.del(k); onStatus(""); return null; }
      onStatus("restored", d.at);
      return d;
    },
    changed() { clearTimeout(timer); timer = setTimeout(save, 300); onStatus("typing"); },
    flush: save,
    // done forgets the draft (after a successful save); close keeps it.
    done() { clearTimeout(timer); if (key) store.del(key); key = null; },
    close() { save(); key = null; },
    discard() { if (key) store.del(key); },
    has: (k) => !!store.get(k),
  };
}
