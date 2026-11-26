---
name: performance
title: Performance review
kind: performance
scope: since_last_scan
# Suggested: Mondays at 07:00. Built-in schedules are off until turned on.
schedule: 0 7 * * 1
---
Review the code in {{path}} for performance problems and report what you
find.

Look for:

- N+1 queries: a database query, HTTP call or other round trip made once per
  item in a loop where one batched query or request would do.
- Unbounded work: loops, reads and allocations whose size depends on input
  or data that has no limit, such as reading a whole request body, file or
  table into memory, a query without LIMIT or pagination, a list or map that
  grows for every request and is never trimmed, or retries without a cap.
- Missing database indexes: for queries added or changed, a filter, join or
  sort on columns with no index in the schema or migrations, on a table that
  can grow large.
- Slow or blocking work in request handlers: synchronous calls to external
  services, heavy computation, file or network I/O, sleeps or lock
  contention on the request path that could be cached, batched, moved to a
  background job or done with a timeout.

In the web front end, if the project has one:

- Bundle size: large dependencies imported whole for a small part (all of
  lodash or moment, an icon set), libraries loaded on every page that one
  page uses, and code that could be loaded on demand. Name the import and,
  where you can tell, its size.
- Needless re-renders and DOM work: React, Vue or Svelte components that
  re-render on every parent change because of new object, array or
  function props each render, context values rebuilt each render, effects
  with missing or always-changing dependencies, long lists rendered whole
  without virtualization, and plain JavaScript that rebuilds large parts of
  the DOM or forces layout in a loop.
- Images: large images served unresized or uncompressed (megabytes for a
  thumbnail), no modern format (WebP, AVIF) or `srcset` where it matters,
  and images below the fold without `loading="lazy"`.
- Blocking scripts and styles: scripts in the `<head>` without `defer` or
  `async`, large inline scripts, render-blocking third-party scripts and
  fonts, and work on the main thread at startup that could wait.

Only report a problem with a plausible real cost: say how big the input or
data can get, how often the code runs, and what it costs (latency, memory,
database load, a timeout or out-of-memory). Leave out micro-optimisations,
code that runs once at startup or only in tests and scripts, and work on
data that is small and bounded. Each finding explains the cost in its
details and the fix in its suggested fix.

Report each as type "issue". Use the shared severity scale: "high" only
when the cost already hurts or will with the data the project expects (a
request that times out, memory that grows until the process is killed, a
page that takes seconds to become usable), "medium" for a cost that grows
with use, "low" for a modest saving. Fingerprint "<kind>:<qualified
function, route or file>", with kind one of n+1, unbounded, index,
blocking, bundle, rerender, image or render-blocking (for example
"n+1:internal/store.ListTodos", "bundle:web/src/app.tsx:lodash").

Do not change any files.
