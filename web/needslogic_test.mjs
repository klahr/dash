// Tests for Needs you's list (web/static/needslogic.js).
// Run with `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { applyEvent, needKey, nextKey, permNeed, reviewSince, sortNeeds, taskNeed, waitedText } from "./static/needslogic.js";

const task = (id, status, finished, more = {}) => ({ id, project_id: "p1", status, title: id, finished_at: finished, updated_at: finished, ...more });
const perm = (id, todo, created, more = {}) => ({ id, todo_id: todo, project_id: "p1", tool: "Bash", created, ...more });
const keys = (needs) => needs.map(needKey);

test("reviews and failed tasks need you; other statuses don't", () => {
  assert.equal(taskNeed(task("a", "review", "2026-01-01T10:00:00Z")).kind, "review");
  assert.equal(taskNeed(task("b", "failed", "2026-01-01T10:00:00Z")).kind, "failed");
  for (const s of ["draft", "queued", "in_progress", "merging", "merged"]) assert.equal(taskNeed(task("c", s, "2026-01-01T10:00:00Z")), null, s);
});

test("a pull request waits on you only while a person has to act on GitHub, as on the Wall", () => {
  const t = task("a", "review", "2026-01-01T10:00:00Z", { pr: { state: "OPEN", number: 3 } });
  assert.equal(reviewSince(t), null, "waits on its checks");
  assert.equal(taskNeed(t), null);
  t.pr.waits = "review";
  t.pr.waits_since = "2026-01-01T12:00:00Z";
  assert.equal(reviewSince(t), "2026-01-01T12:00:00Z", "from when it started waiting, if later");
  t.pr.waits_since = "2026-01-01T09:00:00Z";
  assert.equal(reviewSince(t), "2026-01-01T10:00:00Z", "else from when Claude finished");
  assert.equal(reviewSince(task("b", "review", "2026-01-01T10:00:00Z", { pr: { state: "MERGED" } })), "2026-01-01T10:00:00Z", "a closed pull request is reviewed in Dash");
});

test("questions and permission requests are told apart", () => {
  assert.equal(permNeed(perm("q", "a", "2026-01-01T10:00:00Z", { tool: "AskUserQuestion" })).kind, "question");
  assert.equal(permNeed(perm("r", "a", "2026-01-01T10:00:00Z")).kind, "permission");
});

test("oldest first", () => {
  const needs = sortNeeds([
    taskNeed(task("new", "review", "2026-01-01T12:00:00Z")),
    taskNeed(task("old", "failed", "2026-01-01T08:00:00Z")),
    permNeed(perm("ask", "x", "2026-01-01T10:00:00Z")),
  ]);
  assert.deepEqual(keys(needs), ["old", "ask", "new"]);
});

test("live events add, move and drop needs", () => {
  let needs = sortNeeds([taskNeed(task("a", "review", "2026-01-01T08:00:00Z"))]);
  needs = applyEvent(needs, { type: "todo.updated", todo_id: "b", data: task("b", "failed", "2026-01-01T07:00:00Z") });
  assert.deepEqual(keys(needs), ["b", "a"], "a failure comes in, in its place");
  needs = applyEvent(needs, { type: "todo.updated", todo_id: "a", data: task("a", "merging", "2026-01-01T08:00:00Z") });
  assert.deepEqual(keys(needs), ["b"], "approved: it no longer waits");
  needs = applyEvent(needs, { type: "todo.updated", todo_id: "c", data: task("c", "in_progress", "2026-01-01T09:00:00Z") });
  needs = applyEvent(needs, { type: "permission.updated", data: perm("r1", "c", "2026-01-01T09:30:00Z") });
  assert.deepEqual(keys(needs), ["b", "r1"], "a request comes in");
  const title = { ...task("c", "in_progress", "2026-01-01T09:00:00Z"), title: "renamed" };
  needs = applyEvent(needs, { type: "todo.updated", todo_id: "c", data: title });
  assert.equal(needs[1].todo.title, "renamed", "a request shows its task as it is now");
  needs = applyEvent(needs, { type: "permission.updated", data: perm("r1", "c", "2026-01-01T09:30:00Z", { decision: "allow" }) });
  assert.deepEqual(keys(needs), ["b"], "answered");
  needs = applyEvent(needs, { type: "todo.deleted", todo_id: "b" });
  assert.deepEqual(keys(needs), []);
  const same = [taskNeed(task("a", "review", "2026-01-01T08:00:00Z"))];
  assert.equal(applyEvent(same, { type: "goal.updated", data: {} }), same, "other events change nothing");
  assert.equal(applyEvent(same, { type: "todo.updated", todo_id: "z", data: task("z", "in_progress", "2026-01-01T09:00:00Z") }), same, "nor do other tasks'");
  assert.equal(applyEvent(same, { type: "todo.deleted", todo_id: "z" }), same);
});

test("a request of a task the list knows shows that task", () => {
  const needs = applyEvent([taskNeed(task("a", "review", "2026-01-01T08:00:00Z"))], { type: "permission.updated", data: perm("r", "a", "2026-01-01T09:00:00Z") });
  assert.equal(needs[1].todo.id, "a");
});

test("after acting, the next row opens", () => {
  const before = ["a", "b", "c"].map((id, i) => taskNeed(task(id, "review", `2026-01-01T0${i}:00:00Z`)));
  const without = (...ids) => before.filter((n) => !ids.includes(needKey(n)));
  assert.equal(nextKey(before, before, "b"), "b", "still there: stays");
  assert.equal(nextKey(before, without("b"), "b"), "c", "the next row");
  assert.equal(nextKey(before, without("c"), "c"), "b", "the last row: the one before");
  assert.equal(nextKey(before, without("b", "c"), "b"), "a");
  assert.equal(nextKey(before, [], "b"), null, "nothing left");
  const fresh = [taskNeed(task("z", "review", "2026-01-01T05:00:00Z"))];
  assert.equal(nextKey(before, fresh, "b"), "z", "only new rows left: the first");
});

test("how long it waited", () => {
  const now = Date.parse("2026-01-01T12:00:00Z");
  assert.equal(waitedText("2026-01-01T11:59:30Z", now), "<1m");
  assert.equal(waitedText("2026-01-01T09:45:00Z", now), "2h 15m");
});
