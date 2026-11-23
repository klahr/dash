// How far a running GitHub Actions build has got, from how long the same
// workflow's earlier successful runs took (the Builds sheet's progress bars).
// It reads only the run records, and imports only eta.js, so Node tests can load it.
import { SAMPLES, etaText, typicalOf } from "./eta.js";

// The runs an estimate is made from: at most this many, newest first.
export const BUILD_SAMPLES = SAMPLES;

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
  const seen = new Set();
  return typicalOf(list, (x) => {
    if (x.id === r.id || x.workflow !== r.workflow || seen.has(x.id)) return 0;
    seen.add(x.id);
    return tookMs(x);
  }, BUILD_SAMPLES);
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
export const buildEtaText = etaText;
