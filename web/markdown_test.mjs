// Tests for the chat's Markdown rendering (web/static/markdown.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { plainText, previewText, renderMarkdown } from "./static/markdown.js";

const fence = (lang, body) => "```" + lang + "\n" + body + "\n```";
const srcOf = (html) => decodeURIComponent(html.match(/<img src="data:image\/svg\+xml;charset=utf-8,([^"]*)"/)[1].replace(/&#39;/g, "'").replace(/&amp;/g, "&"));

test("an svg block is a picture of a data: URL, with its code hidden behind Code", () => {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4" fill="red"/></svg>`;
  const html = renderMarkdown("Here:\n\n" + fence("svg", svg) + "\n\nDone.");
  assert.match(html, /^<p>Here:<\/p><figure class="md-svg" data-preview-group>/);
  assert.match(html, /<a class="md-svg-pic" href="data:image\/svg\+xml;charset=utf-8,[^"]*" data-preview data-name="drawing.svg" data-type="image\/svg\+xml" data-size="\d+"/);
  assert.equal(srcOf(html), svg, "the data: URL is the SVG itself");
  assert.match(html, /<button type="button" class="md-svg-code" aria-expanded="false">Code<\/button>/);
  assert.match(html, /<div class="code-block" hidden><button type="button" class="code-copy"[^]*<code class="lang-svg">&lt;svg xmlns=/, "the source is there to see and copy");
  assert.match(html, /<\/figure><p>Done.<\/p>$/);
  assert.match(renderMarkdown(fence("SVG", svg)), /^<figure class="md-svg"/, "the language is matched in any case");
});

test("an svg block with a script or event handlers stays inert", () => {
  const evil = `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><script>alert(document.cookie)</script>` +
    `<image href="https://example.com/x.png" onerror="alert(2)"/><foreignObject><img src=x onerror="alert(3)"></foreignObject>` +
    `<a href="javascript:alert(4)"><text>"quoted" &amp; 'single'</text></a></svg>`;
  const html = renderMarkdown(fence("svg", evil));
  // Outside the data: URL and the escaped code, there is no markup from the SVG.
  const page = html.replace(/"data:image\/svg\+xml;charset=utf-8,[^"]*"/g, '""').replace(/<code class="lang-svg">[^<]*<\/code>/, "");
  assert.doesNotMatch(page, /<svg xmlns|<script|onload|onerror|<image|<foreignObject|javascript:/i);
  assert.equal(page.match(/<svg/g).length, 1, "only the copy button's own icon is an <svg> in the page");
  // The data: URL breaks out of neither its attribute nor its tag.
  for (const [, url] of html.matchAll(/(?:src|href)="(data:[^"]*)"/g)) assert.doesNotMatch(url, /[<>"\s]/);
  assert.equal(srcOf(html), evil, "the picture is the SVG as written, which an <img> never runs");
  assert.match(html, /<code class="lang-svg">&lt;svg xmlns=&quot;http:\/\/www.w3.org\/2000\/svg&quot; onload=&quot;alert\(1\)&quot;&gt;&lt;script&gt;/);
});

test("an svg block still streaming in shows as code", () => {
  const html = renderMarkdown("Drawing:\n\n```svg\n<svg viewBox=\"0 0 4 4\">\n<rect width=\"4\"");
  assert.doesNotMatch(html, /md-svg|<img/);
  assert.match(html, /<div class="code-block"><button[^]*<code class="lang-svg" data-open>&lt;svg viewBox=&quot;0 0 4 4&quot;&gt;\n&lt;rect width=&quot;4&quot;<\/code>/);
  assert.match(renderMarkdown("```svg\n<svg/>\n``"), /<div class="code-block">/, "a closing fence half there is not closed");
  assert.match(renderMarkdown("```svg\n<svg/>\n```"), /md-svg/);
});

test("an svg block without an <svg> element, and other languages, stay code", () => {
  assert.doesNotMatch(renderMarkdown(fence("svg", "just some text")), /md-svg/);
  assert.doesNotMatch(renderMarkdown(fence("svgx", "<svg/>")), /md-svg/);
  assert.doesNotMatch(renderMarkdown(fence("xml", "<svg/>")), /md-svg/);
  assert.doesNotMatch(renderMarkdown(fence("", "<svg/>")), /md-svg/);
  assert.match(renderMarkdown(fence("html", "<b>x</b>")), /<code class="lang-html">&lt;b&gt;x&lt;\/b&gt;<\/code>/);
});

test("an svg block in a list item is a picture too", () => {
  const html = renderMarkdown("- one:\n\n  ```svg\n  <svg viewBox=\"0 0 1 1\"/>\n  ```\n");
  assert.match(html, /<li>one:<figure class="md-svg"/);
});

test("emphasis can hold a single character", () => {
  assert.equal(renderMarkdown("**a** __b__ *c* _d_ ~~e~~"),
    "<p><strong>a</strong> <strong>b</strong> <em>c</em> <em>d</em> <del>e</del></p>");
  assert.equal(renderMarkdown("**a** and **b**"), "<p><strong>a</strong> and <strong>b</strong></p>");
  assert.equal(renderMarkdown("x **** y"), "<p>x **** y</p>");
  assert.equal(renderMarkdown("** a**"), "<p>** a**</p>");
});

test("a run of asterisks or underscores stays as it is", () => {
  assert.equal(renderMarkdown("x ****** y"), "<p>x ****** y</p>");
  assert.equal(renderMarkdown("x ______ y"), "<p>x ______ y</p>");
  assert.equal(renderMarkdown("**_a_** __*b*__"), "<p><strong><em>a</em></strong> <strong><em>b</em></strong></p>");
});

test("***bold italic*** nests properly", () => {
  assert.equal(renderMarkdown("***a***"), "<p><strong><em>a</em></strong></p>");
  assert.equal(renderMarkdown("___a b___ and ***c***"),
    "<p><strong><em>a b</em></strong> and <strong><em>c</em></strong></p>");
});

test("the rest renders as before", () => {
  assert.equal(renderMarkdown("**bold** `b` [c](https://example.com)"),
    '<p><strong>bold</strong> <code>b</code> <a href="https://example.com" target="_blank" rel="noopener noreferrer">c</a></p>');
  assert.equal(renderMarkdown("<img src=x onerror=alert(1)>"), "<p>&lt;img src=x onerror=alert(1)&gt;</p>");
  assert.equal(renderMarkdown("![x](javascript:alert(1))"), "<p>![x](javascript:alert(1))</p>");
});

test("plainText strips Markdown for a card's preview", () => {
  assert.equal(plainText("Fix the following issue.\n\n## Issue reported by Claude\n\n**Severity:** low"),
    "Fix the following issue. Issue reported by Claude Severity: low");
  assert.equal(plainText("- one\n- [x] two\n1. three\n> quoted *em* and _em_"), "one two three quoted em and em");
  assert.equal(plainText("See [the docs](https://example.com) and ![shot](a.png), <https://example.dev>"),
    "See the docs and shot, https://example.dev");
  assert.equal(plainText("Run:\n```sh\ngo test ./...\n```\nthen `make`"), "Run: go test ./... then make");
  assert.equal(plainText("keep snake_case_names and 2 * 3 * 4"), "keep snake_case_names and 2 * 3 * 4");
  assert.equal(plainText("| a | b |\n|---|---|\n| 1 | 2 |"), "a b 1 2");
  assert.equal(plainText("~~old~~ ***both*** __strong__\n\n---\n\nend"), "old both strong end");
  assert.equal(plainText(""), "");
});

test("previewText reads a planning card's prose, without its headings", () => {
  assert.equal(previewText("## Why\nSo **finance** can close the month.\n\n## What\n- PDF export\n- *one* click"),
    "So finance can close the month.");
  assert.equal(previewText("- PDF export\n- *one* click\n\nMore later."), "PDF export one click");
  assert.equal(previewText("First line\nsecond line\n\nNext paragraph"), "First line second line");
  assert.equal(previewText("# Only a heading"), "Only a heading");
  assert.equal(previewText("#hashtag is not a heading"), "#hashtag is not a heading");
  assert.equal(previewText(""), "");
});
