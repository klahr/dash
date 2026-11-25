// Tests for which activity labels survive a status change (web/static/activityclear.js).
import test from "node:test";
import assert from "node:assert/strict";
import { activityAfterStatus, activityFits } from "./static/activityclear.js";

const claude = { key: "t1", tool: "t1", label: "Claude is running", cmd: "sleep 100", line: "", since: 1 };
const tests = { key: "tests1", label: "Running tests", cmd: "go test ./...", line: "", since: 1 };

test("Claude's label is cleared when a task moves to review, failed or merging", () => {
  for (const s of ["review", "failed", "merging", "draft", "queued", "merged"]) {
    assert.equal(activityAfterStatus(claude, s, "in_progress"), undefined, s);
  }
});

test("a new run clears Claude's label left from the last one", () => {
  assert.equal(activityAfterStatus(claude, "in_progress", "review"), undefined);
  assert.equal(activityAfterStatus(claude, "in_progress", "in_progress"), claude);
});

test("Dash's own running tests stay in review, failed and merging", () => {
  for (const s of ["review", "failed", "merging", "in_progress"]) {
    assert.equal(activityAfterStatus(tests, s, "review"), tests, s);
  }
  for (const s of ["draft", "queued", "merged"]) assert.equal(activityAfterStatus(tests, s, "review"), undefined, s);
});

test("no activity stays none", () => {
  assert.equal(activityAfterStatus(undefined, "review", "in_progress"), undefined);
});

test("a late event doesn't start an activity on a task whose run is over", () => {
  for (const s of ["merged", "queued", "draft", "review", "failed", "merging"]) assert.equal(activityFits(claude, s), false, s);
  for (const s of ["merged", "queued", "draft"]) assert.equal(activityFits(tests, s), false, s);
  assert.equal(activityFits(claude, "in_progress"), true);
  for (const s of ["in_progress", "review", "failed", "merging"]) assert.equal(activityFits(tests, s), true, s);
  assert.equal(activityFits(claude, undefined), true); // a task not loaded yet
});
