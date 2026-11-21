// How far a running GitHub Actions build has got, from how long the same
// workflow's earlier successful runs took (the Builds sheet's progress bars).
// It reads only the run records, so it has no imports and Node tests can load it.

// The runs an estimate is made from: at most this many, newest first.
export const BUILD_SAMPLES = 10;

// tookMs is how long finished, successful run r took, in ms: from when its
// latest attempt started until it finished. 0 when it didn't pass or the
// times are missing (runs listed by an older Dash).
export function tookMs(r) {
  if (r.status !== "completed" || r.conclusion !== "success" || !r.started_at || !r.updated_at) return 0;
  const ms = new Date(r.updated_at) - new Date(r.started_at);
  return ms > 0 ? ms : 0;
}

// typicalRun is the median time of up to BUILD_SAMPLES of the newest
// successful runs of r's workflow in list, with how many it is from:
// { ms, n }; ms is 0 when there are none.
export function typicalRun(r, list) {
  const times = [], seen = new Set();
  for (const x of list) {
    if (times.length >= BUILD_SAMPLES) break;
    if (x.id === r.id || x.workflow !== r.workflow || seen.has(x.id)) continue;
    seen.add(x.id);
    const ms = tookMs(x);
    if (ms) times.push(ms);
  }
  if (!times.length) return { ms: 0, n: 0 };
  times.sort((a, b) => a - b);
  const mid = times.length >> 1;
  const ms = times.length % 2 ? times[mid] : (times[mid - 1] + times[mid]) / 2;
  return { ms, n: times.length };
}

// buildProgress says how far running run r is at now (ms since the epoch),
// from list (runs, newest first): { frac, left, over, typical, n }: frac is
// 0..1 of the typical time, left is ms to go, over is true once it has taken
// longer than usual. Null when r isn't running, hasn't started (queued) or
// no earlier run of its workflow passed to go by.
export function buildProgress(r, list, now) {
  if (!r || r.status === "completed" || r.status === "queued" || r.status === "waiting" || r.status === "pending") return null;
  const start = r.started_at || r.created_at;
  if (!start) return null;
  const t = typicalRun(r, list);
  if (!t.ms) return null;
  const elapsed = Math.max(0, now - new Date(start));
  const left = t.ms - elapsed;
  return { frac: Math.min(1, elapsed / t.ms), left: Math.max(0, left), over: left <= 0, typical: t.ms, n: t.n };
}

// buildEtaText words a progress: "about 3m left", "under a minute left",
// "taking longer than usual".
export function buildEtaText(p) {
  if (!p) return "";
  if (p.over) return "taking longer than usual";
  const min = Math.round(p.left / 60000);
  if (min < 1) return "under a minute left";
  if (min < 60) return `about ${min}m left`;
  const h = Math.floor(min / 60), m = min % 60;
  return `about ${h}h${m ? ` ${m}m` : ""} left`;
}
