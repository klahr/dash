// Markdown prompt editor: a textarea with a syntax-highlighted layer behind
// it (same font and metrics, so the caret lines up), plus autosaved drafts
// in localStorage so a closed sheet, a reload or a crash loses nothing.

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

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

// ---- rendering (chat answers). Everything is escaped first; links only keep
// http(s) and mailto URLs, so model output can't inject markup or scripts.

const SAFE_URL = /^(https?:|mailto:)/i;

// Code spans and links are stashed first so the emphasis rules can't reach
// into them (or into an href).
function inlineHTML(s) {
  const kept = [];
  const keep = (html) => `\u0000${kept.push(html) - 1}\u0000`;
  const a = (url, text) => keep(`<a href="${url}" target="_blank" rel="noopener noreferrer">${text}</a>`);
  s = esc(s)
    .replace(/(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)/g, (_, __, c) => keep(`<code>${c.trim() || c}</code>`))
    .replace(/!?\[([^\]]*)\]\(([^)\s]+)(?:\s+&quot;[^)]*&quot;)?\)/g, (m, text, url) => (SAFE_URL.test(url) ? a(url, text || url) : m))
    .replace(/(^|[\s(])(https?:\/\/[^\s<)]+[^\s<).,:;!?'&])/g, (_, pre, url) => pre + a(url, url))
    .replace(/(\*\*|__)(?=\S)([\s\S]+?\S)\1/g, "<strong>$2</strong>")
    .replace(/(^|[^*\w])([*_])(?=\S)([^*_]*?\S)\2(?![*\w])/g, "$1<em>$3</em>")
    .replace(/~~(?=\S)([\s\S]+?\S)~~/g, "<del>$1</del>");
  while (s.includes("\u0000")) s = s.replace(/\u0000(\d+)\u0000/g, (_, i) => kept[i]); // links can hold code
  return s;
}

const cells = (row) => row.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
const LIST = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;

function blocks(lines) {
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    let m;
    if (!line.trim()) { i++; continue; }
    if ((m = line.match(/^\s*(```+|~~~+)\s*([\w+-]*)/))) {
      const fence = m[1], body = [];
      for (i++; i < lines.length && !(lines[i].trim().startsWith(fence[0].repeat(fence.length)) && !lines[i].trim().slice(fence.length).trim()); i++) body.push(lines[i]);
      i++;
      out.push(`<pre><code${m[2] ? ` class="lang-${m[2]}"` : ""}>${esc(body.join("\n"))}</code></pre>`);
      continue;
    }
    if ((m = line.match(/^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/))) {
      out.push(`<h${m[1].length}>${inlineHTML(m[2])}</h${m[1].length}>`);
      i++; continue;
    }
    if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) { out.push("<hr>"); i++; continue; }
    if (/^\s*>/.test(line)) {
      const body = [];
      for (; i < lines.length && /^\s*>/.test(lines[i]); i++) body.push(lines[i].replace(/^\s*>\s?/, ""));
      out.push(`<blockquote>${blocks(body)}</blockquote>`);
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/.test(lines[i + 1])) {
      const align = cells(lines[i + 1]).map((c) => (c.endsWith(":") ? (c.startsWith(":") ? "center" : "right") : c.startsWith(":") ? "left" : ""));
      const row = (r, tag) => `<tr>${cells(r).map((c, j) => `<${tag}${align[j] ? ` style="text-align:${align[j]}"` : ""}>${inlineHTML(c)}</${tag}>`).join("")}</tr>`;
      const head = row(line, "th"), body = [];
      for (i += 2; i < lines.length && lines[i].includes("|") && lines[i].trim(); i++) body.push(row(lines[i], "td"));
      out.push(`<div class="table-wrap"><table><thead>${head}</thead><tbody>${body.join("")}</tbody></table></div>`);
      continue;
    }
    if ((m = line.match(LIST))) {
      const indent = m[1].length, ordered = /\d/.test(m[2]);
      const items = [];
      while (i < lines.length) {
        const lm = lines[i].match(LIST);
        if (lm && lm[1].length <= indent) {
          if (/\d/.test(lm[2]) !== ordered) break;
          items.push([lm[3]]); i++; continue;
        }
        if (!lines[i].trim()) {
          // A blank line ends the list unless more indented content or another item follows.
          const next = lines[i + 1];
          if (next != null && (next.match(LIST)?.[1].length >= indent || /^\s{2,}\S/.test(next))) { items[items.length - 1].push(""); i++; continue; }
          break;
        }
        if (lm || /^\s{2,}\S/.test(lines[i]) || !items.length) { items[items.length - 1].push(lines[i].replace(new RegExp(`^\\s{0,${indent + 4}}`), "")); i++; continue; }
        if (/^\s*(#|>|```|~~~)/.test(lines[i])) break;
        items[items.length - 1][items[items.length - 1].length - 1] += "\n" + lines[i].trim(); i++; // lazy continuation
      }
      const start = ordered && parseInt(m[2], 10) !== 1 ? ` start="${parseInt(m[2], 10)}"` : "";
      const lis = items.map(([first, ...rest]) => {
        const t = first.match(/^\[([ xX])\]\s+(.*)$/);
        const head = t ? `<input type="checkbox" disabled${t[1] !== " " ? " checked" : ""}> ${inlineHTML(t[2])}` : inlineHTML(first);
        const sub = rest.some((r) => r.trim()) ? blocks(rest) : "";
        return `<li${t ? ' class="task"' : ""}>${head}${sub}</li>`;
      });
      out.push(ordered ? `<ol${start}>${lis.join("")}</ol>` : `<ul>${lis.join("")}</ul>`);
      continue;
    }
    const para = [lines[i++].trim()];
    for (; i < lines.length && lines[i].trim() && !/^\s*(```|~~~|#{1,6}\s|>)/.test(lines[i]) && !LIST.test(lines[i]) && !/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(lines[i]); i++) para.push(lines[i].trim());
    out.push(`<p>${para.map(inlineHTML).join("<br>")}</p>`);
  }
  return out.join("");
}

export function renderMarkdown(text) {
  return blocks(text.replace(/\r\n?/g, "\n").split("\n"));
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
