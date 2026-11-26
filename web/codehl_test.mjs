// Tests for the syntax colours of code blocks (web/static/codehl.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { MAX_CODE, fenceLanguage, tokens } from "./static/codehl.js";
import { renderMarkdown } from "./static/markdown.js";

// spans shows pieces as "[class:text]" and plain text as itself.
const spans = (pieces) => pieces.map(([text, cls]) => (cls ? `[${cls}:${text}]` : text)).join("");

test("languages resolve by name, alias or file extension", () => {
  assert.equal(fenceLanguage("go")?.name, "Go");
  assert.equal(fenceLanguage("JavaScript")?.name, "JavaScript");
  assert.equal(fenceLanguage("js")?.name, "JavaScript");
  assert.equal(fenceLanguage("sh")?.name, "Shell");
  assert.equal(fenceLanguage("py")?.name, "Python");
  assert.equal(fenceLanguage(""), null);
  assert.equal(fenceLanguage("nosuchlanguage"), null);
});

test("code is split into coloured pieces, its text unchanged", async () => {
  const cases = {
    go: ['package main\n\n// Main runs.\nfunc main() { fmt.Println("hi", 42) }', ["[tok-keyword:package]", "[tok-comment:// Main runs.]", '[tok-string:"hi"]', "[tok-number:42]", "[tok-function:main]"]],
    js: ['const a = "x"; // note\nlet f = () => a', ["[tok-keyword:const]", '[tok-string:"x"]', "[tok-comment:// note]"]],
    sh: ['echo "hi" # say it\nif true; then ls; fi', ['[tok-string:"hi"]', "[tok-comment:# say it]", "[tok-keyword:if]"]],
    sql: ["SELECT name FROM users WHERE id = 1 -- one", ["[tok-keyword:SELECT]", "[tok-number:1]", "[tok-comment:-- one]"]],
    diff: ["--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new", ["[tok-deleted:-old]", "[tok-inserted:+new]", "[tok-muted:@@ -1 +1 @@]"]],
  };
  for (const [lang, [code, want]] of Object.entries(cases)) {
    const got = await tokens(code, lang);
    assert.equal(got.map(([text]) => text).join(""), code, `${lang}: the text is all there, in order`);
    const s = spans(got);
    for (const w of want) assert.ok(s.includes(w), `${lang}: ${w} in ${s}`);
  }
});

test("markup in code stays text, and embedded languages are coloured too", async () => {
  const code = "<b onclick='x()'>hi</b><script>let a = 1</script>";
  const got = await tokens(code, "html");
  assert.equal(got.map(([text]) => text).join(""), code);
  assert.ok(spans(got).includes("[tok-keyword:let]"));
});

test("unknown languages and big blocks stay plain", async () => {
  assert.equal(await tokens("x", "nosuchlanguage"), null);
  assert.equal(await tokens("x", ""), null);
  assert.equal(await tokens("x".repeat(MAX_CODE + 1), "go"), null);
});

test("a fence not closed yet is marked open, a closed one isn't", () => {
  assert.match(renderMarkdown("```go\nfunc main() {"), /<code class="lang-go" data-open>/);
  assert.match(renderMarkdown("```go\nfunc main() {}\n```"), /<code class="lang-go">/);
  assert.match(renderMarkdown("```\nplain"), /<pre><code>plain/);
});
