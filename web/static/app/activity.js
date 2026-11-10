import { h } from "./dom.js";
import { state } from "./state.js";
import { renderBoard, updateLiveLine } from "./branches.js";
import { renderChecks } from "./review.js";

// ------------------------------------------------------------------ live activity
// What a task is busy with right now, from its worker's events: a phase Dash
// runs itself (setup, tests) or a shell command of Claude's. Each has a start,
// so cards and the timeline can show how long it has been going, and its
// latest output line. Other tools are quick and the progress line covers them.
const TEST_CMD = /(^|[\s;&|/(])(go test|cargo test|pytest|jest|vitest|mocha|phpunit|rspec|ctest|dotnet test|mvn (-\S+ )*(test|verify)|gradlew? (\S+ )*test|(npm|pnpm|yarn|bun)( run)? test|make (\S+ )*(test|check)|tox|unittest|node --test|deno test|swift test|flutter test|mix test)\b/;
export function trackActivity(id, e) {
  const cur = state.activity.get(id);
  const at = Date.now(); // not e.at: the runner's clock may differ from this one
  let m;
  if (e.kind === "status" && (m = /^▶ (setup|tests): ([^]*)/.exec(e.text || ""))) {
    state.activity.set(id, { key: `${m[1]}${at}`, label: m[1] === "tests" ? "Running tests" : "Setting up the worktree", cmd: m[2], line: "", since: at });
  } else if (e.kind === "status" && (e.text || "").startsWith("│ ")) {
    if (cur && !cur.tool) cur.line = e.text.slice(2);
    return; // the card's line updates with todo.output
  } else if (e.kind === "tool_use" && e.tool === "Bash") {
    const cmd = String(e.input?.command || "").trim().split("\n")[0];
    state.activity.set(id, { key: e.id, tool: e.id, label: TEST_CMD.test(cmd) ? "Claude is running tests" : "Claude is running", cmd, line: "", since: at });
  } else if (e.kind === "tool_result") {
    if (!cur?.tool || cur.tool !== e.id) return;
    state.activity.delete(id);
    state.lastLine.set(id, `${e.is_error ? "✗" : "✓"} ${cur.cmd} (${duration(at - cur.since)})`);
  } else {
    if (!cur) return;
    state.activity.delete(id); // something else is happening now
  }
  // The activity changed: cards that show it in a state the live line doesn't cover re-render.
  const t = state.todos.get(id);
  if (t?.status === "in_progress") updateLiveLine(id);
  else renderBoard();
  if (t && state.detail === id) renderChecks(t);
}

export function activityNodes(a) {
  return [
    h("span", { class: "act-spin", "aria-hidden": "true" }),
    h("span", { class: "act-label" }, a.label),
    h("span", { class: "act-time", "data-since": String(a.since) }, duration(Date.now() - a.since)),
    h("span", { class: "act-line" }, a.line || a.cmd),
  ];
}

// "8s", "1m 05s", "1h 02m"
export function duration(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(s / 3600)}h ${String(Math.floor(s / 60) % 60).padStart(2, "0")}m`;
}
// Elapsed-time labels on whatever is still running tick once a second.
setInterval(() => {
  if (document.hidden) return;
  const now = Date.now();
  for (const el of document.querySelectorAll("[data-since]")) {
    const ms = now - Number(el.dataset.since);
    el.textContent = el.dataset.quiet != null && ms < 3000 ? "" : duration(ms);
  }
}, 1000);
