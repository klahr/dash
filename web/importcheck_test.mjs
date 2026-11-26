// Checks the names the web client's scripts use: every name a script in
// web/static or web/static/app reads is declared in it, imported, or a
// browser/JavaScript global, and every name it imports from a relative
// module is one that module exports. `node --check` only checks syntax, so
// a name dropped from an import line (as sizeText once was from planning.js)
// passes it and fails only when the line runs in the browser.
//
// It parses with the JavaScript parser in the vendored CodeMirror bundle
// (static/vendor/codemirror.js), so it needs no npm packages. The parser
// recovers from the odd construct it misreads (a defaulted destructured
// arrow parameter, say), so the rest of that file is still checked. The scope
// analysis is per file, not per function: a name declared anywhere in a file
// counts as declared everywhere in it. That keeps it simple and free of
// false positives, and still catches a name a file never declares or imports.
//
// Run with `node --test web/`. A name that is a real global but missing from
// BROWSER_GLOBALS below goes in that list. A script's own `window.Name = …`
// makes Name a global for every script (as pagekit.js does with DashPage).
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import vm from "node:vm";
import { LanguageDescription, languages } from "./static/vendor/codemirror.js";

const here = dirname(fileURLToPath(import.meta.url));
const staticDir = join(here, "static");

// Names the browser provides, beyond the JavaScript built-ins (which come
// from a fresh vm context below, so Node-only globals such as process don't
// slip in). Window and worker (sw.js) globals both.
const BROWSER_GLOBALS = `
window self globalThis document navigator location history screen console
fetch Headers Request Response FormData Blob File FileReader FileList URL URLSearchParams
AbortController AbortSignal TextEncoder TextDecoder TextDecoderStream TextEncoderStream
ReadableStream WritableStream TransformStream CompressionStream DecompressionStream
setTimeout clearTimeout setInterval clearInterval requestAnimationFrame cancelAnimationFrame
requestIdleCallback cancelIdleCallback queueMicrotask structuredClone atob btoa
alert confirm prompt open close print getComputedStyle getSelection matchMedia scrollTo scrollBy
innerWidth innerHeight outerWidth outerHeight devicePixelRatio scrollX scrollY visualViewport
localStorage sessionStorage indexedDB caches crypto performance isSecureContext origin
Event CustomEvent EventTarget KeyboardEvent MouseEvent PointerEvent TouchEvent WheelEvent
FocusEvent InputEvent DragEvent ClipboardEvent ErrorEvent MessageEvent ProgressEvent
PopStateEvent HashChangeEvent StorageEvent SubmitEvent AnimationEvent TransitionEvent
Node Element HTMLElement HTMLInputElement HTMLTextAreaElement HTMLSelectElement
HTMLButtonElement HTMLAnchorElement HTMLImageElement HTMLVideoElement HTMLAudioElement
HTMLCanvasElement HTMLDialogElement HTMLFormElement HTMLDetailsElement HTMLTemplateElement
HTMLIFrameElement HTMLLabelElement HTMLOptionElement HTMLMediaElement SVGElement
Text Comment DocumentFragment Range Selection ShadowRoot NodeList HTMLCollection DOMParser
DOMRect DOMException XMLSerializer CSS CSSStyleSheet Image Audio Option
MutationObserver ResizeObserver IntersectionObserver
WebSocket EventSource BroadcastChannel MessageChannel MessagePort Worker
Notification PushManager PushSubscription ServiceWorker ServiceWorkerRegistration
Clipboard ClipboardItem DataTransfer
MediaRecorder MediaStream MediaStreamTrack MediaSource AudioContext webkitAudioContext
OfflineAudioContext AnalyserNode AudioWorkletNode
SpeechRecognition webkitSpeechRecognition SpeechSynthesisUtterance speechSynthesis
ImageData ImageBitmap createImageBitmap OffscreenCanvas Path2D
Intl WeakRef FinalizationRegistry
clients registration skipWaiting importScripts
addEventListener removeEventListener dispatchEvent
DOMMatrix DOMMatrixReadOnly DOMPoint RadioNodeList CropTarget
`.split(/\s+/).filter(Boolean);

const GLOBALS = new Set([...vm.runInNewContext("Object.getOwnPropertyNames(globalThis)"), ...BROWSER_GLOBALS, "undefined", "arguments"]);

const parser = (await LanguageDescription.matchFilename(languages, "x.js").load()).language.parser;

function children(node) {
  const out = [];
  for (let c = node.firstChild; c; c = c.nextSibling) out.push(c);
  return out;
}

// Splits an ImportGroup's or ExportGroup's names into [from, to] pairs:
// `{a as b, c}` gives [a, b] and [c, c].
function groupPairs(group, text) {
  const pairs = [];
  let item = [];
  const flush = () => {
    if (item.length) pairs.push([text(item[0]), text(item[item.length - 1])]);
    item = [];
  };
  for (const c of children(group)) {
    if (c.name === "," || c.name === "}") flush();
    else if (c.name !== "{" && c.name !== "as") item.push(c);
  }
  return pairs;
}

const stripQuotes = (s) => s.slice(1, -1);

// What one file declares, reads, imports and exports.
function analyze(file) {
  const src = readFileSync(file, "utf8");
  const text = (n) => src.slice(n.from, n.to);
  const line = (n) => src.slice(0, n.from).split("\n").length;
  const info = {
    file,
    defs: new Set(),
    refs: [], // {name, line}
    imports: [], // {spec, imported, local, line}; imported "*" is a namespace
    exports: new Set(),
    reexports: [], // {spec, imported, exported}; imported "*" is `export *`
    members: [], // {ns, name, line}: ns.name on a namespace import
    windowDefs: [...src.matchAll(/\bwindow\.([A-Za-z_$][\w$]*)\s*=(?!=)/g)].map((m) => m[1]),
  };

  // Every definition and read in a subtree.
  const walk = (node) => {
    switch (node.name) {
      case "VariableDefinition":
        info.defs.add(text(node));
        return;
      case "VariableName":
        info.refs.push({ name: text(node), line: line(node) });
        return;
      case "Property": {
        // Shorthand {name} reads name.
        const cs = children(node);
        if (cs.length === 1 && cs[0].name === "PropertyDefinition") info.refs.push({ name: text(cs[0]), line: line(cs[0]) });
        break;
      }
      case "MemberExpression": {
        const [obj, dot, prop] = children(node);
        if (obj?.name === "VariableName" && dot?.name === "." && prop?.name === "PropertyName") {
          info.members.push({ ns: text(obj), name: text(prop), line: line(prop) });
        }
        break;
      }
    }
    for (const c of children(node)) walk(c);
  };

  // The names a declaration binds, leaving out its initializers (whose
  // arrow functions' parameters aren't exports).
  const bound = (decl) => {
    const names = [];
    let init = false;
    const visit = (n) => {
      if (n.name === "VariableDefinition") names.push(text(n));
      else for (const c of children(n)) visit(c);
    };
    for (const c of children(decl)) {
      if (c.name === "Equals") init = true;
      else if (c.name === ",") init = false;
      else if (!init) visit(c);
      if (decl.name !== "VariableDeclaration" && c.name === "VariableDefinition") break;
    }
    return names;
  };

  const tree = parser.parse(src);
  for (const stmt of children(tree.topNode)) {
    const cs = children(stmt);
    const from = cs.find((c) => c.name === "String");
    const spec = from && cs.some((c) => c.name === "from") ? stripQuotes(text(from)) : null;
    if (stmt.name === "ImportDeclaration") {
      const imp = (imported, local) => {
        info.defs.add(local);
        info.imports.push({ spec: stripQuotes(text(from)), imported, local, line: line(stmt) });
      };
      for (let i = 0; i < cs.length; i++) {
        const c = cs[i];
        if (c.name === "Star") imp("*", text(cs[i + 2]));
        else if (c.name === "VariableDefinition" && cs[i - 1]?.name !== "as") imp("default", text(c));
        else if (c.name === "ImportGroup") for (const [a, b] of groupPairs(c, text)) imp(a, b);
      }
      continue;
    }
    if (stmt.name === "ExportDeclaration") {
      const group = cs.find((c) => c.name === "ExportGroup");
      if (cs.some((c) => c.name === "default")) info.exports.add("default");
      if (cs[1]?.name === "Star") {
        if (cs[2]?.name === "as") info.exports.add(text(cs[3]));
        else info.reexports.push({ spec, imported: "*", exported: "*" });
      } else if (group) {
        for (const [a, b] of groupPairs(group, text)) {
          if (spec) info.reexports.push({ spec, imported: a, exported: b });
          else {
            info.exports.add(b);
            info.refs.push({ name: a, line: line(group) });
          }
        }
      } else {
        for (const c of cs) {
          if (/Declaration$/.test(c.name)) for (const n of bound(c)) info.exports.add(n);
          walk(c);
        }
      }
      continue;
    }
    walk(stmt);
  }
  return info;
}

const cache = new Map();
const parsed = (file) => {
  if (!cache.has(file)) cache.set(file, analyze(file));
  return cache.get(file);
};

// Every name a module exports, following `export … from`. Vendored bundles
// are imported rather than parsed: they load in Node, and parsing
// codemirror.js's 1.7 MB takes half a minute.
async function exportsOf(file, seen = new Set()) {
  if (seen.has(file)) return new Set();
  seen.add(file);
  if (file.startsWith(join(staticDir, "vendor") + sep)) {
    return new Set(Object.keys(await import(pathToFileURL(file).href)));
  }
  const info = parsed(file);
  const names = new Set(info.exports);
  for (const r of info.reexports) {
    const target = join(dirname(file), r.spec);
    if (r.imported === "*") {
      for (const n of await exportsOf(target, seen)) if (n !== "default") names.add(n);
    } else names.add(r.exported);
  }
  return names;
}

const scripts = [staticDir, join(staticDir, "app")].flatMap((dir) =>
  readdirSync(dir)
    .filter((f) => f.endsWith(".js"))
    .map((f) => join(dir, f)),
);

const where = (file, line) => `${relative(here, file).replaceAll("\\", "/")}:${line}`;

test("every name a script reads is declared, imported or a global", () => {
  const globals = new Set([...GLOBALS, ...scripts.flatMap((f) => parsed(f).windowDefs)]);
  const missing = [];
  for (const f of scripts) {
    const info = parsed(f);
    for (const r of info.refs) {
      if (!info.defs.has(r.name) && !globals.has(r.name)) missing.push(`${where(f, r.line)}: ${r.name}`);
    }
  }
  assert.deepEqual([...new Set(missing)], [], "not declared, imported or a known global (a missing import?)");
});

test("every name a script imports is exported by its module", async () => {
  const missing = [];
  for (const f of scripts) {
    const info = parsed(f);
    const namespaces = new Map();
    for (const imp of info.imports) {
      if (!imp.spec.startsWith(".")) continue;
      const target = join(dirname(f), imp.spec);
      const names = await exportsOf(target);
      if (imp.imported === "*") namespaces.set(imp.local, { spec: imp.spec, names });
      else if (!names.has(imp.imported)) missing.push(`${where(f, imp.line)}: ${imp.imported} from ${imp.spec}`);
    }
    for (const m of info.members) {
      const ns = namespaces.get(m.ns);
      if (ns && !ns.names.has(m.name)) missing.push(`${where(f, m.line)}: ${m.ns}.${m.name} from ${ns.spec}`);
    }
  }
  assert.deepEqual(missing, [], "imported but not exported by the module named");
});
