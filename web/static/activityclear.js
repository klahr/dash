// Which live activity (see app/activity.js) survives a task's status change.
// Pure, so web/activityclear_test.mjs can run it under Node.
//
// An activity with a `tool` is Claude's shell command; it ends when its result
// event arrives, which a run that ends mid-command (or a page that missed the
// event) never delivers. Others are Dash's own phases (setup, tests) and can
// really be running while the task is in Review or merging.
const NOTHING_RUNS = ["draft", "queued", "merged"];
const NO_CLAUDE_RUN = ["review", "failed", "merging"];

export function activityAfterStatus(cur, status, prevStatus) {
  if (!cur) return cur;
  if (NOTHING_RUNS.includes(status)) return undefined;
  if (cur.tool && (NO_CLAUDE_RUN.includes(status) || (status === "in_progress" && prevStatus !== "in_progress"))) return undefined;
  return cur;
}
