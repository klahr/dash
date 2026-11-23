// What the build and deploy estimates share (buildeta.js, deployeta.js): the
// typical time of the newest earlier runs and how the time left is worded.
// It has no imports, so Node tests can load it.

// The runs an estimate is made from by default: at most this many, newest first.
export const SAMPLES = 10;

// typicalOf is the median of pick(x) over list (newest first), taken from
// the first max entries that pick gives a time for, with how many it is
// from: { ms, n }. pick returns ms, or 0 to skip x; ms is 0 when none count.
export function typicalOf(list, pick, max = SAMPLES) {
  const times = [];
  for (const x of list) {
    if (times.length >= max) break;
    const ms = pick(x);
    if (ms) times.push(ms);
  }
  if (!times.length) return { ms: 0, n: 0 };
  times.sort((a, b) => a - b);
  const mid = times.length >> 1;
  const ms = times.length % 2 ? times[mid] : (times[mid - 1] + times[mid]) / 2;
  return { ms, n: times.length };
}

// etaText words an estimate { left, over }: "about 3m left", "under a
// minute left", "taking longer than usual".
export function etaText(e) {
  if (!e) return "";
  if (e.over) return "taking longer than usual";
  const min = Math.round(e.left / 60000);
  if (min < 1) return "under a minute left";
  if (min < 60) return `about ${min}m left`;
  const h = Math.floor(min / 60), m = min % 60;
  return `about ${h}h${m ? ` ${m}m` : ""} left`;
}
