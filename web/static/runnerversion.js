// Whether a runner runs the hub's Dash version (see app/runners.js).
// Pure, so web/runnerversion_test.mjs can run it under Node.
//
// Release tags have no "v" (0.7.0), but either side may carry one, as
// internal/release.ParseVersion allows; dev builds are never releases.

// isRelease says whether v is a release version like 0.7.0 or v0.7.0.
export const isRelease = (v) => /^v?\d+\.\d+\.\d+/.test(v || "");

// sameVersion compares two versions, ignoring a leading "v" on either.
const bare = (v) => String(v || "").trim().replace(/^v(?=\d)/, "");
export const sameVersion = (a, b) => bare(a) === bare(b);

// runnerOutdated says whether a runner reporting version runs another
// version than the hub's; only a release hub flags runners.
export const runnerOutdated = (hub, version) => isRelease(hub) && !!version && !sameVersion(version, hub);
