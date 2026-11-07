---
name: dependencies
title: Dependency audit
kind: dependencies
scope: repo
# The audit tools need a shell (and the network); the checkout is a throwaway copy.
tools: Read, Grep, Glob, Bash
# Suggested: Mondays at 06:00. Built-in schedules are off until turned on.
schedule: 0 6 * * 1
---
Audit the third-party dependencies of the project in {{path}}.

Find the dependency manifests and lock files (go.mod, package.json and its
lock file, requirements*.txt, pyproject.toml, Cargo.toml, Gemfile, …). Where
the ecosystem's audit tool is installed, run it (for example `govulncheck
./...`, `npm audit --json`, `pip-audit`, `cargo audit`); do not install
anything. Otherwise judge the pinned versions from what you know.

Report each vulnerable dependency once: the package and version in the
title, the advisory ids and whether the vulnerable code is reachable in the
details, the version that fixes it in the suggested fix, and the manifest as
the file. Use the advisory id or "<package>@<version>" as the fingerprint.
Also report dependencies that are abandoned or far behind a major version
as type "issue" with severity "low".

Do not change any files: no upgrades, no lock file updates.
