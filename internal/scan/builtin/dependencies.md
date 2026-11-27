---
name: dependencies
title: Dependency audit
kind: dependencies
scope: repo
# Only the named audit and metadata commands, in Claude Code's sandbox (see
# docs/scans.md): no general shell. npm and cargo audit only as written, so
# no fix mode (which installs packages and runs their scripts) slips in.
# No `cargo metadata`: it runs rustc, or a wrapper the repository's
# .cargo/config.toml names, so it would run the repository's code.
# git history through the git tools.
tools: Read, Grep, Glob, mcp__dash_git__git_log, mcp__dash_git__git_show, mcp__dash_git__git_diff, Bash(govulncheck:*), Bash(npm audit), Bash(npm audit --json), Bash(pip-audit --disable-pip:*), Bash(cargo audit), Bash(cargo audit --json), Bash(pip show:*)
# Suggested: Mondays at 06:00. Built-in schedules are off until turned on.
schedule: 0 6 * * 1
---
Audit the third-party dependencies of the project in {{path}}.

Find the dependency manifests and lock files (go.mod, package.json and its
lock file, requirements*.txt, pyproject.toml, Cargo.toml, Gemfile, …). Where
the ecosystem's audit tool is installed, run it: `govulncheck ./...`, `npm
audit --json`, `pip-audit --disable-pip --no-deps -r <requirements file>`,
`cargo audit --json` (npm audit and cargo audit exactly as written). These
and `pip show` are the only commands you can run: no other
shell commands, no fix modes, and do not install anything. Read files with
the Read, Grep and Glob tools. The tools' output is the evidence for a
vulnerability.

Report each vulnerable dependency once: the package and version in the
title, the advisory ids and whether the vulnerable code is reachable in the
details, the version that fixes it in the suggested fix, and the manifest as
the file. Use the advisory id or "<package>@<version>" as the fingerprint.

- Every vulnerability finding names at least one advisory id: a CVE
  (CVE-2024-12345), GHSA (GHSA-xxxx-xxxx-xxxx) or OSV id (GO-2024-1234,
  PYSEC-…, RUSTSEC-…). Without an id, don't report it.
- A finding is verified when an audit tool you ran reported it for the
  version in the lock file: say which tool in the details. Rate it with the
  shared severity scale, from the advisory's severity and whether the
  vulnerable code is reachable (govulncheck says so; otherwise check
  whether the project calls the affected package or function).
- A finding no tool confirmed (no tool for that ecosystem is installed, or
  it couldn't run) starts its title with "Unverified:", says in the details
  why it couldn't be checked and how to check it (the command to run), and
  has severity "medium" at most.
- If an ecosystem's audit tool is missing or fails, say so once, as type
  "issue", severity "low", with the command to install or run it.
  Fingerprint "audit-tool:<ecosystem>" (for example "audit-tool:npm").

Also report dependencies that are abandoned or far behind a major version
as type "issue" with severity "low".

## Licences

Then check the dependencies' licences against the project's own.

1. The project's licence: the LICENSE / LICENCE / COPYING file at the root
   of the repository (or of {{path}}), else the `license` field of its own
   manifest (package.json, Cargo.toml, pyproject.toml, *.gemspec, …). With
   neither, treat the project as proprietary (all rights reserved) and say
   so in the details of each licence finding.
2. Each dependency's licence, from metadata the package managers already
   have, without installing anything or adding tools: the `license` field in
   package-lock.json, yarn.lock or pnpm-lock.yaml entries and in
   node_modules/*/package.json; the LICENSE files of Go modules in vendor/;
   `pip show <package>` or the dist-info METADATA in a virtualenv in the
   repository; the `license` field of the Cargo.toml of crates vendored in
   the repository (vendor/); vendored gems' gemspecs. Cargo.lock names the
   Rust crates and versions but not their licences. Where the metadata is not in the repository or
   those commands' output, use what you know of the package and say so in
   the details.
   Concentrate on direct dependencies, but report a transitive one whose
   licence clashes.
3. Report, as type "issue" with the manifest as the file:
   - a dependency whose licence clashes with the project's: for example
     GPL or AGPL (any version) in an MIT, Apache-2.0, BSD or proprietary
     project, LGPL statically linked into a proprietary binary (Go, Rust),
     SSPL or a "non-commercial" / source-available licence in a commercial
     product. Severity "high" for a strong copyleft (GPL, AGPL, SSPL) that
     ships with the product, "medium" for a weak copyleft or a clash only in
     development or test dependencies. Name both licences and say how the
     dependency is used (shipped, linked, dev-only, a separate tool) in the
     details, and suggest a replacement with a compatible licence or what
     would make it acceptable. Fingerprint "licence:<package>".
   - a dependency with no licence, or a licence you cannot identify (a
     custom text, "SEE LICENSE IN …", "UNLICENSED" from a third party), as
     severity "medium". Fingerprint "licence-unknown:<package>".
   - a dependency whose licence changed when it was upgraded. Look at the
     lock files' recent history (the git_log tool on the lock file, with
     patch, for the last few months' commits) for upgraded packages and compare the licence of the old
     version with the new one. Report a change to a licence that clashes, or
     to a more restrictive one, as above; a change between permissive
     licences (MIT to Apache-2.0, say) as severity "low". Give both versions
     and licences in the details. Fingerprint
     "licence-change:<package>@<new version>".
   Compatible combinations (MIT, BSD, ISC, Apache-2.0, MPL-2.0 used
   unmodified, … in a permissive or proprietary project; anything
   GPL-compatible in a GPL project) are not findings. Treat dual-licensed
   packages ("MIT OR GPL-3.0") as their most permissive option.

Do not change any files: no upgrades, no lock file updates.
