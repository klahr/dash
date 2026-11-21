// How long a running deploy has left, from how long the same environment's
// earlier successful deploys took (the deploy pane's Log heading). It reads
// only the deploy records, so it has no imports and Node tests can load it.

// The deploys an estimate is made from: at most this many, newest first.
export const ETA_SAMPLES = 10;

const ACTIVE = ["building", "deploying", "verifying"];

// The kind of run a deploy is: a promote and a rollback skip the build, so
// they are timed apart from the deploys that build.
const kind = (d) => (d.promote ? "promote" : d.rollback ? "rollback" : "build");

// tookToLive is how long successful deploy d took, from its start until it
// went live (or finished, deployed and passing), in ms; 0 when it didn't succeed.
export function tookToLive(d) {
  if (!d.started_at || d.status === "failed" || d.cancelled) return 0;
  const live = (d.phases || []).find((p) => p.status === "live")?.at;
  if (!live) return 0;
  const ms = new Date(live) - new Date(d.started_at);
  return ms > 0 ? ms : 0;
}

// typical is the median time to live of up to ETA_SAMPLES of the newest
// successful deploys in list like d (same environment, same kind), with how
// many it is from: { ms, n }; ms is 0 when there are none.
export function typical(d, list) {
  const times = [];
  for (const x of list) {
    if (times.length >= ETA_SAMPLES) break;
    if (x.id === d.id || x.environment !== d.environment || kind(x) !== kind(d)) continue;
    const ms = tookToLive(x);
    if (ms) times.push(ms);
  }
  if (!times.length) return { ms: 0, n: 0 };
  times.sort((a, b) => a - b);
  const mid = times.length >> 1;
  const ms = times.length % 2 ? times[mid] : (times[mid - 1] + times[mid]) / 2;
  return { ms, n: times.length };
}

// estimate says how long running deploy d has left at now (ms since the
// epoch), from list (deploys, newest first): { left, over, typical, n }:
// left is ms to go, over is true once it has taken longer than usual. Null
// when d isn't running or no earlier deploy succeeded to go by.
export function estimate(d, list, now) {
  if (!d || !ACTIVE.includes(d.status) || !d.started_at) return null;
  const t = typical(d, list);
  if (!t.ms) return null;
  const left = t.ms - (now - new Date(d.started_at));
  return { left: Math.max(0, left), over: left <= 0, typical: t.ms, n: t.n };
}

// etaText words an estimate: "about 3m left", "under a minute left",
// "taking longer than usual".
export function etaText(e) {
  if (!e) return "";
  if (e.over) return "taking longer than usual";
  const min = Math.round(e.left / 60000);
  if (min < 1) return "under a minute left";
  if (min < 60) return `about ${min}m left`;
  const h = Math.floor(min / 60), m = min % 60;
  return `about ${h}h${m ? ` ${m}m` : ""} left`;
}
