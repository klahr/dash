---
name: test-gaps
title: Test gaps
kind: tests
scope: since_last_scan
# Suggested: weekdays at 07:30. Built-in schedules are off until turned on.
schedule: 30 7 * * 1-5
---
Look for gaps in the tests of the code in {{path}} and report them.

Report two things:

1. New or changed code with no test covering it: a function, method, branch
   or error path that changed and that no test exercises. Check the test
   files next to it and search the rest of the project (table-driven tests,
   integration and end-to-end tests, tests of the callers) before deciding
   it is uncovered.
2. Tests that can't fail: tests with no assertions or checks, tests whose
   mocks or stubs return exactly what is asserted so the code under test is
   never really run, assertions that compare a value with itself or ignore
   the error they should check, and skipped or disabled tests left behind
   (t.Skip, it.skip, xit, @pytest.mark.skip, @Ignore, commented-out tests)
   without a reason or a linked issue.

Each finding names the file and the function or test in the title, uses the
line of the untested code or the broken test, and says in the details what
input or behaviour goes untested. The suggested fix describes the missing
test concretely: what to call, with which input, and what to assert (for
example "call parseRange with an empty string and check it returns
ErrEmpty"). Use "<file>:<function>" as the fingerprint. Use severity "high"
for untested code that handles security, money, data loss or concurrency,
"medium" for other behaviour with branches or error handling, and "low"
otherwise.

Keep it low-noise. Do not report trivial getters and setters, plain data
types, constants, generated code (files marked "Code generated", vendored or
build output), main functions that only wire things together, code that is
obviously covered by an existing test elsewhere, or a general wish for more
coverage. Report only what a reviewer would agree needs a test.

Do not change any files: describe the tests, don't write them.
