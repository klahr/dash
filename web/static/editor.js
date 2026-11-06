// Markdown prompt editor (CodeMirror 6, optional vim keys) on top of a hidden
// textarea that stays the source of truth, plus autosaved drafts in
// localStorage so a closed sheet, a reload or a crash loses nothing.

import {
  EditorView, EditorState, Compartment, Prec, Transaction, keymap, placeholder, drawSelection,
  history, historyKeymap, defaultKeymap, insertNewline, syntaxHighlighting, HighlightStyle, LanguageDescription, tags as t,
  markdown, languages, vim, getCM,
} from "./vendor/codemirror.js";

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

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

// ---- prompt editor

const LIST_ITEM = /^\s*([-*+]|\d+[.)])\s/;

function insideFence(doc, pos) {
  return ((doc.sliceString(0, pos).match(/^\s*(```|~~~)/gm) || []).length % 2) === 1;
}

// Enter continues a list ("- ", "1. ", "- [ ] "); Enter on an empty item ends it.
function continueList(view) {
  const { state } = view;
  const r = state.selection.main;
  if (!r.empty || state.selection.ranges.length > 1) return false;
  const line = state.doc.lineAt(r.head);
  const m = state.sliceDoc(line.from, r.head).match(/^(\s*)([-*+]|\d+)([.)]?)(\s+)(\[[ xX]\]\s)?(.*)$/);
  if (!m || (!m[3] && !/[-*+]/.test(m[2])) || (m[3] && !/^\d+$/.test(m[2]))) return false;
  if (!m[6].trim()) { // empty item: end the list
    view.dispatch({ changes: { from: line.from, to: r.head }, scrollIntoView: true, userEvent: "delete" });
    return true;
  }
  const marker = m[3] ? `${Number(m[2]) + 1}${m[3]}` : m[2];
  const insert = `\n${m[1]}${marker}${m[4]}${m[5] ? "[ ] " : ""}`;
  view.dispatch({ changes: { from: r.head, insert }, selection: { anchor: r.head + insert.length }, scrollIntoView: true, userEvent: "input" });
  return true;
}

// Tab indents list items and code instead of leaving the field; Shift-Tab
// outdents. Plain text returns false, so Tab moves focus on.
function indent(view, out) {
  const { state } = view;
  const r = state.selection.main;
  const first = state.doc.lineAt(r.from), last = state.doc.lineAt(r.to);
  if (r.empty && !LIST_ITEM.test(first.text) && !insideFence(state.doc, r.head)) return false;
  const changes = [];
  for (let n = first.number; n <= last.number; n++) {
    const line = state.doc.line(n);
    if (!out) changes.push({ from: line.from, insert: "  " });
    else {
      const k = line.text.match(/^ {1,2}/)?.[0].length || 0;
      if (k) changes.push({ from: line.from, to: line.from + k });
    }
  }
  if (changes.length) view.dispatch({ changes, userEvent: out ? "delete.dedent" : "input.indent" });
  return true;
}

// Colours come from the page's CSS variables, so they follow its theme.
const highlightStyle = HighlightStyle.define([
  { tag: t.heading, color: "var(--text)", fontWeight: "600" },
  { tag: t.processingInstruction, color: "var(--accent)" },
  { tag: t.contentSeparator, color: "var(--faint)" },
  { tag: t.emphasis, color: "var(--goal)", fontStyle: "italic" },
  { tag: t.strong, color: "var(--text)", fontWeight: "600" },
  { tag: t.strikethrough, textDecoration: "line-through" },
  { tag: t.link, color: "var(--accent)" },
  { tag: t.url, color: "var(--muted)", textDecoration: "underline", textDecorationColor: "var(--faint)" },
  { tag: t.monospace, color: "var(--review)" },
  { tag: t.quote, color: "var(--muted)" },
  { tag: [t.labelName, t.meta], color: "var(--muted)" },
  // code in fences
  { tag: [t.keyword, t.controlKeyword, t.moduleKeyword, t.operatorKeyword, t.definitionKeyword, t.modifier], color: "var(--goal)" },
  { tag: [t.string, t.special(t.string), t.regexp, t.character], color: "var(--merged)" },
  { tag: t.comment, color: "var(--faint)", fontStyle: "italic" },
  { tag: [t.number, t.bool, t.null, t.atom, t.escape], color: "var(--running)" },
  { tag: [t.typeName, t.className, t.namespace, t.standard(t.typeName)], color: "var(--review)" },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.definition(t.function(t.variableName)), t.macroName], color: "var(--accent)" },
  { tag: [t.tagName, t.attributeName], color: "var(--accent)" },
  { tag: t.invalid, color: "var(--failed)" },
]);

const theme = EditorView.theme({
  "&": { color: "var(--text)", backgroundColor: "var(--bg)", border: "1px solid var(--line)", borderRadius: "var(--r-m)" },
  "&.cm-focused": { outline: "none", borderColor: "var(--accent)" },
  ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.55", minHeight: "178px" },
  ".cm-content": { padding: "11px 0", caretColor: "var(--text)" },
  ".cm-line": { padding: "0 12px" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--text)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection":
    { backgroundColor: "color-mix(in srgb, var(--accent) 35%, transparent)" },
  ".cm-placeholder": { color: "var(--faint)" },
  ".cm-panels": { backgroundColor: "var(--surface)", color: "var(--muted)", borderRadius: "0 0 var(--r-m) var(--r-m)" },
  ".cm-panels.cm-panels-bottom": { borderTop: "1px solid var(--line)" },
  ".cm-vim-panel": { fontFamily: "var(--mono)", fontSize: "12px", minHeight: "0", padding: "3px 12px" },
  ".cm-vim-panel input": { color: "var(--text)", fontFamily: "inherit" },
  ".cm-vim-message": { color: "var(--running) !important" }, // vim sets red inline
  "&.cm-focused .cm-fat-cursor": { background: "color-mix(in srgb, var(--accent) 70%, transparent)", color: "var(--bg)" },
  "&.cm-editor:not(.cm-focused) .cm-fat-cursor": { outline: "solid 1px var(--accent)" },
  ".cm-searchMatch": { backgroundColor: "color-mix(in srgb, var(--running) 30%, transparent)" },
}, { dark: true });

// Vim keys are a setting of this browser, off by default.
const VIM_KEY = "dash.vim";
const editors = new Set();
const vimKeys = () => [vim({ status: true }), drawSelection()];
export const vimOn = () => { try { return localStorage.getItem(VIM_KEY) === "1"; } catch { return false; } };
export function setVim(on) {
  try { localStorage.setItem(VIM_KEY, on ? "1" : "0"); } catch {}
  for (const e of editors) e.setVim(on);
}

// In vim mode Esc leaves insert (or visual) mode; Esc in normal mode, with
// nothing pending, still closes the dialog the way it always has.
const vimNormal = (view) => {
  const v = getCM(view)?.state.vim;
  if (!v || v.insertMode || v.visualMode) return false;
  const s = v.inputState;
  return !(s.operator || s.motion || s.keyBuffer.length || s.prefixRepeat.length || s.motionRepeat.length);
};

// A fence's info string names the language ("go", "python") or, often, its
// file extension ("py", "rs").
const fenceLanguage = (info) => {
  const name = info.trim().split(/\s/)[0].toLowerCase();
  return LanguageDescription.matchLanguageName(languages, name, false) || languages.find((l) => l.extensions.includes(name)) || null;
};

const hasFiles = (data) => !!data?.files?.length;

// mdEditor replaces a textarea with CodeMirror. The textarea stays (hidden)
// as the source of truth: reading .value works as before, and writing it
// (or a form reset) updates the editor. el is the editor's element.
export function mdEditor(textarea) {
  const native = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
  const wrap = document.createElement("div");
  wrap.className = "md-editor";
  textarea.after(wrap);
  textarea.hidden = true;
  const labelledBy = textarea.getAttribute("aria-labelledby");
  const vimMode = new Compartment(), readOnly = new Compartment();
  let quiet = false; // a programmatic write: not an edit by the user
  const view = new EditorView({
    parent: wrap,
    state: EditorState.create({
      doc: textarea.value,
      extensions: [
        vimMode.of(vimOn() ? vimKeys() : []),
        history(),
        Prec.high(keymap.of([
          { key: "Enter", run: (v) => continueList(v) || insertNewline(v), shift: insertNewline },
          { key: "Tab", run: (v) => indent(v, false), shift: (v) => indent(v, true) },
        ])),
        // Esc closes the dialog and Ctrl-Enter submits the form, as before.
        keymap.of([...defaultKeymap.filter((b) => b.key !== "Escape" && b.key !== "Mod-Enter"), ...historyKeymap]),
        markdown({ codeLanguages: fenceLanguage, addKeymap: false }),
        syntaxHighlighting(highlightStyle),
        EditorView.lineWrapping,
        placeholder(textarea.placeholder),
        readOnly.of(EditorState.readOnly.of(textarea.readOnly)),
        EditorView.contentAttributes.of({
          spellcheck: "true", autocorrect: "on", autocapitalize: "sentences", writingsuggestions: "true",
          ...(labelledBy ? { "aria-labelledby": labelledBy } : {}),
        }),
        // Files are attachments: leave pasted or dropped files to the form.
        EditorView.domEventHandlers({ paste: (e) => hasFiles(e.clipboardData), drop: (e) => hasFiles(e.dataTransfer) }),
        EditorView.updateListener.of((u) => {
          if (!u.docChanged) return;
          native.set.call(textarea, u.state.doc.toString());
          if (!quiet) textarea.dispatchEvent(new Event("input", { bubbles: true }));
        }),
        theme,
      ],
    }),
  });

  const render = () => {
    const v = native.get.call(textarea);
    if (v === view.state.doc.toString()) return;
    quiet = true;
    try {
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: v },
        selection: { anchor: Math.min(view.state.selection.main.head, v.length) },
        annotations: Transaction.addToHistory.of(false),
      });
    } finally { quiet = false; }
  };
  Object.defineProperty(textarea, "value", {
    configurable: true,
    get() { return native.get.call(this); },
    set(v) { native.set.call(this, v); render(); },
  });
  textarea.focus = (opts) => view.focus(opts);
  // reset() restores the default value without the setter; it runs right after the event.
  textarea.form?.addEventListener("reset", () => queueMicrotask(render));
  new MutationObserver(() => view.dispatch({ effects: readOnly.reconfigure(EditorState.readOnly.of(textarea.readOnly)) }))
    .observe(textarea, { attributes: true, attributeFilter: ["readonly"] });

  view.dom.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && e.target === view.contentDOM && vimNormal(view)) e.stopPropagation(); // past vim, to the dialog
  }, true);

  const ed = {
    el: wrap,
    view,
    render,
    setVim(on) { view.dispatch({ effects: vimMode.reconfigure(on ? vimKeys() : []) }); },
  };
  editors.add(ed);
  return ed;
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
