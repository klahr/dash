// Tests for the Claude pill's words (web/static/claudepill.js).
import test from "node:test";
import assert from "node:assert/strict";
import { affectedServices, claudePillText } from "./static/claudepill.js";

const code = { name: "Claude Code", status: "degraded_performance" };
const web = { name: "claude.ai", status: "partial_outage" };

test("one incident on several services counts the services", () => {
  const api = { name: "API errors", impact: "major", components: ["Claude API", "Claude Code", "Cowork"], counted: ["Claude API", "Claude Code"] };
  const other = { name: "Logins fail", impact: "minor", counted: ["claude.ai"] };
  // Claude Code is both a component and named by the incident: once.
  assert.equal(claudePillText([api, other], [code, web]), "Claude: 3 services affected");
  // The sheet lists the same services: a component's status, else the
  // naming incident's impact.
  assert.deepEqual(affectedServices([api, other], [code, web]), [
    { name: "Claude Code", status: "degraded_performance" },
    { name: "claude.ai", status: "partial_outage" },
    { name: "Claude API", impact: "major" },
  ]);
});

test("names match as the hub matches them", () => {
  const i = { impact: "major", counted: ["claude  CODE"] };
  assert.equal(claudePillText([i], [code]), "Claude: Claude Code degraded performance");
});

test("one service says which and how", () => {
  assert.equal(claudePillText([], [code]), "Claude: Claude Code degraded performance");
});

test("one service named only by an incident says the incident", () => {
  assert.equal(claudePillText([{ impact: "major", counted: ["Claude API"] }], []), "Claude: major incident");
});

test("incidents that name no service fall back to the incidents", () => {
  assert.equal(claudePillText([{ impact: "critical" }], []), "Claude: critical incident");
  assert.equal(claudePillText([{ impact: "minor" }, { impact: "major" }], []), "Claude: 2 incidents");
});

test("several components with no incidents", () => {
  assert.equal(claudePillText([], [code, web]), "Claude: 2 services affected");
});
