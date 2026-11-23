// Markdown rendering for chat answers and notes, kept free of the DOM so
// Node tests can run it (web/markdown_test.mjs). editor.js re-exports
// renderMarkdown.

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");

// Everything is escaped first; links only keep http(s) and mailto URLs, and
// an svg block's picture is an <img> of a data: URL (see svgFigure), so
// model output can't inject markup or scripts.

const SAFE_URL = /^(https?:|mailto:)/i;

// Code spans and links are stashed first so the emphasis rules can't reach
// into them (or into an href).
function inlineHTML(s) {
  const kept = [];
  const keep = (html) => `\u0000${kept.push(html) - 1}\u0000`;
  const a = (url, text) => keep(`<a href="${url}" target="_blank" rel="noopener noreferrer">${text}</a>`);
  // \u0000 marks stashed pieces, so a NUL in the text itself (pasted from a
  // terminal, say) would never be replaced and the loop below would spin forever.
  s = esc(s.replace(/\u0000/g, "�"))
    .replace(/(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)/g, (_, __, c) => keep(`<code>${c.trim() || c}</code>`))
    // URLs must never take in a \u0000N\u0000 placeholder: unstashing it would
    // put a whole <a …> (or <code>) inside this link's href and break out of
    // the attribute.
    .replace(/!?\[([^\]]*)\]\(([^)\s\u0000]+)(?:\s+&quot;[^)]*&quot;)?\)/g, (m, text, url) => (SAFE_URL.test(url) ? a(url, text || url) : m))
    .replace(/(^|[\s(])(https?:\/\/[^\s<)\u0000]+[^\s<).,:;!?'&\u0000])/g, (_, pre, url) => pre + a(url, url))
    .replace(/(\*\*|__)(\S|\S[\s\S]*?\S)\1/g, "<strong>$2</strong>")
    .replace(/(^|[^*\w])([*_])(?=\S)([^*_]*?\S)\2(?![*\w])/g, "$1<em>$3</em>")
    .replace(/~~(\S|\S[\s\S]*?\S)~~/g, "<del>$1</del>");
  const STASHED = /\u0000(\d+)\u0000/;
  while (STASHED.test(s)) s = s.replace(/\u0000(\d+)\u0000/g, (_, i) => kept[i]); // links can hold code
  return s;
}

const cells = (row) => row.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
const LIST = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;

// codeBlock is a fenced code block, with its copy button. The buttons
// (copy, and an svg picture's Code) are handled by document-level click
// listeners in app/dialogs.js, so they keep working when streaming replies
// re-render the HTML.
function codeBlock(src, lang, hidden = false) {
  return `<div class="code-block"${hidden ? " hidden" : ""}><button type="button" class="code-copy" title="Copy" aria-label="Copy code"><svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M9 9h11v11H9zM5 15H4V4h11v1" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg></button><pre><code${lang ? ` class="lang-${lang}"` : ""}>${esc(src)}</code></pre></div>`;
}

// svgFigure shows an svg code block as the picture it draws, with its code
// a click away (Code), or is "" when it holds no <svg>. The SVG is never
// put in the page: it is an <img> of a data: URL, where scripts don't run
// and nothing outside it loads. Clicking it opens the lightbox.
function svgFigure(src, lang) {
  if (!/<svg[\s/>]/i.test(src)) return "";
  const url = esc(`data:image/svg+xml;charset=utf-8,${encodeURIComponent(src.trim())}`);
  const size = new TextEncoder().encode(src).length;
  return `<figure class="md-svg" data-preview-group>` +
    `<a class="md-svg-pic" href="${url}" data-preview data-name="drawing.svg" data-type="image/svg+xml" data-size="${size}" title="Open the picture">` +
    `<img src="${url}" alt="A picture, drawn in SVG"></a>` +
    `<figcaption><button type="button" class="md-svg-code" aria-expanded="false">Code</button></figcaption>` +
    codeBlock(src, lang, true) + `</figure>`;
}

function blocks(lines) {
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    let m;
    if (!line.trim()) { i++; continue; }
    if ((m = line.match(/^\s*(```+|~~~+)\s*([\w+-]*)/))) {
      const fence = m[1], lang = m[2], body = [];
      for (i++; i < lines.length && !(lines[i].trim().startsWith(fence[0].repeat(fence.length)) && !lines[i].trim().slice(fence.length).trim()); i++) body.push(lines[i]);
      const closed = i < lines.length; // a reply still streaming may not have closed it yet
      i++;
      const src = body.join("\n");
      out.push((closed && lang.toLowerCase() === "svg" && svgFigure(src, lang)) || codeBlock(src, lang));
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
