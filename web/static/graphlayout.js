// The History view's commit graph lanes, kept free of the DOM so they can be
// tested with Node (web/graphlayout_test.mjs).

// LANE_COLORS is how many lane colours style.css defines (--lane-0…).
export const LANE_COLORS = 6;

// graphLayout gives each commit (newest first, children before parents, as
// `git log --topo-order` lists them) a lane and the lines to draw above and
// below its dot.
//
// Lanes are slots waiting for a commit hash. A commit takes the first slot
// waiting for it (the others waiting for it merge into it and close) or a
// free one if none is (a branch tip). Its first parent carries on in that
// slot; further parents (a merge) join a slot already waiting for them or
// take a free one. Freed slots are reused, so the graph stays narrow.
//
// Each line is [from, to, colour], lanes counted from the left: "up" lines
// run from the row's top to the dot, "down" lines from the dot's height to
// the row's bottom. A lane keeps its colour for as long as it is open.
export function graphLayout(commits) {
  const lanes = [], colors = [];
  let next = 0, width = 1;
  const open = () => {
    let i = lanes.indexOf(null);
    if (i < 0) i = lanes.length;
    colors[i] = next++ % LANE_COLORS;
    return i;
  };
  const rows = commits.map((c) => {
    const up = [], down = [];
    let col = lanes.indexOf(c.hash);
    if (col < 0) col = open(); // a branch tip: nothing comes from above
    lanes.forEach((h, k) => {
      if (h == null) return;
      if (h !== c.hash) up.push([k, k, colors[k]]);
      else {
        up.push([k, col, colors[k]]);
        if (k !== col) lanes[k] = null;
      }
    });
    const color = colors[col];
    const [first, ...rest] = c.parents || [];
    lanes[col] = first ?? null;
    const forks = new Set();
    const joins = rest.map((p) => {
      let j = lanes.indexOf(p);
      if (j < 0) { j = open(); lanes[j] = p; forks.add(j); }
      return j;
    });
    lanes.forEach((h, k) => {
      if (h != null && !forks.has(k)) down.push([k, k, k === col ? color : colors[k]]);
    });
    for (const j of joins) down.push([col, j, colors[j]]);
    while (lanes.length && lanes.at(-1) == null) { lanes.pop(); colors.pop(); }
    for (const [a, b] of [...up, ...down]) width = Math.max(width, a + 1, b + 1);
    width = Math.max(width, col + 1);
    return { col, color, up, down };
  });
  return { rows, width };
}
