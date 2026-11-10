// The History view's commit pane, the parts free of the DOM: moving the
// selection through the commits, and loading a commit's detail so that a
// slow answer for a commit no longer selected never replaces the current one.

// stepCommit is the hash n commits after current in hashes (before it for
// a negative n), kept within the list; Infinity is the last commit. With
// nothing selected it starts from the first.
export function stepCommit(hashes, current, n) {
  if (!hashes.length) return null;
  const i = hashes.indexOf(current);
  if (i < 0) return n === Infinity ? hashes.at(-1) : hashes[0];
  const j = Math.max(0, Math.min(hashes.length - 1, i + n));
  return hashes[j];
}

// latestOnly runs one load at a time: starting another aborts the one
// before (through the AbortSignal fn gets) and makes its answer stale.
// run resolves to { value } for the latest run, or { stale: true } when a
// later run (or cancel) came first; only the latest run's errors reject.
// wait > 0 debounces: the load starts after wait ms unless superseded.
export function latestOnly({ setTimer = setTimeout, clearTimer = clearTimeout } = {}) {
  let seq = 0, ctrl = null, timer = null, pending = null;
  const supersede = () => {
    seq++;
    ctrl?.abort();
    ctrl = null;
    if (timer != null) clearTimer(timer);
    timer = null;
    pending?.();
    pending = null;
  };
  return {
    run(fn, { wait = 0 } = {}) {
      supersede();
      const my = seq;
      return new Promise((resolve, reject) => {
        const go = () => {
          timer = null;
          pending = null;
          const c = new AbortController();
          ctrl = c;
          Promise.resolve().then(() => fn(c.signal)).then(
            (value) => resolve(my === seq ? { value } : { stale: true }),
            (err) => (my === seq && !c.signal.aborted ? reject(err) : resolve({ stale: true })));
        };
        if (wait > 0) {
          pending = () => resolve({ stale: true });
          timer = setTimer(go, wait);
        } else go();
      });
    },
    cancel: supersede,
  };
}
