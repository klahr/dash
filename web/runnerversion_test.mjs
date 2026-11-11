// Tests for the runner "out of date" check (web/static/runnerversion.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { isRelease, runnerOutdated, sameVersion } from "./static/runnerversion.js";

test("release versions with or without a leading v", () => {
  assert.equal(isRelease("0.7.0"), true);
  assert.equal(isRelease("v0.7.0"), true);
  assert.equal(isRelease("dev"), false);
  assert.equal(isRelease(""), false);
  assert.equal(isRelease(undefined), false);
});

test("versions compare equal whichever side has a v", () => {
  assert.ok(sameVersion("0.7.0", "v0.7.0"));
  assert.ok(sameVersion("v0.7.0", "0.7.0"));
  assert.ok(!sameVersion("0.6.2", "0.7.0"));
});

test("hub 0.7.0 (no v): an older runner is out of date, a matching one isn't", () => {
  assert.equal(runnerOutdated("0.7.0", "0.6.2"), true);
  assert.equal(runnerOutdated("0.7.0", "v0.6.2"), true);
  assert.equal(runnerOutdated("0.7.0", "0.7.0"), false);
  assert.equal(runnerOutdated("0.7.0", "v0.7.0"), false);
  assert.equal(runnerOutdated("v0.7.0", "0.7.0"), false);
  assert.equal(runnerOutdated("0.7.0", ""), false); // no version reported
});

test("a dev hub flags no runner", () => {
  assert.equal(runnerOutdated("dev", "0.6.2"), false);
  assert.equal(runnerOutdated("", "0.6.2"), false);
});
