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

Only report a problem with a plausible real cost: say how big the input or
data can get, how often the code runs, and what it costs (latency, memory,
database load, a timeout or out-of-memory). Leave out micro-optimisations,
code that runs once at startup or only in tests and scripts, and work on
data that is small and bounded. Each finding explains the cost in its
details and the fix in its suggested fix.

Do not change any files.
