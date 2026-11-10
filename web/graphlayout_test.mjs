// Tests for the History graph's lanes (web/static/graphlayout.js). Run with
// `node --test web/`; groups_test.go runs them as part of `go test ./...`.
import test from "node:test";
import assert from "node:assert/strict";
import { graphLayout } from "./static/graphlayout.js";

const c = (hash, ...parents) => ({ hash, parents });

test("a straight history is one lane", () => {
  const { rows, width } = graphLayout([c("c", "b"), c("b", "a"), c("a")]);
  assert.equal(width, 1);
  assert.deepEqual(rows.map((r) => r.col), [0, 0, 0]);
  assert.deepEqual(rows[0].up, []); // nothing above the newest commit
  assert.deepEqual(rows[1].up, [[0, 0, 0]]);
  assert.deepEqual(rows[2].down, []); // nothing below the root
});

test("a merge forks a lane that closes into its base", () => {
  // m merges s into x; s and x both come from r.
  const { rows, width } = graphLayout([c("m", "x", "s"), c("s", "r"), c("x", "r"), c("r")]);
  assert.equal(width, 2);
  const [m, s, x, r] = rows;
  assert.equal(m.col, 0);
  assert.deepEqual(m.down, [[0, 0, 0], [0, 1, 1]]); // first parent straight down, the merged branch curves out
  assert.equal(s.col, 1);
  assert.deepEqual(s.up, [[0, 0, 0], [1, 1, 1]]);
  assert.equal(x.col, 0);
  assert.deepEqual(x.down, [[0, 0, 0], [1, 1, 1]]); // both wait for r
  assert.equal(r.col, 0);
  assert.deepEqual(r.up, [[0, 0, 0], [1, 0, 1]]); // s's lane merges into r's
  assert.deepEqual(r.down, []);
});

test("a merge joins a lane already waiting for its parent", () => {
  // t and m are both tips; m merges b, which t's lane is already waiting for.
  const { rows } = graphLayout([c("t", "b"), c("m", "a", "b"), c("b", "a"), c("a")]);
  assert.equal(rows[0].col, 0);
  assert.equal(rows[1].col, 1);
  assert.deepEqual(rows[1].down, [[0, 0, 0], [1, 1, 1], [1, 0, 0]]);
  assert.equal(rows[2].col, 0);
});

test("freed lanes are reused, so the graph stays narrow", () => {
  // Two side branches merged one after the other.
  const { rows, width } = graphLayout([
    c("m2", "m1", "s2"), c("s2", "m1"),
    c("m1", "a", "s1"), c("s1", "a"),
    c("a"),
  ]);
  assert.equal(width, 2);
  assert.equal(rows[1].col, 1);
  assert.equal(rows[3].col, 1);
  assert.notEqual(rows[1].color, rows[3].color); // a new branch, a new colour
});

test("lanes waiting for commits past the list run to the bottom", () => {
  const { rows } = graphLayout([c("b", "a")]);
  assert.deepEqual(rows[0].down, [[0, 0, 0]]);
});
