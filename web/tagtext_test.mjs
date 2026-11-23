// Tests for task tags as text (web/static/tagtext.js): quick add's #tags and
// the tag field's limits. Run with `node --test web/`; groups_test.go runs
// them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { MAX_TAGS, addTag, cleanTag, quickTags } from "./static/tagtext.js";

const q = (text) => { const r = quickTags(text); return [r.text, r.tags]; };

test("#tags are taken out of the text", () => {
  assert.deepEqual(q("Fix login redirect #auth #bug"), ["Fix login redirect", ["auth", "bug"]]);
  assert.deepEqual(q("#auth Fix login redirect"), ["Fix login redirect", ["auth"]]);
  assert.deepEqual(q("Fix #auth login redirect"), ["Fix login redirect", ["auth"]]);
  assert.deepEqual(q("Fix login redirect #auth."), ["Fix login redirect.", ["auth"]]);
  assert.deepEqual(q("Fix it (#ui)"), ["Fix it (#ui)", []]);
  assert.deepEqual(q("Fix it #ui, then ship"), ["Fix it, then ship", ["ui"]]);
});

test("a # that isn't a whole word stays", () => {
  assert.deepEqual(q("Close issue #123"), ["Close issue #123", []]);
  assert.deepEqual(q("Port it to C#"), ["Port it to C#", []]);
  assert.deepEqual(q("Port it to C# #dotnet"), ["Port it to C#", ["dotnet"]]);
  assert.deepEqual(q("a#b and # heading and ##x"), ["a#b and # heading and ##x", []]);
  assert.deepEqual(q("See https://example.com/page#top"), ["See https://example.com/page#top", []]);
  assert.deepEqual(q("Odd #tag! here"), ["Odd! here", ["tag"]]);
  assert.deepEqual(q("Odd #tag$ here"), ["Odd #tag$ here", []]);
});

test("tags keep their case, dots, colons and dashes inside, without repeats", () => {
  assert.deepEqual(q("Bump #deps #v1.2 #scan:deps #front-end #Deps"), ["Bump", ["deps", "v1.2", "scan:deps", "front-end"]]);
  assert.deepEqual(q("Bump #v2."), ["Bump.", ["v2"]]);
  assert.deepEqual(q("Fix #ÅÄÖ"), ["Fix", ["ÅÄÖ"]]);
});

test("code is left alone", () => {
  assert.deepEqual(q("Use `#define X` #c"), ["Use `#define X`", ["c"]]);
  assert.deepEqual(q("Add it #build\n```\n#include <stdio.h>\n```"), ["Add it\n```\n#include <stdio.h>\n```", ["build"]]);
});

test("tags on lines of their own", () => {
  assert.deepEqual(q("Fix login\n\n#auth #bug"), ["Fix login", ["auth", "bug"]]);
  assert.deepEqual(q("#only #tags"), ["", ["only", "tags"]]);
  assert.deepEqual(q(""), ["", []]);
});

test("limits: long tags stay text, past the 20th tags stay text", () => {
  const long = "x".repeat(41);
  assert.deepEqual(q(`Fix #${long}`), [`Fix #${long}`, []]);
  const many = Array.from({ length: MAX_TAGS + 1 }, (_, i) => `#t${i}a`).join(" ");
  const r = quickTags(`Go ${many}`);
  assert.equal(r.tags.length, MAX_TAGS);
  assert.equal(r.text, `Go #t${MAX_TAGS}a`);
});

test("addTag cleans, skips repeats and keeps the server's limits", () => {
  assert.equal(cleanTag("  front   end, "), "front end");
  assert.deepEqual(addTag(["UI"], " ui "), { tags: ["UI"] });
  assert.deepEqual(addTag(["UI"], "  "), { tags: ["UI"] });
  assert.deepEqual(addTag(["UI"], " back   end "), { tags: ["UI", "back end"] });
  assert.match(addTag([], "x".repeat(41)).error, /at most 40/);
  assert.deepEqual(addTag([], "x".repeat(40)), { tags: ["x".repeat(40)] });
  assert.match(addTag([], "å".repeat(21)).error, /at most 40/); // bytes, as the server counts
  const full = Array.from({ length: MAX_TAGS }, (_, i) => `t${i}`);
  assert.match(addTag(full, "one more").error, /at most 20 tags/);
});
