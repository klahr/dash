// Syntax colours for code: the prompt and file editors (editor.js) and the
// fenced code blocks markdown.js renders (chat replies, task prompts, goals).
// Both use codeHighlighter, whose tok-* classes style.css colours, so code
// looks the same in an editor and in a reply, in light and dark mode.
//
// Code blocks are coloured as they appear in the page (a MutationObserver),
// wherever renderMarkdown's HTML is put. The text is parsed with the
// language's CodeMirror parser and rebuilt as spans of text nodes, never as
// HTML; Copy still copies the code's textContent. A block with no language,
// one CodeMirror doesn't know, or one over MAX_CODE stays plain text.

import { LanguageDescription, languages, highlightCode, tagHighlighter, tags as t } from "./vendor/codemirror.js";

export const codeHighlighter = tagHighlighter([
  // markdown (the prompt editor)
  { tag: t.heading, class: "tok-heading" },
  { tag: t.processingInstruction, class: "tok-mark" },
  { tag: t.contentSeparator, class: "tok-faint" },
  { tag: t.emphasis, class: "tok-emphasis" },
  { tag: t.strong, class: "tok-strong" },
  { tag: t.strikethrough, class: "tok-strike" },
  { tag: t.link, class: "tok-link" },
  { tag: t.url, class: "tok-url" },
  { tag: t.monospace, class: "tok-mono" },
  { tag: t.quote, class: "tok-muted" },
  { tag: [t.labelName, t.meta], class: "tok-muted" },
  // code
  { tag: [t.keyword, t.controlKeyword, t.moduleKeyword, t.operatorKeyword, t.definitionKeyword, t.modifier], class: "tok-keyword" },
  { tag: [t.string, t.special(t.string), t.regexp, t.character], class: "tok-string" },
  { tag: t.comment, class: "tok-comment" },
  { tag: [t.number, t.bool, t.null, t.atom, t.escape], class: "tok-number" },
  { tag: [t.typeName, t.className, t.namespace, t.standard(t.typeName)], class: "tok-type" },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.definition(t.function(t.variableName)), t.macroName], class: "tok-function" },
  { tag: [t.tagName, t.attributeName], class: "tok-tag" },
  { tag: t.inserted, class: "tok-inserted" },
  { tag: t.deleted, class: "tok-deleted" },
  { tag: t.invalid, class: "tok-invalid" },
]);

// A fence's info string names the language ("go", "python") or, often, its
// file extension ("py", "rs").
export const fenceLanguage = (info) => {
  const name = (info || "").trim().split(/\s/)[0].toLowerCase();
  if (!name) return null;
  return LanguageDescription.matchLanguageName(languages, name, false) || languages.find((l) => l.extensions.includes(name)) || null;
};

// Bigger blocks stay plain: parsing them would hold up the page.
export const MAX_CODE = 100_000;

// tokenize is code parsed with a loaded language (a LanguageSupport) as
// [text, classes] pieces; a line break is ["\n", ""].
export function tokenize(code, support) {
  const out = [];
  highlightCode(code, support.language.parser.parse(code), codeHighlighter, (text, cls) => out.push([text, cls]), () => out.push(["\n", ""]));
  return out;
}

// tokens is code highlighted as the fence language lang (see tokenize), or
// null for an unknown language or code over MAX_CODE.
export async function tokens(code, lang) {
  const d = code.length <= MAX_CODE ? fenceLanguage(lang) : null;
  if (!d) return null;
  return tokenize(code, d.support || await d.load());
}

// ---- in the page

// Blocks already coloured are marked data-hl, so they aren't again.
const BLOCKS = 'pre > code[class*="lang-"]:not([data-hl])';

// Highlighted blocks by language and text, most recently used last. A reply
// streaming in is rendered again on every piece of text: its finished
// blocks are copied from here at once, so they don't flicker.
const done = new Map();
const DONE_MAX = 64;
const failed = new Set(); // languages that wouldn't load

function fragment(pieces) {
  const f = document.createDocumentFragment();
  for (const [text, cls] of pieces) {
    if (!cls) { f.append(text); continue; }
    const s = document.createElement("span");
    s.className = cls;
    s.textContent = text;
    f.append(s);
  }
  return f;
}

function block(code) {
  // A block whose closing fence hasn't come yet waits while its reply streams in.
  if (code.hasAttribute("data-open") && code.closest(".pending")) return;
  const lang = [...code.classList].find((c) => c.startsWith("lang-"))?.slice(5);
  const text = code.textContent;
  const d = text.length <= MAX_CODE ? fenceLanguage(lang) : null;
  if (!d || failed.has(d)) { code.dataset.hl = "plain"; return; }
  const key = `${d.name}\u0000${text}`;
  let f = done.get(key);
  if (f) done.delete(key);
  else {
    if (!d.support) { load(d); return; } // coloured once it has loaded
    f = fragment(tokenize(text, d.support));
    if (done.size >= DONE_MAX) done.delete(done.keys().next().value);
  }
  done.set(key, f);
  code.replaceChildren(f.cloneNode(true));
  code.dataset.hl = "";
}

const loading = new Set();
function load(d) {
  if (loading.has(d)) return;
  loading.add(d);
  d.load().catch(() => failed.add(d)).finally(() => { loading.delete(d); highlightBlocks(document.body); });
}

// highlightBlocks colours the code blocks in root (or root itself) that
// aren't yet.
export function highlightBlocks(root) {
  if (!root?.querySelectorAll) return;
  if (root.matches(BLOCKS)) return block(root);
  for (const code of root.querySelectorAll(BLOCKS)) block(code);
}

// New blocks are coloured as they are added; a block left open by a reply
// that streamed is coloured when the reply ends (its .pending goes).
if (typeof document !== "undefined" && typeof MutationObserver !== "undefined") {
  const pendingEnded = (r) => r.type === "attributes" && r.oldValue?.includes("pending") && !r.target.classList.contains("pending");
  const watch = new MutationObserver((records) => {
    for (const r of records) {
      if (pendingEnded(r)) highlightBlocks(r.target);
      else for (const n of r.addedNodes) if (n.nodeType === 1 && (n.tagName === "CODE" || n.firstElementChild)) highlightBlocks(n);
    }
  });
  const start = () => {
    watch.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["class"], attributeOldValue: true });
    highlightBlocks(document.body);
  };
  document.body ? start() : addEventListener("DOMContentLoaded", start, { once: true });
}
