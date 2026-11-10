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
   node_modules/*/package.json; the LICENSE files of Go modules in the module
   cache (`go env GOMODCACHE`) or vendor/; `pip show <package>` or the
   dist-info METADATA in an existing virtualenv; `cargo metadata
   --format-version 1` (its `license` fields) or the files in
   ~/.cargo/registry; the gemspecs of installed gems. Where the metadata is
   not on disk, use what you know of the package and say so in the details.
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
     lock files' recent history (`git log -p --since="3 months ago" --
     <lock file>`) for upgraded packages and compare the licence of the old
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
