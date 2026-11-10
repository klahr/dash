---
name: flaky-tests
title: Flaky tests
kind: flaky_tests
scope: repo
# Running the tests needs a shell; the checkout is a throwaway copy.
tools: Read, Grep, Glob, Bash
# Suggested: Sundays at 03:00. Built-in schedules are off until turned on.
schedule: 0 3 * * 0
---
Find the flaky tests of the project in {{path}}: tests that sometimes pass
and sometimes fail on the same code.

1. Find how the project runs its tests. Look in CLAUDE.md, CONTRIBUTING.md
   and the README, the Makefile or justfile, package.json's scripts, the CI
   configuration (.github/workflows, .gitlab-ci.yml, …), and the toolchain
   files (go.mod, pyproject.toml / pytest.ini / tox.ini, Cargo.toml,
   Gemfile, pom.xml / build.gradle, …). Prefer the command the project
   documents. Use only test tools and dependencies already installed: do not
   install anything, and skip suites that need services that aren't running
   (a database, a browser, Docker) or credentials.
2. Run the suite several times, 3 to 5 runs, in an order that changes where
   the toolchain supports it, and keep each run's output (write it to a file
   under the system's temp directory and grep it, rather than reading it
   all). For example:
   - Go: `go test -count=1 -shuffle=on ./...` per run (or once with
     `-count=3`); add `-race` when the suite is quick, since races cause
     many flakes.
   - JavaScript: the project's test script; Jest `--randomize`, Vitest
     `--sequence.shuffle`, Mocha `--parallel` where available.
   - Python: `pytest -p no:cacheprovider`, with `-p random_order` or
     `-p randomly` when pytest-randomly or pytest-random-order is installed.
   - Rust: `cargo test` (add `-- -Z unstable-options --shuffle` only on a
     nightly toolchain).
   - Others: the documented command, repeated.
3. Cap the total time. Time the first run. Stop after 5 runs, or earlier when
   further runs would take the total past about 25 minutes; if one run alone
   takes more than 10 minutes, do just that run plus reruns of the packages
   or files that failed. Wrap every command in `timeout` (for example
   `timeout 600 go test …`) so a hanging test can't stall the scan, and treat
   a test that hangs in one run but passes in another as flaky.
4. Rerun what failed. For each test that failed in some runs and passed in
   others, run it alone a few more times (`go test -run '^TestName$'
   -count=10`, `pytest path::test_name`, `npx jest -t "name"`, …) to confirm
   it is flaky and to see whether it fails alone or only next to others.

Report each flaky test once, as type "bug": the test's name and file in the
title, the line where it is declared (or where it fails), and in the details
how often it failed (for example "failed 2 of 5 runs, 1 of 10 alone"), the
command that reproduces it and the relevant failure output (the assertion,
error or panic, trimmed to the lines that matter). Name the likely cause and
the evidence for it from reading the test and the code it exercises:

- timing: sleeps, fixed timeouts, deadlines or polling that assume a fast
  machine, wall-clock or time-zone dependence;
- ordering: passes alone but fails after another test, or depends on the
  order of a map, set, goroutines or parallel tests;
- shared state: globals, singletons, environment variables, the working
  directory, files, ports or a database touched by several tests without
  cleanup, data races;
- network: real HTTP or DNS calls, fixed ports already in use;
- randomness: unseeded random input.

The suggested fix is concrete: what to wait on instead of sleeping, which
state to reset or isolate, what to fake or seed. Use "flaky:<file>:<test
name>" as the fingerprint. Use severity "high" for a test that fails in half
or more of the runs or flakes in a way that hides a real bug in the code
(a data race, a missing lock, an order the code itself depends on),
"medium" for one that fails now and then, and "low" for one that only
fails under the shuffled order or -race and never in the project's own
command.

Tests that fail in every run are not flaky: do not report them one by one.
If the suite does not run at all (it doesn't build, or needs something
missing), report that once as type "issue", severity "low", fingerprint
"flaky-tests:suite", with the command and the error, and stop. If no test
flaked, report nothing.

Do not change any files in the repository: don't fix or skip tests, and
don't commit anything; run the tests and read the code only.
