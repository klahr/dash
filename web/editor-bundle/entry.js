// Everything web/static/editor.js needs from CodeMirror, as one ES module.
// language-data's lazy language imports are inlined (no code splitting), so
// fenced-code highlighting works offline from this single file.
export { EditorView, keymap, placeholder, drawSelection, lineNumbers, highlightActiveLine, highlightActiveLineGutter } from "@codemirror/view";
export { EditorState, Compartment, Prec, Transaction } from "@codemirror/state";
export { history, historyKeymap, defaultKeymap, insertNewline } from "@codemirror/commands";
export { syntaxHighlighting, HighlightStyle, LanguageDescription, bracketMatching } from "@codemirror/language";
export { tags } from "@lezer/highlight";
export { markdown } from "@codemirror/lang-markdown";
export { languages } from "@codemirror/language-data";
export { vim, getCM } from "@replit/codemirror-vim";
